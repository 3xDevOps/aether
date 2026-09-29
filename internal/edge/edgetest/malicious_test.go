package edgetest

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	edgestore "github.com/3xDevOps/Aether/internal/edge/store"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

func newSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// resign returns open with its grant re-signed by the edge's key after
// edit changed it: what an attacker holding the edge does to a client's
// connection in flight.
func (h *harness) resign(t *testing.T, open edgeproto.Open, serverID string, edit func(*edgeproto.Grant)) edgeproto.Open {
	t.Helper()
	key := h.edgeKey()
	g, err := edgeproto.VerifyGrant(key.Public().(ed25519.PublicKey), open.Grant,
		edgeproto.GrantScope{Issuer: h.relayURL, ServerID: serverID, ConnID: open.ConnID, Kind: open.Kind}, time.Now())
	if err != nil {
		t.Errorf("the edge's own grant: %v", err)
		return open
	}
	edit(&g)
	if open.Grant, err = edgeproto.SignGrant(key, g); err != nil {
		t.Errorf("re-sign grant: %v", err)
	}
	return open
}

// substituteAccount makes the edge re-sign every open of kind to server s
// for account, keeping the client's device key.
func (h *harness) substituteAccount(t *testing.T, s *serverNode, kind string, account edgeproto.Account) {
	h.proxy.setTamper(t, func(e logEntry) (edgeproto.Message, bool) {
		o, ok := e.msg.(edgeproto.Open)
		if !ok || !e.fromEdge || e.serverID != s.id || o.Kind != kind {
			return e.msg, true
		}
		return h.resign(t, o, s.id, func(g *edgeproto.Grant) { g.Account = account }), true
	})
}

// approvedKeys are the device keys s holds approved.
func (sn snapshot) approvedKeys() map[string]string {
	out := map[string]string{}
	for key, d := range sn.devices {
		if strings.HasSuffix(d, " "+string(domain.DeviceApproved)) {
			out[key] = d
		}
	}
	return out
}

func sameKeys(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// reenrolled waits until the server enrolls again after from, as it does
// after its control channel closed.
func (h *harness) reenrolled(t *testing.T, s *serverNode, from int) {
	t.Helper()
	h.proxy.await(t, "server "+s.id+" enrolled again", from, func(e logEntry) bool {
		r, ok := e.msg.(edgeproto.Ready)
		return ok && r.ServerID == s.id
	})
}

// An attacker holding the edge and its signing key, against a server
// that admits approved devices only. Every attack ends without workspace
// access and without an administrator gaining a usable credential; the
// test reads the server's store after each.
func TestMaliciousEdgeApprovedDevices(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	a := h.newServer(edgeproto.PolicyApprovedDevices)
	cs := h.login(alice, bob, mallory, bob)
	al, bo, ma, bo2 := cs[0], cs[1], cs[2], cs[3]
	h.claimServer(al, a)
	ctl := h.control(al, a)
	inviteLogin(t, ctl, bo, a.id, "collaborator")
	_, err := h.dial(bo, h.link(a))
	approve(t, ctl, waitingCode(t, "bob's first device", err))
	h.mustDial(bo, a)
	// An open admin invitation, for dave, who has not signed in yet.
	invite(t, ctl, protocol.MemberInvitationCreateParams{Provider: edgeproto.ProviderGitHub, Login: dave.Login, Role: "admin"})
	start := a.snapshot(t)
	approved := start.approvedKeys()

	// A grant for the admin, with the attacker's key: a waiting device.
	attacker := newSigner(t)
	nc, err := h.forge(t, a, forgery{kind: edgeproto.KindSSH, account: alice.account(), key: attacker.PublicKey()})
	if err != nil {
		t.Fatalf("forged grant: the server refused the open: %v", err)
	}
	_, banner, err := sshOver(nc, a, edgeproto.AccountUser(alice.account()), attacker)
	if err == nil || !strings.Contains(banner, "signed in as github account alice, is waiting for approval") {
		t.Fatalf("forged grant for the admin: %v, banner %q; want a waiting device", err, banner)
	}
	// Whoever is handed its code is shown, before approving, that it
	// admits the key as alice's admin member.
	m := approvalCode.FindStringSubmatch(banner)
	if m == nil {
		t.Fatalf("no approval code in %q", banner)
	}
	found := call[protocol.MemberDeviceLookupResult](t, ctl, protocol.MethodMemberDeviceLookup, protocol.MemberDeviceLookupParams{Code: m[1]})
	if found.MemberID != string(a.memberOf(t, alice).ID) || found.Role != string(domain.RoleAdmin) || found.Device.Account != alice.Login {
		t.Fatalf("lookup of the attacker's code = %+v, want alice's admin member", found)
	}
	if got := a.snapshot(t); !sameKeys(got.approvedKeys(), approved) || len(got.admins()) != len(start.admins()) {
		t.Fatalf("after a forged admin grant: approved %v, admins %v", got.approvedKeys(), got.admins())
	}
	if d := a.snapshot(t).devices[edgeproto.DeviceKeyLine(attacker.PublicKey())]; !strings.HasSuffix(d, " pending") {
		t.Fatalf("the attacker's device is %q, want pending", d)
	}

	// A grant naming another account than the one the device signed in
	// as: bob's on alice's connection with her approved key, and the
	// admin's on a new device of bob's. Each client names its own account
	// inside SSH, so the server records nothing.
	recorded := len(a.snapshot(t).devices)
	for _, sub := range []struct {
		c       *client
		account ghUser
	}{{al, bob}, {bo2, alice}} {
		h.substituteAccount(t, a, edgeproto.KindSSH, sub.account.account())
		_, err = h.dial(sub.c, h.link(a))
		if err == nil || !strings.Contains(err.Error(), "but the edge signed the connection in as github account "+sub.account.Login) {
			t.Fatalf("a device of %s under %s's account: %v", sub.c.user.Login, sub.account.Login, err)
		}
	}
	h.proxy.setTamper(t, nil)
	if got := a.snapshot(t); !sameKeys(got.approvedKeys(), approved) || len(got.devices) != recorded {
		t.Fatalf("after grants naming another account: approved %v, %d devices, want %d", got.approvedKeys(), len(got.devices), recorded)
	}

	// A forged acceptance of dave's admin invitation, under dave's account
	// with the attacker's key, creates no member and no administrator,
	// binds no account and uses nothing up: the device waits on the
	// invitation until a person approves it.
	daveKey := newSigner(t)
	nc, err = h.forge(t, a, forgery{kind: edgeproto.KindSSH, account: dave.account(), key: daveKey.PublicKey()})
	if err != nil {
		t.Fatal(err)
	}
	if _, banner, herr := sshOver(nc, a, edgeproto.AccountUser(dave.account()), daveKey); herr == nil || !strings.Contains(banner, "is waiting for approval") {
		t.Fatalf("forged invitation acceptance: %v, banner %q", herr, banner)
	}
	ctx := context.Background()
	if m, gerr := a.db.GetMemberByIdentity(ctx, edgeproto.ProviderGitHub, dave.account().Subject); !errors.Is(gerr, store.ErrNotFound) {
		t.Fatalf("a forged acceptance bound dave's account to %+v, %v; want no member", m, gerr)
	}
	got := a.snapshot(t)
	if len(got.members) != len(start.members) || len(got.admins()) != len(start.admins()) || !sameKeys(got.approvedKeys(), approved) {
		t.Fatalf("after a forged acceptance: members %v -> %v, approved %v; want nothing added", start.members, got.members, got.approvedKeys())
	}
	waiting, err := a.db.GetDeviceByCredential(ctx, edgeproto.DeviceKeyLine(daveKey.PublicKey()))
	if err != nil || waiting.Member != "" || waiting.Invitation == "" || waiting.Status != domain.DevicePending {
		t.Fatalf("the forged device = %+v, %v; want it pending on the invitation, with no member", waiting, err)
	}
	if inv, ierr := a.db.GetInvitation(ctx, waiting.Invitation); ierr != nil || inv.ConsumedAt != nil || inv.Role != domain.RoleAdmin {
		t.Fatalf("dave's invitation after a forged acceptance = %+v, %v; want it open", inv, ierr)
	}

	// A connection replayed: the server refuses a connection id it served.
	honest := h.proxy.await(t, "an ssh open of alice", 0, func(e logEntry) bool {
		o, ok := e.msg.(edgeproto.Open)
		return ok && e.fromEdge && e.serverID == a.id && o.Kind == edgeproto.KindSSH
	}).msg.(edgeproto.Open)
	if _, oerr := h.sendOpen(t, a, honest); oerr == nil || !strings.Contains(oerr.Error(), "already used") {
		t.Fatalf("replayed open: %v, want refused as already used", oerr)
	}

	// The server's announced policy and directory altered on their way to
	// the edge: the edge believes them, the server does not.
	from := h.proxy.mark()
	h.proxy.setTamper(t, func(e logEntry) (edgeproto.Message, bool) {
		if hello, ok := e.msg.(edgeproto.Hello); ok && !e.fromEdge {
			hello.AccessPolicy = edgeproto.PolicyAccount
			return hello, true
		}
		if d, ok := e.msg.(edgeproto.Directory); ok && !e.fromEdge && e.serverID == a.id {
			d.Entries = append(d.Entries, edgeproto.DirectoryEntry{Kind: edgeproto.EntryMember,
				Provider: edgeproto.ProviderGitHub, Subject: mallory.account().Subject, Role: "admin"})
			return d, true
		}
		return e.msg, true
	})
	h.proxy.closeLinks()
	h.reenrolled(t, a, from)
	waitRole(t, ma, a.id, "admin")
	servers, err := ma.edge.Servers(context.Background())
	if err != nil || len(servers) != 1 || servers[0].AccessPolicy != edgeproto.PolicyAccount {
		t.Fatalf("mallory's servers = %+v %v; want the altered policy the edge believes", servers, err)
	}
	_, err = h.dial(ma, h.link(a))
	if err == nil || !strings.Contains(err.Error(), "github account mallory is not a member of this server") {
		t.Fatalf("mallory, listed by the edge as an admin: %v", err)
	}
	h.proxy.setTamper(t, nil)
	if got := a.snapshot(t); !sameKeys(got.approvedKeys(), approved) || got.identities["github/6666"] != "" {
		t.Fatalf("after an altered directory: approved %v, identities %v", got.approvedKeys(), got.identities)
	}

	// Ownership reports sent to the server, which only ever sends them:
	// the server drops the control channel and keeps its owner.
	for _, m := range []edgeproto.Message{
		edgeproto.Claimed{ConnID: edgeproto.NewConnID(), Owner: edgeproto.AccountPrincipal(mallory.account())},
		edgeproto.OwnerTransferred{ID: edgeproto.NewConnID(), Owner: edgeproto.AccountPrincipal(mallory.account())},
		edgeproto.Ownerless{},
	} {
		from := h.proxy.mark()
		h.proxy.inject(t, a.id, m, true)
		h.reenrolled(t, a, from)
		if owner, oerr := a.state().Owner(); oerr != nil || owner == nil || owner.Login != alice.Login {
			t.Fatalf("owner after a forged %T = %+v, %v; want alice", m, owner, oerr)
		}
	}

	// A forged account deletion takes access away and grants none: bob
	// loses his edge identity and devices, and stays a member.
	h.proxy.inject(t, a.id, edgeproto.AccountDeleted{Provider: edgeproto.ProviderGitHub, Subject: bob.account().Subject}, true)
	eventually(t, "bob's edge identity removed", func() error {
		if _, gerr := a.db.GetMemberByIdentity(context.Background(), edgeproto.ProviderGitHub, bob.account().Subject); gerr == nil {
			return errors.New("still bound")
		}
		return nil
	})
	end := a.snapshot(t)
	if len(end.members) != len(start.members) || len(end.admins()) != len(start.admins()) {
		t.Fatalf("members %v -> %v; want the same members and admins", start.members, end.members)
	}
	delete(approved, edgeproto.DeviceKeyLine(bo.signer(t).PublicKey()))
	if !sameKeys(end.approvedKeys(), approved) {
		t.Fatalf("approved devices %v, want %v", end.approvedKeys(), approved)
	}
	if _, err = h.dial(bo, h.link(a)); err == nil || !strings.Contains(err.Error(), "not a member of this server") {
		t.Fatalf("bob after the forged deletion: %v, want refused as no member", err)
	}
	h.mustDial(al, a)
}

// A claim connection whose account the edge substitutes is refused by
// the server before the code is tried, under either policy: the client
// names the account it signed in as, inside SSH.
func TestMaliciousEdgeSubstitutesTheClaimingAccount(t *testing.T) {
	t.Parallel()
	for _, policy := range policies {
		t.Run(string(policy), func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			s := h.newServer(policy)
			al := h.login(alice)[0]
			code := s.claimCode(t, time.Now())
			h.substituteAccount(t, s, edgeproto.KindClaim, mallory.account())
			_, err := h.claim(al, code)
			if err == nil || !strings.Contains(err.Error(), "the edge signed the connection in as github account mallory") {
				t.Fatalf("claim with a substituted account: %v", err)
			}
			if left := s.claimAttemptsLeft(t); left != edgeproto.ClaimCodeAttempts {
				t.Fatalf("the substituted claim spent attempts: %d left", left)
			}
			if members, err := s.db.ListMembers(context.Background()); err != nil || len(members) != 0 {
				t.Fatalf("members after a substituted claim = %d, %v; want none", len(members), err)
			}
			if owner, err := s.state().Owner(); err != nil || owner != nil {
				t.Fatalf("owner after a substituted claim = %+v, %v", owner, err)
			}
			if _, err := h.edgeStore().Server(context.Background(), s.id); !errors.Is(err, edgestore.ErrNotFound) {
				t.Fatalf("the edge recorded a claim: %v", err)
			}
			// Without the substitution the same code claims for alice.
			h.proxy.setTamper(t, nil)
			if res, err := h.claim(al, code); err != nil || res.Info.Member.Role != string(domain.RoleAdmin) {
				t.Fatalf("honest claim: %+v %v", res.Info.Member, err)
			}
		})
	}
}

// The same attacker against a server under account access. This is
// what that policy trusts the edge with: a grant the attacker signs for
// the admin, with the attacker's own key, is admitted with the admin's
// role. What the server checks for itself still holds: a key registered
// to one account never serves another.
func TestMaliciousEdgeAccountAccess(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	a := h.newServer(edgeproto.PolicyAccount)
	cs := h.login(alice, bob)
	al, bo := cs[0], cs[1]
	h.claimServer(al, a)
	ctl := h.control(al, a)
	inviteLogin(t, ctl, bo, a.id, "collaborator")
	h.mustDial(bo, a)

	attacker := newSigner(t)
	nc, err := h.forge(t, a, forgery{kind: edgeproto.KindSSH, account: alice.account(), key: attacker.PublicKey()})
	if err != nil {
		t.Fatal(err)
	}
	sc, banner, err := sshOver(nc, a, edgeproto.AccountUser(alice.account()), attacker)
	if err != nil {
		t.Fatalf("forged admin grant under account access: %v (banner %q); this policy admits it", err, banner)
	}
	defer sc.Close() //nolint:errcheck // test client
	forged := controlOver(t, sc)
	info := call[protocol.ServerInfoResult](t, forged, protocol.MethodServerInfo, struct{}{})
	if info.Member.Role != string(domain.RoleAdmin) || info.Member.ID != string(a.memberOf(t, alice).ID) {
		t.Fatalf("the forged connection is %+v, want alice's admin member", info.Member)
	}
	invite(t, forged, protocol.MemberInvitationCreateParams{Provider: edgeproto.ProviderGitHub, Login: mallory.Login, Role: "admin"})
	if d := a.snapshot(t).devices[edgeproto.DeviceKeyLine(attacker.PublicKey())]; !strings.HasSuffix(d, " registered") {
		t.Fatalf("the attacker's device is %q, want registered and listed", d)
	}

	h.substituteAccount(t, a, edgeproto.KindSSH, bob.account())
	_, err = h.dial(al, h.link(a))
	if err == nil || !strings.Contains(err.Error(), "but the edge signed the connection in as github account bob") {
		t.Fatalf("alice's key under bob's account: %v", err)
	}
}

// Someone who takes over bob's GitHub account signs in to the honest
// edge as bob on their own machine. Under account access that is bob's
// access; under approved devices it is a waiting device, and bob's own
// devices keep working.
func TestTakenOverProviderAccount(t *testing.T) {
	t.Parallel()
	for _, policy := range policies {
		t.Run(string(policy), func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			a := h.newServer(policy)
			cs := h.login(alice, bob, bob)
			al, bo, thief := cs[0], cs[1], cs[2]
			h.claimServer(al, a)
			ctl := h.control(al, a)
			inviteLogin(t, ctl, bo, a.id, "admin")
			if policy == edgeproto.PolicyApprovedDevices {
				_, err := h.dial(bo, h.link(a))
				approve(t, ctl, waitingCode(t, "bob's device", err))
			}
			h.mustDial(bo, a)
			before := a.snapshot(t)

			waitRole(t, thief, a.id, "admin")
			_, err := h.dial(thief, h.link(a))
			switch policy {
			case edgeproto.PolicyAccount:
				if err != nil {
					t.Fatalf("taken-over account under account access: %v; this policy admits it", err)
				}
				info := call[protocol.ServerInfoResult](t, h.control(thief, a), protocol.MethodServerInfo, struct{}{})
				if info.Member.Role != string(domain.RoleAdmin) {
					t.Fatalf("the thief is %+v, want bob's admin member", info.Member)
				}
			default:
				waitingCode(t, "taken-over account under approved devices", err)
				after := a.snapshot(t)
				if !sameKeys(after.approvedKeys(), before.approvedKeys()) || len(after.admins()) != len(before.admins()) {
					t.Fatalf("the thief changed who is approved: %v -> %v", before.approvedKeys(), after.approvedKeys())
				}
			}
			// Either way the thief's device is listed on the server.
			if got := a.deviceStatus(t, thief); got == "" {
				t.Fatal("the thief's device is not listed")
			}
			h.mustDial(bo, a)
		})
	}
}
