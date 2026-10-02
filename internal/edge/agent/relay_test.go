package edgeagent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"
)

func deviceKeyLine(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return edgeproto.DeviceKeyLine(key)
}

func sshGrant(t *testing.T, a *Agent, connID string) edgeproto.Grant {
	now := time.Now()
	return edgeproto.Grant{
		Issuer: a.origin, ServerID: a.ServerID(), ConnID: connID, Kind: edgeproto.KindSSH,
		Account:  edgeproto.Account{Provider: edgeproto.ProviderGitHub, Subject: "1001", Login: "octo"},
		DeviceID: "dev-1", DeviceKey: deviceKeyLine(t),
		IssuedAt: now, ExpiresAt: now.Add(edgeproto.GrantTTL),
	}
}

func enrolled(t *testing.T) (*fakeEdge, *fakeSSH, *Agent, *edgeControl) {
	t.Helper()
	edge := newFakeEdge(t)
	sshd := newFakeSSH()
	a := newAgent(t, edge.srv.URL, t.TempDir(), sshd)
	run(t, a)
	ec := edge.nextControl(t)
	expect[edgeproto.Directory](t, ec)
	return edge, sshd, a, ec
}

// echo checks that bytes written on the edge side of a data socket come
// back through the server's side.
func echo(t *testing.T, c *websocket.Conn) {
	t.Helper()
	nc := websocket.NetConn(context.Background(), c, websocket.MessageBinary)
	if _, err := nc.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	_ = nc.SetReadDeadline(time.Now().Add(waitFor))
	if _, err := io.ReadFull(nc, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("relayed %q, %v; want the echo", buf, err)
	}
}

func TestValidGrantReachesSSHOnce(t *testing.T) {
	edge, sshd, a, ec := enrolled(t)
	connID := edgeproto.NewConnID()
	g := edge.grant(t, sshGrant(t, a, connID))
	edge.open(ec, connID, g)
	if r := expect[edgeproto.OpenResult](t, ec); r.ConnID != connID || r.Error != "" {
		t.Fatalf("open result %+v, want success", r)
	}
	echo(t, edge.nextData(t))
	if got := <-sshd.served; got.ConnID != connID || got.Account.Subject != "1001" {
		t.Fatalf("sshd got grant %+v", got)
	}

	edge.open(ec, connID, g)
	if r := expect[edgeproto.OpenResult](t, ec); !strings.Contains(r.Error, "already used") {
		t.Fatalf("replayed grant: open result %+v, want a refusal", r)
	}
	select {
	case g := <-sshd.served:
		t.Fatalf("replayed grant reached sshd: %+v", g)
	case <-time.After(100 * time.Millisecond):
	}
}

// An edge still signing Google accounts in, as a build from the v0.5.2-alpha.3
// tag could, opens nothing, and its client is told why.
func TestGoogleGrantRefusedWithTheReason(t *testing.T) {
	edge, sshd, a, ec := enrolled(t)
	openID := edgeproto.NewConnID()
	g := sshGrant(t, a, openID)
	g.Account = edgeproto.Account{Provider: "google", Subject: "g-1", Email: "octo@example.com"}
	payload, err := json.Marshal(g)
	if err != nil {
		t.Fatal(err)
	}
	sig := ed25519.Sign(edge.priv, append([]byte("aether-edge-grant-v1\x00"), payload...))
	edge.open(ec, openID, base64.RawURLEncoding.EncodeToString(payload)+"."+base64.RawURLEncoding.EncodeToString(sig))
	r := expect[edgeproto.OpenResult](t, ec)
	if r.ConnID != openID || !strings.Contains(r.Error, `sign-in provider "google" is not supported: Aether signs in with GitHub only`) {
		t.Fatalf("open result %+v, want the reason", r)
	}
	select {
	case g := <-sshd.served:
		t.Fatalf("a Google grant reached sshd: %+v", g)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestBadGrantsNeverReachSSH(t *testing.T) {
	edge, sshd, a, ec := enrolled(t)
	_, forger, _ := ed25519.GenerateKey(rand.Reader)
	other := edgeproto.ServerID(newHostKey(t).PublicKey())
	for name, tc := range map[string]struct {
		mutate func(g *edgeproto.Grant, openID *string)
		sign   ed25519.PrivateKey
		want   error
	}{
		"forged": {sign: forger, want: edgeproto.ErrGrantSignature},
		"expired": {mutate: func(g *edgeproto.Grant, _ *string) {
			g.IssuedAt = time.Now().Add(-5 * time.Minute)
			g.ExpiresAt = g.IssuedAt.Add(edgeproto.GrantTTL)
		}, want: edgeproto.ErrGrantExpired},
		"wrong server":     {mutate: func(g *edgeproto.Grant, _ *string) { g.ServerID = other }, want: edgeproto.ErrGrantServer},
		"another edge":     {mutate: func(g *edgeproto.Grant, _ *string) { g.Issuer = "https://edge.example.test" }, want: edgeproto.ErrGrantIssuer},
		"wrong connection": {mutate: func(_ *edgeproto.Grant, id *string) { *id = edgeproto.NewConnID() }, want: edgeproto.ErrGrantConn},
		"claim grant": {mutate: func(g *edgeproto.Grant, _ *string) { g.Kind = edgeproto.KindClaim },
			want: edgeproto.ErrGrantKind},
	} {
		t.Run(name, func(t *testing.T) {
			openID := edgeproto.NewConnID()
			g := sshGrant(t, a, openID)
			if tc.mutate != nil {
				tc.mutate(&g, &openID)
			}
			key := edge.priv
			if tc.sign != nil {
				key = tc.sign
			}
			signed, err := edgeproto.SignGrant(key, g)
			if err != nil {
				t.Fatal(err)
			}
			edge.open(ec, openID, signed)
			r := expect[edgeproto.OpenResult](t, ec)
			if r.ConnID != openID || !strings.Contains(r.Error, tc.want.Error()) {
				t.Fatalf("open result %+v, want refusal %q", r, tc.want)
			}
		})
	}
	select {
	case g := <-sshd.served:
		t.Fatalf("a bad grant reached sshd: %+v", g)
	case c := <-edge.data:
		_ = c.CloseNow()
		t.Fatal("server attached a data socket for a bad grant")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestRevokedDeviceLosesItsConnections(t *testing.T) {
	edge, sshd, a, ec := enrolled(t)
	connID := edgeproto.NewConnID()
	g := sshGrant(t, a, connID)
	edge.open(ec, connID, edge.grant(t, g))
	expect[edgeproto.OpenResult](t, ec)
	data := edge.nextData(t)
	echo(t, data)
	<-sshd.served
	ec.send(edgeproto.DeviceRevoked{DeviceID: g.DeviceID})
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()
	if _, _, err := data.Read(ctx); err == nil || ctx.Err() != nil {
		t.Fatalf("relayed connection still open after revocation: %v", err)
	}
}

// A full connection budget refuses the next open.
func TestConnectionLimit(t *testing.T) {
	edge, _, a, ec := enrolled(t)
	connID := edgeproto.NewConnID()
	edge.open(ec, connID, edge.grant(t, sshGrant(t, a, connID)))
	if r := expect[edgeproto.OpenResult](t, ec); r.Error != "" {
		t.Fatalf("ssh open under the limit: %+v", r)
	}
	// Nothing reads the edge side of this connection's data socket, so it
	// closes, as a real edge's would, before Run is stopped: otherwise
	// Run waits out a close handshake nobody answers.
	data := edge.nextData(t)
	t.Cleanup(func() { _ = data.CloseNow() })
	for range edgeproto.MaxSSHConnsPerServer - 1 {
		a.slots <- struct{}{}
	}
	connID = edgeproto.NewConnID()
	edge.open(ec, connID, edge.grant(t, sshGrant(t, a, connID)))
	if r := expect[edgeproto.OpenResult](t, ec); r.Error != string(edgeproto.RefusalConnLimit) {
		t.Fatalf("ssh open beyond the limit: %+v", r)
	}
}
