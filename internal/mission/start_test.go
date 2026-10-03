package mission

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

// recordingLauncher persists the run the attempt already reserved, which is
// what the scheduler seam must do: the durable RunID is never re-issued.
type recordingLauncher struct {
	db *store.DB
	mu sync.Mutex
}

func (l *recordingLauncher) LaunchMission(ctx context.Context, req MissionLaunchRequest) (*domain.Run, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	r := &domain.Run{
		ID: req.RunID, WorkspaceID: req.WorkspaceID, MemberID: req.RunOwnerID, Task: req.Task,
		Harness: req.Harness, Mode: req.Mode, Status: domain.RunQueued,
	}
	if err := l.db.CreateRunWithID(ctx, r); err != nil {
		return nil, err
	}
	return r, nil
}

type taskSpec struct {
	key        string
	title      string
	paths      []string
	exclusions []string
}

// activate proposes the given tasks and starts the mission, returning the
// tasks in the order given, reloaded from the store so their current revision
// is the accepted one.
func (f *missionFixture) activate(t *testing.T, specs ...taskSpec) []*domain.Task {
	t.Helper()
	ctx := context.Background()
	ids := make([]domain.TaskID, 0, len(specs))
	for _, spec := range specs {
		out := f.mustCall(t, protocol.MethodTaskPropose, protocol.TaskProposeParams{
			MissionID: string(f.mission.ID),
			Revision: protocol.TaskRevision{Title: spec.title, Objective: spec.title, Scope: protocol.TaskScope{
				ExpectedPaths: spec.paths, Exclusions: spec.exclusions,
			}},
			IdempotencyKey: spec.key,
		})
		mutation, ok := out.(protocol.TaskMutationResult)
		if !ok || mutation.Task.ID == "" {
			t.Fatalf("task.propose result = %#v, want a task", out)
		}
		ids = append(ids, domain.TaskID(mutation.Task.ID))
	}
	f.start(t, "start-1")
	tasks := make([]*domain.Task, 0, len(ids))
	for _, id := range ids {
		task, err := f.db.GetTask(ctx, id)
		if err != nil {
			t.Fatalf("reload started task %s: %v", id, err)
		}
		if task.Revision == nil || task.Revision.Status != domain.TaskRevisionAccepted {
			t.Fatalf("task %s after start = %+v, want an accepted revision", id, task.Revision)
		}
		tasks = append(tasks, task)
	}
	return tasks
}

func (f *missionFixture) startWorker(t *testing.T, task *domain.Task, dispatchKey string) error {
	t.Helper()
	current, err := f.db.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("reload task %s: %v", task.ID, err)
	}
	_, startErr := f.call(t, f.mission.CurrentIntegratorRunID, protocol.MethodWorkerStart, protocol.WorkerStartParams{
		MissionID: string(f.mission.ID), TaskID: string(current.ID), TaskRevision: current.CurrentRevision,
		DispatchKey: dispatchKey, Harness: "claude", Mode: string(domain.LaunchHeadless),
		AccountOwnerID: string(f.member.ID),
	})
	return startErr
}

func (f *missionFixture) reloadMission(t *testing.T) *domain.Mission {
	t.Helper()
	m, err := f.db.GetMission(context.Background(), f.mission.ID)
	if err != nil {
		t.Fatalf("reload mission: %v", err)
	}
	return m
}

// TestActiveMissionChangesTasksWithoutAHuman: once started, the integrator
// adds work and changes scope on its own, and approved work keeps running.
func TestActiveMissionChangesTasksWithoutAHuman(t *testing.T) {
	ctx := context.Background()
	f := newMissionFixture(t)
	tasks := f.activate(t, taskSpec{key: "propose-a", title: "task a", paths: []string{"internal/a"}, exclusions: []string{"internal/a/gen"}},
		taskSpec{key: "propose-b", title: "task b", paths: []string{"internal/b"}})
	widened, running := tasks[0], tasks[1]

	if err := f.startWorker(t, running, "dispatch-b"); err != nil {
		t.Fatalf("worker.start on an accepted task: %v", err)
	}
	f.mustCall(t, protocol.MethodTaskRevise, protocol.TaskReviseParams{
		TaskID: string(widened.ID), IdempotencyKey: "revise-a",
		Revision: protocol.TaskRevision{Title: "task a widened", Objective: "task a widened",
			Scope: protocol.TaskScope{ExpectedPaths: []string{"internal/a", "internal/c"}}},
	})
	f.mustCall(t, protocol.MethodTaskAccept, protocol.TaskAcceptParams{
		TaskID: string(widened.ID), Revision: 2, ExpectedIntegratorGeneration: f.mission.IntegratorGeneration,
		IdempotencyKey: "accept-a-2",
	})
	current, err := f.db.GetTask(ctx, widened.ID)
	if err != nil {
		t.Fatalf("reload widened task: %v", err)
	}
	if current.CurrentRevision != 2 || current.PendingRevision != nil || current.Revision.AcceptedByRunID != f.mission.CurrentIntegratorRunID {
		t.Fatalf("widened task = revision %d pending %+v accepted by %q, want revision 2 accepted by the integrator",
			current.CurrentRevision, current.PendingRevision, current.Revision.AcceptedByRunID)
	}
	added := f.propose(t, "propose-c")
	f.mustCall(t, protocol.MethodTaskAccept, protocol.TaskAcceptParams{
		TaskID: added, Revision: 1, ExpectedIntegratorGeneration: f.mission.IntegratorGeneration,
		IdempotencyKey: "accept-c",
	})
	if err := f.startWorker(t, &domain.Task{ID: domain.TaskID(added)}, "dispatch-c"); err != nil {
		t.Fatalf("worker.start on new work accepted while active: %v", err)
	}
	if m := f.reloadMission(t); m.Phase != domain.MissionPhaseActive {
		t.Fatalf("phase = %s, want active throughout", m.Phase)
	}
}

// TestReplacedIntegratorLosesTheMissionMethods: replacement retires the old
// run's authority immediately.
func TestReplacedIntegratorLosesTheMissionMethods(t *testing.T) {
	ctx := context.Background()
	f := newMissionFixture(t)
	taskID := f.propose(t, "propose-a")
	retired := f.mission.CurrentIntegratorRunID

	replaced, err := f.db.ReplaceIntegrator(ctx, f.mission.ID, f.mission.IntegratorGeneration,
		domain.MissionIntegrator{AccountMemberID: f.member.ID, Harness: "claude", Mode: domain.LaunchTUI},
		f.member.ID, f.member.ID, "replace-during-planning")
	if err != nil {
		t.Fatalf("replace integrator: %v", err)
	}
	regressionRun(t, f.db, replaced.CurrentIntegratorRunID, f.workspace.ID, f.member.ID, "replacement integrator")

	for method, params := range map[string]any{
		protocol.MethodMissionStart: protocol.MissionStartParams{MissionID: string(f.mission.ID), IdempotencyKey: "start-retired"},
		protocol.MethodTaskAccept: protocol.TaskAcceptParams{
			TaskID: taskID, Revision: 1, ExpectedIntegratorGeneration: f.mission.IntegratorGeneration,
			IdempotencyKey: "accept-retired",
		},
	} {
		_, callErr := f.call(t, retired, method, params)
		if !errors.Is(callErr, store.ErrMissionStale) && !errors.Is(callErr, store.ErrNotFound) {
			t.Fatalf("%s from the retired integrator = %v, want a closed refusal", method, callErr)
		}
	}
}

// TestDiagnosticsDescribeAPendingRevision: the overlap a proposed revision
// would create is shown before the integrator accepts it.
func TestDiagnosticsDescribeAPendingRevision(t *testing.T) {
	ctx := context.Background()
	f := newMissionFixture(t)
	tasks := f.activate(t, taskSpec{key: "propose-a", title: "task a", paths: []string{"internal/a"}},
		taskSpec{key: "propose-b", title: "task b", paths: []string{"internal/b"}})
	before, err := f.svc.Show(ctx, protocol.MissionShowParams{MissionID: string(f.mission.ID)})
	if err != nil {
		t.Fatalf("mission.show: %v", err)
	}
	for _, d := range before.Diagnostics {
		if d.Kind == scopeDiagIntended {
			t.Fatalf("accepted tasks already report an intended overlap: %+v", d)
		}
	}
	f.mustCall(t, protocol.MethodTaskRevise, protocol.TaskReviseParams{
		TaskID: string(tasks[0].ID), IdempotencyKey: "revise-a",
		Revision: protocol.TaskRevision{Title: "task a into b", Objective: "task a into b",
			Scope: protocol.TaskScope{ExpectedPaths: []string{"internal/b"}}},
	})
	after, err := f.svc.Show(ctx, protocol.MissionShowParams{MissionID: string(f.mission.ID)})
	if err != nil {
		t.Fatalf("mission.show after the revision: %v", err)
	}
	// Tasks created in the same millisecond have no stable order, so the
	// overlap may name either task first.
	a, b := string(tasks[0].ID), string(tasks[1].ID)
	found := false
	for _, d := range after.Diagnostics {
		pair := (d.TaskID == a && d.PeerTaskID == b) || (d.TaskID == b && d.PeerTaskID == a)
		if d.Kind == scopeDiagIntended && pair && len(d.Paths) == 1 && d.Paths[0] == "internal/b" {
			found = true
		}
	}
	if !found {
		t.Fatalf("diagnostics after the revision = %+v, want an intended overlap on internal/b", after.Diagnostics)
	}
	for _, task := range after.Tasks {
		if task.ID == string(tasks[0].ID) && (task.PendingRevision == nil || task.PendingRevision.Revision != 2) {
			t.Fatalf("revised task on the wire = %+v, want pending revision 2", task.PendingRevision)
		}
	}
}

// TestAssignmentCarriesOpenQuestionsWhilePlanning: the integrator reads the
// phase and its open questions off its own assignment.
func TestAssignmentCarriesOpenQuestionsWhilePlanning(t *testing.T) {
	ctx := context.Background()
	f := newMissionFixture(t)
	f.mustCall(t, protocol.MethodMissionQuestionAsk, protocol.MissionQuestionAskParams{
		Body: "which checkout flow?", IdempotencyKey: "ask-1",
	})
	assignment, err := f.svc.Assignment(ctx, f.mission.CurrentIntegratorRunID)
	if err != nil {
		t.Fatalf("assignment: %v", err)
	}
	if assignment.Phase != string(domain.MissionPhasePlanning) || assignment.OpenQuestions != 1 {
		t.Fatalf("assignment = %+v, want planning with one open question", assignment)
	}
	if !slicesContain(assignment.Capabilities, protocol.MethodMissionStart) {
		t.Fatalf("integrator capabilities = %v, want mission.start", assignment.Capabilities)
	}
}

func slicesContain(in []string, want string) bool {
	for _, v := range in {
		if v == want {
			return true
		}
	}
	return false
}
