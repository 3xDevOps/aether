package sshd

import (
	"context"
	"encoding/json"
	"fmt"
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

	asks := newRun(e.member.ID, domain.LaunchTUI, domain.RunNeedsAttention, "needs input: which database?", domain.ActivityNeedsYou)
	interrupted := newRun(e.member.ID, domain.LaunchACP, domain.RunNeedsAttention, "turn interrupted", domain.ActivityNeedsYou)
	stranded := newRun(e.member.ID, domain.LaunchACP, domain.RunNeedsAttention, "wait broken: pr:o/r#12:checks resolved, but the agent was not woken", domain.ActivityNeedsYou)
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
		asks.ID:         {protocol.AttentionKindNeedsInput, "Agent asks: which database?"},
		interrupted.ID:  {protocol.AttentionKindNeedsInput, "Turn interrupted · reply to continue"},
		stranded.ID:     {protocol.AttentionKindNeedsInput, "Wait broken: pr:o/r#12:checks resolved, but the agent was not woken · reply to continue"},
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

func TestAttentionListCarriesTheLinkOfALinkRequest(t *testing.T) {
	e := newTestEnv(t, func(cfg *Config) { cfg.Control = control.New(control.Config{}) })
	run := &domain.Run{WorkspaceID: e.ws.ID, MemberID: e.member.ID, Task: "sign in", Harness: "codex", Mode: domain.LaunchACP, ACP: true, Status: domain.RunRunning}
	if err := e.store.CreateRun(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.UpdateRunStatus(t.Context(), run.ID, domain.RunNeedsAttention, "", domain.ActivityNeedsYou, nil, nil); err != nil {
		t.Fatal(err)
	}
	e.runs.mu.Lock()
	e.runs.acpPending = map[domain.RunID][]acphost.Request{run.ID: {{
		ID: "req-1", Kind: acphost.RequestLink, Title: "Sign in to GitHub", URL: "https://github.com/login/device",
		Options: []acphost.Option{{ID: "accept", Name: "Accept"}, {ID: "decline", Name: "Decline"}},
	}}}
	e.runs.mu.Unlock()

	item := attentionItems(t, e, e.member.ID, protocol.AttentionListParams{})[run.ID]
	if item.Kind != protocol.AttentionKindQuestion || item.Request == nil ||
		item.Request.Kind != "question" || item.Request.URL != "https://github.com/login/device" || len(item.Request.Options) != 2 {
		t.Errorf("link request item = %+v request %+v", item, item.Request)
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
		"no update for 30m":              "No update for 30m",
		"no update for 30m: CI on PR 12": "No update for 30m: CI on PR 12",
		"waiting for 2 workers":          "Waiting for 2 workers",
		"something new":                  "something new",
	} {
		if got := plainReason(reason); got != want {
			t.Errorf("plainReason(%q) = %q, want %q", reason, got, want)
		}
	}
}

func TestAttentionListFindsRoomRequestsBeyondTheNewestPage(t *testing.T) {
	e := newTestEnv(t, func(cfg *Config) { cfg.Control = control.New(control.Config{}) })
	ctx := t.Context()
	_, bob := addMember(t, e, "Bob", domain.RoleCollaborator, false)
	bobs := &domain.Run{WorkspaceID: e.ws.ID, MemberID: bob.ID, Task: "bob's task", Harness: "claude", Mode: domain.LaunchTUI, Status: domain.RunRunning}
	if err := e.store.CreateRun(ctx, bobs); err != nil {
		t.Fatal(err)
	}
	post := func(run domain.RunID, kind store.RoomMessageKind, state store.RoomMessageState, body, key string) *store.RoomMessage {
		t.Helper()
		m := &store.RoomMessage{WorkspaceID: e.ws.ID, RunID: run, ActorID: e.member.ID, Kind: kind, State: state, Body: body, IdempotencyKey: key}
		if err := e.store.CreateRoomMessage(ctx, m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	question := post(bobs.ID, store.RoomMessageQuestion, "", "Is the migration safe?", "q")
	steer := post(e.run.ID, store.RoomMessageSteerRequest, store.RoomMessageQueued, "stop and rebase", "s")
	for i := range store.MaxCollaborationPageSize + 20 {
		post(bobs.ID, store.RoomMessageComment, "", "noise", fmt.Sprintf("b%d", i))
		post(e.run.ID, store.RoomMessageComment, "", "noise", fmt.Sprintf("a%d", i))
	}
	if _, _, err := e.srv.cfg.Control.Acquire(string(e.run.ID), string(bob.ID), "bob-tab", false); err != nil {
		t.Fatal(err)
	}

	got := attentionItems(t, e, bob.ID, protocol.AttentionListParams{})
	if q := got[bobs.ID]; q.Kind != protocol.AttentionKindRoomQuestion || q.Message == nil || q.Message.ID != question.ID {
		t.Errorf("room question = %+v, want %s", q, question.ID)
	}
	if q := got[e.run.ID]; q.Kind != protocol.AttentionKindQueuedMessage || q.Reason != "Ada sent a message, approve to deliver" || q.Message == nil || q.Message.ID != steer.ID {
		t.Errorf("queued message = %+v, want %s", q, steer.ID)
	}

	e.srv.cfg.Store = noOpenRoomRequests{e.store}
	if q := attentionItems(t, e, bob.ID, protocol.AttentionListParams{})[bobs.ID]; q.Kind != protocol.AttentionKindRoomQuestion || q.Reason != "A teammate asked a question" || q.Message != nil {
		t.Errorf("room question without its text = %+v", q)
	}
}

type noOpenRoomRequests struct{ store.Store }

func (noOpenRoomRequests) ListOpenRoomRequests(context.Context, domain.RunID) ([]*store.RoomMessage, error) {
	return nil, nil
}

func TestCompareSinceOrdersByTimeUnknownLast(t *testing.T) {
	got := []string{"", "2026-10-22T10:00:00Z", "2026-10-22T10:00:00.5Z", "2026-10-22T09:00:00-02:00"}
	slices.SortStableFunc(got, compareSince)
	want := []string{"2026-10-22T10:00:00Z", "2026-10-22T10:00:00.5Z", "2026-10-22T09:00:00-02:00", ""}
	if !slices.Equal(got, want) {
		t.Errorf("sorted = %q, want %q", got, want)
	}
}
