package coordcli

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestCLIMissionPlanUsageErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "mission without subcommand", args: []string{"mission"}},
		{name: "unknown group", args: []string{"mission", "wat", "show"}},
		{name: "clarification without subcommand", args: []string{"mission", "clarification"}},
		{name: "unknown clarification command", args: []string{"mission", "clarification", "start"}},
		{name: "clarification complete without key", args: []string{"mission", "clarification", "complete"}},
		{name: "clarification complete with positional argument", args: []string{"mission", "clarification", "complete", "--idempotency-key", "clarify-1", "extra"}},
		{name: "question without subcommand", args: []string{"mission", "question"}},
		{name: "unknown question command", args: []string{"mission", "question", "answer"}},
		{name: "plan without subcommand", args: []string{"mission", "plan"}},
		{name: "unknown plan command", args: []string{"mission", "plan", "decide"}},
		{name: "ask unknown flag", args: []string{"mission", "question", "ask", "--unknown"}},
		{name: "ask without body", args: []string{"mission", "question", "ask", "--idempotency-key", "ask-1"}},
		{name: "ask without key", args: []string{"mission", "question", "ask", "--body", "which checkout flow?"}},
		{name: "ask with positional argument", args: []string{"mission", "question", "ask", "--body", "why?", "--idempotency-key", "ask-1", "extra"}},
		{name: "submit without summary", args: []string{"mission", "plan", "submit", "--idempotency-key", "submit-1"}},
		{name: "submit without key", args: []string{"mission", "plan", "submit", "--summary", "build it"}},
		{name: "show wait above maximum", args: []string{"mission", "plan", "show", "--wait", "31"}},
		{name: "show wait below zero", args: []string{"mission", "plan", "show", "--wait", "-1"}},
		{name: "show malformed wait", args: []string{"mission", "plan", "show", "--wait", "soon"}},
		{name: "show with positional argument", args: []string{"mission", "plan", "show", "mission-1"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := newCLISocket(t, func(req protocol.Request) protocol.Response {
				t.Errorf("usage error still issued %s", req.Method)
				return protocol.Response{Error: &protocol.Error{Code: protocol.CodeInternal}}
			})
			code, raw := runCLI(t, s.path, test.args, "")
			if code != ExitUsage {
				t.Fatalf("exit = %d, want %d; output = %s", code, ExitUsage, raw)
			}
			env := decodeEnvelope(t, raw)
			if env.OK || env.Error == nil || env.Error.Code != protocol.CodeInvalidParams {
				t.Fatalf("envelope = %+v, want invalid params", env)
			}
			if got := s.requests(); len(got) != 0 {
				t.Fatalf("requests = %+v, want none", got)
			}
		})
	}
}

func TestCLIMissionPlanHelpIsSocketIndependent(t *testing.T) {
	for _, args := range [][]string{
		{"mission", "--help"},
		{"mission", "clarification", "complete", "--help"},
		{"mission", "question", "ask", "--help"},
		{"mission", "plan", "show", "--help"},
		{"mission", "plan", "submit", "--help"},
	} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			var out bytes.Buffer
			code, err := Run(context.Background(), args, Config{Socket: filepath.Join(t.TempDir(), "missing.sock"), Out: &out})
			if err != nil {
				t.Fatal(err)
			}
			if code != ExitOK || !strings.Contains(out.String(), "usage: aether-internal mission") {
				t.Fatalf("help = code %d, output %q", code, out.String())
			}
		})
	}
}

func TestCLIMissionPlanEnvelopesAndParams(t *testing.T) {
	s := newCLISocket(t, func(req protocol.Request) protocol.Response {
		var raw map[string]any
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &raw); err != nil {
				t.Fatalf("decode %s params: %v", req.Method, err)
			}
		}
		if raw["mission_id"] != nil || raw["run_id"] != nil {
			t.Fatalf("%s carried a caller-supplied mission or run identity: %+v", req.Method, raw)
		}
		switch req.Method {
		case protocol.MethodMissionClarificationComplete:
			var p protocol.MissionClarificationCompleteParams
			if err := json.Unmarshal(req.Params, &p); err != nil {
				t.Fatalf("decode clarification complete: %v", err)
			}
			if p.IdempotencyKey != "clarify-1" {
				t.Fatalf("clarification complete params = %+v", p)
			}
			return protocol.Response{Result: json.RawMessage(`{"plan":{"mission_id":"mission-1","phase":"clarified","plan_version":0,"integrator_generation":1}}`)}
		case protocol.MethodMissionQuestionAsk:
			var p protocol.MissionQuestionAskParams
			if err := json.Unmarshal(req.Params, &p); err != nil {
				t.Fatalf("decode ask: %v", err)
			}
			if p.Body != "which checkout flow?" || p.IdempotencyKey != "ask-1" {
				t.Fatalf("ask params = %+v", p)
			}
			return protocol.Response{Result: json.RawMessage(`{"question":{"id":"question-1","mission_id":"mission-1","seq":1,"body":"which checkout flow?","asked_by_run_id":"run-1","asked_at":"2026-01-01T00:00:00Z"}}`)}
		case protocol.MethodMissionPlanShow:
			var p protocol.MissionPlanShowParams
			if err := json.Unmarshal(req.Params, &p); err != nil {
				t.Fatalf("decode show: %v", err)
			}
			if p.WaitSeconds != protocol.CoordMaxInboxWaitSeconds {
				t.Fatalf("show wait_seconds = %d", p.WaitSeconds)
			}
			return protocol.Response{Result: json.RawMessage(`{"plan":{"mission_id":"mission-1","phase":"planning","plan_version":0,"integrator_generation":1,"open_questions":1}}`)}
		case protocol.MethodMissionPlanSubmit:
			var p protocol.MissionPlanSubmitParams
			if err := json.Unmarshal(req.Params, &p); err != nil {
				t.Fatalf("decode submit: %v", err)
			}
			if p.Summary != "build the checkout flow" || p.IdempotencyKey != "submit-1" {
				t.Fatalf("submit params = %+v", p)
			}
			return protocol.Response{Result: json.RawMessage(`{"plan":{"mission_id":"mission-1","phase":"plan_review","plan_version":1,"integrator_generation":1}}`)}
		default:
			return protocol.Response{Error: &protocol.Error{Code: protocol.CodeMethodNotFound, Message: "unexpected " + req.Method}}
		}
	})

	code, raw := runCLI(t, s.path, []string{"mission", "clarification", "complete", "--idempotency-key", "clarify-1"}, "")
	if code != ExitOK {
		t.Fatalf("mission clarification complete = code %d, envelope %s", code, raw)
	}
	var clarified protocol.MissionClarificationCompleteResult
	decodeResult(t, raw, &clarified)
	if clarified.Plan.Phase != "clarified" {
		t.Fatalf("clarified plan state = %+v", clarified.Plan)
	}

	code, raw = runCLI(t, s.path, []string{"mission", "question", "ask", "--body-file", "-", "--idempotency-key", "ask-1"}, "which checkout flow?")
	if code != ExitOK || !decodeEnvelope(t, raw).OK {
		t.Fatalf("mission question ask = code %d, envelope %s", code, raw)
	}

	code, raw = runCLI(t, s.path, []string{"mission", "plan", "show", "--wait", "30"}, "")
	if code != ExitOK {
		t.Fatalf("mission plan show = code %d, envelope %s", code, raw)
	}
	var show protocol.MissionPlanShowResult
	decodeResult(t, raw, &show)
	if show.Plan.Phase != "planning" || show.Plan.OpenQuestions != 1 {
		t.Fatalf("plan state = %+v", show.Plan)
	}
	if show.Questions == nil || show.PlanReviews == nil {
		t.Fatalf("plan show result carried null collections: %s", raw)
	}

	code, raw = runCLI(t, s.path, []string{"mission", "plan", "submit", "--summary", "build the checkout flow", "--idempotency-key", "submit-1"}, "")
	if code != ExitOK {
		t.Fatalf("mission plan submit = code %d, envelope %s", code, raw)
	}
	var submit protocol.MissionPlanSubmitResult
	decodeResult(t, raw, &submit)
	if submit.Plan.Phase != "plan_review" || submit.Plan.PlanVersion != 1 {
		t.Fatalf("submitted plan = %+v", submit.Plan)
	}
}

func TestCLIMissionPlanPhaseRefusalIsDenied(t *testing.T) {
	s := newCLISocket(t, func(protocol.Request) protocol.Response {
		return protocol.Response{Error: &protocol.Error{Code: protocol.CodeInvalidState, Message: "mission.plan.submit: mission is in phase plan_review"}}
	})
	code, raw := runCLI(t, s.path, []string{"mission", "plan", "submit", "--summary", "again", "--idempotency-key", "submit-2"}, "")
	if code != ExitDenied {
		t.Fatalf("phase refusal exit = %d, want %d; output = %s", code, ExitDenied, raw)
	}
	env := decodeEnvelope(t, raw)
	if env.OK || env.Error == nil || env.Error.Code != protocol.CodeInvalidState ||
		!strings.Contains(env.Error.Message, "mission is in phase plan_review") {
		t.Fatalf("phase refusal envelope = %+v, want the server message verbatim", env)
	}
}

func TestCLISkillPhaseActionBoundaries(t *testing.T) {
	for _, phase := range []string{"planning", "clarified", "plan_review", "active", "amendment_review", "rejected"} {
		t.Run(phase, func(t *testing.T) {
			var out bytes.Buffer
			code, err := writeSkill(&out, &protocol.CoordStatusResult{
				RunID: "run-current",
				Assignment: &protocol.CoordMissionAssignment{
					Role: "integrator", MissionID: "mission-current", Phase: phase,
				},
			})
			if err != nil || code != ExitOK {
				t.Fatalf("skill = %d, %v", code, err)
			}
			raw := out.String()
			if !strings.Contains(raw, "Phase: "+phase+"\n") {
				t.Fatalf("skill lost current phase: %s", raw)
			}
			hasIntegration := strings.Contains(raw, "aether-internal integration ")
			if hasIntegration != (phase == "active" || phase == "amendment_review") {
				t.Fatalf("integration guidance in phase %s: %s", phase, raw)
			}
			if phase == "plan_review" || phase == "amendment_review" {
				if !strings.Contains(raw, "aether-internal mission plan show --wait 30\n") {
					t.Fatalf("review phase missing wait command: %s", raw)
				}
				for _, mutation := range []string{"task propose", "task revise", "task abandon", "mission plan submit"} {
					if strings.Contains(raw, "aether-internal "+mutation) {
						t.Fatalf("frozen phase advertises mutation %s: %s", mutation, raw)
					}
				}
			}
			if phase != "active" && strings.Contains(raw, "aether-internal worker start ") {
				t.Fatalf("phase %s advertises new dispatch: %s", phase, raw)
			}
		})
	}
}

func TestCLITaskAbandonCarriesTheOptionalRevision(t *testing.T) {
	var got []protocol.TaskAbandonParams
	s := newCLISocket(t, func(req protocol.Request) protocol.Response {
		if req.Method != protocol.MethodTaskAbandon {
			t.Fatalf("unexpected method %s", req.Method)
		}
		var p protocol.TaskAbandonParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			t.Fatalf("decode abandon: %v", err)
		}
		got = append(got, p)
		return protocol.Response{Result: json.RawMessage(`{"task":{"id":"task-1","mission_id":"mission-1","status":"abandoned","current_revision":1}}`)}
	})

	base := []string{"task", "abandon", "--task-id", "task-1", "--expected-integrator-generation", "4", "--idempotency-key", "abandon-1"}
	if code, raw := runCLI(t, s.path, base, ""); code != ExitOK {
		t.Fatalf("task abandon = code %d, envelope %s", code, raw)
	}
	if code, raw := runCLI(t, s.path, append(append([]string{}, base...), "--revision", "3"), ""); code != ExitOK {
		t.Fatalf("task abandon --revision = code %d, envelope %s", code, raw)
	}
	if len(got) != 2 {
		t.Fatalf("abandon calls = %d, want 2", len(got))
	}
	if got[0].Revision != 0 || got[0].TaskID != "task-1" || got[0].ExpectedIntegratorGeneration != 4 {
		t.Fatalf("whole-task abandon params = %+v, want revision 0", got[0])
	}
	if got[1].Revision != 3 {
		t.Fatalf("per-revision abandon params = %+v, want revision 3", got[1])
	}
}

func decodeResult(t *testing.T, raw string, out any) {
	t.Helper()
	env := decodeEnvelope(t, raw)
	if !env.OK {
		t.Fatalf("envelope = %+v", env)
	}
	data, err := json.Marshal(env.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if err := json.Unmarshal(data, out); err != nil {
		t.Fatalf("decode result: %v", err)
	}
}
