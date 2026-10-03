package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
)

// shownSwarm is what mission.show reports while the integrator plans.
var shownSwarm = protocol.MissionShowResult{
	Mission: protocol.Mission{
		ID: "m1", Phase: "planning", IntegratorGeneration: 2,
		Integrator: protocol.MissionIntegrator{AccountMemberID: "mem1", Harness: "claude", Mode: "tui"},
	},
	Questions: []protocol.MissionQuestion{{ID: "q1", Body: "Which branch?"}},
}

func TestSwarmGateRejectsBadInputBeforeAnyRPC(t *testing.T) {
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"removed approve":            {args: []string{"approve", "m1"}, want: `unknown swarm command "approve"`},
		"removed request-changes":    {args: []string{"request-changes", "m1", "split it"}, want: `unknown swarm command "request-changes"`},
		"removed reject":             {args: []string{"reject", "m1"}, want: `unknown swarm command "reject"`},
		"answer without question":    {args: []string{"answer", "m1", "main"}, want: "usage: aether swarm answer <mission-id> --question <question-id>"},
		"answer empty stdin":         {args: []string{"answer", "m1", "--question", "q1", "-"}, want: "answer on stdin is empty"},
		"cancel with extra argument": {args: []string{"cancel", "m1", "now"}, want: "usage: aether swarm cancel <mission-id>"},
		"replace without agent":      {args: []string{"replace-integrator", "m1"}, want: "usage: aether swarm replace-integrator <mission-id> --agent <harness>"},
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

func TestSwarmCancelPrintsPhaseOrRefusal(t *testing.T) {
	cancel := &fakeReply{result: protocol.MissionCancelResult{Mission: protocol.Mission{ID: "m1", Phase: "cancelled"}}}
	c := fakeControlMethods(t, map[string]*fakeReply{protocol.MethodMissionCancel: cancel})
	var out bytes.Buffer
	if err := cancelSwarm(c, &out, "m1", "key-1"); err != nil {
		t.Fatalf("cancelSwarm: %v", err)
	}
	if got, want := string(cancel.got), mustJSON(t, protocol.MissionCancelParams{MissionID: "m1", IdempotencyKey: "key-1"}); got != want {
		t.Errorf("mission.cancel params = %s, want %s", got, want)
	}
	if want := "swarm m1 cancelled\n"; out.String() != want {
		t.Errorf("output = %q, want %q", out.String(), want)
	}

	refused := &protocol.Error{Code: protocol.CodeInvalidState, Message: "store: mission phase forbids this operation: mission.cancel: mission is in phase completed; the mission is completed"}
	c = fakeControlMethods(t, map[string]*fakeReply{protocol.MethodMissionCancel: {err: refused}})
	err := cancelSwarm(c, &out, "m1", "key-2")
	if err == nil || err.Error() != "rpc error -32002: "+refused.Message {
		t.Errorf("error = %v, want the server refusal verbatim", err)
	}
}

func TestSwarmReplaceIntegratorSendsTheShownGeneration(t *testing.T) {
	replace := &fakeReply{result: protocol.MissionReplaceIntegratorResult{Mission: protocol.Mission{ID: "m1", Phase: "planning"}, RunID: "r9"}}
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
	if wantOut := "swarm m1 planning\nintegrator run r9\n"; out.String() != wantOut {
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
