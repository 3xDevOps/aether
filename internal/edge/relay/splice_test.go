package relay

import (
	"errors"
	"net"
	"testing"
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
	if got := e.r.unflushed.Load(); got != 2 {
		t.Fatalf("unflushed = %d, want 2", got)
	}
}
