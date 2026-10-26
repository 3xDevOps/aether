package sshd

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestConnectorMethodScopesNameRegisteredMethods(t *testing.T) {
	for method := range connectorMethodScopes {
		if _, ok := methodHandlers[method]; !ok {
			t.Errorf("connectorMethodScopes lists %s, which no handler serves", method)
		}
	}
}

// A connector calls a method only under the scope connectorMethodScopes
// gives it, and no unlisted method at all; the dashboard and the CLI act
// with the member's role alone.
func TestConnectorScopesGateEachMethod(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	ctx := context.Background()
	run := json.RawMessage(`{"run_id":"` + string(e.run.ID) + `"}`)
	ready := &domain.Run{WorkspaceID: e.ws.ID, MemberID: e.member.ID, Task: "reported", Harness: "claude", Mode: domain.LaunchTUI, Status: domain.RunRunning}
	if err := e.store.CreateRun(ctx, ready); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.store.SetRunReview(ctx, ready.ID, domain.ReviewReady, ""); err != nil {
		t.Fatal(err)
	}
	review := func(id domain.RunID, action string) json.RawMessage {
		return json.RawMessage(`{"run_id":"` + string(id) + `","action":"` + action + `"}`)
	}
	read := []string{ConnectorScopeRead}
	write := []string{ConnectorScopeRead, ConnectorScopeWrite}
	all := []string{ConnectorScopeRead, ConnectorScopeWrite, ConnectorScopeControl}
	connector := func(scopes []string) *Local {
		return e.srv.ConnectorLocal(ConnectorGrant{Member: e.member.ID}, "con-test", "ChatGPT", scopes)
	}
	for _, tc := range []struct {
		name   string
		caller *Local
		method string
		params json.RawMessage
		denied string
	}{
		{"read scope reads", connector(read), protocol.MethodRunGet, run, ""},
		{"read scope cannot write", connector(read), protocol.MethodRunPause, run,
			"run.pause needs the write scope, which ChatGPT connector con-test was not granted; select Reconnect next to it in Settings → ChatGPT and Claude and check Launch and message on the consent page"},
		{"read scope cannot launch", connector(read), protocol.MethodRunLaunch, json.RawMessage(`{}`), "run.launch needs the write scope"},
		{"read scope cannot create a swarm", connector(read), protocol.MethodMissionCreate, json.RawMessage(`{}`), "mission.create needs the write scope"},
		{"read scope cannot stop", connector(read), protocol.MethodRunKill, run, "run.kill needs the control scope"},
		{"write scope writes", connector(write), protocol.MethodRunPause, run, ""},
		{"write scope cannot stop", connector(write), protocol.MethodRunKill, run, "run.kill needs the control scope, which ChatGPT connector con-test was not granted"},
		{"write scope cannot merge", connector(write), protocol.MethodRunPRMerge, run, "run.pr.merge needs the control scope"},
		{"control scope stops", connector(all), protocol.MethodRunKill, run, ""},
		{"write scope cannot complete a live run", connector(write), protocol.MethodRunReview, review(e.run.ID, protocol.RunReviewComplete),
			"run.review complete of a run with no reported success awaiting review needs the control scope"},
		{"write scope puts a review off", connector(write), protocol.MethodRunReview, review(e.run.ID, protocol.RunReviewLater), ""},
		{"write scope completes a reported success", connector(write), protocol.MethodRunReview, review(ready.ID, protocol.RunReviewComplete), ""},
		{"control scope completes a live run", connector(all), protocol.MethodRunReview, review(e.run.ID, protocol.RunReviewComplete), ""},
		{"read scope reads a room", connector(read), protocol.MethodRunRoomList,
			json.RawMessage(`{"workspace_id":"` + string(e.ws.ID) + `","run_id":"` + string(e.run.ID) + `"}`), ""},
		{"unlisted method", connector(all), protocol.MethodMemberRole, json.RawMessage(`{}`),
			"member.role: ChatGPT connector con-test cannot call this method; no connector can, so use the Aether app or the aether CLI"},
		{"dashboard is unscoped", e.srv.Local(e.member.ID), protocol.MethodMemberList, nil, ""},
		{"dashboard stops", e.srv.Local(e.member.ID), protocol.MethodRunKill, run, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, perr := tc.caller.Call(ctx, tc.method, tc.params)
			if tc.denied == "" {
				if perr != nil {
					t.Fatalf("%s = %v, want success", tc.method, perr)
				}
				return
			}
			if perr == nil || perr.Code != protocol.CodeDenied || !strings.HasPrefix(perr.Message, tc.denied) {
				t.Fatalf("%s = %v, want CodeDenied %q", tc.method, perr, tc.denied)
			}
		})
	}
}

// A connector answers a permission request only under control and a
// question only under write, so either scope reaches run.input.respond.
func TestConnectorAnswerNeedsTheRequestsScope(t *testing.T) {
	e, run := newACPEnv(t)
	e.runs.acpPending = map[domain.RunID][]acphost.Request{run.ID: {
		{ID: "perm", Kind: acphost.RequestPermission, Status: acphost.RequestPending},
		{ID: "ask", Kind: acphost.RequestQuestion, Status: acphost.RequestPending},
	}}
	connector := func(scopes ...string) *Local {
		return e.srv.ConnectorLocal(ConnectorGrant{Member: e.member.ID}, "con-test", "ChatGPT", append([]string{ConnectorScopeRead}, scopes...))
	}
	for _, tc := range []struct {
		name    string
		caller  *Local
		request string
		denied  string
	}{
		{"read cannot answer", connector(), "ask", "run.input.respond needs the write or control scope"},
		{"write answers a question", connector(ConnectorScopeWrite), "ask", ""},
		{"write cannot decide a permission", connector(ConnectorScopeWrite), "perm",
			"run.input.respond needs the control scope, which ChatGPT connector con-test was not granted; select Reconnect next to it in Settings → ChatGPT and Claude and check Stop and approve on the consent page"},
		{"control decides a permission", connector(ConnectorScopeControl), "perm", ""},
		{"control cannot answer a question", connector(ConnectorScopeControl), "ask", "run.input.respond needs the write scope"},
		{"dashboard decides a permission", e.srv.Local(e.member.ID), "perm", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			params, _ := json.Marshal(protocol.RunInputRespondParams{RunID: string(run.ID), RequestID: tc.request, OptionID: "allow"})
			_, perr := tc.caller.Call(t.Context(), protocol.MethodRunInputRespond, params)
			switch {
			case tc.denied == "" && perr != nil:
				t.Fatalf("respond = %v", perr)
			case tc.denied != "" && (perr == nil || perr.Code != protocol.CodeDenied || !strings.HasPrefix(perr.Message, tc.denied)):
				t.Fatalf("respond = %v, want CodeDenied %q", perr, tc.denied)
			}
		})
	}
}
