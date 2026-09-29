package edgetest

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	edgeclient "github.com/3xDevOps/Aether/internal/edge/client"
	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestInvitations(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	a := h.newServer(edgeproto.PolicyAccount)
	cs := h.login(alice, bob, carol, dave, erin)
	al, bo, ca, da, er := cs[0], cs[1], cs[2], cs[3], cs[4]
	h.claimServer(al, a)
	ctl := h.control(al, a)

	// By GitHub login: bob sees the server, and his first connection
	// makes him a member with the invited role and uses the invitation up.
	inviteLogin(t, ctl, bo, a.id, "admin")
	if info := call[protocol.ServerInfoResult](t, h.control(bo, a), protocol.MethodServerInfo, struct{}{}); info.Member.Role != "admin" {
		t.Fatalf("bob joined as %+v", info.Member)
	}
	if invs := call[protocol.MemberInvitationListResult](t, ctl, protocol.MethodMemberInvitationList, struct{}{}); len(invs.Invitations) != 0 {
		t.Fatalf("invitations after bob joined = %+v", invs.Invitations)
	}

	// By verified email, from either provider.
	invite(t, ctl, protocol.MemberInvitationCreateParams{Email: ca.user.Email, Role: "viewer"})
	waitRole(t, ca, a.id, "viewer")
	h.mustDial(ca, a)

	// Revoked before use.
	id := invite(t, ctl, protocol.MemberInvitationCreateParams{Provider: edgeproto.ProviderGitHub, Login: da.user.Login, Role: "collaborator"})
	waitRole(t, da, a.id, "collaborator")
	call[struct{}](t, ctl, protocol.MethodMemberInvitationRevoke, protocol.MemberInvitationRevokeParams{InvitationID: id})
	waitRole(t, da, a.id, "")
	_, err := h.dial(da, h.link(a))
	wantRefusal(t, "revoked invitation", err, edgeproto.RefusalNotMember)

	// Its creator demoted: an admin's open invitations go with the role.
	bctl := h.control(bo, a)
	invite(t, bctl, protocol.MemberInvitationCreateParams{Provider: edgeproto.ProviderGitHub, Login: er.user.Login, Role: "admin"})
	waitRole(t, er, a.id, "admin")
	call[protocol.MemberRoleResult](t, ctl, protocol.MethodMemberRole, protocol.MemberRoleParams{MemberID: string(a.memberOf(t, bob).ID), Role: "collaborator"})
	waitRole(t, er, a.id, "")
	_, err = h.dial(er, h.link(a))
	wantRefusal(t, "invitation of a demoted admin", err, edgeproto.RefusalNotMember)

	// Expired: the server leaves it out of the directory, so the edge
	// refuses erin.
	admin := a.memberOf(t, alice)
	if err = a.db.CreateInvitation(context.Background(), &domain.Invitation{
		Provider: edgeproto.ProviderGitHub, Login: er.user.Login, Role: domain.RoleCollaborator,
		CreatedBy: admin.ID, ExpiresAt: time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	from := h.proxy.mark()
	id = invite(t, ctl, protocol.MemberInvitationCreateParams{Provider: edgeproto.ProviderGitHub, Login: "someone-else", Role: "viewer"})
	pushed := h.proxy.await(t, "a directory push", from, func(e logEntry) bool {
		_, ok := e.msg.(edgeproto.Directory)
		return ok && !e.fromEdge && e.serverID == a.id
	}).msg.(edgeproto.Directory)
	for _, e := range pushed.Entries {
		if e.Login == er.user.Login {
			t.Fatalf("expired invitation pushed to the edge: %+v", e)
		}
	}
	_, err = h.dial(er, h.link(a))
	wantRefusal(t, "expired invitation at the edge", err, edgeproto.RefusalNotMember)

	// An edge that still lists it, stale or compromised, cannot let erin
	// in: the server checks the invitation itself.
	stale := edgeproto.Directory{Entries: append(pushed.Entries, edgeproto.DirectoryEntry{
		Kind: edgeproto.EntryInvitation, Provider: edgeproto.ProviderGitHub, Login: er.user.Login,
		Role: "collaborator", ExpiresAt: time.Now().Add(time.Hour),
	})}
	h.proxy.inject(t, a.id, stale, false)
	waitRole(t, er, a.id, "collaborator")
	_, err = h.dial(er, h.link(a))
	if err == nil || !strings.Contains(err.Error(), "is not a member of this server") || strings.Contains(err.Error(), "refused: ") {
		t.Fatalf("expired invitation at the server: %v, want the server's own refusal", err)
	}
	call[struct{}](t, ctl, protocol.MethodMemberInvitationRevoke, protocol.MemberInvitationRevokeParams{InvitationID: id})
}

// A role change reaches the edge's list and the member's live connection.
func TestRoleChange(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	a := h.newServer(edgeproto.PolicyApprovedDevices)
	cs := h.login(alice, bob)
	al, bo := cs[0], cs[1]
	h.claimServer(al, a)
	ctl := h.control(al, a)
	h.join(ctl, bo, a, "viewer")
	bctl := h.control(bo, a)
	deniedCall(t, bctl, protocol.MethodMemberInvitationCreate,
		protocol.MemberInvitationCreateParams{Provider: edgeproto.ProviderGitHub, Login: "someone", Role: "viewer"}, "admin")

	call[protocol.MemberRoleResult](t, ctl, protocol.MethodMemberRole, protocol.MemberRoleParams{MemberID: string(a.memberOf(t, bob).ID), Role: "admin"})
	waitRole(t, bo, a.id, "admin")
	if info := call[protocol.ServerInfoResult](t, bctl, protocol.MethodServerInfo, struct{}{}); info.Member.Role != "admin" {
		t.Fatalf("bob's live connection sees %+v", info.Member)
	}
	invite(t, bctl, protocol.MemberInvitationCreateParams{Provider: edgeproto.ProviderGitHub, Login: "someone", Role: "viewer"})
}

// A member of one server reaches no other: the edge refuses, and a grant
// an attacker at the edge signs for another server's member, or for
// another server, is refused by the server itself.
func TestCrossServerIsolation(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	a, b := h.newServer(edgeproto.PolicyAccount), h.newServer(edgeproto.PolicyAccount)
	cs := h.login(alice, bob)
	al, bo := cs[0], cs[1]
	h.claimServer(al, a)
	h.claimServer(bo, b)

	_, err := h.dial(al, h.link(b))
	wantRefusal(t, "owner of A connecting to B", err, edgeproto.RefusalNotMember)
	_, err = h.dial(bo, h.link(a))
	wantRefusal(t, "owner of B connecting to A", err, edgeproto.RefusalNotMember)

	nc, err := h.forge(t, b, forgery{kind: edgeproto.KindSSH, account: alice.account(), key: al.signer(t).PublicKey()})
	if err != nil {
		t.Fatal(err)
	}
	if _, banner, herr := sshOver(nc, b, edgeproto.AccountUser(alice.account()), al.signer(t)); herr == nil || !strings.Contains(banner, "github account alice is not a member of this server") {
		t.Fatalf("forged grant for A's owner on B: %v, banner %q", herr, banner)
	}

	connID := edgeproto.NewConnID()
	now := time.Now()
	grant, err := edgeproto.SignGrant(h.edgeKey(), edgeproto.Grant{
		Issuer: h.relayURL, ServerID: a.id, ConnID: connID, Kind: edgeproto.KindSSH, Account: alice.account(),
		DeviceID: "forged", DeviceKey: edgeproto.DeviceKeyLine(al.signer(t).PublicKey()), IssuedAt: now, ExpiresAt: now.Add(edgeproto.GrantTTL),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.sendOpen(t, b, edgeproto.Open{ConnID: connID, Ticket: edgeproto.NewToken(), Kind: edgeproto.KindSSH, Grant: grant}); err == nil ||
		!strings.Contains(err.Error(), edgeproto.ErrGrantServer.Error()) {
		t.Fatalf("A's grant opened on B: %v", err)
	}
}

// Removing a member, revoking a device on the server or on its console,
// revoking a device token at the edge, and deleting an account each close
// a live connection; deleting the account closes direct ones too, since
// each server then removes the account's devices. The test logs how long
// each took.
func TestRevocationClosesLiveConnections(t *testing.T) {
	t.Parallel()
	for _, policy := range policies {
		t.Run(string(policy), func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			a := h.newServer(policy)
			cs := h.login(alice, bob, carol, dave, erin, mallory)
			al, bo, ca, da, er, ma := cs[0], cs[1], cs[2], cs[3], cs[4], cs[5]
			h.claimServer(al, a)
			ctl := h.control(al, a)
			live := map[*client]*ssh.Client{}
			for _, c := range []*client{bo, ca, da, er, ma} {
				h.join(ctl, c, a, "collaborator")
				live[c] = h.mustDial(c, a)
			}

			start := time.Now()
			call[struct{}](t, ctl, protocol.MethodMemberRemove, protocol.MemberRemoveParams{MemberID: string(a.memberOf(t, bob).ID)})
			closedWithin(t, "member.remove", live[bo], start, 2*time.Second)
			waitRole(t, bo, a.id, "")
			_, err := h.dial(bo, h.link(a))
			wantRefusal(t, "removed member", err, edgeproto.RefusalNotMember)

			start = time.Now()
			call[protocol.MemberDeviceResult](t, ctl, protocol.MethodMemberDeviceRevoke, protocol.MemberDeviceRevokeParams{DeviceID: string(a.deviceIDOf(t, ca))})
			closedWithin(t, "member.device.revoke", live[ca], start, 2*time.Second)
			if _, err = h.dial(ca, h.link(a)); err == nil || !strings.Contains(err.Error(), "was revoked on this server") {
				t.Fatalf("revoked device: %v", err)
			}

			// `aether-server device review` revokes from another process,
			// which the running server notices at its next revalidation.
			start = time.Now()
			if err = a.console(t).RevokeDevice(context.Background(), a.deviceIDOf(t, ma)); err != nil {
				t.Fatal(err)
			}
			closedWithin(t, "device revoked on the console", live[ma], start, 5*time.Second)

			// A device token revoked at the edge, by aether logout, closes
			// the device's relayed connections. Its device key stays
			// approved on the server, so a direct connection stays open
			// and a new one is admitted; the direct path takes approved
			// devices only.
			for _, c := range []*client{da, er} {
				h.approveForDirect(ctl, c, a)
			}
			direct, err := h.dial(da, directLink(a))
			if err != nil {
				t.Fatalf("direct connection: %v", err)
			}
			tokens := filepath.Join(da.dir, edgeclient.TokensFile)
			saved, err := os.ReadFile(tokens)
			if err != nil {
				t.Fatal(err)
			}
			start = time.Now()
			if err = da.edge.Logout(context.Background()); err != nil {
				t.Fatal(err)
			}
			closedWithin(t, "aether logout", live[da], start, 2*time.Second)
			if info := call[protocol.ServerInfoResult](t, controlOver(t, direct), protocol.MethodServerInfo, struct{}{}); info.Member.ID == "" {
				t.Fatal("the direct connection lost its member")
			}
			if err = os.WriteFile(tokens, saved, 0o600); err != nil {
				t.Fatal(err)
			}
			_, err = h.dial(da, h.link(a))
			wantRefusal(t, "revoked device token", err, edgeproto.RefusalTokenRevoked)
			sc, err := h.dial(da, directLink(a))
			if err != nil {
				t.Fatalf("direct connection after logout: %v", err)
			}
			_ = sc.Close()

			// An account deleted at the edge.
			direct, err = h.dial(er, directLink(a))
			if err != nil {
				t.Fatalf("direct connection: %v", err)
			}
			start = time.Now()
			h.deleteAccount(er)
			closedWithin(t, "account deletion (relayed)", live[er], start, 3*time.Second)
			closedWithin(t, "account deletion (direct)", direct, start, 3*time.Second)
			if _, err = h.dial(er, directLink(a)); err == nil {
				t.Fatal("a deleted account's device key still connects directly")
			}
		})
	}
}

// Revocations made on the server while the edge is down take effect
// without it, and hold once it is back.
func TestRevocationWhileTheEdgeIsDown(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	a := h.newServer(edgeproto.PolicyApprovedDevices)
	cs := h.login(alice, bob, carol)
	al, bo, ca := cs[0], cs[1], cs[2]
	h.claimServer(al, a)
	ctl := h.control(al, a)
	h.join(ctl, bo, a, "collaborator")
	h.join(ctl, ca, a, "collaborator")
	bobID, carolID := a.deviceIDOf(t, bo), a.memberOf(t, carol).ID

	h.stopEdge()
	bobDirect, err := h.dial(bo, directLink(a))
	if err != nil {
		t.Fatalf("bob's direct connection: %v", err)
	}
	admin, err := h.dial(al, directLink(a))
	if err != nil {
		t.Fatalf("alice's direct connection with the edge down: %v", err)
	}
	actl := controlOver(t, admin)
	start := time.Now()
	call[protocol.MemberDeviceResult](t, actl, protocol.MethodMemberDeviceRevoke, protocol.MemberDeviceRevokeParams{DeviceID: string(bobID)})
	closedWithin(t, "member.device.revoke with the edge down", bobDirect, start, 2*time.Second)
	call[struct{}](t, actl, protocol.MethodMemberRemove, protocol.MemberRemoveParams{MemberID: string(carolID)})

	from := h.proxy.mark()
	h.startEdge()
	h.reenrolled(t, a, from)
	if _, err = h.dial(bo, h.link(a)); err == nil || !strings.Contains(err.Error(), "was revoked on this server") {
		t.Fatalf("bob's revoked device after the edge came back: %v", err)
	}
	waitRole(t, ca, a.id, "")
	_, err = h.dial(ca, h.link(a))
	wantRefusal(t, "carol, removed while the edge was down", err, edgeproto.RefusalNotMember)
	if _, err = h.dial(bo, directLink(a)); err == nil {
		t.Fatal("bob's revoked device connects directly")
	}
	h.mustDial(al, a)
}
