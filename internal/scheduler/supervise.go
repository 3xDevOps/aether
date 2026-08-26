package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// finalizeTimeout bounds the post-exit work (commit, publish, destroy),
// which runs on a fresh context so shutdown cannot orphan half-finalized
// runs.
const finalizeTimeout = time.Minute

// superviseWait blocks on the container's main process and finalizes the
// run when it exits. Supervision-context cancellation (Close / server
// shutdown) ends supervision without touching the container or the run.
func (s *Scheduler) superviseWait(entry *supervised) {
	defer s.wg.Done()
	st, err := s.cfg.Runtime.Wait(s.superCtx, entry.containerID)
	if err != nil {
		if s.superCtx.Err() != nil {
			return
		}
		slog.Warn("scheduler: container wait failed; treating as crash", "run", entry.runID, "error", err)
		st = runtime.ExitStatus{Code: -1}
	}
	s.recordExitObserved(entry, st.Code)
	s.finalize(entry, st.Code)
}

// recordExitObserved durably persists the Wait result before finalize so a
// crash between Wait and status/commit resumes the original exit.
func (s *Scheduler) recordExitObserved(entry *supervised, code int) {
	s.mu.Lock()
	entry.exitObserved = true
	entry.exitCode = code
	sc := entry.sidecar()
	live := s.runs[entry.runID] == entry
	s.mu.Unlock()
	if !live {
		return
	}
	if err := s.writeSidecar(sc); err != nil {
		slog.Warn("scheduler: persist exit_observed", "run", entry.runID, "error", err)
	}
}

// finalize implements the pinned exit handling (§6.6): stop the watches,
// commit results ("aether:" on clean exit, "wip:" otherwise), publish the
// run branch, record the terminal-or-parked status, destroy the container,
// and drop the sidecar. Checkout and transcript are always preserved.
func (s *Scheduler) finalize(entry *supervised, code int) {
	ctx, cancel := context.WithTimeout(context.Background(), finalizeTimeout)
	defer cancel()

	s.cfg.Git.StopDiffWatch(entry.runID)
	if err := s.cfg.PTY.StopSession(ctx, entry.runID); err != nil {
		slog.Warn("scheduler: stop pty session", "run", entry.runID, "error", err)
	}

	s.mu.Lock()
	killed, killActor := entry.killRequested, entry.killActor
	s.mu.Unlock()

	msg := "wip: "
	if code == 0 && !killed {
		msg = "aether: "
	}
	if _, err := s.cfg.Git.CommitAll(ctx, entry.runID, msg+taskLine(entry.task)); err != nil {
		slog.Warn("scheduler: commit results", "run", entry.runID, "error", err)
	}
	if _, err := s.cfg.Git.PublishRunBranch(ctx, entry.runID); err != nil {
		slog.Warn("scheduler: publish run branch", "run", entry.runID, "error", err)
	}

	var (
		to     domain.RunStatus
		reason string
		actor  domain.MemberID
	)
	switch {
	case killed:
		to, reason, actor = domain.RunAbandoned, "killed", killActor
	case code == 0:
		to, reason = domain.RunNeedsAttention, "agent exited; results committed"
	default:
		to, reason = domain.RunFailed, fmt.Sprintf("agent exited %d", code)
	}
	s.mu.Lock()
	// A Kill accepted after the snapshot above still owns the outcome: the
	// caller was told the kill succeeded.
	if entry.killRequested {
		to, reason, actor = domain.RunAbandoned, "killed", entry.killActor
	}
	err := s.transitionLocked(ctx, entry.runID, entry.workspaceID, entry.status, to, reason, actor)
	delete(s.runs, entry.runID)
	s.mu.Unlock()
	// ErrInvalidTransition means the run already reached a terminal state
	// (e.g. CloseRun raced the exit); the cleanup below still applies.
	if err != nil && !errors.Is(err, ErrInvalidTransition) {
		slog.Warn("scheduler: record exit status", "run", entry.runID, "error", err)
	}

	if err := s.cfg.Runtime.Destroy(ctx, entry.containerID); err != nil {
		slog.Warn("scheduler: destroy container", "run", entry.runID, "error", err)
	}
	s.removeSidecar(entry.runID)
}

// checkStalls implements §6.7: a running, non-paused run with no PTY
// output and no file changes past StallThreshold parks at needs-attention;
// a stalled-but-alive run whose activity refreshes returns to running.
func (s *Scheduler) checkStalls(ctx context.Context) {
	s.mu.Lock()
	entries := make([]*supervised, 0, len(s.runs))
	for _, e := range s.runs {
		entries = append(entries, e)
	}
	s.mu.Unlock()

	now := time.Now().UTC()
	for _, e := range entries {
		s.mu.Lock()
		live := s.runs[e.runID] == e
		paused, status, started := e.paused, e.status, e.startedAt
		s.mu.Unlock()
		if !live || paused || (status != domain.RunRunning && status != domain.RunNeedsAttention) {
			continue
		}
		activity := started
		if t, ok := s.cfg.PTY.LastOutput(e.runID); ok && t.After(activity) {
			activity = t
		}
		if t, ok := s.cfg.Git.LastFileChange(e.runID); ok && t.After(activity) {
			activity = t
		}
		idle := now.Sub(activity)

		s.mu.Lock()
		if s.runs[e.runID] == e && !e.paused {
			var err error
			switch {
			case e.status == domain.RunRunning && idle > s.cfg.StallThreshold:
				err = s.transitionLocked(ctx, e.runID, e.workspaceID, e.status, domain.RunNeedsAttention,
					fmt.Sprintf("stalled: no output or file changes for %s", idle.Truncate(time.Second)), "")
			case e.status == domain.RunNeedsAttention && idle <= s.cfg.StallThreshold:
				err = s.transitionLocked(ctx, e.runID, e.workspaceID, e.status, domain.RunRunning,
					"activity resumed", "")
			}
			if err != nil {
				slog.Warn("scheduler: stall transition", "run", e.runID, "error", err)
			}
		}
		s.mu.Unlock()
	}
}

// sweepCheckouts applies the checkout TTL (§6.8): terminal runs whose
// checkout outlived CheckoutTTL lose the checkout directory - the branch
// and transcript are the artifacts and are never GC'd.
func (s *Scheduler) sweepCheckouts(ctx context.Context) {
	workspaces, err := s.cfg.Store.ListWorkspaces(ctx)
	if err != nil {
		slog.Warn("scheduler: checkout gc: list workspaces", "error", err)
		return
	}
	// Checkouts are per-run-ID (relaunch clones a new tree from the old
	// published branch), but skip reclaiming a path an active run still
	// names in case of a leftover shared tree.
	active, err := s.cfg.Store.ListActiveRuns(ctx)
	if err != nil {
		slog.Warn("scheduler: checkout gc: list active runs", "error", err)
		return
	}
	inUse := make(map[string]bool, len(active))
	for _, a := range active {
		if a.Worktree != "" {
			inUse[a.Worktree] = true
		}
	}
	cutoff := time.Now().UTC().Add(-s.cfg.CheckoutTTL)
	for _, ws := range workspaces {
		runs, err := s.cfg.Store.ListRunsByWorkspace(ctx, ws.ID)
		if err != nil {
			slog.Warn("scheduler: checkout gc: list runs", "workspace", ws.ID, "error", err)
			continue
		}
		for _, r := range runs {
			if !r.Status.Terminal() || r.Worktree == "" || r.FinishedAt == nil || r.FinishedAt.After(cutoff) {
				continue
			}
			if inUse[r.Worktree] {
				continue
			}
			if err := s.cfg.Git.RemoveRunCheckout(ctx, r.ID); err != nil {
				slog.Warn("scheduler: checkout gc: remove checkout", "run", r.ID, "error", err)
				continue
			}
			// Re-read before the full-row write: UpdateRun replaces every
			// mutable column, and the row listed above may be stale (e.g. a
			// concurrent handoff changed member_id).
			fresh, err := s.cfg.Store.GetRun(ctx, r.ID)
			if err != nil {
				slog.Warn("scheduler: checkout gc: reread run", "run", r.ID, "error", err)
				continue
			}
			fresh.Worktree = ""
			if err := s.cfg.Store.UpdateRun(ctx, fresh); err != nil {
				slog.Warn("scheduler: checkout gc: clear worktree", "run", r.ID, "error", err)
				continue
			}
			s.removeSidecar(r.ID)
		}
	}
}
