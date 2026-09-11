package ptyhost

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/attribution"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// maxClientBuffer bounds the per-client pending output; a client that falls
// this far behind is force-detached so it can never block the agent.
const maxClientBuffer = 4 << 20

// resizeTimeout bounds each att.Resize call: geometry reconciliation runs
// off the session lock and a hung runtime must never wedge it forever.
const resizeTimeout = 5 * time.Second

// echoWindow bounds how long the terminal's echo of written input is still
// expected back. The line discipline echoes as it accepts the bytes, so
// anything later is the agent's own output; without the bound a terminal
// with echo off - every full-screen agent - would leave the expectation
// standing against whatever the agent printed next.
const echoWindow = time.Second

// paintQuiet is how long agent output stays liveness-silent after the
// session pokes the PTY with a redraw nudge. A TUI answers the nudge with
// a full repaint; those bytes prove nothing about the agent's progress,
// so they must not clear a stall the way a real answer would. Tests
// shorten it to keep assertions off the real window.
var paintQuiet = 3 * time.Second

// maxPendingEcho caps the echo the session tracks at once. An expectation
// past it is dropped whole: the bytes then count as the agent's, which only
// costs a stall one more threshold, where an unbounded queue would grow for
// as long as anyone kept steering.
const maxPendingEcho = 8 << 10

var errSlowClient = errors.New("ptyhost: client too slow, detached")

// session is one persistent PTY session: the adopted attachment, its pump,
// the replay ring, the transcript, and the attached clients.
type session struct {
	run SessionKey
	att runtime.Attachment
	tr  *castWriter

	stdinMu sync.Mutex
	stdin   io.WriteCloser

	mu              sync.Mutex
	clients         map[*client]struct{}
	ring            *ring
	cols            uint
	rows            uint
	geoGen          uint64  // bumped whenever the PTY must be (re)sized
	geoApplied      uint64  // last geoGen an applier has picked up
	geoTold         [2]uint // last size the clients were told about
	ended           bool
	stopped         bool
	lastOut         time.Time
	paintQuietUntil time.Time
	done            chan struct{}
	title           titleScanner
	modes           modeScanner
	onTitle         func(string)

	// pendingEcho is the echo the terminal still owes for input the server
	// wrote to the agent - an injected line, or a member's keystrokes. The
	// line discipline echoes them back through the PTY even when the agent
	// never reads them, so those bytes are not evidence the agent is alive;
	// see expectEcho and consumeEcho. echoDeadline expires the expectation.
	pendingEcho  []byte
	echoDeadline time.Time

	// resizeMu serializes att.Resize applications so nudge sequences from
	// concurrent reconciles never interleave; it is never held with mu.
	resizeMu sync.Mutex
}

func (s *session) pump() {
	s.mu.Lock()
	att := s.att
	stopped := s.stopped
	s.mu.Unlock()
	if stopped || att == nil {
		return
	}
	out := att.Stdout()
	buf := make([]byte, 32*1024)
	for {
		n, err := out.Read(buf)
		if n > 0 {
			s.deliver(buf[:n])
		}
		if err != nil {
			s.end()
			return
		}
	}
}

func (s *session) deliver(p []byte) {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped || s.ended {
		return
	}
	// Viewers, the transcript and the title scanner get every byte; only
	// the liveness clock discounts the terminal's echo of what the server
	// wrote, and a repaint provoked by our own resize nudge.
	if s.consumeEcho(p, now) && now.After(s.paintQuietUntil) {
		s.lastOut = now
	}
	s.title.scan(p, s.onTitle)
	s.modes.scan(p)
	s.ring.write(p)
	s.tr.output(p)
	for c := range s.clients {
		c.enqueue(p)
	}
}

// end marks the session ended after the agent exited: the transcript closes
// and every attachment drains to EOF, but the session stays queryable until
// StopSession.
func (s *session) end() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended || s.stopped {
		return
	}
	s.ended = true
	tr := s.tr
	s.tr = nil
	if tr != nil {
		_ = tr.close()
	}
	for c := range s.clients {
		c.close(nil)
	}
	s.ring = nil // no further attaches: release the replay buffer
	close(s.done)
}

func (s *session) stop() {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	tr := s.tr
	s.tr = nil
	if tr != nil {
		_ = tr.close()
	}
	for c := range s.clients {
		c.close(nil)
	}
	// The sessions map keeps a lightweight stopped entry so StopSession
	// remains idempotent. Release all attachment and transcript state that
	// would otherwise retain runtime stream buffers for the life of the host.
	s.ring = nil
	s.clients = nil
	att := s.att
	s.att = nil
	s.stdin = nil
	s.title = titleScanner{}
	s.pendingEcho = nil
	s.onTitle = nil
	if !s.ended {
		close(s.done)
	}
	s.mu.Unlock()
	if att != nil {
		_ = att.Close()
	}
}

func (s *session) isActive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.stopped && !s.ended
}

func (s *session) lastOutput() (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return time.Time{}, false
	}
	return s.lastOut, true
}

func (s *session) addClient(c *client) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return ErrNoSession
	}
	if s.ended {
		return ErrSessionEnded
	}
	// A resuming client still holds this session's screen and its terminal
	// state, so it gets neither. Everyone else rebuilds from the ring,
	// which is a byte tail: the modes the agent set at startup are long
	// gone from it, and the preamble puts them back ahead of the replay.
	if !c.resume {
		c.replay = append(s.modes.preamble(), s.ring.bytes()...)
	}
	s.clients[c] = struct{}{}
	// Any join can change who imposes, not just this client: the mirror
	// that was alone here a moment ago no longer is. A resume asks for no
	// redraw - that is the point of it - so it nudges only if the size it
	// brings actually differs from the one the session already has.
	s.reconcileLocked(s.imposesNow(c) && !c.resume)
	return nil
}

func (s *session) removeClient(c *client) {
	s.mu.Lock()
	if _, ok := s.clients[c]; ok {
		delete(s.clients, c)
		// Leaving can promote the client left behind, so the size is
		// recomputed whoever it was that went.
		if !s.ended && !s.stopped {
			s.reconcileLocked(false)
		}
	}
	s.mu.Unlock()
	c.close(nil)
}

// geometry is the size the session's PTY has now.
func (s *session) geometry() (uint, uint) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cols, s.rows
}

func (s *session) resizeClient(c *client, cols, rows uint) {
	if cols == 0 || rows == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.clients[c]; !ok {
		return
	}
	c.cols, c.rows = cols, rows
	if s.imposesNow(c) && !s.ended && !s.stopped {
		s.reconcileLocked(false)
	}
}

// reconcileLocked recomputes the effective PTY size as the per-dimension
// minimum over the clients that impose one (see imposesNow), records it, and schedules the att.Resize application
// off the lock (a slow runtime resize must never stall output delivery).
// With no such client the size stays unchanged. force schedules a redraw
// nudge even when the size did not change (repaint for a new write-mode
// joiner).
func (s *session) reconcileLocked(force bool) {
	var cols, rows uint
	found := false
	for c := range s.clients {
		if !s.imposesNow(c) {
			continue
		}
		if !found {
			cols, rows = c.cols, c.rows
			found = true
			continue
		}
		cols = min(cols, c.cols)
		rows = min(rows, c.rows)
	}
	if !found {
		return
	}
	changed := cols != s.cols || rows != s.rows
	if !changed && !force {
		return
	}
	s.cols, s.rows = cols, rows
	if changed {
		s.tr.resize(cols, rows)
	}
	s.geoGen++
	go s.applyResize()
}

// applyResize applies the latest recorded geometry to the attachment with a
// redraw nudge (rows-1 then rows) so TUIs repaint, then tells the attached
// followers what the session now is. Appliers serialize on resizeMu and
// always apply the newest geometry, so concurrent reconciles coalesce and
// never apply stale sizes, and the clients are told in that same order;
// each call is bounded by resizeTimeout so a hung runtime cannot wedge the
// session. A resize the runtime refuses or takes too long to answer tells
// nobody: the PTY is not that size, so a follower would be drawing at a
// geometry that exists only here.
func (s *session) applyResize() {
	s.resizeMu.Lock()
	defer s.resizeMu.Unlock()
	s.mu.Lock()
	if s.geoApplied == s.geoGen || s.ended || s.stopped || s.att == nil {
		s.mu.Unlock()
		return
	}
	s.geoApplied = s.geoGen
	cols, rows := s.cols, s.rows
	att := s.att
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), resizeTimeout)
	defer cancel()
	if rows > 1 {
		_ = att.Resize(ctx, cols, rows-1)
	}
	applied := att.Resize(ctx, cols, rows) == nil
	s.mu.Lock()
	s.paintQuietUntil = time.Now().Add(paintQuiet)
	var followers []*client
	// Left unrecorded, a size that failed is told the next time it is
	// reconciled rather than suppressed as already sent.
	if applied && s.geoTold != [2]uint{cols, rows} {
		s.geoTold = [2]uint{cols, rows}
		for c := range s.clients {
			// Only a follower redraws at someone else's geometry; a
			// client that imposed this size asked for it.
			if c.follow {
				followers = append(followers, c)
			}
		}
	}
	s.mu.Unlock()
	for _, c := range followers {
		c.tellGeometry(cols, rows)
	}
}

// writeStdin forwards keystrokes to the agent; false once the session is
// over. The terminal echoes them back exactly as it echoes an injected
// line, so they register the same expectation: a member typing at a hung
// agent is not the agent talking. stdinMu is held across the registration
// so the queued echoes stay in the order the writes reach the PTY.
func (s *session) writeStdin(p []byte) bool {
	s.stdinMu.Lock()
	defer s.stdinMu.Unlock()
	s.mu.Lock()
	if s.ended || s.stopped || s.stdin == nil {
		s.mu.Unlock()
		return false
	}
	stdin := s.stdin
	s.expectEcho(p, time.Now())
	s.mu.Unlock()

	_, err := stdin.Write(p)
	if err != nil {
		s.dropEcho()
	}
	return err == nil
}

func (s *session) annotateInjection(actorName, actorColor, message string) error {
	safeActor, safeMessage := bannerText(actorName), bannerText(message)
	banner := renderBanner(safeActor, actorColor, safeMessage)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return ErrNoSession
	}
	if s.ended {
		return ErrSessionEnded
	}
	s.tr.output(banner)
	s.tr.marker("inject by " + safeActor + ": " + safeMessage)
	for c := range s.clients {
		c.enqueue(banner)
	}
	return nil
}

func (s *session) inject(actorName, actorColor, message, submit string) error {
	line := []byte(message + submit)
	s.stdinMu.Lock()
	defer s.stdinMu.Unlock()
	s.mu.Lock()
	if s.stopped || s.stdin == nil {
		s.mu.Unlock()
		return ErrNoSession
	}
	if s.ended {
		s.mu.Unlock()
		return ErrSessionEnded
	}
	stdin := s.stdin
	tr := s.tr
	// Neither the banner nor the echo the terminal owes us may touch
	// lastOut: stall detection reads that clock, and counting the server's
	// own bytes would clear a stall for an agent that never answered.
	s.expectEcho(line, time.Now())
	s.mu.Unlock()
	n, err := stdin.Write(line)
	if err != nil {
		s.dropEcho()
		return fmt.Errorf("ptyhost: inject stdin write: %w", err)
	}
	if n != len(line) {
		s.dropEcho()
		return fmt.Errorf("ptyhost: inject stdin write: %w", io.ErrShortWrite)
	}
	if err := s.annotateInjection(actorName, actorColor, message); err != nil {
		// The write above already accepted the full line: a session that
		// ended in this window must not turn delivered input into a
		// reported failure, which would invite a double-submitting retry.
		// The banner has no viewers on a wound-down session, but the
		// transcript still takes the attribution marker.
		if errors.Is(err, ErrSessionEnded) || errors.Is(err, ErrNoSession) {
			if tr != nil {
				tr.lateMarker("inject by " + bannerText(actorName) + ": " + bannerText(message))
			}
			return nil
		}
		return err
	}
	return nil
}

// expectEcho queues the bytes the terminal will echo back for input the
// server just wrote to the agent's stdin. The line discipline rewrites
// every lone CR or LF as CRLF (ICRNL and ONLCR) and renders any other
// control byte in hat notation (ECHOCTL), TAB excepted, so a multi-line or
// control-bearing steer echoes as something quite unlike what was written.
// The editing characters are left out: DEL and the other VERASE/VKILL
// bindings are consumed by the line editor, which repaints rather than
// echoes them, so they take the divergence path and cost a stall one more
// threshold instead of being modelled wrongly here. An expectation too
// large to hold is dropped whole rather than half-matched. Callers hold
// mu, and stdinMu so the queue keeps the order the writes reach the PTY.
func (s *session) expectEcho(in []byte, now time.Time) {
	echo := make([]byte, 0, len(in)+8)
	for _, b := range in {
		switch {
		case b == '\r' || b == '\n':
			echo = append(echo, '\r', '\n')
		case b == '\t':
			echo = append(echo, b)
		case b < 0x20:
			echo = append(echo, '^', b^0x40)
		default:
			echo = append(echo, b)
		}
	}
	if len(s.pendingEcho)+len(echo) > maxPendingEcho {
		s.pendingEcho = nil
		return
	}
	s.pendingEcho = append(s.pendingEcho, echo...)
	s.echoDeadline = now.Add(echoWindow)
}

// dropEcho forgets the queued echo after a failed stdin write: bytes that
// never reached the terminal are never echoed, and discounting them would
// eat the agent's own output instead.
func (s *session) dropEcho() {
	s.mu.Lock()
	s.pendingEcho = nil
	s.mu.Unlock()
}

// consumeEcho strips the echo the session is still owed from the head of p
// and reports whether anything is left over - that leftover is the agent
// talking. The terminal echoes written input back through the PTY even when
// the agent never reads it, so an echo alone must not refresh the liveness
// clock. Two things end the expectation: the first byte that diverges from
// it, and echoWindow passing, since a terminal that is going to echo does
// so as it accepts the bytes. Either way the bytes count as the agent's,
// which is the safe direction. Callers hold mu.
func (s *session) consumeEcho(p []byte, now time.Time) bool {
	if len(s.pendingEcho) > 0 && now.After(s.echoDeadline) {
		s.pendingEcho = nil
	}
	n := 0
	for n < len(p) && n < len(s.pendingEcho) && p[n] == s.pendingEcho[n] {
		n++
	}
	if n < len(p) && n < len(s.pendingEcho) {
		s.pendingEcho = nil
		return true
	}
	s.pendingEcho = s.pendingEcho[n:]
	return n < len(p)
}

// renderBanner renders an attributed injection banner shown to viewers and
// recorded in the transcript; it is never written to the agent's input.
func renderBanner(actorName, actorColor, message string) []byte {
	var b bytes.Buffer
	b.WriteString("\r\n")
	b.WriteString(attribution.ANSI(actorColor))
	fmt.Fprintf(&b, "\x1b[7m ▸ %s injects \x1b[0m %s\r\n", actorName, message)
	return b.Bytes()
}

// bannerText keeps user-controlled message bytes from becoming terminal
// control sequences in the transcript or another member's terminal.
func bannerText(text string) string {
	var b strings.Builder
	for _, r := range text {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			fmt.Fprintf(&b, "\\x%02X", r)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// ring keeps the last max bytes of raw PTY output for replay-on-attach.
type ring struct {
	max     int
	buf     []byte
	dropped bool
}

func newRing(max int) *ring { return &ring{max: max} }

func (r *ring) write(p []byte) {
	if len(p) >= r.max {
		if len(p) > r.max || len(r.buf) > 0 {
			r.dropped = true
		}
		r.buf = append(r.buf[:0], p[len(p)-r.max:]...)
		return
	}
	if len(r.buf)+len(p) > r.max {
		r.dropped = true
	}
	r.buf = append(r.buf, p...)
	if n := len(r.buf) - r.max; n > 0 {
		r.buf = append(r.buf[:0], r.buf[n:]...)
	}
}

func (r *ring) bytes() []byte {
	start := 0
	if r.dropped {
		if i := bytes.IndexByte(r.buf, '\n'); i >= 0 {
			start = i + 1
		}
	}
	return append([]byte(nil), r.buf[start:]...)
}

// client is one attachment: an outbound buffer pumped to conn by its own
// write loop so a slow client can never block the session pump.
type client struct {
	conn     io.ReadWriter
	readOnly bool
	// follow keeps this client out of the geometry reconcile whether or
	// not it may write: it renders the size the session is, so it can
	// never reflow the agent's screen for anyone else.
	follow bool
	// resume means this client kept the screen from a previous attach, so
	// it is sent no replay and provokes no redraw.
	resume bool
	cols   uint // guarded by session.mu
	rows   uint // guarded by session.mu
	replay []byte

	mu     sync.Mutex
	cond   *sync.Cond
	buf    []byte
	closed bool
	err    error
	done   chan struct{}
}

func newClient(conn io.ReadWriter, a AttachClient) *client {
	c := &client{
		conn:     conn,
		readOnly: a.ReadOnly,
		follow:   a.Follow,
		resume:   a.Resume,
		cols:     a.Cols,
		rows:     a.Rows,
		done:     make(chan struct{}),
	}
	c.cond = sync.NewCond(&c.mu)
	return c
}

// imposesNow reports whether c's geometry is one the PTY has to fit
// inside. A follower never counts: it renders at the size the session
// already is, which is the whole point of following. A client that may
// write always counts. A read-only mirror counts only while it is the
// only client that could - alone there is no other screen to reflow, so
// a watcher resizing its window is just ssh resizing a terminal, and the
// agent is better drawn at the size someone is actually looking at.
// Callers hold mu.
func (s *session) imposesNow(c *client) bool {
	if c.follow {
		return false
	}
	if !c.readOnly {
		return true
	}
	for other := range s.clients {
		if other != c && !other.follow {
			return false
		}
	}
	return true
}

// tellGeometry hands the session's size to a client that asked to be told,
// which is how an ack reports the live geometry and how a later change
// reaches a follower. Never called with the session lock held: the conn
// writes.
func (c *client) tellGeometry(cols, rows uint) {
	if w, ok := c.conn.(GeometryWriter); ok {
		w.SetGeometry(cols, rows)
	}
}

func (c *client) enqueue(p []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	if len(c.buf)+len(p) > maxClientBuffer {
		c.closed = true
		c.err = errSlowClient
		close(c.done)
		c.cond.Broadcast()
		return
	}
	c.buf = append(c.buf, p...)
	c.cond.Broadcast()
}

// close records the client's terminal condition; the first call wins.
func (c *client) close(err error) {
	c.mu.Lock()
	if !c.closed {
		c.closed = true
		c.err = err
		close(c.done)
	}
	c.cond.Broadcast()
	c.mu.Unlock()
}

func (c *client) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

func (c *client) getErr() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// writeLoop pumps buffered output to conn until the client is closed and
// drained (returning the closing error) or a conn write fails.
func (c *client) writeLoop() error {
	replay := c.replay
	c.replay = nil
	if rw, ok := c.conn.(ReplayWriter); ok {
		if _, err := rw.WriteReplay(replay); err != nil {
			c.close(err)
			return err
		}
	} else if len(replay) > 0 {
		if _, err := c.conn.Write(replay); err != nil {
			c.close(err)
			return err
		}
	}
	for {
		c.mu.Lock()
		for len(c.buf) == 0 && !c.closed {
			c.cond.Wait()
		}
		if len(c.buf) == 0 {
			err := c.err
			c.mu.Unlock()
			return err
		}
		p := c.buf
		c.buf = nil
		c.mu.Unlock()
		if _, err := c.conn.Write(p); err != nil {
			c.close(err)
			return err
		}
	}
}
