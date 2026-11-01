package store

import (
	"path/filepath"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

func TestChildRunsOnByDefaultMigrationKeepsAnExplicitOff(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "aether.db")
	raw := openLegacy(t, path, 95)
	if _, err := raw.Exec(`
		INSERT INTO members (id, display_name, public_key, color, role, created_at)
			VALUES ('snoozed', 'Ada', ?, '#e6194b', 'admin', 1),
			       ('chose-off', 'Grace', ?, '#3cb44b', 'collaborator', 1);
		INSERT INTO child_settings (member_id, enabled, configured, nudge_snoozes, updated_at)
			VALUES ('snoozed', 0, 0, 2, 1), ('chose-off', 0, 1, 0, 1);
	`, testKey(t, "snoozed"), testKey(t, "chose-off")); err != nil {
		_ = raw.Close()
		t.Fatalf("seed child settings: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	for member, want := range map[domain.MemberID]bool{"snoozed": true, "chose-off": false, "never-saved": true} {
		got, err := db.ChildSettings(t.Context(), member)
		if err != nil || got.Enabled != want {
			t.Fatalf("settings of %s = %+v, %v; want enabled %v", member, got, err, want)
		}
	}
}
