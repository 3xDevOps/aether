package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/disk"
	introspection "github.com/containerd/containerd/api/services/introspection/v1"
	"github.com/moby/moby/api/types/system"
	"github.com/moby/moby/client"
	rpcstatus "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type capacityIntrospection struct {
	introspection.UnimplementedIntrospectionServer
	plugin func(context.Context, *introspection.PluginInfoRequest) (*introspection.PluginInfoResponse, error)
}

func (s *capacityIntrospection) PluginInfo(ctx context.Context, req *introspection.PluginInfoRequest) (*introspection.PluginInfoResponse, error) {
	return s.plugin(ctx, req)
}

func capacitySocketPath(t *testing.T, name string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "aether-cap-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	return filepath.Join(dir, name)
}

func introspectionSocket(t *testing.T, fn func(context.Context, *introspection.PluginInfoRequest) (*introspection.PluginInfoResponse, error)) string {
	t.Helper()
	socket := capacitySocketPath(t, "containerd.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	introspection.RegisterIntrospectionServer(server, &capacityIntrospection{plugin: fn})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	return socket
}

func localCapacityInfo(t *testing.T) system.Info {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "engine-id"), []byte("engine-identity\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "overlay2"), 0o700); err != nil {
		t.Fatal(err)
	}
	return system.Info{
		ID: "engine-identity", DockerRootDir: root, Driver: "overlay2",
		MemoryLimit: true, SwapLimit: true, CPUCfsPeriod: true, CPUCfsQuota: true, PidsLimit: true,
	}
}

func capacityDocker(t *testing.T, info system.Info) *Docker {
	t.Helper()
	socket := capacitySocketPath(t, "docker.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/info"):
			_ = json.NewEncoder(w).Encode(info)
		case strings.HasSuffix(r.URL.Path, "/system/df"):
			_, _ = io.WriteString(w, `{}`)
		default:
			t.Errorf("unexpected capacity request (no container stats allowed): %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	cli, err := client.New(client.WithHost("unix://"+socket), client.WithAPIVersion("1.52"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	return &Docker{cli: cli}
}

func TestDockerCapacityClassicAndContainerdRoots(t *testing.T) {
	if goruntime.GOOS != "linux" {
		t.Skip("host MemAvailable is Linux-specific")
	}
	for _, driver := range []string{"overlay2", "vfs", "overlayfs", "native"} {
		t.Run(driver, func(t *testing.T) {
			info := localCapacityInfo(t)
			info.Driver = driver
			wantRoots := 2
			if driver == "vfs" {
				if err := os.MkdirAll(filepath.Join(info.DockerRootDir, "vfs", "dir"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			content, snapshot := t.TempDir(), t.TempDir()
			if driver == "overlayfs" || driver == "native" {
				wantRoots = 3
				info.DriverStatus = [][2]string{{"driver-type", "io.containerd.snapshotter.v1"}}
				info.Containerd = &system.ContainerdInfo{Address: introspectionSocket(t, func(_ context.Context, req *introspection.PluginInfoRequest) (*introspection.PluginInfoResponse, error) {
					root := content
					if req.Type == "io.containerd.snapshotter.v1" && req.ID == driver {
						root = snapshot
					} else if req.Type != "io.containerd.content.v1" || req.ID != "content" {
						t.Errorf("wrong plugin: %v", req)
					}
					return &introspection.PluginInfoResponse{Plugin: &introspection.Plugin{Type: req.Type, ID: req.ID, Exports: map[string]string{"root": root}}}, nil
				})}
			}
			d := capacityDocker(t, info)
			got, err := d.Capacity(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Filesystems) != wantRoots || got.MemoryTotalBytes == 0 || got.MemoryAvailableBytes > got.MemoryTotalBytes {
				t.Fatalf("capacity = %+v", got)
			}
			for _, fs := range got.Filesystems {
				if fs.Name == "" || strings.Contains(fs.Name, "/") || fs.TotalBytes == 0 || fs.FreeBytes > fs.TotalBytes {
					t.Fatalf("filesystem = %+v", fs)
				}
			}
			roots, err := dockerFilesystemRoots(t.Context(), d.cli.DaemonHost(), info)
			if err != nil {
				t.Fatal(err)
			}
			if wantRoots == 3 && (roots[1].path != content || roots[2].path != snapshot) {
				t.Fatalf("did not use effective plugin root overrides: %+v", roots)
			}
			if wantRoots == 2 {
				want := filepath.Join(info.DockerRootDir, driver)
				if driver == "vfs" {
					want = filepath.Join(want, "dir")
				}
				if roots[1].path != want {
					t.Fatalf("did not use classic layer root: %+v, want %s", roots, want)
				}
			}
			u := d.StorageUsage(t.Context(), info.DockerRootDir)
			if u.TotalBytes == nil || u.SharedFilesystem == nil || !*u.SharedFilesystem {
				t.Fatalf("same-filesystem capacity not reported: %+v", u)
			}
		})
	}
}

func TestDockerCapacityRejectsUnavailableResourceControls(t *testing.T) {
	for _, tc := range []struct {
		name    string
		disable func(*system.Info)
	}{
		{"MemoryLimit", func(info *system.Info) { info.MemoryLimit = false }},
		{"SwapLimit", func(info *system.Info) { info.SwapLimit = false }},
		{"CpuCfsPeriod", func(info *system.Info) { info.CPUCfsPeriod = false }},
		{"CpuCfsQuota", func(info *system.Info) { info.CPUCfsQuota = false }},
		{"PidsLimit", func(info *system.Info) { info.PidsLimit = false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := localCapacityInfo(t)
			tc.disable(&info)
			d := capacityDocker(t, info)
			got, err := d.Capacity(t.Context())
			if err == nil || !strings.Contains(err.Error(), tc.name) || !strings.Contains(err.Error(), "cgroup hierarchy") {
				t.Fatalf("missing actionable unsupported %s error: %v", tc.name, err)
			}
			if len(got.Filesystems) != 0 || got.MemoryTotalBytes != 0 || got.MemoryAvailableBytes != 0 {
				t.Fatalf("unsupported resource controls permitted admission: %+v", got)
			}
			// Resource enforcement is an admission requirement, not a reason
			// to hide storage measurements from an administrator fixing it.
			usage := d.StorageUsage(t.Context(), info.DockerRootDir)
			if usage.TotalBytes == nil || usage.SharedFilesystem == nil || !*usage.SharedFilesystem {
				t.Fatalf("unsupported resource controls hid storage capacity: %+v", usage)
			}
		})
	}
}

func TestDockerCapacityContainerdRejectsUnknownRoots(t *testing.T) {
	for _, scenario := range []string{"missing export", "relative export", "absent directory", "wrong plugin", "plugin failed", "permission denied", "deadline", "unsupported snapshotter", "missing address", "non socket"} {
		t.Run(scenario, func(t *testing.T) {
			info := localCapacityInfo(t)
			info.Driver = "overlayfs"
			info.DriverStatus = [][2]string{{"driver-type", "io.containerd.snapshotter.v1"}}
			info.Containerd = &system.ContainerdInfo{Address: introspectionSocket(t, func(ctx context.Context, req *introspection.PluginInfoRequest) (*introspection.PluginInfoResponse, error) {
				p := &introspection.Plugin{Type: req.Type, ID: req.ID, Exports: map[string]string{"root": info.DockerRootDir}}
				switch scenario {
				case "missing export":
					p.Exports = nil
				case "relative export":
					p.Exports["root"] = "relative"
				case "absent directory":
					p.Exports["root"] = filepath.Join(info.DockerRootDir, "absent")
				case "wrong plugin":
					p.ID = "unrelated"
				case "plugin failed":
					p.InitErr = &rpcstatus.Status{Code: int32(codes.Internal), Message: "cannot initialize"}
				case "permission denied":
					return nil, status.Error(codes.PermissionDenied, "introspection access denied")
				case "deadline":
					<-ctx.Done()
					return nil, ctx.Err()
				}
				return &introspection.PluginInfoResponse{Plugin: p}, nil
			})}
			switch scenario {
			case "unsupported snapshotter":
				info.Driver = "devmapper"
			case "missing address":
				info.Containerd = nil
			case "non socket":
				info.Containerd.Address = filepath.Join(info.DockerRootDir, "engine-id")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancel()
			got, err := dockerFilesystemRoots(ctx, "unix:///docker.sock", info)
			if err == nil || got != nil {
				t.Fatalf("unknown capacity accepted: %+v, %v", got, err)
			}
		})
	}
}

func TestDockerCapacityIdentityBeforeIntrospection(t *testing.T) {
	for _, kind := range []string{"mismatch", "missing", "symlink", "oversize"} {
		t.Run(kind, func(t *testing.T) {
			info := localCapacityInfo(t)
			path := filepath.Join(info.DockerRootDir, "engine-id")
			switch kind {
			case "mismatch":
				info.ID = "remote-engine"
			case "missing":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Rename(path, path+"-target"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path+"-target", path); err != nil {
					t.Fatal(err)
				}
			case "oversize":
				if err := os.WriteFile(path, []byte(strings.Repeat("x", 4097)), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			got, err := capacityDocker(t, info).Capacity(t.Context())
			if err == nil || len(got.Filesystems) != 0 || got.MemoryTotalBytes != 0 || !strings.Contains(err.Error(), "identity") {
				t.Fatalf("unverified host measured: %+v, %v", got, err)
			}
		})
	}
}

func TestParseHostMemoryBoundaries(t *testing.T) {
	for _, tc := range []struct {
		input            string
		total, available uint64
		valid            bool
	}{
		{"MemTotal: 1024 kB\nMemAvailable: 512 kB\nSwapFree: 999999 kB\n", 1048576, 524288, true},
		{"MemTotal: 1 kB\nMemAvailable: 0 kB\n", 1024, 0, true},
		{"MemTotal: 1024 kB\nMemFree: 512 kB\n", 0, 0, false},
		{"MemTotal: 1 kB\nMemAvailable: 2 kB\n", 0, 0, false},
		{"MemTotal: 0 kB\nMemAvailable: 0 kB\n", 0, 0, false},
		{"MemTotal: -1 kB\nMemAvailable: 0 kB\n", 0, 0, false},
		{"MemTotal: 18446744073709551615 kB\nMemAvailable: 0 kB\n", 0, 0, false},
		{"MemTotal: 1 MB\nMemAvailable: 0 kB\n", 0, 0, false},
		{"MemTotal: 1 kB\nMemAvailable: 0 kB\nMemAvailable: 1 kB\n", 0, 0, false},
	} {
		total, available, err := parseHostMemory(strings.NewReader(tc.input))
		if (err == nil) != tc.valid || total != tc.total || available != tc.available {
			t.Errorf("parse %q = %d,%d,%v", tc.input, total, available, err)
		}
	}
	if _, _, err := parseHostMemory(failingMemoryReader{}); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("read error hidden: %v", err)
	}
}

type failingMemoryReader struct{}

func (failingMemoryReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestDockerCapacityClassicLayerDirectoryBoundaries(t *testing.T) {
	for _, driver := range []string{"overlay2", "vfs"} {
		for _, scenario := range []string{"missing home", "home file", "broken home symlink", "empty", "ready", "layer file", "broken layer symlink"} {
			t.Run(driver+"/"+scenario, func(t *testing.T) {
				info := localCapacityInfo(t)
				info.Driver = driver
				home := filepath.Join(info.DockerRootDir, driver)
				if err := os.MkdirAll(home, 0o700); err != nil {
					t.Fatal(err)
				}
				layer := home
				if driver == "vfs" {
					layer = filepath.Join(home, "dir")
				}
				target := layer
				if strings.Contains(scenario, "home") {
					target = home
				}
				switch scenario {
				case "missing home":
					if err := os.Remove(home); err != nil {
						t.Fatal(err)
					}
				case "home file", "layer file", "broken home symlink", "broken layer symlink":
					if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
						t.Fatal(err)
					}
					if strings.Contains(scenario, "symlink") {
						if err := os.Symlink(filepath.Join(info.DockerRootDir, "absent"), target); err != nil {
							t.Fatal(err)
						}
					} else if err := os.WriteFile(target, nil, 0o600); err != nil {
						t.Fatal(err)
					}
				case "ready":
					if err := os.MkdirAll(layer, 0o700); err != nil {
						t.Fatal(err)
					}
				}
				roots, err := dockerFilesystemRoots(t.Context(), "unix:///daemon.sock", info)
				if scenario != "empty" && scenario != "ready" {
					if err == nil || roots != nil {
						t.Fatalf("unverified layer root accepted: %+v, %v", roots, err)
					}
					usage := capacityDocker(t, info).StorageUsage(t.Context(), info.DockerRootDir)
					if usage.TotalBytes != nil || usage.FreeBytes != nil || usage.UsedBytes != nil || usage.SharedFilesystem != nil {
						t.Fatalf("unverified layers reported data-root capacity: %+v", usage)
					}
					return
				}
				want := layer
				if scenario == "empty" {
					want = home
				}
				if err != nil || len(roots) != 2 || roots[1].path != want {
					t.Fatalf("classic layer filesystem = %+v, %v; want %s", roots, err, want)
				}
				if driver == "vfs" && scenario == "empty" {
					if _, err := os.Lstat(layer); !os.IsNotExist(err) {
						t.Fatalf("capacity discovery mutated the lazy VFS layer directory: %v", err)
					}
				}
			})
		}
	}
}

func TestDockerCapacityAcrossFilesystemsHasNoAggregate(t *testing.T) {
	if goruntime.GOOS != "linux" {
		t.Skip("Linux separate-filesystem fixture")
	}
	// /dev/shm is an existing local tmpfs, never a test-created mount.
	if st, err := os.Stat("/dev/shm"); err != nil || !st.IsDir() {
		t.Skip("separate tmpfs fixture is unavailable")
	}
	for _, driver := range []string{"overlay2", "vfs", "overlayfs"} {
		t.Run(driver, func(t *testing.T) {
			info := localCapacityInfo(t)
			same, err := disk.SameFilesystem(info.DockerRootDir, "/dev/shm")
			if err != nil {
				t.Fatal(err)
			}
			if same {
				t.Skip("temporary directory and /dev/shm share a filesystem")
			}
			info.Driver = driver
			wantRoots := 2
			if driver == "overlayfs" {
				wantRoots = 3
				info.DriverStatus = [][2]string{{"driver-type", "io.containerd.snapshotter.v1"}}
				info.Containerd = &system.ContainerdInfo{Address: introspectionSocket(t, func(_ context.Context, req *introspection.PluginInfoRequest) (*introspection.PluginInfoResponse, error) {
					root := info.DockerRootDir
					if req.Type == "io.containerd.snapshotter.v1" {
						root = "/dev/shm"
					}
					return &introspection.PluginInfoResponse{Plugin: &introspection.Plugin{Type: req.Type, ID: req.ID, Exports: map[string]string{"root": root}}}, nil
				})}
			} else {
				layerRoot := filepath.Join(info.DockerRootDir, driver)
				if driver == "vfs" {
					if mkdirErr := os.Mkdir(layerRoot, 0o700); mkdirErr != nil {
						t.Fatal(mkdirErr)
					}
					layerRoot = filepath.Join(layerRoot, "dir")
				} else if removeErr := os.Remove(layerRoot); removeErr != nil {
					t.Fatal(removeErr)
				}
				// Only this test's symlink is created; no shared paths or
				// mounts are modified. Statfs must follow the layer root.
				if linkErr := os.Symlink("/dev/shm", layerRoot); linkErr != nil {
					t.Fatal(linkErr)
				}
			}
			d := capacityDocker(t, info)
			got, err := d.Capacity(t.Context())
			if err != nil || len(got.Filesystems) != wantRoots {
				t.Fatalf("separate filesystems lost: %+v, %v", got, err)
			}
			fs, err := disk.Filesystem("/dev/shm")
			if err != nil {
				t.Fatal(err)
			}
			layers := got.Filesystems[len(got.Filesystems)-1]
			if layers.TotalBytes != fs.TotalBytes || layers.FreeBytes > layers.TotalBytes {
				t.Fatalf("layer capacity did not probe its actual filesystem: %+v, want %+v", layers, fs)
			}
			roots, err := dockerFilesystemRoots(t.Context(), d.cli.DaemonHost(), info)
			if err != nil || roots[len(roots)-1].path != "/dev/shm" {
				t.Fatalf("layer root did not resolve to the separate filesystem: %+v, %v", roots, err)
			}
			u := d.StorageUsage(t.Context(), info.DockerRootDir)
			if u.TotalBytes != nil || u.FreeBytes != nil || u.UsedBytes != nil || u.SharedFilesystem != nil || !strings.Contains(u.Error, "multiple filesystems") {
				t.Fatalf("invented single filesystem total: %+v", u)
			}
		})
	}
}
