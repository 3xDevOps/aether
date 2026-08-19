package adapter

import (
	"bytes"
	"context"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/store"
)

type fakeRuns struct {
	mu   sync.Mutex
	runs map[domain.RunID]*domain.Run
}

func (f *fakeRuns) GetRun(_ context.Context, id domain.RunID) (*domain.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.runs[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	cp := *r
	return &cp, nil
}

func (f *fakeRuns) ListActiveRuns(context.Context) ([]*domain.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*domain.Run
	for _, r := range f.runs {
		if !r.Status.Terminal() {
			cp := *r
			out = append(out, &cp)
		}
	}
	return out, nil
}

// fakeTap serves each run's canned PTY output once and records which runs
// were tapped.
type fakeTap struct {
	mu     sync.Mutex
	output map[domain.RunID][]byte
	tapped map[domain.RunID]int
}

func (f *fakeTap) TapOutput(run domain.RunID) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.tapped == nil {
		f.tapped = make(map[domain.RunID]int)
	}
	f.tapped[run]++
	return io.NopCloser(bytes.NewReader(f.output[run])), nil
}

func (f *fakeTap) taps(run domain.RunID) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tapped[run]
}

func newTestBus(t *testing.T) *events.InProc {
	t.Helper()
	bus, err := events.NewInProc(t.Context(), nil)
	if err != nil {
		t.Fatalf("NewInProc: %v", err)
	}
	t.Cleanup(func() { _ = bus.Close() })
	return bus
}

func startManager(t *testing.T, bus events.Bus, runs RunLookup, tap OutputTap) {
	t.Helper()
	m := NewManager(bus, runs, tap)
	if err := m.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
}

func publishRunning(t *testing.T, bus events.Bus, run domain.RunID) {
	t.Helper()
	_, err := bus.Publish(t.Context(), events.Event{
		SessionID: "sess_1",
		RunID:     run,
		Payload:   events.RunStatusPayload{From: domain.RunProvisioning, To: domain.RunRunning},
	})
	if err != nil {
		t.Fatalf("publish run status: %v", err)
	}
}

// collect drains sub until want events arrived or the deadline passed.
func collect(t *testing.T, sub events.Subscription, want int, deadline time.Duration) []events.Event {
	t.Helper()
	var got []events.Event
	timeout := time.After(deadline)
	for len(got) < want {
		select {
		case e, ok := <-sub.Events():
			if !ok {
				t.Fatalf("subscription closed after %d of %d events", len(got), want)
			}
			got = append(got, e)
		case <-timeout:
			t.Fatalf("timed out with %d of %d events", len(got), want)
		}
	}
	return got
}

// TestManagerEndToEnd feeds the TTY-mangled fixture bytes through
// manager -> normalizer -> adapter -> bus and asserts the published
// envelopes: the full typed event sequence, scoped to the run's session
// and run IDs.
func TestManagerEndToEnd(t *testing.T) {
	data, err := os.ReadFile("testdata/claude_tty.jsonl")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	const (
		session = domain.SessionID("sess_1")
		run     = domain.RunID("run_1")
	)
	bus := newTestBus(t)
	runs := &fakeRuns{runs: map[domain.RunID]*domain.Run{
		run: {ID: run, SessionID: session, Harness: "claude", Mode: domain.LaunchHeadless, Status: domain.RunRunning},
	}}
	tap := &fakeTap{output: map[domain.RunID][]byte{run: data}}

	sub, err := bus.Subscribe(t.Context(), events.SubscribeOptions{
		Filter: events.Filter{Types: []events.Type{events.TypeAgentEvent, events.TypeRunCost}},
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer func() { _ = sub.Close() }()

	startManager(t, bus, runs, tap)
	publishRunning(t, bus, run)

	want := fixturePayloads()
	got := collect(t, sub, len(want), 5*time.Second)
	for i, e := range got {
		if e.SessionID != session || e.RunID != run {
			t.Errorf("event %d scoped to %s/%s, want %s/%s", i, e.SessionID, e.RunID, session, run)
		}
	}
	var payloads []events.Payload
	for _, e := range got {
		payloads = append(payloads, e.Payload)
	}
	assertPayloads(t, payloads, want)
}

// TestManagerGracefulDegradation: runs without an adapter (unknown
// harness) and non-headless runs produce zero adapter events and are
// never tapped.
func TestManagerGracefulDegradation(t *testing.T) {
	const session = domain.SessionID("sess_1")
	bus := newTestBus(t)
	runs := &fakeRuns{runs: map[domain.RunID]*domain.Run{
		"run_codex": {ID: "run_codex", SessionID: session, Harness: "codex", Mode: domain.LaunchHeadless, Status: domain.RunRunning},
		"run_tui":   {ID: "run_tui", SessionID: session, Harness: "claude", Mode: domain.LaunchTUI, Status: domain.RunRunning},
	}}
	tap := &fakeTap{output: map[domain.RunID][]byte{}}

	sub, err := bus.Subscribe(t.Context(), events.SubscribeOptions{
		Filter: events.Filter{Types: []events.Type{events.TypeAgentEvent, events.TypeRunCost}},
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer func() { _ = sub.Close() }()

	startManager(t, bus, runs, tap)
	publishRunning(t, bus, "run_codex")
	publishRunning(t, bus, "run_tui")
	publishRunning(t, bus, "run_gone") // unknown run: logged, not fatal

	select {
	case e := <-sub.Events():
		t.Fatalf("unexpected adapter event %#v", e)
	case <-time.After(200 * time.Millisecond):
	}
	if n := tap.taps("run_codex") + tap.taps("run_tui") + tap.taps("run_gone"); n != 0 {
		t.Errorf("%d taps opened, want 0", n)
	}
}

// TestManagerAttachesOnce: repeated running transitions (stall recovery)
// reuse the original tap while its pump is live.
func TestManagerAttachesOnce(t *testing.T) {
	const (
		session = domain.SessionID("sess_1")
		run     = domain.RunID("run_1")
	)
	bus := newTestBus(t)
	runs := &fakeRuns{runs: map[domain.RunID]*domain.Run{
		run: {ID: run, SessionID: session, Harness: "claude", Mode: domain.LaunchHeadless, Status: domain.RunRunning},
	}}
	// A tap that never ends: the pump stays live across transitions.
	pr, pw := io.Pipe()
	defer func() { _ = pw.Close() }()
	tap := &pipeTap{r: pr}

	startManager(t, bus, runs, tap)
	publishRunning(t, bus, run)
	waitFor(t, "first tap", func() bool { return tap.taps() == 1 })
	publishRunning(t, bus, run)

	time.Sleep(100 * time.Millisecond)
	if n := tap.taps(); n != 1 {
		t.Errorf("taps = %d, want 1", n)
	}
}

// TestManagerStartupScan: a headless run already running at Start (server
// restart reattaches sessions without republishing a running transition)
// is tapped by the startup store scan, with no run-status event at all.
func TestManagerStartupScan(t *testing.T) {
	data, err := os.ReadFile("testdata/claude_tty.jsonl")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	const (
		session = domain.SessionID("sess_1")
		run     = domain.RunID("run_1")
	)
	bus := newTestBus(t)
	runs := &fakeRuns{runs: map[domain.RunID]*domain.Run{
		run:        {ID: run, SessionID: session, Harness: "claude", Mode: domain.LaunchHeadless, Status: domain.RunRunning},
		"run_prov": {ID: "run_prov", SessionID: session, Harness: "claude", Mode: domain.LaunchHeadless, Status: domain.RunProvisioning},
		"run_done": {ID: "run_done", SessionID: session, Harness: "claude", Mode: domain.LaunchHeadless, Status: domain.RunFailed},
	}}
	tap := &fakeTap{output: map[domain.RunID][]byte{run: data}}

	sub, err := bus.Subscribe(t.Context(), events.SubscribeOptions{
		Filter: events.Filter{Types: []events.Type{events.TypeAgentEvent, events.TypeRunCost}},
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer func() { _ = sub.Close() }()

	startManager(t, bus, runs, tap) // no running event published

	want := fixturePayloads()
	got := collect(t, sub, len(want), 5*time.Second)
	var payloads []events.Payload
	for _, e := range got {
		payloads = append(payloads, e.Payload)
	}
	assertPayloads(t, payloads, want)
	if n := tap.taps("run_prov") + tap.taps("run_done"); n != 0 {
		t.Errorf("non-running runs tapped %d times, want 0", n)
	}
}

type pipeTap struct {
	mu sync.Mutex
	n  int
	r  io.ReadCloser
}

func (p *pipeTap) TapOutput(domain.RunID) (io.ReadCloser, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.n++
	return p.r, nil
}

func (p *pipeTap) taps() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.n
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
