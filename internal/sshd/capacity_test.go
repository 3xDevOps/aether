package sshd

import (
	"fmt"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/scheduler"
)

func TestCapacityRefusalsAreUnavailableWithResourceReason(t *testing.T) {
	for _, tc := range []struct {
		err    error
		reason string
	}{
		{scheduler.ErrDiskFull, "containerd snapshotter has insufficient free bytes"},
		{scheduler.ErrMemoryPressure, "host MemAvailable is below startup headroom"},
		{scheduler.ErrCapacityUnknown, "Docker host identity cannot be verified"},
		{scheduler.ErrBrowserCPUCapacity, "all browser CPU slots are reserved"},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			err := fmt.Errorf("%w: %s", tc.err, tc.reason)
			wire := rpcError(err)
			if wire.Code != protocol.CodeUnavailable || !strings.Contains(wire.Message, tc.reason) {
				t.Fatalf("capacity refusal mapped as %+v", wire)
			}
			if tc.err != scheduler.ErrDiskFull && strings.Contains(wire.Message, "insufficient disk") {
				t.Fatalf("non-disk refusal misreported as disk pressure: %s", wire.Message)
			}
		})
	}
}
