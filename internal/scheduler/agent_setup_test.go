package scheduler

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/store"
	"github.com/3xDevOps/Aether/internal/toolenv"
)

func agentSetupEnv(t *testing.T) *testEnv {
	t.Helper()
	homes := filepath.Join(t.TempDir(), "homes")
	return newTestEnv(t, func(c *Config) {
		mgr, err := toolenv.NewManager(filepath.Join(t.TempDir(), "tools"), c.Store)
		if err != nil {
			t.Fatal(err)
		}
		c.Toolenv = mgr
		c.HomesDir = homes
	})
}

func runAgentSetupShell(t *testing.T, e *testEnv, req domain.WorkspaceShellRequest, beforeExit func(c *fakeContainer), exitCode int) error {
	t.Helper()
	before := len(e.rt.allContainers())
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	drainPipe(client)
	done := make(chan error, 1)
	go func() {
		done <- e.sched.WorkspaceShell(t.Context(), e.member.ID, req, 80, 24, server, nil)
	}()
	waitFor(t, "agent setup container", func() bool { return len(e.rt.allContainers()) > before })
	containers := e.rt.allContainers()
	c := containers[len(containers)-1]
	if beforeExit != nil {
		beforeExit(c)
	}
	c.exitNow(exitCode)
	select {
	case err := <-done:
		return err
	case <-time.After(waitTimeout):
		t.Fatal("workspace shell did not finish")
		return nil
	}
}

// The one-step onboarding contract: a custom agent-setup shell mounts the
// member's harness home writable at $HOME plus tool staging at ~/.local,
// and a clean exit snapshots tools, discovers what login wrote into home,
// and registers the member definition with those credential paths.
func TestAgentSetupShellRegistersDefinition(t *testing.T) {
	e := agentSetupEnv(t)
	hostHome := filepath.Join(e.cfg.HomesDir, string(e.member.ID), "omp")
	req := domain.WorkspaceShellRequest{
		Workspace:    domain.WorkspaceSelector{ID: e.ws.ID},
		Mode:         domain.WorkspaceShellAgentSetup,
		Harness:      "omp",
		TUIArgs:      []string{"omp", "{task}"},
		HeadlessArgs: []string{"omp", "-p", "{task}"},
	}
	err := runAgentSetupShell(t, e, req, func(c *fakeContainer) {
		if len(c.spec.Mounts) < 2 {
			t.Fatalf("mounts = %+v, want home then staging", c.spec.Mounts)
		}
		home, staging := c.spec.Mounts[0], c.spec.Mounts[1]
		if home.HostPath != hostHome || home.ContainerPath != "/root" || home.ReadOnly {
			t.Fatalf("home mount = %+v, want writable %s at /root", home, hostHome)
		}
		if staging.ContainerPath != "/root/.local" || staging.ReadOnly {
			t.Fatalf("staging mount = %+v, want writable /root/.local", staging)
		}
		if c.spec.Env["PS1"] != "aether-agent-setup$ " {
			t.Fatalf("PS1 = %q", c.spec.Env["PS1"])
		}
		// Simulate the member's session: install the executable into tool
		// staging and let the vendor login write state into home.
		if mkErr := os.MkdirAll(filepath.Join(staging.HostPath, "bin"), 0o755); mkErr != nil {
			t.Fatal(mkErr)
		}
		if wrErr := os.WriteFile(filepath.Join(staging.HostPath, "bin", "omp"), []byte("#!/bin/sh\n"), 0o755); wrErr != nil {
			t.Fatal(wrErr)
		}
		if mkErr := os.MkdirAll(filepath.Join(hostHome, ".omp"), 0o700); mkErr != nil {
			t.Fatal(mkErr)
		}
		if wrErr := os.WriteFile(filepath.Join(hostHome, ".omp", "auth.json"), []byte("{}"), 0o600); wrErr != nil {
			t.Fatal(wrErr)
		}
		// Noise that must never become a credential path.
		if mkErr := os.MkdirAll(filepath.Join(hostHome, ".cache"), 0o700); mkErr != nil {
			t.Fatal(mkErr)
		}
		if wrErr := os.WriteFile(filepath.Join(hostHome, ".ash_history"), []byte("omp\n"), 0o600); wrErr != nil {
			t.Fatal(wrErr)
		}
	}, 0)
	if err != nil {
		t.Fatalf("agent setup shell: %v", err)
	}
	if _, headErr := e.db.GetToolHead(t.Context(), e.member.ID, e.ws.ID); headErr != nil {
		t.Fatalf("tools were not snapshotted: %v", headErr)
	}
	row, err := e.db.GetHarnessDefinition(t.Context(), e.member.ID, "omp")
	if err != nil {
		t.Fatalf("definition was not registered: %v", err)
	}
	var def harness.Definition
	if unmarshalErr := json.Unmarshal(row.Definition, &def); unmarshalErr != nil {
		t.Fatalf("stored definition: %v", unmarshalErr)
	}
	if len(def.CredentialPaths) != 1 || def.CredentialPaths[0] != "/root/.omp" {
		t.Fatalf("credential paths = %v, want exactly /root/.omp", def.CredentialPaths)
	}
	if def.ProfileRoot != "/root/.omp" {
		t.Fatalf("profile root = %q", def.ProfileRoot)
	}
	if def.Executable != "omp" || len(def.TUIArgs) != 2 {
		t.Fatalf("definition = %+v", def)
	}

	// The registered agent must now resolve for real launches.
	argv, _, err := e.sched.command(t.Context(), e.member.ID, "omp", domain.LaunchTUI, "hi")
	if err != nil {
		t.Fatalf("registered agent does not resolve: %v", err)
	}
	if argv[0] != "omp" || argv[1] != "hi" {
		t.Fatalf("argv = %v", argv)
	}
}

// A shipped name combines bootstrap and login but never stores a member
// definition: the shipped profile stays the single source of truth.
func TestAgentSetupShellShippedNameStoresNothing(t *testing.T) {
	e := agentSetupEnv(t)
	req := domain.WorkspaceShellRequest{
		Workspace: domain.WorkspaceSelector{ID: e.ws.ID},
		Mode:      domain.WorkspaceShellAgentSetup,
		Harness:   "claude",
	}
	err := runAgentSetupShell(t, e, req, func(c *fakeContainer) {
		staging := c.spec.Mounts[1]
		if mkErr := os.MkdirAll(filepath.Join(staging.HostPath, "bin"), 0o755); mkErr != nil {
			t.Fatal(mkErr)
		}
		if wrErr := os.WriteFile(filepath.Join(staging.HostPath, "bin", "claude"), []byte("#!/bin/sh\n"), 0o755); wrErr != nil {
			t.Fatal(wrErr)
		}
	}, 0)
	if err != nil {
		t.Fatalf("agent setup shell: %v", err)
	}
	if _, err := e.db.GetHarnessDefinition(t.Context(), e.member.ID, "claude"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("shipped name stored a member definition (err=%v)", err)
	}
}

// A symlink planted in the home during setup must abort registration: at
// run time it would be resolved on the host and could alias another
// member's credentials.
func TestAgentSetupShellRejectsSymlinkLoginState(t *testing.T) {
	e := agentSetupEnv(t)
	hostHome := filepath.Join(e.cfg.HomesDir, string(e.member.ID), "omp")
	req := domain.WorkspaceShellRequest{
		Workspace: domain.WorkspaceSelector{ID: e.ws.ID},
		Mode:      domain.WorkspaceShellAgentSetup,
		Harness:   "omp",
	}
	err := runAgentSetupShell(t, e, req, func(c *fakeContainer) {
		staging := c.spec.Mounts[1]
		if mkErr := os.MkdirAll(filepath.Join(staging.HostPath, "bin"), 0o755); mkErr != nil {
			t.Fatal(mkErr)
		}
		if wrErr := os.WriteFile(filepath.Join(staging.HostPath, "bin", "omp"), []byte("#!/bin/sh\n"), 0o755); wrErr != nil {
			t.Fatal(wrErr)
		}
		if lnErr := os.Symlink("../../evil", filepath.Join(hostHome, ".loot")); lnErr != nil {
			t.Fatal(lnErr)
		}
	}, 0)
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("err = %v, want a symlink rejection", err)
	}
	if _, defErr := e.db.GetHarnessDefinition(t.Context(), e.member.ID, "omp"); !errors.Is(defErr, store.ErrNotFound) {
		t.Fatalf("symlinked home registered a definition (err=%v)", defErr)
	}
}

// Adding a second agent must not evict the first agent's tools: fresh
// staging is seeded from the active snapshot before the session starts.
func TestAgentSetupShellAccumulatesTools(t *testing.T) {
	e := agentSetupEnv(t)
	install := func(name string) func(c *fakeContainer) {
		return func(c *fakeContainer) {
			staging := c.spec.Mounts[1]
			if mkErr := os.MkdirAll(filepath.Join(staging.HostPath, "bin"), 0o755); mkErr != nil {
				t.Fatal(mkErr)
			}
			if wrErr := os.WriteFile(filepath.Join(staging.HostPath, "bin", name), []byte("#!/bin/sh\n"), 0o755); wrErr != nil {
				t.Fatal(wrErr)
			}
		}
	}
	first := domain.WorkspaceShellRequest{
		Workspace: domain.WorkspaceSelector{ID: e.ws.ID},
		Mode:      domain.WorkspaceShellAgentSetup,
		Harness:   "agenta",
	}
	if err := runAgentSetupShell(t, e, first, install("agenta"), 0); err != nil {
		t.Fatalf("first agent setup: %v", err)
	}
	second := first
	second.Harness = "agentb"
	if err := runAgentSetupShell(t, e, second, install("agentb"), 0); err != nil {
		t.Fatalf("second agent setup: %v", err)
	}
	active, err := e.cfg.Toolenv.ActivePath(t.Context(), e.member.ID, e.ws.ID)
	if err != nil {
		t.Fatalf("active snapshot: %v", err)
	}
	for _, name := range []string{"agenta", "agentb"} {
		if _, statErr := os.Stat(filepath.Join(active, "bin", name)); statErr != nil {
			t.Fatalf("active snapshot lost %s: %v", name, statErr)
		}
	}
}

// A dirty exit must register nothing and promote nothing.
func TestAgentSetupShellDirtyExitRegistersNothing(t *testing.T) {
	e := agentSetupEnv(t)
	req := domain.WorkspaceShellRequest{
		Workspace: domain.WorkspaceSelector{ID: e.ws.ID},
		Mode:      domain.WorkspaceShellAgentSetup,
		Harness:   "omp",
		TUIArgs:   []string{"omp", "{task}"},
	}
	if err := runAgentSetupShell(t, e, req, nil, 3); err == nil {
		t.Fatal("dirty exit reported success")
	}
	if _, err := e.db.GetHarnessDefinition(t.Context(), e.member.ID, "omp"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("dirty exit stored a definition (err=%v)", err)
	}
	if _, err := e.db.GetToolHead(t.Context(), e.member.ID, e.ws.ID); err == nil {
		t.Fatal("dirty exit promoted a tool snapshot")
	}
}

// Reserved scheduler-internal names must be refused up front.
func TestAgentSetupShellRejectsReservedNames(t *testing.T) {
	e := agentSetupEnv(t)
	for _, name := range []string{"custom", "fake"} {
		err := e.sched.WorkspaceShell(t.Context(), e.member.ID, domain.WorkspaceShellRequest{
			Workspace: domain.WorkspaceSelector{ID: e.ws.ID},
			Mode:      domain.WorkspaceShellAgentSetup,
			Harness:   name,
		}, 80, 24, &noopConn{}, nil)
		if err == nil {
			t.Fatalf("reserved name %q accepted", name)
		}
	}
}

type noopConn struct{}

func (noopConn) Read(p []byte) (int, error)  { return 0, nil }
func (noopConn) Write(p []byte) (int, error) { return len(p), nil }
