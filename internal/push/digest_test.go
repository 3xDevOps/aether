package push

import (
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
	phone.expect(t, notification{
		Title: "2 need you", Body: "Fix login test: run the migration?\nLaunch docs: Blocked: no docs key", Notice: true, Quiet: true,
	})
	if got, want := phone.topic.Load(), topicOf(string(a.ID)); got != want {
		t.Errorf("summary topic = %v, want the first push's %s, which it replaces", got, want)
	}

	h.clock.advance(burstWindow)
	h.status(c, domain.RunNeedsAttention, domain.ActivityNeedsYou, "blocked: lockfile conflict")
	phone.expect(t, notification{Title: "Bump deps", Body: "Blocked: lockfile conflict", Run: string(c.ID)})
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

	h.clock.advance(batchWindow)
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
	h.clock.advance(batchWindow)
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
	h.clock.advance(batchWindow)
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
