package store

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
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
			Variables:   map[string]string{"GOFLAGS": "-trimpath", "TZ": "UTC"},
			SetupPolicy: domain.SetupPolicy{Script: "make deps\n"},
		},
	}
	if err := db.CreateWorkspace(context.Background(), w); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	return w
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

func mustCreateRun(t *testing.T, db *DB, wid domain.WorkspaceID, mid domain.MemberID, status domain.RunStatus) *domain.Run {
	t.Helper()
	r := &domain.Run{
		WorkspaceID: wid,
		MemberID:    mid,
		Task:        "fix the auth bug",
		Harness:     "claude",
		Mode:        domain.LaunchTUI,
		Status:      status,
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

func TestRunCommitMigrationAddsColumns(t *testing.T) {
	db := openTestDB(t)
	rows, err := db.db.Query(`PRAGMA table_info(runs)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info(runs): %v", err)
	}
	defer func() { _ = rows.Close() }()

	type column struct {
		typ     string
		notNull int
		defVal  sql.NullString
	}
	columns := make(map[string]column)
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var defVal sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defVal, &pk); err != nil {
			t.Fatalf("scan runs column: %v", err)
		}
		columns[name] = column{typ: typ, notNull: notNull, defVal: defVal}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate runs columns: %v", err)
	}
	if got := columns["last_commit"]; got.typ != "TEXT" || got.notNull != 1 ||
		!got.defVal.Valid || got.defVal.String != "''" {
		t.Fatalf("last_commit column = %+v, want TEXT NOT NULL DEFAULT ''", got)
	}
	if got := columns["last_commit_at"]; got.typ != "INTEGER" || got.notNull != 0 ||
		got.defVal.Valid {
		t.Fatalf("last_commit_at column = %+v, want nullable INTEGER", got)
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
	w := &domain.Workspace{Name: "bare"}
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

	if err := db.UpdateMemberImage(ctx, m.ID, "aether/member-"+string(m.ID)+":123"); err != nil {
		t.Fatalf("UpdateMemberImage: %v", err)
	}
	got, err = db.GetMember(ctx, m.ID)
	if err != nil {
		t.Fatalf("GetMember after image update: %v", err)
	}
	if got.Image != "aether/member-"+string(m.ID)+":123" {
		t.Fatalf("member image = %q, want saved image", got.Image)
	}
	list, err = db.ListMembers(ctx)
	if err != nil {
		t.Fatalf("ListMembers after image update: %v", err)
	}
	if len(list) != 1 || list[0].Image != got.Image {
		t.Fatalf("ListMembers image = %+v, want %q", list, got.Image)
	}
	if err := db.UpdateMemberImage(ctx, "missing", "tag"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("UpdateMemberImage missing member = %v, want ErrNotFound", err)
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
	// schema with the inline UNIQUE public_key, runs still hanging off a
	// session), seed a member and a run referencing it, then Open: v2 must
	// rebuild the members table without losing rows or breaking
	// runs.member_id references, and v12 must rehome the run onto the
	// workspace.
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
	if r.WorkspaceID != "w1" {
		t.Fatalf("run.workspace_id = %s, want w1", r.WorkspaceID)
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
	m := mustCreateMember(t, db)

	r := mustCreateRun(t, db, w.ID, m.ID, domain.RunQueued)
	if r.ID == "" {
		t.Fatal("CreateRun did not assign an ID")
	}
	r.Branch = "aether/run-" + string(r.ID) + "-auth-fix"
	r.Worktree = "/var/lib/aether/worktrees/" + string(r.ID)
	r.LastCommit = strings.Repeat("a", 40)
	lastCommitAt := time.Date(2026, 8, 9, 10, 31, 0, 123456789, time.UTC)
	r.LastCommitAt = lastCommitAt
	if err := db.UpdateRun(ctx, r); err != nil {
		t.Fatalf("UpdateRun (branch/worktree/commit): %v", err)
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

func TestDeleteRunCleansRunOwnedData(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	w := mustCreateWorkspace(t, db)
	m := mustCreateMember(t, db)
	run := mustCreateRun(t, db, w.ID, m.ID, domain.RunFailed)
	other := mustCreateRun(t, db, w.ID, m.ID, domain.RunRunning)

	if err := db.CreateApproval(ctx, &Approval{
		WorkspaceID: w.ID,
		RunID:       run.ID,
		Action:      "Bash",
	}); err != nil {
		t.Fatalf("CreateApproval: %v", err)
	}
	if err := db.PutRunCost(ctx, &RunCost{
		RunID: run.ID, WorkspaceID: w.ID, MemberID: m.ID, CostUSD: 1.25,
	}); err != nil {
		t.Fatalf("PutRunCost: %v", err)
	}
	for _, msg := range []*RunMessage{
		{WorkspaceID: w.ID, FromRun: run.ID, ToRun: other.ID, Body: "outbound"},
		{WorkspaceID: w.ID, FromRun: other.ID, ToRun: run.ID, Body: "inbound"},
	} {
		if err := db.AppendRunMessage(ctx, msg, 10); err != nil {
			t.Fatalf("AppendRunMessage: %v", err)
		}
	}

	if err := db.DeleteRun(ctx, run.ID); err != nil {
		t.Fatalf("DeleteRun: %v", err)
	}
	if _, err := db.GetRun(ctx, run.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetRun after delete: %v, want ErrNotFound", err)
	}
	if _, err := db.GetRun(ctx, other.ID); err != nil {
		t.Fatalf("unrelated run deleted: %v", err)
	}

	for _, check := range []struct {
		table string
		where string
		args  []any
	}{
		{"approvals", "run_id = ?", []any{run.ID}},
		{"run_costs", "run_id = ?", []any{run.ID}},
		{"run_messages", "from_run = ? OR to_run = ?", []any{run.ID, run.ID}},
	} {
		var count int
		if err := db.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM "+check.table+" WHERE "+check.where,
			check.args...,
		).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", check.table, err)
		}
		if count != 0 {
			t.Fatalf("%s rows remain for deleted run: %d", check.table, count)
		}
	}
}

func TestRunQueries(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	w1 := mustCreateWorkspace(t, db)
	w2 := mustCreateWorkspace(t, db)
	ada := mustCreateMember(t, db)
	bob := mustCreateMember(t, db)

	r1 := mustCreateRun(t, db, w1.ID, ada.ID, domain.RunRunning)
	r2 := mustCreateRun(t, db, w1.ID, bob.ID, domain.RunNeedsAttention)
	r3 := mustCreateRun(t, db, w2.ID, ada.ID, domain.RunMerged)
	mustCreateRun(t, db, w2.ID, bob.ID, domain.RunFailed)

	byWorkspace, err := db.ListRunsByWorkspace(ctx, w1.ID)
	if err != nil {
		t.Fatalf("ListRunsByWorkspace: %v", err)
	}
	if len(byWorkspace) != 2 {
		t.Fatalf("ListRunsByWorkspace len = %d, want 2", len(byWorkspace))
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
	m := mustCreateMember(t, db)

	bad := &domain.Run{WorkspaceID: w.ID, MemberID: m.ID, Mode: domain.LaunchTUI, Status: "exploded"}
	if err := db.CreateRun(ctx, bad); err == nil {
		t.Fatal("CreateRun accepted invalid status")
	}
	bad = &domain.Run{WorkspaceID: w.ID, MemberID: m.ID, Mode: "vr", Status: domain.RunQueued}
	if err := db.CreateRun(ctx, bad); err == nil {
		t.Fatal("CreateRun accepted invalid launch mode")
	}

	r := mustCreateRun(t, db, w.ID, m.ID, domain.RunQueued)
	r.Status = "warp-drive"
	if err := db.UpdateRun(ctx, r); err == nil {
		t.Fatal("UpdateRun accepted invalid status")
	}
}

func TestForeignKeyEnforcement(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	w := mustCreateWorkspace(t, db)
	m := mustCreateMember(t, db)

	r := &domain.Run{WorkspaceID: "no-such-workspace", MemberID: m.ID, Mode: domain.LaunchTUI, Status: domain.RunQueued}
	if err := db.CreateRun(ctx, r); !errors.Is(err, ErrNotFound) {
		t.Fatalf("CreateRun with missing workspace FK: %v, want ErrNotFound", err)
	}
	r = &domain.Run{WorkspaceID: w.ID, MemberID: "no-such-member", Mode: domain.LaunchTUI, Status: domain.RunQueued}
	if err := db.CreateRun(ctx, r); !errors.Is(err, ErrNotFound) {
		t.Fatalf("CreateRun with missing member FK: %v, want ErrNotFound", err)
	}

	mustCreateRun(t, db, w.ID, m.ID, domain.RunQueued)
	if err := db.DeleteWorkspace(ctx, w.ID); !errors.Is(err, ErrInUse) {
		t.Fatalf("DeleteWorkspace with dependent runs: %v, want ErrInUse", err)
	}
	if err := db.DeleteMember(ctx, m.ID); !errors.Is(err, ErrInUse) {
		t.Fatalf("DeleteMember with dependent runs: %v, want ErrInUse", err)
	}
}

// The database holds credential-home blobs, so neither it nor the WAL
// sidecars SQLite derives from it may be readable by other local accounts.
func TestOpenKeepsDatabaseFilesPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("asserts unix permission bits; NTFS ACLs surface as 0666 through os.FileMode")
	}
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
	m := mustCreateMember(t, db)
	r := mustCreateRun(t, db, w.ID, m.ID, domain.RunQueued)

	started := time.Date(2026, 8, 9, 10, 30, 0, 0, time.UTC)
	if err := db.UpdateRunStatus(ctx, r.ID, domain.RunRunning, "container started", &started, nil); err != nil {
		t.Fatalf("UpdateRunStatus to running: %v", err)
	}
	got, err := db.GetRun(ctx, r.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.Status != domain.RunRunning || !timePtrEqual(got.StartedAt, &started) || got.FinishedAt != nil {
		t.Fatalf("after running transition: %+v", got)
	}
	if got.Reason != "container started" {
		t.Fatalf("reason = %q, want %q", got.Reason, "container started")
	}
	if got.Task != r.Task || got.MemberID != r.MemberID {
		t.Fatalf("UpdateRunStatus touched unrelated fields: %+v", got)
	}

	finished := started.Add(time.Hour)
	if uerr := db.UpdateRunStatus(ctx, r.ID, domain.RunMerged, "", nil, &finished); uerr != nil {
		t.Fatalf("UpdateRunStatus to merged: %v", uerr)
	}
	got, err = db.GetRun(ctx, r.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.Status != domain.RunMerged || !timePtrEqual(got.StartedAt, &started) || !timePtrEqual(got.FinishedAt, &finished) {
		t.Fatalf("after merged transition: %+v", got)
	}
	if got.Reason != "" {
		t.Fatalf("second transition kept stale reason %q, want empty", got.Reason)
	}

	if err := db.UpdateRunStatus(ctx, r.ID, "warp-drive", "", nil, nil); err == nil {
		t.Fatal("UpdateRunStatus accepted invalid status")
	}
	if err := db.UpdateRunStatus(ctx, "no-such-run", domain.RunRunning, "", nil, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("UpdateRunStatus on missing run: %v, want ErrNotFound", err)
	}
}

func TestTransferRun(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	w := mustCreateWorkspace(t, db)
	ada := mustCreateMember(t, db)
	bob := mustCreateMember(t, db)
	r := mustCreateRun(t, db, w.ID, ada.ID, domain.RunRunning)

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
	m := mustCreateMember(t, db)

	zero := time.Time{}
	r := &domain.Run{WorkspaceID: w.ID, MemberID: m.ID, Mode: domain.LaunchTUI,
		Status: domain.RunQueued, StartedAt: &zero}
	if err := db.CreateRun(ctx, r); err == nil {
		t.Fatal("CreateRun accepted a zero StartedAt")
	}

	ok := mustCreateRun(t, db, w.ID, m.ID, domain.RunQueued)
	ok.FinishedAt = &zero
	if err := db.UpdateRun(ctx, ok); err == nil {
		t.Fatal("UpdateRun accepted a zero FinishedAt")
	}
	if err := db.UpdateRunStatus(ctx, ok.ID, domain.RunRunning, "", &zero, nil); err == nil {
		t.Fatal("UpdateRunStatus accepted a zero StartedAt")
	}
	far := time.Date(2263, 1, 1, 0, 0, 0, 0, time.UTC)
	ws := &domain.Workspace{Name: "far", CreatedAt: far}
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
	w := &domain.Workspace{Name: "old", CreatedAt: want}
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
	if got.ID != want.ID || got.WorkspaceID != want.WorkspaceID || got.MemberID != want.MemberID ||
		got.Task != want.Task || got.Harness != want.Harness || got.Mode != want.Mode ||
		got.Status != want.Status || got.Branch != want.Branch || got.Worktree != want.Worktree ||
		got.ProfileSnapshotID != want.ProfileSnapshotID ||
		got.LastCommit != want.LastCommit || !got.LastCommitAt.Equal(want.LastCommitAt) ||
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

func TestWorkspaceEnvironmentUsesFirstClassRepresentation(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	w := &domain.Workspace{
		Name: "environment",
		Environment: domain.WorkspaceEnvironment{
			Variables:   map[string]string{"A": "1"},
			SetupPolicy: domain.SetupPolicy{Script: "echo setup"},
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
	if got.Environment.Variables["A"] != "1" ||
		got.Environment.SetupPolicy.Script != "echo setup" {
		t.Fatalf("environment = %+v", got.Environment)
	}
}

func TestHarnessDefinitionRoundTrip(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	m := mustCreateMember(t, db)

	blob := []byte(`{"Name":"mytool","Executable":"mytool","TUIArgs":["--tui"]}`)
	def := &HarnessDefinition{MemberID: m.ID, Name: "mytool", Definition: blob}
	if err := db.UpsertHarnessDefinition(ctx, def); err != nil {
		t.Fatalf("UpsertHarnessDefinition: %v", err)
	}
	if def.CreatedAt.IsZero() || def.UpdatedAt.IsZero() {
		t.Fatalf("timestamps not set: %+v", def)
	}

	got, err := db.GetHarnessDefinition(ctx, m.ID, "mytool")
	if err != nil {
		t.Fatalf("GetHarnessDefinition: %v", err)
	}
	if got.MemberID != m.ID || got.Name != "mytool" || string(got.Definition) != string(blob) {
		t.Fatalf("round trip = %+v, want member/name/blob preserved", got)
	}
	if !got.CreatedAt.Equal(def.CreatedAt) || !got.UpdatedAt.Equal(def.UpdatedAt) {
		t.Fatalf("timestamps = %v/%v, want %v/%v", got.CreatedAt, got.UpdatedAt, def.CreatedAt, def.UpdatedAt)
	}
}

func TestHarnessDefinitionNotFound(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	m := mustCreateMember(t, db)

	if _, err := db.GetHarnessDefinition(ctx, m.ID, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get missing = %v, want ErrNotFound", err)
	}
	def := &HarnessDefinition{MemberID: "nonexistent", Name: "x", Definition: []byte(`{}`)}
	if err := db.UpsertHarnessDefinition(ctx, def); !errors.Is(err, ErrNotFound) {
		t.Fatalf("upsert for missing member = %v, want ErrNotFound", err)
	}
}

func TestHarnessDefinitionUpsertOverwrites(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	m := mustCreateMember(t, db)

	first := &HarnessDefinition{
		MemberID:   m.ID,
		Name:       "mytool",
		Definition: []byte(`{"Executable":"v1"}`),
		CreatedAt:  time.Unix(0, 1_000_000_000).UTC(),
		UpdatedAt:  time.Unix(0, 1_000_000_000).UTC(),
	}
	if err := db.UpsertHarnessDefinition(ctx, first); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	second := &HarnessDefinition{MemberID: m.ID, Name: "mytool", Definition: []byte(`{"Executable":"v2"}`)}
	if err := db.UpsertHarnessDefinition(ctx, second); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	got, err := db.GetHarnessDefinition(ctx, m.ID, "mytool")
	if err != nil {
		t.Fatalf("GetHarnessDefinition: %v", err)
	}
	if string(got.Definition) != `{"Executable":"v2"}` {
		t.Fatalf("definition = %s, want overwritten blob", got.Definition)
	}
	if !got.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("created_at = %v, want preserved %v", got.CreatedAt, first.CreatedAt)
	}
	if !got.UpdatedAt.After(first.UpdatedAt) {
		t.Fatalf("updated_at = %v, want bumped past %v", got.UpdatedAt, first.UpdatedAt)
	}
}

func TestListHarnessDefinitionsScopedAndSorted(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	a := mustCreateMember(t, db)
	b := mustCreateMember(t, db)

	for _, seed := range []struct {
		member domain.MemberID
		name   string
	}{
		{a.ID, "zeta"},
		{a.ID, "alpha"},
		{b.ID, "other"},
	} {
		d := &HarnessDefinition{MemberID: seed.member, Name: seed.name, Definition: []byte(`{}`)}
		if err := db.UpsertHarnessDefinition(ctx, d); err != nil {
			t.Fatalf("upsert %s/%s: %v", seed.member, seed.name, err)
		}
	}
	got, err := db.ListHarnessDefinitions(ctx, a.ID)
	if err != nil {
		t.Fatalf("ListHarnessDefinitions: %v", err)
	}
	if len(got) != 2 || got[0].Name != "alpha" || got[1].Name != "zeta" {
		t.Fatalf("list = %+v, want [alpha zeta] for member a only", got)
	}
	empty, err := db.ListHarnessDefinitions(ctx, "no-such-member")
	if err != nil {
		t.Fatalf("list unknown member: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("list unknown member = %+v, want empty", empty)
	}
}

func TestTerminalPersistence(t *testing.T) {
	db := openTestDB(t)
	member := mustCreateMember(t, db)
	started := time.Date(2026, 9, 3, 12, 0, 7, 123, time.UTC)
	terminal := &domain.Terminal{
		Member:      member.ID,
		ContainerID: "container-1",
		Image:       "standard:latest",
		StartedAt:   started,
	}
	ctx := context.Background()
	if err := db.PutTerminal(ctx, terminal); err != nil {
		t.Fatalf("PutTerminal: %v", err)
	}
	got, err := db.GetTerminal(ctx, member.ID)
	if err != nil {
		t.Fatalf("GetTerminal: %v", err)
	}
	if got.Member != terminal.Member || got.ContainerID != terminal.ContainerID || got.Image != terminal.Image || got.StartedAt.Unix() != started.Unix() {
		t.Fatalf("terminal = %+v, want fields from %+v", got, terminal)
	}
	terminal.ContainerID = "container-2"
	if updateErr := db.PutTerminal(ctx, terminal); updateErr != nil {
		t.Fatalf("PutTerminal update: %v", updateErr)
	}
	got, err = db.GetTerminal(ctx, member.ID)
	if err != nil {
		t.Fatalf("GetTerminal after update: %v", err)
	}
	if got.ContainerID != terminal.ContainerID {
		t.Fatalf("container ID = %q, want %q", got.ContainerID, terminal.ContainerID)
	}
	if err := db.DeleteTerminal(ctx, member.ID); err != nil {
		t.Fatalf("DeleteTerminal: %v", err)
	}
	if err := db.DeleteTerminal(ctx, member.ID); err != nil {
		t.Fatalf("DeleteTerminal missing: %v", err)
	}
	if _, err := db.GetTerminal(ctx, member.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetTerminal after delete error = %v, want ErrNotFound", err)
	}
}
