package runtime

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

const (
	labelBrowser    = "aether.browser"
	labelBrowserRun = "aether.browser.run-container"
)

// Playwright v1.63.0 utils/docker/seccomp_profile.json (Docker's default
// allowlist plus user namespaces), with clone3 ENOSYS fallback and modern libc
// close_range/faccessat2/epoll_pwait2 support. No privileged or unconfined retry.
//
//go:embed browser_seccomp.json
var browserSeccomp string

var _ BrowserRuntime = (*Docker)(nil)

func (d *Docker) browserContainerConfig(spec BrowserSpec) (*container.Config, *container.HostConfig) {
	labels := maps.Clone(d.labels)
	if labels == nil {
		labels = make(map[string]string)
	}
	// Never inherit a run's setup gate or let deployment labels replace identity.
	delete(labels, labelSetupScript)
	delete(labels, labelSetupSentinel)
	labels[labelManaged] = "true"
	labels[labelBrowser] = "true"
	labels[labelCreationKey] = spec.CreationKey
	labels[labelBrowserRun] = string(spec.RunContainer)
	initEnabled := true
	pids := int64(512)
	return &container.Config{
		Image:      spec.Image,
		User:       "1000:1000",
		WorkingDir: "/opt/aether-browser",
		Entrypoint: []string{"node", "/opt/aether-browser/server.mjs"},
		Env:        []string{"HOME=/tmp/aether-browser-home", "NODE_ENV=production", "PLAYWRIGHT_BROWSERS_PATH=/opt/playwright", "AETHER_BROWSER_CREATION_KEY=" + spec.CreationKey},
		Labels:     labels,
	}, &container.HostConfig{
		Init:           &initEnabled,
		NetworkMode:    container.NetworkMode("container:" + string(spec.RunContainer)),
		IpcMode:        "private",
		ShmSize:        BrowserSharedMemoryBytes,
		ReadonlyRootfs: true,
		CapDrop:        []string{"ALL"},
		SecurityOpt:    []string{"no-new-privileges=true", "seccomp=" + browserSeccomp},
		Tmpfs:          map[string]string{"/tmp": "rw,nosuid,nodev,size=536870912,mode=1777"},
		Mounts:         []mount.Mount{{Type: mount.TypeBind, Source: spec.ControlHostPath, Target: "/aether-control", BindOptions: &mount.BindOptions{Propagation: mount.PropagationRPrivate}}},
		Resources:      container.Resources{NanoCPUs: nanoCPUs(spec.CPULimit), Memory: spec.MemoryLimitBytes, MemorySwap: spec.MemoryLimitBytes, PidsLimit: &pids},
		LogConfig:      container.LogConfig{Type: "local", Config: map[string]string{"max-size": "10m", "max-file": "2"}},
	}
}

func (d *Docker) CreateBrowser(ctx context.Context, spec BrowserSpec) (ID, error) {
	if err := spec.Validate(); err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(spec.ControlHostPath)
	if err != nil {
		return "", fmt.Errorf("runtime: browser control directory: %w", err)
	}
	if resolved != filepath.Clean(spec.ControlHostPath) {
		return "", errors.New("runtime: browser control directory must not traverse symlinks")
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("runtime: browser control directory must be private (0700)")
	}
	if err := d.checkBrowserNetwork(ctx, spec.RunContainer); err != nil {
		return "", err
	}
	image, err := d.cli.ImageInspect(ctx, spec.Image)
	if cerrdefs.IsNotFound(err) {
		if strings.HasPrefix(spec.Image, "sha256:") {
			return "", fmt.Errorf("runtime: immutable browser image %s is not loaded in the Docker daemon", spec.Image)
		}
		if err := d.pull(ctx, spec.Image); err != nil {
			return "", err
		}
		image, err = d.cli.ImageInspect(ctx, spec.Image)
	}
	if err != nil {
		return "", fmt.Errorf("runtime: inspect browser image %q: %w", spec.Image, err)
	}
	if image.ID == "" {
		return "", errors.New("runtime: browser image has no immutable identity")
	}
	cfg, hostCfg := d.browserContainerConfig(spec)
	cfg.Image = image.ID
	name := ""
	if spec.Name != "" {
		name = d.namePrefix + spec.Name
	}
	opts := client.ContainerCreateOptions{Config: cfg, HostConfig: hostCfg, Name: name}
	created, err := d.cli.ContainerCreate(ctx, opts)
	if err != nil {
		return "", fmt.Errorf("runtime: create browser companion: %w", err)
	}
	return ID(created.ID), nil
}

// Follow namespace-sharing chains rather than mistaking container:host-run for
// isolation. The root may be bridge/none/custom; it must never be host network.
func (d *Docker) checkBrowserNetwork(ctx context.Context, id ID) error {
	seen := make(map[string]bool)
	for current := string(id); ; {
		if seen[current] || len(seen) >= 32 {
			return errors.New("runtime: browser network namespace chain is cyclic or too deep")
		}
		seen[current] = true
		result, err := d.cli.ContainerInspect(ctx, current, client.ContainerInspectOptions{})
		if err != nil {
			return dockerWaitError("inspect browser network target", ID(current), err)
		}
		info := result.Container
		if info.State == nil || !info.State.Running || info.State.Paused || info.HostConfig == nil {
			return errors.New("runtime: browser network target is not a running container")
		}
		mode := string(info.HostConfig.NetworkMode)
		if mode == "host" {
			return errors.New("runtime: browser companions cannot join host networking")
		}
		if !strings.HasPrefix(mode, "container:") {
			return nil
		}
		current = strings.TrimPrefix(mode, "container:")
		if current == "" {
			return errors.New("runtime: browser network namespace target is empty")
		}
	}
}

func (d *Docker) InspectBrowser(ctx context.Context, id ID) (BrowserInfo, error) {
	result, err := d.cli.ContainerInspect(ctx, string(id), client.ContainerInspectOptions{})
	if err != nil {
		return BrowserInfo{}, dockerWaitError("inspect browser companion", id, err)
	}
	info := result.Container
	if info.Config == nil || info.HostConfig == nil || info.State == nil || info.Config.Labels[labelManaged] != "true" || info.Config.Labels[labelBrowser] != "true" {
		return BrowserInfo{}, errors.New("runtime: resource is not an owned browser companion")
	}
	key, run := info.Config.Labels[labelCreationKey], info.Config.Labels[labelBrowserRun]
	if key == "" || run == "" || string(info.HostConfig.NetworkMode) != "container:"+run {
		return BrowserInfo{}, errors.New("runtime: browser companion ownership or network mismatch")
	}
	state := string(info.State.Status)
	if info.State.Paused {
		state = "paused"
	}
	return BrowserInfo{ContainerID: id, RunContainer: ID(run), CreationKey: key, State: state, Image: info.Image}, nil
}
