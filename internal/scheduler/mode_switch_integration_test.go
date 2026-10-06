//go:build integration

package scheduler

import (
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/acphost/acpmock"
	"github.com/3xDevOps/Aether/internal/agentstatus"
	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/runtime"
)

func newSwitchDocker(t *testing.T) *runtime.Docker {
	t.Helper()
	docker, err := runtime.NewDocker(
		runtime.WithLabels(map[string]string{"aether.test": t.Name()}),
		runtime.WithNetworkMode("none"),
	)
	if err != nil {
		t.Fatalf("NewDocker: %v", err)
	}
	t.Cleanup(func() { _ = docker.Close() })
	return docker
}

// processes lists the command lines running in container cid.
func processes(t *testing.T, docker *runtime.Docker, cid runtime.ID) string {
	t.Helper()
	_, out, _, err := docker.Exec(t.Context(), cid, []string{"/bin/sh", "-c", `for f in /proc/[0-9]*/cmdline; do tr '\0' ' ' < "$f"; echo; done`}, "")
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func running(ps, cmd string) bool {
	return slices.ContainsFunc(strings.Split(ps, "\n"), func(line string) bool { return strings.TrimSpace(line) == cmd })
}

// TestIntegrationSupervisorSwapsInContainer runs the supervisor as PID 1 of
// a real container, under busybox's sh and, with AETHER_ACP_IMAGE set,
// under the standard image's, and swaps its child through the swap exec.
func TestIntegrationSupervisorSwapsInContainer(t *testing.T) {
	images := []string{"busybox:1.36"}
	if image := os.Getenv("AETHER_ACP_IMAGE"); image != "" {
		images = append(images, image)
	}
	for _, image := range images {
		t.Run(image, func(t *testing.T) {
			docker := newSwitchDocker(t)
			dir := t.TempDir()
			if err := os.Chmod(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
			defer cancel()
			cid, err := docker.Create(ctx, runtime.Spec{
				Name: "switch-" + strings.ToLower(rand.Text()[:8]), Image: image, TTY: true,
				Command:     wrapTUICommand([]string{"sleep", "3601"}),
				Mounts:      []runtime.Mount{{HostPath: dir, ContainerPath: coordtransport.MountDir, ReadOnly: true}},
				CreationKey: rand.Text(),
			})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			t.Cleanup(func() { _ = docker.Destroy(context.Background(), cid) })
			if err = docker.Start(ctx, cid); err != nil {
				t.Fatalf("Start: %v", err)
			}
			s := &Scheduler{cfg: Config{Runtime: docker}}
			swap := func(nonce, body string, settle time.Duration) error {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, coordtransport.NextCommandName), []byte("# "+nonce+"\n"+body), 0o644); err != nil {
					t.Fatal(err)
				}
				return s.swapChild(ctx, cid, nonce, settle)
			}

			if err := swap("n1", "exec sleep 3602\n", time.Second); err != nil {
				t.Fatalf("swap to a resumed command: %v", err)
			}
			if ps := processes(t, docker, cid); !running(ps, "sleep 3602") || running(ps, "sleep 3601") {
				t.Fatalf("processes after the first swap:\n%s", ps)
			}
			if err := swap("n2", "exit 5\n", time.Second); err == nil || !strings.Contains(err.Error(), "exited with code 5") {
				t.Fatalf("swap to a command that exits: %v", err)
			}
			if err := swap("n3", "", 0); err != nil {
				t.Fatalf("swap to a login shell: %v", err)
			}
			if ps := processes(t, docker, cid); running(ps, "sleep 3602") || !running(ps, "/bin/sh -l") && !running(ps, "/bin/bash -l") {
				t.Fatalf("processes after the swap to a shell:\n%s", ps)
			}
		})
	}
}

// TestIntegrationModeSwitchDocker switches a run in a real container both
// ways: the acpmock agent stands in for omp's ACP server and a script for
// its terminal, both installed in the member's home as omp.
func TestIntegrationModeSwitchDocker(t *testing.T) {
	docker := newSwitchDocker(t)
	server := buildStatic(t, "./cmd/aether-server", "aether-server")
	agent := buildStatic(t, "./internal/acphost/acpmock/agent", "acp-mock")
	e := newTestEnv(t, func(cfg *Config) {
		cfg.Runtime = docker
		cfg.WorktreeMount = "/workspace"
		cfg.ServerBinary = server
		cfg.HarnessUpdateDisabled = true
	})
	coord := &fakeCoordinator{root: t.TempDir()}
	e.sched.UseCoordination(coord, t.TempDir(), false)
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
	omp := "#!/bin/sh\nif [ \"$1\" = acp ]; then exec \"$HOME/.local/bin/acp-mock\"; fi\nsleep 3600\n"
	if err = os.WriteFile(filepath.Join(home, ".local/bin/omp"), []byte(omp), 0o755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	run, err := e.sched.Launch(ctx, e.ws.ID, e.member.ID, e.member.ID, "say pong", "omp", domain.LaunchACP)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	sc, err := e.sched.readSidecar(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	cid := runtime.ID(sc.ContainerID)
	t.Cleanup(func() { _ = docker.Destroy(context.Background(), cid) })
	waitItems(t, e.sched, run.ID, "the task's turn", assistantSaid("pong"))
	fix, err := acpmock.Load("claude")
	if err != nil {
		t.Fatal(err)
	}

	if err = e.sched.SwitchMode(ctx, run.ID, e.member.ID, domain.LaunchTUI, admitNow); err != nil {
		t.Fatalf("switch to Standard: %v", err)
	}
	ps := processes(t, docker, cid)
	if !strings.Contains(ps, "omp --auto-approve --resume="+fix.SessionID()) || strings.Contains(ps, "acp-mock") {
		t.Fatalf("processes in Standard mode:\n%s", ps)
	}

	if err = e.sched.ReportAgentState(ctx, run.ID, agentstatus.Report{State: agentstatus.Idle, SessionID: "tui-session"}); err != nil {
		t.Fatal(err)
	}
	if err = e.sched.SwitchMode(ctx, run.ID, e.member.ID, domain.LaunchACP, admitNow); err != nil {
		t.Fatalf("switch to Enhanced: %v", err)
	}
	ps = processes(t, docker, cid)
	if strings.Contains(ps, "--resume=") || !strings.Contains(ps, "acp-mock") {
		t.Fatalf("processes in Enhanced mode:\n%s", ps)
	}
	if _, err = e.sched.Inject(ctx, run.ID, e.member.ID, "say pong", false, nil); err != nil {
		t.Fatal(err)
	}
	waitItems(t, e.sched, run.ID, "a turn after switching back", turnEnded("end_turn", 2))
	if row, _ := e.db.GetRun(ctx, run.ID); row.Mode != domain.LaunchACP || row.HarnessSessionID != "tui-session" {
		t.Fatalf("row mode %q session %q", row.Mode, row.HarnessSessionID)
	}
}
