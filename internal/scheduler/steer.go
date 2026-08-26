package scheduler

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/ptyhost"
)

// Kill terminates a run: any non-terminal state moves to abandoned with
// reason "killed"; the checkout, branch, and transcript are preserved and
// partial work is committed as "wip:". For a supervised run the container
// is stopped and the wait goroutine finishes the job.
func (s *Scheduler) Kill(ctx context.Context, run domain.RunID, actor domain.MemberID) error {
	s.mu.Lock()
	entry := s.runs[run]
	if entry == nil {
		s.mu.Unlock()
		return s.killUnsupervised(ctx, run, actor)
	}
	entry.killRequested = true
	entry.killActor = actor
	workspace, cid := entry.workspaceID, entry.containerID
	// Written under s.mu so a finalize that concurrently removes the entry
	// (and its sidecar) cannot interleave and leave an orphaned file.
	if err := s.writeSidecar(entry.sidecar()); err != nil {
		slog.Warn("scheduler: persist kill flag", "run", run, "error", err)
	}
	s.mu.Unlock()
	// No container yet (still provisioning): the provisioning checkpoints
	// see killRequested and abort.
	if cid != "" {
		if err := s.cfg.Runtime.Stop(ctx, cid, s.cfg.StopGrace); err != nil {
			return err
		}
	}
	s.publishTimeline(ctx, workspace, run, actor, events.TimelineKill, "")
	return nil
}

// killUnsupervised abandons a non-terminal run the scheduler holds no
// container for (e.g. a run parked at needs-attention, or store state left
// over from an incomplete recovery). The status is re-read and transitioned
// under s.mu: every scheduler status write holds the lock, so the locked
// read is authoritative and a concurrent terminal transition cannot be
// overwritten.
func (s *Scheduler) killUnsupervised(ctx context.Context, id domain.RunID, actor domain.MemberID) error {
	r, err := s.cfg.Store.GetRun(ctx, id)
	if err != nil {
		return err
	}
	if r.Status.Terminal() {
		return fmt.Errorf("%w: run is already %s", ErrInvalidTransition, r.Status)
	}
	if r.Worktree != "" {
		if _, cerr := s.cfg.Git.CommitAll(ctx, id, "wip: "+taskLine(r.Task)); cerr != nil {
			slog.Warn("scheduler: wip commit on kill", "run", id, "error", cerr)
		}
		if _, perr := s.cfg.Git.PublishRunBranch(ctx, id); perr != nil {
			slog.Warn("scheduler: publish branch on kill", "run", id, "error", perr)
		}
	}
	s.mu.Lock()
	if s.runs[id] != nil {
		// A launch registered the run meanwhile: route through the
		// supervised path.
		s.mu.Unlock()
		return s.Kill(ctx, id, actor)
	}
	r, err = s.cfg.Store.GetRun(ctx, id)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	if r.Status.Terminal() {
		s.mu.Unlock()
		return fmt.Errorf("%w: run is already %s", ErrInvalidTransition, r.Status)
	}
	err = s.transitionLocked(ctx, id, r.WorkspaceID, r.Status, domain.RunAbandoned, "killed", actor)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	s.removeSidecar(id)
	s.publishTimeline(ctx, r.WorkspaceID, id, actor, events.TimelineKill, "")
	return nil
}

// Pause freezes a supervised run's container (SIGSTOP semantics). Status
// is unchanged; the paused flag is durable and exempts the run from stall
// detection.
func (s *Scheduler) Pause(ctx context.Context, run domain.RunID, actor domain.MemberID) error {
	s.mu.Lock()
	entry := s.runs[run]
	if entry == nil || entry.containerID == "" {
		s.mu.Unlock()
		return fmt.Errorf("%w: run has no live container", ErrInvalidTransition)
	}
	if entry.paused {
		s.mu.Unlock()
		return fmt.Errorf("%w: run is already paused", ErrInvalidTransition)
	}
	workspace, cid := entry.workspaceID, entry.containerID
	s.mu.Unlock()
	if err := s.cfg.Runtime.Pause(ctx, cid); err != nil {
		return err
	}
	s.setPaused(entry, true)
	s.publishTimeline(ctx, workspace, run, actor, events.TimelinePause, "")
	return nil
}

// Resume thaws a paused run.
func (s *Scheduler) Resume(ctx context.Context, run domain.RunID, actor domain.MemberID) error {
	s.mu.Lock()
	entry := s.runs[run]
	if entry == nil || entry.containerID == "" {
		s.mu.Unlock()
		return fmt.Errorf("%w: run has no live container", ErrInvalidTransition)
	}
	if !entry.paused {
		s.mu.Unlock()
		return fmt.Errorf("%w: run is not paused", ErrInvalidTransition)
	}
	workspace, cid := entry.workspaceID, entry.containerID
	s.mu.Unlock()
	if err := s.cfg.Runtime.Resume(ctx, cid); err != nil {
		return err
	}
	s.setPaused(entry, false)
	s.publishTimeline(ctx, workspace, run, actor, events.TimelineResume, "")
	return nil
}

func (s *Scheduler) setPaused(entry *supervised, paused bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry.paused = paused
	if s.runs[entry.runID] != entry {
		// Finalized concurrently: the sidecar is gone and must stay gone.
		return
	}
	if err := s.writeSidecar(entry.sidecar()); err != nil {
		slog.Warn("scheduler: persist paused flag", "run", entry.runID, "error", err)
	}
}

// Paused reports whether a supervised run's container is currently
// frozen. Unknown or finished runs report false.
func (s *Scheduler) Paused(run domain.RunID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.runs[run]
	return entry != nil && entry.paused
}

// Inject writes a steering message into a live agent's PTY, attributed
// to the actor, and stamps the act into the workspace timeline. A live
// supervised run in running or needs-attention is accepted; a clean-exited
// needs-attention run has no PTY and returns ptyhost.ErrNoSession.
func (s *Scheduler) Inject(ctx context.Context, run domain.RunID, actor domain.MemberID, message string) error {
	s.mu.Lock()
	entry := s.runs[run]
	if entry != nil && (entry.status == domain.RunRunning || entry.status == domain.RunNeedsAttention) {
		workspace := entry.workspaceID
		s.mu.Unlock()
		return s.injectLive(ctx, run, workspace, actor, message)
	}
	s.mu.Unlock()

	r, err := s.cfg.Store.GetRun(ctx, run)
	if err != nil {
		return err
	}
	if r.Status == domain.RunNeedsAttention {
		return fmt.Errorf("%w", ptyhost.ErrNoSession)
	}
	return fmt.Errorf("%w: inject requires a running or needs-attention run", ErrInvalidTransition)
}

func (s *Scheduler) injectLive(ctx context.Context, run domain.RunID, workspace domain.WorkspaceID, actor domain.MemberID, message string) error {
	m, err := s.cfg.Store.GetMember(ctx, actor)
	if err != nil {
		return err
	}
	if err := s.cfg.PTY.Inject(ctx, run, m.DisplayName, m.Color, message); err != nil {
		return err
	}
	s.publishTimeline(ctx, workspace, run, actor, events.TimelineSteer, message)
	return nil
}

// CloseRun resolves a needs-attention run to its human-decided outcome:
// merged or abandoned, reason "closed". Any other source state or outcome
// is an invalid transition.
func (s *Scheduler) CloseRun(ctx context.Context, run domain.RunID, actor domain.MemberID, outcome domain.RunStatus) error {
	if outcome != domain.RunMerged && outcome != domain.RunAbandoned {
		return fmt.Errorf("%w: close outcome must be merged or abandoned, got %q", ErrInvalidTransition, outcome)
	}
	s.mu.Lock()
	if entry := s.runs[run]; entry != nil {
		if entry.status != domain.RunNeedsAttention {
			s.mu.Unlock()
			return fmt.Errorf("%w: close requires needs-attention, run is %s", ErrInvalidTransition, entry.status)
		}
		err := s.transitionLocked(ctx, run, entry.workspaceID, domain.RunNeedsAttention, outcome, "closed", actor)
		cid := entry.containerID
		s.mu.Unlock()
		if err != nil {
			return err
		}
		// Closed while the (stalled but alive) container still runs: stop
		// it; the wait goroutine commits partial work and cleans up.
		if serr := s.cfg.Runtime.Stop(ctx, cid, s.cfg.StopGrace); serr != nil {
			slog.Warn("scheduler: stop container on close", "run", run, "error", serr)
		}
		return nil
	}
	// Unsupervised: the status is read and transitioned under s.mu so a
	// concurrent Kill or CloseRun cannot both win and overwrite each
	// other's terminal state.
	r, err := s.cfg.Store.GetRun(ctx, run)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	if r.Status != domain.RunNeedsAttention {
		s.mu.Unlock()
		return fmt.Errorf("%w: close requires needs-attention, run is %s", ErrInvalidTransition, r.Status)
	}
	err = s.transitionLocked(ctx, run, r.WorkspaceID, r.Status, outcome, "closed", actor)
	s.mu.Unlock()
	return err
}
