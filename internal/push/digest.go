package push

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/store"
)

const (
	digestLines      = 5
	digestTitleRunes = 40
	// digestBodyRunes keeps five lines inside one push message.
	digestBodyRunes = 100
	// digestTick is how often the service looks for a digest that fell due.
	digestTick = 30 * time.Second
	// digestAttempts bounds how often a digest no device took is tried.
	digestAttempts = 5
	// runSource names the items of a session's own notification.
	runSource = "run"
)

var kinds = []struct{ kind, label, action string }{
	{KindNeed, "need you", "Answer"},
	{KindFailed, "failed", "Open"},
	{KindDone, "done", "Review"},
	{KindMerged, "merged", "Open"},
	{KindChecks, "checks", "Open"},
	{KindOther, "other", "Open"},
}

// ErrNotYet is a DigestSource's answer for an item that stays queued
// without going out in this digest, such as a need of a paused session.
var ErrNotYet = errors.New("push: not yet")

// DigestSource is what queues items of one source. Stale says why an item
// is no longer worth telling, empty while it is, or returns ErrNotYet.
// Sent records the digest push an item went out in, and Dropped one
// dropped unsent with reason.
type DigestSource interface {
	Stale(ctx context.Context, item *store.DigestItem) (string, error)
	Sent(ctx context.Context, item *store.DigestItem, results []DeviceResult, err error) error
	Dropped(ctx context.Context, item *store.DigestItem, reason string) error
}

// UseDigestSource has source settle the items queued under name. Call it
// before Start.
func (s *Service) UseDigestSource(name string, source DigestSource) {
	s.sources[name] = source
}

// Queue adds item to its member's next digest.
func (s *Service) Queue(ctx context.Context, item *store.DigestItem) error {
	return s.store.AddDigestItem(ctx, item)
}

// digestText is a digest's title and body: counts of each kind, then at
// most digestLines items, those quiet hours held first and marked
// Overnight:, each group what needs the member first, and a count of the
// rest.
func digestText(items []*store.DigestItem) (string, string) {
	rank := map[string]int{}
	counts := map[string]int{}
	for i, k := range kinds {
		rank[k.kind] = i
	}
	for _, item := range items {
		counts[item.Kind]++
	}
	title := "Aether"
	for _, k := range kinds {
		if counts[k.kind] > 0 {
			title += fmt.Sprintf(" · %d %s", counts[k.kind], k.label)
		}
	}
	ordered := slices.Clone(items)
	slices.SortStableFunc(ordered, func(a, b *store.DigestItem) int {
		if a.Overnight != b.Overnight {
			if a.Overnight {
				return -1
			}
			return 1
		}
		return rank[a.Kind] - rank[b.Kind]
	})
	shown := ordered[:min(len(ordered), digestLines)]
	var lines []string
	for _, item := range shown {
		lines = append(lines, digestLine(item, kinds[rank[item.Kind]].action))
	}
	rest := map[string]int{}
	for _, item := range ordered[len(shown):] {
		rest[item.Kind]++
	}
	var more []string
	for _, k := range kinds {
		if rest[k.kind] > 0 {
			more = append(more, fmt.Sprintf("+%d %s", rest[k.kind], k.label))
		}
	}
	if len(more) > 0 {
		lines = append(lines, strings.Join(more, " · "))
	}
	return title, strings.Join(lines, "\n")
}

func digestLine(item *store.DigestItem, action string) string {
	line := clip(plain(item.Title), digestTitleRunes)
	if body := plain(item.Body); body != "" {
		line += ": " + clip(body, digestBodyRunes)
	}
	if item.Workspace != "" {
		line = plain(item.Workspace) + "  " + line
	}
	if item.Overnight {
		line = "Overnight: " + line
	}
	return line + "  [" + action + "]"
}

// plain is text an agent may have written, on one line.
func plain(s string) string {
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
}

func (s *Service) digests(ctx context.Context) {
	tick := time.NewTicker(digestTick)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			s.sendDigests(ctx)
		}
	}
}

// sendDigests sends every digest that fell due.
func (s *Service) sendDigests(ctx context.Context) {
	members, err := s.store.DigestMembers(ctx)
	if err != nil {
		slog.Warn("push: list members with a digest", "error", err)
		return
	}
	for _, member := range members {
		if err := s.sendDigest(ctx, member); err != nil && ctx.Err() == nil {
			slog.Warn("push: send a digest; trying again later", "member", member, "error", err)
		}
	}
}

// sendDigest sends member's digest once it is due and they have left every
// dashboard. Items no longer worth telling are dropped first; a digest
// left empty is not sent. One no device took is tried again after retry,
// up to digestAttempts times.
func (s *Service) sendDigest(ctx context.Context, member domain.MemberID) error {
	now := s.now()
	s.mu.Lock()
	retrying := s.digestRetry[member].After(now)
	s.mu.Unlock()
	if retrying || s.freeAt(member).After(now) {
		return nil
	}
	prefs, err := s.store.GetNotifyPrefs(ctx, member)
	if err != nil {
		return err
	}
	items, err := s.store.ListDigestItems(ctx, member)
	if err != nil || len(items) == 0 {
		return err
	}
	if digestDue(prefs, items, now).After(now) {
		return nil
	}
	var current, stale []*store.DigestItem
	var errs []error
	for _, item := range items {
		reason, staleErr := s.source(item).Stale(ctx, item)
		switch {
		case errors.Is(staleErr, ErrNotYet):
		case staleErr != nil:
			errs = append(errs, staleErr)
		case reason != "":
			errs = append(errs, s.source(item).Dropped(ctx, item, reason))
			stale = append(stale, item)
		default:
			current = append(current, item)
		}
	}
	if err = s.store.DeleteDigestItems(ctx, member, stale); err != nil {
		return errors.Join(append(errs, err)...)
	}
	if err = errors.Join(errs...); err != nil || len(current) == 0 {
		return err
	}
	title, body := digestText(current)
	payload, err := json.Marshal(message{
		notification: notification{Title: title, Body: body, Notice: true},
		URL:          "/", Tag: "digest",
	})
	if err != nil {
		return fmt.Errorf("push: encode a digest: %w", err)
	}
	results, err := s.deliverPayload(ctx, member, payload, topicOf("digest:"+string(member)), urgencyNormal)
	if failed(results, err) {
		s.mu.Lock()
		s.digestAttempts[member]++
		attempts := s.digestAttempts[member]
		s.digestRetry[member] = now.Add(retry)
		s.mu.Unlock()
		if attempts < digestAttempts {
			return fmt.Errorf("push: no device took the digest (attempt %d of %d): %w", attempts, digestAttempts, errors.Join(err, deviceErrors(results)))
		}
	}
	s.mu.Lock()
	delete(s.digestAttempts, member)
	delete(s.digestRetry, member)
	s.mu.Unlock()
	for _, item := range current {
		errs = append(errs, s.source(item).Sent(ctx, item, results, err))
	}
	return errors.Join(append(errs, s.store.DeleteDigestItems(ctx, member, current))...)
}

// failed reports whether a member with a device had none take a push.
func failed(results []DeviceResult, err error) bool {
	if err != nil {
		return true
	}
	for _, r := range results {
		if r.Status == DeviceDelivered {
			return false
		}
	}
	return len(results) > 0
}

func deviceErrors(results []DeviceResult) error {
	var errs []error
	for _, r := range results {
		errs = append(errs, r.Err)
	}
	return errors.Join(errs...)
}

func (s *Service) source(item *store.DigestItem) DigestSource {
	if item.Source == runSource {
		return runItems{s}
	}
	if source, ok := s.sources[item.Source]; ok {
		return source
	}
	return orphanItems{}
}

// queueNeed adds a session's notification to its owner's digest.
func (s *Service) queueNeed(ctx context.Context, run *domain.Run, n *need, kind string, overnight bool) error {
	workspace := ""
	if ws, err := s.store.GetWorkspace(ctx, run.WorkspaceID); err == nil {
		workspace = ws.Name
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	return s.Queue(ctx, &store.DigestItem{
		MemberID: n.member, Source: runSource, Key: n.key, Kind: kind, Workspace: workspace,
		Title: RunTitle(run), Body: n.body, URL: "/?run=" + url.QueryEscape(string(run.ID)), RunID: run.ID,
		Overnight: overnight, CreatedAt: s.now(),
	})
}

// runItems settles a session's own notifications: one is worth telling
// while the session still stands in the need it announced.
type runItems struct{ s *Service }

func (r runItems) Stale(ctx context.Context, item *store.DigestItem) (string, error) {
	_, n, err := r.s.read(ctx, item.RunID)
	if err != nil {
		return "", err
	}
	if n == nil || n.key != item.Key {
		return "Resolved before the digest was sent", nil
	}
	return "", nil
}

func (runItems) Sent(context.Context, *store.DigestItem, []DeviceResult, error) error { return nil }
func (runItems) Dropped(context.Context, *store.DigestItem, string) error             { return nil }

// orphanItems are items whose source this server does not run, such as
// watch notices on a server without watches: they are sent as they are.
type orphanItems struct{}

func (orphanItems) Stale(context.Context, *store.DigestItem) (string, error) { return "", nil }
func (orphanItems) Sent(context.Context, *store.DigestItem, []DeviceResult, error) error {
	return nil
}
func (orphanItems) Dropped(context.Context, *store.DigestItem, string) error { return nil }
