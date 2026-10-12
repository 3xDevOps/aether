package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/adapter"
	"github.com/3xDevOps/Aether/internal/agentstatus"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/fallback"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/ptyhost"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/store"
)

// UsageFallback wires reactive usage fallback: a live run whose agent hit
// its account's usage limit continues on another account.
type UsageFallback struct {
	Usage fallback.QuotaReader
	// AuthorizationMu is the mutex the RPC layer holds over every launch and
	// share revocation; a move holds it from choosing the account until the
	// run records it, and not while it replaces the container.
	AuthorizationMu *sync.Mutex
	// Note posts a system note to the run's room; key makes it idempotent.
	Note func(ctx context.Context, run *domain.Run, body, key string) error
}

// UseUsageFallback attaches the seams built after the scheduler.
func (s *Scheduler) UseUsageFallback(f UsageFallback) {
	s.mu.Lock()
	s.usageFallback = &f
	s.mu.Unlock()
}

func (s *Scheduler) usageFallbackSeam() *UsageFallback {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.usageFallback
}

// usageLimitReason starts the reason of a run whose account ran out with no
// account to continue on, or whose move to one failed. The dashboard matches
// it (web/src/lib/needs-you.ts).
const usageLimitReason = "Usage limit reached on "

// usageLimitSegment is the reason an account segment records for a move.
const usageLimitSegment = "usage limit"

// usageMoveTimeout bounds a move: removing the container, starting another
// and reopening the agent's session.
const usageMoveTimeout = 5 * time.Minute

// accountSwap is a move to another account the run has recorded but not
// finished. The sidecar keeps it so a restart can tell which account the
// container runs on, and send an Enhanced session the continuation the move
// did not get to.
type accountSwap struct {
	From domain.MemberID `json:"from"`
	To   domain.MemberID `json:"to"`
	// Container is the container created on To, empty until it exists.
	Container string `json:"container,omitempty"`
	// Continuation is the prompt the Enhanced session on To is still owed.
	Continuation string `json:"continuation,omitempty"`
}

func noFallbackReason(owner string) string {
	return usageLimitReason + owner + "'s account; no fallback has usage left"
}

func cannotContinueReason(owner, cause string) string {
	return usageLimitReason + owner + "'s account; cannot continue on another account: " + cause
}

func continuationPrompt(from, to string) string {
	return fmt.Sprintf("Your previous turn stopped because %s's usage ran out. The run continues on %s's account. Continue the task where you stopped.", from, to)
}

// usageMovableLocked reports whether entry's run may move to another account
// now: a live run nothing else is changing, in its terminal (Standard) when
// terminal is set and otherwise Enhanced or headless. Caller holds s.mu.
func (s *Scheduler) usageMovableLocked(entry *supervised, terminal bool) bool {
	return s.runs[entry.runID] == entry && entry.containerID != "" && !entry.retained && !entry.destroyPending &&
		!entry.killRequested && !entry.paused && entry.switching == "" && entry.switchIntent == nil &&
		!entry.evidencePending && entry.reported == "" && !entry.reportFinishing &&
		(entry.status == domain.RunRunning || entry.status == domain.RunNeedsAttention) &&
		(entry.launchMode == domain.LaunchTUI) == terminal
}

type usageOutcome int

const (
	// usageRefused: the run is not this move's to take; it carries on as
	// it would have without fallback.
	usageRefused usageOutcome = iota
	// usageNoFallback: the run stays where it is and stops with the reason.
	usageNoFallback
	usageContinued
	// usageFailed: the move failed with the run's container gone; the
	// caller finalizes the run with the reason.
	usageFailed
	// usageEnded: the run was finalized; there is nothing left to do.
	usageEnded
	// usageShutdown: the server is stopping. The move changed nothing that
	// decides the run's outcome, and leaves its usage-limit mark and any
	// marker for recovery.
	usageShutdown
)

// continueLiveRun moves a run whose live agent hit its usage limit to the
// next account. For usageNoFallback it returns the reason the run stops
// with.
func (s *Scheduler) continueLiveRun(entry *supervised, terminal bool) (usageOutcome, string) {
	entry.lifecycleMu.Lock()
	s.mu.Lock()
	claimed := s.usageMovableLocked(entry, terminal) && !entry.finalizing && !entry.exitObserved
	if claimed {
		entry.finalizing = true
	}
	s.mu.Unlock()
	entry.lifecycleMu.Unlock()
	if !claimed {
		return usageRefused, ""
	}
	ctx, cancel := context.WithTimeout(s.superCtx, usageMoveTimeout)
	defer cancel()
	outcome, reason := s.moveAccount(ctx, entry)
	switch outcome {
	case usageContinued, usageEnded, usageShutdown:
		return outcome, ""
	case usageFailed:
		s.finalizeWithReason(entry, reason)
		return usageEnded, ""
	}
	if s.releaseMoveClaim(entry) {
		return usageEnded, ""
	}
	return usageNoFallback, reason
}

// releaseMoveClaim gives up a move's claim on a run that stays where it is.
// The wait for the container's exit leaves an exit it saw while the move held
// the claim to the claim's owner, so a container that exited or was killed
// meanwhile is finalized here. It reports whether it finalized the run.
func (s *Scheduler) releaseMoveClaim(entry *supervised) bool {
	entry.lifecycleMu.Lock()
	s.mu.Lock()
	entry.finalizing = false
	cid := entry.containerID
	s.mu.Unlock()
	entry.lifecycleMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.ExitProbeTimeout)
	st, err := s.cfg.Runtime.Wait(ctx, cid)
	cancel()
	switch {
	case errors.Is(err, runtime.ErrNotFound):
		st.Code = -1
	case err != nil:
		return false
	}
	entry.lifecycleMu.Lock()
	s.mu.Lock()
	claimed := s.runs[entry.runID] == entry && entry.containerID == cid && !entry.finalizing && !entry.retained
	if claimed {
		entry.finalizing = true
	}
	s.mu.Unlock()
	entry.lifecycleMu.Unlock()
	if claimed {
		s.recordExitObserved(entry, st.Code)
		s.finalize(entry, st.Code)
	}
	return true
}

// continueExitedRun moves a headless run whose agent exited on its usage
// limit to the next account, from finalize, which has claimed it. It
// reports whether the run continues; otherwise finalize goes on with the
// reason it set.
func (s *Scheduler) continueExitedRun(entry *supervised, code int) bool {
	s.mu.Lock()
	candidate := code != 0 && entry.launchMode == domain.LaunchHeadless && !entry.acp &&
		!entry.killRequested && entry.reported == "" && entry.failReason == ""
	s.mu.Unlock()
	if !candidate {
		return false
	}
	ctx, cancel := context.WithTimeout(s.superCtx, usageMoveTimeout)
	defer cancel()
	if !s.awaitUsageLimit(entry) {
		return false
	}
	s.mu.Lock()
	movable := s.usageMovableLocked(entry, false)
	s.mu.Unlock()
	if !movable {
		s.clearUsageLimit(entry)
		return false
	}
	// A successful move already dropped the old container's mark; a mark
	// left now is the new container's.
	outcome, reason := s.moveAccount(ctx, entry)
	switch outcome {
	case usageContinued, usageShutdown:
		return true
	}
	s.clearUsageLimit(entry)
	s.mu.Lock()
	entry.failReason = reason
	s.mu.Unlock()
	return false
}

// clearUsageLimit drops a usage-limit mark once a move has acted on it, so a
// later exit of the same container, or of a reopened retained one, is not
// taken for it. A move in progress keeps the mark: it follows a new limit
// once it is done.
func (s *Scheduler) clearUsageLimit(entry *supervised) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry.usageLimit == "" || (entry.finalizing && entry.accountSwap != nil) {
		return
	}
	entry.usageLimit = ""
	if s.runs[entry.runID] != entry {
		return
	}
	if err := s.writeSidecar(entry.sidecar()); err != nil {
		slog.Warn("scheduler: clear the usage limit", "run", entry.runID, "error", err)
	}
}

// awaitUsageLimit reports whether the run's adapter saw the usage limit. The
// adapter reads the agent's output beside the exit, so for a harness with
// an adapter it waits briefly for the mark.
func (s *Scheduler) awaitUsageLimit(entry *supervised) bool {
	run, err := s.cfg.Store.GetRun(context.Background(), entry.runID)
	if err != nil {
		return false
	}
	if _, ok := adapter.ForHarness(run.Harness); !ok {
		return false
	}
	deadline := time.Now().Add(exitDrainWait)
	for {
		s.mu.Lock()
		marked := entry.usageLimit != ""
		s.mu.Unlock()
		if marked || time.Now().After(deadline) {
			return marked
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// markUsageLimit records the adapter's usage-limit signal for a headless
// run, in the sidecar too, so a restart that finds the agent exited still
// knows why. A swarm worker's container outlives its agent, so its move
// starts here rather than at the container's exit.
func (s *Scheduler) markUsageLimit(run domain.RunID, message, session string) {
	s.mu.Lock()
	entry := s.runs[run]
	live := entry != nil && entry.launchMode == domain.LaunchHeadless && !entry.acp
	newSession := live && domain.ValidAgentSessionID(session) && entry.agentSessionID != session
	if live {
		entry.usageLimit = message
		if newSession {
			entry.agentSessionID = session
		}
		if err := s.writeSidecar(entry.sidecar()); err != nil {
			slog.Warn("scheduler: persist the usage limit", "run", run, "error", err)
		}
	}
	supervised := live && entry.missionAssigned
	s.mu.Unlock()
	if newSession {
		s.recordAgentSession(context.Background(), entry)
	}
	if !supervised {
		return
	}
	s.goMove(func() {
		outcome, reason := s.continueLiveRun(entry, false)
		if outcome == usageContinued || outcome == usageShutdown {
			return
		}
		s.clearUsageLimit(entry)
		if outcome == usageNoFallback {
			if err := s.ReportAgentState(context.Background(), run, agentstatus.Report{State: agentstatus.Idle, Reason: reason}); err != nil {
				slog.Warn("scheduler: report the exhausted account", "run", run, "error", err)
			}
		}
	})
}

// goMove runs a move its trigger cannot wait for, a hook or a session
// callback, where Close waits for it. Close cancels superCtx under s.mu, so
// no move starts once it waits.
func (s *Scheduler) goMove(move func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.superCtx.Err() != nil {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		move()
	}()
}

// continueTerminalRun takes a Standard run's report that its agent stopped
// on the usage limit, and reports whether it did. The move runs behind the
// agent's hook, which must return at once; when the run is not moved, the
// report applies as it would have without fallback.
func (s *Scheduler) continueTerminalRun(ctx context.Context, run domain.RunID, report agentstatus.Report) bool {
	s.mu.Lock()
	entry := s.runs[run]
	movable := entry != nil && s.usageMovableLocked(entry, true) && !entry.finalizing && !entry.exitObserved
	newSession := movable && s.recordReportedSessionLocked(entry, report.SessionID)
	s.mu.Unlock()
	if !movable {
		return false
	}
	if newSession {
		s.recordAgentSession(ctx, entry)
	}
	s.goMove(func() {
		report.UsageLimit = false
		switch outcome, stopped := s.continueLiveRun(entry, true); outcome {
		case usageRefused:
		case usageNoFallback:
			report = agentstatus.Report{State: agentstatus.Idle, Reason: stopped}
		default:
			return
		}
		if err := s.ReportAgentState(context.Background(), run, report); err != nil {
			slog.Warn("scheduler: report the exhausted account", "run", run, "error", err)
		}
	})
	return true
}

// subscribeUsageLimits replays from after when it is not zero, or from now
// when the bus keeps no log to replay.
func (s *Scheduler) subscribeUsageLimits(ctx context.Context, after uint64) (events.Subscription, error) {
	opts := events.SubscribeOptions{Filter: events.Filter{Types: []events.Type{events.TypeRunUsageLimit}}}
	if after > 0 {
		replayed := opts
		replayed.Replay, replayed.AfterSeq = true, after
		if sub, err := s.cfg.Bus.Subscribe(ctx, replayed); err == nil {
			return sub, nil
		}
	}
	return s.cfg.Bus.Subscribe(ctx, opts)
}

// watchUsageLimits marks the runs whose adapters read a usage limit in
// their agents' output, until ctx ends. A limit must not be lost: a dropped
// event or a closed stream resubscribes with replay from the last event
// handled.
func (s *Scheduler) watchUsageLimits(ctx context.Context, sub events.Subscription) {
	var last uint64
	for {
		last = s.consumeUsageLimits(ctx, sub, last)
		_ = sub.Close()
		if ctx.Err() != nil {
			return
		}
		next, err := s.subscribeUsageLimits(ctx, last)
		if err != nil {
			if !errors.Is(err, events.ErrBusClosed) && ctx.Err() == nil {
				slog.Warn("scheduler: resubscribe to usage limits; headless runs will not continue on another account", "error", err)
			}
			return
		}
		sub = next
	}
}

// consumeUsageLimits returns at a dropped event, for the caller to replay
// from the last sequence it handled.
func (s *Scheduler) consumeUsageLimits(ctx context.Context, sub events.Subscription, last uint64) uint64 {
	for {
		select {
		case <-ctx.Done():
			return last
		case e, ok := <-sub.Events():
			if !ok || sub.Dropped() > 0 {
				return last
			}
			if p, isLimit := e.Payload.(events.RunUsageLimitPayload); isLimit {
				s.markUsageLimit(e.RunID, p.Message, p.SessionID)
			}
			last = max(last, e.Seq)
		}
	}
}

func (s *Scheduler) finalizeWithReason(entry *supervised, reason string) {
	s.mu.Lock()
	entry.failReason = reason
	s.mu.Unlock()
	s.finalize(entry, -1)
}

// moveAccount moves the claimed run to the next account in its fallback
// policy and resumes the agent's session there. usageNoFallback leaves the
// run where it is, with the reason it stops with; usageFailed means its
// container is gone and the reason is what it fails with.
func (s *Scheduler) moveAccount(ctx context.Context, entry *supervised) (usageOutcome, string) {
	outcome, reason := s.tryMove(ctx, entry)
	if outcome != usageContinued && s.superCtx.Err() != nil {
		return usageShutdown, ""
	}
	return outcome, reason
}

func (s *Scheduler) tryMove(ctx context.Context, entry *supervised) (usageOutcome, string) {
	run, err := s.cfg.Store.GetRun(ctx, entry.runID)
	if err != nil {
		return usageNoFallback, cannotContinueReason("the run", "load the run: "+err.Error())
	}
	from := run.AccountMember()
	fromName := s.memberName(ctx, from)
	stop := func(cause string) (usageOutcome, string) {
		return usageNoFallback, cannotContinueReason(fromName, cause)
	}
	if run.Fallback.Disabled {
		return usageNoFallback, usageLimitReason + fromName + "'s account; fallback is off for this run"
	}
	f := s.usageFallbackSeam()
	if f == nil || f.Usage == nil {
		return stop("usage fallback is not configured on this server")
	}
	if run.HomeMemberID == "" {
		return stop("the run predates narrowed account shares, and its container mounts the account owner's whole home")
	}
	profile, _, err := s.launchProfile(ctx, run.HomeMember(), from, run.Harness)
	if err != nil {
		return stop(err.Error())
	}
	s.mu.Lock()
	session := entry.agentSessionID
	s.mu.Unlock()
	if session == "" {
		session = run.HarnessSessionID
	}
	resume := profile.HeadlessResumeArgs
	if run.Mode == domain.LaunchTUI {
		resume = profile.ResumeArgs
	}
	if !run.ACP {
		switch {
		case len(resume) == 0:
			return stop(profile.Name + " cannot resume its session in this mode")
		case !domain.ValidAgentSessionID(session):
			return stop("the agent has not reported a session to resume")
		}
	}
	segments, err := s.cfg.Store.ListRunAccountSegments(ctx, run.ID)
	if err != nil {
		return stop(err.Error())
	}
	used := make([]domain.MemberID, 0, len(segments))
	for _, segment := range segments {
		used = append(used, segment.AccountMemberID)
	}
	measured, err := fallback.MeasureNext(ctx, s.cfg.Store, f.Usage, run.MemberID, from, used, run.Harness, run.Fallback)
	if err != nil {
		return stop("read fallback usage: " + err.Error())
	}
	run, to, seq, excluded, err := s.recordMove(ctx, entry, f, run.ID, from, measured)
	switch {
	case err != nil:
		return stop(err.Error())
	case to == "" && excluded != nil:
		return stop(excluded.Error())
	case to == "":
		return usageNoFallback, noFallbackReason(fromName)
	case excluded != nil:
		slog.Warn("scheduler: fallback accounts left out of a move", "run", run.ID, "error", excluded)
	}
	return s.replaceContainer(ctx, entry, f, run, from, to, seq, session)
}

// mayLaunch is the launch admission a member's own standing needs, checked
// again for a move as for a launch.
func (s *Scheduler) mayLaunch(ctx context.Context, id domain.MemberID) error {
	member, err := s.cfg.Store.GetMember(ctx, id)
	if err != nil {
		return err
	}
	if member.Pending {
		return fmt.Errorf("%w: %s's membership is pending admin approval", permissions.ErrDenied, member.DisplayName)
	}
	return permissions.Check(permissions.Launch, permissions.Actor{ID: member.ID, Role: member.Role}, permissions.Target{})
}

// recordMove chooses the account and records the run on it under the
// authorization mutex, as a launch is admitted: a launch, handoff or
// revocation is ordered entirely before or after it. The run is read again
// under the mutex and authorised for both the member whose home its
// container mounts and its current owner, who differ after a handoff; an
// account measured for an earlier owner but not shared with this one is
// left out. It returns the run as read, and no account when none qualifies
// with excluded joining the candidate checks that failed; it records
// nothing on an error.
func (s *Scheduler) recordMove(ctx context.Context, entry *supervised, f *UsageFallback, id domain.RunID, from domain.MemberID, measured fallback.Measured) (*domain.Run, domain.MemberID, int, error, error) {
	f.AuthorizationMu.Lock()
	defer f.AuthorizationMu.Unlock()
	run, err := s.cfg.Store.GetRun(ctx, id)
	if err != nil {
		return nil, "", 0, nil, fmt.Errorf("load the run: %w", err)
	}
	if run.AccountMember() != from {
		return nil, "", 0, nil, fmt.Errorf("the run moved to %s's account meanwhile", s.memberName(ctx, run.AccountMember()))
	}
	home := run.HomeMember()
	for _, member := range slices.Compact([]domain.MemberID{home, run.MemberID}) {
		if launchErr := s.mayLaunch(ctx, member); launchErr != nil {
			return nil, "", 0, nil, launchErr
		}
	}
	mode := run.Mode
	if run.ACP {
		mode = domain.LaunchACP
	}
	to, excluded := fallback.Select(ctx, s.cfg.Store, measured, run.MemberID, from, run.Fallback, func(ctx context.Context, account domain.MemberID) (bool, error) {
		if home != run.MemberID {
			if _, authErr := fallback.ResolveLaunchAccount(ctx, s.cfg.Store, home, string(account)); authErr != nil {
				if errors.Is(authErr, permissions.ErrDenied) || errors.Is(authErr, store.ErrNotFound) {
					return false, nil
				}
				return false, authErr
			}
		}
		return s.FallbackLaunchable(ctx, home, from, account, run.Harness, mode)
	})
	if to == from {
		return run, "", 0, excluded, nil
	}
	swap := &accountSwap{From: from, To: to}
	if run.ACP {
		swap.Continuation = continuationPrompt(s.memberName(ctx, from), s.memberName(ctx, to))
	}
	s.mu.Lock()
	if entry.killRequested {
		s.mu.Unlock()
		return nil, "", 0, nil, errors.New("the run is being killed")
	}
	entry.accountSwap = swap
	err = s.writeSidecar(entry.sidecar())
	if err != nil {
		entry.accountSwap = nil
	}
	s.mu.Unlock()
	if err != nil {
		return nil, "", 0, nil, fmt.Errorf("persist the move: %w", err)
	}
	seq, err := s.cfg.Store.SetRunAccount(ctx, run.ID, to, usageLimitSegment, time.Now().UTC())
	if err != nil {
		s.clearAccountSwap(entry, swap)
		return nil, "", 0, nil, fmt.Errorf("record the run's account: %w", err)
	}
	return run, to, seq, nil, nil
}

// replaceContainer runs outside the authorization mutex: the run is already
// recorded on to.
func (s *Scheduler) replaceContainer(ctx context.Context, entry *supervised, f *UsageFallback, run *domain.Run, from, to domain.MemberID, seq int, session string) (usageOutcome, string) {
	fromName, toName := s.memberName(ctx, from), s.memberName(ctx, to)
	failure := func(err error) string {
		return fmt.Sprintf("%s%s's account; continuing on %s's account failed: %v", usageLimitReason, fromName, toName, err)
	}
	failed := func(err error) (usageOutcome, string) {
		if s.superCtx.Err() != nil {
			// Recovery settles the move from its marker, which stays.
			return usageShutdown, ""
		}
		s.mu.Lock()
		entry.accountSwap = nil
		if werr := s.writeSidecar(entry.sidecar()); werr != nil {
			slog.Warn("scheduler: persist the failed account move", "run", run.ID, "error", werr)
		}
		s.mu.Unlock()
		if run.ACP {
			s.acp.switchNotice(run.ID, "error", "Continuing on "+toName+"'s account failed", err.Error())
		}
		return usageFailed, failure(err)
	}
	s.mu.Lock()
	cid := entry.containerID
	s.mu.Unlock()
	// The container's name and creation key are the run's, and its login is
	// mounted at creation, so the old container goes before the new exists.
	if stopErr := s.entryDriver(entry).Stop(ctx, run.ID); stopErr != nil {
		slog.Warn("scheduler: stop the exhausted agent", "run", run.ID, "error", stopErr)
	}
	s.cfg.PTY.StopSessionsWithPrefix(ctx, string(ptyhost.RunShellSession(run.ID, "")))
	if derr := s.destroyDevelopmentContainer(ctx, run.ID, cid); derr != nil && !errors.Is(derr, runtime.ErrNotFound) {
		return failed(fmt.Errorf("remove the container on %s's account: %w", fromName, derr))
	}
	s.mu.Lock()
	entry.containerID = ""
	entry.agentExec = nil
	if entry.userReservation != nil {
		delete(s.credentialUsers, entry.userReservation)
		entry.userReservation = nil
	}
	entry.runUser = ""
	// The exit and usage limit were the old container's; the sidecar that
	// names the new one must not carry them.
	entry.exitObserved, entry.exitCode, entry.usageLimit = false, 0, ""
	if werr := s.writeSidecar(entry.sidecar()); werr != nil {
		slog.Warn("scheduler: persist the removed container", "run", run.ID, "error", werr)
	}
	s.mu.Unlock()
	if run.ACP {
		s.acp.switchNotice(run.ID, "info", "Continued on "+toName+"'s account", fromName+"'s usage ran out.")
	}

	ws, err := s.cfg.Store.GetWorkspace(ctx, run.WorkspaceID)
	if err != nil {
		return failed(err)
	}
	home, err := s.cfg.Store.GetMember(ctx, run.HomeMember())
	if err != nil {
		return failed(err)
	}
	next := *run
	next.AccountMemberID = to
	profile, _, err := s.launchProfile(ctx, home.ID, to, run.Harness)
	if err != nil {
		return failed(err)
	}
	var argv []string
	switch {
	case run.ACP:
	case run.Mode == domain.LaunchTUI:
		argv = append(profile.ResumeCommand(session), continuationPrompt(fromName, toName))
	default:
		argv = profile.HeadlessResumeCommand(session, continuationPrompt(fromName, toName))
	}
	plan, err := s.runPlan(ctx, entry, &next, ws, home, profile)
	if err != nil {
		return failed(err)
	}
	s.mu.Lock()
	persistSupervisor := entry.missionAssigned
	s.mu.Unlock()
	if _, err = s.startRunContainer(ctx, entry, &next, home, argv, profile, plan, persistSupervisor); err != nil {
		return failed(err)
	}

	s.mu.Lock()
	swap := entry.accountSwap
	if !run.ACP {
		entry.accountSwap = nil
	}
	entry.finalizing, entry.waitStarted = false, false
	again := entry.usageLimit
	if werr := s.writeSidecar(entry.sidecar()); werr != nil {
		slog.Warn("scheduler: persist the account move", "run", run.ID, "error", werr)
	}
	killed, cid := entry.killRequested, entry.containerID
	s.mu.Unlock()
	s.publishAccount(ctx, run.ID, from)
	s.startSupervision(entry)
	if err := s.reopenDevelopment(ctx, run.ID); err != nil {
		slog.Warn("scheduler: reopen the run's terminals", "run", run.ID, "error", err)
	}
	if killed {
		if err := s.cfg.Runtime.Stop(ctx, cid, s.cfg.StopGrace); err != nil && !errors.Is(err, runtime.ErrNotFound) {
			slog.Warn("scheduler: stop the killed run's new container", "run", run.ID, "error", err)
		}
	}
	if run.ACP {
		if _, err := s.acp.live(run.ID); err != nil {
			s.clearAccountSwap(entry, swap)
			return usageNoFallback, fmt.Sprintf("%s%s's account; the run is on %s's account and its session could not be restored: %v", usageLimitReason, fromName, toName, err)
		}
	}
	if f.Note != nil {
		body := fmt.Sprintf("Continued on %s's account because %s's usage ran out.", toName, fromName)
		if err := f.Note(ctx, run, body, fmt.Sprintf("%s:account:%d", run.ID, seq)); err != nil {
			slog.Warn("scheduler: post the account move to the room", "run", run.ID, "error", err)
		}
	}
	switch {
	case run.ACP:
		s.acp.sendContinuation(entry)
	case again != "" && persistSupervisor:
		s.markUsageLimit(run.ID, again, "")
	}
	return usageContinued, ""
}

// clearAccountSwap drops swap once nothing of the move is left to do, unless
// a later move has replaced it.
func (s *Scheduler) clearAccountSwap(entry *supervised, swap *accountSwap) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry.accountSwap != swap {
		return
	}
	entry.accountSwap = nil
	if s.runs[entry.runID] != entry {
		return
	}
	if err := s.writeSidecar(entry.sidecar()); err != nil {
		slog.Warn("scheduler: clear the account move", "run", entry.runID, "error", err)
	}
}

func (s *Scheduler) publishAccount(ctx context.Context, id domain.RunID, from domain.MemberID) {
	run, err := s.cfg.Store.GetRun(ctx, id)
	if err != nil {
		slog.Warn("scheduler: publish the run's account", "run", id, "error", err)
		return
	}
	s.publish(ctx, events.Event{WorkspaceID: run.WorkspaceID, RunID: id, Payload: events.RunAccountPayload{
		Account: run.AccountMember(), Previous: from, Requested: run.RequestedAccountMemberID,
	}})
}

func (s *Scheduler) memberName(ctx context.Context, id domain.MemberID) string {
	if m, err := s.cfg.Store.GetMember(ctx, id); err == nil && m.DisplayName != "" {
		return m.DisplayName
	}
	return string(id)
}

func (s *Scheduler) dropAccountSwap(run domain.RunID, sc *sidecar) {
	sc.AccountSwap = nil
	if err := s.writeSidecar(*sc); err != nil {
		slog.Warn("scheduler: clear an interrupted account move", "run", run, "error", err)
	}
}
