package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/3xDevOps/Aether/internal/disk"
	"github.com/moby/moby/api/types/system"
	"github.com/moby/moby/client"
)

// StorageUsage is an optional reporting seam, not part of Runtime. It never
// prunes resources or exposes daemon paths and volume names to callers.
func (d *Docker) StorageUsage(ctx context.Context, dataDir string) disk.DockerUsage {
	var out disk.DockerUsage
	usage, err := d.cli.DiskUsage(ctx, client.DiskUsageOptions{
		Images: true, Containers: true, Volumes: true, BuildCache: true, Verbose: true,
	})
	if err != nil {
		out.Error = dockerStorageError("disk usage", err)
	} else {
		out = dockerCategoryUsage(usage)
	}
	info, err := d.cli.Info(ctx, client.InfoOptions{})
	if err != nil {
		out.Error = strings.TrimSpace(out.Error + " " + dockerStorageError("info", err))
		return out
	}
	root, err := dockerFilesystemRoot(d.cli.DaemonHost(), info.Info)
	if err != nil {
		out.Error = strings.TrimSpace(out.Error + " " + dockerStorageError("filesystem", err))
		return out
	}
	fs, err := disk.Filesystem(root)
	if err != nil {
		out.Error = strings.TrimSpace(out.Error + " " + dockerStorageError("data-root filesystem", err))
		return out
	}
	out.UsedBytes, out.TotalBytes, out.FreeBytes = &fs.UsedBytes, &fs.TotalBytes, &fs.FreeBytes
	if same, err := disk.SameFilesystem(dataDir, info.Info.DockerRootDir); err == nil {
		out.SharedFilesystem = &same
	} else {
		out.Error = strings.TrimSpace(out.Error + " " + dockerStorageError("filesystem identity", err))
	}
	return out
}

func dockerCategoryUsage(usage client.DiskUsageResult) disk.DockerUsage {
	out := disk.DockerUsage{
		// Use the SDK's daemon layer totals, never summed virtual image sizes.
		ImagesBytes:     knownDockerBytes(usage.Images.TotalSize),
		ContainersBytes: knownDockerBytes(usage.Containers.TotalSize),
	}
	reclaimKnown := true
	for _, image := range usage.Images.Items {
		if image.Containers < 0 || (image.Containers == 0 && (image.Size < 0 || image.SharedSize < 0)) {
			reclaimKnown = false
		}
	}
	for _, container := range usage.Containers.Items {
		if container.SizeRw < 0 {
			out.ContainersBytes = nil
			reclaimKnown = false
		}
	}
	// New APIs include image-shared cache in TotalSize; legacy conversion
	// excludes it. Complete verbose records give one consistent exclusive sum.
	cacheKnown := int64(len(usage.BuildCache.Items)) == usage.BuildCache.TotalCount
	var cacheBytes, reclaimableCache int64
	for _, cache := range usage.BuildCache.Items {
		if cache.Shared {
			continue
		}
		if cache.Size < 0 {
			cacheKnown = false
			continue
		}
		cacheBytes += cache.Size
		if !cache.InUse {
			reclaimableCache += cache.Size
		}
	}
	if cacheKnown {
		out.BuildCacheBytes = knownDockerBytes(cacheBytes)
	} else {
		reclaimKnown = false
	}
	var volumes, reclaimableVolumes int64
	volumeKnown := int64(len(usage.Volumes.Items)) == usage.Volumes.TotalCount
	volumeReclaimKnown := volumeKnown
	for _, volume := range usage.Volumes.Items {
		if volume.Driver == "" {
			volumeKnown, volumeReclaimKnown = false, false
			continue
		}
		// Bind-backed named volumes are aliases of host paths, including
		// member homes. Network/plugin volumes are not local Docker bytes.
		if volume.Driver != "local" || volume.Options["device"] != "" {
			continue
		}
		if volume.UsageData == nil || volume.UsageData.Size < 0 {
			volumeKnown, volumeReclaimKnown = false, false
			continue
		}
		volumes += volume.UsageData.Size
		if volume.UsageData.RefCount < 0 {
			volumeReclaimKnown = false
		} else if volume.UsageData.RefCount == 0 {
			reclaimableVolumes += volume.UsageData.Size
		}
	}
	if volumeKnown {
		out.VolumesBytes = knownDockerBytes(volumes)
	} else {
		out.Error = "Docker local volume usage is incomplete."
	}
	if reclaimKnown && volumeReclaimKnown && out.ImagesBytes != nil && out.ContainersBytes != nil && out.BuildCacheBytes != nil && usage.Images.Reclaimable >= 0 && usage.Containers.Reclaimable >= 0 {
		out.ReclaimableBytes = knownDockerBytes(usage.Images.Reclaimable + usage.Containers.Reclaimable + reclaimableCache + reclaimableVolumes)
	}
	if out.ImagesBytes == nil || out.ContainersBytes == nil || out.BuildCacheBytes == nil {
		out.Error = strings.TrimSpace(out.Error + " Docker reported unknown category sizes.")
	}
	return out
}

func knownDockerBytes(n int64) *uint64 {
	if n < 0 {
		return nil
	}
	u := uint64(n)
	return &u
}

func dockerFilesystemRoot(host string, info system.Info) (string, error) {
	if !strings.HasPrefix(host, "unix://") || info.DockerRootDir == "" {
		return "", errors.New("filesystem usage is unavailable for a remote daemon")
	}
	for _, status := range info.DriverStatus {
		if strings.Contains(strings.ToLower(status[1]), "containerd") {
			return "", errors.New("containerd image-store filesystem is not verified")
		}
	}
	// Only these layouts establish that image/container layers live below
	// DockerRootDir. Other storage drivers may use separate backing devices.
	if info.Driver != "overlay2" && info.Driver != "vfs" {
		return "", errors.New("image-store filesystem layout is not verified")
	}
	id, err := os.ReadFile(filepath.Join(info.DockerRootDir, "engine-id"))
	if err != nil {
		return "", fmt.Errorf("local daemon identity could not be verified: %w", err)
	}
	if info.ID == "" || strings.TrimSpace(string(id)) != info.ID {
		return "", errors.New("local daemon identity does not match connected engine")
	}
	return info.DockerRootDir, nil
}

// Endpoint URLs can include credentials, query tokens and private paths. Strip
// them entirely, as well as quoted values and remaining absolute paths, while
// retaining the daemon's actionable error text.
var dockerPrivateErrorValue = regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://[^\s"'<>]+|"[^"]*"|'[^']*'`)
var dockerPrivateErrorPath = regexp.MustCompile(`(?i)(^|[\s:(])(?:[a-z]:\\|/)[^\s,;:)]*`)

func dockerErrorDetail(err error) string {
	detail := dockerPrivateErrorValue.ReplaceAllString(err.Error(), "[redacted]")
	return dockerPrivateErrorPath.ReplaceAllString(detail, "${1}[redacted]")
}

func dockerStorageError(operation string, err error) string {
	reason := dockerErrorDetail(err)
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		reason = pathErr.Op + ": " + dockerErrorDetail(pathErr.Err)
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		reason = "request timed out: " + reason
	case errors.Is(err, context.Canceled):
		reason = "request canceled: " + reason
	case client.IsErrConnectionFailed(err):
		reason = "daemon connection failed: " + reason
	}
	return "Docker " + operation + " unavailable: " + reason + "."
}
