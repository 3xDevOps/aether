package push

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/store"
	"github.com/3xDevOps/Aether/internal/store/storetest"
)

const testQuiet = 300 * time.Millisecond

type fakeRuns struct {
	mu     sync.Mutex
	inputs map[domain.RunID][]domain.RunInputRequest
	paused atomic.Bool
}

func (f *fakeRuns) PendingInputs(run domain.RunID) []domain.RunInputRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.inputs[run]
}

func (f *fakeRuns) Paused(domain.RunID) bool { return f.paused.Load() }

func (f *fakeRuns) set(run domain.RunID, inputs ...domain.RunInputRequest) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inputs[run] = inputs
}

// device is a subscribed browser and the push service in front of it: what
// arrives is decrypted with the browser's keys before the test sees it.
type device struct {
	*browser
	service  *httptest.Server
	received chan notification
	topic    atomic.Value
	urgency  atomic.Value
	answer   atomic.Int32
	// While slow is set, the push service takes a message and answers only
	// once release is closed.
	slow    atomic.Bool
	release chan struct{}
}

func (d *device) endpoint() string { return d.service.URL + "/send/device" }

type harness struct {
	t    *testing.T
	ctx  context.Context
	db   *store.DB
	bus  *events.InProc
	runs *fakeRuns
	svc  *Service
	ws   *domain.Workspace
	ada  *domain.Member
	bob  *domain.Member
	keys int

	keyPath string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()
	db, err := storetest.Open(filepath.Join(t.TempDir(), "aether.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	bus, err := events.NewInProc(ctx, nil)
	if err != nil {
		t.Fatalf("bus: %v", err)
	}
	t.Cleanup(func() { _ = bus.Close() })
	h := &harness{t: t, ctx: ctx, db: db, bus: bus, runs: &fakeRuns{inputs: map[domain.RunID][]domain.RunInputRequest{}}}
	h.ws = &domain.Workspace{Name: "proj", BaseBranch: domain.DefaultBaseBranch}
	if err = db.CreateWorkspace(ctx, h.ws); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	h.ada, h.bob = h.member("Ada"), h.member("Bob")

	h.keyPath = filepath.Join(t.TempDir(), "push", "vapid_key.pem")
	h.start()
	return h
}

// start runs a new Service on the harness's store, as a server start does.
func (h *harness) start() {
	h.t.Helper()
	// The fake push services are loopback, which the real client refuses,
	// and each presents its own self-signed certificate.
	insecure := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // test servers only
	}}
	svc, err := New(Config{Store: h.db, Bus: h.bus, Runs: h.runs, KeyPath: h.keyPath, Client: insecure, Quiet: testQuiet})
	if err != nil {
		h.t.Fatalf("New: %v", err)
	}
	if err = svc.Start(h.ctx); err != nil {
		h.t.Fatalf("Start: %v", err)
	}
	h.t.Cleanup(func() { _ = svc.Close() })
	h.svc = svc
}

func (h *harness) member(name string) *domain.Member {
	h.t.Helper()
	h.keys++
	m := &domain.Member{DisplayName: name, TailnetLogin: fmt.Sprintf("%s-%d@example.com", strings.ToLower(name), h.keys), Color: "#e6194b", Role: domain.RoleCollaborator}
	if err := h.db.CreateMember(h.ctx, m); err != nil {
		h.t.Fatalf("create member: %v", err)
	}
	return m
}

// subscribe gives member a new device with notifications on.
func (h *harness) subscribe(member *domain.Member) *device {
	h.t.Helper()
	d := &device{browser: newBrowser(h.t), received: make(chan notification, 8), release: make(chan struct{})}
	d.answer.Store(http.StatusCreated)
	d.service = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if status := int(d.answer.Load()); status != http.StatusCreated {
			http.Error(w, "unsubscribed", status)
			return
		}
		plain, err := d.open(body)
		if err != nil {
			h.t.Errorf("message does not decrypt with the subscription's keys: %v", err)
			return
		}
		var n notification
		if err = json.Unmarshal(plain, &n); err != nil {
			h.t.Errorf("payload %q is not a notification: %v", plain, err)
			return
		}
		d.topic.Store(r.Header.Get("Topic"))
		d.urgency.Store(r.Header.Get("Urgency"))
		d.received <- n
		if d.slow.Load() {
			<-d.release
		}
		w.WriteHeader(http.StatusCreated)
	}))
	h.t.Cleanup(d.service.Close)
	if err := h.svc.Subscribe(h.ctx, member.ID, d.endpoint(), d.p256dh(), d.authKey()); err != nil {
		h.t.Fatalf("Subscribe: %v", err)
	}
	return d
}

func (h *harness) run(owner *domain.Member, task string, mode domain.LaunchMode) *domain.Run {
	h.t.Helper()
	run := &domain.Run{
		WorkspaceID: h.ws.ID, MemberID: owner.ID, Task: task, Harness: "claude",
		Mode: mode, ACP: mode == domain.LaunchACP, Status: domain.RunRunning,
	}
	if err := h.db.CreateRun(h.ctx, run); err != nil {
		h.t.Fatalf("create run: %v", err)
	}
	return run
}

// status moves the run the way the scheduler does: the row, then the event.
func (h *harness) status(run *domain.Run, to domain.RunStatus, activity domain.RunActivity, reason string) {
	h.t.Helper()
	if _, err := h.db.UpdateRunStatus(h.ctx, run.ID, to, reason, activity, nil, nil); err != nil {
		h.t.Fatalf("UpdateRunStatus: %v", err)
	}
	h.publish(run, events.RunStatusPayload{To: to, Reason: reason, Activity: activity})
}

func (h *harness) publish(run *domain.Run, payload events.Payload) {
	h.t.Helper()
	if _, err := h.bus.Publish(h.ctx, events.Event{WorkspaceID: run.WorkspaceID, RunID: run.ID, Payload: payload}); err != nil {
		h.t.Fatalf("publish: %v", err)
	}
}

func (d *device) expect(t *testing.T, want notification) {
	t.Helper()
	select {
	case got := <-d.received:
		if got != want {
			t.Fatalf("notification = %+v, want %+v", got, want)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("no notification arrived, want %+v", want)
	}
}

func (d *device) expectNone(t *testing.T, within time.Duration) {
	t.Helper()
	select {
	case got := <-d.received:
		t.Fatalf("unexpected notification %+v", got)
	case <-time.After(within):
	}
}

func TestOwnerIsNotifiedOnceWhileANeedStands(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	phone, laptop, bobs := h.subscribe(h.ada), h.subscribe(h.ada), h.subscribe(h.bob)
	run := h.run(h.ada, "Fix the login redirect\n\nIt loops on Safari.", domain.LaunchACP)
	want := notification{Title: "Fix the login redirect", Body: "which region?", Run: string(run.ID)}

	// A turn end, a stall and a wait on workers need nobody.
	h.status(run, domain.RunNeedsAttention, domain.ActivityIdle, "agent idle")
	h.status(run, domain.RunNeedsAttention, domain.ActivityIdle, "stalled: no output or file changes for 10m0s")
	h.status(run, domain.RunNeedsAttention, domain.ActivityWaitingAgents, "waiting on 3 workers")
	h.status(run, domain.RunRunning, domain.ActivityWaitingTools, "working in background: 1 task")
	phone.expectNone(t, testQuiet)

	h.status(run, domain.RunNeedsAttention, domain.ActivityNeedsYou, "needs input: which region?")
	phone.expect(t, want)
	laptop.expect(t, want)

	// The same standing state, published again.
	h.publish(run, events.RunStatusPayload{From: domain.RunNeedsAttention, To: domain.RunNeedsAttention, Reason: "needs input: which region?"})
	phone.expectNone(t, testQuiet)

	h.status(run, domain.RunRunning, domain.ActivityWorking, "agent resumed")
	h.status(run, domain.RunNeedsAttention, domain.ActivityNeedsYou, "blocked: need the staging API key")
	want.Body = "Blocked: need the staging API key"
	phone.expect(t, want)

	// A handoff moves the notifications with the run, starting with what it
	// needs as it changes hands.
	if err := h.db.TransferRun(h.ctx, run.ID, h.bob.ID); err != nil {
		t.Fatalf("TransferRun: %v", err)
	}
	h.publish(run, events.TimelinePayload{Kind: events.TimelineHandoff, Message: string(h.bob.ID)})
	bobs.expect(t, want)
	h.status(run, domain.RunRunning, domain.ActivityWorking, "agent resumed")
	h.status(run, domain.RunNeedsAttention, domain.ActivityNeedsYou, "needs input: which region?")
	want.Body = "which region?"
	bobs.expect(t, want)
	phone.expectNone(t, testQuiet)
}

func TestRequestsAndReportedFinishesAreAnnounced(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	phone := h.subscribe(h.ada)
	run := h.run(h.ada, "Upgrade the linter", domain.LaunchACP)
	id := string(run.ID)

	h.runs.set(run.ID, domain.RunInputRequest{ID: "q1", SessionID: "s1", Kind: "question"})
	h.publish(run, events.RunInputPayload{PendingInputs: h.runs.PendingInputs(run.ID)})
	phone.expect(t, notification{Title: "Upgrade the linter", Body: "Question from the agent", Run: id})

	// Answered, then the turn ends: one notification, for the new need.
	h.runs.set(run.ID)
	h.publish(run, events.RunInputPayload{PendingInputs: []domain.RunInputRequest{}})
	h.status(run, domain.RunNeedsAttention, domain.ActivityNeedsYou, "needs input: which region?")
	phone.expect(t, notification{Title: "Upgrade the linter", Body: "which region?", Run: id})

	h.status(run, domain.RunRunning, domain.ActivityWorking, "agent resumed")
	finished := time.Now().UTC()
	if _, err := h.db.FinishRunReported(h.ctx, run.ID, domain.RunCompleted, "agent reported success", "", nil, &finished); err != nil {
		t.Fatalf("FinishRunReported: %v", err)
	}
	h.publish(run, events.RunStatusPayload{From: domain.RunRunning, To: domain.RunCompleted, OutcomeUnseen: true})
	phone.expect(t, notification{Title: "Upgrade the linter", Body: "Agent reported success, review the result", Run: id})
	stamp, _, err := h.db.SetRunReview(h.ctx, run.ID, domain.ReviewAccepted, domain.ReviewReady)
	if err != nil {
		t.Fatalf("SetRunReview: %v", err)
	}
	h.publish(run, events.RunReviewPayload{Review: stamp.Review, StatusSeq: stamp.StatusSeq})
	phone.expectNone(t, testQuiet)

	// A member closing a run of their own is not news to them.
	closed := h.run(h.ada, "Try the other approach", domain.LaunchTUI)
	if _, err := h.db.FinishRunByMember(h.ctx, closed.ID, domain.RunAbandoned, "", "", nil, &finished); err != nil {
		t.Fatalf("FinishRunByMember: %v", err)
	}
	h.publish(closed, events.RunStatusPayload{From: domain.RunRunning, To: domain.RunAbandoned})
	phone.expectNone(t, testQuiet)
}

// Opening a run bumps its status_seq, not its need: a teammate opening a
// failure, and a later event, must not push it to the owner again. A
// finished result does not wake the device; a failure does.
func TestAnOpenedNeedIsNotPushedAgain(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	phone := h.subscribe(h.ada)
	run := h.run(h.ada, "Upgrade the linter", domain.LaunchACP)
	id := string(run.ID)

	if _, err := h.db.FinishRunReported(h.ctx, run.ID, domain.RunNeedsAttention, "agent reported failure", domain.ActivityFailed, nil, nil); err != nil {
		t.Fatalf("FinishRunReported: %v", err)
	}
	h.publish(run, events.RunStatusPayload{To: domain.RunNeedsAttention, Activity: domain.ActivityFailed, OutcomeUnseen: true})
	phone.expect(t, notification{Title: "Upgrade the linter", Body: "Agent reported failure, review the result", Run: id})
	if got := phone.urgency.Load(); got != urgencyHigh {
		t.Errorf("failure urgency = %v, want %s", got, urgencyHigh)
	}

	if _, err := h.db.ClearRunFinishUnopened(h.ctx, run.ID); err != nil {
		t.Fatalf("ClearRunFinishUnopened: %v", err)
	}
	h.publish(run, events.RunOutcomeSeenPayload{})
	h.publish(run, events.TimelinePayload{Kind: events.TimelineSteer, Message: "look again"})
	phone.expectNone(t, testQuiet)

	h.status(run, domain.RunRunning, domain.ActivityWorking, "agent resumed")
	if _, err := h.db.FinishRunReported(h.ctx, run.ID, domain.RunNeedsAttention, "agent reported success", domain.ActivityFinished, nil, nil); err != nil {
		t.Fatalf("FinishRunReported: %v", err)
	}
	h.publish(run, events.RunStatusPayload{To: domain.RunNeedsAttention, Activity: domain.ActivityFinished, OutcomeUnseen: true})
	phone.expect(t, notification{Title: "Upgrade the linter", Body: "Agent reported success, review the result", Run: id})
	if got := phone.urgency.Load(); got != urgencyNormal {
		t.Errorf("finished urgency = %v, want %s", got, urgencyNormal)
	}
}

// A need that stood before the server started, such as one the first start
// after an upgrade backfills a state for, was announced by the server before.
func TestARestartDoesNotAnnounceStandingNeeds(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	phone := h.subscribe(h.ada)
	run := h.run(h.ada, "Upgrade the linter", domain.LaunchACP)
	h.status(run, domain.RunNeedsAttention, domain.ActivityNeedsYou, "needs input: which region?")
	phone.expect(t, notification{Title: "Upgrade the linter", Body: "which region?", Run: string(run.ID)})

	if err := h.svc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	h.start()
	h.publish(run, events.RunStatusPayload{To: domain.RunNeedsAttention, Activity: domain.ActivityNeedsYou, Reason: "needs input: which region?"})
	phone.expectNone(t, testQuiet)

	h.status(run, domain.RunNeedsAttention, domain.ActivityNeedsYou, "blocked: need the staging API key")
	phone.expect(t, notification{Title: "Upgrade the linter", Body: "Blocked: need the staging API key", Run: string(run.ID)})
}

// Every notification for a run replaces the last one on a device, so an
// older one must never arrive after a newer one, however slow a push service
// is.
func TestANewerNeedIsNeverOvertakenByAnOlderOne(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	slow, phone := h.subscribe(h.ada), h.subscribe(h.ada)
	slow.slow.Store(true)
	run := h.run(h.ada, "Upgrade the linter", domain.LaunchACP)
	idle := notification{Title: "Upgrade the linter", Body: "which region?", Run: string(run.ID)}
	permission := notification{Title: "Upgrade the linter", Body: "Permission requested", Run: string(run.ID)}

	h.status(run, domain.RunNeedsAttention, domain.ActivityNeedsYou, "needs input: which region?")
	slow.expect(t, idle)
	// The first device's push service has not answered yet when the run
	// asks for a permission.
	h.runs.set(run.ID, domain.RunInputRequest{ID: "p1", SessionID: "s1", Kind: "permission"})
	h.publish(run, events.RunInputPayload{PendingInputs: h.runs.PendingInputs(run.ID)})
	phone.expectNone(t, testQuiet)
	slow.slow.Store(false)
	close(slow.release)

	slow.expect(t, permission)
	phone.expect(t, permission)
	phone.expectNone(t, testQuiet)
}

// A need that came and went while an older announcement was still being
// sent is not announced afterwards, and the older one, standing again,
// still reaches the remaining devices.
func TestANeedThatClearedBehindASlowSendIsNotSent(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	slow, phone := h.subscribe(h.ada), h.subscribe(h.ada)
	slow.slow.Store(true)
	run := h.run(h.ada, "Upgrade the linter", domain.LaunchACP)
	idle := notification{Title: "Upgrade the linter", Body: "which region?", Run: string(run.ID)}

	h.status(run, domain.RunNeedsAttention, domain.ActivityNeedsYou, "needs input: which region?")
	slow.expect(t, idle)
	h.runs.set(run.ID, domain.RunInputRequest{ID: "p1", SessionID: "s1", Kind: "permission"})
	h.publish(run, events.RunInputPayload{PendingInputs: h.runs.PendingInputs(run.ID)})
	h.runs.set(run.ID)
	h.publish(run, events.RunInputPayload{PendingInputs: []domain.RunInputRequest{}})
	phone.expectNone(t, testQuiet)
	slow.slow.Store(false)
	close(slow.release)

	phone.expect(t, idle)
	phone.expectNone(t, testQuiet)
	slow.expectNone(t, testQuiet)
}

// A handoff commits while the former owner's first device is still being
// sent to: their other devices get nothing more, and the new owner is told
// what the run needs.
func TestAHandedOffRunNotifiesOnlyItsNewOwner(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	slow, phone, bobs := h.subscribe(h.ada), h.subscribe(h.ada), h.subscribe(h.bob)
	slow.slow.Store(true)
	run := h.run(h.ada, "Upgrade the linter", domain.LaunchACP)
	idle := notification{Title: "Upgrade the linter", Body: "which region?", Run: string(run.ID)}

	h.status(run, domain.RunNeedsAttention, domain.ActivityNeedsYou, "needs input: which region?")
	slow.expect(t, idle)
	if err := h.db.TransferRun(h.ctx, run.ID, h.bob.ID); err != nil {
		t.Fatalf("TransferRun: %v", err)
	}
	h.publish(run, events.TimelinePayload{Kind: events.TimelineHandoff, Message: string(h.bob.ID)})
	slow.slow.Store(false)
	close(slow.release)

	phone.expectNone(t, testQuiet)
	bobs.expect(t, idle)
	bobs.expectNone(t, testQuiet)
}

// The owner comes back to a dashboard while a slow push service holds their
// first device: the rest wait until the owner has left it alone again, and
// the first is not sent the same need twice.
func TestAnOwnerWhoCameBackIsNotInterruptedBehindASlowSend(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	slow, phone := h.subscribe(h.ada), h.subscribe(h.ada)
	slow.slow.Store(true)
	run := h.run(h.ada, "Upgrade the linter", domain.LaunchACP)
	idle := notification{Title: "Upgrade the linter", Body: "which region?", Run: string(run.ID)}

	h.status(run, domain.RunNeedsAttention, domain.ActivityNeedsYou, "needs input: which region?")
	slow.expect(t, idle)
	h.svc.Active(h.ada.ID)
	slow.slow.Store(false)
	close(slow.release)
	phone.expectNone(t, testQuiet/2)

	phone.expect(t, idle)
	phone.expectNone(t, testQuiet)
	slow.expectNone(t, testQuiet)
}

// A paused run needs nobody, even with a question open; resumed, it needs
// its owner again.
func TestAResumedRunIsAnnounced(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	phone := h.subscribe(h.ada)
	run := h.run(h.ada, "Fix the login redirect", domain.LaunchTUI)

	h.runs.paused.Store(true)
	h.status(run, domain.RunNeedsAttention, domain.ActivityNeedsYou, "blocked: need the staging API key")
	phone.expectNone(t, testQuiet)
	h.runs.paused.Store(false)
	h.publish(run, events.TimelinePayload{Kind: events.TimelineResume})
	phone.expect(t, notification{Title: "Fix the login redirect", Body: "Blocked: need the staging API key", Run: string(run.ID)})
}

func TestNotificationWaitsWhileTheOwnerUsesADashboard(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	phone := h.subscribe(h.ada)
	stopped := h.run(h.ada, "Stopped by hand", domain.LaunchTUI)
	answered := h.run(h.ada, "Answered straight away", domain.LaunchTUI)

	h.svc.Active(h.ada.ID)
	// The dashboard reports the owner's use until they leave it, so the
	// quiet window starts after the clear however slowly the test runs.
	leave, left := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(left)
		for using := true; using; {
			select {
			case <-leave:
				using = false
			case <-time.After(testQuiet / 10):
			}
			h.svc.Active(h.ada.ID)
		}
	}()
	stop := sync.OnceFunc(func() { close(leave); <-left })
	t.Cleanup(stop)
	h.status(stopped, domain.RunNeedsAttention, domain.ActivityNeedsYou, "blocked: need a key")
	h.status(answered, domain.RunNeedsAttention, domain.ActivityNeedsYou, "blocked: need a key")
	phone.expectNone(t, testQuiet)
	// Cleared before the owner went quiet: never sent.
	h.status(answered, domain.RunRunning, domain.ActivityWorking, "agent resumed")
	stop()

	phone.expect(t, notification{Title: "Stopped by hand", Body: "Blocked: need a key", Run: string(stopped.ID)})
	phone.expectNone(t, testQuiet)
}

func TestGoneSubscriptionIsForgotten(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	gone, phone := h.subscribe(h.ada), h.subscribe(h.ada)
	gone.answer.Store(http.StatusGone)
	run := h.run(h.ada, "Fix the login redirect", domain.LaunchTUI)

	h.status(run, domain.RunNeedsAttention, domain.ActivityNeedsYou, "needs input: which region?")
	phone.expect(t, notification{Title: "Fix the login redirect", Body: "which region?", Run: string(run.ID)})
	deadline := time.Now().Add(10 * time.Second)
	for {
		subscribed, err := h.svc.Subscribed(h.ctx, h.ada.ID, gone.endpoint())
		if err != nil {
			t.Fatalf("Subscribed: %v", err)
		}
		if !subscribed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the subscription the push service reported gone is still stored")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if subscribed, err := h.svc.Subscribed(h.ctx, h.ada.ID, phone.endpoint()); err != nil || !subscribed {
		t.Fatalf("the working device's subscription = %v, %v; want it kept", subscribed, err)
	}
}

func TestTestSendsNowAndReturnsTheRealError(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	phone := h.subscribe(h.ada)

	h.svc.Active(h.ada.ID)
	if err := h.svc.Test(h.ctx, h.ada.ID, phone.endpoint()); err != nil {
		t.Fatalf("Test: %v", err)
	}
	phone.expect(t, notification{Title: "Aether", Body: "Notifications work on this device."})

	if err := h.svc.Test(h.ctx, h.bob.ID, phone.endpoint()); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("testing another member's device = %v, want ErrNotFound", err)
	}

	phone.answer.Store(http.StatusForbidden)
	host := strings.TrimPrefix(phone.service.URL, "https://")
	err := h.svc.Test(h.ctx, h.ada.ID, phone.endpoint())
	if want := "push: the push service did not take the notification: " + host + " answered 403 Forbidden: unsubscribed"; !errors.Is(err, ErrDelivery) || err.Error() != want {
		t.Fatalf("Test against a refusing push service = %v, want %q", err, want)
	}
	// A device nothing was sent to yet, so no open connection can carry the
	// request and it has to dial.
	unreachable := h.subscribe(h.ada)
	unreachable.service.Close()
	host = strings.TrimPrefix(unreachable.service.URL, "https://")
	err = h.svc.Test(h.ctx, h.ada.ID, unreachable.endpoint())
	if !errors.Is(err, ErrDelivery) || !strings.Contains(err.Error(), host+": dial tcp") {
		t.Fatalf("Test against an unreachable push service = %v, want the dial error", err)
	}
}

func TestSubscribeValidatesAndBoundsDevices(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	phone := newBrowser(t)

	if err := h.svc.Subscribe(h.ctx, h.ada.ID, "http://push.example/x", phone.p256dh(), phone.authKey()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("subscribing a plain-http endpoint = %v, want ErrInvalid", err)
	}
	for i := range store.MaxPushSubscriptions {
		if err := h.svc.Subscribe(h.ctx, h.ada.ID, fmt.Sprintf("https://push.example/%d", i), phone.p256dh(), phone.authKey()); err != nil {
			t.Fatalf("Subscribe device %d: %v", i, err)
		}
	}
	if err := h.svc.Subscribe(h.ctx, h.ada.ID, "https://push.example/one-more", phone.p256dh(), phone.authKey()); !errors.Is(err, store.ErrLimit) {
		t.Fatalf("subscribing past the bound = %v, want ErrLimit", err)
	}
	if err := h.svc.Subscribe(h.ctx, h.ada.ID, "https://push.example/0", phone.p256dh(), phone.authKey()); err != nil {
		t.Fatalf("subscribing a known device again at the bound: %v", err)
	}
	if err := h.svc.Unsubscribe(h.ctx, h.ada.ID, "https://push.example/0"); err != nil {
		t.Fatalf("Unsubscribe: %v", err)
	}
	if subscribed, err := h.svc.Subscribed(h.ctx, h.ada.ID, "https://push.example/0"); err != nil || subscribed {
		t.Fatalf("Subscribed after Unsubscribe = %v, %v; want false", subscribed, err)
	}
	if err := h.svc.Unsubscribe(h.ctx, h.ada.ID, "https://push.example/0"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unsubscribing twice = %v, want ErrNotFound", err)
	}
}

func TestNotifyUsesTheRunTopicAndWaitsForTheQuietWindow(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	phone := h.subscribe(h.ada)
	other := h.subscribe(h.bob)
	run := h.run(h.ada, "Fix the login redirect", domain.LaunchACP)
	h.svc.Active(h.ada.ID)
	started := time.Now()

	if err := h.svc.Notify(h.ctx, h.ada.ID, `Run "Fix the login redirect" finished`, "All green", run.ID, true, nil, nil); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	phone.expect(t, notification{Title: `Run "Fix the login redirect" finished`, Body: "All green", Run: string(run.ID)})
	if waited := time.Since(started); waited < testQuiet {
		t.Fatalf("sent after %v, want it held for the %v quiet window", waited, testQuiet)
	}
	other.expectNone(t, testQuiet/2)

	// The topic is the one needOf notifications use, so a device collapses them.
	h.status(run, domain.RunNeedsAttention, domain.ActivityNeedsYou, "needs input: which region?")
	phone.expect(t, notification{Title: "Fix the login redirect", Body: "which region?", Run: string(run.ID)})
	if got, want := phone.topic.Load(), runTopic(run.ID); got != want {
		t.Fatalf("needs topic = %v, want %v", got, want)
	}
	if err := h.svc.Notify(h.ctx, h.ada.ID, "Again", "body", run.ID, true, nil, nil); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	phone.expect(t, notification{Title: "Again", Body: "body", Run: string(run.ID)})
	if got, want := phone.topic.Load(), runTopic(run.ID); got != want {
		t.Fatalf("notify topic = %v, want %v", got, want)
	}
}

func TestNotifyReturnsTheDeviceError(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	gone := h.subscribe(h.ada)
	gone.answer.Store(http.StatusGone)
	run := h.run(h.ada, "Task", domain.LaunchACP)
	if err := h.svc.Notify(h.ctx, h.ada.ID, "t", "b", run.ID, true, nil, nil); !errors.Is(err, ErrDelivery) {
		t.Fatalf("Notify = %v, want ErrDelivery", err)
	}
}

func TestNotifySkipsANeedThatClearedWhileItWaited(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	phone := h.subscribe(h.ada)
	run := h.run(h.ada, "Task", domain.LaunchACP)
	h.svc.Active(h.ada.ID)
	var asked atomic.Bool
	if err := h.svc.Notify(h.ctx, h.ada.ID, "t", "b", run.ID, true, nil, func(context.Context) bool {
		asked.Store(true)
		return false
	}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if !asked.Load() {
		t.Fatal("the need was not checked again")
	}
	phone.expectNone(t, testQuiet)
}

func TestNotifyFlushEndsTheQuietWaitButKeepsTheRecheck(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	phone := h.subscribe(h.ada)
	run := h.run(h.ada, "Task", domain.LaunchACP)
	h.svc.Active(h.ada.ID)
	flush := make(chan struct{})
	done := make(chan error, 2)
	go func() { done <- h.svc.Notify(h.ctx, h.ada.ID, "kept", "b", run.ID, true, flush, nil) }()
	go func() {
		done <- h.svc.Notify(h.ctx, h.ada.ID, "dropped", "b", run.ID, true, flush, func(context.Context) bool { return false })
	}()
	phone.expectNone(t, testQuiet/3)
	start := time.Now()
	close(flush)
	phone.expect(t, notification{Title: "kept", Body: "b", Run: string(run.ID)})
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if time.Since(start) > testQuiet {
		t.Fatalf("flush took %v, want it to skip the %v quiet window", time.Since(start), testQuiet)
	}
	phone.expectNone(t, testQuiet/3)
}
