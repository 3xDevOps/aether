//go:build integration

package scheduler

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestIntegrationSharedAccountBorrowsInstallationDocker launches claude for
// Ada, who has none installed, on Grace's shared account. The run executes
// Grace's claude, installed as the native installer does, through her
// ~/.local/bin mounted read-only, and Grace's home reaches the container only
// at her login and her read-only installation.
func TestIntegrationSharedAccountBorrowsInstallationDocker(t *testing.T) {
	e, docker, cli, owner := newShareDockerEnv(t, []string{"claude"})
	ownerHome, err := e.cfg.Homes.Path(owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	seedHome(t, ownerHome, map[string]string{
		".claude/.credentials.json":             "grace-login",
		".local/lib/node_modules/tool/index.js": "grace-lib",
		".local/share/opencode/auth.json":       "grace-opencode-login",
		".ssh/id_ed25519":                       "grace-ssh-key",
	})
	const version = ".local/share/claude/versions/1/claude"
	seedHome(t, ownerHome, map[string]string{version: "#!/bin/sh\n" +
		`sleep 1; printf 'harness-login:%s\n' "$(cat "$HOME/.claude/.credentials.json")"; printf 'harness-path:%s\n' "$0"; sleep 1` + "\n"})
	if err = os.Chmod(filepath.Join(ownerHome, version), 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(ownerHome, ".local", "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink("/root/"+version, filepath.Join(ownerHome, ".local", "bin", "claude")); err != nil {
		t.Fatal(err)
	}

	shared := launchShareRun(t, e, docker, cli, e.member.ID, owner.ID, "borrowed install", "grace-login")
	if out := e.pty.session(shared.run.ID).output(); !strings.Contains(out, "harness-path:/root/.aether/account/bin/claude") {
		t.Fatalf("the run did not execute Grace's claude through the borrowed bin; pty output = %q", out)
	}
	code, table := shared.sh(t, "cat /proc/self/mountinfo")
	if code != 0 {
		t.Fatalf("read mountinfo: exit %d, %q", code, table)
	}
	want := map[string]bool{
		"/root/.claude/.credentials.json": false,
		"/root/.aether/account/bin":       true,
		"/root/.aether/account/lib":       true,
		"/root/.local/share/claude":       true,
	}
	marker := "/homes/" + filepath.Base(ownerHome) + "/"
	seen := map[string]bool{}
	for _, line := range strings.Split(table, "\n") {
		f := strings.Fields(line)
		if len(f) < 6 || !strings.Contains(f[3]+"/", marker) {
			continue
		}
		t.Logf("mountinfo: %s at %s (%s)", f[3], f[4], f[5])
		readOnly, ok := want[f[4]]
		if !ok {
			t.Fatalf("Grace's home is mounted at %s, outside her login and installation", f[4])
		}
		if readOnly != strings.HasPrefix(f[5]+",", "ro,") {
			t.Fatalf("%s mount options %q, want read-only %v", f[4], f[5], readOnly)
		}
		seen[f[4]] = true
	}
	if len(seen) != len(want) {
		t.Fatalf("Grace's home is mounted at %v, want %v", seen, want)
	}
	if code, out := shared.sh(t, "touch /root/.aether/account/bin/planted"); code == 0 {
		t.Fatalf("writing into Grace's borrowed bin succeeded: %q", out)
	}
	if code, out := shared.sh(t, "grep -r -l -e grace-opencode-login -e grace-ssh-key /root"); code != 1 {
		t.Fatalf("grep for Grace's other files under /root exited %d: %s", code, out)
	}
	shared.close(t, e)
}
