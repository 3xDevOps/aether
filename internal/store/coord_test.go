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
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// coordSchemaVersion is the migration slot that adds the v3 coordination
// metadata, durable peer accounting, outcome reservations, and fair outbox
// retry state.
const coordSchemaVersion = 29

// TestRunMailboxDeliveryTokens covers the whole at-least-once contract:
// a batch is delivered once under one token, redelivered under the same
// token until acknowledged, and only then makes way for the next batch.
func TestRunMailboxDeliveryTokens(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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

func TestAppendRunMessageWithPeerConvergesConcurrentSameKey(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	w := mustCreateWorkspace(t, db)
	m := mustCreateMember(t, db)
	from := mustCreateRun(t, db, w.ID, m.ID, domain.RunRunning)
	to := mustCreateRun(t, db, w.ID, m.ID, domain.RunRunning)

	const callers = 32
	start := make(chan struct{})
	errs := make(chan error, callers)
	ids := make(chan string, callers)
	var wg sync.WaitGroup
	wg.Add(callers)
	for range callers {
		go func() {
			defer wg.Done()
			<-start
			msg := &RunMessage{
				WorkspaceID: w.ID, FromRun: from.ID, ToRun: to.ID,
				Body: "same", Kind: RunMessageKindQuestion, IdempotencyKey: "same-key",
			}
			_, err := db.AppendRunMessageWithPeer(ctx, msg, callers, 8, true)
			if err != nil {
				errs <- err
				return
			}
			ids <- msg.ID
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	close(ids)
	for err := range errs {
		t.Fatalf("concurrent same-key append: %v", err)
	}
	var wantID string
	for id := range ids {
		if wantID == "" {
			wantID = id
		} else if id != wantID {
			t.Fatalf("same key converged to IDs %q and %q", wantID, id)
		}
	}
	var messages, peers int
	if err := db.db.QueryRowContext(ctx,
		`SELECT count(*) FROM run_messages WHERE from_run = ? AND idempotency_key = ?`,
		from.ID, "same-key").Scan(&messages); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if err := db.db.QueryRowContext(ctx,
		`SELECT count(*) FROM coord_peers WHERE from_run = ? AND to_run = ?`,
		from.ID, to.ID).Scan(&peers); err != nil {
		t.Fatalf("count peers: %v", err)
	}
	if messages != 1 || peers != 1 {
		t.Fatalf("durable counts = messages %d, peers %d; want one each", messages, peers)
	}
}

func TestAppendRunMessageWithPeerRejectsConcurrentCrossKindReuse(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	w := mustCreateWorkspace(t, db)
	m := mustCreateMember(t, db)
	from := mustCreateRun(t, db, w.ID, m.ID, domain.RunRunning)
	to := mustCreateRun(t, db, w.ID, m.ID, domain.RunRunning)

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	for _, kind := range []RunMessageKind{RunMessageKindMessage, RunMessageKindQuestion} {
		kind := kind
		go func() {
			defer wg.Done()
			<-start
			msg := &RunMessage{
				WorkspaceID: w.ID, FromRun: from.ID, ToRun: to.ID,
				Body: "cross-kind", Kind: kind, IdempotencyKey: "cross-kind-key",
			}
			_, err := db.AppendRunMessageWithPeer(ctx, msg, 10, 8, true)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	var successes, conflicts int
	for err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrIdempotencyConflict):
			conflicts++
		default:
			t.Fatalf("cross-kind concurrent append error = %v, want conflict", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("cross-kind results = successes %d, conflicts %d; want one each", successes, conflicts)
	}
}

// TestRunMailboxRejectsUnknownRuns proves the foreign keys surface as the
// store's own sentinel rather than a driver error.
func TestRunMailboxRejectsUnknownRuns(t *testing.T) {
	t.Parallel()
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
		INSERT INTO workspaces (id, name, created_at, environment, base_branch, steer_others, origin)
			VALUES ('w1', 'proj', 1, '{}', 'main', '', '');
		INSERT INTO runs (id, workspace_id, member_id, task, harness, mode, status, branch, worktree, created_at)
			VALUES ('r1', 'w1', 'm1', 'a', 'claude', 'tui', 'running', 'b', 'w', 1);
		INSERT INTO runs (id, workspace_id, member_id, task, harness, mode, status, branch, worktree, created_at)
			VALUES ('r2', 'w1', 'm1', 'b', 'claude', 'tui', 'running', 'b', 'w', 1);
		INSERT INTO run_messages (id, workspace_id, from_run, to_run, body, created_at)
			VALUES ('legacy-msg', 'w1', 'r1', 'r2', 'legacy', 1);
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
	var version int
	if versionErr := db.db.QueryRow(`SELECT max(version) FROM schema_migrations`).Scan(&version); versionErr != nil {
		t.Fatalf("read schema version: %v", versionErr)
	}
	if version != len(migrations) {
		t.Fatalf("schema version = %d, want %d (the latest)", version, len(migrations))
	}
	var kind, correlationID, idempotencyKey string
	if metadataErr := db.db.QueryRow(`SELECT kind, correlation_id, idempotency_key FROM run_messages WHERE id = 'legacy-msg'`).
		Scan(&kind, &correlationID, &idempotencyKey); metadataErr != nil {
		t.Fatalf("inspect migrated mailbox row: %v", metadataErr)
	}
	if kind != string(RunMessageKindMessage) || correlationID != "" || idempotencyKey != "" {
		t.Fatalf("migrated mailbox metadata = %q, %q, %q; want message and empty metadata",
			kind, correlationID, idempotencyKey)
	}
	var legacyAudits int
	if legacyErr := db.db.QueryRow(`SELECT count(*) FROM coord_audit_publications WHERE message_id = 'legacy-msg'`).Scan(&legacyAudits); legacyErr != nil {
		t.Fatalf("inspect legacy audit backfill: %v", legacyErr)
	}
	if legacyAudits != 0 {
		t.Fatalf("legacy audit rows = %d, want 0; v2 messages were already audited", legacyAudits)
	}
	var reports int
	if reportsErr := db.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'coord_reports'`).Scan(&reports); reportsErr != nil {
		t.Fatalf("inspect coord_reports table: %v", reportsErr)
	}
	if reports != 1 {
		t.Fatalf("coord_reports table count = %d, want 1", reports)
	}

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
	t.Parallel()
	path := templateDBPath(t)
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
			// Yield between commits: the regression under test is the
			// BUSY_SNAPSHOT lock upgrade, which any interleaved commit
			// triggers. An unthrottled loop instead starves the reader
			// past its busy timeout on slow CI runners.
			time.Sleep(time.Millisecond)
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

// TestDeleteRunMessagesRetiresOnlyTheRecipientInbox proves release-time
// cleanup removes every row addressed to the run - undelivered, delivered,
// and acknowledged alike - while preserving the run's outbound unread
// message for its peer.
func TestDeleteRunMessagesRetiresOnlyTheRecipientInbox(t *testing.T) {
	t.Parallel()
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
	outbound := &RunMessage{WorkspaceID: w.ID, FromRun: to.ID, ToRun: from.ID, Body: "still deliver this"}
	if err := db.AppendRunMessage(ctx, outbound, 100); err != nil {
		t.Fatalf("AppendRunMessage(outbound): %v", err)
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
		t.Fatalf("count recipient rows: %v", err)
	}
	if n != 0 {
		t.Fatalf("recipient rows after delete = %d, want 0", n)
	}
	if err := db.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM run_messages WHERE id = ?`, outbound.ID).Scan(&n); err != nil {
		t.Fatalf("count outbound row: %v", err)
	}
	if n != 1 {
		t.Fatalf("outbound row after recipient release = %d, want 1", n)
	}
}
func TestCoordAuditSnapshotSurvivesMailboxAndRunRetirement(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	w := mustCreateWorkspace(t, db)
	m := mustCreateMember(t, db)
	from := mustCreateRun(t, db, w.ID, m.ID, domain.RunRunning)
	to := mustCreateRun(t, db, w.ID, m.ID, domain.RunRunning)
	published := &RunMessage{
		WorkspaceID: w.ID, FromRun: from.ID, ToRun: to.ID,
		Body: "already published audit",
	}
	pending := &RunMessage{
		WorkspaceID: w.ID, FromRun: from.ID, ToRun: to.ID,
		Body: "immutable audit projection",
	}
	for _, msg := range []*RunMessage{published, pending} {
		if err := db.AppendRunMessage(ctx, msg, 100); err != nil {
			t.Fatalf("AppendRunMessage: %v", err)
		}
	}
	pub, err := db.GetCoordAuditPublication(ctx, pending.ID)
	if err != nil {
		t.Fatalf("GetCoordAuditPublication: %v", err)
	}
	if pub.WorkspaceID != pending.WorkspaceID || pub.FromRun != pending.FromRun ||
		pub.ToRun != pending.ToRun || pub.Body != pending.Body {
		t.Fatalf("audit snapshot = %+v, want immutable message projection", pub)
	}
	if markErr := db.MarkCoordAuditPublished(ctx, published.ID, CoordAuditEventID(published.ID)); markErr != nil {
		t.Fatalf("MarkCoordAuditPublished: %v", markErr)
	}
	if deleteErr := db.DeleteRunMessages(ctx, to.ID); deleteErr != nil {
		t.Fatalf("DeleteRunMessages: %v", deleteErr)
	}
	if _, getErr := db.GetCoordAuditPublication(ctx, published.ID); !errors.Is(getErr, ErrNotFound) {
		t.Fatalf("published audit after inbox retirement = %v, want ErrNotFound", getErr)
	}
	if deleteErr := db.DeleteRun(ctx, to.ID); deleteErr != nil {
		t.Fatalf("DeleteRun: %v", deleteErr)
	}
	pub, err = db.GetCoordAuditPublication(ctx, pending.ID)
	if err != nil {
		t.Fatalf("pending audit after mailbox/run retirement: %v", err)
	}
	if pub.Body != pending.Body {
		t.Fatalf("retained audit body = %q, want %q", pub.Body, pending.Body)
	}
	if err := db.MarkCoordAuditPublished(ctx, pending.ID, pub.EventID); err != nil {
		t.Fatalf("MarkCoordAuditPublished after retirement: %v", err)
	}
	if _, err := db.ListPendingCoordAuditPublications(ctx, 10); err != nil {
		t.Fatalf("ListPendingCoordAuditPublications cleanup: %v", err)
	}
	if _, err := db.GetCoordAuditPublication(ctx, pending.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("published orphan audit = %v, want ErrNotFound after cleanup", err)
	}
}

func TestRunMailboxV3MetadataAndIdempotency(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	w := mustCreateWorkspace(t, db)
	m := mustCreateMember(t, db)
	from := mustCreateRun(t, db, w.ID, m.ID, domain.RunRunning)
	to := mustCreateRun(t, db, w.ID, m.ID, domain.RunRunning)

	first := &RunMessage{
		WorkspaceID: w.ID, FromRun: from.ID, ToRun: to.ID, Body: "question",
		Kind: RunMessageKindQuestion, IdempotencyKey: "question-1",
	}
	if err := db.AppendRunMessage(ctx, first, 100); err != nil {
		t.Fatalf("AppendRunMessage: %v", err)
	}
	if first.ID == "" || first.CorrelationID != first.ID {
		t.Fatalf("question metadata = %+v, want self correlation", first)
	}
	retry := &RunMessage{
		WorkspaceID: w.ID, FromRun: from.ID, ToRun: to.ID, Body: "question",
		Kind: RunMessageKindQuestion, IdempotencyKey: "question-1",
	}
	if err := db.AppendRunMessage(ctx, retry, 100); err != nil {
		t.Fatalf("idempotent retry: %v", err)
	}
	if retry.ID != first.ID || retry.Body != first.Body || retry.CorrelationID != first.CorrelationID {
		t.Fatalf("retry = %+v, want original question", retry)
	}
	if err := db.AppendRunMessage(ctx, &RunMessage{
		WorkspaceID: w.ID, FromRun: from.ID, ToRun: to.ID, Body: "changed",
		Kind: RunMessageKindQuestion, IdempotencyKey: "question-1",
	}, 100); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed retry error = %v, want ErrIdempotencyConflict", err)
	}
	got, err := db.GetQuestion(ctx, first.ID)
	if err != nil || got.CorrelationID != first.ID {
		t.Fatalf("GetQuestion = %+v (err %v), want persisted correlation", got, err)
	}
}

func TestRunMailboxRejectsCrossKindIdempotency(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	w := mustCreateWorkspace(t, db)
	m := mustCreateMember(t, db)
	from := mustCreateRun(t, db, w.ID, m.ID, domain.RunRunning)
	to := mustCreateRun(t, db, w.ID, m.ID, domain.RunRunning)

	if err := db.AppendRunMessage(ctx, &RunMessage{
		WorkspaceID: w.ID, FromRun: from.ID, ToRun: to.ID, Body: "ask",
		Kind: RunMessageKindQuestion, IdempotencyKey: "shared-key",
	}, 100); err != nil {
		t.Fatalf("question: %v", err)
	}
	err := db.AppendRunMessage(ctx, &RunMessage{
		WorkspaceID: w.ID, FromRun: from.ID, ToRun: to.ID, Body: "send",
		Kind: RunMessageKindMessage, IdempotencyKey: "shared-key",
	}, 100)
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("cross-kind idempotency = %v, want ErrIdempotencyConflict", err)
	}
}

func TestCoordPeerAccountingIsAtomicAndSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "aether.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	w := mustCreateWorkspace(t, db)
	m := mustCreateMember(t, db)
	from := mustCreateRun(t, db, w.ID, m.ID, domain.RunRunning)
	blocker := mustCreateRun(t, db, w.ID, m.ID, domain.RunRunning)
	targets := make([]*domain.Run, 0, 9)
	for range 9 {
		targets = append(targets, mustCreateRun(t, db, w.ID, m.ID, domain.RunRunning))
	}
	// A failed enqueue must roll back its peer row.
	if appendErr := db.AppendRunMessage(ctx, &RunMessage{
		WorkspaceID: w.ID, FromRun: blocker.ID, ToRun: targets[0].ID, Body: "fills inbox",
	}, 1); appendErr != nil {
		t.Fatalf("fill target inbox: %v", appendErr)
	}
	created, err := db.AppendRunMessageWithPeer(ctx, &RunMessage{
		WorkspaceID: w.ID, FromRun: from.ID, ToRun: targets[0].ID, Body: "must retry",
	}, 1, 8, true)
	if created || !errors.Is(err, ErrInboxFull) {
		t.Fatalf("failed peer enqueue = created %v, err %v; want no peer and ErrInboxFull", created, err)
	}
	var peers int
	if peerCountErr := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM coord_peers WHERE from_run = ?`, from.ID).Scan(&peers); peerCountErr != nil {
		t.Fatalf("count rolled-back peers: %v", peerCountErr)
	}
	if peers != 0 {
		t.Fatalf("peer rows after failed enqueue = %d, want 0", peers)
	}
	for i, target := range targets[:8] {
		msg := &RunMessage{
			WorkspaceID: w.ID, FromRun: from.ID, ToRun: target.ID, Body: fmt.Sprintf("peer-%d", i),
			IdempotencyKey: fmt.Sprintf("peer-%d", i),
		}
		peerCreated, peerErr := db.AppendRunMessageWithPeer(ctx, msg, 100, 8, true)
		if !peerCreated || peerErr != nil {
			t.Fatalf("peer %d enqueue = created %v, err %v", i, peerCreated, peerErr)
		}
	}
	if closeErr := db.Close(); closeErr != nil {
		t.Fatalf("close: %v", closeErr)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = db.Close() }()
	created, err = db.AppendRunMessageWithPeer(ctx, &RunMessage{
		WorkspaceID: w.ID, FromRun: from.ID, ToRun: targets[8].ID, Body: "ninth",
		IdempotencyKey: "peer-8",
	}, 100, 8, true)
	if created || !errors.Is(err, ErrCoordPeerLimit) {
		t.Fatalf("ninth peer after restart = created %v, err %v; want durable cap", created, err)
	}
}

func TestCoordReportPersistenceAndIdempotency(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	w := mustCreateWorkspace(t, db)
	m := mustCreateMember(t, db)
	run := mustCreateRun(t, db, w.ID, m.ID, domain.RunRunning)

	report := &CoordReport{
		WorkspaceID: w.ID, RunID: run.ID, Outcome: "blocked",
		Summary: "waiting for review", EvidenceRefs: []string{"ev_1", "ev_2"},
		IdempotencyKey: "report-1",
	}
	if err := db.AppendCoordReport(ctx, report); err != nil {
		t.Fatalf("AppendCoordReport: %v", err)
	}
	retry := &CoordReport{
		WorkspaceID: w.ID, RunID: run.ID, Outcome: "blocked",
		Summary: "waiting for review", EvidenceRefs: []string{"ev_1", "ev_2"},
		IdempotencyKey: "report-1",
	}
	if err := db.AppendCoordReport(ctx, retry); err != nil {
		t.Fatalf("idempotent report retry: %v", err)
	}
	if retry.ID != report.ID || retry.Outcome != report.Outcome ||
		len(retry.EvidenceRefs) != len(report.EvidenceRefs) {
		t.Fatalf("retry = %+v, want original report", retry)
	}
	if err := db.AppendCoordReport(ctx, &CoordReport{
		WorkspaceID: w.ID, RunID: run.ID, Outcome: "success",
		Summary: "must not replace", IdempotencyKey: "report-1",
	}); !errors.Is(err, ErrCoordReportIdempotencyConflict) {
		t.Fatalf("changed report retry error = %v, want ErrCoordReportIdempotencyConflict", err)
	}
	got, err := db.GetCoordReport(ctx, report.ID)
	if err != nil || got.Outcome != "blocked" || len(got.EvidenceRefs) != 2 {
		t.Fatalf("GetCoordReport = %+v (err %v), want persisted outcome", got, err)
	}
	if err := db.AppendCoordReport(ctx, &CoordReport{
		WorkspaceID: w.ID, RunID: run.ID, Outcome: "unknown",
		Summary: "bad", IdempotencyKey: "bad",
	}); err == nil {
		t.Fatal("invalid outcome accepted")
	}
}
