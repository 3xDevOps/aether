package watch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/github"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

type notifyCall struct {
	from, to        domain.RunID
	body, corr, key string
}

// fakeInbox is coord.Service.Notify: a key it already took adds nothing.
type fakeInbox struct {
	mu    sync.Mutex
	calls []notifyCall
	kept  map[string]notifyCall
	fail  atomic.Bool
}

func (f *fakeInbox) Notify(_ context.Context, from, to domain.RunID, body, corr, key string) error {
	if f.fail.Load() {
		return errors.New("inbox unavailable")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	c := notifyCall{from: from, to: to, body: body, corr: corr, key: key}
	f.calls = append(f.calls, c)
	if _, ok := f.kept[key]; !ok {
		f.kept[key] = c
	}
	return nil
}

// messages are the distinct messages the inbox kept, for run.
func (f *fakeInbox) messages(run domain.RunID) []notifyCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []notifyCall
	for _, c := range f.kept {
		if c.to == run {
			out = append(out, c)
		}
	}
	return out
}

type waitHarness struct {
	*harness
	inbox *fakeInbox
	keys  atomic.Int64
}

func newWaitHarness(t *testing.T) *waitHarness {
	t.Helper()
	h := &waitHarness{harness: newHarness(t), inbox: &fakeInbox{kept: map[string]notifyCall{}}}
	_ = h.svc.Close()
	h.restart()
	return h
}

// restart runs a new Service with the inbox on the same store, as a
// server start does.
func (h *waitHarness) restart() {
	h.t.Helper()
	_ = h.svc.Close()
	svc, err := New(Config{
		Store: h.db, Bus: h.bus, Runs: h.runs, Swarm: h.swarmOf, Push: h.push, Head: h.log.LastSeq,
		Debounce: 50 * time.Millisecond, Tick: 50 * time.Millisecond, Now: h.clock.now,
	})
	if err != nil {
		h.t.Fatalf("New: %v", err)
	}
	if h.pr != nil {
		svc.UsePRSource(h.pr)
	}
	svc.UseRunNotifier(h.inbox)
	if err = svc.Start(h.ctx); err != nil {
		h.t.Fatalf("Start: %v", err)
	}
	h.t.Cleanup(func() { _ = svc.Close() })
	h.svc = svc
}

// report registers owner's waiting report on until under key, as
// coord.report does, and has the conditions read.
func (h *waitHarness) report(owner *domain.Run, key string, until ...string) (bool, error) {
	h.t.Helper()
	specs := make([]protocol.WaitSpec, 0, len(until))
	texts := make([]string, 0, len(until))
	for _, raw := range until {
		spec, err := protocol.ParseWaitSpec(raw)
		if err != nil {
			h.t.Fatalf("ParseWaitSpec: %v", err)
		}
		specs, texts = append(specs, spec), append(texts, spec.Spec)
	}
	inserted, err := h.svc.RegisterWait(h.ctx, owner, &store.CoordStateReport{
		RunID: owner.ID, IdempotencyKey: key, Outcome: protocol.CoordOutcomeWaiting, Summary: "waiting", Until: texts,
	}, specs)
	if err == nil {
		h.svc.EvaluateRunWaits(owner.ID)
	}
	return inserted, err
}

func (h *waitHarness) mustReport(owner *domain.Run, until ...string) {
	h.t.Helper()
	if _, err := h.report(owner, fmt.Sprintf("k%d", h.keys.Add(1)), until...); err != nil {
		h.t.Fatalf("report: %v", err)
	}
}

func (h *waitHarness) waits(run domain.RunID) []protocol.WaitCondition {
	h.t.Helper()
	waits, err := h.svc.RunWaits(h.ctx, run)
	if err != nil {
		h.t.Fatalf("RunWaits: %v", err)
	}
	return waits
}

func (h *waitHarness) only(run domain.RunID) protocol.WaitCondition {
	h.t.Helper()
	waits := h.waits(run)
	if len(waits) != 1 {
		h.t.Fatalf("waits = %+v, want one", waits)
	}
	return waits[0]
}

func finished(id domain.RunID) string { return "run:" + string(id) + ":finished" }

func TestAWaitOnARunThatAlreadyEndedWakesAtOnce(t *testing.T) {
	t.Parallel()
	h := newWaitHarness(t)
	owner, target := h.run(h.ada, "Integrate"), h.run(h.ada, "Build")
	h.status(target, domain.RunFailed, "", "tests failed")
	h.mustReport(owner, finished(target.ID))
	h.eventually("the wake", func() bool { return h.only(owner.ID).DeliveredAt != "" })
	w := h.only(owner.ID)
	if w.State != protocol.WaitResolved || w.Outcome != "failed" || w.DeliveredAt == "" || w.DeliveryAttempts != 1 {
		t.Fatalf("wait = %+v, want resolved failed and delivered once", w)
	}
	m := h.inbox.messages(owner.ID)[0]
	if m.from != owner.ID || m.corr != w.ID || !strings.Contains(m.body, "ended failed: tests failed") || !strings.Contains(m.body, "Next: ") {
		t.Fatalf("message = %+v", m)
	}
}

// The target ends just before, during or after the report registers the
// wait: every order resolves it, and wakes the run once.
func TestAnEndRacingTheRegistrationIsNeverMissed(t *testing.T) {
	t.Parallel()
	h := newWaitHarness(t)
	for i := range 10 {
		owner, target := h.run(h.ada, fmt.Sprintf("Owner %d", i)), h.run(h.ada, fmt.Sprintf("Target %d", i))
		var wg sync.WaitGroup
		wg.Go(func() { h.status(target, domain.RunCompleted, "", "") })
		wg.Go(func() { h.mustReport(owner, finished(target.ID)) })
		wg.Wait()
		h.eventually(fmt.Sprintf("wake %d", i), func() bool { return len(h.inbox.messages(owner.ID)) == 1 })
	}
	settle()
	h.inbox.mu.Lock()
	defer h.inbox.mu.Unlock()
	if len(h.inbox.calls) != 10 {
		t.Fatalf("Notify called %d times for 10 waits", len(h.inbox.calls))
	}
}

func TestARetriedReportRegistersOneWait(t *testing.T) {
	t.Parallel()
	h := newWaitHarness(t)
	owner, target := h.run(h.ada, "Integrate"), h.run(h.ada, "Build")
	for range 3 {
		if _, err := h.report(owner, "same", finished(target.ID)); err != nil {
			t.Fatal(err)
		}
	}
	if w := h.only(owner.ID); w.Revision != 1 || w.State != protocol.WaitPending {
		t.Fatalf("wait = %+v, want revision 1 pending", w)
	}
	other := h.run(h.ada, "Docs")
	if _, err := h.report(owner, "same", finished(other.ID)); !errors.Is(err, store.ErrCoordReportIdempotencyConflict) {
		t.Fatalf("same key with other conditions: %v, want an idempotency conflict", err)
	}
}

func TestANewReportSupersedesAndAnotherOutcomeCancels(t *testing.T) {
	t.Parallel()
	h := newWaitHarness(t)
	owner, a, b := h.run(h.ada, "Integrate"), h.run(h.ada, "A"), h.run(h.ada, "B")
	h.mustReport(owner, finished(a.ID))
	first := h.only(owner.ID)
	h.mustReport(owner, finished(b.ID))
	second := h.only(owner.ID)
	if second.Revision != 2 || second.RunID != string(b.ID) {
		t.Fatalf("latest = %+v, want revision 2 on %s", second, b.ID)
	}
	old, err := h.db.GetRunWait(h.ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if old.WaitState != protocol.WaitSuperseded || old.Cause == "" {
		t.Fatalf("first = %s (%q), want superseded with a cause", old.WaitState, old.Cause)
	}
	h.status(a, domain.RunCompleted, "", "")
	if err := h.svc.EndRunWaits(h.ctx, owner.ID, "the run reported answered"); err != nil {
		t.Fatal(err)
	}
	h.status(b, domain.RunCompleted, "", "")
	settle()
	if w := h.only(owner.ID); w.State != protocol.WaitCancelled || w.EndedCause != "the run reported answered" {
		t.Fatalf("wait = %+v, want cancelled by the report", w)
	}
	if n := len(h.inbox.messages(owner.ID)); n != 0 {
		t.Fatalf("%d wakes after the waits ended, want none", n)
	}
}

func TestTheFirstConditionWakesOnceAndEndsTheOthers(t *testing.T) {
	t.Parallel()
	h := newWaitHarness(t)
	owner, a, b := h.run(h.ada, "Integrate"), h.run(h.ada, "A"), h.run(h.ada, "B")
	h.mustReport(owner, finished(a.ID), finished(b.ID))
	h.status(a, domain.RunCompleted, "", "")
	h.eventually("the wake", func() bool { return len(h.inbox.messages(owner.ID)) == 1 })
	h.status(b, domain.RunAbandoned, "", "")
	settle()
	states := map[string]string{}
	for _, w := range h.waits(owner.ID) {
		states[w.RunID] = w.State
	}
	if states[string(a.ID)] != protocol.WaitResolved || states[string(b.ID)] != protocol.WaitCancelled || len(h.inbox.messages(owner.ID)) != 1 {
		t.Fatalf("states = %v, %d wakes; want a resolved, b cancelled, one wake", states, len(h.inbox.messages(owner.ID)))
	}
}

func TestAPendingWakeSurvivesARestartAndIsSentOnce(t *testing.T) {
	t.Parallel()
	h := newWaitHarness(t)
	owner, target := h.run(h.ada, "Integrate"), h.run(h.ada, "Build")
	h.inbox.fail.Store(true)
	h.mustReport(owner, finished(target.ID))
	h.status(target, domain.RunCompleted, "", "")
	h.eventually("a failed attempt", func() bool {
		w := h.only(owner.ID)
		return w.State == protocol.WaitResolved && w.DeliveryAttempts >= 1 && w.LastDeliveryError == "inbox unavailable"
	})
	_ = h.svc.Close()
	h.inbox.fail.Store(false)
	h.restart()
	h.clock.advance(maxBackoff)
	h.eventually("the wake", func() bool { return h.only(owner.ID).DeliveredAt != "" })
	settle()
	if n := len(h.inbox.messages(owner.ID)); n != 1 {
		t.Fatalf("%d wakes, want one", n)
	}
	if w := h.only(owner.ID); w.LastDeliveryError != "" {
		t.Fatalf("last delivery error = %q after delivery", w.LastDeliveryError)
	}
}

// A server that stops between the report's commit and the first read
// reads the wait when it starts.
func TestAWaitRegisteredBeforeACrashIsReadOnStart(t *testing.T) {
	t.Parallel()
	h := newWaitHarness(t)
	owner, target := h.run(h.ada, "Integrate"), h.run(h.ada, "Build")
	h.status(target, domain.RunMerged, "", "")
	_ = h.svc.Close()
	spec, _ := protocol.ParseWaitSpec(finished(target.ID))
	if _, err := h.svc.RegisterWait(h.ctx, owner, &store.CoordStateReport{
		RunID: owner.ID, IdempotencyKey: "k", Outcome: protocol.CoordOutcomeWaiting, Summary: "waiting", Until: []string{spec.Spec},
	}, []protocol.WaitSpec{spec}); err != nil {
		t.Fatal(err)
	}
	h.restart()
	h.eventually("the wake", func() bool { return len(h.inbox.messages(owner.ID)) == 1 })
	if w := h.only(owner.ID); w.Outcome != "merged" {
		t.Fatalf("outcome = %q, want merged", w.Outcome)
	}
}

func TestAPausedRunGetsItsWakeInItsInbox(t *testing.T) {
	t.Parallel()
	h := newWaitHarness(t)
	owner, target := h.run(h.ada, "Integrate"), h.run(h.ada, "Build")
	h.mustReport(owner, finished(target.ID))
	h.runs.pause(owner.ID, true)
	h.status(target, domain.RunCompleted, "", "")
	h.eventually("the message", func() bool { return len(h.inbox.messages(owner.ID)) == 1 })
	notes, err := h.db.ListWatchNotifications(h.ctx, h.only(owner.ID).ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 || len(notes[0].Deliveries) != 1 || notes[0].Deliveries[0].Channel != protocol.WatchChannelRunInbox ||
		!strings.Contains(notes[0].Deliveries[0].Detail, "paused") {
		t.Fatalf("notices = %+v, want one run_inbox delivery saying the run is paused", notes)
	}
}

func TestAnEndedOwnerCancelsItsWaitsAndUndeliveredWake(t *testing.T) {
	t.Parallel()
	h := newWaitHarness(t)
	owner, target := h.run(h.ada, "Integrate"), h.run(h.ada, "Build")
	h.mustReport(owner, finished(target.ID))
	h.status(owner, domain.RunAbandoned, "", "")
	h.eventually("the cancel", func() bool { return h.only(owner.ID).State == protocol.WaitCancelled })
	h.status(target, domain.RunCompleted, "", "")
	settle()
	if n := len(h.inbox.messages(owner.ID)); n != 0 {
		t.Fatalf("%d wakes to an ended run", n)
	}

	other := h.run(h.ada, "Review")
	h.inbox.fail.Store(true)
	h.mustReport(other, finished(target.ID))
	h.eventually("a failed attempt", func() bool { return h.only(other.ID).DeliveryAttempts >= 1 })
	h.status(other, domain.RunInterrupted, "", "")
	h.inbox.fail.Store(false)
	h.clock.advance(maxBackoff)
	h.eventually("the wake dropped", func() bool {
		w := h.only(other.ID)
		return w.LastDeliveryError != "" && strings.HasPrefix(w.LastDeliveryError, "Not delivered: the run ended")
	})
	if n := len(h.inbox.messages(other.ID)); n != 0 {
		t.Fatalf("%d wakes to an ended run", n)
	}
}

// Delivery goes to the run that reported, never to the run it waits on,
// and a run can wait only on a run its member sees in its workspace.
func TestAWaitWakesOnlyItsOwnRun(t *testing.T) {
	t.Parallel()
	h := newWaitHarness(t)
	owner, target := h.run(h.ada, "Integrate"), h.run(h.bob, "Build")
	h.mustReport(owner, finished(target.ID))
	h.status(target, domain.RunCompleted, "", "")
	h.eventually("the wake", func() bool { return len(h.inbox.messages(owner.ID)) == 1 })
	h.inbox.mu.Lock()
	for _, c := range h.inbox.calls {
		if c.from != owner.ID || c.to != owner.ID {
			t.Errorf("Notify(%s -> %s), want the owner to itself", c.from, c.to)
		}
	}
	h.inbox.mu.Unlock()

	ws := &domain.Workspace{Name: "elsewhere", BaseBranch: domain.DefaultBaseBranch}
	if err := h.db.CreateWorkspace(h.ctx, ws); err != nil {
		t.Fatal(err)
	}
	foreign := &domain.Run{WorkspaceID: ws.ID, MemberID: h.bob.ID, Task: "Elsewhere", Harness: "claude", Mode: domain.LaunchACP, ACP: true, Status: domain.RunRunning}
	if err := h.db.CreateRun(h.ctx, foreign); err != nil {
		t.Fatal(err)
	}
	for _, until := range []string{finished(foreign.ID), finished(owner.ID), "run:run-nosuchrun0:finished"} {
		if _, err := h.report(owner, "refused-"+until, until); !errors.Is(err, ErrInvalid) {
			t.Errorf("until %s: %v, want refused", until, err)
		}
	}
}

func TestAWaitThatNothingResolvesExpiresAndWakes(t *testing.T) {
	t.Parallel()
	h := newWaitHarness(t)
	owner, target := h.run(h.ada, "Integrate"), h.run(h.ada, "Build")
	h.mustReport(owner, finished(target.ID))
	h.eventually("the first read", func() bool { return h.only(owner.ID).LastCheckedAt != "" })
	h.clock.advance(WaitExpiry + time.Minute)
	h.eventually("the wake", func() bool { return len(h.inbox.messages(owner.ID)) == 1 })
	if w := h.only(owner.ID); w.State != protocol.WaitExpired {
		t.Fatalf("state = %s, want expired", w.State)
	}
	if got, err := h.db.GetWatch(h.ctx, h.only(owner.ID).ID); err != nil || got.State != protocol.WatchExpired {
		t.Fatalf("watch = %+v, %v; want expired, so it is purged", got, err)
	}
}

func TestAPersonsPromptCancelsAndWaitsStayOffTheWatchList(t *testing.T) {
	t.Parallel()
	h := newWaitHarness(t)
	owner, target := h.run(h.ada, "Integrate"), h.run(h.ada, "Build")
	h.mustReport(owner, finished(target.ID))
	list, err := h.svc.List(h.ctx, h.ada.ID, protocol.WatchListParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Watches) != 0 {
		t.Fatalf("watch.list = %+v, want the run's waits left out", list.Watches)
	}
	if _, err = h.svc.Get(h.ctx, h.ada.ID, h.only(owner.ID).ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("watch.get on a wait: %v, want not found", err)
	}
	if err = h.svc.EndRunWaits(h.ctx, owner.ID, "a person sent a new prompt"); err != nil {
		t.Fatal(err)
	}
	if w := h.only(owner.ID); w.State != protocol.WaitCancelled || w.EndedCause != "a person sent a new prompt" {
		t.Fatalf("wait = %+v", w)
	}
	got, err := h.svc.CancelRunWait(h.ctx, target.ID, h.only(owner.ID).ID, h.ada.ID)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cancel through another run: %+v, %v; want not found", got, err)
	}
	h.mustReport(owner, finished(target.ID))
	got, err = h.svc.CancelRunWait(h.ctx, owner.ID, h.only(owner.ID).ID, h.bob.ID)
	if err != nil || got.State != protocol.WaitCancelled || got.EndedCause != protocol.WaitCancelled || got.EndedByMemberID != string(h.bob.ID) {
		t.Fatalf("run.wait.cancel = %+v, %v; want cancelled by bob", got, err)
	}
}

func TestRunWaitChangedIsPublished(t *testing.T) {
	t.Parallel()
	h := newWaitHarness(t)
	sub, err := h.bus.Subscribe(h.ctx, events.SubscribeOptions{Filter: events.Filter{Types: []events.Type{events.TypeRunWaitChanged}}, Buffer: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close() //nolint:errcheck // test cleanup
	owner, target := h.run(h.ada, "Integrate"), h.run(h.ada, "Build")
	h.mustReport(owner, finished(target.ID))
	h.status(target, domain.RunCompleted, "", "")
	var states []string
	deadline := time.After(10 * time.Second)
	for !strings.HasSuffix(strings.Join(states, ","), protocol.WaitResolved+","+protocol.WaitResolved) {
		select {
		case e := <-sub.Events():
			p := e.Payload.(events.RunWaitChangedPayload)
			if p.RunID != owner.ID || p.WorkspaceID != h.ws.ID || e.RunID != owner.ID {
				t.Fatalf("event = %+v", e)
			}
			states = append(states, p.State)
		case <-deadline:
			t.Fatalf("states = %v, want pending, resolved, then the delivery", states)
		}
	}
}

// newWaitPRHarness replaces http.DefaultTransport, so its tests do not run
// in parallel.
func newWaitPRHarness(t *testing.T) (*waitHarness, *fakeGitHub) {
	t.Helper()
	p := newPRHarness(t)
	prs, err := NewPRs(PRConfig{GitHub: github.New(p.homes), Store: p.db, Bus: p.bus, Now: p.clock.now})
	if err != nil {
		t.Fatal(err)
	}
	p.pr = prs
	h := &waitHarness{harness: p.harness, inbox: &fakeInbox{kept: map[string]notifyCall{}}}
	h.restart()
	return h, p.gh
}

func TestAPullRequestClosedUnmergedWakesWithAFailure(t *testing.T) {
	h, gh := newWaitPRHarness(t)
	gh.set(func(f *fakeGitHub) { f.state = "closed" })
	owner := h.run(h.ada, "Ship")
	h.mustReport(owner, "pr:Acme/App#7:merged")
	h.eventually("the wake", func() bool { return len(h.inbox.messages(owner.ID)) == 1 })
	w := h.only(owner.ID)
	if w.Spec != "pr:acme/app#7:merged" || w.State != protocol.WaitResolved || w.Outcome != "closed" || w.Observed.PRState != "closed" {
		t.Fatalf("wait = %+v, want resolved closed", w)
	}
	if body := h.inbox.messages(owner.ID)[0].body; !strings.Contains(body, "closed without merging") || !strings.Contains(body, "https://github.com/acme/app/pull/7") {
		t.Fatalf("message = %q", body)
	}
}

func TestFailedChecksWakeWithTheFailingNames(t *testing.T) {
	h, gh := newWaitPRHarness(t)
	gh.set(func(f *fakeGitHub) {
		f.checks[shaOne] = []fakeCheck{{ID: 1, Name: "lint", Status: "in_progress"}, {ID: 2, Name: "test", Status: "completed", Conclusion: "success", Completed: "2026-10-27T00:00:00Z"}}
	})
	owner := h.run(h.ada, "Ship")
	h.mustReport(owner, "pr:acme/app#7:checks")
	h.eventually("the first read", func() bool { return h.only(owner.ID).SourceCheckedAt != "" })
	if w := h.only(owner.ID); w.State != protocol.WaitPending || w.Observed.Checks == nil || w.Observed.Checks.Pending != 1 {
		t.Fatalf("wait = %+v, want pending with one check running", w)
	}
	gh.set(func(f *fakeGitHub) {
		f.checks[shaOne][0] = fakeCheck{ID: 1, Name: "lint", Status: "completed", Conclusion: "failure", Completed: "2026-10-27T00:01:00Z"}
	})
	h.clock.advance(2 * PRPollBusy)
	h.eventually("the wake", func() bool { return len(h.inbox.messages(owner.ID)) == 1 })
	w := h.only(owner.ID)
	if w.Outcome != "failed" || !strings.Contains(w.Summary, "failed: lint") || w.Observed.Checks.Failed != 1 || w.Observed.HeadSHA != shaOne {
		t.Fatalf("wait = %+v, want failed naming lint", w)
	}
	if body := h.inbox.messages(owner.ID)[0].body; !strings.Contains(body, "fix them and push") {
		t.Fatalf("message = %q, want what to do next", body)
	}
}

func TestAPullRequestGitHubCannotFindWakesWithAnError(t *testing.T) {
	h, _ := newWaitPRHarness(t)
	owner := h.run(h.ada, "Ship")
	h.mustReport(owner, "pr:acme/gone#1:merged")
	h.eventually("the wake", func() bool { return len(h.inbox.messages(owner.ID)) == 1 })
	if w := h.only(owner.ID); w.State != protocol.WaitError || !strings.Contains(w.LastError, "not reachable with your GitHub login") {
		t.Fatalf("wait = %+v, want error saying GitHub cannot find it", w)
	}
}
