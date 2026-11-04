package sshd

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// rulesOnly is an assistant service that records the rule calls it gets.
type rulesOnly struct {
	AssistantService
	calls chan string
}

func (a rulesOnly) ApproveAlways(_ context.Context, _ domain.MemberID, id string) (protocol.AssistantRule, error) {
	a.calls <- "always " + id
	return protocol.AssistantRule{ID: "rule-1"}, nil
}

func (a rulesOnly) Rules(context.Context, domain.MemberID) ([]protocol.AssistantRule, error) {
	a.calls <- "rules"
	return nil, nil
}

func (a rulesOnly) RevokeRule(_ context.Context, _ domain.MemberID, id string) error {
	a.calls <- "revoke " + id
	return nil
}

// Only the member, from an Aether device, saves, lists or revokes approval
// rules: the assistant's own calls and every connector are refused before
// the service sees them.
func TestAssistantRulesOnlyFromTheMember(t *testing.T) {
	t.Parallel()
	svc := rulesOnly{calls: make(chan string, 8)}
	e := newTestEnv(t, func(c *Config) { c.Services.Assistant = svc })
	ctx := context.Background()
	always := json.RawMessage(`{"confirmation_id":"c1","approve":true,"always":true}`)
	revoke := json.RawMessage(`{"id":"rule-1"}`)
	connector := e.srv.ConnectorLocal(ConnectorGrant{Member: e.member.ID}, "con-test", "ChatGPT",
		[]string{ConnectorScopeRead, ConnectorScopeWrite, ConnectorScopeControl})
	for _, caller := range []*Local{e.srv.AssistantLocal(e.member.ID), connector} {
		for method, params := range map[string]json.RawMessage{
			protocol.MethodAssistantConfirm:    always,
			protocol.MethodAssistantRules:      nil,
			protocol.MethodAssistantRuleRevoke: revoke,
		} {
			if _, perr := caller.Call(ctx, method, params); perr == nil || perr.Code != protocol.CodeDenied {
				t.Errorf("%s = %v, want denied", method, perr)
			}
		}
	}
	select {
	case call := <-svc.calls:
		t.Fatalf("a refused caller reached the service: %s", call)
	default:
	}

	member := e.srv.Local(e.member.ID)
	var confirmed protocol.AssistantConfirmResult
	raw, perr := member.Call(ctx, protocol.MethodAssistantConfirm, always)
	if perr != nil || json.Unmarshal(raw, &confirmed) != nil || confirmed.Rule == nil || confirmed.Rule.ID != "rule-1" {
		t.Fatalf("always from the member = %s, %v", raw, perr)
	}
	if _, perr = member.Call(ctx, protocol.MethodAssistantConfirm, json.RawMessage(`{"confirmation_id":"c1","always":true}`)); perr == nil || perr.Code != protocol.CodeInvalidParams {
		t.Fatalf("always without approve = %v, want invalid params", perr)
	}
	if raw, perr = member.Call(ctx, protocol.MethodAssistantRules, nil); perr != nil || !strings.Contains(string(raw), `"rules":[]`) {
		t.Fatalf("rules from the member = %s, %v", raw, perr)
	}
	if _, perr = member.Call(ctx, protocol.MethodAssistantRuleRevoke, revoke); perr != nil {
		t.Fatalf("revoke from the member = %v", perr)
	}
	for _, want := range []string{"always c1", "rules", "revoke rule-1"} {
		if got := <-svc.calls; got != want {
			t.Fatalf("service call = %q, want %q", got, want)
		}
	}
	for _, method := range []string{protocol.MethodAssistantConfirm, protocol.MethodAssistantRules, protocol.MethodAssistantRuleRevoke} {
		if _, listed := connectorMethodScopes[method]; listed {
			t.Errorf("connectors may call %s", method)
		}
	}
}
