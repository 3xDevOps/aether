package relay

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/edgeproto"
	"github.com/coder/websocket"
)

func TestEnrollmentReady(t *testing.T) {
	e := newEnv(t)
	signer := newSigner(t)
	a := enroll(t, e, signer)
	if want := edgeproto.ServerID(signer.PublicKey()); a.ready.ServerID != want {
		t.Fatalf("ready names %s, want %s", a.ready.ServerID, want)
	}
	if a.ready.State != edgeproto.StateUnclaimed {
		t.Fatalf("state %q, want unclaimed", a.ready.State)
	}
	if !a.ready.EdgeKey.Equal(e.edgeKey.Public()) {
		t.Fatal("ready carries another edge key")
	}
	if e.r.Online(a.id) {
		t.Fatal("unclaimed server reported online")
	}

	c := claimedAgent(t, e)
	if c.ready.State != edgeproto.StateClaimed || !e.r.Online(c.id) {
		t.Fatalf("claimed server: state %q online %v", c.ready.State, e.r.Online(c.id))
	}
}

func TestEnrollmentRefused(t *testing.T) {
	e := newEnv(t)
	tests := []struct {
		name  string
		hello func(t *testing.T, c edgeproto.Challenge) edgeproto.Hello
	}{
		{"signature by another key", func(t *testing.T, c edgeproto.Challenge) edgeproto.Hello {
			h := helloFor(t, newSigner(t), e.origin, c.Nonce)
			h.HostKey = newSigner(t).PublicKey().Marshal()
			return h
		}},
		{"signed for another edge", func(t *testing.T, c edgeproto.Challenge) edgeproto.Hello {
			return helloFor(t, newSigner(t), "https://edge.example.test", c.Nonce)
		}},
		{"signed over another nonce", func(t *testing.T, c edgeproto.Challenge) edgeproto.Hello {
			nonce := append([]byte(nil), c.Nonce...)
			nonce[0] ^= 1
			return helloFor(t, newSigner(t), e.origin, nonce)
		}},
		{"version below minimum", func(t *testing.T, c edgeproto.Challenge) edgeproto.Hello {
			h := helloFor(t, newSigner(t), e.origin, c.Nonce)
			h.Version = edgeproto.MinVersion - 1
			return h
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := dialControl(t, e)
			send(t, ws, tt.hello(t, challenge(t, ws)))
			err := waitClosed(t, ws)
			if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
				t.Fatalf("closed with %v, want policy violation", err)
			}
		})
	}
	if got := e.r.Metrics().Refusals["enrollment refused"]; got != uint64(len(tests)) {
		t.Fatalf("enrollment refusals %d, want %d", got, len(tests))
	}
	if m := e.r.Metrics(); m.Servers+m.UnclaimedServers != 0 {
		t.Fatalf("refused servers registered: %+v", m)
	}
}

func TestEnrollmentReplayedHelloRefused(t *testing.T) {
	e := newEnv(t)
	signer := newSigner(t)
	first := dialControl(t, e)
	hello := helloFor(t, signer, e.origin, challenge(t, first).Nonce)
	send(t, first, hello)
	if _, err := recv(t.Context(), first); err != nil {
		t.Fatal(err)
	}

	second := dialControl(t, e)
	c := challenge(t, second)
	send(t, second, hello)
	err := waitClosed(t, second)
	if websocket.CloseStatus(err) != websocket.StatusPolicyViolation || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("replayed hello: %v, want a signature refusal", err)
	}
	if len(c.Nonce) != edgeproto.NonceSize {
		t.Fatalf("nonce is %d bytes", len(c.Nonce))
	}
}

func TestNewerRegistrationReplacesOlder(t *testing.T) {
	e := newEnv(t)
	signer := newSigner(t)
	old := enroll(t, e, signer)
	enroll(t, e, signer)
	err := waitClosed(t, old.ws)
	if !strings.Contains(err.Error(), errReplaced.Error()) {
		t.Fatalf("older registration closed with %v", err)
	}
	if m := e.r.Metrics(); m.UnclaimedServers != 1 {
		t.Fatalf("%d unclaimed registrations, want 1", m.UnclaimedServers)
	}
}

func TestUnclaimedServerIsNotRelayed(t *testing.T) {
	e := newEnv(t)
	a := enroll(t, e, newSigner(t))
	go a.run()
	acct := account("1")
	e.dir.addMember(a.id, acct)
	_, token := e.addDevice(t, acct, "dev-1")

	// A server asserting its own owner stays unclaimed.
	send(t, a.ws, edgeproto.Claimed{Owner: acct})
	status, text := e.get(t, a.id, bearerHeader(token))
	if status != http.StatusServiceUnavailable || text != string(edgeproto.RefusalNotConnected) {
		t.Fatalf("connect to unclaimed server: %d %q", status, text)
	}
	if e.r.Online(a.id) {
		t.Fatal("unclaimed server online after asserting an owner")
	}
}

func TestUnclaimedRegistrationsPerAddress(t *testing.T) {
	e := newEnv(t)
	for range edgeproto.MaxUnclaimedPerAddress {
		enroll(t, e, newSigner(t))
	}
	ws := dialControl(t, e)
	send(t, ws, helloFor(t, newSigner(t), e.origin, challenge(t, ws).Nonce))
	if err := waitClosed(t, ws); !strings.Contains(err.Error(), "unclaimed servers are already connected") {
		t.Fatalf("fourth unclaimed server: %v", err)
	}
	// A claimed server from the same address is not limited.
	claimedAgent(t, e)
}

func TestUnclaimedRegistrationExpires(t *testing.T) {
	e := newEnv(t)
	e.r.unclaimedTTL = 100 * time.Millisecond
	a := enroll(t, e, newSigner(t))
	if err := waitClosed(t, a.ws); !strings.Contains(err.Error(), errUnclaimed.Error()) {
		t.Fatalf("closed with %v", err)
	}
	eventually(t, "registration dropped", func() bool { return e.r.Metrics().UnclaimedServers == 0 })
}

func TestIdleControlChannelCloses(t *testing.T) {
	e := newEnv(t)
	e.r.idleTimeout = 150 * time.Millisecond
	e.r.pingInterval = 50 * time.Millisecond
	a := enroll(t, e, newSigner(t))
	// Reading pings without answering them is silence.
	start := time.Now()
	if err := waitClosed(t, a.ws); err == nil {
		t.Fatal("no error")
	}
	if time.Since(start) < 100*time.Millisecond {
		t.Fatal("closed before the idle timeout")
	}
	eventually(t, "registration dropped", func() bool { return e.r.Metrics().UnclaimedServers == 0 })
}

func TestClaim(t *testing.T) {
	e := newEnv(t)
	a := enroll(t, e, newSigner(t))
	go a.run()
	acct := account("7")
	dev, _ := e.addDevice(t, acct, "dev-7")
	code, err := edgeproto.NewClaimCode(a.id)
	if err != nil {
		t.Fatal(err)
	}

	answer := func(errText string) {
		m := next[edgeproto.Claim](t, a)
		g, verr := edgeproto.VerifyGrant(a.ready.EdgeKey, m.Grant, edgeproto.GrantScope{ServerID: a.id, ConnID: m.ID, Kind: edgeproto.KindClaim}, time.Now())
		if verr != nil {
			t.Error(verr)
		}
		if g.Account != acct || g.DeviceKey != dev.Key || m.Code != code {
			t.Errorf("claim carries %+v code %q", g, m.Code)
		}
		if werr := writeControl(t.Context(), a.ws, edgeproto.ClaimResult{ID: m.ID, Error: errText}); werr != nil {
			t.Error(werr)
		}
	}

	go answer(string(edgeproto.RefusalClaimWrong))
	_, _, err = e.r.Claim(t.Context(), strings.ToUpper(code), acct, dev)
	var ref edgeproto.Refusal
	if !errors.As(err, &ref) || ref != edgeproto.RefusalClaimWrong || ref.Status() != http.StatusForbidden {
		t.Fatalf("wrong code: %v", err)
	}

	go answer("")
	id, name, err := e.r.Claim(t.Context(), code, acct, dev)
	if err != nil || id != a.id || name != "devbox" {
		t.Fatalf("claim: %q %q %v", id, name, err)
	}
	if !e.r.Online(a.id) {
		t.Fatal("claimed server is not online")
	}
	if _, _, err := e.r.Claim(t.Context(), code, acct, dev); !errors.Is(err, edgeproto.RefusalClaimed) {
		t.Fatalf("second claim: %v", err)
	}
}

func TestClaimRefusesAmbiguousPrefix(t *testing.T) {
	e := newEnv(t)
	a := enroll(t, e, newSigner(t))
	go a.run()
	// A second registration whose id shares the claim prefix, as a host
	// key ground to collect another server's claim code would have.
	twin := &registration{id: a.id[:edgeproto.ClaimPrefixLength] +
		strings.Repeat("a", edgeproto.ServerIDLength-edgeproto.ClaimPrefixLength)}
	twin.ctx, twin.cancel = context.WithCancelCause(t.Context())
	e.r.mu.Lock()
	e.r.servers[twin.id] = twin
	e.r.mu.Unlock()
	defer func() {
		e.r.mu.Lock()
		delete(e.r.servers, twin.id)
		e.r.mu.Unlock()
	}()
	code, err := edgeproto.NewClaimCode(a.id)
	if err != nil {
		t.Fatal(err)
	}
	acct := account("7")
	dev, _ := e.addDevice(t, acct, "dev-7")
	if _, _, err := e.r.Claim(t.Context(), code, acct, dev); !errors.Is(err, edgeproto.RefusalNotConnected) {
		t.Fatalf("claim with two matching servers: %v, want %q", err, edgeproto.RefusalNotConnected)
	}
	e.r.mu.Lock()
	pending := len(e.r.claims)
	e.r.mu.Unlock()
	if pending != 0 {
		t.Errorf("%d claims were forwarded, want none", pending)
	}
}

func TestClaimNotAnswered(t *testing.T) {
	e := newEnv(t)
	e.r.attachDeadline = 100 * time.Millisecond
	a := enroll(t, e, newSigner(t))
	code, err := edgeproto.NewClaimCode(a.id)
	if err != nil {
		t.Fatal(err)
	}
	dev := edgeproto.Device{ID: "browser-1", Label: "browser"}
	if _, _, err = e.r.Claim(t.Context(), code, account("1"), dev); err == nil || !strings.Contains(err.Error(), "did not answer") {
		t.Fatalf("unanswered claim: %v", err)
	}
	other, err := edgeproto.NewClaimCode(edgeproto.ServerID(newSigner(t).PublicKey()))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = e.r.Claim(t.Context(), other, account("1"), dev); !errors.Is(err, edgeproto.RefusalNotConnected) {
		t.Fatalf("claim for a server that is not connected: %v", err)
	}
}

func TestWebRedeem(t *testing.T) {
	e := newEnv(t)
	a := claimedAgent(t, e)
	code := edgeproto.NewToken()
	e.dir.mu.Lock()
	e.dir.webCodes[a.id+" "+code] = "signed-web-grant"
	e.dir.mu.Unlock()

	id := edgeproto.NewConnID()
	send(t, a.ws, edgeproto.WebRedeem{ID: id, Code: code, Verifier: edgeproto.NewVerifier()})
	if got := next[edgeproto.WebRedeemResult](t, a); got.ID != id || got.Grant != "signed-web-grant" {
		t.Fatalf("redeem: %+v", got)
	}
	send(t, a.ws, edgeproto.WebRedeem{ID: id, Code: edgeproto.NewToken(), Verifier: edgeproto.NewVerifier()})
	if got := next[edgeproto.WebRedeemResult](t, a); got.Error != "web sign-in code is not valid" {
		t.Fatalf("unknown code: %+v", got)
	}
}

func TestServerLeaves(t *testing.T) {
	e := newEnv(t)
	a := claimedAgent(t, e)
	send(t, a.ws, edgeproto.Unenroll{})
	eventually(t, "server forgotten", func() bool {
		e.dir.mu.Lock()
		defer e.dir.mu.Unlock()
		return len(e.dir.unenrolled) == 1 && e.dir.unenrolled[0] == a.id
	})
	eventually(t, "server offline", func() bool { return !e.r.Online(a.id) })
}

func TestShutdownDrains(t *testing.T) {
	e := newEnv(t)
	a := claimedAgent(t, e)
	if err := e.r.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	next[edgeproto.Drain](t, a)
	_, resp, err := websocket.Dial(t.Context(), e.wsBase+edgeproto.PathServerControl, nil)
	if err == nil || resp == nil || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("enrollment while draining: %v", err)
	}
}
