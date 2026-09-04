package scheduler

import (
	"context"
	"errors"
	"testing"

	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/store"
)

func TestBuildTerminalPlanWithoutWorkspace(t *testing.T) {
	e := newTestEnv(t, func(cfg *Config) { cfg.StandardImage = "standard:latest" })
	plan, err := e.sched.BuildEnvironmentPlan(context.Background(), nil, nil, e.member, profileForTerminalTest(), EnvironmentPurposeTerminal)
	if err != nil {
		t.Fatalf("BuildEnvironmentPlan: %v", err)
	}
	if plan.Image != "standard:latest" || plan.SetupScript != "" {
		t.Fatalf("plan = %+v", plan)
	}
	if _, ok := plan.Env["WS"]; ok {
		t.Fatalf("terminal plan leaked workspace variables: %+v", plan.Env)
	}
	if plan.Env["HOME"] != "/root" || plan.Env["TERM"] != "xterm-256color" {
		t.Fatalf("terminal environment = %+v", plan.Env)
	}
	if _, err := e.sched.BuildEnvironmentPlan(context.Background(), nil, nil, e.member, profileForTerminalTest(), EnvironmentPurposeRun); err == nil {
		t.Fatal("run plan without workspace unexpectedly succeeded")
	}
}

func TestEnsureTerminalCreatesPersistentContainer(t *testing.T) {
	e := newTestEnv(t, func(cfg *Config) { cfg.StandardImage = "standard:latest" })
	terminal, err := e.sched.EnsureTerminal(context.Background(), e.member.ID)
	if err != nil {
		t.Fatalf("EnsureTerminal: %v", err)
	}
	if terminal.ContainerID == "" || terminal.Image != "standard:latest" {
		t.Fatalf("terminal = %+v", terminal)
	}
	container, err := e.rt.get(runtime.ID(terminal.ContainerID))
	if err != nil {
		t.Fatal(err)
	}
	if container.spec.Name != terminalContainerName(e.member.ID) || !container.spec.TTY || container.spec.WorkingDir != "/root" {
		t.Fatalf("spec = %+v", container.spec)
	}
	if len(container.spec.Command) != 2 || container.spec.Command[0] != "/bin/bash" || container.spec.Command[1] != "-l" {
		t.Fatalf("command = %v", container.spec.Command)
	}
	status, err := e.sched.TerminalStatus(context.Background(), e.member.ID)
	if err != nil {
		t.Fatalf("TerminalStatus: %v", err)
	}
	if !status.Running || len(status.Tabs) != 1 || status.Tabs[0] != "main" {
		t.Fatalf("status = %+v", status)
	}
	if err := e.sched.StopTerminal(context.Background(), e.member.ID); err != nil {
		t.Fatalf("StopTerminal: %v", err)
	}
	if _, err := e.db.GetTerminal(context.Background(), e.member.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("terminal row after stop: %v", err)
	}
}

func TestEnsureTerminalTabRetriesShellFallback(t *testing.T) {
	e := newTestEnv(t, func(cfg *Config) { cfg.StandardImage = "standard:latest" })
	if _, err := e.sched.EnsureTerminal(context.Background(), e.member.ID); err != nil {
		t.Fatalf("EnsureTerminal: %v", err)
	}
	calls := 0
	e.rt.execTTYHook = func(ctx context.Context, id runtime.ID, argv []string, workDir string, cols, rows uint) (runtime.Attachment, error) {
		calls++
		if calls == 1 {
			return nil, &runtime.ExecExitError{Code: 127}
		}
		return e.rt.attachForExec(ctx, id, argv, workDir, cols, rows)
	}
	if err := e.sched.EnsureTerminalTab(context.Background(), e.member.ID, "logs", 120, 40); err != nil {
		t.Fatalf("EnsureTerminalTab: %v", err)
	}
	execCalls := e.rt.execTTYCalls()
	if len(execCalls) != 2 || execCalls[0].argv[0] != "/bin/bash" || execCalls[1].argv[0] != "/bin/sh" {
		t.Fatalf("exec calls = %+v", execCalls)
	}
	if execCalls[1].cols != 120 || execCalls[1].rows != 40 || execCalls[1].workDir != "/root" {
		t.Fatalf("fallback call = %+v", execCalls[1])
	}
}

// A member typing `exit` in the main shell must get a fresh environment on
// the next open: the exited container is destroyed, the row pruned, and
// EnsureTerminal creates a new container instead of adopting the corpse.
func TestEnsureTerminalRecreatesAfterMainShellExit(t *testing.T) {
	e := newTestEnv(t, func(cfg *Config) { cfg.StandardImage = "standard:latest" })
	first, err := e.sched.EnsureTerminal(context.Background(), e.member.ID)
	if err != nil {
		t.Fatalf("EnsureTerminal: %v", err)
	}
	container, err := e.rt.get(runtime.ID(first.ContainerID))
	if err != nil {
		t.Fatal(err)
	}
	container.exitNow(0)
	waitFor(t, "terminal supervision cleanup", func() bool {
		return e.sched.lookupTerminal(e.member.ID) == nil
	})
	waitFor(t, "terminal row pruned", func() bool {
		_, rowErr := e.db.GetTerminal(context.Background(), e.member.ID)
		return errors.Is(rowErr, store.ErrNotFound)
	})
	second, err := e.sched.EnsureTerminal(context.Background(), e.member.ID)
	if err != nil {
		t.Fatalf("EnsureTerminal after exit: %v", err)
	}
	if second.ContainerID == first.ContainerID {
		t.Fatalf("terminal reused the exited container %s", first.ContainerID)
	}
	if _, err := e.rt.get(runtime.ID(first.ContainerID)); err == nil {
		t.Fatal("exited terminal container was never destroyed")
	}
}

func profileForTerminalTest() harness.Profile {
	return harness.Profile{}
}

func TestEnsureTerminalTabLimit(t *testing.T) {
	e := newTestEnv(t, func(cfg *Config) { cfg.StandardImage = "standard:latest" })
	for _, tab := range []string{"t1", "t2", "t3", "t4", "t5"} {
		if err := e.sched.EnsureTerminalTab(context.Background(), e.member.ID, tab, 80, 24); err != nil {
			t.Fatalf("EnsureTerminalTab(%q): %v", tab, err)
		}
	}
	if err := e.sched.EnsureTerminalTab(context.Background(), e.member.ID, "t6", 80, 24); !errors.Is(err, ErrTerminalTabLimit) {
		t.Fatalf("EnsureTerminalTab over limit error = %v, want %v", err, ErrTerminalTabLimit)
	}
}
