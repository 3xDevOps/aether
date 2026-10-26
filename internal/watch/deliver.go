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
// ones quiet hours held as one push.
func (s *Service) deliverMember(ctx context.Context, member domain.MemberID, notes []*store.WatchNotification) error {
	now := s.now()
	prefs, err := s.store.GetNotifyPrefs(ctx, member)
	if err != nil {
		return err
	}
	var sendable []*store.WatchNotification
	for _, n := range notes {
		status, detail, err := s.stillDue(ctx, n, prefs)
		if err != nil {
			return err
		}
		if status != "" {
			if err := s.settle(ctx, n, store.WatchPushNone, n.PushAttempts, nil, status, detail); err != nil {
				return err
			}
			continue
		}
		sendable = append(sendable, n)
	}
	if len(sendable) == 0 {
		return nil
	}
	if until, quiet := quietUntil(prefs, now); quiet {
		for _, n := range sendable {
			if n.PushState == store.WatchPushHeld && n.PushNextAt != nil && n.PushNextAt.Equal(until) {
				continue
			}
			detail := "Until " + until.In(location(prefs)).Format("15:04 MST")
			if err := s.settle(ctx, n, store.WatchPushHeld, n.PushAttempts, &until, protocol.WatchStatusHeldQuiet, detail); err != nil {
				return err
			}
		}
		return nil
	}
	if held := s.push.HeldUntil(member); held.After(now) {
		for _, n := range sendable {
			if err := s.store.SetWatchPush(ctx, n.ID, n.PushState, n.PushAttempts, &held, nil); err != nil {
				return err
			}
		}
		return nil
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
		m := push.Message{
			Title: fmt.Sprintf("%d watch notices", len(quietHeld)),
			Body:  summarize(quietHeld),
			URL:   WatchesLink(),
			Topic: "watches:" + string(member),
		}
		if err := s.send(ctx, member, m, quietHeld); err != nil {
			return err
		}
	}
	for _, n := range single {
		m := push.Message{Title: n.Title, Body: n.Body, URL: n.Link, Topic: n.WatchID, Urgent: n.Condition == protocol.WatchNeedsYou}
		if w, err := s.store.GetWatch(ctx, n.WatchID); err == nil && w.TargetKind == protocol.WatchTargetRun {
			m.Run = w.RunID
		}
		if err := s.send(ctx, member, m, []*store.WatchNotification{n}); err != nil {
			return err
		}
	}
	return nil
}

func summarize(notes []*store.WatchNotification) string {
	lines := make([]string, 0, len(notes))
	for _, n := range notes {
		lines = append(lines, n.Title+": "+n.Body)
	}
	return strings.Join(lines, "\n")
}

// stillDue returns the status that drops n's push, empty when it is still
// to be sent.
func (s *Service) stillDue(ctx context.Context, n *store.WatchNotification, prefs store.NotifyPrefs) (string, string, error) {
	if !prefs.Push {
		return protocol.WatchStatusDisabled, "Push is off in your notification settings", nil
	}
	w, err := s.store.GetWatch(ctx, n.WatchID)
	if err != nil {
		return "", "", err
	}
	if err = s.allowed(ctx, w); errors.Is(err, errRevoked) {
		return protocol.WatchStatusFailed, err.Error(), nil
	} else if err != nil {
		return "", "", err
	}
	if n.Condition != protocol.WatchNeedsYou {
		return "", "", nil
	}
	current, err := s.currentNeed(ctx, w)
	if err != nil {
		return "", "", err
	}
	if current != n.Key {
		return protocol.WatchStatusCleared, "Resolved before the push was sent", nil
	}
	return "", "", nil
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
// failed otherwise, tried again with backoff up to maxPushAttempts.
func (s *Service) send(ctx context.Context, member domain.MemberID, m push.Message, notes []*store.WatchNotification) error {
	results, err := s.push.Deliver(ctx, member, m)
	status, detail := pushOutcome(results, err)
	for _, n := range notes {
		attempts := n.PushAttempts + 1
		state, next := store.WatchPushNone, (*time.Time)(nil)
		if status == protocol.WatchStatusFailed {
			if attempts < maxPushAttempts {
				at := s.now().Add(backoff(attempts))
				state, next = store.WatchPushPending, &at
			} else {
				detail += fmt.Sprintf("; gave up after %d attempts", attempts)
			}
		}
		if err := s.settle(ctx, n, state, attempts, next, status, detail); err != nil {
			return err
		}
	}
	return nil
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
	switch delivered {
	case 0:
		return protocol.WatchStatusFailed, strings.Join(failures, "; ")
	case 1:
		return protocol.WatchStatusDelivered, "1 device"
	}
	return protocol.WatchStatusDelivered, fmt.Sprintf("%d devices", delivered)
}

func (s *Service) settle(ctx context.Context, n *store.WatchNotification, state string, attempts int, next *time.Time, status, detail string) error {
	d := &store.WatchDelivery{Channel: protocol.WatchChannelPush, Status: status, Detail: detail, At: s.now()}
	if err := s.store.SetWatchPush(ctx, n.ID, state, attempts, next, d); err != nil {
		return err
	}
	if w, err := s.store.GetWatch(ctx, n.WatchID); err == nil {
		s.changed(ctx, w, "")
	}
	return nil
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
