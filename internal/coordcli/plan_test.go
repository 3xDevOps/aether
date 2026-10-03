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
		{name: "start without mission", args: []string{"mission", "start", "--idempotency-key", "start-1"}},
		{name: "start without key", args: []string{"mission", "start", "--mission-id", "mission-1"}},
		{name: "start with positional argument", args: []string{"mission", "start", "--mission-id", "mission-1", "--idempotency-key", "start-1", "extra"}},
		{name: "question without subcommand", args: []string{"mission", "question"}},
		{name: "unknown question command", args: []string{"mission", "question", "answer"}},
		{name: "plan without subcommand", args: []string{"mission", "plan"}},
		{name: "unknown plan command", args: []string{"mission", "plan", "decide"}},
		{name: "ask unknown flag", args: []string{"mission", "question", "ask", "--unknown"}},
		{name: "ask without body", args: []string{"mission", "question", "ask", "--idempotency-key", "ask-1"}},
		{name: "ask without key", args: []string{"mission", "question", "ask", "--body", "which checkout flow?"}},
		{name: "ask with positional argument", args: []string{"mission", "question", "ask", "--body", "why?", "--idempotency-key", "ask-1", "extra"}},
		{name: "removed plan submit", args: []string{"mission", "plan", "submit", "--summary", "build it", "--idempotency-key", "submit-1"}},
		{name: "removed clarification", args: []string{"mission", "clarification", "complete", "--idempotency-key", "clarify-1"}},
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
		{"mission", "question", "ask", "--help"},
		{"mission", "plan", "show", "--help"},
		{"mission", "start", "--help"},
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
		if raw["run_id"] != nil || (req.Method != protocol.MethodMissionStart && raw["mission_id"] != nil) {
			t.Fatalf("%s carried a caller-supplied mission or run identity: %+v", req.Method, raw)
		}
		switch req.Method {
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
			return protocol.Response{Result: json.RawMessage(`{"plan":{"mission_id":"mission-1","phase":"planning","integrator_generation":1,"open_questions":1}}`)}
		case protocol.MethodMissionStart:
			var p protocol.MissionStartParams
			if err := json.Unmarshal(req.Params, &p); err != nil {
				t.Fatalf("decode start: %v", err)
			}
			if p.MissionID != "mission-1" || p.IdempotencyKey != "start-1" {
				t.Fatalf("start params = %+v", p)
			}
			return protocol.Response{Result: json.RawMessage(`{"plan":{"mission_id":"mission-1","phase":"active","integrator_generation":1}}`)}
		default:
			return protocol.Response{Error: &protocol.Error{Code: protocol.CodeMethodNotFound, Message: "unexpected " + req.Method}}
		}
	})

	code, raw := runCLI(t, s.path, []string{"mission", "question", "ask", "--body-file", "-", "--idempotency-key", "ask-1"}, "which checkout flow?")
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
	if show.Questions == nil {
		t.Fatalf("plan show result carried a null collection: %s", raw)
	}

	code, raw = runCLI(t, s.path, []string{"mission", "start", "--mission-id", "mission-1", "--idempotency-key", "start-1"}, "")
	if code != ExitOK {
		t.Fatalf("mission start = code %d, envelope %s", code, raw)
	}
	var started protocol.MissionStartResult
	decodeResult(t, raw, &started)
	if started.Plan.Phase != "active" {
		t.Fatalf("started plan = %+v", started.Plan)
	}
}

func TestCLIMissionPlanPhaseRefusalIsDenied(t *testing.T) {
	s := newCLISocket(t, func(protocol.Request) protocol.Response {
		return protocol.Response{Error: &protocol.Error{Code: protocol.CodeInvalidState, Message: "mission.start: mission is in phase active"}}
	})
	code, raw := runCLI(t, s.path, []string{"mission", "start", "--mission-id", "mission-1", "--idempotency-key", "start-2"}, "")
	if code != ExitDenied {
		t.Fatalf("phase refusal exit = %d, want %d; output = %s", code, ExitDenied, raw)
	}
	env := decodeEnvelope(t, raw)
	if env.OK || env.Error == nil || env.Error.Code != protocol.CodeInvalidState ||
		!strings.Contains(env.Error.Message, "mission is in phase active") {
		t.Fatalf("phase refusal envelope = %+v, want the server message verbatim", env)
	}
}

func TestCLISkillPhaseActionBoundaries(t *testing.T) {
	for _, phase := range []string{"planning", "active", "completed", "cancelled"} {
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
			if hasIntegration != (phase == "active") {
				t.Fatalf("integration guidance in phase %s: %s", phase, raw)
			}
			if phase != "active" && strings.Contains(raw, "aether-internal worker start ") {
				t.Fatalf("phase %s advertises new dispatch: %s", phase, raw)
			}
			if hasStart := strings.Contains(raw, "aether-internal mission start "); hasStart != (phase == "planning") {
				t.Fatalf("mission start guidance in phase %s: %s", phase, raw)
			}
			for _, removed := range []string{"plan submit", "clarification complete", "material", "amendment"} {
				if strings.Contains(raw, removed) {
					t.Fatalf("phase %s still mentions %q: %s", phase, removed, raw)
				}
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
