package cli

import (
	"encoding/binary"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/protocol"
)

// The server reports a session's own resize as a window-change request on
// the attach channel, the same request shape a client sends to ask for
// one. A client following the session redraws at what arrives; the
// transport half of that is TestAttachFollowerIsToldTheSessionGeometry in
// internal/sshd.
func TestWindowChangeRequestsBecomeGeometry(t *testing.T) {
	reqs := make(chan *ssh.Request, 4)
	sizes := make(chan [2]uint, 1)
	done := make(chan error, 1)
	go func() { done <- awaitRequests(reqs, sizes) }()

	size := func(cols, rows uint32) []byte {
		p := make([]byte, 16)
		binary.BigEndian.PutUint32(p, cols)
		binary.BigEndian.PutUint32(p[4:], rows)
		return p
	}
	reqs <- &ssh.Request{Type: protocol.WindowChangeRequest, Payload: size(132, 43)}
	if got := <-sizes; got != [2]uint{132, 43} {
		t.Fatalf("geometry = %v, want 132x43", got)
	}

	// One slot, latest wins: a follower that was busy redraws at the size
	// the session is now, never at one it has already left behind.
	reqs <- &ssh.Request{Type: protocol.WindowChangeRequest, Payload: size(120, 40)}
	reqs <- &ssh.Request{Type: protocol.WindowChangeRequest, Payload: size(100, 30)}
	reqs <- &ssh.Request{Type: "exit-status", Payload: size(0, 0)}
	close(reqs)

	// Read once every request has been consumed, so what is left in the
	// slot is the answer rather than a race with the reader.
	if err := <-done; err != nil {
		t.Fatalf("await: %v", err)
	}
	if got := <-sizes; got != [2]uint{100, 30} {
		t.Fatalf("geometry = %v, want the newest 100x30", got)
	}
	if _, open := <-sizes; open {
		t.Fatal("the geometry channel outlived the channel's requests")
	}
}
