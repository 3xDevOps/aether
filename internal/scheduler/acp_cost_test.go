package scheduler

import (
	"bytes"
	"context"
	"errors"
	"math"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
)

type failFirstCost struct {
	events.Bus
	failed atomic.Bool
}

func (b *failFirstCost) Publish(ctx context.Context, e events.Event) (events.Event, error) {
	if _, ok := e.Payload.(events.RunCostPayload); ok && b.failed.CompareAndSwap(false, true) {
		return events.Event{}, errors.New("publish refused")
	}
	return b.Bus.Publish(ctx, e)
}

// An Enhanced run is charged what the agent's running cost adds to the total
// in the run's sidecar, which moves only once a charge is published: a failed
// publish is charged with the next total, a stale total adds nothing, and a
// restored session is charged only what its next total adds.
func TestEnhancedRunChargesWhatTheRunningCostAdds(t *testing.T) {
	t.Parallel()
	e, rt := newACPEnv(t, func(cfg *Config) { cfg.Bus = &failFirstCost{Bus: cfg.Bus} })
	sub := e.subscribe(t)
	charged := func(when string, in int64, usd float64) {
		t.Helper()
		timeout := time.After(waitTimeout)
		for {
			select {
			case ev := <-sub.Events():
				p, ok := ev.Payload.(events.RunCostPayload)
				if !ok {
					continue
				}
				if !p.Metered || !p.Increment || p.InputTokens != in || math.Abs(p.CostUSD-usd) > 1e-9 {
					t.Fatalf("%s: run.cost %+v, want %d input tokens and %v", when, p, in, usd)
				}
				return
			case <-timeout:
				t.Fatalf("%s: no run.cost event", when)
			}
		}
	}
	saved := func(s *Scheduler, run domain.RunID, want float64) {
		t.Helper()
		waitFor(t, "the charged total in the sidecar", func() bool {
			sc, err := s.readSidecar(run)
			return err == nil && math.Abs(sc.AgentCost-want) < 1e-9
		})
	}
	const turnTokens = 2 + 10202 + 16905

	run := e.launchACP(t, "say pong")
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	if _, err := e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: "again"}, false, nil); err != nil {
		t.Fatal(err)
	}
	charged("the turn after a failed publish", 2*turnTokens, 0.3418385)
	saved(e.sched, run.ID, 0.3418385)
	e.sched.mu.Lock()
	entry := e.sched.runs[run.ID]
	e.sched.mu.Unlock()
	e.sched.acp.charge(entry, acphost.Spend{InputTokens: 1, Total: 0.2})
	saved(e.sched, run.ID, 0.3418385)
	if err := e.sched.Close(); err != nil {
		t.Fatal(err)
	}
	rt.dropAdapters(t)

	updates := slices.Clone(rt.fixture.Prompt.Updates)
	for i, u := range updates {
		updates[i] = bytes.Replace(u, []byte(`"amount": 0.34183850000000005`), []byte(`"amount": 0.5`), 1)
	}
	rt.fixture.Prompt.Updates = updates
	cfg := e.cfg
	cfg.PTY = newFakePTY()
	cfg.PTY.(*fakePTY).logDir = e.pty.logDir
	s2, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	startScheduler(t, s2)
	waitFor(t, "resumed session", func() bool { return s2.acp.session(run.ID) != nil })
	if _, err := s2.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: "again"}, false, nil); err != nil {
		t.Fatal(err)
	}
	charged("the turn after the restart", turnTokens, 0.5-0.3418385)
	saved(s2, run.ID, 0.5)
}
