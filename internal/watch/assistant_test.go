package watch

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

// conversation records what is written into assistant conversations. While
// stall is set, writes wait for their context to end.
type conversation struct {
	mu      sync.Mutex
	notices []AssistantNotice
	stall   bool
	// toast, when set, is the error raising each notice's toast returns.
	toast error
}

func (c *conversation) Transcribe(ctx context.Context, _ domain.MemberID, n AssistantNotice) error {
	c.mu.Lock()
	stall := c.stall
	c.mu.Unlock()
	if stall {
		<-ctx.Done()
		return ctx.Err()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.notices = append(c.notices, n)
	if c.toast != nil {
		return fmt.Errorf("%w: %w", ErrNotAnnounced, c.toast)
	}
	return nil
}

func (c *conversation) written() []AssistantNotice {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]AssistantNotice(nil), c.notices...)
}

func deliveryOf(n *store.WatchNotification, channel string) store.WatchDelivery {
	for _, d := range n.Deliveries {
		if d.Channel == channel {
			return d
		}
	}
	return store.WatchDelivery{}
}

func (h *harness) follow(member *domain.Member, run *domain.Run) protocol.Watch {
	h.t.Helper()
	res, err := h.svc.Follow(h.ctx, member.ID, run.ID)
	if err != nil {
		h.t.Fatalf("Follow: %v", err)
	}
	return res.Watch
}

func TestAssistantWatchWritesNewsToTheConversationAndPushesOnlyWhatTheRunDoesNot(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.assistant = &conversation{}
	_ = h.svc.Close()
	h.start()
	talk := h.assistant.(*conversation)
	phone := h.subscribe(h.ada)
	run := h.run(h.ada, "Fix the login redirect")

	// The assistant writes to a run that stands in a question: it has just
	// answered it, so that question is no news.
	h.runs.ask(run.ID, "q1", "Which region?")
	w := h.follow(h.ada, run)
	if w.Source != protocol.WatchSourceAssistant || w.State != protocol.WatchActive {
		t.Fatalf("watch = %+v, want an active assistant watch", w)
	}
	if again := h.follow(h.ada, run); again.ID != w.ID {
		t.Fatalf("following again made watch %s, want %s kept", again.ID, w.ID)
	}
	// A watch Ada paused stays paused when the assistant acts on the run.
	paused, resumed := true, false
	if _, err := h.svc.Update(h.ctx, h.ada.ID, protocol.WatchUpdateParams{WatchID: w.ID, Paused: &paused}); err != nil {
		t.Fatal(err)
	}
	if again := h.follow(h.ada, run); again.ID != w.ID || again.State != protocol.WatchPaused {
		t.Fatalf("following a paused watch = %+v, want %s left paused", again, w.ID)
	}
	if _, err := h.svc.Update(h.ctx, h.ada.ID, protocol.WatchUpdateParams{WatchID: w.ID, Paused: &resumed}); err != nil {
		t.Fatal(err)
	}
	h.settle()
	if got := h.notices(w.ID); len(got) != 0 {
		t.Fatalf("notices for the standing question = %q, want none", bodies(got))
	}

	h.runs.ask(run.ID, "q2", "Deploy to staging first?")
	h.publish(run.ID, events.RunInputPayload{PendingInputs: h.runs.PendingInputs(run.ID)})
	h.eventually("the assistant delivery", func() bool {
		n := h.notices(w.ID)
		return len(n) == 1 && deliveryOf(n[0], protocol.WatchChannelAssistant).Status == protocol.WatchStatusDelivered
	})
	h.settle()
	written := talk.written()
	if len(written) != 1 || written[0].Condition != protocol.WatchNeedsYou || written[0].Body != "Question: Deploy to staging first?" ||
		written[0].RunID != run.ID || written[0].WatchLink != WatchLink(w.ID) || written[0].Title != "Fix the login redirect" {
		t.Fatalf("written = %+v, want the new question once with the watch link", written)
	}
	note := h.notices(w.ID)[0]
	if d := deliveryOf(note, protocol.WatchChannelInApp); d.Status != protocol.WatchStatusDelivered {
		t.Fatalf("in-app = %+v, want delivered", d)
	}
	if d := pushOf(note); d.Status != protocol.WatchStatusSkipped || phone.received.Load() != 0 {
		t.Fatalf("push = %+v with %d received, want skipped: the run's own notification pushed Ada", d, phone.received.Load())
	}

	// A run's end is news to its owner's devices.
	h.runs.answer(run.ID)
	h.status(run, domain.RunAbandoned, "", "closed")
	h.eventually("the end's push", func() bool { return phone.received.Load() == 1 })
	h.eventually("the end in the conversation", func() bool { return len(talk.written()) == 2 })
	if last := talk.written()[1]; last.Condition != protocol.WatchFinished || last.Body != "Closed as abandoned" {
		t.Fatalf("last written = %+v, want the end", last)
	}
	h.eventually("the watch to be done", func() bool { return h.watchState(w.ID).State == protocol.WatchDone })
}

func TestAnOwnersNeedWithNoDeviceHasNoDestination(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	run := h.run(h.ada, "Fix the login redirect")
	w := h.watch(h.ada, runTarget(run)).Watch
	h.runs.ask(run.ID, "q1", "Deploy to staging first?")
	h.publish(run.ID, events.RunInputPayload{PendingInputs: h.runs.PendingInputs(run.ID)})
	h.eventually("the need", func() bool { return len(h.notices(w.ID)) == 1 })
	if d := pushOf(h.notices(w.ID)[0]); d.Status != protocol.WatchStatusNoDestination {
		t.Fatalf("push = %+v, want no_destination: Ada has no device, so nothing pushed her", d)
	}
}

func TestAssistantNoticeCutShortByAStopIsWrittenAfterTheRestart(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	stalled := &conversation{stall: true}
	h.assistant = stalled
	_ = h.svc.Close()
	h.start()
	run := h.run(h.bob, "Rotate the keys")
	w := h.follow(h.ada, run)

	h.status(run, domain.RunNeedsAttention, domain.ActivityNeedsYou, "blocked: no credentials")
	h.eventually("the notice", func() bool { return len(h.notices(w.ID)) == 1 })
	h.settle()
	_ = h.svc.Close()
	if d := deliveryOf(h.notices(w.ID)[0], protocol.WatchChannelAssistant); d.Status != protocol.WatchStatusPending {
		t.Fatalf("assistant delivery after the stop = %+v, want pending", d)
	}

	talk := &conversation{}
	h.assistant = talk
	h.start()
	h.eventually("the delivery after the restart", func() bool {
		return deliveryOf(h.notices(w.ID)[0], protocol.WatchChannelAssistant).Status == protocol.WatchStatusDelivered
	})
	h.settle()
	if got := talk.written(); len(got) != 1 || got[0].Body != "Blocked: no credentials" {
		t.Fatalf("written after the restart = %+v, want the need once", got)
	}
	// Bob's run: Ada is not its owner, so the watch pushes her.
	if d := pushOf(h.notices(w.ID)[0]); d.Status == protocol.WatchStatusSkipped {
		t.Fatalf("push = %+v, want it not skipped for a run Ada does not own", d)
	}
}

func TestOnlyAssistantWatchesWriteToTheConversation(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.assistant = &conversation{}
	_ = h.svc.Close()
	h.start()
	run := h.run(h.bob, "Audit the logs")
	w := h.watch(h.ada, runTarget(run)).Watch
	h.status(run, domain.RunNeedsAttention, domain.ActivityNeedsYou, "blocked: x")
	h.eventually("the notice", func() bool { return len(h.notices(w.ID)) == 1 })
	h.settle()
	if d := deliveryOf(h.notices(w.ID)[0], protocol.WatchChannelAssistant); d.Channel != "" {
		t.Fatalf("dashboard watch has an assistant delivery %+v", d)
	}
	if got := h.assistant.(*conversation).written(); len(got) != 0 {
		t.Fatalf("written = %+v, want nothing", got)
	}
}

func TestAssistantWatchIsItsOwnBesideADashboardWatch(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.assistant = &conversation{}
	_ = h.svc.Close()
	h.start()
	run := h.run(h.bob, "Write the changelog")
	dashboard := h.watch(h.ada, runTarget(run)).Watch
	w := h.follow(h.ada, run)
	if w.ID == dashboard.ID || w.Source != protocol.WatchSourceAssistant {
		t.Fatalf("assistant watch = %+v, want its own beside the dashboard's %s", w, dashboard.ID)
	}
	h.status(run, domain.RunCompleted, "", "")
	h.eventually("the end in the conversation", func() bool { return len(h.assistant.(*conversation).written()) == 1 })
	h.eventually("the dashboard watch's end", func() bool { return len(h.notices(dashboard.ID)) == 1 })
	h.settle()
	if got := h.notices(w.ID); len(got) != 1 || got[0].Body != "Completed" {
		t.Fatalf("assistant watch notices = %q, want the end", bodies(got))
	}
	if got := h.notices(dashboard.ID); len(got) != 1 || deliveryOf(got[0], protocol.WatchChannelAssistant).Channel != "" {
		t.Fatalf("dashboard watch notices = %+v, want the end without an assistant delivery", got)
	}
}

func TestAssistantRequestForADashboardWatchWritesItsNoticesToTheConversation(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.assistant = &conversation{}
	_ = h.svc.Close()
	h.start()
	run := h.run(h.bob, "Tidy the fixtures")
	dashboard := h.watch(h.ada, runTarget(run)).Watch
	res, err := h.svc.Create(h.ctx, h.ada.ID, protocol.WatchCreateParams{Target: runTarget(run), Source: protocol.WatchSourceAssistant})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if w := res.Watch; w.ID != dashboard.ID || w.Source != protocol.WatchSourceAssistant {
		t.Fatalf("assistant request = %+v, want watch %s as an assistant watch", w, dashboard.ID)
	}
	if w := h.follow(h.ada, run); w.ID != dashboard.ID {
		t.Fatalf("follow = %s, want the same watch %s", w.ID, dashboard.ID)
	}
	h.status(run, domain.RunCompleted, "", "")
	h.eventually("the end in the conversation", func() bool { return len(h.assistant.(*conversation).written()) == 1 })
	h.settle()
	if got := h.assistant.(*conversation).written(); len(got) != 1 {
		t.Fatalf("written = %+v, want one notice", got)
	}
	if got := h.notices(dashboard.ID); len(got) != 1 || deliveryOf(got[0], protocol.WatchChannelAssistant).Channel == "" {
		t.Fatalf("notices = %+v, want the end with an assistant delivery", got)
	}
}

func TestOwnersRunNotificationCoversOnlyWhatItPushes(t *testing.T) {
	t.Parallel()
	w := &store.Watch{MemberID: "ada"}
	started := time.Now()
	before := started.Add(-time.Minute)
	own := func(edit func(*domain.Run)) *domain.Run {
		r := &domain.Run{MemberID: "ada", Review: domain.ReviewReady}
		if edit != nil {
			edit(r)
		}
		return r
	}
	need := Notice{Condition: protocol.WatchNeedsYou, Key: "status:x"}
	reported := Notice{Condition: protocol.WatchFinished, Key: "reported|live|1"}
	failed := Notice{Condition: protocol.WatchFinished, Key: "failed"}
	for _, c := range []struct {
		name string
		run  *domain.Run
		n    Notice
		want bool
	}{
		{"a need of the owner's run", own(nil), need, true},
		{"a need of someone else's run", own(func(r *domain.Run) { r.MemberID = "bob" }), need, false},
		{"a need of a swarm run", own(func(r *domain.Run) { r.MissionID = "m1" }), need, false},
		{"a success parked for review", own(nil), reported, true},
		{"a success never put up for review", own(func(r *domain.Run) { r.Review = "" }), reported, false},
		{"a success put off for later", own(func(r *domain.Run) { r.Review = domain.ReviewLater }), reported, false},
		{"a failure nobody has seen", own(func(r *domain.Run) { r.OutcomeUnseen = true }), failed, true},
		{"a failure already seen", own(nil), failed, false},
		{"closed as merged", own(nil), Notice{Condition: protocol.WatchFinished, Key: "merged"}, false},
		{"closed as abandoned", own(nil), Notice{Condition: protocol.WatchFinished, Key: "abandoned"}, false},
		{"stopped", own(nil), Notice{Condition: protocol.WatchFinished, Key: "interrupted"}, false},
		{"completed", own(nil), Notice{Condition: protocol.WatchFinished, Key: "completed"}, false},
		{"its pull request merged", own(nil), Notice{Condition: protocol.WatchPRMerged, Key: "merged"}, false},
		{"a need entered before the server started", own(func(r *domain.Run) { r.StatusChangedAt = &before }), need, false},
		{"a success parked before the server started", own(func(r *domain.Run) { r.StatusChangedAt = &before }), reported, false},
		{"a question asked before the server started", own(func(r *domain.Run) { r.StatusChangedAt = &before }),
			Notice{Condition: protocol.WatchNeedsYou, Key: "input:s|question|q1"}, true},
	} {
		if got := ownerPushed(w, c.run, c.n, started); got != c.want {
			t.Errorf("%s: skipped = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestRenewedAssistantWatchTakesTheStandingNeedAsSeen(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.assistant = &conversation{}
	_ = h.svc.Close()
	h.start()
	run := h.run(h.bob, "Long task")
	w := h.follow(h.ada, run)
	h.clock.advance(DefaultExpiry + time.Hour)
	h.eventually("expiry", func() bool { return h.watchState(w.ID).State == protocol.WatchExpired })

	// The assistant answers a question the run asked while the watch was
	// expired: renewing the watch does not announce it.
	h.runs.ask(run.ID, "q1", "Which region?")
	if again := h.follow(h.ada, run); again.ID != w.ID || again.State != protocol.WatchActive {
		t.Fatalf("following an expired watch = %+v, want %s renewed", again, w.ID)
	}
	h.settle()
	if got := h.notices(w.ID); len(got) != 0 {
		t.Fatalf("notices after the renewal = %q, want none", bodies(got))
	}
	h.runs.ask(run.ID, "q2", "Deploy now?")
	h.publish(run.ID, events.RunInputPayload{PendingInputs: h.runs.PendingInputs(run.ID)})
	h.eventually("the next question", func() bool { return len(h.notices(w.ID)) == 1 })
}

func TestPausedAssistantWatchHoldsItsConversationNotices(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	stalled := &conversation{stall: true}
	h.assistant = stalled
	_ = h.svc.Close()
	h.start()
	run := h.run(h.bob, "Rotate the keys")
	w := h.follow(h.ada, run)
	h.status(run, domain.RunNeedsAttention, domain.ActivityNeedsYou, "blocked: no credentials")
	h.eventually("the notice", func() bool { return len(h.notices(w.ID)) == 1 })

	// Paused while its notice was still to be written: the restart keeps it
	// pending until Ada resumes the watch.
	paused, resumed := true, false
	if _, err := h.svc.Update(h.ctx, h.ada.ID, protocol.WatchUpdateParams{WatchID: w.ID, Paused: &paused}); err != nil {
		t.Fatal(err)
	}
	_ = h.svc.Close()
	talk := &conversation{}
	h.assistant = talk
	h.start()
	h.clock.advance(time.Minute)
	h.settle()
	if got := talk.written(); len(got) != 0 {
		t.Fatalf("written while paused = %+v, want nothing", got)
	}
	if d := deliveryOf(h.notices(w.ID)[0], protocol.WatchChannelAssistant); d.Status != protocol.WatchStatusPending {
		t.Fatalf("assistant delivery while paused = %+v, want pending", d)
	}
	if _, err := h.svc.Update(h.ctx, h.ada.ID, protocol.WatchUpdateParams{WatchID: w.ID, Paused: &resumed}); err != nil {
		t.Fatal(err)
	}
	h.eventually("the delivery after resuming", func() bool { return len(talk.written()) == 1 })
}

func TestAssistantWatchOfARelaunchedRunStartsAgain(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.assistant = &conversation{}
	_ = h.svc.Close()
	h.start()
	run := h.run(h.bob, "Retry the import")
	first := h.follow(h.ada, run)
	h.status(run, domain.RunAbandoned, "", "closed")
	h.eventually("the watch to be done", func() bool { return h.watchState(first.ID).State == protocol.WatchDone })

	// Writing to a run that ended keeps its done watch.
	if again := h.follow(h.ada, run); again.ID != first.ID || again.State != protocol.WatchDone {
		t.Fatalf("following the ended run = %+v, want %s left done", again, first.ID)
	}

	// The run is relaunched and the assistant writes to it: a new watch
	// announces its next end, even the same one.
	h.status(run, domain.RunRunning, domain.ActivityWorking, "")
	second := h.follow(h.ada, run)
	if second.ID == first.ID || second.State != protocol.WatchActive {
		t.Fatalf("following the relaunched run = %+v, want a new active watch", second)
	}
	if again := h.follow(h.ada, run); again.ID != second.ID {
		t.Fatalf("following again made watch %s, want %s kept", again.ID, second.ID)
	}
	h.status(run, domain.RunAbandoned, "", "closed")
	h.eventually("the second end", func() bool { return len(h.notices(second.ID)) == 1 })
	if got := h.notices(first.ID); len(got) != 1 {
		t.Fatalf("first watch notices = %q, want its own end alone", bodies(got))
	}
}

func TestAssistantWatchAnnouncesAnEndReachedBeforeItBegan(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.assistant = &conversation{}
	_ = h.svc.Close()
	h.start()
	run := h.run(h.bob, "Fails at once")
	h.status(run, domain.RunFailed, "", "image not found")

	// The run the assistant just launched failed before its watch began:
	// that end is the news.
	w := h.follow(h.ada, run)
	h.eventually("the end in the conversation", func() bool { return len(h.assistant.(*conversation).written()) == 1 })
	if got := h.notices(w.ID); len(got) != 1 || got[0].Body != "Failed: image not found" {
		t.Fatalf("notices = %q, want the failure", bodies(got))
	}
	h.eventually("the watch to be done", func() bool { return h.watchState(w.ID).State == protocol.WatchDone })
	if _, err := h.svc.Create(h.ctx, h.ada.ID, protocol.WatchCreateParams{Target: runTarget(run)}); err == nil {
		t.Fatal("a dashboard watch on the ended run was created, want it refused")
	}
}

func TestAssistantNoticeWithoutItsToastIsDeliveredWithTheReason(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	talk := &conversation{toast: errors.New("event log full")}
	h.assistant = talk
	_ = h.svc.Close()
	h.start()
	run := h.run(h.bob, "Rotate the keys")
	w := h.follow(h.ada, run)
	h.status(run, domain.RunNeedsAttention, domain.ActivityNeedsYou, "blocked: no credentials")
	h.eventually("the assistant delivery", func() bool {
		n := h.notices(w.ID)
		return len(n) == 1 && deliveryOf(n[0], protocol.WatchChannelAssistant).Status != protocol.WatchStatusPending
	})
	d := deliveryOf(h.notices(w.ID)[0], protocol.WatchChannelAssistant)
	if d.Status != protocol.WatchStatusDelivered || d.Detail != "written to the conversation, but its toast was not raised: event log full" {
		t.Fatalf("assistant delivery = %+v, want delivered with the toast's error", d)
	}
	h.settle()
	if got := talk.written(); len(got) != 1 {
		t.Fatalf("written = %d notices, want the one, not retried", len(got))
	}
}
