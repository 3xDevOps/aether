//go:build integration

package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/scheduler"
)

// TestIntegrationAssistantApprovalRule drives an approval rule through the
// wired server: approving a launch card with always saves the rule the card
// offers, the next launch of that kind runs without a card and is recorded
// as passed by the rule, a launch of another kind still asks, the tool
// socket can neither list nor revoke rules, and revoking brings the card
// back.
func TestIntegrationAssistantApprovalRule(t *testing.T) {
	requireBinary(t, "git")
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	t.Setenv("AETHER_FAKE_AGENT", "fake-agent {task}")
	env := startAssistantServer(t, ctx, nil)
	defer env.stop()
	ws := seedAssistantWorkspace(t, ctx, env, "rules")
	if perr := env.call(protocol.MethodAssistantStart, protocol.AssistantStartParams{}, nil); perr != nil {
		t.Fatalf("assistant.start: %v", perr)
	}
	frames := env.watch(ctx, 0)
	conn, err := net.Dial("unix", filepath.Join(scheduler.AssistantSocketDir(filepath.Join(env.data, "assistant"), env.member.ID), "assistant.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	r := bufio.NewReader(conn)
	tool := func(method string, params any) protocol.Response {
		t.Helper()
		raw, _ := json.Marshal(params)
		req, _ := json.Marshal(protocol.Request{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: method, Params: raw})
		if _, err := conn.Write(append(req, '\n')); err != nil {
			t.Fatal(err)
		}
		line, err := protocol.ReadLine(r)
		if err != nil {
			t.Fatalf("tool %s: %v", method, err)
		}
		var resp protocol.Response
		if err := json.Unmarshal(line, &resp); err != nil {
			t.Fatal(err)
		}
		return resp
	}
	launch := func(task string, noFallback bool) protocol.RunLaunchParams {
		env.rt.script(task, func(c *e2eContainer) { c.exitNow(0) })
		return protocol.RunLaunchParams{WorkspaceID: string(ws.ID), Task: task, Harness: "fake", Mode: string(domain.LaunchHeadless), NoFallback: noFallback}
	}
	// answer makes the call, waits for its card and answers it with p.
	answer := func(params protocol.RunLaunchParams, p protocol.AssistantConfirmParams, out any) (protocol.AssistantConfirmation, protocol.Response) {
		t.Helper()
		done := make(chan protocol.Response, 1)
		go func() { done <- tool(protocol.MethodRunLaunch, params) }()
		pending := frames.until(t, "the card for "+params.Task, func(f protocol.AssistantFrame) bool {
			return f.Status != nil && len(f.Status.Confirmations) == 1 && strings.Contains(f.Status.Confirmations[0].Summary, params.Task)
		})
		card := pending.Status.Confirmations[0]
		p.ConfirmationID = card.ID
		if perr := env.call(protocol.MethodAssistantConfirm, p, out); perr != nil {
			t.Fatalf("assistant.confirm %s: %v", params.Task, perr)
		}
		return card, <-done
	}

	var saved protocol.AssistantConfirmResult
	card, resp := answer(launch("first of its kind", false), protocol.AssistantConfirmParams{Approve: true, Always: true}, &saved)
	const wording = "Always approve launches of fake (headless) in rules on your account, permission default"
	if card.Rule == nil || card.Rule.Wording != wording {
		t.Fatalf("card rule = %+v, want %q", card.Rule, wording)
	}
	if resp.Error != nil || saved.Rule == nil || saved.Rule.Wording != wording {
		t.Fatalf("always = %+v, rule %+v", resp.Error, saved.Rule)
	}

	if resp = tool(protocol.MethodRunLaunch, launch("second of its kind", false)); resp.Error != nil {
		t.Fatalf("launch the rule covers = %+v", resp.Error)
	}
	if st := env.status(); len(st.Confirmations) != 0 {
		t.Fatalf("cards after a launch the rule covers = %+v", st.Confirmations)
	}

	card, resp = answer(launch("another kind", true), protocol.AssistantConfirmParams{Approve: false}, nil)
	if card.Rule == nil || !strings.HasSuffix(card.Rule.Wording, ", no fallback") || resp.Error == nil || !strings.Contains(resp.Error.Message, "declined") {
		t.Fatalf("launch of another kind: card %+v, response %+v; want a card the member declined", card, resp)
	}

	for _, method := range []string{protocol.MethodAssistantRules, protocol.MethodAssistantRuleRevoke} {
		if resp = tool(method, protocol.AssistantRuleRevokeParams{ID: saved.Rule.ID}); resp.Error == nil || resp.Error.Code != protocol.CodeMethodNotFound {
			t.Fatalf("%s through the tool socket = %+v, want method not found", method, resp)
		}
	}
	var rules protocol.AssistantRulesResult
	if perr := env.call(protocol.MethodAssistantRules, nil, &rules); perr != nil || len(rules.Rules) != 1 || rules.Rules[0].Uses != 1 || rules.Rules[0].Inactive != "" {
		t.Fatalf("assistant.rules = %+v, %v; want the one rule, used once", rules, perr)
	}
	if perr := env.call(protocol.MethodAssistantRuleRevoke, protocol.AssistantRuleRevokeParams{ID: saved.Rule.ID}, nil); perr != nil {
		t.Fatalf("assistant.rules.revoke: %v", perr)
	}
	if _, resp = answer(launch("after the revoke", false), protocol.AssistantConfirmParams{Approve: false}, nil); resp.Error == nil {
		t.Fatal("a launch after the revoke ran without the member's answer")
	}

	var history protocol.RunACPHistoryResult
	if perr := env.call(protocol.MethodAssistantHistory, protocol.AssistantHistoryParams{}, &history); perr != nil {
		t.Fatal(perr)
	}
	var all strings.Builder
	for _, f := range history.Frames {
		all.Write(f.Item)
	}
	if want := `"title":"Auto-approved by rule ` + saved.Rule.ID + `: Launch fake (headless) in rules: \"second of its kind\""`; !strings.Contains(all.String(), want) {
		t.Fatalf("transcript lacks %s:\n%s", want, all.String())
	}
}
