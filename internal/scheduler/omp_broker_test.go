package scheduler

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/harness"
	"github.com/3xDevOps/cloudfield/internal/memberhome"
	"github.com/3xDevOps/cloudfield/internal/ompbroker"
	"github.com/3xDevOps/cloudfield/internal/runtime"
	"github.com/3xDevOps/cloudfield/internal/store/storetest"
)

func TestEveryOMPInASessionVMUsesItsAccountsBroker(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	homes, err := memberhome.New(filepath.Join(t.TempDir(), "homes"), filepath.Join(t.TempDir(), "home-caches"), nil)
	if err != nil {
		t.Fatal(err)
	}
	db, err := storetest.Open(filepath.Join(t.TempDir(), "aether.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ada := &domain.Member{DisplayName: "Ada", PublicKey: testPublicKey(t), Color: "#3cb44b", Role: domain.RoleSuperAdmin}
	if err = db.CreateMember(ctx, ada); err != nil {
		t.Fatal(err)
	}
	// A unix socket's path must stay short, unlike t.TempDir's.
	state, err := os.MkdirTemp("", "omp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(state) })
	s := &Scheduler{cfg: Config{StandardImage: "standard:latest", Homes: homes, Store: db, StateDir: state, Runtime: vmRuntime{}}}
	s.useOMPBroker(ompbroker.NewManager(ompbroker.Config{Dir: t.TempDir()}))
	omp, _ := harness.Lookup("omp")
	claude, _ := harness.Lookup("claude")
	run := &domain.Run{ID: "run-omp", MemberID: ada.ID, Harness: "omp"}
	for _, c := range []struct {
		name    string
		run     *domain.Run
		profile harness.Profile
		purpose EnvironmentPurpose
		session string
		state   bool
		shared  bool
	}{
		{"omp session", run, omp, EnvironmentPurposeRun, "run-omp", true, true},
		{"claude session", &domain.Run{ID: "run-claude", MemberID: ada.ID, Harness: "claude"}, claude, EnvironmentPurposeRun, "run-claude", false, true},
		{"environment terminal", nil, harness.Profile{}, EnvironmentPurposeTerminal, "terminal-" + string(ada.ID), false, true},
		{"isolated omp session", &domain.Run{ID: "run-iso", MemberID: ada.ID, Harness: "omp", Permission: domain.PermissionIsolated}, omp, EnvironmentPurposeRun, "run-iso", true, false},
	} {
		// A workspace's XDG_DATA_HOME stays, except in an omp session.
		ws := &domain.Workspace{ID: "ws", Environment: domain.WorkspaceEnvironment{Variables: map[string]string{"XDG_DATA_HOME": "/srv/data"}}}
		plan, planErr := s.BuildEnvironmentPlan(ctx, c.run, ws, ada, c.profile, c.purpose)
		if planErr != nil {
			t.Fatalf("%s: %v", c.name, planErr)
		}
		mounts := map[string]runtime.Mount{}
		for _, m := range plan.Mounts {
			mounts[m.ContainerPath] = m
		}
		broker := mounts[ompBrokerDir]
		if broker.Port != ompBrokerPort || broker.HostPath != filepath.Join(s.cfg.StateDir, "omp-broker", c.session) {
			t.Errorf("%s: broker mount %+v", c.name, broker)
		}
		if _, statErr := os.Stat(filepath.Join(broker.HostPath, ompbroker.SocketName)); statErr != nil {
			t.Errorf("%s: the session's broker socket: %v", c.name, statErr)
		}
		data := "/srv/data"
		if c.state {
			data = "/root/.local/share"
		}
		for key, want := range map[string]string{
			"OMP_AUTH_BROKER_URL": "http://127.0.0.1:47630", "OMP_AUTH_BROKER_SNAPSHOT_TTL_MS": "0",
			"XDG_DATA_HOME": data, "XDG_STATE_HOME": "/root/.local/state", "XDG_CACHE_HOME": "/root/.cache",
		} {
			if plan.Env[key] != want {
				t.Errorf("%s: %s = %q, want %q", c.name, key, plan.Env[key], want)
			}
		}
		if plan.Env["OMP_AUTH_BROKER_TOKEN"] == "" {
			t.Errorf("%s: no broker token; omp refuses a broker URL without one", c.name)
		}
		if m, ok := mounts["/root/.local/share/omp/plugins"]; ok != c.shared || (ok && m.Subpath != ".omp/plugins") {
			t.Errorf("%s: home plugins mount %+v, present %v, want %v", c.name, m, ok, c.shared)
		}
		if m := mounts["/root/.local/share/omp"]; (m.Kind == runtime.MountState) != c.state {
			t.Errorf("%s: omp's own data directory: %+v", c.name, m)
		}
	}
	home, _ := homes.Path(ada.ID)
	for _, dir := range []string{".local/share/omp", ".local/state/omp", ".cache/omp", ".omp/plugins", ".omp/agent/sessions"} {
		if info, statErr := os.Stat(filepath.Join(home, dir)); statErr != nil || !info.IsDir() {
			t.Errorf("%s in the home: %v", dir, statErr)
		}
	}

	ws := &domain.Workspace{Name: "ws", BaseBranch: "main"}
	if err = db.CreateWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}
	stored := &domain.Run{WorkspaceID: ws.ID, MemberID: ada.ID, Harness: "omp", Mode: domain.LaunchTUI, Status: domain.RunRunning}
	if err = db.CreateRun(ctx, stored); err != nil {
		t.Fatal(err)
	}
	if a, authErr := s.authorizeOMP(ctx, string(stored.ID)); authErr != nil || a.ID != string(ada.ID) || a.Shared {
		t.Errorf("an open session's account %+v, %v", a, authErr)
	}
	if _, err = s.authorizeOMP(ctx, "run-missing"); err == nil {
		t.Error("an unknown session was authorized")
	}
	a, err := s.authorizeOMP(ctx, "terminal-"+string(ada.ID))
	if err != nil || a.ID != string(ada.ID) || a.Home != home {
		t.Errorf("terminal account %+v, %v", a, err)
	}
	// The standard image has no USER, so its sessions, and the files they
	// write in the home, are unprivilegedUser's.
	s.cfg.Runtime = noUserImage{}
	wantUID, wantGID, _ := strings.Cut(unprivilegedUser, ":")
	if uid, gid, ownerErr := s.sessionOwner(ctx, ada.ID); ownerErr != nil || strconv.Itoa(uid) != wantUID || strconv.Itoa(gid) != wantGID {
		t.Errorf("the owner of a session from an image with no USER = %d:%d, %v; want %s", uid, gid, ownerErr, unprivilegedUser)
	}
	// stored has no broker socket: before it has a VM it is a session still
	// starting, and with one it is an omp session from before brokers.
	if _, _, prepErr := s.prepareOMPBroker(ctx, ada.ID); prepErr != nil {
		t.Errorf("a broker beside a session still starting = %v", prepErr)
	}
	s.runs = map[domain.RunID]*supervised{stored.ID: {containerID: "vm-from-before"}}
	if _, _, prepErr := s.prepareOMPBroker(ctx, ada.ID); prepErr == nil || !strings.Contains(prepErr.Error(), string(stored.ID)) {
		t.Errorf("a broker beside an omp session from before brokers = %v", prepErr)
	}
	delete(s.runs, stored.ID)
	now := time.Now()
	stored.FinishedAt, stored.Status = &now, domain.RunAbandoned
	if err = db.UpdateRun(ctx, stored); err != nil {
		t.Fatal(err)
	}
	if _, err = s.authorizeOMP(ctx, string(stored.ID)); err == nil || !strings.Contains(err.Error(), "has ended") {
		t.Errorf("a finished session = %v", err)
	}
	if _, _, err = s.prepareOMPBroker(ctx, ada.ID); err != nil {
		t.Errorf("a broker once the old session ended = %v", err)
	}
}

// noUserImage runs session VMs from images that set no USER.
type noUserImage struct{ vmRuntime }

func (noUserImage) ImageUser(context.Context, string) (string, error) { return "", nil }
