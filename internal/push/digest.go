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
	{KindCard, "to approve", "Approve"},
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
// Sent records the digest push an item went out in, retrying when no
// device took it and it will be tried again, and Dropped one dropped
// unsent with reason.
type DigestSource interface {
	Stale(ctx context.Context, item *store.DigestItem) (string, error)
	Sent(ctx context.Context, item *store.DigestItem, results []DeviceResult, err error, retrying bool) error
	Dropped(ctx context.Context, item *store.DigestItem, reason string) error
}

// UseDigestSource has source settle the items queued under name. Call it
// before Start.
func (s *Service) UseDigestSource(name string, source DigestSource) {
	s.sources[name] = source
}

func (s *Service) Queue(ctx context.Context, item *store.DigestItem) error {
	return s.store.AddDigestItem(ctx, item)
}

// digestText is a digest's title and body: counts of each kind, then at
// most digestLines items, those quiet hours held first and marked
// Overnight:, each group what needs the member first, and a count of the
// rest.
func digestText(items []*store.DigestItem, lines int) (string, string) {
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
	shown := ordered[:min(len(ordered), lines)]
	var out []string
	for _, item := range shown {
		out = append(out, digestLine(item, kinds[rank[item.Kind]].action))
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
		out = append(out, strings.Join(more, " · "))
	}
	return title, strings.Join(out, "\n")
}

func digestLine(item *store.DigestItem, action string) string {
	line := clip(plain(item.Title), digestTitleRunes)
	if body := plain(item.Body); body != "" {
		line += ": " + clip(body, digestBodyRunes)
	}
	if item.Workspace != "" {
		line = clip(plain(item.Workspace), digestTitleRunes) + "  " + line
	}
	if item.Overnight {
		line = "Overnight: " + line
	}
	return line + "  [" + action + "]"
}

// plain is text an agent may have written, on one line, without control
// or format characters such as bidirectional overrides.
func plain(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
}

func (s *Service) digests(ctx context.Context) {
	if s.recovered != nil {
		select {
		case <-ctx.Done():
			return
		case <-s.recovered:
		}
	}
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
	// Each item is read again before the due time is: one that waits
	// (ErrNotYet) or cannot be read now must not make the rest due.
	var current, stale []*store.DigestItem
	var errs []error
	for _, item := range items {
		reason, staleErr := s.source(item).Stale(ctx, item)
		switch {
		case errors.Is(staleErr, ErrNotYet):
		case staleErr != nil:
			slog.Warn("push: read a digest item again; it stays queued", "member", member, "source", item.Source, "key", item.Key, "error", staleErr)
		case reason != "":
			// One whose drop could not be recorded stays queued to be
			// dropped again.
			if err = s.source(item).Dropped(ctx, item, reason); err != nil {
				errs = append(errs, err)
				continue
			}
			stale = append(stale, item)
		default:
			current = append(current, item)
		}
	}
	if err = s.store.DeleteDigestItems(ctx, member, stale); err != nil {
		errs = append(errs, err)
	}
	if len(current) == 0 {
		s.mu.Lock()
		delete(s.digestAttempts, member)
		delete(s.digestRetry, member)
		s.mu.Unlock()
		return errors.Join(errs...)
	}
	if digestDue(prefs, current, now).After(now) {
		return errors.Join(errs...)
	}
	payload, err := digestPayload(current)
	if err != nil {
		return err
	}
	results, err := s.deliverPayload(ctx, member, payload, topicOf("digest:"+string(member)), urgencyNormal)
	if failed(results, err) {
		s.mu.Lock()
		s.digestAttempts[member]++
		attempts := s.digestAttempts[member]
		s.digestRetry[member] = now.Add(retry)
		s.mu.Unlock()
		if attempts < digestAttempts {
			for _, item := range current {
				if sentErr := s.source(item).Sent(ctx, item, results, err, true); sentErr != nil {
					errs = append(errs, sentErr)
				}
			}
			return errors.Join(append(errs, fmt.Errorf("push: no device took the digest (attempt %d of %d): %w", attempts, digestAttempts, errors.Join(err, deviceErrors(results))))...)
		}
	}
	s.mu.Lock()
	delete(s.digestAttempts, member)
	delete(s.digestRetry, member)
	s.mu.Unlock()
	for _, item := range current {
		if sentErr := s.source(item).Sent(ctx, item, results, err, false); sentErr != nil {
			errs = append(errs, sentErr)
		}
	}
	if err = s.store.DeleteDigestItems(ctx, member, current); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// digestPayload is the digest of items as one push message: lines that
// would not fit it go into the count of the rest.
func digestPayload(items []*store.DigestItem) ([]byte, error) {
	for lines := digestLines; ; lines-- {
		title, body := digestText(items, lines)
		payload, err := json.Marshal(message{notification: notification{Title: title, Body: body, Notice: true}, URL: "/", Tag: "digest"})
		if err != nil {
			return nil, fmt.Errorf("push: encode a digest: %w", err)
		}
		if len(payload) <= maxPayload || lines == 0 {
			return payload, nil
		}
	}
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
	switch item.Source {
	case runSource:
		return runItems{s}
	case cardSource:
		return cardItems{s}
	}
	if source, ok := s.sources[item.Source]; ok {
		return source
	}
	return orphanItems{}
}

// queueNeed adds a session's notification to its owner's digest.
func (s *Service) queueNeed(ctx context.Context, run *domain.Run, n *need, kind string, overnight bool) error {
	// Queued as of when the need began, not when the member left the
	// dashboard, so a hold never pushes it past its digest.
	queued := s.now()
	if !n.since.IsZero() && n.since.Before(queued) {
		queued = n.since
	}
	workspace := ""
	if ws, err := s.store.GetWorkspace(ctx, run.WorkspaceID); err == nil {
		workspace = ws.Name
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	return s.Queue(ctx, &store.DigestItem{
		MemberID: n.member, Source: runSource, Key: n.key, Kind: kind, Workspace: workspace,
		Title: RunTitle(run), Body: n.body, URL: "/?run=" + url.QueryEscape(string(run.ID)), RunID: run.ID,
		Overnight: overnight, CreatedAt: queued,
	})
}

// runItems settles a session's own notifications: one is worth telling
// while the session still stands in the need it announced, and a result
// while nobody has opened it. A paused session's waits for it to resume.
type runItems struct{ s *Service }

func (r runItems) Stale(ctx context.Context, item *store.DigestItem) (string, error) {
	if r.s.runs.Paused(item.RunID) {
		return "", ErrNotYet
	}
	run, n, err := r.s.read(ctx, item.RunID)
	if err != nil {
		return "", err
	}
	if n == nil || n.key != item.Key {
		return "Resolved before the digest was sent", nil
	}
	if item.Kind == KindDone && !run.OutcomeUnseen {
		return "Opened before the digest was sent", nil
	}
	return "", nil
}

func (runItems) Sent(context.Context, *store.DigestItem, []DeviceResult, error, bool) error {
	return nil
}
func (runItems) Dropped(context.Context, *store.DigestItem, string) error { return nil }

// orphanItems are items whose source this server does not run, such as
// watch notices on a server without watches: they are sent as they are.
type orphanItems struct{}

func (orphanItems) Stale(context.Context, *store.DigestItem) (string, error) { return "", nil }
func (orphanItems) Sent(context.Context, *store.DigestItem, []DeviceResult, error, bool) error {
	return nil
}
func (orphanItems) Dropped(context.Context, *store.DigestItem, string) error { return nil }
