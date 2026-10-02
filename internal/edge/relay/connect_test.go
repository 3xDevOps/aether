package relay

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/coder/websocket"
)

func TestConnectRefusals(t *testing.T) {
	e := newEnv(t)
	a := claimedAgent(t, e)
	member := account("1")
	e.dir.addMember(a.id, member)
	_, memberToken := e.addDevice(t, member, "dev-1")
	_, strangerToken := e.addDevice(t, account("2"), "dev-2")
	browserToken := edgeproto.NewToken()
	e.dir.mu.Lock()
	e.dir.tokens[browserToken] = tokenEntry{account: member, device: edgeproto.Device{ID: "browser-1", Label: "browser"}}
	e.dir.mu.Unlock()
	offline := edgeproto.ServerID(newSigner(t).PublicKey())
	e.dir.addMember(offline, member)

	tests := []struct {
		name     string
		serverID string
		header   http.Header
		want     edgeproto.Refusal
	}{
		{"no token", a.id, http.Header{}, edgeproto.RefusalTokenRequired},
		{"malformed token", a.id, bearerHeader("not-a-token"), edgeproto.RefusalTokenRevoked},
		{"unknown token", a.id, bearerHeader(edgeproto.NewToken()), edgeproto.RefusalTokenRevoked},
		{"browser token", a.id, bearerHeader(browserToken), edgeproto.RefusalTokenRequired},
		{"malformed server id", "NOT-A-SERVER-ID", bearerHeader(memberToken), edgeproto.RefusalUnknownServer},
		{"server with no owner", edgeproto.ServerID(newSigner(t).PublicKey()), bearerHeader(memberToken), edgeproto.RefusalUnknownServer},
		{"not a member", a.id, bearerHeader(strangerToken), edgeproto.RefusalNotMember},
		{"server not connected", offline, bearerHeader(memberToken), edgeproto.RefusalNotConnected},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, text := e.get(t, tt.serverID, tt.header)
			if status != tt.want.Status() || text != string(tt.want) {
				t.Fatalf("got %d %q, want %d %q", status, text, tt.want.Status(), tt.want)
			}
		})
	}
	if got := e.r.Metrics().Refusals[string(edgeproto.RefusalTokenRevoked)]; got != 2 {
		t.Fatalf("counted %d token refusals, want 2", got)
	}

	t.Run("version below minimum", func(t *testing.T) {
		h := bearerHeader(memberToken)
		h.Set(edgeproto.HeaderVersion, "0")
		status, text := e.get(t, a.id, h)
		if status != http.StatusUpgradeRequired || !strings.Contains(text, "upgrade required: 1") {
			t.Fatalf("got %d %q", status, text)
		}
	})
}

func TestConnectServerRefusal(t *testing.T) {
	e := newEnv(t)
	a := claimedAgent(t, e)
	a.attach = false
	acct := account("1")
	e.dir.addMember(a.id, acct)
	_, token := e.addDevice(t, acct, "dev-1")
	go func() {
		o := next[edgeproto.Open](t, a)
		if err := writeControl(t.Context(), a.ws, edgeproto.OpenResult{ConnID: o.ConnID, Error: "device dev-1 is pending approval"}); err != nil {
			t.Error(err)
		}
	}()
	status, text := e.get(t, a.id, bearerHeader(token))
	if status != http.StatusForbidden || text != "device dev-1 is pending approval" {
		t.Fatalf("got %d %q", status, text)
	}
}

func TestAttachDeadline(t *testing.T) {
	e := newEnv(t)
	e.r.attachDeadline = 150 * time.Millisecond
	a := claimedAgent(t, e)
	a.attach = false
	acct := account("1")
	e.dir.addMember(a.id, acct)
	_, token := e.addDevice(t, acct, "dev-1")
	start := time.Now()
	status, text := e.get(t, a.id, bearerHeader(token))
	if status != http.StatusGatewayTimeout || text != string(edgeproto.RefusalNotAttached) {
		t.Fatalf("got %d %q", status, text)
	}
	if time.Since(start) < e.r.attachDeadline {
		t.Fatal("refused before the attach deadline")
	}
	eventually(t, "pending connection dropped", func() bool {
		e.r.mu.Lock()
		defer e.r.mu.Unlock()
		return len(e.r.conns) == 0
	})
}

func TestSpliceCarriesDataAndCloses(t *testing.T) {
	e := newEnv(t)
	a := claimedAgent(t, e)
	acct := account("1")
	e.dir.addMember(a.id, acct)
	dev, token := e.addDevice(t, acct, "dev-1")

	client, server := e.connect(t, a, token)
	open := next[edgeproto.Open](t, a)
	g, err := edgeproto.VerifyGrant(a.ready.EdgeKey, open.Grant, edgeproto.GrantScope{
		Issuer: e.origin, ServerID: a.id, ConnID: open.ConnID, Kind: edgeproto.KindSSH}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if g.Account != acct || g.DeviceID != dev.ID || g.DeviceKey != dev.Key {
		t.Fatalf("grant %+v", g)
	}
	if !strings.HasPrefix(open.ClientAddr, "127.0.0.1:") {
		t.Fatalf("client address %q", open.ClientAddr)
	}

	roundTrip(t, client, server, "SSH-2.0-client\r\n")
	roundTrip(t, server, client, "SSH-2.0-server\r\n")
	if m := e.r.Metrics(); m.Splices != 1 || m.BytesRelayed == 0 {
		t.Fatalf("metrics %+v", m)
	}
	_ = client.Close()
	expectClosed(t, server)
	eventually(t, "splice ended", func() bool { return e.r.Metrics().Splices == 0 })

	client, server = e.connect(t, a, token)
	_ = server.Close()
	expectClosed(t, client)
}

func roundTrip(t *testing.T, from, to net.Conn, msg string) {
	t.Helper()
	if _, err := from.Write([]byte(msg)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len(msg))
	_ = to.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(to, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != msg {
		t.Fatalf("read %q, want %q", buf, msg)
	}
}

func expectClosed(t *testing.T, c net.Conn) {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, err := c.Read(make([]byte, 1))
	if err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("connection still open: %v", err)
	}
}

func TestTicketIsSingleUse(t *testing.T) {
	e := newEnv(t)
	a := claimedAgent(t, e)
	a.attach = false
	acct := account("1")
	e.dir.addMember(a.id, acct)
	_, token := e.addDevice(t, acct, "dev-1")

	type result struct {
		status int
		text   string
	}
	done := make(chan result, 1)
	go func() {
		status, text := e.get(t, a.id, bearerHeader(token))
		done <- result{status, text}
	}()
	o := next[edgeproto.Open](t, a)
	if _, err := a.dialData(o.ConnID, edgeproto.NewToken()); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("wrong ticket: %v", err)
	}
	// The wrong guess spent the ticket.
	if _, err := a.dialData(o.ConnID, o.Ticket); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("ticket after a wrong guess: %v", err)
	}
	if r := <-done; r.status != http.StatusGatewayTimeout || r.text != string(edgeproto.RefusalNotAttached) {
		t.Fatalf("client got %+v", r)
	}

	a.attach = true
	e.connect(t, a, token)
	used := next[edgeproto.Open](t, a)
	if _, err := a.dialData(used.ConnID, used.Ticket); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("reused ticket: %v", err)
	}
}

func TestSpliceBackpressure(t *testing.T) {
	r := &Relay{conns: map[string]*relayConn{}, throttleRate: throttledRate}
	client, clientPeer := net.Pipe()
	server, serverPeer := net.Pipe()
	c := &relayConn{id: "c"}
	c.ctx, c.cancel = context.WithCancelCause(context.Background())
	spliced := make(chan struct{})
	go func() {
		r.splice(c, client, server)
		close(spliced)
	}()

	var written atomic.Int64
	go func() {
		chunk := make([]byte, 1024)
		for {
			n, err := serverPeer.Write(chunk)
			written.Add(int64(n))
			if err != nil {
				return
			}
		}
	}()
	// Nobody reads clientPeer: the relay must stop reading from the server
	// instead of buffering.
	var last int64 = -1
	for {
		time.Sleep(50 * time.Millisecond)
		n := written.Load()
		if n == last {
			break
		}
		last = n
	}
	if last > copyBufferSize {
		t.Fatalf("relay accepted %d bytes with a stalled reader, bound %d", last, copyBufferSize)
	}
	if _, err := io.ReadFull(clientPeer, make([]byte, 4096)); err != nil {
		t.Fatal(err)
	}
	c.cancel(errors.New("test done"))
	<-spliced
}

func TestRevocationClosesLiveSplices(t *testing.T) {
	e := newEnv(t)
	a := claimedAgent(t, e)
	alice, bob := account("1"), account("2")
	e.dir.addMember(a.id, alice)
	e.dir.addMember(a.id, bob)
	_, aliceLaptop := e.addDevice(t, alice, "alice-laptop")
	_, alicePhone := e.addDevice(t, alice, "alice-phone")
	_, bobLaptop := e.addDevice(t, bob, "bob-laptop")

	laptop, _ := e.connect(t, a, aliceLaptop)
	phone, phoneServer := e.connect(t, a, alicePhone)
	bobConn, bobServer := e.connect(t, a, bobLaptop)

	e.r.RevokeDevice("alice-laptop")
	expectClosed(t, laptop)
	if got := next[edgeproto.DeviceRevoked](t, a); got.DeviceID != "alice-laptop" {
		t.Fatalf("device revoked %q", got.DeviceID)
	}
	roundTrip(t, phone, phoneServer, "other devices stay open")

	// A directory push that drops bob closes his splice.
	send(t, a.ws, edgeproto.Directory{Entries: []edgeproto.DirectoryEntry{
		{Kind: edgeproto.EntryMember, Provider: alice.Provider, Subject: alice.Subject, Role: "admin"},
	}})
	expectClosed(t, bobConn)
	// The server's data socket carries the reason too.
	_ = bobServer.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := bobServer.Read(make([]byte, 1)); err == nil || !strings.Contains(err.Error(), string(edgeproto.RefusalNotMember)) {
		t.Fatalf("server side of the dropped splice closed with %v", err)
	}

	laptop, _ = e.connect(t, a, aliceLaptop)
	e.r.CloseServer(a.id)
	expectClosed(t, laptop)
}

func TestRevocationDuringConnect(t *testing.T) {
	e := newEnv(t)
	a := claimedAgent(t, e)
	alice := account("1")
	e.dir.addMember(a.id, alice)
	_, token := e.addDevice(t, alice, "alice-laptop")

	// The revocation lands after the relay checked the token and before
	// it registered the connection.
	admitting, release := make(chan struct{}), make(chan struct{})
	e.dir.mu.Lock()
	e.dir.admitHook = func() {
		close(admitting)
		<-release
	}
	e.dir.mu.Unlock()
	type dialed struct {
		status int
		err    error
	}
	done := make(chan dialed, 1)
	go func() {
		ws, resp, err := websocket.Dial(t.Context(), e.wsBase+edgeproto.ConnectPath(a.id), &websocket.DialOptions{
			HTTPHeader: bearerHeader(token),
		})
		d := dialed{err: err}
		if resp != nil {
			d.status = resp.StatusCode
		}
		if ws != nil {
			_ = ws.CloseNow()
		}
		done <- d
	}()
	<-admitting
	e.dir.mu.Lock()
	delete(e.dir.tokens, token)
	e.dir.mu.Unlock()
	e.r.RevokeDevice("alice-laptop")
	close(release)

	if d := <-done; d.err == nil || d.status != http.StatusUnauthorized {
		t.Fatalf("connection racing its device's revocation: status %d, %v; want 401", d.status, d.err)
	}
	select {
	case m := <-a.msgs:
		t.Fatalf("the server received %T for a revoked device", m)
	default:
	}
}

func TestConnectionLimits(t *testing.T) {
	e := newEnv(t)
	a := claimedAgent(t, e)
	acct := account("1")
	e.dir.addMember(a.id, acct)
	_, token := e.addDevice(t, acct, "dev-1")
	for range edgeproto.MaxConnsPerDevice {
		e.connect(t, a, token)
	}
	status, text := e.get(t, a.id, bearerHeader(token))
	if status != http.StatusTooManyRequests || text != string(edgeproto.RefusalConnLimit) {
		t.Fatalf("device over its limit: %d %q", status, text)
	}

	for i := edgeproto.MaxConnsPerDevice; i < edgeproto.MaxSSHConnsPerServer; i++ {
		_, other := e.addDevice(t, acct, "dev-extra-"+strings.Repeat("x", i))
		e.connect(t, a, other)
	}
	_, fresh := e.addDevice(t, acct, "dev-fresh")
	status, text = e.get(t, a.id, bearerHeader(fresh))
	if status != http.StatusTooManyRequests || text != string(edgeproto.RefusalConnLimit) {
		t.Fatalf("server over its limit: %d %q", status, text)
	}
}

func TestEgressBudgetThrottlesAndPersists(t *testing.T) {
	store := &fakeEgress{months: map[string]int64{}}
	e := newEnvWith(t, 1, store)
	e.r.throttleRate = 64 << 10
	a := claimedAgent(t, e)
	acct := account("1")
	e.dir.addMember(a.id, acct)
	_, token := e.addDevice(t, acct, "dev-1")
	client, server := e.connect(t, a, token)

	roundTrip(t, client, server, "x")
	if !e.r.Metrics().Throttled {
		t.Fatal("not throttled past the budget")
	}
	// Four 16 KiB writes at 64 KiB/s: at least three throttle pauses of
	// 250ms each before the last one is read.
	go func() {
		chunk := make([]byte, 16<<10)
		for range 4 {
			if _, err := client.Write(chunk); err != nil {
				return
			}
		}
	}()
	start := time.Now()
	_ = server.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.ReadFull(server, make([]byte, 64<<10)); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < 700*time.Millisecond {
		t.Fatalf("64 KiB crossed a throttled splice in %s", elapsed)
	}

	if err := e.r.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	month := monthOf(time.Now())
	store.mu.Lock()
	saved := store.months[month]
	store.mu.Unlock()
	if saved < 64<<10 {
		t.Fatalf("saved %d bytes of egress", saved)
	}
	restarted := newEnvWith(t, 1, store)
	if got := restarted.r.Metrics(); got.EgressThisMonth != saved || !got.Throttled {
		t.Fatalf("after restart: %+v, want %d bytes and throttled", got, saved)
	}
}

func TestThrottleIsEdgeWide(t *testing.T) {
	e := newEnvWith(t, 1, &fakeEgress{months: map[string]int64{}})
	e.r.throttleRate = 64 << 10
	e.r.count(1)
	a := claimedAgent(t, e)
	acct := account("1")
	e.dir.addMember(a.id, acct)
	_, token := e.addDevice(t, acct, "dev-1")

	// Four splices each carry 16 KiB. Throttled one by one they would
	// finish together in a quarter second; sharing 64 KiB/s they need
	// about a second.
	const splices, size = 4, 16 << 10
	var clients, servers []net.Conn
	for range splices {
		c, s := e.connect(t, a, token)
		clients, servers = append(clients, c), append(servers, s)
	}
	start := time.Now()
	errs := make(chan error, splices)
	for i := range splices {
		go func() {
			if _, err := clients[i].Write(make([]byte, size)); err != nil {
				errs <- err
				return
			}
			_ = servers[i].SetReadDeadline(time.Now().Add(10 * time.Second))
			_, err := io.ReadFull(servers[i], make([]byte, size))
			errs <- err
		}()
	}
	for range splices {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if elapsed := time.Since(start); elapsed < 700*time.Millisecond {
		t.Fatalf("%d splices carried %d KiB in %s: more connections bought more throughput", splices, splices*size>>10, elapsed)
	}
}

// TestThrottleIsFairAcrossServers floods one server with splices from two
// devices once the budget is spent: a terminal on another server still
// gets its turn within about one throttled read.
func TestThrottleIsFairAcrossServers(t *testing.T) {
	e := newEnvWith(t, 1, &fakeEgress{months: map[string]int64{}})
	e.r.throttleRate = 32 << 10
	e.r.count(1)
	acct := account("1")
	flooded, quiet := claimedAgent(t, e), claimedAgent(t, e)
	e.dir.addMember(flooded.id, acct)
	e.dir.addMember(quiet.id, acct)
	flood := func(client, server net.Conn) {
		go func() { _, _ = io.Copy(io.Discard, server) }()
		go func() {
			for {
				if _, err := client.Write(make([]byte, 32<<10)); err != nil {
					return
				}
			}
		}()
	}
	_, flooder := e.addDevice(t, acct, "flooder")
	_, other := e.addDevice(t, acct, "other flooder")
	for range 8 {
		flood(e.connect(t, flooded, flooder))
		flood(e.connect(t, flooded, other))
	}

	_, token := e.addDevice(t, acct, "terminal")
	client, server := e.connect(t, quiet, token)
	time.Sleep(500 * time.Millisecond)
	start := time.Now()
	roundTrip(t, client, server, "k")
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("a keystroke to another server took %s behind the flood", elapsed)
	}
}

func TestEgressResetsAtTheMonthBoundary(t *testing.T) {
	now := time.Now().UTC()
	thisMonth := monthOf(now)
	store := &fakeEgress{months: map[string]int64{thisMonth: 100}}
	e := newEnvWith(t, 100, store)
	if !e.r.Metrics().Throttled {
		t.Fatal("not throttled with the month's budget spent")
	}
	e.r.count(10)
	nextMonth := time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC)
	if err := e.r.flushEgress(t.Context(), nextMonth); err != nil {
		t.Fatal(err)
	}
	if m := e.r.Metrics(); m.EgressThisMonth != 0 || m.Throttled {
		t.Fatalf("after the month changed: %+v, want a fresh budget", m)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if got := store.months[thisMonth]; got != 110 {
		t.Fatalf("%s saved %d bytes, want the 10 counted before the boundary added to 100", thisMonth, got)
	}
}

func TestWebSocketCompressionIsOff(t *testing.T) {
	e := newEnv(t)
	_, resp, err := websocket.Dial(t.Context(), e.wsBase+edgeproto.PathServerControl, &websocket.DialOptions{
		CompressionMode: websocket.CompressionContextTakeover,
	})
	if err != nil {
		t.Fatal(err)
	}
	if ext := resp.Header.Get("Sec-WebSocket-Extensions"); ext != "" {
		t.Fatalf("relay negotiated %q", ext)
	}
}
