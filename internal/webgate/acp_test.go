package webgate

import (
	"io"
	"testing"

	"github.com/coder/websocket"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestACPEndClose(t *testing.T) {
	for _, tc := range []struct {
		err    error
		code   websocket.StatusCode
		reason string
	}{
		{io.EOF, websocket.StatusServiceRestart, "session stream ended; resubscribe with after_seq"},
		{&protocol.RemoteExitError{Status: 1}, websocket.StatusServiceRestart, "session stream ended; resubscribe with after_seq"},
		{&protocol.RemoteExitError{Status: protocol.AttachExitSteerRevoked}, websocket.StatusPolicyViolation, "steer permission withdrawn"},
		{&protocol.RemoteExitError{Status: protocol.AttachExitMembershipRevoked}, websocket.StatusPolicyViolation, "membership withdrawn"},
	} {
		if code, reason := acpEndClose(tc.err); code != tc.code || reason != tc.reason {
			t.Errorf("acpEndClose(%v) = (%d, %q), want (%d, %q)", tc.err, code, reason, tc.code, tc.reason)
		}
	}
}
