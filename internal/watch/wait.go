package watch

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

const (
	// WaitExpiry is how long a run waits on its conditions before it is
	// woken anyway.
	WaitExpiry = 7 * 24 * time.Hour
	// waitRunPoll reads a run target again when no event arrived, so a
	// deleted run is noticed.
	waitRunPoll = 10 * time.Minute
	// registeredRetention keeps the conditions of a report not yet applied
	// for its retry.
	registeredRetention = 24 * time.Hour
)

// RunNotifier writes a server-authored message into a run's inbox and
// wakes the run. A retry with the same key adds nothing and reports it
// written; written is false for a run that ended. Satisfied by
// *coord.Service.
type RunNotifier interface {
	NotifyRun(ctx context.Context, from, to domain.RunID, body, correlation, key string) (written bool, err error)
}

// UseRunNotifier delivers the wakes of runs' waits. Call it before Start.
func (s *Service) UseRunNotifier(n RunNotifier) { s.notifier = n }

// RegisterWait records owner's waiting report with its conditions in one
// transaction. They stay registered, read by nothing, until the report is
// applied; store.MarkCoordStateReportApplied then arms them in place of
// the conditions the run waited on, due at once, so the next pass or a
// restart reads them even when SettleRunWaits does not finish. inserted
// is false for a retry of a recorded report, which registers nothing
// again.
func (s *Service) RegisterWait(ctx context.Context, owner *domain.Run, report *store.CoordStateReport, specs []protocol.WaitSpec) (bool, error) {
	now := s.now()
	waits := make([]*store.RunWait, 0, len(specs))
	for _, spec := range specs {
		w := &store.RunWait{
			Watch: store.Watch{
				MemberID: owner.MemberID, WorkspaceID: owner.WorkspaceID, Repo: spec.Repo, Number: spec.Number,
				TargetTitle: spec.Spec, Conditions: []string{spec.Kind}, Source: protocol.WatchSourceAgent,
				CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(WaitExpiry), NextCheckAt: &now,
			},
			Spec: spec.Spec, Kind: spec.Kind, TargetRun: domain.RunID(spec.RunID), HeadSHA: spec.HeadSHA,
		}
		w.TargetKind = protocol.WatchTargetPR
		if spec.Kind == protocol.WaitKindRunFinished {
			w.TargetKind = protocol.WatchTargetRun
			if err := s.waitTargetVisible(ctx, owner, w.TargetRun); err != nil {
				return false, err
			}
		} else if _, ok := s.pr.(prSnapshots); !ok {
			return false, invalid("until %q: this server does not read pull requests", spec.Spec)
		}
		waits = append(waits, w)
	}
	if report.CreatedAt.IsZero() {
		report.CreatedAt = now
	}
	inserted, err := s.store.RecordWaitingReport(ctx, report, waits)
	if errors.Is(err, store.ErrRunWaitLimit) {
		return false, invalid("until: %s", strings.TrimPrefix(err.Error(), "store: "))
	}
	return inserted, err
}

// SettleBound is how long SettleRunWaits reads conditions before it
// leaves the rest to the background. A wake the background finds within
// SettleBound of the condition's registration, before the report read it,
// is held until then, so the report returns it instead.
const SettleBound = 10 * time.Second

const returnedInReport = "Returned in the report that registered it"

// SettleRunWaits reads run's armed pending conditions as they stand, for
// at most SettleBound, and returns the ones resolved whose wake it takes:
// the report that registered them returns those to the agent, so their
// wake is recorded delivered there and never sent to the inbox. A wake an
// inbox delivery already started on is left to it. A read cut short is
// left due for the background.
func (s *Service) SettleRunWaits(ctx context.Context, run domain.RunID) ([]protocol.WaitCondition, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), SettleBound)
	defer cancel()
	waits, err := s.store.RunWaitConditions(ctx, run)
	if err != nil {
		return nil, err
	}
	for _, w := range waits {
		if w.WaitState != protocol.WaitPending {
			continue
		}
		if err = s.evaluateWait(ctx, w.ID, true); err != nil {
			if ctx.Err() != nil {
				s.wake()
				break
			}
			slog.Warn("watch: read a new wait; the background reads it", "wait", w.ID, "error", err)
		}
	}
	ctx = context.WithoutCancel(ctx)
	if waits, err = s.store.RunWaitConditions(ctx, run); err != nil {
		return nil, err
	}
	var out []protocol.WaitCondition
	for _, w := range waits {
		if !w.Woke() {
			continue
		}
		// A background read can resolve it first: its wake is taken back
		// unless a delivery attempt started, which counts before it writes.
		if w.DeliveryNextAt != nil && w.DeliveryAttempts == 0 {
			now := s.now()
			w.DeliveredAt, w.DeliveryNextAt = &now, nil
			d := store.WatchDelivery{Channel: protocol.WatchChannelRunInbox, Status: protocol.WatchStatusDelivered, Detail: returnedInReport, At: now}
			if err = s.store.SetRunWaitDelivery(ctx, w, d); errors.Is(err, store.ErrConflict) {
				if w, err = s.store.GetRunWait(ctx, w.ID); err != nil {
					return nil, err
				}
			} else if err != nil {
				return nil, err
			}
		}
		if w.DeliveredAt == nil || w.DeliveryAttempts > 0 {
			continue
		}
		p, err := waitProto(w)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

func (s *Service) waitTargetVisible(ctx context.Context, owner *domain.Run, target domain.RunID) error {
	if target == owner.ID {
		return invalid("until run:%s:finished: a run cannot wait for itself", target)
	}
	run, err := s.store.GetRun(ctx, target)
	if errors.Is(err, store.ErrNotFound) {
		return invalid("until run:%s:finished: no such run", target)
	}
	if err != nil {
		return err
	}
	if run.WorkspaceID != owner.WorkspaceID {
		return invalid("until run:%s:finished: no such run in this workspace", target)
	}
	m, err := s.store.GetMember(ctx, owner.MemberID)
	if err != nil {
		return err
	}
	if err := permissions.Check(permissions.View, permissions.Actor{ID: m.ID, Role: m.Role}, permissions.Target{Workspace: run.WorkspaceID}); err != nil {
		return invalid("until run:%s:finished: %v", target, err)
	}
	return nil
}

// waitReading is what one read of a wait's target found. done resolves
// the wait in state with outcome. summary is server-written; detail is
// third-party text. observation keys what it resolved on, so the same
// observation never wakes the run twice. head is the head commit a checks
// condition is bound to after the read.
type waitReading struct {
	done        bool
	state       string
	outcome     string
	summary     string
	detail      string
	observation string
	head        string
	link        string
	observed    protocol.WaitObserved
	next        time.Time
}

// errWaitUnreadable marks a target the wait can never read, which
// resolves it in the error state.
var errWaitUnreadable = errors.New("unreadable")

// evaluateWait reads wait id unless it is not due: a pull request wait in
// backoff or between polls is left to its next check, as events about the
// owner run say nothing about it. A run target is read on its every event. inline marks the read of the report
// that registered it, which returns a resolution to the agent itself and
// reads GitHub afresh.
func (s *Service) evaluateWait(ctx context.Context, id string, inline bool) error {
	for range 4 {
		w, err := s.store.GetRunWait(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if w.WaitState != protocol.WaitPending {
			return nil
		}
		err = s.commitWait(ctx, w, inline)
		if errors.Is(err, store.ErrConflict) {
			continue
		}
		return err
	}
	return fmt.Errorf("wait %s: changed by every concurrent evaluation", id)
}

func (s *Service) commitWait(ctx context.Context, w *store.RunWait, inline bool) error {
	now := s.now()
	owner, err := s.store.GetRun(ctx, w.OwnerRunID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if owner.Status.Terminal() {
		return s.endWaits(ctx, w.OwnerRunID, "", "the run ended "+string(owner.Status), "")
	}
	expired := !w.ExpiresAt.After(now)
	notDue := w.NextCheckAt != nil && now.Before(*w.NextCheckAt)
	if !inline && !expired && notDue && w.Kind != protocol.WaitKindRunFinished {
		return nil
	}
	if err = s.allowed(ctx, &w.Watch); errors.Is(err, errRevoked) {
		return s.endWaits(ctx, w.OwnerRunID, "", "the run's member lost access: "+err.Error(), "")
	} else if err != nil {
		return err
	}
	before := *w
	w.LastCheckAt, w.UpdatedAt = &now, now
	var r waitReading
	var readErr error
	if expired {
		r = waitReading{done: true, state: protocol.WaitExpired, outcome: "expired", observation: "expired",
			summary: fmt.Sprintf("nothing resolved within %s", WaitExpiry)}
	} else {
		r, readErr = s.readWait(ctx, w, now, inline || w.SourceCheckedAt == nil)
	}
	if readErr != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if readErr != nil && !errors.Is(readErr, errWaitUnreadable) {
		w.Failures++
		w.Error, w.ErrorAt, w.State = readErr.Error(), &now, protocol.WatchError
		next := now.Add(backoff(w.Failures))
		var hint interface{ RetryAt() time.Time }
		if errors.As(readErr, &hint) && hint.RetryAt().After(next) {
			next = hint.RetryAt()
		}
		next = minTime(next, w.ExpiresAt)
		w.NextCheckAt = &next
		_, err = s.store.CommitRunWait(ctx, w, nil)
		if err == nil && (before.Error != w.Error || before.State != w.State) {
			s.waitChanged(ctx, w)
		}
		return err
	}
	if readErr != nil {
		r = waitReading{done: true, state: protocol.WaitError, outcome: "unreadable", observation: "unreadable",
			summary: "Aether cannot read the target", detail: strings.TrimPrefix(readErr.Error(), errWaitUnreadable.Error()+": "),
			observed: r.observed, link: r.link}
		w.Error, w.ErrorAt = readErr.Error(), &now
	} else {
		w.SourceCheckedAt = &now
		w.Error = ""
	}
	observed, err := json.Marshal(r.observed)
	if err != nil {
		return err
	}
	w.Observed, w.Summary, w.Detail, w.Observation, w.Failures = observed, r.summary, r.detail, r.observation, 0
	if r.head != "" {
		w.HeadSHA = r.head
	}
	var n *store.WatchNotification
	if r.done {
		w.WaitState, w.Outcome, w.ResolvedAt = r.state, r.outcome, &now
		w.State, w.EndedAt, w.NextCheckAt = protocol.WatchDone, &now, nil
		if r.state == protocol.WaitExpired {
			w.State = protocol.WatchExpired
		}
		d := store.WatchDelivery{Channel: protocol.WatchChannelRunInbox, Status: protocol.WatchStatusPending, Detail: "Waking the run", At: now}
		switch {
		case inline:
			w.DeliveredAt = &now
			d.Status, d.Detail = protocol.WatchStatusDelivered, returnedInReport
		case before.LastCheckAt == nil && now.Before(w.CreatedAt.Add(SettleBound)):
			held := w.CreatedAt.Add(SettleBound)
			w.DeliveryNextAt = &held
		default:
			w.DeliveryNextAt = &now
		}
		n = &store.WatchNotification{
			Condition: w.Kind, Key: w.Outcome, Title: w.Spec, Body: waitBody(w, r.link), Link: cmp.Or(r.link, RunLink(w.OwnerRunID)),
			CreatedAt: now, PushState: store.WatchPushNone, Deliveries: []store.WatchDelivery{d},
		}
	} else {
		w.State = protocol.WatchActive
		next := minTime(r.next, w.ExpiresAt)
		w.NextCheckAt = &next
	}
	ended, err := s.store.CommitRunWait(ctx, w, n)
	if err != nil {
		return err
	}
	if w.WaitState != before.WaitState || w.Summary != before.Summary || string(w.Observed) != string(before.Observed) || before.Error != w.Error {
		s.waitChanged(ctx, w)
	}
	for _, e := range ended {
		s.waitChanged(ctx, e)
	}
	if w.DeliveryNextAt != nil {
		s.wake()
	}
	return nil
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

func (s *Service) readWait(ctx context.Context, w *store.RunWait, now time.Time, fresh bool) (waitReading, error) {
	if w.Kind == protocol.WaitKindRunFinished {
		return s.readWaitRun(ctx, w, now)
	}
	return s.readWaitPR(ctx, w, now, fresh)
}

func (s *Service) readWaitRun(ctx context.Context, w *store.RunWait, now time.Time) (waitReading, error) {
	r := waitReading{link: RunLink(w.TargetRun), next: now.Add(jitter(waitRunPoll))}
	run, err := s.store.GetRun(ctx, w.TargetRun)
	if errors.Is(err, store.ErrNotFound) {
		return r, fmt.Errorf("%w: run %s no longer exists", errWaitUnreadable, w.TargetRun)
	}
	if err != nil {
		return r, fmt.Errorf("read run %s: %w", w.TargetRun, err)
	}
	r.observed.RunStatus = string(run.Status)
	outcome, ended := runEnd(run)
	if !ended {
		r.summary = "run " + string(run.ID) + " is " + string(run.Status)
		if run.Activity != "" {
			r.summary += ", " + string(run.Activity)
		}
		return r, nil
	}
	r.done, r.state, r.outcome = true, protocol.WaitResolved, outcome
	r.summary = "run " + string(run.ID) + " ended " + outcome
	r.detail = run.Reason
	r.observation = outcome
	if run.StatusChangedAt != nil {
		r.observation += "@" + strconv.FormatInt(run.StatusChangedAt.UnixNano(), 10)
	}
	return r, nil
}

// runEnd is how a run ended, in the outcomes run:<id>:finished resolves
// with; ended is false while it has not.
func runEnd(run *domain.Run) (outcome string, ended bool) {
	switch run.Status {
	case domain.RunMerged, domain.RunAbandoned, domain.RunFailed, domain.RunInterrupted:
		return string(run.Status), true
	case domain.RunCompleted:
		if run.Reason == domain.ReportedSuccessReason {
			return "reported-success", true
		}
		return "completed", true
	}
	if run.Status.Live() && run.Activity == domain.ActivityFinished {
		return "reported-success", true
	}
	return "", false
}

// endWaits cancels run's pending waits, or only wait, and announces them.
func (s *Service) endWaits(ctx context.Context, run domain.RunID, wait, cause string, by domain.MemberID) error {
	ended, err := s.store.EndRunWaits(ctx, run, wait, protocol.WaitCancelled, cause, by, s.now())
	if err != nil {
		return err
	}
	for _, w := range ended {
		s.waitChanged(ctx, w)
	}
	return nil
}

// EndRunWaits cancels run's pending wait conditions and their undelivered
// wakes, for cause: another report, a person's prompt or a relaunch.
func (s *Service) EndRunWaits(ctx context.Context, run domain.RunID, cause string) error {
	return s.endWaits(ctx, run, "", cause, "")
}

// CancelRunWait cancels one of run's wait conditions for actor. The caller
// checked actor may control the run.
func (s *Service) CancelRunWait(ctx context.Context, run domain.RunID, wait string, actor domain.MemberID) (protocol.WaitCondition, error) {
	if wait == "" {
		return protocol.WaitCondition{}, invalid("wait_id is required")
	}
	w, err := s.store.GetRunWait(ctx, wait)
	if err != nil {
		return protocol.WaitCondition{}, err
	}
	if w.OwnerRunID != run {
		return protocol.WaitCondition{}, fmt.Errorf("store: wait %s: %w", wait, store.ErrNotFound)
	}
	if err = s.endWaits(ctx, run, wait, protocol.WaitCancelled, actor); err != nil {
		return protocol.WaitCondition{}, err
	}
	if w, err = s.store.GetRunWait(ctx, wait); err != nil {
		return protocol.WaitCondition{}, err
	}
	return waitProto(w)
}

// RunWaits is run's latest revision of wait conditions, live or ended.
func (s *Service) RunWaits(ctx context.Context, run domain.RunID) ([]protocol.WaitCondition, error) {
	byRun, err := s.RunWaitsOf(ctx, []domain.RunID{run})
	return byRun[run], err
}

// RunWaitsOf is RunWaits of each of runs, in one read.
func (s *Service) RunWaitsOf(ctx context.Context, runs []domain.RunID) (map[domain.RunID][]protocol.WaitCondition, error) {
	byRun, err := s.store.RunWaitConditionsOf(ctx, runs)
	if err != nil {
		return nil, err
	}
	out := make(map[domain.RunID][]protocol.WaitCondition, len(byRun))
	for run, waits := range byRun {
		for _, w := range waits {
			p, err := waitProto(w)
			if err != nil {
				return nil, err
			}
			out[run] = append(out[run], p)
		}
	}
	return out, nil
}

func waitProto(w *store.RunWait) (protocol.WaitCondition, error) {
	out := protocol.WaitCondition{
		ID: w.ID, Revision: w.Revision, Spec: w.Spec, Kind: w.Kind, Repo: w.Repo, Number: w.Number, RunID: string(w.TargetRun),
		State: w.WaitState, Outcome: w.Outcome, Summary: w.Summary, Detail: w.Detail, HeadSHA: w.HeadSHA, CreatedAt: stamp(&w.CreatedAt),
		LastCheckedAt: stamp(w.LastCheckAt), SourceCheckedAt: stamp(w.SourceCheckedAt), NextCheckAt: stamp(w.NextCheckAt),
		ConsecutiveFailures: w.Failures, LastError: w.Error, LastErrorAt: stamp(w.ErrorAt), ResolvedAt: stamp(w.ResolvedAt),
		DeliveredAt: stamp(w.DeliveredAt), DeliveryAttempts: w.DeliveryAttempts, LastDeliveryError: w.DeliveryError,
		ExpiresAt: stamp(&w.ExpiresAt), EndedCause: w.Cause, EndedByMemberID: string(w.EndedBy),
	}
	if err := json.Unmarshal(w.Observed, &out.Observed); err != nil {
		return protocol.WaitCondition{}, fmt.Errorf("wait %s observed: %w", w.ID, err)
	}
	return out, nil
}

// deliverWaits writes each due wake into its run's inbox. A run that ended
// is not woken; a paused one gets the message, which its resumption wakes
// it for.
func (s *Service) deliverWaits(ctx context.Context) {
	now := s.now()
	due, err := s.store.DueRunWaitDeliveries(ctx, now)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("watch: list due wait wakes", "error", err)
		}
		return
	}
	for _, w := range due {
		if err := s.deliverWait(ctx, w, now); err != nil && ctx.Err() == nil {
			slog.Warn("watch: record a wait wake", "wait", w.ID, "error", err)
		}
	}
}

func (s *Service) deliverWait(ctx context.Context, w *store.RunWait, now time.Time) error {
	owner, err := s.store.GetRun(ctx, w.OwnerRunID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	d := store.WatchDelivery{Channel: protocol.WatchChannelRunInbox, At: now}
	w.DeliveryAttempts++
	switch {
	case owner.Status.Terminal():
		return s.settleWait(ctx, w, d, false, nil)
	case s.notifier == nil:
		return s.settleWait(ctx, w, d, false, errors.New("this server has no run inboxes"))
	}
	// Claimed before the write: an end or a relaunch that commits first
	// makes the claim fail, and the run is not woken.
	lease := now.Add(backoff(w.DeliveryAttempts))
	w.DeliveryNextAt = &lease
	d.Status, d.Detail = protocol.WatchStatusPending, "Writing to the run's inbox"
	if err = s.store.SetRunWaitDelivery(ctx, w, d); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nil
		}
		return err
	}
	var link string
	notes, err := s.store.ListWatchNotifications(ctx, w.ID, 1)
	if err != nil {
		return err
	}
	if len(notes) > 0 {
		link = notes[0].Link
	}
	key := "wait:" + w.ID + ":" + w.Outcome
	written, err := s.notifier.NotifyRun(ctx, w.OwnerRunID, w.OwnerRunID, waitBody(w, link), w.ID, key)
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return s.settleWait(context.WithoutCancel(ctx), w, store.WatchDelivery{Channel: protocol.WatchChannelRunInbox, At: s.now()}, written, err)
}

// settleWait records how a claimed wake ended: written to the inbox, not
// written because the run ended, or failed and due again.
func (s *Service) settleWait(ctx context.Context, w *store.RunWait, d store.WatchDelivery, written bool, err error) error {
	now := d.At
	switch {
	case written:
		w.DeliveredAt, w.DeliveryNextAt, w.DeliveryError = &now, nil, ""
		d.Status, d.Detail = protocol.WatchStatusDelivered, "Written to the run's inbox"
		if s.runs.Paused(w.OwnerRunID) {
			d.Detail += "; the run is paused and is woken when it resumes"
		}
	case err == nil:
		w.DeliveryNextAt, w.DeliveryError = nil, "Not delivered: the run ended"
		d.Status, d.Detail = protocol.WatchStatusCleared, w.DeliveryError
	default:
		next := now.Add(backoff(w.DeliveryAttempts))
		w.DeliveryNextAt, w.DeliveryError = &next, err.Error()
		d.Status, d.Detail = protocol.WatchStatusPending, "Retrying: "+err.Error()
	}
	if err := s.store.SetRunWaitDelivery(ctx, w, d); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nil
		}
		return err
	}
	s.waitChanged(ctx, w)
	return nil
}

// waitChanged tells readers of w's run to read its waits again.
func (s *Service) waitChanged(ctx context.Context, w *store.RunWait) {
	_, err := s.bus.Publish(ctx, events.Event{
		WorkspaceID: w.WorkspaceID, RunID: w.OwnerRunID,
		Payload: events.RunWaitChangedPayload{RunID: w.OwnerRunID, WorkspaceID: w.WorkspaceID, WaitID: w.ID, State: w.WaitState},
	})
	if err != nil && ctx.Err() == nil {
		slog.Warn("watch: publish run.wait.changed", "wait", w.ID, "error", err)
	}
}
