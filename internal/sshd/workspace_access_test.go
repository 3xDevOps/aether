package sshd

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// accessEnv is a scoped server with the threat matrix's members: super
// admin S (the seeded member), admins A and B owning workspaces wsA and
// wsB, collaborator U and viewer V granted nothing yet.
type accessEnv struct {
	*testEnv
	a, b, u, v       *domain.Member
	aKey, bKey, uKey ssh.Signer
	wsA, wsB         *domain.Workspace
}

func newAccessEnv(t *testing.T, mod func(*Config)) *accessEnv {
	t.Helper()
	e := &accessEnv{testEnv: newScopedTestEnv(t, mod)}
	e.aKey, e.a = addMember(t, e.testEnv, "A", domain.RoleAdmin, false)
	e.bKey, e.b = addMember(t, e.testEnv, "B", domain.RoleAdmin, false)
	e.uKey, e.u = addMember(t, e.testEnv, "U", domain.RoleCollaborator, false)
	_, e.v = addMember(t, e.testEnv, "V", domain.RoleViewer, false)
	e.wsA = ownedWorkspace(t, e.testEnv, "web", e.a.ID)
	e.wsB = ownedWorkspace(t, e.testEnv, "api", e.b.ID)
	return e
}

func ownedWorkspace(t *testing.T, e *testEnv, name string, owner domain.MemberID) *domain.Workspace {
	t.Helper()
	w := &domain.Workspace{Name: name, BaseBranch: "main", OwnerID: owner}
	if err := e.db.CreateWorkspace(context.Background(), w); err != nil {
		t.Fatalf("create workspace %s: %v", name, err)
	}
	return w
}

func wantErrCode(t *testing.T, err error, code int, what string) *protocol.Error {
	t.Helper()
	var pe *protocol.Error
	if !errors.As(err, &pe) || pe.Code != code {
		t.Fatalf("%s = %v, want code %d", what, err, code)
	}
	return pe
}

// wantMissing asserts err is exactly the error a workspace that never
// existed gets.
func wantMissing(t *testing.T, err error, what string) {
	t.Helper()
	pe := wantErrCode(t, err, protocol.CodeNotFound, what)
	if pe.Message != "store: not found" {
		t.Fatalf("%s message = %q, want the not-found of a missing workspace", what, pe.Message)
	}
}

func (e *accessEnv) access(t *testing.T, c *protocol.Client, ws domain.WorkspaceID) protocol.WorkspaceAccessResult {
	t.Helper()
	var out protocol.WorkspaceAccessResult
	if err := c.Call(protocol.MethodWorkspaceAccessList, protocol.WorkspaceAccessParams{WorkspaceID: string(ws)}, &out); err != nil {
		t.Fatalf("workspace.access.list %s: %v", ws, err)
	}
	return out
}

func grantees(res protocol.WorkspaceAccessResult) []string {
	out := []string{}
	for _, g := range res.Grants {
		out = append(out, g.MemberID)
	}
	slices.Sort(out)
	return out
}

func sortedIDs(ids ...domain.MemberID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = string(id)
	}
	slices.Sort(out)
	return out
}

func grant(c *protocol.Client, ws domain.WorkspaceID, members ...domain.MemberID) error {
	p := protocol.WorkspaceAccessGrantParams{WorkspaceID: string(ws)}
	for _, m := range members {
		p.MemberIDs = append(p.MemberIDs, string(m))
	}
	return c.Call(protocol.MethodWorkspaceAccessGrant, p, nil)
}

// A workspace's owner grants collaborators and viewers into it and no
// one into another admin's workspace, which answers exactly like one that
// does not exist. A super admin grants anyone anywhere; granting the
// owner or a super admin changes nothing. Each grant is published in its
// workspace.
func TestWorkspaceAccessGrantRules(t *testing.T) {
	t.Parallel()
	e := newAccessEnv(t, nil)
	ctx := context.Background()
	sub, err := e.bus.Subscribe(ctx, events.SubscribeOptions{Filter: events.Filter{Types: []events.Type{events.TypeWorkspaceAccess}}})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close() //nolint:errcheck
	a, s, u := controlAs(t, e.testEnv, e.aKey), controlClient(t, e.testEnv), controlAs(t, e.testEnv, e.uKey)

	if err = grant(a, e.wsA.ID, e.u.ID, e.v.ID); err != nil {
		t.Fatalf("owner grants a collaborator and a viewer: %v", err)
	}
	if got, want := grantees(e.access(t, a, e.wsA.ID)), sortedIDs(e.u.ID, e.v.ID); !slices.Equal(got, want) {
		t.Fatalf("grants on wsA = %v, want %v", got, want)
	}
	for range 2 {
		select {
		case ev := <-sub.Events():
			p, ok := ev.Payload.(events.WorkspaceAccessPayload)
			if !ok || ev.WorkspaceID != e.wsA.ID || p.Action != events.AccessGranted || ev.ActorID != e.a.ID {
				t.Fatalf("workspace.access event = %+v", ev)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("no workspace.access event for a grant")
		}
	}

	wantMissing(t, grant(a, e.wsB.ID, e.u.ID), "A grants into B's workspace")
	wantMissing(t, grant(a, "ws-never-existed", e.u.ID), "A grants into a missing workspace")
	_ = wantErrCode(t, grant(a, e.wsA.ID, e.b.ID), protocol.CodeDenied, "A grants an admin")
	_, pending := addMember(t, e.testEnv, "P", domain.RoleCollaborator, true)
	_ = wantErrCode(t, grant(a, e.wsA.ID, pending.ID), protocol.CodeInvalidState, "A grants a pending member")
	_ = wantErrCode(t, u.Call(protocol.MethodWorkspaceAccessList, protocol.WorkspaceAccessParams{WorkspaceID: string(e.wsA.ID)}, nil),
		protocol.CodeDenied, "a granted collaborator lists access")
	wantMissing(t, u.Call(protocol.MethodWorkspaceAccessList, protocol.WorkspaceAccessParams{WorkspaceID: string(e.wsB.ID)}, nil),
		"an ungranted collaborator lists access")

	if err = grant(s, e.wsA.ID, e.b.ID, e.a.ID, e.member.ID); err != nil {
		t.Fatalf("super admin grants an admin, the owner and itself: %v", err)
	}
	if got, want := grantees(e.access(t, s, e.wsA.ID)), sortedIDs(e.b.ID, e.u.ID, e.v.ID); !slices.Equal(got, want) {
		t.Fatalf("grants on wsA = %v, want %v: the owner and the super admin need none", got, want)
	}
	access, err := e.db.MemberWorkspaces(ctx, e.b.ID)
	if err != nil || !access.Granted[e.wsA.ID] || !access.Owned[e.wsB.ID] {
		t.Fatalf("B's workspaces = %+v, %v", access, err)
	}
}

// Revoking a grant is refused, naming what uses it, while the member has
// a live session in the workspace; once it ends, the member no longer
// reaches the workspace.
func TestWorkspaceAccessRevokeRefusedWhileInUse(t *testing.T) {
	t.Parallel()
	e := newAccessEnv(t, nil)
	ctx := context.Background()
	a := controlAs(t, e.testEnv, e.aKey)
	if err := grant(a, e.wsA.ID, e.u.ID); err != nil {
		t.Fatal(err)
	}
	run := &domain.Run{WorkspaceID: e.wsA.ID, MemberID: e.u.ID, Task: "t", Harness: "claude", Mode: domain.LaunchTUI,
		Status: domain.RunRunning, Branch: "aether/run-u"}
	if err := e.db.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	revoke := protocol.WorkspaceAccessRevokeParams{WorkspaceID: string(e.wsA.ID), MemberID: string(e.u.ID)}
	pe := wantErrCode(t, a.Call(protocol.MethodWorkspaceAccessRevoke, revoke, nil), protocol.CodeConflict, "revoke while in use")
	if !strings.Contains(pe.Message, "in use") || !strings.Contains(pe.Message, string(run.ID)) {
		t.Fatalf("revoke refusal %q does not name the session in use", pe.Message)
	}
	wantMissing(t, a.Call(protocol.MethodWorkspaceAccessRevoke,
		protocol.WorkspaceAccessRevokeParams{WorkspaceID: string(e.wsB.ID), MemberID: string(e.u.ID)}, nil), "A revokes in B's workspace")

	if _, err := e.db.UpdateRunStatus(ctx, run.ID, domain.RunCompleted, "", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := a.Call(protocol.MethodWorkspaceAccessRevoke, revoke, nil); err != nil {
		t.Fatalf("revoke once the session ended: %v", err)
	}
	if access, err := e.db.MemberWorkspaces(ctx, e.u.ID); err != nil || access.Granted[e.wsA.ID] {
		t.Fatalf("U's workspaces after revoke = %+v, %v", access, err)
	}
	_ = wantErrCode(t, a.Call(protocol.MethodWorkspaceAccessRevoke, revoke, nil), protocol.CodeNotFound, "revoke a grant that is gone")
}

// Only a super admin changes a workspace's owner, and only to an admin or
// super admin; the previous owner keeps reaching it as a grant.
func TestWorkspaceOwnerSet(t *testing.T) {
	t.Parallel()
	e := newAccessEnv(t, nil)
	ctx := context.Background()
	a, s := controlAs(t, e.testEnv, e.aKey), controlClient(t, e.testEnv)
	set := func(c *protocol.Client, ws domain.WorkspaceID, owner domain.MemberID) error {
		return c.Call(protocol.MethodWorkspaceOwnerSet, protocol.WorkspaceOwnerSetParams{WorkspaceID: string(ws), MemberID: string(owner)}, nil)
	}
	_ = wantErrCode(t, set(a, e.wsA.ID, e.b.ID), protocol.CodeDenied, "the owner sets their workspace's owner")
	_ = wantErrCode(t, set(a, e.wsB.ID, e.a.ID), protocol.CodeDenied, "A sets B's workspace's owner")
	_ = wantErrCode(t, set(s, e.wsA.ID, e.u.ID), protocol.CodeInvalidState, "owner set to a collaborator")
	if err := grant(s, e.wsA.ID, e.b.ID); err != nil {
		t.Fatal(err)
	}
	if err := set(s, e.wsA.ID, e.b.ID); err != nil {
		t.Fatalf("super admin sets the owner: %v", err)
	}
	res := e.access(t, s, e.wsA.ID)
	if res.OwnerMemberID != string(e.b.ID) || !slices.Equal(grantees(res), sortedIDs(e.a.ID)) {
		t.Fatalf("access after owner change = %+v, want B owning and A granted", res)
	}
	actor, err := resolveActor(ctx, e.store, e.a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if role, ok := actor.RoleIn(e.wsA.ID); !ok || role != domain.RoleCollaborator {
		t.Fatalf("A's role in the workspace they gave away = %q, %v; want collaborator", role, ok)
	}
}

// workspace.add makes its caller the owner and is for admins only;
// workspace.delete is for the workspace's owner, and another admin's
// workspace is not found.
func TestWorkspaceAddOwnsAndDeleteIsTheOwners(t *testing.T) {
	t.Parallel()
	e := newAccessEnv(t, nil)
	a, u := controlAs(t, e.testEnv, e.aKey), controlAs(t, e.testEnv, e.uKey)
	var added protocol.WorkspaceAddResult
	if err := a.Call(protocol.MethodWorkspaceAdd, protocol.WorkspaceAddParams{Name: "docs", Environment: protocol.WorkspaceEnvironment{}}, &added); err != nil {
		t.Fatalf("admin workspace.add: %v", err)
	}
	if added.Workspace.OwnerMemberID != string(e.a.ID) {
		t.Fatalf("added workspace = %+v, want it owned by its creator", added.Workspace)
	}
	_ = wantErrCode(t, u.Call(protocol.MethodWorkspaceAdd, protocol.WorkspaceAddParams{Name: "x", Environment: protocol.WorkspaceEnvironment{}}, nil),
		protocol.CodeDenied, "collaborator workspace.add")
	del := func(ws domain.WorkspaceID) error {
		return a.Call(protocol.MethodWorkspaceDelete, protocol.WorkspaceDeleteParams{WorkspaceID: string(ws)}, nil)
	}
	wantMissing(t, del(e.wsB.ID), "A deletes B's workspace")
	wantMissing(t, del("ws-never-existed"), "A deletes a missing workspace")
	_ = wantErrCode(t, del(e.wsA.ID), protocol.CodeUnavailable, "A deletes their own workspace on a server without deletion")
}

// member.role and member.remove are a super admin's: an admin promotes
// no one and demotes no super admin. A member who owns workspaces is
// neither demoted below admin nor removed until each has a new owner.
func TestRolesAndRemovalAreSuperAdmins(t *testing.T) {
	t.Parallel()
	e := newAccessEnv(t, nil)
	a, s := controlAs(t, e.testEnv, e.aKey), controlClient(t, e.testEnv)
	role := func(c *protocol.Client, m domain.MemberID, r domain.Role) error {
		return c.Call(protocol.MethodMemberRole, protocol.MemberRoleParams{MemberID: string(m), Role: string(r)}, nil)
	}
	_ = wantErrCode(t, role(a, e.u.ID, domain.RoleAdmin), protocol.CodeDenied, "A promotes U to admin")
	_ = wantErrCode(t, role(a, e.member.ID, domain.RoleViewer), protocol.CodeDenied, "A demotes S")
	_ = wantErrCode(t, a.Call(protocol.MethodMemberRemove, protocol.MemberRemoveParams{MemberID: string(e.u.ID)}, nil),
		protocol.CodeDenied, "A removes U")

	pe := wantErrCode(t, role(s, e.a.ID, domain.RoleCollaborator), protocol.CodeInvalidState, "demote an owner")
	if !strings.Contains(pe.Message, string(e.wsA.ID)) || !strings.Contains(pe.Message, "aether workspace owner") {
		t.Fatalf("demotion refusal %q does not name the workspace and the command", pe.Message)
	}
	pe = wantErrCode(t, s.Call(protocol.MethodMemberRemove, protocol.MemberRemoveParams{MemberID: string(e.a.ID)}, nil),
		protocol.CodeInvalidState, "remove an owner")
	if !strings.Contains(pe.Message, string(e.wsA.ID)) {
		t.Fatalf("removal refusal %q does not name the workspace", pe.Message)
	}
	if err := role(s, e.a.ID, domain.RoleSuperAdmin); err != nil {
		t.Fatalf("promote an admin to super admin: %v", err)
	}
	if err := role(s, e.member.ID, domain.RoleAdmin); err != nil {
		t.Fatalf("demote a super admin beside another: %v", err)
	}
	_ = wantErrCode(t, role(controlAs(t, e.testEnv, e.aKey), e.a.ID, domain.RoleAdmin), protocol.CodeDenied, "demote the last super admin")
}

// An admin's invite code names at least one workspace, only ones they
// own; the member it admits is granted them. A workspace its issuer no
// longer owns when the code is used is not granted.
func TestInviteCodeGrantsTheIssuersWorkspaces(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "invites")
	e := newAccessEnv(t, func(c *Config) { c.InvitesDir = dir })
	ctx := context.Background()
	a, s := controlAs(t, e.testEnv, e.aKey), controlClient(t, e.testEnv)
	invite := func(c *protocol.Client, ws ...domain.WorkspaceID) (protocol.MemberInviteResult, error) {
		p := protocol.MemberInviteParams{}
		for _, w := range ws {
			p.WorkspaceIDs = append(p.WorkspaceIDs, string(w))
		}
		var out protocol.MemberInviteResult
		return out, c.Call(protocol.MethodMemberInvite, p, &out)
	}
	_, err := invite(a)
	_ = wantErrCode(t, err, protocol.CodeInvalidParams, "admin invite code with no workspace")
	_, err = invite(a, e.wsA.ID, e.wsB.ID)
	wantMissing(t, err, "admin invite code into B's workspace")
	join := func(code string) *domain.Member {
		t.Helper()
		client, derr := e.dialAs("invite:"+code+":Jo", nil, ssh.PublicKeys(newSigner(t)))
		if derr != nil {
			t.Fatalf("invite dial: %v", derr)
		}
		t.Cleanup(func() { _ = client.Close() })
		m, merr := e.db.GetMember(ctx, domain.MemberID(serverInfoMember(t, controlClientOn(t, client)).ID))
		if merr != nil {
			t.Fatal(merr)
		}
		return m
	}
	code, err := invite(a, e.wsA.ID)
	if err != nil {
		t.Fatalf("admin invite code into their workspace: %v", err)
	}
	if m := join(code.Code); !m.Access.Granted[e.wsA.ID] || len(m.Access.Granted) != 1 {
		t.Fatalf("joined member's workspaces = %+v, want wsA", m.Access)
	}

	stale, err := invite(a, e.wsA.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Call(protocol.MethodWorkspaceOwnerSet, protocol.WorkspaceOwnerSetParams{WorkspaceID: string(e.wsA.ID), MemberID: string(e.b.ID)}, nil); err != nil {
		t.Fatal(err)
	}
	if m := join(stale.Code); len(m.Access.Granted) != 0 {
		t.Fatalf("member admitted by a code whose issuer gave the workspace away has %+v, want none", m.Access)
	}
	if _, err := invite(s); err != nil {
		t.Fatalf("super admin invite code with no workspace: %v", err)
	}
}

// An admin invites collaborators and viewers into workspaces they own,
// never an admin and never into another's workspace; a super admin
// invites anyone. An admin lists only their own invitations.
func TestEdgeInvitationScopes(t *testing.T) {
	t.Parallel()
	e := newAccessEnv(t, nil)
	e.srv.SetEdgeOwner(&fakeEdgeOwner{})
	a, s := controlAs(t, e.testEnv, e.aKey), controlClient(t, e.testEnv)
	create := func(c *protocol.Client, login string, role domain.Role, ws ...domain.WorkspaceID) error {
		p := protocol.MemberInvitationCreateParams{Login: login, Role: string(role)}
		for _, w := range ws {
			p.WorkspaceIDs = append(p.WorkspaceIDs, string(w))
		}
		return c.Call(protocol.MethodMemberInvitationCreate, p, nil)
	}
	_ = wantErrCode(t, create(a, "dana", domain.RoleAdmin, e.wsA.ID), protocol.CodeDenied, "A invites an admin")
	wantMissing(t, create(a, "dana", domain.RoleCollaborator, e.wsB.ID), "A invites into B's workspace")
	_ = wantErrCode(t, create(a, "dana", domain.RoleCollaborator), protocol.CodeInvalidParams, "A invites into no workspace")
	created := e.invite(t, a, protocol.MethodMemberInvitationCreate,
		protocol.MemberInvitationCreateParams{Login: "dana", Role: string(domain.RoleViewer), WorkspaceIDs: []string{string(e.wsA.ID)}})
	if !slices.Equal(created.Invitation.WorkspaceIDs, []string{string(e.wsA.ID)}) {
		t.Fatalf("invitation = %+v, want it to carry wsA", created.Invitation)
	}
	if err := create(s, "erin", domain.RoleSuperAdmin); err != nil {
		t.Fatalf("super admin invites a super admin: %v", err)
	}
	list := func(c *protocol.Client) []string {
		t.Helper()
		var out protocol.MemberInvitationListResult
		if err := c.Call(protocol.MethodMemberInvitationList, struct{}{}, &out); err != nil {
			t.Fatal(err)
		}
		var logins []string
		for _, inv := range out.Invitations {
			logins = append(logins, inv.Login)
		}
		slices.Sort(logins)
		return logins
	}
	if got := list(a); !slices.Equal(got, []string{"dana"}) {
		t.Fatalf("A's invitations = %v, want only their own", got)
	}
	if got := list(s); !slices.Equal(got, []string{"dana", "erin"}) {
		t.Fatalf("S's invitations = %v, want all", got)
	}
}

// An admin approves the device of a member below admin, not one that
// would admit as a super admin.
func TestAdminApprovesDevicesOnlyBelowAdmin(t *testing.T) {
	t.Parallel()
	e := newAccessEnv(t, nil)
	a := controlAs(t, e.testEnv, e.aKey)
	inviteAccount(t, e.testEnv, domain.Invitation{Provider: "github", Login: mallory().Login, Role: domain.RoleSuperAdmin})
	_ = wantErrCode(t, approveByCode(a, e.refusedForApproval(t, mallory(), newSigner(t), "mallory-laptop"), nil),
		protocol.CodeDenied, "A approves a device admitting as a super admin")
	inviteAccount(t, e.testEnv, domain.Invitation{Provider: "github", Login: octo.Login, Role: domain.RoleCollaborator,
		Workspaces: []domain.WorkspaceID{e.wsA.ID}})
	if err := approveByCode(a, e.refusedForApproval(t, octo, newSigner(t), "octo-laptop"), nil); err != nil {
		t.Fatalf("A approves a device admitting as a collaborator: %v", err)
	}
	m, err := identities(t, e.testEnv).GetMemberByIdentity(context.Background(), octo.Provider, octo.Subject)
	if err != nil {
		t.Fatal(err)
	}
	if access, err := e.db.MemberWorkspaces(context.Background(), m.ID); err != nil || !access.Granted[e.wsA.ID] {
		t.Fatalf("octo's workspaces after accepting the invitation = %+v, %v; want wsA", access, err)
	}
}
