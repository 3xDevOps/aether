package sshd

import (
	"context"
	"log/slog"

	"github.com/3xDevOps/Aether/internal/control"
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
	// Release clears the durable hold after a successful read-only PTY
	// replacement or in-stream control readiness transition.
	// It deliberately has no generation argument because attach release
	// admission already identifies the live human lease.
	Release(context.Context, domain.RunID, domain.MemberID) error
	// ReleaseHold is the human-only durable release path. The expected
	// generation is a CAS fence against clearing a newer takeover.
	ReleaseHold(context.Context, domain.RunID, domain.MemberID, uint64) (*domain.MissionWorkerAssignment, error)
	// ReleaseIdle clears the durable hold, whoever took it. Callers first
	// establish, under the worker's control lock, that nobody controls it.
	ReleaseIdle(context.Context, domain.RunID) error
	AdmitInput(context.Context, domain.RunID, domain.RunID, uint64, func() error) error
}

// holdUntil counts a live session behind a swarm worker's durable hold until
// done closes. A session without a control lease must call it before it takes
// the hold. Once the last session ends with no control lease left, nobody
// controls the worker, so the hold is released.
func (s *Server) holdUntil(run domain.RunID, done <-chan struct{}) {
	mission := s.cfg.Services.MissionControl
	if mission == nil || s.cfg.Control == nil {
		return
	}
	s.holdsMu.Lock()
	s.holds[run]++
	s.holdsMu.Unlock()
	s.spawn(func() {
		select {
		case <-done:
		case <-s.holdsStop:
			return
		}
		s.holdsMu.Lock()
		if s.holds[run]--; s.holds[run] == 0 {
			delete(s.holds, run)
		}
		s.holdsMu.Unlock()
		err := s.cfg.Control.AdmitSnapshot(string(run), func(_ control.Snapshot, held bool) error {
			s.holdsMu.Lock()
			live := s.holds[run] > 0
			s.holdsMu.Unlock()
			if held || live {
				return nil
			}
			return mission.ReleaseIdle(context.Background(), run)
		})
		if err != nil {
			slog.Warn("sshd: release swarm worker hold", "run", run, "error", err)
		}
	})
}
