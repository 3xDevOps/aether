package scheduler

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
)

// TestNormalizeStagedSymlinks covers the vendor-installer layout that broke
// agent setup in the field: ~/.local/bin/claude as an absolute symlink into
// the container home, unresolvable from the host.
func TestNormalizeStagedSymlinks(t *testing.T) {
	staging := t.TempDir()
	versions := filepath.Join(staging, "share", "claude", "versions")
	if err := os.MkdirAll(versions, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(versions, "1.0"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(staging, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/root/.local/share/claude/versions/1.0", filepath.Join(staging, "bin", "claude")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/home/aether/.local/share/claude/versions/1.0", filepath.Join(staging, "bin", "other-home")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(staging, "bin", "outside")); err != nil {
		t.Fatal(err)
	}
	if err := normalizeStagedSymlinks(staging); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"claude", "other-home"} {
		target, err := os.Readlink(filepath.Join(staging, "bin", name))
		if err != nil {
			t.Fatal(err)
		}
		if filepath.IsAbs(target) {
			t.Fatalf("bin/%s still absolute: %q", name, target)
		}
		if _, err := os.Stat(filepath.Join(staging, "bin", name)); err != nil {
			t.Fatalf("bin/%s does not resolve on the host: %v", name, err)
		}
	}
	// A link outside ~/.local is not rewritten (and never followed here).
	target, err := os.Readlink(filepath.Join(staging, "bin", "outside"))
	if err != nil || target != "/etc/passwd" {
		t.Fatalf("outside link changed: %q %v", target, err)
	}
	if err := verifyStagedExecutable(staging, "claude"); err != nil {
		t.Fatalf("verify normalized executable: %v", err)
	}
}

func TestVerifyStagedExecutableFailuresNameContainerPaths(t *testing.T) {
	staging := t.TempDir()
	if err := os.MkdirAll(filepath.Join(staging, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := verifyStagedExecutable(staging, "claude")
	if err == nil {
		t.Fatal("missing executable verified")
	}
	if strings.Contains(err.Error(), staging) {
		t.Fatalf("error leaks the host staging path: %v", err)
	}
	if !strings.Contains(err.Error(), "~/.local/bin/claude") {
		t.Fatalf("error does not name the container path: %v", err)
	}
	// A symlink chain escaping the tree fails instead of statting host files.
	if err := os.Symlink("../../../../../../bin/sh", filepath.Join(staging, "bin", "claude")); err != nil {
		t.Fatal(err)
	}
	if err := verifyStagedExecutable(staging, "claude"); err == nil {
		t.Fatal("escaping symlink verified")
	}
}

func TestAgentSetupCommandInstallsThenFallsBackToShell(t *testing.T) {
	command := agentSetupCommand("claude", "curl -fsSL https://example.invalid | bash", "claude")
	if command[0] != "/bin/sh" || command[1] != "-c" {
		t.Fatalf("command = %v", command)
	}
	script := command[2]
	for _, want := range []string{`[ ! -x "$HOME/.local/bin/claude" ]`, "exec /bin/sh -i", "install failed"} {
		if !strings.Contains(script, want) {
			t.Fatalf("script missing %q: %s", want, script)
		}
	}
	// No install script keeps the plain interactive shell.
	plain := agentSetupCommand("omp", "", "omp")
	if len(plain) != 2 || plain[0] != "/bin/sh" || plain[1] != "-i" {
		t.Fatalf("plain command = %v", plain)
	}
}

func TestStagedExecutablePrefersProfileArgv(t *testing.T) {
	profile, _ := harness.Lookup("claude")
	if got := stagedExecutable(domain.WorkspaceShellRequest{Harness: "claude"}, profile); got != "claude" {
		t.Fatalf("staged executable = %q", got)
	}
	if got := stagedExecutable(domain.WorkspaceShellRequest{Harness: "omp"}, harness.Profile{}); got != "omp" {
		t.Fatalf("staged executable without profile argv = %q", got)
	}
}

// TestPublicRunStatusReasonKeepsTheCause pins the fix for the field failure
// where the informative tail of a long Docker error ("executable file not
// found in $PATH") was cut off and only the generic prefix survived.
func TestPublicRunStatusReasonKeepsTheCause(t *testing.T) {
	long := "start container: " + strings.Repeat("failed to create task: ", 20) +
		`exec: "claude": executable file not found in $PATH`
	got := publicRunStatusReason(long)
	if len([]rune(got)) > maxPublicRunStatusReason {
		t.Fatalf("reason exceeds cap: %d", len([]rune(got)))
	}
	if !strings.Contains(got, "executable file not found in $PATH") {
		t.Fatalf("cause lost: %q", got)
	}
	if !strings.HasPrefix(got, "start container: ") {
		t.Fatalf("step lost: %q", got)
	}
}
