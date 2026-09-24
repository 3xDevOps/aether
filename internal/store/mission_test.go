package store

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

// mustCreateMission returns a mission whose plan a human already approved.
// The gate that stands between creation and that state is covered on its own
// in mission_plan_test.go; every other mission test is about what an approved
// mission does, so it skips straight past the gate.
func mustCreateMission(t *testing.T, db *DB, workspace domain.WorkspaceID, member domain.MemberID, concurrent, total int) *domain.Mission {
	t.Helper()
	m := mustCreatePlanningMission(t, db, workspace, member, concurrent, total, "mission-create-1")
	if _, err := db.db.ExecContext(context.Background(), `UPDATE missions SET phase=?, plan_version=1 WHERE id=?`, domain.MissionPhaseActive, m.ID); err != nil {
		t.Fatalf("approve mission plan: %v", err)
	}
	m.Phase, m.PlanVersion = domain.MissionPhaseActive, 1
	return m
}

func mustCreatePlanningMission(t *testing.T, db *DB, workspace domain.WorkspaceID, member domain.MemberID, concurrent, total int, key string) *domain.Mission {
	t.Helper()
	m := &domain.Mission{
		WorkspaceID: workspace, Objective: "ship the bounded change", AccountableHumanID: member,
		Integrator:            domain.MissionIntegrator{AccountMemberID: member, Harness: "claude", Mode: domain.LaunchTUI},
		MaxConcurrentAttempts: concurrent, MaxTotalAttempts: total, IdempotencyKey: key,
	}
	if err := db.CreateMission(context.Background(), m); err != nil {
		t.Fatalf("CreateMission: %v", err)
	}
	if m.Phase != domain.MissionPhasePlanning || m.PlanVersion != 0 {
		t.Fatalf("new mission = phase %s plan version %d, want planning and 0", m.Phase, m.PlanVersion)
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
	type reservationResult struct {
		key string
		err error
	}
	results := make(chan reservationResult, 2)
	for _, key := range []string{"dispatch-a", "dispatch-b"} {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			_, _, err := reserveMissionAttempt(t, db, mission, task, key)
			results <- reservationResult{key: key, err: err}
		}(key)
	}
	wg.Wait()
	close(results)
	var admitted, limited int
	var admittedKey string
	for result := range results {
		if result.err == nil {
			admitted++
			admittedKey = result.key
		} else if errors.Is(result.err, ErrMissionLimit) {
			limited++
		} else {
			t.Fatalf("reserve concurrent attempt: %v", result.err)
		}
	}
	if admitted != 1 || limited != 1 {
		t.Fatalf("concurrent admission = admitted %d limited %d, want one each", admitted, limited)
	}

	attempt, replay, err := reserveMissionAttempt(t, db, mission, task, admittedKey)
	if err != nil || !replay {
		t.Fatalf("idempotent replay = %+v, replay %v, err %v", attempt, replay, err)
	}
	if _, _, reserveErr := db.ReserveAttempt(context.Background(), &domain.AttemptReservation{
		MissionID: mission.ID, TaskID: task.ID, TaskRevision: task.CurrentRevision,
		DispatchKey: admittedKey, Harness: "codex", Mode: domain.LaunchHeadless,
		IntegratorGeneration: mission.IntegratorGeneration,
	}); !errors.Is(reserveErr, ErrMissionIdempotencyConflict) {
		t.Fatalf("semantic replay conflict = %v", reserveErr)
	}
	if updateErr := db.UpdateAttemptState(context.Background(), attempt.ID, attempt.RunID, attempt.AuthorityGeneration, attempt.IntegratorGeneration, domain.AttemptFailed, ""); updateErr != nil {
		t.Fatalf("settle first attempt: %v", updateErr)
	}
	if _, _, reserveErr := reserveMissionAttempt(t, db, mission, task, "dispatch-c"); reserveErr != nil {
		t.Fatalf("retry within total allowance: %v", reserveErr)
	}
	if _, _, reserveErr := reserveMissionAttempt(t, db, mission, task, "dispatch-d"); !errors.Is(reserveErr, ErrMissionLimit) {
		t.Fatalf("total allowance = %v, want ErrMissionLimit", reserveErr)
	}
}

func TestMissionDependenciesRejectCyclesAndProjectBlocked(t *testing.T) {
	db := openTestDB(t)
	workspace := mustCreateWorkspace(t, db)
	member := mustCreateMember(t, db)
	mission := mustCreateMission(t, db, workspace.ID, member.ID, 2, 4)
	// Both tasks carry an approved revision 1: the integrator may only accept
	// revisions of work a human already approved. Dependencies are declared on
	// the proposed revision 2, which acceptance then makes current.
	first := mustCreateMissionTask(t, db, mission.ID, "first")
	second := mustCreateMissionTask(t, db, mission.ID, "second")
	revised, err := db.ProposeTaskRevision(context.Background(), first.ID, &domain.TaskRevision{Title: "first", Objective: "first"}, "first-rev-2")
	if err != nil {
		t.Fatalf("ProposeTaskRevision: %v", err)
	}
	if dependencyErr := db.SetTaskDependencies(context.Background(), first.ID, revised.Revision, []domain.TaskDependency{{TaskID: first.ID, Revision: revised.Revision, DependsOnTaskID: second.ID, DependsOnRevision: 1}}, "dep-a"); dependencyErr != nil {
		t.Fatalf("set dependency: %v", dependencyErr)
	}
	if acceptErr := db.AcceptTaskRevision(context.Background(), first.ID, revised.Revision, mission.IntegratorGeneration, mission.CurrentIntegratorRunID, "accept-first"); acceptErr != nil {
		t.Fatalf("accept first: %v", acceptErr)
	}
	projected, projectErr := db.ProjectTask(context.Background(), first.ID)
	if projectErr != nil {
		t.Fatalf("ProjectTask: %v", projectErr)
	}
	if projected.Status != domain.TaskBlocked || len(projected.Blockers) != 1 {
		t.Fatalf("blocked projection = %+v, want one dependency blocker", projected)
	}
	cycleA := &domain.Task{MissionID: mission.ID, Revision: &domain.TaskRevision{Title: "cycle-a", Objective: "cycle-a", Status: domain.TaskRevisionProposed}}
	cycleB := &domain.Task{MissionID: mission.ID, Revision: &domain.TaskRevision{Title: "cycle-b", Objective: "cycle-b", Status: domain.TaskRevisionProposed}}
	if cycleAErr := db.CreateTask(context.Background(), cycleA); cycleAErr != nil {
		t.Fatalf("create cycle A: %v", cycleAErr)
	}
	if cycleBErr := db.CreateTask(context.Background(), cycleB); cycleBErr != nil {
		t.Fatalf("create cycle B: %v", cycleBErr)
	}
	if dependencyErr := db.SetTaskDependencies(context.Background(), cycleA.ID, 1, []domain.TaskDependency{{TaskID: cycleA.ID, Revision: 1, DependsOnTaskID: cycleB.ID, DependsOnRevision: 1}}, "dep-cycle-a"); dependencyErr != nil {
		t.Fatalf("set cycle A dependency: %v", dependencyErr)
	}
	if cycleErr := db.SetTaskDependencies(context.Background(), cycleB.ID, 1, []domain.TaskDependency{{TaskID: cycleB.ID, Revision: 1, DependsOnTaskID: cycleA.ID, DependsOnRevision: 1}}, "dep-cycle-b"); !errors.Is(cycleErr, ErrMissionCycle) {
		t.Fatalf("cycle dependency = %v, want ErrMissionCycle", cycleErr)
	}
}

func TestMissionAcceptanceRequiresExactEvidenceAndRevision(t *testing.T) {
	db := openTestDB(t)
	workspace := mustCreateWorkspace(t, db)
	member := mustCreateMember(t, db)
	mission := mustCreateMission(t, db, workspace.ID, member.ID, 1, 2)
	task := &domain.Task{MissionID: mission.ID, Revision: &domain.TaskRevision{Title: "verify", Objective: "verify", Status: domain.TaskRevisionAccepted, EvidenceRequirements: []domain.EvidenceRequirement{{Kind: "test"}}}}
	if createErr := db.CreateTask(context.Background(), task); createErr != nil {
		t.Fatalf("CreateTask: %v", createErr)
	}
	attempt, _, err := reserveMissionAttempt(t, db, mission, task, "evidence-dispatch")
	if err != nil {
		t.Fatalf("ReserveAttempt: %v", err)
	}
	reservedRun := &domain.Run{ID: attempt.RunID, WorkspaceID: workspace.ID, MemberID: member.ID, Task: task.Revision.Title, Harness: "claude", Mode: domain.LaunchHeadless, Status: domain.RunQueued}
	if runErr := db.CreateRunWithID(context.Background(), reservedRun); runErr != nil {
		t.Fatalf("CreateRunWithID: %v", runErr)
	}
	submission, err := db.SubmitAttempt(context.Background(), attempt.ID, attempt.AuthorityGeneration, attempt.IntegratorGeneration, domain.SubmissionRef{WorkspaceID: workspace.ID, RunID: attempt.RunID, EvidenceRef: "packet-1", RetainedRevision: "tree-1"}, []domain.SubmissionEvidence{{Kind: "test", Ref: "packet-1", Available: false}}, nil)
	if err != nil {
		t.Fatalf("SubmitAttempt: %v", err)
	}
	if _, acceptErr := db.AcceptSubmission(context.Background(), submission.ID, mission.CurrentIntegratorRunID, mission.IntegratorGeneration, mission.AcceptedSetVersion, "", "accept-submission"); !errors.Is(acceptErr, ErrMissionNotReady) {
		t.Fatalf("accept missing evidence = %v, want ErrMissionNotReady", acceptErr)
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
	if acceptErr := db.AcceptTaskRevision(context.Background(), task.ID, third.Revision, mission.IntegratorGeneration, mission.CurrentIntegratorRunID, "accept-3"); acceptErr != nil {
		t.Fatalf("accept third: %v", acceptErr)
	}
	if staleErr := db.AcceptTaskRevision(context.Background(), task.ID, second.Revision, mission.IntegratorGeneration, mission.CurrentIntegratorRunID, "accept-2"); !errors.Is(staleErr, ErrMissionStale) {
		t.Fatalf("accept older revision: %v, want ErrMissionStale", staleErr)
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
	if _, lookupErr := db.GetMissionByRun(context.Background(), initialRun); !errors.Is(lookupErr, ErrMissionStale) {
		t.Fatalf("initial run lookup = %v, want ErrMissionStale", lookupErr)
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
	defer func() { _ = db.Close() }()
	workspace := mustCreateWorkspace(t, db)
	member := mustCreateMember(t, db)
	mission := mustCreateMission(t, db, workspace.ID, member.ID, 1, 2)
	task := mustCreateMissionTask(t, db, mission.ID, "control outbox")
	attempt, _, err := reserveMissionAttempt(t, db, mission, task, "control-outbox")
	if err != nil {
		t.Fatalf("ReserveAttempt: %v", err)
	}
	ctx := context.Background()
	if _, takeoverErr := db.SetMissionWorkerTakeover(ctx, attempt.RunID, member.ID, true); takeoverErr != nil {
		t.Fatalf("set takeover: %v", takeoverErr)
	}
	first, err := db.PendingMissionControlChange(ctx, mission.ID)
	if err != nil {
		t.Fatalf("pending first change: %v", err)
	}
	if first == 0 {
		t.Fatal("pending first change = 0, want nonzero generation")
	}
	if _, clearErr := db.SetMissionWorkerTakeover(ctx, attempt.RunID, member.ID, false); clearErr != nil {
		t.Fatalf("clear takeover: %v", clearErr)
	}
	second, err := db.PendingMissionControlChange(ctx, mission.ID)
	if err != nil {
		t.Fatalf("pending coalesced change: %v", err)
	}
	if second <= first {
		t.Fatalf("coalesced generation = %d, want > %d", second, first)
	}
	if ackErr := db.AckMissionControlChange(ctx, mission.ID, first); ackErr != nil {
		t.Fatalf("ack first generation: %v", ackErr)
	}
	pending, err := db.PendingMissionControlChange(ctx, mission.ID)
	if err != nil {
		t.Fatalf("pending after old ack: %v", err)
	}
	if pending != second {
		t.Fatalf("pending after old ack = %d, want newer %d", pending, second)
	}
	if closeErr := db.Close(); closeErr != nil {
		t.Fatalf("close before reopen: %v", closeErr)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	pending, err = reopened.PendingMissionControlChange(ctx, mission.ID)
	if err != nil {
		t.Fatalf("pending after reopen: %v", err)
	}
	if pending != second {
		t.Fatalf("pending after reopen = %d, want newer %d", pending, second)
	}
	if ackErr := reopened.AckMissionControlChange(ctx, mission.ID, second); ackErr != nil {
		t.Fatalf("ack latest generation: %v", ackErr)
	}
	if pending, pendingErr := reopened.PendingMissionControlChange(ctx, mission.ID); pendingErr != nil {
		t.Fatalf("pending after latest ack: %v", pendingErr)
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
	run := &domain.Run{ID: attempt.RunID, WorkspaceID: mission.WorkspaceID, MemberID: mission.AccountableHumanID, Task: task.Revision.Title, Harness: "claude", Mode: domain.LaunchHeadless, Status: domain.RunQueued}
	if createErr := db.CreateRunWithID(context.Background(), run); createErr != nil {
		t.Fatalf("CreateRunWithID: %v", createErr)
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
	if acceptErr := db.AcceptTaskRevision(context.Background(), task.ID, revision.Revision, mission.IntegratorGeneration, mission.CurrentIntegratorRunID, "replacement-accept"); acceptErr != nil {
		t.Fatalf("AcceptTaskRevision: %v", acceptErr)
	}
	afterReplacement, err := db.GetMission(context.Background(), mission.ID)
	if err != nil {
		t.Fatalf("GetMission after replacement: %v", err)
	}
	if afterReplacement.AcceptedSetVersion != 2 {
		t.Fatalf("accepted set version after replacement = %d, want 2", afterReplacement.AcceptedSetVersion)
	}
	var historical int
	if countErr := db.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM mission_acceptances WHERE mission_id=? AND task_id=? AND task_revision=?`, mission.ID, task.ID, 1).Scan(&historical); countErr != nil {
		t.Fatalf("count historical acceptance: %v", countErr)
	}
	if historical != 1 {
		t.Fatalf("historical acceptance count = %d, want 1", historical)
	}

	if replayAcceptErr := db.AcceptTaskRevision(context.Background(), task.ID, revision.Revision, mission.IntegratorGeneration, mission.CurrentIntegratorRunID, "replacement-accept"); replayAcceptErr != nil {
		t.Fatalf("replay AcceptTaskRevision: %v", replayAcceptErr)
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
	// The task's approved revision produced no output, so replacing it takes
	// nothing out of the current accepted set.
	task := mustCreateMissionTask(t, db, mission.ID, "proposed")
	revision, err := db.ProposeTaskRevision(context.Background(), task.ID, &domain.TaskRevision{Title: "proposed replacement", Objective: "proposed replacement"}, "proposed-revision")
	if err != nil {
		t.Fatalf("ProposeTaskRevision: %v", err)
	}
	if acceptErr := db.AcceptTaskRevision(context.Background(), task.ID, revision.Revision, mission.IntegratorGeneration, mission.CurrentIntegratorRunID, "accept-proposed"); acceptErr != nil {
		t.Fatalf("AcceptTaskRevision: %v", acceptErr)
	}
	afterAccept, err := db.GetMission(context.Background(), mission.ID)
	if err != nil {
		t.Fatalf("GetMission after proposed acceptance: %v", err)
	}
	if afterAccept.AcceptedSetVersion != 0 {
		t.Fatalf("accepted set version after proposed work = %d, want 0", afterAccept.AcceptedSetVersion)
	}
	if abandonErr := db.AbandonTask(context.Background(), task.ID, 0, mission.IntegratorGeneration, "abandon-proposed"); abandonErr != nil {
		t.Fatalf("AbandonTask: %v", abandonErr)
	}
	afterAbandon, err := db.GetMission(context.Background(), mission.ID)
	if err != nil {
		t.Fatalf("GetMission after proposed abandonment: %v", err)
	}
	if afterAbandon.AcceptedSetVersion != 0 {
		t.Fatalf("accepted set version after proposed abandonment = %d, want 0", afterAbandon.AcceptedSetVersion)
	}
	if replayAbandonErr := db.AbandonTask(context.Background(), task.ID, 0, mission.IntegratorGeneration, "abandon-proposed"); replayAbandonErr != nil {
		t.Fatalf("replay AbandonTask: %v", replayAbandonErr)
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
	if acceptErr := db.AcceptTaskRevision(context.Background(), firstTask.ID, revision.Revision, mission.IntegratorGeneration, mission.CurrentIntegratorRunID, "first-replacement-accept"); acceptErr != nil {
		t.Fatalf("AcceptTaskRevision: %v", acceptErr)
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
	if _, staleErr := db.AcceptSubmission(context.Background(), secondSubmission.ID, current.CurrentIntegratorRunID, current.IntegratorGeneration, oldSetVersion, "", "accept-stale-output"); !errors.Is(staleErr, ErrMissionStale) {
		t.Fatalf("stale accepted set = %v, want ErrMissionStale", staleErr)
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
	if abandonErr := db.AbandonTask(context.Background(), task.ID, 0, mission.IntegratorGeneration, "abandon-output"); abandonErr != nil {
		t.Fatalf("AbandonTask: %v", abandonErr)
	}
	afterAbandon, err := db.GetMission(context.Background(), mission.ID)
	if err != nil {
		t.Fatalf("GetMission after abandonment: %v", err)
	}
	if afterAbandon.AcceptedSetVersion != 2 {
		t.Fatalf("accepted set version after abandonment = %d, want 2", afterAbandon.AcceptedSetVersion)
	}
	if replayAbandonErr := db.AbandonTask(context.Background(), task.ID, 0, mission.IntegratorGeneration, "abandon-output"); replayAbandonErr != nil {
		t.Fatalf("replay AbandonTask: %v", replayAbandonErr)
	}
	afterReplay, err := db.GetMission(context.Background(), mission.ID)
	if err != nil {
		t.Fatalf("GetMission after abandonment replay: %v", err)
	}
	if afterReplay.AcceptedSetVersion != 2 {
		t.Fatalf("accepted set version after abandonment replay = %d, want 2", afterReplay.AcceptedSetVersion)
	}
}
