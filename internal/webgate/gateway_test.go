package webgate

import (
	"errors"
	"net"
	"testing"
	"time"
)

// deadListener is a listener whose Accept fails for good, the shape of a
// socket the kernel closed under a running server.
type deadListener struct {
	net.Listener
	err error
}

func (l deadListener) Accept() (net.Conn, error) { return nil, l.err }

func TestServeReportsWhenEveryListenerHasDied(t *testing.T) {
	g, err := New(Config{Authorize: admitAll(&stubBackend{})})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = g.Close() }()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	broken := errors.New("accept: socket is not connected")
	g.Serve(deadListener{Listener: ln, err: broken})
	select {
	case <-g.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Done never closed although the only listener died")
	}
	if !errors.Is(g.Err(), broken) {
		t.Fatalf("Err = %v, want the accept failure", g.Err())
	}
}

func TestServeStaysQuietThroughClose(t *testing.T) {
	g, err := New(Config{Authorize: admitAll(&stubBackend{})})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	g.Serve(ln)
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-g.Done():
		t.Fatal("Done closed on an ordinary Close")
	case <-time.After(100 * time.Millisecond):
	}
}
