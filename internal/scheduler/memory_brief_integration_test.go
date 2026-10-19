//go:build integration

package scheduler

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// TestIntegrationBriefReachesTheAgentDocker launches a fake agent in a real
// container that writes the task prompt it was given into its checkout.
func TestIntegrationBriefReachesTheAgentDocker(t *testing.T) {
	docker, err := runtime.NewDocker(
		runtime.WithLabels(map[string]string{"aether.test": t.Name()}),
		runtime.WithNetworkMode("none"),
	)
	if err != nil {
		t.Fatalf("NewDocker: %v", err)
	}
	t.Cleanup(func() { _ = docker.Close() })
	e := newTestEnv(t, func(cfg *Config) {
		cfg.Runtime = docker
		cfg.RunContainerTTL = -time.Second
		cfg.ServerBinary = fakeServerBinary(t, "#!/bin/sh\n")
		// The prompt is an argument, never shell text: the brief holds
		// backticks and quotes.
		cfg.Harnesses = map[string]HarnessSpec{
			"fake": {TUIArgs: []string{"sh", "-c", `printf '%s' "$1" > .prompt && mv .prompt prompt.txt; sleep 3600`, "sh", "{task}"}},
		}
		withTeamMemory(t)(cfg)
	})
	e.sched.UseCoordination(&fakeCoordinator{root: t.TempDir()}, t.TempDir(), false)
	seed := seedTeamMemory(t, e)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()

	run, err := e.sched.Launch(ctx, e.ws.ID, e.member.ID, e.member.ID, "fix the login", "fake", domain.LaunchTUI)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if sc, scErr := e.sched.readSidecar(run.ID); scErr == nil {
		t.Cleanup(func() {
			cctx, ccancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer ccancel()
			_ = docker.Destroy(cctx, runtime.ID(sc.ContainerID))
		})
	}
	var prompt []byte
	waitFor(t, "the agent's prompt in the checkout", func() bool {
		prompt, err = os.ReadFile(filepath.Join(run.Worktree, "prompt.txt"))
		return err == nil && len(prompt) > 0
	})
	seed.check(t, "the agent's prompt", string(prompt))
	if err = e.sched.CloseRun(ctx, run.ID, e.member.ID, domain.RunAbandoned); err != nil {
		t.Fatalf("CloseRun: %v", err)
	}
}
