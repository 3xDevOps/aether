package sshd

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
)

type failingPublishBus struct {
	events.Bus
	mu     sync.Mutex
	calls  int
	failAt int
	err    error
}

func (b *failingPublishBus) Publish(ctx context.Context, event events.Event) (events.Event, error) {
	b.mu.Lock()
	b.calls++
	call := b.calls
	b.mu.Unlock()
	if call == b.failAt {
		return events.Event{}, b.err
	}
	return b.Bus.Publish(ctx, event)
}

func (b *failingPublishBus) Calls() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls
}

func TestRunProtectReportsPublicationFailures(t *testing.T) {
	for _, failAt := range []int{1, 2} {
		t.Run("event-"+string(rune('0'+failAt)), func(t *testing.T) {
			env := newTestEnv(t, nil)
			publishErr := errors.New("event log unavailable")
			bus := &failingPublishBus{Bus: env.bus, failAt: failAt, err: publishErr}
			env.srv.cfg.Bus = bus
			params, err := json.Marshal(protocol.RunProtectParams{RunID: string(env.run.ID), Protected: true})
			if err != nil {
				t.Fatal(err)
			}

			if _, perr := env.srv.runProtect(context.Background(), env.member.ID, params); perr == nil ||
				perr.Code != protocol.CodeInternal || !strings.Contains(perr.Message, "publish run protection") ||
				!strings.Contains(perr.Message, publishErr.Error()) {
				t.Fatalf("runProtect publication error = %+v, want contextual internal error", perr)
			}
			if got := bus.Calls(); got != 2 {
				t.Fatalf("protection publication calls = %d, want both events attempted", got)
			}
			protected, err := env.store.GetRun(context.Background(), env.run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !protected.Protected {
				t.Fatal("protection mutation was lost when publication failed")
			}
		})
	}
}
