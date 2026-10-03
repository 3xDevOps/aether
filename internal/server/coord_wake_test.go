package server

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/coord"
	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/mission"
	"github.com/3xDevOps/Aether/internal/overlap"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/scheduler"
	"github.com/3xDevOps/Aether/internal/sshd"
	"github.com/3xDevOps/Aether/internal/store"
)

type wakeNoPeers struct{}

func (wakeNoPeers) Overlaps(context.Context) ([]overlap.Entry, error) { return nil, nil }

type wakeServerFixture struct {
	db              *store.DB
	dir             string
	bus             *events.InProc
	control         *control.Service
	admission       coordWakeAdmission
	run             *domain.Run
	member          *domain.Member
	mission         *domain.Mission
	attempt         *domain.Attempt
	authorizationMu *sync.Mutex
	missions        *mission.Service
	coord           *coord.Service
}

func newWakeServerFixture(t *testing.T, worker bool) *wakeServerFixture {
	t.Helper()
	ctx := context.Background()
	dir, tempErr := os.MkdirTemp("", "wake-server-")
	if tempErr != nil {
		t.Fatal(tempErr)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	db, openErr := store.Open(filepath.Join(dir, "state.db"))
	if openErr != nil {
		t.Fatal(openErr)
	}
	t.Cleanup(func() { _ = db.Close() })
	bus, busErr := events.NewInProc(ctx, nil)
	if busErr != nil {
		t.Fatal(busErr)
	}
	t.Cleanup(func() { _ = bus.Close() })
	workspace := &domain.Workspace{Name: "wake", BaseBranch: domain.DefaultBaseBranch}
	if createErr := db.CreateWorkspace(ctx, workspace); createErr != nil {
		t.Fatal(createErr)
	}
	member := &domain.Member{DisplayName: "Wake owner", TailnetLogin: "wake@example.com", Role: domain.RoleCollaborator}
	if createErr := db.CreateMember(ctx, member); createErr != nil {
		t.Fatal(createErr)
	}
	run := &domain.Run{WorkspaceID: workspace.ID, MemberID: member.ID, Task: "wake test", Harness: "omp", Mode: domain.LaunchTUI, Status: domain.RunRunning}
	f := &wakeServerFixture{db: db, dir: dir, bus: bus, run: run, member: member, control: control.New(control.Config{})}
	if worker {
		m := &domain.Mission{
			WorkspaceID: workspace.ID, Objective: "wake worker", AccountableHumanID: member.ID,
			Integrator:       domain.MissionIntegrator{AccountMemberID: member.ID, Harness: "omp", Mode: domain.LaunchTUI},
			IdempotencyKey:   "wake-mission",
			ExecutionChoices: []domain.MissionExecutionChoice{{AccountMemberID: member.ID, Harness: "omp", Mode: domain.LaunchTUI}},
		}
		if createErr := db.CreateMission(ctx, m); createErr != nil {
			t.Fatal(createErr)
		}
		if err := db.CreateRunWithID(ctx, &domain.Run{ID: m.CurrentIntegratorRunID, WorkspaceID: workspace.ID, MemberID: member.ID, Task: "integrator", Harness: "omp", Mode: domain.LaunchTUI, Status: domain.RunRunning}); err != nil {
			t.Fatal(err)
		}
		task := &domain.Task{MissionID: m.ID, Revision: &domain.TaskRevision{Title: "worker", Objective: "worker", Status: domain.TaskRevisionProposed}}
		if err := db.CreateTask(ctx, task); err != nil {
			t.Fatal(err)
		}
		question, err := db.InsertMissionQuestion(ctx, m.ID, m.CurrentIntegratorRunID, "scope?", "wake-question")
		if err != nil {
			t.Fatal(err)
		}
		if _, answerErr := db.AnswerMissionQuestion(ctx, question.ID, member.ID, "worker scope", "wake-answer"); answerErr != nil {
			t.Fatal(answerErr)
		}
		m, err = db.StartMission(ctx, m.ID, m.CurrentIntegratorRunID, "wake-start")
		if err != nil {
			t.Fatal(err)
		}
		attempt, _, err := db.ReserveAttempt(ctx, &domain.AttemptReservation{
			MissionID: m.ID, TaskID: task.ID, TaskRevision: task.CurrentRevision, DispatchKey: "wake-worker",
			Harness: "omp", Mode: domain.LaunchTUI, IntegratorGeneration: m.IntegratorGeneration,
			ActorRunID: m.CurrentIntegratorRunID, AuthorizingHumanID: member.ID,
			RunOwnerID: member.ID, AccountOwnerID: member.ID, AuthorityGeneration: m.IntegratorGeneration,
		})
		if err != nil {
			t.Fatal(err)
		}
		run.ID = attempt.RunID
		if err = db.CreateRunWithID(ctx, run); err != nil {
			t.Fatal(err)
		}
		f.mission, f.attempt = m, attempt
	} else if err := db.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	f.authorizationMu = &sync.Mutex{}
	missions, err := mission.New(mission.Config{
		Store: db, Missions: db, AuthorizationMu: f.authorizationMu,
		Runs: wakeMissionLauncher{db}, RequireCoordination: func() error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	f.missions = missions
	missionControl := missionControlService{store: db, control: f.control}
	f.admission = coordWakeAdmission{
		store: db, control: f.control, mission: missions,
		authorizationMu: f.authorizationMu,
		missionControl:  func() sshd.MissionControl { return missionControl },
		observe: func(context.Context, domain.RunID) (scheduler.MissionRunObservation, error) {
			return scheduler.MissionRunObservation{State: scheduler.MissionRunActive}, nil
		},
	}
	msg := &store.RunMessage{WorkspaceID: workspace.ID, FromRun: run.ID, ToRun: run.ID, Body: "durable mail", IdempotencyKey: "wake-mail"}
	if err = db.AppendRunMessage(ctx, msg, protocol.CoordMaxUnread); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *wakeServerFixture) client(t *testing.T, admit coord.WakeAdmission) *protocol.Client {
	t.Helper()
	svc, err := coord.New(coord.Config{Dir: filepath.Join(f.dir, "coord"), Store: f.db, Mail: f.db, Bus: f.bus, Peers: wakeNoPeers{}, WakeAdmission: admit})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	f.coord = svc
	dir, err := svc.Provision(context.Background(), f.run.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("unix", filepath.Join(dir, coordtransport.SocketName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return protocol.NewClient(conn)
}

func (f *wakeServerFixture) hold() error {
	return f.control.Admit(string(f.run.ID), func() error {
		if f.attempt != nil {
			return (missionControlService{store: f.db, control: f.control}).Takeover(context.Background(), f.run.ID, f.member.ID)
		}
		return f.db.SetRunProtected(context.Background(), f.run.ID, true)
	})
}

func wakeSocketCall(client *protocol.Client) <-chan struct {
	status protocol.CoordStatusResult
	err    error
} {
	done := make(chan struct {
		status protocol.CoordStatusResult
		err    error
	}, 1)
	go func() {
		var status protocol.CoordStatusResult
		err := client.Call(protocol.MethodCoordHookStatus, protocol.CoordHookStatusParams{}, &status)
		done <- struct {
			status protocol.CoordStatusResult
			err    error
		}{status, err}
	}()
	return done
}

func awaitWakeBarrier(t *testing.T, ready <-chan struct{}) {
	t.Helper()
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("admission barrier not reached")
	}
}

func TestCoordWakeHoldBeforeDispatchSuppressesFrame(t *testing.T) {
	for _, worker := range []bool{false, true} {
		name := "protection"
		if worker {
			name = "takeover"
		}
		t.Run(name, func(t *testing.T) {
			f := newWakeServerFixture(t, worker)
			ready, resume := make(chan struct{}), make(chan struct{})
			client := f.client(t, func(ctx context.Context, run domain.RunID, dispatch func() error) error {
				close(ready)
				<-resume
				return f.admission.Admit(ctx, run, dispatch)
			})
			done := wakeSocketCall(client)
			awaitWakeBarrier(t, ready)
			if err := f.hold(); err != nil {
				t.Fatal(err)
			}
			close(resume)
			select {
			case reply := <-done:
				if reply.err != nil || !reply.status.WaitSupported || reply.status.WakeAdmitted || len(reply.status.UnreadMessageIDs) != 1 {
					t.Fatalf("hold-first response = %+v, %v", reply.status, reply.err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("suppressed response missing")
			}
		})
	}
}

func TestCoordWakeDispatchBeforeHoldOrdersAcceptedFrame(t *testing.T) {
	for _, worker := range []bool{false, true} {
		name := "protection"
		if worker {
			name = "takeover"
		}
		t.Run(name, func(t *testing.T) {
			f := newWakeServerFixture(t, worker)
			ready, resume, written := make(chan struct{}), make(chan struct{}), make(chan struct{})
			client := f.client(t, func(ctx context.Context, run domain.RunID, dispatch func() error) error {
				return f.admission.Admit(ctx, run, func() error {
					close(ready)
					<-resume
					err := dispatch()
					close(written)
					return err
				})
			})
			done := wakeSocketCall(client)
			awaitWakeBarrier(t, ready)
			holding, held := make(chan struct{}), make(chan error, 1)
			go func() { close(holding); held <- f.hold() }()
			awaitWakeBarrier(t, holding)
			select {
			case err := <-held:
				t.Fatalf("hold passed an in-flight admitted frame: %v", err)
			case <-time.After(50 * time.Millisecond):
			}
			close(resume)
			select {
			case reply := <-done:
				if reply.err != nil || !reply.status.WakeAdmitted {
					t.Fatalf("dispatch-first response = %+v, %v", reply.status, reply.err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("admitted frame missing")
			}
			awaitWakeBarrier(t, written)
			select {
			case err := <-held:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("hold did not follow accepted frame")
			}
		})
	}
}

func TestCoordWakeAdmissionFailsClosedForUnavailableAndFinishedRuns(t *testing.T) {
	for _, mode := range []string{"missing-control", "missing-mission", "missing-runtime", "stopped", "terminal", "submitted", "missing-worker-control"} {
		t.Run(mode, func(t *testing.T) {
			f := newWakeServerFixture(t, mode == "submitted" || mode == "missing-worker-control")
			switch mode {
			case "missing-control":
				f.admission.control = nil
			case "missing-mission":
				f.admission.mission = nil
			case "missing-runtime":
				f.admission.observe = nil
			case "stopped":
				f.admission.observe = func(context.Context, domain.RunID) (scheduler.MissionRunObservation, error) {
					return scheduler.MissionRunObservation{State: scheduler.MissionRunStopped}, nil
				}
			case "terminal":
				if err := f.db.UpdateRunStatus(context.Background(), f.run.ID, domain.RunCompleted, "", nil, nil); err != nil {
					t.Fatal(err)
				}
			case "submitted":
				a := f.attempt
				if err := f.db.UpdateAttemptState(context.Background(), a.ID, a.RunID, a.AuthorityGeneration, a.IntegratorGeneration, domain.AttemptSubmitted, ""); err != nil {
					t.Fatal(err)
				}
			case "missing-worker-control":
				f.admission.missionControl = nil
			}
			client := f.client(t, f.admission.Admit)
			var status protocol.CoordStatusResult
			if err := client.Call(protocol.MethodCoordHookStatus, protocol.CoordHookStatusParams{}, &status); err != nil {
				t.Fatal(err)
			}
			if !status.WaitSupported || status.WakeAdmitted || len(status.UnreadMessageIDs) != 1 {
				t.Fatalf("unavailable authority admitted %+v", status)
			}
		})
	}
}

type gatedWakeMission struct {
	coordWakeMission
	ready  chan struct{}
	resume chan struct{}
	first  bool
}

func (m *gatedWakeMission) Assignment(ctx context.Context, run domain.RunID) (protocol.CoordMissionAssignment, error) {
	assignment, err := m.coordWakeMission.Assignment(ctx, run)
	if !m.first {
		m.first = true
		close(m.ready)
		<-m.resume
	}
	return assignment, err
}

func TestCoordWakeReplacementIntegratorInvalidatesReadyObserver(t *testing.T) {
	f := newWakeServerFixture(t, true)
	gate := &gatedWakeMission{coordWakeMission: f.admission.mission, ready: make(chan struct{}), resume: make(chan struct{})}
	f.admission.mission = gate
	client := f.client(t, f.admission.Admit)
	done := wakeSocketCall(client)
	awaitWakeBarrier(t, gate.ready)
	m := f.mission
	if _, err := f.db.ReplaceIntegrator(context.Background(), m.ID, m.IntegratorGeneration, m.Integrator, f.member.ID, f.member.ID, "wake-replacement"); err != nil {
		t.Fatal(err)
	}
	close(gate.resume)
	select {
	case reply := <-done:
		if reply.err != nil || reply.status.WakeAdmitted || len(reply.status.UnreadMessageIDs) != 1 {
			t.Fatalf("old integrator authority admitted a ready wake: %+v, %v", reply.status, reply.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("replacement response missing")
	}
}

type wakeMissionLauncher struct{ db *store.DB }

func (l wakeMissionLauncher) LaunchMission(ctx context.Context, req mission.MissionLaunchRequest) (*domain.Run, error) {
	run := &domain.Run{
		ID: req.RunID, WorkspaceID: req.WorkspaceID, MemberID: req.RunOwnerID,
		Task: req.Task, Harness: req.Harness, Mode: req.Mode, Status: domain.RunRunning,
	}
	return run, l.db.CreateRunWithID(ctx, run)
}

func TestCoordWakeMissionMutationAfterValidationOrdersFrame(t *testing.T) {
	for _, change := range []string{"replacement-worker", "replacement-integrator", "revision"} {
		t.Run(change, func(t *testing.T) {
			f := newWakeServerFixture(t, true)
			ctx := context.Background()
			if change == "replacement-integrator" {
				var err error
				f.run, err = f.db.GetRun(ctx, f.mission.CurrentIntegratorRunID)
				if err != nil {
					t.Fatal(err)
				}
				if err := f.db.AppendRunMessage(ctx, &store.RunMessage{
					WorkspaceID: f.run.WorkspaceID, FromRun: f.attempt.RunID, ToRun: f.run.ID,
					Body: "integrator mail", IdempotencyKey: "integrator-wake",
				}, protocol.CoordMaxUnread); err != nil {
					t.Fatal(err)
				}
			}
			mutate := func() error {
				_, err := f.missions.ReplaceIntegrator(ctx, f.member.ID, protocol.MissionReplaceIntegratorParams{
					MissionID: string(f.mission.ID), ExpectedGeneration: f.mission.IntegratorGeneration,
					Integrator:     protocol.MissionIntegrator{AccountMemberID: string(f.member.ID), Harness: "omp", Mode: string(domain.LaunchTUI)},
					IdempotencyKey: "wake-racing-replacement",
				})
				return err
			}
			if change == "revision" {
				revision, err := f.db.ProposeTaskRevision(ctx, f.attempt.TaskID,
					&domain.TaskRevision{Title: "replacement", Objective: "replacement"}, "wake-propose-revision")
				if err != nil {
					t.Fatal(err)
				}
				raw, err := json.Marshal(protocol.TaskAcceptParams{
					TaskID: string(f.attempt.TaskID), Revision: revision.Revision,
					ExpectedIntegratorGeneration: f.mission.IntegratorGeneration,
					IdempotencyKey:               "wake-accept-revision",
				})
				if err != nil {
					t.Fatal(err)
				}
				mutate = func() error {
					_, err := f.missions.HandleAgent(ctx, f.mission.CurrentIntegratorRunID, protocol.MethodTaskAccept, raw)
					return err
				}
			}
			validated, release, written := make(chan struct{}), make(chan struct{}), make(chan struct{})
			resume := sync.OnceFunc(func() { close(release) })
			defer resume()
			pause := sync.OnceFunc(func() { close(validated); <-release })
			accepted := sync.OnceFunc(func() { close(written) })
			client := f.client(t, func(ctx context.Context, run domain.RunID, dispatch func() error) error {
				return f.admission.Admit(ctx, run, func() error {
					pause() // Assignment and ValidateWake have both completed.
					err := dispatch()
					accepted()
					return err
				})
			})
			done := wakeSocketCall(client)
			awaitWakeBarrier(t, validated)
			// Probe the mutation boundary at the deterministic check-to-frame
			// gap; neither replacement nor revision acceptance may enter it.
			if f.authorizationMu.TryLock() {
				f.authorizationMu.Unlock()
				t.Fatal("mission mutation can commit after validation but before dispatch")
			}
			started, changed := make(chan struct{}), make(chan error, 1)
			go func() {
				close(started)
				err := mutate()
				select {
				case <-written:
				default:
					err = errors.Join(err, errors.New("mission mutation finished before the complete wake frame"))
				}
				changed <- err
			}()
			awaitWakeBarrier(t, started)
			resume()
			select {
			case reply := <-done:
				if reply.err != nil || !reply.status.WakeAdmitted {
					t.Fatalf("frame-first response = %+v, %v", reply.status, reply.err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("admitted frame missing")
			}
			select {
			case err := <-changed:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("mission mutation did not follow the accepted frame")
			}
			var next protocol.CoordStatusResult
			if err := client.Call(protocol.MethodCoordHookStatus, protocol.CoordHookStatusParams{}, &next); err != nil {
				t.Fatal(err)
			}
			// Replacement retires the old integrator, not its workers. A
			// surviving worker can enter a new admission under the new owner.
			wantAdmitted := change == "replacement-worker"
			if next.WakeAdmitted != wantAdmitted || len(next.UnreadMessageIDs) != 1 {
				t.Fatalf("post-mutation wake = %+v, want admitted %v with durable mail", next, wantAdmitted)
			}
		})
	}
}

func TestCoordWakeOrdinaryRunDoesNotRequireMissionAuthorization(t *testing.T) {
	for _, missing := range []bool{false, true} {
		name := "locked"
		if missing {
			name = "missing"
		}
		t.Run(name, func(t *testing.T) {
			f := newWakeServerFixture(t, false)
			if missing {
				f.admission.authorizationMu = nil
			} else {
				f.authorizationMu.Lock()
				defer f.authorizationMu.Unlock()
			}
			done := wakeSocketCall(f.client(t, f.admission.Admit))
			select {
			case reply := <-done:
				if reply.err != nil || !reply.status.WakeAdmitted || len(reply.status.UnreadMessageIDs) != 1 {
					t.Fatalf("ordinary wake = %+v, %v", reply.status, reply.err)
				}
			case <-time.After(time.Second):
				t.Fatal("ordinary wake waited for unrelated mission authorization")
			}
		})
	}
}

func TestCoordWakeMissionRequiresSharedAuthorization(t *testing.T) {
	f := newWakeServerFixture(t, true)
	f.admission.authorizationMu = nil
	var status protocol.CoordStatusResult
	if err := f.client(t, f.admission.Admit).Call(protocol.MethodCoordHookStatus, protocol.CoordHookStatusParams{}, &status); err != nil {
		t.Fatal(err)
	}
	if status.WakeAdmitted || len(status.UnreadMessageIDs) != 1 {
		t.Fatalf("missing mission mutation boundary admitted wake or lost mail: %+v", status)
	}
}

type wakeCleanupCanceller struct {
	service *coord.Service
	ready   chan struct{}
	cleanup chan struct{}
	abort   chan struct{}
}

func (c wakeCleanupCanceller) CancelMission(_ context.Context, run domain.RunID) error {
	close(c.ready)
	select {
	case <-c.abort:
		return context.Canceled
	case <-c.cleanup:
		// Retained-container destruction reaches this synchronous boundary.
		return c.service.Release(run)
	}
}

func TestCoordWakeDefersToWorkerCancellationBeforeSynchronousCleanup(t *testing.T) {
	f := newWakeServerFixture(t, true)
	ctx := context.Background()
	observed, releaseWake := make(chan struct{}), make(chan struct{})
	resumeWake := sync.OnceFunc(func() { close(releaseWake) })
	defer resumeWake()
	client := f.client(t, func(ctx context.Context, run domain.RunID, dispatch func() error) error {
		close(observed)
		<-releaseWake
		return f.admission.Admit(ctx, run, dispatch)
	})
	wake := wakeSocketCall(client)
	awaitWakeBarrier(t, observed) // The observer already owns its run reference.
	canceller := wakeCleanupCanceller{
		service: f.coord, ready: make(chan struct{}), cleanup: make(chan struct{}), abort: make(chan struct{}),
	}
	defer close(canceller.abort)
	missions, err := mission.New(mission.Config{
		Store: f.db, Missions: f.db, AuthorizationMu: f.authorizationMu, Cancel: canceller,
		MissionControl: func() (sshd.MissionControl, error) {
			return missionControlService{store: f.db, control: f.control}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(protocol.WorkerCancelParams{
		AttemptID: string(f.attempt.ID), ExpectedIntegratorGeneration: f.mission.IntegratorGeneration,
		IdempotencyKey: "wake-racing-cancel",
	})
	if err != nil {
		t.Fatal(err)
	}
	cancelled := make(chan error, 1)
	go func() {
		_, err := missions.HandleAgent(ctx, f.mission.CurrentIntegratorRunID, protocol.MethodWorkerCancel, raw)
		cancelled <- err
	}()
	awaitWakeBarrier(t, canceller.ready) // workerCancel owns AuthorizationMu and Control.
	if err := f.db.UpdateRunStatus(ctx, f.run.ID, domain.RunCompleted, "retained", nil, nil); err != nil {
		t.Fatal(err)
	}
	resumeWake()
	select {
	case reply := <-wake:
		if reply.err != nil || reply.status.WakeAdmitted || len(reply.status.UnreadMessageIDs) != 1 {
			t.Fatalf("wake during cancellation = %+v, %v", reply.status, reply.err)
		}
	case <-time.After(time.Second):
		t.Fatal("wake retained its run reference while waiting for cancellation's authorization")
	}
	close(canceller.cleanup)
	select {
	case err := <-cancelled:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("synchronous retained-run cleanup waited for the suppressed observer")
	}
}

func TestCoordWakeDefersToControlBeforeSynchronousCleanup(t *testing.T) {
	for _, worker := range []bool{false, true} {
		name := "ordinary"
		if worker {
			name = "worker"
		}
		t.Run(name, func(t *testing.T) {
			f := newWakeServerFixture(t, worker)
			observed, releaseWake := make(chan struct{}), make(chan struct{})
			resumeWake := sync.OnceFunc(func() { close(releaseWake) })
			defer resumeWake()
			client := f.client(t, func(ctx context.Context, run domain.RunID, dispatch func() error) error {
				close(observed)
				<-releaseWake
				return f.admission.Admit(ctx, run, dispatch)
			})
			wake := wakeSocketCall(client)
			awaitWakeBarrier(t, observed)
			entered, cleanup, abort := make(chan struct{}), make(chan struct{}), make(chan struct{})
			defer close(abort)
			released := make(chan error, 1)
			go func() {
				released <- f.control.Admit(string(f.run.ID), func() error {
					close(entered)
					select {
					case <-abort:
						return context.Canceled
					case <-cleanup:
						return f.coord.Release(f.run.ID)
					}
				})
			}()
			awaitWakeBarrier(t, entered)
			resumeWake()
			select {
			case reply := <-wake:
				if reply.err != nil || reply.status.WakeAdmitted || len(reply.status.UnreadMessageIDs) != 1 {
					t.Fatalf("wake during control cleanup = %+v, %v", reply.status, reply.err)
				}
			case <-time.After(time.Second):
				t.Fatal("wake waited on Control while retaining cleanup's run reference")
			}
			close(cleanup)
			select {
			case err := <-released:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("Control-owned cleanup did not finish")
			}
		})
	}
}

func TestCoordWakeReportSubmissionOrdersAcceptedFrame(t *testing.T) {
	for _, frameFirst := range []bool{false, true} {
		name := "submission-first"
		if frameFirst {
			name = "frame-first"
		}
		t.Run(name, func(t *testing.T) {
			f := newWakeServerFixture(t, true)
			ctx := context.Background()
			packet := protocol.EvidencePacket{
				ID: "wake-report-packet", WorkspaceID: string(f.run.WorkspaceID), RunID: string(f.run.ID),
				Origin:       protocol.EvidenceOrigin{Kind: protocol.EvidenceOriginRun, ID: string(f.run.ID)},
				Availability: protocol.EvidenceAvailable, RetainedRevision: "wake-report-revision",
			}
			report := &store.CoordReport{
				WorkspaceID: f.run.WorkspaceID, RunID: f.run.ID, Outcome: store.CoordOutcomeSuccess,
				Summary: "worker completed", EvidenceRefs: []string{packet.ID},
			}
			ready, release, admissionFinished := make(chan struct{}), make(chan struct{}), make(chan struct{})
			resume := sync.OnceFunc(func() { close(release) })
			defer resume()
			finish := sync.OnceFunc(func() { close(admissionFinished) })
			client := f.client(t, func(ctx context.Context, run domain.RunID, dispatch func() error) error {
				defer finish()
				if frameFirst {
					return f.admission.Admit(ctx, run, func() error {
						close(ready) // Final ValidateWake passed; no frame bytes accepted yet.
						<-release
						return dispatch()
					})
				}
				close(ready)
				<-release
				return f.admission.Admit(ctx, run, dispatch)
			})
			done := wakeSocketCall(client)
			awaitWakeBarrier(t, ready)
			var submitted chan error
			if frameFirst {
				submitted = make(chan error, 1)
				go func() { submitted <- f.missions.ReconcileReport(ctx, f.run.ID, report, packet) }()
			} else if err := f.missions.ReconcileReport(ctx, f.run.ID, report, packet); err != nil {
				t.Fatal(err)
			}
			resume()
			select {
			case reply := <-done:
				if reply.err != nil || reply.status.WakeAdmitted != frameFirst || len(reply.status.UnreadMessageIDs) != 1 {
					t.Fatalf("ordered submission response = %+v, %v; want admitted %v", reply.status, reply.err, frameFirst)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("ordered wake frame missing")
			}
			awaitWakeBarrier(t, admissionFinished)
			if frameFirst {
				select {
				case reportErr := <-submitted:
					if reportErr != nil {
						t.Fatalf("submission did not commit after accepted frame: %v", reportErr)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("submission did not finish after accepted frame")
				}
				var next protocol.CoordStatusResult
				if callErr := client.Call(protocol.MethodCoordHookStatus, protocol.CoordHookStatusParams{}, &next); callErr != nil {
					t.Fatal(callErr)
				}
				if next.WakeAdmitted {
					t.Fatal("submitted worker received a subsequent admitted wake")
				}
			}
			attempt, err := f.db.GetAttempt(ctx, f.attempt.ID)
			if err != nil || attempt.State != domain.AttemptSubmitted {
				t.Fatalf("report did not settle to submitted: %+v, %v", attempt, err)
			}
		})
	}
}
