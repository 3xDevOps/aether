package runtime

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
	"github.com/moby/moby/client/pkg/versions"
)

const (
	defaultNamePrefix = "aether-run-"

	labelManaged       = "aether.managed"
	labelSetupScript   = "aether.setup-script"
	labelSetupSentinel = "aether.setup-sentinel"
	// labelCreationKey persists Spec.CreationKey so FindByCreationKey can
	// recover a container created before its ID was persisted.
	labelCreationKey = "aether.creation-key"

	// The suffix is random per Create so neither image contents nor
	// container processes can pre-release the setup gate.
	setupSentinelPrefix = "/tmp/.aether-setup-"
)

// execOutputLimit covers stdout and stderr together.
const execOutputLimit = 1 << 20

// hijackWriteTimeout bounds one stdin write. Cancellation interrupts a write
// only through its deadline; it never closes the shared stdin stream.
const hijackWriteTimeout = 5 * time.Second

type dockerWaitClient interface {
	ContainerInspect(context.Context, string, client.ContainerInspectOptions) (client.ContainerInspectResult, error)
	ContainerWait(context.Context, string, client.ContainerWaitOptions) client.ContainerWaitResult
}

type Docker struct {
	cli         *client.Client
	waitClient  dockerWaitClient
	namePrefix  string
	labels      map[string]string
	networkMode string
}

var _ Runtime = (*Docker)(nil)

type DockerOption func(*Docker)

// WithNamePrefix defaults to "aether-run-".
func WithNamePrefix(prefix string) DockerOption {
	return func(d *Docker) { d.namePrefix = prefix }
}

func WithLabels(labels map[string]string) DockerOption {
	return func(d *Docker) { d.labels = maps.Clone(labels) }
}

// WithNetworkMode: empty means the daemon default.
func WithNetworkMode(mode string) DockerOption {
	return func(d *Docker) { d.networkMode = mode }
}

// NewDocker connects lazily: daemon reachability surfaces on first use.
func NewDocker(opts ...DockerOption) (*Docker, error) {
	cli, err := client.New(client.FromEnv)
	if err != nil {
		return nil, fmt.Errorf("runtime: docker client: %w", err)
	}
	d := &Docker{cli: cli, waitClient: cli, namePrefix: defaultNamePrefix}
	for _, opt := range opts {
		opt(d)
	}
	return d, nil
}

func (d *Docker) Close() error { return d.cli.Close() }

// Create pulls the image if it is not present locally.
func (d *Docker) Create(ctx context.Context, spec Spec) (ID, error) {
	if err := spec.Validate(); err != nil {
		return "", err
	}
	if slices.ContainsFunc(spec.Mounts, func(m Mount) bool { return m.Subpath != "" }) {
		// Negotiating here settles the version the create request is sent
		// at, so ClientVersion reports it rather than the client's maximum.
		ping, err := d.cli.Ping(ctx, client.PingOptions{NegotiateAPIVersion: true})
		if err != nil {
			return "", fmt.Errorf("runtime: read docker engine API version: %w", err)
		}
		if err := requireSubpathAPI(ping.APIVersion, d.cli.ClientVersion()); err != nil {
			return "", err
		}
	}
	cfg, hostCfg := d.containerConfig(spec)
	var name string
	if spec.Name != "" {
		name = d.namePrefix + spec.Name
	}
	createOpts := client.ContainerCreateOptions{
		Config:     cfg,
		HostConfig: hostCfg,
		Name:       name,
	}
	resp, err := d.cli.ContainerCreate(ctx, createOpts)
	if cerrdefs.IsNotFound(err) {
		if err = d.pull(ctx, spec.Image); err != nil {
			return "", err
		}
		resp, err = d.cli.ContainerCreate(ctx, createOpts)
	}
	if err != nil {
		return "", fmt.Errorf("runtime: create container: %w", err)
	}
	return ID(resp.ID), nil
}

func (d *Docker) pull(ctx context.Context, ref string) error {
	if localOnlyImage(ref) {
		return fmt.Errorf("runtime: image %s is built locally and is missing from the daemon", ref)
	}
	rc, err := d.cli.ImagePull(ctx, ref, client.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("runtime: pull image %s: %w", ref, err)
	}
	defer func() { _ = rc.Close() }()
	// The pull only completes once the progress stream is drained.
	if _, err := io.Copy(io.Discard, rc); err != nil {
		return fmt.Errorf("runtime: pull image %s: %w", ref, err)
	}
	return nil
}

// containerConfig always overrides the entrypoint so Spec.Command is exactly
// the main process; a setup script holds it behind gateEntrypoint.
func (d *Docker) containerConfig(spec Spec) (*container.Config, *container.HostConfig) {
	labels := map[string]string{labelManaged: "true"}
	maps.Copy(labels, d.labels)

	cfg := &container.Config{
		Image:      spec.Image,
		User:       spec.User,
		Env:        dockerEnv(trustCheckout(spec.Env, spec.WorktreeMountPath)),
		WorkingDir: spec.WorkingDir,
		Entrypoint: spec.Command,
		Labels:     labels,
		Tty:        spec.TTY,
		// StdinOnce stays false: the first detach must not close the
		// process's stdin (Attachment.Close never stops the container, and
		// later attachments may still supply input).
		OpenStdin: true,
	}
	if spec.CreationKey != "" {
		cfg.Labels[labelCreationKey] = spec.CreationKey
	}
	if spec.SetupScript != "" {
		sentinel := newSetupSentinel()
		cfg.Entrypoint = gateEntrypoint(sentinel)
		cfg.Cmd = spec.Command
		cfg.Labels[labelSetupScript] = spec.SetupScript
		cfg.Labels[labelSetupSentinel] = sentinel
	}

	initEnabled := true
	hostCfg := &container.HostConfig{
		// The agent can spawn descendants without waiting for them; Docker's
		// init reaps the orphans.
		Init:        &initEnabled,
		NetworkMode: container.NetworkMode(d.networkMode),
		Resources: container.Resources{
			NanoCPUs:   nanoCPUs(spec.CPULimit),
			Memory:     spec.MemoryLimitBytes,
			MemorySwap: spec.MemoryLimitBytes,
		},
		LogConfig: container.LogConfig{Type: "local", Config: map[string]string{"max-size": "10m", "max-file": "3"}},
	}
	if spec.PidsLimit > 0 {
		pids := spec.PidsLimit
		hostCfg.PidsLimit = &pids
	}
	if spec.WorktreeHostPath != "" {
		hostCfg.Mounts = []mount.Mount{bindMount(Mount{
			HostPath:      spec.WorktreeHostPath,
			ContainerPath: spec.WorktreeMountPath,
		})}
	}
	for _, m := range spec.Mounts {
		if m.Subpath != "" {
			hostCfg.Mounts = append(hostCfg.Mounts, subpathMount(m))
			continue
		}
		hostCfg.Mounts = append(hostCfg.Mounts, bindMount(m))
	}
	return cfg, hostCfg
}

// minSubpathAPI: an older engine, or a request sent at an older version,
// silently mounts the whole base, so a subpath mount is refused there.
const minSubpathAPI = "1.45"

// requireSubpathAPI checks the client version too: negotiation never sends
// below the engine's, so a lower one is a DOCKER_API_VERSION pin.
func requireSubpathAPI(engine, sent string) error {
	if engine == "" || versions.LessThan(engine, minSubpathAPI) {
		return fmt.Errorf("runtime: docker engine API %q cannot mount a path beneath a member home; that needs API %s (Docker Engine 26.0) or newer", engine, minSubpathAPI)
	}
	if versions.LessThan(sent, minSubpathAPI) {
		return fmt.Errorf("runtime: docker client sends API %q (DOCKER_API_VERSION), which cannot mount a path beneath a member home; that needs API %s or newer", sent, minSubpathAPI)
	}
	return nil
}

// subpathMount uses a volume subpath, not a plain bind of HostPath/Subpath:
// a bind would follow a symlink another container planted in the base after
// validation, while the engine refuses a volume subpath that leaves it.
func subpathMount(m Mount) mount.Mount {
	sum := sha256.Sum256([]byte(m.HostPath))
	return mount.Mount{
		Type:     mount.TypeVolume,
		Source:   "aether-home-" + hex.EncodeToString(sum[:])[:32],
		Target:   m.ContainerPath,
		ReadOnly: m.ReadOnly,
		VolumeOptions: &mount.VolumeOptions{
			// Never copy image content into the base directory.
			NoCopy:  true,
			Subpath: m.Subpath,
			Labels:  map[string]string{labelManaged: "true"},
			DriverConfig: &mount.Driver{
				Name:    "local",
				Options: map[string]string{"type": "none", "o": "bind", "device": m.HostPath},
			},
		},
	}
}

// bindMount: Docker has no per-bind nosuid/nodev; see the ValidateMounts
// security note.
func bindMount(m Mount) mount.Mount {
	return mount.Mount{
		Type:        mount.TypeBind,
		Source:      m.HostPath,
		Target:      m.ContainerPath,
		ReadOnly:    m.ReadOnly,
		BindOptions: &mount.BindOptions{Propagation: mount.PropagationRPrivate},
	}
}

func newSetupSentinel() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // crypto/rand.Read does not fail
	}
	return setupSentinelPrefix + hex.EncodeToString(b[:])
}

// gateEntrypoint's fractional-sleep fallback covers strictly POSIX sleep
// utilities.
func gateEntrypoint(sentinel string) []string {
	script := fmt.Sprintf(`until [ -e %s ]; do sleep 0.1 2>/dev/null || sleep 1; done; exec "$@"`, sentinel)
	return []string{"/bin/sh", "-c", script, "aether-gate"}
}

func dockerEnv(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	out := make([]string, 0, len(env))
	for _, k := range slices.Sorted(maps.Keys(env)) {
		out = append(out, k+"="+env[k])
	}
	return out
}

// Execs inherit the container env, so this also covers every git the server
// runs inside the container.
func trustCheckout(env map[string]string, checkout string) map[string]string {
	if checkout == "" {
		return env
	}
	n := 0
	if count, ok := env["GIT_CONFIG_COUNT"]; ok {
		parsed, err := strconv.Atoi(count)
		if err != nil || parsed < 0 {
			return env
		}
		n = parsed
	}
	out := make(map[string]string, len(env)+3)
	maps.Copy(out, env)
	out["GIT_CONFIG_KEY_"+strconv.Itoa(n)] = "safe.directory"
	out["GIT_CONFIG_VALUE_"+strconv.Itoa(n)] = checkout
	out["GIT_CONFIG_COUNT"] = strconv.Itoa(n + 1)
	return out
}

func nanoCPUs(cores float64) int64 {
	return int64(math.Round(cores * 1e9))
}

// Start returns only after any setup script succeeded; a nonzero setup exit
// kills the container. The sentinel on the container filesystem makes a
// later Start skip the script.
func (d *Docker) Start(ctx context.Context, id ID) error {
	info, err := d.cli.ContainerInspect(ctx, string(id), client.ContainerInspectOptions{})
	if err != nil {
		return fmt.Errorf("runtime: inspect container: %w", err)
	}
	wasRunning := info.Container.State != nil && info.Container.State.Running
	if _, err := d.cli.ContainerStart(ctx, string(id), client.ContainerStartOptions{}); err != nil {
		// The daemon may have started it despite the error (a cancelled ctx
		// mid-request).
		if !wasRunning {
			_, _ = d.cli.ContainerKill(
				context.WithoutCancel(ctx),
				string(id),
				client.ContainerKillOptions{Signal: "KILL"},
			)
		}
		return fmt.Errorf("runtime: start container: %w", err)
	}
	script := info.Container.Config.Labels[labelSetupScript]
	if script == "" {
		return nil
	}
	sentinel := info.Container.Config.Labels[labelSetupSentinel]
	if err := d.runSetup(ctx, id, script, sentinel, info.Container.Config.WorkingDir); err != nil {
		// Never kill a run that was already live before this Start call: a
		// redundant Start must not take down a healthy container.
		if !wasRunning {
			_, _ = d.cli.ContainerKill(
				context.WithoutCancel(ctx),
				string(id),
				client.ContainerKillOptions{Signal: "KILL"},
			)
		}
		return err
	}
	return nil
}

func (d *Docker) runSetup(ctx context.Context, id ID, script, sentinel, workDir string) error {
	code, output, err := d.execCombined(ctx, id, []string{"/bin/sh", "-c", "test -e " + sentinel}, "")
	if err != nil {
		slog.Error("runtime: setup gate probe failed", "container", id, "output", output, "error", err)
		return fmt.Errorf("runtime: probe setup gate: %w", err)
	}
	if code == 0 {
		return nil // setup already completed for this container
	}
	code, output, err = d.execCombined(ctx, id, []string{"/bin/sh", "-ec", script}, workDir)
	if err != nil {
		slog.Error("runtime: setup script execution failed", "container", id, "working_dir", workDir, "script", script, "output", output, "error", err)
		return fmt.Errorf("runtime: setup script: %w", err)
	}
	if code != 0 {
		slog.Error("runtime: setup script exited nonzero", "container", id, "working_dir", workDir, "script", script, "output", output, "exit_code", code)
		return fmt.Errorf("runtime: setup script exited %d: %s", code, strings.TrimSpace(output))
	}
	release := []string{"/bin/sh", "-c", "mkdir -p /tmp && : > " + sentinel}
	code, output, err = d.execCombined(ctx, id, release, "")
	if err != nil {
		slog.Error("runtime: release setup gate failed", "container", id, "output", output, "error", err)
		return fmt.Errorf("runtime: release setup gate: %w", err)
	}
	if code != 0 {
		slog.Error("runtime: release setup gate exited nonzero", "container", id, "output", output, "exit_code", code)
		return fmt.Errorf("runtime: release setup gate exited %d: %s", code, strings.TrimSpace(output))
	}
	return nil
}

// execCombined is Exec for the setup script, whose log lines want one
// transcript rather than two streams.
func (d *Docker) execCombined(ctx context.Context, id ID, cmd []string, workDir string) (int, string, error) {
	code, stdout, stderr, err := d.Exec(ctx, id, cmd, workDir)
	return code, stdout + stderr, err
}

func (d *Docker) Exec(ctx context.Context, id ID, cmd []string, workDir string) (int, string, string, error) {
	created, err := d.cli.ExecCreate(ctx, string(id), client.ExecCreateOptions{
		Cmd:          cmd,
		WorkingDir:   workDir,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return 0, "", "", fmt.Errorf("exec create: %w", err)
	}
	att, err := d.cli.ExecAttach(ctx, created.ID, client.ExecAttachOptions{})
	if err != nil {
		return 0, "", "", fmt.Errorf("exec attach: %w", err)
	}
	defer att.Close()
	// After the hijack, ctx no longer governs the connection; closing it on
	// cancellation unblocks StdCopy so a hung command cannot wedge exec.
	watchDone := make(chan struct{})
	defer close(watchDone)
	go func() {
		select {
		case <-ctx.Done():
			att.Close()
		case <-watchDone:
		}
	}()
	stdout, stderr, copyErr := readExecOutput(att.Reader)
	if err := ctx.Err(); err != nil {
		return 0, stdout, stderr, err
	}
	if copyErr != nil {
		return 0, stdout, stderr, fmt.Errorf("exec output: %w", copyErr)
	}
	for {
		ins, err := d.cli.ExecInspect(ctx, created.ID, client.ExecInspectOptions{})
		if err != nil {
			return 0, stdout, stderr, fmt.Errorf("exec inspect: %w", err)
		}
		if !ins.Running {
			return ins.ExitCode, stdout, stderr, nil
		}
		select {
		case <-ctx.Done():
			return 0, stdout, stderr, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// readExecOutput refuses output past execOutputLimit: the container chooses
// what it prints and the caller buffers it all.
func readExecOutput(r io.Reader) (stdout, stderr string, err error) {
	// One byte past the cap, so an output of exactly that size is not
	// mistaken for a truncated one.
	counted := &countingReader{r: io.LimitReader(r, execOutputLimit+1)}
	var out, errOut bytes.Buffer
	_, copyErr := stdcopy.StdCopy(&out, &errOut, counted)
	if counted.read > execOutputLimit {
		return out.String(), errOut.String(), fmt.Errorf("output passed the %d byte limit", execOutputLimit)
	}
	return out.String(), errOut.String(), copyErr
}

type countingReader struct {
	r    io.Reader
	read int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.read += int64(n)
	return n, err
}

func (d *Docker) Pause(ctx context.Context, id ID) error {
	if _, err := d.cli.ContainerPause(ctx, string(id), client.ContainerPauseOptions{}); err != nil {
		return fmt.Errorf("runtime: pause container: %w", err)
	}
	return nil
}

func (d *Docker) Resume(ctx context.Context, id ID) error {
	if _, err := d.cli.ContainerUnpause(ctx, string(id), client.ContainerUnpauseOptions{}); err != nil {
		return fmt.Errorf("runtime: resume container: %w", err)
	}
	return nil
}

// Stop thaws a paused container first so the signal can be delivered.
func (d *Docker) Stop(ctx context.Context, id ID, grace time.Duration) error {
	info, err := d.cli.ContainerInspect(ctx, string(id), client.ContainerInspectOptions{})
	if err != nil {
		return dockerWaitError("inspect", id, err)
	}
	if info.Container.State != nil && info.Container.State.Paused {
		if _, err := d.cli.ContainerUnpause(ctx, string(id), client.ContainerUnpauseOptions{}); err != nil {
			return fmt.Errorf("runtime: unpause before stop: %w", err)
		}
	}
	var opts client.ContainerStopOptions
	if grace >= 0 {
		secs := int(math.Ceil(grace.Seconds()))
		opts.Timeout = &secs
	}
	if _, err := d.cli.ContainerStop(ctx, string(id), opts); err != nil {
		return dockerWaitError("stop", id, err)
	}
	return nil
}

// Destroy treats a missing container as success.
func (d *Docker) Destroy(ctx context.Context, id ID) error {
	_, err := d.cli.ContainerRemove(ctx, string(id), client.ContainerRemoveOptions{
		Force:         true,
		RemoveVolumes: true,
	})
	if err != nil && !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("runtime: destroy container: %w", err)
	}
	return nil
}

func (d *Docker) Attach(ctx context.Context, id ID) (Attachment, error) {
	info, err := d.cli.ContainerInspect(ctx, string(id), client.ContainerInspectOptions{})
	if err != nil {
		return nil, fmt.Errorf("runtime: inspect container: %w", err)
	}
	tty := info.Container.Config != nil && info.Container.Config.Tty
	resp, err := d.cli.ContainerAttach(ctx, string(id), client.ContainerAttachOptions{
		Stream: true,
		Stdin:  true,
		Stdout: true,
		Stderr: true,
	})
	if err != nil {
		return nil, fmt.Errorf("runtime: attach container: %w", err)
	}
	return newDockerAttachment(d.cli, string(id), tty, resp.HijackedResponse), nil
}

func (d *Docker) ContainerIP(ctx context.Context, id ID) (string, error) {
	info, err := d.cli.ContainerInspect(ctx, string(id), client.ContainerInspectOptions{})
	if err != nil {
		return "", fmt.Errorf("runtime: inspect container %q: %w", id, err)
	}
	if info.Container.NetworkSettings != nil {
		for _, network := range info.Container.NetworkSettings.Networks {
			if network != nil && network.IPAddress.IsValid() {
				return network.IPAddress.String(), nil
			}
		}
	}
	return "", fmt.Errorf("runtime: container %q has no network IP", id)
}

func (d *Docker) ExecTTY(ctx context.Context, id ID, argv []string, workDir string, cols, rows uint) (Attachment, error) {
	opts := client.ExecCreateOptions{
		TTY:          true,
		AttachStdin:  true,
		AttachStdout: true,
		Cmd:          argv,
		WorkingDir:   workDir,
	}
	if cols != 0 && rows != 0 {
		opts.ConsoleSize = client.ConsoleSize{Height: rows, Width: cols}
	}
	created, err := d.cli.ExecCreate(ctx, string(id), opts)
	if err != nil {
		return nil, fmt.Errorf("runtime: exec create: %w", err)
	}
	resp, err := d.cli.ExecAttach(ctx, created.ID, client.ExecAttachOptions{TTY: true})
	if err != nil {
		if exitErr := d.execExitError(ctx, created.ID); exitErr != nil {
			return nil, exitErr
		}
		return nil, fmt.Errorf("runtime: exec attach: %w", err)
	}
	att := newExecAttachment(d.cli, created.ID, true, false, resp.HijackedResponse)
	if cols != 0 && rows != 0 {
		if err := att.Resize(ctx, cols, rows); err != nil {
			_ = att.Close()
			// The daemon accepts the attach even when the executable is
			// missing; the immediate exit only surfaces here.
			if exitErr := d.execExitError(ctx, created.ID); exitErr != nil {
				return nil, exitErr
			}
			return nil, err
		}
	}
	return att, nil
}

func (d *Docker) execExitError(ctx context.Context, execID string) error {
	ins, err := d.cli.ExecInspect(ctx, execID, client.ExecInspectOptions{})
	if err != nil || ins.Running {
		return nil
	}
	if ins.ExitCode == 126 || ins.ExitCode == 127 {
		return &ExecExitError{Code: ins.ExitCode}
	}
	return nil
}

// Wait on a never-started container waits for its first run rather than
// reporting a phantom zero exit.
func (d *Docker) Wait(ctx context.Context, id ID) (ExitStatus, error) {
	cond := container.WaitConditionNotRunning
	info, err := d.waitClient.ContainerInspect(ctx, string(id), client.ContainerInspectOptions{})
	if err != nil {
		return ExitStatus{}, dockerWaitError("inspect", id, err)
	}
	if info.Container.State != nil && info.Container.State.Status == container.StateCreated {
		cond = container.WaitConditionNextExit
	}
	wait := d.waitClient.ContainerWait(ctx, string(id), client.ContainerWaitOptions{Condition: cond})
	select {
	case err := <-wait.Error:
		return ExitStatus{}, dockerWaitError("wait", id, err)
	case resp := <-wait.Result:
		if resp.Error != nil {
			return ExitStatus{}, fmt.Errorf("runtime: wait container: %s", resp.Error.Message)
		}
		return ExitStatus{Code: int(resp.StatusCode)}, nil
	}
}

func dockerWaitError(action string, id ID, err error) error {
	if cerrdefs.IsNotFound(err) {
		return fmt.Errorf("runtime: %s container %q: %w: %w", action, id, ErrNotFound, err)
	}
	return fmt.Errorf("runtime: %s container %q: %w", action, id, err)
}

// Inspect deliberately does not resolve the image tag again: recovered
// callers need the HOME and user of this container.
func (d *Docker) Inspect(ctx context.Context, id ID) (ContainerInfo, error) {
	info, err := d.cli.ContainerInspect(ctx, string(id), client.ContainerInspectOptions{})
	if err != nil {
		return ContainerInfo{}, dockerWaitError("inspect", id, err)
	}
	if info.Container.Config == nil {
		return ContainerInfo{}, fmt.Errorf("runtime: inspect container %q: missing config", id)
	}
	return ContainerInfo{
		Image: info.Container.Config.Image,
		User:  info.Container.Config.User,
		Env:   append([]string(nil), info.Container.Config.Env...),
	}, nil
}

// FindByCreationKey matches containers in any state.
func (d *Docker) FindByCreationKey(ctx context.Context, key string) (ID, error) {
	if key == "" {
		return "", fmt.Errorf("runtime: find by creation key: %w", ErrNotFound)
	}
	list, err := d.cli.ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: make(client.Filters).Add("label", labelCreationKey+"="+key),
	})
	if err != nil {
		return "", fmt.Errorf("runtime: find by creation key: %w", err)
	}
	if len(list.Items) == 0 {
		return "", fmt.Errorf("runtime: find by creation key %q: %w", key, ErrNotFound)
	}
	return ID(list.Items[0].ID), nil
}

// ImageUser returns the OCI config User (name, uid, or uid:gid; empty means
// root), pulling the image if needed.
func (d *Docker) ImageUser(ctx context.Context, ref string) (string, error) {
	info, err := d.cli.ImageInspect(ctx, ref)
	if cerrdefs.IsNotFound(err) {
		if err = d.pull(ctx, ref); err != nil {
			return "", err
		}
		info, err = d.cli.ImageInspect(ctx, ref)
	}
	if err != nil {
		return "", fmt.Errorf("runtime: inspect image %s: %w", ref, err)
	}
	if info.Config == nil {
		return "", nil
	}
	return info.Config.User, nil
}

// dockerAttachment buffers demuxed stdout and stderr independently so
// reading only one never stalls the other.
type dockerAttachment struct {
	cli       *client.Client
	id        string
	tty       bool
	resp      client.HijackedResponse
	stdout    *streamBuffer
	stderr    *streamBuffer
	closeOnce sync.Once
}

func newDockerAttachment(cli *client.Client, id string, tty bool, resp client.HijackedResponse) *dockerAttachment {
	a := &dockerAttachment{
		cli:    cli,
		id:     id,
		tty:    tty,
		resp:   resp,
		stdout: newStreamBuffer(),
		stderr: newStreamBuffer(),
	}
	go func() {
		var err error
		if tty {
			_, err = io.Copy(a.stdout, resp.Reader)
		} else {
			_, err = stdcopy.StdCopy(a.stdout, a.stderr, resp.Reader)
		}
		a.stdout.CloseWithError(err)
		a.stderr.CloseWithError(err)
	}()
	return a
}

func (a *dockerAttachment) Stdin() io.WriteCloser { return hijackStdin{a.resp} }
func (a *dockerAttachment) Stdout() io.Reader     { return a.stdout }
func (a *dockerAttachment) Stderr() io.Reader     { return a.stderr }

func (a *dockerAttachment) Resize(ctx context.Context, cols, rows uint) error {
	if !a.tty {
		return errors.New("runtime: resize: attachment has no TTY")
	}
	_, err := a.cli.ContainerResize(ctx, a.id, client.ContainerResizeOptions{Width: cols, Height: rows})
	if err != nil {
		return fmt.Errorf("runtime: resize: %w", err)
	}
	return nil
}

func (a *dockerAttachment) Close() error {
	a.closeOnce.Do(func() {
		// EOF the readers first so drained consumers see a clean end of
		// stream rather than the connection-teardown error.
		a.stdout.CloseWithError(nil)
		a.stderr.CloseWithError(nil)
		a.resp.Close()
	})
	return nil
}

// hijackStdin's WriteContext changes only the write deadline on
// cancellation, preserving shared stdin for later inputs and attachments.
type hijackStdin struct {
	resp client.HijackedResponse
}

func (h hijackStdin) Write(p []byte) (int, error) { return h.resp.Conn.Write(p) }

func (h hijackStdin) WriteContext(ctx context.Context, p []byte) (int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	deadline := time.Now().Add(hijackWriteTimeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	if err := h.resp.Conn.SetWriteDeadline(deadline); err != nil {
		return 0, err
	}
	cancelDone := make(chan struct{})
	stopCancel := context.AfterFunc(ctx, func() {
		_ = h.resp.Conn.SetWriteDeadline(time.Now())
		close(cancelDone)
	})
	n, err := h.resp.Conn.Write(p)
	if !stopCancel() {
		<-cancelDone
	}
	_ = h.resp.Conn.SetWriteDeadline(time.Time{})
	return n, err
}

func (h hijackStdin) Close() error { return h.resp.CloseWrite() }

const maxStreamBuffer = 8 << 20

// streamBuffer writes never block, since blocking the demux goroutine would
// stall the sibling stream; past maxStreamBuffer the oldest bytes are
// dropped. A lossless buffer blocks at the cap instead, for protocol streams
// that cannot lose a byte.
type streamBuffer struct {
	mu       sync.Mutex
	cond     *sync.Cond
	buf      bytes.Buffer
	lossless bool
	closed   bool
	err      error
}

func newStreamBuffer() *streamBuffer {
	b := &streamBuffer{}
	b.cond = sync.NewCond(&b.mu)
	return b
}

func newLosslessStreamBuffer() *streamBuffer {
	b := newStreamBuffer()
	b.lossless = true
	return b
}

func (b *streamBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for b.lossless && !b.closed && b.buf.Len() >= maxStreamBuffer {
		b.cond.Wait()
	}
	if b.closed {
		return 0, io.ErrClosedPipe
	}
	n, _ := b.buf.Write(p) // bytes.Buffer.Write cannot fail
	if b.lossless {
		b.cond.Broadcast()
		return n, nil
	}
	if over := b.buf.Len() - maxStreamBuffer; over > 0 {
		b.buf.Next(over)
	}
	b.cond.Broadcast()
	return n, nil
}

func (b *streamBuffer) Read(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for b.buf.Len() == 0 && !b.closed {
		b.cond.Wait()
	}
	if b.buf.Len() > 0 {
		if b.lossless {
			b.cond.Broadcast()
		}
		return b.buf.Read(p)
	}
	if b.err != nil {
		return 0, b.err
	}
	return 0, io.EOF
}

// CloseWithError leaves buffered data readable first. Only the first close
// takes effect.
func (b *streamBuffer) CloseWithError(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	b.err = err
	b.cond.Broadcast()
}
