package watch

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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
)

// RunNotifier writes a server-authored message into a run's inbox and
// wakes the run. A retry with the same key adds nothing. Satisfied by
// *coord.Service.
type RunNotifier interface {
	Notify(ctx context.Context, from, to domain.RunID, body, correlation, key string) error
}

// UseRunNotifier delivers the wakes of runs' waits. Call it before Start.
func (s *Service) UseRunNotifier(n RunNotifier) { s.notifier = n }

// RegisterWait records owner's waiting report with its conditions in one
// transaction, replacing the conditions it waited on. Each is due for a
// check at once, so the next pass or a restart reads it even when
// EvaluateRunWaits is never called. inserted is false for a retry of a
// recorded report, which registers nothing again.
func (s *Service) RegisterWait(ctx context.Context, owner *domain.Run, report *store.CoordStateReport, specs []protocol.WaitSpec) (bool, error) {
	now := s.now()
	waits := make([]*store.RunWait, 0, len(specs))
	for _, spec := range specs {
		w := &store.RunWait{
			Watch: store.Watch{
				MemberID: owner.MemberID, WorkspaceID: owner.WorkspaceID, Repo: spec.Repo, Number: spec.Number,
				TargetTitle: spec.Spec, Conditions: []string{spec.Kind}, Source: protocol.WatchSourceAgent, State: protocol.WatchActive,
				CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(WaitExpiry), NextCheckAt: &now,
			},
			Spec: spec.Spec, Kind: spec.Kind, TargetRun: domain.RunID(spec.RunID),
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
	inserted, ended, err := s.store.RecordWaitingReport(ctx, report, waits)
	if err != nil || !inserted {
		return inserted, err
	}
	ctx = context.WithoutCancel(ctx)
	for _, w := range ended {
		s.waitChanged(ctx, w)
	}
	for _, w := range waits {
		s.waitChanged(ctx, w)
	}
	return true, nil
}

// EvaluateRunWaits reads run's pending conditions as they stand, in the
// background: one already true wakes the run at once.
func (s *Service) EvaluateRunWaits(run domain.RunID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.loop == nil {
		return
	}
	ctx := s.loop
	s.wg.Go(func() {
		waits, err := s.store.RunWaitConditions(ctx, run)
		if err != nil {
			if ctx.Err() == nil {
				slog.Warn("watch: list a run's new waits", "run", run, "error", err)
			}
			return
		}
		for _, w := range waits {
			if w.WaitState != protocol.WaitPending {
				continue
			}
			if err := s.evaluate(ctx, w.ID); err != nil && ctx.Err() == nil {
				slog.Warn("watch: evaluate a new wait", "wait", w.ID, "error", err)
			}
		}
	})
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
// the wait in state with outcome.
type waitReading struct {
	done     bool
	state    string
	outcome  string
	summary  string
	link     string
	observed protocol.WaitObserved
	next     time.Time
}

// errWaitUnreadable marks a target the wait can never read, which
// resolves it in the error state.
var errWaitUnreadable = errors.New("unreadable")

func (s *Service) evaluateWait(ctx context.Context, id string) error {
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
		err = s.commitWait(ctx, w)
		if errors.Is(err, store.ErrConflict) {
			continue
		}
		return err
	}
	return fmt.Errorf("wait %s: changed by every concurrent evaluation", id)
}

func (s *Service) commitWait(ctx context.Context, w *store.RunWait) error {
	now := s.now()
	owner, err := s.store.GetRun(ctx, w.OwnerRunID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if owner.Status.Terminal() {
		return s.endWaits(ctx, w.OwnerRunID, "", protocol.WaitCancelled, "the run ended "+string(owner.Status))
	}
	if err := s.allowed(ctx, &w.Watch); errors.Is(err, errRevoked) {
		return s.endWaits(ctx, w.OwnerRunID, "", protocol.WaitCancelled, "the run's member lost access: "+err.Error())
	} else if err != nil {
		return err
	}
	before := *w
	w.LastCheckAt, w.UpdatedAt = &now, now
	var r waitReading
	var readErr error
	if !w.ExpiresAt.After(now) {
		r = waitReading{done: true, state: protocol.WaitExpired, outcome: "expired",
			summary: fmt.Sprintf("nothing resolved within %s", WaitExpiry)}
	} else {
		r, readErr = s.readWait(ctx, w, now)
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
		_, err := s.store.CommitRunWait(ctx, w, nil)
		if err == nil && (before.Error != w.Error || before.State != w.State) {
			s.waitChanged(ctx, w)
		}
		return err
	}
	if readErr != nil {
		r = waitReading{done: true, state: protocol.WaitError, outcome: "unreadable", summary: readErr.Error(), observed: r.observed, link: r.link}
		w.Error, w.ErrorAt = readErr.Error(), &now
	} else {
		w.SourceCheckedAt = &now
		w.Error = ""
	}
	observed, err := json.Marshal(r.observed)
	if err != nil {
		return err
	}
	w.Observed, w.Summary, w.Failures = observed, r.summary, 0
	var n *store.WatchNotification
	if r.done {
		w.WaitState, w.Outcome, w.ResolvedAt = r.state, r.outcome, &now
		w.State, w.EndedAt, w.NextCheckAt, w.DeliveryNextAt = protocol.WatchDone, &now, nil, &now
		if r.state == protocol.WaitExpired {
			w.State = protocol.WatchExpired
		}
		n = &store.WatchNotification{
			Condition: w.Kind, Key: w.Outcome, Title: w.Spec, Body: waitBody(w, r.link), Link: cmp.Or(r.link, RunLink(w.OwnerRunID)),
			CreatedAt: now, PushState: store.WatchPushNone,
			Deliveries: []store.WatchDelivery{{Channel: protocol.WatchChannelRunInbox, Status: protocol.WatchStatusPending, Detail: "Waking the run", At: now}},
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

func (s *Service) readWait(ctx context.Context, w *store.RunWait, now time.Time) (waitReading, error) {
	if w.Kind == protocol.WaitKindRunFinished {
		return s.readWaitRun(ctx, w, now)
	}
	return s.readWaitPR(ctx, w, now)
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
	if reason := run.Reason; reason != "" {
		r.summary += ": " + clip(reason, 200)
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

// endWaits ends run's pending waits, or only wait, and announces them.
func (s *Service) endWaits(ctx context.Context, run domain.RunID, wait, state, cause string) error {
	ended, err := s.store.EndRunWaits(ctx, run, wait, state, cause, s.now())
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
	return s.endWaits(ctx, run, "", protocol.WaitCancelled, cause)
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
	if err := s.endWaits(ctx, run, wait, protocol.WaitCancelled, "cancelled by "+string(actor)); err != nil {
		return protocol.WaitCondition{}, err
	}
	if w, err = s.store.GetRunWait(ctx, wait); err != nil {
		return protocol.WaitCondition{}, err
	}
	return waitProto(w)
}

// RunWaits is run's latest revision of wait conditions, live or ended.
func (s *Service) RunWaits(ctx context.Context, run domain.RunID) ([]protocol.WaitCondition, error) {
	waits, err := s.store.RunWaitConditions(ctx, run)
	if err != nil {
		return nil, err
	}
	out := make([]protocol.WaitCondition, 0, len(waits))
	for _, w := range waits {
		p, err := waitProto(w)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

func waitProto(w *store.RunWait) (protocol.WaitCondition, error) {
	out := protocol.WaitCondition{
		ID: w.ID, Revision: w.Revision, Spec: w.Spec, Kind: w.Kind, Repo: w.Repo, Number: w.Number, RunID: string(w.TargetRun),
		State: w.WaitState, Outcome: w.Outcome, Summary: w.Summary, CreatedAt: stamp(&w.CreatedAt),
		LastCheckedAt: stamp(w.LastCheckAt), SourceCheckedAt: stamp(w.SourceCheckedAt), NextCheckAt: stamp(w.NextCheckAt),
		ConsecutiveFailures: w.Failures, LastError: w.Error, LastErrorAt: stamp(w.ErrorAt), ResolvedAt: stamp(w.ResolvedAt),
		DeliveredAt: stamp(w.DeliveredAt), DeliveryAttempts: w.DeliveryAttempts, LastDeliveryError: w.DeliveryError,
		ExpiresAt: stamp(&w.ExpiresAt), EndedCause: w.Cause,
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
		w.DeliveryNextAt, w.DeliveryError = nil, "Not delivered: the run ended "+string(owner.Status)
		d.Status, d.Detail = protocol.WatchStatusCleared, w.DeliveryError
	case s.notifier == nil:
		err = errors.New("this server has no run inboxes")
	default:
		var link string
		var n []*store.WatchNotification
		if n, err = s.store.ListWatchNotifications(ctx, w.ID, 1); err == nil && len(n) > 0 {
			link = n[0].Link
		}
		if err == nil {
			err = s.notifier.Notify(ctx, w.OwnerRunID, w.OwnerRunID, waitBody(w, link), w.ID, w.ID+":"+w.Kind+":"+w.Outcome)
		}
		if err == nil {
			w.DeliveredAt, w.DeliveryNextAt, w.DeliveryError = &now, nil, ""
			d.Status, d.Detail = protocol.WatchStatusDelivered, "Written to the run's inbox"
			if s.runs.Paused(w.OwnerRunID) {
				d.Detail += "; the run is paused and is woken when it resumes"
			}
		}
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
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
