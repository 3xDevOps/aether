package edgetest

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func wantClaimRefused(t *testing.T, what string, err error, want edgeproto.Refusal) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), string(want)) {
		t.Fatalf("%s: %v, want %q", what, err, want)
	}
}

// A claim sends its secret only to the server whose host key derives the
// id in the code, and spends one of five attempts per try; an expired
// code claims nothing. The claiming device is approved under either
// policy, and a claimed server refuses another account's claim.
func TestClaim(t *testing.T) {
	t.Parallel()
	for _, policy := range policies {
		t.Run(string(policy), func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			a, b := h.newServer(policy), h.newServer(policy)
			cs := h.login(alice, bob)
			al, bo := cs[0], cs[1]

			// An edge that relays a's claim to b: the client sees b's host
			// key and stops before it authenticates.
			code := a.claimCode(t, time.Now())
			b.claimCode(t, time.Now())
			h.proxy.setRoute(t, func(path string) string {
				return strings.Replace(path, edgeproto.ClaimConnectPath(a.id), edgeproto.ClaimConnectPath(b.id), 1)
			})
			_, err := h.claim(al, code)
			if err == nil || !strings.Contains(err.Error(), "not the server the claim code names "+a.id) {
				t.Fatalf("claim relayed to another server: %v, want the host key refused", err)
			}
			for _, s := range []*serverNode{a, b} {
				if left := s.claimAttemptsLeft(t); left != edgeproto.ClaimCodeAttempts {
					t.Fatalf("server %s: %d claim attempts left, want %d: the code reached it", s.id, left, edgeproto.ClaimCodeAttempts)
				}
				if members, lerr := s.db.ListMembers(context.Background()); lerr != nil || len(members) != 0 {
					t.Fatalf("server %s has members %v, %v", s.id, members, lerr)
				}
			}
			h.proxy.setRoute(t, nil)

			_, err = h.claim(al, a.claimCode(t, time.Now().Add(-edgeproto.ClaimCodeTTL-time.Minute)))
			wantClaimRefused(t, "expired code", err, edgeproto.RefusalClaimExpired)

			code = a.claimCode(t, time.Now())
			wrong := code[:edgeproto.ServerIDLength+1] + strings.Repeat("a", len(code)-edgeproto.ServerIDLength-1)
			for range edgeproto.ClaimCodeAttempts - 1 {
				_, err = h.claim(al, wrong)
				wantClaimRefused(t, "wrong code", err, edgeproto.RefusalClaimWrong)
			}
			_, err = h.claim(al, wrong)
			wantClaimRefused(t, "last wrong attempt", err, edgeproto.RefusalClaimExhausted)
			_, err = h.claim(al, code)
			wantClaimRefused(t, "right code after five attempts", err, edgeproto.RefusalClaimExhausted)

			h.claimServer(al, a)
			if got := a.deviceStatus(t, al); got != domain.DeviceApproved {
				t.Fatalf("claiming device is %q, want approved", got)
			}
			servers, err := al.edge.Servers(context.Background())
			if err != nil || len(servers) != 1 || servers[0].ID != a.id || servers[0].Role != "admin" || !servers[0].Online ||
				servers[0].AccessPolicy != policy {
				t.Fatalf("alice's servers = %+v %v", servers, err)
			}

			_, err = h.claim(bo, a.claimCode(t, time.Now()))
			wantRefusal(t, "second account claiming a claimed server", err, edgeproto.RefusalClaimed)
			if owner := h.edgeOwner(a.id); owner == nil || owner.Login != alice.Login {
				t.Fatalf("edge owner = %+v, want alice", owner)
			}
		})
	}
}

// TestFailedClaimRecordRecovers makes the edge fail to record the owner
// after the server made the claimant its admin and used up the code. The
// edge refuses the server's report and closes its control channel, the
// server gives up its owner when the edge reports it unclaimed, a fresh
// code claims it again for the same account without changing its
// members, and no other account can use the gap.
func TestFailedClaimRecordRecovers(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	a := h.newServer(edgeproto.PolicyApprovedDevices)
	cs := h.login(alice, bob)
	al, bo := cs[0], cs[1]
	ctx := context.Background()

	// The fault is injected into the edge's own database, so nothing in
	// the edge's code path changes.
	edgeDB, err := sql.Open("sqlite", "file:"+url.PathEscape(filepath.Join(h.edgeDir, "edge.db"))+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = edgeDB.Close() })
	if _, err = edgeDB.Exec(`CREATE TRIGGER fail_claim BEFORE INSERT ON servers
		BEGIN SELECT RAISE(ABORT, 'injected: the edge cannot record the owner'); END`); err != nil {
		t.Fatal(err)
	}
	from := h.proxy.mark()
	if _, err = h.claim(al, a.claimCode(t, time.Now())); err != nil {
		t.Fatalf("claim: %v", err)
	}
	members, err := a.db.ListMembers(ctx)
	if err != nil || len(members) != 1 || members[0].Role != domain.RoleAdmin {
		t.Fatalf("members after the claim = %+v, %v; want alice as admin", members, err)
	}
	admin := members[0].ID
	if _, err = edgeDB.Exec(`DROP TRIGGER fail_claim`); err != nil {
		t.Fatal(err)
	}

	// The edge closed the control channel with its reason; the server's
	// next enrollment is told it is unclaimed.
	h.proxy.await(t, "the server back, unclaimed", from, func(e logEntry) bool {
		r, ok := e.msg.(edgeproto.Ready)
		return ok && r.ServerID == a.id && r.State == edgeproto.StateUnclaimed
	})
	eventually(t, "the server forgets the owner the edge never recorded", func() error {
		owner, ownerErr := a.state().Owner()
		if ownerErr == nil && owner != nil {
			ownerErr = errors.New("still claimed by " + owner.Login)
		}
		return ownerErr
	})

	// As `aether-server edge claim-code` does, now that the server is
	// unclaimed.
	code := a.claimCode(t, time.Now())
	_, err = h.claim(bo, code)
	wantClaimRefused(t, "another account claiming the unrecorded server", err, edgeproto.RefusalClaimed)
	if res, cerr := h.claim(al, code); cerr != nil || res.Info.Member.ID != string(admin) {
		t.Fatalf("alice claims again: %+v, %v", res.Info.Member, cerr)
	}
	members, err = a.db.ListMembers(ctx)
	if err != nil || len(members) != 1 || members[0].ID != admin || members[0].Role != domain.RoleAdmin {
		t.Fatalf("members after the second claim = %+v, %v; want alice unchanged", members, err)
	}
	eventually(t, "the edge records alice", func() error {
		if !h.relay().Online(a.id) {
			return errors.New("not online")
		}
		return nil
	})
	h.mustDial(al, a)
	if _, err = h.dial(bo, h.link(a)); err == nil {
		t.Fatal("bob reached the server")
	}
}

// A server that already has members is claimed by an admin who linked
// their account. The directory the server pushes as it accepts the claim
// reaches the edge before the edge records the claim, and must not be
// lost: bob's open invitation is in it.
func TestClaimKeepsTheDirectoryPushedWithIt(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	a := h.newServer(edgeproto.PolicyAccount)
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
