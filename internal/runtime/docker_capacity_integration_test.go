//go:build integration && linux

package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/disk"
	"github.com/moby/moby/client"
)

// This deliberately does not inspect or reconfigure the shared test daemon.
// Docker 29 autodetects the system containerd even with a different data-root,
// so both daemons are isolated explicitly. Root and installed daemon binaries
// are required for this scenario; missing binaries under root are failures.
func TestDockerCapacityContainerdIsolated(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("isolated dockerd/containerd capacity proof requires root; run this scenario with sudo")
	}
	for _, binary := range []string{"dockerd", "containerd"} {
		if _, err := exec.LookPath(binary); err != nil {
			t.Fatalf("isolated capacity integration requires %s: %v", binary, err)
		}
	}
	// Short paths also fit sockaddr_un when Go's test name is long.
	base, err := os.MkdirTemp("", "aether-capacity-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(base); err != nil {
			t.Errorf("remove isolated daemon state: %v", err)
		}
	})
	contentRoot := filepath.Join(base, "containerd-data")
	snapshotRoot := filepath.Join(base, "custom-snapshots")
	containerdSocket := filepath.Join(base, "containerd.sock")
	containerdConfig := filepath.Join(base, "containerd.toml")
	// root_path is the official overlayfs plugin override, not a guessed
	// containerd data-root sibling. Introspection must return this exact path.
	config := fmt.Sprintf(`version = 2
disabled_plugins = ["io.containerd.grpc.v1.cri", "io.containerd.cri.v1.images", "io.containerd.cri.v1.runtime"]
[plugins."io.containerd.snapshotter.v1.overlayfs"]
  root_path = %q
`, snapshotRoot)
	if err := os.WriteFile(containerdConfig, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	containerdDone := startCapacityDaemon(t, base, "containerd", "--config", containerdConfig, "--root", contentRoot, "--state", filepath.Join(base, "containerd-state"), "--address", containerdSocket)
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	for {
		if st, err := os.Stat(containerdSocket); err == nil && st.Mode()&os.ModeSocket != 0 {
			break
		}
		select {
		case <-containerdDone:
			t.Fatal("isolated containerd exited before creating its socket; see owned daemon log")
		case <-ctx.Done():
			t.Fatal("isolated containerd did not create its socket")
		case <-time.After(50 * time.Millisecond):
		}
	}
	dockerRoot := filepath.Join(base, "docker-data")
	dockerSocket := filepath.Join(base, "docker.sock")
	dockerConfig := filepath.Join(base, "daemon.json")
	cfg := map[string]any{
		"hosts":                []string{"unix://" + dockerSocket},
		"data-root":            dockerRoot,
		"exec-root":            filepath.Join(base, "docker-exec"),
		"pidfile":              filepath.Join(base, "dockerd.pid"),
		"containerd":           containerdSocket,
		"containerd-namespace": "aether-capacity",
		"features":             map[string]bool{"containerd-snapshotter": true},
		"storage-driver":       "overlayfs",
		"bridge":               "none", "iptables": false, "ip6tables": false,
		"ip-forward": false, "ip-masq": false, "userland-proxy": false,
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dockerConfig, data, 0o600); err != nil {
		t.Fatal(err)
	}
	// Docker 28/29 register the plural CLI flag but use a singular JSON tag;
	// the singular JSON key fails validation and the plural key is ignored.
	dockerDone := startCapacityDaemon(t, base, "dockerd", "--config-file", dockerConfig, "--containerd-plugins-namespace", "aether-capacity-plugins")
	cli, err := client.New(client.WithHost("unix://" + dockerSocket))
	if err != nil {
		t.Fatal(err)
	}
	d := &Docker{cli: cli, waitClient: cli, networkMode: "none"}
	t.Cleanup(func() { _ = d.Close() })
	for {
		probe, stop := context.WithTimeout(ctx, time.Second)
		_, err = cli.Ping(probe, client.PingOptions{})
		stop()
		if err == nil {
			break
		}
		select {
		case <-containerdDone:
			t.Fatal("isolated containerd exited while waiting for Docker; see owned daemon log")
		case <-dockerDone:
			t.Fatal("isolated dockerd exited before readiness; see owned daemon log")
		case <-ctx.Done():
			t.Fatalf("isolated Docker did not become ready: %v", err)
		case <-time.After(50 * time.Millisecond):
		}
	}
	info, err := cli.Info(ctx, client.InfoOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if info.Info.Containerd == nil || info.Info.Containerd.Address == "" {
		t.Fatalf("Docker %s API %s does not expose Containerd.Address; the isolated capacity proof requires Docker info API >= 1.46 with a configured containerd address", info.Info.ServerVersion, cli.ClientVersion())
	}
	if info.Info.Driver != "overlayfs" || info.Info.Containerd.Address != containerdSocket {
		t.Fatalf("not using isolated containerd image store: %+v", info.Info)
	}
	if info.Info.Containerd.Namespaces.Containers != "aether-capacity" || info.Info.Containerd.Namespaces.Plugins != "aether-capacity-plugins" {
		t.Fatalf("not using isolated containerd namespaces: %+v", info.Info.Containerd.Namespaces)
	}
	roots, err := dockerFilesystemRoots(ctx, cli.DaemonHost(), info.Info)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{dockerRoot, filepath.Join(contentRoot, "io.containerd.content.v1.content"), snapshotRoot}
	if len(roots) != len(want) {
		t.Fatalf("did not discover all actual roots: %+v", roots)
	}
	capacity, err := d.Capacity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(capacity.Filesystems) != len(want) || capacity.MemoryTotalBytes == 0 || capacity.MemoryAvailableBytes > capacity.MemoryTotalBytes {
		t.Fatalf("invalid capacity: %+v", capacity)
	}
	for i, root := range roots {
		if root.path != want[i] {
			t.Fatalf("root %s = %s, want actual configured root %s", root.name, root.path, want[i])
		}
		fs, err := disk.Filesystem(root.path)
		if err != nil {
			t.Fatal(err)
		}
		got := capacity.Filesystems[i]
		if got.Name != root.name || got.TotalBytes != fs.TotalBytes || got.FreeBytes == 0 || got.FreeBytes > got.TotalBytes {
			t.Fatalf("capacity %s = %+v, direct filesystem = %+v", root.name, got, fs)
		}
		// Other tests/daemon metadata may write between samples, so compare
		// available bytes with a small tolerance, not exact temporal equality.
		delta := int64(got.FreeBytes) - int64(fs.FreeBytes)
		if delta < -(64<<20) || delta > 64<<20 {
			t.Fatalf("free space for %s differs from its actual filesystem by %d", root.name, delta)
		}
		t.Logf("verified %s root=%s total=%d free=%d", root.name, root.path, got.TotalBytes, got.FreeBytes)
	}
}

func startCapacityDaemon(t *testing.T, base, binary string, args ...string) <-chan struct{} {
	t.Helper()
	logPath := filepath.Join(base, binary+".log")
	log, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, args...)
	cmd.Stdout, cmd.Stderr = log, log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Never notify or consume sockets from the host's systemd service.
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if key != "NOTIFY_SOCKET" && key != "LISTEN_PID" && key != "LISTEN_FDS" && key != "LISTEN_FDNAMES" {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	if err := cmd.Start(); err != nil {
		_ = log.Close()
		t.Fatalf("start isolated %s: %v", binary, err)
	}
	done := make(chan struct{})
	var waitErr error
	go func() { waitErr = cmd.Wait(); close(done) }()
	t.Cleanup(func() {
		select {
		case <-done:
			t.Errorf("isolated %s exited unexpectedly: %v", binary, waitErr)
		default:
			_ = cmd.Process.Signal(syscall.SIGTERM)
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Errorf("isolated %s did not exit after SIGKILL", binary)
				}
				t.Errorf("isolated %s needed forced shutdown", binary)
			}
		}
		_ = log.Close()
		if t.Failed() {
			if data, err := os.ReadFile(logPath); err == nil {
				t.Logf("%s log:\n%s", binary, data)
			}
		}
	})
	return done
}
