package watch

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/tls"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/cost"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/push"
	"github.com/3xDevOps/Aether/internal/store"
	"github.com/3xDevOps/Aether/internal/store/storetest"
)

func TestAStaleClearNeverHidesANewerNeed(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	phone := h.subscribe(h.bob)
	run := h.run(h.ada, "Rotate the keys")
	w := h.watch(h.bob, runTarget(run)).Watch
	h.runs.ask(run.ID, "q1", "Rotate prod too?")
	h.publish(run.ID, events.RunInputPayload{PendingInputs: h.runs.PendingInputs(run.ID)})
	h.eventually("the first push", func() bool { return h.pushStatus(w.ID) == protocol.WatchStatusDelivered })

	// The clear fails, and a new question is pushed before it is retried.
	phone.status.Store(http.StatusInternalServerError)
	h.runs.answer(run.ID)
	h.publish(run.ID, events.RunInputPayload{PendingInputs: []domain.RunInputRequest{}})
	// The attempt is counted before it is sent: wait for the device's
	// answer, or the clear could still reach it once it accepts again.
	h.eventually("the clear to fail", func() bool {
		n := h.notices(w.ID)[0]
		return n.PushState == store.WatchPushWithdraw && n.PushAttempts == 1 && phone.refused.Load() == 1
	})
	phone.status.Store(http.StatusCreated)
	h.runs.ask(run.ID, "q2", "Rotate staging?")
	h.publish(run.ID, events.RunInputPayload{PendingInputs: h.runs.PendingInputs(run.ID)})
	h.eventually("the second push", func() bool {
		return len(h.notices(w.ID)) == 2 && pushOf(h.notices(w.ID)[1]).Status == protocol.WatchStatusDelivered
	})

	h.clock.advance(time.Minute)
	h.eventually("the stale clear settled", func() bool { return pushOf(h.notices(w.ID)[0]).Detail == "Replaced by a newer need" })
	if n := phone.received.Load(); n != 2 {
		t.Fatalf("device received %d pushes, want the two questions and no clear", n)
	}
}

func TestDroppingNeedsYouSettlesItsPushes(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	phone := h.subscribe(h.bob)
	run := h.run(h.ada, "Rotate the keys")
	w := h.watch(h.bob, runTarget(run)).Watch
	h.runs.ask(run.ID, "q1", "Rotate prod too?")
	h.publish(run.ID, events.RunInputPayload{PendingInputs: h.runs.PendingInputs(run.ID)})
	h.eventually("the first push", func() bool { return h.pushStatus(w.ID) == protocol.WatchStatusDelivered })

	// The second question's push fails and waits for its retry.
	phone.status.Store(http.StatusInternalServerError)
	h.runs.request(run.ID, "q2", "question", "Rotate staging?")
	h.publish(run.ID, events.RunInputPayload{PendingInputs: h.runs.PendingInputs(run.ID)})
	h.eventually("the second push to wait", func() bool {
		return len(h.notices(w.ID)) == 2 && strings.HasPrefix(pushOf(h.notices(w.ID)[1]).Detail, "Retry")
	})
	phone.status.Store(http.StatusCreated)

	if _, err := h.svc.Update(h.ctx, h.bob.ID, protocol.WatchUpdateParams{WatchID: w.ID, Conditions: []string{protocol.WatchFinished}}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	h.eventually("the delivered need withdrawn", func() bool {
		return pushOf(h.notices(w.ID)[0]).Status == protocol.WatchStatusWithdrawn
	})
	notes := h.notices(w.ID)
	if d := pushOf(notes[0]); d.Detail != "Withdrawn: You stopped watching for needs you" {
		t.Fatalf("first push row = %+v, want it withdrawn because needs you is no longer watched", d)
	}
	if d := pushOf(notes[1]); d.Status != protocol.WatchStatusCleared || d.Detail != "You stopped watching for needs you" || notes[1].PushState != store.WatchPushNone {
		t.Fatalf("second push = %+v, %s; want it dropped as cleared", d, notes[1].PushState)
	}
	h.clock.advance(time.Hour)
	settle()
	if n := phone.received.Load(); n != 2 {
		t.Fatalf("device received %d pushes, want the first question and its withdrawal", n)
	}
}

func TestANeedSentWhileNeedsYouIsDroppedIsWithdrawn(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	phone := h.subscribe(h.bob)
	g := &gate{arrived: make(chan struct{}, 1), release: make(chan struct{})}
	phone.gate.Store(g)
	run := h.run(h.ada, "Rotate the keys")
	w := h.watch(h.bob, runTarget(run)).Watch
	h.runs.ask(run.ID, "q1", "Rotate prod too?")
	h.publish(run.ID, events.RunInputPayload{PendingInputs: h.runs.PendingInputs(run.ID)})
	select {
	case <-g.arrived:
	case <-time.After(10 * time.Second):
		t.Fatal("the push was never sent")
	}

	if _, err := h.svc.Update(h.ctx, h.bob.ID, protocol.WatchUpdateParams{WatchID: w.ID, Conditions: []string{protocol.WatchFinished}}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	phone.gate.Store(nil)
	close(g.release)
	h.eventually("the need withdrawn", func() bool { return h.pushStatus(w.ID) == protocol.WatchStatusWithdrawn })
	if n := phone.received.Load(); n != 2 {
		t.Fatalf("device received %d pushes, want the question and its withdrawal", n)
	}
}

func TestAQueuedClearStopsAtItsAttemptLimit(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	phone := h.subscribe(h.bob)
	run := h.run(h.ada, "Rotate the keys")
	w := h.watch(h.bob, runTarget(run)).Watch
	h.runs.ask(run.ID, "q1", "Rotate prod too?")
	h.publish(run.ID, events.RunInputPayload{PendingInputs: h.runs.PendingInputs(run.ID)})
	h.eventually("the push", func() bool { return h.pushStatus(w.ID) == protocol.WatchStatusDelivered })

	// The server stopped after reserving the last attempt of a clear.
	_ = h.svc.Close()
	h.runs.answer(run.ID)
	now := h.clock.now()
	if err := h.db.SetWatchPush(h.ctx, h.notices(w.ID)[0].ID, store.WatchPushWithdraw, maxPushAttempts, &now, nil); err != nil {
		t.Fatal(err)
	}
	h.start()
	h.eventually("the clear given up", func() bool { return h.pushStatus(w.ID) == protocol.WatchStatusFailed })
	if n := phone.received.Load(); n != 1 {
		t.Fatalf("device received %d pushes, want the question alone", n)
	}
}

func TestANeedSentInASummaryIsNotWithdrawnOnItsOwn(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	phone := h.subscribe(h.ada)
	now := h.clock.now().UTC()
	if _, err := h.svc.SetPrefs(h.ctx, h.ada.ID, protocol.NotifyPrefs{QuietHours: &protocol.QuietHours{
		Start: now.Add(-time.Hour).Format("15:04"), End: now.Add(time.Hour).Format("15:04"), TZ: "UTC",
	}}); err != nil {
		t.Fatal(err)
	}
	a, b := h.run(h.ada, "First"), h.run(h.ada, "Second")
	wa, wb := h.watch(h.ada, runTarget(a)).Watch, h.watch(h.ada, runTarget(b)).Watch
	h.status(a, domain.RunNeedsAttention, domain.ActivityNeedsYou, "blocked: one")
	h.status(b, domain.RunNeedsAttention, domain.ActivityNeedsYou, "blocked: two")
	h.eventually("both held", func() bool {
		return h.pushStatus(wa.ID) == protocol.WatchStatusHeldQuiet && h.pushStatus(wb.ID) == protocol.WatchStatusHeldQuiet
	})
	h.clock.advance(time.Hour + time.Minute)
	h.eventually("the summary", func() bool { return h.pushStatus(wa.ID) == protocol.WatchStatusDelivered })

	h.status(a, domain.RunRunning, domain.ActivityWorking, "")
	h.eventually("the need read as ended", func() bool {
		var o protocol.WatchObserved
		return json.Unmarshal(h.watchState(wa.ID).Observed, &o) == nil && o.Need == ""
	})
	settle()
	if d := pushOf(h.notices(wa.ID)[0]); d.Status != protocol.WatchStatusDelivered || !strings.HasPrefix(d.Detail, store.WatchPushSummarized) || phone.received.Load() != 1 {
		t.Fatalf("push row = %+v, %d sent; want the summary alone and no clear", d, phone.received.Load())
	}
}

type fakeRuns struct {
	mu      sync.Mutex
	inputs  map[domain.RunID][]domain.RunInputRequest
	pending map[domain.RunID][]acphost.Request
	paused  map[domain.RunID]bool
}

func (f *fakeRuns) PendingInputs(run domain.RunID) []domain.RunInputRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.inputs[run]
}

func (f *fakeRuns) ACPPending(run domain.RunID) []acphost.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pending[run]
}

func (f *fakeRuns) Paused(run domain.RunID) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.paused[run]
}

func (f *fakeRuns) pause(run domain.RunID, paused bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paused[run] = paused
}

func (f *fakeRuns) ask(run domain.RunID, id, title string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inputs[run] = []domain.RunInputRequest{{ID: id, SessionID: "s1", Kind: "question"}}
	f.pending[run] = []acphost.Request{{ID: id, Kind: "question", Title: title}}
}

// request adds an open request of kind beside the run's others.
func (f *fakeRuns) request(run domain.RunID, id, kind, title string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inputs[run] = append(f.inputs[run], domain.RunInputRequest{ID: id, SessionID: "s1", Kind: kind})
	f.pending[run] = append(f.pending[run], acphost.Request{ID: id, Kind: kind, Title: title})
}

func (f *fakeRuns) answer(run domain.RunID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.inputs, run)
	delete(f.pending, run)
}

type noBudgets struct{}

func (noBudgets) Budget(context.Context, domain.WorkspaceID) (cost.Status, error) {
	return cost.Status{}, nil
}

// clock is the time the service reads, moved forward by tests.
type clock struct{ offset atomic.Int64 }

func (c *clock) now() time.Time          { return time.Now().Add(time.Duration(c.offset.Load())) }
func (c *clock) advance(d time.Duration) { c.offset.Add(int64(d)) }

// device is one subscribed browser's push service. status is what it
// answers.
type device struct {
	server   *httptest.Server
	received atomic.Int32
	status   atomic.Int32
	// refused counts the pushes it answered with an error status.
	refused atomic.Int32
	// While gate is set, the push service answers once it is released.
	gate atomic.Pointer[gate]
}

type gate struct{ arrived, release chan struct{} }

type harness struct {
	t     *testing.T
	ctx   context.Context
	path  string
	db    *store.DB
	log   *events.SQLiteLog
	bus   *events.InProc
	runs  *fakeRuns
	push  *push.Service
	clock *clock
	svc   *Service
	ws    *domain.Workspace
	ada   *domain.Member
	bob   *domain.Member
	swarm atomic.Pointer[protocol.AttentionItem]
	keys  int
	pr    PRSource
	// recovered, when set, is the scheduler recovery the next start waits for.
	recovered chan struct{}
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()
	h := &harness{t: t, ctx: ctx, path: filepath.Join(t.TempDir(), "aether.db"), clock: &clock{},
		runs: &fakeRuns{inputs: map[domain.RunID][]domain.RunInputRequest{}, pending: map[domain.RunID][]acphost.Request{}, paused: map[domain.RunID]bool{}}}
	db, err := storetest.Open(h.path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	h.db = db
	if h.log, err = events.OpenSQLiteLog(h.path); err != nil {
		t.Fatalf("open event log: %v", err)
	}
	t.Cleanup(func() { _ = h.log.Close() })
	if h.bus, err = events.NewInProc(ctx, h.log); err != nil {
		t.Fatalf("bus: %v", err)
	}
	t.Cleanup(func() { _ = h.bus.Close() })
	h.ws = &domain.Workspace{Name: "proj", BaseBranch: domain.DefaultBaseBranch}
	if err = db.CreateWorkspace(ctx, h.ws); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	h.ada, h.bob = h.member("Ada"), h.member("Bob")
	insecure := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // test servers only
	}}
	h.push, err = push.New(push.Config{
		Store: db, Bus: h.bus, Runs: h.runs, Swarm: h.swarmOf, Budgets: noBudgets{},
		KeyPath: filepath.Join(t.TempDir(), "vapid.pem"), Client: insecure, Quiet: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("push.New: %v", err)
	}
	h.start()
	return h
}

func (h *harness) swarmOf(_ context.Context, run *domain.Run) (*protocol.AttentionItem, domain.MemberID, error) {
	if run.MissionRole != "integrator" {
		return nil, "", nil
	}
	return h.swarm.Load(), h.ada.ID, nil
}

// start runs a new Service on the harness's store, as a server start does.
func (h *harness) start() {
	h.t.Helper()
	svc, err := New(Config{
		Store: h.db, Bus: h.bus, Runs: h.runs, Swarm: h.swarmOf, Push: h.push, Head: h.log.LastSeq,
		Debounce: 50 * time.Millisecond, Tick: 50 * time.Millisecond, Now: h.clock.now, Recovered: h.recovered,
	})
	if err != nil {
		h.t.Fatalf("New: %v", err)
	}
	if h.pr != nil {
		svc.UsePRSource(h.pr)
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

// subscribe gives member a device whose push service answers 201.
func (h *harness) subscribe(member *domain.Member) *device {
	h.t.Helper()
	d := &device{}
	d.status.Store(http.StatusCreated)
	d.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if g := d.gate.Load(); g != nil {
			g.arrived <- struct{}{}
			<-g.release
		}
		if status := int(d.status.Load()); status != http.StatusCreated {
			http.Error(w, "push service refused", status)
			d.refused.Add(1)
			return
		}
		d.received.Add(1)
		w.WriteHeader(http.StatusCreated)
	}))
	h.t.Cleanup(d.server.Close)
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		h.t.Fatal(err)
	}
	auth := make([]byte, 16)
	_, _ = rand.Read(auth)
	b64 := base64.RawURLEncoding
	if err := h.push.Subscribe(h.ctx, member.ID, d.server.URL+"/send/device", b64.EncodeToString(key.PublicKey().Bytes()), b64.EncodeToString(auth)); err != nil {
		h.t.Fatalf("Subscribe: %v", err)
	}
	return d
}

func (h *harness) run(owner *domain.Member, title string) *domain.Run {
	h.t.Helper()
	run := &domain.Run{WorkspaceID: h.ws.ID, MemberID: owner.ID, Task: title, Harness: "claude",
		Mode: domain.LaunchACP, ACP: true, Status: domain.RunRunning}
	if err := h.db.CreateRun(h.ctx, run); err != nil {
		h.t.Fatalf("create run: %v", err)
	}
	return run
}

// status moves the run the way the scheduler does: the row, then the event.
func (h *harness) status(run *domain.Run, to domain.RunStatus, activity domain.RunActivity, reason string) uint64 {
	h.t.Helper()
	if _, err := h.db.UpdateRunStatus(h.ctx, run.ID, to, reason, activity, nil, nil); err != nil {
		h.t.Fatalf("UpdateRunStatus: %v", err)
	}
	return h.publish(run.ID, events.RunStatusPayload{To: to, Reason: reason, Activity: activity})
}

func (h *harness) publish(run domain.RunID, payload events.Payload) uint64 {
	h.t.Helper()
	e, err := h.bus.Publish(h.ctx, events.Event{WorkspaceID: h.ws.ID, RunID: run, Payload: payload})
	if err != nil {
		h.t.Fatalf("publish: %v", err)
	}
	return e.Seq
}

func (h *harness) watch(member *domain.Member, target protocol.WatchTarget) protocol.WatchResult {
	h.t.Helper()
	res, err := h.svc.Create(h.ctx, member.ID, protocol.WatchCreateParams{Target: target})
	if err != nil {
		h.t.Fatalf("Create: %v", err)
	}
	return res
}

func runTarget(run *domain.Run) protocol.WatchTarget {
	return protocol.WatchTarget{Kind: protocol.WatchTargetRun, RunID: string(run.ID)}
}

// notices is the watch's notifications, oldest first.
func (h *harness) notices(id string) []*store.WatchNotification {
	h.t.Helper()
	notes, err := h.db.ListWatchNotifications(h.ctx, id, 100)
	if err != nil {
		h.t.Fatalf("ListWatchNotifications: %v", err)
	}
	for i, j := 0, len(notes)-1; i < j; i, j = i+1, j-1 {
		notes[i], notes[j] = notes[j], notes[i]
	}
	return notes
}

// eventually waits for cond, failing with what describes the state.
func (h *harness) eventually(what string, cond func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			h.t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// settle waits long enough for any debounced evaluation and push to have
// happened.
func settle() { time.Sleep(400 * time.Millisecond) }

func bodies(notes []*store.WatchNotification) []string {
	out := make([]string, 0, len(notes))
	for _, n := range notes {
		out = append(out, n.Condition+": "+n.Body)
	}
	return out
}

// pushOf is the notice's push delivery row.
func pushOf(n *store.WatchNotification) store.WatchDelivery {
	for _, d := range n.Deliveries {
		if d.Channel == protocol.WatchChannelPush {
			return d
		}
	}
	return store.WatchDelivery{}
}

// pushStatus is the push status of the watch's only notice, empty before
// it has one.
func (h *harness) pushStatus(id string) string {
	h.t.Helper()
	notes := h.notices(id)
	if len(notes) != 1 {
		return ""
	}
	return pushOf(notes[0]).Status
}

func (h *harness) watchState(id string) *store.Watch {
	h.t.Helper()
	w, err := h.db.GetWatch(h.ctx, id)
	if err != nil {
		h.t.Fatalf("GetWatch: %v", err)
	}
	return w
}

func TestRunNeedsAreNotifiedOncePerNeedAndFinishWordsStayApart(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	phone := h.subscribe(h.ada)
	run := h.run(h.ada, "Fix the login redirect")
	w := h.watch(h.ada, runTarget(run)).Watch

	// Waiting on workers, CI or nothing, an answered question and an
	// unreported turn end need nobody.
	h.status(run, domain.RunNeedsAttention, domain.ActivityWaitingAgents, "waiting on 3 workers")
	h.status(run, domain.RunRunning, domain.ActivityWaitingCI, "waiting for CI")
	h.status(run, domain.RunNeedsAttention, domain.ActivityAnswered, "answered")
	h.status(run, domain.RunNeedsAttention, domain.ActivityWaiting, "turn ended without a report")
	h.status(run, domain.RunNeedsAttention, domain.ActivityIdle, "agent idle")
	settle()
	if got := h.notices(w.ID); len(got) != 0 {
		t.Fatalf("notices for waiting states = %q, want none", bodies(got))
	}

	h.status(run, domain.RunNeedsAttention, domain.ActivityNeedsYou, "needs input: which region?")
	// The same event again, and a duplicate replay of it, change nothing.
	h.publish(run.ID, events.RunStatusPayload{To: domain.RunNeedsAttention, Activity: domain.ActivityNeedsYou})
	h.publish(run.ID, events.RunStatusPayload{To: domain.RunNeedsAttention, Activity: domain.ActivityNeedsYou})
	h.eventually("the need's push", func() bool { return phone.received.Load() == 1 })
	settle()
	got := h.notices(w.ID)
	if len(got) != 1 || got[0].Body != "which region?" || got[0].Title != "Fix the login redirect" || got[0].Link != "/?run="+string(run.ID) {
		t.Fatalf("notices = %q, want one for the question", bodies(got))
	}
	h.eventually("the push row to say delivered", func() bool { return pushOf(h.notices(w.ID)[0]).Status == protocol.WatchStatusDelivered })
	if d := pushOf(h.notices(w.ID)[0]); d.Detail != "1 device" {
		t.Fatalf("push = %+v, want delivered to 1 device", d)
	}

	h.runs.ask(run.ID, "q2", "Deploy to staging first?")
	h.publish(run.ID, events.RunInputPayload{PendingInputs: h.runs.PendingInputs(run.ID)})
	h.eventually("the second need", func() bool { return len(h.notices(w.ID)) == 2 })
	if got := h.notices(w.ID)[1].Body; got != "Question: Deploy to staging first?" {
		t.Fatalf("question notice = %q, want the question's text", got)
	}

	h.runs.answer(run.ID)
	if _, err := h.db.FinishRunReported(h.ctx, run.ID, domain.RunCompleted, domain.ReportedSuccessReason, "", nil, nil); err != nil {
		t.Fatalf("FinishRunReported: %v", err)
	}
	h.publish(run.ID, events.RunStatusPayload{To: domain.RunCompleted})
	h.eventually("the finish", func() bool { return len(h.notices(w.ID)) == 3 })
	if got := h.notices(w.ID)[2]; got.Condition != protocol.WatchFinished || got.Body != "Agent reported done" {
		t.Fatalf("finish notice = %s: %s, want finished: Agent reported done", got.Condition, got.Body)
	}
	if state := h.watchState(w.ID).State; state != protocol.WatchActive {
		t.Fatalf("state after a reported success = %s, want active until a member closes the run", state)
	}

	h.status(run, domain.RunMerged, "", "closed")
	h.eventually("the watch to finish", func() bool { return h.watchState(w.ID).State == protocol.WatchDone })
	if got := bodies(h.notices(w.ID)); len(got) != 4 || got[3] != "finished: Closed as merged" {
		t.Fatalf("notices = %q, want Closed as merged last", got)
	}
}

func TestReportedSuccessIsAnnouncedOnceAfterTheOwnerClearsReview(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	run := h.run(h.ada, "Tidy the config loader")
	w := h.watch(h.ada, runTarget(run)).Watch

	// The owner puts the success off before the debounced evaluation reads it.
	if _, err := h.db.FinishRunReported(h.ctx, run.ID, domain.RunNeedsAttention, domain.ReportedSuccessReason, domain.ActivityFinished, nil, nil); err != nil {
		t.Fatalf("FinishRunReported: %v", err)
	}
	if _, ok, err := h.db.SetRunReview(h.ctx, run.ID, domain.ReviewLater, domain.ReviewReady); err != nil || !ok {
		t.Fatalf("SetRunReview = %v, %v", ok, err)
	}
	h.publish(run.ID, events.RunStatusPayload{To: domain.RunNeedsAttention, Activity: domain.ActivityFinished})
	h.eventually("the finish", func() bool { return len(h.notices(w.ID)) == 1 })

	// Accepting it, and the run then closing on that report, are the same success.
	if _, ok, err := h.db.SetRunReview(h.ctx, run.ID, domain.ReviewAccepted, domain.ReviewLater); err != nil || !ok {
		t.Fatalf("SetRunReview = %v, %v", ok, err)
	}
	h.publish(run.ID, events.RunStatusPayload{To: domain.RunNeedsAttention, Activity: domain.ActivityFinished})
	if _, err := h.db.FinishRunReported(h.ctx, run.ID, domain.RunCompleted, domain.ReportedSuccessReason, "", nil, nil); err != nil {
		t.Fatalf("FinishRunReported: %v", err)
	}
	h.publish(run.ID, events.RunStatusPayload{To: domain.RunCompleted})
	settle()
	if got := bodies(h.notices(w.ID)); len(got) != 1 || got[0] != "finished: Agent reported done" {
		t.Fatalf("notices = %q, want Agent reported done once", got)
	}
}

func TestASuccessAfterResumedWorkIsAnnouncedAgain(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	run := h.run(h.ada, "Port the parser")
	w := h.watch(h.ada, runTarget(run)).Watch
	if _, err := h.db.FinishRunReported(h.ctx, run.ID, domain.RunNeedsAttention, domain.ReportedSuccessReason, domain.ActivityFinished, nil, nil); err != nil {
		t.Fatal(err)
	}
	h.publish(run.ID, events.RunStatusPayload{To: domain.RunNeedsAttention, Activity: domain.ActivityFinished})
	h.eventually("the first success", func() bool { return len(h.notices(w.ID)) == 1 })

	// The run goes back to work and completes on a second report before
	// the watch reads the turn between them.
	if _, err := h.db.UpdateRunStatus(h.ctx, run.ID, domain.RunRunning, "", domain.ActivityWorking, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.FinishRunReported(h.ctx, run.ID, domain.RunCompleted, domain.ReportedSuccessReason, "", nil, nil); err != nil {
		t.Fatal(err)
	}
	h.publish(run.ID, events.RunStatusPayload{To: domain.RunCompleted})
	h.eventually("the second success", func() bool { return len(h.notices(w.ID)) == 2 })
	if got := bodies(h.notices(w.ID)); got[0] != "finished: Agent reported done" || got[1] != "finished: Agent reported done" {
		t.Fatalf("notices = %q, want Agent reported done twice", got)
	}
}

func TestARetainedRunThatCompletesAgainIsAnnouncedAgain(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	run := h.run(h.ada, "Rebuild the index")
	w := h.watch(h.ada, runTarget(run)).Watch
	h.status(run, domain.RunCompleted, "", "exited; retained container")
	h.eventually("the first completion", func() bool { return len(h.notices(w.ID)) == 1 })

	// Relaunched in its retained container, it completes again.
	h.status(run, domain.RunRunning, domain.ActivityWorking, "")
	h.status(run, domain.RunCompleted, "", "exited; retained container")
	h.eventually("the second completion", func() bool { return len(h.notices(w.ID)) == 2 })
	settle()
	if got := bodies(h.notices(w.ID)); len(got) != 2 || got[0] != "finished: Completed" || got[1] != "finished: Completed" {
		t.Fatalf("notices = %q, want Completed twice", got)
	}
}

// failingPrefs fails reading notification preferences while fail is set.
type failingPrefs struct {
	*store.DB
	fail atomic.Bool
}

func (f *failingPrefs) GetNotifyPrefs(ctx context.Context, member domain.MemberID) (store.NotifyPrefs, error) {
	if f.fail.Load() {
		return store.NotifyPrefs{}, errors.New("database is locked")
	}
	return f.DB.GetNotifyPrefs(ctx, member)
}

func TestAFailedCheckAtStartIsRetried(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	run := h.run(h.ada, "Ship the release")
	w := h.watch(h.ada, runTarget(run)).Watch
	h.eventually("the first check", func() bool { return h.watchState(w.ID).LastCheckAt != nil })

	// A need arises while the server is down, and no event will replay it.
	_ = h.svc.Close()
	if _, err := h.db.UpdateRunStatus(h.ctx, run.ID, domain.RunNeedsAttention, "needs input: which tag?", domain.ActivityNeedsYou, nil, nil); err != nil {
		t.Fatal(err)
	}
	st := &failingPrefs{DB: h.db}
	st.fail.Store(true)
	svc, err := New(Config{
		Store: st, Bus: h.bus, Runs: h.runs, Swarm: h.swarmOf, Push: h.push, Head: h.log.LastSeq,
		Debounce: 50 * time.Millisecond, Tick: 50 * time.Millisecond, Now: h.clock.now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Start(h.ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	h.eventually("the failure recorded", func() bool {
		got := h.watchState(w.ID)
		return got.State == protocol.WatchError && got.NextCheckAt != nil && strings.Contains(got.Error, "database is locked")
	})

	st.fail.Store(false)
	h.clock.advance(maxBackoff)
	h.eventually("the retried need", func() bool { return len(h.notices(w.ID)) == 1 })
	h.eventually("the watch recovered", func() bool {
		got := h.watchState(w.ID)
		return got.State == protocol.WatchActive && got.Error == "" && got.NextCheckAt == nil
	})
}

func TestAStartingRunHasNotFinished(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	run := &domain.Run{WorkspaceID: h.ws.ID, MemberID: h.ada.ID, Task: "Queued work", Harness: "claude",
		Mode: domain.LaunchACP, ACP: true, Status: domain.RunQueued}
	if err := h.db.CreateRun(h.ctx, run); err != nil {
		t.Fatal(err)
	}
	w := h.watch(h.ada, runTarget(run)).Watch
	h.status(run, domain.RunProvisioning, "", "")
	h.eventually("the provisioning run read", func() bool {
		var o protocol.WatchObserved
		return json.Unmarshal(h.watchState(w.ID).Observed, &o) == nil && o.RunStatus == string(domain.RunProvisioning)
	})
	var o protocol.WatchObserved
	if err := json.Unmarshal(h.watchState(w.ID).Observed, &o); err != nil {
		t.Fatal(err)
	}
	if got := h.notices(w.ID); len(got) != 0 || o.Summary == "Agent reported done" {
		t.Fatalf("notices = %q, summary %q; want none for a starting run", bodies(got), o.Summary)
	}
}

func TestEvaluationReadsCurrentStateWhateverTheEventsSay(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	run := h.run(h.ada, "Refactor the store")
	w := h.watch(h.ada, runTarget(run)).Watch

	// A stale event claiming a need, arriving after the row moved on.
	if _, err := h.db.UpdateRunStatus(h.ctx, run.ID, domain.RunRunning, "", domain.ActivityWorking, nil, nil); err != nil {
		t.Fatal(err)
	}
	h.publish(run.ID, events.RunStatusPayload{To: domain.RunNeedsAttention, Activity: domain.ActivityNeedsYou, Reason: "needs input: stale"})
	h.eventually("the run as it stands", func() bool {
		return strings.Contains(string(h.watchState(w.ID).Observed), `"activity":"working"`)
	})
	if got := h.notices(w.ID); len(got) != 0 {
		t.Fatalf("notices from a stale event = %q, want none", bodies(got))
	}
}

func TestRestartRecoversMissedNeedsWithoutRepeatingNotifiedOnes(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	run := h.run(h.ada, "Ship the release")
	other := h.run(h.ada, "Write the changelog")
	w := h.watch(h.ada, runTarget(run)).Watch
	w2 := h.watch(h.ada, runTarget(other)).Watch

	h.status(run, domain.RunNeedsAttention, domain.ActivityNeedsYou, "needs input: which tag?")
	h.eventually("the first need", func() bool { return len(h.notices(w.ID)) == 1 })

	// The server stops, and a need arises while it is down.
	_ = h.svc.Close()
	h.status(other, domain.RunNeedsAttention, domain.ActivityNeedsYou, "blocked: no credentials")
	h.start()
	h.eventually("the need missed while down", func() bool { return len(h.notices(w2.ID)) == 1 })
	settle()
	if got := h.notices(w.ID); len(got) != 1 {
		t.Fatalf("notices after restart = %q, want the first need once", bodies(got))
	}

	// A burst cut off by a stop before its debounce ran is evaluated from
	// the cursor by the next server.
	_ = h.svc.Close()
	h.start()
	last := h.status(run, domain.RunNeedsAttention, domain.ActivityNeedsYou, "needs input: which branch?")
	_ = h.svc.Close()
	h.start()
	h.eventually("the need from the cut-off burst", func() bool { return len(h.notices(w.ID)) == 2 })
	settle()
	if got := h.notices(w.ID); len(got) != 2 {
		t.Fatalf("notices = %q, want two distinct needs", bodies(got))
	}
	seq, _, err := h.db.WatchCursor(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if seq < last {
		t.Fatalf("cursor = %d, want it past the last event applied, %d", seq, last)
	}
}

func TestRestartWaitsForTheSchedulerToRecoverRuns(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	run := h.run(h.ada, "Ship the release")
	w := h.watch(h.ada, runTarget(run)).Watch
	h.status(run, domain.RunRunning, domain.ActivityNeedsYou, "")
	h.runs.ask(run.ID, "q1", "Tag it?")
	h.publish(run.ID, events.RunInputPayload{PendingInputs: h.runs.PendingInputs(run.ID)})
	h.eventually("the question", func() bool { return len(h.notices(w.ID)) == 1 })

	// The server stops; the new one knows no open request until the
	// scheduler has reconciled the run, which interrupts its turn.
	_ = h.svc.Close()
	h.runs.answer(run.ID)
	h.recovered = make(chan struct{})
	h.start()
	settle()
	if got := h.notices(w.ID); len(got) != 1 {
		t.Fatalf("notices before recovery = %q, want the question only", bodies(got))
	}
	h.status(run, domain.RunNeedsAttention, domain.ActivityNeedsYou, "turn interrupted")
	close(h.recovered)
	h.eventually("the interrupted turn", func() bool { return len(h.notices(w.ID)) == 2 })
	settle()
	if got := bodies(h.notices(w.ID)); len(got) != 2 || got[1] != "needs_you: Turn interrupted, send a message to continue" {
		t.Fatalf("notices = %q, want the question, then the interrupted turn once", got)
	}
}

func TestAServerThatNeverRecoversStillStops(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	_ = h.svc.Close()
	h.recovered = make(chan struct{})
	h.start()
	closed := make(chan struct{})
	go func() {
		_ = h.svc.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return while recovery never finished")
	}
}

func TestConcurrentEvaluatorsRecordOneNotice(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	run := h.run(h.ada, "Migrate the database")
	w := h.watch(h.ada, runTarget(run)).Watch
	if _, err := h.db.UpdateRunStatus(h.ctx, run.ID, domain.RunNeedsAttention, "needs input: go?", domain.ActivityNeedsYou, nil, nil); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if err := h.svc.evaluate(h.ctx, w.ID); err != nil {
				t.Errorf("evaluate: %v", err)
			}
		})
	}
	wg.Wait()
	if got := h.notices(w.ID); len(got) != 1 {
		t.Fatalf("notices = %q, want one", bodies(got))
	}
}

func TestNeedAnsweredBeforeThePushIsDropped(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	phone := h.subscribe(h.ada)
	run := h.run(h.ada, "Rotate the keys")
	w := h.watch(h.ada, runTarget(run)).Watch

	// Ada is on a dashboard: the in-app toast covers it and push waits.
	h.push.Active(h.ada.ID)
	h.runs.ask(run.ID, "q1", "Rotate prod too?")
	h.publish(run.ID, events.RunInputPayload{PendingInputs: h.runs.PendingInputs(run.ID)})
	h.eventually("the in-app notice", func() bool { return len(h.notices(w.ID)) == 1 })
	if d := pushOf(h.notices(w.ID)[0]); d.Status != protocol.WatchStatusPending || d.Detail != dashboardHold {
		t.Fatalf("push while on a dashboard = %+v, want pending until Ada leaves", d)
	}
	h.runs.answer(run.ID)
	h.publish(run.ID, events.RunInputPayload{PendingInputs: []domain.RunInputRequest{}})
	h.eventually("the push to be cleared", func() bool { return h.pushStatus(w.ID) == protocol.WatchStatusCleared })
	if n := phone.received.Load(); n != 0 {
		t.Fatalf("device received %d pushes, want none", n)
	}
}

func TestDeliveredNeedIsWithdrawnWhenItEnds(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	phone := h.subscribe(h.bob)
	run := h.run(h.ada, "Rotate the keys")
	w := h.watch(h.bob, runTarget(run)).Watch

	h.runs.ask(run.ID, "q1", "Rotate prod too?")
	h.publish(run.ID, events.RunInputPayload{PendingInputs: h.runs.PendingInputs(run.ID)})
	h.eventually("the push", func() bool { return h.pushStatus(w.ID) == protocol.WatchStatusDelivered })

	h.runs.answer(run.ID)
	h.publish(run.ID, events.RunInputPayload{PendingInputs: []domain.RunInputRequest{}})
	h.eventually("the withdrawal", func() bool { return h.pushStatus(w.ID) == protocol.WatchStatusWithdrawn })
	if d := pushOf(h.notices(w.ID)[0]); d.Detail != "Withdrawn: no longer needs you" {
		t.Fatalf("push row = %+v, want it to say the push was withdrawn", d)
	}
	h.status(run, domain.RunInterrupted, "", "")
	h.eventually("the finish", func() bool { return len(h.notices(w.ID)) == 2 })
	h.eventually("the finish pushed", func() bool { return phone.received.Load() == 3 })
	settle()
	if n := phone.received.Load(); n != 3 {
		t.Fatalf("device received %d pushes, want the need, its withdrawal and the finish", n)
	}
}

func TestEveryOpenRequestIsItsOwnNeed(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	phone := h.subscribe(h.ada)
	run := h.run(h.ada, "Two questions")
	w := h.watch(h.ada, runTarget(run)).Watch

	h.push.Active(h.ada.ID)
	h.runs.request(run.ID, "q1", "question", "Which region?")
	h.runs.request(run.ID, "q2", "question", "Which tier?")
	h.publish(run.ID, events.RunInputPayload{PendingInputs: h.runs.PendingInputs(run.ID)})
	h.eventually("a notice per question", func() bool { return len(h.notices(w.ID)) == 2 })

	// A permission request arriving does not clear the questions' held
	// pushes, and nothing is announced twice.
	h.runs.request(run.ID, "p1", "permission", "Run make test")
	h.publish(run.ID, events.RunInputPayload{PendingInputs: h.runs.PendingInputs(run.ID)})
	h.eventually("the permission notice", func() bool { return len(h.notices(w.ID)) == 3 })
	h.publish(run.ID, events.RunInputPayload{PendingInputs: h.runs.PendingInputs(run.ID)})
	settle()
	got := h.notices(w.ID)
	if b := bodies(got); len(b) != 3 || b[0] != "needs_you: Question: Which region?" || b[1] != "needs_you: Question: Which tier?" ||
		b[2] != "needs_you: Permission requested: Run make test" {
		t.Fatalf("notices = %q", b)
	}
	if err := h.svc.deliverMember(h.ctx, h.ada.ID, got); err != nil {
		t.Fatal(err)
	}
	for _, n := range h.notices(w.ID) {
		if d := pushOf(n); d.Status != protocol.WatchStatusPending {
			t.Fatalf("push of %q = %+v, want it still pending while every request is open", n.Body, d)
		}
	}
	if phone.received.Load() != 0 {
		t.Fatal("a push went out while Ada was on a dashboard")
	}
}

func TestRevokedAccessStopsNoticesWithoutAnnouncingThem(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	run := h.run(h.ada, "Audit the logs")
	w := h.watch(h.bob, runTarget(run)).Watch

	// Revoked while the server is down: a check that began with access
	// could otherwise still read the need below and announce it.
	_ = h.svc.Close()
	h.bob.Pending = true
	if err := h.db.UpdateMember(h.ctx, h.bob); err != nil {
		t.Fatal(err)
	}
	h.start()
	h.status(run, domain.RunNeedsAttention, domain.ActivityNeedsYou, "blocked: no access")
	h.eventually("the watch to fail", func() bool { return h.watchState(w.ID).State == protocol.WatchError })
	if got := h.watchState(w.ID); !strings.HasPrefix(got.Error, "access revoked") {
		t.Fatalf("error = %q, want access revoked", got.Error)
	}
	if got := h.notices(w.ID); len(got) != 0 {
		t.Fatalf("notices = %q, want none", bodies(got))
	}

	// Approved again, the next event recovers it and the need is told.
	h.bob.Pending = false
	if err := h.db.UpdateMember(h.ctx, h.bob); err != nil {
		t.Fatal(err)
	}
	h.publish(run.ID, events.RunStatusPayload{To: domain.RunNeedsAttention})
	h.eventually("the recovered need", func() bool { return len(h.notices(w.ID)) == 1 })
	if state := h.watchState(w.ID).State; state != protocol.WatchActive {
		t.Fatalf("state = %s, want active", state)
	}

	if err := h.db.DeleteMember(h.ctx, h.bob.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.GetWatch(h.ctx, w.ID); err == nil {
		t.Fatal("a deleted member's watch survived")
	}
}

func TestWatchExpires(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	run := h.run(h.ada, "Long task")
	res, err := h.svc.Create(h.ctx, h.ada.ID, protocol.WatchCreateParams{
		Target: runTarget(run), ExpiresAt: h.clock.now().Add(time.Hour).Format(time.RFC3339),
	})
	if err != nil {
		t.Fatal(err)
	}
	h.clock.advance(2 * time.Hour)
	h.eventually("expiry", func() bool { return h.watchState(res.Watch.ID).State == protocol.WatchExpired })
	h.status(run, domain.RunNeedsAttention, domain.ActivityNeedsYou, "needs input: still there?")
	settle()
	if got := h.notices(res.Watch.ID); len(got) != 0 {
		t.Fatalf("notices after expiry = %q, want none", bodies(got))
	}
	h.clock.advance(Retention)
	h.eventually("the purge", func() bool {
		_, err := h.db.GetWatch(h.ctx, res.Watch.ID)
		return err != nil
	})
}

func TestQuietHoursHoldPushesAndSendOneWhenTheyEnd(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	phone := h.subscribe(h.ada)
	now := h.clock.now().UTC()
	start, end := now.Add(-time.Hour), now.Add(time.Hour)
	if _, err := h.svc.SetPrefs(h.ctx, h.ada.ID, protocol.NotifyPrefs{QuietHours: &protocol.QuietHours{
		Start: start.Format("15:04"), End: end.Format("15:04"), TZ: "UTC",
	}}); err != nil {
		t.Fatal(err)
	}
	a, b := h.run(h.ada, "First"), h.run(h.ada, "Second")
	wa, wb := h.watch(h.ada, runTarget(a)).Watch, h.watch(h.ada, runTarget(b)).Watch
	h.status(a, domain.RunNeedsAttention, domain.ActivityNeedsYou, "blocked: one")
	h.status(b, domain.RunNeedsAttention, domain.ActivityNeedsYou, "blocked: two")
	h.eventually("both held", func() bool {
		return h.pushStatus(wa.ID) == protocol.WatchStatusHeldQuiet && h.pushStatus(wb.ID) == protocol.WatchStatusHeldQuiet
	})

	// The hold survives a restart, and the end of quiet hours sends one.
	_ = h.svc.Close()
	h.start()
	h.clock.advance(time.Hour + time.Minute)
	h.eventually("the coalesced push", func() bool { return phone.received.Load() == 1 })
	settle()
	if n := phone.received.Load(); n != 1 {
		t.Fatalf("device received %d pushes, want one for both notices", n)
	}
	for _, id := range []string{wa.ID, wb.ID} {
		if s := h.pushStatus(id); s != protocol.WatchStatusDelivered {
			t.Fatalf("push of %s = %s, want delivered", id, s)
		}
	}
}

func TestADeletedWatchDoesNotCostAnotherItsPush(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	phone := h.subscribe(h.ada)
	h.push.Active(h.ada.ID)
	a, b := h.run(h.ada, "Kept"), h.run(h.ada, "Deleted")
	wa, wb := h.watch(h.ada, runTarget(a)).Watch, h.watch(h.ada, runTarget(b)).Watch
	h.status(a, domain.RunNeedsAttention, domain.ActivityNeedsYou, "blocked: one")
	h.status(b, domain.RunNeedsAttention, domain.ActivityNeedsYou, "blocked: two")
	h.eventually("both notices", func() bool { return len(h.notices(wa.ID)) == 1 && len(h.notices(wb.ID)) == 1 })
	batch := []*store.WatchNotification{h.notices(wb.ID)[0], h.notices(wa.ID)[0]}
	watches := map[string]*store.Watch{wa.ID: h.watchState(wa.ID), wb.ID: h.watchState(wb.ID)}
	if err := h.svc.Delete(h.ctx, h.ada.ID, wb.ID); err != nil {
		t.Fatal(err)
	}
	// The delivery loop is stopped, so this send is the batch's only one.
	_ = h.svc.Close()
	var built []*store.WatchNotification
	if err := h.svc.send(h.ctx, h.ada.ID, batch, watches, func(reserved []*store.WatchNotification) push.Message {
		built = reserved
		return push.Message{Title: "1 watch notice", URL: WatchesLink()}
	}); err != nil {
		t.Fatal(err)
	}
	if s := h.pushStatus(wa.ID); s != protocol.WatchStatusDelivered || phone.received.Load() != 1 {
		t.Fatalf("kept watch push = %s, %d sent; want delivered once", s, phone.received.Load())
	}
	if len(built) != 1 || built[0].WatchID != wa.ID {
		t.Fatalf("push built from %d notices, want the kept watch's alone", len(built))
	}
}

func TestPushResultsAreTheDevicesOwn(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	none := h.run(h.ada, "No device")
	w := h.watch(h.ada, runTarget(none)).Watch
	if !strings.Contains(h.watch(h.ada, runTarget(none)).Receipt, "in-app only") {
		t.Fatal("receipt does not say a member without a device is told in-app only")
	}
	h.status(none, domain.RunNeedsAttention, domain.ActivityNeedsYou, "blocked: x")
	h.eventually("no destination", func() bool { return h.pushStatus(w.ID) == protocol.WatchStatusNoDestination })

	phone := h.subscribe(h.bob)
	phone.status.Store(http.StatusInternalServerError)
	failing := h.run(h.bob, "Failing device")
	w = h.watch(h.bob, runTarget(failing)).Watch
	if r := h.watch(h.bob, runTarget(failing)).Receipt; !strings.Contains(r, "Push: 1 device.") {
		t.Fatalf("receipt = %q, want it to count the device", r)
	}
	h.status(failing, domain.RunNeedsAttention, domain.ActivityNeedsYou, "blocked: y")
	for attempt := 1; attempt <= maxPushAttempts; attempt++ {
		h.eventually(fmt.Sprintf("push attempt %d", attempt), func() bool {
			n := h.notices(w.ID)
			return len(n) == 1 && n[0].PushAttempts == attempt &&
				(strings.HasPrefix(pushOf(n[0]).Detail, fmt.Sprintf("Retry %d of", attempt+1)) || pushOf(n[0]).Status == protocol.WatchStatusFailed)
		})
		if d := pushOf(h.notices(w.ID)[0]); attempt == 1 && (d.Status != protocol.WatchStatusPending || !strings.HasPrefix(d.Detail, "Retry 2 of 5 at ")) {
			t.Fatalf("push after a failed attempt = %+v, want pending with the retry", d)
		}
		h.clock.advance(maxBackoff)
	}
	notes := h.notices(w.ID)
	last := pushOf(notes[0])
	if last.Status != protocol.WatchStatusFailed || !strings.Contains(last.Detail, "500") || !strings.Contains(last.Detail, "gave up after 5 attempts") {
		t.Fatalf("last push = %+v, want failed with the push service's error and the give-up", last)
	}

	phone.status.Store(http.StatusCreated)
	ok := h.run(h.bob, "Working device")
	w = h.watch(h.bob, runTarget(ok)).Watch
	h.status(ok, domain.RunNeedsAttention, domain.ActivityNeedsYou, "blocked: z")
	h.eventually("delivered", func() bool { return h.pushStatus(w.ID) == protocol.WatchStatusDelivered })

	// A device its push service reports gone is unsubscribed.
	phone.status.Store(http.StatusGone)
	gone := h.run(h.bob, "Gone device")
	w = h.watch(h.bob, runTarget(gone)).Watch
	h.status(gone, domain.RunNeedsAttention, domain.ActivityNeedsYou, "blocked: g")
	h.eventually("a failed attempt", func() bool {
		n := h.notices(w.ID)
		return len(n) == 1 && strings.HasPrefix(pushOf(n[0]).Detail, "Retry 2 of")
	})
	if n, _ := h.push.Devices(h.ctx, h.bob.ID); n != 0 {
		t.Fatalf("devices after 410 = %d, want 0", n)
	}
}

// setPhase moves the swarm's phase, and on a finish its finish time, on
// the harness's database file directly, which the store offers no method
// for.
func (h *harness) setPhase(mission domain.MissionID, phase domain.MissionPhase) {
	h.t.Helper()
	raw, err := sql.Open("sqlite", "file:"+url.PathEscape(h.path)+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		h.t.Fatal(err)
	}
	defer raw.Close() //nolint:errcheck // test cleanup
	if _, err := raw.ExecContext(h.ctx, `UPDATE missions SET phase = ?1,
		finished_at = CASE WHEN ?1 IN ('completed', 'cancelled') THEN ?2 ELSE finished_at END WHERE id = ?3`,
		phase, time.Now().UnixNano(), mission); err != nil {
		h.t.Fatalf("set swarm phase: %v", err)
	}
}

func TestSwarmWatchSaysSwarmTransitionsOnly(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	m := &domain.Mission{WorkspaceID: h.ws.ID, Objective: "Ship watches\nmore detail", AccountableHumanID: h.ada.ID,
		Integrator: domain.MissionIntegrator{AccountMemberID: h.ada.ID, Harness: "claude", Mode: domain.LaunchTUI}, IdempotencyKey: "swarm"}
	if err := h.db.CreateMission(h.ctx, m); err != nil {
		t.Fatal(err)
	}
	integrator := &domain.Run{ID: m.CurrentIntegratorRunID, WorkspaceID: h.ws.ID, MemberID: h.ada.ID, Task: "integrate",
		Harness: "claude", Mode: domain.LaunchTUI, Status: domain.RunRunning, MissionID: m.ID, MissionRole: "integrator"}
	if err := h.db.CreateRunWithID(h.ctx, integrator); err != nil {
		t.Fatal(err)
	}
	w := h.watch(h.ada, protocol.WatchTarget{Kind: protocol.WatchTargetMission, MissionID: string(m.ID)}).Watch

	// Each open question is a need of its own, whichever is answered first.
	first, err := h.db.InsertMissionQuestion(h.ctx, m.ID, integrator.ID, "merge now?", "q-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.db.InsertMissionQuestion(h.ctx, m.ID, integrator.ID, "which base?", "q-2"); err != nil {
		t.Fatal(err)
	}
	h.swarm.Store(&protocol.AttentionItem{Kind: protocol.AttentionKindSwarmQuestion, Reason: "The integrator asks: merge now?"})
	h.publish(integrator.ID, events.MissionChangedPayload{MissionID: m.ID})
	h.eventually("both swarm questions", func() bool { return len(h.notices(w.ID)) == 2 })
	if got := h.notices(w.ID)[0]; got.Title != "Ship watches" || got.Link != "/?page=missions&id="+string(m.ID) {
		t.Fatalf("notice = %+v", got)
	}
	if got := bodies(h.notices(w.ID)); got[0] != "needs_you: The integrator asks: merge now?" || got[1] != "needs_you: The integrator asks: which base?" {
		t.Fatalf("notices = %q, want each question", got)
	}
	if _, _, err = h.db.AnswerMissionQuestion(h.ctx, first.ID, h.ada.ID, "yes", "a-1"); err != nil {
		t.Fatal(err)
	}
	if _, err = h.db.InsertMissionQuestion(h.ctx, m.ID, integrator.ID, "squash?", "q-3"); err != nil {
		t.Fatal(err)
	}
	h.publish(integrator.ID, events.MissionChangedPayload{MissionID: m.ID})
	h.eventually("the third question", func() bool { return len(h.notices(w.ID)) == 3 })
	h.setPhase(m.ID, domain.MissionPhaseActive)
	// The integrator's own run states are not the swarm's.
	h.swarm.Store(nil)
	h.status(integrator, domain.RunNeedsAttention, domain.ActivityWaitingAgents, "waiting on 2 workers")
	settle()
	if got := h.notices(w.ID); len(got) != 3 {
		t.Fatalf("notices = %q, want the questions alone", bodies(got))
	}

	// A completed swarm can be reopened or still deliver, so its watch
	// stays; a cancelled one is over.
	h.setPhase(m.ID, domain.MissionPhaseCompleted)
	h.publish(integrator.ID, events.MissionChangedPayload{MissionID: m.ID})
	h.eventually("the swarm to complete", func() bool { return len(h.notices(w.ID)) == 4 })
	if got := bodies(h.notices(w.ID)); got[3] != "finished: Swarm completed" || h.watchState(w.ID).State != protocol.WatchActive {
		t.Fatalf("notices = %q, state %s; want Swarm completed last and the watch active", got, h.watchState(w.ID).State)
	}
	// Reopened, it is announced again when it completes again.
	h.setPhase(m.ID, domain.MissionPhaseActive)
	h.publish(integrator.ID, events.MissionChangedPayload{MissionID: m.ID})
	settle()
	h.setPhase(m.ID, domain.MissionPhaseCompleted)
	h.publish(integrator.ID, events.MissionChangedPayload{MissionID: m.ID})
	h.eventually("the second completion", func() bool { return len(h.notices(w.ID)) == 5 })
	h.setPhase(m.ID, domain.MissionPhaseCancelled)
	h.publish(integrator.ID, events.MissionChangedPayload{MissionID: m.ID})
	h.eventually("the watch to end", func() bool { return h.watchState(w.ID).State == protocol.WatchDone })
}

func TestACancelledSwarmNoLongerNeedsYou(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.subscribe(h.ada)
	m := &domain.Mission{WorkspaceID: h.ws.ID, Objective: "Ship watches", AccountableHumanID: h.ada.ID,
		Integrator: domain.MissionIntegrator{AccountMemberID: h.ada.ID, Harness: "claude", Mode: domain.LaunchTUI}, IdempotencyKey: "swarm"}
	if err := h.db.CreateMission(h.ctx, m); err != nil {
		t.Fatal(err)
	}
	integrator := &domain.Run{ID: m.CurrentIntegratorRunID, WorkspaceID: h.ws.ID, MemberID: h.ada.ID, Task: "integrate",
		Harness: "claude", Mode: domain.LaunchTUI, Status: domain.RunRunning, MissionID: m.ID, MissionRole: "integrator"}
	if err := h.db.CreateRunWithID(h.ctx, integrator); err != nil {
		t.Fatal(err)
	}
	w := h.watch(h.ada, protocol.WatchTarget{Kind: protocol.WatchTargetMission, MissionID: string(m.ID)}).Watch
	if _, err := h.db.InsertMissionQuestion(h.ctx, m.ID, integrator.ID, "merge now?", "q-1"); err != nil {
		t.Fatal(err)
	}
	h.publish(integrator.ID, events.MissionChangedPayload{MissionID: m.ID})
	h.eventually("the question's push", func() bool { return h.pushStatus(w.ID) == protocol.WatchStatusDelivered })

	// Its question stays unanswered, but nobody can act on it any more.
	h.setPhase(m.ID, domain.MissionPhaseCancelled)
	h.publish(integrator.ID, events.MissionChangedPayload{MissionID: m.ID})
	h.eventually("the watch to end", func() bool { return h.watchState(w.ID).State == protocol.WatchDone })
	h.eventually("the withdrawal", func() bool { return pushOf(h.notices(w.ID)[0]).Status == protocol.WatchStatusWithdrawn })
	var observed protocol.WatchObserved
	if err := json.Unmarshal(h.watchState(w.ID).Observed, &observed); err != nil || observed.Need != "" || observed.Summary != "Swarm cancelled" {
		t.Fatalf("observed = %+v, %v; want no need and Swarm cancelled", observed, err)
	}
	if got := bodies(h.notices(w.ID)); len(got) != 2 || got[1] != "finished: Swarm cancelled" {
		t.Fatalf("notices = %q, want the question and the finish", got)
	}
}

func TestAnIntegratorThatNeverLaunchedNeedsYou(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	m := &domain.Mission{WorkspaceID: h.ws.ID, Objective: "Unlaunched", AccountableHumanID: h.ada.ID,
		Integrator: domain.MissionIntegrator{AccountMemberID: h.ada.ID, Harness: "claude", Mode: domain.LaunchTUI}, IdempotencyKey: "unlaunched"}
	if err := h.db.CreateMission(h.ctx, m); err != nil {
		t.Fatal(err)
	}
	w := h.watch(h.ada, protocol.WatchTarget{Kind: protocol.WatchTargetMission, MissionID: string(m.ID)}).Watch
	if _, err := h.db.RecordIntegratorLaunch(h.ctx, m.ID, m.CurrentIntegratorRunID, "capture base: repository is empty", false, time.Now()); err != nil {
		t.Fatal(err)
	}
	h.publish("", events.MissionChangedPayload{MissionID: m.ID})
	h.eventually("the launch failure", func() bool { return len(h.notices(w.ID)) == 1 })
	if got := bodies(h.notices(w.ID)); got[0] != "needs_you: Integrator failed to launch, replace it to continue: capture base: repository is empty" {
		t.Fatalf("notices = %q", got)
	}
}

func TestPushOfAPausedRunWaitsForItsResumption(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	phone := h.subscribe(h.ada)
	now := h.clock.now().UTC()
	if _, err := h.svc.SetPrefs(h.ctx, h.ada.ID, protocol.NotifyPrefs{QuietHours: &protocol.QuietHours{
		Start: now.Add(-time.Hour).Format("15:04"), End: now.Add(time.Hour).Format("15:04"), TZ: "UTC",
	}}); err != nil {
		t.Fatal(err)
	}
	run := h.run(h.ada, "Paused mid-question")
	w := h.watch(h.ada, runTarget(run)).Watch
	h.status(run, domain.RunNeedsAttention, domain.ActivityNeedsYou, "needs input: keep going?")
	h.eventually("the held push", func() bool { return h.pushStatus(w.ID) == protocol.WatchStatusHeldQuiet })

	h.runs.pause(run.ID, true)
	if _, err := h.svc.SetPrefs(h.ctx, h.ada.ID, protocol.NotifyPrefs{}); err != nil {
		t.Fatal(err)
	}
	settle()
	if s := h.pushStatus(w.ID); s != protocol.WatchStatusHeldQuiet || phone.received.Load() != 0 {
		t.Fatalf("push while the run is paused = %s, %d sent; want it still queued", s, phone.received.Load())
	}
	h.runs.pause(run.ID, false)
	h.eventually("the push after resumption", func() bool { return h.pushStatus(w.ID) == protocol.WatchStatusDelivered })
}

func TestWatchingAFinishedTargetAnnouncesOnlyWhatComesNext(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	run := h.run(h.ada, "Already done")
	if _, err := h.db.FinishRunReported(h.ctx, run.ID, domain.RunCompleted, domain.ReportedSuccessReason, "", nil, nil); err != nil {
		t.Fatal(err)
	}
	w := h.watch(h.ada, runTarget(run)).Watch
	settle()
	if got := h.notices(w.ID); len(got) != 0 {
		t.Fatalf("notices = %q, want none for a finish reached before the watch", bodies(got))
	}
	h.status(run, domain.RunMerged, "", "closed")
	h.eventually("the watch to finish", func() bool { return h.watchState(w.ID).State == protocol.WatchDone })
	if got := bodies(h.notices(w.ID)); len(got) != 1 || got[0] != "finished: Closed as merged" {
		t.Fatalf("notices = %q, want Closed as merged alone", got)
	}

	m := &domain.Mission{WorkspaceID: h.ws.ID, Objective: "Done swarm", AccountableHumanID: h.ada.ID,
		Integrator: domain.MissionIntegrator{AccountMemberID: h.ada.ID, Harness: "claude", Mode: domain.LaunchTUI}, IdempotencyKey: "done"}
	if err := h.db.CreateMission(h.ctx, m); err != nil {
		t.Fatal(err)
	}
	h.setPhase(m.ID, domain.MissionPhaseCompleted)
	sw := h.watch(h.ada, protocol.WatchTarget{Kind: protocol.WatchTargetMission, MissionID: string(m.ID)}).Watch
	settle()
	if got := h.notices(sw.ID); len(got) != 0 || h.watchState(sw.ID).State != protocol.WatchActive {
		t.Fatalf("notices = %q, want none and the watch active for a swarm completed before it", bodies(got))
	}
}

// slowList widens the gap between looking for a matching watch and
// inserting one.
type slowList struct{ *store.DB }

func (s slowList) ListWatches(ctx context.Context, f store.WatchFilter) ([]*store.Watch, error) {
	watches, err := s.DB.ListWatches(ctx, f)
	time.Sleep(50 * time.Millisecond)
	return watches, err
}

func TestConcurrentCreatesMakeOneWatch(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	svc, err := New(Config{Store: slowList{h.db}, Bus: h.bus, Runs: h.runs, Swarm: h.swarmOf, Push: h.push, Now: h.clock.now})
	if err != nil {
		t.Fatal(err)
	}
	run := h.run(h.ada, "Popular run")
	ids := make([]string, 8)
	var wg sync.WaitGroup
	for i := range ids {
		wg.Go(func() {
			res, cerr := svc.Create(h.ctx, h.ada.ID, protocol.WatchCreateParams{Target: runTarget(run)})
			if cerr != nil {
				t.Errorf("Create: %v", cerr)
				return
			}
			ids[i] = res.Watch.ID
		})
	}
	wg.Wait()
	watches, err := h.db.ListWatches(h.ctx, store.WatchFilter{MemberID: h.ada.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(watches) != 1 {
		t.Fatalf("watches = %d, want one", len(watches))
	}
	for _, id := range ids {
		if id != watches[0].ID {
			t.Fatalf("Create returned %s, want every call to return %s", id, watches[0].ID)
		}
	}
}

type rateLimited struct{ at time.Time }

func (e rateLimited) Error() string      { return "GitHub rate limit exceeded" }
func (e rateLimited) RetryAt() time.Time { return e.at }

// fakePR fails with err while it is set.
type fakePR struct{ err atomic.Pointer[error] }

func (f *fakePR) Validate(context.Context, domain.MemberID, protocol.WatchTarget) (string, error) {
	return "", nil
}

func (f *fakePR) Evaluate(context.Context, PRCheck) (PRResult, error) {
	if err := f.err.Load(); err != nil {
		return PRResult{}, *err
	}
	return PRResult{Observed: protocol.WatchObserved{PRState: "open"}}, nil
}

func TestPullRequestFailureKeepsTheRunsOwnNotices(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	pr := &fakePR{}
	h.pr = pr
	_ = h.svc.Close()
	h.start()
	run := h.run(h.ada, "Open the PR")
	res, err := h.svc.Create(h.ctx, h.ada.ID, protocol.WatchCreateParams{Target: runTarget(run), Conditions: []string{"needs_you", "ci"}})
	if err != nil {
		t.Fatal(err)
	}
	retry := h.clock.now().Add(time.Hour)
	var limited error = rateLimited{at: retry}
	pr.err.Store(&limited)

	h.status(run, domain.RunNeedsAttention, domain.ActivityNeedsYou, "needs input: which base?")
	h.eventually("the run's need", func() bool { return len(h.notices(res.Watch.ID)) == 1 })
	w := h.watchState(res.Watch.ID)
	if w.State != protocol.WatchError || w.Error != "GitHub rate limit exceeded" {
		t.Fatalf("watch = %s %q, want error with the pull request's error", w.State, w.Error)
	}
	if w.NextCheckAt == nil || w.NextCheckAt.Before(retry) {
		t.Fatalf("next check = %v, want the rate limit's retry time %v", w.NextCheckAt, retry)
	}

	pr.err.Store(nil)
	h.publish(run.ID, events.RunStatusPayload{To: domain.RunNeedsAttention})
	h.eventually("recovery", func() bool { return h.watchState(res.Watch.ID).State == protocol.WatchActive })
	if got := h.notices(res.Watch.ID); len(got) != 1 {
		t.Fatalf("notices = %q, want the need once", bodies(got))
	}
}

func TestTurningQuietHoursOffReleasesHeldPushesExceptPausedOnes(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	phone := h.subscribe(h.ada)
	now := h.clock.now().UTC()
	if _, err := h.svc.SetPrefs(h.ctx, h.ada.ID, protocol.NotifyPrefs{QuietHours: &protocol.QuietHours{
		Start: now.Add(-time.Hour).Format("15:04"), End: now.Add(time.Hour).Format("15:04"), TZ: "UTC",
	}}); err != nil {
		t.Fatal(err)
	}
	a, b := h.run(h.ada, "Kept"), h.run(h.ada, "Paused")
	wa, wb := h.watch(h.ada, runTarget(a)).Watch, h.watch(h.ada, runTarget(b)).Watch
	h.status(a, domain.RunNeedsAttention, domain.ActivityNeedsYou, "blocked: one")
	h.status(b, domain.RunNeedsAttention, domain.ActivityNeedsYou, "blocked: two")
	h.eventually("both held", func() bool {
		return h.pushStatus(wa.ID) == protocol.WatchStatusHeldQuiet && h.pushStatus(wb.ID) == protocol.WatchStatusHeldQuiet
	})
	paused := true
	if _, err := h.svc.Update(h.ctx, h.ada.ID, protocol.WatchUpdateParams{WatchID: wb.ID, Paused: &paused}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.SetPrefs(h.ctx, h.ada.ID, protocol.NotifyPrefs{}); err != nil {
		t.Fatal(err)
	}
	h.eventually("the released push", func() bool { return phone.received.Load() == 1 })
	settle()
	if s := h.pushStatus(wa.ID); s != protocol.WatchStatusDelivered {
		t.Fatalf("kept watch push = %s, want delivered", s)
	}
	if s := h.pushStatus(wb.ID); s != protocol.WatchStatusHeldQuiet || phone.received.Load() != 1 {
		t.Fatalf("paused watch push = %s, want it still held", s)
	}
	// A batch listed before the pause does not send it either.
	if err := h.svc.deliverMember(h.ctx, h.ada.ID, h.notices(wb.ID)); err != nil {
		t.Fatal(err)
	}
	if s := h.pushStatus(wb.ID); s != protocol.WatchStatusHeldQuiet || phone.received.Load() != 1 {
		t.Fatalf("paused watch push from a stale batch = %s, want it still held", s)
	}

	// Paused until it expires, its held push is dropped, never sent.
	h.clock.advance(DefaultExpiry + time.Hour)
	h.eventually("the paused push to be dropped", func() bool { return h.pushStatus(wb.ID) == protocol.WatchStatusCleared })
	settle()
	if n := phone.received.Load(); n != 1 {
		t.Fatalf("device received %d pushes, want the paused watch's never sent", n)
	}
}
