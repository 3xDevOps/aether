package ptyhost

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
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

var errSlowClient = errors.New("ptyhost: client too slow, detached")

// session is one persistent PTY session: the adopted attachment, its pump,
// the replay ring, the transcript, and the attached clients.
type session struct {
	run SessionKey
	att runtime.Attachment
	tr  *castWriter

	stdinMu sync.Mutex
	stdin   io.WriteCloser

	mu         sync.Mutex
	clients    map[*client]struct{}
	ring       *ring
	cols       uint
	rows       uint
	geoGen     uint64 // bumped whenever the PTY must be (re)sized
	geoApplied uint64 // last geoGen an applier has picked up
	ended      bool
	stopped    bool
	lastOut    time.Time
	done       chan struct{}

	// resizeMu serializes att.Resize applications so nudge sequences from
	// concurrent reconciles never interleave; it is never held with mu.
	resizeMu sync.Mutex
}

func (s *session) pump() {
	out := s.att.Stdout()
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
	s.lastOut = now
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
	_ = s.tr.close()
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
	_ = s.tr.close()
	for c := range s.clients {
		c.close(nil)
	}
	// The sessions map keeps stopped entries (idempotent StopSession), so
	// release the replay buffer and client set to bound long-term memory.
	s.ring = nil
	s.clients = nil
	if !s.ended {
		close(s.done)
	}
	s.mu.Unlock()
	_ = s.att.Close()
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
	if tail := s.ring.bytes(); len(tail) > 0 {
		c.enqueue(tail)
	}
	s.clients[c] = struct{}{}
	if !c.readOnly {
		s.reconcileLocked(true)
	}
	return nil
}

func (s *session) removeClient(c *client) {
	s.mu.Lock()
	if _, ok := s.clients[c]; ok {
		delete(s.clients, c)
		if !c.readOnly && !s.ended && !s.stopped {
			s.reconcileLocked(false)
		}
	}
	s.mu.Unlock()
	c.close(nil)
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
	if !c.readOnly && !s.ended && !s.stopped {
		s.reconcileLocked(false)
	}
}

// reconcileLocked recomputes the effective PTY size as the per-dimension
// minimum over write-capable clients (read-only mirrors never affect it),
// records it, and schedules the att.Resize application off the lock (a slow
// runtime resize must never stall output delivery). With no writers the
// size stays unchanged. force schedules a redraw nudge even when the size
// did not change (repaint for a new write-mode joiner).
func (s *session) reconcileLocked(force bool) {
	var cols, rows uint
	found := false
	for c := range s.clients {
		if c.readOnly {
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
// redraw nudge (rows-1 then rows) so TUIs repaint. Appliers serialize on
// resizeMu and always apply the newest geometry, so concurrent reconciles
// coalesce and never apply stale sizes; each call is bounded by
// resizeTimeout so a hung runtime cannot wedge the session.
func (s *session) applyResize() {
	s.resizeMu.Lock()
	defer s.resizeMu.Unlock()
	s.mu.Lock()
	if s.geoApplied == s.geoGen || s.ended || s.stopped {
		s.mu.Unlock()
		return
	}
	s.geoApplied = s.geoGen
	cols, rows := s.cols, s.rows
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), resizeTimeout)
	defer cancel()
	if rows > 1 {
		_ = s.att.Resize(ctx, cols, rows-1)
	}
	_ = s.att.Resize(ctx, cols, rows)
}

// writeStdin forwards keystrokes to the agent; false once the session is
// over.
func (s *session) writeStdin(p []byte) bool {
	s.mu.Lock()
	over := s.ended || s.stopped
	s.mu.Unlock()
	if over {
		return false
	}
	s.stdinMu.Lock()
	_, err := s.stdin.Write(p)
	s.stdinMu.Unlock()
	return err == nil
}

func (s *session) inject(actorName, actorColor, message string) error {
	banner := renderBanner(actorName, actorColor, message)
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return ErrNoSession
	}
	if s.ended {
		s.mu.Unlock()
		return ErrSessionEnded
	}
	s.lastOut = time.Now()
	s.tr.output(banner)
	s.tr.marker("inject by " + actorName + ": " + message)
	for c := range s.clients {
		c.enqueue(banner)
	}
	s.mu.Unlock()

	s.stdinMu.Lock()
	defer s.stdinMu.Unlock()
	if _, err := s.stdin.Write([]byte(message + "\r")); err != nil {
		return fmt.Errorf("ptyhost: inject stdin write: %w", err)
	}
	return nil
}

// renderBanner renders the attributed injection banner shown to viewers and
// recorded in the transcript; it is never written to the agent's input.
func renderBanner(actorName, actorColor, message string) []byte {
	var b bytes.Buffer
	b.WriteString("\r\n")
	b.WriteString(attribution.ANSI(actorColor))
	fmt.Fprintf(&b, "\x1b[7m ▸ %s injects \x1b[0m %s\r\n", actorName, message)
	return b.Bytes()
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
	cols     uint // guarded by session.mu
	rows     uint // guarded by session.mu

	mu     sync.Mutex
	cond   *sync.Cond
	buf    []byte
	closed bool
	err    error
	done   chan struct{}
}

func newClient(conn io.ReadWriter, readOnly bool, cols, rows uint) *client {
	c := &client{conn: conn, readOnly: readOnly, cols: cols, rows: rows, done: make(chan struct{})}
	c.cond = sync.NewCond(&c.mu)
	return c
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
