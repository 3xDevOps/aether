package coord

import (
	"context"
	"errors"
	"maps"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
)

type fakeACPWaker struct {
	mu      sync.Mutex
	idle    map[domain.RunID]bool
	prompts []string
	woken   chan string
	budget  time.Duration
}

func newFakeACPWaker() *fakeACPWaker {
	return &fakeACPWaker{idle: make(map[domain.RunID]bool), woken: make(chan string, 16)}
}

func (w *fakeACPWaker) endTurn(run domain.RunID) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.idle[run] = true
}

func (w *fakeACPWaker) IdleEnhanced(run domain.RunID) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.idle[run]
}

func (w *fakeACPWaker) WakeEnhanced(ctx context.Context, run domain.RunID, prompt string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.idle[run] {
		return errors.New("busy")
	}
	if deadline, ok := ctx.Deadline(); ok {
		w.budget = time.Until(deadline)
	}
	w.idle[run] = false
	w.prompts = append(w.prompts, prompt)
	w.woken <- prompt
	return nil
}

func (w *fakeACPWaker) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.prompts)
}

func (w *fakeACPWaker) next(t *testing.T) string {
	t.Helper()
	select {
	case prompt := <-w.woken:
		return prompt
	case <-time.After(5 * time.Second):
		t.Fatal("no enhanced wake")
		return ""
	}
}

func TestEnhancedWakeOncePerUnreadSet(t *testing.T) {
	waker := newFakeACPWaker()
	h := newHarness(t, 2, func(c *Config) {
		c.WakeAdmission = allowHookWake
		c.ACPWaker = waker
	})
	a, b := h.run(0), h.run(1)
	h.peers.pair(a, b, "shared.go")
	waker.endTurn(b)
	ctx := context.Background()

	if _, err := h.svc.Send(ctx, a, sendParams(b, "first")); err != nil {
		t.Fatal(err)
	}
	if got, want := waker.next(t), protocol.CoordInboxContext(1); got != want {
		t.Fatalf("wake prompt %q, want %q", got, want)
	}

	waker.endTurn(b)
	h.svc.wakeEnhanced(b)
	if n := waker.count(); n != 1 {
		t.Fatalf("an unchanged unread set woke the run again: %d wakes", n)
	}

	if _, err := h.svc.Send(ctx, a, sendParams(b, "second")); err != nil {
		t.Fatal(err)
	}
	if got, want := waker.next(t), protocol.CoordInboxContext(2); got != want {
		t.Fatalf("a new message must re-arm the wake: prompt %q, want %q", got, want)
	}
}

func TestEnhancedWakeSkipsBusySessionUntilIdle(t *testing.T) {
	waker := newFakeACPWaker()
	h := newHarness(t, 2, func(c *Config) {
		c.WakeAdmission = allowHookWake
		c.ACPWaker = waker
	})
	a, b := h.run(0), h.run(1)
	h.peers.pair(a, b, "shared.go")
	if _, err := h.svc.Send(context.Background(), a, sendParams(b, "while busy")); err != nil {
		t.Fatal(err)
	}
	h.svc.wakeEnhanced(b)
	if n := waker.count(); n != 0 {
		t.Fatalf("a busy session was prompted %d times", n)
	}

	waker.endTurn(b)
	h.svc.WakeIdle(b)
	if got, want := waker.next(t), protocol.CoordInboxContext(1); got != want {
		t.Fatalf("wake at idle: prompt %q, want %q", got, want)
	}
}

func TestEnhancedWakeRetriesARefusedAdmission(t *testing.T) {
	waker := newFakeACPWaker()
	var mu sync.Mutex
	refusals := 0
	h := newHarness(t, 2, func(c *Config) {
		c.ACPWaker = waker
		c.WakeAdmission = func(_ context.Context, _ domain.RunID, dispatch func() error) error {
			mu.Lock()
			defer mu.Unlock()
			if refusals == 0 {
				refusals++
				return errors.New("run is protected")
			}
			return dispatch()
		}
	})
	a, b := h.run(0), h.run(1)
	h.peers.pair(a, b, "shared.go")
	waker.endTurn(b)
	if _, err := h.svc.Send(context.Background(), a, sendParams(b, "refused once")); err != nil {
		t.Fatal(err)
	}
	if got, want := waker.next(t), protocol.CoordInboxContext(1); got != want {
		t.Fatalf("retried wake prompt %q, want %q", got, want)
	}
	mu.Lock()
	defer mu.Unlock()
	if refusals != 1 {
		t.Fatalf("admission refused %d times, want 1", refusals)
	}
}

func TestEnhancedWakePromptKeepsItsBudgetAfterASlowAdmission(t *testing.T) {
	waker := newFakeACPWaker()
	h := newHarness(t, 2, func(c *Config) {
		c.ACPWaker = waker
		c.WakeAdmission = func(_ context.Context, _ domain.RunID, dispatch func() error) error {
			time.Sleep(time.Second) // A mission lock held by integration prepare.
			return dispatch()
		}
	})
	a, b := h.run(0), h.run(1)
	h.peers.pair(a, b, "shared.go")
	waker.endTurn(b)
	if _, err := h.svc.Send(t.Context(), a, sendParams(b, "after a slow admission")); err != nil {
		t.Fatal(err)
	}
	waker.next(t)
	waker.mu.Lock()
	defer waker.mu.Unlock()
	if waker.budget < enhancedWakeTimeout-500*time.Millisecond {
		t.Fatalf("the prompt had %s left to confirm its turn, want close to %s", waker.budget, enhancedWakeTimeout)
	}
}

func TestEnhancedWakeSweepLandsAWakeItsRetriesGaveUpOn(t *testing.T) {
	retries := enhancedWakeRetries
	enhancedWakeRetries = []time.Duration{time.Millisecond}
	t.Cleanup(func() { enhancedWakeRetries = retries })
	// The event's own attempts, plus two sweeps, are refused.
	refuse := len(enhancedWakeRetries) + 3
	waker := newFakeACPWaker()
	var mu sync.Mutex
	attempts := 0
	h := newHarness(t, 2, func(c *Config) {
		c.ACPWaker = waker
		c.wakeSweep = 100 * time.Millisecond
		c.WakeAdmission = func(_ context.Context, _ domain.RunID, dispatch func() error) error {
			mu.Lock()
			defer mu.Unlock()
			attempts++
			if attempts <= refuse {
				return errors.New("run control is busy")
			}
			return dispatch()
		}
	})
	a, b := h.run(0), h.run(1)
	h.peers.pair(a, b, "shared.go")
	if _, err := h.svc.Provision(t.Context(), b, nil); err != nil {
		t.Fatal(err)
	}
	h.start()
	waker.endTurn(b)
	if _, err := h.svc.Send(t.Context(), a, sendParams(b, "refused past its retries")); err != nil {
		t.Fatal(err)
	}
	if got, want := waker.next(t), protocol.CoordInboxContext(1); got != want {
		t.Fatalf("swept wake prompt %q, want %q", got, want)
	}
	mu.Lock()
	defer mu.Unlock()
	if attempts != refuse+1 {
		t.Fatalf("admission ran %d times, want %d", attempts, refuse+1)
	}
}

func TestEnhancedWakeSweepPassesARunWhoseAdmissionWaits(t *testing.T) {
	waker := newFakeACPWaker()
	var mu sync.Mutex
	attempts := 0
	var blocked, open domain.RunID
	h := newHarness(t, 3, func(c *Config) {
		c.ACPWaker = waker
		c.wakeSweep = 50 * time.Millisecond
		c.WakeAdmission = func(ctx context.Context, run domain.RunID, dispatch func() error) error {
			if run == blocked {
				<-ctx.Done()
				return ctx.Err()
			}
			mu.Lock()
			attempts++
			first := attempts == 1
			mu.Unlock()
			if first {
				return errors.New("run control is busy")
			}
			return dispatch()
		}
	})
	a := h.run(0)
	blocked, open = h.run(1), h.run(2)
	h.peers.hub(a, []domain.RunID{blocked, open}, "shared.go")
	for _, run := range []domain.RunID{blocked, open} {
		if _, err := h.svc.Provision(t.Context(), run, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := h.svc.Send(t.Context(), a, sendParams(run, "mail during a turn")); err != nil {
			t.Fatal(err)
		}
	}
	h.start()
	waker.endTurn(blocked)
	waker.endTurn(open)
	// Only sweeps offer these wakes; the second offer to open must not wait for blocked.
	if got, want := waker.next(t), protocol.CoordInboxContext(1); got != want {
		t.Fatalf("swept wake prompt %q, want %q", got, want)
	}
	if waker.IdleEnhanced(open) {
		t.Fatal("the sweep woke the blocked run instead")
	}
}

func TestEnhancedWakeRetriesWhenProtectionIsLifted(t *testing.T) {
	retries := enhancedWakeRetries
	enhancedWakeRetries = []time.Duration{time.Millisecond}
	t.Cleanup(func() { enhancedWakeRetries = retries })
	waker := newFakeACPWaker()
	var protected atomic.Bool
	protected.Store(true)
	refused := make(chan struct{}, 16)
	h := newHarness(t, 2, func(c *Config) {
		c.ACPWaker = waker
		c.wakeSweep = time.Hour
		c.WakeAdmission = func(_ context.Context, _ domain.RunID, dispatch func() error) error {
			if protected.Load() {
				refused <- struct{}{}
				return errors.New("run is protected")
			}
			return dispatch()
		}
	})
	a, b := h.run(0), h.run(1)
	h.peers.pair(a, b, "shared.go")
	h.start()
	waker.endTurn(b)
	if _, err := h.svc.Send(t.Context(), a, sendParams(b, "held by protection")); err != nil {
		t.Fatal(err)
	}
	for range len(enhancedWakeRetries) + 1 {
		select {
		case <-refused:
		case <-time.After(5 * time.Second):
			t.Fatal("the protected wake was not attempted")
		}
	}
	protected.Store(false)
	if _, err := h.bus.Publish(t.Context(), events.Event{
		WorkspaceID: h.workspace, RunID: b, Payload: events.RunProtectedPayload{Protected: false},
	}); err != nil {
		t.Fatal(err)
	}
	if got, want := waker.next(t), protocol.CoordInboxContext(1); got != want {
		t.Fatalf("wake after unprotect: prompt %q, want %q", got, want)
	}
}

type changeMissionStub struct {
	missionTransportStub
	mu      sync.Mutex
	seq     uint64
	changes map[domain.MissionChange]uint64
}

func (m *changeMissionStub) Assignment(ctx context.Context, run domain.RunID) (protocol.CoordMissionAssignment, error) {
	a, err := m.missionTransportStub.Assignment(ctx, run)
	m.mu.Lock()
	defer m.mu.Unlock()
	a.ChangeSeq, a.Changes = m.seq, maps.Clone(m.changes)
	return a, err
}

// change counts one swarm change; one the integrator made itself is
// counted but not listed, as the store does.
func (m *changeMissionStub) change(kind domain.MissionChange, byIntegrator bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	if byIntegrator {
		return
	}
	if m.changes == nil {
		m.changes = make(map[domain.MissionChange]uint64)
	}
	m.changes[kind] = m.seq
}

func newMissionWakeHarness(t *testing.T) (*coordHarness, *fakeACPWaker, *changeMissionStub, domain.RunID) {
	t.Helper()
	waker := newFakeACPWaker()
	stub := &changeMissionStub{}
	h := newHarness(t, 2, func(c *Config) {
		c.WakeAdmission = allowHookWake
		c.ACPWaker = waker
		c.Mission = stub
	})
	worker, integrator := h.run(0), h.run(1)
	stub.mission, stub.integrator = []domain.RunID{worker, integrator}, integrator
	h.start()
	return h, waker, stub, integrator
}

func (h *coordHarness) publishMissionChanged(t *testing.T) {
	t.Helper()
	if _, err := h.bus.Publish(context.Background(), events.Event{
		WorkspaceID: h.workspace, Payload: events.MissionChangedPayload{MissionID: "mission-1"},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestEnhancedWakeAnnouncesMissionChangesToIntegrator(t *testing.T) {
	h, waker, stub, integrator := newMissionWakeHarness(t)
	stub.change(domain.MissionTaskProposed, false)

	waker.endTurn(integrator)
	h.svc.wakeEnhanced(integrator)
	if n := waker.count(); n != 0 {
		t.Fatalf("the first mission state seen woke the integrator %d times", n)
	}

	stub.change(domain.MissionWorkerEnded, false)
	h.publishMissionChanged(t)
	want := protocol.CoordMissionUpdateContext("mission-1", []domain.MissionChange{domain.MissionWorkerEnded})
	if got := waker.next(t); got != want {
		t.Fatalf("mission wake prompt %q, want %q", got, want)
	}

	waker.endTurn(integrator)
	h.publishMissionChanged(t)
	h.svc.wakeEnhanced(integrator)
	if n := waker.count(); n != 1 {
		t.Fatalf("an unchanged mission woke the integrator again: %d wakes", n)
	}
}

func TestEnhancedWakeAnnouncesAnAskAndAnswerWithinOneTurn(t *testing.T) {
	h, waker, stub, integrator := newMissionWakeHarness(t)
	h.svc.EnhancedSessionOpened(context.Background(), integrator)

	stub.change(domain.MissionQuestionAsked, true)
	h.publishMissionChanged(t)
	stub.change(domain.MissionQuestionAnswered, false)
	h.publishMissionChanged(t)
	waker.endTurn(integrator)
	h.svc.WakeIdle(integrator)
	want := protocol.CoordMissionUpdateContext("mission-1", []domain.MissionChange{domain.MissionQuestionAnswered})
	if got := waker.next(t); got != want {
		t.Fatalf("mission wake prompt %q, want %q", got, want)
	}
	if !strings.HasPrefix(want, "Mission update (question answered): ") {
		t.Fatalf("prompt %q does not name the change", want)
	}
}

func TestEnhancedWakeSkipsTheIntegratorsOwnChanges(t *testing.T) {
	h, waker, stub, integrator := newMissionWakeHarness(t)
	h.svc.EnhancedSessionOpened(context.Background(), integrator)

	stub.change(domain.MissionTaskProposed, true)
	stub.change(domain.MissionPhaseChanged, true)
	waker.endTurn(integrator)
	h.svc.wakeEnhanced(integrator)
	if n := waker.count(); n != 0 {
		t.Fatalf("the integrator's own changes woke it %d times", n)
	}

	stub.change(domain.MissionWorkerEnded, false)
	stub.change(domain.MissionTaskProposed, false)
	h.svc.wakeEnhanced(integrator)
	want := protocol.CoordMissionUpdateContext("mission-1", []domain.MissionChange{domain.MissionWorkerEnded, domain.MissionTaskProposed})
	if got := waker.next(t); got != want {
		t.Fatalf("mission wake prompt %q, want %q", got, want)
	}
}
