package ptyhost

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// Local copies of the Wave 1 contract's consumer-side seam interfaces
// (scheduler.PTYHost, sshd.PTYAttacher): Host must satisfy them exactly.
type (
	schedulerPTYHost interface {
		StartSession(ctx context.Context, run domain.RunID, att runtime.Attachment) error
		StopSession(ctx context.Context, run domain.RunID) error
		LastOutput(run domain.RunID) (time.Time, bool)
		Inject(ctx context.Context, run domain.RunID, actorName, actorColor, message string) error
	}
	sshdPTYAttacher interface {
		Attach(ctx context.Context, run domain.RunID, member domain.MemberID, cols, rows uint, readOnly bool, conn io.ReadWriter, resize <-chan [2]uint) error
	}
)

var (
	_ schedulerPTYHost = (*Host)(nil)
	_ sshdPTYAttacher  = (*Host)(nil)
)

// fakeAtt is a pipe-based runtime.Attachment honoring the contract: Stdout
// carries the merged TTY output, Stdin feeds the process, Resize records
// geometry, Close detaches the streams.
type fakeAtt struct {
	inR  *io.PipeReader
	inW  *io.PipeWriter
	outR *io.PipeReader
	outW *io.PipeWriter

	mu      sync.Mutex
	resizes [][2]uint
	closed  bool
}

func newFakeAtt() *fakeAtt {
	a := &fakeAtt{}
	a.inR, a.inW = io.Pipe()
	a.outR, a.outW = io.Pipe()
	return a
}

func (a *fakeAtt) Stdin() io.WriteCloser { return a.inW }
func (a *fakeAtt) Stdout() io.Reader     { return a.outR }
func (a *fakeAtt) Stderr() io.Reader     { return strings.NewReader("") }

func (a *fakeAtt) Resize(_ context.Context, cols, rows uint) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.resizes = append(a.resizes, [2]uint{cols, rows})
	return nil
}

func (a *fakeAtt) Close() error {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil
	}
	a.closed = true
	a.mu.Unlock()
	_ = a.outR.Close()
	return nil
}

func (a *fakeAtt) sizeCalls() [][2]uint {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([][2]uint(nil), a.resizes...)
}

// captureStdin drains the process side of stdin into a sink.
func (a *fakeAtt) captureStdin() *sink {
	s := &sink{}
	go func() { _, _ = io.Copy(s, a.inR) }()
	return s
}

func (a *fakeAtt) writeOutput(t *testing.T, data string) {
	t.Helper()
	if _, err := a.outW.Write([]byte(data)); err != nil {
		t.Fatalf("write agent output: %v", err)
	}
}

type sink struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *sink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *sink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func (s *sink) Bytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.buf.Bytes()...)
}

type testConn struct {
	r io.Reader
	w io.Writer
}

func (c *testConn) Read(p []byte) (int, error)  { return c.r.Read(p) }
func (c *testConn) Write(p []byte) (int, error) { return c.w.Write(p) }

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func newTestHost(t *testing.T, opts ...func(*Config)) (*Host, string) {
	t.Helper()
	cfg := Config{TranscriptDir: t.TempDir()}
	for _, o := range opts {
		o(&cfg)
	}
	h, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })
	return h, cfg.TranscriptDir
}

type attachHandle struct {
	out    *sink
	keys   *io.PipeWriter
	resize chan [2]uint
	errCh  chan error
}

func startAttach(t *testing.T, h *Host, run domain.RunID, member domain.MemberID, cols, rows uint, readOnly bool) *attachHandle {
	t.Helper()
	kr, kw := io.Pipe()
	hnd := &attachHandle{
		out:    &sink{},
		keys:   kw,
		resize: make(chan [2]uint, 4),
		errCh:  make(chan error, 1),
	}
	go func() {
		hnd.errCh <- h.Attach(context.Background(), run, member, cols, rows, readOnly, &testConn{r: kr, w: hnd.out}, hnd.resize)
	}()
	t.Cleanup(func() {
		_ = kw.Close()
		_ = kr.Close()
	})
	return hnd
}

func (hnd *attachHandle) typeKeys(t *testing.T, keys string) {
	t.Helper()
	if _, err := hnd.keys.Write([]byte(keys)); err != nil {
		t.Fatalf("type keys: %v", err)
	}
}

func (hnd *attachHandle) detach() { _ = hnd.keys.Close() }

func (hnd *attachHandle) wait(t *testing.T) error {
	t.Helper()
	select {
	case err := <-hnd.errCh:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("attach did not return")
		return nil
	}
}

func waitAttached(t *testing.T, h *Host, run domain.RunID, n int) {
	t.Helper()
	s := h.lookup(run)
	if s == nil {
		t.Fatalf("no session for %s", run)
	}
	waitFor(t, fmt.Sprintf("%d attached clients", n), func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.clients) == n
	})
}

func TestAttachPassthroughAndReattach(t *testing.T) {
	h, _ := newTestHost(t)
	att := newFakeAtt()
	stdin := att.captureStdin()
	run := domain.RunID("run-pass")
	if err := h.StartSession(context.Background(), run, att); err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	att.writeOutput(t, "alpha")
	a := startAttach(t, h, run, "m1", 120, 30, false)
	waitFor(t, "replay to first client", func() bool { return a.out.String() == "alpha" })

	att.writeOutput(t, "beta")
	waitFor(t, "live output to first client", func() bool { return a.out.String() == "alphabeta" })

	a.typeKeys(t, "ls\r")
	waitFor(t, "keystrokes on stdin", func() bool { return stdin.String() == "ls\r" })

	a.detach()
	if err := a.wait(t); err != nil {
		t.Fatalf("detach returned %v, want nil", err)
	}

	// The session survives with zero attachments; the agent never notices.
	att.writeOutput(t, "gamma")
	b := startAttach(t, h, run, "m2", 120, 30, false)
	waitFor(t, "replay to second client", func() bool { return b.out.String() == "alphabetagamma" })

	if got := stdin.String(); got != "ls\r" {
		t.Fatalf("agent stdin disturbed by attach/detach: %q", got)
	}
	if err := h.StopSession(context.Background(), run); err != nil {
		t.Fatalf("StopSession: %v", err)
	}
	if err := b.wait(t); err != nil {
		t.Fatalf("attach after stop returned %v, want nil", err)
	}
	if got := b.out.String(); got != "alphabetagamma" {
		t.Fatalf("second client output = %q", got)
	}
}

func TestGeometryClampAndRestore(t *testing.T) {
	h, dir := newTestHost(t)
	att := newFakeAtt()
	run := domain.RunID("run-geo")
	if err := h.StartSession(context.Background(), run, att); err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	waitSizes := func(n int) {
		t.Helper()
		waitFor(t, fmt.Sprintf("%d resize calls", n), func() bool { return len(att.sizeCalls()) == n })
	}
	waitSizes(1) // initial default geometry

	a := startAttach(t, h, run, "ma", 100, 40, false)
	waitSizes(3) // clamp to (100,40) with redraw nudge
	b := startAttach(t, h, run, "mb", 80, 50, false)
	waitSizes(5) // clamp to (80,40)

	ro := startAttach(t, h, run, "mv", 10, 5, true)
	waitAttached(t, h, run, 3)
	att.writeOutput(t, "x")
	waitFor(t, "read-only mirror output", func() bool { return ro.out.String() == "x" })
	if n := len(att.sizeCalls()); n != 5 {
		t.Fatalf("read-only attach changed geometry: %d resize calls", n)
	}

	b.resize <- [2]uint{90, 45} // min over writers becomes (90,40)
	waitSizes(7)

	b.detach()
	if err := b.wait(t); err != nil {
		t.Fatalf("detach b: %v", err)
	}
	waitSizes(9) // larger writer's size restored

	a.detach()
	if err := a.wait(t); err != nil {
		t.Fatalf("detach a: %v", err)
	}
	waitAttached(t, h, run, 1)
	if n := len(att.sizeCalls()); n != 9 {
		t.Fatalf("no-writer detach changed geometry: %d resize calls", n)
	}

	want := [][2]uint{
		{120, 30},            // StartSession default
		{100, 39}, {100, 40}, // writer a joins
		{80, 39}, {80, 40}, // writer b clamps
		{90, 39}, {90, 40}, // b resizes, min recomputed
		{100, 39}, {100, 40}, // b detaches, a's size restored
	}
	got := att.sizeCalls()
	if len(got) != len(want) {
		t.Fatalf("resize calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("resize call %d = %v, want %v (all: %v)", i, got[i], want[i], got)
		}
	}

	if err := h.StopSession(context.Background(), run); err != nil {
		t.Fatalf("StopSession: %v", err)
	}
	_, events := parseCast(t, dir+"/"+string(run)+".cast")
	var sizes []string
	for _, ev := range events {
		if ev.code == "r" {
			sizes = append(sizes, ev.data)
		}
	}
	wantSizes := []string{"100x40", "80x40", "90x40", "100x40"}
	if strings.Join(sizes, ",") != strings.Join(wantSizes, ",") {
		t.Fatalf("transcript resize events = %v, want %v", sizes, wantSizes)
	}
}

type blockedWriter struct{ release chan struct{} }

func (w *blockedWriter) Write(p []byte) (int, error) {
	<-w.release
	return len(p), nil
}

func TestSlowClientDoesNotBlockAgent(t *testing.T) {
	h, _ := newTestHost(t)
	att := newFakeAtt()
	run := domain.RunID("run-slow")
	if err := h.StartSession(context.Background(), run, att); err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	bw := &blockedWriter{release: make(chan struct{})}
	t.Cleanup(func() { close(bw.release) })
	kr, kw := io.Pipe()
	t.Cleanup(func() { _ = kw.Close(); _ = kr.Close() })
	errCh := make(chan error, 1)
	go func() {
		errCh <- h.Attach(context.Background(), run, "m1", 120, 30, false, &testConn{r: kr, w: bw}, nil)
	}()
	waitAttached(t, h, run, 1)

	chunk := bytes.Repeat([]byte("x"), 64*1024)
	agentDone := make(chan struct{})
	go func() {
		defer close(agentDone)
		for range 80 { // 5 MiB > maxClientBuffer
			if _, err := att.outW.Write(chunk); err != nil {
				return
			}
		}
	}()
	select {
	case <-agentDone:
	case <-time.After(10 * time.Second):
		t.Fatal("slow client blocked the agent")
	}
	select {
	case err := <-errCh:
		if !errors.Is(err, errSlowClient) {
			t.Fatalf("attach returned %v, want errSlowClient", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("slow client attach did not return")
	}
}

func TestInjectAndTranscriptReplay(t *testing.T) {
	h, dir := newTestHost(t)
	att := newFakeAtt()
	stdin := att.captureStdin()
	run := domain.RunID("run-cast")
	if err := h.StartSession(context.Background(), run, att); err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	a := startAttach(t, h, run, "m1", 120, 30, false)
	waitAttached(t, h, run, 1)

	att.writeOutput(t, "\x1b[1mBold\x1b[0m h\xc3") // é split across writes
	att.writeOutput(t, "\xa9llo ")
	waitFor(t, "output before inject", func() bool {
		return strings.HasSuffix(a.out.String(), "llo ")
	})
	if err := h.Inject(context.Background(), run, "Ana", "#ff8800", "fix the tests"); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	att.writeOutput(t, "after")

	wantLive := "\x1b[1mBold\x1b[0m héllo " +
		"\r\n\x1b[38;2;255;136;0m\x1b[7m ▸ Ana injects \x1b[0m fix the tests\r\n" +
		"after"
	waitFor(t, "full live stream", func() bool { return a.out.String() == wantLive })
	waitFor(t, "injected instruction on stdin", func() bool { return stdin.String() == "fix the tests\r" })

	if err := h.StopSession(context.Background(), run); err != nil {
		t.Fatalf("StopSession: %v", err)
	}
	if err := a.wait(t); err != nil {
		t.Fatalf("attach returned %v", err)
	}

	header, events := parseCast(t, dir+"/"+string(run)+".cast")
	if header.Version != 2 || header.Width != 120 || header.Height != 30 {
		t.Fatalf("header = %+v", header)
	}
	if header.Env["TERM"] != "xterm-256color" {
		t.Fatalf("header env = %v", header.Env)
	}
	var replay strings.Builder
	var markers []string
	last := -1.0
	for _, ev := range events {
		if ev.t < last {
			t.Fatalf("event timestamps not monotonic: %v", events)
		}
		last = ev.t
		switch ev.code {
		case "o":
			replay.WriteString(ev.data)
		case "m":
			markers = append(markers, ev.data)
		}
	}
	if replay.String() != wantLive {
		t.Fatalf("transcript replay = %q, want live bytes %q", replay.String(), wantLive)
	}
	if len(markers) != 1 || markers[0] != "inject by Ana: fix the tests" {
		t.Fatalf("markers = %v", markers)
	}
	// Injection reached the process input exactly once, banner excluded.
	if got := stdin.String(); got != "fix the tests\r" {
		t.Fatalf("stdin = %q, want single injected message", got)
	}
}

func TestWriteGate(t *testing.T) {
	gateErr := errors.New("not allowed")
	h, _ := newTestHost(t, func(cfg *Config) {
		cfg.Gate = func(_ context.Context, member domain.MemberID, _ domain.RunID) error {
			if member == "bad" {
				return gateErr
			}
			return nil
		}
	})
	att := newFakeAtt()
	run := domain.RunID("run-gate")
	if err := h.StartSession(context.Background(), run, att); err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	err := h.Attach(context.Background(), run, "bad", 80, 24, false, &testConn{r: strings.NewReader(""), w: &sink{}}, nil)
	if !errors.Is(err, ErrWriteDenied) {
		t.Fatalf("write attach = %v, want ErrWriteDenied", err)
	}

	// Read-only mirror is not gated; write access for an allowed member is.
	ro := startAttach(t, h, run, "bad", 80, 24, true)
	waitAttached(t, h, run, 1)
	ro.detach()
	if err := ro.wait(t); err != nil {
		t.Fatalf("read-only attach = %v", err)
	}
	wr := startAttach(t, h, run, "good", 80, 24, false)
	waitAttached(t, h, run, 1)
	wr.detach()
	if err := wr.wait(t); err != nil {
		t.Fatalf("allowed write attach = %v", err)
	}
}

func TestLifecycleErrorsAndLastOutput(t *testing.T) {
	h, _ := newTestHost(t)
	ctx := context.Background()
	if err := h.Attach(ctx, "nope", "m", 80, 24, false, &testConn{r: strings.NewReader(""), w: &sink{}}, nil); !errors.Is(err, ErrNoSession) {
		t.Fatalf("Attach unknown run = %v", err)
	}
	if err := h.Inject(ctx, "nope", "a", "", "hi"); !errors.Is(err, ErrNoSession) {
		t.Fatalf("Inject unknown run = %v", err)
	}
	if err := h.StopSession(ctx, "nope"); !errors.Is(err, ErrNoSession) {
		t.Fatalf("StopSession unknown run = %v", err)
	}
	if _, ok := h.LastOutput("nope"); ok {
		t.Fatal("LastOutput for unknown run reported a session")
	}

	att := newFakeAtt()
	run := domain.RunID("run-life")
	if err := h.StartSession(ctx, run, att); err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if err := h.StartSession(ctx, run, newFakeAtt()); err == nil {
		t.Fatal("duplicate StartSession succeeded")
	}
	if ts, ok := h.LastOutput(run); !ok || !ts.IsZero() {
		t.Fatalf("LastOutput before output = %v, %v", ts, ok)
	}
	before := time.Now()
	att.writeOutput(t, "hi")
	waitFor(t, "last output updates", func() bool {
		ts, ok := h.LastOutput(run)
		return ok && !ts.Before(before)
	})

	a := startAttach(t, h, run, "m1", 80, 24, false)
	waitAttached(t, h, run, 1)

	// Agent exits: attachments drain to EOF; session ends but stays
	// queryable until StopSession.
	_ = att.outW.Close()
	if err := a.wait(t); err != nil {
		t.Fatalf("attach at session end = %v, want nil", err)
	}
	if got := a.out.String(); got != "hi" {
		t.Fatalf("client output = %q", got)
	}
	waitFor(t, "session end", func() bool {
		err := h.Inject(ctx, run, "a", "", "x")
		return errors.Is(err, ErrSessionEnded)
	})
	if err := h.Attach(ctx, run, "m2", 80, 24, false, &testConn{r: strings.NewReader(""), w: &sink{}}, nil); !errors.Is(err, ErrSessionEnded) {
		t.Fatalf("Attach after end = %v, want ErrSessionEnded", err)
	}
	if _, ok := h.LastOutput(run); !ok {
		t.Fatal("LastOutput false for ended-but-not-stopped session")
	}

	if err := h.StopSession(ctx, run); err != nil {
		t.Fatalf("StopSession: %v", err)
	}
	if err := h.StopSession(ctx, run); err != nil {
		t.Fatalf("StopSession is not idempotent: %v", err)
	}
	if _, ok := h.LastOutput(run); ok {
		t.Fatal("LastOutput true after StopSession")
	}
	if err := h.Inject(ctx, run, "a", "", "x"); !errors.Is(err, ErrNoSession) {
		t.Fatalf("Inject after stop = %v, want ErrNoSession", err)
	}
}

func TestAttachContextCancel(t *testing.T) {
	h, _ := newTestHost(t)
	att := newFakeAtt()
	run := domain.RunID("run-ctx")
	if err := h.StartSession(context.Background(), run, att); err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	kr, kw := io.Pipe()
	t.Cleanup(func() { _ = kw.Close(); _ = kr.Close() })
	errCh := make(chan error, 1)
	go func() {
		errCh <- h.Attach(ctx, run, "m1", 80, 24, false, &testConn{r: kr, w: &sink{}}, nil)
	}()
	waitAttached(t, h, run, 1)
	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("attach = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("attach did not return on ctx cancel")
	}
	waitAttached(t, h, run, 0)
}

// blockingResizeAtt simulates a hung runtime resize RPC (e.g. a stuck
// Docker daemon): Resize blocks until released.
type blockingResizeAtt struct {
	*fakeAtt
	block   chan struct{}
	entered chan struct{}
}

func (a *blockingResizeAtt) Resize(ctx context.Context, cols, rows uint) error {
	select {
	case a.entered <- struct{}{}:
	default:
	}
	<-a.block
	return a.fakeAtt.Resize(ctx, cols, rows)
}

func TestBlockingResizeDoesNotStallSession(t *testing.T) {
	h, _ := newTestHost(t)
	att := &blockingResizeAtt{
		fakeAtt: newFakeAtt(),
		block:   make(chan struct{}, 16),
		entered: make(chan struct{}, 16),
	}
	release := sync.OnceFunc(func() { close(att.block) })
	defer release()
	att.block <- struct{}{} // let the initial StartSession resize through
	run := domain.RunID("run-hung-resize")
	if err := h.StartSession(context.Background(), run, att); err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	<-att.entered
	_ = att.captureStdin() // drain inject writes

	ro := startAttach(t, h, run, "mv", 200, 60, true)
	waitAttached(t, h, run, 1)

	// A write-attach reconciles geometry; the resize application hangs in
	// the runtime, and must not stall anything else.
	go func() {
		_ = h.Attach(context.Background(), run, "mw", 100, 40, false,
			&testConn{r: strings.NewReader(""), w: &sink{}}, nil)
	}()
	<-att.entered // resize applier is now wedged inside att.Resize

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, ok := h.LastOutput(run); !ok {
			t.Error("LastOutput lost the session")
		}
		att.writeOutput(t, "still-flowing")
		if err := h.Inject(context.Background(), run, "Ana", "", "hi"); err != nil {
			t.Errorf("Inject: %v", err)
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		release()
		t.Fatal("session operations stalled behind a hung att.Resize")
	}
	waitFor(t, "output delivered while resize hung", func() bool {
		return strings.Contains(ro.out.String(), "still-flowing")
	})
	release()
	if err := h.StopSession(context.Background(), run); err != nil {
		t.Fatalf("StopSession: %v", err)
	}
}

// stuckWriter accepts no writes: the first Write parks forever, as a client
// with a permanently full transport window would.
type stuckWriter struct {
	stuck chan struct{}
	freed chan struct{}
}

func (w *stuckWriter) Write(p []byte) (int, error) {
	select {
	case w.stuck <- struct{}{}:
	default:
	}
	<-w.freed
	return len(p), nil
}

func TestAttachDrainHonorsContext(t *testing.T) {
	h, _ := newTestHost(t)
	att := newFakeAtt()
	run := domain.RunID("run-drain")
	if err := h.StartSession(context.Background(), run, att); err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	kr, kw := io.Pipe()
	t.Cleanup(func() { _ = kw.Close(); _ = kr.Close() })
	sw := &stuckWriter{stuck: make(chan struct{}, 1), freed: make(chan struct{})}
	t.Cleanup(func() { close(sw.freed) })
	errCh := make(chan error, 1)
	go func() {
		errCh <- h.Attach(ctx, run, "m1", 80, 24, false, &testConn{r: kr, w: sw}, nil)
	}()
	waitAttached(t, h, run, 1)
	att.writeOutput(t, "wedge")
	<-sw.stuck // write loop is parked in conn.Write
	att.writeOutput(t, "buffered")
	if err := h.StopSession(context.Background(), run); err != nil {
		t.Fatalf("StopSession: %v", err)
	}
	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Attach = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Attach did not return after ctx cancel while draining to a stuck client")
	}
}

func TestConcurrentDuplicateStartSessionPreservesTranscript(t *testing.T) {
	h, dir := newTestHost(t)
	run := domain.RunID("run-dup")
	atts := [2]*fakeAtt{newFakeAtt(), newFakeAtt()}
	var results [2]error
	var wg sync.WaitGroup
	for i := range atts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = h.StartSession(context.Background(), run, atts[i])
		}()
	}
	wg.Wait()
	winnerIdx := -1
	for i, err := range results {
		if err == nil {
			winnerIdx = i
		}
	}
	if winnerIdx < 0 || results[1-winnerIdx] == nil {
		t.Fatalf("StartSession results = %v, want exactly one winner", results)
	}
	atts[winnerIdx].writeOutput(t, "winner-output")
	waitFor(t, "winner output recorded", func() bool {
		ts, ok := h.LastOutput(run)
		return ok && !ts.IsZero()
	})
	if err := h.StopSession(context.Background(), run); err != nil {
		t.Fatalf("StopSession: %v", err)
	}
	_, events := parseCast(t, dir+"/"+string(run)+".cast")
	var out strings.Builder
	for _, ev := range events {
		if ev.code == "o" {
			out.WriteString(ev.data)
		}
	}
	if out.String() != "winner-output" {
		t.Fatalf("winner transcript = %q, want %q", out.String(), "winner-output")
	}
}

func TestStoppedSessionReleasesBuffers(t *testing.T) {
	h, _ := newTestHost(t)
	att := newFakeAtt()
	run := domain.RunID("run-mem")
	if err := h.StartSession(context.Background(), run, att); err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	att.writeOutput(t, "scrollback")
	if err := h.StopSession(context.Background(), run); err != nil {
		t.Fatalf("StopSession: %v", err)
	}
	s := h.lookup(run)
	if s == nil {
		t.Fatal("stopped session must stay in the map")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ring != nil || s.clients != nil {
		t.Fatalf("stopped session retains buffers: ring=%v clients=%v", s.ring != nil, s.clients != nil)
	}
}

func TestHostCloseStopsSessions(t *testing.T) {
	h, _ := newTestHost(t)
	att := newFakeAtt()
	run := domain.RunID("run-close")
	if err := h.StartSession(context.Background(), run, att); err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	a := startAttach(t, h, run, "m1", 80, 24, false)
	waitAttached(t, h, run, 1)
	if err := h.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := a.wait(t); err != nil {
		t.Fatalf("attach after host close = %v, want nil", err)
	}
	att.mu.Lock()
	closed := att.closed
	att.mu.Unlock()
	if !closed {
		t.Fatal("host close did not close the attachment")
	}
	if err := h.StartSession(context.Background(), domain.RunID("run-new"), newFakeAtt()); err == nil {
		t.Fatal("StartSession on closed host succeeded")
	}
}
