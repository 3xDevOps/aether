package scheduler

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/memberhome"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/store/storetest"
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
	profile := harness.Profile{Name: "agent", VMState: []harness.VMState{{Dir: ".agent/db", Env: "AGENT_DB_HOME"}, {Dir: ".agent/index"}}}
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
		for _, m := range plan.Mounts {
			if m.ContainerPath == "/root/.agent/db" || m.ContainerPath == "/root/.agent/index" {
				state = append(state, m)
			}
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

func TestOnlyOneVMSessionRunsAnAgentWithUnmovableDatabases(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(cfg *Config) {
		cfg.RunContainerTTL = time.Hour
		cfg.Harnesses["omp"] = HarnessSpec{TUIArgs: []string{"fake-omp", "{task}"}}
		cfg.Runtime = vmFakeRuntime{cfg.Runtime.(*fakeRuntime)}
	})
	ctx := t.Context()
	launch := func(task string) (*domain.Run, error) {
		return e.sched.Launch(ctx, e.ws.ID, e.member.ID, e.member.ID, task, "omp", domain.LaunchTUI)
	}
	first, err := launch("first")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = launch("second"); err == nil || !strings.Contains(err.Error(), "oh-my-pi keeps its logins in a SQLite database that two session VMs cannot share") || !strings.Contains(err.Error(), string(first.ID)) {
		t.Fatalf("a second live launch = %v", err)
	}
	runs, err := e.db.ListRunsByMember(ctx, e.member.ID)
	if err != nil || len(runs) != 1 {
		t.Fatalf("a refused launch left %d sessions, %v", len(runs), err)
	}
	if err = e.sched.CloseRun(ctx, first.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatal(err)
	}
	e.waitStoreStatus(t, first.ID, domain.RunMerged)
	second, err := launch("second")
	if err != nil {
		t.Fatalf("a launch beside a closed session = %v", err)
	}
	if _, err = e.sched.Relaunch(ctx, first.ID, e.member.ID); err == nil || !strings.Contains(err.Error(), string(second.ID)) {
		t.Fatalf("relaunch beside a live session = %v", err)
	}
	if err = e.sched.Kill(ctx, second.ID, e.member.ID); err != nil {
		t.Fatal(err)
	}
	e.waitStoreStatus(t, second.ID, domain.RunAbandoned)
	// The killed session counts until its VM is gone.
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		_, err = e.sched.Relaunch(ctx, first.ID, e.member.ID)
		if err != nil && strings.Contains(err.Error(), "after this one closed") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("relaunch after another session used the databases = %v", err)
		}
	}
}

func TestVMSingleCountsTheOwnersHomeOnlyForADirectoryLogin(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(cfg *Config) { cfg.Runtime = vmFakeRuntime{cfg.Runtime.(*fakeRuntime)} })
	ctx := t.Context()
	grace := &domain.Member{DisplayName: "Grace", PublicKey: testPublicKey(t), Color: "#3cb44b", Role: domain.RoleCollaborator}
	if err := e.db.CreateMember(ctx, grace); err != nil {
		t.Fatal(err)
	}
	if err := e.cfg.Homes.PrepareLoginMountpoint(grace.ID, ".omp/agent", true); err != nil {
		t.Fatal(err)
	}
	if err := e.cfg.Homes.PrepareLoginMountpoint(grace.ID, ".copilot/config.json", false); err != nil {
		t.Fatal(err)
	}
	for _, agent := range []string{"omp", "copilot"} {
		live := &domain.Run{WorkspaceID: e.ws.ID, MemberID: grace.ID, Harness: agent, Mode: domain.LaunchTUI, Status: domain.RunNeedsAttention}
		if err := e.db.CreateRun(ctx, live); err != nil {
			t.Fatal(err)
		}
		profile, _ := harness.Lookup(agent)
		err := e.sched.vmSingleConflict(ctx, profile, agent, e.member.ID, grace.ID, "")
		if agent == "omp" && (err == nil || !strings.Contains(err.Error(), "Grace's session "+string(live.ID)) || !strings.Contains(err.Error(), "ask them to close it, or launch on another account")) {
			t.Errorf("omp on Grace's account beside her live session = %v", err)
		}
		if agent == "copilot" && err != nil {
			t.Errorf("copilot on Grace's account, whose database stays in the launcher's home = %v", err)
		}
	}
}
