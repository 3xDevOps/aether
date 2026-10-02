package relay

import (
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func TestListenerLimitsConnectionsPerAddress(t *testing.T) {
	e := newEnv(t)
	e.r.maxConnsPerAddress = 2
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln := e.r.Listener(inner)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			// Hold the connection until the client closes it, as the
			// edge's HTTPS server does.
			go func() {
				_, _ = io.Copy(io.Discard, c)
				_ = c.Close()
			}()
		}
	}()
	dial := func() net.Conn {
		c, err := net.Dial("tcp", inner.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		return c
	}
	// open reports whether the listener kept c open for a moment.
	open := func(c net.Conn) bool {
		_ = c.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		_, err := c.Read(make([]byte, 1))
		var ne net.Error
		return errors.As(err, &ne) && ne.Timeout()
	}
	first, second := dial(), dial()
	if !open(first) || !open(second) {
		t.Fatal("connections under the limit were closed")
	}
	if open(dial()) {
		t.Fatal("a third connection from one address was kept open")
	}
	if got := e.r.Metrics().Refusals["too many connections from one address"]; got != 1 {
		t.Fatalf("refusals counted %d, want 1", got)
	}
	_ = first.Close()
	eventually(t, "closed connection gives back its slot", func() bool { return open(dial()) })
}
