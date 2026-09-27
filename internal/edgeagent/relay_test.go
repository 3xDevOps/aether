package edgeagent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/edgeproto"
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

func sshGrant(t *testing.T, serverID, connID string) edgeproto.Grant {
	now := time.Now()
	return edgeproto.Grant{
		ServerID: serverID, ConnID: connID, Kind: edgeproto.KindSSH,
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
	g := edge.grant(t, sshGrant(t, a.ServerID(), connID))
	edge.open(ec, connID, edgeproto.KindSSH, g)
	if r := expect[edgeproto.OpenResult](t, ec); r.ConnID != connID || r.Error != "" {
		t.Fatalf("open result %+v, want success", r)
	}
	echo(t, edge.nextData(t))
	if got := <-sshd.served; got.ConnID != connID || got.Account.Subject != "1001" {
		t.Fatalf("sshd got grant %+v", got)
	}

	edge.open(ec, connID, edgeproto.KindSSH, g)
	if r := expect[edgeproto.OpenResult](t, ec); !strings.Contains(r.Error, "already used") {
		t.Fatalf("replayed grant: open result %+v, want a refusal", r)
	}
	select {
	case g := <-sshd.served:
		t.Fatalf("replayed grant reached sshd: %+v", g)
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
		"wrong connection": {mutate: func(_ *edgeproto.Grant, id *string) { *id = edgeproto.NewConnID() }, want: edgeproto.ErrGrantConn},
		"claim grant": {mutate: func(g *edgeproto.Grant, _ *string) { g.Kind = edgeproto.KindClaim },
			want: edgeproto.ErrGrantKind},
	} {
		t.Run(name, func(t *testing.T) {
			openID := edgeproto.NewConnID()
			g := sshGrant(t, a.ServerID(), openID)
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
			edge.open(ec, openID, edgeproto.KindSSH, signed)
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
	g := sshGrant(t, a.ServerID(), connID)
	edge.open(ec, connID, edgeproto.KindSSH, edge.grant(t, g))
	expect[edgeproto.OpenResult](t, ec)
	data := edge.nextData(t)
	echo(t, data)
	<-sshd.served
	ec.send(edgeproto.DeviceRevoked{DeviceID: g.DeviceID})
	select {
	case key := <-sshd.closed:
		if key != g.DeviceKey {
			t.Errorf("CloseEdgeDevice(%q), want the grant's device key", key)
		}
	case <-time.After(waitFor):
		t.Fatal("sshd was not told about the revoked device")
	}
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()
	if _, _, err := data.Read(ctx); err == nil || ctx.Err() != nil {
		t.Fatalf("relayed connection still open after revocation: %v", err)
	}
}

func TestWebOpenReachesTheWebListener(t *testing.T) {
	edge, _, a, ec := enrolled(t)
	connID := edgeproto.NewConnID()
	edge.open(ec, connID, edgeproto.KindWeb, "")
	if r := expect[edgeproto.OpenResult](t, ec); r.Error != "" {
		t.Fatalf("web open refused: %s", r.Error)
	}
	data := edge.nextData(t)
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := a.WebListener().Accept()
		if err != nil {
			t.Error(err)
		}
		accepted <- c
	}()
	c := <-accepted
	// net/http stops its background read on every hijack and after every
	// response with a read deadline in the past: that interrupts the read
	// and leaves the connection open.
	readErr := make(chan error, 1)
	go func() {
		_, err := c.Read(make([]byte, 1))
		readErr <- err
	}()
	time.Sleep(50 * time.Millisecond)
	if err := c.SetReadDeadline(time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	if err := <-readErr; !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("read under a past deadline: %v, want a timeout", err)
	}
	if err := c.SetReadDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = io.Copy(c, c) }()
	echo(t, data)
	if c.RemoteAddr().String() == "192.0.2.10:4242" {
		t.Error("the edge-asserted client address became the connection's RemoteAddr")
	}
	// Closing the gateway's end closes the data socket.
	_ = c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()
	if _, _, err := data.Read(ctx); err == nil || ctx.Err() != nil {
		t.Fatalf("data socket after the gateway closed its connection: %v", err)
	}
}

func TestConnectionLimit(t *testing.T) {
	edge, _, a, ec := enrolled(t)
	for range edgeproto.MaxConnsPerServer {
		a.slots <- struct{}{}
	}
	connID := edgeproto.NewConnID()
	edge.open(ec, connID, edgeproto.KindWeb, "")
	if r := expect[edgeproto.OpenResult](t, ec); r.Error != string(edgeproto.RefusalConnLimit) {
		t.Fatalf("open beyond the limit: %+v", r)
	}
}

func TestWebGrantIsNotReplayable(t *testing.T) {
	edge, _, a, ec := enrolled(t)
	redeem := func(answer func(req edgeproto.WebRedeem)) error {
		done := make(chan error, 1)
		go func() {
			_, err := a.RedeemWebCode(context.Background(), edgeproto.NewToken(), edgeproto.NewVerifier())
			done <- err
		}()
		answer(expect[edgeproto.WebRedeem](t, ec))
		return <-done
	}
	var first edgeproto.WebRedeemResult
	if err := redeem(func(req edgeproto.WebRedeem) {
		now := time.Now()
		first = edgeproto.WebRedeemResult{ID: req.ID, Grant: edge.grant(t, edgeproto.Grant{
			ServerID: a.ServerID(), ConnID: req.ID, Kind: edgeproto.KindWeb,
			Account:  edgeproto.Account{Provider: edgeproto.ProviderGoogle, Subject: "g-7"},
			DeviceID: "browser-1", IssuedAt: now, ExpiresAt: now.Add(time.Minute),
		})}
		ec.send(first)
		// The same answer again finds no request waiting for it.
		ec.send(first)
	}); err != nil {
		t.Fatal(err)
	}
	// Answering a later redemption with the first grant is refused: every
	// redemption names a fresh id.
	err := redeem(func(req edgeproto.WebRedeem) {
		ec.send(edgeproto.WebRedeemResult{ID: req.ID, Grant: first.Grant})
	})
	if !errors.Is(err, edgeproto.ErrGrantConn) {
		t.Fatalf("replayed web grant: %v, want ErrGrantConn", err)
	}
}

func TestRedeemWebCode(t *testing.T) {
	edge, _, a, ec := enrolled(t)
	_, forger, _ := ed25519.GenerateKey(rand.Reader)
	for name, key := range map[string]ed25519.PrivateKey{"edge": edge.priv, "forger": forger} {
		t.Run(name, func(t *testing.T) {
			type result struct {
				g   edgeproto.Grant
				err error
			}
			done := make(chan result, 1)
			code, verifier := edgeproto.NewToken(), edgeproto.NewVerifier()
			go func() {
				g, err := a.RedeemWebCode(context.Background(), code, verifier)
				done <- result{g, err}
			}()
			req := expect[edgeproto.WebRedeem](t, ec)
			if req.Code != code || req.Verifier != verifier {
				t.Fatalf("redeem request %+v", req)
			}
			now := time.Now()
			signed, err := edgeproto.SignGrant(key, edgeproto.Grant{
				ServerID: a.ServerID(), ConnID: req.ID, Kind: edgeproto.KindWeb,
				Account:  edgeproto.Account{Provider: edgeproto.ProviderGoogle, Subject: "g-7"},
				DeviceID: "browser-1", IssuedAt: now, ExpiresAt: now.Add(time.Minute),
			})
			if err != nil {
				t.Fatal(err)
			}
			ec.send(edgeproto.WebRedeemResult{ID: req.ID, Grant: signed})
			r := <-done
			if key.Equal(edge.priv) {
				if r.err != nil || r.g.Account.Subject != "g-7" {
					t.Fatalf("redeem = %+v, %v", r.g, r.err)
				}
				return
			}
			if !errors.Is(r.err, edgeproto.ErrGrantSignature) {
				t.Fatalf("forged web grant: err = %v, want ErrGrantSignature", r.err)
			}
		})
	}
}
