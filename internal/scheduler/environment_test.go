package scheduler

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/memberhome"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/secretstore"
	"github.com/3xDevOps/Aether/internal/store/storetest"
)

func TestBuildEnvironmentPlanMountsOnePersistentHomeFirst(t *testing.T) {
	t.Parallel()
	homes, err := memberhome.New(filepath.Join(t.TempDir(), "homes"), filepath.Join(t.TempDir(), "home-caches"), nil)
	if err != nil {
		t.Fatalf("memberhome.New: %v", err)
	}
	db, err := storetest.Open(filepath.Join(t.TempDir(), "aether.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s := &Scheduler{cfg: Config{StandardImage: "standard:latest", Homes: homes, Store: db, StateDir: t.TempDir()}}
	ws := &domain.Workspace{ID: "ws", Environment: domain.WorkspaceEnvironment{
		Variables: map[string]string{"PATH": "/workspace/bin", "EXTRA": "yes"},
	}}
	member := &domain.Member{ID: "member"}
	for _, purpose := range []EnvironmentPurpose{EnvironmentPurposeRun, EnvironmentPurposeTerminal} {
		plan, planErr := s.BuildEnvironmentPlan(context.Background(), nil, ws, member, harness.Profile{}, purpose)
		if planErr != nil {
			t.Fatalf("BuildEnvironmentPlan(%q): %v", purpose, planErr)
		}
		wantMounts := 2
		if purpose == EnvironmentPurposeTerminal {
			wantMounts = 3
		}
		if len(plan.Mounts) != wantMounts {
			t.Fatalf("purpose %q mounts = %v, want home plus isolated cache mount", purpose, plan.Mounts)
		}
		home, homeErr := homes.Path(member.ID)
		if homeErr != nil {
			t.Fatalf("homes.Path: %v", homeErr)
		}
		want := runtime.Mount{HostPath: home, ContainerPath: "/root"}
		if plan.Mounts[0] != want {
			t.Fatalf("purpose %q home mount = %+v, want %+v", purpose, plan.Mounts[0], want)
		}
		pool := "runs"
		if purpose == EnvironmentPurposeTerminal {
			pool = "terminal"
		}
		cache := runtime.Mount{HostPath: filepath.Join(homes.CacheRoot(), string(member.ID), pool, "data"), ContainerPath: "/aether-cache"}
		if plan.Mounts[1] != cache {
			t.Fatalf("purpose %q cache mount = %+v, want %+v", purpose, plan.Mounts[1], cache)
		}
		if plan.Env["GOCACHE"] != "/aether-cache/go-build" || plan.Env["HOME"] != "/root" || plan.Env["PATH"] != "/root/.local/bin:/workspace/bin" {
			t.Fatalf("cache defaults changed persistent environment: %#v", plan.Env)
		}

	}
}

// Runs and the environment terminal both reach the member's signing agent,
// over a workspace that sets SSH_AUTH_SOCK itself.
func TestBuildEnvironmentPlanMountsTheSigningAgent(t *testing.T) {
	t.Parallel()
	homes, err := memberhome.New(filepath.Join(t.TempDir(), "homes"), filepath.Join(t.TempDir(), "home-caches"), nil)
	if err != nil {
		t.Fatalf("memberhome.New: %v", err)
	}
	db, err := storetest.Open(filepath.Join(t.TempDir(), "aether.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	cfg := Config{StandardImage: "standard:latest", Homes: homes, Store: db, StateDir: t.TempDir()}
	withSigning(t)(&cfg)
	s := &Scheduler{cfg: cfg}
	ws := &domain.Workspace{ID: "ws", Environment: domain.WorkspaceEnvironment{
		Variables: map[string]string{"SSH_AUTH_SOCK": "/tmp/workspace-agent.sock"},
	}}
	member := &domain.Member{ID: "member"}
	for _, purpose := range []EnvironmentPurpose{EnvironmentPurposeRun, EnvironmentPurposeTerminal} {
		plan, planErr := s.BuildEnvironmentPlan(t.Context(), nil, ws, member, harness.Profile{}, purpose)
		if planErr != nil {
			t.Fatalf("BuildEnvironmentPlan(%q): %v", purpose, planErr)
		}
		if got := plan.Env["SSH_AUTH_SOCK"]; got != coordtransport.SigningSocketPath {
			t.Errorf("purpose %q SSH_AUTH_SOCK = %q, want %q", purpose, got, coordtransport.SigningSocketPath)
		}
		i := slices.IndexFunc(plan.Mounts, func(m runtime.Mount) bool { return m.ContainerPath == coordtransport.SigningMountDir })
		if i < 0 || !plan.Mounts[i].ReadOnly {
			t.Fatalf("purpose %q mounts = %+v, want the agent directory read-only at %s", purpose, plan.Mounts, coordtransport.SigningMountDir)
		}
		if _, statErr := os.Stat(filepath.Join(plan.Mounts[i].HostPath, "agent.sock")); statErr != nil {
			t.Errorf("purpose %q agent socket: %v", purpose, statErr)
		}
	}
}

func TestBuildEnvironmentPlanUsesStandardImageAndToolsFirstPath(t *testing.T) {
	t.Parallel()
	s := &Scheduler{cfg: Config{StandardImage: "standard:latest"}}
	ws := &domain.Workspace{ID: "ws", Environment: domain.WorkspaceEnvironment{
		Variables: map[string]string{"PATH": "/workspace/bin", "EXTRA": "yes"},
	}}
	member := &domain.Member{ID: "member"}
	plan, err := s.BuildEnvironmentPlan(context.Background(), nil, ws, member, harness.Profile{}, EnvironmentPurposeRun)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Image != "standard:latest" {
		t.Fatalf("image = %q, want standard:latest", plan.Image)
	}
	if plan.Env["PATH"] != "/root/.local/bin:/workspace/bin" {
		t.Fatalf("PATH = %q, want tools first", plan.Env["PATH"])
	}
	if plan.Env["EXTRA"] != "yes" || plan.Env["HOME"] != "/root" {
		t.Fatalf("environment = %#v", plan.Env)
	}
}
func TestBuildEnvironmentPlanUsesSavedMemberImage(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(cfg *Config) { cfg.StandardImage = "standard:latest" })
	const saved = "aether/member-saved:123"
	e.member.Image = saved
	if err := e.db.UpdateMemberImage(t.Context(), e.member.ID, saved); err != nil {
		t.Fatalf("UpdateMemberImage: %v", err)
	}
	e.rt.images = map[string]string{saved: "saved"}
	for _, purpose := range []EnvironmentPurpose{EnvironmentPurposeRun, EnvironmentPurposeTerminal} {
		plan, err := e.sched.BuildEnvironmentPlan(t.Context(), nil, e.ws, e.member, harness.Profile{}, purpose)
		if err != nil {
			t.Fatalf("BuildEnvironmentPlan(%q): %v", purpose, err)
		}
		if plan.Image != saved {
			t.Fatalf("purpose %q image = %q, want %q", purpose, plan.Image, saved)
		}
	}
}

func TestBuildEnvironmentPlanReadsRowAndSecretsAsOneChange(t *testing.T) {
	t.Parallel()
	secrets := secretstore.New(t.TempDir())
	e := newTestEnv(t, func(cfg *Config) {
		cfg.StandardImage = "standard:latest"
		cfg.Secrets = secrets
	})
	if err := secrets.Put(e.ws.ID, map[string]string{"API_TOKEN": "as-a-secret"}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// What a launch read before API_TOKEN turned plain.
	stale := *e.ws
	e.ws.Environment.Variables = map[string]string{"API_TOKEN": "now-plain"}
	if err := e.db.UpdateWorkspace(t.Context(), e.ws); err != nil {
		t.Fatalf("UpdateWorkspace: %v", err)
	}
	if err := secrets.Put(e.ws.ID, nil); err != nil {
		t.Fatalf("Put: %v", err)
	}
	build := func() (*EnvironmentPlan, error) {
		return e.sched.BuildEnvironmentPlan(t.Context(), nil, &stale, e.member, harness.Profile{}, EnvironmentPurposeRun)
	}
	plan, err := build()
	if err != nil || plan.Env["API_TOKEN"] != "now-plain" || len(plan.SecretEnv) != 0 {
		t.Fatalf("plan = %+v, %v, want API_TOKEN as the plain variable it became", plan, err)
	}

	// A change that has written one of the two holds the launch back.
	secrets.Lock()
	built := make(chan error, 1)
	go func() {
		_, err := build()
		built <- err
	}()
	select {
	case err := <-built:
		t.Fatalf("plan built during an environment change: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	secrets.Unlock()
	if err := <-built; err != nil {
		t.Fatalf("plan after the change: %v", err)
	}
}

func TestBuildEnvironmentPlanRejectsMissingSavedMemberImage(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(cfg *Config) { cfg.StandardImage = "standard:latest" })
	const saved = "aether/member-missing:123"
	e.member.Image = saved
	if err := e.db.UpdateMemberImage(t.Context(), e.member.ID, saved); err != nil {
		t.Fatalf("UpdateMemberImage: %v", err)
	}
	_, err := e.sched.BuildEnvironmentPlan(t.Context(), nil, e.ws, e.member, harness.Profile{}, EnvironmentPurposeRun)
	if err == nil || !strings.Contains(err.Error(), "aether env reset") || !strings.Contains(err.Error(), saved) {
		t.Fatalf("missing image error = %v, want saved tag and reset command", err)
	}
}

// A harness's fixed launch requirement reaches the container and outranks
// a workspace variable: claude refuses --dangerously-skip-permissions as
// root unless IS_SANDBOX declares the container a sandbox, so a workspace
// that unsets it would leave the agent unable to start. Only runs carry a
// harness; the environment terminal passes an empty profile and gets none
// of this.
func TestBuildEnvironmentPlanAppliesHarnessLaunchEnv(t *testing.T) {
	t.Parallel()
	s := &Scheduler{cfg: Config{StandardImage: "standard:latest"}}
	ws := &domain.Workspace{ID: "ws", Environment: domain.WorkspaceEnvironment{
		Variables: map[string]string{"IS_SANDBOX": "0"},
	}}
	claude, ok := harness.Lookup("claude")
	if !ok {
		t.Fatal("claude is not in the registry")
	}
	plan, err := s.BuildEnvironmentPlan(context.Background(), nil, ws, &domain.Member{ID: "member"}, claude, EnvironmentPurposeRun)
	if err != nil {
		t.Fatalf("BuildEnvironmentPlan: %v", err)
	}
	if plan.Env["IS_SANDBOX"] != "1" {
		t.Fatalf("IS_SANDBOX = %q, want 1", plan.Env["IS_SANDBOX"])
	}
}

func TestApplyInstallEnvironmentKeepsExplicitPrefixes(t *testing.T) {
	t.Parallel()
	env := map[string]string{}
	applyInstallEnvironment(env, "/home/dev")
	if env["NPM_CONFIG_PREFIX"] != "/home/dev/.local" || env["CARGO_INSTALL_ROOT"] != "/home/dev/.local" {
		t.Fatalf("defaults = %v, want both under /home/dev/.local", env)
	}
	env = map[string]string{"npm_config_prefix": "/opt/npm", "CARGO_INSTALL_ROOT": "/opt/cargo"}
	applyInstallEnvironment(env, "/home/dev")
	if _, added := env["NPM_CONFIG_PREFIX"]; added || env["npm_config_prefix"] != "/opt/npm" || env["CARGO_INSTALL_ROOT"] != "/opt/cargo" {
		t.Fatalf("explicit settings = %v, want them unchanged", env)
	}
}
