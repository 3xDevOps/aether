package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

// mustCreateMission returns an active mission. mission.start, which stands
// between creation and that state, is covered on its own in
// mission_plan_test.go; every other mission test is about what an active
// mission does, so it skips straight past it.
func mustCreateMission(t *testing.T, db *DB, workspace domain.WorkspaceID, member domain.MemberID) *domain.Mission {
	t.Helper()
	m := mustCreatePlanningMission(t, db, workspace, member, "mission-create-1")
	if _, err := db.db.ExecContext(context.Background(), `UPDATE missions SET phase=? WHERE id=?`, domain.MissionPhaseActive, m.ID); err != nil {
		t.Fatalf("start mission: %v", err)
	}
	m.Phase = domain.MissionPhaseActive
	return m
}

func mustCreatePlanningMission(t *testing.T, db *DB, workspace domain.WorkspaceID, member domain.MemberID, key string) *domain.Mission {
	t.Helper()
	m := &domain.Mission{
		WorkspaceID: workspace, Objective: "ship the bounded change", AccountableHumanID: member,
		Integrator:     domain.MissionIntegrator{AccountMemberID: member, Harness: "claude", Mode: domain.LaunchTUI},
		IdempotencyKey: key,
	}
	if err := db.CreateMission(context.Background(), m); err != nil {
		t.Fatalf("CreateMission: %v", err)
	}
	if m.Phase != domain.MissionPhasePlanning {
		t.Fatalf("new mission phase = %s, want planning", m.Phase)
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

func TestMissionAttemptReservationIsIdempotent(t *testing.T) {
	db := openTestDB(t)
	workspace := mustCreateWorkspace(t, db)
	member := mustCreateMember(t, db)
	mission := mustCreateMission(t, db, workspace.ID, member.ID)
	task := mustCreateMissionTask(t, db, mission.ID, "worker")
	const admittedKey = "dispatch-a"
	if _, _, err := reserveMissionAttempt(t, db, mission, task, admittedKey); err != nil {
		t.Fatalf("reserve attempt: %v", err)
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
	if _, _, reserveErr := db.ReserveAttempt(context.Background(), &domain.AttemptReservation{
		MissionID: mission.ID, TaskID: task.ID, TaskRevision: task.CurrentRevision,
		DispatchKey: admittedKey, Harness: "claude", Mode: domain.LaunchHeadless, Model: "opus",
		IntegratorGeneration: mission.IntegratorGeneration,
	}); !errors.Is(reserveErr, ErrMissionIdempotencyConflict) {
		t.Fatalf("replay with another model = %v, want a conflict", reserveErr)
	}
	if _, _, reserveErr := db.ReserveAttempt(context.Background(), &domain.AttemptReservation{
		MissionID: mission.ID, TaskID: task.ID, TaskRevision: task.CurrentRevision,
		DispatchKey: admittedKey, Harness: "claude", Mode: domain.LaunchHeadless, Effort: "high",
		IntegratorGeneration: mission.IntegratorGeneration,
	}); !errors.Is(reserveErr, ErrMissionIdempotencyConflict) {
		t.Fatalf("replay with another effort = %v, want a conflict", reserveErr)
	}
}

func TestMissionEffortSeparatesChoicesAndBindsTheReceipts(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	workspace := mustCreateWorkspace(t, db)
	member := mustCreateMember(t, db)
	newMission := func(integratorEffort, workerEffort string) *domain.Mission {
		return &domain.Mission{
			WorkspaceID: workspace.ID, Objective: "ship", AccountableHumanID: member.ID,
			Integrator: domain.MissionIntegrator{AccountMemberID: member.ID, Harness: "claude", Mode: domain.LaunchTUI, Effort: integratorEffort},
			ExecutionChoices: []domain.MissionExecutionChoice{
				{AccountMemberID: member.ID, Harness: "claude", Mode: domain.LaunchTUI, Effort: "high"},
				{AccountMemberID: member.ID, Harness: "claude", Mode: domain.LaunchTUI, Effort: workerEffort},
			},
			IdempotencyKey: "efforts",
		}
	}
	m := newMission("high", "low")
	if err := db.CreateMission(ctx, m); err != nil {
		t.Fatalf("create with choices differing only in effort: %v", err)
	}
	got, err := db.GetMission(ctx, m.ID)
	if err != nil || got.Integrator.Effort != "high" || len(got.ExecutionChoices) != 2 || got.ExecutionChoices[1].Effort != "low" {
		t.Fatalf("stored mission = %+v, %v", got, err)
	}
	if replayErr := db.CreateMission(ctx, newMission("low", "low")); !errors.Is(replayErr, ErrMissionIdempotencyConflict) {
		t.Fatalf("replay with another integrator effort = %v, want a conflict", replayErr)
	}
	if replayErr := db.CreateMission(ctx, newMission("high", "max")); !errors.Is(replayErr, ErrMissionIdempotencyConflict) {
		t.Fatalf("replay with another worker effort = %v, want a conflict", replayErr)
	}
	if dupErr := db.CreateMission(ctx, newMission("high", "high")); dupErr == nil || !strings.Contains(dupErr.Error(), "duplicate mission execution choice") {
		t.Fatalf("create with two identical choices = %v, want a duplicate refusal", dupErr)
	}

	replacement := domain.MissionIntegrator{AccountMemberID: member.ID, Harness: "claude", Mode: domain.LaunchTUI, Effort: "low"}
	replaced, err := db.ReplaceIntegrator(ctx, m.ID, m.IntegratorGeneration, replacement, member.ID, member.ID, "replace-effort")
	if err != nil {
		t.Fatalf("replace integrator: %v", err)
	}
	if replaced.Integrator.Effort != "low" {
		t.Fatalf("replaced integrator effort = %q, want low", replaced.Integrator.Effort)
	}
	replacement.Effort = "max"
	if _, replayErr := db.ReplaceIntegrator(ctx, m.ID, m.IntegratorGeneration, replacement, member.ID, member.ID, "replace-effort"); !errors.Is(replayErr, ErrMissionIdempotencyConflict) {
		t.Fatalf("replacement replay with another effort = %v, want a conflict", replayErr)
	}

	task := mustCreateMissionTask(t, db, m.ID, "worker")
	if _, execErr := db.db.ExecContext(ctx, `UPDATE missions SET phase=? WHERE id=?`, domain.MissionPhaseActive, m.ID); execErr != nil {
		t.Fatalf("start mission: %v", execErr)
	}
	attempt, _, err := db.ReserveAttempt(ctx, &domain.AttemptReservation{
		MissionID: m.ID, TaskID: task.ID, TaskRevision: task.CurrentRevision,
		DispatchKey: "effort", Harness: "claude", Mode: domain.LaunchHeadless, Effort: "high",
		IntegratorGeneration: replaced.IntegratorGeneration,
	})
	if err != nil {
		t.Fatalf("reserve attempt: %v", err)
	}
	if stored, getErr := db.GetAttempt(ctx, attempt.ID); getErr != nil || stored.Effort != "high" {
		t.Fatalf("stored attempt = %+v, %v; want effort high", stored, getErr)
	}
}

// A receipt written before v93 has no integrator_effort and an
// execution_choices blob without effort keys; a replay without effort must
// still return its swarm.
func TestMissionCreateReceiptFromBeforeEffortStillMatches(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aether.db")
	raw := openLegacy(t, path, 92)
	const choices = `[{"account_member_id":"m1","harness":"claude","mode":"tui","model":"opus"},{"account_member_id":"m1","harness":"codex","mode":"headless"}]`
	for _, seed := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO members (id, display_name, public_key, color, role, created_at) VALUES ('m1', 'Ada', ?, '#e6194b', 'admin', 1)`, []any{testKey(t, "")}},
		{`INSERT INTO workspaces (id, name, created_at) VALUES ('w1', 'proj', 1)`, nil},
		{`INSERT INTO missions (id, workspace_id, objective, accountable_human_id, integrator_account_member_id,
		                        integrator_harness, integrator_mode, integrator_model, execution_choices,
		                        current_integrator_run_id, idempotency_key, created_at, updated_at,
		                        integrator_authorizing_human_id, integrator_run_owner_id)
			VALUES ('mis1', 'w1', 'ship', 'm1', 'm1', 'claude', 'tui', 'opus', ?, 'run1', 'legacy', 1, 1, 'm1', 'm1')`, []any{choices}},
		{`INSERT INTO mission_create_receipts (workspace_id, idempotency_key, mission_id, objective, accountable_human_id,
		                                       integrator_account_member_id, integrator_harness, integrator_mode, integrator_model,
		                                       execution_choices, initial_run_id, created_at)
			VALUES ('w1', 'legacy', 'mis1', 'ship', 'm1', 'm1', 'claude', 'tui', 'opus', ?, 'run1', 1)`, []any{choices}},
	} {
		if _, err := raw.Exec(seed.query, seed.args...); err != nil {
			t.Fatalf("seed v92 swarm: %v", err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open (v93 migration): %v", err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	replay := func(integratorEffort string) *domain.Mission {
		return &domain.Mission{
			WorkspaceID: "w1", Objective: "ship", AccountableHumanID: "m1",
			Integrator: domain.MissionIntegrator{AccountMemberID: "m1", Harness: "claude", Mode: domain.LaunchTUI, Model: "opus", Effort: integratorEffort},
			ExecutionChoices: []domain.MissionExecutionChoice{
				{AccountMemberID: "m1", Harness: "claude", Mode: domain.LaunchTUI, Model: "opus", Effort: integratorEffort},
				{AccountMemberID: "m1", Harness: "codex", Mode: domain.LaunchHeadless},
			},
			IdempotencyKey: "legacy",
		}
	}
	m := replay("")
	if err := db.CreateMission(ctx, m); err != nil || m.ID != "mis1" || m.Integrator.Effort != "" {
		t.Fatalf("replay of a v92 create = %+v, %v; want swarm mis1 without effort", m, err)
	}
	if err := db.CreateMission(ctx, replay("high")); !errors.Is(err, ErrMissionIdempotencyConflict) {
		t.Fatalf("replay of a v92 create with an effort = %v, want a conflict", err)
	}
}

func TestMissionModelSeparatesChoicesAndBindsTheCreateReceipt(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	workspace := mustCreateWorkspace(t, db)
	member := mustCreateMember(t, db)
	newMission := func(integratorModel string) *domain.Mission {
		return &domain.Mission{
			WorkspaceID: workspace.ID, Objective: "ship", AccountableHumanID: member.ID,
			Integrator: domain.MissionIntegrator{AccountMemberID: member.ID, Harness: "claude", Mode: domain.LaunchTUI, Model: integratorModel},
			ExecutionChoices: []domain.MissionExecutionChoice{
				{AccountMemberID: member.ID, Harness: "claude", Mode: domain.LaunchTUI, Model: "opus"},
				{AccountMemberID: member.ID, Harness: "claude", Mode: domain.LaunchTUI, Model: "sonnet"},
			},
			IdempotencyKey: "models",
		}
	}
	m := newMission("opus")
	if err := db.CreateMission(ctx, m); err != nil {
		t.Fatalf("create with choices differing only in model: %v", err)
	}
	got, err := db.GetMission(ctx, m.ID)
	if err != nil || got.Integrator.Model != "opus" || len(got.ExecutionChoices) != 2 || got.ExecutionChoices[1].Model != "sonnet" {
		t.Fatalf("stored mission = %+v, %v", got, err)
	}
	if err := db.CreateMission(ctx, newMission("sonnet")); !errors.Is(err, ErrMissionIdempotencyConflict) {
		t.Fatalf("replay with another integrator model = %v, want a conflict", err)
	}
}

// TestListAttemptsHasNoCap: a mission's attempt count is the integrator's to
// decide, and ending the mission lists every attempt to stop the live ones.
func TestListAttemptsHasNoCap(t *testing.T) {
	db := openTestDB(t)
	workspace := mustCreateWorkspace(t, db)
	member := mustCreateMember(t, db)
	mission := mustCreateMission(t, db, workspace.ID, member.ID)
	task := mustCreateMissionTask(t, db, mission.ID, "worker")
	const want = 1025
	for i := range want {
		if _, _, err := reserveMissionAttempt(t, db, mission, task, fmt.Sprintf("dispatch-%d", i)); err != nil {
			t.Fatalf("reserve attempt %d: %v", i, err)
		}
	}
	attempts, err := db.ListAttempts(context.Background(), mission.ID, "")
	if err != nil || len(attempts) != want {
		t.Fatalf("ListAttempts = %d attempts, %v, want %d", len(attempts), err, want)
	}
}

// TestMissionDependenciesFollowTheDependencyCurrentRevision: a dependency is
// satisfied by an accepted submission of the dependency task's current
// revision, whatever revision existed when it was declared, and a cycle or
// self reference is refused inside the revision's own transaction.
func TestMissionDependenciesFollowTheDependencyCurrentRevision(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	workspace := mustCreateWorkspace(t, db)
	member := mustCreateMember(t, db)
	mission := mustCreateMission(t, db, workspace.ID, member.ID)
	// Both tasks carry an approved revision 1: the integrator may only accept
	// revisions of work a human already approved. The dependency is declared
	// on the proposed revision 2, which acceptance then makes current.
	first := mustCreateMissionTask(t, db, mission.ID, "first")
	second := mustCreateMissionTask(t, db, mission.ID, "second")
	revised, err := db.ProposeTaskRevision(ctx, first.ID, &domain.TaskRevision{Title: "first", Objective: "first", DependsOn: []domain.TaskID{second.ID}}, "first-rev-2")
	if err != nil {
		t.Fatalf("ProposeTaskRevision: %v", err)
	}
	if acceptErr := db.AcceptTaskRevision(ctx, first.ID, revised.Revision, mission.IntegratorGeneration, mission.CurrentIntegratorRunID, "accept-first"); acceptErr != nil {
		t.Fatalf("accept first: %v", acceptErr)
	}
	first.CurrentRevision = revised.Revision
	expectBlocked := func(step string) {
		t.Helper()
		projected, projectErr := db.ProjectTask(ctx, first.ID)
		if projectErr != nil {
			t.Fatalf("%s: ProjectTask: %v", step, projectErr)
		}
		if projected.Status != domain.TaskBlocked || len(projected.Blockers) != 1 || projected.Blockers[0].TaskID != second.ID {
			t.Fatalf("%s: projection = %s %+v, want blocked by %s", step, projected.Status, projected.Blockers, second.ID)
		}
		if _, _, reserveErr := reserveMissionAttempt(t, db, mission, first, "dispatch-"+step); !errors.Is(reserveErr, ErrMissionNotReady) || !strings.Contains(reserveErr.Error(), "task "+string(first.ID)+" waits for task "+string(second.ID)) {
			t.Fatalf("%s: reserve = %v, want ErrMissionNotReady naming %s", step, reserveErr, second.ID)
		}
	}
	expectBlocked("declared")

	mustAcceptMissionSubmission(t, db, mission, mustSubmitMissionAttempt(t, db, mission, second, "second-1"), "accept-second-1")
	projected, err := db.ProjectTask(ctx, first.ID)
	if err != nil {
		t.Fatalf("ProjectTask after acceptance: %v", err)
	}
	if projected.Status != domain.TaskReady || len(projected.Dependencies) != 1 || projected.Dependencies[0].DependsOnRevision != 1 {
		t.Fatalf("projection after acceptance = %s %+v, want ready with the revision 1 dependency row", projected.Status, projected.Dependencies)
	}

	// A new current revision of the dependency has no accepted output yet,
	// so the dependent waits again even though the row still names revision 1.
	secondRevised, err := db.ProposeTaskRevision(ctx, second.ID, &domain.TaskRevision{Title: "second", Objective: "second again"}, "second-rev-2")
	if err != nil {
		t.Fatalf("revise second: %v", err)
	}
	if acceptErr := db.AcceptTaskRevision(ctx, second.ID, secondRevised.Revision, mission.IntegratorGeneration, mission.CurrentIntegratorRunID, "accept-second-rev-2"); acceptErr != nil {
		t.Fatalf("accept second revision 2: %v", acceptErr)
	}
	expectBlocked("revised")

	if _, cycleErr := db.ProposeTaskRevision(ctx, second.ID, &domain.TaskRevision{Title: "second", Objective: "second", DependsOn: []domain.TaskID{first.ID}}, "second-rev-3"); !errors.Is(cycleErr, ErrMissionCycle) || !strings.Contains(cycleErr.Error(), "task "+string(second.ID)+" waits for task "+string(first.ID)+", which waits for task "+string(second.ID)) {
		t.Fatalf("cycle dependency = %v, want ErrMissionCycle naming the cycle", cycleErr)
	}
	if _, selfErr := db.ProposeTaskRevision(ctx, second.ID, &domain.TaskRevision{Title: "second", Objective: "second", DependsOn: []domain.TaskID{second.ID}}, "second-rev-self"); !errors.Is(selfErr, ErrMissionCycle) {
		t.Fatalf("self dependency = %v, want ErrMissionCycle", selfErr)
	}
	reloaded, err := db.GetTask(ctx, second.ID)
	if err != nil || reloaded.CurrentRevision != secondRevised.Revision || reloaded.PendingRevision != nil {
		t.Fatalf("second after refused revisions = %+v (err %v), want no pending revision", reloaded, err)
	}

	// A newer draft supersedes the one before it, so a replaced draft can
	// neither be accepted nor lend its edges to the cycle check.
	firstFree, err := db.ProposeTaskRevision(ctx, first.ID, &domain.TaskRevision{Title: "first", Objective: "first alone"}, "first-rev-3")
	if err != nil {
		t.Fatalf("revise first without dependencies: %v", err)
	}
	if acceptErr := db.AcceptTaskRevision(ctx, first.ID, firstFree.Revision, mission.IntegratorGeneration, mission.CurrentIntegratorRunID, "accept-first-rev-3"); acceptErr != nil {
		t.Fatalf("accept first revision 3: %v", acceptErr)
	}
	stale, err := db.ProposeTaskRevision(ctx, first.ID, &domain.TaskRevision{Title: "first", Objective: "stale draft", DependsOn: []domain.TaskID{second.ID}}, "first-rev-4")
	if err != nil {
		t.Fatalf("propose stale draft: %v", err)
	}
	if _, err := db.ProposeTaskRevision(ctx, first.ID, &domain.TaskRevision{Title: "first", Objective: "latest draft"}, "first-rev-5"); err != nil {
		t.Fatalf("propose latest draft: %v", err)
	}
	if acceptErr := db.AcceptTaskRevision(ctx, first.ID, stale.Revision, mission.IntegratorGeneration, mission.CurrentIntegratorRunID, "accept-stale"); !errors.Is(acceptErr, ErrConflict) {
		t.Fatalf("accept the superseded draft = %v, want ErrConflict", acceptErr)
	}
	if _, err := db.ProposeTaskRevision(ctx, second.ID, &domain.TaskRevision{Title: "second", Objective: "second", DependsOn: []domain.TaskID{first.ID}}, "second-rev-4"); err != nil {
		t.Fatalf("dependency on a task whose stale draft depended back = %v, want accepted", err)
	}
}

func TestMissionAcceptanceRequiresExactEvidenceAndRevision(t *testing.T) {
	db := openTestDB(t)
	workspace := mustCreateWorkspace(t, db)
	member := mustCreateMember(t, db)
	mission := mustCreateMission(t, db, workspace.ID, member.ID)
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
	if _, acceptErr := db.AcceptSubmission(context.Background(), submission.ID, mission.CurrentIntegratorRunID, mission.IntegratorGeneration, mission.AcceptedSetVersion, "", "accept-submission", &SubmissionEvidenceValidation{Ref: submission.Ref, Evidence: submission.Evidence}); !errors.Is(acceptErr, ErrMissionNotReady) {
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
	mission := mustCreateMission(t, db, workspace.ID, member.ID)
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
	mission := mustCreateMission(t, db, workspace.ID, member.ID)
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
	mission := mustCreateMission(t, db, workspace.ID, member.ID)
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

func TestSubmitAttemptKeepsEveryScopeViolation(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	workspace := mustCreateWorkspace(t, db)
	member := mustCreateMember(t, db)
	mission := mustCreateMission(t, db, workspace.ID, member.ID)
	task := mustCreateMissionTask(t, db, mission.ID, "wide change")
	attempt, _, err := reserveMissionAttempt(t, db, mission, task, "wide-change")
	if err != nil {
		t.Fatalf("ReserveAttempt: %v", err)
	}
	run := &domain.Run{ID: attempt.RunID, WorkspaceID: workspace.ID, MemberID: member.ID, Task: task.Revision.Title, Harness: "claude", Mode: domain.LaunchHeadless, Status: domain.RunQueued}
	if createErr := db.CreateRunWithID(ctx, run); createErr != nil {
		t.Fatalf("CreateRunWithID: %v", createErr)
	}
	violations := make([]string, 200)
	for i := range violations {
		violations[i] = fmt.Sprintf("outside/%s/%03d.go", strings.Repeat("d", 400), i)
	}
	submission, err := db.SubmitAttempt(ctx, attempt.ID, attempt.AuthorityGeneration, attempt.IntegratorGeneration, domain.SubmissionRef{
		WorkspaceID: workspace.ID, RunID: attempt.RunID, EvidenceRef: "packet-wide", RetainedRevision: "tree-wide",
	}, []domain.SubmissionEvidence{{Kind: "retained_packet", Ref: "packet-wide", Available: true}}, violations)
	if err != nil {
		t.Fatalf("SubmitAttempt with %d scope violations: %v", len(violations), err)
	}
	stored, err := db.GetSubmission(ctx, submission.ID)
	if err != nil {
		t.Fatalf("GetSubmission: %v", err)
	}
	if !slices.Equal(stored.ScopeViolations, violations) {
		t.Fatalf("stored scope violations = %d paths, want all %d", len(stored.ScopeViolations), len(violations))
	}
	got, err := db.GetAttempt(ctx, attempt.ID)
	if err != nil {
		t.Fatalf("GetAttempt: %v", err)
	}
	if got.State != domain.AttemptSubmitted {
		t.Fatalf("attempt state = %q, want submitted", got.State)
	}
}

func mustAcceptMissionSubmission(t *testing.T, db *DB, mission *domain.Mission, submission *domain.Submission, key string) *domain.Acceptance {
	t.Helper()
	accepted, err := db.AcceptSubmission(context.Background(), submission.ID, mission.CurrentIntegratorRunID, mission.IntegratorGeneration, mission.AcceptedSetVersion, "", key, &SubmissionEvidenceValidation{Ref: submission.Ref, Evidence: submission.Evidence})
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
	mission := mustCreateMission(t, db, workspace.ID, member.ID)
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
	mission := mustCreateMission(t, db, workspace.ID, member.ID)
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
	mission := mustCreateMission(t, db, workspace.ID, member.ID)
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
	if _, staleErr := db.AcceptSubmission(context.Background(), secondSubmission.ID, current.CurrentIntegratorRunID, current.IntegratorGeneration, oldSetVersion, "", "accept-stale-output", &SubmissionEvidenceValidation{Ref: secondSubmission.Ref, Evidence: secondSubmission.Evidence}); !errors.Is(staleErr, ErrMissionStale) {
		t.Fatalf("stale accepted set = %v, want ErrMissionStale", staleErr)
	}
}

func TestMissionAcceptedSetVersionAdvancesOnAbandonmentOfCurrentOutput(t *testing.T) {
	db := openTestDB(t)
	workspace := mustCreateWorkspace(t, db)
	member := mustCreateMember(t, db)
	mission := mustCreateMission(t, db, workspace.ID, member.ID)
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

func TestMissionWorkerHoldGenerationAndRestartRelease(t *testing.T) {
	db := openTestDB(t)
	workspace := mustCreateWorkspace(t, db)
	member := mustCreateMember(t, db)
	mission := mustCreateMission(t, db, workspace.ID, member.ID)
	task := mustCreateMissionTask(t, db, mission.ID, "held worker")
	attempt, _, err := reserveMissionAttempt(t, db, mission, task, "held-worker")
	if err != nil {
		t.Fatalf("ReserveAttempt: %v", err)
	}
	ctx := context.Background()
	held, err := db.SetMissionWorkerTakeover(ctx, attempt.RunID, member.ID, true)
	if err != nil {
		t.Fatalf("take hold: %v", err)
	}
	again, err := db.SetMissionWorkerTakeover(ctx, attempt.RunID, member.ID, true)
	if err != nil || again.Generation != held.Generation || !again.UpdatedAt.Equal(held.UpdatedAt) {
		t.Fatalf("same member retake = %+v, %v; want generation %d since %s", again, err, held.Generation, held.UpdatedAt)
	}
	if shown, getErr := db.GetAttemptByRun(ctx, attempt.RunID); getErr != nil || !shown.TakeoverSince.Equal(held.UpdatedAt) {
		t.Fatalf("attempt while held = %+v, %v; want takeover_since %s", shown, getErr, held.UpdatedAt)
	}
	if _, _, reserveErr := reserveMissionAttempt(t, db, mission, task, "held-worker-again"); !errors.Is(reserveErr, ErrMissionTakeover) ||
		!strings.Contains(reserveErr.Error(), "member "+string(member.ID)+" has held it since ") {
		t.Fatalf("worker.start while held = %v, want ErrMissionTakeover naming %s", reserveErr, member.ID)
	}
	other := mustCreateMember(t, db)
	taken, err := db.SetMissionWorkerTakeover(ctx, attempt.RunID, other.ID, true)
	if err != nil || taken.Generation <= held.Generation {
		t.Fatalf("another member's take = %+v, %v; want a generation past %d", taken, err, held.Generation)
	}
	if err = db.CheckIntegratorInput(ctx, mission.CurrentIntegratorRunID, attempt.RunID, mission.IntegratorGeneration); !errors.Is(err, ErrMissionTakeover) || !strings.Contains(err.Error(), "member "+string(other.ID)+" has held it since ") {
		t.Fatalf("integrator input while held = %v, want ErrMissionTakeover naming %s", err, other.ID)
	}
	if err = db.ReleaseMissionWorkerTakeovers(ctx); err != nil {
		t.Fatalf("release holds at start: %v", err)
	}
	released, err := db.GetMissionWorkerAssignment(ctx, attempt.RunID)
	if err != nil || released.Active || released.Generation <= taken.Generation {
		t.Fatalf("hold after a server start = %+v, %v", released, err)
	}
	if err = db.CheckIntegratorInput(ctx, mission.CurrentIntegratorRunID, attempt.RunID, mission.IntegratorGeneration); err != nil {
		t.Fatalf("integrator input after a server start = %v", err)
	}
}
