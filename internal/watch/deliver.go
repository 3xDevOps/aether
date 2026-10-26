package watch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
// longer see the target, turned push off, or the need was resolved; held
// through quiet hours and while they use a dashboard; otherwise sent, the
// ones quiet hours held as one push. A notice that fails to settle does
// not stop the others.
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
			watches[n.WatchID] = w
			sendable = append(sendable, n)
		}
	}
	if len(sendable) == 0 {
		return errors.Join(errs...)
	}
	if until, quiet := quietUntil(prefs, now); quiet {
		for _, n := range sendable {
			if n.PushState == store.WatchPushHeld && n.PushNextAt != nil && n.PushNextAt.Equal(until) {
				continue
			}
			errs = append(errs, s.settle(ctx, watches[n.WatchID], n, store.WatchPushHeld, n.PushAttempts, &until, protocol.WatchStatusHeldQuiet, quietDetail(prefs, until)))
		}
		return errors.Join(errs...)
	}
	if held := s.push.HeldUntil(member); held.After(now) {
		for _, n := range sendable {
			errs = append(errs, s.hold(ctx, watches[n.WatchID], n, held))
		}
		return errors.Join(errs...)
	}
	var quietHeld, single []*store.WatchNotification
	for _, n := range sendable {
		if n.PushState == store.WatchPushHeld {
			quietHeld = append(quietHeld, n)
		} else {
			single = append(single, n)
		}
	}
	if len(quietHeld) == 1 {
		single, quietHeld = append(single, quietHeld[0]), nil
	}
	if len(quietHeld) > 0 {
		lines := make([]string, 0, len(quietHeld))
		for _, n := range quietHeld {
			lines = append(lines, n.Title+": "+n.Body)
		}
		m := push.Message{
			Title: fmt.Sprintf("%d watch notices", len(quietHeld)),
			Body:  strings.Join(lines, "\n"),
			URL:   WatchesLink(),
			Topic: "watches:" + string(member),
		}
		errs = append(errs, s.send(ctx, member, m, quietHeld, watches))
	}
	for _, n := range single {
		m := push.Message{Title: n.Title, Body: n.Body, URL: n.Link, Topic: n.WatchID, Urgent: n.Condition == protocol.WatchNeedsYou}
		if w := watches[n.WatchID]; w.TargetKind == protocol.WatchTargetRun {
			m.Run = w.RunID
		}
		errs = append(errs, s.send(ctx, member, m, []*store.WatchNotification{n}, watches))
	}
	return errors.Join(errs...)
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
var errRunPaused = errors.New("the run is paused")

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
	if w.TargetKind == protocol.WatchTargetRun && s.runs.Paused(w.RunID) {
		return w, "", "", errRunPaused
	}
	current, err := s.currentNeed(ctx, w)
	if err != nil {
		return nil, "", "", err
	}
	if current != n.Key {
		return w, protocol.WatchStatusCleared, "Resolved before the push was sent", nil
	}
	return w, "", "", nil
}

// currentNeed is the key of what w's target waits on its owner for now,
// empty for nothing.
func (s *Service) currentNeed(ctx context.Context, w *store.Watch) (string, error) {
	var n *need
	switch w.TargetKind {
	case protocol.WatchTargetRun:
		run, err := s.store.GetRun(ctx, w.RunID)
		if errors.Is(err, store.ErrNotFound) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		n = s.runNeed(run)
	case protocol.WatchTargetMission:
		m, err := s.store.GetMission(ctx, w.MissionID)
		if errors.Is(err, store.ErrNotFound) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		if n, err = s.missionNeed(ctx, m); err != nil {
			return "", err
		}
	}
	if n == nil {
		return "", nil
	}
	return n.key, nil
}

// send pushes m to member's devices and records the result on each of
// notes: delivered when a device took it, no_destination with none, and
// failed otherwise, tried again with backoff up to maxPushAttempts. The
// attempt is counted and its retry scheduled before the push goes out, so
// a crash before the result is saved repeats it at most that many times.
func (s *Service) send(ctx context.Context, member domain.MemberID, m push.Message, notes []*store.WatchNotification, watches map[string]*store.Watch) error {
	retryAt := make(map[string]time.Time, len(notes))
	for _, n := range notes {
		attempts := n.PushAttempts + 1
		retryAt[n.ID] = s.now().Add(backoff(attempts))
		next := retryAt[n.ID]
		if attempts >= maxPushAttempts {
			next = s.now().Add(maxBackoff)
		}
		if err := s.store.SetWatchPush(ctx, n.ID, n.PushState, attempts, &next, nil); err != nil {
			return err
		}
	}
	results, err := s.push.Deliver(ctx, member, m)
	status, detail := pushOutcome(results, err)
	var errs []error
	for _, n := range notes {
		attempts := n.PushAttempts + 1
		state, next, rowStatus, rowDetail := store.WatchPushNone, (*time.Time)(nil), status, detail
		if status == protocol.WatchStatusFailed {
			if attempts < maxPushAttempts {
				at := retryAt[n.ID]
				state, next = store.WatchPushPending, &at
				if n.PushState == store.WatchPushHeld {
					state = store.WatchPushHeld
				}
				rowStatus = protocol.WatchStatusPending
				rowDetail = fmt.Sprintf("Retry %d of %d at %s: %s", attempts+1, maxPushAttempts, at.UTC().Format("15:04 MST"), detail)
			} else {
				rowDetail += fmt.Sprintf("; gave up after %d attempts", attempts)
			}
		}
		errs = append(errs, s.settle(ctx, watches[n.WatchID], n, state, attempts, next, rowStatus, rowDetail))
	}
	return errors.Join(errs...)
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

func quietDetail(p store.NotifyPrefs, until time.Time) string {
	return "Until " + until.In(location(p)).Format("15:04 MST")
}

func location(p store.NotifyPrefs) *time.Location {
	loc, err := time.LoadLocation(p.QuietTZ)
	if err != nil {
		return time.UTC
	}
	return loc
}

// quietUntil reports whether now falls in p's quiet hours, and when they
// end.
func quietUntil(p store.NotifyPrefs, now time.Time) (time.Time, bool) {
	if p.QuietTZ == "" {
		return time.Time{}, false
	}
	start, okStart := minutes(p.QuietStart)
	end, okEnd := minutes(p.QuietEnd)
	if !okStart || !okEnd || start == end {
		return time.Time{}, false
	}
	local := now.In(location(p))
	m := local.Hour()*60 + local.Minute()
	quiet := start <= m && m < end
	if start > end {
		quiet = m >= start || m < end
	}
	if !quiet {
		return time.Time{}, false
	}
	until := time.Date(local.Year(), local.Month(), local.Day(), end/60, end%60, 0, 0, local.Location())
	if !until.After(local) {
		until = until.AddDate(0, 0, 1)
	}
	return until, true
}

func minutes(hhmm string) (int, bool) {
	t, err := time.Parse("15:04", hhmm)
	if err != nil {
		return 0, false
	}
	return t.Hour()*60 + t.Minute(), true
}
