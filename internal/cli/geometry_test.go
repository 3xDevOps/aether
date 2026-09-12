package cli

import (
	"encoding/binary"
	"errors"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/protocol"
)

// Server window-change requests are no longer a reverse geometry channel:
// framed terminal records carry geometry in stream order. The SSH request
// pump still consumes exit status so the stream reports remote failures.
func TestAwaitRequestsReportsExitStatus(t *testing.T) {
	reqs := make(chan *ssh.Request, 2)
	done := make(chan error, 1)
	go func() { done <- awaitRequests(reqs) }()

	payload := make([]byte, 4)
	binary.BigEndian.PutUint32(payload, 7)
	reqs <- &ssh.Request{Type: "exit-status", Payload: payload}
	close(reqs)

	err := <-done
	var exit *protocol.RemoteExitError
	if !errors.As(err, &exit) || exit.Status != 7 {
		t.Fatalf("await = %v, want remote exit status 7", err)
	}
}
