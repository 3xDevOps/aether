package assistant

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/store"
)

type fakeFollows struct {
	mu       sync.Mutex
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
}

func (f *fakeRunState) PendingInputs(domain.RunID) []domain.RunInputRequest { return f.inputs }
func (f *fakeRunState) ACPPending(domain.RunID) []acphost.Request           { return f.pending }

func newRun(status domain.RunStatus, reason string) *domain.Run {
	changed := time.Unix(100, 5)
	return &domain.Run{ID: "run-1", WorkspaceID: "ws-1", MemberID: "m1", Title: "Fix login", Status: status, Reason: reason, StatusChangedAt: &changed}
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
		{
			name: "input outranks attention", run: newRun(domain.RunNeedsAttention, "needs input: pick"), inputs: []domain.RunInputRequest{input},
			kind: "needs_input", severity: "warning", title: `Run "Fix login" needs your input`, body: "Question: answer in the run",
		},
	}
	for _, tc := range tests {
		got := classify(tc.run, tc.inputs, tc.pending, tc.approval)
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

func TestCheckAnnouncesEachStateOnceAndEndsTheFollowWhenFinished(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fs := &fakeFollows{run: newRun(domain.RunRunning, ""), follows: map[domain.MemberID]*store.AssistantFollow{}}
	rs := &fakeRunState{}
	var got []notice
	f := &follower{store: fs, runs: rs, deliver: func(_ context.Context, n notice) { got = append(got, n) }}
	_ = fs.PutAssistantFollow(ctx, "m1", "run-1")
	_ = fs.PutAssistantFollow(ctx, "m2", "run-1")

	f.check(ctx, "run-1", false)
	if len(got) != 0 {
		t.Fatalf("a running run announced %+v", got)
	}

	fs.run = newRun(domain.RunNeedsAttention, "agent idle")
	f.check(ctx, "run-1", false)
	f.check(ctx, "run-1", false)
	if len(got) != 2 || got[0].kind != "needs_attention" || got[0].member == got[1].member {
		t.Fatalf("one state announced as %+v, want once to each follower", got)
	}

	// A restart rebuilds the follower; what is stored keeps it quiet.
	f = &follower{store: fs, runs: rs, deliver: func(_ context.Context, n notice) { got = append(got, n) }}
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

	fs.run = newRun(domain.RunCompleted, "")
	f.check(ctx, "run-1", true)
	if len(got) != 6 || got[4].kind != "finished" {
		t.Fatalf("finish announced %+v", got[4:])
	}
	if len(fs.follows) != 0 {
		t.Fatalf("follows after the run finished: %+v", fs.follows)
	}
	f.check(ctx, "run-1", false)
	if len(got) != 6 {
		t.Fatalf("a finished run announced again: %+v", got[6:])
	}
}
