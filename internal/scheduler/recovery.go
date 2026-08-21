package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/runtime"
)

const (
	relaunchRequiresCheckout = "relaunch requires a terminal run with a preserved checkout"
	// exitProbeTimeout is the short non-destructive Wait window used on
	// startup to learn whether a container already exited before attach.
	exitProbeTimeout = 2 * time.Second
)

// Relaunch creates a new run from a terminal source: same session, task,
// harness, and mode, owned by the actor. The new checkout is cloned from
// refs/heads/<old.Branch> via Git.CreateRunCheckout and is named for the
// new run ID. The old row and its checkout are left untouched.
func (s *Scheduler) Relaunch(ctx context.Context, run domain.RunID, actor domain.MemberID) (*domain.Run, error) {
	if err := s.checkFreeSpace(); err != nil {
		return nil, err
	}
	old, err := s.cfg.Store.GetRun(ctx, run)
	if err != nil {
		return nil, err
	}
	if !old.Status.Terminal() || old.Worktree == "" || old.Branch == "" {
		return nil, fmt.Errorf("%w: %s", ErrInvalidTransition, relaunchRequiresCheckout)
	}
	if _, statErr := os.Stat(old.Worktree); statErr != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalidTransition, relaunchRequiresCheckout)
	}
	argv, profile, err := s.command(ctx, actor, old.Harness, old.Mode, old.Task)
	if err != nil {
		return nil, err
	}
	// A run the reboot interrupted still has the agent's own session behind
	// it, so the relaunch continues it where the harness can (failure
	// table, "Server reboot"). A run that finished on its own has nothing
	// to resume and starts fresh.
	if old.Status == domain.RunInterrupted {
		argv = harness.ResumeArgv(argv, profile.ResumeFlag)
	}
	m, err := s.cfg.Store.GetMember(ctx, actor)
	if err != nil {
		return nil, err
	}
	sess, err := s.cfg.Store.GetSession(ctx, old.SessionID)
	if err != nil {
		return nil, err
	}
	ws, err := s.cfg.Store.GetWorkspace(ctx, sess.WorkspaceID)
	if err != nil {
		return nil, err
	}
	published, err := s.cfg.Git.WorkspaceBranchExists(ctx, ws.ID, old.Branch)
	if err != nil {
		return nil, err
	}
	if !published {
		return nil, fmt.Errorf("%w: %s", ErrInvalidTransition, relaunchRequiresCheckout)
	}
	next := &domain.Run{
		SessionID: old.SessionID,
		MemberID:  actor,
		Task:      old.Task,
		Harness:   old.Harness,
		Mode:      old.Mode,
		Status:    domain.RunQueued,
	}
	// Checking for an active run in the same checkout and creating the new
	// row under one critical section serializes concurrent relaunches of
	// a still-in-use old tree. After the per-run-ID checkout fix two
	// actives never share a tree, but the guard still rejects a leftover
	// shared path.
	s.mu.Lock()
	active, err := s.cfg.Store.ListActiveRuns(ctx)
	if err == nil {
		for _, a := range active {
			if a.Worktree != "" && a.Worktree == old.Worktree {
				err = fmt.Errorf("%w: checkout already in use by active run %s", ErrInvalidTransition, a.ID)
				break
			}
		}
	}
	if err == nil {
		err = s.cfg.Store.CreateRun(ctx, next)
	}
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	checkout, branch, err := s.cfg.Git.CreateRunCheckout(ctx, ws.ID, next.ID, old.Branch, next.Task)
	if err != nil {
		s.failRelaunch(next, actor, fmt.Errorf("create checkout: %w", err))
		return nil, err
	}
	next.Worktree, next.Branch = checkout, branch
	if err := s.cfg.Store.UpdateRun(ctx, next); err != nil {
		s.failRelaunch(next, actor, fmt.Errorf("record checkout: %w", err))
		return nil, err
	}
	if err := s.provision(ctx, next, sess, ws, m, argv, profile, true); err != nil {
		return nil, err
	}
	return s.freshen(ctx, next), nil
}

// failRelaunch marks a queued relaunch row failed after checkout creation
// failed, walking queued -> provisioning -> failed so the transition table
// stays legal.
func (s *Scheduler) failRelaunch(run *domain.Run, actor domain.MemberID, cause error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s.mu.Lock()
	err := s.transitionLocked(ctx, run.ID, run.SessionID, domain.RunQueued, domain.RunProvisioning, "", actor)
	if err == nil {
		err = s.transitionLocked(ctx, run.ID, run.SessionID, domain.RunProvisioning, domain.RunFailed,
			"provisioning: "+cause.Error(), actor)
	}
	s.mu.Unlock()
	if err != nil {
		slog.Warn("scheduler: record relaunch checkout failure", "run", run.ID, "error", err)
	}
}

// recoverRuns reconciles the store's non-terminal runs against the
// runtime's actual containers on startup (§6.8): resume supervision where
// the container still runs, otherwise preserve the work and mark the run
// interrupted.
func (s *Scheduler) recoverRuns(ctx context.Context) error {
	active, err := s.cfg.Store.ListActiveRuns(ctx)
	if err != nil {
		return fmt.Errorf("scheduler: recovery: %w", err)
	}
	for _, r := range active {
		s.mu.Lock()
		_, alreadySupervised := s.runs[r.ID]
		s.mu.Unlock()
		if alreadySupervised {
			continue
		}
		switch r.Status {
		case domain.RunQueued, domain.RunProvisioning:
			s.recoverUnstarted(ctx, r)
		case domain.RunRunning, domain.RunNeedsAttention:
			s.recoverSupervised(ctx, r)
		}
	}
	// The sidecars that survived reconciliation are the live references to
	// staged bridge binaries; anything they no longer name is a build no
	// container holds.
	s.collectStagedBridges()
	return nil
}

// recoverUnstarted handles queued/provisioning rows whose launch died with
// the server. Any container created for the run must be destroyed before
// interrupting, or a still-running agent would keep mutating the checkout
// forever - and corrupt it alongside a relaunch. The sidecar (written
// right after Runtime.Start) names the container for the wide crash
// window; a crash in the narrow window between Runtime.Create and the
// sidecar write is reconciled through the container's creation key (the
// run ID, persisted by the runtime at Create).
func (s *Scheduler) recoverUnstarted(ctx context.Context, r *domain.Run) {
	var cid runtime.ID
	if sc, err := s.readSidecar(r.ID); err == nil && sc.ContainerID != "" {
		cid = runtime.ID(sc.ContainerID)
	} else if found, err := s.cfg.Runtime.FindByCreationKey(ctx, string(r.ID)); err == nil {
		cid = found
	} else if !errors.Is(err, runtime.ErrNotFound) {
		slog.Warn("scheduler: creation-key lookup during recovery", "run", r.ID, "error", err)
	}
	if cid != "" {
		if derr := s.cfg.Runtime.Destroy(ctx, cid); derr != nil {
			slog.Warn("scheduler: destroy orphaned container during recovery", "run", r.ID, "error", derr)
		}
	}
	if r.Worktree != "" {
		if _, cerr := s.cfg.Git.CommitAll(ctx, r.ID, "wip: "+taskLine(r.Task)); cerr != nil {
			slog.Warn("scheduler: wip commit during recovery", "run", r.ID, "error", cerr)
		}
		if _, perr := s.cfg.Git.PublishRunBranch(ctx, r.ID); perr != nil {
			slog.Warn("scheduler: publish branch during recovery", "run", r.ID, "error", perr)
		}
	}
	s.interrupt(ctx, r)
}

// interrupt marks a run interrupted ("server restarted"), preserving its
// checkout for relaunch. The status is re-read under s.mu so a steering
// call that raced recovery (e.g. a kill) is never overwritten.
func (s *Scheduler) interrupt(ctx context.Context, r *domain.Run) {
	s.mu.Lock()
	fresh, err := s.cfg.Store.GetRun(ctx, r.ID)
	if err == nil {
		if fresh.Status.Terminal() || s.runs[r.ID] != nil {
			s.mu.Unlock()
			return
		}
		err = s.transitionLocked(ctx, r.ID, r.SessionID, fresh.Status, domain.RunInterrupted, "server restarted", "")
	}
	s.mu.Unlock()
	if err != nil {
		slog.Warn("scheduler: mark run interrupted", "run", r.ID, "error", err)
		return
	}
	s.removeSidecar(r.ID)
}

func (s *Scheduler) recoverSupervised(ctx context.Context, r *domain.Run) {
	sc, err := s.readSidecar(r.ID)
	if err != nil {
		s.interrupt(ctx, r)
		return
	}
	cid := runtime.ID(sc.ContainerID)

	if sc.ExitObserved {
		s.finalizeObservedExit(ctx, r, sc)
		return
	}

	probeCtx, cancel := context.WithTimeout(ctx, exitProbeTimeout)
	st, waitErr := s.cfg.Runtime.Wait(probeCtx, cid)
	cancel()
	switch {
	case waitErr == nil:
		sc.ExitObserved = true
		sc.ExitCode = st.Code
		if werr := s.writeSidecar(sc); werr != nil {
			slog.Warn("scheduler: persist exit_observed during recovery", "run", r.ID, "error", werr)
		}
		s.finalizeObservedExit(ctx, r, sc)
		return
	case errors.Is(waitErr, runtime.ErrNotFound):
		s.didNotSurvive(ctx, r, cid)
		return
	case errors.Is(waitErr, context.DeadlineExceeded):
		s.attachAndSupervise(ctx, r, sc, cid)
		return
	case ctx.Err() != nil:
		return
	default:
		slog.Warn("scheduler: exit probe failed during recovery; retaining state", "run", r.ID, "error", waitErr)
		return
	}
}

func (s *Scheduler) finalizeObservedExit(_ context.Context, r *domain.Run, sc sidecar) {
	entry := s.entryFromSidecar(r, sc)
	if exitAlreadyRecorded(r, sc) {
		s.cleanupLeftoverContainer(context.Background(), entry.containerID, r.ID)
		return
	}
	s.finalize(entry, sc.ExitCode)
}

func exitAlreadyRecorded(r *domain.Run, sc sidecar) bool {
	if r.Status.Terminal() {
		return true
	}
	// Clean exit parks at needs-attention, which is still active: destroy
	// and drop the sidecar without re-attaching or flipping to interrupted.
	return sc.ExitCode == 0 && !sc.KillRequested && r.Status == domain.RunNeedsAttention
}

func (s *Scheduler) cleanupLeftoverContainer(ctx context.Context, cid runtime.ID, run domain.RunID) {
	if cid != "" {
		if derr := s.cfg.Runtime.Destroy(ctx, cid); derr != nil {
			slog.Warn("scheduler: destroy leftover container during recovery", "run", run, "error", derr)
		}
	}
	s.removeSidecar(run)
}

func (s *Scheduler) didNotSurvive(ctx context.Context, r *domain.Run, cid runtime.ID) {
	if r.Worktree != "" {
		if _, cerr := s.cfg.Git.CommitAll(ctx, r.ID, "wip: "+taskLine(r.Task)); cerr != nil {
			slog.Warn("scheduler: wip commit during recovery", "run", r.ID, "error", cerr)
		}
		if _, perr := s.cfg.Git.PublishRunBranch(ctx, r.ID); perr != nil {
			slog.Warn("scheduler: publish branch during recovery", "run", r.ID, "error", perr)
		}
	}
	if cid != "" {
		if derr := s.cfg.Runtime.Destroy(ctx, cid); derr != nil {
			slog.Warn("scheduler: destroy stale container during recovery", "run", r.ID, "error", derr)
		}
	}
	s.interrupt(ctx, r)
}

func (s *Scheduler) attachAndSupervise(ctx context.Context, r *domain.Run, sc sidecar, cid runtime.ID) {
	att, err := s.cfg.Runtime.Attach(ctx, cid)
	if err == nil {
		if serr := s.cfg.PTY.StartSession(ctx, r.ID, att); serr != nil {
			_ = att.Close()
			err = serr
		}
	}
	if err == nil {
		if werr := s.cfg.Git.StartDiffWatch(ctx, r.SessionID, r.ID); werr != nil {
			slog.Warn("scheduler: restart diff watch", "run", r.ID, "error", werr)
		}
		entry := s.entryFromSidecar(r, sc)
		entry.containerID = cid
		s.mu.Lock()
		s.runs[r.ID] = entry
		s.mu.Unlock()
		s.wg.Add(1)
		go s.superviseWait(entry)
		if sc.KillRequested {
			if serr := s.cfg.Runtime.Stop(ctx, cid, s.cfg.StopGrace); serr != nil {
				slog.Warn("scheduler: re-issue persisted kill during recovery", "run", r.ID, "error", serr)
			}
		}
		return
	}
	s.didNotSurvive(ctx, r, cid)
}

func (s *Scheduler) entryFromSidecar(r *domain.Run, sc sidecar) *supervised {
	started := time.Now().UTC()
	if r.StartedAt != nil {
		started = *r.StartedAt
	}
	return &supervised{
		runID:         r.ID,
		sessionID:     r.SessionID,
		workspaceID:   domain.WorkspaceID(sc.WorkspaceID),
		containerID:   runtime.ID(sc.ContainerID),
		task:          r.Task,
		memberID:      r.MemberID,
		harness:       r.Harness,
		status:        r.Status,
		startedAt:     started,
		paused:        sc.Paused,
		killRequested: sc.KillRequested,
		runUser:       sc.RunUser,
		exitObserved:  sc.ExitObserved,
		exitCode:      sc.ExitCode,
		bridgeDigest:  sc.BridgeDigest,
		bridgePath:    sc.BridgePath,
		coordDir:      sc.CoordDir,
	}
}
