package store

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// templatesSchemaVersion is the migration slot the templates and
// schedules tables occupy; the upgrade test builds the schema one version
// behind it.
const templatesSchemaVersion = 7

// TestTemplateAndScheduleRoundTrip covers the persistence contract:
// save-as-create, save-as-replace keeping identity, the one-schedule-per-
// template rule, the fired stamp, and the cascade that takes a schedule
// down with its template.
func TestTemplateAndScheduleRoundTrip(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	w := mustCreateWorkspace(t, db)
	m := mustCreateMember(t, db)

	tpl := &Template{
		WorkspaceID: w.ID, Name: "nightly-deps", Task: "upgrade {{ecosystem}} deps",
		Harness: "claude", Mode: domain.LaunchHeadless,
		Params: map[string]string{"ecosystem": "go"}, BudgetUSD: 2.50,
	}
	if err := db.SaveTemplate(ctx, tpl); err != nil {
		t.Fatalf("SaveTemplate: %v", err)
	}
	if tpl.ID == "" || tpl.CreatedAt.IsZero() {
		t.Fatalf("SaveTemplate did not stamp the row: %+v", tpl)
	}

	replacement := &Template{
		WorkspaceID: w.ID, Name: "nightly-deps", Task: "upgrade {{ecosystem}} deps and run tests",
		Harness: "claude", Mode: domain.LaunchHeadless,
		Params: map[string]string{"ecosystem": "npm"},
	}
	if err := db.SaveTemplate(ctx, replacement); err != nil {
		t.Fatalf("SaveTemplate (replace): %v", err)
	}
	if replacement.ID != tpl.ID || !replacement.CreatedAt.Equal(tpl.CreatedAt) {
		t.Fatalf("replace changed identity: %+v, want ID %s", replacement, tpl.ID)
	}

	got, err := db.GetTemplate(ctx, w.ID, "nightly-deps")
	if err != nil {
		t.Fatalf("GetTemplate: %v", err)
	}
	if got.Task != replacement.Task || got.Params["ecosystem"] != "npm" || got.BudgetUSD != 0 {
		t.Fatalf("template after replace = %+v, want the replacement's fields", got)
	}
	list, err := db.ListTemplates(ctx, w.ID)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListTemplates = %+v (err %v), want one", list, err)
	}

	sched := &Schedule{TemplateID: tpl.ID, Cron: "0 3 * * *", MemberID: m.ID}
	if err = db.SaveSchedule(ctx, sched); err != nil {
		t.Fatalf("SaveSchedule: %v", err)
	}
	fired := time.Now().UTC().Truncate(time.Second)
	if err = db.MarkScheduleFired(ctx, sched.ID, fired); err != nil {
		t.Fatalf("MarkScheduleFired: %v", err)
	}
	schedules, err := db.ListSchedules(ctx, w.ID)
	if err != nil || len(schedules) != 1 {
		t.Fatalf("ListSchedules = %+v (err %v), want one", schedules, err)
	}
	if schedules[0].LastFiredAt == nil || !schedules[0].LastFiredAt.Equal(fired) {
		t.Fatalf("last fired = %v, want %v", schedules[0].LastFiredAt, fired)
	}

	// Replacing the rule keeps one schedule and resets its firing state.
	again := &Schedule{TemplateID: tpl.ID, Cron: "30 2 * * *", MemberID: m.ID}
	if err = db.SaveSchedule(ctx, again); err != nil {
		t.Fatalf("SaveSchedule (replace): %v", err)
	}
	if schedules, err = db.ListSchedules(ctx, ""); err != nil || len(schedules) != 1 {
		t.Fatalf("ListSchedules(all) = %+v (err %v), want one", schedules, err)
	}
	if schedules[0].Cron != "30 2 * * *" || schedules[0].LastFiredAt != nil {
		t.Fatalf("replaced schedule = %+v, want the new cron and no fired stamp", schedules[0])
	}

	if err = db.SaveSchedule(ctx, &Schedule{TemplateID: "tpl_missing", Cron: "* * * * *", MemberID: m.ID}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("schedule for missing template = %v, want ErrNotFound", err)
	}

	if err = db.DeleteTemplate(ctx, w.ID, "nightly-deps"); err != nil {
		t.Fatalf("DeleteTemplate: %v", err)
	}
	if schedules, err = db.ListSchedules(ctx, ""); err != nil || len(schedules) != 0 {
		t.Fatalf("schedules after template delete = %+v (err %v), want none", schedules, err)
	}
	if _, err := db.GetTemplate(ctx, w.ID, "nightly-deps"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetTemplate after delete = %v, want ErrNotFound", err)
	}
	if err := db.DeleteTemplate(ctx, w.ID, "nightly-deps"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second DeleteTemplate = %v, want ErrNotFound", err)
	}
}

// TestTemplatesMigrationUpgradesPreviousVersion builds a database one
// schema version behind the templates slot, seeds rows, then opens it:
// the upgrade must add templates and schedules without losing anything.
func TestTemplatesMigrationUpgradesPreviousVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aether.db")
	raw, err := sql.Open("sqlite", "file:"+url.PathEscape(path)+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if _, execErr := raw.Exec(`CREATE TABLE schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at INTEGER NOT NULL
	)`); execErr != nil {
		t.Fatalf("create schema_migrations: %v", execErr)
	}
	for v := 1; v < templatesSchemaVersion; v++ {
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
		INSERT INTO workspaces (id, name, image, env, setup_script, created_at)
			VALUES ('w1', 'proj', 'img', '{}', '', 1);
		INSERT INTO sessions (id, workspace_id, name, base_branch, created_at)
			VALUES ('s1', 'w1', 'effort', 'main', 1);
	`, testKey(t, "")); execErr != nil {
		t.Fatalf("seed rows: %v", execErr)
	}
	if closeErr := raw.Close(); closeErr != nil {
		t.Fatalf("close raw: %v", closeErr)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open (templates migration): %v", err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()

	if _, err := db.GetWorkspace(ctx, "w1"); err != nil {
		t.Fatalf("GetWorkspace after migration: %v", err)
	}
	tpl := &Template{WorkspaceID: "w1", Name: "nightly", Task: "sweep", Harness: "claude", Mode: domain.LaunchHeadless}
	if err := db.SaveTemplate(ctx, tpl); err != nil {
		t.Fatalf("SaveTemplate after migration: %v", err)
	}
	if err := db.SaveSchedule(ctx, &Schedule{TemplateID: tpl.ID, Cron: "0 3 * * *", MemberID: "m1"}); err != nil {
		t.Fatalf("SaveSchedule after migration: %v", err)
	}
}
