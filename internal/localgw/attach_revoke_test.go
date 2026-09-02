package localgw

import (
	"testing"

	"github.com/coder/websocket"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// The server ends a live attach with a distinct exit status when its
// authorization re-check fails. The gateway relays it as the 1008 close the
// terminal view already reads, so a demoted member's pane drops to a mirror
// and a removed member's stops reconnecting.
func TestAttachRevocationCloses1008WithReason(t *testing.T) {
	for _, tc := range []struct {
		status int
		reason string
	}{
		{protocol.AttachExitSteerRevoked, "steer permission withdrawn"},
		{protocol.AttachExitMembershipRevoked, "membership withdrawn"},
	} {
		term := newWSStubTerminal(&cli.RemoteExitError{Status: tc.status})
		b := &wsStubBackend{attachTerm: term, attachAck: protocol.AttachResponse{OK: true, Cols: 80, Rows: 24}}
		g, base := newWSGateway(t, b)
		conn := wsDial(t, base, "/ws/attach/run-1", g.Token())

		writeWSJSON(t, conn, protocol.DashAttachRequest{Write: true})
		if ack := readWSJSON[protocol.AttachResponse](t, conn); !ack.OK {
			t.Fatalf("ack = %+v", ack)
		}
		term.finish()
		if reason := expectClose(t, conn, websocket.StatusPolicyViolation); reason != tc.reason {
			t.Fatalf("status %d: close reason = %q, want %q", tc.status, reason, tc.reason)
		}
	}
}
