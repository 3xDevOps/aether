package edgetest

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/cli"
	edgeagent "github.com/3xDevOps/Aether/internal/edge/agent"
	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// TestServerVerifiesGrants plays a compromised or buggy edge that sends
// the server opens with forged, expired, replayed and misdirected grants:
// the server refuses each before it dials a data socket.
func TestServerVerifiesGrants(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	a := h.newServer(edgeproto.PolicyApprovedDevices)
	al := h.login(alice)[0]
	h.claimServer(al, a)
	h.mustDial(al, a)

	real := h.proxy.await(t, "an ssh open from the edge", 0, func(e logEntry) bool {
		o, ok := e.msg.(edgeproto.Open)
		return ok && e.fromEdge && e.serverID == a.id && o.Kind == edgeproto.KindSSH
	}).msg.(edgeproto.Open)
	key := h.edgeKey()
	// What `aether-server edge trust` offers to pin is the key grants are
	// signed with, read from the relay origin.
	info, err := edgeagent.FetchEdgeInfo(context.Background(), h.relayURL)
	if err != nil || !info.Key.Equal(key.Public()) || info.SigninOrigin != h.signinURL {
		t.Fatalf("edge info = %+v %v, want the signing key and the sign-in origin", info, err)
	}
	template, err := edgeproto.VerifyGrant(key.Public().(ed25519.PublicKey), real.Grant,
		edgeproto.GrantScope{Issuer: h.relayURL, ServerID: a.id, ConnID: real.ConnID, Kind: edgeproto.KindSSH}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	grant := func(signer ed25519.PrivateKey, edit func(*edgeproto.Grant)) (string, string) {
		g := template
		g.ConnID = edgeproto.NewConnID()
		g.IssuedAt = time.Now()
		g.ExpiresAt = g.IssuedAt.Add(edgeproto.GrantTTL)
		edit(&g)
		signed, serr := edgeproto.SignGrant(signer, g)
		if serr != nil {
			t.Fatal(serr)
		}
		return g.ConnID, signed
	}
	open := func(connID, grant, kind string) string {
		_, oerr := h.sendOpen(t, a, edgeproto.Open{ConnID: connID, Ticket: edgeproto.NewToken(), Kind: kind, Grant: grant})
		return errString(oerr)
	}
	_, forger, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	otherServer := edgeproto.ServerID(newSigner(t).PublicKey())

	for _, tc := range []struct {
		name   string
		signer ed25519.PrivateKey
		edit   func(*edgeproto.Grant)
		kind   string
		want   error
	}{
		{"forged", forger, func(*edgeproto.Grant) {}, edgeproto.KindSSH, edgeproto.ErrGrantSignature},
		{"expired", key, func(g *edgeproto.Grant) {
			g.IssuedAt = time.Now().Add(-3 * time.Minute)
			g.ExpiresAt = g.IssuedAt.Add(edgeproto.GrantTTL)
		}, edgeproto.KindSSH, edgeproto.ErrGrantExpired},
		{"for another server", key, func(g *edgeproto.Grant) { g.ServerID = otherServer }, edgeproto.KindSSH, edgeproto.ErrGrantServer},
		{"from another edge origin", key, func(g *edgeproto.Grant) { g.Issuer = "https://edge.example.test" }, edgeproto.KindSSH, edgeproto.ErrGrantIssuer},
		{"of another kind", key, func(g *edgeproto.Grant) { g.Kind = edgeproto.KindClaim }, edgeproto.KindSSH, edgeproto.ErrGrantKind},
	} {
		id, signed := grant(tc.signer, tc.edit)
		if got := open(id, signed, tc.kind); !strings.Contains(got, tc.want.Error()) {
			t.Errorf("%s grant: server answered %q, want %q", tc.name, got, tc.want)
		}
	}
	_, signed := grant(key, func(*edgeproto.Grant) {})
	if got := open(edgeproto.NewConnID(), signed, edgeproto.KindSSH); !strings.Contains(got, edgeproto.ErrGrantConn.Error()) {
		t.Errorf("grant for another connection: server answered %q", got)
	}
	if got := open(real.ConnID, real.Grant, edgeproto.KindSSH); !strings.Contains(got, "already used") {
		t.Errorf("replayed open: server answered %q", got)
	}
	// The same checks pass a well-formed grant: they are what refused
	// the others.
	id, signed := grant(key, func(*edgeproto.Grant) {})
	if got := open(id, signed, edgeproto.KindSSH); got != "" {
		t.Errorf("valid grant refused: %q", got)
	}
}

// replaySigner presents the server's host key and answers every signing
// request with one signature it captured.
type replaySigner struct {
	pub ssh.PublicKey
	sig *ssh.Signature
}

func (s replaySigner) PublicKey() ssh.PublicKey                       { return s.pub }
func (s replaySigner) Sign(io.Reader, []byte) (*ssh.Signature, error) { return s.sig, nil }

// TestEnrollmentSignatureIsNotAHostSignature plays an edge that keeps the
// signature a server's host key made to enroll, and poses as that server
// with it: the client refuses the handshake.
func TestEnrollmentSignatureIsNotAHostSignature(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	a := h.newServer(edgeproto.PolicyApprovedDevices)
	al := h.login(alice)[0]
	h.claimServer(al, a)

	hello := h.proxy.await(t, "the server's hello", 0, func(e logEntry) bool {
		_, ok := e.msg.(edgeproto.Hello)
		return ok && e.serverID == a.id
	}).msg.(edgeproto.Hello)
	hostKey, err := hello.PublicKey()
	if err != nil {
		t.Fatal(err)
	}
	var sig ssh.Signature
	if err = ssh.Unmarshal(hello.Signature, &sig); err != nil {
		t.Fatal(err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	impostor := &ssh.ServerConfig{NoClientAuth: true}
	impostor.AddHostKey(replaySigner{pub: hostKey, sig: &sig})
	go func() {
		for {
			c, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			go func() {
				defer c.Close() //nolint:errcheck // test server
				if sc, _, _, herr := ssh.NewServerConn(c, impostor); herr == nil {
					_ = sc.Close()
				}
			}()
		}
	}()

	_, err = h.dial(al, cli.Config{ServerID: a.id, Addr: ln.Addr().String()})
	if err == nil || !strings.Contains(err.Error(), "signature did not verify") {
		t.Fatalf("impostor with the enrollment signature: %v, want a failed handshake", err)
	}
	// The same client reaches the real server at its direct address, with
	// the device the claim approved.
	sc, err := h.dial(al, directLink(a))
	if err != nil {
		t.Fatalf("direct connection to the real server: %v", err)
	}
	_ = sc.Close()
}

// A server nobody claimed takes no member from the relay: the honest edge
// refuses to connect to it, and a forged grant, even with an invite code
// in the SSH user name, creates no member and spends no invite.
func TestNoBootstrapOverTheRelay(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fresh := h.newServer(edgeproto.PolicyAccount)
	al := h.login(alice)[0]
	_, err := h.dial(al, h.link(fresh))
	wantRefusal(t, "connect to an unclaimed server", err, edgeproto.RefusalUnknownServer)

	attacker := newSigner(t)
	nc, err := h.forge(t, fresh, forgery{kind: edgeproto.KindSSH, account: mallory.account(), key: attacker.PublicKey()})
	if err != nil {
		t.Fatal(err)
	}
	if _, banner, herr := sshOver(nc, fresh, edgeproto.AccountUser(mallory.account()), attacker); herr == nil || !strings.Contains(banner, "is not a member of this server") {
		t.Fatalf("forged grant to an empty server: %v, banner %q", herr, banner)
	}
	if members, lerr := fresh.db.ListMembers(context.Background()); lerr != nil || len(members) != 0 {
		t.Fatalf("members after relayed contact = %d, %v; want none", len(members), lerr)
	}

	a := h.newServer(edgeproto.PolicyAccount)
	h.claimServer(al, a)
	code := call[protocol.MemberInviteResult](t, h.control(al, a), protocol.MethodMemberInvite, protocol.MemberInviteParams{}).Code
	nc, err = h.forge(t, a, forgery{kind: edgeproto.KindSSH, account: mallory.account(), key: attacker.PublicKey()})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := sshOver(nc, a, "invite:"+code, attacker); err == nil {
		t.Fatal("an invite code joined over the relay")
	}
	if members, err := a.db.ListMembers(context.Background()); err != nil || len(members) != 1 {
		t.Fatalf("members after an invite code over the relay = %d, %v; want the owner only", len(members), err)
	}
	if _, err := dialKey(a, attacker); err == nil {
		t.Fatal("the attacker's key joined")
	}
}

// An edge that floods a server with relayed connections that never
// finish their handshake fills the relayed budget only: the next relayed
// open is refused, and the member's direct connection still works.
func TestRelayedFloodDoesNotBlockDirect(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	a := h.newServer(edgeproto.PolicyApprovedDevices)
	al := h.login(alice)[0]
	h.claimServer(al, a)
	attacker := newSigner(t)
	// The claim's own connection holds one of the server's relayed slots.
	held := 0
	for {
		_, err := h.forge(t, a, forgery{kind: edgeproto.KindSSH, account: alice.account(), key: attacker.PublicKey()})
		if err != nil {
			if !strings.Contains(err.Error(), string(edgeproto.RefusalConnLimit)) {
				t.Fatalf("relayed open %d: %v", held+1, err)
			}
			break
		}
		if held++; held > edgeproto.MaxSSHConnsPerServer {
			t.Fatalf("the server holds %d relayed connections, more than its %d", held, edgeproto.MaxSSHConnsPerServer)
		}
	}
	t.Logf("the flood holds %d relayed connections; the next is refused", held)
	start := time.Now()
	sc, err := h.dial(al, directLink(a))
	if err != nil {
		t.Fatalf("direct connection during the flood: %v", err)
	}
	_ = sc.Close()
	t.Logf("direct connection during the flood took %s", time.Since(start).Round(time.Millisecond))
}
