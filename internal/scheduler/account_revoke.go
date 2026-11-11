package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/3xDevOps/Aether/internal/agentstatus"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/fallback"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/store"
)

// revokedReason starts the reason of a run that stopped because the share
// of the account it ran on was revoked.
const revokedReason = "account share revoked: "

// workspaceRevokedReason starts the reason of a run that stopped because its
// launcher or the member whose home it mounts lost one of its workspaces.
const workspaceRevokedReason = "workspace access revoked: "

// revokedSegment is the reason an account segment records for a move off a
// revoked account.
const revokedSegment = "share revoked"

// accountMove is why a run leaves the account it is on: the account's usage
// ran out, or its owner revoked the share the run uses it by.
type accountMove struct{ revoked bool }

var (
	usageMove  = accountMove{}
	revokeMove = accountMove{revoked: true}
)

func (m accountMove) segment() string {
	if m.revoked {
		return revokedSegment
	}
	return usageLimitSegment
}

// because says why the run left from's account.
func (m accountMove) because(from string) string {
	if m.revoked {
		return from + " revoked access to their account"
	}
	return from + "'s usage ran out"
}

// stopped starts the reason of a run that did not leave from's account, or
// whose move off it failed.
func (m accountMove) stopped(from string) string {
	if m.revoked {
		return revokedReason + m.because(from) + "; "
	}
	return usageLimitReason + from + "'s account; "
}

func (m accountMove) cannotContinue(from, cause string) string {
	return m.stopped(from) + "cannot continue on another account: " + cause
}

func (m accountMove) continuation(from, to string) string {
	return fmt.Sprintf("Your previous turn stopped because %s. The session continues on %s's account. Continue the task where you stopped.", m.because(from), to)
}

// RevokeAccount moves grantee's live runs on owner's account, whose share the
// caller has just removed, to another account they may use, or stops them.
// It returns once the moves have started. The scheduler's sweep repeats the
// check, so a run a restart or another move kept on the account still leaves
// it.
func (s *Scheduler) RevokeAccount(owner, grantee domain.MemberID) {
	s.goMove(func() {
		// As for a usage move: recovery's reopen of a run must not land on
		// the container a move replaced.
		select {
		case <-s.recoveryReady:
		case <-s.superCtx.Done():
			return
		}
		s.leaveRevokedAccounts(s.superCtx, func(run *domain.Run) bool {
			return run.AccountMember() == owner && (run.MemberID == grantee || run.HomeMember() == grantee)
		})
	})
}

// leaveRevokedAccounts starts a move off its account for every live run
// match selects, nil for every one, whose owner or home member may no longer
// use that account.
func (s *Scheduler) leaveRevokedAccounts(ctx context.Context, match func(*domain.Run) bool) {
	runs, err := s.cfg.Store.ListActiveRuns(ctx)
	if err != nil {
		slog.Warn("scheduler: list sessions on revoked accounts", "error", err)
		return
	}
	for _, run := range runs {
		s.mu.Lock()
		entry := s.runs[run.ID]
		s.mu.Unlock()
		if entry == nil || (match != nil && !match(run)) {
			continue
		}
		revoked, err := s.accountRevoked(ctx, run)
		if err != nil {
			slog.Warn("scheduler: check the session's account share", "run", run.ID, "error", err)
			continue
		}
		if revoked {
			s.goMove(func() { s.leaveRevokedAccount(entry) })
		}
	}
}

// accountRevoked reports whether the run's launcher may no longer launch on
// the run's account. A handoff changes the run's owner but keeps the
// launcher's account, so the new owner's shares do not count. A row from
// before shares were narrowed records no launcher; its owner stands in.
func (s *Scheduler) accountRevoked(ctx context.Context, run *domain.Run) (bool, error) {
	err := s.resolveRunAccount(ctx, run)
	switch {
	case errors.Is(err, permissions.ErrDenied) || errors.Is(err, store.ErrNotFound):
		return true, nil
	case err != nil:
		return false, err
	}
	return false, nil
}

// resolveRunAccount refuses the run's account as a launch by its launcher
// would be refused now, including when the launcher, the member whose home
// it mounts, or the account's owner no longer reaches one of the run's
// workspaces.
func (s *Scheduler) resolveRunAccount(ctx context.Context, run *domain.Run) error {
	if err := s.checkRunReach(ctx, run); err != nil {
		return err
	}
	_, err := fallback.ResolveLaunchAccountIn(ctx, s.cfg.Store, runLauncher(run), string(run.AccountMember()), runWorkspaces(run))
	return err
}

// checkRunReach refuses the run when its launcher or the member whose home
// it mounts no longer reaches one of its workspaces, which no move to
// another account can fix. A handoff keeps the home and the launcher, so
// the current owner's access does not stand in.
func (s *Scheduler) checkRunReach(ctx context.Context, run *domain.Run) error {
	members := []domain.MemberID{runLauncher(run)}
	if home := run.HomeMember(); home != members[0] {
		members = append(members, home)
	}
	for _, id := range members {
		m, err := s.cfg.Store.GetMember(ctx, id)
		if err != nil {
			return err
		}
		reach := permissions.ActorOf(m)
		for _, ws := range runWorkspaces(run) {
			if !reach.Reaches(ws) {
				return fmt.Errorf("%w: member %s has no access to workspace %s", permissions.ErrDenied, id, ws)
			}
		}
	}
	return nil
}

// runLauncher is who launched the run. A row from before shares were
// narrowed records no launcher; its owner stands in.
func runLauncher(run *domain.Run) domain.MemberID {
	if run.HomeMemberID != "" {
		return run.HomeMemberID
	}
	return run.MemberID
}

func runWorkspaces(run *domain.Run) []domain.WorkspaceID {
	out := []domain.WorkspaceID{run.WorkspaceID}
	for _, repo := range run.Extras {
		out = append(out, repo.WorkspaceID)
	}
	return out
}

// leaveRevokedAccount claims a run on a revoked account and moves it to
// another account as a usage-limit move would, or stops it with the reason
// when it cannot move now, no account can take it, or the move fails. A run
// already ending, or held by another move, is left to that owner; the sweep
// checks it again.
func (s *Scheduler) leaveRevokedAccount(entry *supervised) {
	entry.lifecycleMu.Lock()
	s.mu.Lock()
	claimed := s.runs[entry.runID] == entry && entry.containerID != "" && !entry.retained && !entry.destroyPending &&
		!entry.killRequested && !entry.finalizing && !entry.exitObserved && !entry.moving &&
		!entry.evidencePending && !entry.reportFinishing &&
		(entry.status == domain.RunRunning || entry.status == domain.RunNeedsAttention)
	movable := claimed && s.usageMovableLocked(entry, entry.launchMode == domain.LaunchTUI)
	paused := entry.paused
	if claimed {
		entry.finalizing, entry.moving, entry.moveFrom = true, true, entry.containerID
	}
	s.mu.Unlock()
	entry.lifecycleMu.Unlock()
	if !claimed {
		return
	}
	ctx, cancel := context.WithTimeout(s.superCtx, usageMoveTimeout)
	defer cancel()
	// A move that finished between the check and the claim may have left
	// the revoked account already.
	run, err := s.cfg.Store.GetRun(ctx, entry.runID)
	revoked := err == nil
	if err == nil {
		revoked, err = s.accountRevoked(ctx, run)
	}
	if err != nil || !revoked {
		if err != nil {
			slog.Warn("scheduler: recheck the session's account share", "run", entry.runID, "error", err)
		}
		s.releaseMoveClaim(entry)
		return
	}
	if reachErr := s.checkRunReach(ctx, run); errors.Is(reachErr, permissions.ErrDenied) || errors.Is(reachErr, store.ErrNotFound) {
		s.stopRevoked(ctx, entry, workspaceRevokedReason+reachErr.Error())
		return
	}
	if !movable {
		cause := "the session cannot move to another account now"
		if paused {
			cause = "the session is paused"
		}
		s.stopRevoked(ctx, entry, revokeMove.cannotContinue(s.memberName(ctx, run.AccountMember()), cause))
		return
	}
	outcome, reason := s.moveAccount(ctx, entry, revokeMove)
	switch outcome {
	case usageContinued, usageShutdown:
		return
	case usageFailed:
		s.finalizeWithReason(entry, reason)
		return
	}
	s.mu.Lock()
	held := entry.moving
	s.mu.Unlock()
	if !held {
		// The run is on the new account, but its Enhanced conversation could
		// not be restored there.
		s.acp.report(entry.runID, agentstatus.Report{State: agentstatus.Idle, Reason: reason})
		return
	}
	s.stopRevoked(ctx, entry, reason)
}

// stopRevoked stops the claimed run's container, which still mounts the
// revoked login, and finalizes the run with reason. At shutdown it leaves the
// run to recovery and the sweep after it.
func (s *Scheduler) stopRevoked(ctx context.Context, entry *supervised, reason string) {
	if s.superCtx.Err() != nil {
		return
	}
	s.mu.Lock()
	cid := entry.containerID
	s.mu.Unlock()
	if err := s.prepareDevelopmentClose(ctx, entry.runID); err != nil {
		slog.Warn("scheduler: close the revoked session's development resources", "run", entry.runID, "error", err)
	}
	if err := s.cfg.Runtime.Stop(ctx, cid, s.cfg.StopGrace); err != nil && !errors.Is(err, runtime.ErrNotFound) {
		slog.Warn("scheduler: stop the session on a revoked account", "run", entry.runID, "error", err)
	}
	s.finalizeWithReason(entry, reason)
}
