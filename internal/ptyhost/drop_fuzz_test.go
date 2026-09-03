package ptyhost

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// errDropped is what a dropping connection returns once its cut point is
// reached: the transport died, not a clean client detach.
var errDropped = errors.New("connection reset by peer")

// dropConn is an attachment whose read side hands out keystrokes up to a
// cut point and then fails like a dropped SSH connection. Everything past
// the cut is what the member typed but the transport never delivered.
type dropConn struct {
	mu      sync.Mutex
	pending []byte
	out     bytes.Buffer
}

func (c *dropConn) Read(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.pending) == 0 {
		return 0, errDropped
	}
	n := copy(p, c.pending)
	c.pending = c.pending[n:]
	return n, nil
}

func (c *dropConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.out.Write(p)
}

// lateConn is the other half of the boundary: a connection that is already
// dead but still parked in Read, holding bytes the transport had not yet
// handed over. Releasing it after the attach has unwound is what a late TCP
// retransmit on a dead socket looks like from the server's side.
type lateConn struct {
	mu      sync.Mutex
	pending []byte
	late    []byte
	served  bool
	release chan struct{}
	out     bytes.Buffer
}

func (c *lateConn) Read(p []byte) (int, error) {
	c.mu.Lock()
	if len(c.pending) > 0 {
		n := copy(p, c.pending)
		c.pending = c.pending[n:]
		c.mu.Unlock()
		return n, nil
	}
	c.mu.Unlock()

	<-c.release
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.served {
		return 0, errDropped
	}
	c.served = true
	// The straggler arrives together with the reset, which is the only
	// shape a dead socket can deliver it in.
	return copy(p, c.late), errDropped
}

func (c *lateConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.out.Write(p)
}

// FuzzAttachDropMidInput drives the failure table's "SSH drop mid-attach"
// row at the byte the transport died on: no matter where the cut lands, the
// agent's stdin holds a prefix of the delivered keystrokes - never
// reordered, never duplicated, never a byte the client did not send, and
// never one from past the cut.
//
// The other half of the row - that nothing arriving after the attach
// unwound reaches the agent - cannot be reached by cutting the stream,
// because the read loop stops at the first error and never asks the dead
// connection for more. It has its own scenario:
// TestAttachIgnoresBytesThatArriveAfterItUnwinds.
func FuzzAttachDropMidInput(f *testing.F) {
	f.Add([]byte("git status\r"), uint16(4))
	f.Add([]byte("\x1b[Ayes\r"), uint16(1))
	f.Add(bytes.Repeat([]byte("paste-line\n"), 600), uint16(4096))
	f.Add([]byte{}, uint16(0))

	f.Fuzz(func(t *testing.T, typed []byte, cutAt uint16) {
		if len(typed) > 1<<16 {
			typed = typed[:1<<16]
		}
		cut := int(cutAt)
		if len(typed) == 0 {
			cut = 0
		} else {
			cut %= len(typed) + 1
		}
		delivered := typed[:cut]

		h, _ := newTestHost(t)
		att := newFakeAtt()
		agent := att.captureStdin()
		run := domain.RunID("run_drop")
		if err := h.StartSession(context.Background(), RunSession(run), att); err != nil {
			t.Fatalf("StartSession: %v", err)
		}

		conn := &dropConn{pending: delivered}
		err := h.Attach(context.Background(), RunSession(run), "m1", 80, 24, false, conn, nil)
		if err != nil && !errors.Is(err, errDropped) {
			t.Fatalf("Attach after a dropped connection: %v", err)
		}

		// The attach has returned, so the agent has seen everything it will
		// ever see from this connection. Let anything still in flight land
		// before reading the agent's stdin.
		time.Sleep(20 * time.Millisecond)
		got := agent.Bytes()
		// A byte from past the cut makes the agent's stdin longer than what
		// the transport delivered, and a reorder or duplication breaks the
		// prefix, so one check covers all three.
		if !bytes.HasPrefix(delivered, got) {
			t.Fatalf("agent stdin (%d bytes) is not a prefix of the %d delivered bytes; the "+
				"connection dropped after byte %d of %d\n got %q\nwant a prefix of %q",
				len(got), len(delivered), cut, len(typed), got, delivered)
		}
	})
}

// TestAttachDropDoesNotLeakIntoReattach pins the half-sent boundary across
// a reattach: the member's next connection must never see - or share the
// agent's stdin with - what the dead one was still holding.
func TestAttachDropDoesNotLeakIntoReattach(t *testing.T) {
	h, _ := newTestHost(t)
	att := newFakeAtt()
	agent := att.captureStdin()
	run := domain.RunID("run_reattach")
	if err := h.StartSession(context.Background(), RunSession(run), att); err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	dead := &dropConn{pending: []byte("half-ty")}
	if err := h.Attach(context.Background(), RunSession(run), "m1", 80, 24, false, dead, nil); err != nil &&
		!errors.Is(err, errDropped) {
		t.Fatalf("Attach: %v", err)
	}
	waitFor(t, "the dropped attach's delivered keystrokes", func() bool {
		return bytes.Equal(agent.Bytes(), []byte("half-ty"))
	})

	fresh := &dropConn{pending: []byte("\x03reset\r")}
	if err := h.Attach(context.Background(), RunSession(run), "m1", 80, 24, false, fresh, nil); err != nil &&
		!errors.Is(err, errDropped) {
		t.Fatalf("reattach: %v", err)
	}
	waitFor(t, "the reattach's keystrokes", func() bool {
		return bytes.Equal(agent.Bytes(), []byte("half-ty\x03reset\r"))
	})
	if got := string(agent.Bytes()); got != "half-ty\x03reset\r" {
		t.Fatalf("agent stdin = %q, want the dropped attach's delivered prefix followed by the "+
			"reattach's input and nothing else", got)
	}
}

// TestAttachIgnoresBytesThatArriveAfterItUnwinds pins the second half of the
// failure table's "SSH drop mid-attach" row: the connection is dead but its
// read is still parked holding bytes the transport had not handed over, and
// the attach unwinds first. Whatever the socket coughs up afterwards must
// never reach the agent - otherwise it would interleave with the input of
// whatever attach came next.
func TestAttachIgnoresBytesThatArriveAfterItUnwinds(t *testing.T) {
	h, _ := newTestHost(t)
	att := newFakeAtt()
	agent := att.captureStdin()
	run := domain.RunID("run_late")
	if err := h.StartSession(context.Background(), RunSession(run), att); err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	conn := &lateConn{
		pending: []byte("typed"),
		late:    []byte("too-late\r"),
		release: make(chan struct{}),
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- h.Attach(ctx, RunSession(run), "m1", 80, 24, false, conn, nil)
	}()
	waitFor(t, "the keystrokes the transport did deliver", func() bool {
		return bytes.Equal(agent.Bytes(), []byte("typed"))
	})

	// Unwind the attach with the read still parked, then let the straggler
	// come back through it.
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Attach after the connection went away = %v, want the context error", err)
	}
	close(conn.release)

	time.Sleep(50 * time.Millisecond)
	if got := string(agent.Bytes()); got != "typed" {
		t.Fatalf("agent stdin = %q, want only %q: a byte that arrived after the attach unwound "+
			"reached the agent", got, "typed")
	}
}
