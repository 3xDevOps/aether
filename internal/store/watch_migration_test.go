package store

import (
	"path/filepath"
	"testing"
)

func TestDroppingDeliveredWatchConditionsMigrationFromV96(t *testing.T) {
	t.Parallel()
	const previous = 96
	path := filepath.Join(t.TempDir(), "aether.db")
	raw := openLegacy(t, path, previous)
	if _, execErr := raw.Exec(`
		INSERT INTO members (id, display_name, public_key, color, role, created_at)
			VALUES ('m1', 'Ada', ?, '#e6194b', 'admin', 1);
		INSERT INTO watches (id, member_id, target_kind, repo, pr_number, conditions, source, state, created_at, updated_at, expires_at)
			VALUES ('only', 'm1', 'pr', 'o/r', 1, '["delivered"]', 'user', 'active', 1, 1, 9),
			       ('mixed', 'm1', 'pr', 'o/r', 2, '["needs_you","delivered","finished"]', 'user', 'active', 1, 1, 9),
			       ('plain', 'm1', 'pr', 'o/r', 3, '["finished"]', 'user', 'active', 1, 1, 9);
	`, testKey(t, "")); execErr != nil {
		t.Fatalf("seed rows: %v", execErr)
	}
	if closeErr := raw.Close(); closeErr != nil {
		t.Fatalf("close raw: %v", closeErr)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open (migration from v%d): %v", previous, err)
	}
	defer func() { _ = db.Close() }()
	for id, want := range map[string]string{
		"only":  "expired [\"delivered\"]",
		"mixed": "active [\"needs_you\",\"finished\"]",
		"plain": "active [\"finished\"]",
	} {
		var state, conditions string
		if err := db.db.QueryRow(`SELECT state, conditions FROM watches WHERE id = ?`, id).Scan(&state, &conditions); err != nil {
			t.Fatal(err)
		}
		if got := state + " " + conditions; got != want {
			t.Errorf("watch %s = %s, want %s", id, got, want)
		}
	}
}
