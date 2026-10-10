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
}

func (f *fakeRuns) PendingInputs(run domain.RunID) []domain.RunInputRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.inputs[run]
}

func (f *fakeRuns) Paused(domain.RunID) bool { return false }

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
	answer   atomic.Int32
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

	// The fake push services are loopback, which the real client refuses,
	// and each presents its own self-signed certificate.
	insecure := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // test servers only
	}}
	h.svc, err = New(Config{
		Store: db, Bus: bus, Runs: h.runs,
		KeyPath: filepath.Join(t.TempDir(), "push", "vapid_key.pem"),
		Client:  insecure, Quiet: testQuiet,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err = h.svc.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = h.svc.Close() })
	return h
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
	d := &device{browser: newBrowser(h.t), received: make(chan notification, 8)}
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
		d.received <- n
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
func (h *harness) status(run *domain.Run, to domain.RunStatus, reason string) {
	h.t.Helper()
	if err := h.db.UpdateRunStatus(h.ctx, run.ID, to, reason, nil, nil); err != nil {
		h.t.Fatalf("UpdateRunStatus: %v", err)
	}
	h.publish(run, events.RunStatusPayload{To: to, Reason: reason})
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
	want := notification{Title: "Fix the login redirect", Body: "Waiting for your reply", Run: string(run.ID)}

	h.status(run, domain.RunNeedsAttention, "agent idle")
	phone.expect(t, want)
	laptop.expect(t, want)

	// The same standing state, published again.
	h.publish(run, events.RunStatusPayload{From: domain.RunNeedsAttention, To: domain.RunNeedsAttention, Reason: "agent idle"})
	phone.expectNone(t, testQuiet)

	h.status(run, domain.RunRunning, "agent resumed")
	h.status(run, domain.RunNeedsAttention, "blocked: need the staging API key")
	want.Body = "Blocked: need the staging API key"
	phone.expect(t, want)

	// A handoff moves the notifications with the run.
	if err := h.db.TransferRun(h.ctx, run.ID, h.bob.ID); err != nil {
		t.Fatalf("TransferRun: %v", err)
	}
	h.status(run, domain.RunRunning, "agent resumed")
	h.status(run, domain.RunNeedsAttention, "agent idle")
	want.Body = "Waiting for your reply"
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
	h.status(run, domain.RunNeedsAttention, "agent idle")
	phone.expect(t, notification{Title: "Upgrade the linter", Body: "Waiting for your reply", Run: id})

	approval := &store.Approval{WorkspaceID: h.ws.ID, RunID: run.ID, Action: "ExitPlanMode"}
	if err := h.db.CreateApproval(h.ctx, approval); err != nil {
		t.Fatalf("CreateApproval: %v", err)
	}
	h.publish(run, events.ApprovalPayload{RequestID: approval.ID, Action: approval.Action, Decision: events.ApprovalRequested})
	phone.expect(t, notification{Title: "Upgrade the linter", Body: "Permission: ExitPlanMode", Run: id})
	if err := h.db.DecideApproval(h.ctx, approval.ID, string(events.ApprovalApproved), h.ada.ID, time.Now()); err != nil {
		t.Fatalf("DecideApproval: %v", err)
	}
	h.publish(run, events.ApprovalPayload{RequestID: approval.ID, Action: approval.Action, Decision: events.ApprovalApproved})
	// Deciding it leaves the idle park it interrupted, which was announced.
	phone.expectNone(t, testQuiet)

	h.status(run, domain.RunRunning, "agent resumed")
	finished := time.Now().UTC()
	if err := h.db.FinishRunReported(h.ctx, run.ID, domain.RunCompleted, "agent reported success", nil, &finished); err != nil {
		t.Fatalf("FinishRunReported: %v", err)
	}
	h.publish(run, events.RunStatusPayload{From: domain.RunRunning, To: domain.RunCompleted, OutcomeUnseen: true})
	phone.expect(t, notification{Title: "Upgrade the linter", Body: "Finished, review the result", Run: id})

	// A member closing a run of their own is not news to them.
	closed := h.run(h.ada, "Try the other approach", domain.LaunchTUI)
	if err := h.db.FinishRunByMember(h.ctx, closed.ID, domain.RunAbandoned, "", nil, &finished); err != nil {
		t.Fatalf("FinishRunByMember: %v", err)
	}
	h.publish(closed, events.RunStatusPayload{From: domain.RunRunning, To: domain.RunAbandoned})
	phone.expectNone(t, testQuiet)
}

func TestNotificationWaitsWhileTheOwnerUsesADashboard(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	phone := h.subscribe(h.ada)
	stopped := h.run(h.ada, "Stopped by hand", domain.LaunchTUI)
	answered := h.run(h.ada, "Answered straight away", domain.LaunchTUI)

	h.svc.Active(h.ada.ID)
	h.status(stopped, domain.RunNeedsAttention, "agent idle")
	h.status(answered, domain.RunNeedsAttention, "agent idle")
	phone.expectNone(t, testQuiet/2)
	// Cleared before the owner went quiet: never sent.
	h.status(answered, domain.RunRunning, "agent resumed")

	phone.expect(t, notification{Title: "Stopped by hand", Body: "Agent idle", Run: string(stopped.ID)})
	phone.expectNone(t, testQuiet)
}

func TestGoneSubscriptionIsForgotten(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	gone, phone := h.subscribe(h.ada), h.subscribe(h.ada)
	gone.answer.Store(http.StatusGone)
	run := h.run(h.ada, "Fix the login redirect", domain.LaunchTUI)

	h.status(run, domain.RunNeedsAttention, "agent idle")
	phone.expect(t, notification{Title: "Fix the login redirect", Body: "Agent idle", Run: string(run.ID)})
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
	phone.service.Close()
	err = h.svc.Test(h.ctx, h.ada.ID, phone.endpoint())
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
	for i := range maxDevices {
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
