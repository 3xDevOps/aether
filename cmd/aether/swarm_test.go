package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
)

// fakeReply is one served method: what it answers and the params of the last
// request it received.
type fakeReply struct {
	mu     sync.Mutex
	result any
	err    *protocol.Error
	got    json.RawMessage
}

func (r *fakeReply) answer(result any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.result = result
}

func (r *fakeReply) params() json.RawMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.got
}

// fakeControlMethods serves every method in replies; any other method is
// answered with a method-not-found error, so a test proves which RPCs ran.
func fakeControlMethods(t *testing.T, replies map[string]*fakeReply) *protocol.Client {
	t.Helper()
	server, client := net.Pipe()
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
			reply, served := replies[req.Method]
			if !served {
				resp.Error = &protocol.Error{Code: protocol.CodeMethodNotFound, Message: "unexpected method " + req.Method}
			} else {
				reply.mu.Lock()
				reply.got = append(json.RawMessage(nil), req.Params...)
				resp.Error = reply.err
				if reply.err == nil {
					resp.Result, _ = json.Marshal(reply.result)
				}
				reply.mu.Unlock()
			}
			out, _ := json.Marshal(resp)
			if _, err := server.Write(append(out, '\n')); err != nil {
				return
			}
		}
	}()
	c := protocol.NewClient(client)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestSwarmCreateSendsIntegratorTupleAndWorkers(t *testing.T) {
	spec, err := parseSwarmCreate([]string{
		"-", "--agent", "claude", "--model", "opus", "--worker", "codex:background:gpt-5-codex", "--worker", "claude",
		"--worker", "codex:background:gpt-5-codex", "--worker", "claude:standard:opus", "--worker", "codex:background", "--worker", "codex::gpt-5-codex",
		"--workspace", "ws-name", "--repo", "api", "--repo", "shared",
	}, strings.NewReader("ship the swarm CLI\n"))
	if err != nil {
		t.Fatalf("parseSwarmCreate: %v", err)
	}
	if strings.Join(spec.repos, ",") != "api,shared" {
		t.Errorf("repos = %q, want api and shared", spec.repos)
	}
	mission := protocol.Mission{ID: "01MISSION", Phase: "planning", CurrentIntegratorRunID: "01INTEGRATOR"}
	created := &fakeReply{result: protocol.MissionCreateResult{Mission: mission}}
	c := fakeControlMethods(t, map[string]*fakeReply{
		protocol.MethodAgentList: {result: protocol.AgentListResult{Agents: []protocol.AgentInfo{
			{Name: "claude", Enhanced: "adapter"}, {Name: "codex", Enhanced: "adapter", EnhancedInstalled: true},
		}}},
		protocol.MethodMissionCreate: created,
	})
	if err := fillSwarmModes(c, &spec); err != nil {
		t.Fatalf("fillSwarmModes: %v", err)
	}
	var out bytes.Buffer
	if err := createSwarm(c, &out, missionCreateParams("ws1", "mem1", spec, "key-1")); err != nil {
		t.Fatalf("createSwarm: %v", err)
	}
	var sent protocol.MissionCreateParams
	if err := json.Unmarshal(created.got, &sent); err != nil {
		t.Fatalf("decode sent params: %v", err)
	}
	want := protocol.MissionCreateParams{
		WorkspaceID: "ws1",
		Objective:   "ship the swarm CLI",
		Integrator:  protocol.MissionIntegrator{AccountMemberID: "mem1", Harness: "claude", Mode: "tui", Model: "opus"},
		ExecutionChoices: []protocol.MissionExecutionChoice{
			{AccountMemberID: "mem1", Harness: "claude", Mode: "tui", Model: "opus"},
			{AccountMemberID: "mem1", Harness: "codex", Mode: "headless", Model: "gpt-5-codex"},
			{AccountMemberID: "mem1", Harness: "claude", Mode: "tui"},
			{AccountMemberID: "mem1", Harness: "codex", Mode: "headless"},
			{AccountMemberID: "mem1", Harness: "codex", Mode: "acp", Model: "gpt-5-codex"},
		},
		IdempotencyKey: "key-1",
	}
	if sentJSON, wantJSON := mustJSON(t, sent), mustJSON(t, want); sentJSON != wantJSON {
		t.Errorf("mission.create params = %s, want %s", sentJSON, wantJSON)
	}
	if wantOut := "swarm 01MISSION planning\nintegrator run 01INTEGRATOR\n"; out.String() != wantOut {
		t.Errorf("output = %q, want %q", out.String(), wantOut)
	}
}

func TestSwarmCreateNamesTheInstallWhenNoDefaultModeCanWake(t *testing.T) {
	spec, err := parseSwarmCreate([]string{"objective", "--agent", "codex"}, strings.NewReader(""))
	if err != nil {
		t.Fatalf("parseSwarmCreate: %v", err)
	}
	c := fakeControlMethods(t, map[string]*fakeReply{protocol.MethodAgentList: {result: protocol.AgentListResult{Agents: []protocol.AgentInfo{
		{Name: "codex", Enhanced: "adapter", SwarmStandardRefusal: "codex in Standard mode has no idle wake; start it in Enhanced"},
	}}}})
	want := "codex in Standard mode has no idle wake; start it in Enhanced, which is not installed: aether agent add codex --enhanced"
	if err := fillSwarmModes(c, &spec); err == nil || err.Error() != want {
		t.Errorf("fillSwarmModes = %v, want %q", err, want)
	}
}

func TestSwarmCreateSendsFallbackPolicy(t *testing.T) {
	spec, err := parseSwarmCreate([]string{"objective", "--agent", "claude", "--fallback", "mem2, mem3"}, strings.NewReader(""))
	if err != nil {
		t.Fatalf("parseSwarmCreate: %v", err)
	}
	if params := missionCreateParams("ws1", "mem1", spec, "key-1"); strings.Join(params.FallbackAccountMemberIDs, ",") != "mem2,mem3" || params.NoFallback {
		t.Errorf("--fallback params = %+v, want ordered mem2,mem3", params)
	}
	spec, err = parseSwarmCreate([]string{"objective", "--agent", "claude", "--no-fallback"}, strings.NewReader(""))
	if err != nil {
		t.Fatalf("parseSwarmCreate: %v", err)
	}
	if params := missionCreateParams("ws1", "mem1", spec, "key-1"); !params.NoFallback || params.FallbackAccountMemberIDs != nil {
		t.Errorf("--no-fallback params = %+v, want no_fallback only", params)
	}
}

func TestSwarmCreateReusesAnIdempotencyKey(t *testing.T) {
	spec, err := parseSwarmCreate([]string{"objective", "--agent", "claude", "--idempotency-key", "k-retry"}, strings.NewReader(""))
	if err != nil || spec.key != "k-retry" {
		t.Fatalf("parseSwarmCreate --idempotency-key = %q, %v; want k-retry", spec.key, err)
	}
}

func TestSwarmCreateEnhancedIntegrator(t *testing.T) {
	spec, err := parseSwarmCreate([]string{"objective", "--agent", "codex", "--mode", "enhanced"}, strings.NewReader(""))
	if err != nil {
		t.Fatalf("parseSwarmCreate: %v", err)
	}
	params := missionCreateParams("ws1", "mem1", spec, "key-1")
	want := protocol.MissionIntegrator{AccountMemberID: "mem1", Harness: "codex", Mode: "acp"}
	if params.Integrator != want || len(params.ExecutionChoices) != 1 || protocol.MissionIntegrator(params.ExecutionChoices[0]) != want {
		t.Fatalf("mission.create params %+v, want an acp integrator among the choices", params)
	}
}

func TestSwarmCreateRejectsBadInputBeforeAnyRPC(t *testing.T) {
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"no agent":              {args: []string{"objective"}, want: "usage: aether swarm create"},
		"bad worker mode":       {args: []string{"objective", "--agent", "claude", "--worker", "codex:headless"}, want: `invalid --worker "codex:headless"`},
		"empty worker":          {args: []string{"objective", "--agent", "claude", "--worker", ":standard"}, want: `invalid --worker ":standard"`},
		"bad worker model":      {args: []string{"objective", "--agent", "claude", "--worker", "codex:background:bad model"}, want: `invalid --worker "codex:background:bad model"`},
		"bad integrator model":  {args: []string{"objective", "--agent", "claude", "--model", "-opus"}, want: "invalid --model"},
		"background integrator": {args: []string{"objective", "--agent", "claude", "--mode", "background"}, want: "--mode must be standard or enhanced"},
		"fallback and none":     {args: []string{"objective", "--agent", "claude", "--fallback", "mem2", "--no-fallback"}, want: "--fallback and --no-fallback cannot be used together"},
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
	res := protocol.MissionShowResult{
		Mission: protocol.Mission{
			ID: "m1", Phase: "active", Objective: "ship the swarm CLI", AccountableHumanID: "mem1",
			Integrator:             protocol.MissionIntegrator{AccountMemberID: "mem1", Harness: "claude", Mode: "tui"},
			CurrentIntegratorRunID: "r1", IntegratorGeneration: 2, OpenQuestions: 1,
			IntegratorLaunchError: "image missing", IntegratorLaunchErrorAt: "2026-09-25T07:01:00Z",
			ExtraWorkspaceIDs: []string{"ws-api", "ws-shared"},
		},
		Questions: []protocol.MissionQuestion{
			{ID: "q1", Body: "Which harness?", Answer: "claude", AnsweredAt: &answered},
			{ID: "q2", Body: "Which branch?"},
		},
		Tasks: []protocol.Task{
			{ID: "t1", Status: "working", Revision: &protocol.TaskRevision{Title: "Add the command"}, PendingRevision: &protocol.TaskRevision{Revision: 2, Title: "Add the\tswarm command"}},
			{ID: "t2", Status: "proposed", PendingRevision: &protocol.TaskRevision{Title: "Write the docs"},
				Blockers: []protocol.TaskBlocker{{Kind: "proposal", TaskID: "t2"}, {Kind: "dependency", TaskID: "t1"}}},
		},
		Attempts: []protocol.Attempt{
			{ID: "a1", TaskID: "t1", State: "running", RunID: "r2", AccountOwnerID: "mem1", LaunchAccountID: "mem1"},
			{ID: "a2", TaskID: "t2", State: "running", RunID: "r3", AccountOwnerID: "mem1", LaunchAccountID: "mem2"},
		},
	}
	var buf bytes.Buffer
	if err := renderSwarm(&buf, res); err != nil {
		t.Fatalf("renderSwarm: %v", err)
	}
	want := `swarm m1 active
objective: ship the swarm CLI
extra repos: ws-api, ws-shared
accountable human: mem1
integrator: run r1 generation 2 (claude standard, account mem1)
launch error: image missing (since 2026-09-25T07:01:00Z)

questions (1 open):
  q1 Which harness?
    answer: claude
  q2 Which branch?
    unanswered

tasks:
ID  TITLE                                                   STATUS    BLOCKERS
t1  Add the command (pending rev 2: Add the swarm command)  working   
t2  Write the docs                                          proposed  proposal t2, dependency t1

attempts:
ID  TASK  STATE    RUN  ACCOUNT
a1  t1    running  r2   mem1
a2  t2    running  r3   mem2 (fallback)
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
