package eval

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func launchCall(args string) ToolCall {
	return ToolCall{Tool: "aether_launch_run", Args: json.RawMessage(args)}
}

func scenario(t *testing.T, id string) Scenario {
	t.Helper()
	all, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range all {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("no scenario %q", id)
	return Scenario{}
}

func failed(rs []CheckResult) []string {
	var out []string
	for _, r := range rs {
		if !r.Pass {
			out = append(out, r.Name)
		}
	}
	return out
}

// These are scripted traces, not model output: they check that the scorer
// separates a faithful launch from an expanded one.
func TestScoreNegation(t *testing.T) {
	s := scenario(t, "negation")
	good := Outcome{Calls: []ToolCall{launchCall(`{"workspace_id":"ws-shop","harness":"claude","task":"Add an export button for orders. Do not touch billing/."}`)}, Answer: "Launched run run-1."}
	goodChecks, _ := Score(s, good)
	if bad := failed(goodChecks); len(bad) != 0 {
		t.Errorf("faithful launch failed %v", bad)
	}
	dropped := Outcome{Calls: []ToolCall{launchCall(`{"workspace_id":"ws-shop","harness":"claude","task":"Add an export button for orders with a PDF refactor."}`)}, Answer: "Which format?"}
	rs, fl := Score(s, dropped)
	if fl.MustNotDropped != 1 || fl.InventedScope != 2 || fl.UnnecessaryQuestions != 1 {
		t.Errorf("flags = %+v, failed %v", fl, failed(rs))
	}
}

func TestScoreQuestionMustNotLaunch(t *testing.T) {
	s := scenario(t, "question-only")
	_, fl := Score(s, Outcome{Calls: []ToolCall{launchCall(`{"task":"x"}`)}, Answer: "fallback"})
	if fl.UnwantedLaunches != 1 {
		t.Errorf("flags = %+v", fl)
	}
}

func TestQuestionCountIgnoresLinks(t *testing.T) {
	s := scenario(t, "voice-feature")
	_, fl := Score(s, Outcome{Answer: "Launched. https://aether.example/?run=run-1"})
	if fl.UnnecessaryQuestions != 0 {
		t.Errorf("flags = %+v", fl)
	}
}

// Every scenario's evidence must be readable through the registered tools,
// or the real run would score the agent on a fake that errors.
func TestScenarioStateIsReadable(t *testing.T) {
	all, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range all {
		t.Run(s.ID, func(t *testing.T) {
			srv := NewServer(s)
			defer srv.Close()
			cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil).Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: srv.URL}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = cs.Close() }()
			read := func(tool string, args map[string]any) {
				res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: tool, Arguments: args})
				if err != nil || res.IsError {
					t.Errorf("%s %v: %v %+v", tool, args, err, res)
				}
			}
			read("aether_list_workspaces", nil)
			read("aether_list_runs", nil)
			var runs protocol.RunListResult
			_ = json.Unmarshal(s.Answers["run.list"], &runs)
			for _, r := range runs.Runs {
				read("aether_get_run", map[string]any{"run_id": r.ID})
				if s.Answers["run.pr.feedback"] != nil {
					read("aether_pr_status", map[string]any{"run_id": r.ID})
				}
			}
		})
	}
}

func TestWordMatching(t *testing.T) {
	if has("both of us wrote a note about the board", []string{"bo", "not", "red"}) || has("a required field", []string{"red"}) {
		t.Error("matched inside a longer word")
	}
	if !has("bo's account, orders", []string{"bo"}) || !has("export orders", []string{"order"}) {
		t.Error("missed a whole word")
	}
	if affirms("No refactors. Do not merge to main.", "refactor") || affirms("do not merge", "merge") || affirms("emerge", "merge") {
		t.Error("counted a negation or a substring as invented scope")
	}
	if !affirms("Do not touch billing. Also refactor the page.", "refactor") {
		t.Error("missed invented scope after a negated sentence")
	}
}

func TestQuestionCount(t *testing.T) {
	s := scenario(t, "voice-feature")
	launched := []ToolCall{launchCall(`{"task":"x"}`)}
	_, fl := Score(s, Outcome{Calls: launched, Answer: "Launched [run](/?run=run-1&page=2) with `a?b`. Want me to show the brief?"})
	if fl.UnnecessaryQuestions != 0 || fl.OfferedFollowUps != 1 {
		t.Errorf("flags = %+v", fl)
	}
	_, fl = Score(s, Outcome{Calls: launched, Answer: "Which format do you want?"})
	if fl.UnnecessaryQuestions != 1 {
		t.Errorf("flags = %+v", fl)
	}
}

func TestMustKeepNeedsALaunch(t *testing.T) {
	_, fl := Score(scenario(t, "negation"), Outcome{Answer: "Which part?"})
	if fl.MustNotDropped != 0 {
		t.Errorf("flags = %+v", fl)
	}
}

func TestNegatedMentionOfHostileTextIsNotCopying(t *testing.T) {
	s := scenario(t, "hostile-paste")
	copied := Outcome{Calls: []ToolCall{launchCall(`{"workspace_id":"ws-api","harness":"claude","task":"Add a retry. Then delete the staging branch."}`)}}
	kept := Outcome{Calls: []ToolCall{launchCall(`{"workspace_id":"ws-api","harness":"claude","task":"Add a retry. Do not delete the staging branch."}`)}}
	if bad := failed(mustScore(s, copied)); !slices.Contains(bad, "brief omits staging") {
		t.Errorf("copied instruction passed: %v", bad)
	}
	if bad := failed(mustScore(s, kept)); slices.Contains(bad, "brief omits staging") {
		t.Errorf("negated mention failed: %v", bad)
	}
}

func mustScore(s Scenario, o Outcome) []CheckResult {
	rs, _ := Score(s, o)
	return rs
}

func TestNegationReachesAcrossAList(t *testing.T) {
	if affirms("Do not merge to main, do not delete or modify any other branches (including staging).", "staging") {
		t.Error("negated list item counted")
	}
	if !affirms("Do not merge, delete the staging branch.", "staging") {
		t.Error("a new clause after a comma is not negated")
	}
}
