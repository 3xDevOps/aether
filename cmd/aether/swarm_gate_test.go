package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
)

// shownSwarm is what mission.show reports while a plan waits for a decision.
var shownSwarm = protocol.MissionShowResult{
	Mission: protocol.Mission{
		ID: "m1", Phase: "plan_review", PlanVersion: 3, IntegratorGeneration: 2,
		Integrator: protocol.MissionIntegrator{AccountMemberID: "mem1", Harness: "claude", Mode: "tui"},
	},
	Questions: []protocol.MissionQuestion{{ID: "q1", Body: "Which branch?"}},
}

func TestSwarmDecisionsSendTheShownPlanVersion(t *testing.T) {
	for name, tc := range map[string]struct {
		args     []string
		decision string
		feedback string
		phase    string
	}{
		"approve":         {args: []string{"m1"}, decision: "approve", phase: "active"},
		"request-changes": {args: []string{"m1", "split the docs"}, decision: "revise", feedback: "split the docs", phase: "planning"},
		"reject":          {args: []string{"m1", "wrong repository"}, decision: "reject", feedback: "wrong repository", phase: "rejected"},
		"reject silently": {args: []string{"m1"}, decision: "reject", phase: "rejected"},
	} {
		t.Run(name, func(t *testing.T) {
			missionID, feedback, err := parseSwarmDecision(tc.decision, tc.args)
			if err != nil {
				t.Fatalf("parseSwarmDecision: %v", err)
			}
			decide := &fakeReply{result: protocol.MissionPlanDecideResult{Mission: protocol.Mission{ID: "m1", Phase: tc.phase}}}
			c := fakeControlMethods(t, map[string]*fakeReply{
				protocol.MethodMissionShow:       {result: shownSwarm},
				protocol.MethodMissionPlanDecide: decide,
			})
			var out bytes.Buffer
			if err := decideSwarmPlan(c, &out, missionID, tc.decision, feedback, "key-1"); err != nil {
				t.Fatalf("decideSwarmPlan: %v", err)
			}
			want := protocol.MissionPlanDecideParams{MissionID: "m1", ExpectedPlanVersion: 3, Decision: tc.decision, Feedback: tc.feedback, IdempotencyKey: "key-1"}
			if got, wantJSON := string(decide.got), mustJSON(t, want); got != wantJSON {
				t.Errorf("mission.plan.decide params = %s, want %s", got, wantJSON)
			}
			if wantOut := "swarm m1 " + tc.phase + "\n"; out.String() != wantOut {
				t.Errorf("output = %q, want %q", out.String(), wantOut)
			}
		})
	}
}

func TestSwarmGateRejectsBadInputBeforeAnyRPC(t *testing.T) {
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"request-changes without feedback": {args: []string{"request-changes", "m1"}, want: `usage: aether swarm request-changes <mission-id> "<feedback>"`},
		"request-changes blank feedback":   {args: []string{"request-changes", "m1", " "}, want: `usage: aether swarm request-changes <mission-id> "<feedback>"`},
		"approve with feedback":            {args: []string{"approve", "m1", "looks good"}, want: "usage: aether swarm approve <mission-id>"},
		"approve without mission":          {args: []string{"approve"}, want: "usage: aether swarm approve <mission-id>"},
		"answer without question":          {args: []string{"answer", "m1", "main"}, want: "usage: aether swarm answer <mission-id> --question <question-id>"},
		"answer empty stdin":               {args: []string{"answer", "m1", "--question", "q1", "-"}, want: "answer on stdin is empty"},
		"cancel with extra argument":       {args: []string{"cancel", "m1", "now"}, want: "usage: aether swarm cancel <mission-id>"},
		"replace without agent":            {args: []string{"replace-integrator", "m1"}, want: "usage: aether swarm replace-integrator <mission-id> --agent <harness>"},
	} {
		t.Run(name, func(t *testing.T) {
			// Every gate command reaches withControl only after validation,
			// and no control channel exists here, so a usage error proves
			// no RPC ran.
			err := runSwarm(tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestSwarmDecisionWithoutPlanStopsBeforeDeciding(t *testing.T) {
	c := fakeControlMethods(t, map[string]*fakeReply{
		protocol.MethodMissionShow: {result: protocol.MissionShowResult{Mission: protocol.Mission{ID: "m1", Phase: "planning"}}},
	})
	err := decideSwarmPlan(c, &bytes.Buffer{}, "m1", "approve", "", "key-1")
	if want := "swarm m1 has no submitted plan to decide (phase planning)"; err == nil || err.Error() != want {
		t.Errorf("error = %v, want %q", err, want)
	}
}

func TestSwarmGatePrintsServerRefusalVerbatim(t *testing.T) {
	refused := &protocol.Error{Code: protocol.CodeInvalidState, Message: "reject is refused on an amendment: request changes and let the integrator drop it"}
	c := fakeControlMethods(t, map[string]*fakeReply{
		protocol.MethodMissionShow:       {result: shownSwarm},
		protocol.MethodMissionPlanDecide: {err: refused},
	})
	var out bytes.Buffer
	err := decideSwarmPlan(c, &out, "m1", "reject", "", "key-1")
	var rpcErr *protocol.Error
	if !errors.As(err, &rpcErr) || rpcErr.Message != refused.Message {
		t.Fatalf("error = %v, want the server refusal %q", err, refused.Message)
	}
	// main prints err verbatim after "aether:" and exits 1 for any error.
	if want := "rpc error -32002: " + refused.Message; err.Error() != want {
		t.Errorf("error text = %q, want %q", err.Error(), want)
	}
	if out.Len() != 0 {
		t.Errorf("output = %q, want none", out.String())
	}
}

func TestSwarmCancelPrintsPhaseOrRefusal(t *testing.T) {
	cancel := &fakeReply{result: protocol.MissionCancelResult{Mission: protocol.Mission{ID: "m1", Phase: "rejected"}}}
	c := fakeControlMethods(t, map[string]*fakeReply{protocol.MethodMissionCancel: cancel})
	var out bytes.Buffer
	if err := cancelSwarm(c, &out, "m1", "key-1"); err != nil {
		t.Fatalf("cancelSwarm: %v", err)
	}
	if got, want := string(cancel.got), mustJSON(t, protocol.MissionCancelParams{MissionID: "m1", IdempotencyKey: "key-1"}); got != want {
		t.Errorf("mission.cancel params = %s, want %s", got, want)
	}
	if want := "swarm m1 rejected\n"; out.String() != want {
		t.Errorf("output = %q, want %q", out.String(), want)
	}

	refused := &protocol.Error{Code: protocol.CodeInvalidState, Message: "mission m1 is active: cancel is refused once a plan is approved"}
	c = fakeControlMethods(t, map[string]*fakeReply{protocol.MethodMissionCancel: {err: refused}})
	err := cancelSwarm(c, &out, "m1", "key-2")
	if err == nil || err.Error() != "rpc error -32002: "+refused.Message {
		t.Errorf("error = %v, want the server refusal verbatim", err)
	}
}

func TestSwarmReplaceIntegratorSendsTheShownGeneration(t *testing.T) {
	replace := &fakeReply{result: protocol.MissionReplaceIntegratorResult{Mission: protocol.Mission{ID: "m1", Phase: "plan_review"}, RunID: "r9"}}
	c := fakeControlMethods(t, map[string]*fakeReply{
		protocol.MethodMissionShow:              {result: shownSwarm},
		protocol.MethodMissionReplaceIntegrator: replace,
	})
	var out bytes.Buffer
	if err := replaceSwarmIntegrator(c, &out, "m1", "codex", "", "key-1"); err != nil {
		t.Fatalf("replaceSwarmIntegrator: %v", err)
	}
	want := protocol.MissionReplaceIntegratorParams{
		MissionID: "m1", ExpectedGeneration: 2,
		Integrator:     protocol.MissionIntegrator{AccountMemberID: "mem1", Harness: "codex", Mode: "tui"},
		IdempotencyKey: "key-1",
	}
	if got, wantJSON := string(replace.got), mustJSON(t, want); got != wantJSON {
		t.Errorf("mission.replace-integrator params = %s, want %s", got, wantJSON)
	}
	if wantOut := "swarm m1 plan_review\nintegrator run r9\n"; out.String() != wantOut {
		t.Errorf("output = %q, want %q", out.String(), wantOut)
	}

	if err := replaceSwarmIntegrator(c, &out, "m1", "codex", "mem2", "key-2"); err != nil {
		t.Fatalf("replaceSwarmIntegrator with --account: %v", err)
	}
	var sent protocol.MissionReplaceIntegratorParams
	if err := json.Unmarshal(replace.got, &sent); err != nil {
		t.Fatal(err)
	}
	if sent.Integrator.AccountMemberID != "mem2" {
		t.Errorf("--account sent %q, want mem2", sent.Integrator.AccountMemberID)
	}
}

func TestSwarmAnswerSendsStdinAnswerForAShownQuestion(t *testing.T) {
	missionID, questionID, answer, err := parseSwarmAnswer([]string{"m1", "--question", "q1", "-"}, strings.NewReader("main, not a release branch\n"))
	if err != nil {
		t.Fatalf("parseSwarmAnswer: %v", err)
	}
	reply := &fakeReply{result: protocol.MissionQuestionResult{Question: protocol.MissionQuestion{ID: "q1", Answer: answer}}}
	c := fakeControlMethods(t, map[string]*fakeReply{
		protocol.MethodMissionShow:           {result: shownSwarm},
		protocol.MethodMissionQuestionAnswer: reply,
	})
	var out bytes.Buffer
	if err = answerSwarmQuestion(c, &out, missionID, questionID, answer, "key-1"); err != nil {
		t.Fatalf("answerSwarmQuestion: %v", err)
	}
	want := protocol.MissionQuestionAnswerParams{QuestionID: "q1", Answer: "main, not a release branch", IdempotencyKey: "key-1"}
	if got, wantJSON := string(reply.got), mustJSON(t, want); got != wantJSON {
		t.Errorf("mission.question.answer params = %s, want %s", got, wantJSON)
	}
	if wantOut := "question q1 answered\n"; out.String() != wantOut {
		t.Errorf("output = %q, want %q", out.String(), wantOut)
	}

	// A question of another swarm is refused before the answer RPC; the
	// fake would have answered it.
	err = answerSwarmQuestion(c, &out, "m1", "q-other", "main", "key-2")
	if want := "swarm m1 has no question q-other"; err == nil || err.Error() != want {
		t.Errorf("error = %v, want %q", err, want)
	}
}
