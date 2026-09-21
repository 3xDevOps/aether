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

func TestCLIMissionPlanUsageTextNamesRequiredFlags(t *testing.T) {
	_, raw := runCLI(t, "", []string{"mission", "plan", "submit", "--summary", "build it"}, "")
	if got := decodeEnvelope(t, raw).Error.Message; got != "mission plan submit requires --summary or --summary-file and --idempotency-key" {
		t.Fatalf("submit usage message = %q", got)
	}
	_, raw = runCLI(t, "", []string{"mission", "question", "ask", "--body", "why?"}, "")
	if got := decodeEnvelope(t, raw).Error.Message; got != "mission question ask requires --body or --body-file and --idempotency-key" {
		t.Fatalf("ask usage message = %q", got)
	}
	_, raw = runCLI(t, "", []string{"mission", "plan", "show", "--wait", "31"}, "")
	if got := decodeEnvelope(t, raw).Error.Message; got != "mission plan show: --wait must be between 0 and 30" {
		t.Fatalf("show usage message = %q", got)
	}
}

func TestCLIMissionPlanHelpIsSocketIndependent(t *testing.T) {
	for _, args := range [][]string{
		{"mission", "--help"},
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
	if !strings.Contains(commandUsages["mission"], "asks the accountable human, who answers in the dashboard") ||
		!strings.Contains(commandUsages["mission"], "separate mailboxes") {
		t.Fatalf("mission usage does not distinguish the human mailbox from peer ask: %q", commandUsages["mission"])
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

func TestCLISkillPhaseText(t *testing.T) {
	tests := []struct {
		name       string
		assignment string
		want       []string
		absent     []string
	}{
		{
			name:       "planning",
			assignment: `"phase":"planning","plan_version":0,"open_questions":2`,
			want: []string{
				"Phase: planning\nPlan version: 0\nOpen questions: 2\n",
				"aether-internal mission question ask --body 'question' --idempotency-key <key>",
				"aether-internal mission plan show --wait 30",
				"aether-internal mission plan submit --summary 'what will be built and why' --idempotency-key <key>",
				"No worker can start until a human approves the plan.",
			},
			absent: []string{"Latest feedback", "human-decision boundary"},
		},
		{
			name:       "planning after revise",
			assignment: `"phase":"planning","plan_version":1,"open_questions":0,"latest_feedback":"split the migration out"`,
			want: []string{
				"Phase: planning\nPlan version: 1\nOpen questions: 0\nLatest feedback: split the migration out\n",
				"Revise an existing task rather than proposing a duplicate",
			},
		},
		{
			name:       "plan review",
			assignment: `"phase":"plan_review","plan_version":2`,
			want: []string{
				"Phase: plan_review\nPlan version: 2\nA human is reviewing the plan.",
				"Wait for the decision by repeating this call until the phase changes:\n  aether-internal mission plan show --wait 30",
				"do not run\naether-internal report",
			},
			absent: []string{"Planning flow:", "human-decision boundary"},
		},
		{
			name:       "active",
			assignment: `"phase":"active","plan_version":3`,
			want: []string{
				"Phase: active\nPlan version: 3 (approved)\n",
				"Integrator candidate flow:",
				"human-decision boundary",
			},
			absent: []string{"Planning flow:", "A human is reviewing the plan."},
		},
		{
			name:       "rejected",
			assignment: `"phase":"rejected","plan_version":1`,
			want: []string{
				"Phase: rejected\nA human rejected the plan and this run is being cancelled.",
				"the cancellation is the\nterminal event.",
			},
			absent: []string{"Plan version:", "Planning flow:", "Integrator candidate flow:"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := newCLISocket(t, func(protocol.Request) protocol.Response {
				return protocol.Response{Result: json.RawMessage(`{"wire_version":"v3","run_id":"run-1","workspace_id":"ws-1","member_id":"member-1","task":"ship the release","assignment":{"mission_id":"mission-1","role":"integrator","integrator_generation":1,` + test.assignment + `},"peers":[],"unread":0,"capabilities":[]}`)}
			})
			code, raw := runCLI(t, s.path, []string{"skill"}, "")
			if code != ExitOK {
				t.Fatalf("skill exit = %d, want %d", code, ExitOK)
			}
			for _, want := range test.want {
				if !strings.Contains(raw, want) {
					t.Fatalf("skill output for %s omits %q:\n%s", test.name, want, raw)
				}
			}
			for _, absent := range test.absent {
				if strings.Contains(raw, absent) {
					t.Fatalf("skill output for %s contains %q:\n%s", test.name, absent, raw)
				}
			}
		})
	}
}

func TestCLISkillWorkerHasNoPhaseText(t *testing.T) {
	s := newCLISocket(t, func(protocol.Request) protocol.Response {
		return protocol.Response{Result: json.RawMessage(`{"wire_version":"v3","run_id":"run-worker","workspace_id":"ws-1","member_id":"member-2","task":"implement","assignment":{"mission_id":"mission-1","role":"worker","task_id":"task-1","task_revision":2,"attempt_id":"attempt-1","phase":"active","plan_version":3},"peers":[],"unread":0,"capabilities":[]}`)}
	})
	code, raw := runCLI(t, s.path, []string{"skill"}, "")
	if code != ExitOK {
		t.Fatalf("skill exit = %d, want %d", code, ExitOK)
	}
	if strings.Contains(raw, "Phase:") || strings.Contains(raw, "Planning flow:") {
		t.Fatalf("worker skill printed integrator plan-gate text: %q", raw)
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
