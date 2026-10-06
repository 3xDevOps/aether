package store

import (
	"context"
	"database/sql"
	"errors"
	"maps"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

func TestMissionChangeCountsEachMutationOnceAndListsOthers(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	workspace := mustCreateWorkspace(t, db)
	member := mustCreateMember(t, db)
	mission := mustCreatePlanningMission(t, db, workspace.ID, member.ID, "mission-change")
	integrator := mission.CurrentIntegratorRunID
	expect := func(seq uint64, want map[domain.MissionChange]uint64) {
		t.Helper()
		got, err := db.GetMission(ctx, mission.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.ChangeSeq != seq || !maps.Equal(got.Changes, want) {
			t.Fatalf("change_seq %d, changes %v; want %d, %v", got.ChangeSeq, got.Changes, seq, want)
		}
	}

	question, err := db.InsertMissionQuestion(ctx, mission.ID, integrator, "which login flow?", "ask")
	if err != nil {
		t.Fatal(err)
	}
	expect(1, map[domain.MissionChange]uint64{})
	for range 2 {
		if _, err := db.AnswerMissionQuestion(ctx, question.ID, member.ID, "the SSO one", "answer"); err != nil {
			t.Fatal(err)
		}
	}
	expect(2, map[domain.MissionChange]uint64{domain.MissionQuestionAnswered: 2})
	task := &domain.Task{MissionID: mission.ID, Revision: &domain.TaskRevision{Title: "split", Objective: "split", ProposedByRunID: "run_worker"}}
	if _, _, err := db.CreateTaskWithIdempotency(ctx, task, "propose"); err != nil {
		t.Fatal(err)
	}
	expect(3, map[domain.MissionChange]uint64{domain.MissionQuestionAnswered: 2, domain.MissionTaskProposed: 3})
}

func TestMissionChangeListsAWorkerEndOnlyWhenTheIntegratorDidNotCancelIt(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	workspace := mustCreateWorkspace(t, db)
	member := mustCreateMember(t, db)
	mission := mustCreateMission(t, db, workspace.ID, member.ID)
	cancelled, _, err := reserveMissionAttempt(t, db, mission, mustCreateMissionTask(t, db, mission.ID, "cancelled"), "dispatch-cancelled")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, cancelErr := db.RequestAttemptCancellation(ctx, cancelled.ID, mission.CurrentIntegratorRunID, mission.IntegratorGeneration, "cancel"); cancelErr != nil {
		t.Fatal(cancelErr)
	}
	if endErr := db.EndObservedAttempt(ctx, cancelled.ID, cancelled.RunID, cancelled.AuthorityGeneration, cancelled.IntegratorGeneration, domain.AttemptCancelled, "killed"); endErr != nil {
		t.Fatal(endErr)
	}
	failed, _, err := reserveMissionAttempt(t, db, mission, mustCreateMissionTask(t, db, mission.ID, "failed"), "dispatch-failed")
	if err != nil {
		t.Fatal(err)
	}
	if endErr := db.EndObservedAttempt(ctx, failed.ID, failed.RunID, failed.AuthorityGeneration, failed.IntegratorGeneration, domain.AttemptFailed, "exited"); endErr != nil {
		t.Fatal(endErr)
	}
	if endErr := db.EndObservedAttempt(ctx, failed.ID, failed.RunID, failed.AuthorityGeneration, failed.IntegratorGeneration, domain.AttemptFailed, "exited"); !errors.Is(endErr, ErrMissionStale) {
		t.Fatalf("ending an ended attempt: %v, want ErrMissionStale", endErr)
	}
	got, err := db.GetMission(ctx, mission.ID)
	if err != nil {
		t.Fatal(err)
	}
	if want := map[domain.MissionChange]uint64{domain.MissionWorkerEnded: 2}; got.ChangeSeq != 2 || !maps.Equal(got.Changes, want) {
		t.Fatalf("change_seq %d, changes %v; want 2, %v", got.ChangeSeq, got.Changes, want)
	}
}

func TestMissionChangeMigrationStartsExistingSwarmsAtZero(t *testing.T) {
	const changeVersion = 53
	path := filepath.Join(t.TempDir(), "aether.db")
	raw, err := sql.Open("sqlite", "file:"+url.PathEscape(path)+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if _, execErr := raw.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`); execErr != nil {
		t.Fatalf("create schema_migrations: %v", execErr)
	}
	for v := 1; v < changeVersion; v++ {
		if _, execErr := raw.Exec(migrations[v-1]); execErr != nil {
			t.Fatalf("apply v%d: %v", v, execErr)
		}
		if _, execErr := raw.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, 0)`, v); execErr != nil {
			t.Fatalf("record v%d: %v", v, execErr)
		}
	}
	if _, execErr := raw.Exec(`
		INSERT INTO members (id, display_name, public_key, color, role, created_at)
			VALUES ('m1', 'Ada', ?, '#e6194b', 'admin', 1);
		INSERT INTO workspaces (id, name, created_at, environment, base_branch, steer_others, origin)
			VALUES ('w1', 'proj', 1, '{}', 'main', '', '');
		INSERT INTO missions (id, workspace_id, objective, accountable_human_id, current_integrator_run_id, idempotency_key, created_at, updated_at)
			VALUES ('mis1', 'w1', 'ship it', 'm1', 'run_int', 'k1', 1, 1);
	`, testKey(t, "")); execErr != nil {
		t.Fatalf("seed rows: %v", execErr)
	}
	if closeErr := raw.Close(); closeErr != nil {
		t.Fatalf("close raw: %v", closeErr)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open (change counter migration): %v", err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	m, err := db.GetMission(ctx, "mis1")
	if err != nil || m.ChangeSeq != 0 || len(m.Changes) != 0 {
		t.Fatalf("migrated mission = %+v, %v; want no changes", m, err)
	}
	if _, cancelErr := db.CancelMission(ctx, "mis1", "m1", "cancel"); cancelErr != nil {
		t.Fatal(cancelErr)
	}
	if m, err = db.GetMission(ctx, "mis1"); err != nil || m.ChangeSeq != 1 || m.Changes[domain.MissionPhaseChanged] != 1 {
		t.Fatalf("after one change = %+v, %v", m, err)
	}
}
