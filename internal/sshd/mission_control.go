package sshd

import (
	"context"

	"github.com/3xDevOps/Aether/internal/domain"
)

// MissionControl is the narrow mission authority seam used by human
// attachments and the in-container orchestrator. Implementations resolve a
// worker from its run ID; callers never provide an attempt or mission ID.
//
// Takeover and Release are called from the existing control lease admission
// callbacks. ReleaseHold is the explicit human action and is coordinated by
// the SSH handler with the same control lock. A failed call must leave the
// control lease and durable hold unchanged. AdmitInput acquires the target
// worker's existing control admission lock before checking assignment/takeover
// and invoking its callback; callers must not pre-lock that worker.
type MissionControl interface {
	Takeover(context.Context, domain.RunID, domain.MemberID) error
	// Release clears the durable hold after a successful PTY replacement.
	// It deliberately has no generation argument because attach release
	// admission already identifies the live human lease.
	Release(context.Context, domain.RunID, domain.MemberID) error
	// ReleaseHold is the human-only durable release path. The expected
	// generation is a CAS fence against clearing a newer takeover.
	ReleaseHold(context.Context, domain.RunID, domain.MemberID, uint64) (*domain.MissionWorkerAssignment, error)
	AdmitInput(context.Context, domain.RunID, domain.RunID, uint64, func() error) error
}
