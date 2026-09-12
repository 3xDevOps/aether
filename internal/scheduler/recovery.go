package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/agentstatus"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/ptyhost"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/store"
)

const (
	retainedCloseReason       = "closed; retained container"
	retainedExpiredReason     = "retained container expired"
	retainedUnavailableReason = "retained container unavailable"
	// exitProbeTimeout is the short non-destructive Wait window used on
	// startup to learn whether a container already exited before attach.
	exitProbeTimeout = 2 * time.Second
)

func retainedTransitionError() error {
	return fmt.Errorf("%w: %s", ErrInvalidTransition, retainedUnavailableReason)
}

// Relaunch reopens the exact retained TUI run and container. It never creates
// a run row, checkout, branch, or replacement container.
func (s *Scheduler) Relaunch(ctx context.Context, run domain.RunID, actor domain.MemberID) (*domain.Run, error) {
	old, err := s.cfg.Store.GetRun(ctx, run)
	if err != nil {
		return nil, err
	}
	if old.Mode != domain.LaunchTUI ||
		(old.Status != domain.RunMerged && old.Status != domain.RunAbandoned) ||
		old.Reason != retainedCloseReason {
		return nil, retainedTransitionError()
	}

	// A negative retention policy is an explicit no-retention mode. A
	// durable sidecar still owns its container (and coordination assets)
	// until destruction is confirmed, but it must never be adopted.
	if s.cfg.RunContainerTTL < 0 {
		s.mu.Lock()
		entry := s.runs[run]
		s.mu.Unlock()
		if entry != nil && entry.retained {
			if derr := s.expireRetained(ctx, entry); derr != nil {
				return nil, errors.Join(retainedTransitionError(), derr)
			}
		} else if entry == nil {
			sc, serr := s.readSidecar(run)
			if serr == nil && (sc.Retained || sc.RetainedUntil != nil) && sc.ContainerID != "" {
				if entry := s.installRetainedDestroyOwner(ctx, old, sc); entry != nil {
					if derr := s.expireRetained(ctx, entry); derr != nil {
						return nil, errors.Join(retainedTransitionError(), derr)
					}
				}
			}
		}
		return nil, retainedTransitionError()
	}

	s.mu.Lock()
	entry := s.runs[run]
	adopted := false
	if entry == nil {
		sc, serr := s.readSidecar(run)
		if serr == nil && (sc.Retained || sc.RetainedUntil != nil) &&
			sc.Mode == domain.LaunchTUI && sc.ContainerID != "" {
			entry = s.entryFromSidecar(old, sc)
			entry.retained = true
			s.runs[run] = entry
			s.syncRunUserReservationsLocked()
			adopted = true
		}
	}
	s.mu.Unlock()
	if entry == nil {
		return nil, retainedTransitionError()
	}
	if adopted {
		// Sidecar fallback adoption must have the same single Wait owner as
		// normal recovery, otherwise a natural exit leaves the row stranded.
		s.startSupervision(entry)
	}
	entry.lifecycleMu.Lock()
	defer entry.lifecycleMu.Unlock()
	s.mu.Lock()
	fresh, ferr := s.cfg.Store.GetRun(ctx, run)
	deadline := entry.retainedUntil
	if ferr != nil {
		s.mu.Unlock()
		return nil, ferr
	}
	valid := s.runs[run] == entry && entry.retained && fresh.Mode == domain.LaunchTUI &&
		(fresh.Status == domain.RunMerged || fresh.Status == domain.RunAbandoned) &&
		fresh.Reason == retainedCloseReason && deadline != nil && time.Now().UTC().Before(*deadline)
	paused, cid := entry.paused, entry.containerID
	s.mu.Unlock()
	if !valid {
		if deadline == nil || !time.Now().UTC().Before(*deadline) {
			if deadline == nil {
				s.mu.Lock()
				if s.runs[run] == entry && entry.retained {
					s.markRetainedDestroyDueLocked(entry)
				}
				s.mu.Unlock()
			}
			if expireErr := s.expireRetainedLocked(ctx, entry); expireErr != nil {
				return nil, expireErr
			}
		}
		return nil, retainedTransitionError()
	}

	resumed := false
	if paused {
		if resumeErr := s.cfg.Runtime.Resume(ctx, cid); resumeErr != nil {
			return nil, errors.Join(retainedTransitionError(), resumeErr)
		}
		resumed = true
		s.mu.Lock()
		if s.runs[run] == entry {
			entry.paused = false
			if werr := s.writeSidecar(entry.sidecar()); werr != nil {
				slog.Warn("scheduler: persist resumed retained run", "run", run, "error", werr)
			}
		}
		s.mu.Unlock()
	}

	rollback := func(cause error) error {
		s.cfg.Git.StopDiffWatch(run)
		_ = s.cfg.PTY.StopSession(context.WithoutCancel(ctx), ptyhost.RunSession(run))
		mustPause := resumed || !paused
		if mustPause {
			if perr := s.cfg.Runtime.Pause(context.WithoutCancel(ctx), cid); perr != nil &&
				!errors.Is(perr, runtime.ErrNotFound) {
				return errors.Join(cause, fmt.Errorf("scheduler: relaunch rollback pause: %w", perr))
			}
		}
		s.mu.Lock()
		var werr error
		if s.runs[run] == entry {
			entry.paused = true
			entry.retained = true
			werr = s.writeSidecar(entry.sidecar())
		}
		s.mu.Unlock()
		if werr != nil {
			return errors.Join(cause, fmt.Errorf("scheduler: relaunch rollback sidecar: %w", werr))
		}
		return cause
	}

	att, err := s.cfg.Runtime.Attach(ctx, cid)
	if err != nil {
		return nil, rollback(err)
	}
	if startSessionErr := s.cfg.PTY.StartSession(ctx, ptyhost.RunSession(run), att); startSessionErr != nil {
		_ = att.Close()
		return nil, rollback(startSessionErr)
	}
	if diffWatchErr := s.cfg.Git.StartDiffWatch(ctx, fresh.WorkspaceID, run); diffWatchErr != nil {
		return nil, rollback(diffWatchErr)
	}

	now := time.Now().UTC()
	s.mu.Lock()
	if s.runs[run] != entry {
		s.mu.Unlock()
		return nil, rollback(retainedTransitionError())
	}
	fresh, err = s.cfg.Store.GetRun(ctx, run)
	if err == nil {
		fresh.Status = domain.RunRunning
		fresh.Reason = ""
		fresh.StartedAt = &now
		fresh.FinishedAt = nil
		err = s.cfg.Store.UpdateRun(ctx, fresh)
	}
	s.mu.Unlock()
	if err != nil {
		return nil, rollback(err)
	}

	s.mu.Lock()
	entry.status = domain.RunRunning
	entry.startedAt = now
	entry.retained = false
	entry.retainedUntil = nil
	entry.paused = false
	s.publish(ctx, events.Event{
		WorkspaceID: fresh.WorkspaceID,
		RunID:       run,
		ActorID:     actor,
		Payload:     events.RunStatusPayload{From: old.Status, To: domain.RunRunning},
	})
	if werr := s.writeSidecar(entry.sidecar()); werr != nil {
		slog.Warn("scheduler: clear retained sidecar after relaunch", "run", run, "error", werr)
	}
	s.mu.Unlock()
	return fresh, nil

}

// installRetainedDestroyOwner installs the sidecar's exact container as a
// retained owner before a boot-time destroy. That makes an uncertain destroy
// visible to the in-memory lifecycle and gives the bounded sweep a due entry
// to retry.
func (s *Scheduler) installRetainedDestroyOwner(ctx context.Context, r *domain.Run, scanned sidecar) *supervised {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry := s.runs[r.ID]; entry != nil {
		if !entry.retained || entry.containerID == "" ||
			(scanned.ContainerID != "" && entry.containerID != runtime.ID(scanned.ContainerID)) {
			return nil
		}
		s.markRetainedDestroyDueLocked(entry)
		return entry
	}
	fresh, err := s.cfg.Store.GetRun(ctx, r.ID)
	if err != nil {
		slog.Warn("scheduler: reload retained run before destroy retry", "run", r.ID, "error", err)
		return nil
	}
	current, err := s.readSidecar(r.ID)
	if err != nil {
		slog.Warn("scheduler: reload retained sidecar before destroy retry", "run", r.ID, "error", err)
		return nil
	}
	mode := current.Mode
	if mode == "" {
		mode = fresh.Mode
	}
	if fresh.Mode != domain.LaunchTUI || !fresh.Status.Terminal() ||
		fresh.Reason != retainedCloseReason || mode != domain.LaunchTUI ||
		(!current.Retained && current.RetainedUntil == nil) || current.ContainerID == "" ||
		(scanned.ContainerID != "" && current.ContainerID != scanned.ContainerID) {
		return nil
	}
	current.Mode = mode
	entry := s.entryFromSidecar(fresh, current)
	entry.retained = true
	s.runs[r.ID] = entry
	s.syncRunUserReservationsLocked()
	s.markRetainedDestroyDueLocked(entry)
	return entry
}

// startRetainedWaitAfterDestroyFailure adds a Wait owner only when the
// retained container is still alive. startSupervision supplies the
// single-owner check when another recovery path already won the race.
func (s *Scheduler) startRetainedWaitAfterDestroyFailure(ctx context.Context, entry *supervised) {
	if entry == nil || ctx.Err() != nil {
		return
	}
	s.mu.Lock()
	eligible := s.runs[entry.runID] == entry && entry.retained && !entry.waitStarted
	s.mu.Unlock()
	if !eligible {
		return
	}
	probeCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	_, err := s.cfg.Runtime.Wait(probeCtx, entry.containerID)
	cancel()
	if errors.Is(err, context.DeadlineExceeded) {
		s.startSupervision(entry)
	}
}

// markRetainedDestroyDueLocked keeps failed destroy ownership eligible for
// the next bounded sweep. The sidecar remains the durable ownership record.
func (s *Scheduler) markRetainedDestroyDueLocked(entry *supervised) {
	if entry == nil || s.runs[entry.runID] != entry || !entry.retained {
		return
	}
	now := time.Now().UTC()
	entry.retainedUntil = &now
	if err := s.writeSidecar(entry.sidecar()); err != nil {
		slog.Warn("scheduler: persist retained destroy retry", "run", entry.runID, "error", err)
	}
}

// recoverRuns reconciles the store's runtime-active runs against the
// runtime's actual containers on startup (§6.8): resume supervision where
// the container still runs, otherwise preserve the work and mark the run
// interrupted. Terminal rows never regain runtime supervision; lingering
// sidecars for them are cleaned up separately.
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
	s.cleanupTerminalSidecars(ctx)
	// The sidecars that survived reconciliation are the live references to
	// staged bridge binaries; anything they no longer name is a build no
	// container holds.
	s.collectStagedBridges()
	return nil
}

// cleanupTerminalSidecars reconciles terminal sidecars. An unexpired retained
// TUI sidecar is adopted as dormant supervision; every other sidecar is
// destroyed idempotently. Terminal rows never become active work during boot.
func (s *Scheduler) cleanupTerminalSidecars(ctx context.Context) {
	entries, err := os.ReadDir(s.cfg.StateDir)
	if err != nil {
		slog.Warn("scheduler: list sidecars during recovery", "error", err)
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		run := domain.RunID(strings.TrimSuffix(entry.Name(), ".json"))
		r, err := s.cfg.Store.GetRun(ctx, run)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				sc, serr := s.readSidecar(run)
				if serr == nil {
					if derr := s.cleanupLeftoverContainer(ctx, runtime.ID(sc.ContainerID), run); derr != nil {
						slog.Warn("scheduler: retain orphan sidecar after destroy failure", "run", run, "error", derr)
					}
				}
				continue
			}
			slog.Warn("scheduler: load sidecar run during recovery", "run", run, "error", err)
			continue
		}
		if !r.Status.Terminal() {
			continue
		}
		sc, err := s.readSidecar(run)
		if err != nil {
			slog.Warn("scheduler: read terminal sidecar during recovery", "run", run, "error", err)
			continue
		}
		// The sidecar and the terminal row can change while the directory
		// listing is in flight (notably when Relaunch clears retention).
		// Never destroy a sidecar based on that stale terminal snapshot.
		fresh, ferr := s.cfg.Store.GetRun(ctx, run)
		if ferr != nil {
			slog.Warn("scheduler: reload terminal sidecar run", "run", run, "error", ferr)
			continue
		}
		if !fresh.Status.Terminal() {
			continue
		}
		r = fresh
		mode := sc.Mode
		if mode == "" {
			mode = r.Mode
		}
		retained := sc.Retained || sc.RetainedUntil != nil
		if mode != domain.LaunchTUI || !retained || r.Reason != retainedCloseReason {
			if derr := s.cleanupLeftoverContainer(ctx, runtime.ID(sc.ContainerID), run); derr != nil {
				slog.Warn("scheduler: retain invalid terminal sidecar", "run", run, "error", derr)
			}
			continue
		}
		sc.Mode = mode
		sc.Retained = true
		expireRetained := func(reason string) {
			if sc.ContainerID == "" {
				s.removeSidecar(run)
				s.markRetainedReason(ctx, r, retainedExpiredReason)
				return
			}
			owner := s.installRetainedDestroyOwner(ctx, r, sc)
			if owner == nil {
				return
			}
			if derr := s.expireRetained(ctx, owner); derr != nil {
				slog.Warn("scheduler: "+reason, "run", run, "error", derr)
			}
		}
		if sc.RetainedUntil == nil {
			expireRetained("retain undated sidecar after destroy failure")
			continue
		}
		if s.cfg.RunContainerTTL < 0 {
			expireRetained("retain negative-TTL sidecar after destroy failure")
			continue
		}
		if !time.Now().UTC().Before(*sc.RetainedUntil) {
			expireRetained("retain expired sidecar after destroy failure")
			continue
		}
		probeCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
		_, waitErr := s.cfg.Runtime.Wait(probeCtx, runtime.ID(sc.ContainerID))
		cancel()
		switch {
		case waitErr == nil, errors.Is(waitErr, runtime.ErrNotFound):
			if derr := s.cleanupLeftoverContainer(ctx, runtime.ID(sc.ContainerID), run); derr != nil {
				slog.Warn("scheduler: retain unavailable sidecar after destroy failure", "run", run, "error", derr)
				continue
			}
			s.markRetainedReason(ctx, r, retainedUnavailableReason)
		case errors.Is(waitErr, context.DeadlineExceeded):
			// The probe only establishes that the container is still alive.
			// Reconcile the durable row and in-memory owner under one lock
			// before installing a recovered Wait owner. A concurrent
			// Relaunch may already own this run; never replace it.
			s.mu.Lock()
			if s.runs[run] != nil {
				s.mu.Unlock()
				continue
			}
			fresh, ferr := s.cfg.Store.GetRun(ctx, run)
			if ferr != nil {
				s.mu.Unlock()
				slog.Warn("scheduler: reload retained run after probe", "run", run, "error", ferr)
				continue
			}
			if fresh.Mode != domain.LaunchTUI ||
				!fresh.Status.Terminal() || fresh.Reason != retainedCloseReason {
				s.mu.Unlock()
				continue
			}
			entry := s.entryFromSidecar(fresh, sc)
			entry.retained = true
			s.runs[run] = entry
			s.mu.Unlock()
			s.startSupervision(entry)
		default:
			slog.Warn("scheduler: retained sidecar probe failed", "run", run, "error", waitErr)
		}
	}
}
func (s *Scheduler) markRetainedReason(ctx context.Context, r *domain.Run, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.transitionLocked(ctx, r.ID, r.WorkspaceID, r.Status, r.Status, reason, ""); err != nil {
		slog.Warn("scheduler: update retained close reason", "run", r.ID, "error", err)
	}
}

// recoverUnstarted handles queued/provisioning rows whose launch died with
// the server. Any container created for the run must be destroyed before
// interrupting, or a still-running agent would keep mutating the checkout
// forever. The sidecar (written right after Runtime.Start) names the
// container for the wide crash window; a crash in the narrow window between
// Runtime.Create and the sidecar write is reconciled through the container's
// creation key (the run ID, persisted by the runtime at Create).
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
		if derr := s.cfg.Runtime.Destroy(ctx, cid); derr != nil && !errors.Is(derr, runtime.ErrNotFound) {
			slog.Warn("scheduler: destroy orphaned container during recovery", "run", r.ID, "error", derr)
			return
		}
	}
	if r.Worktree != "" {
		if _, cerr := s.commitAll(ctx, r.ID, "wip: "+taskLine(r.Task)); cerr != nil {
			slog.Warn("scheduler: wip commit during recovery", "run", r.ID, "error", cerr)
		}
		if _, perr := s.cfg.Git.PublishRunBranch(ctx, r.ID); perr != nil {
			slog.Warn("scheduler: publish branch during recovery", "run", r.ID, "error", perr)
		}
	}
	s.interrupt(ctx, r)
}

// interrupt marks a run interrupted ("server restarted"), preserving its
// checkout for inspection and pull. The status is re-read under s.mu so a
// steering call that raced recovery (e.g. a kill) is never overwritten.
func (s *Scheduler) interrupt(ctx context.Context, r *domain.Run) {
	s.mu.Lock()
	fresh, err := s.cfg.Store.GetRun(ctx, r.ID)
	if err == nil {
		if fresh.Status.Terminal() || s.runs[r.ID] != nil {
			s.mu.Unlock()
			return
		}
		err = s.transitionLocked(ctx, r.ID, r.WorkspaceID, fresh.Status, domain.RunInterrupted, "server restarted", "")
	}
	s.mu.Unlock()
	if err != nil {
		slog.Warn("scheduler: mark run interrupted", "run", r.ID, "error", err)
		return
	}
	s.removeSidecar(r.ID)
}
func (s *Scheduler) recoverSupervised(ctx context.Context, r *domain.Run) {
	if r.Status.Terminal() {
		if sc, err := s.readSidecar(r.ID); err == nil {
			if cleanupErr := s.cleanupLeftoverContainer(ctx, runtime.ID(sc.ContainerID), r.ID); cleanupErr != nil {
				slog.Warn("scheduler: cleanup terminal run container during recovery", "run", r.ID, "error", cleanupErr)
			}
		}
		return
	}
	sc, err := s.readSidecar(r.ID)
	if err != nil {
		s.interrupt(ctx, r)
		return
	}
	// A successful relaunch persists RunRunning before clearing its retained
	// marker. On reboot, the active durable row wins: stale terminal-retention
	// metadata must not make the sweep destroy this live container.
	if sc.Retained || sc.RetainedUntil != nil {
		sc.Retained = false
		sc.RetainedUntil = nil
		if werr := s.writeSidecar(sc); werr != nil {
			slog.Warn("scheduler: clear stale retained sidecar", "run", r.ID, "error", werr)
		}
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

func (s *Scheduler) finalizeObservedExit(ctx context.Context, r *domain.Run, sc sidecar) {
	entry := s.entryFromSidecar(r, sc)
	// Match the lifecycle lock order used by steering and supervision:
	// lifecycleMu precedes Scheduler.mu. Claim finalizing before releasing
	// either lock, then run the potentially blocking finalization work with
	// both locks released.
	entry.lifecycleMu.Lock()
	s.mu.Lock()
	if s.runs[r.ID] != nil {
		s.mu.Unlock()
		entry.lifecycleMu.Unlock()
		return
	}
	// Kill and Close both use s.mu to serialize their status write. Read
	// the durable status while holding that same lock, then register the
	// recovered entry before releasing it so a concurrent Kill cannot win
	// unsupervised and be overwritten by the observed clean exit.
	fresh, err := s.cfg.Store.GetRun(ctx, r.ID)
	if err != nil {
		s.mu.Unlock()
		entry.lifecycleMu.Unlock()
		slog.Warn("scheduler: reload observed-exit run", "run", r.ID, "error", err)
		return
	}
	if fresh.Status.Terminal() {
		s.mu.Unlock()
		entry.lifecycleMu.Unlock()
		if derr := s.cleanupLeftoverContainer(context.Background(), entry.containerID, r.ID); derr != nil {
			slog.Warn("scheduler: retain observed-exit sidecar after destroy failure", "run", r.ID, "error", derr)
		}
		return
	}
	entry.status = fresh.Status
	s.runs[r.ID] = entry
	if s.runs[r.ID] != entry || entry.retained || entry.finalizing {
		s.mu.Unlock()
		entry.lifecycleMu.Unlock()
		return
	}
	entry.finalizing = true
	s.mu.Unlock()
	entry.lifecycleMu.Unlock()
	s.finalize(entry, sc.ExitCode)
}

func (s *Scheduler) cleanupLeftoverContainer(ctx context.Context, cid runtime.ID, run domain.RunID) error {
	if cid != "" {
		if derr := s.cfg.Runtime.Destroy(ctx, cid); derr != nil && !errors.Is(derr, runtime.ErrNotFound) {
			return fmt.Errorf("scheduler: destroy leftover container during recovery: %w", derr)
		}
	}
	s.removeSidecar(run)
	return nil
}

func (s *Scheduler) didNotSurvive(ctx context.Context, r *domain.Run, cid runtime.ID) {
	if r.Worktree != "" {
		if _, cerr := s.commitAll(ctx, r.ID, "wip: "+taskLine(r.Task)); cerr != nil {
			slog.Warn("scheduler: wip commit during recovery", "run", r.ID, "error", cerr)
		}
		if _, perr := s.cfg.Git.PublishRunBranch(ctx, r.ID); perr != nil {
			slog.Warn("scheduler: publish branch during recovery", "run", r.ID, "error", perr)
		}
	}
	if cid != "" {
		if derr := s.cfg.Runtime.Destroy(ctx, cid); derr != nil && !errors.Is(derr, runtime.ErrNotFound) {
			slog.Warn("scheduler: destroy stale container during recovery", "run", r.ID, "error", derr)
			return
		}
	}
	s.interrupt(ctx, r)
}

func (s *Scheduler) attachAndSupervise(ctx context.Context, r *domain.Run, sc sidecar, cid runtime.ID) {
	// Sidecars written before the captured HOME field was introduced need a
	// live inspection to recover the container's actual HOME. A failed
	// inspection is inconclusive metadata, not evidence that the container
	// exited: keep the survivor supervised and leave the real error in the
	// log.
	homeRecovered := false
	if sc.Home == "" {
		info, inspectErr := s.cfg.Runtime.Inspect(ctx, cid)
		if inspectErr != nil {
			slog.Warn("scheduler: inspect container HOME during recovery", "run", r.ID, "container", cid, "error", inspectErr)
		} else {
			sc.Home = containerHome(info.Env)
			if sc.Home == "" {
				slog.Warn("scheduler: recovered container has no absolute HOME", "run", r.ID, "container", cid)
			} else {
				homeRecovered = true
			}
		}
	}
	att, err := s.cfg.Runtime.Attach(ctx, cid)
	if err == nil {
		if werr := s.cfg.Git.StartDiffWatch(ctx, r.WorkspaceID, r.ID); werr != nil {
			slog.Warn("scheduler: restart diff watch", "run", r.ID, "error", werr)
		}
		if homeRecovered {
			if werr := s.writeSidecar(sc); werr != nil {
				slog.Warn("scheduler: persist recovered container HOME", "run", r.ID, "error", werr)
			}
		}
		entry := s.entryFromSidecar(r, sc)
		entry.containerID = cid
		s.mu.Lock()
		s.runs[r.ID] = entry
		s.mu.Unlock()
		if serr := s.cfg.PTY.StartSession(ctx, ptyhost.RunSession(r.ID), att); serr != nil {
			_ = att.Close()
			s.cfg.Git.StopDiffWatch(r.ID)
			s.mu.Lock()
			if s.runs[r.ID] == entry {
				delete(s.runs, r.ID)
			}
			s.mu.Unlock()
			s.didNotSurvive(ctx, r, cid)
			return
		}
		s.startSupervision(entry)
		if sc.KillRequested {
			if serr := s.cfg.Runtime.Stop(ctx, cid, s.cfg.StopGrace); serr != nil {
				slog.Warn("scheduler: re-issue persisted kill during recovery", "run", r.ID, "error", serr)
			}
		}
		return
	}
	s.didNotSurvive(ctx, r, cid)
}

func containerHome(env []string) string {
	for _, value := range env {
		name, candidate, ok := strings.Cut(value, "=")
		if ok && name == "HOME" && strings.HasPrefix(candidate, "/") {
			return candidate
		}
	}
	return ""
}

func (s *Scheduler) entryFromSidecar(r *domain.Run, sc sidecar) *supervised {
	started := time.Now().UTC()
	if r.StartedAt != nil {
		started = *r.StartedAt
	}
	// The sidecar does not carry when the report parked the run, and it
	// does not need to: nothing has been observed on the terminal since the
	// restart, so the park effectively begins again here.
	var parked time.Time
	if sc.agentReport().State == agentstatus.Waiting {
		parked = time.Now().UTC()
	}
	mode := sc.Mode
	if mode == "" {
		mode = r.Mode
	}
	return &supervised{
		runID: r.ID,
		// The workspace comes off the run row, not the sidecar: a sidecar
		// written by an older build has no workspace scope at all, and the
		// row is the source of truth either way.
		workspaceID: r.WorkspaceID,
		containerID: runtime.ID(sc.ContainerID),
		task:        r.Task,
		memberID:    r.AccountMember(),
		// The reporter and the last report both come off the sidecar,
		// because only the live server saw either: which reporter the
		// container was actually given, and whether the agent parked this
		// run itself. Reattaching resizes the terminal, so a full-screen
		// agent repaints at once - and without the report that repaint
		// would read as work resuming and hand the run back to an agent
		// that is still waiting for their member.
		reporter:       sc.Reporter,
		agentReport:    sc.agentReport(),
		parkedAt:       parked,
		launchMode:     mode,
		status:         r.Status,
		startedAt:      started,
		paused:         sc.Paused,
		killRequested:  sc.KillRequested,
		retained:       sc.Retained,
		retainedUntil:  sc.RetainedUntil,
		runUser:        sc.RunUser,
		home:           sc.Home,
		exitObserved:   sc.ExitObserved,
		exitCode:       sc.ExitCode,
		bridgeDigest:   sc.BridgeDigest,
		bridgePath:     sc.BridgePath,
		coordDir:       sc.CoordDir,
		gitAuthorEmail: sc.GitAuthorEmail,
		done:           make(chan struct{}),
	}
}
