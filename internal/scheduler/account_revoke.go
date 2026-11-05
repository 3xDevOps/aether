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
	launcher := run.HomeMemberID
	if launcher == "" {
		launcher = run.MemberID
	}
	_, err := fallback.ResolveLaunchAccount(ctx, s.cfg.Store, launcher, string(run.AccountMember()))
	switch {
	case errors.Is(err, permissions.ErrDenied) || errors.Is(err, store.ErrNotFound):
		return true, nil
	case err != nil:
		return false, err
	}
	return false, nil
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
