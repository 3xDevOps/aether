package runtime

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/disk"
	introspection "github.com/containerd/containerd/api/services/introspection/v1"
	"github.com/moby/moby/api/types/system"
	"github.com/moby/moby/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const dockerCapacityTimeout = 5 * time.Second

var _ CapacityRuntime = (*Docker)(nil)

type dockerStorageRoot struct {
	name string
	path string
}

// Capacity only measures the host after the connected engine's persisted ID
// matches the local data root. A Unix socket alone can be a remote forwarder.
// Managed admission also requires enforceable CPU, memory, swap and PID ceilings.
func (d *Docker) Capacity(ctx context.Context) (HostCapacity, error) {
	ctx, cancel := context.WithTimeout(ctx, dockerCapacityTimeout)
	defer cancel()
	info, err := d.cli.Info(ctx, client.InfoOptions{})
	if err != nil {
		return HostCapacity{}, errors.New(dockerStorageError("capacity info", err))
	}
	// Docker can discard unsupported memory, swap and PID limits with only a
	// create warning. Refuse admission before any container is provisioned.
	// SwapLimit describes cgroup enforcement, not whether swap is currently
	// enabled on the host; observing zero swap is not an enforcement guarantee.
	for _, capability := range [...]struct {
		supported bool
		name      string
	}{
		{info.Info.MemoryLimit, "memory limit (MemoryLimit)"},
		{info.Info.SwapLimit, "memory-plus-swap limit (SwapLimit)"},
		{info.Info.CPUCfsPeriod, "CPU CFS period (CpuCfsPeriod)"},
		{info.Info.CPUCfsQuota, "CPU CFS quota (CpuCfsQuota)"},
		{info.Info.PidsLimit, "PID limit (PidsLimit)"},
	} {
		if !capability.supported {
			return HostCapacity{}, fmt.Errorf("docker cannot enforce managed resource budgets: %s support is unavailable; enable the corresponding controller and limit support in the kernel and Docker daemon's cgroup hierarchy", capability.name)
		}
	}
	roots, err := dockerFilesystemRoots(ctx, d.cli.DaemonHost(), info.Info)
	if err != nil {
		return HostCapacity{}, errors.New(dockerStorageError("capacity filesystem discovery", err))
	}
	var out HostCapacity
	for _, root := range roots {
		if contextErr := ctx.Err(); contextErr != nil {
			return HostCapacity{}, contextErr
		}
		fs, filesystemErr := disk.Filesystem(root.path)
		if filesystemErr != nil {
			return HostCapacity{}, errors.New(dockerStorageError(root.name+" capacity", filesystemErr))
		}
		out.Filesystems = append(out.Filesystems, FilesystemCapacity{Name: root.name, TotalBytes: fs.TotalBytes, FreeBytes: fs.FreeBytes})
	}
	if goruntime.GOOS != "linux" {
		return HostCapacity{}, errors.New("docker host memory capacity requires a verified local Linux host")
	}
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return HostCapacity{}, errors.New(dockerStorageError("host memory", err))
	}
	defer func() { _ = f.Close() }()
	out.MemoryTotalBytes, out.MemoryAvailableBytes, err = parseHostMemory(f)
	if err != nil {
		return HostCapacity{}, errors.New(dockerStorageError("host memory", err))
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return HostCapacity{}, contextErr
	}
	return out, nil
}

func dockerFilesystemRoots(ctx context.Context, host string, info system.Info) ([]dockerStorageRoot, error) {
	if err := verifyLocalDocker(host, info); err != nil {
		return nil, err
	}
	root, err := verifiedStorageDirectory(info.DockerRootDir)
	if err != nil {
		return nil, fmt.Errorf("data-root: %w", err)
	}
	roots := []dockerStorageRoot{{name: "Docker data", path: root}}
	containerdStore := false
	for _, status := range info.DriverStatus {
		if status[0] == "driver-type" && status[1] == "io.containerd.snapshotter.v1" {
			containerdStore = true
		}
	}
	if !containerdStore {
		if info.Driver != "overlay2" && info.Driver != "vfs" {
			return nil, errors.New("image-store filesystem layout is not verified; supported classic drivers are overlay2 and vfs")
		}
		// Moby initializes each classic driver at data-root/<driver>.
		// Its layer store can be a separate mount or a symlink, so probing
		// only data-root does not establish layer-store capacity.
		layerRoot, layerErr := verifiedStorageDirectory(filepath.Join(root, info.Driver))
		if layerErr != nil {
			return nil, fmt.Errorf("%s layer root: %w", info.Driver, layerErr)
		}
		layerName := "Docker overlay2 layers"
		if info.Driver == "vfs" {
			layerName = "Docker vfs layers"
			// VFS Init creates its home, but creates dir/ only with the first
			// layer. Until then new layers allocate on the verified home FS.
			// Lstat distinguishes an absent dir from a broken symlink; never
			// substitute the parent for an existing but unverifiable store.
			dir := filepath.Join(layerRoot, "dir")
			if _, statErr := os.Lstat(dir); statErr == nil {
				layerRoot, layerErr = verifiedStorageDirectory(dir)
				if layerErr != nil {
					return nil, fmt.Errorf("vfs layer directory: %w", layerErr)
				}
			} else if !os.IsNotExist(statErr) {
				return nil, fmt.Errorf("vfs layer directory: %w", statErr)
			}
		}
		return append(roots, dockerStorageRoot{name: layerName, path: layerRoot}), nil
	}
	// These built-in snapshotters store layer data beneath their exported root.
	// Block-device, remote, and proxy snapshotters need different measurements.
	if info.Driver != "overlayfs" && info.Driver != "native" {
		return nil, fmt.Errorf("containerd snapshotter %q filesystem layout is unsupported; supported snapshotters are overlayfs and native", info.Driver)
	}
	if info.Containerd == nil || info.Containerd.Address == "" {
		return nil, errors.New("containerd address is unavailable; Docker must expose Containerd.Address in its info API")
	}
	address := strings.TrimPrefix(info.Containerd.Address, "unix://")
	if !filepath.IsAbs(address) || strings.Contains(address, "://") {
		return nil, errors.New("containerd introspection requires an absolute local Unix socket address")
	}
	st, err := os.Stat(address)
	if err != nil {
		return nil, fmt.Errorf("containerd introspection socket: %w", err)
	}
	if st.Mode()&os.ModeSocket == 0 {
		return nil, errors.New("containerd introspection address is not a local Unix socket")
	}
	conn, err := grpc.NewClient("passthrough:///containerd", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", address)
	}))
	if err != nil {
		return nil, fmt.Errorf("containerd introspection client: %w", err)
	}
	defer func() { _ = conn.Close() }()
	api := introspection.NewIntrospectionClient(conn)
	for _, plugin := range []struct{ kind, id, name string }{
		{"io.containerd.content.v1", "content", "containerd content"},
		{"io.containerd.snapshotter.v1", info.Driver, "containerd snapshots"},
	} {
		response, err := api.PluginInfo(ctx, &introspection.PluginInfoRequest{Type: plugin.kind, ID: plugin.id})
		if err != nil {
			return nil, fmt.Errorf("%s introspection: %w", plugin.name, err)
		}
		p := response.GetPlugin()
		if p == nil || p.Type != plugin.kind || p.ID != plugin.id {
			return nil, fmt.Errorf("%s introspection returned no matching plugin", plugin.name)
		}
		if p.InitErr != nil && p.InitErr.Code != 0 {
			return nil, fmt.Errorf("%s plugin initialization failed: %s", plugin.name, p.InitErr.Message)
		}
		// Official local-content, overlayfs and native plugins export the
		// effective root, including snapshotter root_path overrides.
		path, err := verifiedStorageDirectory(p.Exports["root"])
		if err != nil {
			return nil, fmt.Errorf("%s exported root is not verified: %w", plugin.name, err)
		}
		roots = append(roots, dockerStorageRoot{name: plugin.name, path: path})
	}
	return roots, nil
}

func verifiedStorageDirectory(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("storage root must be an absolute directory")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	st, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !st.IsDir() {
		return "", errors.New("storage root is not a directory")
	}
	return resolved, nil
}

func parseHostMemory(r io.Reader) (total, available uint64, err error) {
	// Missing MemAvailable is unknown, not MemFree or swap-backed headroom.
	scanner := bufio.NewScanner(io.LimitReader(r, 1<<20))
	seen := make(map[string]bool, 2)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 || (fields[0] != "MemTotal:" && fields[0] != "MemAvailable:") {
			continue
		}
		if seen[fields[0]] || len(fields) != 3 || fields[2] != "kB" {
			return 0, 0, errors.New("invalid or duplicate Linux memory field")
		}
		n, parseErr := strconv.ParseUint(fields[1], 10, 64)
		if parseErr != nil || n > math.MaxUint64/1024 {
			return 0, 0, errors.New("invalid Linux memory quantity")
		}
		seen[fields[0]] = true
		if fields[0] == "MemTotal:" {
			total = n * 1024
		} else {
			available = n * 1024
		}
	}
	if scanErr := scanner.Err(); scanErr != nil {
		return 0, 0, scanErr
	}
	if !seen["MemTotal:"] || !seen["MemAvailable:"] || total == 0 || available > total {
		return 0, 0, errors.New("linux MemTotal and MemAvailable are missing or inconsistent")
	}
	return total, available, nil
}
