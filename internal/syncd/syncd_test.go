package syncd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestBackoffDelay(t *testing.T) {
	// Zero jitter pins the delay at the bottom of the window: d/2.
	wantHalf := []time.Duration{
		500 * time.Millisecond, // attempt 0
		time.Second,            // 1
		2 * time.Second,        // 2
		4 * time.Second,        // 3
		8 * time.Second,        // 4
		16 * time.Second,       // 5
		30 * time.Second,       // 6 (capped)
		30 * time.Second,       // 7 (still capped)
	}
	for attempt, want := range wantHalf {
		if got := backoffDelay(attempt, 0); got != want {
			t.Errorf("backoffDelay(%d, 0) = %v, want %v", attempt, got, want)
		}
	}
	if got := backoffDelay(100, 0); got != 30*time.Second {
		t.Errorf("backoffDelay(100, 0) = %v, want capped 30s", got)
	}
	// Jitter spreads over [d/2, d).
	if got := backoffDelay(2, 0.5); got < 2*time.Second || got >= 4*time.Second {
		t.Errorf("backoffDelay(2, 0.5) = %v, want in [2s, 4s)", got)
	}
	if got := backoffDelay(0, 0.999); got >= time.Second {
		t.Errorf("backoffDelay(0, 0.999) = %v, want < base", got)
	}
}

func TestNextAttempt(t *testing.T) {
	// Ack then immediate drop keeps the backoff climbing.
	attempt := 0
	for i := 1; i <= 4; i++ {
		attempt = nextAttempt(attempt, true, 100*time.Millisecond)
		if attempt != i {
			t.Fatalf("ack-then-drop round %d: attempt = %d, want %d", i, attempt, i)
		}
	}
	// Just under the threshold still counts as a failure.
	if got := nextAttempt(2, true, stableSession-time.Millisecond); got != 3 {
		t.Errorf("near-threshold session: attempt = %d, want 3", got)
	}
	// A subscribed session held past the stability threshold resets it.
	if got := nextAttempt(attempt, true, stableSession); got != 0 {
		t.Errorf("stable session: attempt = %d, want 0", got)
	}
	// A long session that never got an ack does not reset.
	if got := nextAttempt(3, false, time.Hour); got != 4 {
		t.Errorf("long unsubscribed session: attempt = %d, want 4", got)
	}
	// Plain connect failure keeps climbing.
	if got := nextAttempt(0, false, 0); got != 1 {
		t.Errorf("connect failure: attempt = %d, want 1", got)
	}
}

func TestWantsFetch(t *testing.T) {
	ev := func(typ, payload string) protocol.Event {
		return protocol.Event{Type: typ, Payload: json.RawMessage(payload)}
	}
	cases := []struct {
		name      string
		ev        protocol.Event
		workspace string
		want      bool
	}{
		{"git.branch, any workspace", ev("git.branch", `{"workspace_id":"ws1","branch":"aether/run-r1","commit":"abc"}`), "", true},
		{"git.branch, matching workspace", ev("git.branch", `{"workspace_id":"ws1","branch":"aether/run-r1","commit":"abc"}`), "ws1", true},
		{"git.branch, other workspace", ev("git.branch", `{"workspace_id":"ws2","branch":"aether/run-r1","commit":"abc"}`), "ws1", false},
		{"run.status ignored", ev("run.status", `{"to":"running"}`), "", false},
		{"run.diff ignored", ev("run.diff", `{"files":[]}`), "ws1", false},
		{"bad payload ignored when filtering", ev("git.branch", `notjson`), "ws1", false},
		{"bad payload still fetches unfiltered", ev("git.branch", `notjson`), "", true},
	}
	for _, tc := range cases {
		if got := wantsFetch(tc.ev, tc.workspace); got != tc.want {
			t.Errorf("%s: wantsFetch = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// pipeStream is one end of a duplex in-memory connection.
type pipeStream struct {
	io.Reader
	io.Writer
	closed atomic.Bool
	cr, cw func() error
}

func (p *pipeStream) Close() error {
	if p.closed.Swap(true) {
		return nil
	}
	_ = p.cr()
	return p.cw()
}

// fakeServer is the far end of a runSession stream: an NDJSON event
// server driven by the test.
type fakeServer struct {
	r *bufio.Reader
	w io.Writer
}

// newSessionPair wires a Daemon-side stream to a test-side fakeServer.
func newSessionPair() (io.ReadWriteCloser, *fakeServer) {
	cr, sw := io.Pipe() // server -> client
	sr, cw := io.Pipe() // client -> server
	client := &pipeStream{Reader: cr, Writer: cw, cr: cr.Close, cw: cw.Close}
	return client, &fakeServer{r: bufio.NewReader(sr), w: sw}
}

func (s *fakeServer) readSubscribe(t *testing.T) protocol.SubscribeRequest {
	t.Helper()
	line, err := protocol.ReadLine(s.r)
	if err != nil {
		t.Fatalf("read subscribe: %v", err)
	}
	var req protocol.SubscribeRequest
	if err := json.Unmarshal(line, &req); err != nil {
		t.Fatalf("decode subscribe: %v", err)
	}
	return req
}

func (s *fakeServer) send(t *testing.T, v any) {
	t.Helper()
	line, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.w.Write(append(line, '\n')); err != nil {
		t.Fatalf("server write: %v", err)
	}
}

// newTestDaemon returns a Daemon whose git actions only count calls.
func newTestDaemon(t *testing.T, workspace string) (*Daemon, *atomic.Int64, *atomic.Int64) {
	t.Helper()
	d, err := New(Config{Server: "127.0.0.1:2222", RepoPath: ".", WorkspaceID: workspace})
	if err != nil {
		t.Fatal(err)
	}
	var fetches, pushes atomic.Int64
	d.fetch = func(context.Context) error { fetches.Add(1); return nil }
	d.push = func(context.Context) error { pushes.Add(1); return nil }
	return d, &fetches, &pushes
}

func gitBranchEvent(seq uint64, workspace string) protocol.Event {
	return protocol.Event{
		ID: "ev", Seq: seq, Type: "git.branch",
		Payload: json.RawMessage(`{"workspace_id":"` + workspace + `","branch":"aether/run-r1","commit":"abc"}`),
	}
}

// runSessionAsync drives d.runSession in the background and returns a
// channel with its outcome.
func runSessionAsync(ctx context.Context, d *Daemon, stream io.ReadWriteCloser) chan error {
	done := make(chan error, 1)
	go func() {
		_, err := d.runSession(ctx, stream)
		done <- err
	}()
	return done
}

func waitCount(t *testing.T, c *atomic.Int64, want int64, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for c.Load() < want {
		if time.Now().After(deadline) {
			t.Fatalf("%s = %d, want %d", what, c.Load(), want)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestSessionCatchupThenEventDrivenFetch(t *testing.T) {
	d, fetches, pushes := newTestDaemon(t, "ws1")
	client, srv := newSessionPair()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := runSessionAsync(ctx, d, client)

	req := srv.readSubscribe(t)
	if req.Replay || req.AfterSeq != 0 {
		t.Errorf("first subscribe = %+v, want no replay from zero cursor", req)
	}
	if len(req.Types) != 1 || req.Types[0] != "git.branch" {
		t.Errorf("subscribe types = %v, want [git.branch]", req.Types)
	}
	srv.send(t, protocol.SubscribeResponse{OK: true})

	// Catch-up fires immediately after the ack: one fetch, one push.
	waitCount(t, fetches, 1, "catch-up fetches")
	waitCount(t, pushes, 1, "catch-up pushes")

	// A matching git.branch event triggers a fetch; a foreign one does not.
	srv.send(t, gitBranchEvent(7, "ws1"))
	waitCount(t, fetches, 2, "fetches after own-workspace event")
	srv.send(t, gitBranchEvent(8, "ws2"))
	srv.send(t, gitBranchEvent(9, "ws1"))
	waitCount(t, fetches, 3, "fetches after foreign + own events")
	if fetches.Load() != 3 {
		t.Errorf("fetches = %d, want 3 (foreign-workspace event must not fetch)", fetches.Load())
	}

	// The cursor tracks the highest seq seen, even on filtered events.
	if d.lastSeq != 9 {
		t.Errorf("lastSeq = %d, want 9", d.lastSeq)
	}

	// Server drop ends the session as an error (Run will reconnect).
	_ = client.Close()
	if err := <-done; err == nil {
		t.Error("runSession = nil error after stream drop, want failure")
	}
}

func TestSessionResumesFromCursorOnReconnect(t *testing.T) {
	d, fetches, _ := newTestDaemon(t, "")

	// Session one: see an event at seq 42, then drop.
	client, srv := newSessionPair()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := runSessionAsync(ctx, d, client)
	srv.readSubscribe(t)
	srv.send(t, protocol.SubscribeResponse{OK: true})
	srv.send(t, gitBranchEvent(42, "ws1"))
	waitCount(t, fetches, 2, "fetches in session one") // catch-up + event
	_ = client.Close()
	<-done

	// Session two: the subscribe resumes with replay after seq 42, and the
	// catch-up fetch fires again (the offline story).
	client2, srv2 := newSessionPair()
	done2 := runSessionAsync(ctx, d, client2)
	req := srv2.readSubscribe(t)
	if !req.Replay || req.AfterSeq != 42 {
		t.Errorf("resume subscribe = %+v, want Replay with AfterSeq 42", req)
	}
	srv2.send(t, protocol.SubscribeResponse{OK: true})
	waitCount(t, fetches, 3, "catch-up fetch on reconnect")
	_ = client2.Close()
	<-done2
}

func TestSessionSubscribeRefused(t *testing.T) {
	d, fetches, _ := newTestDaemon(t, "")
	d.lastSeq = 10
	client, srv := newSessionPair()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	done := make(chan struct {
		sub bool
		err error
	}, 1)
	go func() {
		sub, err := d.runSession(ctx, client)
		done <- struct {
			sub bool
			err error
		}{sub, err}
	}()
	srv.readSubscribe(t)
	srv.send(t, protocol.SubscribeResponse{OK: false, Code: protocol.CodeUnavailable, Error: "no event log"})
	res := <-done
	if res.sub || res.err == nil {
		t.Errorf("refused subscribe: sub=%v err=%v, want false + error", res.sub, res.err)
	}
	if d.lastSeq != 0 {
		t.Errorf("lastSeq = %d after unavailable replay, want cursor dropped", d.lastSeq)
	}
	if fetches.Load() != 0 {
		t.Errorf("fetches = %d before any ack, want 0", fetches.Load())
	}
}

func TestNewValidatesAndDefaults(t *testing.T) {
	if _, err := New(Config{RepoPath: "."}); err == nil {
		t.Error("New without server: want error")
	}
	if _, err := New(Config{Server: "h:1"}); err == nil {
		t.Error("New without repo: want error")
	}
	d, err := New(Config{Server: "h:1", RepoPath: "."})
	if err != nil {
		t.Fatal(err)
	}
	c := d.cfg
	if c.User != "aether" || c.Remote != "aether" || c.BaseBranch != "main" || c.GitPath != "git" {
		t.Errorf("defaults = %+v", c)
	}
	if c.PushInterval <= 0 || c.CatchupInterval <= 0 {
		t.Errorf("intervals not defaulted: %+v", c)
	}
}

func TestRunStopsOnContextCancel(t *testing.T) {
	d, _, _ := newTestDaemon(t, "")
	d.cfg.Server = "127.0.0.1:1" // nothing listens here
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Run = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop on cancel")
	}
}

func TestRunBranchRefspec(t *testing.T) {
	got := runBranchRefspec("aether")
	want := "+refs/heads/aether/run-*:refs/remotes/aether/aether/run-*"
	if got != want {
		t.Errorf("refspec = %q, want %q", got, want)
	}
	if !strings.HasPrefix(got, "+") {
		t.Error("refspec must be forced: run branches are server-owned")
	}
}
