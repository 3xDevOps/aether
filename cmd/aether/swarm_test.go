package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
)

// fakeControl serves one JSON-RPC method over a pipe and records the params
// it received, standing in for aether-server behind withControl.
func fakeControl(t *testing.T, method string, result any, rpcErr *protocol.Error) (*protocol.Client, *json.RawMessage) {
	t.Helper()
	server, client := net.Pipe()
	var got json.RawMessage
	go func() {
		defer server.Close() //nolint:errcheck
		r := bufio.NewReader(server)
		for {
			line, err := protocol.ReadLine(r)
			if err != nil {
				return
			}
			var req protocol.Request
			if err := json.Unmarshal(line, &req); err != nil {
				return
			}
			resp := protocol.Response{JSONRPC: "2.0", ID: req.ID}
			switch {
			case req.Method != method:
				resp.Error = &protocol.Error{Code: protocol.CodeMethodNotFound, Message: "unexpected method " + req.Method}
			case rpcErr != nil:
				got = append(json.RawMessage(nil), req.Params...)
				resp.Error = rpcErr
			default:
				got = append(json.RawMessage(nil), req.Params...)
				resp.Result, _ = json.Marshal(result)
			}
			out, _ := json.Marshal(resp)
			if _, err := server.Write(append(out, '\n')); err != nil {
				return
			}
		}
	}()
	c := protocol.NewClient(client)
	t.Cleanup(func() { _ = c.Close() })
	return c, &got
}

func TestSwarmCreateSendsIntegratorTupleAndWorkers(t *testing.T) {
	spec, err := parseSwarmCreate([]string{
		"-", "--agent", "claude", "--worker", "codex:headless", "--worker", "claude", "--worker", "codex:headless",
		"--max-concurrent", "3", "--max-attempts", "12", "--workspace", "ws-name",
	}, strings.NewReader("ship the swarm CLI\n"))
	if err != nil {
		t.Fatalf("parseSwarmCreate: %v", err)
	}
	mission := protocol.Mission{ID: "01MISSION", Phase: "planning", CurrentIntegratorRunID: "01INTEGRATOR"}
	c, got := fakeControl(t, protocol.MethodMissionCreate, protocol.MissionCreateResult{Mission: mission}, nil)
	var out bytes.Buffer
	if err := createSwarm(c, &out, missionCreateParams("ws1", "mem1", spec, "key-1")); err != nil {
		t.Fatalf("createSwarm: %v", err)
	}
	var sent protocol.MissionCreateParams
	if err := json.Unmarshal(*got, &sent); err != nil {
		t.Fatalf("decode sent params: %v", err)
	}
	want := protocol.MissionCreateParams{
		WorkspaceID: "ws1",
		Objective:   "ship the swarm CLI",
		Integrator:  protocol.MissionIntegrator{AccountMemberID: "mem1", Harness: "claude", Mode: "tui"},
		ExecutionChoices: []protocol.MissionExecutionChoice{
			{AccountMemberID: "mem1", Harness: "claude", Mode: "tui"},
			{AccountMemberID: "mem1", Harness: "codex", Mode: "headless"},
		},
		MaxConcurrentAttempts: 3,
		MaxTotalAttempts:      12,
		IdempotencyKey:        "key-1",
	}
	if sentJSON, wantJSON := mustJSON(t, sent), mustJSON(t, want); sentJSON != wantJSON {
		t.Errorf("mission.create params = %s, want %s", sentJSON, wantJSON)
	}
	if wantOut := "swarm 01MISSION planning\nintegrator run 01INTEGRATOR\n"; out.String() != wantOut {
		t.Errorf("output = %q, want %q", out.String(), wantOut)
	}
}

func TestSwarmCreateRejectsBadInputBeforeAnyRPC(t *testing.T) {
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"no agent":               {args: []string{"objective"}, want: "usage: aether swarm create"},
		"bad worker mode":        {args: []string{"objective", "--agent", "claude", "--worker", "codex:batch"}, want: `invalid --worker "codex:batch"`},
		"empty worker":           {args: []string{"objective", "--agent", "claude", "--worker", ":tui"}, want: `invalid --worker ":tui"`},
		"concurrent too big":     {args: []string{"objective", "--agent", "claude", "--max-concurrent", "9"}, want: "--max-concurrent 9 is out of range (want 1..8)"},
		"total too big":          {args: []string{"objective", "--agent", "claude", "--max-attempts", "129"}, want: "--max-attempts 129 is out of range (want 1..128)"},
		"total below concurrent": {args: []string{"objective", "--agent", "claude", "--max-concurrent", "4", "--max-attempts", "2"}, want: "--max-attempts 2 is below --max-concurrent 4"},
	} {
		t.Run(name, func(t *testing.T) {
			// swarmCreate reaches withControl only after validation, and no
			// control channel exists here, so a usage error proves no RPC ran.
			err := swarmCreate(tc.args, strings.NewReader(""))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestRenderSwarms(t *testing.T) {
	missions := []protocol.Mission{
		{ID: "m1", Phase: "active", Objective: "short objective\nsecond line", CurrentIntegratorRunID: "r1", UpdatedAt: "2026-09-25T07:00:00Z"},
		{ID: "m2", Phase: "planning", Objective: strings.Repeat("x", 70), UpdatedAt: "2026-09-25T08:00:00Z"},
	}
	var buf bytes.Buffer
	if err := renderSwarms(&buf, missions); err != nil {
		t.Fatalf("renderSwarms: %v", err)
	}
	want := "ID  PHASE     OBJECTIVE                                                     INTEGRATOR  UPDATED\n" +
		"m1  active    short objective ...                                           r1          2026-09-25T07:00:00Z\n" +
		"m2  planning  " + strings.Repeat("x", 57) + "...              2026-09-25T08:00:00Z\n"
	if got := buf.String(); got != want {
		t.Errorf("renderSwarms() =\n%s\nwant\n%s", got, want)
	}
}

func TestRenderSwarm(t *testing.T) {
	answered := "2026-09-25T07:05:00Z"
	decided := "2026-09-25T07:20:00Z"
	res := protocol.MissionShowResult{
		Mission: protocol.Mission{
			ID: "m1", Phase: "active", Objective: "ship the swarm CLI", AccountableHumanID: "mem1",
			Integrator:             protocol.MissionIntegrator{AccountMemberID: "mem1", Harness: "claude", Mode: "tui"},
			CurrentIntegratorRunID: "r1", IntegratorGeneration: 2, PlanVersion: 1, OpenQuestions: 1,
			IntegratorLaunchError: "image missing", IntegratorLaunchErrorAt: "2026-09-25T07:01:00Z",
		},
		Questions: []protocol.MissionQuestion{
			{ID: "q1", Body: "Which harness?", Answer: "claude", AnsweredAt: &answered},
			{ID: "q2", Body: "Which branch?"},
		},
		PlanReviews: []protocol.MissionPlanReview{
			{PlanVersion: 1, Summary: "two tasks", SubmittedPhase: "clarified", SubmittedAt: "2026-09-25T07:10:00Z", Decision: "revise", Feedback: "split the docs", DecidedAt: &decided},
			{PlanVersion: 2, Summary: "three tasks", SubmittedPhase: "clarified", SubmittedAt: "2026-09-25T07:30:00Z"},
		},
		Tasks: []protocol.Task{
			{ID: "t1", Status: "working", Revision: &protocol.TaskRevision{Title: "Add the command"}},
			{ID: "t2", Status: "proposed", PendingRevision: &protocol.TaskRevision{Title: "Write the docs"},
				Blockers: []protocol.TaskBlocker{{Kind: "proposal", TaskID: "t2"}, {Kind: "dependency", TaskID: "t1"}}},
		},
		Attempts: []protocol.Attempt{{ID: "a1", TaskID: "t1", State: "running", RunID: "r2"}},
	}
	var buf bytes.Buffer
	if err := renderSwarm(&buf, res); err != nil {
		t.Fatalf("renderSwarm: %v", err)
	}
	want := `swarm m1 active
objective: ship the swarm CLI
accountable human: mem1
integrator: run r1 generation 2 (claude tui, account mem1)
launch error: image missing (since 2026-09-25T07:01:00Z)
plan version: 1

questions (1 open):
  q1 Which harness?
    answer: claude
  q2 Which branch?
    unanswered

plan reviews:
  v1 submitted from clarified at 2026-09-25T07:10:00Z: revise
    summary: two tasks
    feedback: split the docs
  v2 submitted from clarified at 2026-09-25T07:30:00Z: undecided
    summary: three tasks

tasks:
ID  TITLE            STATUS    BLOCKERS
t1  Add the command  working   
t2  Write the docs   proposed  proposal t2, dependency t1

attempts:
ID  TASK  STATE    RUN
a1  t1    running  r2
`
	if got := buf.String(); got != want {
		t.Errorf("renderSwarm() =\n%s\nwant\n%s", got, want)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
