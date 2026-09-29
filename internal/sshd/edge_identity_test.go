package sshd

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

// Under approved-devices every device that signs in as an invited account
// waits on the invitation, the real invitee's and one an edge forged for
// the same login alike. Nothing is created until a person approves one:
// that approval creates the member with the invited role, binds that
// device's account and uses the invitation up, and the other devices
// waiting on it are gone.
func TestInvitationWaitsUntilOneDeviceIsApproved(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	ctx := context.Background()
	inv := inviteAccount(t, e, domain.Invitation{Provider: "github", Login: "octo", Role: domain.RoleAdmin})
	forged := edgeproto.Account{Provider: edgeproto.ProviderGitHub, Subject: "6666", Login: "octo", IdentityAt: time.Now()}
	laptop, attacker := newSigner(t), newSigner(t)
	code := e.refusedForApproval(t, octo, laptop, "laptop")
	forgedCode := e.refusedForApproval(t, forged, attacker, "attacker")

	members, err := e.store.ListMembers(ctx)
	if err != nil || len(members) != 1 {
		t.Fatalf("members while devices wait = %d, %v; want only the admin", len(members), err)
	}
	for _, a := range []edgeproto.Account{octo, forged} {
		if _, gerr := identities(t, e).GetMemberByIdentity(ctx, a.Provider, a.Subject); !errors.Is(gerr, store.ErrNotFound) {
			t.Fatalf("account %s bound while waiting: %v", a.Subject, gerr)
		}
	}
	if open, gerr := identities(t, e).GetInvitation(ctx, inv.ID); gerr != nil || open.ConsumedAt != nil {
		t.Fatalf("invitation while devices wait = %+v, %v; want it open", open, gerr)
	}

	approveAsAdmin(t, e, code)
	m, err := identities(t, e).GetMemberByIdentity(ctx, octo.Provider, octo.Subject)
	if err != nil || m.Role != domain.RoleAdmin {
		t.Fatalf("member after approval = %+v, %v; want the invited admin", m, err)
	}
	if deviceStatus(t, e, laptop) != domain.DeviceApproved {
		t.Fatal("the approved device is not approved")
	}
	err = approveByCode(controlClient(t, e), forgedCode, nil)
	var pe *protocol.Error
	if !errors.As(err, &pe) || pe.Code != protocol.CodeNotFound {
		t.Fatalf("approving the other waiting device after the invitation was used = %v, want not found", err)
	}
	if _, err := identities(t, e).GetDeviceByCredential(ctx, edgeproto.DeviceKeyLine(attacker.PublicKey())); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the other waiting device outlived the invitation: %v", err)
	}
	e.mustRefuseEdge(t, forged, attacker, "attacker", "is not a member of this server")
	if got := serverInfoMember(t, controlClientOn(t, e.mustDialEdge(t, octo, laptop, "laptop"))); got.ID != string(m.ID) {
		t.Fatalf("the approved device connects as %s, want %s", got.ID, m.ID)
	}
}

// Revoking an invitation removes the devices waiting on it, and only an
// admin approves a device waiting on an invitation that creates a member.
func TestRevokedInvitationDropsItsWaitingDevices(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	ctx := context.Background()
	inv := inviteAccount(t, e, domain.Invitation{Provider: "github", Login: "octo", Role: domain.RoleCollaborator})
	laptop := newSigner(t)
	code := e.refusedForApproval(t, octo, laptop, "laptop")
	bob, _ := addMember(t, e, "Bob", domain.RoleCollaborator, false)
	var pe *protocol.Error
	err := approveByCode(controlAs(t, e, bob), code, nil)
	if !errors.As(err, &pe) || pe.Code != protocol.CodeDenied {
		t.Fatalf("a member who is not an admin approving an invitation device = %v, want denied", err)
	}
	if err := controlClient(t, e).Call(protocol.MethodMemberInvitationRevoke,
		protocol.MemberInvitationRevokeParams{InvitationID: string(inv.ID)}, nil); err != nil {
		t.Fatalf("revoke invitation: %v", err)
	}
	if _, err := identities(t, e).GetDeviceByCredential(ctx, edgeproto.DeviceKeyLine(laptop.PublicKey())); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a device waiting on a revoked invitation: %v, want removed", err)
	}
	e.mustRefuseEdge(t, octo, laptop, "laptop", "is not a member of this server")
}

// member.identity.list shows a member's accounts and the account each of
// the member's devices signed in as; member.identity.remove unbinds one
// account, revokes its devices and closes their connections, and keeps
// the member. Both are the member's or an admin's, and under
// approved-devices removal needs a credential a person approved.
func TestMemberIdentityListAndRemove(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	ctx := context.Background()
	inviteAccount(t, e, domain.Invitation{Provider: "github", Login: "octo", Role: domain.RoleCollaborator})
	laptop := newSigner(t)
	approveAsAdmin(t, e, e.refusedForApproval(t, octo, laptop, "laptop"))
	live := e.mustDialEdge(t, octo, laptop, "laptop")
	self := controlClientOn(t, live)
	m, err := identities(t, e).GetMemberByIdentity(ctx, octo.Provider, octo.Subject)
	if err != nil {
		t.Fatal(err)
	}

	var list protocol.MemberIdentityListResult
	if err := self.Call(protocol.MethodMemberIdentityList, protocol.MemberIdentityListParams{MemberID: string(m.ID)}, &list); err != nil {
		t.Fatalf("list own identities: %v", err)
	}
	if len(list.Identities) != 1 || list.Identities[0].Subject != octo.Subject || list.Identities[0].Login != "octo" ||
		len(list.Devices) != 1 || list.Devices[0].Account != "octo" || list.Devices[0].Provider != "github" {
		t.Fatalf("identity list = %+v", list)
	}
	bob, _ := addMember(t, e, "Bob", domain.RoleCollaborator, false)
	other := controlAs(t, e, bob)
	var pe *protocol.Error
	for method, params := range map[string]any{
		protocol.MethodMemberIdentityList:   protocol.MemberIdentityListParams{MemberID: string(m.ID)},
		protocol.MethodMemberIdentityRemove: protocol.MemberIdentityRemoveParams{MemberID: string(m.ID), Provider: octo.Provider, Subject: octo.Subject},
	} {
		if err := other.Call(method, params, nil); !errors.As(err, &pe) || pe.Code != protocol.CodeDenied {
			t.Fatalf("%s by another member = %v, want denied", method, err)
		}
	}

	// A device awaiting approval, pending or registered, removes nothing.
	for _, status := range []domain.DeviceStatus{domain.DevicePending, domain.DeviceRegistered} {
		dev := &domain.Device{Member: m.ID, Provider: octo.Provider, Subject: octo.Subject,
			Credential: edgeproto.DeviceKeyLine(newSigner(t).PublicKey()), Label: string(status), Status: status}
		if err := identities(t, e).RegisterDevice(ctx, dev); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(protocol.MemberIdentityRemoveParams{MemberID: string(m.ID), Provider: octo.Provider, Subject: octo.Subject})
		_, perr := e.srv.dispatch(withDevice(m.ID, dev), m.ID, protocol.MethodMemberIdentityRemove, raw)
		if perr == nil || perr.Code != protocol.CodeDenied || !strings.Contains(perr.Message, "needs an approved device") {
			t.Fatalf("remove from a %s device = %v, want denied", status, perr)
		}
	}

	var removed protocol.MemberIdentityRemoveResult
	if err := controlClient(t, e).Call(protocol.MethodMemberIdentityRemove,
		protocol.MemberIdentityRemoveParams{MemberID: string(m.ID), Provider: octo.Provider, Subject: octo.Subject}, &removed); err != nil {
		t.Fatalf("admin removes the identity: %v", err)
	}
	if len(removed.Revoked) != 3 {
		t.Fatalf("revoked %+v, want the identity's three devices", removed.Revoked)
	}
	waitClosed(t, live)
	if _, err := identities(t, e).GetMemberByIdentity(ctx, octo.Provider, octo.Subject); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("identity after removal: %v", err)
	}
	if after, err := e.store.GetMember(ctx, m.ID); err != nil || after.Role != domain.RoleCollaborator {
		t.Fatalf("member after removal = %+v, %v; want kept", after, err)
	}
	if got := deviceStatus(t, e, laptop); got != domain.DeviceRevoked {
		t.Fatalf("laptop after removal is %s, want revoked", got)
	}
	e.mustRefuseEdge(t, octo, laptop, "laptop", "not a member of this server")
	if err := controlClient(t, e).Call(protocol.MethodMemberIdentityList,
		protocol.MemberIdentityListParams{MemberID: string(m.ID)}, &list); err != nil || len(list.Identities) != 0 || len(list.Devices) != 3 {
		t.Fatalf("list after removal = %+v, %v; want no identity and the revoked devices", list, err)
	}
}

// A claim code the machine's administrator issued for an existing admin
// binds the claiming account to that admin and approves the claiming
// device. It never creates a member or raises a role: a code naming a
// member who is not an admin, or an account of another member, is
// refused.
func TestClaimCodeForAnAdminRecoversThatAdmin(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	ctx := context.Background()
	_, bob := addMember(t, e, "Bob", domain.RoleCollaborator, false)
	laptop := newSigner(t)
	claim := func(admin domain.MemberID, account edgeproto.Account) (string, error) {
		codes := newFakeClaimCode()
		codes.admin = string(admin)
		c, banner, err := e.dialClaim(t, codes, claimGrantFor(account, laptop), laptop, claimUser(t, testClaimCode, account))
		if err == nil {
			t.Cleanup(func() { _ = c.Close() })
		}
		return banner, err
	}

	if banner, err := claim(bob.ID, octo); err == nil || !strings.Contains(banner, "is collaborator, not an admin") {
		t.Fatalf("recovery for a collaborator = %v, banner %q", err, banner)
	}
	if _, err := identities(t, e).GetMemberByIdentity(ctx, octo.Provider, octo.Subject); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a refused recovery bound the account: %v", err)
	}
	if banner, err := claim(e.member.ID, octo); err != nil {
		t.Fatalf("recovery for the admin: %v (banner %q)", err, banner)
	}
	m, err := identities(t, e).GetMemberByIdentity(ctx, octo.Provider, octo.Subject)
	if err != nil || m.ID != e.member.ID || m.Role != domain.RoleAdmin {
		t.Fatalf("recovered account is %+v, %v; want the admin %s", m, err, e.member.ID)
	}
	if deviceStatus(t, e, laptop) != domain.DeviceApproved {
		t.Fatal("the claiming device is not approved")
	}
	if members, _ := e.store.ListMembers(ctx); len(members) != 2 {
		t.Fatalf("members after recovery = %d, want the same two", len(members))
	}
	if after, _ := e.store.GetMember(ctx, bob.ID); after.Role != domain.RoleCollaborator {
		t.Fatalf("bob's role changed to %s", after.Role)
	}

	// An account that is another member's is not moved to the admin.
	inviteAccount(t, e, domain.Invitation{Provider: "github", Login: "mallory", Role: domain.RoleViewer})
	approveAsAdmin(t, e, e.refusedForApproval(t, mallory(), newSigner(t), "phone"))
	if banner, err := claim(e.member.ID, mallory()); err == nil || !strings.Contains(banner, "belongs to member") {
		t.Fatalf("recovery with another member's account = %v, banner %q", err, banner)
	}
}

// TestGoogleIdentityFromAnEarlierVersion meets what v0.5.2-alpha.3 could
// store through an edge that signed people in with Google: an identity
// with its device, and an email invitation. The directory leaves them out
// while the listings show them, ownership cannot go to them, and the
// removal methods remove them.
func TestGoogleIdentityFromAnEarlierVersion(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, withPolicy(edgeproto.PolicyAccount))
	ctx := context.Background()
	ids := identities(t, e)
	admin := controlClient(t, e)
	e.srv.SetEdgeOwner(&fakeEdgeOwner{})
	_, bob := addMember(t, e, "Bob", domain.RoleAdmin, false)
	if err := ids.BindIdentity(ctx, &domain.Identity{Member: bob.ID, Provider: "google", Subject: "g-1", Email: "bob@example.com"}); err != nil {
		t.Fatal(err)
	}
	dev := &domain.Device{Member: bob.ID, Provider: "google", Subject: "g-1", Email: "bob@example.com",
		Credential: edgeproto.DeviceKeyLine(newSigner(t).PublicKey()), Label: "phone", Status: domain.DeviceApproved}
	if err := ids.RegisterDevice(ctx, dev); err != nil {
		t.Fatal(err)
	}
	inv := inviteAccount(t, e, domain.Invitation{Provider: "google", Email: "dana@example.com", Role: domain.RoleViewer})

	entries, err := e.srv.EdgeDirectory(ctx)
	if err != nil {
		t.Fatalf("directory with Google rows: %v", err)
	}
	for _, en := range entries {
		if en.Provider == "google" {
			t.Fatalf("directory carries %+v", en)
		}
	}
	var listed protocol.MemberIdentityListResult
	if err = admin.Call(protocol.MethodMemberIdentityList, protocol.MemberIdentityListParams{MemberID: string(bob.ID)}, &listed); err != nil ||
		len(listed.Identities) != 1 || listed.Identities[0].Provider != "google" || listed.Identities[0].Subject != "g-1" ||
		len(listed.Devices) != 1 || listed.Devices[0].Provider != "google" {
		t.Fatalf("identities of %s: %+v, %v", bob.ID, listed, err)
	}
	var invs protocol.MemberInvitationListResult
	if err = admin.Call(protocol.MethodMemberInvitationList, nil, &invs); err != nil ||
		len(invs.Invitations) != 1 || invs.Invitations[0].Provider != "google" {
		t.Fatalf("invitations: %+v, %v", invs, err)
	}

	var pe *protocol.Error
	err = admin.Call(protocol.MethodServerOwnerTransfer, protocol.ServerOwnerTransferParams{MemberID: string(bob.ID)}, nil)
	if !errors.As(err, &pe) || pe.Code != protocol.CodeInvalidState || !strings.Contains(pe.Message, "has no GitHub identity, only google:g-1") {
		t.Fatalf("transfer to a Google identity = %v, want refused with the reason", err)
	}

	var removed protocol.MemberIdentityRemoveResult
	if err = admin.Call(protocol.MethodMemberIdentityRemove,
		protocol.MemberIdentityRemoveParams{MemberID: string(bob.ID), Provider: "google", Subject: "g-1"}, &removed); err != nil ||
		len(removed.Revoked) != 1 || removed.Revoked[0].ID != string(dev.ID) {
		t.Fatalf("unlink the Google identity: %+v, %v", removed, err)
	}
	if err = admin.Call(protocol.MethodMemberInvitationRevoke, protocol.MemberInvitationRevokeParams{InvitationID: string(inv.ID)}, nil); err != nil {
		t.Fatalf("revoke the Google invitation: %v", err)
	}
	if all, _ := ids.ListIdentities(ctx); len(all) != 0 {
		t.Fatalf("identities left: %+v", all)
	}
	if left, _ := ids.ListInvitations(ctx); len(left) != 0 {
		t.Fatalf("invitations left: %+v", left)
	}
}
