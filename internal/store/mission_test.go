package store

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

func mustCreateMission(t *testing.T, db *DB, workspace domain.WorkspaceID, member domain.MemberID, concurrent, total int) *domain.Mission {
	t.Helper()
	m := &domain.Mission{
		WorkspaceID: workspace, Objective: "ship the bounded change", AccountableHumanID: member,
		Integrator:            domain.MissionIntegrator{AccountMemberID: member, Harness: "claude", Mode: domain.LaunchHeadless},
		MaxConcurrentAttempts: concurrent, MaxTotalAttempts: total, IdempotencyKey: "mission-create-1",
	}
	if err := db.CreateMission(context.Background(), m); err != nil {
		t.Fatalf("CreateMission: %v", err)
	}
	return m
}

func mustCreateMissionTask(t *testing.T, db *DB, mission domain.MissionID, title string) *domain.Task {
	t.Helper()
	task := &domain.Task{MissionID: mission, Revision: &domain.TaskRevision{Title: title, Objective: title, Status: domain.TaskRevisionAccepted}}
	if err := db.CreateTask(context.Background(), task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return task
}

func reserveMissionAttempt(t *testing.T, db *DB, mission *domain.Mission, task *domain.Task, key string) (*domain.Attempt, bool, error) {
	t.Helper()
	return db.ReserveAttempt(context.Background(), &domain.AttemptReservation{
		MissionID: mission.ID, TaskID: task.ID, TaskRevision: task.CurrentRevision,
		DispatchKey: key, Harness: "claude", Mode: domain.LaunchHeadless,
		IntegratorGeneration: mission.IntegratorGeneration,
	})
}

func TestMissionAttemptReservationIsAtomicAndIdempotent(t *testing.T) {
	db := openTestDB(t)
	workspace := mustCreateWorkspace(t, db)
	member := mustCreateMember(t, db)
	mission := mustCreateMission(t, db, workspace.ID, member.ID, 1, 2)
	task := mustCreateMissionTask(t, db, mission.ID, "bounded worker")

	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, key := range []string{"dispatch-a", "dispatch-b"} {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			_, _, err := reserveMissionAttempt(t, db, mission, task, key)
			results <- err
		}(key)
	}
	wg.Wait()
	close(results)
	var admitted, limited int
	for err := range results {
		if err == nil {
			admitted++
		} else if errors.Is(err, ErrMissionLimit) {
			limited++
		} else {
			t.Fatalf("reserve concurrent attempt: %v", err)
		}
	}
	if admitted != 1 || limited != 1 {
		t.Fatalf("concurrent admission = admitted %d limited %d, want one each", admitted, limited)
	}

	attempt, replay, err := reserveMissionAttempt(t, db, mission, task, "dispatch-a")
	if err != nil || !replay {
		t.Fatalf("idempotent replay = %+v, replay %v, err %v", attempt, replay, err)
	}
	if _, _, err := db.ReserveAttempt(context.Background(), &domain.AttemptReservation{
		MissionID: mission.ID, TaskID: task.ID, TaskRevision: task.CurrentRevision,
		DispatchKey: "dispatch-a", Harness: "codex", Mode: domain.LaunchHeadless,
		IntegratorGeneration: mission.IntegratorGeneration,
	}); !errors.Is(err, ErrMissionIdempotencyConflict) {
		t.Fatalf("semantic replay conflict = %v, want ErrMissionIdempotencyConflict", err)
	}
	if err := db.UpdateAttemptState(context.Background(), attempt.ID, attempt.RunID, attempt.AuthorityGeneration, attempt.IntegratorGeneration, domain.AttemptFailed, ""); err != nil {
		t.Fatalf("settle first attempt: %v", err)
	}
	if _, _, err := reserveMissionAttempt(t, db, mission, task, "dispatch-c"); err != nil {
		t.Fatalf("retry within total allowance: %v", err)
	}
	if _, _, err := reserveMissionAttempt(t, db, mission, task, "dispatch-d"); !errors.Is(err, ErrMissionLimit) {
		t.Fatalf("total allowance = %v, want ErrMissionLimit", err)
	}
}

func TestMissionDependenciesRejectCyclesAndProjectBlocked(t *testing.T) {
	db := openTestDB(t)
	workspace := mustCreateWorkspace(t, db)
	member := mustCreateMember(t, db)
	mission := mustCreateMission(t, db, workspace.ID, member.ID, 2, 4)
	first := &domain.Task{MissionID: mission.ID, Revision: &domain.TaskRevision{Title: "first", Objective: "first", Status: domain.TaskRevisionProposed}}
	second := &domain.Task{MissionID: mission.ID, Revision: &domain.TaskRevision{Title: "second", Objective: "second", Status: domain.TaskRevisionProposed}}
	if err := db.CreateTask(context.Background(), first); err != nil {
		t.Fatalf("create first: %v", err)
	}
	if err := db.CreateTask(context.Background(), second); err != nil {
		t.Fatalf("create second: %v", err)
	}
	if err := db.SetTaskDependencies(context.Background(), first.ID, 1, []domain.TaskDependency{{TaskID: first.ID, Revision: 1, DependsOnTaskID: second.ID, DependsOnRevision: 1}}, "dep-a"); err != nil {
		t.Fatalf("set dependency: %v", err)
	}
	if err := db.AcceptTaskRevision(context.Background(), first.ID, 1, mission.IntegratorGeneration, "accept-first"); err != nil {
		t.Fatalf("accept first: %v", err)
	}
	projected, err := db.ProjectTask(context.Background(), first.ID)
	if err != nil {
		t.Fatalf("ProjectTask: %v", err)
	}
	if projected.Status != domain.TaskBlocked || len(projected.Blockers) != 1 {
		t.Fatalf("blocked projection = %+v, want one dependency blocker", projected)
	}
	cycleA := &domain.Task{MissionID: mission.ID, Revision: &domain.TaskRevision{Title: "cycle-a", Objective: "cycle-a", Status: domain.TaskRevisionProposed}}
	cycleB := &domain.Task{MissionID: mission.ID, Revision: &domain.TaskRevision{Title: "cycle-b", Objective: "cycle-b", Status: domain.TaskRevisionProposed}}
	if err := db.CreateTask(context.Background(), cycleA); err != nil {
		t.Fatalf("create cycle A: %v", err)
	}
	if err := db.CreateTask(context.Background(), cycleB); err != nil {
		t.Fatalf("create cycle B: %v", err)
	}
	if err := db.SetTaskDependencies(context.Background(), cycleA.ID, 1, []domain.TaskDependency{{TaskID: cycleA.ID, Revision: 1, DependsOnTaskID: cycleB.ID, DependsOnRevision: 1}}, "dep-cycle-a"); err != nil {
		t.Fatalf("set cycle A dependency: %v", err)
	}
	if err := db.SetTaskDependencies(context.Background(), cycleB.ID, 1, []domain.TaskDependency{{TaskID: cycleB.ID, Revision: 1, DependsOnTaskID: cycleA.ID, DependsOnRevision: 1}}, "dep-cycle-b"); !errors.Is(err, ErrMissionCycle) {
		t.Fatalf("cycle dependency = %v, want ErrMissionCycle", err)
	}
}

func TestMissionAcceptanceRequiresExactEvidenceAndRevision(t *testing.T) {
	db := openTestDB(t)
	workspace := mustCreateWorkspace(t, db)
	member := mustCreateMember(t, db)
	mission := mustCreateMission(t, db, workspace.ID, member.ID, 1, 2)
	task := &domain.Task{MissionID: mission.ID, Revision: &domain.TaskRevision{Title: "verify", Objective: "verify", Status: domain.TaskRevisionAccepted, EvidenceRequirements: []domain.EvidenceRequirement{{Kind: "test"}}}}
	if err := db.CreateTask(context.Background(), task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	attempt, _, err := reserveMissionAttempt(t, db, mission, task, "evidence-dispatch")
	if err != nil {
		t.Fatalf("ReserveAttempt: %v", err)
	}
	reservedRun := &domain.Run{ID: attempt.RunID, WorkspaceID: workspace.ID, MemberID: member.ID, Task: task.Revision.Title, Harness: "claude", Mode: domain.LaunchHeadless, Status: domain.RunQueued}
	if err := db.CreateRunWithID(context.Background(), reservedRun); err != nil {
		t.Fatalf("CreateRunWithID: %v", err)
	}
	submission, err := db.SubmitAttempt(context.Background(), attempt.ID, attempt.AuthorityGeneration, attempt.IntegratorGeneration, domain.SubmissionRef{WorkspaceID: workspace.ID, RunID: attempt.RunID, EvidenceRef: "packet-1", RetainedRevision: "tree-1"}, []domain.SubmissionEvidence{{Kind: "test", Ref: "packet-1", Available: false}}, nil)
	if err != nil {
		t.Fatalf("SubmitAttempt: %v", err)
	}
	if _, err := db.AcceptSubmission(context.Background(), submission.ID, mission.CurrentIntegratorRunID, mission.IntegratorGeneration, mission.AcceptedSetVersion, "", "accept-submission"); !errors.Is(err, ErrMissionNotReady) {
		t.Fatalf("accept missing evidence = %v, want ErrMissionNotReady", err)
	}
	projected, err := db.ProjectTask(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("ProjectTask after review: %v", err)
	}
	if projected.Status != domain.TaskReview {
		t.Fatalf("task status after missing evidence = %q, want review", projected.Status)
	}
}
func TestMissionTaskRevisionAcceptanceFencesOlderProposal(t *testing.T) {
	db := openTestDB(t)
	workspace := mustCreateWorkspace(t, db)
	member := mustCreateMember(t, db)
	mission := mustCreateMission(t, db, workspace.ID, member.ID, 2, 4)
	task := mustCreateMissionTask(t, db, mission.ID, "revision fence")
	second, err := db.ProposeTaskRevision(context.Background(), task.ID, &domain.TaskRevision{Title: "second", Objective: "second"}, "rev-2")
	if err != nil {
		t.Fatalf("propose second: %v", err)
	}
	third, err := db.ProposeTaskRevision(context.Background(), task.ID, &domain.TaskRevision{Title: "third", Objective: "third"}, "rev-3")
	if err != nil {
		t.Fatalf("propose third: %v", err)
	}
	if err := db.AcceptTaskRevision(context.Background(), task.ID, third.Revision, mission.IntegratorGeneration, "accept-3"); err != nil {
		t.Fatalf("accept third: %v", err)
	}
	if err := db.AcceptTaskRevision(context.Background(), task.ID, second.Revision, mission.IntegratorGeneration, "accept-2"); !errors.Is(err, ErrMissionStale) {
		t.Fatalf("accept older revision: %v, want ErrMissionStale", err)
	}
	projected, err := db.ProjectTask(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("project fenced task: %v", err)
	}
	if projected.CurrentRevision != third.Revision || projected.Revision.Status != domain.TaskRevisionAccepted {
		t.Fatalf("current revision after stale accept = %+v", projected)
	}
}
func TestMissionInitialIntegratorRunBecomesRetiredAfterReplacement(t *testing.T) {
	db := openTestDB(t)
	workspace := mustCreateWorkspace(t, db)
	member := mustCreateMember(t, db)
	mission := mustCreateMission(t, db, workspace.ID, member.ID, 1, 2)
	initialRun := mission.CurrentIntegratorRunID
	replaced, err := db.ReplaceIntegrator(context.Background(), mission.ID, mission.IntegratorGeneration, mission.Integrator, member.ID, member.ID, "replace-initial")
	if err != nil {
		t.Fatalf("replace integrator: %v", err)
	}
	if replaced.CurrentIntegratorRunID == initialRun {
		t.Fatalf("replacement retained initial run %q", initialRun)
	}
	if _, err := db.GetMissionByRun(context.Background(), initialRun); !errors.Is(err, ErrMissionStale) {
		t.Fatalf("initial run lookup = %v, want ErrMissionStale", err)
	}
	current, err := db.GetMissionByRun(context.Background(), replaced.CurrentIntegratorRunID)
	if err != nil {
		t.Fatalf("current run lookup: %v", err)
	}
	if current.ID != mission.ID {
		t.Fatalf("current run mission = %s, want %s", current.ID, mission.ID)
	}
}
func TestMissionControlChangeOutboxCoalescesAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mission-control.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	workspace := mustCreateWorkspace(t, db)
	member := mustCreateMember(t, db)
	mission := mustCreateMission(t, db, workspace.ID, member.ID, 1, 2)
	task := mustCreateMissionTask(t, db, mission.ID, "control outbox")
	attempt, _, err := reserveMissionAttempt(t, db, mission, task, "control-outbox")
	if err != nil {
		t.Fatalf("ReserveAttempt: %v", err)
	}
	ctx := context.Background()
	if _, err := db.SetMissionWorkerTakeover(ctx, attempt.RunID, member.ID, true); err != nil {
		t.Fatalf("set takeover: %v", err)
	}
	first, err := db.PendingMissionControlChange(ctx, mission.ID)
	if err != nil {
		t.Fatalf("pending first change: %v", err)
	}
	if first == 0 {
		t.Fatal("pending first change = 0, want nonzero generation")
	}
	if _, err := db.SetMissionWorkerTakeover(ctx, attempt.RunID, member.ID, false); err != nil {
		t.Fatalf("clear takeover: %v", err)
	}
	second, err := db.PendingMissionControlChange(ctx, mission.ID)
	if err != nil {
		t.Fatalf("pending coalesced change: %v", err)
	}
	if second <= first {
		t.Fatalf("coalesced generation = %d, want > %d", second, first)
	}
	if err := db.AckMissionControlChange(ctx, mission.ID, first); err != nil {
		t.Fatalf("ack first generation: %v", err)
	}
	pending, err := db.PendingMissionControlChange(ctx, mission.ID)
	if err != nil {
		t.Fatalf("pending after old ack: %v", err)
	}
	if pending != second {
		t.Fatalf("pending after old ack = %d, want newer %d", pending, second)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close before reopen: %v", err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	pending, err = reopened.PendingMissionControlChange(ctx, mission.ID)
	if err != nil {
		t.Fatalf("pending after reopen: %v", err)
	}
	if pending != second {
		t.Fatalf("pending after reopen = %d, want newer %d", pending, second)
	}
	if err := reopened.AckMissionControlChange(ctx, mission.ID, second); err != nil {
		t.Fatalf("ack latest generation: %v", err)
	}
	if pending, err := reopened.PendingMissionControlChange(ctx, mission.ID); err != nil {
		t.Fatalf("pending after latest ack: %v", err)
	} else if pending != 0 {
		t.Fatalf("pending after latest ack = %d, want 0", pending)
	}
}

func mustSubmitMissionAttempt(t *testing.T, db *DB, mission *domain.Mission, task *domain.Task, dispatchKey string) *domain.Submission {
	t.Helper()
	attempt, _, err := reserveMissionAttempt(t, db, mission, task, dispatchKey)
	if err != nil {
		t.Fatalf("ReserveAttempt: %v", err)
	}
	run := &domain.Run{ID: attempt.RunID, WorkspaceID: mission.WorkspaceID, MemberID: domain.MemberID(mission.AccountableHumanID), Task: task.Revision.Title, Harness: "claude", Mode: domain.LaunchHeadless, Status: domain.RunQueued}
	if err := db.CreateRunWithID(context.Background(), run); err != nil {
		t.Fatalf("CreateRunWithID: %v", err)
	}
	submission, err := db.SubmitAttempt(context.Background(), attempt.ID, attempt.AuthorityGeneration, attempt.IntegratorGeneration, domain.SubmissionRef{
		WorkspaceID: mission.WorkspaceID, RunID: attempt.RunID, EvidenceRef: "packet-" + dispatchKey, RetainedRevision: "tree-" + dispatchKey,
	}, []domain.SubmissionEvidence{{Kind: "retained_packet", Ref: "packet-" + dispatchKey, Available: true}}, nil)
	if err != nil {
		t.Fatalf("SubmitAttempt: %v", err)
	}
	return submission
}

func mustAcceptMissionSubmission(t *testing.T, db *DB, mission *domain.Mission, submission *domain.Submission, key string) *domain.Acceptance {
	t.Helper()
	accepted, err := db.AcceptSubmission(context.Background(), submission.ID, mission.CurrentIntegratorRunID, mission.IntegratorGeneration, mission.AcceptedSetVersion, "", key)
	if err != nil {
		t.Fatalf("AcceptSubmission: %v", err)
	}
	mission.AcceptedSetVersion = accepted.AcceptedSetVersion
	return accepted
}

func TestMissionAcceptedSetVersionAdvancesWhenCurrentOutputLeavesSet(t *testing.T) {
	db := openTestDB(t)
	workspace := mustCreateWorkspace(t, db)
	member := mustCreateMember(t, db)
	mission := mustCreateMission(t, db, workspace.ID, member.ID, 4, 8)
	task := mustCreateMissionTask(t, db, mission.ID, "accepted output")
	submission := mustSubmitMissionAttempt(t, db, mission, task, "accepted-output")
	mustAcceptMissionSubmission(t, db, mission, submission, "accept-output")
	if mission.AcceptedSetVersion != 1 {
		t.Fatalf("initial accepted set version = %d, want 1", mission.AcceptedSetVersion)
	}

	revision, err := db.ProposeTaskRevision(context.Background(), task.ID, &domain.TaskRevision{Title: "replacement", Objective: "replacement"}, "replacement-proposal")
	if err != nil {
		t.Fatalf("ProposeTaskRevision: %v", err)
	}
	if err := db.AcceptTaskRevision(context.Background(), task.ID, revision.Revision, mission.IntegratorGeneration, "replacement-accept"); err != nil {
		t.Fatalf("AcceptTaskRevision: %v", err)
	}
	afterReplacement, err := db.GetMission(context.Background(), mission.ID)
	if err != nil {
		t.Fatalf("GetMission after replacement: %v", err)
	}
	if afterReplacement.AcceptedSetVersion != 2 {
		t.Fatalf("accepted set version after replacement = %d, want 2", afterReplacement.AcceptedSetVersion)
	}
	var historical int
	if err := db.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM mission_acceptances WHERE mission_id=? AND task_id=? AND task_revision=?`, mission.ID, task.ID, 1).Scan(&historical); err != nil {
		t.Fatalf("count historical acceptance: %v", err)
	}
	if historical != 1 {
		t.Fatalf("historical acceptance count = %d, want 1", historical)
	}

	if err := db.AcceptTaskRevision(context.Background(), task.ID, revision.Revision, mission.IntegratorGeneration, "replacement-accept"); err != nil {
		t.Fatalf("replay AcceptTaskRevision: %v", err)
	}
	afterReplay, err := db.GetMission(context.Background(), mission.ID)
	if err != nil {
		t.Fatalf("GetMission after replacement replay: %v", err)
	}
	if afterReplay.AcceptedSetVersion != 2 {
		t.Fatalf("accepted set version after replacement replay = %d, want 2", afterReplay.AcceptedSetVersion)
	}
}

func TestMissionAcceptedSetVersionDoesNotAdvanceForProposedWorkOrAbandonment(t *testing.T) {
	db := openTestDB(t)
	workspace := mustCreateWorkspace(t, db)
	member := mustCreateMember(t, db)
	mission := mustCreateMission(t, db, workspace.ID, member.ID, 4, 8)
	task := &domain.Task{MissionID: mission.ID, Revision: &domain.TaskRevision{Title: "proposed", Objective: "proposed", Status: domain.TaskRevisionProposed}}
	if err := db.CreateTask(context.Background(), task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	revision, err := db.ProposeTaskRevision(context.Background(), task.ID, &domain.TaskRevision{Title: "proposed replacement", Objective: "proposed replacement"}, "proposed-revision")
	if err != nil {
		t.Fatalf("ProposeTaskRevision: %v", err)
	}
	if err := db.AcceptTaskRevision(context.Background(), task.ID, revision.Revision, mission.IntegratorGeneration, "accept-proposed"); err != nil {
		t.Fatalf("AcceptTaskRevision: %v", err)
	}
	afterAccept, err := db.GetMission(context.Background(), mission.ID)
	if err != nil {
		t.Fatalf("GetMission after proposed acceptance: %v", err)
	}
	if afterAccept.AcceptedSetVersion != 0 {
		t.Fatalf("accepted set version after proposed work = %d, want 0", afterAccept.AcceptedSetVersion)
	}
	if err := db.AbandonTask(context.Background(), task.ID, mission.IntegratorGeneration, "abandon-proposed"); err != nil {
		t.Fatalf("AbandonTask: %v", err)
	}
	afterAbandon, err := db.GetMission(context.Background(), mission.ID)
	if err != nil {
		t.Fatalf("GetMission after proposed abandonment: %v", err)
	}
	if afterAbandon.AcceptedSetVersion != 0 {
		t.Fatalf("accepted set version after proposed abandonment = %d, want 0", afterAbandon.AcceptedSetVersion)
	}
	if err := db.AbandonTask(context.Background(), task.ID, mission.IntegratorGeneration, "abandon-proposed"); err != nil {
		t.Fatalf("replay AbandonTask: %v", err)
	}
	replayed, err := db.GetMission(context.Background(), mission.ID)
	if err != nil {
		t.Fatalf("GetMission after proposed abandonment replay: %v", err)
	}
	if replayed.AcceptedSetVersion != 0 {
		t.Fatalf("accepted set version after proposed abandonment replay = %d, want 0", replayed.AcceptedSetVersion)
	}
}

func TestMissionAcceptedSetVersionRejectsStaleAcceptanceAfterCurrentOutputRemoval(t *testing.T) {
	db := openTestDB(t)
	workspace := mustCreateWorkspace(t, db)
	member := mustCreateMember(t, db)
	mission := mustCreateMission(t, db, workspace.ID, member.ID, 4, 8)
	firstTask := mustCreateMissionTask(t, db, mission.ID, "first output")
	firstSubmission := mustSubmitMissionAttempt(t, db, mission, firstTask, "first-output")
	mustAcceptMissionSubmission(t, db, mission, firstSubmission, "accept-first-output")
	oldSetVersion := mission.AcceptedSetVersion

	revision, err := db.ProposeTaskRevision(context.Background(), firstTask.ID, &domain.TaskRevision{Title: "first replacement", Objective: "first replacement"}, "first-replacement-proposal")
	if err != nil {
		t.Fatalf("ProposeTaskRevision: %v", err)
	}
	if err := db.AcceptTaskRevision(context.Background(), firstTask.ID, revision.Revision, mission.IntegratorGeneration, "first-replacement-accept"); err != nil {
		t.Fatalf("AcceptTaskRevision: %v", err)
	}
	current, err := db.GetMission(context.Background(), mission.ID)
	if err != nil {
		t.Fatalf("GetMission: %v", err)
	}
	if current.AcceptedSetVersion != oldSetVersion+1 {
		t.Fatalf("accepted set version = %d, want %d", current.AcceptedSetVersion, oldSetVersion+1)
	}

	secondTask := mustCreateMissionTask(t, db, mission.ID, "second output")
	secondSubmission := mustSubmitMissionAttempt(t, db, current, secondTask, "second-output")
	if _, err := db.AcceptSubmission(context.Background(), secondSubmission.ID, current.CurrentIntegratorRunID, current.IntegratorGeneration, oldSetVersion, "", "accept-stale-output"); !errors.Is(err, ErrMissionStale) {
		t.Fatalf("stale accepted set = %v, want ErrMissionStale", err)
	}
}

func TestMissionAcceptedSetVersionAdvancesOnAbandonmentOfCurrentOutput(t *testing.T) {
	db := openTestDB(t)
	workspace := mustCreateWorkspace(t, db)
	member := mustCreateMember(t, db)
	mission := mustCreateMission(t, db, workspace.ID, member.ID, 4, 8)
	task := mustCreateMissionTask(t, db, mission.ID, "abandoned output")
	submission := mustSubmitMissionAttempt(t, db, mission, task, "abandoned-output")
	mustAcceptMissionSubmission(t, db, mission, submission, "accept-abandoned-output")
	if err := db.AbandonTask(context.Background(), task.ID, mission.IntegratorGeneration, "abandon-output"); err != nil {
		t.Fatalf("AbandonTask: %v", err)
	}
	afterAbandon, err := db.GetMission(context.Background(), mission.ID)
	if err != nil {
		t.Fatalf("GetMission after abandonment: %v", err)
	}
	if afterAbandon.AcceptedSetVersion != 2 {
		t.Fatalf("accepted set version after abandonment = %d, want 2", afterAbandon.AcceptedSetVersion)
	}
	if err := db.AbandonTask(context.Background(), task.ID, mission.IntegratorGeneration, "abandon-output"); err != nil {
		t.Fatalf("replay AbandonTask: %v", err)
	}
	afterReplay, err := db.GetMission(context.Background(), mission.ID)
	if err != nil {
		t.Fatalf("GetMission after abandonment replay: %v", err)
	}
	if afterReplay.AcceptedSetVersion != 2 {
		t.Fatalf("accepted set version after abandonment replay = %d, want 2", afterReplay.AcceptedSetVersion)
	}
}
