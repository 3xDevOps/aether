package scheduler

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
)

// idleCloseAfter is how long a live session goes without activity before the
// server closes it.
const idleCloseAfter = 7 * 24 * time.Hour

const idleClosedNote = "Closed after 7 days without activity, as its owner's Close would: its work is committed and published to its branch. Relaunch reopens it while its sandbox is kept; after that, launch a new session on its branch."

// goCloseIdleRuns runs closeIdleRuns off the supervisor loop, which closing
// many sessions one after another would hold up for minutes, and never two
// at once.
func (s *Scheduler) goCloseIdleRuns() {
	if !s.idleCloseMu.TryLock() {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer s.idleCloseMu.Unlock()
		s.closeIdleRuns(s.superCtx, time.Now().UTC())
	}()
}

// closeIdleRuns closes every live interactive session that nothing happened
// in for idleCloseAfter. Each is judged just before its close. A sleeping VM
// is woken first, as the close commits inside it; one admission will not
// wake is left for the next sweep rather than closed without its commit,
// and one that cannot wake at all is closed as its owner's Close would.
func (s *Scheduler) closeIdleRuns(ctx context.Context, now time.Time) {
	var runs []*supervised
	s.mu.Lock()
	for _, e := range s.runs {
		runs = append(runs, e)
	}
	s.mu.Unlock()
	for _, e := range runs {
		if ctx.Err() != nil {
			return
		}
		wake, idle := s.idleToClose(e, now)
		if !idle {
			continue
		}
		if wake {
			err := s.WakeRun(ctx, e.runID, "")
			if errors.Is(err, ErrMemoryPressure) || errors.Is(err, ErrDiskFull) || errors.Is(err, ErrCapacityUnknown) {
				slog.Info("scheduler: admission refused to wake a session idle for 7 days to close it; trying again at the next sweep", "run", e.runID, "error", err)
				continue
			}
			// A message sent while it woke starts a turn the close would cut.
			if sess := s.acp.session(e.runID); err == nil && sess != nil && (sess.State().TurnInFlight || sess.State().Queued > 0) {
				continue
			}
		}
		if err := s.closeRun(ctx, e.runID, closeSpec{outcome: domain.RunCompleted, reason: "closed", retained: retainedCloseReason}); err != nil {
			slog.Warn("scheduler: close a session idle for 7 days", "run", e.runID, "error", err)
			continue
		}
		s.publishTimeline(ctx, e.workspaceID, e.runID, "", events.TimelineNote, idleClosedNote)
	}
}

// idleToClose reports whether e is a live interactive session whose last
// activity is idleCloseAfter old, and whether it sleeps in a VM, which the
// close must wake to commit in. Only what happened counts: a question waiting
// for its owner, a pause or a turn that made no progress is no activity, and
// a sleep is the last activity of a session asleep. An operation in flight
// or a change of mode or account keeps it open, and so does a VM the memory
// guard saved while it worked: its turn waits for memory, not its owner.
func (s *Scheduler) idleToClose(e *supervised, now time.Time) (wake, idle bool) {
	s.mu.Lock()
	if s.runs[e.runID] != e || !e.status.Live() || !e.launchMode.Interactive() || e.retained || e.finalizing || e.killRequested ||
		e.destroyPending || e.sleeping || e.waking != nil || e.memorySaved > 0 ||
		e.awake > 0 || e.switching != "" || e.switchIntent != nil || e.moving || e.accountSwap != nil {
		s.mu.Unlock()
		return false, false
	}
	_, saving := s.saver(e.containerID)
	enhanced := e.acp && e.launchMode == domain.LaunchACP
	asleep := !e.asleepSince.IsZero()
	// The sleep time is kept across a server restart, which resets the
	// rest.
	facts := idleFacts{active: []time.Time{e.asleepSince}}
	if !asleep {
		facts = s.liveFactsLocked(e)
	}
	s.mu.Unlock()
	if !asleep {
		facts = s.sessionFacts(e.runID, enhanced, saving, facts)
	}
	facts.busy = ""
	last, _ := idleSince(facts)
	return asleep && saving, now.Sub(last) >= idleCloseAfter
}
