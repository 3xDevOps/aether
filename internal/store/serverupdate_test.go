package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestServerUpdateStartsEmpty(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	got, err := db.GetServerUpdate(t.Context())
	if err != nil {
		t.Fatalf("GetServerUpdate: %v", err)
	}
	if got.Pending != nil || got.Last != nil {
		t.Fatalf("fresh database reports %+v, want an empty state", got)
	}
}

func TestPendingServerUpdateReplacesAndClears(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := t.Context()
	at := time.Now().UTC().Truncate(time.Millisecond)

	first := &PendingServerUpdate{Version: "v0.1.0", RequestedBy: "mem_1", RequestedAt: at}
	if err := db.SetPendingServerUpdate(ctx, first); err != nil {
		t.Fatalf("SetPendingServerUpdate: %v", err)
	}
	// A second request replaces the first: one pending update at a time.
	second := &PendingServerUpdate{Version: "v0.2.0", RequestedBy: "mem_2", RequestedAt: at.Add(time.Minute)}
	if err := db.SetPendingServerUpdate(ctx, second); err != nil {
		t.Fatalf("SetPendingServerUpdate replace: %v", err)
	}
	got, err := db.GetServerUpdate(ctx)
	if err != nil {
		t.Fatalf("GetServerUpdate: %v", err)
	}
	if got.Pending == nil || got.Pending.Version != "v0.2.0" || got.Pending.RequestedBy != "mem_2" {
		t.Fatalf("pending = %+v, want v0.2.0 by mem_2", got.Pending)
	}
	if !got.Pending.RequestedAt.Equal(second.RequestedAt) {
		t.Fatalf("requested at = %v, want %v", got.Pending.RequestedAt, second.RequestedAt)
	}

	if cerr := db.SetPendingServerUpdate(ctx, nil); cerr != nil {
		t.Fatalf("clear pending: %v", cerr)
	}
	if got, err = db.GetServerUpdate(ctx); err != nil {
		t.Fatalf("GetServerUpdate after clear: %v", err)
	}
	if got.Pending != nil {
		t.Fatalf("pending = %+v after a cancel, want none", got.Pending)
	}
}

func TestSetLastServerUpdateClearsPending(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := t.Context()
	at := time.Now().UTC().Truncate(time.Millisecond)
	if err := db.SetPendingServerUpdate(ctx, &PendingServerUpdate{
		Version: "v0.2.0", RequestedBy: "mem_1", RequestedAt: at,
	}); err != nil {
		t.Fatalf("SetPendingServerUpdate: %v", err)
	}
	// Recording an outcome retires the pending row in the same write, so
	// the next idle tick cannot retry an attempt that already finished.
	if err := db.SetLastServerUpdate(ctx, ServerUpdateAttempt{
		Version: "v0.2.0", Outcome: ServerUpdateFailed, Detail: "checksum mismatch", At: at,
	}); err != nil {
		t.Fatalf("SetLastServerUpdate: %v", err)
	}
	got, err := db.GetServerUpdate(ctx)
	if err != nil {
		t.Fatalf("GetServerUpdate: %v", err)
	}
	if got.Pending != nil {
		t.Fatalf("pending = %+v, want none after an attempt", got.Pending)
	}
	if got.Last == nil || got.Last.Outcome != ServerUpdateFailed || got.Last.Detail != "checksum mismatch" {
		t.Fatalf("last = %+v, want the failed attempt", got.Last)
	}
	if !got.Last.At.Equal(at) {
		t.Fatalf("last at = %v, want %v", got.Last.At, at)
	}
}

func TestSetPendingServerUpdateRejectsEmptyVersion(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	if err := db.SetPendingServerUpdate(t.Context(), &PendingServerUpdate{RequestedBy: "mem_1"}); err == nil {
		t.Fatal("expected an error for a pending update with no version")
	}
}

// A server that existed before release channels follows beta; a new one
// has not chosen, so it chooses from the pointers at its first check, and
// a choice never replaces a channel already set. The host reads and writes
// the same setting without migrating the database, and reads a database
// from before channels as beta.
func TestReleaseChannelMigration(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	fresh := filepath.Join(t.TempDir(), "aether.db")
	var got string
	db, err := Open(fresh)
	if err != nil {
		t.Fatal(err)
	}
	if got, err = db.ReleaseChannel(ctx); err != nil || got != "" {
		t.Fatalf("new database channel = %q, %v; want none chosen", got, err)
	}
	if got, err = db.ChooseReleaseChannel(ctx, "stable"); err != nil || got != "stable" {
		t.Fatalf("choose on a new database = %q, %v; want stable", got, err)
	}
	if got, err = db.ChooseReleaseChannel(ctx, "beta"); err != nil || got != "stable" {
		t.Fatalf("second choice = %q, %v; want stable kept", got, err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := OpenChannelFile(fresh)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.SetReleaseChannel(ctx, "nightly"); err != nil {
		t.Fatal(err)
	}
	if got, err = f.ReleaseChannel(ctx); err != nil || got != "nightly" {
		t.Fatalf("host channel = %q, %v; want nightly", got, err)
	}
	_ = f.Close()

	for _, schema := range []int{14, releaseChannelSchema - 1} {
		existing := filepath.Join(t.TempDir(), "aether.db")
		raw := openLegacy(t, existing, schema)
		_ = raw.Close()
		f, err = OpenChannelFile(existing)
		if err != nil {
			t.Fatal(err)
		}
		if got, err = f.ReleaseChannel(ctx); err != nil || got != "beta" {
			t.Fatalf("host channel of a v%d database = %q, %v; want beta", schema, got, err)
		}
		if err = f.SetReleaseChannel(ctx, "stable"); err == nil {
			t.Fatalf("host changed the channel of a v%d database without migrating it", schema)
		}
		_ = f.Close()
		db, err = Open(existing)
		if err != nil {
			t.Fatal(err)
		}
		if got, err = db.ReleaseChannel(ctx); err != nil || got != "beta" {
			t.Fatalf("v%d database after migration: channel %q, %v; want beta", schema, got, err)
		}
		if got, err = db.ChooseReleaseChannel(ctx, "stable"); err != nil || got != "beta" {
			t.Fatalf("choice on a migrated v%d database = %q, %v; want beta kept", schema, got, err)
		}
		_ = db.Close()
	}
}

// A retire clears only the pending update it names; one scheduled in
// between stays. The declined build survives in the store and only rises.
func TestRetireClearsOnlyThePendingItNames(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := t.Context()
	at := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	first := PendingServerUpdate{Version: "v0.9.0-nightly.13", RequestedBy: "server", RequestedAt: at}
	second := PendingServerUpdate{Version: "v0.9.0-nightly.14", RequestedBy: "mem_a", RequestedAt: at.Add(time.Minute)}
	if err := db.SetPendingServerUpdate(ctx, &second); err != nil {
		t.Fatal(err)
	}
	retired, err := db.RetirePendingServerUpdate(ctx, first, ServerUpdateAttempt{Version: first.Version, Outcome: ServerUpdateFailed, Detail: "x", At: at})
	if err != nil || retired {
		t.Fatalf("retire of a replaced pending = %v, %v; want nothing retired", retired, err)
	}
	for _, d := range []DeclinedServerUpdate{{Version: "v0.9.0-nightly.13", Build: 413}, {Version: "v0.9.0-nightly.12", Build: 412}} {
		if err = db.RaiseDeclinedServerUpdate(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	if err = db.RaiseDeclinedServerUpdate(ctx, DeclinedServerUpdate{Version: "v0.9.0-nightly.14"}); err == nil {
		t.Fatal("a declined mark without a build was stored")
	}
	got, err := db.GetServerUpdate(ctx)
	if err != nil || got.Pending == nil || got.Pending.Version != second.Version || got.Last != nil || got.Declined == nil || got.Declined.Build != 413 {
		t.Fatalf("state = %+v, %v", got, err)
	}
	if retired, err = db.RetirePendingServerUpdate(ctx, second, ServerUpdateAttempt{Version: second.Version, Outcome: ServerUpdateFailed, Detail: "y", At: at}); err != nil || !retired {
		t.Fatalf("retire = %v, %v; want it retired", retired, err)
	}
}

// A new database stays unchosen however its first migration run went: one
// interrupted early and reopened is still new, so it is not pinned to
// beta, while a database that existed before channels, including one at
// the version just before them, is. Opening again changes nothing.
func TestReleaseChannelOfADatabaseWhoseFirstRunWasInterrupted(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	interrupted := func(t *testing.T, through int) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "aether.db")
		raw, err := openRaw(path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = raw.Close() }()
		if _, err = raw.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
			t.Fatal(err)
		}
		for v := 1; v <= through; v++ {
			if _, err = applyMigration(raw, v, false); err != nil {
				t.Fatal(err)
			}
		}
		return path
	}
	for name, tc := range map[string]struct {
		path func(*testing.T) string
		want string
	}{
		"new database interrupted early":                {func(t *testing.T) string { return interrupted(t, 40) }, ""},
		"new database interrupted just before channels": {func(t *testing.T) string { return interrupted(t, releaseChannelSchema-1) }, ""},
		"new database, one run":                         {func(t *testing.T) string { return filepath.Join(t.TempDir(), "aether.db") }, ""},
		"server from before channels": {func(t *testing.T) string {
			path := filepath.Join(t.TempDir(), "aether.db")
			_ = openLegacy(t, path, releaseChannelSchema-1).Close()
			return path
		}, "beta"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := tc.path(t)
			for range 2 {
				db, err := Open(path)
				if err != nil {
					t.Fatal(err)
				}
				got, err := db.ReleaseChannel(ctx)
				_ = db.Close()
				if err != nil || got != tc.want {
					t.Fatalf("channel = %q, %v; want %q", got, err, tc.want)
				}
			}
		})
	}
}
