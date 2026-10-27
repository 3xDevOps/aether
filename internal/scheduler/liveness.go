package scheduler

import (
	"fmt"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/domain"
)

// Liveness is the live or provisioning Enhanced run's turn phase, the
// agent's last progress and whether its connection is open. ok is false for
// any other run.
func (s *Scheduler) Liveness(run domain.RunID) (domain.RunLiveness, bool) {
	s.mu.Lock()
	entry := s.runs[run]
	enhanced := entry != nil && entry.acp && (entry.status.Live() || entry.status == domain.RunProvisioning)
	var status domain.RunStatus
	if enhanced {
		status = entry.status
	}
	s.mu.Unlock()
	if !enhanced {
		return domain.RunLiveness{}, false
	}
	if status == domain.RunProvisioning {
		return domain.RunLiveness{Phase: domain.PhaseSetup, Transport: domain.TransportUnknown}, true
	}
	s.acp.mu.Lock()
	r := s.acp.runs[run]
	var sess *acphost.Session
	if r != nil {
		sess = r.session
	}
	s.acp.mu.Unlock()
	if sess == nil {
		// A run is recorded once its session opened, or failed to.
		l := domain.RunLiveness{Transport: domain.TransportUnknown}
		if r != nil {
			l.Transport = domain.TransportClosed
		}
		return l, true
	}
	return livenessOf(sess.State()), true
}

func livenessOf(st acphost.State) domain.RunLiveness {
	l := domain.RunLiveness{LastProgress: st.LastProgress, Since: st.OpenedAt, Transport: domain.TransportOpen}
	if st.Closed {
		l.Transport = domain.TransportClosed
	}
	switch {
	case len(st.Pending) > 0:
		l.Phase = domain.PhaseAwaitingInput
	case st.ToolsOpen || st.Compacting:
		l.Phase = domain.PhaseToolRunning
	case st.TurnInFlight && !st.LastProgress.After(st.TurnStartedAt):
		l.Phase = domain.PhaseFirstEvent
	case st.TurnInFlight:
		l.Phase = domain.PhaseStreaming
	case st.Background:
		l.Phase = domain.PhaseToolRunning
	default:
		l.Phase = domain.PhaseBetweenTurns
	}
	return l
}

// reminding reports whether the run's turn is the report reminder askReport
// sent, which the report gate already bounds. A prompt queued behind the
// reminder starts a turn of its own, and so does any turn while the
// reminder's own start is unknown.
func (d *acpDriver) reminding(run domain.RunID) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	r := d.runs[run]
	return r != nil && r.asked != nil && r.session != nil && r.asked.start != 0 &&
		r.session.Log().TurnStartSeq() == r.asked.start
}

// stallLimit is how long a run in phase may go with no activity before it
// parks as stalled. A tool call, a compaction or background tasks can be
// quiet for long; a turn that has not started, or has gone quiet mid-reply,
// should not be.
func (c Config) stallLimit(phase domain.TurnPhase, heldByTasks, reminder bool) time.Duration {
	switch {
	case heldByTasks:
		return c.StallThreshold * heldStallFactor
	case reminder:
	case phase == domain.PhaseFirstEvent:
		return min(c.FirstEventGrace, c.StallThreshold)
	case phase == domain.PhaseStreaming:
		return min(c.StreamQuietGrace, c.StallThreshold)
	}
	return c.StallThreshold
}

func stalledReason(l domain.RunLiveness, enhanced bool, idle time.Duration) string {
	idle = idle.Truncate(time.Second)
	if !enhanced || l.Phase == "" {
		return fmt.Sprintf("%sno output or file changes for %s", stalledReasonPrefix, idle)
	}
	return fmt.Sprintf("%spossibly — no progress for %s, %s, transport %s", stalledReasonPrefix, idle,
		strings.ReplaceAll(string(l.Phase), "-", " "), l.Transport)
}
