package scheduler

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/harness"
	"github.com/3xDevOps/cloudfield/internal/memberhome"
	"github.com/3xDevOps/cloudfield/internal/runtime"
	"github.com/3xDevOps/cloudfield/internal/store/storetest"
)

type vmRuntime struct{ runtime.Runtime }

func (vmRuntime) SessionVMs() {}

func TestASessionVMGetsItsOwnAgentDatabases(t *testing.T) {
	t.Parallel()
	homes, err := memberhome.New(filepath.Join(t.TempDir(), "homes"), filepath.Join(t.TempDir(), "home-caches"), nil)
	if err != nil {
		t.Fatal(err)
	}
	db, err := storetest.Open(filepath.Join(t.TempDir(), "aether.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	profile := harness.Profile{Name: "agent", VMState: []harness.VMState{{Dir: ".agent/db", Env: "AGENT_DB_HOME"}, {Dir: ".agent/index", Seed: []string{"login.json"}}}}
	run := &domain.Run{ID: "run-1"}
	member := &domain.Member{ID: "member"}
	for _, vm := range []bool{false, true} {
		s := &Scheduler{cfg: Config{StandardImage: "standard:latest", Homes: homes, Store: db, StateDir: t.TempDir()}}
		if vm {
			s.cfg.Runtime = vmRuntime{}
		}
		plan, err := s.BuildEnvironmentPlan(context.Background(), run, &domain.Workspace{ID: "ws"}, member, profile, EnvironmentPurposeRun)
		if err != nil {
			t.Fatal(err)
		}
		var state []runtime.Mount
		seeded := false
		for _, m := range plan.Mounts {
			if m.ContainerPath == "/root/.agent/db" || m.ContainerPath == "/root/.agent/index" {
				state = append(state, m)
			}
			seeded = seeded || m == runtime.Mount{HostPath: filepath.Join(s.vmDatabasesRoot(), "run-1"), ContainerPath: "/root/.agent/index/login.json", Kind: runtime.MountSeed}
		}
		if seeded != vm {
			t.Errorf("VM %v: the index's login is seeded: %v", vm, seeded)
		}
		if !vm {
			if len(state) != 0 || plan.Env["AGENT_DB_HOME"] != "" {
				t.Errorf("a Docker session got VM state: %+v, %q", state, plan.Env["AGENT_DB_HOME"])
			}
			continue
		}
		disk := filepath.Join(s.vmDatabasesRoot(), "run-1")
		if len(state) != 2 || state[0].HostPath != disk || state[1].HostPath != disk || state[0].Subpath != "0" || state[1].Subpath != "1" ||
			state[0].Kind != runtime.MountState || plan.Env["AGENT_DB_HOME"] != "/root/.agent/db" {
			t.Fatalf("VM state mounts %+v, AGENT_DB_HOME %q", state, plan.Env["AGENT_DB_HOME"])
		}
		home, _ := homes.Path(member.ID)
		if info, err := os.Stat(filepath.Join(home, ".agent/db")); err != nil || !info.IsDir() {
			t.Errorf("the mount point in the home: %v, %v", info, err)
		}
	}
}

type vmFakeRuntime struct{ *fakeRuntime }

func (vmFakeRuntime) SessionVMs() {}

// A login mounted on its own, as an isolated session or a shared account
// mounts it, sits inside the session's own copy of the directory, which the
// VM seeds from it before covering it.
func TestAVMSessionSeedsALoginMountedInsideItsState(t *testing.T) {
	t.Parallel()
	homes, err := memberhome.New(filepath.Join(t.TempDir(), "homes"), filepath.Join(t.TempDir(), "home-caches"), nil)
	if err != nil {
		t.Fatal(err)
	}
	db, err := storetest.Open(filepath.Join(t.TempDir(), "aether.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s := &Scheduler{cfg: Config{StandardImage: "standard:latest", Homes: homes, Store: db, StateDir: t.TempDir(), Runtime: vmRuntime{}}}
	member := &domain.Member{ID: "member"}
	homePath, err := homes.Path(member.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(homePath, ".copilot"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(homePath, ".copilot", "config.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	copilot, _ := harness.Lookup("copilot")
	run := &domain.Run{ID: "run-iso", Permission: domain.PermissionIsolated}
	plan, err := s.BuildEnvironmentPlan(context.Background(), run, &domain.Workspace{ID: "ws"}, member, copilot, EnvironmentPurposeRun)
	if err != nil {
		t.Fatal(err)
	}
	login := slices.IndexFunc(plan.Mounts, func(m runtime.Mount) bool { return m.ContainerPath == "/root/.copilot/config.json" && m.Kind == "" })
	state := slices.IndexFunc(plan.Mounts, func(m runtime.Mount) bool { return m.ContainerPath == "/root/.copilot" && m.Kind == runtime.MountState })
	seed := slices.IndexFunc(plan.Mounts, func(m runtime.Mount) bool {
		return m.ContainerPath == "/root/.copilot/config.json" && m.Kind == runtime.MountSeed
	})
	if login < 0 || state < login || seed < state {
		t.Fatalf("login %d, state %d, seed %d in %+v", login, state, seed, plan.Mounts)
	}
}
