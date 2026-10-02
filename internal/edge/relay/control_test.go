package relay

import (
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
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

	status, text := e.get(t, a.id, bearerHeader(token))
	if status != http.StatusServiceUnavailable || text != string(edgeproto.RefusalNotConnected) {
		t.Fatalf("ssh connection to an unclaimed server: %d %q", status, text)
	}
	// A server asserting its own owner, with no claim connection behind
	// it, is refused and stays unclaimed.
	send(t, a.ws, edgeproto.Claimed{ConnID: edgeproto.NewConnID(), Owner: edgeproto.AccountPrincipal(acct)})
	if err := a.closedWith(t); !strings.Contains(err.Error(), "ownership report refused: connection") {
		t.Fatalf("self-asserted owner: %v", err)
	}
	if _, owned := e.dir.owner(a.id); owned || e.r.Online(a.id) {
		t.Fatal("a server named its own owner")
	}
}

func TestClaimConnection(t *testing.T) {
	e := newEnv(t)
	a := enroll(t, e, newSigner(t))
	a.attach = true
	go a.run()
	acct := account("7")
	dev, token := e.addDevice(t, acct, "dev-7")

	client, server := e.connectPath(t, a, edgeproto.ClaimConnectPath(a.id), token)
	open := next[edgeproto.Open](t, a)
	g, err := edgeproto.VerifyGrant(a.ready.EdgeKey, open.Grant, edgeproto.GrantScope{
		Issuer: e.origin, ServerID: a.id, ConnID: open.ConnID, Kind: edgeproto.KindClaim}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if open.Kind != edgeproto.KindClaim || g.Account != acct || g.DeviceKey != dev.Key {
		t.Fatalf("open %+v with grant %+v", open, g)
	}
	roundTrip(t, client, server, "claim connection")
	if addrs := e.dir.claimAddrs; len(addrs) != 1 || !addrs[0].IsLoopback() {
		t.Fatalf("claim admitted for addresses %v", addrs)
	}

	// The server accepts the code: it pushes its directory, then reports
	// the claim. The directory waits for the claim to be recorded.
	send(t, a.ws, edgeproto.Directory{Entries: []edgeproto.DirectoryEntry{
		{Kind: edgeproto.EntryMember, Provider: acct.Provider, Subject: acct.Subject, Role: "admin"},
	}})
	send(t, a.ws, edgeproto.Claimed{ConnID: open.ConnID, Owner: edgeproto.AccountPrincipal(acct)})
	eventually(t, "claim recorded", func() bool { return e.r.Online(a.id) })
	if owner, ok := e.dir.owner(a.id); !ok || owner != edgeproto.AccountPrincipal(acct) {
		t.Fatalf("owner %+v", owner)
	}
	eventually(t, "directory stored", func() bool {
		e.dir.mu.Lock()
		defer e.dir.mu.Unlock()
		return len(e.dir.dirs[a.id]) == 1 && e.dir.policies[a.id] == edgeproto.PolicyAccount
	})
	e.dir.addMember(a.id, acct)
	e.connect(t, a, token)

	// A report is good once.
	send(t, a.ws, edgeproto.Claimed{ConnID: open.ConnID, Owner: edgeproto.AccountPrincipal(acct)})
	if err := a.closedWith(t); !strings.Contains(err.Error(), "ownership report refused") {
		t.Fatalf("repeated report: %v", err)
	}
}

// The server may report a claim before it pushes the directory that names
// the claimant. The directory it pushed before the claim, stored once the
// claim is recorded, does not end the claim connection.
func TestClaimConnectionOutlivesAnOlderDirectory(t *testing.T) {
	e := newEnv(t)
	a := enroll(t, e, newSigner(t))
	a.attach = true
	go a.run()
	acct := account("8")
	_, token := e.addDevice(t, acct, "dev-8")
	send(t, a.ws, edgeproto.Directory{})

	client, server := e.connectPath(t, a, edgeproto.ClaimConnectPath(a.id), token)
	open := next[edgeproto.Open](t, a)
	send(t, a.ws, edgeproto.Claimed{ConnID: open.ConnID, Owner: edgeproto.AccountPrincipal(acct)})
	eventually(t, "claim recorded", func() bool { return e.r.Online(a.id) })
	eventually(t, "the older directory stored", func() bool {
		e.dir.mu.Lock()
		defer e.dir.mu.Unlock()
		return e.dir.dirWrites == 1
	})
	roundTrip(t, client, server, "claim connection after the older directory was stored")
}

func TestClaimConnectionRefusals(t *testing.T) {
	e := newEnv(t)
	a := enroll(t, e, newSigner(t))
	go a.run()
	owned := claimedAgent(t, e)
	_, token := e.addDevice(t, account("7"), "dev-7")
	offline := edgeproto.ServerID(newSigner(t).PublicKey())

	for _, tt := range []struct {
		name     string
		serverID string
		header   http.Header
		refusal  error
		want     edgeproto.Refusal
	}{
		{"not signed in", a.id, http.Header{}, nil, edgeproto.RefusalTokenRequired},
		{"revoked token", a.id, bearerHeader(edgeproto.NewToken()), nil, edgeproto.RefusalTokenRevoked},
		{"server has an owner", owned.id, bearerHeader(token), nil, edgeproto.RefusalClaimed},
		{"over a claim limit", a.id, bearerHeader(token), edgeproto.RefusalTooMany, edgeproto.RefusalTooMany},
		{"server not connected", offline, bearerHeader(token), nil, edgeproto.RefusalNotConnected},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e.dir.mu.Lock()
			e.dir.claimRefusal = tt.refusal
			e.dir.mu.Unlock()
			status, text := e.getPath(t, edgeproto.ClaimConnectPath(tt.serverID), tt.header)
			if status != tt.want.Status() || text != string(tt.want) {
				t.Fatalf("got %d %q, want %d %q", status, text, tt.want.Status(), tt.want)
			}
		})
	}
	select {
	case m := <-a.msgs:
		t.Fatalf("the server received %T for a refused claim", m)
	default:
	}
}

// claimOpen opens a claim connection to a for acct and returns its open.
func claimOpen(t *testing.T, e *env, a *agent, acct edgeproto.Account) edgeproto.Open {
	t.Helper()
	_, token := e.addDevice(t, acct, "dev-"+acct.Subject)
	e.connectPath(t, a, edgeproto.ClaimConnectPath(a.id), token)
	return next[edgeproto.Open](t, a)
}

func TestClaimReportNeedsTheServersOwnClaimConnection(t *testing.T) {
	e := newEnv(t)
	a := enroll(t, e, newSigner(t))
	a.attach = true
	go a.run()
	b := enroll(t, e, newSigner(t))
	b.attach = true
	go b.run()
	claimant := account("7")
	open := claimOpen(t, e, a, claimant)

	// b reports a's claim connection.
	send(t, b.ws, edgeproto.Claimed{ConnID: open.ConnID, Owner: edgeproto.AccountPrincipal(claimant)})
	if err := b.closedWith(t); !strings.Contains(err.Error(), "is not to this server") {
		t.Fatalf("report of another server's claim: %v", err)
	}
	// a names an owner other than the connection's account.
	send(t, a.ws, edgeproto.Claimed{ConnID: open.ConnID, Owner: edgeproto.AccountPrincipal(account("8"))})
	if err := a.closedWith(t); !strings.Contains(err.Error(), "is not the account connection") {
		t.Fatalf("report naming another owner: %v", err)
	}
	for _, id := range []string{a.id, b.id} {
		if _, owned := e.dir.owner(id); owned {
			t.Fatalf("server %s owned after refused reports", id)
		}
	}
}

func TestClaimReportNotRecorded(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
		want string
	}{
		{"refused", edgeproto.RefusalAccountBlocked, "ownership report refused: " + string(edgeproto.RefusalAccountBlocked)},
		{"failed", errors.New("disk I/O error at /var/lib/aether-edge/edge.db"), "ownership report refused: internal error"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			signer := newSigner(t)
			a := enroll(t, e, signer)
			a.attach = true
			go a.run()
			acct := account("7")
			open := claimOpen(t, e, a, acct)
			e.dir.mu.Lock()
			e.dir.recordErr = tt.err
			e.dir.mu.Unlock()
			send(t, a.ws, edgeproto.Claimed{ConnID: open.ConnID, Owner: edgeproto.AccountPrincipal(acct)})
			if err := a.closedWith(t); !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("closed with %v, want %q", err, tt.want)
			}
			if again := enroll(t, e, signer); again.ready.State != edgeproto.StateUnclaimed || e.r.Online(a.id) {
				t.Fatalf("server told %q after an unrecorded claim", again.ready.State)
			}
		})
	}
}

func TestOwnershipReports(t *testing.T) {
	e := newEnv(t)
	a := claimedAgent(t, e)
	prev := edgeproto.AccountPrincipal(account("owner"))
	heir := edgeproto.Principal{Type: edgeproto.PrincipalAccount, Provider: edgeproto.ProviderGitHub, Subject: "9"}

	// A refused transfer is answered, so the server keeps its previous
	// owner as the edge does, and the control channel stays up.
	e.dir.mu.Lock()
	e.dir.recordErr = edgeproto.Refusal("github:9 has no account at this edge")
	e.dir.mu.Unlock()
	refused := edgeproto.NewConnID()
	send(t, a.ws, edgeproto.OwnerTransferred{ID: refused, Owner: heir})
	if got := next[edgeproto.OwnerTransferResult](t, a); got.ID != refused || got.Owner != heir ||
		!strings.Contains(got.Error, "ownership report refused: github:9 has no account") {
		t.Fatalf("refused transfer answered %+v", got)
	}
	if p, ok := e.dir.owner(a.id); !ok || p != prev || !e.r.Online(a.id) {
		t.Fatalf("after a refused transfer: owner %+v, online %v; want %+v, online", p, e.r.Online(a.id), prev)
	}

	e.dir.mu.Lock()
	e.dir.recordErr = nil
	e.dir.mu.Unlock()
	recorded := edgeproto.NewConnID()
	send(t, a.ws, edgeproto.OwnerTransferred{ID: recorded, Owner: heir})
	if got := next[edgeproto.OwnerTransferResult](t, a); got != (edgeproto.OwnerTransferResult{ID: recorded, Owner: heir}) {
		t.Fatalf("transfer answered %+v, want it recorded", got)
	}
	if p, ok := e.dir.owner(a.id); !ok || p != heir {
		t.Fatalf("owner after the transfer: %+v", p)
	}
	send(t, a.ws, edgeproto.Ownerless{})
	eventually(t, "owner dropped", func() bool { _, ok := e.dir.owner(a.id); return !ok })
	if !e.r.Online(a.id) {
		t.Fatal("an ownerless server stopped relaying for its members")
	}

	u := enroll(t, e, newSigner(t))
	go u.run()
	send(t, u.ws, edgeproto.Ownerless{})
	send(t, u.ws, edgeproto.OwnerTransferred{ID: edgeproto.NewConnID(), Owner: heir})
	if got := next[edgeproto.OwnerTransferResult](t, u); !strings.Contains(got.Error, "never claimed at this edge") {
		t.Fatalf("transfer by an unclaimed server answered %+v", got)
	}
	if _, ok := e.dir.owner(u.id); ok {
		t.Fatal("an unclaimed server recorded an owner")
	}
}

func TestAccountDeletionsAreDelivered(t *testing.T) {
	e := newEnv(t)
	signer := newSigner(t)
	id := edgeproto.ServerID(signer.PublicKey())
	gone := account("1")
	deleted := edgeproto.AccountDeleted{Provider: gone.Provider, Subject: gone.Subject}
	e.dir.mu.Lock()
	e.dir.owners[id] = true
	e.dir.pending[id] = []edgeproto.AccountDeleted{deleted}
	e.dir.mu.Unlock()

	// Owed while the server was offline: sent when it enrolls, and again
	// at each enrollment until the server answers that it applied it.
	a := enroll(t, e, signer)
	go a.run()
	if got := next[edgeproto.AccountDeleted](t, a); got != deleted {
		t.Fatalf("delivered %+v", got)
	}
	a = enroll(t, e, signer)
	a.attach = true
	go a.run()
	if got := next[edgeproto.AccountDeleted](t, a); got != deleted {
		t.Fatalf("sent again %+v", got)
	}
	send(t, a.ws, edgeproto.AccountDeletionApplied(deleted))
	eventually(t, "applied deletion forgotten", func() bool {
		owed, _ := e.dir.PendingDeletions(t.Context(), id)
		return len(owed) == 0
	})

	// Deleted while it is online: sent at once, and the account's
	// connections close.
	stays := account("2")
	e.dir.addMember(id, gone)
	e.dir.addMember(id, stays)
	_, goneToken := e.addDevice(t, gone, "gone-laptop")
	_, staysToken := e.addDevice(t, stays, "stays-laptop")
	goneConn, _ := e.connect(t, a, goneToken)
	staysConn, staysServer := e.connect(t, a, staysToken)
	e.dir.mu.Lock()
	e.dir.pending[id] = []edgeproto.AccountDeleted{deleted}
	e.dir.mu.Unlock()
	e.r.AccountDeleted(gone, []string{id, edgeproto.ServerID(newSigner(t).PublicKey())})
	expectClosed(t, goneConn)
	if got := next[edgeproto.AccountDeleted](t, a); got != deleted {
		t.Fatalf("delivered %+v", got)
	}
	roundTrip(t, staysConn, staysServer, "other accounts stay connected")
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
	if err := waitClosed(t, a.ws); !strings.Contains(err.Error(), "server silent for 150ms") {
		t.Fatalf("closed with %v", err)
	}
	if time.Since(start) < 100*time.Millisecond {
		t.Fatal("closed before the idle timeout")
	}
	eventually(t, "registration dropped", func() bool { return e.r.Metrics().UnclaimedServers == 0 })
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
	if err := a.closedWith(t); !strings.Contains(err.Error(), errUnenrolled.Error()) {
		t.Fatalf("closed with %v", err)
	}
}

func TestShutdownDrains(t *testing.T) {
	e := newEnv(t)
	a := claimedAgent(t, e)
	if err := e.r.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	next[edgeproto.Drain](t, a)
	if err := a.closedWith(t); !strings.Contains(err.Error(), errDraining.Error()) {
		t.Fatalf("closed with %v", err)
	}
	_, resp, err := websocket.Dial(t.Context(), e.wsBase+edgeproto.PathServerControl, nil)
	if err == nil || resp == nil || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("enrollment while draining: %v", err)
	}
}
