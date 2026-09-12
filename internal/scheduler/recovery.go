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
	valid := s.runs[run] == entry && entry.retained && !entry.destroyPending &&
		fresh.Mode == domain.LaunchTUI &&
		(fresh.Status == domain.RunMerged || fresh.Status == domain.RunAbandoned) &&
		fresh.Reason == retainedCloseReason && deadline != nil && time.Now().UTC().Before(*deadline)
	paused, cid := entry.paused, entry.containerID
	s.mu.Unlock()
	if !valid {
		if entry.destroyPending || deadline == nil || !time.Now().UTC().Before(*deadline) {
			if deadline == nil || entry.destroyPending {
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

	// Re-read under the lifecycle admission before promoting so title or
	// other metadata updates are not clobbered by a stale terminal snapshot.
	s.mu.Lock()
	sameEntry := s.runs[run] == entry
	s.mu.Unlock()
	if !sameEntry {
		return nil, retainedTransitionError()
	}
	latest, err := s.cfg.Store.GetRun(ctx, run)
	if err != nil {
		return nil, err
	}
	if latest.Mode != domain.LaunchTUI ||
		(latest.Status != domain.RunMerged && latest.Status != domain.RunAbandoned) ||
		latest.Reason != retainedCloseReason {
		return nil, retainedTransitionError()
	}
	fresh = latest

	// Promote the durable row before thawing the container. A crash after
	// Resume must never leave a terminal row paired with a live agent.
	now := time.Now().UTC()
	terminalRow := *fresh
	runningRow := *fresh
	runningRow.Status = domain.RunRunning
	runningRow.Reason = ""
	runningRow.StartedAt = &now
	runningRow.FinishedAt = nil
	if updateErr := s.cfg.Store.UpdateRun(ctx, &runningRow); updateErr != nil {
		return nil, updateErr
	}
	resumed := false
	rollback := func(cause error) error {
		s.cfg.Git.StopDiffWatch(run)
		_ = s.cfg.PTY.StopSession(context.WithoutCancel(ctx), ptyhost.RunSession(run))
		mustPause := resumed || !paused
		var pauseErr error
		if mustPause {
			if perr := s.cfg.Runtime.Pause(context.WithoutCancel(ctx), cid); perr != nil &&
				!errors.Is(perr, runtime.ErrNotFound) {
				pauseErr = fmt.Errorf("scheduler: relaunch rollback pause: %w", perr)
			}
		}

		// The promoted row is the source of truth until its terminal
		// replacement is durably restored. Keep the owner and sidecar
		// aligned with that row while the replacement is attempted.
		var activeSidecar sidecar
		active := false
		s.mu.Lock()
		if s.runs[run] == entry {
			entry.status = domain.RunRunning
			entry.startedAt = now
			entry.paused = pauseErr == nil
			entry.retained = false
			entry.retainedUntil = nil
			entry.destroyPending = false
			activeSidecar = entry.sidecar()
			active = true
		}
		s.mu.Unlock()
		if !active {
			return errors.Join(cause, errors.New("scheduler: relaunch rollback owner was replaced"))
		}

		activeSidecarErr := s.writeSidecar(activeSidecar)
		joinRollback := func(errs ...error) error {
			all := []error{cause}
			if activeSidecarErr != nil {
				all = append(all, fmt.Errorf("scheduler: relaunch rollback sidecar: %w", activeSidecarErr))
			}
			all = append(all, errs...)
			return errors.Join(all...)
		}
		if pauseErr != nil {
			return joinRollback(pauseErr)
		}

		// Install the retained marker and deadline before restoring the
		// terminal row. A reboot between these writes then sees either an
		// active row (which wins over stale retention) or a terminal row
		// protected by a durable retained ownership promise.
		retainedSidecar := activeSidecar
		retainedSidecar.Paused = true
		retainedSidecar.Retained = true
		retainedSidecar.RetainedUntil = deadline
		if markerErr := s.persistRetainedSidecar(retainedSidecar); markerErr != nil {
			// The marker write may have renamed its file before directory
			// fsync failed. Reassert the active sidecar so the promoted
			// running row remains the authoritative reboot state.
			restoreErr := s.writeSidecar(activeSidecar)
			if restoreErr != nil {
				restoreErr = fmt.Errorf("scheduler: relaunch rollback sidecar restore: %w", restoreErr)
			}
			return joinRollback(markerErr, restoreErr)
		}

		// Only after the retained marker/deadline is durable may this owner
		// become a retained terminal owner. A failed row write leaves the
		// promoted running row and its paused owner in charge.
		if rowErr := s.cfg.Store.UpdateRun(ctx, &terminalRow); rowErr != nil {
			restoreErr := s.writeSidecar(activeSidecar)
			if restoreErr != nil {
				restoreErr = fmt.Errorf("scheduler: relaunch rollback sidecar restore: %w", restoreErr)
			}
			return joinRollback(fmt.Errorf("scheduler: relaunch rollback row: %w", rowErr), restoreErr)
		}

		s.mu.Lock()
		if s.runs[run] == entry {
			entry.status = terminalRow.Status
			entry.paused = true
			entry.retained = true
			entry.retainedUntil = deadline
			entry.destroyPending = false
		}
		s.mu.Unlock()
		return joinRollback()
	}

	if paused {
		if resumeErr := s.cfg.Runtime.Resume(ctx, cid); resumeErr != nil {
			return nil, rollback(errors.Join(retainedTransitionError(), resumeErr))
		}
		resumed = true
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

	s.mu.Lock()
	if s.runs[run] != entry {
		s.mu.Unlock()
		return nil, rollback(retainedTransitionError())
	}
	entry.status = domain.RunRunning
	entry.startedAt = now
	entry.retained = false
	entry.retainedUntil = nil
	entry.paused = false
	entry.destroyPending = false
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
	return &runningRow, nil

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
	if current.ContainerID == "" ||
		(scanned.ContainerID != "" && current.ContainerID != scanned.ContainerID) ||
		(!current.Retained && current.RetainedUntil == nil) {
		return nil
	}
	return s.adoptDestroyOwnerLocked(fresh, current)
}

// adoptRetainedOwnerLocked adopts a durable terminal retained sidecar while
// preserving its deadline. The caller must hold s.mu.
func (s *Scheduler) adoptRetainedOwnerLocked(r *domain.Run, sc sidecar) *supervised {
	if r == nil || !r.Status.Terminal() || sc.ContainerID == "" ||
		(!sc.Retained && sc.RetainedUntil == nil) {
		return nil
	}
	return s.adoptTerminalOwnerLocked(r, sc)
}

// adoptDestroyOwnerLocked adopts any durable terminal sidecar that still
// names a container and makes it due immediately. It is used for terminal
// sidecars whose first cleanup Destroy failed; the bounded sweep then retries
// without releasing credential or coordination ownership.
func (s *Scheduler) adoptDestroyOwnerLocked(r *domain.Run, sc sidecar) *supervised {
	sc.DestroyPending = true
	entry := s.adoptTerminalOwnerLocked(r, sc)
	if entry != nil {
		entry.destroyPending = true
		s.markRetainedDestroyDueLocked(entry)
	}
	return entry
}

// adoptTerminalOwnerLocked installs one terminal sidecar owner. The caller
// must hold s.mu; validation of whether the sidecar is a retained close or an
func (s *Scheduler) adoptTerminalOwnerLocked(r *domain.Run, sc sidecar) *supervised {
	if r == nil || (!sc.DestroyPending && sc.ContainerID == "") {
		return nil
	}
	if entry := s.runs[r.ID]; entry != nil {
		if entry.containerID == "" || entry.containerID != runtime.ID(sc.ContainerID) {
			return nil
		}
		entry.retained = true
		entry.destroyPending = sc.DestroyPending
		return entry
	}
	mode := sc.Mode
	if mode == "" {
		mode = r.Mode
	}
	sc.Mode = mode
	entry := s.entryFromSidecar(r, sc)
	entry.retained = true
	entry.destroyPending = sc.DestroyPending
	s.runs[r.ID] = entry
	s.syncRunUserReservationsLocked()
	return entry
}

// adoptLiveSidecar installs an active run's durable container owner after a
// recovery probe was inconclusive. A stale retained marker can only be from a
// relaunch whose row update already won; active rows must not be treated as
// terminal retention when a later close reconciles them.
func (s *Scheduler) adoptLiveSidecar(r *domain.Run, sc sidecar) *supervised {
	if r == nil || r.Status.Terminal() || sc.ContainerID == "" {
		return nil
	}
	if sc.RunID != "" && sc.RunID != string(r.ID) {
		return nil
	}
	sc.Retained = false
	sc.RetainedUntil = nil
	sc.DestroyPending = false
	mode := sc.Mode
	if mode == "" {
		mode = r.Mode
	}
	sc.Mode = mode
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry := s.runs[r.ID]; entry != nil {
		return entry
	}
	if s.pending[r.ID] != nil {
		return nil
	}
	entry := s.entryFromSidecar(r, sc)
	entry.retained = false
	entry.retainedUntil = nil
	entry.destroyPending = false
	s.runs[r.ID] = entry
	s.syncRunUserReservationsLocked()
	if err := s.writeSidecar(sc); err != nil {
		slog.Warn("scheduler: clear stale retained sidecar during close recovery", "run", r.ID, "error", err)
	}
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
	entry.destroyPending = true
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
	s.cleanupTerminalCreationKeyContainers(ctx)
	// The sidecars that survived reconciliation are the live references to
	// staged bridge binaries; anything they no longer name is a build no
	// container holds.
	s.collectStagedBridges()
	return nil
}

// cleanupTerminalSidecars reconciles terminal sidecars. An unexpired retained
// TUI sidecar is adopted as dormant supervision; every other sidecar is
// destroyed idempotently. A failed destroy is itself adopted so terminal rows
// never lose runtime, credential, or coordination ownership during recovery.
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
		if sc.DestroyPending {
			// A pending marker, including one with an empty container ID,
			// is an ownership record in its own right. Resolve its
			// creation key before any row or checkout cleanup.
			s.recoverDestroyMetadata(ctx, runtime.ID(sc.ContainerID), &sc)
			owner, admitted := s.admitDestroyPendingOwner(ctx, r, sc, runtime.ID(sc.ContainerID))
			if admitted {
				if derr := s.retryDestroyPendingLocked(ctx, owner); derr != nil {
					slog.Warn("scheduler: retry pending terminal destroy", "run", run, "error", derr)
				}
				owner.lifecycleMu.Unlock()
			}
			continue
		}
		mode := sc.Mode
		if mode == "" {
			mode = r.Mode
		}
		retained := sc.Retained || sc.RetainedUntil != nil
		if mode != domain.LaunchTUI || !retained || r.Reason != retainedCloseReason {
			// Invalid terminal markers are not relaunchable, but their
			// container may still be live. Keep durable ownership while
			// the first cleanup Destroy is uncertain, then let the normal
			// due-owner sweep retry it.
			if derr := s.cleanupLeftoverContainer(ctx, runtime.ID(sc.ContainerID), run); derr != nil {
				s.mu.Lock()
				owner := s.adoptDestroyOwnerLocked(r, sc)
				s.mu.Unlock()
				if owner != nil {
					s.startRetainedWaitAfterDestroyFailure(ctx, owner)
				}
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
				owner := s.installRetainedDestroyOwner(ctx, r, sc)
				if owner != nil {
					slog.Warn("scheduler: retain unavailable sidecar after destroy failure", "run", run, "error", derr)
				} else {
					slog.Warn("scheduler: discard unavailable sidecar after destroy failure", "run", run, "error", derr)
				}
				continue
			}
			s.markRetainedReason(ctx, r, retainedUnavailableReason)
		case ctx.Err() != nil:
			return
		default:
			// Any non-nil result other than NotFound is inconclusive:
			// a transport/daemon error cannot prove the retained
			// container exited. Reconcile the durable row and install
			// its owner before supervision or the next TTL sweep.
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
			entry := s.adoptRetainedOwnerLocked(fresh, sc)

			s.mu.Unlock()
			if entry != nil {
				s.startSupervision(entry)
			}
		}
	}
}

// cleanupTerminalCreationKeyContainers closes the crash window where a
// terminal row survived but its sidecar write did not. The runtime creation
// key is the durable fallback that lets boot recover and retain ownership.
func (s *Scheduler) cleanupTerminalCreationKeyContainers(ctx context.Context) {
	workspaces, err := s.cfg.Store.ListWorkspaces(ctx)
	if err != nil {
		slog.Warn("scheduler: list workspaces for terminal cleanup", "error", err)
		return
	}
	for _, workspace := range workspaces {
		runs, err := s.cfg.Store.ListRunsByWorkspace(ctx, workspace.ID)
		if err != nil {
			slog.Warn("scheduler: list runs for terminal cleanup", "workspace", workspace.ID, "error", err)
			continue
		}
		for _, r := range runs {
			if !r.Status.Terminal() {
				continue
			}
			if _, err := os.Stat(s.sidecarPath(r.ID)); err == nil || !os.IsNotExist(err) {
				continue
			}
			cid, ferr := s.cfg.Runtime.FindByCreationKey(ctx, string(r.ID))
			if ferr != nil && !errors.Is(ferr, runtime.ErrNotFound) {
				sc := sidecar{
					RunID: string(r.ID), WorkspaceID: string(r.WorkspaceID),
					DestroyPending: true, RunUser: unknownRecoveryRunUser,
				}
				owner, admitted := s.admitDestroyPendingOwner(ctx, r, sc, "")
				if admitted {
					slog.Warn("scheduler: defer terminal creation-key cleanup", "run", r.ID, "error", ferr)
					owner.lifecycleMu.Unlock()
				}
				continue
			}
			if ferr != nil {
				continue
			}
			sc := sidecar{RunID: string(r.ID), ContainerID: string(cid), WorkspaceID: string(r.WorkspaceID)}
			s.recoverDestroyMetadata(ctx, cid, &sc)
			owner, admitted := s.admitDestroyPendingOwner(ctx, r, sc, cid)
			if !admitted {
				continue
			}
			if err := s.cfg.Runtime.Destroy(ctx, cid); err != nil && !errors.Is(err, runtime.ErrNotFound) {
				slog.Warn("scheduler: destroy terminal creation-key container", "run", r.ID, "error", err)
				owner.lifecycleMu.Unlock()
				continue
			}
			if err := s.finishDestroyPending(ctx, owner); err != nil {
				slog.Warn("scheduler: finish terminal creation-key cleanup", "run", r.ID, "error", err)
			}
			owner.lifecycleMu.Unlock()
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

const unknownRecoveryRunUser = "<unknown>"

func normalizeRecoveryRunUser(user string) string {
	if user == "" || user == "0" || user == "0:0" {
		return ""
	}
	return user
}

func (s *Scheduler) recoverDestroyMetadata(ctx context.Context, cid runtime.ID, sc *sidecar) {
	if sc == nil || sc.RunUser != "" {
		return
	}
	if cid == "" {
		sc.RunUser = unknownRecoveryRunUser
		return
	}
	info, err := s.cfg.Runtime.Inspect(ctx, cid)
	if err != nil {
		// A container with unknown identity may still mount a member home.
		// Reserve a conservative sentinel until destruction is confirmed.
		sc.RunUser = unknownRecoveryRunUser
		return
	}
	sc.RunUser = normalizeRecoveryRunUser(info.User)
	if sc.Home == "" {
		sc.Home = containerHome(info.Env)
	}
}

// path won admission; an existing owner or a row that changed concurrently
// means the caller must leave cleanup to that owner.
func (s *Scheduler) admitDestroyPendingOwner(ctx context.Context, r *domain.Run, sc sidecar, cid runtime.ID) (*supervised, bool) {
	if r == nil {
		return nil, false
	}

	s.mu.Lock()
	if existing := s.runs[r.ID]; existing != nil || s.pending[r.ID] != nil {
		s.mu.Unlock()
		return nil, false
	}
	fresh, err := s.cfg.Store.GetRun(ctx, r.ID)
	if err != nil {
		s.mu.Unlock()
		slog.Warn("scheduler: reload run before destroy retry", "run", r.ID, "error", err)
		return nil, false
	}
	current, err := s.readSidecar(r.ID)
	if err != nil {
		if !os.IsNotExist(err) {
			s.mu.Unlock()
			slog.Warn("scheduler: reload sidecar before destroy retry", "run", r.ID, "error", err)
			return nil, false
		}
		current = sc
	}
	if current.RunUser == "" {
		current.RunUser = sc.RunUser
	}
	if current.Home == "" {
		current.Home = sc.Home
	}
	if sc.DestroyPending {
		current.DestroyPending = true
	}
	if current.ContainerID != "" && runtime.ID(current.ContainerID) != cid {
		s.mu.Unlock()
		return nil, false
	}
	current.RunID = string(r.ID)
	current.ContainerID = string(cid)
	if current.WorkspaceID == "" {
		current.WorkspaceID = string(fresh.WorkspaceID)
	}
	if fresh.Status.Terminal() {
		owner := s.adoptDestroyOwnerLocked(fresh, current)
		if owner == nil {
			s.mu.Unlock()
			return nil, false
		}
		owner.lifecycleMu.Lock()
		s.mu.Unlock()
		return owner, true
	}
	current.Retained = false
	current.RetainedUntil = nil
	current.DestroyPending = true
	entry := s.entryFromSidecar(fresh, current)
	entry.containerID = cid
	entry.retained = false
	entry.retainedUntil = nil
	entry.destroyPending = true
	entry.lifecycleMu.Lock()
	s.runs[r.ID] = entry
	s.syncRunUserReservationsLocked()
	if werr := s.writeSidecar(entry.sidecar()); werr != nil {
		slog.Warn("scheduler: persist destroy-pending owner", "run", r.ID, "error", werr)
	}
	s.mu.Unlock()
	return entry, true
}

// finishDestroyPending terminalizes an active recovery row only after the
// runtime confirms destruction, then drops every durable and in-memory owner.
// The caller must hold entry.lifecycleMu.
func (s *Scheduler) finishDestroyPending(ctx context.Context, entry *supervised) error {
	if entry == nil {
		return nil
	}
	s.mu.Lock()
	if s.runs[entry.runID] != entry || !entry.destroyPending {
		s.mu.Unlock()
		return nil
	}
	fresh, err := s.cfg.Store.GetRun(ctx, entry.runID)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	if fresh.Status.Terminal() {
		if entry.retained && fresh.Reason == retainedCloseReason {
			if err := s.transitionLocked(ctx, entry.runID, fresh.WorkspaceID, fresh.Status, fresh.Status,
				retainedExpiredReason, ""); err != nil {
				s.mu.Unlock()
				return err
			}
		}
	} else {
		if err := s.transitionLocked(ctx, entry.runID, fresh.WorkspaceID, fresh.Status,
			domain.RunInterrupted, "server restarted", ""); err != nil {
			s.mu.Unlock()
			return err
		}
	}
	entry.destroyPending = false
	entry.retained = false
	entry.retainedUntil = nil
	if entry.userReservation != nil {
		delete(s.credentialUsers, entry.userReservation)
		entry.userReservation = nil
	}
	s.closeDone(entry)
	delete(s.runs, entry.runID)
	s.syncRunUserReservationsLocked()
	s.mu.Unlock()
	s.removeSidecar(entry.runID)
	return nil
}

func (s *Scheduler) preserveRecoveryWork(ctx context.Context, run domain.RunID, task string) {
	r, err := s.cfg.Store.GetRun(ctx, run)
	if err != nil {
		slog.Warn("scheduler: load run before recovery snapshot", "run", run, "error", err)
		return
	}
	if r.Status.Terminal() || r.Worktree == "" {
		return
	}
	if _, err := s.commitAll(ctx, run, "wip: "+taskLine(task)); err != nil {
		slog.Warn("scheduler: wip commit during recovery", "run", run, "error", err)
	}
	if _, err := s.cfg.Git.PublishRunBranch(ctx, run); err != nil {
		slog.Warn("scheduler: publish branch during recovery", "run", run, "error", err)
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
	var (
		cid       runtime.ID
		sc        sidecar
		lookupErr error
	)
	if loaded, err := s.readSidecar(r.ID); err == nil && loaded.ContainerID != "" {
		sc = loaded
		cid = runtime.ID(loaded.ContainerID)
	} else {
		var found runtime.ID
		found, lookupErr = s.cfg.Runtime.FindByCreationKey(ctx, string(r.ID))
		if lookupErr == nil {
			cid = found
			sc = sidecar{RunID: string(r.ID), ContainerID: string(cid), WorkspaceID: string(r.WorkspaceID)}
		} else if !errors.Is(lookupErr, runtime.ErrNotFound) {
			slog.Warn("scheduler: creation-key lookup during recovery", "run", r.ID, "error", lookupErr)
		}
	}
	var owner *supervised
	if cid != "" {
		s.recoverDestroyMetadata(ctx, cid, &sc)
		var admitted bool
		owner, admitted = s.admitDestroyPendingOwner(ctx, r, sc, cid)
		if !admitted {
			return
		}
		defer owner.lifecycleMu.Unlock()
	} else if lookupErr != nil && !errors.Is(lookupErr, runtime.ErrNotFound) {
		sc = sidecar{
			RunID: string(r.ID), WorkspaceID: string(r.WorkspaceID),
			DestroyPending: true, RunUser: unknownRecoveryRunUser,
		}
		var admitted bool
		owner, admitted = s.admitDestroyPendingOwner(ctx, r, sc, "")
		if !admitted {
			return
		}
		// Keep the synthetic owner until the bounded sweep can retry the
		// creation-key lookup. No runtime destruction is confirmed yet.
		defer owner.lifecycleMu.Unlock()
		return
	}
	if owner != nil {
		if derr := s.cfg.Runtime.Destroy(ctx, cid); derr != nil && !errors.Is(derr, runtime.ErrNotFound) {
			slog.Warn("scheduler: destroy orphaned container during recovery", "run", r.ID, "error", derr)
			return
		}
	}
	s.preserveRecoveryWork(ctx, r.ID, r.Task)
	if owner != nil {
		if err := s.finishDestroyPending(ctx, owner); err != nil {
			slog.Warn("scheduler: finish orphaned container cleanup", "run", r.ID, "error", err)
		}
		return
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
	// A destroy-pending owner is already on the cleanup path. It must not
	// be probed or attached as though the active container were resumable.
	if sc.DestroyPending {
		if entry, admitted := s.admitDestroyPendingOwner(ctx, r, sc, runtime.ID(sc.ContainerID)); admitted {
			entry.lifecycleMu.Unlock()
			s.retryDestroyPending(ctx, entry)
		}
		return
	}
	// A successful relaunch persists RunRunning before clearing its retained
	// marker. On reboot, the active durable row wins: stale terminal-retention
	// metadata must not make the sweep destroy this live container.
	if sc.Retained || sc.RetainedUntil != nil {
		sc.Retained = false
		sc.RetainedUntil = nil
		sc.DestroyPending = false
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
		// A transport/daemon error is not evidence that the container
		// exited. Install the durable sidecar owner without attaching; the
		// Wait owner retries until the daemon recovers, and therefore lets
		// Kill/Close and eventual exit cleanup destroy it safely.
		entry := s.adoptLiveSidecar(r, sc)
		if entry != nil {
			s.startSupervision(entry)
		}
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
	var owner *supervised
	if cid != "" {
		sc, serr := s.readSidecar(r.ID)
		if serr != nil && !os.IsNotExist(serr) {
			slog.Warn("scheduler: read sidecar before stale cleanup", "run", r.ID, "error", serr)
			return
		}
		if serr != nil {
			sc = sidecar{RunID: string(r.ID), ContainerID: string(cid), WorkspaceID: string(r.WorkspaceID)}
		}
		s.recoverDestroyMetadata(ctx, cid, &sc)
		var admitted bool
		owner, admitted = s.admitDestroyPendingOwner(ctx, r, sc, cid)
		if !admitted {
			return
		}
		defer owner.lifecycleMu.Unlock()
	}
	if owner != nil {
		if derr := s.cfg.Runtime.Destroy(ctx, cid); derr != nil && !errors.Is(derr, runtime.ErrNotFound) {
			slog.Warn("scheduler: destroy stale container during recovery", "run", r.ID, "error", derr)
			return
		}
	}
	s.preserveRecoveryWork(ctx, r.ID, r.Task)
	if owner != nil {
		if err := s.finishDestroyPending(ctx, owner); err != nil {
			slog.Warn("scheduler: finish stale container cleanup", "run", r.ID, "error", err)
		}
		return
	}
	s.interrupt(ctx, r)
}

// admitRecoveryAttachment installs one recovered run owner while holding its
// lifecycle lock. The lock is kept by the caller through Attach and PTY
// setup, so Close/adoption either wins before admission or waits behind this
// exact owner; it can never replace it between the probe and attachment.
func (s *Scheduler) admitRecoveryAttachment(ctx context.Context, r *domain.Run, cid runtime.ID) (*supervised, bool) {
	s.mu.Lock()
	if existing := s.runs[r.ID]; existing != nil || s.pending[r.ID] != nil {
		s.mu.Unlock()
		return nil, false
	}
	fresh, err := s.cfg.Store.GetRun(ctx, r.ID)
	if err != nil {
		s.mu.Unlock()
		slog.Warn("scheduler: reload run before recovery attach", "run", r.ID, "error", err)
		return nil, false
	}
	current, err := s.readSidecar(r.ID)
	if err != nil {
		s.mu.Unlock()
		slog.Warn("scheduler: reload sidecar before recovery attach", "run", r.ID, "error", err)
		return nil, false
	}
	if fresh.Status.Terminal() || current.ContainerID == "" ||
		runtime.ID(current.ContainerID) != cid ||
		(current.RunID != "" && current.RunID != string(r.ID)) ||
		current.DestroyPending {
		s.mu.Unlock()
		return nil, false
	}
	// The active row is authoritative if a relaunch won just before this
	// admission. Do not let a stale retained marker turn this owner into a
	// terminal cleanup candidate.
	current.Retained = false
	current.RetainedUntil = nil
	current.DestroyPending = false
	entry := s.entryFromSidecar(fresh, current)
	entry.containerID = cid
	entry.lifecycleMu.Lock()
	s.runs[r.ID] = entry
	s.syncRunUserReservationsLocked()
	if werr := s.writeSidecar(entry.sidecar()); werr != nil {
		slog.Warn("scheduler: persist recovered run owner", "run", r.ID, "error", werr)
	}
	s.mu.Unlock()
	return entry, true
}

// cleanupFailedRecoveryAttachment reuses the owner admitted before Attach or
// PTY setup. The owner remains in s.runs while destruction and terminalization
// complete, so Delete/ Kill cannot observe a gap and install a second owner.
// The caller must hold entry.lifecycleMu.
func (s *Scheduler) cleanupFailedRecoveryAttachment(ctx context.Context, entry *supervised, cid runtime.ID) {
	if entry == nil {
		return
	}
	var pendingSidecar sidecar
	s.mu.Lock()
	if s.runs[entry.runID] != entry {
		s.mu.Unlock()
		return
	}
	entry.containerID = cid
	entry.retained = false
	entry.retainedUntil = nil
	entry.destroyPending = true
	pendingSidecar = entry.sidecar()
	s.mu.Unlock()
	if err := s.writeSidecar(pendingSidecar); err != nil {
		slog.Warn("scheduler: persist recovery attachment cleanup owner", "run", entry.runID, "error", err)
	}
	if err := s.cfg.Runtime.Destroy(ctx, cid); err != nil && !errors.Is(err, runtime.ErrNotFound) {
		slog.Warn("scheduler: destroy failed recovery attachment", "run", entry.runID, "error", err)
		return
	}
	s.preserveRecoveryWork(ctx, entry.runID, entry.task)
	if err := s.finishDestroyPending(ctx, entry); err != nil {
		slog.Warn("scheduler: finish failed recovery attachment cleanup", "run", entry.runID, "error", err)
	}
}

func (s *Scheduler) attachAndSupervise(ctx context.Context, r *domain.Run, sc sidecar, cid runtime.ID) {
	// Sidecars written before captured HOME and RunUser fields were introduced
	// need a live inspection. A failed inspection is inconclusive metadata,
	// not evidence that the container exited: keep the survivor supervised.
	metadataRecovered := false
	if sc.Home == "" || sc.RunUser == "" {
		info, inspectErr := s.cfg.Runtime.Inspect(ctx, cid)
		if inspectErr != nil {
			slog.Warn("scheduler: inspect container metadata during recovery", "run", r.ID, "container", cid, "error", inspectErr)
			if sc.RunUser == "" {
				sc.RunUser = unknownRecoveryRunUser
				metadataRecovered = true
			}
		} else {
			if sc.Home == "" {
				sc.Home = containerHome(info.Env)
				if sc.Home == "" {
					slog.Warn("scheduler: recovered container has no absolute HOME", "run", r.ID, "container", cid)
				} else {
					metadataRecovered = true
				}
			}
			if sc.RunUser == "" {
				sc.RunUser = normalizeRecoveryRunUser(info.User)
				metadataRecovered = metadataRecovered || sc.RunUser != ""
			}
		}
	}
	entry, admitted := s.admitRecoveryAttachment(ctx, r, cid)
	if !admitted {
		return
	}
	defer entry.lifecycleMu.Unlock()

	if metadataRecovered {
		s.mu.Lock()
		if s.runs[r.ID] == entry {
			entry.home = sc.Home
			entry.runUser = sc.RunUser
			s.syncRunUserReservationsLocked()
			if werr := s.writeSidecar(entry.sidecar()); werr != nil {
				slog.Warn("scheduler: persist recovered container metadata", "run", r.ID, "error", werr)
			}
		}
		s.mu.Unlock()
	}
	att, err := s.cfg.Runtime.Attach(ctx, cid)
	if err == nil {
		if werr := s.cfg.Git.StartDiffWatch(ctx, r.WorkspaceID, r.ID); werr != nil {
			slog.Warn("scheduler: restart diff watch", "run", r.ID, "error", werr)
		}
		if serr := s.cfg.PTY.StartSession(ctx, ptyhost.RunSession(r.ID), att); serr != nil {
			_ = att.Close()
			s.cfg.Git.StopDiffWatch(r.ID)
			s.cleanupFailedRecoveryAttachment(ctx, entry, cid)
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
	s.cleanupFailedRecoveryAttachment(ctx, entry, cid)
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
		destroyPending: sc.DestroyPending,
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
