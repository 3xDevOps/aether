package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/mission"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
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
	}, &output, io.Discard)
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
		{AccountMemberID: "member-self", Harness: "codex", Mode: "tui"},
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
	}, io.Discard, io.Discard)
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
			}, io.Discard, io.Discard)
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

func TestSwarmTablesKeepMultilineTextInOneRow(t *testing.T) {
	var output bytes.Buffer
	err := renderMissions(&output, []protocol.Mission{
		{ID: "mission-1", Objective: "Repair\nparser\tstate\r\nnow"},
		{ID: "mission-2", Objective: "Second objective"},
	})
	if err != nil {
		t.Fatal(err)
	}
	rows := strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n")
	if len(rows) != 3 || !strings.Contains(rows[1], "Repair parser state now") || !strings.Contains(rows[2], "mission-2") {
		t.Fatalf("mission records did not stay on separate rows:\n%s", output.String())
	}

	output.Reset()
	err = renderMissionShow(&output, protocol.MissionShowResult{
		Tasks: []protocol.Task{{
			ID: "task-1", CurrentRevision: 1,
			Revision: &protocol.TaskRevision{Title: "Repair\nparser\tstate"},
		}},
		Attempts: []protocol.Attempt{{ID: "attempt-1", LastError: "command\nfailed:\ttry again"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Repair parser state", "command failed: try again"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("missing single-line cell %q:\n%s", want, output.String())
		}
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
		"swarm mission-1", "integrator launch error agent exited", "TASKS", "task-1", "Fix parser state",
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

func TestSwarmShowDisplaysCurrentAndPendingRevisions(t *testing.T) {
	var output bytes.Buffer
	err := renderMissionShow(&output, protocol.MissionShowResult{Tasks: []protocol.Task{
		{
			ID: "task-revised", Status: "blocked", CurrentRevision: 2,
			Revision:        &protocol.TaskRevision{Revision: 2, Title: "Accepted work"},
			PendingRevision: &protocol.TaskRevision{Revision: 3, Title: "Proposed change"},
		},
		{
			ID: "task-new", Status: "proposed",
			PendingRevision: &protocol.TaskRevision{Revision: 1, Title: "New proposal"},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(strings.Fields(output.String()), " ")
	for _, want := range []string{
		"task-revised blocked 2 Accepted work",
		"task-revised blocked 3 (pending) Proposed change",
		"task-new proposed 1 (pending) New proposal",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing revision row %q:\n%s", want, output.String())
		}
	}
	if strings.Contains(text, "task-new proposed 0") {
		t.Fatalf("pending-only task displayed as an accepted revision:\n%s", output.String())
	}
}

func TestSwarmTablesReturnOutputFailures(t *testing.T) {
	cases := []struct {
		name   string
		render func(io.Writer) error
	}{
		{"list", func(out io.Writer) error {
			return renderMissions(out, []protocol.Mission{{ID: "record-fails", Objective: "first\fsecond"}, {ID: "later"}})
		}},
		{"execution choices", func(out io.Writer) error {
			return renderMissionShow(out, protocol.MissionShowResult{Mission: protocol.Mission{
				ExecutionChoices: []protocol.MissionExecutionChoice{{AccountMemberID: "record-fails", Harness: "claude", Mode: "tui"}},
			}})
		}},
		{"tasks", func(out io.Writer) error {
			return renderMissionShow(out, protocol.MissionShowResult{Tasks: []protocol.Task{{
				ID: "record-fails", CurrentRevision: 1, Revision: &protocol.TaskRevision{Title: "first\fsecond"},
			}}})
		}},
		{"attempts", func(out io.Writer) error {
			return renderMissionShow(out, protocol.MissionShowResult{Attempts: []protocol.Attempt{{
				ID: "record-fails", LastError: "first\fsecond",
			}}})
		}},
		{"submissions", func(out io.Writer) error {
			return renderMissionShow(out, protocol.MissionShowResult{Submissions: []protocol.Submission{{ID: "record-fails"}}})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writer := &swarmFailOnceWriter{needle: "record-fails"}
			if err := tc.render(writer); !errors.Is(err, io.ErrClosedPipe) {
				t.Fatalf("output error = %v, want closed pipe", err)
			}
		})
	}
}

type swarmFailOnceWriter struct {
	needle string
	failed bool
}

func (w *swarmFailOnceWriter) Write(p []byte) (int, error) {
	if !w.failed && bytes.Contains(p, []byte(w.needle)) {
		w.failed = true
		return 0, io.ErrClosedPipe
	}
	return len(p), nil
}

func TestSwarmCreateRetryReusesPersistedMission(t *testing.T) {
	for _, lostResponse := range []bool{false, true} {
		t.Run(fmt.Sprintf("lost-response=%t", lostResponse), func(t *testing.T) {
			ctx := context.Background()
			db, err := store.Open(filepath.Join(t.TempDir(), "missions.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			pub, _, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			key, err := ssh.NewPublicKey(pub)
			if err != nil {
				t.Fatal(err)
			}
			member := &domain.Member{DisplayName: "caller", Role: domain.RoleCollaborator, PublicKey: string(ssh.MarshalAuthorizedKey(key))}
			if createErr := db.CreateMember(ctx, member); createErr != nil {
				t.Fatal(createErr)
			}
			workspace := &domain.Workspace{Name: "project"}
			if createErr := db.CreateWorkspace(ctx, workspace); createErr != nil {
				t.Fatal(createErr)
			}
			launcher := &swarmRetryLauncher{db: db, fail: !lostResponse}
			service, err := mission.New(mission.Config{
				Store: db, Runs: launcher, RequireCoordination: func() error { return nil },
			})
			if err != nil {
				t.Fatal(err)
			}
			requests := make(chan protocol.Request, 8)
			var missionID, serverError string
			handle := func(req protocol.Request) any {
				switch req.Method {
				case protocol.MethodWorkspaceList:
					return protocol.WorkspaceListResult{Workspaces: []protocol.Workspace{{ID: string(workspace.ID), Name: workspace.Name}}}
				case protocol.MethodMissionCreate:
					var params protocol.MissionCreateParams
					if decodeErr := json.Unmarshal(req.Params, &params); decodeErr != nil {
						return &protocol.Error{Message: decodeErr.Error()}
					}
					result, createErr := service.Create(ctx, member.ID, params)
					missionID = result.Mission.ID
					if createErr != nil {
						serverError = createErr.Error()
						return &protocol.Error{Code: protocol.CodeInternal, Message: serverError}
					}
					if lostResponse {
						lostResponse = false
						return io.ErrUnexpectedEOF
					}
					return result
				default:
					return &protocol.Error{Message: "unexpected method"}
				}
			}
			args := []string{"objective", "--integrator", "claude", "--account", string(member.ID)}
			var output, stderr bytes.Buffer
			invoke := func() error {
				client := newSwarmTestClient(t, requests, handle)
				return executeSwarmCreate(args, func(fn func(*protocol.Client) error) error { return fn(client) }, &output, &stderr)
			}
			if callErr := invoke(); callErr == nil || (serverError != "" && !strings.Contains(callErr.Error(), serverError)) {
				t.Fatalf("first create error = %v, want original launch error or dropped response", callErr)
			}
			retryKey := strings.TrimSpace(strings.TrimPrefix(stderr.String(), "idempotency-key: "))
			if retryKey == "" || strings.ContainsAny(retryKey, " \n\t") {
				t.Fatalf("no reusable key printed for the failed request: %q", stderr.String())
			}
			args = append(args, "--idempotency-key", retryKey)
			stderr.Reset()
			if callErr := invoke(); callErr != nil {
				t.Fatalf("retry: %v", callErr)
			}
			missions, err := db.ListMissions(ctx, workspace.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(missions) != 1 || launcher.calls != 1 {
				t.Fatalf("retry created %d missions and launched %d integrators", len(missions), launcher.calls)
			}
			if !strings.Contains(output.String(), "swarm "+missionID+" planning") {
				t.Fatalf("retry did not recover the original mission: %s", output.String())
			}
		})
	}
}

type swarmRetryLauncher struct {
	db    *store.DB
	fail  bool
	calls int
}

func (l *swarmRetryLauncher) LaunchMission(ctx context.Context, req mission.MissionLaunchRequest) (*domain.Run, error) {
	l.calls++
	run := &domain.Run{
		ID: req.RunID, WorkspaceID: req.WorkspaceID, MemberID: req.RunOwnerID,
		Task: req.Task, Harness: req.Harness, Mode: req.Mode, Status: domain.RunRunning,
	}
	if l.fail {
		run.Status = domain.RunFailed
	}
	if err := l.db.CreateRunWithID(ctx, run); err != nil {
		return nil, err
	}
	if l.fail {
		return nil, errors.New("provisioning failed")
	}
	return run, nil
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
			value := handle(req)
			response := protocol.Response{JSONRPC: "2.0", ID: req.ID}
			if rpcErr, ok := value.(*protocol.Error); ok {
				response.Error = rpcErr
			} else if _, ok := value.(error); ok {
				return
			} else {
				result, marshalErr := json.Marshal(value)
				if marshalErr != nil {
					return
				}
				response.Result = result
			}
			encoded, err := json.Marshal(response)
			if err != nil {
				return
			}
			if _, err := server.Write(append(encoded, '\n')); err != nil {
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
