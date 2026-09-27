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
	"github.com/3xDevOps/Aether/internal/edgeagent"
	"github.com/3xDevOps/Aether/internal/edgeproto"
)

// TestServerVerifiesGrants plays a compromised edge that sends the server
// opens with forged, expired, replayed and misdirected grants: the server
// refuses each before it dials a data socket.
func TestServerVerifiesGrants(t *testing.T) {
	h := newHarness(t)
	a := h.newServer()
	al := h.login(alice)[0]
	h.claimServer(al, a)
	h.mustDial(al, a)

	real := h.proxy.await(t, "an ssh open from the edge", 0, func(e logEntry) bool {
		o, ok := e.msg.(edgeproto.Open)
		return ok && e.fromEdge && e.serverID == a.id && o.Kind == edgeproto.KindSSH
	}).msg.(edgeproto.Open)
	key := h.edgeKey()
	// What `aether-server edge trust` offers to pin is the key grants are
	// signed with.
	fetched, err := edgeagent.FetchEdgeKey(context.Background(), h.origin)
	if err != nil || !fetched.Equal(key.Public()) {
		t.Fatalf("edge key = %x %v, want the signing key", fetched, err)
	}
	template, err := edgeproto.VerifyGrant(key.Public().(ed25519.PublicKey), real.Grant,
		edgeproto.GrantScope{ServerID: a.id, ConnID: real.ConnID, Kind: edgeproto.KindSSH}, time.Now())
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
	// open sends an open to the server as the edge and returns the
	// server's refusal, empty when it accepted.
	open := func(connID, grant string) string {
		from := h.proxy.mark()
		h.proxy.inject(t, a.id, edgeproto.Open{ConnID: connID, Ticket: edgeproto.NewToken(), Kind: edgeproto.KindSSH, Grant: grant}, true)
		return h.proxy.await(t, "the server's answer to "+connID, from, func(e logEntry) bool {
			r, ok := e.msg.(edgeproto.OpenResult)
			return ok && !e.fromEdge && r.ConnID == connID
		}).msg.(edgeproto.OpenResult).Error
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
		want   error
	}{
		{"forged", forger, func(*edgeproto.Grant) {}, edgeproto.ErrGrantSignature},
		{"expired", key, func(g *edgeproto.Grant) {
			g.IssuedAt = time.Now().Add(-3 * time.Minute)
			g.ExpiresAt = g.IssuedAt.Add(edgeproto.GrantTTL)
		}, edgeproto.ErrGrantExpired},
		{"for another server", key, func(g *edgeproto.Grant) { g.ServerID = otherServer }, edgeproto.ErrGrantServer},
	} {
		id, signed := grant(tc.signer, tc.edit)
		if got := open(id, signed); !strings.Contains(got, tc.want.Error()) {
			t.Errorf("%s grant: server answered %q, want %q", tc.name, got, tc.want)
		}
	}
	_, signed := grant(key, func(*edgeproto.Grant) {})
	if got := open(edgeproto.NewConnID(), signed); !strings.Contains(got, edgeproto.ErrGrantConn.Error()) {
		t.Errorf("grant for another connection: server answered %q", got)
	}
	if got := open(real.ConnID, real.Grant); !strings.Contains(got, "already used") {
		t.Errorf("replayed open: server answered %q", got)
	}
	// The same checks pass a well-formed grant: they are what refused
	// the others.
	if got := open(grant(key, func(*edgeproto.Grant) {})); got != "" {
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
	h := newHarness(t)
	a := h.newServer()
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
	// The same client reaches the real server at its direct address,
	// with the device key the server registered on its first relayed
	// connection.
	h.mustDial(al, a)
	sc, err := h.dial(al, cli.Config{ServerID: a.id, Addr: a.addr})
	if err != nil {
		t.Fatalf("direct connection to the real server: %v", err)
	}
	_ = sc.Close()
}
