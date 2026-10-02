package server

import (
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

// A run agent verifies in its run owner's environment, the one its own
// container mounts, even when the run uses another member's shared account;
// it never gets anything of the account owner's.
func TestIntegrationEnvironmentUsesRunOwner(t *testing.T) {
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
	for _, m := range []*domain.Member{launcher, owner} {
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
	// Building an environment for a member without a saved image touches
	// none of these seams.
	sched, err := scheduler.New(scheduler.Config{
		Store: db, Bus: bus, Homes: homes, StateDir: filepath.Join(dir, "scheduler"),
		Runtime: struct{ runtime.Runtime }{}, Git: struct{ scheduler.GitEngine }{}, PTY: struct{ scheduler.PTYHost }{},
		StandardImage: "busybox:1.36", WorktreeMount: "/workspace",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sched.Close() })

	spec, err := integrationEnvironment(Deps{Store: db, Runs: sched})(ctx, integration.Actor{RunID: run.ID}, ws, filepath.Join(dir, "checkout"))
	if err != nil {
		t.Fatalf("integration environment: %v", err)
	}
	launcherHome, err := homes.Path(launcher.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.Mounts) != 1 || spec.Mounts[0] != (runtime.Mount{HostPath: launcherHome, ContainerPath: "/root"}) {
		t.Fatalf("mounts = %+v, want only the run owner's home %q", spec.Mounts, launcherHome)
	}
}
