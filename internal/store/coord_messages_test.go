package store

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

func TestRetireRunMessagesKeepsHistoryOutOfTheInbox(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	w := mustCreateWorkspace(t, db)
	m := mustCreateMember(t, db)
	from := mustCreateRun(t, db, w.ID, m.ID, domain.RunRunning)
	to := mustCreateRun(t, db, w.ID, m.ID, domain.RunRunning)

	for _, body := range []string{"acked", "unread"} {
		if err := db.AppendRunMessage(ctx, &RunMessage{WorkspaceID: w.ID, FromRun: from.ID, ToRun: to.ID, Body: body}, 2); err != nil {
			t.Fatalf("AppendRunMessage(%s): %v", body, err)
		}
	}
	outbound := &RunMessage{WorkspaceID: w.ID, FromRun: to.ID, ToRun: from.ID, Body: "still deliver this"}
	if err := db.AppendRunMessage(ctx, outbound, 100); err != nil {
		t.Fatalf("AppendRunMessage(outbound): %v", err)
	}
	_, token, _, deliverErr := db.DeliverRunMessages(ctx, to.ID, "", 1)
	if deliverErr != nil {
		t.Fatalf("DeliverRunMessages: %v", deliverErr)
	}
	if _, _, _, err := db.DeliverRunMessages(ctx, to.ID, token, 1); err != nil {
		t.Fatalf("DeliverRunMessages (ack): %v", err)
	}

	if err := db.RetireRunMessages(ctx, to.ID); err != nil {
		t.Fatalf("RetireRunMessages: %v", err)
	}
	if n, err := db.CountUnackedRunMessages(ctx, to.ID); err != nil || n != 0 {
		t.Fatalf("unacked after retirement = %d, %v; want 0", n, err)
	}
	if msgs, _, _, err := db.DeliverRunMessages(ctx, to.ID, "", 10); err != nil || len(msgs) != 0 {
		t.Fatalf("delivered after retirement = %d, %v; want none", len(msgs), err)
	}
	if err := db.AppendRunMessage(ctx, &RunMessage{WorkspaceID: w.ID, FromRun: from.ID, ToRun: to.ID, Body: "after"}, 2); err != nil {
		t.Fatalf("retired mail still counts toward the cap: %v", err)
	}
	if n, err := db.CountUnackedRunMessages(ctx, from.ID); err != nil || n != 1 {
		t.Fatalf("outbound unacked = %d, %v; want 1", n, err)
	}

	page, err := db.ListRunMessages(ctx, RunMessageFilter{WorkspaceID: w.ID, RunID: to.ID})
	if err != nil {
		t.Fatalf("ListRunMessages: %v", err)
	}
	byBody := map[string]*ListedRunMessage{}
	for _, item := range page.Items {
		byBody[item.Body] = item
	}
	if len(byBody) != 4 {
		t.Fatalf("history = %d rows, want 4 (both sides of the run)", len(byBody))
	}
	if acked := byBody["acked"]; acked.AckedAt == nil || acked.RetiredAt != nil {
		t.Fatalf("acked row = %+v, want acked and not retired", acked.RunMessage)
	}
	if unread := byBody["unread"]; unread.AckedAt != nil || unread.RetiredAt == nil {
		t.Fatalf("unread row = %+v, want retired and not acked", unread.RunMessage)
	}
	if run, err := db.GetRun(ctx, to.ID); err != nil || run.UnackedMessages != 1 {
		t.Fatalf("snapshot unacked = %v, %v; want 1", run, err)
	}
}

func TestCoordAuditOutboxSurvivesRunDeletion(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	w := mustCreateWorkspace(t, db)
	m := mustCreateMember(t, db)
	from := mustCreateRun(t, db, w.ID, m.ID, domain.RunRunning)
	to := mustCreateRun(t, db, w.ID, m.ID, domain.RunRunning)
	published := &RunMessage{WorkspaceID: w.ID, FromRun: from.ID, ToRun: to.ID, Body: "published"}
	pending := &RunMessage{WorkspaceID: w.ID, FromRun: from.ID, ToRun: to.ID, Body: "pending", Kind: RunMessageKindQuestion}
	for _, msg := range []*RunMessage{published, pending} {
		if err := db.AppendRunMessage(ctx, msg, 100); err != nil {
			t.Fatalf("AppendRunMessage: %v", err)
		}
	}
	pub, getErr := db.GetCoordAuditPublication(ctx, CoordAuditEventID(pending.ID))
	if getErr != nil {
		t.Fatalf("GetCoordAuditPublication: %v", getErr)
	}
	if pub.EventType != CoordAuditMessage || pub.MessageID != pending.ID || pub.FromRun != from.ID ||
		pub.ToRun != to.ID || pub.Kind != string(RunMessageKindQuestion) || pub.CorrelationID != pending.ID {
		t.Fatalf("audit snapshot = %+v, want the question's projection", pub)
	}
	if err := db.MarkCoordAuditPublished(ctx, CoordAuditEventID(published.ID)); err != nil {
		t.Fatalf("MarkCoordAuditPublished: %v", err)
	}
	if _, err := db.GetCoordAuditPublication(ctx, CoordAuditEventID(published.ID)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("published audit = %v, want ErrNotFound", err)
	}
	if err := db.MarkCoordAuditPublished(ctx, CoordAuditEventID(published.ID)); err != nil {
		t.Fatalf("MarkCoordAuditPublished twice: %v", err)
	}
	if err := db.DeleteRun(ctx, to.ID); err != nil {
		t.Fatalf("DeleteRun: %v", err)
	}
	if pub, getErr = db.GetCoordAuditPublication(ctx, CoordAuditEventID(pending.ID)); getErr != nil {
		t.Fatalf("pending audit after run deletion: %v", getErr)
	}
	if err := db.MarkCoordAuditPublished(ctx, pub.EventID); err != nil {
		t.Fatalf("MarkCoordAuditPublished after deletion: %v", err)
	}
	if left, err := db.ListPendingCoordAuditPublications(ctx, 10); err != nil || len(left) != 0 {
		t.Fatalf("pending after publication = %d, %v; want none", len(left), err)
	}
}

func TestAckQueuesOneAuditPublicationPerMessage(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	w := mustCreateWorkspace(t, db)
	m := mustCreateMember(t, db)
	from := mustCreateRun(t, db, w.ID, m.ID, domain.RunRunning)
	to := mustCreateRun(t, db, w.ID, m.ID, domain.RunRunning)
	msg := &RunMessage{WorkspaceID: w.ID, FromRun: from.ID, ToRun: to.ID, Body: "hello"}
	if err := db.AppendRunMessage(ctx, msg, 100); err != nil {
		t.Fatalf("AppendRunMessage: %v", err)
	}
	_, token, _, deliverErr := db.DeliverRunMessages(ctx, to.ID, "", 10)
	if deliverErr != nil {
		t.Fatalf("DeliverRunMessages: %v", deliverErr)
	}
	for range 2 {
		if _, _, _, err := db.DeliverRunMessages(ctx, to.ID, token, 10); err != nil {
			t.Fatalf("DeliverRunMessages (ack): %v", err)
		}
	}
	acked, err := db.GetRunMessage(ctx, msg.ID)
	if err != nil {
		t.Fatalf("GetRunMessage: %v", err)
	}
	pub, err := db.GetCoordAuditPublication(ctx, CoordAuditAckedEventID(msg.ID))
	if err != nil {
		t.Fatalf("GetCoordAuditPublication(acked): %v", err)
	}
	if pub.EventType != CoordAuditAcked || pub.ToRun != to.ID || pub.AckedAt == nil || !pub.AckedAt.Equal(*acked.AckedAt) {
		t.Fatalf("ack audit = %+v, want the acknowledgement of %s at %v", pub, msg.ID, acked.AckedAt)
	}
	pending, err := db.ListPendingCoordAuditPublications(ctx, 10)
	if err != nil {
		t.Fatalf("ListPendingCoordAuditPublications: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("pending publications = %d, want the send and one ack", len(pending))
	}
}

func TestListRunMessagesFiltersAndJoinsReports(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	w := mustCreateWorkspace(t, db)
	member := mustCreateMember(t, db)
	mission := mustCreateMission(t, db, w.ID, member.ID)
	task := mustCreateMissionTask(t, db, mission.ID, "bounded worker")
	integrator := &domain.Run{
		ID: mission.CurrentIntegratorRunID, WorkspaceID: w.ID, MemberID: member.ID,
		Task: "integrate", Harness: "claude", Mode: domain.LaunchTUI, Status: domain.RunRunning,
	}
	if err := db.CreateRunWithID(ctx, integrator); err != nil {
		t.Fatalf("create integrator: %v", err)
	}
	attempt, _, reserveErr := reserveMissionAttempt(t, db, mission, task, "worker")
	if reserveErr != nil {
		t.Fatalf("reserve worker: %v", reserveErr)
	}
	worker := &domain.Run{
		ID: attempt.RunID, WorkspaceID: w.ID, MemberID: member.ID,
		Task: "work", Harness: "claude", Mode: domain.LaunchTUI, Status: domain.RunRunning,
	}
	if err := db.CreateRunWithID(ctx, worker); err != nil {
		t.Fatalf("create worker: %v", err)
	}
	ordinary := mustCreateRun(t, db, w.ID, member.ID, domain.RunRunning)
	other := mustCreateRun(t, db, w.ID, member.ID, domain.RunRunning)

	send := func(from, to domain.RunID, kind RunMessageKind, correlation, body string) *RunMessage {
		t.Helper()
		msg := &RunMessage{WorkspaceID: w.ID, FromRun: from, ToRun: to, Kind: kind, CorrelationID: correlation, Body: body}
		if err := db.AppendRunMessage(ctx, msg, 100); err != nil {
			t.Fatalf("AppendRunMessage(%s): %v", body, err)
		}
		return msg
	}
	question := send(integrator.ID, worker.ID, RunMessageKindQuestion, "", "which file?")
	reply := send(worker.ID, integrator.ID, RunMessageKindReply, question.ID, "main.go")
	report := &CoordReport{
		WorkspaceID: w.ID, RunID: worker.ID, Outcome: CoordOutcomeSuccess,
		Summary: "done", NextAction: "merge", IdempotencyKey: "report",
	}
	if err := db.AppendCoordReport(ctx, report); err != nil {
		t.Fatalf("AppendCoordReport: %v", err)
	}
	forwarded := send(worker.ID, integrator.ID, RunMessageKindMessage, report.ID, "done")
	overlap := send(ordinary.ID, other.ID, RunMessageKindMessage, "", "same file")

	if question.MissionID != mission.ID || reply.MissionID != mission.ID || overlap.MissionID != "" {
		t.Fatalf("mission stamps = %q, %q, %q; want %q, %q, none",
			question.MissionID, reply.MissionID, overlap.MissionID, mission.ID, mission.ID)
	}

	list := func(f RunMessageFilter) []*ListedRunMessage {
		t.Helper()
		f.WorkspaceID = w.ID
		page, err := db.ListRunMessages(ctx, f)
		if err != nil {
			t.Fatalf("ListRunMessages(%+v): %v", f, err)
		}
		return page.Items
	}
	ids := func(items []*ListedRunMessage) []string {
		out := make([]string, 0, len(items))
		for _, item := range items {
			out = append(out, item.ID)
		}
		return out
	}
	assertIDs := func(name string, got []*ListedRunMessage, want ...*RunMessage) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s = %v, want %d rows", name, ids(got), len(want))
		}
		for i := range want {
			if got[i].ID != want[i].ID {
				t.Fatalf("%s = %v, want newest first ending in %s", name, ids(got), want[len(want)-1].ID)
			}
		}
	}
	assertIDs("workspace", list(RunMessageFilter{}), overlap, forwarded, reply, question)
	assertIDs("mission", list(RunMessageFilter{MissionID: mission.ID}), forwarded, reply, question)
	assertIDs("recipient side", list(RunMessageFilter{RunID: other.ID}), overlap)
	assertIDs("sender side", list(RunMessageFilter{RunID: ordinary.ID}), overlap)
	assertIDs("thread", list(RunMessageFilter{CorrelationID: question.ID}), reply, question)

	items := list(RunMessageFilter{MissionID: mission.ID})
	if items[0].Report == nil || items[0].Report.Outcome != CoordOutcomeSuccess ||
		items[0].Report.Summary != "done" || items[0].Report.NextAction != "merge" {
		t.Fatalf("forwarded report join = %+v, want the worker's report", items[0].Report)
	}
	if items[1].Report != nil || items[2].Report != nil {
		t.Fatal("only the forwarded report joins a coord_reports row")
	}

	first, err := db.ListRunMessages(ctx, RunMessageFilter{WorkspaceID: w.ID, Limit: 3})
	if err != nil || first.NextBefore == "" {
		t.Fatalf("first page = %+v, %v; want a cursor", first, err)
	}
	second, err := db.ListRunMessages(ctx, RunMessageFilter{WorkspaceID: w.ID, Limit: 3, Before: first.NextBefore})
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	assertIDs("second page", second.Items, question)
	if second.NextBefore != "" {
		t.Fatalf("exhausted page cursor = %q, want empty", second.NextBefore)
	}
	if _, cursorErr := db.ListRunMessages(ctx, RunMessageFilter{WorkspaceID: w.ID, Before: "not-a-cursor"}); !errors.Is(cursorErr, ErrInvalidCursor) {
		t.Fatal("an invalid cursor must be refused")
	}

	runs, err := db.ListMissionRunIDs(ctx, mission.ID)
	if err != nil {
		t.Fatalf("ListMissionRunIDs: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("mission runs = %v, want the integrator and the worker", runs)
	}
}

func TestMessageHistoryMigrationKeepsMailAndTypesTheOutbox(t *testing.T) {
	const historyVersion = 50
	path := filepath.Join(t.TempDir(), "aether.db")
	raw, err := sql.Open("sqlite", "file:"+url.PathEscape(path)+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if _, execErr := raw.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`); execErr != nil {
		t.Fatalf("create schema_migrations: %v", execErr)
	}
	for v := 1; v < historyVersion; v++ {
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
			VALUES ('r1', 'w1', 'm1', 'a', 'claude', 'tui', 'running', 'b', 'w', 1),
			       ('r2', 'w1', 'm1', 'a', 'claude', 'tui', 'running', 'c', 'w', 1);
		INSERT INTO run_messages (id, workspace_id, from_run, to_run, body, created_at, kind, correlation_id)
			VALUES ('msg1', 'w1', 'r1', 'r2', 'which file?', 5, 'question', 'msg1');
		INSERT INTO coord_audit_publications (message_id, event_id, workspace_id, from_run, to_run, body, publication_state, created_at)
			VALUES ('msg1', 'coord-message:msg1', 'w1', 'r1', 'r2', 'which file?', 'pending', 5);
	`, testKey(t, "")); execErr != nil {
		t.Fatalf("seed rows: %v", execErr)
	}
	if closeErr := raw.Close(); closeErr != nil {
		t.Fatalf("close raw: %v", closeErr)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open (message history migration): %v", err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	msg, err := db.GetRunMessage(ctx, "msg1")
	if err != nil || msg.MissionID != "" || msg.RetiredAt != nil || msg.Body != "which file?" {
		t.Fatalf("migrated message = %+v, %v; want the unchanged question", msg, err)
	}
	pub, err := db.GetCoordAuditPublication(ctx, CoordAuditEventID("msg1"))
	if err != nil {
		t.Fatalf("migrated audit publication: %v", err)
	}
	if pub.EventType != CoordAuditMessage || pub.Kind != "question" || pub.CorrelationID != "msg1" {
		t.Fatalf("migrated audit publication = %+v, want a pending typed question", pub)
	}
}
