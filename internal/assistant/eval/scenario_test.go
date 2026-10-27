package eval

import (
	"encoding/json"
	"testing"
)

func tc(tool, args string) ToolCall { return ToolCall{Tool: tool, Args: json.RawMessage(args)} }

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
	good := Outcome{Calls: []ToolCall{tc("aether_launch_run", `{"workspace_id":"ws-shop","harness":"claude","task":"Add an export button for orders. Do not touch billing/."}`)}, Answer: "Launched run run-1."}
	if bad := failed(first(Score(s, good))); len(bad) != 0 {
		t.Errorf("faithful launch failed %v", bad)
	}
	dropped := Outcome{Calls: []ToolCall{tc("aether_launch_run", `{"workspace_id":"ws-shop","harness":"claude","task":"Add an export button for orders with a PDF refactor."}`)}, Answer: "Which format?"}
	rs, fl := Score(s, dropped)
	if fl.MustNotDropped != 1 || fl.InventedScope != 2 || fl.UnnecessaryQuestions != 1 {
		t.Errorf("flags = %+v, failed %v", fl, failed(rs))
	}
}

func TestScoreQuestionMustNotLaunch(t *testing.T) {
	s := scenario(t, "question-only")
	_, fl := Score(s, Outcome{Calls: []ToolCall{tc("aether_launch_run", `{"task":"x"}`)}, Answer: "fallback"})
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

func first(rs []CheckResult, _ Flags) []CheckResult { return rs }
