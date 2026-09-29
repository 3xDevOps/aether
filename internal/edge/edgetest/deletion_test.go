package edgetest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// waitEdgeOwner waits until the edge records u as the owner of s, or no
// owner when u is nil.
func (h *harness) waitEdgeOwner(t *testing.T, s *serverNode, u *ghUser) {
	t.Helper()
	eventually(t, "the edge's owner of "+s.id, func() error {
		srv, err := h.edgeStore().Server(context.Background(), s.id)
		switch {
		case err != nil:
			return err
		case u == nil && srv.Owner != nil:
			return fmt.Errorf("owner %s", srv.Owner.Login)
		case u != nil && (srv.Owner == nil || srv.Owner.Subject != fmt.Sprint(u.ID)):
			return fmt.Errorf("owner %+v", srv.Owner)
		}
		return nil
	})
}

// identityGone waits until s holds no identity for u.
func identityGone(t *testing.T, s *serverNode, u ghUser) {
	t.Helper()
	eventually(t, u.Login+"'s identity removed from "+s.id, func() error {
		if _, err := s.db.GetMemberByIdentity(context.Background(), edgeproto.ProviderGitHub, fmt.Sprint(u.ID)); err == nil {
			return errors.New("still bound")
		}
		return nil
	})
}

// The owner transfers ownership to another admin first, then deletes
// their account: the server keeps its owner, and keeps the old owner as
// a member with the same role, without the deleted account's identity.
func TestDeleteAccountAfterTransfer(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	a := h.newServer(edgeproto.PolicyApprovedDevices)
	cs := h.login(alice, bob)
	al, bo := cs[0], cs[1]
	h.claimServer(al, a)
	ctl := h.control(al, a)
	h.join(ctl, bo, a, "admin")
	aliceID, bobID := a.memberOf(t, alice).ID, a.memberOf(t, bob).ID

	res := call[protocol.ServerOwnerTransferResult](t, ctl, protocol.MethodServerOwnerTransfer, protocol.ServerOwnerTransferParams{MemberID: string(bobID)})
	if res.MemberID != string(bobID) || res.Login != bob.Login {
		t.Fatalf("transfer = %+v", res)
	}
	h.waitEdgeOwner(t, a, &bob)
	before := a.snapshot(t)
	live := h.mustDial(al, a)

	start := time.Now()
	h.deleteAccount(al)
	closedWithin(t, "account deletion", live, start, 3*time.Second)
	identityGone(t, a, alice)

	after := a.snapshot(t)
	if len(after.members) != len(before.members) || after.members[aliceID] != domain.RoleAdmin || after.members[bobID] != domain.RoleAdmin {
		t.Fatalf("members %v -> %v; want both admins kept", before.members, after.members)
	}
	h.waitEdgeOwner(t, a, &bob)
	if owner, err := a.state().Owner(); err != nil || owner == nil || owner.Login != bob.Login {
		t.Fatalf("server's owner = %+v, %v; want bob", owner, err)
	}
	h.mustDial(bo, a)
	// Alice's member still works on the tailnet-hosted dashboard.
	var list protocol.MemberListResult
	if err := a.local(t, aliceID, protocol.MethodMemberList, struct{}{}, &list); err != nil || len(list.Members) != 2 {
		t.Fatalf("alice on the tailnet dashboard: %+v %v", list, err)
	}
}

// The owner deletes their account without transferring: the server
// stays enrolled and becomes ownerless, keeps every member, role and
// workspace, and gains no administrator. Its administrator recovers it
// on the machine: an admin links the account again and a new claim code
// claims it.
func TestDeleteAccountLeavesTheServerOwnerless(t *testing.T) {
	t.Parallel()
	for _, policy := range policies {
		t.Run(string(policy), func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			a := h.newServer(policy)
			cs := h.login(carol, dave)
			ca, da := cs[0], cs[1]
			h.claimServer(ca, a)
			ctl := h.control(ca, a)
			h.join(ctl, da, a, "collaborator")
			ctx := context.Background()
			if err := a.db.CreateWorkspace(ctx, &domain.Workspace{Name: "kept", BaseBranch: "main"}); err != nil {
				t.Fatal(err)
			}
			carolID := a.memberOf(t, carol).ID
			before := a.snapshot(t)

			h.deleteAccount(ca)
			identityGone(t, a, carol)
			h.waitEdgeOwner(t, a, nil)
			eventually(t, "the server gives up its owner", func() error {
				owner, err := a.state().Owner()
				if err == nil && owner != nil {
					err = errors.New("still owned by " + owner.Login)
				}
				return err
			})
			after := a.snapshot(t)
			if len(after.members) != len(before.members) || len(after.admins()) != len(before.admins()) || after.members[carolID] != domain.RoleAdmin {
				t.Fatalf("members %v -> %v; want every member and role kept", before.members, after.members)
			}
			if ws, err := a.db.ListWorkspaces(ctx); err != nil || len(ws) != 1 || ws[0].Name != "kept" {
				t.Fatalf("workspaces = %+v %v", ws, err)
			}
			if !h.relay().Online(a.id) {
				t.Fatal("the ownerless server is off the edge")
			}
			h.mustDial(da, a)

			// A member who is not an admin cannot take the ownerless server
			// with a code: the claim makes no new admin.
			code := a.claimCode(t, time.Now())
			_, err := h.claim(da, code)
			wantClaimRefused(t, "a collaborator's claim of the ownerless server", err, edgeproto.RefusalClaimed)
			if got := a.snapshot(t); len(got.admins()) != len(before.admins()) {
				t.Fatalf("admins after the refused claim = %v", got.admins())
			}

			// Recovery on the machine: carol's member, on the tailnet
			// dashboard, links her account again, and a new code claims.
			if err = a.local(t, carolID, protocol.MethodMemberIdentityLink,
				protocol.MemberIdentityLinkParams{Provider: edgeproto.ProviderGitHub, Login: carol.Login}, nil); err != nil {
				t.Fatalf("link carol's account: %v", err)
			}
			ca2 := h.login(carol)[0]
			res, err := h.claim(ca2, a.claimCode(t, time.Now()))
			if err != nil || res.Info.Member.ID != string(carolID) || res.Info.Member.Role != "admin" {
				t.Fatalf("recovery claim: %+v %v", res.Info.Member, err)
			}
			h.waitEdgeOwner(t, a, &carol)
			if got := a.snapshot(t); len(got.members) != len(before.members) || len(got.admins()) != len(before.admins()) {
				t.Fatalf("members after recovery %v; want %v", got.members, before.members)
			}
		})
	}
}

// An account deleted while one of its servers is offline reaches that
// server when it next enrolls.
func TestAccountDeletionReachesAnOfflineServer(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	a := h.newServer(edgeproto.PolicyAccount)
	cs := h.login(alice, erin)
	al, er := cs[0], cs[1]
	h.claimServer(al, a)
	h.join(h.control(al, a), er, a, "collaborator")
	erinID := a.memberOf(t, erin).ID

	h.proxy.cutServers()
	eventually(t, "server off the edge", func() error {
		if h.relay().Online(a.id) {
			return errors.New("still online")
		}
		return nil
	})
	h.deleteAccount(er)
	time.Sleep(time.Second)
	if _, err := a.db.GetMemberByIdentity(context.Background(), edgeproto.ProviderGitHub, fmt.Sprint(erin.ID)); err != nil {
		t.Fatalf("the offline server lost erin's identity before it reconnected: %v", err)
	}
	from := h.proxy.mark()
	h.proxy.restoreServers()
	h.proxy.await(t, "the owed deletion", from, func(e logEntry) bool {
		d, ok := e.msg.(edgeproto.AccountDeleted)
		return ok && e.fromEdge && e.serverID == a.id && d.Subject == fmt.Sprint(erin.ID)
	})
	identityGone(t, a, erin)
	if m, err := a.db.GetMember(context.Background(), erinID); err != nil || m.Role != domain.RoleCollaborator {
		t.Fatalf("erin's member after the deletion = %+v, %v; want kept", m, err)
	}
	if devs := a.devicesOf(t, erinID); len(devs) != 0 {
		t.Fatalf("erin's edge devices after the deletion = %+v", devs)
	}
	if strings.Contains(fmt.Sprint(a.snapshot(t).identities), fmt.Sprint(erin.ID)) {
		t.Fatal("erin's identity is still listed")
	}
}

// An admin who reached the server only through the edge deletes their
// account: nobody remote can restore them. On the machine, a claim code
// naming that admin binds the account they sign in with again to the
// same member and approves the claiming device. A code naming a member
// who is not an admin claims nothing, and no member or admin is added.
func TestConsoleRecoveryOfAnAdminWithoutAnAccount(t *testing.T) {
	t.Parallel()
	for _, policy := range policies {
		t.Run(string(policy), func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			a := h.newServer(policy)
			cs := h.login(carol, dave)
			ca, da := cs[0], cs[1]
			h.claimServer(ca, a)
			h.join(h.control(ca, a), da, a, "collaborator")
			carolID, daveID := a.memberOf(t, carol).ID, a.memberOf(t, dave).ID
			before := a.snapshot(t)

			h.deleteAccount(ca)
			identityGone(t, a, carol)
			h.waitEdgeOwner(t, a, nil)
			ca2 := h.login(carol)[0]
			if _, err := h.claim(ca2, a.claimCode(t, time.Now())); err == nil {
				t.Fatal("a plain claim code bound an account no admin holds")
			}

			_, err := h.claim(ca2, a.recoveryCode(t, daveID))
			if err == nil || !strings.Contains(err.Error(), "is collaborator, not an admin") {
				t.Fatalf("a recovery code naming a collaborator: %v", err)
			}
			res, err := h.claim(ca2, a.recoveryCode(t, carolID))
			if err != nil || res.Info.Member.ID != string(carolID) || res.Info.Member.Role != string(domain.RoleAdmin) {
				t.Fatalf("console recovery claim: %+v %v; want carol's admin member", res.Info.Member, err)
			}
			h.waitEdgeOwner(t, a, &carol)
			if a.deviceStatus(t, ca2) != domain.DeviceApproved {
				t.Fatal("the recovering device is not approved")
			}
			after := a.snapshot(t)
			if len(after.members) != len(before.members) || len(after.admins()) != len(before.admins()) ||
				after.members[daveID] != domain.RoleCollaborator {
				t.Fatalf("members %v -> %v; want the same members and roles", before.members, after.members)
			}
			h.mustDial(ca2, a)
		})
	}
}

// An account deletion lost between the edge and the server, with the
// control channel closing before the server applied it, stays owed: the
// edge sends it again when the server reconnects and forgets it only once
// the server answers that it applied it. The deleted account's identity
// and edge devices are then gone, its device key no longer connects
// directly, and the same notice again changes nothing.
func TestLostAccountDeletionIsSentAgain(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	a := h.newServer(edgeproto.PolicyApprovedDevices)
	cs := h.login(alice, erin)
	al, er := cs[0], cs[1]
	h.claimServer(al, a)
	ctl := h.control(al, a)
	h.join(ctl, er, a, "collaborator")
	h.approveForDirect(ctl, er, a)
	erinID := a.memberOf(t, erin).ID
	ctx := context.Background()
	notice := edgeproto.AccountDeleted{Provider: edgeproto.ProviderGitHub, Subject: fmt.Sprint(erin.ID)}
	isNotice := func(e logEntry) bool { return e.fromEdge && e.serverID == a.id && e.msg == notice }
	isAnswer := func(e logEntry) bool {
		return !e.fromEdge && e.serverID == a.id && e.msg == edgeproto.AccountDeletionApplied(notice)
	}

	from := h.proxy.mark()
	h.proxy.setTamper(t, func(e logEntry) (edgeproto.Message, bool) { return e.msg, !isNotice(e) })
	h.deleteAccount(er)
	h.proxy.await(t, "the deletion the proxy drops", from, isNotice)
	if _, err := a.db.GetMemberByIdentity(ctx, notice.Provider, notice.Subject); err != nil {
		t.Fatalf("the server lost erin's identity without the notice: %v", err)
	}
	if owed, err := h.edgeStore().PendingDeletions(ctx, a.id); err != nil || len(owed) != 1 || owed[0] != notice {
		t.Fatalf("deletions owed after the write = %+v, %v; want erin's still owed", owed, err)
	}

	h.proxy.setTamper(t, nil)
	from = h.proxy.mark()
	h.proxy.closeLinks()
	h.proxy.await(t, "the deletion sent again", from, isNotice)
	h.proxy.await(t, "the server's answer", from, isAnswer)
	identityGone(t, a, erin)
	if devs := a.devicesOf(t, erinID); len(devs) != 0 {
		t.Fatalf("erin's edge devices after the deletion = %+v", devs)
	}
	if sc, err := h.dial(er, directLink(a)); err == nil {
		_ = sc.Close()
		t.Fatal("the deleted account's device key still connects directly")
	}
	eventually(t, "the edge forgets the applied deletion", func() error {
		owed, err := h.edgeStore().PendingDeletions(ctx, a.id)
		if err == nil && len(owed) > 0 {
			err = fmt.Errorf("still owes %+v", owed)
		}
		return err
	})

	before := a.snapshot(t)
	from = h.proxy.mark()
	h.proxy.inject(t, a.id, notice, true)
	h.proxy.await(t, "the answer to a repeated deletion", from, isAnswer)
	if after := a.snapshot(t); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Fatalf("a repeated deletion changed the server: %+v -> %+v", before, after)
	}
	if !h.relay().Online(a.id) {
		t.Fatal("a repeated deletion took the server off the edge")
	}
}
