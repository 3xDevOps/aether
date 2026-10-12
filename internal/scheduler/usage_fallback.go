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

// UseUsageFallback attaches reactive usage fallback, built after the
// scheduler.
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
	// usageEnded: the run was finalized; there is nothing left to report.
	usageEnded
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
	ctx, cancel := context.WithTimeout(context.Background(), usageMoveTimeout)
	defer cancel()
	outcome, reason := s.moveAccount(ctx, entry)
	switch outcome {
	case usageContinued:
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
func (s *Scheduler) continueExitedRun(ctx context.Context, entry *supervised, code int) bool {
	s.mu.Lock()
	candidate := code != 0 && entry.launchMode == domain.LaunchHeadless && !entry.acp &&
		!entry.killRequested && entry.reported == "" && entry.failReason == ""
	s.mu.Unlock()
	if !candidate || !s.awaitUsageLimit(ctx, entry) {
		return false
	}
	s.mu.Lock()
	movable := s.usageMovableLocked(entry, false)
	s.mu.Unlock()
	if !movable {
		return false
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), usageMoveTimeout)
	defer cancel()
	outcome, reason := s.moveAccount(ctx, entry)
	if outcome == usageContinued {
		return true
	}
	s.mu.Lock()
	entry.failReason = reason
	s.mu.Unlock()
	return false
}

// awaitUsageLimit reports whether the run's adapter saw the usage limit. The
// adapter reads the agent's output beside the exit, so for a harness with
// an adapter it waits briefly for the mark.
func (s *Scheduler) awaitUsageLimit(ctx context.Context, entry *supervised) bool {
	run, err := s.cfg.Store.GetRun(ctx, entry.runID)
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
func (s *Scheduler) markUsageLimit(run domain.RunID, message string) {
	s.mu.Lock()
	entry := s.runs[run]
	live := entry != nil && entry.launchMode == domain.LaunchHeadless && !entry.acp
	if live {
		entry.usageLimit = message
		if err := s.writeSidecar(entry.sidecar()); err != nil {
			slog.Warn("scheduler: persist the usage limit", "run", run, "error", err)
		}
	}
	supervised := live && entry.missionAssigned
	s.mu.Unlock()
	if !supervised {
		return
	}
	go func() {
		if outcome, reason := s.continueLiveRun(entry, false); outcome == usageNoFallback {
			if err := s.ReportAgentState(context.Background(), run, agentstatus.Report{State: agentstatus.Idle, Reason: reason}); err != nil {
				slog.Warn("scheduler: report the exhausted account", "run", run, "error", err)
			}
		}
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
	go func() {
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
	}()
	return true
}

// recordHeadlessSession keeps the session a headless agent reported in its
// output, which a move to another account resumes.
func (s *Scheduler) recordHeadlessSession(run domain.RunID, session string) {
	if !domain.ValidAgentSessionID(session) {
		return
	}
	s.mu.Lock()
	entry := s.runs[run]
	changed := entry != nil && !entry.acp && entry.agentSessionID != session
	if changed {
		entry.agentSessionID = session
		if err := s.writeSidecar(entry.sidecar()); err != nil {
			slog.Warn("scheduler: persist the agent's session", "run", run, "error", err)
		}
	}
	s.mu.Unlock()
	if changed {
		s.recordAgentSession(context.Background(), entry)
	}
}

// watchAgentOutput applies what run adapters read from headless agents'
// output: their sessions and usage limits.
func (s *Scheduler) watchAgentOutput(ctx context.Context) (<-chan events.Event, func()) {
	sub, err := s.cfg.Bus.Subscribe(ctx, events.SubscribeOptions{
		Filter: events.Filter{Types: []events.Type{events.TypeRunUsageLimit, events.TypeAgentEvent}},
	})
	if err != nil {
		slog.Warn("scheduler: subscribe to agent output; headless runs will not continue on another account", "error", err)
		return nil, func() {}
	}
	return sub.Events(), func() { _ = sub.Close() }
}

func (s *Scheduler) applyAgentOutput(e events.Event) {
	switch p := e.Payload.(type) {
	case events.RunUsageLimitPayload:
		s.markUsageLimit(e.RunID, p.Message)
	case events.AgentEventPayload:
		if p.Kind == events.AgentSession {
			s.recordHeadlessSession(e.RunID, p.HarnessSessionID)
		}
	}
}

// finalizeWithReason finalizes a claimed run that failed for reason.
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
	to, seq, err := s.recordMove(ctx, entry, f, run, measured)
	switch {
	case err != nil:
		return stop(err.Error())
	case to == "":
		return usageNoFallback, noFallbackReason(fromName)
	}
	return s.replaceContainer(ctx, entry, f, run, to, seq, session)
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
// authorization mutex, as a launch is admitted: a launch or revocation is
// ordered entirely before or after it. The run is authorised for both the
// member whose home its container mounts and its current owner, who differ
// after a handoff. It returns no account when none qualifies, and records
// nothing on an error.
func (s *Scheduler) recordMove(ctx context.Context, entry *supervised, f *UsageFallback, run *domain.Run, measured fallback.Measured) (domain.MemberID, int, error) {
	f.AuthorizationMu.Lock()
	defer f.AuthorizationMu.Unlock()
	home, from := run.HomeMember(), run.AccountMember()
	for _, member := range slices.Compact([]domain.MemberID{home, run.MemberID}) {
		if err := s.mayLaunch(ctx, member); err != nil {
			return "", 0, err
		}
	}
	mode := run.Mode
	if run.ACP {
		mode = domain.LaunchACP
	}
	to, err := fallback.Select(ctx, s.cfg.Store, measured, run.MemberID, from, run.Fallback, func(ctx context.Context, account domain.MemberID) (bool, error) {
		if home != run.MemberID {
			if _, err := fallback.ResolveLaunchAccount(ctx, s.cfg.Store, home, string(account)); err != nil {
				if errors.Is(err, permissions.ErrDenied) || errors.Is(err, store.ErrNotFound) {
					return false, nil
				}
				return false, err
			}
		}
		if ok, err := s.FallbackLaunchable(ctx, home, from, account, run.Harness, mode); !ok || err != nil {
			return ok, err
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if entry.runUser == "" {
			return true, nil
		}
		s.syncRunUserReservationsLocked()
		return s.reservationConflictLocked(entry.memberID, account, entry.runUser, "live run "+string(run.ID)) == nil, nil
	})
	if err != nil {
		return "", 0, fmt.Errorf("choose a fallback account: %w", err)
	}
	if to == from {
		return "", 0, nil
	}
	swap := &accountSwap{From: from, To: to}
	if run.ACP {
		swap.Continuation = continuationPrompt(s.memberName(ctx, from), s.memberName(ctx, to))
	}
	s.mu.Lock()
	entry.accountSwap = swap
	err = s.writeSidecar(entry.sidecar())
	if err != nil {
		entry.accountSwap = nil
	}
	s.mu.Unlock()
	if err != nil {
		return "", 0, fmt.Errorf("persist the move: %w", err)
	}
	seq, err := s.cfg.Store.SetRunAccount(ctx, run.ID, to, usageLimitSegment, time.Now().UTC())
	if err != nil {
		s.clearAccountSwap(entry, swap)
		return "", 0, fmt.Errorf("record the run's account: %w", err)
	}
	return to, seq, nil
}

// replaceContainer replaces the container of a run recorded on account to
// with one that mounts to's login, and resumes the agent's session there.
func (s *Scheduler) replaceContainer(ctx context.Context, entry *supervised, f *UsageFallback, run *domain.Run, to domain.MemberID, seq int, session string) (usageOutcome, string) {
	from := run.AccountMember()
	fromName, toName := s.memberName(ctx, from), s.memberName(ctx, to)
	failure := func(err error) string {
		return fmt.Sprintf("%s%s's account; continuing on %s's account failed: %v", usageLimitReason, fromName, toName, err)
	}
	failed := func(err error) (usageOutcome, string) {
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
			return usageNoFallback, failure(err)
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
		s.markUsageLimit(run.ID, again)
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
