package coord

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

type fakeACPWaker struct {
	mu      sync.Mutex
	idle    map[domain.RunID]bool
	prompts []string
	woken   chan string
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

func (w *fakeACPWaker) WakeEnhanced(_ context.Context, run domain.RunID, prompt string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.idle[run] {
		return errors.New("busy")
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

	// The agent ended its turn without reading the message.
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

func TestEnhancedWakeRefusedByAdmissionStaysArmed(t *testing.T) {
	waker := newFakeACPWaker()
	var mu sync.Mutex
	refuse := true
	h := newHarness(t, 2, func(c *Config) {
		c.ACPWaker = waker
		c.WakeAdmission = func(_ context.Context, _ domain.RunID, dispatch func() error) error {
			mu.Lock()
			defer mu.Unlock()
			if refuse {
				return errors.New("run is protected")
			}
			return dispatch()
		}
	})
	a, b := h.run(0), h.run(1)
	h.peers.pair(a, b, "shared.go")
	waker.endTurn(b)
	if _, err := h.svc.Send(context.Background(), a, sendParams(b, "refused")); err != nil {
		t.Fatal(err)
	}
	h.svc.wakeEnhanced(b)
	if n := waker.count(); n != 0 {
		t.Fatalf("a refused admission still prompted the agent %d times", n)
	}

	mu.Lock()
	refuse = false
	mu.Unlock()
	h.svc.wakeEnhanced(b)
	if n := waker.count(); n != 1 {
		t.Fatalf("after admission allows it, wakes = %d, want 1", n)
	}
}
