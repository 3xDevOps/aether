//go:build integration

package scheduler

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/acphost/acpmock"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// buildStatic builds a Go command for the container, which has no libc to
// link against.
func buildStatic(t *testing.T, pkg, name string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), name)
	build := exec.CommandContext(t.Context(), "go", "build", "-o", out, pkg)
	build.Dir = "../.."
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", pkg, err, output)
	}
	return out
}

// TestIntegrationBackgroundRunDocker runs a background task through the
// acpmock agent installed in the member's home: after its one turn the
// supervisor's container exits 0 on SIGUSR1 and the run completes.
func TestIntegrationBackgroundRunDocker(t *testing.T) {
	docker, err := runtime.NewDocker(
		runtime.WithLabels(map[string]string{"aether.test": t.Name()}),
		runtime.WithNetworkMode("none"),
	)
	if err != nil {
		t.Fatalf("NewDocker: %v", err)
	}
	t.Cleanup(func() { _ = docker.Close() })
	server := buildStatic(t, "./cmd/aether-server", "aether-server")
	agent := buildStatic(t, "./internal/acphost/acpmock/agent", "acp-mock")

	e := newTestEnv(t, func(cfg *Config) {
		cfg.Runtime = docker
		cfg.WorktreeMount = "/workspace"
		cfg.ServerBinary = server
		cfg.Harnesses = map[string]HarnessSpec{
			"fake": {HeadlessArgs: []string{"sh", "-c", "exit 3"}, ACPArgs: []string{"acp-mock"}},
		}
	})
	e.sched.UseCoordination(&fakeCoordinator{root: t.TempDir()}, t.TempDir(), false)
	home, err := e.cfg.Homes.Path(e.member.ID)
	if err != nil {
		t.Fatal(err)
	}
	bin, err := os.ReadFile(agent)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(home, ".local/bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(home, ".local/bin/acp-mock"), bin, 0o755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	run, err := e.sched.Launch(ctx, e.ws.ID, e.member.ID, e.member.ID, "say pong", "fake", domain.LaunchHeadless)
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
	if !run.ACP {
		t.Fatal("the background run is not driven over ACP")
	}
	got := e.waitStoreStatus(t, run.ID, domain.RunCompleted)
	if got.Reason != exitedCompletedReason {
		t.Fatalf("reason %q, want %q", got.Reason, exitedCompletedReason)
	}
	items, err := e.sched.ACPHistory(run.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !assistantSaid("pong")(items) {
		t.Fatalf("the item log does not hold the turn: %+v", items)
	}
}

// TestIntegrationEnhancedRunDocker drives an enhanced run in a real
// container: the staged dev-exec helper runs the acpmock agent as a managed
// pipe exec beside the supervisor's login shell, the task is the first
// prompt, a permission request is answered, and closing and reopening the run
// resumes the same agent session in a fresh exec.
func TestIntegrationEnhancedRunDocker(t *testing.T) {
	docker, err := runtime.NewDocker(
		runtime.WithLabels(map[string]string{"aether.test": t.Name()}),
		runtime.WithNetworkMode("none"),
	)
	if err != nil {
		t.Fatalf("NewDocker: %v", err)
	}
	t.Cleanup(func() { _ = docker.Close() })
	server := buildStatic(t, "./cmd/aether-server", "aether-server")
	agent := buildStatic(t, "./internal/acphost/acpmock/agent", "acp-mock")

	e := newTestEnv(t, func(cfg *Config) {
		cfg.Runtime = docker
		cfg.WorktreeMount = "/workspace"
		cfg.ServerBinary = server
		cfg.Harnesses = map[string]HarnessSpec{
			"fake": {ACPArgs: []string{"sh", "-c", `exec "$HOME/.local/bin/acp-mock"`}},
		}
	})
	e.sched.UseCoordination(&fakeCoordinator{root: t.TempDir()}, t.TempDir(), false)
	home, err := e.cfg.Homes.Path(e.member.ID)
	if err != nil {
		t.Fatal(err)
	}
	bin, err := os.ReadFile(agent)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(home, ".local/bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(home, ".local/bin/acp-mock"), bin, 0o755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	run, err := e.sched.Launch(ctx, e.ws.ID, e.member.ID, e.member.ID, "say pong", "fake", domain.LaunchACP)
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
	waitItems(t, e.sched, run.ID, "the task's turn", assistantSaid("pong"))
	e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
	fix, err := acpmock.Load("claude")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := e.db.GetRun(ctx, run.ID); got.HarnessSessionID != fix.SessionID() {
		t.Fatalf("harness_session_id %q, want %q", got.HarnessSessionID, fix.SessionID())
	}

	if _, err = e.sched.Inject(ctx, run.ID, e.member.ID, acpmock.PromptAskPermission, false); err != nil {
		t.Fatal(err)
	}
	var pending []domain.RunInputRequest
	waitFor(t, "permission request", func() bool {
		pending = e.sched.PendingInputs(run.ID)
		return len(pending) == 1
	})
	if err = e.sched.ACPAnswer(run.ID, pending[0].ID, "allow", nil); err != nil {
		t.Fatal(err)
	}
	waitItems(t, e.sched, run.ID, "the answered turn", assistantSaid("permission: allow"))

	if err = e.sched.CloseRun(ctx, run.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatalf("CloseRun: %v", err)
	}
	if _, err = e.sched.Relaunch(ctx, run.ID, e.member.ID); err != nil {
		t.Fatalf("Relaunch: %v", err)
	}
	if _, err = e.sched.Inject(ctx, run.ID, e.member.ID, "after reopen", false); err != nil {
		t.Fatal(err)
	}
	waitItems(t, e.sched, run.ID, "a turn in the resumed session", turnEnded("end_turn", 3))
	if err = e.sched.DeleteRun(ctx, run.ID, e.member.ID); err != nil {
		t.Fatalf("DeleteRun: %v", err)
	}
}
