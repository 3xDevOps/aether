package eval

import (
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/3xDevOps/Aether/internal/protocol"
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
	goodChecks, _ := Score(s, good)
	if bad := failed(goodChecks); len(bad) != 0 {
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
			var missions protocol.MissionListResult
			_ = json.Unmarshal(s.Answers["mission.list"], &missions)
			for _, m := range missions.Missions {
				read("aether_get_swarm", map[string]any{"swarm_id": m.ID})
			}
		})
	}
}
