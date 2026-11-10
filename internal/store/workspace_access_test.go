package store

import (
	"context"
	"errors"
	"path/filepath"
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
	if err := db.RevokeWorkspaceGrant(ctx, w.ID, user.ID); err != nil {
		t.Fatalf("RevokeWorkspaceGrant: %v", err)
	}
	if err := db.RevokeWorkspaceGrant(ctx, w.ID, user.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second revoke = %v, want ErrNotFound", err)
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
	if err := db.GrantWorkspace(ctx, w.ID, next.ID, owner.ID); err != nil {
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
	if _, err := db.SetMemberRole(ctx, super.ID, domain.RoleAdmin); !errors.Is(err, ErrLastSuperAdmin) {
		t.Fatalf("demote the last approved super admin = %v, want ErrLastSuperAdmin", err)
	}
	if was, err := db.SetMemberRole(ctx, pending.ID, domain.RoleViewer); err != nil || was != domain.RoleSuperAdmin {
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
	if _, err := db.SetMemberRole(ctx, owner.ID, domain.RoleCollaborator); !errors.As(err, &owns) || len(owns.Workspaces) != 1 {
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
	if was, err := db.SetMemberRole(ctx, owner.ID, domain.RoleCollaborator); err != nil || was != domain.RoleAdmin {
		t.Fatalf("demote a former owner = %s, %v", was, err)
	}
	if _, err := db.GetInvitation(ctx, inv.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("demoted admin's invitation = %v, want ErrNotFound", err)
	}
	if _, err := db.SetMemberRole(ctx, "mem_missing", domain.RoleViewer); !errors.Is(err, ErrNotFound) {
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
		{"DeleteMember", func(db *DB, id domain.MemberID) error { return db.DeleteMember(ctx, id) }},
		{"DeleteMemberWithPreferences", func(db *DB, id domain.MemberID) error {
			return db.DeleteMemberWithPreferences(ctx, id, func([]string) error { return nil })
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
			if _, err := db.SetMemberRole(ctx, first.ID, domain.RoleAdmin); err != nil {
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
	if _, err = db.AcceptMerge(ctx, accept); !isRefusal(err, "owns workspaces "+string(w.ID)) {
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
