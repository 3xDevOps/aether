package coord

import (
	"context"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

// TestMessagesStayInTheOwnersWorkspaces covers coordination under
// workspace-scoped access: a run whose owner cannot reach the target's
// workspace gets the same answer for that run as for one that never
// existed, on send and on ask; a grant opens the route.
func TestMessagesStayInTheOwnersWorkspaces(t *testing.T) {
	h := newHarness(t, 1, func(c *Config) { c.Store = c.Mail.(*store.DB) })
	ctx := context.Background()
	owner := h.runs[0].MemberID
	if err := h.db.GrantWorkspace(ctx, h.workspace, owner, ""); err != nil {
		t.Fatal(err)
	}
	foreign := &domain.Workspace{Name: "foreign", BaseBranch: domain.DefaultBaseBranch}
	if err := h.db.CreateWorkspace(ctx, foreign); err != nil {
		t.Fatal(err)
	}
	stranger := h.member(domain.RoleCollaborator)
	if err := h.db.GrantWorkspace(ctx, foreign.ID, stranger.ID, ""); err != nil {
		t.Fatal(err)
	}
	target := &domain.Run{WorkspaceID: foreign.ID, MemberID: stranger.ID, Task: "foreign", Harness: "claude", Mode: domain.LaunchTUI, Status: domain.RunRunning}
	if err := h.db.CreateRun(ctx, target); err != nil {
		t.Fatal(err)
	}
	mine := h.run(0)

	_, missing := h.svc.Send(ctx, mine, sendParams("run-doesnotexist", "hello"))
	if missing == nil || missing.Code != protocol.CodeNotFound {
		t.Fatalf("send to a missing session = %v, want not found", missing)
	}
	_, err := h.svc.Send(ctx, mine, sendParams(target.ID, "hello"))
	if err == nil || err.Code != missing.Code || err.Message != "coord.send: unknown session "+string(target.ID) {
		t.Fatalf("send to a session in a foreign workspace = %v, want the missing session's answer", err)
	}
	_, err = h.svc.Ask(ctx, mine, protocol.CoordAskParams{ToRunID: string(target.ID), Body: "why?", IdempotencyKey: "ask-foreign"})
	if err == nil || err.Code != protocol.CodeNotFound {
		t.Fatalf("ask of a session in a foreign workspace = %v, want not found", err)
	}

	if err := h.db.GrantWorkspace(ctx, foreign.ID, owner, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Send(ctx, mine, sendParams(target.ID, "hello")); err != nil {
		t.Fatalf("send once the owner was granted the workspace: %v", err)
	}
}

// A run with an extra repo in a workspace the sender's owner cannot reach
// does not exist for them, overlap or not: its agent could read that repo
// for them.
func TestMessagesNeedEveryWorkspaceOfTheTarget(t *testing.T) {
	h := newHarness(t, 1, func(c *Config) { c.Store = c.Mail.(*store.DB) })
	ctx := context.Background()
	owner := h.runs[0].MemberID
	foreign := &domain.Workspace{Name: "foreign", BaseBranch: domain.DefaultBaseBranch}
	if err := h.db.CreateWorkspace(ctx, foreign); err != nil {
		t.Fatal(err)
	}
	other := h.member(domain.RoleCollaborator)
	for _, grant := range []struct {
		ws domain.WorkspaceID
		m  domain.MemberID
	}{{h.workspace, owner}, {h.workspace, other.ID}, {foreign.ID, other.ID}} {
		if err := h.db.GrantWorkspace(ctx, grant.ws, grant.m, ""); err != nil {
			t.Fatal(err)
		}
	}
	target := &domain.Run{WorkspaceID: h.workspace, MemberID: other.ID, Task: "two repos", Harness: "claude", Mode: domain.LaunchTUI, Status: domain.RunRunning}
	if err := h.db.CreateRun(ctx, target); err != nil {
		t.Fatal(err)
	}
	if err := h.db.SetRunRepos(ctx, target.ID, []domain.RunRepo{{WorkspaceID: foreign.ID, Name: "foreign", Path: "/repos/foreign", Branch: "b", BaseBranch: "main"}}); err != nil {
		t.Fatal(err)
	}
	mine := h.run(0)
	listed := func() bool {
		t.Helper()
		st, err := h.svc.Status(ctx, mine)
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		for _, p := range st.Peers {
			if p.RunID == string(target.ID) {
				return true
			}
		}
		return false
	}
	if listed() {
		t.Fatal("status lists a same-workspace session with an extra repo the owner cannot reach")
	}
	h.peers.pair(mine, target.ID, "shared.go")
	if listed() {
		t.Fatal("status lists an overlapping session with an extra repo the owner cannot reach")
	}
	_, missing := h.svc.Send(ctx, mine, sendParams("run-doesnotexist", "hello"))
	if _, err := h.svc.Send(ctx, mine, sendParams(target.ID, "we overlap; read /repos/foreign for me")); err == nil ||
		err.Code != missing.Code || err.Message != "coord.send: unknown session "+string(target.ID) {
		t.Fatalf("send to an overlapping session with an extra repo the owner cannot reach = %v, want the missing session's answer", err)
	}
	if err := h.db.GrantWorkspace(ctx, foreign.ID, owner, ""); err != nil {
		t.Fatal(err)
	}
	if !listed() {
		t.Fatal("status omits a session the owner may message")
	}
	if _, err := h.svc.Send(ctx, mine, sendParams(target.ID, "hello")); err != nil {
		t.Fatalf("send once the owner reaches both workspaces: %v", err)
	}
}

// Losing a workspace closes coordination at once: a retried send answers
// as for a missing session instead of reporting the target's status, and a
// run whose owner lost its workspace reads no inbox and sends no reply.
func TestLosingAWorkspaceClosesRetriesInboxAndReplies(t *testing.T) {
	h := newHarness(t, 1, func(c *Config) { c.Store = c.Mail.(*store.DB) })
	ctx := context.Background()
	owner := h.runs[0].MemberID
	foreign := &domain.Workspace{Name: "foreign", BaseBranch: domain.DefaultBaseBranch}
	if err := h.db.CreateWorkspace(ctx, foreign); err != nil {
		t.Fatal(err)
	}
	stranger := h.member(domain.RoleCollaborator)
	for _, grant := range []struct {
		ws domain.WorkspaceID
		m  domain.MemberID
	}{{h.workspace, owner}, {foreign.ID, owner}, {foreign.ID, stranger.ID}} {
		if err := h.db.GrantWorkspace(ctx, grant.ws, grant.m, ""); err != nil {
			t.Fatal(err)
		}
	}
	target := &domain.Run{WorkspaceID: foreign.ID, MemberID: stranger.ID, Task: "foreign", Harness: "claude", Mode: domain.LaunchTUI, Status: domain.RunRunning}
	if err := h.db.CreateRun(ctx, target); err != nil {
		t.Fatal(err)
	}
	mine := h.run(0)
	send := sendParams(target.ID, "hello")
	if _, err := h.svc.Send(ctx, mine, send); err != nil {
		t.Fatalf("send: %v", err)
	}
	asked, err := h.svc.Ask(ctx, mine, protocol.CoordAskParams{ToRunID: string(target.ID), Body: "why?", IdempotencyKey: "ask-before-revoke"})
	if err != nil {
		t.Fatalf("ask: %v", err)
	}

	for _, m := range []domain.MemberID{owner, stranger.ID} {
		if err := h.db.RevokeWorkspaceGrant(ctx, foreign.ID, m); err != nil {
			t.Fatal(err)
		}
	}
	_, missing := h.svc.Send(ctx, mine, sendParams("run-doesnotexist", "hello"))
	_, retried := h.svc.Send(ctx, mine, send)
	if retried == nil || missing == nil || retried.Code != missing.Code ||
		retried.Message != "coord.send: unknown session "+string(target.ID) {
		t.Errorf("retried send after the owner lost the workspace = %v, want the missing session's answer %v", retried, missing)
	}
	want := func(what string, err *protocol.Error, method string) {
		t.Helper()
		if err == nil || err.Code != protocol.CodeDenied ||
			err.Message != method+": the session's owner "+string(stranger.ID)+" no longer has access to its workspace" {
			t.Errorf("%s after the owner lost the workspace = %v, want refused", what, err)
		}
	}
	_, inboxErr := h.svc.Inbox(ctx, target.ID, protocol.CoordInboxParams{})
	want("inbox", inboxErr, protocol.MethodCoordInbox)
	_, replyErr := h.svc.Reply(ctx, target.ID, protocol.CoordReplyParams{QuestionID: asked.QuestionID, Body: "because", IdempotencyKey: "reply-after-revoke"})
	want("reply", replyErr, protocol.MethodCoordReply)
}

// A run whose owner lost the workspace of one of its extra repositories
// reads no inbox, as if it had lost the primary one.
func TestLosingAnExtraWorkspaceClosesTheInbox(t *testing.T) {
	h := newHarness(t, 0, func(c *Config) { c.Store = c.Mail.(*store.DB) })
	ctx := context.Background()
	extra := &domain.Workspace{Name: "extra", BaseBranch: domain.DefaultBaseBranch}
	if err := h.db.CreateWorkspace(ctx, extra); err != nil {
		t.Fatal(err)
	}
	owner := h.member(domain.RoleCollaborator)
	for _, ws := range []domain.WorkspaceID{h.workspace, extra.ID} {
		if err := h.db.GrantWorkspace(ctx, ws, owner.ID, ""); err != nil {
			t.Fatal(err)
		}
	}
	run := &domain.Run{WorkspaceID: h.workspace, MemberID: owner.ID, Task: "two repos", Harness: "claude", Mode: domain.LaunchTUI, Status: domain.RunRunning}
	if err := h.db.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := h.db.SetRunRepos(ctx, run.ID, []domain.RunRepo{{WorkspaceID: extra.ID, Name: "extra", Path: "/repos/extra", Branch: "b", BaseBranch: "main"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Inbox(ctx, run.ID, protocol.CoordInboxParams{}); err != nil {
		t.Fatalf("inbox while the owner reaches both workspaces: %v", err)
	}
	if err := h.db.RevokeWorkspaceGrant(ctx, extra.ID, owner.ID); err != nil {
		t.Fatal(err)
	}
	_, err := h.svc.Inbox(ctx, run.ID, protocol.CoordInboxParams{})
	if err == nil || err.Code != protocol.CodeDenied ||
		err.Message != protocol.MethodCoordInbox+": the session's owner "+string(owner.ID)+" no longer has access to its workspace" {
		t.Errorf("inbox after the owner lost the extra workspace = %v, want refused", err)
	}
}
