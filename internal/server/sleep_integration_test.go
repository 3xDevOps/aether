//go:build integration

package server

import (
	"context"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// An idle Enhanced Claude Code session's container is stopped, not removed,
// and the next prompt starts it again with the agent in the same
// conversation: the model's next request carries the turn from before the
// sleep.
func TestIntegrationClaudeEnhancedSleepsAndWakes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	r := startRealAgents(ctx, t, func(cfg *Config) {
		cfg.PollInterval = time.Second
		cfg.RunSleepAfter = 5 * time.Second
	})
	a := &realAgentRun{srv: r.srv, ctrl: r.ctrl, model: r.model, workspace: string(r.e.ws.ID), agent: "claude", mode: domain.LaunchACP, marker: "sleep-claude-enhanced"}
	before := a.marker + "/before"
	r.model.script(before, step{command: reportCommand("answered", before)})
	r.launch(t, a, tagged(before)+" do the scripted step.")
	a.waitIdle(ctx, t, 0)

	a.waitFor(ctx, t, "the idle session asleep", func() bool { return a.get(t).AsleepSince != nil })
	cid, err := r.docker.FindByCreationKey(ctx, a.run.ID)
	if err != nil {
		t.Fatalf("find the sleeping session's container: %v", err)
	}
	info, err := r.docker.Inspect(ctx, cid)
	if err != nil {
		t.Fatalf("inspect the sleeping session's container: %v", err)
	}
	if !info.Exited {
		t.Fatal("the sleeping session's container is still running")
	}
	if got := a.get(t); domain.RunStatus(got.Status).Terminal() {
		t.Fatalf("the sleep finished the session: %s (%q)", got.Status, got.Reason)
	}

	after := a.marker + "/after"
	r.model.script(after, step{command: reportCommand("answered", after)})
	if err := r.ctrl.Call(protocol.MethodRunInject, protocol.RunInjectParams{
		RunID: a.run.ID, Message: tagged(after) + " do the scripted step.", IdempotencyKey: a.marker + "-wake",
	}, nil); err != nil {
		t.Fatalf("run.inject to the sleeping session: %v", err)
	}
	a.waitFor(ctx, t, "a request of the woken agent that carries the turn from before the sleep", func() bool {
		return r.model.saw(tagged(after), tagged(before))
	})
	if info, err = r.docker.Inspect(ctx, cid); err != nil || info.Exited {
		t.Fatalf("the woken session's container: exited %v, %v", info.Exited, err)
	}
}
