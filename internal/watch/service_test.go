package watch

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/tls"
	"database/sql"
	"encoding/base64"
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

type fakeRuns struct {
	mu      sync.Mutex
	inputs  map[domain.RunID][]domain.RunInputRequest
	pending map[domain.RunID][]acphost.Request
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

func (f *fakeRuns) Paused(domain.RunID) bool { return false }

func (f *fakeRuns) ask(run domain.RunID, id, title string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inputs[run] = []domain.RunInputRequest{{ID: id, SessionID: "s1", Kind: "question"}}
	f.pending[run] = []acphost.Request{{ID: id, Kind: "question", Title: title}}
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
}

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
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()
	h := &harness{t: t, ctx: ctx, path: filepath.Join(t.TempDir(), "aether.db"), clock: &clock{},
		runs: &fakeRuns{inputs: map[domain.RunID][]domain.RunInputRequest{}, pending: map[domain.RunID][]acphost.Request{}}}
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
		Debounce: 50 * time.Millisecond, Tick: 50 * time.Millisecond, Now: h.clock.now,
	})
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

// subscribe gives member a device whose push service answers 201.
func (h *harness) subscribe(member *domain.Member) *device {
	h.t.Helper()
	d := &device{}
	d.status.Store(http.StatusCreated)
	d.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if status := int(d.status.Load()); status != http.StatusCreated {
			http.Error(w, "push service refused", status)
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

func pushStatuses(n *store.WatchNotification) []string {
	var out []string
	for _, d := range n.Deliveries {
		if d.Channel == protocol.WatchChannelPush {
			out = append(out, d.Status)
		}
	}
	return out
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
	if s := pushStatuses(h.notices(w.ID)[0]); len(s) != 1 || s[0] != protocol.WatchStatusDelivered {
		t.Fatalf("push deliveries = %v, want [delivered]", s)
	}

	h.runs.ask(run.ID, "q2", "Deploy to staging first?")
	h.publish(run.ID, events.RunInputPayload{PendingInputs: h.runs.PendingInputs(run.ID)})
	h.eventually("the second need", func() bool { return len(h.notices(w.ID)) == 2 })
	if got := h.notices(w.ID)[1].Body; got != "Question: Deploy to staging first?" {
		t.Fatalf("question notice = %q, want the question's text", got)
	}

	h.runs.answer(run.ID)
	if _, err := h.db.FinishRunReported(h.ctx, run.ID, domain.RunCompleted, "agent reported success", "", nil, nil); err != nil {
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
	settle()
	if got := h.notices(w.ID); len(got) != 0 {
		t.Fatalf("notices from a stale event = %q, want none", bodies(got))
	}
	if got := h.watchState(w.ID).Observed; !strings.Contains(string(got), `"activity":"working"`) {
		t.Fatalf("observed = %s, want the run as it stands", got)
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
	h.runs.answer(run.ID)
	h.publish(run.ID, events.RunInputPayload{PendingInputs: []domain.RunInputRequest{}})
	h.eventually("the push to settle", func() bool { return len(pushStatuses(h.notices(w.ID)[0])) == 1 })
	if s := pushStatuses(h.notices(w.ID)[0]); s[0] != protocol.WatchStatusCleared {
		t.Fatalf("push = %v, want cleared", s)
	}
	if n := phone.received.Load(); n != 0 {
		t.Fatalf("device received %d pushes, want none", n)
	}
}

func TestRevokedAccessStopsNoticesWithoutAnnouncingThem(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	run := h.run(h.ada, "Audit the logs")
	w := h.watch(h.bob, runTarget(run)).Watch

	h.bob.Pending = true
	if err := h.db.UpdateMember(h.ctx, h.bob); err != nil {
		t.Fatal(err)
	}
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
	if _, err := h.svc.SetPrefs(h.ctx, h.ada.ID, protocol.NotifyPrefs{Push: true, QuietHours: &protocol.QuietHours{
		Start: start.Format("15:04"), End: end.Format("15:04"), TZ: "UTC",
	}}); err != nil {
		t.Fatal(err)
	}
	a, b := h.run(h.ada, "First"), h.run(h.ada, "Second")
	wa, wb := h.watch(h.ada, runTarget(a)).Watch, h.watch(h.ada, runTarget(b)).Watch
	h.status(a, domain.RunNeedsAttention, domain.ActivityNeedsYou, "blocked: one")
	h.status(b, domain.RunNeedsAttention, domain.ActivityNeedsYou, "blocked: two")
	h.eventually("both held", func() bool {
		na, nb := h.notices(wa.ID), h.notices(wb.ID)
		return len(na) == 1 && len(nb) == 1 && len(pushStatuses(na[0])) == 1 && len(pushStatuses(nb[0])) == 1
	})
	if s := pushStatuses(h.notices(wa.ID)[0]); s[0] != protocol.WatchStatusHeldQuiet {
		t.Fatalf("push = %v, want held_quiet_hours", s)
	}

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
		s := pushStatuses(h.notices(id)[0])
		if len(s) != 2 || s[1] != protocol.WatchStatusDelivered {
			t.Fatalf("push of %s = %v, want held_quiet_hours then delivered", id, s)
		}
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
	h.eventually("no destination", func() bool {
		n := h.notices(w.ID)
		return len(n) == 1 && len(pushStatuses(n[0])) == 1
	})
	if s := pushStatuses(h.notices(w.ID)[0]); s[0] != protocol.WatchStatusNoDestination {
		t.Fatalf("push = %v, want no_destination", s)
	}

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
			return len(n) == 1 && len(pushStatuses(n[0])) == attempt
		})
		h.clock.advance(maxBackoff)
	}
	notes := h.notices(w.ID)
	last := notes[0].Deliveries[len(notes[0].Deliveries)-1]
	if last.Status != protocol.WatchStatusFailed || !strings.Contains(last.Detail, "500") || !strings.Contains(last.Detail, "gave up after 5 attempts") {
		t.Fatalf("last push = %+v, want failed with the push service's error and the give-up", last)
	}

	phone.status.Store(http.StatusCreated)
	ok := h.run(h.bob, "Working device")
	w = h.watch(h.bob, runTarget(ok)).Watch
	h.status(ok, domain.RunNeedsAttention, domain.ActivityNeedsYou, "blocked: z")
	h.eventually("delivered", func() bool {
		n := h.notices(w.ID)
		return len(n) == 1 && len(pushStatuses(n[0])) == 1
	})
	got := h.notices(w.ID)[0].Deliveries
	if got[1].Status != protocol.WatchStatusDelivered || got[1].Detail != "1 device" {
		t.Fatalf("push = %+v, want delivered to 1 device", got[1])
	}

	// A device its push service reports gone is unsubscribed.
	phone.status.Store(http.StatusGone)
	gone := h.run(h.bob, "Gone device")
	w = h.watch(h.bob, runTarget(gone)).Watch
	h.status(gone, domain.RunNeedsAttention, domain.ActivityNeedsYou, "blocked: g")
	h.eventually("failed", func() bool {
		n := h.notices(w.ID)
		return len(n) == 1 && len(pushStatuses(n[0])) == 1
	})
	if n, _ := h.push.Devices(h.ctx, h.bob.ID); n != 0 {
		t.Fatalf("devices after 410 = %d, want 0", n)
	}
}

// exec runs a statement on the harness's database file directly, for
// writes the store offers no method for.
func (h *harness) exec(query string, args ...any) {
	h.t.Helper()
	raw, err := sql.Open("sqlite", "file:"+url.PathEscape(h.path)+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		h.t.Fatal(err)
	}
	defer raw.Close() //nolint:errcheck // test cleanup
	if _, err := raw.ExecContext(h.ctx, query, args...); err != nil {
		h.t.Fatalf("exec %q: %v", query, err)
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
	h.exec(`UPDATE missions SET phase = ? WHERE id = ?`, domain.MissionPhaseActive, m.ID)
	integrator := &domain.Run{ID: m.CurrentIntegratorRunID, WorkspaceID: h.ws.ID, MemberID: h.ada.ID, Task: "integrate",
		Harness: "claude", Mode: domain.LaunchTUI, Status: domain.RunRunning}
	if err := h.db.CreateRunWithID(h.ctx, integrator); err != nil {
		t.Fatal(err)
	}
	w := h.watch(h.ada, protocol.WatchTarget{Kind: protocol.WatchTargetMission, MissionID: string(m.ID)}).Watch

	h.swarm.Store(&protocol.AttentionItem{Kind: protocol.AttentionKindSwarmQuestion, Reason: "The integrator asks: merge now?",
		Message: &protocol.AttentionMessage{ID: "q-1", Body: "merge now?"}})
	h.publish(integrator.ID, events.MissionChangedPayload{MissionID: m.ID})
	h.eventually("the swarm question", func() bool { return len(h.notices(w.ID)) == 1 })
	if got := h.notices(w.ID)[0]; got.Title != "Ship watches" || got.Body != "The integrator asks: merge now?" || got.Link != "/?page=missions&id="+string(m.ID) {
		t.Fatalf("notice = %+v", got)
	}
	// The integrator's own run states are not the swarm's.
	h.swarm.Store(nil)
	h.status(integrator, domain.RunNeedsAttention, domain.ActivityWaitingAgents, "waiting on 2 workers")
	settle()
	if got := h.notices(w.ID); len(got) != 1 {
		t.Fatalf("notices = %q, want the question alone", bodies(got))
	}

	h.exec(`UPDATE missions SET phase = ? WHERE id = ?`, domain.MissionPhaseCompleted, m.ID)
	h.publish(integrator.ID, events.MissionChangedPayload{MissionID: m.ID})
	h.eventually("the swarm to finish", func() bool { return h.watchState(w.ID).State == protocol.WatchDone })
	if got := bodies(h.notices(w.ID)); len(got) != 2 || got[1] != "finished: Swarm completed" {
		t.Fatalf("notices = %q, want Swarm completed last", got)
	}
}
