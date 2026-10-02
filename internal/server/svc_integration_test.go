package server

import (
	"database/sql"
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

// A run agent verifies in the home its run's container mounts, recorded on
// the run row at launch. A handoff rewrites the run's owner but not that
// home, so verification keeps the launcher's home and never gets the new
// owner's or the account owner's. No container or sidecar is consulted: the
// runtime panics on any call.
func TestIntegrationEnvironmentUsesTheRunContainersHome(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")
	db, err := store.Open(dbPath)
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
	sched, err := scheduler.New(scheduler.Config{
		Store: db, Bus: bus, Homes: homes, StateDir: filepath.Join(dir, "scheduler"),
		Runtime: struct{ runtime.Runtime }{}, Git: struct{ scheduler.GitEngine }{}, PTY: struct{ scheduler.PTYHost }{},
		StandardImage: "busybox:1.36", WorktreeMount: "/workspace",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sched.Close() })
	environment := integrationEnvironment(Deps{Store: db, Runs: sched})
	checkout := filepath.Join(dir, "checkout")

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

	wantOnlyHome("before handoff", launcher.ID)

	if err = db.TransferRun(ctx, run.ID, recipient.ID); err != nil {
		t.Fatal(err)
	}
	wantOnlyHome("after handoff", launcher.ID)

	// A row from before account shares were narrowed has no home member;
	// its container mounted the account's whole home.
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	if _, err = raw.ExecContext(ctx, `UPDATE runs SET home_member_id = NULL WHERE id = ?`, run.ID); err != nil {
		t.Fatal(err)
	}
	wantOnlyHome("legacy row", owner.ID)
}
