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
	"github.com/3xDevOps/Aether/internal/runtime"
)

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
		// Destroy the container after close so the sidecar cleanup assertion
		// remains meaningful.
		cfg.RunContainerTTL = -time.Second
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

	run, err := e.sched.Launch(ctx, e.ws.ID, e.member.ID, e.member.ID, "integration smoke", "fake", domain.LaunchTUI)
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

	// A clean harness exit is not a completed run in TUI mode: the supervisor
	// keeps its container and login shell available for the member.
	sess := e.pty.session(run.ID)
	if sess == nil {
		t.Fatal("no pty session recorded")
	}
	waitFor(t, "harness exit", func() bool {
		return strings.Contains(sess.output(), "[aether] harness exited with code 0")
	})
	if _, err := e.sched.Inject(ctx, run.ID, e.member.ID, domain.AgentPrompt{Text: "printf 'scheduler-login-shell-ready\\n'"}, false, nil); err != nil {
		t.Fatalf("Inject login-shell probe: %v", err)
	}
	waitFor(t, "login shell probe", func() bool {
		return strings.Contains(sess.output(), "scheduler-login-shell-ready")
	})
	if active, err := e.db.GetRun(ctx, run.ID); err != nil {
		t.Fatalf("GetRun after harness exit: %v", err)
	} else if active.Status != domain.RunRunning {
		t.Fatalf("run status after harness exit = %s, want running", active.Status)
	}
	if err := e.sched.CloseRun(ctx, run.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatalf("CloseRun: %v", err)
	}
	waitStatusEvent(t, sub, run.ID, domain.RunMerged)

	sess = e.pty.session(run.ID)
	if sess == nil {
		t.Fatal("no pty session recorded")
	}
	out := sess.output()
	if !strings.Contains(out, "aether agent started") || !strings.Contains(out, "task: integration smoke") {
		t.Fatalf("pty output = %q", out)
	}

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

// Real bind mounts, native shell environment and credential survival exercise
// the same sweep used at startup, hourly and before a disk-admission refusal.
func TestIntegrationAutomaticCachePoolsDocker(t *testing.T) {
	docker, err := runtime.NewDocker(
		runtime.WithLabels(map[string]string{"aether.test": t.Name()}),
		runtime.WithNetworkMode("none"),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = docker.Close() })
	e := newTestEnv(t, func(cfg *Config) {
		cfg.Runtime = docker
		cfg.RunContainerTTL = -time.Second
		cfg.Harnesses = map[string]HarnessSpec{"fake": {TUIArgs: []string{"sh", "-c", "sleep 3600"}}}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	terminal, err := e.sched.EnsureTerminal(ctx, e.member.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.sched.StopTerminal(context.Background(), e.member.ID) })
	exec := func(cid runtime.ID, script string) {
		t.Helper()
		code, stdout, stderr, err := docker.Exec(ctx, cid, []string{"sh", "-c", script}, "/root")
		if err != nil || code != 0 {
			t.Fatalf("cache shell: code=%d stdout=%s stderr=%s err=%v", code, stdout, stderr, err)
		}
	}
	exec(runtime.ID(terminal.ContainerID), `test "$AETHER_CACHE_DIR" = /aether-cache &&
		test "$GOCACHE" = /aether-cache/go-build &&
		printf terminal-cache > /aether-cache/build &&
		mkdir -p "$HOME/.local/bin" "$HOME/.config/gh" "$HOME/.ssh" &&
		printf installed > "$HOME/.local/bin/cache-survival" &&
		printf credential > "$HOME/.config/gh/cache-survival" &&
		printf signing > "$HOME/.ssh/cache-survival"`)
	run, err := e.sched.Launch(ctx, e.ws.ID, e.member.ID, e.member.ID, "cache smoke", "fake", domain.LaunchTUI)
	if err != nil {
		t.Fatal(err)
	}
	sc, err := e.sched.readSidecar(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = docker.Destroy(context.Background(), runtime.ID(sc.ContainerID)) })
	exec(runtime.ID(sc.ContainerID), `test "$npm_config_cache" = /aether-cache/npm &&
		test ! -e /aether-cache/build &&
		printf run-cache > /aether-cache/build &&
		test "$(cat "$HOME/.config/gh/cache-survival")" = credential`)
	runsPath := filepath.Join(e.cfg.Homes.CacheRoot(), string(e.member.ID), "runs", "data")
	terminalPath := filepath.Join(e.cfg.Homes.CacheRoot(), string(e.member.ID), "terminal", "data")
	e.sched.sweepCaches(ctx, true)
	requireCacheExists(t, runsPath, true)
	requireCacheExists(t, terminalPath, true)
	if err := e.sched.CloseRun(ctx, run.ID, e.member.ID, domain.RunAbandoned); err != nil {
		t.Fatal(err)
	}
	e.sched.sweepCaches(ctx, true)
	requireCacheExists(t, runsPath, false)
	requireCacheExists(t, terminalPath, true)
	exec(runtime.ID(terminal.ContainerID), `test "$(cat /aether-cache/build)" = terminal-cache &&
		test "$(cat "$HOME/.local/bin/cache-survival")" = installed &&
		test "$(cat "$HOME/.ssh/cache-survival")" = signing`)
	if err := e.sched.StopTerminal(ctx, e.member.ID); err != nil {
		t.Fatal(err)
	}
	e.sched.sweepCaches(ctx, true)
	requireCacheExists(t, terminalPath, false)
	home, err := e.cfg.Homes.Path(e.member.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{".local/bin/cache-survival", ".config/gh/cache-survival", ".ssh/cache-survival"} {
		if _, err := os.Stat(filepath.Join(home, rel)); err != nil {
			t.Fatalf("durable home file lost: %s: %v", rel, err)
		}
	}
}
