package webgate

import (
	"testing"

	"github.com/coder/websocket"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestAttachEndCloseMapsRevocationReasons(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		reason string
	}{
		{name: "steer permission", status: protocol.AttachExitSteerRevoked, reason: "steer permission withdrawn"},
		{name: "membership", status: protocol.AttachExitMembershipRevoked, reason: "membership withdrawn"},
		{name: "control takeover", status: protocol.AttachExitControlRevoked, reason: "control taken over"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, reason := attachEndClose(&protocol.RemoteExitError{Status: tc.status})
			if code != websocket.StatusPolicyViolation || reason != tc.reason {
				t.Fatalf("attachEndClose(%d) = (%d, %q), want (1008, %q)", tc.status, code, reason, tc.reason)
			}
		})
	}
}

func TestAttachEndCloseKeepsUnknownExitGeneric(t *testing.T) {
	code, reason := attachEndClose(&protocol.RemoteExitError{Status: 1})
	if code != websocket.StatusInternalError || reason != "attach ended" {
		t.Fatalf("attachEndClose(1) = (%d, %q), want (1011, %q)", code, reason, "attach ended")
	}
}
