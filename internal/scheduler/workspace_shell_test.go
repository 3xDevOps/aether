package scheduler

import (
	"bytes"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/store"
	"github.com/3xDevOps/Aether/internal/toolenv"
)

// drainPipe discards everything the shell writes to the client half of a
// net.Pipe. A real SSH channel buffers writes; net.Pipe does not, so an
// undrained pipe would deadlock the shell's provisioning notice.
func drainPipe(c net.Conn) {
	go func() { _, _ = io.Copy(io.Discard, c) }()
}

func TestWorkspaceShellRequiresWorkspaceSelector(t *testing.T) {
	e := newTestEnv(t, func(c *Config) {
		mgr, err := toolenv.NewManager(filepath.Join(t.TempDir(), "tools"), c.Store)
		if err != nil {
			t.Fatal(err)
		}
		c.Toolenv = mgr
		c.NeutralImage = "busybox:1.36"
	})
	err := e.sched.WorkspaceShell(t.Context(), e.member.ID, domain.WorkspaceShellRequest{Mode: domain.WorkspaceShellBootstrapTools}, 80, 24, &bytes.Buffer{}, nil)
	if err == nil {
		t.Fatal("missing workspace selector accepted")
	}
}

func TestWorkspaceShellBootstrapUsesWritableStaging(t *testing.T) {
	e := newTestEnv(t, func(c *Config) {
		mgr, err := toolenv.NewManager(filepath.Join(t.TempDir(), "tools"), c.Store)
		if err != nil {
			t.Fatal(err)
		}
		c.Toolenv = mgr
	})
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	drainPipe(client)
	done := make(chan error, 1)
	go func() {
		done <- e.sched.WorkspaceShell(t.Context(), e.member.ID, domain.WorkspaceShellRequest{
			Workspace: domain.WorkspaceSelector{ID: e.ws.ID},
			Mode:      domain.WorkspaceShellBootstrapTools,
		}, 80, 24, server, nil)
	}()
	select {
	case err := <-done:
		t.Fatalf("workspace shell exited before container creation: %v", err)
	case <-time.After(2 * time.Second):
	}
	var containers []*fakeContainer
	waitFor(t, "workspace shell", func() bool {
		containers = e.rt.allContainers()
		return len(containers) > 0
	})
	for _, c := range containers {
		if len(c.spec.Mounts) == 0 || c.spec.Mounts[0].ReadOnly {
			t.Fatal("bootstrap staging is not writable")
		}
		if !c.startedWithAttach {
			t.Fatal("workspace shell started before attaching; the first prompt would be lost")
		}
		if c.spec.Env["PS1"] != "aether-bootstrap$ " {
			t.Fatalf("PS1 = %q, want the bootstrap prompt", c.spec.Env["PS1"])
		}
		c.exitNow(0)
	}
	_ = client.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.GetToolHead(t.Context(), e.member.ID, e.ws.ID); err != nil {
		t.Fatalf("bootstrap did not activate a snapshot: %v", err)
	}
}
func TestWorkspaceShellUsesPinnedSnapshotPathAfterHeadChange(t *testing.T) {
	e := newTestEnv(t, func(c *Config) {
		mgr, err := toolenv.NewManager(filepath.Join(t.TempDir(), "tools"), c.Store)
		if err != nil {
			t.Fatal(err)
		}
		c.Toolenv = mgr
	})
	mgr := e.sched.cfg.Toolenv
	stage1, err := mgr.CreateStaging(string(e.member.ID), string(e.ws.ID))
	if err != nil {
		t.Fatal(err)
	}
	if writeErr := os.WriteFile(filepath.Join(stage1, "one"), []byte("one"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	first, err := mgr.Promote(t.Context(), string(e.member.ID), string(e.ws.ID), stage1, domain.ToolManifest{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	stage2, err := mgr.CreateStaging(string(e.member.ID), string(e.ws.ID))
	if err != nil {
		t.Fatal(err)
	}
	if writeErr := os.WriteFile(filepath.Join(stage2, "two"), []byte("two"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	if _, promoteErr := mgr.Promote(t.Context(), string(e.member.ID), string(e.ws.ID), stage2, domain.ToolManifest{}, nil); promoteErr != nil {
		t.Fatal(promoteErr)
	}
	run := &domain.Run{
		ID: "pin-run", SessionID: e.sess.ID, MemberID: e.member.ID, Task: "pin tools",
		Harness: "fake", Mode: domain.LaunchTUI, Status: domain.RunRunning,
	}
	if createRunErr := e.db.CreateRun(t.Context(), run); createRunErr != nil {
		t.Fatal(createRunErr)
	}
	plan, err := e.sched.BuildEnvironmentPlan(t.Context(), run, e.ws, e.member, harness.Profile{}, EnvironmentPurposeRun, "")
	if err != nil {
		t.Fatal(err)
	}
	if setHeadErr := e.db.SetToolHead(t.Context(), e.member.ID, e.ws.ID, first.ID); setHeadErr != nil {
		t.Fatal(setHeadErr)
	}
	pinned, err := e.db.GetRun(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	want, err := mgr.SnapshotPath(t.Context(), e.member.ID, e.ws.ID, pinned.ToolSnapshotID)
	if err != nil {
		t.Fatal(err)
	}
	if plan.ToolHostPath != want || pinned.ToolSnapshotID == first.ID {
		t.Fatalf("tool mount = %q, pinned run = %q, want immutable path for pre-change head", plan.ToolHostPath, pinned.ToolSnapshotID)
	}
}

func TestWorkspaceShellResumeRequiresExplicitResumeAndCleansSelectedPendingRow(t *testing.T) {
	e := newTestEnv(t, func(c *Config) {
		mgr, err := toolenv.NewManager(filepath.Join(t.TempDir(), "tools"), c.Store)
		if err != nil {
			t.Fatal(err)
		}
		c.Toolenv = mgr
	})
	oldStaging, err := e.cfg.Toolenv.CreateStaging(string(e.member.ID), string(e.ws.ID))
	if err != nil {
		t.Fatal(err)
	}
	pending := &store.PendingWorkspaceShell{WorkspaceID: e.ws.ID, MemberID: e.member.ID, StagingID: filepath.Base(oldStaging)}
	if createPendingErr := e.db.CreatePendingWorkspaceShell(t.Context(), pending); createPendingErr != nil {
		t.Fatal(createPendingErr)
	}
	client, server := net.Pipe()
	drainPipe(client)
	done := make(chan error, 1)
	go func() {
		done <- e.sched.WorkspaceShell(t.Context(), e.member.ID, domain.WorkspaceShellRequest{
			Workspace: domain.WorkspaceSelector{ID: e.ws.ID},
			Mode:      domain.WorkspaceShellBootstrapTools,
		}, 80, 24, server, nil)
	}()
	waitFor(t, "fresh bootstrap staging", func() bool { return len(e.rt.allContainers()) == 1 })
	container := e.rt.allContainers()[0]
	if len(container.spec.Mounts) == 0 || container.spec.Mounts[0].HostPath == oldStaging {
		t.Fatal("bootstrap reused pending staging without Resume=true")
	}
	_ = client.Close()
	if doneErr := <-done; doneErr != nil {
		t.Fatal(doneErr)
	}
	remaining, err := e.db.ListPendingWorkspaceShells(t.Context(), e.member.ID, e.ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 2 {
		t.Fatalf("pending rows after non-resume disconnect = %d, want 2", len(remaining))
	}
}

func TestWorkspaceShellResumeDeletesPendingRowAfterTeardownAndPromotion(t *testing.T) {
	e := newTestEnv(t, func(c *Config) {
		mgr, err := toolenv.NewManager(filepath.Join(t.TempDir(), "tools"), c.Store)
		if err != nil {
			t.Fatal(err)
		}
		c.Toolenv = mgr
	})
	staging, err := e.cfg.Toolenv.CreateStaging(string(e.member.ID), string(e.ws.ID))
	if err != nil {
		t.Fatal(err)
	}
	pending := &store.PendingWorkspaceShell{WorkspaceID: e.ws.ID, MemberID: e.member.ID, StagingID: filepath.Base(staging)}
	if createPendingErr := e.db.CreatePendingWorkspaceShell(t.Context(), pending); createPendingErr != nil {
		t.Fatal(createPendingErr)
	}
	client, server := net.Pipe()
	drainPipe(client)
	done := make(chan error, 1)
	go func() {
		done <- e.sched.WorkspaceShell(t.Context(), e.member.ID, domain.WorkspaceShellRequest{
			Workspace: domain.WorkspaceSelector{ID: e.ws.ID},
			Mode:      domain.WorkspaceShellBootstrapTools,
			Resume:    true,
		}, 80, 24, server, nil)
	}()
	waitFor(t, "resumed bootstrap", func() bool { return len(e.rt.allContainers()) == 1 })
	container := e.rt.allContainers()[0]
	container.exitNow(0)
	_ = client.Close()
	if doneErr := <-done; doneErr != nil {
		t.Fatal(doneErr)
	}
	remaining, err := e.db.ListPendingWorkspaceShells(t.Context(), e.member.ID, e.ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 0 {
		t.Fatalf("pending rows after promotion = %d, want 0", len(remaining))
	}
	if len(e.rt.allContainers()) != 0 {
		t.Fatal("bootstrap container was not torn down before promotion")
	}
}

func TestWorkspaceShellLoginUsesConfiguredCustomHarnessCredentials(t *testing.T) {
	e := newTestEnv(t, func(c *Config) {
		c.HomesDir = filepath.Join(t.TempDir(), "homes")
		c.Harnesses = map[string]HarnessSpec{
			"custom-login": {
				TUIArgs:         []string{"custom-agent"},
				HeadlessArgs:    []string{"custom-agent"},
				Executable:      "custom-agent",
				ProfileRoot:     "/root/.custom",
				CredentialPaths: []string{"/root/.custom/auth"},
			},
		}
		c.PollInterval = time.Millisecond
	})
	client, server := net.Pipe()
	drainPipe(client)
	done := make(chan error, 1)
	go func() {
		done <- e.sched.WorkspaceShell(t.Context(), e.member.ID, domain.WorkspaceShellRequest{
			Workspace: domain.WorkspaceSelector{ID: e.ws.ID},
			Mode:      domain.WorkspaceShellHarnessLogin,
			Harness:   "custom-login",
		}, 80, 24, server, nil)
	}()
	waitFor(t, "custom harness login", func() bool { return len(e.rt.allContainers()) == 1 })
	container := e.rt.allContainers()[0]
	if len(container.spec.Mounts) == 0 || container.spec.Mounts[0].ContainerPath != "/root/.custom/auth" {
		t.Fatalf("custom login mounts = %+v, want scoped credential mount", container.spec.Mounts)
	}
	_ = client.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceShellStopsOnMemberRevocation(t *testing.T) {
	e := newTestEnv(t, func(c *Config) { c.PollInterval = time.Millisecond })
	client, server := net.Pipe()
	drainPipe(client)
	done := make(chan error, 1)
	go func() {
		done <- e.sched.WorkspaceShell(t.Context(), e.member.ID, domain.WorkspaceShellRequest{
			Workspace: domain.WorkspaceSelector{ID: e.ws.ID},
			Mode:      domain.WorkspaceShellHarnessLogin,
			Harness:   "claude",
		}, 80, 24, server, nil)
	}()
	waitFor(t, "live workspace shell", func() bool { return len(e.rt.allContainers()) == 1 })
	e.member.Role = domain.RoleViewer
	if err := e.db.UpdateMember(t.Context(), e.member); err != nil {
		t.Fatal(err)
	}
	_ = client.Close()
	if err := <-done; err == nil {
		t.Fatal("revoked workspace shell returned nil")
	}
	if len(e.rt.allContainers()) != 0 {
		t.Fatal("revoked workspace shell container remains")
	}
}
