package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/adapter"
	"github.com/3xDevOps/Aether/internal/agentstatus"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/fallback"
	"github.com/3xDevOps/Aether/internal/ptyhost"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// UsageFallback wires reactive usage fallback: a live run whose agent hit
// its account's usage limit continues on another account.
type UsageFallback struct {
	Usage fallback.QuotaReader
	// AuthorizationMu is the mutex the RPC layer holds over every launch and
	// share revocation; a move holds it from choosing the account until the
	// run records it.
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

// accountSwap is a move to another account whose container may exist. The
// sidecar keeps it so a restart can tell which account the container runs on.
type accountSwap struct {
	From domain.MemberID `json:"from"`
	To   domain.MemberID `json:"to"`
	// Container is the container created on To, empty until it exists.
	Container string `json:"container,omitempty"`
}

func noFallbackReason(owner string) string {
	return usageLimitReason + owner + "'s account; no fallback has usage left"
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
	usageNoFallback
	usageContinued
	// usageEnded: the move failed after the run's container was gone, and
	// the run was finalized with the failure.
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
	moved, reason, err := s.moveAccount(ctx, entry)
	switch {
	case err != nil:
		slog.Warn("scheduler: continue on another account", "run", entry.runID, "error", err)
		s.finalizeWithReason(entry, reason)
		return usageEnded, ""
	case moved:
		return usageContinued, ""
	}
	s.mu.Lock()
	entry.finalizing = false
	s.mu.Unlock()
	return usageNoFallback, reason
}

// continueExitedRun moves a headless run whose agent exited on its usage
// limit to the next account, from finalize, which has claimed it. It
// reports whether the run continues; otherwise finalize goes on with the
// reason it set.
func (s *Scheduler) continueExitedRun(ctx context.Context, entry *supervised, code int) bool {
	s.mu.Lock()
	candidate := code != 0 && entry.launchMode == domain.LaunchHeadless && !entry.acp &&
		!entry.killRequested && entry.reported == ""
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
	moved, reason, err := s.moveAccount(ctx, entry)
	if err != nil {
		slog.Warn("scheduler: continue on another account", "run", entry.runID, "error", err)
	}
	if !moved {
		s.mu.Lock()
		entry.failReason = reason
		s.mu.Unlock()
	}
	return moved
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
// run. A swarm worker's container outlives its agent, so its move starts
// here rather than at the container's exit.
func (s *Scheduler) markUsageLimit(run domain.RunID, message string) {
	s.mu.Lock()
	entry := s.runs[run]
	live := entry != nil && entry.launchMode == domain.LaunchHeadless && !entry.acp
	if live {
		entry.usageLimit = message
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
	if entry != nil && !entry.acp {
		// The session line starts an agent process. A usage limit read
		// before it is the previous process's, replayed when the adapter
		// tapped the new container's output.
		entry.usageLimit = ""
	}
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

// moveAccount replaces the claimed run's container with one on the next
// account in its fallback policy and resumes the agent's session there. With
// no account to move to it changes nothing and returns the reason the run
// stops with. An error means the run's container is gone or not running the
// agent, and reason is what the run fails with.
func (s *Scheduler) moveAccount(ctx context.Context, entry *supervised) (moved bool, reason string, err error) {
	run, err := s.cfg.Store.GetRun(ctx, entry.runID)
	if err != nil {
		slog.Warn("scheduler: load the exhausted run", "run", entry.runID, "error", err)
		return false, noFallbackReason(s.memberName(ctx, entry.memberID)), nil
	}
	from := run.AccountMember()
	reason = noFallbackReason(s.memberName(ctx, from))
	f := s.usageFallbackSeam()
	if f == nil || run.HomeMemberID == "" {
		return false, reason, nil
	}
	profile, _, err := s.launchProfile(ctx, run.MemberID, from, run.Harness)
	if err != nil {
		slog.Warn("scheduler: resolve the exhausted run's agent", "run", run.ID, "error", err)
		return false, reason, nil
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
	if !run.ACP && (len(resume) == 0 || !domain.ValidAgentSessionID(session)) {
		return false, reason, nil
	}
	segments, err := s.cfg.Store.ListRunAccountSegments(ctx, run.ID)
	if err != nil {
		slog.Warn("scheduler: read the run's accounts", "run", run.ID, "error", err)
		return false, reason, nil
	}
	used := make([]domain.MemberID, 0, len(segments))
	for _, segment := range segments {
		used = append(used, segment.AccountMemberID)
	}
	measured, err := fallback.MeasureNext(ctx, s.cfg.Store, f.Usage, run.MemberID, from, used, run.Harness, run.Fallback)
	if err != nil {
		slog.Warn("scheduler: read fallback usage", "run", run.ID, "error", err)
		return false, reason, nil
	}
	to, seq, reason, err := s.moveAuthorized(ctx, entry, f, run, measured, session, reason)
	if err != nil || to == "" {
		return false, reason, err
	}
	s.publishAccount(ctx, run.ID, from)
	s.startSupervision(entry)
	if err := s.reopenDevelopment(ctx, run.ID); err != nil {
		slog.Warn("scheduler: reopen the run's terminals", "run", run.ID, "error", err)
	}
	s.mu.Lock()
	killed, cid := entry.killRequested, entry.containerID
	s.mu.Unlock()
	if killed {
		if err := s.cfg.Runtime.Stop(ctx, cid, s.cfg.StopGrace); err != nil && !errors.Is(err, runtime.ErrNotFound) {
			slog.Warn("scheduler: stop the killed run's new container", "run", run.ID, "error", err)
		}
	}
	if f.Note != nil {
		body := fmt.Sprintf("Continued on %s's account because %s's usage ran out.", s.memberName(ctx, to), s.memberName(ctx, from))
		if err := f.Note(ctx, run, body, fmt.Sprintf("%s:account:%d", run.ID, seq)); err != nil {
			slog.Warn("scheduler: post the account move to the room", "run", run.ID, "error", err)
		}
	}
	return true, "", nil
}

// moveAuthorized chooses the account and moves the run to it under the
// authorization mutex, so a share revoked before the run records its new
// account excludes that account. It returns no account when none qualifies.
func (s *Scheduler) moveAuthorized(ctx context.Context, entry *supervised, f *UsageFallback, run *domain.Run, measured fallback.Measured, session, noFallback string) (domain.MemberID, int, string, error) {
	f.AuthorizationMu.Lock()
	defer f.AuthorizationMu.Unlock()
	from := run.AccountMember()
	mode := run.Mode
	if run.ACP {
		mode = domain.LaunchACP
	}
	to, err := fallback.Select(ctx, s.cfg.Store, measured, run.MemberID, from, run.Fallback, func(ctx context.Context, account domain.MemberID) (bool, error) {
		if ok, err := s.FallbackLaunchable(ctx, run.MemberID, account, run.Harness, mode); !ok || err != nil {
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
		slog.Warn("scheduler: choose a fallback account", "run", run.ID, "error", err)
		return "", 0, noFallback, nil
	}
	if to == from {
		return "", 0, noFallback, nil
	}
	fromName, toName := s.memberName(ctx, from), s.memberName(ctx, to)
	continuation := fmt.Sprintf("Your previous turn stopped because %s's usage ran out. The run continues on %s's account. Continue the task where you stopped.", fromName, toName)
	failed := func(err error) (domain.MemberID, int, string, error) {
		s.mu.Lock()
		entry.accountSwap, entry.continuation = nil, ""
		s.mu.Unlock()
		return "", 0, fmt.Sprintf("%s%s's account; continuing on %s's account failed: %v", usageLimitReason, fromName, toName, err), err
	}

	s.mu.Lock()
	cid := entry.containerID
	entry.accountSwap = &accountSwap{From: from, To: to}
	entry.continuation = continuation
	if werr := s.writeSidecar(entry.sidecar()); werr != nil {
		entry.accountSwap, entry.continuation = nil, ""
		s.mu.Unlock()
		slog.Warn("scheduler: persist the account move", "run", run.ID, "error", werr)
		return "", 0, noFallback, nil
	}
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
	if werr := s.writeSidecar(entry.sidecar()); werr != nil {
		slog.Warn("scheduler: persist the removed container", "run", run.ID, "error", werr)
	}
	s.mu.Unlock()

	ws, err := s.cfg.Store.GetWorkspace(ctx, run.WorkspaceID)
	if err != nil {
		return failed(err)
	}
	actor, err := s.cfg.Store.GetMember(ctx, run.HomeMember())
	if err != nil {
		return failed(err)
	}
	next := *run
	next.AccountMemberID = to
	profile, _, err := s.launchProfile(ctx, run.MemberID, to, run.Harness)
	if err != nil {
		return failed(err)
	}
	var argv []string
	switch {
	case run.ACP:
		s.acp.switchNotice(run.ID, "info", "Continued on "+toName+"'s account", fromName+"'s usage ran out.")
	case run.Mode == domain.LaunchTUI:
		argv = append(profile.ResumeCommand(session), continuation)
	default:
		argv = profile.HeadlessResumeCommand(session, continuation)
	}
	plan, err := s.runPlan(ctx, entry, &next, ws, actor, profile)
	if err != nil {
		return failed(err)
	}
	s.mu.Lock()
	persistSupervisor := entry.missionAssigned
	s.mu.Unlock()
	if _, err = s.startRunContainer(ctx, entry, &next, actor, argv, profile, plan, persistSupervisor); err != nil {
		return failed(err)
	}

	seq, err := s.cfg.Store.SetRunAccount(ctx, run.ID, to, usageLimitSegment, time.Now().UTC())
	if err != nil {
		// The sidecar keeps the move; recovery records it.
		slog.Warn("scheduler: record the run's account", "run", run.ID, "error", err)
	}
	s.mu.Lock()
	if err == nil {
		entry.accountSwap = nil
	}
	entry.continuation, entry.usageLimit, entry.failReason = "", "", ""
	entry.exitObserved, entry.exitCode = false, 0
	entry.finalizing, entry.waitStarted = false, false
	if werr := s.writeSidecar(entry.sidecar()); werr != nil {
		slog.Warn("scheduler: persist the account move", "run", run.ID, "error", werr)
	}
	s.mu.Unlock()
	return to, seq, "", nil
}

// settleAccountSwap finishes a move the previous server process left between
// creating the container on the new account and recording it: sc's container
// is the move's. It reports whether the move is recorded.
func (s *Scheduler) settleAccountSwap(ctx context.Context, r *domain.Run, swap *accountSwap) bool {
	if _, err := s.cfg.Store.SetRunAccount(ctx, r.ID, swap.To, usageLimitSegment, time.Now().UTC()); err != nil {
		slog.Warn("scheduler: record an interrupted account move", "run", r.ID, "error", err)
		return false
	}
	s.publishAccount(ctx, r.ID, swap.From)
	return true
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
