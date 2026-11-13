package scheduler

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
		if len(state) != 2 || state[0].Kind != runtime.MountScratch || state[1].Kind != runtime.MountScratch || plan.Env["AGENT_DB_HOME"] != "/root/.agent/db" {
			t.Fatalf("VM state mounts %+v, AGENT_DB_HOME %q", state, plan.Env["AGENT_DB_HOME"])
		}
		home, _ := homes.Path(member.ID)
		if info, err := os.Stat(filepath.Join(home, ".agent/db")); err != nil || !info.IsDir() {
			t.Errorf("the mount point in the home: %v, %v", info, err)
		}
	}
}

func TestOnlyOneVMSessionRunsAnAgentWithUnmovableDatabases(t *testing.T) {
	e := newTestEnv(t, nil)
	ctx := t.Context()
	members := map[string]domain.MemberID{"ada": e.member.ID}
	for _, name := range []string{"grace", "linus", "edsger"} {
		m := &domain.Member{DisplayName: name, PublicKey: testPublicKey(t), Color: "#3cb44b", Role: domain.RoleCollaborator}
		if err := e.db.CreateMember(ctx, m); err != nil {
			t.Fatal(err)
		}
		members[name] = m.ID
	}
	live := &domain.Run{WorkspaceID: e.ws.ID, MemberID: members["ada"], AccountMemberID: members["grace"], Harness: "omp", Mode: domain.LaunchTUI, Status: domain.RunNeedsAttention}
	ended := &domain.Run{WorkspaceID: e.ws.ID, MemberID: members["linus"], Harness: "omp", Mode: domain.LaunchTUI, Status: domain.RunCompleted}
	for _, r := range []*domain.Run{live, ended} {
		if err := e.db.CreateRun(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	omp := harness.Profile{Name: "omp", DisplayName: "oh-my-pi", VMSingle: "keeps its logins in a SQLite database that two session VMs cannot share"}
	vm := &Scheduler{cfg: Config{Store: e.db, Runtime: vmRuntime{}}}
	docker := &Scheduler{cfg: Config{Store: e.db}}
	for _, tc := range []struct {
		run    *domain.Run
		s      *Scheduler
		refuse bool
	}{
		{&domain.Run{ID: "run-own", MemberID: members["ada"], Harness: "omp"}, vm, true},
		{&domain.Run{ID: "run-shared", MemberID: members["edsger"], AccountMemberID: members["grace"], Harness: "omp"}, vm, true},
		{&domain.Run{ID: "run-other", MemberID: members["linus"], Harness: "omp"}, vm, false},
		{&domain.Run{ID: "run-claude", MemberID: members["ada"], Harness: "claude"}, vm, false},
		{&domain.Run{ID: "run-docker", MemberID: members["ada"], Harness: "omp"}, docker, false},
		{live, vm, false},
	} {
		err := tc.s.checkVMSingle(ctx, tc.run, omp)
		want := "oh-my-pi keeps its logins in a SQLite database that two session VMs cannot share, and session " + string(live.ID)
		if tc.refuse != (err != nil) || (err != nil && !strings.Contains(err.Error(), want)) {
			t.Errorf("%s: %v", tc.run.ID, err)
		}
	}
}
