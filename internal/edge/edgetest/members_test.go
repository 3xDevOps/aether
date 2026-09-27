package edgetest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/edgeclient"
	"github.com/3xDevOps/Aether/internal/edgeproto"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// waitRole waits until c's server list shows serverID with role, or
// without serverID when role is empty: the edge has the directory the
// server pushed after a change.
func waitRole(t *testing.T, c *client, serverID, role string) {
	t.Helper()
	eventually(t, fmt.Sprintf("%s's role on %s to be %q", c.user.Login, serverID, role), func() error {
		ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
		defer cancel()
		servers, _, err := c.edge.Servers(ctx)
		if err != nil {
			return err
		}
		got := ""
		for _, s := range servers {
			if s.ID == serverID {
				got = s.Role
			}
		}
		if got != role {
			return fmt.Errorf("role %q", got)
		}
		return nil
	})
}

func invite(t *testing.T, ctl *protocol.Client, p protocol.MemberInvitationCreateParams) string {
	t.Helper()
	return call[protocol.MemberInvitationResult](t, ctl, protocol.MethodMemberInvitationCreate, p).Invitation.ID
}

func memberNamed(t *testing.T, ctl *protocol.Client, name string) protocol.Member {
	t.Helper()
	for _, m := range call[protocol.MemberListResult](t, ctl, protocol.MethodMemberList, struct{}{}).Members {
		if m.DisplayName == name {
			return m
		}
	}
	t.Fatalf("no member named %s", name)
	return protocol.Member{}
}

func TestInvitations(t *testing.T) {
	h := newHarness(t)
	a := h.newServer()
	cs := h.login(alice, bob, carol, dave, erin)
	al, bo, ca, da, er := cs[0], cs[1], cs[2], cs[3], cs[4]
	h.claimServer(al, a)
	ctl := h.control(al, a)

	// By GitHub login: bob sees the server, and his first connection
	// makes him a member with the invited role and uses the invitation up.
	invite(t, ctl, protocol.MemberInvitationCreateParams{Provider: edgeproto.ProviderGitHub, Login: bo.user.Login, Role: "collaborator"})
	waitRole(t, bo, a.id, "collaborator")
	devices := call[protocol.MemberDeviceListResult](t, h.control(bo, a), protocol.MethodMemberDeviceList, struct{}{})
	if len(devices.Devices) != 1 || devices.Devices[0].Status != "approved" {
		t.Fatalf("bob's devices = %+v", devices.Devices)
	}
	if m := memberNamed(t, ctl, bo.user.Login); m.Role != "collaborator" {
		t.Fatalf("bob joined as %q", m.Role)
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

	// Expired: the server leaves it out of the directory, so the edge
	// refuses erin.
	admin := memberNamed(t, ctl, al.user.Login)
	if err = a.db.CreateInvitation(context.Background(), &domain.Invitation{
		Provider: edgeproto.ProviderGitHub, Login: er.user.Login, Role: domain.Role("collaborator"),
		CreatedBy: domain.MemberID(admin.ID), ExpiresAt: time.Now().Add(-time.Minute),
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
	if err == nil || !strings.Contains(err.Error(), "is not a member of this server") || strings.Contains(err.Error(), h.frontAddr) {
		t.Fatalf("expired invitation at the server: %v, want the server's own refusal", err)
	}
	call[struct{}](t, ctl, protocol.MethodMemberInvitationRevoke, protocol.MemberInvitationRevokeParams{InvitationID: id})
}

// A server that already has members is claimed by an admin who linked
// their account. The directory the server pushes as it accepts the claim
// reaches the edge before the edge records the claim, and must not be
// lost: bob's open invitation is in it.
func TestClaimKeepsTheDirectoryPushedWithIt(t *testing.T) {
	h := newHarness(t)
	a := h.newServer()
	cs := h.login(alice, bob)
	al, bo := cs[0], cs[1]
	ctx := context.Background()
	admin := &domain.Member{DisplayName: "Admin", Color: "#3cb44b", Role: domain.RoleAdmin,
		PublicKey: string(ssh.MarshalAuthorizedKey(newSigner(t).PublicKey()))}
	if err := a.db.CreateMember(ctx, admin); err != nil {
		t.Fatal(err)
	}
	expires := time.Now().Add(time.Hour)
	for _, inv := range []*domain.Invitation{
		{Provider: edgeproto.ProviderGitHub, Login: al.user.Login, Member: admin.ID, CreatedBy: admin.ID, ExpiresAt: expires},
		{Provider: edgeproto.ProviderGitHub, Login: bo.user.Login, Role: domain.RoleCollaborator, CreatedBy: admin.ID, ExpiresAt: expires},
	} {
		if err := a.db.CreateInvitation(ctx, inv); err != nil {
			t.Fatal(err)
		}
	}
	h.claimServer(al, a)
	waitRole(t, bo, a.id, "collaborator")
	info := call[protocol.ServerInfoResult](t, h.control(bo, a), protocol.MethodServerInfo, struct{}{})
	if info.Member.Role != string(domain.RoleCollaborator) {
		t.Fatalf("bob joined as %+v", info.Member)
	}
}

// closedWithin reports whether sc closes within waitTimeout.
func closedWithin(t *testing.T, what string, sc *ssh.Client) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		_ = sc.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(waitTimeout):
		t.Fatalf("%s: connection still open after %s", what, waitTimeout)
	}
}

func TestRevocationClosesLiveConnections(t *testing.T) {
	h := newHarness(t)
	a := h.newServer()
	cs := h.login(alice, bob, carol, dave)
	al, bo, ca, da := cs[0], cs[1], cs[2], cs[3]
	h.claimServer(al, a)
	ctl := h.control(al, a)
	live := map[*client]*ssh.Client{}
	for _, c := range []*client{bo, ca, da} {
		invite(t, ctl, protocol.MemberInvitationCreateParams{Provider: edgeproto.ProviderGitHub, Login: c.user.Login, Role: "collaborator"})
		waitRole(t, c, a.id, "collaborator")
		live[c] = h.mustDial(c, a)
	}

	// A member removed.
	bob := memberNamed(t, ctl, bo.user.Login)
	call[struct{}](t, ctl, protocol.MethodMemberRemove, protocol.MemberRemoveParams{MemberID: bob.ID})
	closedWithin(t, "member.remove", live[bo])
	waitRole(t, bo, a.id, "")
	_, err := h.dial(bo, h.link(a))
	wantRefusal(t, "removed member", err, edgeproto.RefusalNotMember)

	// A device revoked on the server.
	carol := memberNamed(t, ctl, ca.user.Login)
	var device string
	for _, d := range call[protocol.MemberDeviceListResult](t, ctl, protocol.MethodMemberDeviceList, struct{}{}).Devices {
		if d.MemberID == carol.ID {
			device = d.ID
		}
	}
	call[protocol.MemberDeviceResult](t, ctl, protocol.MethodMemberDeviceRevoke, protocol.MemberDeviceRevokeParams{DeviceID: device})
	closedWithin(t, "member.device.revoke", live[ca])
	if _, err = h.dial(ca, h.link(a)); err == nil || !strings.Contains(err.Error(), "was revoked on this server") {
		t.Fatalf("revoked device: %v", err)
	}

	// A device token revoked at the edge, by aether logout.
	tokens := filepath.Join(da.dir, edgeclient.TokensFile)
	saved, err := os.ReadFile(tokens)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()
	if err = da.edge.Logout(ctx); err != nil {
		t.Fatal(err)
	}
	closedWithin(t, "aether logout", live[da])
	if err = os.WriteFile(tokens, saved, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = h.dial(da, h.link(a))
	wantRefusal(t, "revoked device token", err, edgeproto.RefusalTokenRevoked)
}
