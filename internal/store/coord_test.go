package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"sync"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

// coordSchemaVersion is the migration slot the run_messages table
// occupies; the upgrade test builds the schema one version behind it.
const coordSchemaVersion = 8

// TestRunMailboxDeliveryTokens covers the whole at-least-once contract:
// a batch is delivered once under one token, redelivered under the same
// token until acknowledged, and only then makes way for the next batch.
func TestRunMailboxDeliveryTokens(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	w := mustCreateWorkspace(t, db)
	m := mustCreateMember(t, db)
	from := mustCreateRun(t, db, w.ID, m.ID, domain.RunRunning)
	to := mustCreateRun(t, db, w.ID, m.ID, domain.RunRunning)

	first := &RunMessage{WorkspaceID: w.ID, FromRun: from.ID, ToRun: to.ID, Body: "rewriting login()"}
	if err := db.AppendRunMessage(ctx, first, 100); err != nil {
		t.Fatalf("AppendRunMessage: %v", err)
	}
	if first.ID == "" || first.CreatedAt.IsZero() || first.DeliveryToken != "" {
		t.Fatalf("AppendRunMessage did not stamp an undelivered row: %+v", first)
	}
	if n, err := db.CountUnackedRunMessages(ctx, to.ID); err != nil || n != 1 {
		t.Fatalf("unacked = %d (err %v), want 1", n, err)
	}

	batch, token, err := db.DeliverRunMessages(ctx, to.ID, "", 100)
	if err != nil {
		t.Fatalf("DeliverRunMessages: %v", err)
	}
	if len(batch) != 1 || batch[0].ID != first.ID || token == "" || batch[0].DeliveredAt == nil {
		t.Fatalf("first read = %+v token %q, want the one message delivered", batch, token)
	}

	// A second message arrives before the first batch is acknowledged: the
	// token binds the exact batch it was issued for, so the retry returns
	// that batch alone.
	second := &RunMessage{WorkspaceID: w.ID, FromRun: from.ID, ToRun: to.ID, Body: "going ahead"}
	if aerr := db.AppendRunMessage(ctx, second, 100); aerr != nil {
		t.Fatalf("AppendRunMessage (second): %v", aerr)
	}
	retry, retryToken, err := db.DeliverRunMessages(ctx, to.ID, "", 100)
	if err != nil {
		t.Fatalf("DeliverRunMessages (retry): %v", err)
	}
	if len(retry) != 1 || retry[0].ID != first.ID || retryToken != token {
		t.Fatalf("retry = %+v token %q, want the same batch under token %q", retry, retryToken, token)
	}

	// An unknown token and another run's token acknowledge nothing.
	if again, _, berr := db.DeliverRunMessages(ctx, to.ID, "not-a-token", 100); berr != nil || len(again) != 1 {
		t.Fatalf("read with a bogus token = %+v (err %v), want the batch still outstanding", again, berr)
	}
	if _, _, ferr := db.DeliverRunMessages(ctx, from.ID, token, 100); ferr != nil {
		t.Fatalf("DeliverRunMessages (other run): %v", ferr)
	}
	if n, cerr := db.CountUnackedRunMessages(ctx, to.ID); cerr != nil || n != 2 {
		t.Fatalf("unacked after foreign ack = %d (err %v), want 2", n, cerr)
	}

	next, nextToken, err := db.DeliverRunMessages(ctx, to.ID, token, 100)
	if err != nil {
		t.Fatalf("DeliverRunMessages (ack): %v", err)
	}
	if len(next) != 1 || next[0].ID != second.ID || nextToken == "" || nextToken == token {
		t.Fatalf("after ack = %+v token %q, want the second message under a fresh token", next, nextToken)
	}
	// Acknowledging the same token twice is a no-op, not a second ack.
	if drained, _, err := db.DeliverRunMessages(ctx, to.ID, token, 100); err != nil || len(drained) != 1 || drained[0].ID != second.ID {
		t.Fatalf("replayed ack = %+v (err %v), want the outstanding batch untouched", drained, err)
	}
	if _, finalToken, err := db.DeliverRunMessages(ctx, to.ID, nextToken, 100); err != nil || finalToken != "" {
		t.Fatalf("drained inbox token = %q (err %v), want none", finalToken, err)
	}
	if n, err := db.CountUnackedRunMessages(ctx, to.ID); err != nil || n != 0 {
		t.Fatalf("unacked after draining = %d (err %v), want 0", n, err)
	}
}

// TestRunMailboxInboxCap proves the depth cap is enforced by the insert
// itself and names the condition, and that acknowledging frees room.
func TestRunMailboxInboxCap(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	w := mustCreateWorkspace(t, db)
	m := mustCreateMember(t, db)
	from := mustCreateRun(t, db, w.ID, m.ID, domain.RunRunning)
	to := mustCreateRun(t, db, w.ID, m.ID, domain.RunRunning)

	for i := range 3 {
		msg := &RunMessage{WorkspaceID: w.ID, FromRun: from.ID, ToRun: to.ID, Body: fmt.Sprintf("m%d", i)}
		if err := db.AppendRunMessage(ctx, msg, 3); err != nil {
			t.Fatalf("AppendRunMessage %d: %v", i, err)
		}
	}
	over := &RunMessage{WorkspaceID: w.ID, FromRun: from.ID, ToRun: to.ID, Body: "one too many"}
	if err := db.AppendRunMessage(ctx, over, 3); !errors.Is(err, ErrInboxFull) {
		t.Fatalf("AppendRunMessage past the cap = %v, want ErrInboxFull", err)
	}

	_, token, err := db.DeliverRunMessages(ctx, to.ID, "", 100)
	if err != nil {
		t.Fatalf("DeliverRunMessages: %v", err)
	}
	if _, _, err := db.DeliverRunMessages(ctx, to.ID, token, 100); err != nil {
		t.Fatalf("DeliverRunMessages (ack): %v", err)
	}
	if err := db.AppendRunMessage(ctx, over, 3); err != nil {
		t.Fatalf("AppendRunMessage after draining: %v", err)
	}
}

// TestRunMailboxRejectsUnknownRuns proves the foreign keys surface as the
// store's own sentinel rather than a driver error.
func TestRunMailboxRejectsUnknownRuns(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	w := mustCreateWorkspace(t, db)
	m := mustCreateMember(t, db)
	r := mustCreateRun(t, db, w.ID, m.ID, domain.RunRunning)

	err := db.AppendRunMessage(ctx, &RunMessage{WorkspaceID: w.ID, FromRun: r.ID, ToRun: "nope", Body: "hi"}, 100)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("AppendRunMessage to an unknown run = %v, want ErrNotFound", err)
	}
	if err := db.AppendRunMessage(ctx, &RunMessage{WorkspaceID: w.ID, ToRun: r.ID, Body: "hi"}, 100); err == nil {
		t.Fatal("AppendRunMessage without a sender = nil, want an error")
	}
}

// TestCoordMigrationUpgradesPreviousVersion builds a database one schema
// version behind the mailbox slot, seeds rows, then opens it: the upgrade
// must add run_messages without losing anything, and a delivery token
// written before the restart must still bind its batch after it.
func TestCoordMigrationUpgradesPreviousVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aether.db")
	raw, err := sql.Open("sqlite", "file:"+url.PathEscape(path)+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if _, execErr := raw.Exec(`CREATE TABLE schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at INTEGER NOT NULL
	)`); execErr != nil {
		t.Fatalf("create schema_migrations: %v", execErr)
	}
	for v := 1; v < coordSchemaVersion; v++ {
		if _, execErr := raw.Exec(migrations[v-1]); execErr != nil {
			t.Fatalf("apply v%d: %v", v, execErr)
		}
		if _, execErr := raw.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, 0)`, v); execErr != nil {
			t.Fatalf("record v%d: %v", v, execErr)
		}
	}
	if _, execErr := raw.Exec(`
		INSERT INTO members (id, display_name, public_key, color, role, created_at)
			VALUES ('m1', 'Ada', ?, '#e6194b', 'admin', 1);
		INSERT INTO workspaces (id, name, image, env, setup_script, created_at)
			VALUES ('w1', 'proj', 'img', '{}', '', 1);
		INSERT INTO sessions (id, workspace_id, name, base_branch, created_at)
			VALUES ('s1', 'w1', 'effort', 'main', 1);
		INSERT INTO runs (id, session_id, member_id, task, harness, mode, status, branch, worktree, created_at)
			VALUES ('r1', 's1', 'm1', 'a', 'claude', 'tui', 'running', 'b', 'w', 1);
		INSERT INTO runs (id, session_id, member_id, task, harness, mode, status, branch, worktree, created_at)
			VALUES ('r2', 's1', 'm1', 'b', 'claude', 'tui', 'running', 'b', 'w', 1);
	`, testKey(t, "")); execErr != nil {
		t.Fatalf("seed rows: %v", execErr)
	}
	if closeErr := raw.Close(); closeErr != nil {
		t.Fatalf("close raw: %v", closeErr)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open (coord migration): %v", err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()

	if _, gerr := db.GetWorkspace(ctx, "w1"); gerr != nil {
		t.Fatalf("GetWorkspace after migration: %v", gerr)
	}
	if aerr := db.AppendRunMessage(ctx, &RunMessage{WorkspaceID: "w1", FromRun: "r1", ToRun: "r2", Body: "hi"}, 100); aerr != nil {
		t.Fatalf("AppendRunMessage after migration: %v", aerr)
	}
	_, token, err := db.DeliverRunMessages(ctx, "r2", "", 100)
	if err != nil || token == "" {
		t.Fatalf("DeliverRunMessages after migration: token %q, err %v", token, err)
	}

	// Tokens live in the database, so they survive a restart: reopening the
	// same file and presenting the token still acknowledges its batch.
	if cerr := db.Close(); cerr != nil {
		t.Fatalf("close before reopen: %v", cerr)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	if _, _, err := reopened.DeliverRunMessages(ctx, "r2", token, 100); err != nil {
		t.Fatalf("DeliverRunMessages after reopen: %v", err)
	}
	if n, err := reopened.CountUnackedRunMessages(ctx, "r2"); err != nil || n != 0 {
		t.Fatalf("unacked after acking a pre-restart token = %d (err %v), want 0", n, err)
	}
}

// TestDeliverRunMessagesSurvivesConcurrentCommits pins the retry the
// delivery transaction needs. It reads before it writes, so it has to
// upgrade to a write lock partway through; another connection committing
// to the same file in between - which happens on every published event,
// because the event log shares aether.db - fails that upgrade with
// SQLITE_BUSY_SNAPSHOT, and the driver's busy handler does not retry it.
func TestDeliverRunMessagesSurvivesConcurrentCommits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aether.db")
	reader, err := Open(path)
	if err != nil {
		t.Fatalf("open reader: %v", err)
	}
	defer func() { _ = reader.Close() }()
	writer, err := Open(path)
	if err != nil {
		t.Fatalf("open writer: %v", err)
	}
	defer func() { _ = writer.Close() }()

	ctx := context.Background()
	w := mustCreateWorkspace(t, reader)
	m := mustCreateMember(t, reader)
	from := mustCreateRun(t, reader, w.ID, m.ID, domain.RunRunning)
	to := mustCreateRun(t, reader, w.ID, m.ID, domain.RunRunning)
	noise := mustCreateRun(t, reader, w.ID, m.ID, domain.RunRunning)

	const messages = 40
	for i := range messages {
		msg := &RunMessage{WorkspaceID: w.ID, FromRun: from.ID, ToRun: to.ID, Body: fmt.Sprintf("m%d", i)}
		if aerr := reader.AppendRunMessage(ctx, msg, messages); aerr != nil {
			t.Fatalf("seed %d: %v", i, aerr)
		}
	}

	// A second connection commits continuously, the way the event log does.
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			// Errors here are the writer losing a race, which is not what
			// this test is about.
			_ = writer.AppendRunMessage(ctx,
				&RunMessage{WorkspaceID: w.ID, FromRun: from.ID, ToRun: noise.ID, Body: "noise"}, 1<<20)
		}
	}()
	defer func() {
		close(stop)
		wg.Wait()
	}()

	token := ""
	for i := range messages {
		batch, next, derr := reader.DeliverRunMessages(ctx, to.ID, token, 1)
		if derr != nil {
			t.Fatalf("DeliverRunMessages under concurrent commits (call %d): %v", i, derr)
		}
		if len(batch) == 0 {
			t.Fatalf("inbox drained after %d of %d messages", i, messages)
		}
		token = next
	}
}

// TestDeleteRunMessagesRetiresTheWholeMailbox proves release-time cleanup
// removes every row addressed to the run - undelivered, delivered, and
// acknowledged alike - so the table does not grow for the life of the
// database.
func TestDeleteRunMessagesRetiresTheWholeMailbox(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	w := mustCreateWorkspace(t, db)
	m := mustCreateMember(t, db)
	from := mustCreateRun(t, db, w.ID, m.ID, domain.RunRunning)
	to := mustCreateRun(t, db, w.ID, m.ID, domain.RunRunning)

	for _, body := range []string{"first", "second"} {
		msg := &RunMessage{WorkspaceID: w.ID, FromRun: from.ID, ToRun: to.ID, Body: body}
		if err := db.AppendRunMessage(ctx, msg, 100); err != nil {
			t.Fatalf("AppendRunMessage(%s): %v", body, err)
		}
	}
	// Deliver and acknowledge the first batch so acked rows exist too.
	_, token, err := db.DeliverRunMessages(ctx, to.ID, "", 1)
	if err != nil {
		t.Fatalf("DeliverRunMessages: %v", err)
	}
	if _, _, err := db.DeliverRunMessages(ctx, to.ID, token, 1); err != nil {
		t.Fatalf("DeliverRunMessages (ack): %v", err)
	}

	if err := db.DeleteRunMessages(ctx, to.ID); err != nil {
		t.Fatalf("DeleteRunMessages: %v", err)
	}
	var n int
	if err := db.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM run_messages WHERE to_run = ?`, to.ID).Scan(&n); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if n != 0 {
		t.Fatalf("rows after delete = %d, want 0", n)
	}
}
