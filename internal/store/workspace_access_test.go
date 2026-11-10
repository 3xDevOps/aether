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
	if err := db.SetWorkspaceOwner(ctx, w.ID, user.ID); err != nil {
		t.Fatalf("SetWorkspaceOwner: %v", err)
	}
	if ws, err := db.GetWorkspace(ctx, w.ID); err != nil || ws.OwnerID != user.ID {
		t.Fatalf("owner after set = %+v, %v", ws, err)
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
	if access, err := db.MemberWorkspaces(ctx, dana.ID); err != nil || len(access.Granted) != 1 || !access.Granted[owned.ID] {
		t.Fatalf("accepted member's workspaces = %+v, %v; want only the issuer's surviving one", access, err)
	}

	joined := &domain.Member{DisplayName: "Jo", PublicKey: testKey(t, "jo"), Color: "#f58231", Role: domain.RoleCollaborator}
	if err := db.JoinMember(ctx, joined, []domain.WorkspaceID{owned.ID, others.ID}, super.ID); err != nil {
		t.Fatal(err)
	}
	if access, err := db.MemberWorkspaces(ctx, joined.ID); err != nil || len(access.Granted) != 2 {
		t.Fatalf("member a super admin's code admitted has %+v, %v; want both workspaces", access, err)
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
