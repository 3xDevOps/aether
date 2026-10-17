//go:build edgeupgrade

package edgetest

import (
	"fmt"
	"io"
	"os"
	"testing"
	"time"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
)

// TestPreviousServer is copied into a worktree of the previous release by
// TestEdgeUpgrade. It runs that release's server, sshd and edge agent
// against the edge at AETHER_EDGE_QUALIFY_RELAY, prints its server id and a
// claim code, and serves until its standard input closes.
func TestPreviousServer(t *testing.T) {
	h := &harness{t: t, relayURL: os.Getenv("AETHER_EDGE_QUALIFY_RELAY")}
	s := h.newServer(edgeproto.PolicyApprovedDevices)
	fmt.Printf("qualify-peer: %s %s\n", s.id, s.claimCode(t, time.Now()))
	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		t.Fatal(err)
	}
}
