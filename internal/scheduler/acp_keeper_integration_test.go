//go:build integration

package scheduler

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/acphost/acpmock"
	"github.com/3xDevOps/Aether/internal/acpkeeper"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// TestIntegrationEnhancedRunSurvivesServerRestarts restarts the server twice
// mid-turn in a real container: the agent, the command it left running and
// the streaming turn outlive both, and nothing is sent to the agent again.
func TestIntegrationEnhancedRunSurvivesServerRestarts(t *testing.T) {
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

	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	run, err := e.sched.Launch(ctx, e.ws.ID, e.member.ID, e.member.ID, "say pong", "fake", domain.LaunchACP)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	sc, err := e.sched.readSidecar(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	container := sc.ContainerID
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer ccancel()
		_ = docker.Destroy(cctx, runtime.ID(container))
	})
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	if _, err = e.sched.Inject(ctx, run.ID, e.member.ID, domain.AgentPrompt{Text: acpmock.PromptSpawn}, false, nil); err != nil {
		t.Fatal(err)
	}
	waitItems(t, e.sched, run.ID, "the spawned command", turnEnded("end_turn", 2))
	if _, err = e.sched.Inject(ctx, run.ID, e.member.ID, domain.AgentPrompt{Text: acpmock.PromptThink}, false, nil); err != nil {
		t.Fatal(err)
	}
	waitItems(t, e.sched, run.ID, "streaming", func(items []acphost.Item) bool { return thoughts(items) > 0 })
	ps := func() string {
		t.Helper()
		out, err := exec.CommandContext(ctx, "docker", "exec", container, "ps", "-o", "pid,args").CombinedOutput()
		if err != nil {
			t.Fatalf("docker exec ps: %v\n%s", err, out)
		}
		return string(out)
	}
	pid := func(listing, command string) string {
		t.Helper()
		m := regexp.MustCompile(`(?m)^\s*(\d+)\s+` + regexp.QuoteMeta(command)).FindStringSubmatch(listing)
		if m == nil {
			t.Fatalf("no %q process:\n%s", command, listing)
		}
		return m[1]
	}
	listing := ps()
	adapter, sleeper := pid(listing, "/root/.local/bin/acp-mock"), pid(listing, "sleep 600")
	if !strings.Contains(listing, "acp-keeper serve") {
		t.Fatalf("the adapter runs without a keeper:\n%s", listing)
	}
	// The keeper is root; the adapter is the container's user, as a plain
	// exec is, and that user cannot reach the keeper's socket.
	asAgent := func(args ...string) (string, error) {
		out, err := exec.CommandContext(ctx, "docker", append([]string{"exec", container}, args...)...).CombinedOutput()
		return string(out), err
	}
	ids := func(status string) string {
		var keep []string
		for line := range strings.Lines(status) {
			if strings.HasPrefix(line, "Uid:") || strings.HasPrefix(line, "Gid:") || strings.HasPrefix(line, "Groups:") {
				keep = append(keep, strings.Join(strings.Fields(line), " "))
			}
		}
		return strings.Join(keep, "; ")
	}
	plain, err := asAgent("cat", "/proc/self/status")
	if err != nil {
		t.Fatalf("plain exec: %v\n%s", err, plain)
	}
	kept, err := asAgent("cat", "/proc/"+adapter+"/status")
	if err != nil {
		t.Fatalf("adapter status: %v\n%s", err, kept)
	}
	if ids(kept) != ids(plain) {
		t.Fatalf("the adapter runs as %q, a plain exec as %q", ids(kept), ids(plain))
	}
	if mode, err := asAgent("stat", "-c", "%u %a", acpkeeper.SocketRoot); err != nil || strings.TrimSpace(mode) != "0 700" {
		t.Fatalf("%s is %q, %v; want root's alone", acpkeeper.SocketRoot, mode, err)
	}
	if !strings.Contains(ids(plain), "Uid: 0 ") {
		if out, err := asAgent("ls", acpkeeper.SocketRoot); err == nil {
			t.Fatalf("the agent's user can list %s:\n%s", acpkeeper.SocketRoot, out)
		}
	}

	s := e.sched
	for restart := range 2 {
		before := thoughts(waitItems(t, s, run.ID, "streaming", func(items []acphost.Item) bool { return thoughts(items) > 0 }))
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		listing = ps()
		if pid(listing, "/root/.local/bin/acp-mock") != adapter || pid(listing, "sleep 600") != sleeper {
			t.Fatalf("restart %d: the agent or its command did not outlive the server:\n%s", restart, listing)
		}
		cfg := e.cfg
		cfg.PTY = newFakePTY()
		cfg.PTY.(*fakePTY).logDir = e.pty.logDir
		if s, err = New(cfg); err != nil {
			t.Fatal(err)
		}
		next := s
		startCtx, stop := context.WithCancel(ctx)
		started := make(chan error, 1)
		go func() { started <- next.Start(startCtx) }()
		t.Cleanup(func() {
			stop()
			<-started
			_ = next.Close()
		})
		select {
		case <-s.RecoveryReady():
		case <-ctx.Done():
			t.Fatal("recovery did not finish")
		}
		items := waitItems(t, s, run.ID, "streaming after the restart", func(items []acphost.Item) bool { return thoughts(items) > before+3 })
		if interrupted(items) {
			t.Fatalf("restart %d interrupted the turn: %+v", restart, items)
		}
		listing = ps()
		if pid(listing, "/root/.local/bin/acp-mock") != adapter || pid(listing, "sleep 600") != sleeper {
			t.Fatalf("restart %d: the server replaced the agent or its command:\n%s", restart, listing)
		}
	}
	if err = s.ACPCancel(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	waitItems(t, s, run.ID, "the cancelled turn", turnEnded("cancelled", 1))
	if _, err = s.Inject(ctx, run.ID, e.member.ID, domain.AgentPrompt{Text: "after the restarts"}, false, nil); err != nil {
		t.Fatal(err)
	}
	waitItems(t, s, run.ID, "a turn in the adopted session", turnEnded("end_turn", 3))
	if err = s.CloseRun(ctx, run.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatalf("CloseRun: %v", err)
	}
	// Close leaves the container paused, which docker exec cannot enter.
	top, err := exec.CommandContext(ctx, "docker", "top", container, "-o", "pid,args").CombinedOutput()
	if err != nil {
		t.Fatalf("docker top: %v\n%s", err, top)
	}
	if strings.Contains(string(top), "acp-mock") || strings.Contains(string(top), "acp-keeper") {
		t.Fatalf("Close left the agent or its keeper running:\n%s", top)
	}
}
