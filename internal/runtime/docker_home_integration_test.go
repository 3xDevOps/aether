//go:build integration

package runtime

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Installing any package that pulls in systemd runs its postinst, which runs
// systemd-tmpfiles --create. One container's root doing that must leave the
// home every container of the member mounts at /root to the member.
func TestDockerSystemdTmpfilesLeavesTheSharedHome(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("handing the home to uid 1000 requires root; run this scenario with sudo")
	}
	t.Parallel()
	d := newTestDocker(t)
	image := fmt.Sprintf("aether-it-systemd:%d", os.Getpid())
	build := exec.CommandContext(t.Context(), "docker", "build", "-t", image, "-")
	build.Stdin = strings.NewReader("FROM ubuntu:24.04\nRUN apt-get update && apt-get install -y --no-install-recommends systemd sudo && rm -rf /var/lib/apt/lists/*\n")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", image, err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "image", "rm", "-f", image).Run() })

	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "login"), []byte("signed in\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{home, filepath.Join(home, "login")} {
		if err := os.Chown(p, 1000, 1000); err != nil {
			t.Fatal(err)
		}
	}
	session := func(name string, sudo bool) ID {
		id := createContainer(t, d, Spec{
			Name:    fmt.Sprintf("it-home-%s-%d", name, time.Now().UnixNano()),
			Image:   image,
			User:    "1000:1000",
			Env:     map[string]string{"HOME": "/root"},
			Mounts:  []Mount{{HostPath: home, ContainerPath: "/root"}},
			Sudo:    sudo,
			Command: []string{"sleep", "infinity"},
		})
		if err := d.Start(t.Context(), id); err != nil {
			t.Fatalf("Start(%s) error: %v", name, err)
		}
		return id
	}
	root, other := session("root", true), session("other", false)

	code, out, _, err := d.execCombined(t.Context(), root, []string{"sudo", "systemd-tmpfiles", "--create"}, "", "1000:1000")
	if err != nil {
		t.Fatalf("systemd-tmpfiles: %v", err)
	}
	t.Logf("systemd-tmpfiles --create exited %d: %s", code, out)

	info, err := os.Stat(home)
	if err != nil {
		t.Fatal(err)
	}
	if st := info.Sys().(*syscall.Stat_t); st.Uid != 1000 || st.Gid != 1000 {
		t.Fatalf("home owner = %d:%d after systemd-tmpfiles in another container, want 1000:1000", st.Uid, st.Gid)
	}
	code, out, _, err = d.execCombined(t.Context(), other, []string{"cat", "/root/login"}, "", "1000:1000")
	if err != nil || code != 0 || out != "signed in\n" {
		t.Fatalf("other session reading its home: code %d, %q, %v", code, out, err)
	}

	// Naming the shipped file bypasses the mask, so the home must change:
	// otherwise the check above proves nothing.
	if _, out, _, err = d.execCombined(t.Context(), root, []string{"sudo", "systemd-tmpfiles", "--create", "/usr/lib/tmpfiles.d/provision.conf"}, "", "1000:1000"); err != nil {
		t.Fatalf("unmasked systemd-tmpfiles: %v: %s", err, out)
	}
	if info, err = os.Stat(home); err != nil {
		t.Fatal(err)
	}
	if st := info.Sys().(*syscall.Stat_t); st.Uid != 0 {
		t.Fatalf("home owner = %d:%d after the unmasked provision.conf, want uid 0: the scenario did not reach the home", st.Uid, st.Gid)
	}
}
