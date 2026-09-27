package edgeagent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/edgeproto"
	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"
)

const waitFor = 5 * time.Second

// fakeEdge is an edge that enrolls servers with the real protocol and
// hands each enrolled control connection and data socket to the test.
type fakeEdge struct {
	t        *testing.T
	srv      *httptest.Server
	origin   string
	priv     ed25519.PrivateKey
	pub      ed25519.PublicKey
	state    string
	domain   string
	controls chan *edgeControl
	data     chan *websocket.Conn

	mu      sync.Mutex
	tickets map[string]string
}

type edgeControl struct {
	t        *testing.T
	c        *websocket.Conn
	serverID string
	msgs     chan edgeproto.Message
}

func newFakeEdge(t *testing.T) *fakeEdge {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	e := &fakeEdge{
		t: t, priv: priv, pub: pub, state: edgeproto.StateUnclaimed,
		controls: make(chan *edgeControl, 64),
		data:     make(chan *websocket.Conn, 8),
		tickets:  map[string]string{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc(edgeproto.PathServerControl, e.control)
	mux.HandleFunc(edgeproto.PathServerData, e.dataSocket)
	mux.HandleFunc(edgeproto.PathEdgeKey, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(edgeproto.EdgeKeyResponse{Key: e.pub, Fingerprint: edgeproto.EdgeKeyFingerprint(e.pub)})
	})
	e.srv = httptest.NewServer(mux)
	t.Cleanup(e.srv.Close)
	e.origin, err = edgeproto.Origin(e.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *fakeEdge) control(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		e.t.Error(err)
		return
	}
	defer c.CloseNow() //nolint:errcheck // test edge
	ctx := context.Background()
	nonce := make([]byte, edgeproto.NonceSize)
	_, _ = rand.Read(nonce)
	writeMsg(e.t, c, edgeproto.Challenge{Version: edgeproto.Version, Nonce: nonce, Origin: e.origin})
	_, data, err := c.Read(ctx)
	if err != nil {
		return
	}
	m, err := edgeproto.DecodeControl(data)
	if err != nil {
		e.t.Errorf("hello: %v", err)
		return
	}
	hello := m.(edgeproto.Hello)
	key, err := hello.PublicKey()
	if err != nil {
		e.t.Error(err)
		return
	}
	id, err := edgeproto.VerifyEnrollment(key, e.origin, nonce, hello.Signature)
	if err != nil {
		e.t.Errorf("enrollment signature: %v", err)
		return
	}
	writeMsg(e.t, c, edgeproto.Ready{ServerID: id, State: e.state, EdgeKey: e.pub, ServerDomain: e.domain})
	ec := &edgeControl{t: e.t, c: c, serverID: id, msgs: make(chan edgeproto.Message, 64)}
	select {
	case e.controls <- ec:
	default:
		return
	}
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			close(ec.msgs)
			return
		}
		m, err := edgeproto.DecodeControl(data)
		if err != nil {
			e.t.Errorf("server sent an invalid message: %v", err)
			continue
		}
		select {
		case ec.msgs <- m:
		default:
		}
	}
}

func (e *fakeEdge) dataSocket(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	ticket, ok := e.tickets[r.PathValue("conn_id")]
	e.mu.Unlock()
	if !ok || r.Header.Get("Authorization") != "Bearer "+ticket {
		http.Error(w, `{"error":"bad ticket"}`, http.StatusForbidden)
		return
	}
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		e.t.Error(err)
		return
	}
	select {
	case e.data <- c:
	default:
		_ = c.CloseNow()
	}
}

// open sends an open for a fresh ticket and returns it.
func (e *fakeEdge) open(ec *edgeControl, connID, kind, grant string) {
	ticket := edgeproto.NewToken()
	e.mu.Lock()
	e.tickets[connID] = ticket
	e.mu.Unlock()
	ec.send(edgeproto.Open{ConnID: connID, Ticket: ticket, Kind: kind, Grant: grant, ClientAddr: "192.0.2.10:4242"})
}

func (e *fakeEdge) nextControl(t *testing.T) *edgeControl {
	t.Helper()
	select {
	case ec := <-e.controls:
		return ec
	case <-time.After(waitFor):
		t.Fatal("server did not enroll")
		return nil
	}
}

func (e *fakeEdge) nextData(t *testing.T) *websocket.Conn {
	t.Helper()
	select {
	case c := <-e.data:
		return c
	case <-time.After(waitFor):
		t.Fatal("server did not attach a data socket")
		return nil
	}
}

func (e *fakeEdge) grant(t *testing.T, g edgeproto.Grant) string {
	t.Helper()
	signed, err := edgeproto.SignGrant(e.priv, g)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

func writeMsg(t *testing.T, c *websocket.Conn, m edgeproto.Message) {
	data, err := edgeproto.EncodeControl(m)
	if err != nil {
		t.Error(err)
		return
	}
	_ = c.Write(context.Background(), websocket.MessageText, data)
}

func (ec *edgeControl) send(m edgeproto.Message) { writeMsg(ec.t, ec.c, m) }

// expect returns the next message of type T, skipping pings and
// directory pushes the test is not asking for.
func expect[T edgeproto.Message](t *testing.T, ec *edgeControl) T {
	t.Helper()
	deadline := time.After(waitFor)
	for {
		select {
		case m, ok := <-ec.msgs:
			if !ok {
				var zero T
				t.Fatalf("control connection closed while waiting for %T", zero)
			}
			if v, ok := m.(T); ok {
				return v
			}
		case <-deadline:
			var zero T
			t.Fatalf("no %T from the server", zero)
		}
	}
}

type fakeSSH struct {
	mu      sync.Mutex
	entries []edgeproto.DirectoryEntry
	changed chan struct{}
	served  chan edgeproto.Grant
	claimed chan edgeproto.Account
	closed  chan string
}

func newFakeSSH() *fakeSSH {
	return &fakeSSH{
		changed: make(chan struct{}, 1),
		served:  make(chan edgeproto.Grant, 8),
		claimed: make(chan edgeproto.Account, 8),
		closed:  make(chan string, 8),
	}
}

func (f *fakeSSH) ServeEdgeConn(_ context.Context, nc net.Conn, g edgeproto.Grant) {
	f.served <- g
	_, _ = io.Copy(nc, nc)
}

func (f *fakeSSH) ClaimByEdge(_ context.Context, a edgeproto.Account) (domain.Member, error) {
	f.claimed <- a
	return domain.Member{ID: "m-owner"}, nil
}

func (f *fakeSSH) EdgeDirectory(context.Context) ([]edgeproto.DirectoryEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.entries, nil
}

func (f *fakeSSH) EdgeDirectoryChanged() <-chan struct{} { return f.changed }

func (f *fakeSSH) CloseEdgeDevice(key string) { f.closed <- key }

func (f *fakeSSH) setDirectory(entries []edgeproto.DirectoryEntry) {
	f.mu.Lock()
	f.entries = entries
	f.mu.Unlock()
	f.changed <- struct{}{}
}

func newHostKey(t *testing.T) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

func newAgent(t *testing.T, edgeURL, dataDir string, sshd SSH) *Agent {
	t.Helper()
	a, err := New(Config{EdgeURL: edgeURL, DataDir: dataDir, HostKey: newHostKey(t), SSH: sshd})
	if err != nil {
		t.Fatal(err)
	}
	a.minBackoff, a.maxBackoff = 10*time.Millisecond, 50*time.Millisecond
	return a
}

// run starts a.Run and stops it when the test ends, before the edge
// closes.
func run(t *testing.T, a *Agent) (stop func(), done <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan struct{})
	go func() {
		defer close(ch)
		a.Run(ctx)
	}()
	stop = func() {
		cancel()
		select {
		case <-ch:
		case <-time.After(waitFor):
			t.Error("Run did not return after cancel")
		}
	}
	t.Cleanup(stop)
	return stop, ch
}

func TestEnrollPinsEdgeKeyPrivately(t *testing.T) {
	edge := newFakeEdge(t)
	dir := t.TempDir()
	a := newAgent(t, edge.srv.URL, dir, newFakeSSH())
	run(t, a)
	ec := edge.nextControl(t)
	if ec.serverID != a.ServerID() {
		t.Fatalf("edge derived %s, agent is %s", ec.serverID, a.ServerID())
	}
	expect[edgeproto.Directory](t, ec)
	// This edge's ready carries no server domain, as an edge that passes
	// no dashboard through sends it.
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()
	if d, err := a.ServerDomain(ctx); d != "" || err != nil {
		t.Fatalf("ServerDomain = %q, %v; want none", d, err)
	}
	pinned, err := a.state.PinnedKey()
	if err != nil || !pinned.Equal(edge.pub) {
		t.Fatalf("pinned %x, %v; want the edge key", pinned, err)
	}
	for path, want := range map[string]os.FileMode{StateDir(dir): 0o700, a.state.dir: 0o700, a.state.path(pinFile): 0o600} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s mode %o, want %o", path, got, want)
		}
	}
}

func TestChangedEdgeKeyIsRefused(t *testing.T) {
	edge := newFakeEdge(t)
	dir := t.TempDir()
	old, _, _ := ed25519.GenerateKey(rand.Reader)
	if err := openState(t, dir, edge.srv.URL).Pin(old); err != nil {
		t.Fatal(err)
	}
	a := newAgent(t, edge.srv.URL, dir, newFakeSSH())
	run(t, a)
	ec := edge.nextControl(t)
	for m := range ec.msgs {
		t.Errorf("server sent %T to an edge whose key changed", m)
	}
	st := waitStatus(t, a.state, func(st Status) bool { return strings.Contains(st.Error, "edge key changed") })
	for _, want := range []string{edgeproto.EdgeKeyFingerprint(old), edgeproto.EdgeKeyFingerprint(edge.pub), "aether-server edge trust"} {
		if !strings.Contains(st.Error, want) {
			t.Errorf("refusal %q does not name %s", st.Error, want)
		}
	}
	if pinned, _ := a.state.PinnedKey(); !pinned.Equal(old) {
		t.Error("a changed edge key replaced the pin")
	}
}

// Changing edge-url must not meet the previous edge's pin or owner, and
// returning to that edge must find both again.
func TestPinAndOwnerBelongToTheirEdge(t *testing.T) {
	first, second := newFakeEdge(t), newFakeEdge(t)
	first.state = edgeproto.StateClaimed
	dir := t.TempDir()
	owner := edgeproto.Account{Provider: edgeproto.ProviderGitHub, Subject: "1"}
	if err := openState(t, dir, first.srv.URL).write(ownerFile, owner); err != nil {
		t.Fatal(err)
	}
	enroll := func(edge *fakeEdge) *Agent {
		t.Helper()
		a := newAgent(t, edge.srv.URL, dir, newFakeSSH())
		stop, _ := run(t, a)
		edge.nextControl(t)
		waitStatus(t, a.state, func(st Status) bool { return st.Connected })
		stop()
		return a
	}
	enroll(first)
	a := enroll(second)
	if pinned, err := a.state.PinnedKey(); err != nil || !pinned.Equal(second.pub) {
		t.Fatalf("pin at the second edge = %x, %v; want its own key", pinned, err)
	}
	if got, err := a.state.Owner(); got != nil || err != nil {
		t.Fatalf("owner at the second edge = %+v, %v; want none", got, err)
	}
	a = enroll(first)
	if pinned, err := a.state.PinnedKey(); err != nil || !pinned.Equal(first.pub) {
		t.Fatalf("pin back at the first edge = %x, %v; want its key", pinned, err)
	}
	if got, err := a.state.Owner(); err != nil || got == nil || *got != owner {
		t.Fatalf("owner back at the first edge = %+v, %v; want %+v", got, err, owner)
	}
}

// The edge passes no dashboard through to an unclaimed server, so the
// dashboard waits for Claimed; an edge that reports the server claimed
// signals it at enrollment.
func TestClaimedAtAClaimedEnrollment(t *testing.T) {
	edge := newFakeEdge(t)
	edge.state = edgeproto.StateClaimed
	a := newAgent(t, edge.srv.URL, t.TempDir(), newFakeSSH())
	run(t, a)
	expect[edgeproto.Directory](t, edge.nextControl(t))
	select {
	case <-a.Claimed():
	default:
		t.Fatal("Claimed not signalled after the edge reported the server claimed")
	}
}

// The dashboard address outlives the connection that announced it, so
// aether-server edge status can print it while the server is offline.
func TestStatusKeepsTheServerDomain(t *testing.T) {
	edge := newFakeEdge(t)
	edge.domain = "servers.example.test"
	a := newAgent(t, edge.srv.URL, t.TempDir(), newFakeSSH())
	stop, _ := run(t, a)
	edge.nextControl(t)
	waitStatus(t, a.state, func(st Status) bool { return st.Connected && st.ServerDomain == edge.domain })
	stop()
	st, _, err := a.state.Status()
	if err != nil || st.Connected || st.ServerDomain != edge.domain {
		t.Fatalf("status after stop = %+v, %v; want disconnected with %s", st, err, edge.domain)
	}
}

func waitStatus(t *testing.T, s *State, ok func(Status) bool) Status {
	t.Helper()
	deadline := time.Now().Add(waitFor)
	for {
		st, _, err := s.Status()
		if err != nil {
			t.Fatal(err)
		}
		if ok(st) {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("status never matched; last %+v", st)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestReconnectsAfterEdgeDropsAndOnDrain(t *testing.T) {
	edge := newFakeEdge(t)
	a := newAgent(t, edge.srv.URL, t.TempDir(), newFakeSSH())
	run(t, a)
	_ = edge.nextControl(t).c.CloseNow()
	edge.nextControl(t).send(edgeproto.Drain{})
	edge.nextControl(t)
}

// Servers an edge restart drops together must not redial in step: even
// the first wait, before any backoff has grown, is spread.
func TestFirstRedialIsJittered(t *testing.T) {
	const agents = 8
	firstGaps := make(chan time.Duration, agents)
	for range agents {
		var (
			mu    sync.Mutex
			dials []time.Time
		)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			mu.Lock()
			dials = append(dials, time.Now())
			if len(dials) == 2 {
				firstGaps <- dials[1].Sub(dials[0])
			}
			mu.Unlock()
			http.Error(w, "draining", http.StatusServiceUnavailable)
		}))
		t.Cleanup(srv.Close)
		a := newAgent(t, srv.URL, t.TempDir(), newFakeSSH())
		a.minBackoff, a.maxBackoff = 100*time.Millisecond, time.Second
		run(t, a)
	}
	var gaps []time.Duration
	for range agents {
		select {
		case gap := <-firstGaps:
			if gap >= 125*time.Millisecond {
				return
			}
			gaps = append(gaps, gap)
		case <-time.After(waitFor):
			t.Fatal("an agent did not redial")
		}
	}
	t.Fatalf("first redials came %v after the failure; want them spread over [100ms, 200ms)", gaps)
}

func TestSilentEdgeIsDroppedAndRedialed(t *testing.T) {
	edge := newFakeEdge(t)
	a := newAgent(t, edge.srv.URL, t.TempDir(), newFakeSSH())
	a.pingInterval, a.idleTimeout = time.Hour, 100*time.Millisecond
	run(t, a)
	first := edge.nextControl(t)
	edge.nextControl(t)
	for range first.msgs {
	}
}

func TestRunAndWebListenerSurviveEdgeOutage(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	down := "http://" + l.Addr().String()
	_ = l.Close()
	a := newAgent(t, down, t.TempDir(), newFakeSSH())
	stop, done := run(t, a)
	accepted := make(chan error, 1)
	go func() {
		_, aerr := a.WebListener().Accept()
		accepted <- aerr
	}()
	select {
	case <-done:
		t.Fatal("Run returned while the edge was down")
	case aerr := <-accepted:
		t.Fatalf("Accept returned %v while the agent was alive", aerr)
	case <-time.After(300 * time.Millisecond):
	}
	st, ok, err := a.state.Status()
	if err != nil || !ok || st.Connected || st.Error == "" {
		t.Errorf("status %+v, %v, %v; want the dial error", st, ok, err)
	}
	stop()
	if err := <-accepted; !errors.Is(err, net.ErrClosed) {
		t.Errorf("Accept after Run returned = %v, want net.ErrClosed", err)
	}
}

func TestDirectoryPushedOnChange(t *testing.T) {
	edge := newFakeEdge(t)
	sshd := newFakeSSH()
	sshd.entries = []edgeproto.DirectoryEntry{{Kind: edgeproto.EntryMember, Provider: edgeproto.ProviderGitHub, Subject: "1", Role: "admin"}}
	a := newAgent(t, edge.srv.URL, t.TempDir(), sshd)
	run(t, a)
	ec := edge.nextControl(t)
	if d := expect[edgeproto.Directory](t, ec); len(d.Entries) != 1 {
		t.Fatalf("first push %+v, want the member", d)
	}
	sshd.setDirectory(append(sshd.entries, edgeproto.DirectoryEntry{
		Kind: edgeproto.EntryInvitation, Provider: edgeproto.ProviderGitHub, Login: "octo", Role: "member",
		ExpiresAt: time.Now().Add(time.Hour),
	}))
	if d := expect[edgeproto.Directory](t, ec); len(d.Entries) != 2 {
		t.Fatalf("push after change %+v, want member and invitation", d)
	}
}

func TestOwnerForgottenWhenTheEdgeHasNone(t *testing.T) {
	edge := newFakeEdge(t)
	dir := t.TempDir()
	state := openState(t, dir, edge.srv.URL)
	owner := edgeproto.Account{Provider: edgeproto.ProviderGoogle, Subject: "g-1", Email: "owner@example.com"}
	if err := state.write(ownerFile, owner); err != nil {
		t.Fatal(err)
	}
	a := newAgent(t, edge.srv.URL, dir, newFakeSSH())
	run(t, a)
	edge.nextControl(t)
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, err := state.Owner()
		if err != nil {
			t.Fatal(err)
		}
		if got == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("owner %+v kept after an edge answered unclaimed", got)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestUnenrollForgetsTheOwner(t *testing.T) {
	edge := newFakeEdge(t)
	dir := t.TempDir()
	state := openState(t, dir, edge.srv.URL)
	if err := state.write(ownerFile, edgeproto.Account{Provider: edgeproto.ProviderGitHub, Subject: "1"}); err != nil {
		t.Fatal(err)
	}
	edge.state = edgeproto.StateClaimed
	a := newAgent(t, edge.srv.URL, dir, newFakeSSH())
	run(t, a)
	edge.nextControl(t).send(edgeproto.Unenroll{})
	edge.nextControl(t)
	if owner, err := state.Owner(); owner != nil || err != nil {
		t.Fatalf("owner after unenroll = %+v, %v", owner, err)
	}
}

func TestLeaveUnenrollsAndForgetsTheOwner(t *testing.T) {
	edge := newFakeEdge(t)
	dir := t.TempDir()
	state := openState(t, dir, edge.srv.URL)
	if err := state.write(ownerFile, edgeproto.Account{Provider: edgeproto.ProviderGitHub, Subject: "1"}); err != nil {
		t.Fatal(err)
	}
	a := newAgent(t, edge.srv.URL, dir, nil)
	if _, _, err := state.IssueClaimCode(a.ServerID(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := a.Leave(context.Background()); err != nil {
		t.Fatal(err)
	}
	expect[edgeproto.Unenroll](t, edge.nextControl(t))
	if owner, _ := state.Owner(); owner != nil {
		t.Error("owner kept after leave")
	}
	if _, ok, _ := state.ClaimCode(); ok {
		t.Error("claim code kept after leave")
	}
}

func TestFetchEdgeKey(t *testing.T) {
	edge := newFakeEdge(t)
	key, err := FetchEdgeKey(context.Background(), edge.srv.URL)
	if err != nil || !key.Equal(edge.pub) {
		t.Fatalf("FetchEdgeKey = %x, %v", key, err)
	}
}
