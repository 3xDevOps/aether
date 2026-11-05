package watch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/push"
	"github.com/3xDevOps/Aether/internal/store"
)

// deliverDue sends every push that has fallen due, member by member.
func (s *Service) deliverDue(ctx context.Context) {
	if s.push == nil {
		return
	}
	due, err := s.store.DueWatchPushes(ctx, s.now())
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("watch: list due pushes", "error", err)
		}
		return
	}
	var order []domain.MemberID
	byMember := map[domain.MemberID][]*store.WatchNotification{}
	for _, n := range due {
		if byMember[n.MemberID] == nil {
			order = append(order, n.MemberID)
		}
		byMember[n.MemberID] = append(byMember[n.MemberID], n)
	}
	for _, member := range order {
		if err := s.deliverMember(ctx, member, byMember[member]); err != nil && ctx.Err() == nil {
			slog.Warn("watch: deliver pushes", "member", member, "error", err)
		}
	}
}

// deliverMember settles member's due pushes: dropped when the owner can no
// longer see the target, turned push off, or the need was resolved; queued
// for their digest by their level or quiet hours; held while they use a
// dashboard; otherwise sent. A notice that fails to settle does not stop
// the others.
func (s *Service) deliverMember(ctx context.Context, member domain.MemberID, notes []*store.WatchNotification) error {
	now := s.now()
	prefs, err := s.store.GetNotifyPrefs(ctx, member)
	if err != nil {
		return err
	}
	var errs []error
	var sendable []*store.WatchNotification
	watches := map[string]*store.Watch{}
	for _, n := range notes {
		if n.PushState == store.WatchPushWithdraw {
			errs = append(errs, s.withdraw(ctx, member, n))
			continue
		}
		w, status, detail, err := s.stillDue(ctx, n, prefs)
		switch {
		case errors.Is(err, store.ErrNotFound), errors.Is(err, errRunPaused):
		case err != nil:
			errs = append(errs, fmt.Errorf("notice %s: %w", n.ID, err))
		case w.MemberID != member, w.State == protocol.WatchPaused:
			// Paused since the notices were listed, or moved by a merge:
			// it stays queued for its resumption or for the survivor.
		case status != "":
			errs = append(errs, s.settle(ctx, w, n, store.WatchPushNone, n.PushAttempts, nil, status, detail))
		default:
			route := push.RouteOf(prefs, push.NoticeKind(n.Condition, n.Body), now)
			if route == push.RouteNow {
				watches[n.WatchID] = w
				sendable = append(sendable, n)
				continue
			}
			errs = append(errs, s.queue(ctx, w, n, prefs, route))
		}
	}
	if len(sendable) == 0 {
		return errors.Join(errs...)
	}
	if held := s.push.HeldUntil(member); held.After(now) {
		for _, n := range sendable {
			errs = append(errs, s.hold(ctx, watches[n.WatchID], n, held))
		}
		return errors.Join(errs...)
	}
	for _, n := range sendable {
		errs = append(errs, s.send(ctx, member, n, watches[n.WatchID]))
	}
	return errors.Join(errs...)
}

// queue hands n's push to its owner's digest.
func (s *Service) queue(ctx context.Context, w *store.Watch, n *store.WatchNotification, prefs store.NotifyPrefs, route push.Route) error {
	workspace := ""
	if w.WorkspaceID != "" {
		ws, err := s.store.GetWorkspace(ctx, w.WorkspaceID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("notice %s: %w", n.ID, err)
		}
		if ws != nil {
			workspace = ws.Name
		}
	}
	item := &store.DigestItem{
		MemberID: w.MemberID, Source: DigestSourceName, Key: n.ID, Kind: push.NoticeKind(n.Condition, n.Body),
		Workspace: workspace, Title: n.Title, Body: n.Body, URL: n.Link, Overnight: route == push.RouteOvernight, CreatedAt: n.CreatedAt,
	}
	if w.TargetKind == protocol.WatchTargetRun {
		item.RunID = w.RunID
	}
	status, detail := protocol.WatchStatusBatched, "In your next digest"
	if route == push.RouteOvernight {
		until, _ := push.QuietUntil(prefs, s.now())
		status, detail = protocol.WatchStatusHeldQuiet, quietDetail(prefs, until)
	}
	d := store.WatchDelivery{Channel: protocol.WatchChannelPush, Status: status, Detail: detail, At: s.now()}
	if err := s.store.QueueWatchDigest(ctx, n.ID, d, item); err != nil {
		return fmt.Errorf("notice %s: %w", n.ID, err)
	}
	s.changed(ctx, w, "")
	return nil
}

func noticeMessage(n *store.WatchNotification, w *store.Watch) push.Message {
	needsYou := n.Condition == protocol.WatchNeedsYou
	m := push.Message{Title: n.Title, Body: n.Body, URL: n.Link, Topic: topicOf(n), Urgent: needsYou, Notice: !needsYou}
	if w.TargetKind == protocol.WatchTargetRun {
		m.Run = w.RunID
	}
	return m
}

// withdraw quietly replaces n's delivered push on member's devices, the
// way the push service clears a run's need that ended, so it no longer
// reads as one. Unlike a notice it is sent through quiet hours and while
// they use a dashboard: it does not alert.
func (s *Service) withdraw(ctx context.Context, member domain.MemberID, n *store.WatchNotification) error {
	w, err := s.store.GetWatch(ctx, n.WatchID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("notice %s: %w", n.ID, err)
	}
	if n.PushAttempts >= maxPushAttempts {
		return s.settle(ctx, w, n, store.WatchPushNone, n.PushAttempts, nil, protocol.WatchStatusFailed,
			fmt.Sprintf("Delivered, then withdrawing it failed after %d attempts; the server stopped before the last result was saved", n.PushAttempts))
	}
	// A newer need shares the notification, so a clear would hide it.
	current, err := s.currentNeeds(ctx, w)
	if err != nil {
		return fmt.Errorf("notice %s: %w", n.ID, err)
	}
	if len(current) > 0 {
		return s.settle(ctx, w, n, store.WatchPushNone, n.PushAttempts, nil, protocol.WatchStatusWithdrawn, "Replaced by a newer need")
	}
	// A summary of later pushes took over this notification; the push
	// service updates it, and a clear would hide the others.
	if s.push.Summarizes(member, topicOf(n)) {
		return s.settle(ctx, w, n, store.WatchPushNone, n.PushAttempts, nil, protocol.WatchStatusWithdrawn, "Replaced by a summary of later pushes")
	}
	attempts := n.PushAttempts + 1
	next := s.now().Add(backoff(attempts))
	if err := s.store.SetWatchPush(ctx, n.ID, store.WatchPushWithdraw, attempts, &next, nil); err != nil {
		return fmt.Errorf("notice %s: %w", n.ID, err)
	}
	m := push.Message{Title: n.Title, URL: n.Link, Topic: topicOf(n), Clear: true}
	if w.TargetKind == protocol.WatchTargetRun {
		m.Run = w.RunID
	}
	status, detail := pushOutcome(s.push.Deliver(ctx, member, m))
	switch {
	case status == protocol.WatchStatusFailed && attempts < maxPushAttempts:
		slog.Warn("watch: withdraw a push; trying again", "notice", n.ID, "at", next, "error", detail)
		return nil
	case status == protocol.WatchStatusFailed:
		detail = fmt.Sprintf("Delivered, then withdrawing it failed after %d attempts: %s", attempts, detail)
	case !slices.Contains(w.Conditions, protocol.WatchNeedsYou):
		status, detail = protocol.WatchStatusWithdrawn, "Withdrawn: "+stoppedNeeds
	default:
		status, detail = protocol.WatchStatusWithdrawn, "Withdrawn: no longer needs you"
	}
	return s.settle(ctx, w, n, store.WatchPushNone, attempts, nil, status, detail)
}

// topicOf is the push topic of n, under which a push service keeps only
// the newest message a device has yet to receive, and the tag of its
// notification there. A watch's needs have one of their own, so
// withdrawing one never displaces its other notices, and the run's own
// clears never replace them.
func topicOf(n *store.WatchNotification) string {
	if n.Condition == protocol.WatchNeedsYou {
		return n.WatchID + "|" + protocol.WatchNeedsYou
	}
	return n.WatchID
}

// hold puts n's push off until held, saying so once.
func (s *Service) hold(ctx context.Context, w *store.Watch, n *store.WatchNotification, held time.Time) error {
	full, err := s.store.GetWatchNotification(ctx, n.ID)
	if err != nil {
		return err
	}
	for _, d := range full.Deliveries {
		if d.Channel == protocol.WatchChannelPush && d.Status == protocol.WatchStatusPending && d.Detail == dashboardHold {
			return s.store.SetWatchPush(ctx, n.ID, n.PushState, n.PushAttempts, &held, nil)
		}
	}
	return s.settle(ctx, w, n, n.PushState, n.PushAttempts, &held, protocol.WatchStatusPending, dashboardHold)
}

// errRunPaused keeps a needs_you push queued while its run is paused: the
// run's need reads as none then, though it is still pending.
var errRunPaused = errors.New("the session is paused")

// stillDue reads n's watch and returns the status that drops n's push,
// empty when it is still to be sent.
func (s *Service) stillDue(ctx context.Context, n *store.WatchNotification, prefs store.NotifyPrefs) (*store.Watch, string, string, error) {
	w, err := s.store.GetWatch(ctx, n.WatchID)
	if err != nil {
		return nil, "", "", err
	}
	if !prefs.Push {
		return w, protocol.WatchStatusDisabled, "Push is off in your notification settings", nil
	}
	if n.PushAttempts >= maxPushAttempts {
		return w, protocol.WatchStatusFailed, fmt.Sprintf("Gave up after %d attempts; the server stopped before the last result was saved", n.PushAttempts), nil
	}
	if err = s.allowed(ctx, w); errors.Is(err, errRevoked) {
		return w, protocol.WatchStatusFailed, err.Error(), nil
	} else if err != nil {
		return nil, "", "", err
	}
	if n.Condition != protocol.WatchNeedsYou {
		return w, "", "", nil
	}
	if !slices.Contains(w.Conditions, protocol.WatchNeedsYou) {
		return w, protocol.WatchStatusCleared, stoppedNeeds, nil
	}
	if w.TargetKind == protocol.WatchTargetRun && s.runs.Paused(w.RunID) {
		return w, "", "", errRunPaused
	}
	current, err := s.currentNeeds(ctx, w)
	if err != nil {
		return nil, "", "", err
	}
	if !slices.ContainsFunc(current, func(c need) bool { return c.key == n.Key }) {
		return w, protocol.WatchStatusCleared, resolvedBeforePush, nil
	}
	return w, "", "", nil
}

// currentNeeds is what w's target waits on its owner for now, as far as
// they still watch for it.
func (s *Service) currentNeeds(ctx context.Context, w *store.Watch) ([]need, error) {
	if !slices.Contains(w.Conditions, protocol.WatchNeedsYou) {
		return nil, nil
	}
	switch w.TargetKind {
	case protocol.WatchTargetRun:
		run, err := s.store.GetRun(ctx, w.RunID)
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return s.runNeeds(run), nil
	}
	return nil, nil
}

// send pushes n to member's devices and records the result: delivered
// when a device took it, no_destination with none, and failed otherwise,
// tried again with backoff up to maxPushAttempts. The attempt is counted
// and its retry scheduled before the push goes out, so a crash before the
// result is saved repeats it at most that many times.
func (s *Service) send(ctx context.Context, member domain.MemberID, n *store.WatchNotification, w *store.Watch) error {
	attempts := n.PushAttempts + 1
	retryAt := s.now().Add(backoff(attempts))
	next := retryAt
	if attempts >= maxPushAttempts {
		next = s.now().Add(maxBackoff)
	}
	// A notice that cannot be reserved, such as one whose watch was just
	// deleted, is not sent.
	if err := s.store.SetWatchPush(ctx, n.ID, n.PushState, attempts, &next, nil); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("notice %s: %w", n.ID, err)
	}
	results, joined, err := s.push.Push(ctx, member, noticeMessage(n, w), n.Condition == protocol.WatchNeedsYou)
	status, detail := pushOutcome(results, err)
	if status == protocol.WatchStatusDelivered && joined {
		detail = store.WatchPushSummarized + ": " + detail
	}
	state, at := store.WatchPushNone, (*time.Time)(nil)
	if status == protocol.WatchStatusFailed {
		if attempts < maxPushAttempts {
			state, at = store.WatchPushPending, &retryAt
			status = protocol.WatchStatusPending
			detail = fmt.Sprintf("Retry %d of %d at %s: %s", attempts+1, maxPushAttempts, retryAt.UTC().Format("15:04 MST"), detail)
		} else {
			detail += fmt.Sprintf("; gave up after %d attempts", attempts)
		}
	}
	errs := []error{s.settle(ctx, w, n, state, attempts, at, status, detail)}
	if status == protocol.WatchStatusDelivered && n.Condition == protocol.WatchNeedsYou {
		errs = append(errs, s.withdrawIfEnded(ctx, n.WatchID))
	}
	return errors.Join(errs...)
}

// withdrawIfEnded withdraws a needs_you push delivered after its need
// ended, or after its owner stopped watching for it, which the evaluation
// or update that saw it could not. The watch is read again: it can have
// changed while the push was sent.
func (s *Service) withdrawIfEnded(ctx context.Context, id string) error {
	w, err := s.store.GetWatch(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("watch %s: %w", id, err)
	}
	current, err := s.currentNeeds(ctx, w)
	if err != nil || len(current) > 0 {
		return err
	}
	return s.withdrawEnded(ctx, w)
}

func pushOutcome(results []push.DeviceResult, err error) (string, string) {
	if err != nil {
		return protocol.WatchStatusFailed, err.Error()
	}
	if len(results) == 0 {
		return protocol.WatchStatusNoDestination, "No device has notifications turned on"
	}
	delivered := 0
	var failures []string
	for _, r := range results {
		if r.Status == push.DeviceDelivered {
			delivered++
		} else if r.Err != nil {
			failures = append(failures, r.Err.Error())
		}
	}
	if delivered == 0 {
		return protocol.WatchStatusFailed, strings.Join(failures, "; ")
	}
	detail := fmt.Sprintf("%d devices", delivered)
	if delivered == 1 {
		detail = "1 device"
	}
	if len(failures) > 0 {
		detail += "; not delivered to the others: " + strings.Join(failures, "; ")
	}
	return protocol.WatchStatusDelivered, detail
}

// settle records n's push state and delivery row, and tells w's owner.
func (s *Service) settle(ctx context.Context, w *store.Watch, n *store.WatchNotification, state string, attempts int, next *time.Time, status, detail string) error {
	d := &store.WatchDelivery{Channel: protocol.WatchChannelPush, Status: status, Detail: detail, At: s.now()}
	if err := s.store.SetWatchPush(ctx, n.ID, state, attempts, next, d); err != nil {
		return fmt.Errorf("notice %s: %w", n.ID, err)
	}
	s.changed(ctx, w, "")
	return nil
}

const dashboardHold = "Waiting until you leave the dashboard"

const stoppedNeeds = "You stopped watching for needs you"

const resolvedBeforePush = "Resolved before the push was sent"

func quietDetail(p store.NotifyPrefs, until time.Time) string {
	return "Until " + until.In(push.Zone(p.QuietTZ)).Format("15:04 MST")
}
