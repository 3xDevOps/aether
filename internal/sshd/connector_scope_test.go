package sshd

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestConnectorMethodScopesNameRegisteredMethods(t *testing.T) {
	unregistered := map[string]bool{"changes.list": true, "run.children": true}
	for method := range connectorMethodScopes {
		if _, ok := methodHandlers[method]; !ok && !unregistered[method] {
			t.Errorf("connectorMethodScopes lists %s, which no handler serves", method)
		}
	}
}

// A connector calls a method only under the scope connectorMethodScopes
// gives it, and no unlisted method at all; the dashboard, the CLI and the
// assistant act with the member's role alone.
func TestConnectorScopesGateEachMethod(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	ctx := context.Background()
	run := json.RawMessage(`{"run_id":"` + string(e.run.ID) + `"}`)
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
		{"unlisted audit history", connector(all), protocol.MethodAuditList, nil, "audit.list: ChatGPT connector con-test cannot call this method"},
		{"unlisted method", connector(all), protocol.MethodMemberRole, json.RawMessage(`{}`),
			"member.role: ChatGPT connector con-test cannot call this method; no connector can, so use the Aether app or the aether CLI"},
		{"dashboard is unscoped", e.srv.Local(e.member.ID), protocol.MethodMemberList, nil, ""},
		{"dashboard stops", e.srv.Local(e.member.ID), protocol.MethodRunKill, run, ""},
		{"assistant is unscoped", e.srv.AssistantLocal(e.member.ID), protocol.MethodRunKill, run, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, perr := tc.caller.Call(ctx, tc.method, tc.params)
			if tc.denied == "" {
				if perr != nil && (strings.Contains(perr.Message, " scope, which ") || strings.Contains(perr.Message, "cannot call this method")) {
					t.Fatalf("%s = %v, want no scope refusal", tc.method, perr)
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

func TestAuditRecordsConnectorWritesForTheirMemberAndAdmins(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	ctx := context.Background()
	_, bo := addMember(t, e, "Bo", domain.RoleCollaborator, false)
	_, cy := addMember(t, e, "Cy", domain.RoleCollaborator, false)
	run := json.RawMessage(`{"run_id":"` + string(e.run.ID) + `"}`)
	boConnector := e.srv.ConnectorLocal(ConnectorGrant{Member: bo.ID}, "con-bo", "Claude", []string{ConnectorScopeRead})
	adaConnector := e.srv.ConnectorLocal(ConnectorGrant{Member: e.member.ID}, "con-ada", "ChatGPT",
		[]string{ConnectorScopeRead, ConnectorScopeWrite})

	if _, perr := boConnector.Call(ctx, protocol.MethodRunGet, run); perr != nil {
		t.Fatalf("run.get: %v", perr)
	}
	if _, perr := boConnector.Call(ctx, protocol.MethodRunKill, run); perr == nil {
		t.Fatal("run.kill through a read-scoped connector succeeded")
	}
	if _, perr := adaConnector.Call(ctx, protocol.MethodRunInject,
		json.RawMessage(`{"run_id":"`+string(e.run.ID)+`","message":"secret plan","idempotency_key":"k1"}`)); perr != nil {
		t.Fatalf("run.inject: %v", perr)
	}
	if _, perr := e.srv.Local(e.member.ID).Call(ctx, protocol.MethodRunPause, run); perr != nil {
		t.Fatalf("dashboard run.pause: %v", perr)
	}
	if _, perr := e.srv.AssistantLocal(e.member.ID).Call(ctx, protocol.MethodRunResume, run); perr != nil {
		t.Fatalf("assistant run.resume: %v", perr)
	}
	if _, perr := e.srv.Local(bo.ID).Call(ctx, protocol.MethodAccountShare, json.RawMessage(`{"member_id":"`+string(cy.ID)+`"}`)); perr != nil {
		t.Fatalf("account.share: %v", perr)
	}

	list := func(l *Local, params string) protocol.AuditListResult {
		t.Helper()
		raw, perr := l.Call(ctx, protocol.MethodAuditList, json.RawMessage(params))
		if perr != nil {
			t.Fatalf("audit.list %s: %v", params, perr)
		}
		var out protocol.AuditListResult
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	type row struct{ member, via, connector, method, target, outcome string }
	rows := func(res protocol.AuditListResult) []row {
		var out []row
		for _, e := range res.Entries {
			out = append(out, row{e.MemberID, e.Via, e.ConnectorID, e.Method, e.Target, e.Outcome})
		}
		return out
	}
	runID := string(e.run.ID)
	wantAll := []row{
		{string(bo.ID), ViaDashboard, "", protocol.MethodAccountShare, string(cy.ID), "ok"},
		{string(e.member.ID), ViaAssistant, "", protocol.MethodRunResume, runID, "ok"},
		{string(e.member.ID), ViaConnector, "con-ada", protocol.MethodRunInject, runID, "ok"},
		{string(bo.ID), ViaConnector, "con-bo", protocol.MethodRunKill, runID, "error"},
	}
	if got := rows(list(e.srv.Local(e.member.ID), `{}`)); !slices.Equal(got, wantAll) {
		t.Fatalf("admin audit.list = %+v, want %+v", got, wantAll)
	}
	if got, want := rows(list(e.srv.Local(bo.ID), `{}`)), []row{wantAll[0], wantAll[3]}; !slices.Equal(got, want) {
		t.Fatalf("bo's audit.list = %+v, want only bo's %+v", got, want)
	}
	if got := list(e.srv.Local(cy.ID), `{}`); len(got.Entries) != 0 {
		t.Fatalf("cy's audit.list = %+v, want none of bo's or ada's entries", got.Entries)
	}
	if got, want := rows(list(e.srv.Local(e.member.ID), `{"connector_id":"con-ada"}`)), wantAll[2:3]; !slices.Equal(got, want) {
		t.Fatalf("audit.list for con-ada = %+v, want %+v", got, want)
	}
	page := list(e.srv.Local(e.member.ID), `{"limit":3}`)
	if !slices.Equal(rows(page), wantAll[:3]) || page.NextBefore == 0 {
		t.Fatalf("first page = %+v next_before %d", rows(page), page.NextBefore)
	}
	rest := list(e.srv.Local(e.member.ID), `{"limit":3,"before":`+strconv.FormatInt(page.NextBefore, 10)+`}`)
	if !slices.Equal(rows(rest), wantAll[3:]) || rest.NextBefore != 0 {
		t.Fatalf("second page = %+v next_before %d", rows(rest), rest.NextBefore)
	}
	raw, _ := json.Marshal(list(e.srv.Local(e.member.ID), `{}`))
	if strings.Contains(string(raw), "secret plan") {
		t.Fatalf("audit entries hold a message body: %s", raw)
	}
	if _, perr := e.srv.Local(e.member.ID).Call(ctx, protocol.MethodAuditList, json.RawMessage(`{"limit":201}`)); perr == nil || perr.Code != protocol.CodeInvalidParams {
		t.Fatalf("audit.list limit 201 = %v, want invalid params", perr)
	}
}
