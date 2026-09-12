package ptyhost

import (
	"bytes"
	"io"
	"sync"
)

// ring keeps the last max bytes of raw PTY output for replay-on-attach.
// written counts every byte the session has ever produced, so a client
// that says how far it got can be handed exactly what it missed.
type ring struct {
	max     int
	buf     []byte
	dropped bool
	written uint64
}

func newRing(max int) *ring { return &ring{max: max} }

func (r *ring) write(p []byte) {
	r.written += uint64(len(p))
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

// since returns the bytes written after cursor, and whether the ring
// still holds all of them. A cursor from further back than the ring
// retains - or one ahead of what has been written, which no honest
// client can hold - reports false, and the caller replays everything
// instead.
func (r *ring) since(cursor uint64) ([]byte, bool) {
	if cursor > r.written {
		return nil, false
	}
	behind := r.written - cursor
	if behind > uint64(len(r.buf)) {
		return nil, false
	}
	return append([]byte(nil), r.buf[uint64(len(r.buf))-behind:]...), true
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

// resizeBoundary is an unresolved geometry event. writeLoop waits for done
// without holding any session/client lock, then emits the callback only when
// the runtime accepted the geometry.
type resizeBoundary struct {
	mu       sync.Mutex
	done     chan struct{}
	cols     uint
	rows     uint
	tell     bool
	resolved bool
}

type resizeTarget struct {
	client   *client
	boundary *resizeBoundary
}

func newResizeBoundary() *resizeBoundary {
	return &resizeBoundary{done: make(chan struct{})}
}

func (b *resizeBoundary) setResult(cols, rows uint, tell bool) {
	b.mu.Lock()
	b.cols, b.rows, b.tell = cols, rows, tell
	b.mu.Unlock()
}
func (b *resizeBoundary) signal() {
	b.mu.Lock()
	if !b.resolved {
		b.resolved = true
		close(b.done)
	}
	b.mu.Unlock()
}

func (b *resizeBoundary) result() (cols, rows uint, tell bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cols, b.rows, b.tell
}

// clientEvent is one ordered item on an attachment's outbound queue. Output
// remains raw bytes; a resize boundary is an out-of-band callback emitted by
// the same write loop, so it cannot overtake adjacent output.
type clientEvent struct {
	data     []byte
	boundary *resizeBoundary
}

const geometryQueueCost = 24

// client is one attachment: an outbound queue pumped to conn by its own
// write loop so a slow client can never block the session pump.
type client struct {
	conn     io.ReadWriter
	readOnly bool
	// follow keeps this client out of the geometry reconcile whether or
	// not it may write: it renders the size the session is, so it can
	// never reflow the agent's screen for anyone else.
	follow   bool
	snapshot bool
	// resume means this client kept the screen from a previous attach, so
	// it is sent only what it missed and provokes no redraw. cursor is how
	// far it got, and resumed records whether the ring could still answer
	// from there - when it could not, the attach falls back to a full
	// replay and the client has to clear its screen after all.
	resume     bool
	cursor     uint64
	resumed    bool
	replayCols uint
	replayRows uint
	cols       uint // guarded by session.mu
	rows       uint // guarded by session.mu
	replay     []byte

	mu     sync.Mutex
	cond   *sync.Cond
	events []clientEvent
	queued int
	closed bool
	err    error
	done   chan struct{}
}

func newClient(conn io.ReadWriter, a AttachClient) *client {
	c := &client{
		conn:     conn,
		readOnly: a.ReadOnly,
		follow:   a.Follow,
		snapshot: a.Snapshot,
		resume:   a.Resume,
		cursor:   a.Cursor,
		cols:     a.Cols,
		rows:     a.Rows,
		done:     make(chan struct{}),
	}
	c.cond = sync.NewCond(&c.mu)
	return c
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

// sizes reports whether c brings a screen of its own to the question of
// how big the PTY should be. A follower renders at whatever size the
// session is, and an in-process consumer like the adapter tap has no
// terminal at all and attaches with none - neither is a screen anything
// has to fit inside, and neither is company for the check below.
func (c *client) sizes() bool { return !c.follow && c.cols != 0 && c.rows != 0 }

// imposesNow reports whether c's geometry is one the PTY has to fit
// inside. A client that may write always counts. A read-only mirror
// counts only while it is the only client bringing a screen at all -
// alone there is no other screen to reflow, so a watcher resizing its
// window is just ssh resizing a terminal, and the agent is better drawn
// at the size someone is actually looking at. Callers hold mu.
func (s *session) imposesNow(c *client) bool {
	if !c.sizes() {
		return false
	}
	if !c.readOnly {
		return true
	}
	for other := range s.clients {
		if other != c && other.sizes() {
			return false
		}
	}
	return true
}

func (c *client) tellResume() {
	if w, ok := c.conn.(ResumeWriter); ok {
		w.SetResume(c.cursor, c.resumed)
	}
}

// The callback may block on transport I/O; callers must not hold session.mu.
func (c *client) tellGeometry(cols, rows uint) {
	if w, ok := c.conn.(GeometryWriter); ok {
		w.SetGeometry(cols, rows)
	}
}

func (c *client) enqueue(p []byte) {
	if len(p) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	if c.queued+len(p) > maxClientBuffer {
		c.closeSlowLocked()
		return
	}
	if n := len(c.events); n > 0 && c.events[n-1].boundary == nil {
		c.events[n-1].data = append(c.events[n-1].data, p...)
	} else {
		c.events = append(c.events, clientEvent{data: append([]byte(nil), p...)})
	}
	c.queued += len(p)
	c.cond.Broadcast()
}

// enqueueResizeBoundary places an unresolved geometry barrier behind all
// output already queued for this client. Runtime output delivered during the
// resize is appended after it, and writeLoop waits for resolution off locks.
func (c *client) enqueueResizeBoundary(b *resizeBoundary) bool {
	if _, ok := c.conn.(GeometryWriter); !ok {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return false
	}
	if c.queued+geometryQueueCost > maxClientBuffer {
		c.closeSlowLocked()
		return false
	}
	c.events = append(c.events, clientEvent{boundary: b})
	c.queued += geometryQueueCost
	c.cond.Broadcast()
	return true
}

// resolveResizeBoundary releases a barrier after the runtime call. If a
// later accepted geometry has no output between it and this barrier, update
// the earlier event instead and drop this one so consecutive changes
// coalesce. A failed or redundant resize simply drops its barrier.
func (c *client) resolveResizeBoundary(b *resizeBoundary, cols, rows uint, tell bool) []*resizeBoundary {
	c.mu.Lock()
	index := -1
	for i, event := range c.events {
		if event.boundary == b {
			index = i
			break
		}
	}
	if index >= 0 {
		if tell && index > 0 && c.events[index-1].boundary != nil {
			previous := c.events[index-1].boundary
			c.events[index] = clientEvent{}
			copy(c.events[index:], c.events[index+1:])
			c.events[len(c.events)-1] = clientEvent{}
			c.events = c.events[:len(c.events)-1]
			if len(c.events) == 0 {
				c.events = nil
			}
			c.queued -= geometryQueueCost
			c.cond.Broadcast()
			c.mu.Unlock()
			previous.setResult(cols, rows, true)
			b.setResult(cols, rows, false)
			return []*resizeBoundary{previous, b}
		}
		if !tell {
			c.events[index] = clientEvent{}
			copy(c.events[index:], c.events[index+1:])
			c.events[len(c.events)-1] = clientEvent{}
			c.events = c.events[:len(c.events)-1]
			if len(c.events) == 0 {
				c.events = nil
			}
			c.queued -= geometryQueueCost
			c.cond.Broadcast()
		}
	}
	c.mu.Unlock()
	b.setResult(cols, rows, tell)
	return []*resizeBoundary{b}
}

func (c *client) closeSlowLocked() {
	c.closed = true
	c.err = errSlowClient
	close(c.done)
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

// writeLoop pumps replay and ordered output/geometry events to conn until the
// client is closed and drained (returning the closing error) or a conn write
// fails. Geometry callbacks share this goroutine with output writes, so they
// observe exactly the queue order.
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
		for len(c.events) == 0 && !c.closed {
			c.cond.Wait()
		}
		if len(c.events) == 0 {
			err := c.err
			c.mu.Unlock()
			return err
		}
		event := c.events[0]
		c.events[0] = clientEvent{}
		c.events = c.events[1:]
		if len(c.events) == 0 {
			c.events = nil
		}
		if event.boundary != nil {
			c.queued -= geometryQueueCost
		} else {
			c.queued -= len(event.data)
		}
		c.mu.Unlock()

		if event.boundary != nil {
			<-event.boundary.done
			cols, rows, tell := event.boundary.result()
			if tell {
				c.tellGeometry(cols, rows)
			}
			continue
		}
		if _, err := c.conn.Write(event.data); err != nil {
			c.close(err)
			return err
		}
	}
}
