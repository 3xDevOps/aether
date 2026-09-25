package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net"
	"reflect"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestSwarmCreateSendsMissionOptions(t *testing.T) {
	requests := make(chan protocol.Request, 4)
	client := newSwarmTestClient(t, requests, func(req protocol.Request) any {
		switch req.Method {
		case protocol.MethodWorkspaceList:
			return protocol.WorkspaceListResult{Workspaces: []protocol.Workspace{{ID: "ws-1", Name: "project"}}}
		case protocol.MethodServerInfo:
			return protocol.ServerInfoResult{Member: protocol.Member{ID: "member-self"}}
		case protocol.MethodMissionCreate:
			return protocol.MissionCreateResult{Mission: protocol.Mission{
				ID: "mission-1", Phase: "planning", CurrentIntegratorRunID: "run-1",
			}}
		default:
			return nil
		}
	})
	var output bytes.Buffer
	err := executeSwarmCreate([]string{
		"Fix the parser", "--integrator", "claude", "--workspace", "project",
		"--worker", "codex", "--worker", "codex:headless", "--worker", "pi:tui",
		"--max-concurrent-attempts", "3", "--max-total-attempts", "12",
	}, func(fn func(*protocol.Client) error) error {
		return fn(client)
	}, &output)
	if err != nil {
		t.Fatalf("executeSwarmCreate: %v", err)
	}

	var gotMethods []string
	var gotParams protocol.MissionCreateParams
	for len(requests) > 0 {
		req := <-requests
		gotMethods = append(gotMethods, req.Method)
		if req.Method == protocol.MethodMissionCreate {
			if err := json.Unmarshal(req.Params, &gotParams); err != nil {
				t.Fatalf("decode mission.create params: %v", err)
			}
		}
	}
	if want := []string{protocol.MethodWorkspaceList, protocol.MethodServerInfo, protocol.MethodMissionCreate}; !reflect.DeepEqual(gotMethods, want) {
		t.Fatalf("RPC methods = %v, want %v", gotMethods, want)
	}
	wantChoices := []protocol.MissionExecutionChoice{
		{AccountMemberID: "member-self", Harness: "claude", Mode: "tui"},
		{AccountMemberID: "member-self", Harness: "codex", Mode: "headless"},
		{AccountMemberID: "member-self", Harness: "pi", Mode: "tui"},
	}
	if gotParams.WorkspaceID != "ws-1" || gotParams.Objective != "Fix the parser" || gotParams.AccountableHumanID != "" ||
		gotParams.Integrator != (protocol.MissionIntegrator{AccountMemberID: "member-self", Harness: "claude", Mode: "tui"}) ||
		!reflect.DeepEqual(gotParams.ExecutionChoices, wantChoices) || gotParams.MaxConcurrentAttempts != 3 ||
		gotParams.MaxTotalAttempts != 12 || gotParams.IdempotencyKey == "" {
		t.Fatalf("mission.create params = %+v", gotParams)
	}
	if got, want := output.String(), "swarm mission-1 planning\nintegrator run run-1\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestSwarmCreateUsesSelectedAccountAndDefaults(t *testing.T) {
	requests := make(chan protocol.Request, 4)
	client := newSwarmTestClient(t, requests, func(req protocol.Request) any {
		switch req.Method {
		case protocol.MethodWorkspaceList:
			return protocol.WorkspaceListResult{Workspaces: []protocol.Workspace{{ID: "ws-1", Name: "project"}}}
		case protocol.MethodMissionCreate:
			return protocol.MissionCreateResult{Mission: protocol.Mission{ID: "mission-2", Phase: "planning"}}
		default:
			return nil
		}
	})
	err := executeSwarmCreate([]string{
		"objective", "--integrator", "claude", "--account", "member-shared",
	}, func(fn func(*protocol.Client) error) error {
		return fn(client)
	}, io.Discard)
	if err != nil {
		t.Fatalf("executeSwarmCreate: %v", err)
	}

	var gotMethods []string
	var gotParams protocol.MissionCreateParams
	for len(requests) > 0 {
		req := <-requests
		gotMethods = append(gotMethods, req.Method)
		if req.Method == protocol.MethodMissionCreate {
			if err := json.Unmarshal(req.Params, &gotParams); err != nil {
				t.Fatalf("decode mission.create params: %v", err)
			}
		}
	}
	if want := []string{protocol.MethodWorkspaceList, protocol.MethodMissionCreate}; !reflect.DeepEqual(gotMethods, want) {
		t.Fatalf("RPC methods = %v, want %v", gotMethods, want)
	}
	wantChoices := []protocol.MissionExecutionChoice{
		{AccountMemberID: "member-shared", Harness: "claude", Mode: "headless"},
		{AccountMemberID: "member-shared", Harness: "claude", Mode: "tui"},
	}
	if gotParams.Integrator.AccountMemberID != "member-shared" ||
		!reflect.DeepEqual(gotParams.ExecutionChoices, wantChoices) ||
		gotParams.MaxConcurrentAttempts != 2 || gotParams.MaxTotalAttempts != 8 {
		t.Fatalf("mission.create params = %+v", gotParams)
	}
}

func TestSwarmCreateRejectsInvalidValuesBeforeControl(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "worker mode",
			args: []string{"objective", "--integrator", "claude", "--worker", "codex:other"},
			want: `invalid worker mode "other"`,
		},
		{
			name: "concurrent limit",
			args: []string{"objective", "--integrator", "claude", "--max-concurrent-attempts", "9"},
			want: "--max-concurrent-attempts must be between 1 and 8",
		},
		{
			name: "total below concurrent",
			args: []string{"objective", "--integrator", "claude", "--max-concurrent-attempts", "3", "--max-total-attempts", "2"},
			want: "--max-total-attempts must be at least --max-concurrent-attempts",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opened := false
			err := executeSwarmCreate(tc.args, func(func(*protocol.Client) error) error {
				opened = true
				return nil
			}, io.Discard)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want containing %q", err, tc.want)
			}
			if opened {
				t.Fatal("control was opened for invalid create options")
			}
		})
	}
}

func TestListMissionsFetchesEveryPage(t *testing.T) {
	requests := make(chan protocol.Request, 4)
	client := newSwarmTestClient(t, requests, func(req protocol.Request) any {
		var params protocol.MissionListParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil
		}
		switch params.Before {
		case "":
			return protocol.MissionListResult{
				Missions:   []protocol.Mission{{ID: "mission-2"}},
				NextCursor: "mission-2",
			}
		case "mission-2":
			return protocol.MissionListResult{Missions: []protocol.Mission{{ID: "mission-1"}}}
		default:
			return nil
		}
	})
	missions, err := listMissions(client, "ws-1")
	if err != nil {
		t.Fatalf("listMissions: %v", err)
	}
	if got, want := []string{missions[0].ID, missions[1].ID}, []string{"mission-2", "mission-1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("mission IDs = %v, want %v", got, want)
	}
	var before []string
	for len(requests) > 0 {
		var params protocol.MissionListParams
		req := <-requests
		if req.Method != protocol.MethodMissionList {
			t.Fatalf("method = %q, want mission.list", req.Method)
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			t.Fatalf("decode mission.list params: %v", err)
		}
		if params.WorkspaceID != "ws-1" || params.Limit != 100 {
			t.Fatalf("mission.list params = %+v", params)
		}
		before = append(before, params.Before)
	}
	if want := []string{"", "mission-2"}; !reflect.DeepEqual(before, want) {
		t.Fatalf("before cursors = %v, want %v", before, want)
	}
}

func TestRenderSwarmMissions(t *testing.T) {
	var output bytes.Buffer
	err := renderMissions(&output, []protocol.Mission{{
		ID: "mission-1", Phase: "planning", Objective: "Repair parser", OpenQuestions: 2,
		Integrator: protocol.MissionIntegrator{AccountMemberID: "member-1", Harness: "claude", Mode: "tui"},
	}})
	if err != nil {
		t.Fatalf("renderMissions: %v", err)
	}
	want := "ID         PHASE     INTEGRATOR           OPEN QUESTIONS  OBJECTIVE\n" +
		"mission-1  planning  member-1/claude/tui  2               Repair parser\n"
	if got := output.String(); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestRenderMissionShowIncludesProgressAndLaunchFailure(t *testing.T) {
	var output bytes.Buffer
	err := renderMissionShow(&output, protocol.MissionShowResult{
		Mission: protocol.Mission{
			ID: "mission-1", WorkspaceID: "ws-1", Objective: "Repair parser", AccountableHumanID: "member-1",
			Phase: "planning", Integrator: protocol.MissionIntegrator{AccountMemberID: "member-1", Harness: "claude", Mode: "tui"},
			CurrentIntegratorRunID: "run-1", IntegratorLaunchError: "agent exited",
			MaxConcurrentAttempts: 2, MaxTotalAttempts: 8, PlanVersion: 1, OpenQuestions: 1,
			ExecutionChoices: []protocol.MissionExecutionChoice{{AccountMemberID: "member-1", Harness: "codex", Mode: "headless"}},
		},
		Tasks: []protocol.Task{{
			ID: "task-1", Status: "ready", CurrentRevision: 1,
			Revision: &protocol.TaskRevision{Title: "Fix parser state"},
		}},
		Attempts:    []protocol.Attempt{{ID: "attempt-1", TaskID: "task-1", State: "failed", Harness: "codex", Mode: "headless", RunID: "run-2", LastError: "container exited"}},
		Questions:   []protocol.MissionQuestion{{ID: "question-1", AskedAt: "now", Body: "Which format?"}},
		PlanReviews: []protocol.MissionPlanReview{{PlanVersion: 1, Decision: "revise", Summary: "Initial plan", Feedback: "Narrow scope"}},
	})
	if err != nil {
		t.Fatalf("renderMissionShow: %v", err)
	}
	for _, want := range []string{
		"swarm mission-1", "integrator launch error agent exited", "TASKS", "task-1  ready   1         Fix parser state",
		"attempt-1", "container exited", "question-1", "answer: unanswered", "Narrow scope",
	} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("output missing %q: %s", want, output.String())
		}
	}
}

func TestRenderMissionCreatedIncludesShowHint(t *testing.T) {
	var output bytes.Buffer
	err := renderMissionCreated(&output, protocol.Mission{
		ID: "mission-1", Phase: "planning", CurrentIntegratorRunID: "run-1", IntegratorLaunchError: "agent exited",
	})
	if err != nil {
		t.Fatalf("renderMissionCreated: %v", err)
	}
	if want := "inspect with: aether swarm show mission-1"; !strings.Contains(output.String(), want) {
		t.Fatalf("output = %q, want hint %q", output.String(), want)
	}
}

func TestRunSwarmShowRequiresID(t *testing.T) {
	err := runSwarm([]string{"show"})
	if err == nil || err.Error() != "usage: aether swarm show <swarm-id>" {
		t.Fatalf("error = %v, want missing-ID usage", err)
	}
}

func newSwarmTestClient(t *testing.T, requests chan<- protocol.Request, handle func(protocol.Request) any) *protocol.Client {
	t.Helper()
	server, client := net.Pipe()
	control := protocol.NewClient(client)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() { _ = server.Close() }()
		reader := bufio.NewReader(server)
		for {
			line, err := protocol.ReadLine(reader)
			if err != nil {
				return
			}
			var req protocol.Request
			if json.Unmarshal(line, &req) != nil {
				return
			}
			requests <- req
			result, err := json.Marshal(handle(req))
			if err != nil {
				return
			}
			response, err := json.Marshal(protocol.Response{JSONRPC: "2.0", ID: req.ID, Result: result})
			if err != nil {
				return
			}
			if _, err := server.Write(append(response, '\n')); err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		_ = control.Close()
		<-done
	})
	return control
}
