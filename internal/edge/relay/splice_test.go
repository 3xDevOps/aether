package relay

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

// shortWriter accepts two bytes of a write and fails the rest.
type shortWriter struct{ net.Conn }

func (shortWriter) Write(p []byte) (int, error) {
	return min(len(p), 2), errors.New("peer went away")
}

func TestFailedWriteCountsOnlyDeliveredBytes(t *testing.T) {
	e := newEnv(t)
	src, peer := net.Pipe()
	t.Cleanup(func() { _ = src.Close() })
	go func() {
		_, _ = peer.Write([]byte("0123456789"))
		_ = peer.Close()
	}()
	e.r.pipe(nil, shortWriter{}, src)
	if got := e.r.Metrics(); got.BytesRelayed != 2 || got.EgressThisMonth != 2 {
		t.Fatalf("relayed %d, egress %d; want 2 and 2", got.BytesRelayed, got.EgressThisMonth)
	}
	if got := pending(e.r); got != 2 {
		t.Fatalf("pending = %d, want 2", got)
	}
}

// rolloverWriter moves the relay to the next month while a write is in
// flight, then fails it.
type rolloverWriter struct {
	net.Conn
	t *testing.T
	r *Relay
}

func (w rolloverWriter) Write(p []byte) (int, error) {
	if err := w.r.flushEgress(context.Background(), time.Now().AddDate(0, 1, 0)); err != nil {
		w.t.Error(err)
	}
	return 0, errors.New("peer went away")
}

func TestFailedWriteGivesNothingBackToTheNextMonth(t *testing.T) {
	e := newEnv(t)
	src, peer := net.Pipe()
	t.Cleanup(func() { _ = src.Close() })
	go func() {
		_, _ = peer.Write([]byte("0123456789"))
		_ = peer.Close()
	}()
	e.r.pipe(nil, rolloverWriter{t: t, r: e.r}, src)
	if got := e.r.Metrics().EgressThisMonth; got != 0 {
		t.Fatalf("egress of the new month = %d, want 0", got)
	}
	if got := pending(e.r); got != 0 {
		t.Fatalf("pending = %d, want 0", got)
	}
}

func pending(r *Relay) int64 {
	r.egress.mu.Lock()
	defer r.egress.mu.Unlock()
	return r.egress.pending
}

func TestEveryCountedByteIsSavedAcrossMonthChanges(t *testing.T) {
	store := &fakeEgress{months: map[string]int64{}}
	e := newEnvWith(t, 0, store)
	const writers, each = 8, 2000
	var wg sync.WaitGroup
	for range writers {
		wg.Go(func() {
			for range each {
				e.r.count(1)
			}
		})
	}
	now := time.Now()
	for i := range 24 {
		if err := e.r.flushEgress(t.Context(), now.AddDate(0, i, 0)); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
	if err := e.r.flushEgress(t.Context(), now.AddDate(0, 23, 0)); err != nil {
		t.Fatal(err)
	}
	var saved int64
	store.mu.Lock()
	for _, n := range store.months {
		saved += n
	}
	store.mu.Unlock()
	if saved != writers*each {
		t.Fatalf("saved %d bytes of %d counted", saved, writers*each)
	}
	if got, want := e.r.Metrics().EgressThisMonth, store.months[monthOf(now.AddDate(0, 23, 0))]; got != want {
		t.Fatalf("usage of the month = %d, saved for it = %d", got, want)
	}
}
