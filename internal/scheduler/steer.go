package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/ptyhost"
	"github.com/3xDevOps/Aether/internal/runtime"
)

func (s *Scheduler) Kill(ctx context.Context, run domain.RunID, actor domain.MemberID) error {
	s.mu.Lock()
	entry := s.runs[run]
	if entry == nil {
		if pending := s.pending[run]; pending != nil {
			pending.killRequested = true
			pending.killActor = actor
			s.mu.Unlock()
			return nil
		}
		s.mu.Unlock()
		return s.killUnsupervised(ctx, run, actor)
	}
	s.mu.Unlock()

	entry.lifecycleMu.Lock()
	defer entry.lifecycleMu.Unlock()
	s.mu.Lock()
	if s.runs[run] != entry {
		s.mu.Unlock()
		return s.killUnsupervised(ctx, run, actor)
	}
	if entry.destroyPending {
		s.mu.Unlock()
		return s.retryDestroyPendingLocked(ctx, entry)
	}
	if entry.status.Terminal() {
		retained := entry.retained
		s.mu.Unlock()
		if retained {
			return s.expireRetainedLocked(ctx, entry)
		}
		return nil
	}
	entry.killRequested = true
	entry.killActor = actor
	workspace, cid := entry.workspaceID, entry.containerID
	if err := s.writeSidecar(entry.sidecar()); err != nil {
		slog.Warn("scheduler: persist kill flag", "run", run, "error", err)
	}
	s.mu.Unlock()
	// No container yet (still provisioning): the provisioning checkpoints
	// see killRequested and abort. Not-found means the desired state is gone.
	if cid != "" {
		if err := s.cfg.Runtime.Stop(ctx, cid, s.cfg.StopGrace); err != nil && !errors.Is(err, runtime.ErrNotFound) {
			return err
		}
	}
	s.publishTimeline(ctx, workspace, run, actor, events.TimelineKill, "")
	return nil
}

// reconcileActiveDestroyPending admits a durable destroy-pending sidecar for
// an active row before an unsupervised Kill can terminalize it. The sidecar is
// an ownership record even when it has no container ID yet: the admitted owner
// keeps the row and checkout protected while creation-key cleanup is retried.
func (s *Scheduler) reconcileActiveDestroyPending(ctx context.Context, id domain.RunID) (bool, error) {
	s.mu.Lock()
	if s.runs[id] != nil || s.pending[id] != nil {
		s.mu.Unlock()
		return true, nil
	}
	r, err := s.cfg.Store.GetRun(ctx, id)
	if err != nil {
		s.mu.Unlock()
		return false, err
	}
	if r.Status.Terminal() {
		s.mu.Unlock()
		return false, nil
	}
	sc, err := s.readSidecar(id)
	if err != nil {
		s.mu.Unlock()
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("scheduler: inspect destroy-pending sidecar: %w", err)
	}
	if sc.RunID != "" && sc.RunID != string(id) {
		s.mu.Unlock()
		return false, fmt.Errorf("scheduler: destroy-pending sidecar run ID mismatch")
	}
	if !sc.DestroyPending {
		s.mu.Unlock()
		return false, nil
	}
	s.mu.Unlock()

	s.recoverDestroyMetadata(ctx, runtime.ID(sc.ContainerID), &sc)
	owner, admitted := s.admitDestroyPendingOwner(ctx, r, sc, runtime.ID(sc.ContainerID))
	if admitted {
		// Kill reacquires the exact owner's lifecycle lock and performs the
		// physical cleanup. Releasing here keeps this helper non-blocking
		// with respect to the caller's ordinary steering path.
		owner.lifecycleMu.Unlock()
		return true, nil
	}

	// Admission may lose to recovery or another steering call, but a durable
	// pending marker must never fall through to an ordinary row transition.
	s.mu.Lock()
	owned := s.runs[id] != nil || s.pending[id] != nil
	s.mu.Unlock()
	if owned {
		return true, nil
	}
	current, readErr := s.readSidecar(id)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return false, nil
		}
		return false, fmt.Errorf("scheduler: recheck destroy-pending sidecar: %w", readErr)
	}
	if current.DestroyPending {
		return false, errors.New("scheduler: destroy-pending owner admission failed")
	}
	return true, nil
}

// under s.mu: every scheduler status write holds the lock, so the locked
// read is authoritative and a concurrent terminal transition cannot be
// overwritten.
func (s *Scheduler) killUnsupervised(ctx context.Context, id domain.RunID, actor domain.MemberID) error {
	r, err := s.cfg.Store.GetRun(ctx, id)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if s.runs[id] != nil {
		// A launch registered the run meanwhile: route through the
		// supervised path.
		s.mu.Unlock()
		return s.Kill(ctx, id, actor)
	}
	if pending := s.pending[id]; pending != nil {
		pending.killRequested = true
		pending.killActor = actor
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()

	if r.Status.Terminal() {
		// A terminal retained row can outlive the in-memory owner during
		// startup, before recoverRuns has had a chance to adopt its sidecar.
		// Reuse DeleteRun's reconciliation so Kill has the same durable
		// ownership and retry behavior instead of treating that row as
		// a no-op and leaking its container.
		retainedEntry, retry, reconcileErr := s.reconcileRetainedSidecarForDelete(ctx, id)
		if reconcileErr != nil {
			return reconcileErr
		}
		if retainedEntry != nil || retry {
			// Let Kill reacquire an entry's lifecycle lock before deciding
			// between expiry and a terminal no-op. A retry with no owner
			// means a pending launch or a row that moved back to active;
			// route it through Kill as well so that state is not discarded.
			return s.Kill(ctx, id, actor)
		}
		return nil
	}

	// Resolve an active durable cleanup owner before committing/publishing
	// the ordinary kill transition. This is needed when a process rebooted
	// after recording DestroyPending but before recoverRuns admitted it.
	if handled, reconcileErr := s.reconcileActiveDestroyPending(ctx, id); reconcileErr != nil {
		return reconcileErr
	} else if handled {
		return s.Kill(ctx, id, actor)
	}

	if r.Worktree != "" {
		if _, cerr := s.commitAll(ctx, id, "wip: "+taskLine(r.Task)); cerr != nil {
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
	if pending := s.pending[id]; pending != nil {
		pending.killRequested = true
		pending.killActor = actor
		s.mu.Unlock()
		return nil
	}
	r, err = s.cfg.Store.GetRun(ctx, id)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	if r.Status.Terminal() {
		s.mu.Unlock()
		return s.killUnsupervised(ctx, id, actor)
	}
	// Sidecar writes are serialized by s.mu. Recheck immediately before the
	// status write so a pending marker installed after the first probe still
	// owns cleanup and cannot be bypassed by this transition.
	if sc, serr := s.readSidecar(id); serr == nil && sc.DestroyPending {
		s.mu.Unlock()
		return s.killUnsupervised(ctx, id, actor)
	} else if serr != nil && !os.IsNotExist(serr) {
		s.mu.Unlock()
		return fmt.Errorf("scheduler: inspect destroy-pending sidecar: %w", serr)
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

// DeleteRun stops a live run if necessary, waits for supervision to release
// its checkout, removes the checkout and transcripts, and then removes the
// durable run record and its dependent data. The published branch remains.
// Terminal runs are deleted directly; deleting never leaves a live container.
func (s *Scheduler) DeleteRun(ctx context.Context, run domain.RunID, actor domain.MemberID) error {
	var workspace domain.WorkspaceID
	for {
		s.mu.Lock()
		entry := s.runs[run]
		pending := s.pending[run]
		pendingDestroy := entry != nil && entry.destroyPending
		terminal := entry != nil && entry.status.Terminal()
		retained := entry != nil && entry.retained
		var done <-chan struct{}
		if entry != nil {
			done = entry.done
			workspace = entry.workspaceID
		}
		s.mu.Unlock()

		if entry == nil && pending != nil {
			if err := s.waitPending(ctx, run); err != nil {
				return err
			}
			continue
		}
		if entry != nil {
			if pendingDestroy {
				if err := s.Kill(ctx, run, actor); err != nil {
					return err
				}
				continue
			}
			if retained {
				if err := s.expireRetained(ctx, entry); err != nil {
					return err
				}
				continue
			}
			if !terminal {
				if err := s.Kill(ctx, run, actor); err != nil {
					return err
				}
			}
			if done != nil {
				select {
				case <-done:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			break
		}

		current, err := s.cfg.Store.GetRun(ctx, run)
		if err != nil {
			return err
		}
		workspace = current.WorkspaceID
		if !current.Status.Terminal() {
			if killErr := s.Kill(ctx, run, actor); killErr != nil {
				return killErr
			}
			continue
		}
		retainedEntry, retry, err := s.reconcileRetainedSidecarForDelete(ctx, run)
		if err != nil {
			return err
		}
		if retry {
			continue
		}
		if retainedEntry != nil {
			if err := s.expireRetained(ctx, retainedEntry); err != nil {
				return err
			}
			continue
		}
		break
	}

	if err := s.cfg.Git.RemoveRunCheckout(ctx, run); err != nil {
		return fmt.Errorf("scheduler: delete run checkout: %w", err)
	}
	if err := s.cfg.PTY.RemoveRunTranscripts(ctx, run); err != nil {
		return fmt.Errorf("scheduler: delete run transcripts: %w", err)
	}
	if err := s.cfg.Store.DeleteRun(ctx, run); err != nil {
		return err
	}
	s.publish(ctx, events.Event{
		WorkspaceID: workspace,
		RunID:       run,
		ActorID:     actor,
		Payload:     events.RunDeletedPayload{},
	})
	return nil
}

// reconcileRetainedSidecarForDelete atomically checks for a concurrently
// installed owner and adopts a durable retained or destroy-pending sidecar
// when no owner exists. Destroy-pending ownership is returned with pending
// precedence so callers retry physical cleanup rather than retained expiry.
// The adopted entry is deliberately not given a Wait owner: DeleteRun owns
// the lifecycle until destruction is confirmed.
func (s *Scheduler) reconcileRetainedSidecarForDelete(ctx context.Context, run domain.RunID) (*supervised, bool, error) {
	s.mu.Lock()
	if entry := s.runs[run]; entry != nil {
		s.mu.Unlock()
		return entry, true, nil
	}
	if s.pending[run] != nil {
		s.mu.Unlock()
		return nil, true, nil
	}
	current, err := s.cfg.Store.GetRun(ctx, run)
	if err != nil {
		s.mu.Unlock()
		return nil, false, err
	}
	if !current.Status.Terminal() {
		s.mu.Unlock()
		return nil, true, nil
	}
	sc, err := s.readSidecar(run)
	if err != nil {
		s.mu.Unlock()
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("scheduler: inspect retained sidecar: %w", err)
	}
	if sc.DestroyPending {
		s.mu.Unlock()
		s.recoverDestroyMetadata(ctx, runtime.ID(sc.ContainerID), &sc)
		entry, admitted := s.admitDestroyPendingOwner(ctx, current, sc, runtime.ID(sc.ContainerID))
		if admitted {
			entry.lifecycleMu.Unlock()
			return entry, true, nil
		}
		// Another lifecycle path may have won while metadata was being
		// inspected. Never fall through to destructive row deletion while
		// a pending marker remains durable.
		s.mu.Lock()
		entry = s.runs[run]
		pending := s.pending[run] != nil
		s.mu.Unlock()
		if entry != nil || pending {
			return entry, true, nil
		}
		return nil, true, nil
	}
	mode := sc.Mode
	if mode == "" {
		mode = current.Mode
	}
	if sc.RunID != string(run) || sc.ContainerID == "" ||
		mode == "" || (!sc.Retained && sc.RetainedUntil == nil) {
		s.mu.Unlock()
		return nil, false, nil
	}
	entry := s.entryFromSidecar(current, sc)
	entry.retained = true
	s.runs[run] = entry
	s.syncRunUserReservationsLocked()
	s.mu.Unlock()
	return entry, false, nil

}

func (s *Scheduler) Pause(ctx context.Context, run domain.RunID, actor domain.MemberID) error {
	s.mu.Lock()
	entry := s.runs[run]
	s.mu.Unlock()
	if entry == nil {
		return fmt.Errorf("%w: run has no live container", ErrInvalidTransition)
	}
	entry.lifecycleMu.Lock()
	defer entry.lifecycleMu.Unlock()
	s.mu.Lock()
	if s.runs[run] != entry || entry.containerID == "" ||
		entry.status.Terminal() || entry.retained || entry.destroyPending || entry.finalizing {
		s.mu.Unlock()
		return fmt.Errorf("%w: run cannot be paused in its current state", ErrInvalidTransition)
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

// Resume thaws a paused run. Retained terminal containers reopen only through
// Relaunch, which performs the admission checks for re-entry.
func (s *Scheduler) Resume(ctx context.Context, run domain.RunID, actor domain.MemberID) error {
	s.mu.Lock()
	entry := s.runs[run]
	s.mu.Unlock()
	if entry == nil {
		return fmt.Errorf("%w: run has no live container", ErrInvalidTransition)
	}
	entry.lifecycleMu.Lock()
	defer entry.lifecycleMu.Unlock()
	s.mu.Lock()
	if s.runs[run] != entry || entry.containerID == "" ||
		entry.status.Terminal() || entry.retained || entry.destroyPending || entry.finalizing {
		s.mu.Unlock()
		return fmt.Errorf("%w: retained or terminal runs must use Relaunch", ErrInvalidTransition)
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

// Inject writes a steering message to the live run agent's PTY, ending
// with the harness's submit sequence so the text reaches the agent's
// conversation rather than sitting in its input box.
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
	r, err := s.cfg.Store.GetRun(ctx, run)
	if err != nil {
		return err
	}
	submit := "\r"
	if p, ok := harness.Lookup(r.Harness); ok {
		submit = p.SteerSuffix()
	}
	if err := s.cfg.PTY.Inject(ctx, ptyhost.RunSession(run), m.DisplayName, m.Color, message, submit); err != nil {
		return err
	}
	s.publishTimeline(ctx, workspace, run, actor, events.TimelineSteer, message)
	s.RecordSteer(ctx, run, actor)
	return nil
}

// persistRetainedSidecar makes both the sidecar contents and its directory
// entry durable. CloseRun calls it before writing the terminal row, so a
// reboot can never see the retained promise without the ownership marker.
func (s *Scheduler) persistRetainedSidecar(sc sidecar) error {
	if err := s.writeSidecar(sc); err != nil {
		return fmt.Errorf("scheduler: persist retained close: %w", err)
	}
	if err := fsyncDir(s.cfg.StateDir); err != nil {
		return fmt.Errorf("scheduler: persist retained close: %w", err)
	}
	return nil
}

// CloseRun resolves a run's outcome on a human's say-so. A live TUI run is
// detached, paused, committed, published, and retained in its exact
// container; all other runs follow the immediate stop-and-destroy path.
func (s *Scheduler) CloseRun(ctx context.Context, run domain.RunID, actor domain.MemberID, outcome domain.RunStatus) error {
	if outcome != domain.RunMerged && outcome != domain.RunAbandoned {
		return fmt.Errorf("%w: close outcome must be merged or abandoned, got %q", ErrInvalidTransition, outcome)
	}

	s.mu.Lock()
	if pending := s.pending[run]; pending != nil {
		s.mu.Unlock()
		return fmt.Errorf("%w: run %s is still provisioning", ErrInvalidTransition, run)
	}
	entry := s.runs[run]
	s.mu.Unlock()
	if entry == nil {
		// Recovery deliberately leaves an inconclusive active probe
		// unsupervised. Before closing such a row, reconcile its durable
		// sidecar so a live harness cannot keep mutating the checkout after
		// the terminal status is published.
		r, err := s.cfg.Store.GetRun(ctx, run)
		if err != nil {
			return err
		}
		if r.Status == domain.RunQueued {
			return fmt.Errorf("%w: run %s is still provisioning", ErrInvalidTransition, run)
		}
		sc, serr := s.readSidecar(run)
		if serr != nil && !os.IsNotExist(serr) {
			return fmt.Errorf("scheduler: reconcile close sidecar: %w", serr)
		}
		if serr == nil && sc.ContainerID != "" {
			if sc.RunID != "" && sc.RunID != string(run) {
				return fmt.Errorf("scheduler: reconcile close sidecar: run ID mismatch")
			}
			s.mu.Lock()
			if r.Status.Terminal() {
				entry = s.adoptRetainedOwnerLocked(r, sc)
			}
			s.mu.Unlock()
			if !r.Status.Terminal() {
				entry = s.adoptLiveSidecar(r, sc)
			}
			if entry != nil {
				// A single Wait owner is required even though recovery's
				// short probe did not establish liveness.
				s.startSupervision(entry)
			}
		}
		if entry == nil {
			// No durable container owner exists, so this is an ordinary
			// unsupervised terminal transition.
			if r.Status == outcome {
				return nil
			}
			s.mu.Lock()
			err = s.transitionLocked(ctx, run, r.WorkspaceID, r.Status, outcome, "closed", actor)
			s.mu.Unlock()
			return err
		}
	}

	entry.lifecycleMu.Lock()
	defer entry.lifecycleMu.Unlock()
	s.mu.Lock()
	if s.runs[run] != entry {
		s.mu.Unlock()
		return retainedTransitionError()
	}
	if entry.destroyPending {
		s.mu.Unlock()
		return fmt.Errorf("%w: run cleanup is pending", ErrInvalidTransition)
	}
	if entry.finalizing {
		s.mu.Unlock()
		return fmt.Errorf("%w: run finalization is in progress", ErrInvalidTransition)
	}
	status, workspace, cid := entry.status, entry.workspaceID, entry.containerID
	mode, retained, alreadyPaused := entry.launchMode, entry.retained, entry.paused
	if status == outcome && (!retained || s.cfg.RunContainerTTL >= 0) {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()

	if retained {
		// Re-labeling an already closed retained run keeps the same deadline
		// and container; no agent or checkout operation is repeated. Verify
		// the ownership marker is durable before changing the row again.
		s.mu.Lock()
		retainedSidecar := entry.sidecar()
		s.mu.Unlock()
		if err := s.persistRetainedSidecar(retainedSidecar); err != nil {
			return err
		}
		s.mu.Lock()
		if s.runs[run] != entry {
			s.mu.Unlock()
			return retainedTransitionError()
		}
		err := s.transitionLocked(ctx, run, workspace, status, outcome, retainedCloseReason, actor)
		s.mu.Unlock()
		return err
	}

	if mode == domain.LaunchTUI && !status.Terminal() {
		// Detach before committing so no PTY client can continue typing while
		// the close operation snapshots the worktree.
		s.cfg.Git.StopDiffWatch(run)
		_ = s.cfg.PTY.StopSession(context.WithoutCancel(ctx), ptyhost.RunSession(run))
		s.cfg.PTY.StopSessionsWithPrefix(context.WithoutCancel(ctx), string(ptyhost.RunShellSession(run, "")))
		paused := alreadyPaused
		if !paused {
			if err := s.cfg.Runtime.Pause(ctx, cid); err != nil && !errors.Is(err, runtime.ErrNotFound) {
				slog.Warn("scheduler: pause closed TUI run", "run", run, "error", err)
			} else if err == nil {
				paused = true
			}
		}
		if paused {
			msg := "wip: "
			if outcome == domain.RunMerged {
				msg = "aether: "
			}
			if _, cerr := s.commitAll(ctx, run, msg+taskLine(entry.task)); cerr != nil {
				slog.Warn("scheduler: commit closed TUI run", "run", run, "error", cerr)
			}
			if _, perr := s.cfg.Git.PublishRunBranch(ctx, run); perr != nil {
				slog.Warn("scheduler: publish closed TUI run", "run", run, "error", perr)
			}
			if s.cfg.RunContainerTTL < 0 {
				s.mu.Lock()
				err := s.transitionLocked(ctx, run, workspace, status, outcome, "closed", actor)
				s.mu.Unlock()
				if err != nil {
					if restoreErr := s.restoreAfterCloseFailure(ctx, entry, alreadyPaused); restoreErr != nil {
						return errors.Join(err, restoreErr)
					}
					return err
				}
				s.stopCloseContainer(ctx, cid)
				return nil
			}
			deadline := time.Now().UTC().Add(s.cfg.RunContainerTTL)
			s.mu.Lock()
			if s.runs[run] != entry {
				s.mu.Unlock()
				return retainedTransitionError()
			}
			// Install the retained ownership marker before the terminal
			// row. If the process dies between these writes, recovery sees
			// an active row and clears the stale marker rather than
			// destroying the container; once the row is terminal, the
			// marker is already durable and protects the promise.
			preCloseSidecar := entry.sidecar()
			rollbackSidecar := preCloseSidecar
			rollbackSidecar.Paused = true
			rollbackSidecar.Retained = false
			rollbackSidecar.RetainedUntil = nil
			retainedSidecar := preCloseSidecar
			retainedSidecar.Paused = true
			retainedSidecar.Retained = true
			retainedSidecar.RetainedUntil = &deadline
			persistErr := s.persistRetainedSidecar(retainedSidecar)
			if persistErr != nil {
				rollbackErr := s.persistRetainedSidecar(rollbackSidecar)
				s.mu.Unlock()
				restoreErr := s.restoreAfterCloseFailure(ctx, entry, alreadyPaused)
				if rollbackErr != nil || restoreErr != nil {
					return errors.Join(persistErr, rollbackErr, restoreErr)
				}
				return persistErr
			}
			transitionErr := s.transitionLocked(ctx, run, workspace, status, outcome, retainedCloseReason, actor)
			if transitionErr == nil {
				entry.status = outcome
				entry.paused = true
				entry.retained = true
				entry.retainedUntil = &deadline
			}
			s.mu.Unlock()
			if transitionErr != nil {
				// A failed row write leaves entry in its pre-close
				// state. The rollback restores that state, including
				// replacing this marker before reattaching the session.
				if rollbackErr := s.restoreAfterCloseFailure(ctx, entry, alreadyPaused); rollbackErr != nil {
					return errors.Join(transitionErr, rollbackErr)
				}
				return transitionErr
			}
			return nil
		}
	}
	// Headless runs, negative-TTL TUI runs, and pause failures are immediate.
	s.mu.Lock()
	err := s.transitionLocked(ctx, run, workspace, status, outcome, "closed", actor)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	s.stopCloseContainer(ctx, cid)
	return nil
}

// restoreAfterCloseFailure returns a detached TUI run to its exact
// pre-close interaction state after a retained close step failed. The
// container is paused while the restoration is in progress, so every
// confirmed runtime state is persisted before the next potentially failing
// step.
func (s *Scheduler) restoreAfterCloseFailure(ctx context.Context, entry *supervised, alreadyPaused bool) error {
	// CloseRun paused every formerly-running run before reaching the store
	// transition. Keep the in-memory and sidecar state truthful until Resume
	// confirms that the container is running again.
	s.setPaused(entry, true)

	resumed := false
	if !alreadyPaused {
		if err := s.cfg.Runtime.Resume(ctx, entry.containerID); err != nil {
			return fmt.Errorf("scheduler: restore closed run: resume: %w", err)
		}
		resumed = true
		s.setPaused(entry, false)
	}

	att, err := s.cfg.Runtime.Attach(ctx, entry.containerID)
	if err != nil {
		return s.closeRollbackFailure(ctx, entry, alreadyPaused, resumed,
			fmt.Errorf("scheduler: restore closed run: attach: %w", err))
	}
	if err := s.cfg.PTY.StartSession(ctx, ptyhost.RunSession(entry.runID), att); err != nil {
		_ = att.Close()
		_ = s.cfg.PTY.StopSession(context.WithoutCancel(ctx), ptyhost.RunSession(entry.runID))
		return s.closeRollbackFailure(ctx, entry, alreadyPaused, resumed,
			fmt.Errorf("scheduler: restore closed run: pty: %w", err))
	}
	if err := s.cfg.Git.StartDiffWatch(ctx, entry.workspaceID, entry.runID); err != nil {
		s.cfg.Git.StopDiffWatch(entry.runID)
		_ = s.cfg.PTY.StopSession(context.WithoutCancel(ctx), ptyhost.RunSession(entry.runID))
		return s.closeRollbackFailure(ctx, entry, alreadyPaused, resumed,
			fmt.Errorf("scheduler: restore closed run: diff watch: %w", err))
	}
	// For an originally paused run, Attach/PTY/watch do not thaw the
	// container, so the confirmed state remains paused. For an originally
	// running run, Resume already established the running state.
	s.setPaused(entry, alreadyPaused)
	return nil
}

// closeRollbackFailure cleans up resources created by a partial rollback and
// compensates a confirmed Resume. A failed compensating Pause leaves the last
// confirmed state (running) in memory and in the sidecar rather than claiming
// that the container is paused.
func (s *Scheduler) closeRollbackFailure(ctx context.Context, entry *supervised, alreadyPaused, resumed bool, cause error) error {
	if alreadyPaused || !resumed {
		s.setPaused(entry, true)
		return cause
	}
	if err := s.cfg.Runtime.Pause(context.WithoutCancel(ctx), entry.containerID); err != nil {
		s.setPaused(entry, false)
		return errors.Join(cause, fmt.Errorf("scheduler: restore closed run: pause: %w", err))
	}
	s.setPaused(entry, true)
	return cause
}

func (s *Scheduler) stopCloseContainer(ctx context.Context, cid runtime.ID) {
	if cid == "" {
		return
	}
	if err := s.cfg.Runtime.Stop(ctx, cid, s.cfg.StopGrace); err != nil && !errors.Is(err, runtime.ErrNotFound) {
		slog.Warn("scheduler: stop container behind closed run", "container", cid, "error", err)
	}
}
