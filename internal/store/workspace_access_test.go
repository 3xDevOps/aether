package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// The upgrade keeps every approved member on every workspace they reached,
// which was all of them, and hands the server to its earliest admin.
func TestWorkspaceAccessMigrationKeepsTodaysAccess(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "aether.db")
	raw := openLegacy(t, path, len(migrations)-1)
	if _, err := raw.Exec(`
		INSERT INTO members (id, display_name, public_key, color, role, pending, created_at)
			VALUES ('creator', 'Ada', ?, '#e6194b', 'admin', 0, 1),
			       ('admin2', 'Bo', ?, '#3cb44b', 'admin', 0, 2),
			       ('user', 'Cam', ?, '#ffe119', 'collaborator', 0, 3),
			       ('watcher', 'Dee', ?, '#4363d8', 'viewer', 0, 4),
			       ('waiting', 'Eve', ?, '#f58231', 'collaborator', 1, 5),
			       ('gone', 'Fay', ?, '#911eb4', 'admin', 0, 0);
		UPDATE members SET merged_into = 'creator' WHERE id = 'gone';
		INSERT INTO workspaces (id, name, environment, base_branch, steer_others, origin, created_at)
			VALUES ('w1', 'one', '{}', 'main', '', '', 1),
			       ('w2', 'two', '{}', 'main', '', '', 2);
	`, testKey(t, "creator"), testKey(t, "admin2"), testKey(t, "user"), testKey(t, "watcher"),
		testKey(t, "waiting"), testKey(t, "gone")); err != nil {
		_ = raw.Close()
		t.Fatalf("seed legacy members: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()

	roles := map[domain.MemberID]domain.Role{
		"creator": domain.RoleSuperAdmin, "admin2": domain.RoleAdmin,
		"user": domain.RoleCollaborator, "watcher": domain.RoleViewer, "waiting": domain.RoleCollaborator,
	}
	for id, want := range roles {
		m, gerr := db.GetMember(ctx, id)
		if gerr != nil {
			t.Fatalf("GetMember %s: %v", id, gerr)
		}
		if m.Role != want {
			t.Fatalf("%s role = %s, want %s", id, m.Role, want)
		}
		wantGrants := 2
		if id == "creator" || id == "waiting" {
			wantGrants = 0
		}
		if len(m.Access.Granted) != wantGrants {
			t.Fatalf("%s grants = %v, want %d", id, m.Access.Granted, wantGrants)
		}
	}
	for _, id := range []domain.WorkspaceID{"w1", "w2"} {
		w, gerr := db.GetWorkspace(ctx, id)
		if gerr != nil {
			t.Fatalf("GetWorkspace %s: %v", id, gerr)
		}
		if w.OwnerID != "creator" {
			t.Fatalf("%s owner = %q, want creator", id, w.OwnerID)
		}
	}
	creator, err := db.GetMember(ctx, "creator")
	if err != nil {
		t.Fatalf("GetMember creator: %v", err)
	}
	if !creator.Access.Owned["w1"] || !creator.Access.Owned["w2"] {
		t.Fatalf("creator owns %v, want w1 and w2", creator.Access.Owned)
	}
}

// Events written before the upgrade name a second workspace only the way
// the publishers of the day did: a run.pr from an extra repository did not
// say which, and a coordination message or ack named a run in another
// workspace. The upgrade rewrites them as today's publishers would, so a
// reader granted only the run's workspace replays none of it.
func TestWorkspaceAccessMigrationRescopesOldEvents(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "aether.db")
	raw := openLegacy(t, path, len(migrations)-1)
	if _, err := raw.Exec(`
		INSERT INTO members (id, display_name, public_key, color, role, created_at)
			VALUES ('m1', 'Ada', ?, '#e6194b', 'admin', 1);
		INSERT INTO workspaces (id, name, environment, base_branch, steer_others, origin, created_at)
			VALUES ('w1', 'one', '{}', 'main', '', '', 1), ('w2', 'two', '{}', 'main', '', '', 2);
		INSERT INTO runs (id, workspace_id, member_id, task, harness, mode, status, branch, worktree, created_at)
			VALUES ('multi', 'w1', 'm1', 't', 'claude', 'tui', 'running', 'b1', 'w', 1),
			       ('foreign', 'w2', 'm1', 't', 'claude', 'tui', 'running', 'b2', 'w', 2),
			       ('local', 'w1', 'm1', 't', 'claude', 'tui', 'running', 'b3', 'w', 3);
		INSERT INTO run_repos (run_id, position, workspace_id, name, mount_path, base_branch, base_commit)
			VALUES ('multi', 1, 'w2', 'two', '/repos/two', 'main', 'c');
		INSERT INTO run_pull_requests (run_id, workspace_id, repo_workspace_id, repo, number, url, state, created_at, checked_at)
			VALUES ('multi', 'w1', '', 'o/one', 1, 'https://github.com/o/one/pull/1', 'open', 1, 1),
			       ('multi', 'w1', 'w2', 'o/two', 1, 'https://github.com/o/two/pull/1', 'open', 1, 1);
		INSERT INTO events (id, ts, workspace_id, run_id, actor_id, type, payload) VALUES
			('pr-primary', 1, 'w1', 'multi', '', 'run.pr', '{"run_id":"multi","number":1,"url":"https://github.com/o/one/pull/1","state":"open"}'),
			('pr-extra', 2, 'w1', 'multi', '', 'run.pr', '{"run_id":"multi","number":1,"url":"https://github.com/o/two/pull/1","state":"open"}'),
			('pr-unknown', 3, 'w1', 'multi', '', 'run.pr', '{"run_id":"multi","number":2,"url":"https://github.com/o/two/pull/2","state":"open"}'),
			('pr-single', 4, 'w1', 'local', '', 'run.pr', '{"run_id":"local","number":3,"url":"https://github.com/o/one/pull/3","state":"open"}'),
			('msg-foreign', 5, 'w1', 'multi', '', 'coord.message', '{"message_id":"a","workspace_id":"w1","from_run_id":"multi","to_run_id":"foreign","kind":"message"}'),
			('msg-local', 6, 'w1', 'multi', '', 'coord.message', '{"message_id":"b","workspace_id":"w1","from_run_id":"multi","to_run_id":"local","kind":"message"}'),
			('ack-foreign', 7, 'w1', 'foreign', '', 'coord.message.acked', '{"message_id":"a","to_run_id":"foreign","acked_at":"2026-11-01T00:00:00Z"}'),
			('ack-local', 8, 'w1', 'local', '', 'coord.message.acked', '{"message_id":"b","to_run_id":"local","acked_at":"2026-11-01T00:00:00Z"}');
	`, testKey(t, "m1")); err != nil {
		_ = raw.Close()
		t.Fatalf("seed legacy events: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	type row struct {
		runID string
		repo  *string
		to    *string
	}
	get := func(id string) (row, bool) {
		t.Helper()
		var r row
		err := db.db.QueryRowContext(ctx,
			`SELECT run_id, json_extract(payload, '$.repo'), json_extract(payload, '$.to_run_id') FROM events WHERE id = ?`, id).
			Scan(&r.runID, &r.repo, &r.to)
		if errors.Is(err, sql.ErrNoRows) {
			return r, false
		}
		if err != nil {
			t.Fatalf("event %s: %v", id, err)
		}
		return r, true
	}
	str := func(p *string) string {
		if p == nil {
			return "<none>"
		}
		return *p
	}
	for _, want := range []struct {
		id, runID, repo, to string
		gone                bool
	}{
		{id: "pr-primary", runID: "multi", repo: "<none>", to: "<none>"},
		{id: "pr-extra", runID: "multi", repo: "w2", to: "<none>"},
		{id: "pr-unknown", gone: true},
		{id: "pr-single", runID: "local", repo: "<none>", to: "<none>"},
		{id: "msg-foreign", runID: "multi", repo: "<none>", to: ""},
		{id: "msg-local", runID: "multi", repo: "<none>", to: "local"},
		{id: "ack-foreign", runID: "", repo: "<none>", to: ""},
		{id: "ack-local", runID: "local", repo: "<none>", to: "local"},
	} {
		got, ok := get(want.id)
		if ok == want.gone {
			t.Fatalf("event %s kept = %v, want %v", want.id, ok, !want.gone)
		}
		if ok && (got.runID != want.runID || str(got.repo) != want.repo || str(got.to) != want.to) {
			t.Fatalf("event %s = run %q repo %s to %s, want run %q repo %s to %s",
				want.id, got.runID, str(got.repo), str(got.to), want.runID, want.repo, want.to)
		}
	}
}

func TestWorkspaceGrants(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	owner := &domain.Member{DisplayName: "Ada", PublicKey: testKey(t, "ada"), Color: "#e6194b", Role: domain.RoleAdmin}
	user := &domain.Member{DisplayName: "Cam", PublicKey: testKey(t, "cam"), Color: "#3cb44b", Role: domain.RoleCollaborator}
	for _, m := range []*domain.Member{owner, user} {
		if err := db.CreateMember(ctx, m); err != nil {
			t.Fatalf("CreateMember: %v", err)
		}
	}
	w := &domain.Workspace{Name: "web", OwnerID: owner.ID}
	if err := db.CreateWorkspace(ctx, w); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	for range 2 {
		if err := db.GrantWorkspace(ctx, w.ID, user.ID, owner.ID); err != nil {
			t.Fatalf("GrantWorkspace: %v", err)
		}
	}
	grants, err := db.ListWorkspaceGrants(ctx, w.ID)
	if err != nil || len(grants) != 1 || grants[0].MemberID != user.ID || grants[0].GrantedBy != owner.ID {
		t.Fatalf("ListWorkspaceGrants = %+v, %v; want one grant to %s by %s", grants, err, user.ID, owner.ID)
	}
	got, err := db.GetMember(ctx, user.ID)
	if err != nil || !got.Access.Granted[w.ID] || len(got.Access.Owned) != 0 {
		t.Fatalf("user access = %+v, %v; want granted %s", got.Access, err, w.ID)
	}
	if err := db.GrantWorkspace(ctx, "ws_missing", user.ID, owner.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("grant on a missing workspace = %v, want ErrNotFound", err)
	}
	if err := db.RevokeWorkspaceGrant(ctx, w.ID, user.ID, ""); err != nil {
		t.Fatalf("RevokeWorkspaceGrant: %v", err)
	}
	if err := db.RevokeWorkspaceGrant(ctx, w.ID, user.ID, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second revoke = %v, want ErrNotFound", err)
	}
}

// A grant change below super admin is decided against the grantee's role
// at the write, so a promotion between the caller's check and the write
// cannot let a workspace's owner change an admin's grant.
func TestWorkspaceGrantsBelowSuperAdminRefuseAdmins(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	owner := &domain.Member{DisplayName: "Ada", PublicKey: testKey(t, "ada"), Color: "#e6194b", Role: domain.RoleAdmin}
	user := &domain.Member{DisplayName: "Cam", PublicKey: testKey(t, "cam"), Color: "#3cb44b", Role: domain.RoleCollaborator}
	other := &domain.Member{DisplayName: "Dee", PublicKey: testKey(t, "dee"), Color: "#4363d8", Role: domain.RoleCollaborator}
	for _, m := range []*domain.Member{owner, user, other} {
		if err := db.CreateMember(ctx, m); err != nil {
			t.Fatalf("CreateMember: %v", err)
		}
	}
	w := &domain.Workspace{Name: "web", OwnerID: owner.ID}
	if err := db.CreateWorkspace(ctx, w); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	if err := db.GrantWorkspace(ctx, w.ID, user.ID, owner.ID); err != nil {
		t.Fatalf("grant a collaborator: %v", err)
	}
	for _, id := range []domain.MemberID{user.ID, other.ID} {
		if _, err := db.SetMemberRole(ctx, id, domain.RoleAdmin, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.RevokeWorkspaceGrant(ctx, w.ID, user.ID, owner.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("revoke a promoted admin's grant = %v, want ErrConflict", err)
	}
	if err := db.GrantWorkspace(ctx, w.ID, other.ID, owner.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("grant a promoted admin = %v, want ErrConflict", err)
	}
	grants, err := db.ListWorkspaceGrants(ctx, w.ID)
	if err != nil || len(grants) != 1 || grants[0].MemberID != user.ID {
		t.Fatalf("grants = %+v, %v; want only %s's, untouched", grants, err, user.ID)
	}
	if err := db.RevokeWorkspaceGrant(ctx, w.ID, user.ID, ""); err != nil {
		t.Fatalf("super admin's revoke: %v", err)
	}
	if err := db.GrantWorkspace(ctx, w.ID, other.ID, ""); err != nil {
		t.Fatalf("super admin's grant: %v", err)
	}
}

// A session names its launcher, the member whose account it uses and the
// member whose home it mounts, so all three are members of its
// workspaces, its extra repositories' included.
func TestWorkspaceMembersIncludeSessionAccountsAndHomes(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	members := map[string]*domain.Member{}
	for i, name := range []string{"launcher", "account", "home", "stranger"} {
		members[name] = &domain.Member{DisplayName: name, PublicKey: testKey(t, name), Color: "#e6194b", Role: domain.RoleCollaborator}
		if err := db.CreateMember(ctx, members[name]); err != nil {
			t.Fatalf("CreateMember %d: %v", i, err)
		}
	}
	primary, extra := &domain.Workspace{Name: "web"}, &domain.Workspace{Name: "api"}
	for _, w := range []*domain.Workspace{primary, extra} {
		if err := db.CreateWorkspace(ctx, w); err != nil {
			t.Fatalf("CreateWorkspace: %v", err)
		}
	}
	run := &domain.Run{WorkspaceID: primary.ID, MemberID: members["launcher"].ID, AccountMemberID: members["account"].ID,
		HomeMemberID: members["home"].ID, Task: "t", Harness: "claude", Mode: domain.LaunchTUI, Status: domain.RunRunning}
	if err := db.CreateRun(ctx, run); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := db.SetRunRepos(ctx, run.ID, []domain.RunRepo{{WorkspaceID: extra.ID, Name: "api", Path: "/repos/api", Branch: "b", BaseBranch: "main"}}); err != nil {
		t.Fatalf("SetRunRepos: %v", err)
	}
	want := []domain.MemberID{members["account"].ID, members["home"].ID, members["launcher"].ID}
	slices.Sort(want)
	for _, ws := range []domain.WorkspaceID{primary.ID, extra.ID} {
		got, err := db.WorkspaceMembers(ctx, []domain.WorkspaceID{ws})
		slices.Sort(got)
		if err != nil || !slices.Equal(got, want) {
			t.Errorf("WorkspaceMembers(%s) = %v, %v; want %v", ws, got, err, want)
		}
	}
}

// Changing an owner is one transaction: the new owner's grant goes, a
// previous owner below super admin keeps a grant, and a target who is not
// an approved admin changes nothing.
func TestSetWorkspaceOwner(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	owner := &domain.Member{DisplayName: "Ada", PublicKey: testKey(t, "ada"), Color: "#e6194b", Role: domain.RoleAdmin}
	next := &domain.Member{DisplayName: "Bo", PublicKey: testKey(t, "bo"), Color: "#3cb44b", Role: domain.RoleAdmin}
	super := &domain.Member{DisplayName: "Sam", PublicKey: testKey(t, "sam"), Color: "#ffe119", Role: domain.RoleSuperAdmin}
	user := &domain.Member{DisplayName: "Cam", PublicKey: testKey(t, "cam"), Color: "#4363d8", Role: domain.RoleCollaborator}
	for _, m := range []*domain.Member{owner, next, super, user} {
		if err := db.CreateMember(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	w := &domain.Workspace{Name: "web", OwnerID: owner.ID}
	if err := db.CreateWorkspace(ctx, w); err != nil {
		t.Fatal(err)
	}
	if err := db.GrantWorkspace(ctx, w.ID, next.ID, super.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SetWorkspaceOwner(ctx, w.ID, user.ID, super.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("collaborator as owner = %v, want ErrConflict", err)
	}
	if _, err := db.SetWorkspaceOwner(ctx, "ws_missing", next.ID, super.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("owner of a missing workspace = %v, want ErrNotFound", err)
	}
	previous, err := db.SetWorkspaceOwner(ctx, w.ID, next.ID, super.ID)
	if err != nil || previous != owner.ID {
		t.Fatalf("SetWorkspaceOwner = %s, %v; want previous %s", previous, err, owner.ID)
	}
	if ws, gerr := db.GetWorkspace(ctx, w.ID); gerr != nil || ws.OwnerID != next.ID {
		t.Fatalf("owner after set = %+v, %v", ws, gerr)
	}
	grants, err := db.ListWorkspaceGrants(ctx, w.ID)
	if err != nil || len(grants) != 1 || grants[0].MemberID != owner.ID || grants[0].GrantedBy != super.ID {
		t.Fatalf("grants after transfer = %+v, %v; want only the previous owner's, by %s", grants, err, super.ID)
	}
	if _, err = db.SetWorkspaceOwner(ctx, w.ID, super.ID, super.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.SetWorkspaceOwner(ctx, w.ID, owner.ID, super.ID); err != nil {
		t.Fatal(err)
	}
	grants, err = db.ListWorkspaceGrants(ctx, w.ID)
	if err != nil || len(grants) != 1 || grants[0].MemberID != next.ID {
		t.Fatalf("grants after two more transfers = %+v, %v; want only %s's, none for the super admin", grants, err, next.ID)
	}
}

// SetMemberRole refuses, in its transaction, demoting the last approved
// super admin, a pending one not counting, and demoting a workspace owner
// below admin; a demotion revokes the demoted admin's invitations.
func TestSetMemberRoleGuards(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	super := &domain.Member{DisplayName: "Sam", PublicKey: testKey(t, "sam"), Color: "#ffe119", Role: domain.RoleSuperAdmin}
	pending := &domain.Member{DisplayName: "Pat", PublicKey: testKey(t, "pat"), Color: "#3cb44b", Role: domain.RoleSuperAdmin, Pending: true}
	owner := &domain.Member{DisplayName: "Ada", PublicKey: testKey(t, "ada"), Color: "#e6194b", Role: domain.RoleAdmin}
	for _, m := range []*domain.Member{super, pending, owner} {
		if err := db.CreateMember(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.SetMemberRole(ctx, super.ID, domain.RoleAdmin, ""); !errors.Is(err, ErrLastSuperAdmin) {
		t.Fatalf("demote the last approved super admin = %v, want ErrLastSuperAdmin", err)
	}
	if was, err := db.SetMemberRole(ctx, pending.ID, domain.RoleViewer, ""); err != nil || was != domain.RoleSuperAdmin {
		t.Fatalf("demote the pending super admin = %s, %v", was, err)
	}
	w := &domain.Workspace{Name: "web", OwnerID: owner.ID}
	if err := db.CreateWorkspace(ctx, w); err != nil {
		t.Fatal(err)
	}
	inv := &domain.Invitation{Provider: "github", Login: "dana", Role: domain.RoleCollaborator, Workspaces: []domain.WorkspaceID{w.ID},
		CreatedBy: owner.ID, ExpiresAt: time.Now().Add(time.Hour)}
	if err := db.CreateInvitation(ctx, inv); err != nil {
		t.Fatal(err)
	}
	var owns *OwnsWorkspacesError
	if _, err := db.SetMemberRole(ctx, owner.ID, domain.RoleCollaborator, ""); !errors.As(err, &owns) || len(owns.Workspaces) != 1 {
		t.Fatalf("demote a workspace owner = %v, want OwnsWorkspacesError naming %s", err, w.ID)
	}
	if got, err := db.GetMember(ctx, owner.ID); err != nil || got.Role != domain.RoleAdmin {
		t.Fatalf("owner after refused demotion = %+v, %v", got, err)
	}
	if _, err := db.GetInvitation(ctx, inv.ID); err != nil {
		t.Fatalf("invitation after refused demotion: %v", err)
	}
	if _, err := db.SetWorkspaceOwner(ctx, w.ID, super.ID, super.ID); err != nil {
		t.Fatal(err)
	}
	if was, err := db.SetMemberRole(ctx, owner.ID, domain.RoleCollaborator, ""); err != nil || was != domain.RoleAdmin {
		t.Fatalf("demote a former owner = %s, %v", was, err)
	}
	if _, err := db.GetInvitation(ctx, inv.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("demoted admin's invitation = %v, want ErrNotFound", err)
	}
	if _, err := db.SetMemberRole(ctx, "mem_missing", domain.RoleViewer, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("role of a missing member = %v, want ErrNotFound", err)
	}
}

// Deleting a member re-checks, in its transaction, what a role change
// may have changed since the caller looked: with two super admins, a
// removal that counted the first must still refuse once the console
// demoted it. A workspace owner is refused too.
func TestDeleteMemberGuards(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, del := range []struct {
		name string
		fn   func(*DB, domain.MemberID) error
	}{
		{"DeleteMember", func(db *DB, id domain.MemberID) error { return db.DeleteMember(ctx, id, "") }},
		{"DeleteMemberWithPreferences", func(db *DB, id domain.MemberID) error {
			return db.DeleteMemberWithPreferences(ctx, id, "", func([]string) error { return nil })
		}},
	} {
		t.Run(del.name, func(t *testing.T) {
			db := openTestDB(t)
			first := &domain.Member{DisplayName: "Sam", PublicKey: testKey(t, "sam"), Color: "#ffe119", Role: domain.RoleSuperAdmin}
			second := &domain.Member{DisplayName: "Sue", PublicKey: testKey(t, "sue"), Color: "#3cb44b", Role: domain.RoleSuperAdmin}
			owner := &domain.Member{DisplayName: "Ada", PublicKey: testKey(t, "ada"), Color: "#e6194b", Role: domain.RoleAdmin}
			for _, m := range []*domain.Member{first, second, owner} {
				if err := db.CreateMember(ctx, m); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.SetMemberRole(ctx, first.ID, domain.RoleAdmin, ""); err != nil {
				t.Fatal(err)
			}
			if err := del.fn(db, second.ID); !errors.Is(err, ErrLastSuperAdmin) {
				t.Fatalf("delete the last approved super admin = %v, want ErrLastSuperAdmin", err)
			}
			if _, err := db.GetMember(ctx, second.ID); err != nil {
				t.Fatalf("last super admin after refused delete: %v", err)
			}
			w := &domain.Workspace{Name: "web", OwnerID: owner.ID}
			if err := db.CreateWorkspace(ctx, w); err != nil {
				t.Fatal(err)
			}
			var owns *OwnsWorkspacesError
			if err := del.fn(db, owner.ID); !errors.As(err, &owns) || len(owns.Workspaces) != 1 {
				t.Fatalf("delete a workspace owner = %v, want OwnsWorkspacesError", err)
			}
			if err := del.fn(db, first.ID); err != nil {
				t.Fatalf("delete an admin who owns nothing: %v", err)
			}
			if err := del.fn(db, "mem_missing"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("delete a missing member = %v, want ErrNotFound", err)
			}
		})
	}
}

// An invitation's role must be one its creator may grant when it is
// accepted: an admin's admin invitation, from before super admins, adds
// no admin.
func TestAcceptingNeedsACreatorWhoGrantsTheRoleNow(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	admin := &domain.Member{DisplayName: "Ada", PublicKey: testKey(t, "ada"), Color: "#e6194b", Role: domain.RoleAdmin}
	if err := db.CreateMember(ctx, admin); err != nil {
		t.Fatal(err)
	}
	invite := func(login string, role domain.Role) *domain.Invitation {
		inv := &domain.Invitation{Provider: "github", Login: login, Role: role, CreatedBy: admin.ID, ExpiresAt: now.Add(time.Hour)}
		if err := db.CreateInvitation(ctx, inv); err != nil {
			t.Fatal(err)
		}
		return inv
	}
	stale, fine := invite("dana", domain.RoleAdmin), invite("eve", domain.RoleCollaborator)
	if _, err := db.AcceptInvitation(ctx, stale.ID, &domain.Identity{Provider: "github", Subject: "4004", Login: "dana"},
		&domain.Member{DisplayName: "Dana", Color: "#4363d8"}, now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("accept an admin's admin invitation = %v, want ErrNotFound", err)
	}
	if _, err := db.GetMemberByIdentity(ctx, "github", "4004"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("member after a refused acceptance = %v, want none", err)
	}
	m, err := db.AcceptInvitation(ctx, fine.ID, &domain.Identity{Provider: "github", Subject: "5005", Login: "eve"},
		&domain.Member{DisplayName: "Eve", Color: "#911eb4"}, now)
	if err != nil || m.Role != domain.RoleCollaborator {
		t.Fatalf("accept an admin's collaborator invitation = %+v, %v", m, err)
	}
}

// Accepting an invitation, or joining with a code, creates the member with
// a grant on each named workspace that still exists and its issuer still
// administers: any for a super admin, the owned ones for an admin.
func TestJoiningGrantsWhatTheIssuerAdministers(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	admin := &domain.Member{DisplayName: "Ada", PublicKey: testKey(t, "ada"), Color: "#e6194b", Role: domain.RoleAdmin}
	other := &domain.Member{DisplayName: "Bo", PublicKey: testKey(t, "bo"), Color: "#3cb44b", Role: domain.RoleAdmin}
	super := &domain.Member{DisplayName: "Sam", PublicKey: testKey(t, "sam"), Color: "#ffe119", Role: domain.RoleSuperAdmin}
	for _, m := range []*domain.Member{admin, other, super} {
		if err := db.CreateMember(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	owned := &domain.Workspace{Name: "web", OwnerID: admin.ID}
	others := &domain.Workspace{Name: "api", OwnerID: other.ID}
	gone := &domain.Workspace{Name: "old", OwnerID: admin.ID}
	for _, w := range []*domain.Workspace{owned, others, gone} {
		if err := db.CreateWorkspace(ctx, w); err != nil {
			t.Fatal(err)
		}
	}
	named := []domain.WorkspaceID{owned.ID, others.ID, gone.ID}
	inv := &domain.Invitation{Provider: "github", Login: "dana", Role: domain.RoleCollaborator, Workspaces: named,
		CreatedBy: admin.ID, ExpiresAt: now.Add(time.Hour)}
	if err := db.CreateInvitation(ctx, inv); err != nil {
		t.Fatal(err)
	}
	if got, err := db.GetInvitation(ctx, inv.ID); err != nil || len(got.Workspaces) != 3 {
		t.Fatalf("stored invitation = %+v, %v; want its three workspaces", got, err)
	}
	if err := db.DeleteWorkspace(ctx, gone.ID); err != nil {
		t.Fatal(err)
	}
	dana, err := db.AcceptInvitation(ctx, inv.ID, &domain.Identity{Provider: "github", Subject: "4004", Login: "dana"},
		&domain.Member{DisplayName: "Dana", Color: "#4363d8"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if access, aerr := db.MemberWorkspaces(ctx, dana.ID); aerr != nil || len(access.Granted) != 1 || !access.Granted[owned.ID] {
		t.Fatalf("accepted member's workspaces = %+v, %v; want only the issuer's surviving one", access, aerr)
	}

	joined := &domain.Member{DisplayName: "Jo", PublicKey: testKey(t, "jo"), Color: "#f58231", Role: domain.RoleCollaborator}
	if err = db.JoinMember(ctx, joined, []domain.WorkspaceID{owned.ID, others.ID}, super.ID); err != nil {
		t.Fatal(err)
	}
	if access, aerr := db.MemberWorkspaces(ctx, joined.ID); aerr != nil || len(access.Granted) != 2 {
		t.Fatalf("member a super admin's code admitted has %+v, %v; want both workspaces", access, aerr)
	}
	grants, err := db.ListWorkspaceGrants(ctx, others.ID)
	if err != nil || len(grants) != 1 || grants[0].GrantedBy != super.ID {
		t.Fatalf("grants on %s = %+v, %v; want one by the super admin", others.ID, grants, err)
	}
}

// A merge that would leave a workspace owner below admin is refused,
// naming the workspaces, unless the admin side keeps its role.
func TestMergeRefusesDemotingAWorkspaceOwner(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	super := &domain.Member{DisplayName: "Sam", PublicKey: testKey(t, "sam"), Color: "#ffe119", Role: domain.RoleSuperAdmin}
	owner := &domain.Member{DisplayName: "A", TailnetLogin: "a@", Color: "#3cb44b", Role: domain.RoleAdmin}
	user := &domain.Member{DisplayName: "U", PublicKey: testKey(t, "u"), Color: "#e6194b", Role: domain.RoleCollaborator}
	for _, m := range []*domain.Member{super, owner, user} {
		if err := db.CreateMember(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	w := &domain.Workspace{Name: "web", OwnerID: owner.ID}
	if err := db.CreateWorkspace(ctx, w); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateMergeCode(ctx, owner.ID, "code", now, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	plan, err := db.PlanMerge(ctx, "code", user.ID, "", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Minted.OwnedWorkspaces) != 1 || plan.Minted.OwnedWorkspaces[0] != w.ID {
		t.Fatalf("plan owned workspaces = %v, want %s", plan.Minted.OwnedWorkspaces, w.ID)
	}
	pm, err := db.ProposeMerge(ctx, MergeProposal{CodeHash: "code", Proposer: user.ID, Keep: user.ID, Digest: plan.Digest, Now: now, Expires: now.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	accept := MergeAccept{ProposalID: pm.ProposalID, Caller: owner.ID, Digest: plan.Digest, Now: now}
	if _, err = db.AcceptMerge(ctx, accept); !isRefusal(err, "owns workspaces ["+string(w.ID)+"]") {
		t.Fatalf("merge demoting an owner = %v, want refused naming %s", err, w.ID)
	}
	accept.KeepAdmin = true
	merge, err := db.AcceptMerge(ctx, accept)
	if err != nil || merge.RoleAfter != domain.RoleAdmin {
		t.Fatalf("merge keeping admin = %+v, %v", merge, err)
	}
	if got, err := db.GetWorkspace(ctx, w.ID); err != nil || got.OwnerID != user.ID {
		t.Fatalf("workspace after the merge = %+v, %v; want the survivor owning it", got, err)
	}
}

// keep_admin keeps a super admin a super admin, and losing that role is a
// demotion.
func TestMergeKeepsSuperAdmin(t *testing.T) {
	t.Parallel()
	plan := &MergePlan{SuperAdmins: 2,
		Minted:   MergeSnapshot{Member: domain.Member{ID: "s", Role: domain.RoleSuperAdmin}},
		Proposer: MergeSnapshot{Member: domain.Member{ID: "u", Role: domain.RoleViewer}}}
	for _, tc := range []struct {
		minted, proposer bool
		want             domain.Role
	}{{false, false, domain.RoleViewer}, {true, false, domain.RoleSuperAdmin}, {false, true, domain.RoleViewer}} {
		if got := plan.Role(tc.minted, tc.proposer); got != tc.want {
			t.Errorf("Role(%v, %v) = %s, want %s", tc.minted, tc.proposer, got, tc.want)
		}
	}
	plan.Proposer.Member.Role = domain.RoleAdmin
	if got := plan.Role(false, true); got != domain.RoleAdmin {
		t.Errorf("an admin proposer keeping its role = %s, want admin: it does not lift the super admin's", got)
	}
	if plan.LeavesNoSuperAdmin(domain.RoleAdmin) {
		t.Error("a merge beside another super admin takes the last one")
	}
	plan.SuperAdmins = 1
	if !plan.LeavesNoSuperAdmin(domain.RoleAdmin) || plan.LeavesNoSuperAdmin(domain.RoleSuperAdmin) {
		t.Error("LeavesNoSuperAdmin does not guard the last super admin")
	}
}

// An approval that may admit only members below admin is decided in the
// same write, so a promotion landing first is refused, not admitted.
func TestApproveMemberBelowAdminIsGuardedInTheWrite(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	pending := &domain.Member{DisplayName: "Pat", PublicKey: testKey(t, "pat"), Color: "#e6194b", Role: domain.RoleCollaborator, Pending: true}
	admin := &domain.Member{DisplayName: "Ada", PublicKey: testKey(t, "ada"), Color: "#3cb44b", Role: domain.RoleAdmin}
	for _, m := range []*domain.Member{pending, admin} {
		if err := db.CreateMember(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.SetMemberRole(ctx, pending.ID, domain.RoleSuperAdmin, ""); err != nil {
		t.Fatal(err)
	}
	if err := db.ApproveMember(ctx, pending.ID, admin.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("approving a promoted member below admin only = %v, want ErrConflict", err)
	}
	if m, err := db.GetMember(ctx, pending.ID); err != nil || !m.Pending {
		t.Fatalf("member after the refused approval = %+v, %v; want still pending", m, err)
	}
	if err := db.ApproveMember(ctx, pending.ID, ""); err != nil {
		t.Fatalf("a super admin's approval: %v", err)
	}
}

// A role change made by a member who is no longer a super admin is refused
// in its own transaction, so a console demotion landing first wins.
func TestSetMemberRoleChecksTheCallerInTheTransaction(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	caller := &domain.Member{DisplayName: "Sam", PublicKey: testKey(t, "sam"), Color: "#e6194b", Role: domain.RoleSuperAdmin}
	other := &domain.Member{DisplayName: "Sue", PublicKey: testKey(t, "sue"), Color: "#3cb44b", Role: domain.RoleSuperAdmin}
	target := &domain.Member{DisplayName: "Cam", PublicKey: testKey(t, "cam"), Color: "#ffe119", Role: domain.RoleCollaborator}
	for _, m := range []*domain.Member{caller, other, target} {
		if err := db.CreateMember(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.SetMemberRole(ctx, caller.ID, domain.RoleAdmin, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SetMemberRole(ctx, target.ID, domain.RoleSuperAdmin, caller.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("a demoted caller's promotion = %v, want ErrConflict", err)
	}
	if m, err := db.GetMember(ctx, target.ID); err != nil || m.Role != domain.RoleCollaborator {
		t.Fatalf("target after the refused promotion = %+v, %v", m, err)
	}
	if _, err := db.SetMemberRole(ctx, target.ID, domain.RoleSuperAdmin, other.ID); err != nil {
		t.Fatalf("a super admin's promotion: %v", err)
	}
}

// Every write whose authority is the caller's role reads that role in the
// write, so a console demotion, or a lost ownership, landing between the
// RPC's check and the write is refused and changes nothing.
func TestPrivilegedWritesCheckTheCallerInTheWrite(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	sam := &domain.Member{DisplayName: "Sam", PublicKey: testKey(t, "sam"), Color: "#e6194b", Role: domain.RoleSuperAdmin}
	sue := &domain.Member{DisplayName: "Sue", PublicKey: testKey(t, "sue"), Color: "#3cb44b", Role: domain.RoleSuperAdmin}
	ada := &domain.Member{DisplayName: "Ada", PublicKey: testKey(t, "ada"), Color: "#ffe119", Role: domain.RoleAdmin}
	bo := &domain.Member{DisplayName: "Bo", PublicKey: testKey(t, "bo"), Color: "#4363d8", Role: domain.RoleAdmin}
	dee := &domain.Member{DisplayName: "Dee", PublicKey: testKey(t, "dee"), Color: "#f58231", Role: domain.RoleCollaborator}
	pat := &domain.Member{DisplayName: "Pat", PublicKey: testKey(t, "pat"), Color: "#911eb4", Role: domain.RoleCollaborator, Pending: true}
	for _, m := range []*domain.Member{sam, sue, ada, bo, dee, pat} {
		if err := db.CreateMember(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	w := &domain.Workspace{Name: "web", OwnerID: ada.ID}
	if err := db.CreateWorkspace(ctx, w); err != nil {
		t.Fatal(err)
	}
	if err := db.GrantWorkspace(ctx, w.ID, dee.ID, sue.ID); err != nil {
		t.Fatal(err)
	}
	pids, fewer := int64(64), int64(1)
	if _, err := db.UpdateServerRunLimits(ctx, sue.ID, nil, nil, &pids); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SetMemberRole(ctx, sam.ID, domain.RoleAdmin, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SetMemberRole(ctx, bo.ID, domain.RoleCollaborator, ""); err != nil {
		t.Fatal(err)
	}

	refused := map[string]error{
		"GrantWorkspace":       db.GrantWorkspace(ctx, w.ID, pat.ID, sam.ID),
		"RevokeWorkspaceGrant": db.RevokeWorkspaceGrant(ctx, w.ID, dee.ID, sam.ID),
		"DeleteMember":         db.DeleteMember(ctx, dee.ID, sam.ID),
		"DeleteMemberWithPreferences": db.DeleteMemberWithPreferences(ctx, dee.ID, sam.ID,
			func([]string) error { return nil }),
		"ApproveMember": db.ApproveMember(ctx, pat.ID, bo.ID),
	}
	_, refused["SetWorkspaceOwner"] = db.SetWorkspaceOwner(ctx, w.ID, sam.ID, sam.ID)
	_, refused["UpdateServerRunLimits"] = db.UpdateServerRunLimits(ctx, sam.ID, nil, nil, &fewer)
	for name, err := range refused {
		if !errors.Is(err, ErrConflict) {
			t.Errorf("%s by a demoted caller = %v, want ErrConflict", name, err)
		}
	}

	if ws, err := db.GetWorkspace(ctx, w.ID); err != nil || ws.OwnerID != ada.ID {
		t.Fatalf("workspace after the refused owner change = %+v, %v; want owner %s", ws, err, ada.ID)
	}
	grants, err := db.ListWorkspaceGrants(ctx, w.ID)
	if err != nil || len(grants) != 1 || grants[0].MemberID != dee.ID {
		t.Fatalf("grants after the refused changes = %+v, %v; want only %s's", grants, err, dee.ID)
	}
	if m, err := db.GetMember(ctx, pat.ID); err != nil || !m.Pending {
		t.Fatalf("member after the refused approval = %+v, %v; want still pending", m, err)
	}
	if limits, err := db.ServerRunLimits(ctx); err != nil || limits.Pids != pids {
		t.Fatalf("limits after the refused update = %+v, %v; want pids %d", limits, err, pids)
	}

	if err := db.GrantWorkspace(ctx, w.ID, bo.ID, ada.ID); err != nil {
		t.Fatalf("the owner's grant: %v", err)
	}
	if _, err := db.SetWorkspaceOwner(ctx, w.ID, sue.ID, sue.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.RevokeWorkspaceGrant(ctx, w.ID, bo.ID, ada.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("revoke by a former owner = %v, want ErrConflict", err)
	}
	if err := db.ApproveMember(ctx, pat.ID, ada.ID); err != nil {
		t.Fatalf("an admin's approval of a collaborator: %v", err)
	}
	if err := db.DeleteMember(ctx, dee.ID, sue.ID); err != nil {
		t.Fatalf("a super admin's removal: %v", err)
	}
}
