package ptyhost

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	goruntime "runtime"
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
		StartSession(ctx context.Context, key SessionKey, att runtime.Attachment) error
		StopSession(ctx context.Context, key SessionKey) error
		LastOutput(key SessionKey) (time.Time, bool)
		Inject(ctx context.Context, key SessionKey, actorName, actorColor, message, submit string) error
	}
	sshdPTYAttacher interface {
		Attach(ctx context.Context, key SessionKey, client AttachClient, conn io.ReadWriter, resize <-chan [2]uint) error
		Replay(run domain.RunID) (io.ReadCloser, error)
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
	// resizeErr is what the runtime answers a resize with: a Docker
	// daemon that refuses one, or takes longer than resizeTimeout.
	resizeErr error
	closed    bool
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
	return a.resizeErr
}

// refuseResizes makes the runtime answer every resize with err until it is
// called again with nil.
func (a *fakeAtt) refuseResizes(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.resizeErr = err
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

type replayConn struct {
	r io.Reader

	mu      sync.Mutex
	replays [][]byte
	writes  [][]byte
}

func (c *replayConn) Read(p []byte) (int, error) { return c.r.Read(p) }

func (c *replayConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writes = append(c.writes, append([]byte(nil), p...))
	return len(p), nil
}

func (c *replayConn) WriteReplay(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.replays = append(c.replays, append([]byte(nil), p...))
	return len(p), nil
}

func (c *replayConn) replayBytes() [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([][]byte, len(c.replays))
	for i := range c.replays {
		out[i] = append([]byte(nil), c.replays[i]...)
	}
	return out
}

func (c *replayConn) writeBytes() [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([][]byte, len(c.writes))
	for i := range c.writes {
		out[i] = append([]byte(nil), c.writes[i]...)
	}
	return out
}

var _ ReplayWriter = (*replayConn)(nil)

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
		hnd.errCh <- h.Attach(context.Background(), RunSession(run), AttachClient{Member: member, Cols: cols, Rows: rows, ReadOnly: readOnly}, &testConn{r: kr, w: hnd.out}, hnd.resize)
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
	s := h.lookup(RunSession(run))
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
	if err := h.StartSession(context.Background(), RunSession(run), att); err != nil {
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
	if err := h.StopSession(context.Background(), RunSession(run)); err != nil {
		t.Fatalf("StopSession: %v", err)
	}
	if err := b.wait(t); err != nil {
		t.Fatalf("attach after stop returned %v, want nil", err)
	}
	if got := b.out.String(); got != "alphabetagamma" {
		t.Fatalf("second client output = %q", got)
	}
}

func TestAttachReplayWriterReceivesTailBeforeLiveOutput(t *testing.T) {
	h, _ := newTestHost(t)
	att := newFakeAtt()
	run := domain.RunID("run-replay-writer")
	if err := h.StartSession(context.Background(), RunSession(run), att); err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	att.writeOutput(t, "scrollback")
	// The pump delivers output asynchronously; a plain attach observing the
	// bytes proves they reached the ring before the ReplayWriter attaches.
	probe := startAttach(t, h, run, "probe", 80, 24, true)
	waitFor(t, "scrollback in ring", func() bool { return probe.out.String() == "scrollback" })
	probe.detach()
	if err := probe.wait(t); err != nil {
		t.Fatalf("probe detach returned %v, want nil", err)
	}

	kr, kw := io.Pipe()
	conn := &replayConn{r: kr}
	resize := make(chan [2]uint)
	errCh := make(chan error, 1)
	go func() {
		errCh <- h.Attach(context.Background(), RunSession(run), AttachClient{Member: "member", Cols: 80, Rows: 24, ReadOnly: true}, conn, resize)
	}()
	t.Cleanup(func() {
		_ = kw.Close()
		_ = kr.Close()
	})

	waitFor(t, "replay callback", func() bool { return len(conn.replayBytes()) == 1 })
	replays := conn.replayBytes()
	if got := string(replays[0]); got != "scrollback" {
		t.Fatalf("replay = %q, want scrollback", got)
	}

	att.writeOutput(t, " live")
	waitFor(t, "live output", func() bool { return len(conn.writeBytes()) == 1 })
	writes := conn.writeBytes()
	if got := string(writes[0]); got != " live" {
		t.Fatalf("live output = %q, want %q", got, " live")
	}
	if len(conn.replayBytes()) != 1 {
		t.Fatalf("replay callback count = %d, want 1", len(conn.replayBytes()))
	}

	_ = kw.Close()
	if err := <-errCh; err != nil {
		t.Fatalf("attach returned %v, want nil", err)
	}
}

func TestGeometryClampAndRestore(t *testing.T) {
	h, dir := newTestHost(t)
	att := newFakeAtt()
	run := domain.RunID("run-geo")
	if err := h.StartSession(context.Background(), RunSession(run), att); err != nil {
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

	if err := h.StopSession(context.Background(), RunSession(run)); err != nil {
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
	if err := h.StartSession(context.Background(), RunSession(run), att); err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	bw := &blockedWriter{release: make(chan struct{})}
	t.Cleanup(func() { close(bw.release) })
	kr, kw := io.Pipe()
	t.Cleanup(func() { _ = kw.Close(); _ = kr.Close() })
	errCh := make(chan error, 1)
	go func() {
		errCh <- h.Attach(context.Background(), RunSession(run), AttachClient{Member: "m1", Cols: 120, Rows: 30}, &testConn{r: kr, w: bw}, nil)
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
	if err := h.StartSession(context.Background(), RunSession(run), att); err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	a := startAttach(t, h, run, "m1", 120, 30, false)
	waitAttached(t, h, run, 1)

	att.writeOutput(t, "\x1b[1mBold\x1b[0m h\xc3") // é split across writes
	att.writeOutput(t, "\xa9llo ")
	waitFor(t, "output before inject", func() bool {
		return strings.HasSuffix(a.out.String(), "llo ")
	})
	if err := h.Inject(context.Background(), RunSession(run), "Ana", "#ff8800", "fix the tests", "\r"); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	att.writeOutput(t, "after")

	wantLive := "\x1b[1mBold\x1b[0m héllo " +
		"\r\n\x1b[38;2;255;136;0m\x1b[7m ▸ Ana injects \x1b[0m fix the tests\r\n" +
		"after"
	waitFor(t, "full live stream", func() bool { return a.out.String() == wantLive })
	waitFor(t, "injected instruction on stdin", func() bool { return stdin.String() == "fix the tests\r" })

	if err := h.StopSession(context.Background(), RunSession(run)); err != nil {
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

type funcWriter struct {
	write func([]byte) (int, error)
}

func (f funcWriter) Write(p []byte) (int, error) { return f.write(p) }

func (funcWriter) Close() error { return nil }

// A session that ends while the injected line is still being written has
// already accepted the full line: Inject must report success, not an
// error that invites a double-submitting retry, and the transcript must
// still carry the attribution the scheduler records.
func TestInjectDeliveredReportsSuccessWhenSessionEndsDuringWrite(t *testing.T) {
	h, dir := newTestHost(t)
	att := newFakeAtt()
	run := domain.RunID("run-endwin")
	if err := h.StartSession(context.Background(), RunSession(run), att); err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	sess := h.lookup(RunSession(run))
	var written []byte
	sess.stdin = funcWriter{write: func(p []byte) (int, error) {
		written = append(written, p...)
		sess.end() // the attach loop observes the agent's exit mid-write
		return len(p), nil
	}}

	if err := h.Inject(context.Background(), RunSession(run), "Ana", "#ff8800", "ship it", "\r"); err != nil {
		t.Fatalf("Inject after delivery = %v, want nil", err)
	}
	if string(written) != "ship it\r" {
		t.Fatalf("stdin = %q, want the delivered line exactly once", written)
	}
	_, events := parseCast(t, dir+"/"+string(run)+".cast")
	var markers []string
	for _, ev := range events {
		if ev.code == "m" {
			markers = append(markers, ev.data)
		}
	}
	if len(markers) != 1 || markers[0] != "inject by Ana: ship it" {
		t.Fatalf("markers = %v, want the late attribution recorded", markers)
	}
}

type echoStep struct {
	out  string
	want bool
}

// TestEchoExpectation covers the shapes the terminal's echo actually takes.
// The line discipline rewrites lone CRs and LFs as CRLF and renders control
// bytes in hat notation, so the expectation the session builds has to match
// what comes back rather than what was written - a shape it gets wrong is a
// shape where steering a hung agent still refreshes its liveness clock.
func TestEchoExpectation(t *testing.T) {
	start := time.Now()
	cases := []struct {
		name   string
		writes []string      // what the server wrote to the agent's stdin
		delay  time.Duration // how long the terminal took to answer
		steps  []echoStep
	}{
		{
			name:   "line echoes as CRLF",
			writes: []string{"wake\r"},
			steps:  []echoStep{{"wake\r\n", false}, {"got:wake\r\n", true}},
		},
		{
			name:   "echo split across reads",
			writes: []string{"wake\r"},
			steps:  []echoStep{{"wa", false}, {"ke\r\n", false}, {"got:wake\r\n", true}},
		},
		{
			// A terminal with either translation turned off, and an editing
			// character the line editor repaints instead of echoing, all
			// send back something the expectation does not predict. They
			// take the divergence path: one more threshold at worst, and
			// never a missed hang.
			name:   "terminal without ONLCR echoes a bare LF",
			writes: []string{"wake\r"},
			steps:  []echoStep{{"wake\n", true}},
		},
		{
			name:   "terminal without ICRNL echoes the CR in hat notation",
			writes: []string{"wake\r"},
			steps:  []echoStep{{"wake^M", true}},
		},
		{
			name:   "DEL is repainted by the line editor, not echoed",
			writes: []string{"a\x7fb\r"},
			steps:  []echoStep{{"a\x08 \x08b\r\n", true}},
		},
		{
			name:   "multi-line steer echoes every newline as CRLF",
			writes: []string{"wake\nup\r"},
			steps:  []echoStep{{"wake\r\nup\r\n", false}, {"got:wake\r\n", true}},
		},
		{
			name:   "control byte echoes in hat notation",
			writes: []string{"a\x01b\r"},
			steps:  []echoStep{{"a^Ab\r\n", false}, {"got\r\n", true}},
		},
		{
			name:   "tab echoes as itself",
			writes: []string{"a\tb\r"},
			steps:  []echoStep{{"a\tb\r\n", false}},
		},
		{
			name:   "queued writes echo in order",
			writes: []string{"one\r", "two\r"},
			steps:  []echoStep{{"one\r\ntwo\r\n", false}, {"got\r\n", true}},
		},
		{
			name:   "divergent output is the agent",
			writes: []string{"wake\r"},
			steps:  []echoStep{{"thinking...\r\n", true}},
		},
		{
			name:   "echo and answer in one chunk",
			writes: []string{"wake\r"},
			steps:  []echoStep{{"wake\r\ngot:wake\r\n", true}},
		},
		{
			name:   "an echo that never came expires",
			writes: []string{"wake\r"},
			delay:  2 * echoWindow,
			steps:  []echoStep{{"wake\r\n", true}},
		},
		{
			name:   "an expectation past the cap is dropped whole",
			writes: []string{strings.Repeat("x", maxPendingEcho) + "\r"},
			steps:  []echoStep{{"xxx", true}},
		},
		{
			name:  "no expectation at all",
			steps: []echoStep{{"thinking...\r\n", true}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &session{}
			for _, w := range tc.writes {
				s.expectEcho([]byte(w), start)
			}
			now := start.Add(tc.delay)
			for i, step := range tc.steps {
				if got := s.consumeEcho([]byte(step.out), now); got != step.want {
					t.Fatalf("step %d: consumeEcho(%q) = %v, want %v", i, step.out, got, step.want)
				}
			}
		})
	}
}

// TestInjectIsNotAgentOutput pins the liveness clock stall detection reads,
// end to end through the host. A steer puts two lots of bytes on the PTY
// that the agent did not write: the banner, and the line discipline's echo
// of the injected line, which arrives even when the agent never reads its
// stdin. Keystrokes typed on an attach echo the same way. None of it may
// refresh LastOutput, or steering a hung agent would clear its stall.
func TestInjectIsNotAgentOutput(t *testing.T) {
	h, _ := newTestHost(t)
	att := newFakeAtt()
	stdin := att.captureStdin()
	run := domain.RunID("run-liveness")
	origQuiet := paintQuiet
	paintQuiet = time.Millisecond
	t.Cleanup(func() { paintQuiet = origQuiet })
	if err := h.StartSession(context.Background(), RunSession(run), att); err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	a := startAttach(t, h, run, "m1", 120, 30, false)
	waitAttached(t, h, run, 1)
	// Wait for the attach repaint's suppression window to finish so it
	// cannot mask the echo assertions below.
	sess := h.lookup(RunSession(run))
	waitFor(t, "attach repaint quiet window elapsed", func() bool {
		sess.mu.Lock()
		defer sess.mu.Unlock()
		return !sess.paintQuietUntil.IsZero() && time.Now().After(sess.paintQuietUntil)
	})

	att.writeOutput(t, "thinking...\r\n")
	waitFor(t, "agent output before the steer", func() bool { return strings.HasSuffix(a.out.String(), "thinking...\r\n") })
	before, ok := h.LastOutput(RunSession(run))
	if !ok || before.IsZero() {
		t.Fatalf("LastOutput = %v, %v after agent output", before, ok)
	}
	unmoved := func(t *testing.T, what string) {
		t.Helper()
		if ts, _ := h.LastOutput(RunSession(run)); !ts.Equal(before) {
			t.Fatalf("LastOutput moved from %v to %v across %s", before, ts, what)
		}
	}

	// A single-line steer, its echo split across reads as a real PTY
	// delivers it.
	if err := h.Inject(context.Background(), RunSession(run), "Ana", "#ff8800", "wake", "\r"); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	att.writeOutput(t, "wa")
	att.writeOutput(t, "ke\r\n")
	waitFor(t, "banner and echo on the stream", func() bool { return strings.HasSuffix(a.out.String(), "wake\r\n") })
	unmoved(t, "a steer the agent never answered")

	// The dashboard's steer box is a textarea, so a message can carry
	// interior newlines; every one of them echoes back as CRLF.
	if err := h.Inject(context.Background(), RunSession(run), "Ana", "#ff8800", "wake\nup", "\r"); err != nil {
		t.Fatalf("Inject multi-line: %v", err)
	}
	att.writeOutput(t, "wake\r\nup\r\n")
	waitFor(t, "multi-line echo on the stream", func() bool { return strings.HasSuffix(a.out.String(), "up\r\n") })
	unmoved(t, "a multi-line steer the agent never answered")

	// Typing on an attach reaches the same stdin and echoes the same way.
	if _, err := a.keys.Write([]byte("hi\r")); err != nil {
		t.Fatalf("write keystrokes: %v", err)
	}
	waitFor(t, "keystrokes on the agent's stdin", func() bool { return strings.HasSuffix(stdin.String(), "hi\r") })
	att.writeOutput(t, "hi\r\n")
	waitFor(t, "keystroke echo on the stream", func() bool { return strings.HasSuffix(a.out.String(), "hi\r\n") })
	unmoved(t, "keystrokes the agent never answered")

	// The agent's own answer is what moves it.
	att.writeOutput(t, "got:wake\r\n")
	waitFor(t, "LastOutput follows the agent", func() bool {
		ts, _ := h.LastOutput(RunSession(run))
		return ts.After(before)
	})

	if err := h.StopSession(context.Background(), RunSession(run)); err != nil {
		t.Fatalf("StopSession: %v", err)
	}
	if err := a.wait(t); err != nil {
		t.Fatalf("attach returned %v", err)
	}
}

// TestAttachNudgeDoesNotClockLiveness pins the paint-quiet window: the
// redraw nudge every write-capable attach fires makes TUI agents repaint,
// and that repaint must not read as the agent answering. Without it,
// opening a stale run's terminal flips it back to working.
func TestAttachNudgeDoesNotClockLiveness(t *testing.T) {
	h, _ := newTestHost(t)
	att := newFakeAtt()
	run := domain.RunID("run-nudge")
	origQuiet := paintQuiet
	paintQuiet = 50 * time.Millisecond
	t.Cleanup(func() { paintQuiet = origQuiet })
	if err := h.StartSession(context.Background(), RunSession(run), att); err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	a := startAttach(t, h, run, "m1", 120, 30, false)
	waitAttached(t, h, run, 1)
	sess := h.lookup(RunSession(run))
	waitFor(t, "nudge armed the paint-quiet window", func() bool {
		sess.mu.Lock()
		defer sess.mu.Unlock()
		return !sess.paintQuietUntil.IsZero()
	})

	// The agent's repaint answer, well inside the quiet window.
	att.writeOutput(t, "repaint\r\n")
	if ts, _ := h.LastOutput(RunSession(run)); !ts.IsZero() {
		t.Fatalf("LastOutput = %v during the paint-quiet window, want zero", ts)
	}
	waitFor(t, "quiet window elapsed", func() bool {
		return time.Now().After(sess.paintQuietUntil)
	})
	att.writeOutput(t, "real answer\r\n")
	waitFor(t, "post-window output clocks liveness", func() bool {
		ts, _ := h.LastOutput(RunSession(run))
		return !ts.IsZero()
	})
	_ = a
}

func TestWriteGate(t *testing.T) {
	gateErr := errors.New("not allowed")
	h, _ := newTestHost(t, func(cfg *Config) {
		cfg.Gate = func(_ context.Context, member domain.MemberID, _ SessionKey) error {
			if member == "bad" {
				return gateErr
			}
			return nil
		}
	})
	att := newFakeAtt()
	run := domain.RunID("run-gate")
	if err := h.StartSession(context.Background(), RunSession(run), att); err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	err := h.Attach(context.Background(), RunSession(run), AttachClient{Member: "bad", Cols: 80, Rows: 24}, &testConn{r: strings.NewReader(""), w: &sink{}}, nil)
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
	if err := h.Attach(ctx, SessionKey("nope"), AttachClient{Member: "m", Cols: 80, Rows: 24}, &testConn{r: strings.NewReader(""), w: &sink{}}, nil); !errors.Is(err, ErrNoSession) {
		t.Fatalf("Attach unknown run = %v", err)
	}
	if err := h.Inject(ctx, SessionKey("nope"), "a", "", "hi", "\r"); !errors.Is(err, ErrNoSession) {
		t.Fatalf("Inject unknown run = %v", err)
	}
	if err := h.StopSession(ctx, SessionKey("nope")); !errors.Is(err, ErrNoSession) {
		t.Fatalf("StopSession unknown run = %v", err)
	}
	if _, ok := h.LastOutput(SessionKey("nope")); ok {
		t.Fatal("LastOutput for unknown run reported a session")
	}

	att := newFakeAtt()
	run := domain.RunID("run-life")
	if err := h.StartSession(ctx, RunSession(run), att); err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if err := h.StartSession(ctx, RunSession(run), newFakeAtt()); err == nil {
		t.Fatal("duplicate StartSession succeeded")
	}
	if ts, ok := h.LastOutput(RunSession(run)); !ok || !ts.IsZero() {
		t.Fatalf("LastOutput before output = %v, %v", ts, ok)
	}
	before := time.Now()
	att.writeOutput(t, "hi")
	waitFor(t, "last output updates", func() bool {
		ts, ok := h.LastOutput(RunSession(run))
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
		err := h.Inject(ctx, RunSession(run), "a", "", "x", "\r")
		return errors.Is(err, ErrSessionEnded)
	})
	if err := h.Attach(ctx, RunSession(run), AttachClient{Member: "m2", Cols: 80, Rows: 24}, &testConn{r: strings.NewReader(""), w: &sink{}}, nil); !errors.Is(err, ErrSessionEnded) {
		t.Fatalf("Attach after end = %v, want ErrSessionEnded", err)
	}
	if _, ok := h.LastOutput(RunSession(run)); !ok {
		t.Fatal("LastOutput false for ended-but-not-stopped session")
	}

	if err := h.StopSession(ctx, RunSession(run)); err != nil {
		t.Fatalf("StopSession: %v", err)
	}
	if err := h.StopSession(ctx, RunSession(run)); err != nil {
		t.Fatalf("StopSession is not idempotent: %v", err)
	}
	if _, ok := h.LastOutput(RunSession(run)); ok {
		t.Fatal("LastOutput true after StopSession")
	}
	if err := h.Inject(ctx, RunSession(run), "a", "", "x", "\r"); !errors.Is(err, ErrNoSession) {
		t.Fatalf("Inject after stop = %v, want ErrNoSession", err)
	}
}

func TestAttachContextCancel(t *testing.T) {
	h, _ := newTestHost(t)
	att := newFakeAtt()
	run := domain.RunID("run-ctx")
	if err := h.StartSession(context.Background(), RunSession(run), att); err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	kr, kw := io.Pipe()
	t.Cleanup(func() { _ = kw.Close(); _ = kr.Close() })
	errCh := make(chan error, 1)
	go func() {
		errCh <- h.Attach(ctx, RunSession(run), AttachClient{Member: "m1", Cols: 80, Rows: 24}, &testConn{r: kr, w: &sink{}}, nil)
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
	if err := h.StartSession(context.Background(), RunSession(run), att); err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	<-att.entered
	_ = att.captureStdin() // drain inject writes

	ro := startAttach(t, h, run, "mv", 200, 60, true)
	waitAttached(t, h, run, 1)

	// A write-attach reconciles geometry; the resize application hangs in
	// the runtime, and must not stall anything else.
	go func() {
		_ = h.Attach(context.Background(), RunSession(run), AttachClient{Member: "mw", Cols: 100, Rows: 40}, &testConn{r: strings.NewReader(""), w: &sink{}}, nil)
	}()
	<-att.entered // resize applier is now wedged inside att.Resize

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, ok := h.LastOutput(RunSession(run)); !ok {
			t.Error("LastOutput lost the session")
		}
		att.writeOutput(t, "still-flowing")
		if err := h.Inject(context.Background(), RunSession(run), "Ana", "", "hi", "\r"); err != nil {
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
	if err := h.StopSession(context.Background(), RunSession(run)); err != nil {
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
	if err := h.StartSession(context.Background(), RunSession(run), att); err != nil {
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
		errCh <- h.Attach(ctx, RunSession(run), AttachClient{Member: "m1", Cols: 80, Rows: 24}, &testConn{r: kr, w: sw}, nil)
	}()
	waitAttached(t, h, run, 1)
	att.writeOutput(t, "wedge")
	<-sw.stuck // write loop is parked in conn.Write
	att.writeOutput(t, "buffered")
	if err := h.StopSession(context.Background(), RunSession(run)); err != nil {
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

// TestActiveSessionsDoesNotBlockUnrelatedAttach covers the host-lock handoff:
// a session state read may wait, but it must not keep unrelated lookup and
// attach operations from progressing.
func TestActiveSessionsDoesNotBlockUnrelatedAttach(t *testing.T) {
	h, _ := newTestHost(t)
	blockedKey := RunShellSession("r-lock", "blocked")
	otherKey := RunShellSession("r-lock", "other")
	if err := h.StartSession(context.Background(), blockedKey, newFakeAtt()); err != nil {
		t.Fatalf("start blocked session: %v", err)
	}
	if err := h.StartSession(context.Background(), otherKey, newFakeAtt()); err != nil {
		t.Fatalf("start other session: %v", err)
	}
	blocked := h.lookup(blockedKey)
	blocked.mu.Lock()
	released := false
	defer func() {
		if !released {
			blocked.mu.Unlock()
		}
	}()

	activeDone := make(chan []SessionKey, 1)
	h.mu.Lock()
	go func() {
		activeDone <- h.ActiveSessions("run-shell:r-lock:")
	}()
	for range 100 {
		goruntime.Gosched()
	}
	h.mu.Unlock()
	// Give the old implementation's host-locked state read time to acquire
	// h.mu before the unrelated operation starts.
	time.Sleep(2 * time.Millisecond)

	attachDone := make(chan error, 1)
	go func() {
		attachDone <- h.Attach(context.Background(), otherKey, "m-other", 80, 24, true,
			&testConn{r: strings.NewReader(""), w: &sink{}}, nil)
	}()
	select {
	case err := <-attachDone:
		if err != nil {
			t.Fatalf("unrelated Attach: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("unrelated Attach blocked by another session's state read")
	}

	blocked.mu.Unlock()
	released = true
	select {
	case <-activeDone:
	case <-time.After(time.Second):
		t.Fatal("ActiveSessions did not finish after releasing the session lock")
	}
}

// TestReserveDoesNotBlockUnrelatedStart covers the same lock ordering for
// duplicate StartSession checks: reserve must not hold h.mu while inspecting
// an existing session.
func TestReserveDoesNotBlockUnrelatedStart(t *testing.T) {
	h, _ := newTestHost(t)
	blockedKey := RunShellSession("r-reserve", "blocked")
	otherKey := RunShellSession("r-reserve", "other")
	if err := h.StartSession(context.Background(), blockedKey, newFakeAtt()); err != nil {
		t.Fatalf("start blocked session: %v", err)
	}
	if err := h.StartSession(context.Background(), otherKey, newFakeAtt()); err != nil {
		t.Fatalf("start other session: %v", err)
	}
	blocked := h.lookup(blockedKey)
	blocked.mu.Lock()
	released := false
	defer func() {
		if !released {
			blocked.mu.Unlock()
		}
	}()

	reserveDone := make(chan error, 1)
	h.mu.Lock()
	go func() {
		reserveDone <- h.StartSession(context.Background(), blockedKey, newFakeAtt())
	}()
	for range 100 {
		goruntime.Gosched()
	}
	h.mu.Unlock()
	time.Sleep(2 * time.Millisecond)

	otherStartDone := make(chan error, 1)
	go func() {
		otherStartDone <- h.StartSession(context.Background(), otherKey, newFakeAtt())
	}()
	select {
	case err := <-otherStartDone:
		if err == nil {
			t.Fatal("unrelated duplicate StartSession succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("unrelated StartSession blocked by another session's state read")
	}

	blocked.mu.Unlock()
	released = true
	select {
	case err := <-reserveDone:
		if err == nil {
			t.Fatal("blocked duplicate StartSession succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("duplicate StartSession did not finish after releasing the session lock")
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
			results[i] = h.StartSession(context.Background(), RunSession(run), atts[i])
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
		ts, ok := h.LastOutput(RunSession(run))
		return ok && !ts.IsZero()
	})
	if err := h.StopSession(context.Background(), RunSession(run)); err != nil {
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

func TestStoppedSessionClosesAttachmentAndPreservesReplay(t *testing.T) {
	h, _ := newTestHost(t)
	att := newFakeAtt()
	run := domain.RunID("run-stopped")
	if err := h.StartSession(context.Background(), RunSession(run), att); err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	att.writeOutput(t, "scrollback")
	waitFor(t, "scrollback recorded before stop", func() bool {
		ts, ok := h.LastOutput(RunSession(run))
		return ok && !ts.IsZero()
	})
	if err := h.StopSession(context.Background(), RunSession(run)); err != nil {
		t.Fatalf("StopSession: %v", err)
	}
	att.mu.Lock()
	closed := att.closed
	att.mu.Unlock()
	if !closed {
		t.Fatal("StopSession did not close the runtime attachment")
	}

	rc, err := h.Replay(run)
	if err != nil {
		t.Fatalf("Replay after StopSession: %v", err)
	}
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read replay: %v", err)
	}
	if err := rc.Close(); err != nil {
		t.Fatalf("close replay: %v", err)
	}
	if string(got) != "scrollback" {
		t.Fatalf("replay after StopSession = %q, want %q", got, "scrollback")
	}
	if err := h.StopSession(context.Background(), RunSession(run)); err != nil {
		t.Fatalf("second StopSession: %v", err)
	}
}

func TestHostCloseStopsSessions(t *testing.T) {
	h, _ := newTestHost(t)
	att := newFakeAtt()
	run := domain.RunID("run-close")
	if err := h.StartSession(context.Background(), RunSession(run), att); err != nil {
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
	if err := h.StartSession(context.Background(), RunSession(domain.RunID("run-new")), newFakeAtt()); err == nil {
		t.Fatal("StartSession on closed host succeeded")
	}
}
func TestStopSessionsWithPrefix(t *testing.T) {
	h, _ := newTestHost(t)
	shell1 := newFakeAtt()
	shell2 := newFakeAtt()
	run := newFakeAtt()
	if err := h.StartSession(context.Background(), RunShellSession("r1", "t1"), shell1); err != nil {
		t.Fatalf("start shell1: %v", err)
	}
	if err := h.StartSession(context.Background(), RunShellSession("r2", "t1"), shell2); err != nil {
		t.Fatalf("start shell2: %v", err)
	}
	if err := h.StartSession(context.Background(), RunSession("r1"), run); err != nil {
		t.Fatalf("start run: %v", err)
	}

	h.StopSessionsWithPrefix(context.Background(), "run-shell:r1:")
	h.StopSessionsWithPrefix(context.Background(), "run-shell:r1:")

	shell1.mu.Lock()
	shell1Closed := shell1.closed
	shell1.mu.Unlock()
	shell2.mu.Lock()
	shell2Closed := shell2.closed
	shell2.mu.Unlock()
	run.mu.Lock()
	runClosed := run.closed
	run.mu.Unlock()
	if !shell1Closed {
		t.Fatal("matching run-shell session was not stopped")
	}
	if shell2Closed {
		t.Fatal("non-matching run-shell session was stopped")
	}
	if runClosed {
		t.Fatal("run session was stopped")
	}
}
func TestRingBytesReturnsAllWhenUnwrapped(t *testing.T) {
	r := newRing(8)
	r.write([]byte("abc\n"))
	if got := string(r.bytes()); got != "abc\n" {
		t.Fatalf("unwrapped ring bytes = %q, want %q", got, "abc\n")
	}
}

func TestRingBytesStartsAtLineBoundaryAfterWrap(t *testing.T) {
	r := newRing(10)
	r.write([]byte("12345\n6789x"))
	if got := string(r.bytes()); got != "6789x" {
		t.Fatalf("wrapped ring bytes = %q, want %q", got, "6789x")
	}
}

func TestRingBytesReturnsAllWrappedBytesWithoutNewline(t *testing.T) {
	r := newRing(5)
	r.write([]byte("abcdef"))
	if got := string(r.bytes()); got != "bcdef" {
		t.Fatalf("wrapped ring without newline = %q, want %q", got, "bcdef")
	}
}

func TestActiveSessionsFiltersPrefixAndStoppedSessions(t *testing.T) {
	h, _ := newTestHost(t)
	active := newFakeAtt()
	stopped := newFakeAtt()
	ended := newFakeAtt()
	other := newFakeAtt()
	if err := h.StartSession(context.Background(), RunShellSession("r1", "active"), active); err != nil {
		t.Fatalf("start active: %v", err)
	}
	if err := h.StartSession(context.Background(), RunShellSession("r1", "stopped"), stopped); err != nil {
		t.Fatalf("start stopped: %v", err)
	}
	if err := h.StartSession(context.Background(), RunShellSession("r1", "ended"), ended); err != nil {
		t.Fatalf("start ended: %v", err)
	}
	if err := h.StartSession(context.Background(), RunShellSession("r2", "other"), other); err != nil {
		t.Fatalf("start other: %v", err)
	}
	if err := h.StopSession(context.Background(), RunShellSession("r1", "stopped")); err != nil {
		t.Fatalf("stop session: %v", err)
	}
	if err := ended.outW.Close(); err != nil {
		t.Fatalf("close ended output: %v", err)
	}
	waitFor(t, "ended shell session", func() bool {
		return !h.lookup(RunShellSession("r1", "ended")).isActive()
	})

	got := h.ActiveSessions("run-shell:r1:")
	if len(got) != 1 || got[0] != RunShellSession("r1", "active") {
		t.Fatalf("active sessions = %v, want [%q]", got, RunShellSession("r1", "active"))
	}
}

func TestTranscriptPathReplacesSessionKeySeparators(t *testing.T) {
	h, _ := newTestHost(t)
	want := h.cfg.TranscriptDir + "/terminal-m1-main.cast"
	if got := h.transcriptPath(TerminalSession("m1", "main")); got != want {
		t.Fatalf("transcript path = %q, want %q", got, want)
	}
}

func TestRemoveRunTranscripts(t *testing.T) {
	h, dir := newTestHost(t)
	for _, name := range []string{
		"run-1.cast",
		"run-1.123.cast",
		"run-shell-run-1-main.cast",
		"run-2.cast",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("transcript"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	if err := h.RemoveRunTranscripts(t.Context(), "run-1"); err != nil {
		t.Fatalf("RemoveRunTranscripts: %v", err)
	}
	for _, name := range []string{"run-1.cast", "run-1.123.cast", "run-shell-run-1-main.cast"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s still exists (err %v)", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "run-2.cast")); err != nil {
		t.Errorf("unrelated transcript missing: %v", err)
	}
}

// TestStartSessionReplacesEndedSession: a run-shell tab whose shell exited
// must be reopenable under the same key, and the fresh session must not
// replay the dead shell's transcript.
func TestStartSessionReplacesEndedSession(t *testing.T) {
	h, _ := newTestHost(t)
	key := RunShellSession("r1", "t1")
	first := newFakeAtt()
	if err := h.StartSession(context.Background(), key, first); err != nil {
		t.Fatalf("first StartSession: %v", err)
	}
	first.writeOutput(t, "old shell output\n")
	waitFor(t, "first output recorded", func() bool {
		ts, ok := h.LastOutput(key)
		return ok && !ts.IsZero()
	})
	if err := first.outW.Close(); err != nil {
		t.Fatalf("end first shell: %v", err)
	}
	waitFor(t, "first session ended", func() bool {
		return !h.lookup(key).isActive()
	})

	second := newFakeAtt()
	if err := h.StartSession(context.Background(), key, second); err != nil {
		t.Fatalf("reopen StartSession: %v", err)
	}
	second.writeOutput(t, "new shell\n")
	out := &sink{}
	kr, kw := io.Pipe()
	errCh := make(chan error, 1)
	go func() {
		errCh <- h.Attach(context.Background(), key, AttachClient{Member: "m1", Cols: 120, Rows: 30}, &testConn{r: kr, w: out}, nil)
	}()
	t.Cleanup(func() {
		_ = kw.Close()
		_ = kr.Close()
	})
	waitFor(t, "replay of the new shell only", func() bool {
		return strings.Contains(out.String(), "new shell")
	})
	if got := out.String(); strings.Contains(got, "old shell output") {
		t.Fatalf("replay contains the dead shell's output: %q", got)
	}
}

func TestStartSessionTitleCallbackUsesSessionKey(t *testing.T) {
	got := make(chan struct {
		key   SessionKey
		title string
	}, 1)
	h, _ := newTestHost(t, func(cfg *Config) {
		cfg.OnTitle = func(key SessionKey, title string) {
			got <- struct {
				key   SessionKey
				title string
			}{key: key, title: title}
		}
	})
	key := RunSession("run-1")
	att := newFakeAtt()
	if err := h.StartSession(context.Background(), key, att); err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	att.writeOutput(t, "\x1b]0;run title\x07")

	select {
	case gotTitle := <-got:
		if gotTitle.key != key || gotTitle.title != "run title" {
			t.Fatalf("title callback = %#v, want key %q and title %q", gotTitle, key, "run title")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for title callback")
	}
}

// TestReplayTranscript pins the finished-run replay path: after StopSession
// the recorded transcript still replays the exact bytes the agent wrote,
// binary included, and a run that never recorded one reports
// os.ErrNotExist so callers can fall back to their own refusal.
func TestReplayTranscript(t *testing.T) {
	h, _ := newTestHost(t)
	att := newFakeAtt()
	run := domain.RunID("run-replay")
	if err := h.StartSession(context.Background(), RunSession(run), att); err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	att.writeOutput(t, "hello ")
	att.writeOutput(t, "\xffworld")
	// A mirror confirms both chunks passed deliver() - and with it the
	// transcript writer - before the session stops.
	a := startAttach(t, h, run, "m1", 80, 24, true)
	waitFor(t, "output delivered", func() bool {
		return a.out.String() == "hello \xffworld"
	})
	if err := h.StopSession(context.Background(), RunSession(run)); err != nil {
		t.Fatalf("StopSession: %v", err)
	}

	rc, err := h.Replay(run)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read replay: %v", err)
	}
	if cerr := rc.Close(); cerr != nil {
		t.Fatalf("close replay: %v", cerr)
	}
	if string(got) != "hello \xffworld" {
		t.Fatalf("replay = %q, want the exact recorded bytes", got)
	}

	if _, err := h.Replay("run-never"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Replay unknown run = %v, want os.ErrNotExist", err)
	}
}

func TestBannerTextEscapesTerminalControls(t *testing.T) {
	got := bannerText("Ana\x1b]52;c;secret\a\n\u009b31m")
	if got != `Ana\x1B]52;c;secret\x07\x0A\x9B31m` {
		t.Fatalf("bannerText = %q", got)
	}
	if strings.ContainsAny(got, "\x1b\a\n\r") {
		t.Fatalf("bannerText retained terminal control bytes: %q", got)
	}
}

// followConn is an attach that records the geometry the host tells it, the
// way the sshd attach conn puts the first one in its ack and sends the rest
// as window-change requests.
type followConn struct {
	testConn
	mu    sync.Mutex
	sizes [][2]uint
}

func (c *followConn) SetGeometry(cols, rows uint) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sizes = append(c.sizes, [2]uint{cols, rows})
}

func (c *followConn) told() [][2]uint {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([][2]uint(nil), c.sizes...)
}

// A follower may type into the agent without the agent's screen being
// resized to fit its own, which is the whole point of the flag: the phone
// that takes control of a run is smaller than every other viewer.
func TestFollowClientStealsNoGeometry(t *testing.T) {
	h, _ := newTestHost(t)
	att := newFakeAtt()
	run := domain.RunID("run-follow")
	if err := h.StartSession(context.Background(), RunSession(run), att); err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	waitSizes := func(n int) {
		t.Helper()
		waitFor(t, fmt.Sprintf("%d resize calls", n), func() bool { return len(att.sizeCalls()) == n })
	}
	waitSizes(1) // the session's own default

	stdin := att.captureStdin()
	desktop := startAttach(t, h, run, "desktop", 132, 43, false)
	waitSizes(3)

	// The phone: writable, 40x20, and following.
	kr, kw := io.Pipe()
	t.Cleanup(func() { _ = kw.Close() })
	conn := &followConn{testConn: testConn{r: kr, w: &sink{}}}
	phoneErr := make(chan error, 1)
	go func() {
		phoneErr <- h.Attach(context.Background(), RunSession(run), AttachClient{
			Member: "phone", Cols: 40, Rows: 20, Follow: true,
		}, conn, nil)
	}()
	waitAttached(t, h, run, 2)

	// It is told what to draw at before anything else, and the session is
	// untouched: a minimum over the clients that impose one leaves it out.
	waitFor(t, "the phone learns the session geometry", func() bool {
		return len(conn.told()) == 1 && conn.told()[0] == [2]uint{132, 43}
	})
	if n := len(att.sizeCalls()); n != 3 {
		t.Fatalf("a following attach resized the session: %d resize calls %v", n, att.sizeCalls())
	}

	// Following is not watching: the keystrokes reach the agent.
	if _, err := kw.Write([]byte("hello")); err != nil {
		t.Fatalf("type: %v", err)
	}
	waitFor(t, "the phone's keystrokes reach the agent", func() bool {
		return strings.Contains(stdin.String(), "hello")
	})

	// Someone else resizing the session is how the phone learns to redraw.
	desktop.resize <- [2]uint{120, 40}
	waitSizes(5)
	waitFor(t, "the phone hears the new geometry", func() bool {
		told := conn.told()
		return len(told) == 2 && told[1] == [2]uint{120, 40}
	})

	_ = kw.Close()
	if err := <-phoneErr; err != nil {
		t.Fatalf("phone attach: %v", err)
	}
	waitAttached(t, h, run, 1)
	if n := len(att.sizeCalls()); n != 5 {
		t.Fatalf("a following detach resized the session: %d resize calls %v", n, att.sizeCalls())
	}
}

// A geometry the runtime refused is not the geometry the PTY has, so a
// follower must not be told to draw at it - and must be told when the same
// size is applied for real later.
func TestFollowerHearsOnlyGeometryTheRuntimeAccepted(t *testing.T) {
	h, _ := newTestHost(t)
	att := newFakeAtt()
	run := domain.RunID("run-refused")
	if err := h.StartSession(context.Background(), RunSession(run), att); err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	kr, kw := io.Pipe()
	t.Cleanup(func() { _ = kw.Close() })
	conn := &followConn{testConn: testConn{r: kr, w: &sink{}}}
	done := make(chan error, 1)
	go func() {
		done <- h.Attach(context.Background(), RunSession(run), AttachClient{
			Member: "phone", Cols: 40, Rows: 20, Follow: true,
		}, conn, nil)
	}()
	waitAttached(t, h, run, 1)
	// The session's own size, which the ack reports; no resize was needed
	// for it, so it is told whatever the runtime does later.
	waitFor(t, "the follower learns the session geometry", func() bool {
		return len(conn.told()) == 1 && conn.told()[0] == [2]uint{120, 30}
	})

	att.refuseResizes(errors.New("docker: resize refused"))
	desktop := startAttach(t, h, run, "desktop", 132, 43, false)
	waitFor(t, "the refused resize was attempted", func() bool {
		return len(att.sizeCalls()) == 3
	})
	// Nothing to draw at: the terminal is still whatever it was.
	time.Sleep(50 * time.Millisecond)
	if told := conn.told(); len(told) != 1 {
		t.Fatalf("follower was told %v, want only the geometry the runtime accepted", told)
	}

	att.refuseResizes(nil)
	desktop.resize <- [2]uint{100, 30}
	waitFor(t, "the follower hears the geometry that took", func() bool {
		told := conn.told()
		return len(told) == 2 && told[1] == [2]uint{100, 30}
	})

	_ = kw.Close()
	if err := <-done; err != nil {
		t.Fatalf("follower attach: %v", err)
	}
}
