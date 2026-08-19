//go:build integration

package scheduler

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// TestIntegrationHappyPathDocker drives the full happy path against the
// real Docker runtime: a scripted busybox "agent" runs on a TTY with the
// checkout bind-mounted at /workspace, writes a file, exits cleanly, and
// the run parks at needs-attention with results committed.
func TestIntegrationHappyPathDocker(t *testing.T) {
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
		cfg.Harnesses = map[string]HarnessSpec{
			// The leading sleep keeps the first output behind the attach:
			// Docker attachments stream from the attach point onward.
			"fake": {TUIArgs: []string{"sh", "-c",
				"sleep 1; echo aether agent started; echo task: {task}; touch done.txt; sleep 1"}},
		}
	})
	sub := e.subscribe(t)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()

	run, err := e.sched.Launch(ctx, e.sess.ID, e.member.ID, "integration smoke", "fake", domain.LaunchTUI)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	// Belt and braces: destroy the container even if finalize never runs.
	if sc, scErr := e.sched.readSidecar(run.ID); scErr == nil {
		t.Cleanup(func() {
			cctx, ccancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer ccancel()
			_ = docker.Destroy(cctx, runtime.ID(sc.ContainerID))
		})
	}
	if run.Status != domain.RunRunning {
		t.Fatalf("run status after launch = %s, want running", run.Status)
	}

	ev := waitStatusEvent(t, sub, run.ID, domain.RunNeedsAttention)
	if p := ev.Payload.(events.RunStatusPayload); p.Reason != "agent exited; results committed" {
		t.Fatalf("needs-attention reason = %q", p.Reason)
	}

	// The agent's TTY output reached the PTY seam.
	sess := e.pty.session(run.ID)
	if sess == nil {
		t.Fatal("no pty session recorded")
	}
	out := sess.output()
	if !strings.Contains(out, "aether agent started") || !strings.Contains(out, "task: integration smoke") {
		t.Fatalf("pty output = %q", out)
	}

	// The agent's file write landed in the host-side checkout.
	if _, err := os.Stat(filepath.Join(run.Worktree, "done.txt")); err != nil {
		t.Fatalf("agent-written file missing from checkout: %v", err)
	}

	if got := e.git.commitsFor(run.ID); len(got) != 1 || got[0] != "aether: integration smoke" {
		t.Fatalf("commits = %v", got)
	}
	if e.git.publishedCount(run.ID) == 0 {
		t.Fatal("run branch never published")
	}
	waitFor(t, "sidecar removed", func() bool {
		_, err := os.Stat(e.sched.sidecarPath(run.ID))
		return os.IsNotExist(err)
	})
}
