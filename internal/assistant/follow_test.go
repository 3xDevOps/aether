package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/acphost/acpmock"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/store"
)

type fakeFollows struct {
	mu       sync.Mutex
	gone     bool
	run      *domain.Run
	approval *store.Approval
	follows  map[domain.MemberID]*store.AssistantFollow
}

func (f *fakeFollows) PutAssistantFollow(_ context.Context, m domain.MemberID, r domain.RunID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.follows[m] = &store.AssistantFollow{MemberID: m, RunID: r}
	return nil
}

func (f *fakeFollows) ListAssistantFollows(context.Context, domain.RunID) ([]*store.AssistantFollow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*store.AssistantFollow
	for _, fl := range f.follows {
		c := *fl
		out = append(out, &c)
	}
	return out, nil
}

func (f *fakeFollows) SetAssistantFollowNotified(_ context.Context, m domain.MemberID, _ domain.RunID, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.follows[m].NotifiedKey = key
	return nil
}

func (f *fakeFollows) DeleteAssistantFollow(_ context.Context, m domain.MemberID, _ domain.RunID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.follows, m)
	return nil
}

func (f *fakeFollows) GetRun(context.Context, domain.RunID) (*domain.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := *f.run
	return &c, nil
}

func (f *fakeFollows) GetMember(ctx context.Context, id domain.MemberID) (*domain.Member, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.gone {
		return nil, store.ErrNotFound
	}
	return &domain.Member{ID: id}, nil
}

func (f *fakeFollows) ListFollowedRuns(context.Context) ([]domain.RunID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.follows) == 0 {
		return nil, nil
	}
	return []domain.RunID{f.run.ID}, nil
}

func (f *fakeFollows) setRun(r *domain.Run) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.run = r
}

func (f *fakeFollows) notified(m domain.MemberID) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fl := f.follows[m]
	if fl == nil {
		return "", false
	}
	return fl.NotifiedKey, true
}

func (f *fakeFollows) ListApprovals(context.Context, domain.WorkspaceID, string) ([]*store.Approval, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.approval == nil {
		return nil, nil
	}
	return []*store.Approval{f.approval}, nil
}

type fakeRunState struct {
	inputs  []domain.RunInputRequest
	pending []acphost.Request
	paused  atomic.Bool
}

func (f *fakeRunState) PendingInputs(domain.RunID) []domain.RunInputRequest { return f.inputs }
func (f *fakeRunState) ACPPending(domain.RunID) []acphost.Request           { return f.pending }
func (f *fakeRunState) Paused(domain.RunID) bool                            { return f.paused.Load() }

// newRun is a run as the scheduler leaves it: a live run's activity follows
// its reason, a request for a human or an idle turn end.
func newRun(status domain.RunStatus, reason string) *domain.Run {
	changed := time.Unix(100, 5)
	run := &domain.Run{ID: "run-1", WorkspaceID: "ws-1", MemberID: "m1", Title: "Fix login", Status: status, Reason: reason, StatusChangedAt: &changed, StatusSeq: 1}
	switch {
	case status == domain.RunRunning:
		run.Activity = domain.ActivityWorking
	case status == domain.RunNeedsAttention && (strings.HasPrefix(reason, "blocked: ") || strings.HasPrefix(reason, "needs input: ")):
		run.Activity = domain.ActivityNeedsYou
	case status == domain.RunNeedsAttention:
		run.Activity = domain.ActivityIdle
	}
	return run
}

func withActivity(run *domain.Run, activity domain.RunActivity) *domain.Run {
	run.Activity = activity
	return run
}

func unseen(run *domain.Run) *domain.Run {
	run.OutcomeUnseen = true
	return run
}

func TestClassify(t *testing.T) {
	t.Parallel()
	input := domain.RunInputRequest{ID: "req-1", SessionID: "s1", Kind: "question"}
	tests := []struct {
		name     string
		run      *domain.Run
		inputs   []domain.RunInputRequest
		pending  []acphost.Request
		approval *store.Approval
		paused   bool
		kind     string
		severity string
		title    string
		body     string
	}{
		{name: "working", run: newRun(domain.RunRunning, "")},
		{name: "queued", run: newRun(domain.RunQueued, "")},
		{name: "completed", run: newRun(domain.RunCompleted, "agent exited 0"), kind: "finished", severity: "info", title: `Run "Fix login" finished`, body: "agent exited 0"},
		{name: "failed", run: newRun(domain.RunFailed, "agent exited 1"), kind: "finished", severity: "info", title: `Run "Fix login" failed`, body: "agent exited 1"},
		{name: "abandoned", run: newRun(domain.RunAbandoned, ""), kind: "finished", severity: "info", title: `Run "Fix login" was stopped`},
		{name: "interrupted", run: newRun(domain.RunInterrupted, ""), kind: "finished", severity: "info", title: `Run "Fix login" was interrupted`},
		{name: "merged", run: newRun(domain.RunMerged, ""), kind: "finished", severity: "info", title: `Run "Fix login" was merged`},
		{
			name: "question", run: newRun(domain.RunRunning, ""), inputs: []domain.RunInputRequest{input},
			pending: []acphost.Request{{ID: "req-1", Kind: acphost.RequestQuestion, Title: "Which database?"}},
			kind:    "needs_input", severity: "warning", title: `Run "Fix login" needs your input`, body: "Question: Which database?",
		},
		{
			name: "form", run: newRun(domain.RunRunning, ""), inputs: []domain.RunInputRequest{input},
			pending: []acphost.Request{{ID: "req-1", Kind: acphost.RequestQuestion, Title: "Deploy settings", Schema: json.RawMessage(`{}`)}},
			kind:    "needs_input", severity: "warning", title: `Run "Fix login" needs your input`, body: "Form: Deploy settings",
		},
		{
			name: "permission", run: newRun(domain.RunRunning, ""), inputs: []domain.RunInputRequest{{ID: "req-1", SessionID: "s1", Kind: "permission"}},
			pending: []acphost.Request{{ID: "req-1", Kind: acphost.RequestPermission, Title: "Run rm -rf build"}},
			kind:    "needs_input", severity: "warning", title: `Run "Fix login" needs your input`, body: "Permission: Run rm -rf build",
		},
		{
			name: "approval", run: newRun(domain.RunRunning, ""), approval: &store.Approval{ID: "ap-1", Action: "ExitPlanMode"},
			kind: "needs_approval", severity: "warning", title: `Run "Fix login" needs your approval`, body: "ExitPlanMode",
		},
		{
			name: "attention", run: newRun(domain.RunNeedsAttention, "blocked: no credentials"),
			kind: "needs_attention", severity: "warning", title: `Run "Fix login" needs your attention`, body: "Blocked: no credentials",
		},
		{name: "idle", run: newRun(domain.RunNeedsAttention, "agent idle")},
		{name: "stalled", run: newRun(domain.RunNeedsAttention, "stalled: no output or file changes for 10m0s")},
		{name: "waiting on agents", run: withActivity(newRun(domain.RunNeedsAttention, "waiting on 3 workers"), domain.ActivityWaitingAgents)},
		{name: "waiting on a tool", run: withActivity(newRun(domain.RunRunning, "working in background: 1 task"), domain.ActivityWaitingTools)},
		{
			name: "reported success", run: withActivity(newRun(domain.RunNeedsAttention, "agent reported success"), domain.ActivityFinished),
			kind: "finished", severity: "info", title: `Run "Fix login" finished`, body: "agent reported success",
		},
		{
			name: "reported failure", run: unseen(withActivity(newRun(domain.RunNeedsAttention, "agent reported failure"), domain.ActivityFailed)),
			kind: "needs_attention", severity: "warning", title: `Run "Fix login" failed`, body: "Agent reported failure, review the result",
		},
		{name: "seen failure", run: withActivity(newRun(domain.RunNeedsAttention, "agent reported failure"), domain.ActivityFailed)},
		{name: "paused", run: newRun(domain.RunNeedsAttention, "blocked: no credentials"), paused: true},
		{
			name: "disconnected", run: withActivity(newRun(domain.RunNeedsAttention, "enhanced session ended: exit code 1"), domain.ActivityDisconnected),
			kind: "needs_attention", severity: "warning", title: `Run "Fix login" disconnected`, body: "Agent disconnected: exit code 1",
		},
		{
			name: "input outranks attention", run: newRun(domain.RunNeedsAttention, "needs input: pick"), inputs: []domain.RunInputRequest{input},
			kind: "needs_input", severity: "warning", title: `Run "Fix login" needs your input`, body: "Question: answer in the run",
		},
	}
	for _, tc := range tests {
		got := classify(tc.run, tc.inputs, tc.pending, tc.approval, tc.paused)
		if tc.kind == "" {
			if got != nil {
				t.Errorf("%s: classified as %+v, want nothing", tc.name, got)
			}
			continue
		}
		if got == nil || got.kind != tc.kind || got.severity != tc.severity || got.title != tc.title || got.body != tc.body || got.key == "" {
			t.Errorf("%s: classified as %+v, want %s %q %q", tc.name, got, tc.kind, tc.title, tc.body)
		}
	}
}

func newTestFollower(fs *fakeFollows, rs *fakeRunState, got *[]notice) *follower {
	f := &follower{store: fs, runs: rs, queued: map[[2]string]string{}}
	f.deliver = func(n notice) {
		*got = append(*got, n)
		f.finish(n, true)
	}
	return f
}

func TestCheckAnnouncesEachStateOnceAndEndsTheFollowWhenFinished(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fs := &fakeFollows{run: newRun(domain.RunRunning, ""), follows: map[domain.MemberID]*store.AssistantFollow{}}
	rs := &fakeRunState{}
	var got []notice
	f := newTestFollower(fs, rs, &got)
	_ = fs.PutAssistantFollow(ctx, "m1", "run-1")
	_ = fs.PutAssistantFollow(ctx, "m2", "run-1")

	f.check(ctx, "run-1", false)
	if len(got) != 0 {
		t.Fatalf("a running run announced %+v", got)
	}

	fs.run = newRun(domain.RunNeedsAttention, "blocked: no key")
	f.check(ctx, "run-1", false)
	f.check(ctx, "run-1", false)
	if len(got) != 2 || got[0].kind != "needs_attention" || got[0].member == got[1].member {
		t.Fatalf("one state announced as %+v, want once to each follower", got)
	}

	// A restart rebuilds the follower; what is stored keeps it quiet.
	f = newTestFollower(fs, rs, &got)
	f.check(ctx, "run-1", false)
	if len(got) != 2 {
		t.Fatalf("a restart announced the standing state again: %+v", got)
	}

	rs.inputs = []domain.RunInputRequest{{ID: "req-1", SessionID: "s1", Kind: "question"}}
	f.check(ctx, "run-1", false)
	if len(got) != 4 || got[2].kind != "needs_input" {
		t.Fatalf("a new input announced %+v", got[2:])
	}

	// A message to a run that has not changed is left to the events.
	rs.inputs = nil
	fs.run = newRun(domain.RunNeedsAttention, "blocked: x")
	f.check(ctx, "run-1", true)
	if len(got) != 4 {
		t.Fatalf("a recorded follow announced a non-final state: %+v", got[4:])
	}

	// A parked result is no end: the run can still ask, fail or finish.
	fs.run = withActivity(newRun(domain.RunNeedsAttention, "agent reported success"), domain.ActivityFinished)
	f.check(ctx, "run-1", false)
	if len(got) != 6 || got[4].kind != "finished" || len(fs.follows) != 2 {
		t.Fatalf("a parked success announced %+v with follows %+v, want a notice each and both follows kept", got[4:], fs.follows)
	}
	fs.run = newRun(domain.RunNeedsAttention, "needs input: which region?")
	f.check(ctx, "run-1", false)
	if len(got) != 8 || got[6].kind != "needs_attention" {
		t.Fatalf("a question after a parked success announced %+v", got[6:])
	}

	fs.run = newRun(domain.RunCompleted, "")
	f.check(ctx, "run-1", true)
	if len(got) != 10 || got[8].kind != "finished" {
		t.Fatalf("finish announced %+v", got[8:])
	}
	if len(fs.follows) != 0 {
		t.Fatalf("follows after the run finished: %+v", fs.follows)
	}
	f.check(ctx, "run-1", false)
	if len(got) != 10 {
		t.Fatalf("a finished run announced again: %+v", got[10:])
	}
}

func TestAStateInFlightIsQueuedOnceAndAFailedOneAgain(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fs := &fakeFollows{run: newRun(domain.RunNeedsAttention, "blocked: no key"), follows: map[domain.MemberID]*store.AssistantFollow{}}
	_ = fs.PutAssistantFollow(ctx, "m1", "run-1")
	var queued []notice
	f := &follower{store: fs, runs: &fakeRunState{}, queued: map[[2]string]string{}, deliver: func(n notice) { queued = append(queued, n) }}
	f.check(ctx, "run-1", false)
	f.check(ctx, "run-1", false)
	if len(queued) != 1 {
		t.Fatalf("a state in flight was queued %d times", len(queued))
	}
	f.finish(queued[0], false)
	if key, _ := fs.notified("m1"); key != "" {
		t.Fatalf("an undelivered notice stored its key %q", key)
	}
	f.check(ctx, "run-1", false)
	if len(queued) != 2 {
		t.Fatalf("a notice that failed was not queued again")
	}
}

// A need is announced once however its run's status_seq moves, a swarm
// worker's requests are its integrator's, and a need that stood before the
// server started was announced by the one before.
func TestFollowAnnouncesEachNeedOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	failed := unseen(withActivity(newRun(domain.RunNeedsAttention, "agent reported failure"), domain.ActivityFailed))
	fs := &fakeFollows{run: failed, follows: map[domain.MemberID]*store.AssistantFollow{}}
	rs := &fakeRunState{}
	var got []notice
	f := newTestFollower(fs, rs, &got)
	_ = fs.PutAssistantFollow(ctx, "m2", "run-1")
	f.check(ctx, "run-1", false)
	opened := *failed
	opened.StatusSeq, opened.FinishUnopened = 2, false
	fs.setRun(&opened)
	f.check(ctx, "run-1", false)
	if len(got) != 1 || got[0].title != `Run "Fix login" failed` {
		t.Fatalf("a failure a teammate opened announced %+v, want one notice", got)
	}

	worker := newRun(domain.RunNeedsAttention, "needs input: which region?")
	worker.MissionRole = "worker"
	worker.StatusSeq = 3
	fs.setRun(worker)
	rs.inputs = []domain.RunInputRequest{{ID: "req-1", SessionID: "s1", Kind: "question"}}
	f.check(ctx, "run-1", false)
	rs.inputs = nil
	if len(got) != 1 {
		t.Fatalf("a worker's question announced %+v", got[1:])
	}
	blocked := *worker
	blocked.Reason = "blocked: merge conflict"
	fs.setRun(&blocked)
	f.check(ctx, "run-1", false)
	if len(got) != 2 || got[1].body != "Worker blocked: merge conflict" {
		t.Fatalf("a blocked worker announced %+v", got[1:])
	}

	f = newTestFollower(fs, rs, &got)
	f.started = time.Now()
	fs.setRun(newRun(domain.RunNeedsAttention, "blocked: no key"))
	f.check(ctx, "run-1", false)
	if key, _ := fs.notified("m2"); len(got) != 2 || !strings.Contains(key, "blocked: no key") {
		t.Fatalf("a need from before the start announced %+v and stored %q, want it recorded silently", got[2:], key)
	}
}

// A paused run announces nothing, so resuming it is what announces its need.
func TestAResumedFollowedRunIsAnnounced(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	x := newServiceFixture(t, newRun(domain.RunCompleted, ""))
	x.rs.paused.Store(true)
	_ = x.fs.PutAssistantFollow(ctx, "m1", "run-1")
	if err := x.svc.StartFollowing(ctx); err != nil {
		t.Fatal(err)
	}
	run := newRun(domain.RunNeedsAttention, "blocked: no key")
	changed := time.Now()
	run.StatusChangedAt = &changed
	x.fs.setRun(run)
	publish := func(p events.Payload) {
		if _, err := x.bus.Publish(ctx, events.Event{WorkspaceID: "ws-1", RunID: "run-1", Payload: p}); err != nil {
			t.Fatal(err)
		}
	}
	publish(events.RunStatusPayload{To: domain.RunNeedsAttention, Activity: domain.ActivityNeedsYou})
	time.Sleep(50 * time.Millisecond)
	if k, _ := x.fs.notified("m1"); k != "" {
		t.Fatalf("a paused run was announced: %q", k)
	}
	x.rs.paused.Store(false)
	publish(events.TimelinePayload{Kind: events.TimelineResume})
	eventually(t, "the resumed run's notice", func() bool { k, _ := x.fs.notified("m1"); return k != "" })
	if err := x.svc.Close(); err != nil {
		t.Fatal(err)
	}
}

type serviceFixture struct {
	svc *Service
	fs  *fakeFollows
	rs  *fakeRunState
	bus *events.InProc
}

func newServiceFixture(t *testing.T, run *domain.Run) *serviceFixture {
	t.Helper()
	bus, err := events.NewInProc(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bus.Close() })
	fs := &fakeFollows{run: run, follows: map[domain.MemberID]*store.AssistantFollow{}}
	rs := &fakeRunState{}
	svc := New(Config{Dir: t.TempDir(), Store: newMemStore(), Bus: bus, Follows: fs})
	svc.follower = &follower{store: fs, runs: rs, deliver: svc.enqueue, queued: map[[2]string]string{}}
	return &serviceFixture{svc: svc, fs: fs, rs: rs, bus: bus}
}

func (x *serviceFixture) transcript(member domain.MemberID) string {
	b, _ := os.ReadFile(x.svc.logPath(member))
	return string(b)
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestAStalledMemberDoesNotHoldBackAnother(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	x := newServiceFixture(t, newRun(domain.RunNeedsAttention, "blocked: no key"))
	_ = x.fs.PutAssistantFollow(ctx, "m1", "run-1")
	_ = x.fs.PutAssistantFollow(ctx, "m2", "run-1")
	stalled := x.svc.member("m1")
	stalled.op.Lock()

	x.svc.follower.check(ctx, "run-1", false)
	eventually(t, "m2's notice", func() bool { k, _ := x.fs.notified("m2"); return k != "" })
	if k, _ := x.fs.notified("m1"); k != "" {
		t.Fatalf("m1 was announced while their session was starting: %q", k)
	}
	stalled.op.Unlock()
	eventually(t, "m1's notice", func() bool { k, _ := x.fs.notified("m1"); return k != "" })
	if !strings.Contains(x.transcript("m1"), "needs your attention") || !strings.Contains(x.transcript("m2"), "needs your attention") {
		t.Fatalf("transcripts: %q, %q", x.transcript("m1"), x.transcript("m2"))
	}
	if err := x.svc.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestANoticeQueuedAtShutdownIsAnnouncedAfterTheRestart(t *testing.T) {
	ctx := context.Background()
	was := closeDrain
	closeDrain = 100 * time.Millisecond
	t.Cleanup(func() { closeDrain = was })
	x := newServiceFixture(t, newRun(domain.RunNeedsAttention, "blocked: no key"))
	_ = x.fs.PutAssistantFollow(ctx, "m1", "run-1")
	m := x.svc.member("m1")
	m.op.Lock()
	x.svc.follower.check(ctx, "run-1", false)
	eventually(t, "the first notice to be taken up", func() bool {
		x.svc.dmu.Lock()
		defer x.svc.dmu.Unlock()
		return len(x.svc.queues["m1"]) == 0 && x.svc.delivering["m1"]
	})
	// A second state queues behind the first, which waits on the session.
	x.fs.setRun(newRun(domain.RunCompleted, "agent exited 0"))
	x.svc.follower.check(ctx, "run-1", false)

	closed := make(chan error, 1)
	go func() { closed <- x.svc.Close() }()
	time.Sleep(3 * closeDrain)
	m.op.Unlock()
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if k, ok := x.fs.notified("m1"); !ok || !strings.HasPrefix(k, "attention:") {
		t.Fatalf("after shutdown the follow holds %q (present %v), want the first state announced and the follow kept", k, ok)
	}
	if strings.Contains(x.transcript("m1"), "finished") {
		t.Fatalf("the notice dropped at shutdown reached the transcript: %q", x.transcript("m1"))
	}

	var got []notice
	f := newTestFollower(x.fs, x.rs, &got)
	f.check(ctx, "run-1", false)
	if len(got) != 1 || got[0].kind != "finished" {
		t.Fatalf("after the restart announced %+v, want the finished notice", got)
	}
}

func TestNoNoticeIsWrittenForAMemberBeingRemoved(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	x := newServiceFixture(t, newRun(domain.RunCompleted, ""))
	n := classify(x.fs.run, nil, nil, nil, false)
	n.member = "gone"
	x.fs.gone = true
	if x.svc.deliver(*n) {
		t.Fatal("a notice was delivered to a member whose row is gone")
	}
	n.member = "fenced"
	x.fs.gone = false
	if err := x.svc.Fence(ctx, "fenced"); err != nil {
		t.Fatal(err)
	}
	if x.svc.deliver(*n) {
		t.Fatal("a notice was delivered to a fenced member")
	}
	for _, id := range []domain.MemberID{"gone", "fenced"} {
		if _, err := os.Stat(filepath.Dir(x.svc.logPath(id))); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("files were recreated for %s: %v", id, err)
		}
	}
}

func TestStartupResolvesRunsThatFinishedWhileTheServerWasDown(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	x := newServiceFixture(t, newRun(domain.RunCompleted, "agent exited 0"))
	_ = x.fs.PutAssistantFollow(ctx, "m1", "run-1")
	if err := x.svc.StartFollowing(ctx); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the follow to end", func() bool { _, ok := x.fs.notified("m1"); return !ok })
	if !strings.Contains(x.transcript("m1"), "finished") {
		t.Fatalf("transcript = %q, want the finished notice", x.transcript("m1"))
	}
	if err := x.svc.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPushGoesToFinishesAndToOthersNeedsOnly(t *testing.T) {
	t.Parallel()
	x := newServiceFixture(t, newRun(domain.RunNeedsAttention, "blocked: no key"))
	type call struct {
		member  domain.MemberID
		checked bool
		urgent  bool
	}
	var mu sync.Mutex
	var calls []call
	x.svc.UseNotifier(func(_ context.Context, member domain.MemberID, _, _ string, _ domain.RunID, urgent bool, _ <-chan struct{}, current func(context.Context) bool) error {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, call{member, current != nil, urgent})
		return nil
	})
	needs := classify(x.fs.run, nil, nil, nil, false)
	finished := classify(newRun(domain.RunCompleted, ""), nil, nil, nil, false)
	parked := classify(unseen(withActivity(newRun(domain.RunNeedsAttention, "agent reported success"), domain.ActivityFinished)), nil, nil, nil, false)
	for _, c := range []struct {
		n      *notice
		member domain.MemberID
	}{{needs, "m1"}, {needs, "m2"}, {finished, "m1"}, {parked, "m1"}, {parked, "m2"}} {
		n := *c.n
		n.member = c.member
		if !x.svc.deliver(n) {
			t.Fatal("not delivered")
		}
	}
	x.svc.wg.Wait()
	// The owner's push service announces a parked result itself.
	want := []call{{"m2", true, true}, {"m1", false, false}, {"m2", false, false}}
	byMember := func(a, b call) int { return strings.Compare(fmt.Sprint(a), fmt.Sprint(b)) }
	slices.SortFunc(calls, byMember)
	slices.SortFunc(want, byMember)
	if !slices.Equal(calls, want) {
		t.Fatalf("pushes = %+v, want the owner's finish and the other member's notices only: %+v", calls, want)
	}
}

func TestAFinishThatOverlapsAnOlderNoticeIsQueuedOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fs := &fakeFollows{run: newRun(domain.RunNeedsAttention, "blocked: no key"), follows: map[domain.MemberID]*store.AssistantFollow{}}
	_ = fs.PutAssistantFollow(ctx, "m1", "run-1")
	var queued []notice
	f := &follower{store: fs, runs: &fakeRunState{}, queued: map[[2]string]string{}, deliver: func(n notice) { queued = append(queued, n) }}
	f.check(ctx, "run-1", false)
	fs.setRun(newRun(domain.RunCompleted, ""))
	f.check(ctx, "run-1", false)
	if len(queued) != 2 {
		t.Fatalf("queued %d notices, want the attention and the finish", len(queued))
	}
	f.finish(queued[0], true)
	f.check(ctx, "run-1", false)
	if len(queued) != 2 {
		t.Fatalf("the finish was queued again behind the older notice: %d queued", len(queued))
	}
	f.finish(queued[1], true)
	if len(fs.follows) != 0 || len(f.queued) != 0 {
		t.Fatalf("follows %v and queued %v after the finish", fs.follows, f.queued)
	}
}

func TestANoticeReachesTheTranscriptWhetherOrNotTheSessionTakesIt(t *testing.T) {
	t.Parallel()
	fix, err := acpmock.Load("claude")
	if err != nil {
		t.Fatal(err)
	}
	x := newServiceFixture(t, newRun(domain.RunCompleted, ""))
	agentIn, hostOut := io.Pipe()
	hostIn, agentOut := io.Pipe()
	go acpmock.New(fix).Serve(agentIn, agentOut)
	sess, err := acphost.Start(t.Context(), hostIn, hostOut, acphost.Config{LogPath: x.svc.logPath("m1"), Cwd: t.TempDir(), Logger: slog.Default()})
	if err != nil {
		t.Fatal(err)
	}
	m := x.svc.member("m1")
	m.mu.Lock()
	m.sess = &session{acp: sess, ended: make(chan struct{})}
	m.mu.Unlock()

	n := *classify(x.fs.run, nil, nil, nil, false)
	n.member = "m1"
	if err := x.svc.transcribe(m, n); err != nil {
		t.Fatalf("a live session: %v", err)
	}
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}
	_ = agentOut.Close()
	_ = agentIn.Close()
	<-sess.Done()
	if err := sess.Notice("info", "late", ""); !errors.Is(err, acphost.ErrClosed) {
		t.Fatalf("Notice on a closed session = %v, want ErrClosed", err)
	}
	n.title = "after the close"
	if err := x.svc.transcribe(m, n); err != nil {
		t.Fatalf("a closed session: %v", err)
	}
	log := x.transcript("m1")
	if !strings.Contains(log, `finished`) || !strings.Contains(log, "after the close") {
		t.Fatalf("transcript = %q, want both notices", log)
	}
}

func TestACloseSendsThePushesStillWaitingForTheMember(t *testing.T) {
	ctx := context.Background()
	x := newServiceFixture(t, newRun(domain.RunCompleted, ""))
	sent := make(chan string, 1)
	x.svc.UseNotifier(func(ctx context.Context, _ domain.MemberID, title, _ string, _ domain.RunID, _ bool, flush <-chan struct{}, _ func(context.Context) bool) error {
		select {
		case <-flush:
			sent <- title
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	_ = x.fs.PutAssistantFollow(ctx, "m1", "run-1")
	x.svc.follower.check(ctx, "run-1", false)
	eventually(t, "the finish to be announced", func() bool { _, ok := x.fs.notified("m1"); return !ok })
	if err := x.svc.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case title := <-sent:
		if !strings.Contains(title, "finished") {
			t.Fatalf("pushed %q", title)
		}
	default:
		t.Fatal("Close dropped the push waiting for the member")
	}
}
