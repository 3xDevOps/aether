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

func TestCLIMissionPlanUsageTextNamesRequiredFlags(t *testing.T) {
	_, raw := runCLI(t, "", []string{"mission", "plan", "submit", "--summary", "build it"}, "")
	if got := decodeEnvelope(t, raw).Error.Message; got != "mission plan submit requires --summary or --summary-file and --idempotency-key" {
		t.Fatalf("submit usage message = %q", got)
	}
	_, raw = runCLI(t, "", []string{"mission", "question", "ask", "--body", "why?"}, "")
	if got := decodeEnvelope(t, raw).Error.Message; got != "mission question ask requires --body or --body-file and --idempotency-key" {
		t.Fatalf("ask usage message = %q", got)
	}
	_, raw = runCLI(t, "", []string{"mission", "clarification", "complete"}, "")
	if got := decodeEnvelope(t, raw).Error.Message; got != "mission clarification complete requires --idempotency-key" {
		t.Fatalf("clarification usage message = %q", got)
	}
	_, raw = runCLI(t, "", []string{"mission", "plan", "show", "--wait", "31"}, "")
	if got := decodeEnvelope(t, raw).Error.Message; got != "mission plan show: --wait must be between 0 and 30" {
		t.Fatalf("show usage message = %q", got)
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
	if !strings.Contains(commandUsages["mission"], "asks the accountable human, who answers in the dashboard") ||
		!strings.Contains(commandUsages["mission"], "separate mailboxes") {
		t.Fatalf("mission usage does not distinguish the human mailbox from peer ask: %q", commandUsages["mission"])
	}
	if !strings.Contains(commandUsages["mission"], "<clarification|question|plan>") {
		t.Fatalf("mission usage omits the clarification group: %q", commandUsages["mission"])
	}
	if !strings.Contains(commandUsages["mission plan submit"], "clarification must be complete") {
		t.Fatalf("submit usage omits the clarification precondition: %q", commandUsages["mission plan submit"])
	}
	if !strings.Contains(commandUsages["task abandon"], "[--revision <n>]") ||
		!strings.Contains(commandUsages["task abandon"], "drops only that pending revision") {
		t.Fatalf("task abandon usage omits the per-revision form: %q", commandUsages["task abandon"])
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
				"  1. Decide whether the objective is specified well enough to plan. If not,\n     ask the accountable human; this asks the human, not a peer run:\n       aether-internal mission question ask --body 'question' --idempotency-key <key>",
				"  2. When you have what you need (questions are optional), say so:\n       aether-internal mission clarification complete --idempotency-key <key>\n     This is refused while a question you asked is unanswered.",
				"     Declare expected_paths for every task: after approval, work outside\n     them needs a human-approved amendment.",
				"aether-internal mission plan show --wait 30",
				"aether-internal mission plan submit --summary 'what will be built and why' --idempotency-key <key>",
				"No worker can start until a human approves the plan.",
			},
			absent: []string{"Latest feedback", "human-decision boundary", "at least one question"},
		},
		{
			name:       "planning after revise",
			assignment: `"phase":"planning","plan_version":1,"open_questions":0,"latest_feedback":"split the migration out"`,
			want: []string{
				"Phase: planning\nPlan version: 1\nOpen questions: 0\nLatest feedback: split the migration out\nPlanning flow:",
			},
		},
		{
			name:       "clarified",
			assignment: `"phase":"clarified","plan_version":0`,
			want: []string{
				"Phase: clarified\nPlan version: 0\nClarification is complete. Propose or revise tasks, then submit the plan:\n",
				"  aether-internal task list --mission-id <mission-id>\n  aether-internal task propose --mission-id <mission-id> --idempotency-key <key> --revision-file /tmp/aether-task.json\n  aether-internal mission plan submit --summary 'what will be built and why' --idempotency-key <key>\n",
				"Declare expected_paths for every task. No worker can start until a human\napproves the plan.\n",
			},
			absent: []string{"Planning flow:", "Latest feedback", "human-decision boundary"},
		},
		{
			name:       "clarified after revise",
			assignment: `"phase":"clarified","plan_version":1,"latest_feedback":"split the migration out"`,
			want:       []string{"Phase: clarified\nPlan version: 1\nLatest feedback: split the migration out\nClarification is complete."},
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
				"Phase: active\nPlan version: 3 (approved)\nChanging the approved plan:\n",
				"  A revision you accept yourself must stay within the approved plan: not\n  marked \"material\": true, and expected_paths inside the approved paths. The\n  server refuses task accept otherwise.",
				"  A material change - new scope, new subsystems, more work, a changed\n  constraint or success criterion - is proposed with \"material\": true, then\n  submitted for human approval:\n    aether-internal mission plan submit --summary 'what changes and why' --idempotency-key <key>",
				"  revise that task. Drop a pending revision with\n    aether-internal task abandon --task-id <task> --revision <n> --expected-integrator-generation <g> --idempotency-key <key>\n",
				"  Drop a whole task you proposed this round with the same command and no\n  --revision:\n    aether-internal task abandon --task-id <task> --expected-integrator-generation <g> --idempotency-key <key>\n",
				"Integrator candidate flow:",
				"human-decision boundary",
			},
			absent: []string{"Planning flow:", "A human is reviewing the plan."},
		},
		{
			name:       "amendment review",
			assignment: `"phase":"amendment_review","plan_version":4,"latest_feedback":"narrow the migration"`,
			want: []string{
				"Phase: amendment_review\nPlan version: 4\nLatest feedback: narrow the migration\n",
				"A human is reviewing the amendment. Approved work continues: you may accept\nsubmissions, inspect, cancel, and retry workers on approved tasks. Do not\npropose, revise, abandon, or accept task revisions; the server refuses them.\n",
				"Wait for the decision by repeating this call until the phase changes:\n  aether-internal mission plan show --wait 30\n",
				"Waiting is not being blocked and is not an outcome; do not run\naether-internal report. When the phase changes, run aether-internal skill\nagain.\n",
				"Integrator candidate flow:",
			},
			absent: []string{"Planning flow:", "A human is reviewing the plan.", "Changing the approved plan:"},
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
