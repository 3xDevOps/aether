package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/integration"
	"github.com/3xDevOps/Aether/internal/memberhome"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/scheduler"
	"github.com/3xDevOps/Aether/internal/store"
)

// keyedRuntime finds every run's container under one ID; building an
// environment for a member without a saved image touches nothing else.
type keyedRuntime struct{ runtime.Runtime }

func (keyedRuntime) FindByCreationKey(context.Context, string) (runtime.ID, error) {
	return "c1", nil
}

// A run agent verifies in the home its own container mounts. A handoff
// rewrites the run's owner but not that container, so verification keeps
// the launcher's home and never gets the new owner's or the account owner's.
func TestIntegrationEnvironmentUsesTheRunContainersHome(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	bus, err := events.NewInProc(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bus.Close() })
	ws := &domain.Workspace{Name: "ws", BaseBranch: "main"}
	if err = db.CreateWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}
	launcher := &domain.Member{DisplayName: "Ada", TailnetLogin: "ada@example.com", Role: domain.RoleCollaborator}
	owner := &domain.Member{DisplayName: "Grace", TailnetLogin: "grace@example.com", Role: domain.RoleCollaborator}
	recipient := &domain.Member{DisplayName: "Lin", TailnetLogin: "lin@example.com", Role: domain.RoleCollaborator}
	for _, m := range []*domain.Member{launcher, owner, recipient} {
		if err = db.CreateMember(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	run := &domain.Run{WorkspaceID: ws.ID, MemberID: launcher.ID, AccountMemberID: owner.ID, Task: "t", Harness: "claude", Mode: domain.LaunchTUI, Status: domain.RunRunning}
	if err = db.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	homes, err := memberhome.New(filepath.Join(dir, "homes"))
	if err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(dir, "scheduler")
	sched, err := scheduler.New(scheduler.Config{
		Store: db, Bus: bus, Homes: homes, StateDir: stateDir,
		Runtime: keyedRuntime{}, Git: struct{ scheduler.GitEngine }{}, PTY: struct{ scheduler.PTYHost }{},
		StandardImage: "busybox:1.36", WorktreeMount: "/workspace",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sched.Close() })
	environment := integrationEnvironment(Deps{Store: db, Runs: sched})
	checkout := filepath.Join(dir, "checkout")

	if _, err = environment(ctx, integration.Actor{RunID: run.ID}, ws, checkout); err == nil {
		t.Fatal("a run with no live container or sidecar got a verification environment")
	}

	writeSidecar := func(fields map[string]string) {
		t.Helper()
		data, marshalErr := json.Marshal(fields)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if mkdirErr := os.MkdirAll(stateDir, 0o700); mkdirErr != nil {
			t.Fatal(mkdirErr)
		}
		if writeErr := os.WriteFile(filepath.Join(stateDir, string(run.ID)+".json"), data, 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	wantOnlyHome := func(stage string, member domain.MemberID) {
		t.Helper()
		spec, envErr := environment(ctx, integration.Actor{RunID: run.ID}, ws, checkout)
		if envErr != nil {
			t.Fatalf("%s: integration environment: %v", stage, envErr)
		}
		home, pathErr := homes.Path(member)
		if pathErr != nil {
			t.Fatal(pathErr)
		}
		if len(spec.Mounts) != 1 || spec.Mounts[0] != (runtime.Mount{HostPath: home, ContainerPath: "/root"}) {
			t.Fatalf("%s: mounts = %+v, want only %s's home %q", stage, spec.Mounts, member, home)
		}
	}

	writeSidecar(map[string]string{"run_id": string(run.ID), "container_id": "c1", "home_member": string(launcher.ID), "login_member": string(owner.ID)})
	wantOnlyHome("before handoff", launcher.ID)

	if err = db.TransferRun(ctx, run.ID, recipient.ID); err != nil {
		t.Fatal(err)
	}
	wantOnlyHome("after handoff", launcher.ID)

	writeSidecar(map[string]string{"run_id": string(run.ID), "container_id": "c1"})
	wantOnlyHome("legacy sidecar", owner.ID)
}
