package relay

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
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

// TestUnclaimedRegistrationsPerSite enrolls unclaimed servers from many
// IPv6 /64s of one site: the site's /56 and /48 bound them together.
func TestUnclaimedRegistrationsPerSite(t *testing.T) {
	r := &Relay{servers: map[string]*registration{}, maxUnclaimed: maxUnclaimed}
	admitted := func(format string) int {
		n := 0
		for i := range 100 {
			addr := edgeproto.RateLimitKey(netip.MustParseAddr(fmt.Sprintf(format, i)))
			id := fmt.Sprintf(format, i)
			if r.admitUnclaimedLocked(addr, id) == nil {
				r.servers[id] = &registration{id: id, addr: addr}
				n++
			}
		}
		return n
	}
	perSite := func(p string) int {
		return edgeproto.MaxUnclaimedPerAddress * edgeproto.RateLimitScale(netip.MustParsePrefix(p))
	}
	// 100 /64s of one /56.
	if got, want := admitted("2001:db8:1:%x::1"), perSite("2001:db8:1::/56"); got != want {
		t.Errorf("one /56 enrolled %d unclaimed servers, want %d", got, want)
	}
	// 100 /56s of another /48.
	if got, want := admitted("2001:db8:2:%x00::1"), perSite("2001:db8:2::/48"); got != want {
		t.Errorf("one /48 enrolled %d unclaimed servers, want %d", got, want)
	}
}

func TestUnclaimedRegistrationsEdgeWide(t *testing.T) {
	e := newEnv(t)
	e.r.maxUnclaimed = 2
	for range 2 {
		enroll(t, e, newSigner(t))
	}
	ws := dialControl(t, e)
	send(t, ws, helloFor(t, newSigner(t), e.origin, challenge(t, ws).Nonce))
	if err := waitClosed(t, ws); !strings.Contains(err.Error(), "limit of 2 unclaimed servers") {
		t.Fatalf("unclaimed server over the edge-wide limit: %v", err)
	}
	claimedAgent(t, e)
}

func TestDirectoryStoresArePaced(t *testing.T) {
	e := newEnv(t)
	e.r.directoryInterval = 500 * time.Millisecond
	signer := newSigner(t)
	id := edgeproto.ServerID(signer.PublicKey())
	e.dir.mu.Lock()
	e.dir.owners[id] = true
	e.dir.mu.Unlock()
	a := enroll(t, e, signer)
	push := func(ws *websocket.Conn, login string) {
		send(t, ws, edgeproto.Directory{Entries: []edgeproto.DirectoryEntry{
			{Kind: edgeproto.EntryInvitation, Provider: edgeproto.ProviderGitHub, Login: login, Role: "viewer",
				ExpiresAt: time.Now().Add(time.Hour)},
		}})
	}
	stored := func() (string, int) {
		e.dir.mu.Lock()
		defer e.dir.mu.Unlock()
		login := ""
		if d := e.dir.dirs[id]; len(d) == 1 {
			login = d[0].Login
		}
		return login, e.dir.dirWrites
	}
	storedAs := func(login string) func() bool {
		return func() bool { got, _ := stored(); return got == login }
	}

	push(a.ws, "first")
	eventually(t, "the first push stored at once", storedAs("first"))
	for i := range 20 {
		push(a.ws, fmt.Sprintf("burst-%d", i))
	}
	eventually(t, "the last push of a burst stored", storedAs("burst-19"))
	if _, writes := stored(); writes != 2 {
		t.Fatalf("21 pushes took %d stores, want 2", writes)
	}

	// Reconnecting does not skip the wait.
	again := enroll(t, e, signer)
	push(again.ws, "reconnected")
	time.Sleep(e.r.directoryInterval / 4)
	if got, _ := stored(); got != "burst-19" {
		t.Fatalf("a push right after reconnecting was stored at once: %q", got)
	}
	eventually(t, "the push after reconnecting stored", storedAs("reconnected"))
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
	_, _, err = e.r.Claim(t.Context(), strings.ToUpper(code), acct, dev, e.record)
	var ref edgeproto.Refusal
	if !errors.As(err, &ref) || ref != edgeproto.RefusalClaimWrong || ref.Status() != http.StatusForbidden {
		t.Fatalf("wrong code: %v", err)
	}

	go answer("")
	id, name, err := e.r.Claim(t.Context(), code, acct, dev, e.record)
	if err != nil || id != a.id || name != "devbox" {
		t.Fatalf("claim: %q %q %v", id, name, err)
	}
	if !e.r.Online(a.id) {
		t.Fatal("claimed server is not online")
	}
	if _, _, err := e.r.Claim(t.Context(), code, acct, dev, e.record); !errors.Is(err, edgeproto.RefusalClaimed) {
		t.Fatalf("second claim: %v", err)
	}
}

func TestClaimGoesOnlyToTheServerItNames(t *testing.T) {
	e := newEnv(t)
	a := enroll(t, e, newSigner(t))
	go a.run()
	// The code names a server that is not connected, and a's id shares
	// all but its last character, as a host key ground to collect that
	// server's claim code would.
	named := a.id[:edgeproto.ServerIDLength-1] + "a"
	if named == a.id {
		named = a.id[:edgeproto.ServerIDLength-1] + "b"
	}
	code, err := edgeproto.NewClaimCode(named)
	if err != nil {
		t.Fatal(err)
	}
	acct := account("7")
	dev, _ := e.addDevice(t, acct, "dev-7")
	if _, _, err := e.r.Claim(t.Context(), code, acct, dev, e.record); !errors.Is(err, edgeproto.RefusalNotConnected) {
		t.Fatalf("claim for a server that is not connected: %v, want %q", err, edgeproto.RefusalNotConnected)
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
	_, _, err = e.r.Claim(t.Context(), code, account("1"), dev, e.record)
	var unsettled *ClaimUnsettledError
	if !errors.As(err, &unsettled) || !strings.Contains(err.Error(), "did not answer") {
		t.Fatalf("unanswered claim: %v", err)
	}
	// The server may still accept; it must reconnect to learn it is
	// unclaimed.
	if err = waitClosed(t, a.ws); !strings.Contains(err.Error(), errClaimUnsettled.Error()) {
		t.Fatalf("control channel after an unanswered claim: %v", err)
	}
	other, err := edgeproto.NewClaimCode(edgeproto.ServerID(newSigner(t).PublicKey()))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = e.r.Claim(t.Context(), other, account("1"), dev, e.record); !errors.Is(err, edgeproto.RefusalNotConnected) {
		t.Fatalf("claim for a server that is not connected: %v", err)
	}
}

// answerClaim plays the server accepting the next claim a receives.
func answerClaim(t *testing.T, a *agent) {
	m := next[edgeproto.Claim](t, a)
	if err := writeControl(t.Context(), a.ws, edgeproto.ClaimResult{ID: m.ID}); err != nil {
		t.Error(err)
	}
}

func TestClaimNotRecorded(t *testing.T) {
	e := newEnv(t)
	signer := newSigner(t)
	a := enroll(t, e, signer)
	go a.run()
	acct := account("7")
	dev, _ := e.addDevice(t, acct, "dev-7")
	code, err := edgeproto.NewClaimCode(a.id)
	if err != nil {
		t.Fatal(err)
	}

	go answerClaim(t, a)
	failing := func(context.Context, string, string) error { return errors.New("disk I/O error") }
	_, _, err = e.r.Claim(t.Context(), code, acct, dev, failing)
	var unsettled *ClaimUnsettledError
	if !errors.As(err, &unsettled) || unsettled.ServerID != a.id ||
		!strings.Contains(err.Error(), "could not record you as its owner: disk I/O error") ||
		!strings.Contains(err.Error(), "aether-server edge claim-code") {
		t.Fatalf("claim the edge could not record: %v", err)
	}
	if e.r.Online(a.id) {
		t.Fatal("server reported claimed with no owner recorded")
	}
	// The server accepted: it holds an owner the edge does not. The
	// relay disconnects it so that it reconnects, reads that it is
	// unclaimed, and drops that owner.
	eventually(t, "server disconnected", func() bool { return e.r.Metrics().UnclaimedServers == 0 })

	again := enroll(t, e, signer)
	if again.ready.State != edgeproto.StateUnclaimed {
		t.Fatalf("reconnected server told %q, want unclaimed", again.ready.State)
	}
	go again.run()
	fresh, err := edgeproto.NewClaimCode(a.id)
	if err != nil {
		t.Fatal(err)
	}
	go answerClaim(t, again)
	if id, _, err := e.r.Claim(t.Context(), fresh, acct, dev, e.record); err != nil || id != a.id {
		t.Fatalf("claim with a fresh code: %q %v", id, err)
	}
	if !e.r.Online(a.id) || !e.owned(a.id) {
		t.Fatal("second claim not recorded")
	}
}

func TestClaimRecordedAfterReconnect(t *testing.T) {
	e := newEnv(t)
	signer := newSigner(t)
	a := enroll(t, e, signer)
	go a.run()
	acct := account("7")
	dev, _ := e.addDevice(t, acct, "dev-7")
	code, err := edgeproto.NewClaimCode(a.id)
	if err != nil {
		t.Fatal(err)
	}
	go answerClaim(t, a)
	// The server reconnects between accepting and the edge recording
	// the owner, so its new registration was told it is unclaimed.
	var between *agent
	record := func(ctx context.Context, id, name string) error {
		between = enroll(t, e, signer)
		return e.record(ctx, id, name)
	}
	if _, _, err := e.r.Claim(t.Context(), code, acct, dev, record); err != nil {
		t.Fatal(err)
	}
	if err := waitClosed(t, between.ws); !strings.Contains(err.Error(), errClaimRecorded.Error()) {
		t.Fatalf("registration enrolled before the owner was recorded: %v", err)
	}
	if after := enroll(t, e, signer); after.ready.State != edgeproto.StateClaimed {
		t.Fatalf("reconnected server told %q, want claimed", after.ready.State)
	}
}

func TestBlockedServerCannotEnroll(t *testing.T) {
	e := newEnv(t)
	signer := newSigner(t)
	e.dir.mu.Lock()
	e.dir.blocked[edgeproto.ServerID(signer.PublicKey())] = true
	e.dir.mu.Unlock()
	ws := dialControl(t, e)
	c := challenge(t, ws)
	send(t, ws, helloFor(t, signer, e.origin, c.Nonce))
	if err := waitClosed(t, ws); !strings.Contains(err.Error(), string(edgeproto.RefusalServerBlocked)) {
		t.Fatalf("blocked server enrolled: %v", err)
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
