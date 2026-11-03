package watch

import (
	"context"
	"errors"
	"fmt"

	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/push"
	"github.com/3xDevOps/Aether/internal/store"
)

// DigestSourceName names watch notices in a member's digest.
const DigestSourceName = "watch"

// DigestSource settles the watch notices a digest carried, on their push
// delivery rows.
func (s *Service) DigestSource() push.DigestSource { return digestItems{s} }

type digestItems struct{ s *Service }

// Stale is why a queued notice is no longer worth telling: its watch or
// the owner's access went, push was turned off, its watch was paused, or
// the need it announced was resolved. A need of a paused session waits
// for it to resume.
func (d digestItems) Stale(ctx context.Context, item *store.DigestItem) (string, error) {
	n, err := d.s.store.GetWatchNotification(ctx, item.Key)
	if errors.Is(err, store.ErrNotFound) {
		return "The watch was deleted", nil
	}
	if err != nil {
		return "", err
	}
	prefs, err := d.s.store.GetNotifyPrefs(ctx, item.MemberID)
	if err != nil {
		return "", err
	}
	w, status, detail, err := d.s.stillDue(ctx, n, prefs)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return "The watch was deleted", nil
	case errors.Is(err, errRunPaused):
		return "", push.ErrNotYet
	case err != nil:
		return "", err
	case status == protocol.WatchStatusCleared && detail == resolvedBeforePush:
		return "Resolved before the digest was sent", nil
	case status == "" && w.State == protocol.WatchPaused:
		return "You paused the watch", nil
	}
	return detail, nil
}

func (d digestItems) Sent(ctx context.Context, item *store.DigestItem, results []push.DeviceResult, err error, retrying bool) error {
	status, detail := pushOutcome(results, err)
	switch {
	case status == protocol.WatchStatusDelivered:
		detail = store.WatchPushSummarized + ": " + detail
	case retrying:
		status, detail = protocol.WatchStatusPending, "The digest is tried again in a minute: "+detail
	}
	return d.record(ctx, item, status, detail)
}

func (d digestItems) Dropped(ctx context.Context, item *store.DigestItem, reason string) error {
	return d.record(ctx, item, protocol.WatchStatusCleared, reason)
}

func (d digestItems) record(ctx context.Context, item *store.DigestItem, status, detail string) error {
	n, err := d.s.store.GetWatchNotification(ctx, item.Key)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("notice %s: %w", item.Key, err)
	}
	w, err := d.s.store.GetWatch(ctx, n.WatchID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("notice %s: %w", item.Key, err)
	}
	return d.s.settle(ctx, w, n, store.WatchPushNone, n.PushAttempts, nil, status, detail)
}
