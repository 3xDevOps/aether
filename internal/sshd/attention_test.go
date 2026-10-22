package sshd

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

func attentionItems(t *testing.T, e *testEnv, member domain.MemberID, params protocol.AttentionListParams) map[domain.RunID]protocol.AttentionItem {
	t.Helper()
	raw, perr := callJSON(t, e, member, protocol.MethodAttentionList, params)
	if perr != nil {
		t.Fatalf("attention.list: %v", perr)
	}
	var result protocol.AttentionListResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	items := map[domain.RunID]protocol.AttentionItem{}
	for _, item := range result.Items {
		if _, dup := items[domain.RunID(item.RunID)]; dup {
			t.Fatalf("run %s listed twice: %+v", item.RunID, result.Items)
		}
		items[domain.RunID(item.RunID)] = item
	}
	return items
}

func TestAttentionListWordsTheNeedsYouRows(t *testing.T) {
	e := newTestEnv(t, func(cfg *Config) { cfg.Control = control.New(control.Config{}) })
	ctx := t.Context()
	_, bob := addMember(t, e, "Bob", domain.RoleCollaborator, false)
	newRun := func(owner domain.MemberID, mode domain.LaunchMode, status domain.RunStatus, reason string, activity domain.RunActivity) *domain.Run {
		t.Helper()
		run := &domain.Run{WorkspaceID: e.ws.ID, MemberID: owner, Task: "fix the build\nthen ship", Harness: "codex", Mode: mode, ACP: mode == domain.LaunchACP, Status: domain.RunRunning}
		if err := e.store.CreateRun(ctx, run); err != nil {
			t.Fatal(err)
		}
		if _, err := e.store.UpdateRunStatus(ctx, run.ID, status, reason, activity, nil, nil); err != nil {
			t.Fatal(err)
		}
		return run
	}

	permission := newRun(e.member.ID, domain.LaunchACP, domain.RunNeedsAttention, "", domain.ActivityNeedsYou)
	e.runs.mu.Lock()
	e.runs.acpPending = map[domain.RunID][]acphost.Request{permission.ID: {{
		ID: "req-1", Kind: acphost.RequestPermission, Title: "Run npm test",
		Options: []acphost.Option{{ID: "a1", Name: "Allow", Kind: "allow_once"}, {ID: "a2", Name: "Always allow", Kind: "allow_always"}},
	}}}
	e.runs.mu.Unlock()

	approved := newRun(e.member.ID, domain.LaunchTUI, domain.RunNeedsAttention, "", domain.ActivityNeedsYou)
	approval := &store.Approval{WorkspaceID: e.ws.ID, RunID: approved.ID, Action: "Bash", Detail: "rm -rf build"}
	if err := e.store.CreateApproval(ctx, approval); err != nil {
		t.Fatal(err)
	}
	asks := newRun(e.member.ID, domain.LaunchTUI, domain.RunNeedsAttention, "needs input: which database?", domain.ActivityNeedsYou)
	interrupted := newRun(e.member.ID, domain.LaunchACP, domain.RunNeedsAttention, "turn interrupted", domain.ActivityNeedsYou)
	disconnected := newRun(e.member.ID, domain.LaunchACP, domain.RunNeedsAttention, "enhanced session ended: eof", domain.ActivityDisconnected)
	blocked := newRun(e.member.ID, domain.LaunchTUI, domain.RunNeedsAttention, "blocked: no credentials", domain.ActivityNeedsYou)
	paused := newRun(e.member.ID, domain.LaunchTUI, domain.RunNeedsAttention, "needs input: still there?", domain.ActivityNeedsYou)
	e.runs.setPaused(paused.ID, true)
	working := newRun(e.member.ID, domain.LaunchTUI, domain.RunRunning, "", domain.ActivityWorking)
	failed := newRun(e.member.ID, domain.LaunchTUI, domain.RunRunning, "", domain.ActivityWorking)
	if _, err := e.store.FinishRunReported(ctx, failed.ID, domain.RunNeedsAttention, "agent reported failure", domain.ActivityFailed, nil, nil); err != nil {
		t.Fatal(err)
	}
	bobs := newRun(bob.ID, domain.LaunchTUI, domain.RunRunning, "", domain.ActivityWorking)
	question := &store.RoomMessage{WorkspaceID: e.ws.ID, RunID: bobs.ID, ActorID: e.member.ID, ActorDisplayName: "Ada", Kind: store.RoomMessageQuestion, Body: "Is the migration safe?", IdempotencyKey: "q1"}
	if err := e.store.CreateRoomMessage(ctx, question); err != nil {
		t.Fatal(err)
	}

	got := attentionItems(t, e, e.member.ID, protocol.AttentionListParams{})
	want := map[domain.RunID][2]string{
		permission.ID:   {protocol.AttentionKindPermission, "Permission requested"},
		approved.ID:     {protocol.AttentionKindApproval, "Permission: Bash"},
		asks.ID:         {protocol.AttentionKindNeedsInput, "Agent asks: which database?"},
		interrupted.ID:  {protocol.AttentionKindNeedsInput, "Turn interrupted · reply to continue"},
		disconnected.ID: {protocol.AttentionKindDisconnected, "Agent disconnected: eof"},
		blocked.ID:      {protocol.AttentionKindBlocked, "Blocked: no credentials"},
		failed.ID:       {protocol.AttentionKindFailed, "Agent reported failure; review the result. Run remains open for follow-up."},
	}
	for id, w := range want {
		item, ok := got[id]
		if !ok || item.Kind != w[0] || item.Reason != w[1] {
			t.Errorf("run %s = %+v, want kind %q reason %q", id, item, w[0], w[1])
		}
	}
	for _, id := range []domain.RunID{paused.ID, working.ID, bobs.ID, e.run.ID} {
		if item, ok := got[id]; ok {
			t.Errorf("run %s listed for Ada: %+v", id, item)
		}
	}
	p := got[permission.ID]
	if p.Title != "fix the build" || p.Harness != "codex" || p.WorkspaceID != string(e.ws.ID) || p.Request == nil ||
		p.Request.ID != "req-1" || p.Request.Title != "Run npm test" ||
		!slices.Equal(p.Request.Options, []protocol.AttentionOption{{ID: "a1", Label: "Allow", Kind: "allow_once"}, {ID: "a2", Label: "Always allow", Kind: "allow_always"}}) {
		t.Errorf("permission item = %+v request %+v", p, p.Request)
	}
	if a := got[approved.ID].Approval; a == nil || a.ID != approval.ID || a.Detail != "rm -rf build" {
		t.Errorf("approval item = %+v", got[approved.ID])
	}

	forBob := attentionItems(t, e, bob.ID, protocol.AttentionListParams{WorkspaceID: string(e.ws.ID)})
	if len(forBob) != 1 {
		t.Fatalf("Bob's items = %+v, want only his room question", forBob)
	}
	q := forBob[bobs.ID]
	if q.Kind != protocol.AttentionKindRoomQuestion || q.Reason != "Ada asked you: Is the migration safe?" ||
		q.Message == nil || q.Message.ID != question.ID || q.Message.From != string(e.member.ID) {
		t.Errorf("room question = %+v message %+v", q, q.Message)
	}

	if _, perr := callJSON(t, e, e.member.ID, protocol.MethodAttentionList, protocol.AttentionListParams{WorkspaceID: "ws_missing"}); perr == nil || perr.Code != protocol.CodeNotFound {
		t.Errorf("unknown workspace = %v, want CodeNotFound", perr)
	}
}

func TestPlainReasonMatchesTheDashboard(t *testing.T) {
	for reason, want := range map[string]string{
		"killed; retained container":     "Stopped",
		"agent exited 2: boom":           "Agent exited with code 2: boom",
		"turn ended: max_tokens":         "Stopped: the reply reached its token limit",
		"turn ended: end_turn":           "The agent finished its turn",
		"turn ended: other":              "The agent's turn ended (other)",
		"working in background: 2 tasks": "Working in background: 2 tasks",
		"working in background":          "Working in background",
		"something new":                  "something new",
	} {
		if got := plainReason(reason); got != want {
			t.Errorf("plainReason(%q) = %q, want %q", reason, got, want)
		}
	}
}
