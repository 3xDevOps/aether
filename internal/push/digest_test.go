package push

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

func (h *harness) digestItems(member *domain.Member) []*store.DigestItem {
	h.t.Helper()
	items, err := h.db.ListDigestItems(h.ctx, member.ID)
	if err != nil {
		h.t.Fatal(err)
	}
	return items
}

// eventuallyQueued waits for member's digest to hold n items.
func (h *harness) eventuallyQueued(member *domain.Member, n int) {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for len(h.digestItems(member)) != n {
		if time.Now().After(deadline) {
			h.t.Fatalf("digest holds %d items, want %d", len(h.digestItems(member)), n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (h *harness) finish(run *domain.Run) {
	h.t.Helper()
	if _, err := h.db.FinishRunReported(h.ctx, run.ID, domain.RunNeedsAttention, "agent reported success", domain.ActivityFinished, nil, nil); err != nil {
		h.t.Fatalf("FinishRunReported: %v", err)
	}
	h.publish(run, events.RunStatusPayload{To: domain.RunNeedsAttention, Activity: domain.ActivityFinished, OutcomeUnseen: true})
}

func TestPushesWithinTwoMinutesJoinTheFirstAsOneNotification(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	phone := h.subscribe(h.ada)
	a, b, c := h.run(h.ada, "Fix login test", domain.LaunchACP), h.run(h.ada, "Launch docs", domain.LaunchACP), h.run(h.ada, "Bump deps", domain.LaunchACP)

	h.status(a, domain.RunNeedsAttention, domain.ActivityNeedsYou, "needs input: run the migration?")
	phone.expect(t, notification{Title: "Fix login test", Body: "run the migration?", Run: string(a.ID)})
	h.clock.advance(time.Minute)
	h.status(b, domain.RunNeedsAttention, domain.ActivityNeedsYou, "blocked: no docs key")
	phone.expect(t, notification{Title: "2 need you", Body: "Fix login test: run the migration?\nLaunch docs: Blocked: no docs key", Quiet: true})
	if got, want := phone.topic.Load(), topicOf(string(a.ID)); got != want {
		t.Errorf("summary topic = %v, want the first push's %s, which it replaces", got, want)
	}
	if got := phone.urgency.Load(); got != urgencyHigh {
		t.Errorf("summary urgency = %v, want %s while it holds a need", got, urgencyHigh)
	}

	// An item whose need ends leaves the summary; once none is left, it is
	// cleared like a need that ended.
	h.status(b, domain.RunRunning, domain.ActivityWorking, "")
	phone.expect(t, notification{Title: "1 need you", Body: "Fix login test: run the migration?", Quiet: true})
	h.status(a, domain.RunRunning, domain.ActivityWorking, "")
	phone.expect(t, notification{Title: "Fix login test", Body: clearedBody, Run: string(a.ID), Clear: true})
	phone.expect(t, notification{Body: clearedBody, Clear: true})

	h.clock.advance(burstWindow)
	h.status(c, domain.RunNeedsAttention, domain.ActivityNeedsYou, "blocked: lockfile conflict")
	phone.expect(t, notification{Title: "Bump deps", Body: "Blocked: lockfile conflict", Run: string(c.ID)})
}

// waiting is a digest source whose items stay queued, as a paused
// session's need does.
type waiting struct{}

func (waiting) Stale(context.Context, *store.DigestItem) (string, error) { return "", ErrNotYet }
func (waiting) Sent(context.Context, *store.DigestItem, []DeviceResult, error, bool) error {
	return nil
}
func (waiting) Dropped(context.Context, *store.DigestItem, string) error { return nil }

func TestAnItemThatWaitsDoesNotMakeTheDigestDue(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	phone := h.subscribe(h.ada)
	h.svc.UseDigestSource("waiting", waiting{})
	if err := h.svc.Queue(h.ctx, &store.DigestItem{
		MemberID: h.ada.ID, Source: "waiting", Key: "old", Kind: KindNeed, Title: "Paused", CreatedAt: h.clock.now().Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	run := h.run(h.ada, "Upgrade the linter", domain.LaunchACP)
	h.finish(run)
	h.eventuallyQueued(h.ada, 2)
	if err := h.svc.sendDigest(h.ctx, h.ada.ID); err != nil {
		t.Fatal(err)
	}
	phone.expectNone(t, testQuiet)

	h.clock.advance(30 * time.Minute)
	if err := h.svc.sendDigest(h.ctx, h.ada.ID); err != nil {
		t.Fatal(err)
	}
	phone.expect(t, notification{Title: "Aether · 1 done", Body: "proj  Upgrade the linter: Finished: review the result  [Review]", Notice: true})
	if got := h.digestItems(h.ada); len(got) != 1 || got[0].Key != "old" {
		t.Fatalf("digest holds %+v, want the waiting item alone", got)
	}
}

func TestTheBatchedLevelSendsOneDigestOnTheHalfHour(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	phone := h.subscribe(h.ada)
	a, b := h.run(h.ada, "Upgrade the linter", domain.LaunchACP), h.run(h.ada, "Rotate the keys", domain.LaunchACP)

	h.finish(a)
	h.eventuallyQueued(h.ada, 1)
	h.finish(b)
	h.eventuallyQueued(h.ada, 2)
	phone.expectNone(t, testQuiet)
	h.svc.sendDigests(h.ctx)
	phone.expectNone(t, testQuiet)

	h.clock.advance(30 * time.Minute)
	h.svc.sendDigests(h.ctx)
	phone.expect(t, notification{
		Title:  "Aether · 2 done",
		Body:   "proj  Upgrade the linter: Finished: review the result  [Review]\nproj  Rotate the keys: Finished: review the result  [Review]",
		Notice: true,
	})
	if got := h.digestItems(h.ada); len(got) != 0 {
		t.Fatalf("digest still holds %d items after it was sent", len(got))
	}
}

// Seen anywhere, cleared everywhere: a result the member opened on any
// door leaves the digest, and a digest left empty is not sent.
func TestAnItemSeenBeforeTheDigestIsDroppedAndAnEmptyDigestIsNotSent(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	phone := h.subscribe(h.ada)
	run, kept := h.run(h.ada, "Upgrade the linter", domain.LaunchACP), h.run(h.ada, "Rotate the keys", domain.LaunchACP)
	h.finish(run)
	h.eventuallyQueued(h.ada, 1)
	h.finish(kept)
	h.eventuallyQueued(h.ada, 2)

	if _, _, err := h.db.SetRunReview(h.ctx, run.ID, domain.ReviewAccepted, domain.ReviewReady); err != nil {
		t.Fatalf("SetRunReview: %v", err)
	}
	h.clock.advance(30 * time.Minute)
	if err := h.svc.sendDigest(h.ctx, h.ada.ID); err != nil {
		t.Fatal(err)
	}
	phone.expect(t, notification{
		Title: "Aether · 1 done", Body: "proj  Rotate the keys: Finished: review the result  [Review]", Notice: true,
	})

	late := h.run(h.ada, "Write the docs", domain.LaunchACP)
	h.finish(late)
	h.eventuallyQueued(h.ada, 1)
	if _, _, err := h.db.SetRunReview(h.ctx, late.ID, domain.ReviewAccepted, domain.ReviewReady); err != nil {
		t.Fatalf("SetRunReview: %v", err)
	}
	h.clock.advance(30 * time.Minute)
	if err := h.svc.sendDigest(h.ctx, h.ada.ID); err != nil {
		t.Fatal(err)
	}
	phone.expectNone(t, testQuiet)
	if got := h.digestItems(h.ada); len(got) != 0 {
		t.Fatalf("digest still holds %d items", len(got))
	}
}

func TestQuietHoursHoldNeedsForADigestThatLeadsWithThem(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	phone := h.subscribe(h.ada)
	now := h.clock.now().UTC()
	prefs := store.NotifyPrefs{
		Push: true, Level: protocol.NotifyBlockers, DigestAt: now.Add(6 * time.Hour).Format("15:04"),
		QuietStart: now.Add(-time.Hour).Format("15:04"), QuietEnd: now.Add(time.Hour).Format("15:04"), QuietTZ: "UTC",
	}
	if err := h.db.PutNotifyPrefs(h.ctx, h.ada.ID, prefs); err != nil {
		t.Fatal(err)
	}
	run := h.run(h.ada, "Fix login test", domain.LaunchACP)
	h.status(run, domain.RunNeedsAttention, domain.ActivityNeedsYou, "needs input: run the migration?")
	h.eventuallyQueued(h.ada, 1)
	phone.expectNone(t, testQuiet)
	h.svc.sendDigests(h.ctx)
	phone.expectNone(t, testQuiet)

	h.clock.advance(time.Hour + time.Minute)
	h.svc.sendDigests(h.ctx)
	phone.expect(t, notification{
		Title:  "Aether · 1 need you",
		Body:   "Overnight: proj  Fix login test: run the migration?  [Answer]",
		Notice: true,
	})
}

// recorded is a digest source that keeps what each Sent was told.
type recorded struct{ retrying chan bool }

func (recorded) Stale(context.Context, *store.DigestItem) (string, error) { return "", nil }
func (r recorded) Sent(_ context.Context, _ *store.DigestItem, _ []DeviceResult, _ error, retrying bool) error {
	r.retrying <- retrying
	return nil
}
func (recorded) Dropped(context.Context, *store.DigestItem, string) error { return nil }

func TestADigestNoDeviceTookIsTriedAgainAndSaysSo(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	phone := h.subscribe(h.ada)
	phone.answer.Store(http.StatusInternalServerError)
	source := recorded{retrying: make(chan bool, digestAttempts)}
	h.svc.UseDigestSource("recorded", source)
	if err := h.svc.Queue(h.ctx, &store.DigestItem{
		MemberID: h.ada.ID, Source: "recorded", Key: "k", Kind: KindDone, Title: "Done", Overnight: true, CreatedAt: h.clock.now(),
	}); err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= digestAttempts; attempt++ {
		err := h.svc.sendDigest(h.ctx, h.ada.ID)
		if final := attempt == digestAttempts; (err == nil) == !final {
			t.Fatalf("attempt %d: err = %v", attempt, err)
		}
		if retrying := <-source.retrying; retrying != (attempt < digestAttempts) {
			t.Fatalf("attempt %d: retrying = %v", attempt, retrying)
		}
		h.clock.advance(retry)
	}
	if got := h.digestItems(h.ada); len(got) != 0 {
		t.Fatalf("digest still holds %d items after its last attempt", len(got))
	}
}

func TestAPausedSessionsItemWaitsAndAnOpenedResultLeavesTheDigest(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	phone := h.subscribe(h.ada)
	paused, opened := h.run(h.ada, "Paused", domain.LaunchACP), h.run(h.ada, "Opened", domain.LaunchACP)
	h.finish(paused)
	h.eventuallyQueued(h.ada, 1)
	h.finish(opened)
	h.eventuallyQueued(h.ada, 2)

	h.runs.paused.Store(true)
	if _, err := h.db.ClearRunOutcomeUnseen(h.ctx, opened.ID, h.ada.ID); err != nil {
		t.Fatal(err)
	}
	h.clock.advance(30 * time.Minute)
	if err := h.svc.sendDigest(h.ctx, h.ada.ID); err != nil {
		t.Fatal(err)
	}
	phone.expectNone(t, testQuiet)
	if got := h.digestItems(h.ada); len(got) != 2 {
		t.Fatalf("digest holds %d items while their sessions are paused, want both kept", len(got))
	}

	h.runs.paused.Store(false)
	if err := h.svc.sendDigest(h.ctx, h.ada.ID); err != nil {
		t.Fatal(err)
	}
	phone.expect(t, notification{Title: "Aether · 1 done", Body: "proj  Paused: Finished: review the result  [Review]", Notice: true})
	if got := h.digestItems(h.ada); len(got) != 0 {
		t.Fatalf("digest still holds %d items", len(got))
	}
}

func TestExpiredBurstsWithoutANeedAreForgotten(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	for i := range 3 {
		if _, err := h.svc.join(h.ada.ID, fmt.Sprintf("pr-%d", i), "tag", "topic", "acme/web#7", "Checks passed", false); err != nil {
			t.Fatal(err)
		}
		h.clock.advance(burstWindow)
	}
	h.svc.mu.Lock()
	defer h.svc.mu.Unlock()
	if n := len(h.svc.bursts[h.ada.ID]); n != 1 {
		t.Fatalf("member keeps %d bursts, want only the one inside its window", n)
	}
}

func TestASummaryClaimsItsFirstPushsNotificationWhileItShowsANeed(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	for _, key := range []string{"run-a", "run-b"} {
		if _, err := h.svc.join(h.ada.ID, key, "wat-1|needs_you", "topic", "Fix it", "Blocked", true); err != nil {
			t.Fatal(err)
		}
	}
	if !h.svc.Summarizes(h.ada.ID, "wat-1|needs_you") {
		t.Fatal("a summary of two needs does not claim the first push's notification")
	}
	h.svc.unjoin(h.ctx, "run-a", "")
	h.svc.unjoin(h.ctx, "run-b", "")
	if h.svc.Summarizes(h.ada.ID, "wat-1|needs_you") {
		t.Fatal("a summary with no need left still claims the notification")
	}
}

func TestASummaryThatShrankToOneItemStaysASummary(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	for _, key := range []string{"run-a", "run-b"} {
		if _, err := h.svc.join(h.ada.ID, key, "run-a", "topic", "Fix it", "Blocked", true); err != nil {
			t.Fatal(err)
		}
	}
	h.svc.unjoin(h.ctx, "run-a", "")
	sum, err := h.svc.join(h.ada.ID, "run-b", "run-b", "topic-b", "Fix it", "Which region?", true)
	if err != nil || sum == nil {
		t.Fatalf("join = %v, %v; want the summary updated, not a second notification", sum, err)
	}
}

func TestAResultQueuedAfterADashboardHoldKeepsItsTime(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.subscribe(h.ada)
	h.level(h.ada, protocol.NotifyBatched)
	run := h.run(h.ada, "Upgrade the linter", domain.LaunchACP)
	h.svc.Active(h.ada.ID)
	h.finish(run)
	h.eventuallyQueued(h.ada, 1)
	got, err := h.db.GetRun(h.ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if item := h.digestItems(h.ada)[0]; got.ReviewChangedAt == nil || !item.CreatedAt.Equal(got.ReviewChangedAt.UTC()) {
		t.Fatalf("queued at %v, want when the session finished (%v)", item.CreatedAt, got.ReviewChangedAt)
	}
}

func TestAHandoffKeepsTheNewOwnersSummary(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	for _, member := range []*domain.Member{h.ada, h.bob} {
		for _, key := range []string{"run-a", "run-b"} {
			if _, err := h.svc.join(member.ID, key, "run-a", "topic", "Fix it", "Blocked", true); err != nil {
				t.Fatal(err)
			}
		}
	}
	h.svc.unjoin(h.ctx, "run-a", h.bob.ID)
	if h.svc.Summarizes(h.bob.ID, "run-a") != true || !slices.Contains(h.svc.bursts[h.bob.ID][0].keys, "run-a") {
		t.Fatal("the new owner's summary lost the run")
	}
	if slices.Contains(h.svc.bursts[h.ada.ID][0].keys, "run-a") {
		t.Fatal("the former owner's summary still shows the run")
	}
}

func TestAResultFromQuietHoursHeldByTheDashboardStaysOvernight(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.subscribe(h.ada)
	now := h.clock.now().UTC()
	if err := h.db.PutNotifyPrefs(h.ctx, h.ada.ID, store.NotifyPrefs{
		Push: true, Level: protocol.NotifyBlockers, DigestAt: now.Add(-2 * time.Hour).Format("15:04"),
		QuietStart: now.Add(-time.Hour).Format("15:04"), QuietEnd: now.Add(time.Hour).Format("15:04"), QuietTZ: "UTC",
	}); err != nil {
		t.Fatal(err)
	}
	h.svc.Active(h.ada.ID)
	run := h.run(h.ada, "Upgrade the linter", domain.LaunchACP)
	h.finish(run)
	// Quiet hours end while the dashboard still holds the result.
	h.clock.advance(time.Hour + time.Minute)
	h.eventuallyQueued(h.ada, 1)
	if item := h.digestItems(h.ada)[0]; !item.Overnight {
		t.Fatalf("item = %+v, want it held for the digest quiet hours end with, not tomorrow's", *item)
	}
}
