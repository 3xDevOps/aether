package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

func mirrorForWorkspace(id domain.WorkspaceID) *domain.WorkspaceMirror {
	return &domain.WorkspaceMirror{
		WorkspaceID:    id,
		SourceURL:      "https://git.example.test/acme/project.git",
		SourceIdentity: "git.example.test/acme/project",
		Branch:         "main",
		Auth:           domain.MirrorAuthPublic,
		Generation:     3,
		Status:         domain.MirrorStatusReady,
		ObservedCommit: strings.Repeat("a", 40),
		AcceptedCommit: strings.Repeat("b", 40),
		KeyFingerprint: "SHA256:ZmFrZUZpbmdlcnByaW50",
		LastError:      "",
		CreatedAt:      time.Date(2026, 9, 10, 12, 0, 0, 123, time.FixedZone("test", 3600)),
		LastAttemptAt:  time.Date(2026, 9, 11, 8, 0, 0, 0, time.FixedZone("test", 3600)),
		LastSuccessAt:  time.Date(2026, 9, 11, 8, 1, 0, 0, time.FixedZone("test", 3600)),
	}
}

func TestWorkspaceMirrorRoundTrip(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	ws := mustCreateWorkspace(t, db)
	want := mirrorForWorkspace(ws.ID)
	if err := db.SetWorkspaceMirror(ctx, want); err != nil {
		t.Fatalf("SetWorkspaceMirror: %v", err)
	}
	got, err := db.GetWorkspaceMirror(ctx, ws.ID)
	if err != nil {
		t.Fatalf("GetWorkspaceMirror: %v", err)
	}
	if got.WorkspaceID != want.WorkspaceID || got.SourceURL != want.SourceURL ||
		got.SourceIdentity != want.SourceIdentity || got.Branch != want.Branch ||
		got.Auth != want.Auth || got.Generation != want.Generation || got.Status != want.Status ||
		got.ObservedCommit != want.ObservedCommit || got.AcceptedCommit != want.AcceptedCommit ||
		got.KeyFingerprint != want.KeyFingerprint || got.LastError != want.LastError ||
		!got.CreatedAt.Equal(want.CreatedAt) || !got.LastAttemptAt.Equal(want.LastAttemptAt) ||
		!got.LastSuccessAt.Equal(want.LastSuccessAt) || got.UpdatedAt.IsZero() {
		t.Fatalf("round trip mismatch:\n got  %+v\n want %+v", got, want)
	}
	if got.CreatedAt.Location() != time.UTC || got.UpdatedAt.Location() != time.UTC ||
		got.LastAttemptAt.Location() != time.UTC || got.LastSuccessAt.Location() != time.UTC {
		t.Fatalf("timestamps are not UTC: %+v", got)
	}
}

func TestWorkspaceMirrorListEmpty(t *testing.T) {
	db := openTestDB(t)
	mirrors, err := db.ListWorkspaceMirrors(context.Background())
	if err != nil {
		t.Fatalf("ListWorkspaceMirrors: %v", err)
	}
	if mirrors == nil {
		t.Fatal("ListWorkspaceMirrors returned nil slice")
	}
	if len(mirrors) != 0 {
		t.Fatalf("ListWorkspaceMirrors returned %d mirrors, want empty", len(mirrors))
	}
}

func TestWorkspaceMirrorListOrderingAndFields(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	firstWorkspace := mustCreateWorkspace(t, db)
	secondWorkspace := mustCreateWorkspace(t, db)
	first := mirrorForWorkspace(firstWorkspace.ID)
	first.SourceIdentity = "git.example.test/acme/first"
	first.Branch = "release/first"
	first.LastError = "first failed"
	second := mirrorForWorkspace(secondWorkspace.ID)
	second.SourceURL = "ssh://git@git.example.test/acme/second.git"
	second.SourceIdentity = "git.example.test/acme/second"
	second.Branch = "release/second"
	second.Auth = domain.MirrorAuthDeployKey
	second.Generation = 7
	second.Status = domain.MirrorStatusRefreshing
	second.ObservedCommit = strings.Repeat("c", 64)
	second.AcceptedCommit = ""
	second.KeyFingerprint = ""
	second.LastError = "refreshing"
	if err := db.SetWorkspaceMirror(ctx, first); err != nil {
		t.Fatalf("SetWorkspaceMirror first: %v", err)
	}
	if err := db.SetWorkspaceMirror(ctx, second); err != nil {
		t.Fatalf("SetWorkspaceMirror second: %v", err)
	}

	got, err := db.ListWorkspaceMirrors(ctx)
	if err != nil {
		t.Fatalf("ListWorkspaceMirrors: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListWorkspaceMirrors returned %d mirrors, want 2", len(got))
	}
	want := []domain.WorkspaceMirror{*first, *second}
	if want[1].WorkspaceID < want[0].WorkspaceID {
		want[0], want[1] = want[1], want[0]
	}
	for i := range want {
		gotMirror := got[i]
		wantMirror := want[i]
		if gotMirror.WorkspaceID != wantMirror.WorkspaceID ||
			gotMirror.SourceURL != wantMirror.SourceURL ||
			gotMirror.SourceIdentity != wantMirror.SourceIdentity ||
			gotMirror.Branch != wantMirror.Branch ||
			gotMirror.Auth != wantMirror.Auth ||
			gotMirror.Generation != wantMirror.Generation ||
			gotMirror.Status != wantMirror.Status ||
			gotMirror.ObservedCommit != wantMirror.ObservedCommit ||
			gotMirror.AcceptedCommit != wantMirror.AcceptedCommit ||
			gotMirror.KeyFingerprint != wantMirror.KeyFingerprint ||
			gotMirror.LastError != wantMirror.LastError ||
			!gotMirror.CreatedAt.Equal(wantMirror.CreatedAt) ||
			!gotMirror.UpdatedAt.Equal(wantMirror.UpdatedAt) ||
			!gotMirror.LastAttemptAt.Equal(wantMirror.LastAttemptAt) ||
			!gotMirror.LastSuccessAt.Equal(wantMirror.LastSuccessAt) {
			t.Fatalf("mirror %d mismatch:\n got  %+v\n want %+v", i, gotMirror, wantMirror)
		}
	}
}

func TestWorkspaceMirrorUpsertChangesResult(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	ws := mustCreateWorkspace(t, db)
	first := mirrorForWorkspace(ws.ID)
	if err := db.SetWorkspaceMirror(ctx, first); err != nil {
		t.Fatalf("first SetWorkspaceMirror: %v", err)
	}
	before, err := db.GetWorkspaceMirror(ctx, ws.ID)
	if err != nil {
		t.Fatalf("first GetWorkspaceMirror: %v", err)
	}

	second := mirrorForWorkspace(ws.ID)
	second.CreatedAt = time.Time{}
	second.SourceURL = "ssh://git@git.example.test/acme/project.git"
	second.SourceIdentity = "git.example.test/acme/project-v2"
	second.Branch = "release/v2"
	second.Auth = domain.MirrorAuthDeployKey
	second.Generation = 4
	second.Status = domain.MirrorStatusRefreshing
	second.ObservedCommit = strings.Repeat("c", 64)
	second.AcceptedCommit = ""
	second.KeyFingerprint = ""
	second.LastError = "fetch in progress"
	second.LastAttemptAt = time.Time{}
	second.LastSuccessAt = time.Time{}
	if setErr := db.SetWorkspaceMirror(ctx, second); setErr != nil {
		t.Fatalf("second SetWorkspaceMirror: %v", setErr)
	}
	after, err := db.GetWorkspaceMirror(ctx, ws.ID)
	if err != nil {
		t.Fatalf("second GetWorkspaceMirror: %v", err)
	}
	if after.CreatedAt != before.CreatedAt {
		t.Fatalf("upsert changed CreatedAt: before %v, after %v", before.CreatedAt, after.CreatedAt)
	}
	if after.SourceURL != second.SourceURL || after.SourceIdentity != second.SourceIdentity ||
		after.Branch != second.Branch || after.Auth != second.Auth || after.Generation != second.Generation ||
		after.Status != second.Status || after.ObservedCommit != second.ObservedCommit ||
		after.AcceptedCommit != second.AcceptedCommit || after.KeyFingerprint != second.KeyFingerprint ||
		after.LastError != second.LastError || !after.LastAttemptAt.IsZero() || !after.LastSuccessAt.IsZero() {
		t.Fatalf("upsert did not replace mutable fields: %+v", after)
	}
	if !after.UpdatedAt.After(before.UpdatedAt) {
		t.Fatalf("upsert did not advance UpdatedAt: before %v, after %v", before.UpdatedAt, after.UpdatedAt)
	}
}

func TestWorkspaceMirrorDeleteAndLocalOnly(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	ws := mustCreateWorkspace(t, db)
	if _, err := db.GetWorkspaceMirror(ctx, ws.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("local-only GetWorkspaceMirror: %v, want ErrNotFound", err)
	}
	if err := db.DeleteWorkspaceMirror(ctx, ws.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("local-only DeleteWorkspaceMirror: %v, want ErrNotFound", err)
	}
	if err := db.SetWorkspaceMirror(ctx, mirrorForWorkspace(ws.ID)); err != nil {
		t.Fatalf("SetWorkspaceMirror: %v", err)
	}
	if err := db.DeleteWorkspaceMirror(ctx, ws.ID); err != nil {
		t.Fatalf("DeleteWorkspaceMirror: %v", err)
	}
	if _, err := db.GetWorkspaceMirror(ctx, ws.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted GetWorkspaceMirror: %v, want ErrNotFound", err)
	}
}

func TestWorkspaceMirrorUnknownWorkspace(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	m := mirrorForWorkspace("missing")
	if err := db.SetWorkspaceMirror(ctx, m); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetWorkspaceMirror unknown workspace: %v, want ErrNotFound", err)
	}
	if _, err := db.GetWorkspaceMirror(ctx, m.WorkspaceID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetWorkspaceMirror unknown workspace: %v, want ErrNotFound", err)
	}
	if err := db.DeleteWorkspaceMirror(ctx, m.WorkspaceID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteWorkspaceMirror unknown workspace: %v, want ErrNotFound", err)
	}
}

func TestWorkspaceMirrorRejectsInvalidData(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	ws := mustCreateWorkspace(t, db)
	base := mirrorForWorkspace(ws.ID)
	cases := []struct {
		name string
		edit func(*domain.WorkspaceMirror)
	}{
		{"source URL", func(m *domain.WorkspaceMirror) { m.SourceURL = "ftp://example.test/repo" }},
		{"source identity", func(m *domain.WorkspaceMirror) { m.SourceIdentity = "" }},
		{"branch", func(m *domain.WorkspaceMirror) { m.Branch = "bad..branch" }},
		{"auth", func(m *domain.WorkspaceMirror) { m.Auth = "token" }},
		{"generation", func(m *domain.WorkspaceMirror) { m.Generation = 0 }},
		{"status", func(m *domain.WorkspaceMirror) { m.Status = "unknown" }},
		{"observed SHA", func(m *domain.WorkspaceMirror) { m.ObservedCommit = "not-a-sha" }},
		{"accepted SHA", func(m *domain.WorkspaceMirror) { m.AcceptedCommit = strings.Repeat("g", 40) }},
		{"fingerprint", func(m *domain.WorkspaceMirror) { m.KeyFingerprint = "MD5:bad" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := *base
			tc.edit(&m)
			if err := db.SetWorkspaceMirror(ctx, &m); err == nil {
				t.Fatal("SetWorkspaceMirror accepted invalid data")
			}
		})
	}
}

func TestWorkspaceMirrorMigrationFromV24(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aether.db")
	raw := openLegacy(t, path, 24)
	if _, seedErr := raw.Exec(`INSERT INTO workspaces
		(id, name, environment, base_branch, steer_others, origin, created_at)
		VALUES ('w1', 'legacy', '{}', 'main', '', '', 1)`); seedErr != nil {
		if closeErr := raw.Close(); closeErr != nil {
			t.Logf("close v24 database after seed failure: %v", closeErr)
		}
		t.Fatalf("seed v24 workspace: %v", seedErr)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close v24 database: %v", err)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open migrated v24 database: %v", err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.GetWorkspaceMirror(context.Background(), "w1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("migrated workspace mirror: %v, want ErrNotFound", err)
	}
	var version int
	if err := db.db.QueryRow(`SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if version != 26 {
		t.Fatalf("schema version = %d, want 26", version)
	}
	var columns string
	if err := db.db.QueryRow(`SELECT group_concat(name, ',') FROM pragma_table_info('workspace_mirrors')`).Scan(&columns); err != nil {
		t.Fatalf("inspect mirror columns: %v", err)
	}
	if strings.Contains(columns, "private_key") || strings.Contains(columns, "private_key_path") {
		t.Fatalf("mirror schema persists private key material: %s", columns)
	}
	if err := db.SetWorkspaceMirror(context.Background(), mirrorForWorkspace("w1")); err != nil {
		t.Fatalf("set migrated workspace mirror: %v", err)
	}
	if err := db.DeleteWorkspace(context.Background(), "w1"); err != nil {
		t.Fatalf("delete workspace: %v", err)
	}
	var mirrors int
	if err := db.db.QueryRow(`SELECT count(*) FROM workspace_mirrors WHERE workspace_id = 'w1'`).Scan(&mirrors); err != nil {
		t.Fatalf("inspect cascading mirror delete: %v", err)
	}
	if mirrors != 0 {
		t.Fatalf("workspace mirror survived workspace delete: %d rows", mirrors)
	}
}
