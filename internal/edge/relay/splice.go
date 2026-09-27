package relay

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
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
		r.pipe(c.ctx, server, client)
		c.cancel(errSpliceDone)
		close(done)
	}()
	r.pipe(c.ctx, client, server)
	c.cancel(errSpliceDone)
	<-done
	r.drop(c)
}

func (r *Relay) pipe(ctx context.Context, dst, src net.Conn) {
	buf := make([]byte, copyBufferSize)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
			r.count(n)
			if r.throttled() && !sleep(ctx, time.Duration(n)*time.Second/time.Duration(r.throttleRate)) {
				return
			}
		}
		if err != nil {
			return
		}
	}
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

func (r *Relay) count(n int) {
	r.bytes.Add(uint64(n))
	r.monthBytes.Add(int64(n))
	r.unflushed.Add(int64(n))
}

func (r *Relay) throttled() bool {
	return r.budget > 0 && r.monthBytes.Load() >= r.budget
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
	r.egressMu.Lock()
	defer r.egressMu.Unlock()
	if n := r.unflushed.Swap(0); n > 0 {
		if err := r.store.AddEgress(ctx, r.month, n); err != nil {
			r.unflushed.Add(n)
			return fmt.Errorf("relay: save egress for %s: %w", r.month, err)
		}
	}
	if m := monthOf(now); m != r.month {
		used, err := r.store.Egress(ctx, m)
		if err != nil {
			return fmt.Errorf("relay: load egress for %s: %w", m, err)
		}
		r.month = m
		r.monthBytes.Store(used)
	}
	return nil
}
