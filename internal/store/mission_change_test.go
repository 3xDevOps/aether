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

func TestMissionChangeCountsEveryChangeAndListsOthers(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	workspace := mustCreateWorkspace(t, db)
	member := mustCreateMember(t, db)
	mission := mustCreateMission(t, db, workspace.ID, member.ID)
	record := func(kind domain.MissionChange, by domain.RunID) {
		t.Helper()
		if err := db.RecordMissionChange(ctx, mission.ID, kind, by); err != nil {
			t.Fatalf("RecordMissionChange(%s): %v", kind, err)
		}
	}
	record(domain.MissionQuestionAsked, mission.CurrentIntegratorRunID)
	record(domain.MissionQuestionAnswered, "")
	record(domain.MissionWorkerReport, "run_worker")
	record(domain.MissionWorkerReport, "run_worker")

	got, err := db.GetMission(ctx, mission.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := map[domain.MissionChange]uint64{domain.MissionQuestionAnswered: 2, domain.MissionWorkerReport: 4}
	if got.ChangeSeq != 4 || !maps.Equal(got.Changes, want) {
		t.Fatalf("change_seq %d, changes %v; want 4, %v", got.ChangeSeq, got.Changes, want)
	}
	if err := db.RecordMissionChange(ctx, "mis_missing", domain.MissionWorkerReport, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing mission: %v, want ErrNotFound", err)
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
	if recordErr := db.RecordMissionChange(ctx, "mis1", domain.MissionQuestionAnswered, ""); recordErr != nil {
		t.Fatal(recordErr)
	}
	if m, err = db.GetMission(ctx, "mis1"); err != nil || m.ChangeSeq != 1 || m.Changes[domain.MissionQuestionAnswered] != 1 {
		t.Fatalf("after one change = %+v, %v", m, err)
	}
}
