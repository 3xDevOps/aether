package store

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
)

// testKey returns a fresh valid authorized_keys line, with an optional
// comment appended.
func testKey(t *testing.T, comment string) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("ssh public key: %v", err)
	}
	line := strings.TrimSuffix(string(ssh.MarshalAuthorizedKey(sshPub)), "\n")
	if comment != "" {
		line += " " + comment
	}
	return line
}

func openTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "aether.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return db
}

func mustCreateWorkspace(t *testing.T, db *DB) *domain.Workspace {
	t.Helper()
	w := &domain.Workspace{
		Name: "aether",
		Environment: domain.WorkspaceEnvironment{
			CustomImage: "ghcr.io/3xdevops/aether-dev:latest",
			Variables:   map[string]string{"GOFLAGS": "-trimpath", "TZ": "UTC"},
			SetupPolicy: domain.SetupPolicy{Script: "make deps\n"},
		},
	}
	if err := db.CreateWorkspace(context.Background(), w); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	return w
}

func mustCreateSession(t *testing.T, db *DB, wid domain.WorkspaceID) *domain.Session {
	t.Helper()
	s := &domain.Session{WorkspaceID: wid, Name: "auth-fix", BaseBranch: "main"}
	if err := db.CreateSession(context.Background(), s); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return s
}

func mustCreateMember(t *testing.T, db *DB) *domain.Member {
	t.Helper()
	m := &domain.Member{
		DisplayName: "Ada",
		PublicKey:   testKey(t, "ada@laptop"),
		Color:       "#e6194b",
		Role:        domain.RoleCollaborator,
	}
	if err := db.CreateMember(context.Background(), m); err != nil {
		t.Fatalf("CreateMember: %v", err)
	}
	return m
}

func mustCreateRun(t *testing.T, db *DB, sid domain.SessionID, mid domain.MemberID, status domain.RunStatus) *domain.Run {
	t.Helper()
	r := &domain.Run{
		SessionID: sid,
		MemberID:  mid,
		Task:      "fix the auth bug",
		Harness:   "claude",
		Mode:      domain.LaunchTUI,
		Status:    status,
	}
	if err := db.CreateRun(context.Background(), r); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	return r
}

func TestNewID(t *testing.T) {
	seen := make(map[string]bool)
	for range 1000 {
		id, err := newID()
		if err != nil {
			t.Fatalf("newID: %v", err)
		}
		if len(id) != 26 {
			t.Fatalf("id %q: want 26 chars, got %d", id, len(id))
		}
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
}

func TestMigrationIdempotency(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aether.db")

	db, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	w := mustCreateWorkspace(t, db)
	if merr := migrate(db.db); merr != nil {
		t.Fatalf("re-running migrate on live DB: %v", merr)
	}
	if cerr := db.Close(); cerr != nil {
		t.Fatalf("Close: %v", cerr)
	}

	db2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen existing DB: %v", err)
	}
	defer func() { _ = db2.Close() }()
	got, err := db2.GetWorkspace(context.Background(), w.ID)
	if err != nil {
		t.Fatalf("GetWorkspace after reopen: %v", err)
	}
	if got.Name != w.Name {
		t.Fatalf("workspace lost across reopen: got %+v", got)
	}

	var count int
	if err = db2.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if count != len(migrations) {
		t.Fatalf("schema_migrations rows = %d, want %d", count, len(migrations))
	}
}

func TestMigrateRejectsNewerSchema(t *testing.T) {
	db := openTestDB(t)
	if _, err := db.db.Exec(
		`INSERT INTO schema_migrations (version, applied_at) VALUES (?, 0)`,
		len(migrations)+7,
	); err != nil {
		t.Fatalf("insert future version: %v", err)
	}
	if err := migrate(db.db); err == nil {
		t.Fatal("migrate accepted a schema newer than the binary")
	}
}

func TestWorkspaceCRUD(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	w := mustCreateWorkspace(t, db)
	if w.ID == "" {
		t.Fatal("CreateWorkspace did not assign an ID")
	}
	if w.CreatedAt.IsZero() || w.CreatedAt.Location() != time.UTC {
		t.Fatalf("CreatedAt not defaulted to UTC now: %v", w.CreatedAt)
	}

	got, err := db.GetWorkspace(ctx, w.ID)
	if err != nil {
		t.Fatalf("GetWorkspace: %v", err)
	}
	assertWorkspaceEqual(t, w, got)

	w.Name = "aether-v2"
	w.Environment = domain.WorkspaceEnvironment{
		CustomImage: "alpine:3.20",
		Variables:   map[string]string{"A": "1"},
		SetupPolicy: domain.SetupPolicy{Script: "true"},
	}
	if uerr := db.UpdateWorkspace(ctx, w); uerr != nil {
		t.Fatalf("UpdateWorkspace: %v", uerr)
	}
	got, err = db.GetWorkspace(ctx, w.ID)
	if err != nil {
		t.Fatalf("GetWorkspace after update: %v", err)
	}
	assertWorkspaceEqual(t, w, got)

	list, err := db.ListWorkspaces(ctx)
	if err != nil {
		t.Fatalf("ListWorkspaces: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("ListWorkspaces len = %d, want 1", len(list))
	}

	if err := db.DeleteWorkspace(ctx, w.ID); err != nil {
		t.Fatalf("DeleteWorkspace: %v", err)
	}
	if _, err := db.GetWorkspace(ctx, w.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetWorkspace after delete: %v, want ErrNotFound", err)
	}
	if err := db.DeleteWorkspace(ctx, w.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second DeleteWorkspace: %v, want ErrNotFound", err)
	}
	if err := db.UpdateWorkspace(ctx, w); !errors.Is(err, ErrNotFound) {
		t.Fatalf("UpdateWorkspace on missing row: %v, want ErrNotFound", err)
	}
}

func TestWorkspaceNilEnvRoundTrip(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	w := &domain.Workspace{Name: "bare", Environment: domain.WorkspaceEnvironment{CustomImage: "alpine"}}
	if err := db.CreateWorkspace(ctx, w); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	got, err := db.GetWorkspace(ctx, w.ID)
	if err != nil {
		t.Fatalf("GetWorkspace: %v", err)
	}
	if got.Environment.Variables != nil {
		t.Fatalf("nil Variables round-tripped as %#v", got.Environment.Variables)
	}
}

func TestWorkspaceEnvironmentAcceptsNumericNeutralImage(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	w := mustCreateWorkspace(t, db)
	if _, err := db.db.ExecContext(ctx, `UPDATE workspaces SET environment = ? WHERE id = ?`,
		`{"custom_image":"","neutral_image":1,"variables":{},"setup_policy":{"script":""}}`, w.ID); err != nil {
		t.Fatalf("seed legacy environment: %v", err)
	}

	got, err := db.GetWorkspace(ctx, w.ID)
	if err != nil {
		t.Fatalf("GetWorkspace: %v", err)
	}
	if !got.Environment.NeutralImage {
		t.Fatal("numeric neutral_image was not decoded as true")
	}
}

func TestWorkspaceEnvironmentMarshalUsesCanonicalBoolean(t *testing.T) {
	data, err := json.Marshal(domain.WorkspaceEnvironment{NeutralImage: true})
	if err != nil {
		t.Fatalf("marshal environment: %v", err)
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatalf("decode marshaled environment: %v", err)
	}
	if got := string(fields["neutral_image"]); got != "true" {
		t.Fatalf("neutral_image JSON = %s, want true", got)
	}
}

func TestSessionCRUDAndQueries(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	w1 := mustCreateWorkspace(t, db)
	w2 := mustCreateWorkspace(t, db)
	s1 := mustCreateSession(t, db, w1.ID)
	s2 := mustCreateSession(t, db, w1.ID)
	mustCreateSession(t, db, w2.ID)

	got, err := db.GetSession(ctx, s1.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if *got != *s1 {
		t.Fatalf("session round-trip: got %+v, want %+v", got, s1)
	}

	s1.Name = "renamed"
	s1.BaseBranch = "develop"
	if uerr := db.UpdateSession(ctx, s1); uerr != nil {
		t.Fatalf("UpdateSession: %v", uerr)
	}
	got, err = db.GetSession(ctx, s1.ID)
	if err != nil {
		t.Fatalf("GetSession after update: %v", err)
	}
	if *got != *s1 {
		t.Fatalf("session update round-trip: got %+v, want %+v", got, s1)
	}

	all, err := db.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("ListSessions len = %d, want 3", len(all))
	}

	byWS, err := db.ListSessionsByWorkspace(ctx, w1.ID)
	if err != nil {
		t.Fatalf("ListSessionsByWorkspace: %v", err)
	}
	if len(byWS) != 2 {
		t.Fatalf("ListSessionsByWorkspace len = %d, want 2", len(byWS))
	}
	for _, s := range byWS {
		if s.WorkspaceID != w1.ID {
			t.Fatalf("session %s has workspace %s, want %s", s.ID, s.WorkspaceID, w1.ID)
		}
	}

	if err := db.DeleteSession(ctx, s2.ID); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if _, err := db.GetSession(ctx, s2.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetSession after delete: %v, want ErrNotFound", err)
	}
}

func TestMemberCRUDAndPublicKeyLookup(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	key := testKey(t, "ada@laptop")
	m := &domain.Member{
		DisplayName: "Ada",
		PublicKey:   key,
		Color:       "#e6194b",
		Role:        domain.RoleCollaborator,
	}
	if err := db.CreateMember(ctx, m); err != nil {
		t.Fatalf("CreateMember: %v", err)
	}

	got, err := db.GetMember(ctx, m.ID)
	if err != nil {
		t.Fatalf("GetMember: %v", err)
	}
	if *got != *m {
		t.Fatalf("member round-trip: got %+v, want %+v", got, m)
	}

	byKey, err := db.GetMemberByPublicKey(ctx, key)
	if err != nil {
		t.Fatalf("GetMemberByPublicKey: %v", err)
	}
	if byKey.ID != m.ID {
		t.Fatalf("GetMemberByPublicKey returned %s, want %s", byKey.ID, m.ID)
	}
	if _, kerr := db.GetMemberByPublicKey(ctx, testKey(t, "")); !errors.Is(kerr, ErrNotFound) {
		t.Fatalf("GetMemberByPublicKey for unknown key: %v, want ErrNotFound", kerr)
	}

	m.DisplayName = "Ada Lovelace"
	m.Color = "#3cb44b"
	m.Role = domain.RoleAdmin
	if uerr := db.UpdateMember(ctx, m); uerr != nil {
		t.Fatalf("UpdateMember: %v", uerr)
	}
	got, err = db.GetMember(ctx, m.ID)
	if err != nil {
		t.Fatalf("GetMember after update: %v", err)
	}
	if *got != *m {
		t.Fatalf("member update round-trip: got %+v, want %+v", got, m)
	}

	list, err := db.ListMembers(ctx)
	if err != nil {
		t.Fatalf("ListMembers: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("ListMembers len = %d, want 1", len(list))
	}

	if err := db.DeleteMember(ctx, m.ID); err != nil {
		t.Fatalf("DeleteMember: %v", err)
	}
	if _, err := db.GetMember(ctx, m.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetMember after delete: %v, want ErrNotFound", err)
	}
}

func TestMemberInvalidRoleRejected(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	m := &domain.Member{DisplayName: "X", PublicKey: testKey(t, ""), Color: "#fff", Role: "superuser"}
	if err := db.CreateMember(ctx, m); err == nil {
		t.Fatal("CreateMember accepted invalid role")
	}
	good := mustCreateMember(t, db)
	good.Role = "root"
	if err := db.UpdateMember(ctx, good); err == nil {
		t.Fatal("UpdateMember accepted invalid role")
	}
}

func TestMemberInvalidPublicKeyRejected(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	m := &domain.Member{DisplayName: "X", PublicKey: "not a key", Color: "#fff", Role: domain.RoleViewer}
	if err := db.CreateMember(ctx, m); err == nil {
		t.Fatal("CreateMember accepted a malformed public key")
	}
	if _, err := db.GetMemberByPublicKey(ctx, "not a key"); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("GetMemberByPublicKey with malformed key: %v, want parse error", err)
	}
}

func TestMemberPublicKeyNormalization(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	bare := testKey(t, "")
	m := &domain.Member{DisplayName: "Ada", PublicKey: bare + " ada@laptop", Color: "#e6194b", Role: domain.RoleAdmin}
	if err := db.CreateMember(ctx, m); err != nil {
		t.Fatalf("CreateMember: %v", err)
	}
	if m.PublicKey != bare {
		t.Fatalf("stored PublicKey = %q, want normalized %q", m.PublicKey, bare)
	}

	for _, lookup := range []string{bare, bare + " ada@desktop", "  " + bare + " x\n"} {
		got, err := db.GetMemberByPublicKey(ctx, lookup)
		if err != nil {
			t.Fatalf("GetMemberByPublicKey(%q): %v", lookup, err)
		}
		if got.ID != m.ID {
			t.Fatalf("GetMemberByPublicKey(%q) returned %s, want %s", lookup, got.ID, m.ID)
		}
	}

	dup := &domain.Member{DisplayName: "B", PublicKey: bare + " other@host", Color: "#000", Role: domain.RoleViewer}
	if err := db.CreateMember(ctx, dup); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate key with different comment: %v, want ErrConflict", err)
	}
}

func TestMemberPublicKeyUnique(t *testing.T) {
	db := openTestDB(t)
	m := mustCreateMember(t, db)
	dup := &domain.Member{DisplayName: "B", PublicKey: m.PublicKey, Color: "#000", Role: domain.RoleViewer}
	if err := db.CreateMember(context.Background(), dup); !errors.Is(err, ErrConflict) {
		t.Fatalf("CreateMember with duplicate public key: %v, want ErrConflict", err)
	}
}

func TestMemberTailnetIdentity(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	m := &domain.Member{
		DisplayName:  "Bob",
		TailnetLogin: "bob@example.com",
		Pending:      true,
		Color:        "#3cb44b",
		Role:         domain.RoleCollaborator,
	}
	if err := db.CreateMember(ctx, m); err != nil {
		t.Fatalf("CreateMember without key: %v", err)
	}

	got, err := db.GetMemberByTailnetLogin(ctx, "bob@example.com")
	if err != nil {
		t.Fatalf("GetMemberByTailnetLogin: %v", err)
	}
	if *got != *m {
		t.Fatalf("tailnet member round-trip: got %+v, want %+v", got, m)
	}
	if _, lookupErr := db.GetMemberByTailnetLogin(ctx, "nobody@example.com"); !errors.Is(lookupErr, ErrNotFound) {
		t.Fatalf("unknown login: %v, want ErrNotFound", lookupErr)
	}

	// A second key-less tailnet member coexists (no false key conflict).
	m2 := &domain.Member{
		DisplayName: "Carol", TailnetLogin: "carol@example.com",
		Color: "#ffe119", Role: domain.RoleViewer,
	}
	if createErr := db.CreateMember(ctx, m2); createErr != nil {
		t.Fatalf("second key-less member: %v", createErr)
	}

	// Duplicate tailnet login conflicts.
	dup := &domain.Member{
		DisplayName: "Imposter", TailnetLogin: "bob@example.com",
		Color: "#000", Role: domain.RoleViewer,
	}
	if dupErr := db.CreateMember(ctx, dup); !errors.Is(dupErr, ErrConflict) {
		t.Fatalf("duplicate tailnet login: %v, want ErrConflict", dupErr)
	}

	// Both identities empty is rejected.
	empty := &domain.Member{DisplayName: "Ghost", Color: "#000", Role: domain.RoleViewer}
	if emptyErr := db.CreateMember(ctx, empty); emptyErr == nil {
		t.Fatal("CreateMember accepted a member with no identity at all")
	}

	if approveErr := db.ApproveMember(ctx, m.ID); approveErr != nil {
		t.Fatalf("ApproveMember: %v", approveErr)
	}
	got, err = db.GetMember(ctx, m.ID)
	if err != nil {
		t.Fatalf("GetMember after approve: %v", err)
	}
	if got.Pending {
		t.Error("member still pending after ApproveMember")
	}
	if got.TailnetLogin != m.TailnetLogin || got.DisplayName != m.DisplayName {
		t.Errorf("ApproveMember touched other fields: %+v", got)
	}
	if err := db.ApproveMember(ctx, "m_missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ApproveMember unknown id: %v, want ErrNotFound", err)
	}
}

func TestMemberTailnetMigrationPreservesRows(t *testing.T) {
	// Build a genuine v1 database (only migration 1 applied, old members
	// schema with the inline UNIQUE public_key), seed a member and a run
	// referencing it, then Open: v2 must rebuild the members table
	// without losing rows or breaking runs.member_id references.
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
	if _, execErr := raw.Exec(migrations[0]); execErr != nil {
		t.Fatalf("apply v1: %v", execErr)
	}
	if _, execErr := raw.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (1, 0)`); execErr != nil {
		t.Fatalf("record v1: %v", execErr)
	}
	key := testKey(t, "")
	if _, execErr := raw.Exec(`INSERT INTO members (id, display_name, public_key, color, role, created_at)
		VALUES ('m1', 'Ada', ?, '#e6194b', 'admin', 1)`, key); execErr != nil {
		t.Fatalf("seed member: %v", execErr)
	}
	if _, execErr := raw.Exec(`
		INSERT INTO workspaces (id, name, image, env, setup_script, created_at)
			VALUES ('w1', 'proj', 'img', '{}', '', 1);
		INSERT INTO sessions (id, workspace_id, name, base_branch, created_at)
			VALUES ('s1', 'w1', 'effort', 'main', 1);
		INSERT INTO runs (id, session_id, member_id, task, harness, mode, status, branch, worktree, created_at)
			VALUES ('r1', 's1', 'm1', 'task', 'claude', 'tui', 'running', '', '', 1);
	`); execErr != nil {
		t.Fatalf("seed run: %v", execErr)
	}
	if closeErr := raw.Close(); closeErr != nil {
		t.Fatalf("close raw: %v", closeErr)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open (v2 migration): %v", err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	got, err := db.GetMemberByPublicKey(ctx, key)
	if err != nil {
		t.Fatalf("GetMemberByPublicKey after migration: %v", err)
	}
	if got.ID != "m1" || got.TailnetLogin != "" || got.Pending || got.DisplayName != "Ada" {
		t.Fatalf("migrated member = %+v", got)
	}
	r, err := db.GetRun(ctx, "r1")
	if err != nil {
		t.Fatalf("GetRun after migration: %v", err)
	}
	if r.MemberID != "m1" {
		t.Fatalf("run.member_id = %s, want m1", r.MemberID)
	}
	// The rebuilt table accepts key-less tailnet members.
	tm := &domain.Member{DisplayName: "Bob", TailnetLogin: "bob@ts.net", Color: "#3cb44b", Role: domain.RoleViewer}
	if err := db.CreateMember(ctx, tm); err != nil {
		t.Fatalf("post-migration tailnet member: %v", err)
	}
}

func TestRunCRUDRoundTripsEveryField(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	w := mustCreateWorkspace(t, db)
	s := mustCreateSession(t, db, w.ID)
	m := mustCreateMember(t, db)

	r := mustCreateRun(t, db, s.ID, m.ID, domain.RunQueued)
	if r.ID == "" {
		t.Fatal("CreateRun did not assign an ID")
	}
	r.Branch = "aether/run-" + string(r.ID) + "-auth-fix"
	r.Worktree = "/var/lib/aether/worktrees/" + string(r.ID)
	if err := db.UpdateRun(ctx, r); err != nil {
		t.Fatalf("UpdateRun (branch/worktree): %v", err)
	}

	got, err := db.GetRun(ctx, r.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	assertRunEqual(t, r, got)
	if got.StartedAt != nil || got.FinishedAt != nil {
		t.Fatalf("nil times round-tripped non-nil: %+v", got)
	}

	started := time.Date(2026, 8, 9, 10, 30, 0, 123456789, time.UTC)
	finished := started.Add(42 * time.Minute)
	r.Status = domain.RunMerged
	r.Mode = domain.LaunchHeadless
	r.Task = "updated task"
	r.Protected = true
	r.Harness = "codex"
	r.ToolSnapshotID = "tools-123"
	r.StartedAt = &started
	r.FinishedAt = &finished
	if uerr := db.UpdateRun(ctx, r); uerr != nil {
		t.Fatalf("UpdateRun: %v", uerr)
	}
	got, err = db.GetRun(ctx, r.ID)
	if err != nil {
		t.Fatalf("GetRun after update: %v", err)
	}
	assertRunEqual(t, r, got)

	if err := db.DeleteRun(ctx, r.ID); err != nil {
		t.Fatalf("DeleteRun: %v", err)
	}
	if _, err := db.GetRun(ctx, r.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetRun after delete: %v, want ErrNotFound", err)
	}
	if err := db.UpdateRun(ctx, r); !errors.Is(err, ErrNotFound) {
		t.Fatalf("UpdateRun on missing row: %v, want ErrNotFound", err)
	}
}

func TestRunQueries(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	w := mustCreateWorkspace(t, db)
	s1 := mustCreateSession(t, db, w.ID)
	s2 := mustCreateSession(t, db, w.ID)
	ada := mustCreateMember(t, db)
	bob := mustCreateMember(t, db)

	r1 := mustCreateRun(t, db, s1.ID, ada.ID, domain.RunRunning)
	r2 := mustCreateRun(t, db, s1.ID, bob.ID, domain.RunNeedsAttention)
	r3 := mustCreateRun(t, db, s2.ID, ada.ID, domain.RunMerged)
	mustCreateRun(t, db, s2.ID, bob.ID, domain.RunFailed)

	bySession, err := db.ListRunsBySession(ctx, s1.ID)
	if err != nil {
		t.Fatalf("ListRunsBySession: %v", err)
	}
	if len(bySession) != 2 {
		t.Fatalf("ListRunsBySession len = %d, want 2", len(bySession))
	}

	byMember, err := db.ListRunsByMember(ctx, ada.ID)
	if err != nil {
		t.Fatalf("ListRunsByMember: %v", err)
	}
	if len(byMember) != 2 {
		t.Fatalf("ListRunsByMember len = %d, want 2", len(byMember))
	}
	ids := map[domain.RunID]bool{}
	for _, r := range byMember {
		ids[r.ID] = true
	}
	if !ids[r1.ID] || !ids[r3.ID] {
		t.Fatalf("ListRunsByMember returned wrong runs: %v", ids)
	}

	active, err := db.ListActiveRuns(ctx)
	if err != nil {
		t.Fatalf("ListActiveRuns: %v", err)
	}
	if len(active) != 2 {
		t.Fatalf("ListActiveRuns len = %d, want 2", len(active))
	}
	for _, r := range active {
		if r.Status.Terminal() {
			t.Fatalf("ListActiveRuns returned terminal run %s (%s)", r.ID, r.Status)
		}
		if r.ID != r1.ID && r.ID != r2.ID {
			t.Fatalf("ListActiveRuns returned unexpected run %s", r.ID)
		}
	}
}

func TestRunInvalidStatusAndModeRejected(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	w := mustCreateWorkspace(t, db)
	s := mustCreateSession(t, db, w.ID)
	m := mustCreateMember(t, db)

	bad := &domain.Run{SessionID: s.ID, MemberID: m.ID, Mode: domain.LaunchTUI, Status: "exploded"}
	if err := db.CreateRun(ctx, bad); err == nil {
		t.Fatal("CreateRun accepted invalid status")
	}
	bad = &domain.Run{SessionID: s.ID, MemberID: m.ID, Mode: "vr", Status: domain.RunQueued}
	if err := db.CreateRun(ctx, bad); err == nil {
		t.Fatal("CreateRun accepted invalid launch mode")
	}

	r := mustCreateRun(t, db, s.ID, m.ID, domain.RunQueued)
	r.Status = "warp-drive"
	if err := db.UpdateRun(ctx, r); err == nil {
		t.Fatal("UpdateRun accepted invalid status")
	}
}

func TestForeignKeyEnforcement(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	s := &domain.Session{WorkspaceID: "no-such-workspace", Name: "x", BaseBranch: "main"}
	if err := db.CreateSession(ctx, s); !errors.Is(err, ErrNotFound) {
		t.Fatalf("CreateSession with missing workspace FK: %v, want ErrNotFound", err)
	}

	w := mustCreateWorkspace(t, db)
	sess := mustCreateSession(t, db, w.ID)
	m := mustCreateMember(t, db)

	r := &domain.Run{SessionID: "no-such-session", MemberID: m.ID, Mode: domain.LaunchTUI, Status: domain.RunQueued}
	if err := db.CreateRun(ctx, r); !errors.Is(err, ErrNotFound) {
		t.Fatalf("CreateRun with missing session FK: %v, want ErrNotFound", err)
	}
	r = &domain.Run{SessionID: sess.ID, MemberID: "no-such-member", Mode: domain.LaunchTUI, Status: domain.RunQueued}
	if err := db.CreateRun(ctx, r); !errors.Is(err, ErrNotFound) {
		t.Fatalf("CreateRun with missing member FK: %v, want ErrNotFound", err)
	}

	mustCreateRun(t, db, sess.ID, m.ID, domain.RunQueued)
	if err := db.DeleteWorkspace(ctx, w.ID); !errors.Is(err, ErrInUse) {
		t.Fatalf("DeleteWorkspace with dependent sessions: %v, want ErrInUse", err)
	}
	if err := db.DeleteSession(ctx, sess.ID); !errors.Is(err, ErrInUse) {
		t.Fatalf("DeleteSession with dependent runs: %v, want ErrInUse", err)
	}
	if err := db.DeleteMember(ctx, m.ID); !errors.Is(err, ErrInUse) {
		t.Fatalf("DeleteMember with dependent runs: %v, want ErrInUse", err)
	}
}

// The database holds credential-home blobs, so neither it nor the WAL
// sidecars SQLite derives from it may be readable by other local accounts.
func TestOpenKeepsDatabaseFilesPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aether.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	mustCreateWorkspace(t, db)
	for _, suffix := range []string{"", "-wal", "-shm"} {
		info, err := os.Stat(path + suffix)
		if err != nil {
			t.Fatalf("stat %s: %v", path+suffix, err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("%s mode = %#o, want 0600", path+suffix, perm)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestConcurrentOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aether.db")
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			db, err := Open(path)
			errs[i] = err
			if err == nil {
				errs[i] = db.Close()
			}
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("concurrent Open %d: %v", i, err)
		}
	}
}

func TestUpdateRunStatus(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	w := mustCreateWorkspace(t, db)
	s := mustCreateSession(t, db, w.ID)
	m := mustCreateMember(t, db)
	r := mustCreateRun(t, db, s.ID, m.ID, domain.RunQueued)

	started := time.Date(2026, 8, 9, 10, 30, 0, 0, time.UTC)
	if err := db.UpdateRunStatus(ctx, r.ID, domain.RunRunning, &started, nil); err != nil {
		t.Fatalf("UpdateRunStatus to running: %v", err)
	}
	got, err := db.GetRun(ctx, r.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.Status != domain.RunRunning || !timePtrEqual(got.StartedAt, &started) || got.FinishedAt != nil {
		t.Fatalf("after running transition: %+v", got)
	}
	if got.Task != r.Task || got.MemberID != r.MemberID {
		t.Fatalf("UpdateRunStatus touched unrelated fields: %+v", got)
	}

	finished := started.Add(time.Hour)
	if uerr := db.UpdateRunStatus(ctx, r.ID, domain.RunMerged, nil, &finished); uerr != nil {
		t.Fatalf("UpdateRunStatus to merged: %v", uerr)
	}
	got, err = db.GetRun(ctx, r.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.Status != domain.RunMerged || !timePtrEqual(got.StartedAt, &started) || !timePtrEqual(got.FinishedAt, &finished) {
		t.Fatalf("after merged transition: %+v", got)
	}

	if err := db.UpdateRunStatus(ctx, r.ID, "warp-drive", nil, nil); err == nil {
		t.Fatal("UpdateRunStatus accepted invalid status")
	}
	if err := db.UpdateRunStatus(ctx, "no-such-run", domain.RunRunning, nil, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("UpdateRunStatus on missing run: %v, want ErrNotFound", err)
	}
}

func TestTransferRun(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	w := mustCreateWorkspace(t, db)
	s := mustCreateSession(t, db, w.ID)
	ada := mustCreateMember(t, db)
	bob := mustCreateMember(t, db)
	r := mustCreateRun(t, db, s.ID, ada.ID, domain.RunRunning)

	if err := db.TransferRun(ctx, r.ID, bob.ID); err != nil {
		t.Fatalf("TransferRun: %v", err)
	}
	got, err := db.GetRun(ctx, r.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.MemberID != bob.ID {
		t.Fatalf("MemberID = %s, want %s", got.MemberID, bob.ID)
	}
	if got.Status != r.Status || got.Task != r.Task {
		t.Fatalf("TransferRun touched unrelated fields: %+v", got)
	}

	if err := db.TransferRun(ctx, r.ID, "no-such-member"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("TransferRun to missing member: %v, want ErrNotFound", err)
	}
	if err := db.TransferRun(ctx, "no-such-run", bob.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("TransferRun on missing run: %v, want ErrNotFound", err)
	}
}

func TestZeroTimeRejected(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	w := mustCreateWorkspace(t, db)
	s := mustCreateSession(t, db, w.ID)
	m := mustCreateMember(t, db)

	zero := time.Time{}
	r := &domain.Run{SessionID: s.ID, MemberID: m.ID, Mode: domain.LaunchTUI,
		Status: domain.RunQueued, StartedAt: &zero}
	if err := db.CreateRun(ctx, r); err == nil {
		t.Fatal("CreateRun accepted a zero StartedAt")
	}

	ok := mustCreateRun(t, db, s.ID, m.ID, domain.RunQueued)
	ok.FinishedAt = &zero
	if err := db.UpdateRun(ctx, ok); err == nil {
		t.Fatal("UpdateRun accepted a zero FinishedAt")
	}
	if err := db.UpdateRunStatus(ctx, ok.ID, domain.RunRunning, &zero, nil); err == nil {
		t.Fatal("UpdateRunStatus accepted a zero StartedAt")
	}
	far := time.Date(2263, 1, 1, 0, 0, 0, 0, time.UTC)
	ws := &domain.Workspace{Name: "far", Environment: domain.WorkspaceEnvironment{CustomImage: "alpine"}, CreatedAt: far}
	if err := db.CreateWorkspace(ctx, ws); err == nil {
		t.Fatal("CreateWorkspace accepted a CreatedAt outside the storable range")
	}
}

func TestUpdatesNeverTouchCreatedAt(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	w := mustCreateWorkspace(t, db)
	orig := w.CreatedAt
	w.CreatedAt = time.Time{}
	w.Name = "renamed"
	if err := db.UpdateWorkspace(ctx, w); err != nil {
		t.Fatalf("UpdateWorkspace with zero CreatedAt: %v", err)
	}
	got, err := db.GetWorkspace(ctx, w.ID)
	if err != nil {
		t.Fatalf("GetWorkspace: %v", err)
	}
	if !got.CreatedAt.Equal(orig) {
		t.Fatalf("CreatedAt changed by update: %v, want %v", got.CreatedAt, orig)
	}
	if got.Name != "renamed" {
		t.Fatalf("Name = %q, want %q", got.Name, "renamed")
	}
}

func TestCreatedAtPreservedWhenSet(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	want := time.Date(2020, 1, 2, 3, 4, 5, 678900000, time.UTC)
	w := &domain.Workspace{Name: "old", Environment: domain.WorkspaceEnvironment{CustomImage: "alpine"}, CreatedAt: want}
	if err := db.CreateWorkspace(ctx, w); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	got, err := db.GetWorkspace(ctx, w.ID)
	if err != nil {
		t.Fatalf("GetWorkspace: %v", err)
	}
	if !got.CreatedAt.Equal(want) {
		t.Fatalf("CreatedAt = %v, want %v", got.CreatedAt, want)
	}
}

func assertWorkspaceEqual(t *testing.T, want, got *domain.Workspace) {
	t.Helper()
	if got.ID != want.ID || got.Name != want.Name ||
		got.Environment.CustomImage != want.Environment.CustomImage ||
		got.Environment.NeutralImage != want.Environment.NeutralImage ||
		got.Environment.SetupPolicy != want.Environment.SetupPolicy ||
		!got.CreatedAt.Equal(want.CreatedAt) {
		t.Fatalf("workspace round-trip: got %+v, want %+v", got, want)
	}
	if len(got.Environment.Variables) != len(want.Environment.Variables) {
		t.Fatalf("variables round-trip: got %v, want %v", got.Environment.Variables, want.Environment.Variables)
	}
	for k, v := range want.Environment.Variables {
		if got.Environment.Variables[k] != v {
			t.Fatalf("variables[%q] = %q, want %q", k, got.Environment.Variables[k], v)
		}
	}
}

func assertRunEqual(t *testing.T, want, got *domain.Run) {
	t.Helper()
	if got.ID != want.ID || got.SessionID != want.SessionID || got.MemberID != want.MemberID ||
		got.Task != want.Task || got.Harness != want.Harness || got.Mode != want.Mode ||
		got.Status != want.Status || got.Branch != want.Branch || got.Worktree != want.Worktree ||
		got.ProfileSnapshotID != want.ProfileSnapshotID || got.ToolSnapshotID != want.ToolSnapshotID ||
		got.Protected != want.Protected ||
		!got.CreatedAt.Equal(want.CreatedAt) {
		t.Fatalf("run round-trip: got %+v, want %+v", got, want)
	}
	if !timePtrEqual(got.StartedAt, want.StartedAt) {
		t.Fatalf("StartedAt = %v, want %v", got.StartedAt, want.StartedAt)
	}
	if !timePtrEqual(got.FinishedAt, want.FinishedAt) {
		t.Fatalf("FinishedAt = %v, want %v", got.FinishedAt, want.FinishedAt)
	}
}

func timePtrEqual(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

func TestToolSnapshotCRUDAndDeletionProtection(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	w := mustCreateWorkspace(t, db)
	m := mustCreateMember(t, db)
	s := &domain.ToolSnapshot{
		WorkspaceID: w.ID,
		MemberID:    m.ID,
		Digest:      "sha256:first",
		Manifest:    domain.ToolManifest{Executable: "tool", Version: "1"},
	}
	if err := db.CreateToolSnapshot(ctx, s); err != nil {
		t.Fatalf("CreateToolSnapshot: %v", err)
	}
	if err := db.SetToolHead(ctx, m.ID, w.ID, s.ID); err != nil {
		t.Fatalf("SetToolHead: %v", err)
	}
	if err := db.DeleteToolSnapshot(ctx, s.ID); !errors.Is(err, ErrInUse) {
		t.Fatalf("delete active snapshot = %v, want ErrInUse", err)
	}
	if err := db.SetToolHead(ctx, m.ID, w.ID, ""); err != nil {
		t.Fatalf("clear head: %v", err)
	}
	pending := &PendingWorkspaceShell{
		WorkspaceID: w.ID, MemberID: m.ID, SnapshotID: s.ID, StagingID: "staging-1",
	}
	if err := db.CreatePendingWorkspaceShell(ctx, pending); err != nil {
		t.Fatalf("CreatePendingWorkspaceShell: %v", err)
	}
	if err := db.DeleteToolSnapshot(ctx, s.ID); !errors.Is(err, ErrInUse) {
		t.Fatalf("delete pending snapshot = %v, want ErrInUse", err)
	}
	if err := db.DeletePendingWorkspaceShell(ctx, pending.ID); err != nil {
		t.Fatalf("DeletePendingWorkspaceShell: %v", err)
	}
	if err := db.DeleteToolSnapshot(ctx, s.ID); err != nil {
		t.Fatalf("delete unreferenced snapshot: %v", err)
	}
}

func TestWorkspaceEnvironmentUsesFirstClassRepresentation(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	w := &domain.Workspace{
		Name: "neutral",
		Environment: domain.WorkspaceEnvironment{
			NeutralImage: true,
			Variables:    map[string]string{"A": "1"},
			SetupPolicy:  domain.SetupPolicy{Script: "echo setup"},
		},
	}
	if err := db.CreateWorkspace(ctx, w); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	var raw string
	if err := db.db.QueryRowContext(ctx, `SELECT environment FROM workspaces WHERE id = ?`, w.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw == "" || raw == "{}" {
		t.Fatalf("first-class environment is empty: %q", raw)
	}
	got, err := db.GetWorkspace(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Environment.NeutralImage || got.Environment.Variables["A"] != "1" ||
		got.Environment.SetupPolicy.Script != "echo setup" {
		t.Fatalf("environment = %+v", got.Environment)
	}
}

func TestDeleteToolSnapshotProtectsLiveRunReferences(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	w := mustCreateWorkspace(t, db)
	s := mustCreateSession(t, db, w.ID)
	m := mustCreateMember(t, db)

	for i, status := range []domain.RunStatus{
		domain.RunQueued,
		domain.RunProvisioning,
		domain.RunRunning,
		domain.RunNeedsAttention,
	} {
		snapshot := &domain.ToolSnapshot{
			WorkspaceID: w.ID,
			MemberID:    m.ID,
			Digest:      fmt.Sprintf("sha256:live-%d", i),
			Manifest:    domain.ToolManifest{Executable: "tool"},
		}
		if err := db.CreateToolSnapshot(ctx, snapshot); err != nil {
			t.Fatalf("CreateToolSnapshot(%s): %v", status, err)
		}
		run := &domain.Run{
			SessionID:      s.ID,
			MemberID:       m.ID,
			Task:           "task",
			Harness:        "claude",
			Mode:           domain.LaunchTUI,
			Status:         status,
			ToolSnapshotID: snapshot.ID,
		}
		if err := db.CreateRun(ctx, run); err != nil {
			t.Fatalf("CreateRun(%s): %v", status, err)
		}
		if err := db.DeleteToolSnapshot(ctx, snapshot.ID); !errors.Is(err, ErrInUse) {
			t.Fatalf("delete %s snapshot = %v, want ErrInUse", status, err)
		}
		if err := db.UpdateRunStatus(ctx, run.ID, domain.RunMerged, nil, nil); err != nil {
			t.Fatalf("finish %s run: %v", status, err)
		}
		if err := db.DeleteToolSnapshot(ctx, snapshot.ID); err != nil {
			t.Fatalf("delete terminal %s snapshot: %v", status, err)
		}
	}
}

func TestSetRunToolSnapshotPreservesHandoffFields(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	w := mustCreateWorkspace(t, db)
	s := mustCreateSession(t, db, w.ID)
	owner := mustCreateMember(t, db)
	handoff := mustCreateMember(t, db)
	run := mustCreateRun(t, db, s.ID, owner.ID, domain.RunQueued)
	run.Branch = "handoff-branch"
	if err := db.UpdateRun(ctx, run); err != nil {
		t.Fatalf("set branch: %v", err)
	}

	ownerSnapshot := &domain.ToolSnapshot{
		WorkspaceID: w.ID,
		MemberID:    owner.ID,
		Digest:      "sha256:owner",
		Manifest:    domain.ToolManifest{Executable: "tool"},
	}
	handoffSnapshot := &domain.ToolSnapshot{
		WorkspaceID: w.ID,
		MemberID:    handoff.ID,
		Digest:      "sha256:handoff",
		Manifest:    domain.ToolManifest{Executable: "tool"},
	}
	otherWorkspace := mustCreateWorkspace(t, db)
	wrongWorkspaceSnapshot := &domain.ToolSnapshot{
		WorkspaceID: otherWorkspace.ID,
		MemberID:    handoff.ID,
		Digest:      "sha256:other-workspace",
		Manifest:    domain.ToolManifest{Executable: "tool"},
	}
	for _, snapshot := range []*domain.ToolSnapshot{ownerSnapshot, handoffSnapshot, wrongWorkspaceSnapshot} {
		if err := db.CreateToolSnapshot(ctx, snapshot); err != nil {
			t.Fatalf("CreateToolSnapshot: %v", err)
		}
	}

	if err := db.TransferRun(ctx, run.ID, handoff.ID); err != nil {
		t.Fatalf("TransferRun: %v", err)
	}
	if err := db.SetRunToolSnapshot(ctx, run.ID, wrongWorkspaceSnapshot.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("pin snapshot from another workspace = %v, want ErrNotFound", err)
	}
	if err := db.SetRunToolSnapshot(ctx, run.ID, ownerSnapshot.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("pin snapshot from prior owner = %v, want ErrNotFound", err)
	}
	if err := db.SetRunToolSnapshot(ctx, run.ID, handoffSnapshot.ID); err != nil {
		t.Fatalf("pin handoff snapshot: %v", err)
	}
	got, err := db.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.MemberID != handoff.ID || got.Branch != "handoff-branch" ||
		got.ToolSnapshotID != handoffSnapshot.ID {
		t.Fatalf("run after pin = %+v, handoff/branch/tool fields were not preserved", got)
	}
}
