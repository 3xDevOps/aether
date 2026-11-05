package main

import (
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestAssistantRulesListsAndRevokes(t *testing.T) {
	at := time.Date(2026, 11, 3, 14, 2, 0, 0, time.Local)
	c := fakeControlMethods(t, map[string]*fakeReply{
		protocol.MethodAssistantRules: {result: protocol.AssistantRulesResult{Rules: []protocol.AssistantRule{
			{ID: "rule-1", Method: protocol.MethodRunLaunch, Wording: "Always approve launches of Claude Code (Enhanced) in api on your account, permission default, for any task",
				Summary: `Launch claude (acp) in api: "Fix it"`, CreatedAt: at, LastUsedAt: at, Uses: 3},
			{ID: "rule-2", Method: protocol.MethodRunKill, Wording: "Always approve stopping your own sessions in web", Summary: "Stop session run-1",
				CreatedAt: at, Inactive: "workspace ws-web no longer exists or you lost access to it"},
		}}},
		protocol.MethodAssistantRuleRevoke: {result: struct{}{}},
	})
	var out strings.Builder
	if err := assistantRules(c, &out, ""); err != nil {
		t.Fatal(err)
	}
	want := "rule-1  created 2026-11-03 14:02, 3 uses, last 2026-11-03 14:02\n" +
		"  Always approve launches of Claude Code (Enhanced) in api on your account, permission default, for any task\n" +
		"  from: Launch claude (acp) in api: \"Fix it\"\n" +
		"rule-2  created 2026-11-03 14:02, never used\n" +
		"  Always approve stopping your own sessions in web\n" +
		"  from: Stop session run-1\n" +
		"  Inactive: workspace ws-web no longer exists or you lost access to it\n"
	if out.String() != want {
		t.Fatalf("list = %q, want %q", out.String(), want)
	}
	out.Reset()
	if err := assistantRules(c, &out, "rule-1"); err != nil || out.String() != "revoked rule-1: the cards it passed ask again\n" {
		t.Fatalf("revoke = %q, %v", out.String(), err)
	}
}

// A card's summary quotes the task the assistant wrote, so the list prints
// no control character that could drive the terminal.
func TestAssistantRulesNeutralizesControlCharacters(t *testing.T) {
	c := fakeControlMethods(t, map[string]*fakeReply{
		protocol.MethodAssistantRules: {result: protocol.AssistantRulesResult{Rules: []protocol.AssistantRule{{
			ID: "rule-1", Wording: "Always approve\x07", Summary: "Launch \x1b[2J\x1b]8;;https://example.invalid\x07x\x1b]8;;\x07\rdone", CreatedAt: time.Now(),
		}}}},
	})
	var out strings.Builder
	if err := assistantRules(c, &out, ""); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(out.String(), "\x1b\x07\r") {
		t.Fatalf("list = %q, want no control characters", out.String())
	}
}
