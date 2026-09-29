package relay

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"
)

// copyBufferSize bounds what a splice holds per direction: it reads again
// only after the previous read was written, so a peer that stops reading
// stalls the other peer through TCP flow control instead of growing a
// buffer.
const copyBufferSize = 32 << 10

var errSpliceDone = errors.New("connection closed")

// splice copies bytes between client and server until either side closes
// or c is cancelled, then closes both.
func (r *Relay) splice(c *relayConn, client, server net.Conn) {
	r.mu.Lock()
	c.spliced = true
	r.mu.Unlock()
	// Either direction ending cancels c, which closes both sides and so
	// ends the other direction.
	context.AfterFunc(c.ctx, func() {
		go func() { _ = client.Close() }()
		_ = server.Close()
	})
	done := make(chan struct{})
	go func() {
		r.pipe(c, server, client)
		c.cancel(errSpliceDone)
		close(done)
	}()
	r.pipe(c, client, server)
	c.cancel(errSpliceDone)
	<-done
	r.drop(c)
}

func (r *Relay) pipe(c *relayConn, dst, src net.Conn) {
	buf := make([]byte, copyBufferSize)
	for {
		throttled := r.throttled()
		p := buf
		if throttled {
			p = buf[:throttledChunk]
		}
		n, err := src.Read(p)
		if n > 0 {
			if throttled && !r.throttle(c, n) {
				return
			}
			// Counted before the write, so a peer never holds bytes the
			// budget and the final flush have not seen.
			month := r.count(n)
			if w, werr := dst.Write(p[:n]); werr != nil {
				r.giveBack(n-w, month)
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// pacer spreads reads over a rate: each read is sent after the ones
// reserved before it.
type pacer struct {
	mu   sync.Mutex
	next time.Time
}

// reserve reserves n bytes at rate bytes per second and returns how long
// to wait before sending them.
func (p *pacer) reserve(n int, rate int64) time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	if p.next.Before(now) {
		p.next = now
	}
	wait := p.next.Sub(now)
	p.next = p.next.Add(time.Duration(n) * time.Second / time.Duration(rate))
	return wait
}

// throttle waits until n bytes of c may be sent at the throttled rate and
// reports false when c ended first. A server's connections take one turn
// at a time, so a read waits behind at most one read per other server,
// however many connections that server holds. Opening more connections
// does not raise the rate.
func (r *Relay) throttle(c *relayConn, n int) bool {
	select {
	case c.reg.turn <- struct{}{}:
	case <-c.ctx.Done():
		return false
	}
	defer func() { <-c.reg.turn }()
	return sleep(c.ctx, r.pace.reserve(n, r.throttleRate))
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// egressMeter counts relayed bytes against a calendar month. One lock
// covers the month, its usage and the bytes waiting to be saved, so a
// count, a give-back, a save and a month change never see half of another.
type egressMeter struct {
	mu      sync.Mutex
	month   string
	changes uint64 // month changes so far
	used    int64  // bytes of month, saved or not
	pending int64  // bytes of month not saved; negative after saved bytes were given back
}

func (m *egressMeter) usage() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.used
}

// count adds n relayed bytes and returns the month change they were
// counted in.
func (r *Relay) count(n int) uint64 {
	r.bytes.Add(uint64(n))
	m := &r.egress
	m.mu.Lock()
	defer m.mu.Unlock()
	m.used += int64(n)
	m.pending += int64(n)
	return m.changes
}

// giveBack takes back n bytes a failed write did not deliver, unless the
// month changed since they were counted.
func (r *Relay) giveBack(n int, counted uint64) {
	r.bytes.Add(^uint64(n - 1))
	m := &r.egress
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.changes != counted {
		return
	}
	m.used -= int64(n)
	m.pending -= int64(n)
}

func (r *Relay) throttled() bool {
	return r.budget > 0 && r.egress.usage() >= r.budget
}

func monthOf(t time.Time) string {
	return t.UTC().Format("2006-01")
}

func (r *Relay) flushLoop() {
	defer close(r.flushed)
	t := time.NewTicker(egressFlushEvery)
	defer t.Stop()
	for {
		select {
		case <-r.ctx.Done():
			return
		case now := <-t.C:
			ctx, cancel := context.WithTimeout(r.ctx, directoryTimeout)
			if err := r.flushEgress(ctx, now); err != nil {
				slog.Warn("relay: egress counter not saved; retrying", "error", err)
			}
			cancel()
		}
	}
}

// flushEgress adds the bytes counted since the last flush to the stored
// month and starts a new month's count when the month changed.
func (r *Relay) flushEgress(ctx context.Context, now time.Time) error {
	r.flushMu.Lock()
	defer r.flushMu.Unlock()
	if err := r.saveEnded(ctx); err != nil {
		return err
	}
	m := &r.egress
	m.mu.Lock()
	month, n := m.month, max(m.pending, 0)
	m.pending -= n
	m.mu.Unlock()
	if n > 0 {
		if err := r.store.AddEgress(ctx, month, n); err != nil {
			m.mu.Lock()
			m.pending += n
			m.mu.Unlock()
			return fmt.Errorf("relay: save egress for %s: %w", month, err)
		}
	}
	next := monthOf(now)
	if next == month {
		return nil
	}
	used, err := r.store.Egress(ctx, next)
	if err != nil {
		return fmt.Errorf("relay: load egress for %s: %w", next, err)
	}
	m.mu.Lock()
	rest := m.pending
	m.month, m.used, m.pending = next, used, 0
	m.changes++
	m.mu.Unlock()
	// Bytes counted after the save above still belong to the month that ended.
	r.ended, r.endedBytes = month, max(rest, 0)
	return r.saveEnded(ctx)
}

// saveEnded saves what the month that ended is still owed. Until it
// succeeds no later flush saves or changes month, so the amount is kept
// for the next attempt.
func (r *Relay) saveEnded(ctx context.Context) error {
	if r.endedBytes == 0 {
		return nil
	}
	if err := r.store.AddEgress(ctx, r.ended, r.endedBytes); err != nil {
		return fmt.Errorf("relay: save egress for %s: %w", r.ended, err)
	}
	r.endedBytes = 0
	return nil
}
