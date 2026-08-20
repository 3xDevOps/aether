package runtime

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

const (
	defaultNamePrefix = "aether-run-"

	labelManaged       = "aether.managed"
	labelSetupScript   = "aether.setup-script"
	labelSetupSentinel = "aether.setup-sentinel"
	// labelCreationKey persists Spec.CreationKey so FindByCreationKey can
	// recover a container created before its ID was persisted.
	labelCreationKey = "aether.creation-key"

	// setupSentinelPrefix is the base of the per-container sentinel file
	// that gates the main command: Start creates it once the setup script
	// has succeeded. The suffix is random per Create so neither image
	// contents nor container processes can pre-release the gate.
	setupSentinelPrefix = "/tmp/.aether-setup-"
)

type dockerWaitClient interface {
	ContainerInspect(context.Context, string) (container.InspectResponse, error)
	ContainerWait(context.Context, string, container.WaitCondition) (<-chan container.WaitResponse, <-chan error)
}

// Docker is the Runtime implementation backed by the local Docker daemon.
type Docker struct {
	cli         *client.Client
	waitClient  dockerWaitClient
	namePrefix  string
	labels      map[string]string
	networkMode string
}

var _ Runtime = (*Docker)(nil)

// DockerOption customizes a Docker runtime.
type DockerOption func(*Docker)

// WithNamePrefix sets the prefix applied to container names built from
// Spec.Name (default "aether-run-").
func WithNamePrefix(prefix string) DockerOption {
	return func(d *Docker) { d.namePrefix = prefix }
}

// WithLabels adds labels to every container the runtime creates.
func WithLabels(labels map[string]string) DockerOption {
	return func(d *Docker) { d.labels = maps.Clone(labels) }
}

// WithNetworkMode sets the network mode for created containers (e.g.
// "bridge", "host", "none"); empty means the daemon default.
func WithNetworkMode(mode string) DockerOption {
	return func(d *Docker) { d.networkMode = mode }
}

// NewDocker connects to the Docker daemon using the standard environment
// (DOCKER_HOST and friends) and negotiates the API version. The connection
// is lazy: daemon reachability surfaces on first use.
func NewDocker(opts ...DockerOption) (*Docker, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("runtime: docker client: %w", err)
	}
	d := &Docker{cli: cli, waitClient: cli, namePrefix: defaultNamePrefix}
	for _, opt := range opts {
		opt(d)
	}
	return d, nil
}

// Close releases the client's connections to the daemon.
func (d *Docker) Close() error { return d.cli.Close() }

// Create implements Runtime. The image is pulled if not present locally.
func (d *Docker) Create(ctx context.Context, spec Spec) (ID, error) {
	if err := spec.Validate(); err != nil {
		return "", err
	}
	cfg, hostCfg := d.containerConfig(spec)
	var name string
	if spec.Name != "" {
		name = d.namePrefix + spec.Name
	}
	resp, err := d.cli.ContainerCreate(ctx, cfg, hostCfg, nil, nil, name)
	if cerrdefs.IsNotFound(err) {
		if err = d.pull(ctx, spec.Image); err != nil {
			return "", err
		}
		resp, err = d.cli.ContainerCreate(ctx, cfg, hostCfg, nil, nil, name)
	}
	if err != nil {
		return "", fmt.Errorf("runtime: create container: %w", err)
	}
	return ID(resp.ID), nil
}

func (d *Docker) pull(ctx context.Context, ref string) error {
	rc, err := d.cli.ImagePull(ctx, ref, image.PullOptions{})
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

// containerConfig translates a Spec into Docker create arguments. The
// entrypoint is always overridden so Spec.Command is exactly the main
// process; when a setup script is present the command is held behind the
// setup gate (see gateEntrypoint) and the script and sentinel path ride
// along as labels for Start to use.
func (d *Docker) containerConfig(spec Spec) (*container.Config, *container.HostConfig) {
	labels := map[string]string{labelManaged: "true"}
	maps.Copy(labels, d.labels)

	cfg := &container.Config{
		Image:      spec.Image,
		User:       spec.User,
		Env:        dockerEnv(spec.Env),
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

	hostCfg := &container.HostConfig{
		NetworkMode: container.NetworkMode(d.networkMode),
		Resources: container.Resources{
			NanoCPUs: nanoCPUs(spec.CPULimit),
			Memory:   spec.MemoryLimitBytes,
		},
	}
	if spec.WorktreeHostPath != "" {
		hostCfg.Mounts = []mount.Mount{bindMount(Mount{
			HostPath:      spec.WorktreeHostPath,
			ContainerPath: spec.WorktreeMountPath,
		})}
	}
	for _, m := range spec.Mounts {
		hostCfg.Mounts = append(hostCfg.Mounts, bindMount(m))
	}
	return cfg, hostCfg
}

// bindMount translates one Mount into Docker create arguments using only
// the supported controls: the per-bind read-only flag and rprivate
// propagation (mount events never leak between host and container).
// Docker has no per-bind nosuid/nodev; see the ValidateMounts security
// note.
func bindMount(m Mount) mount.Mount {
	return mount.Mount{
		Type:        mount.TypeBind,
		Source:      m.HostPath,
		Target:      m.ContainerPath,
		ReadOnly:    m.ReadOnly,
		BindOptions: &mount.BindOptions{Propagation: mount.PropagationRPrivate},
	}
}

// newSetupSentinel returns a fresh unguessable sentinel path.
func newSetupSentinel() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // crypto/rand.Read does not fail
	}
	return setupSentinelPrefix + hex.EncodeToString(b[:])
}

// gateEntrypoint waits for the setup sentinel, then execs the container's
// Cmd (which docker appends as "$@") so the main command becomes PID 1.
// The fractional-sleep fallback covers strictly POSIX sleep utilities.
func gateEntrypoint(sentinel string) []string {
	script := fmt.Sprintf(`until [ -e %s ]; do sleep 0.1 2>/dev/null || sleep 1; done; exec "$@"`, sentinel)
	return []string{"/bin/sh", "-c", script, "aether-gate"}
}

// dockerEnv flattens an env map into sorted KEY=VALUE form.
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

// nanoCPUs converts a fractional core limit into Docker's NanoCPUs unit.
func nanoCPUs(cores float64) int64 {
	return int64(math.Round(cores * 1e9))
}

// Start implements Runtime. With a setup script present, Start returns only
// after the script has succeeded and the main command has been released; a
// nonzero setup exit kills the container and fails Start. Starting a
// container whose setup already completed (restart after Stop, an
// orchestrator retry against a live run) skips the script: the sentinel on
// the container filesystem marks it done.
func (d *Docker) Start(ctx context.Context, id ID) error {
	info, err := d.cli.ContainerInspect(ctx, string(id))
	if err != nil {
		return fmt.Errorf("runtime: inspect container: %w", err)
	}
	wasRunning := info.State != nil && info.State.Running
	if err := d.cli.ContainerStart(ctx, string(id), container.StartOptions{}); err != nil {
		// The daemon may have started the container even though the client
		// reports an error (a cancelled ctx mid-request); don't leave a
		// container this call launched running.
		if !wasRunning {
			_ = d.cli.ContainerKill(context.WithoutCancel(ctx), string(id), "KILL")
		}
		return fmt.Errorf("runtime: start container: %w", err)
	}
	script := info.Config.Labels[labelSetupScript]
	if script == "" {
		return nil
	}
	sentinel := info.Config.Labels[labelSetupSentinel]
	if err := d.runSetup(ctx, id, script, sentinel, info.Config.WorkingDir); err != nil {
		// Never kill a run that was already live before this Start call: a
		// redundant Start must not take down a healthy container.
		if !wasRunning {
			_ = d.cli.ContainerKill(context.WithoutCancel(ctx), string(id), "KILL")
		}
		return err
	}
	return nil
}

func (d *Docker) runSetup(ctx context.Context, id ID, script, sentinel, workDir string) error {
	code, output, err := d.exec(ctx, id, []string{"/bin/sh", "-c", "test -e " + sentinel}, "")
	if err != nil {
		slog.Error("runtime: setup gate probe failed", "container", id, "output", output, "error", err)
		return fmt.Errorf("runtime: probe setup gate: %w", err)
	}
	if code == 0 {
		return nil // setup already completed for this container
	}
	code, output, err = d.exec(ctx, id, []string{"/bin/sh", "-ec", script}, workDir)
	if err != nil {
		slog.Error("runtime: setup script execution failed", "container", id, "working_dir", workDir, "script", script, "output", output, "error", err)
		return fmt.Errorf("runtime: setup script: %w", err)
	}
	if code != 0 {
		slog.Error("runtime: setup script exited nonzero", "container", id, "working_dir", workDir, "script", script, "output", output, "exit_code", code)
		return fmt.Errorf("runtime: setup script exited %d: %s", code, strings.TrimSpace(output))
	}
	release := []string{"/bin/sh", "-c", "mkdir -p /tmp && : > " + sentinel}
	code, output, err = d.exec(ctx, id, release, "")
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

// exec runs cmd inside the container, blocking until it finishes or ctx is
// cancelled, and returns its exit code and combined output.
func (d *Docker) exec(ctx context.Context, id ID, cmd []string, workDir string) (int, string, error) {
	created, err := d.cli.ContainerExecCreate(ctx, string(id), container.ExecOptions{
		Cmd:          cmd,
		WorkingDir:   workDir,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return 0, "", fmt.Errorf("exec create: %w", err)
	}
	att, err := d.cli.ContainerExecAttach(ctx, created.ID, container.ExecAttachOptions{})
	if err != nil {
		return 0, "", fmt.Errorf("exec attach: %w", err)
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
	var buf bytes.Buffer
	_, copyErr := stdcopy.StdCopy(&buf, &buf, att.Reader)
	if err := ctx.Err(); err != nil {
		return 0, buf.String(), err
	}
	if copyErr != nil {
		return 0, buf.String(), fmt.Errorf("exec output: %w", copyErr)
	}
	for {
		ins, err := d.cli.ContainerExecInspect(ctx, created.ID)
		if err != nil {
			return 0, buf.String(), fmt.Errorf("exec inspect: %w", err)
		}
		if !ins.Running {
			return ins.ExitCode, buf.String(), nil
		}
		select {
		case <-ctx.Done():
			return 0, buf.String(), ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// Pause implements Runtime via the cgroup freezer (SIGSTOP semantics).
func (d *Docker) Pause(ctx context.Context, id ID) error {
	if err := d.cli.ContainerPause(ctx, string(id)); err != nil {
		return fmt.Errorf("runtime: pause container: %w", err)
	}
	return nil
}

// Resume implements Runtime.
func (d *Docker) Resume(ctx context.Context, id ID) error {
	if err := d.cli.ContainerUnpause(ctx, string(id)); err != nil {
		return fmt.Errorf("runtime: resume container: %w", err)
	}
	return nil
}

// Stop implements Runtime. A paused container is thawed first so the
// termination signal can be delivered.
func (d *Docker) Stop(ctx context.Context, id ID, grace time.Duration) error {
	info, err := d.cli.ContainerInspect(ctx, string(id))
	if err != nil {
		return fmt.Errorf("runtime: inspect container: %w", err)
	}
	if info.State != nil && info.State.Paused {
		if err := d.cli.ContainerUnpause(ctx, string(id)); err != nil {
			return fmt.Errorf("runtime: unpause before stop: %w", err)
		}
	}
	var opts container.StopOptions
	if grace >= 0 {
		secs := int(math.Ceil(grace.Seconds()))
		opts.Timeout = &secs
	}
	if err := d.cli.ContainerStop(ctx, string(id), opts); err != nil {
		return fmt.Errorf("runtime: stop container: %w", err)
	}
	return nil
}

// Destroy implements Runtime. Removal is forced, so running or paused
// containers are killed first; a missing container is not an error.
func (d *Docker) Destroy(ctx context.Context, id ID) error {
	err := d.cli.ContainerRemove(ctx, string(id), container.RemoveOptions{
		Force:         true,
		RemoveVolumes: true,
	})
	if err != nil && !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("runtime: destroy container: %w", err)
	}
	return nil
}

// Attach implements Runtime.
func (d *Docker) Attach(ctx context.Context, id ID) (Attachment, error) {
	info, err := d.cli.ContainerInspect(ctx, string(id))
	if err != nil {
		return nil, fmt.Errorf("runtime: inspect container: %w", err)
	}
	tty := info.Config != nil && info.Config.Tty
	resp, err := d.cli.ContainerAttach(ctx, string(id), container.AttachOptions{
		Stream: true,
		Stdin:  true,
		Stdout: true,
		Stderr: true,
	})
	if err != nil {
		return nil, fmt.Errorf("runtime: attach container: %w", err)
	}
	return newDockerAttachment(d.cli, string(id), tty, resp), nil
}

// Wait implements Runtime. A container that has never been started waits
// for its first run to finish rather than reporting a phantom zero exit.
func (d *Docker) Wait(ctx context.Context, id ID) (ExitStatus, error) {
	cond := container.WaitConditionNotRunning
	info, err := d.waitClient.ContainerInspect(ctx, string(id))
	if err != nil {
		return ExitStatus{}, dockerWaitError("inspect", id, err)
	}
	if info.State != nil && info.State.Status == container.StateCreated {
		cond = container.WaitConditionNextExit
	}
	respCh, errCh := d.waitClient.ContainerWait(ctx, string(id), cond)
	select {
	case err := <-errCh:
		return ExitStatus{}, dockerWaitError("wait", id, err)
	case resp := <-respCh:
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

// FindByCreationKey implements Runtime via the aether.creation-key label,
// matching containers in any state.
func (d *Docker) FindByCreationKey(ctx context.Context, key string) (ID, error) {
	if key == "" {
		return "", fmt.Errorf("runtime: find by creation key: %w", ErrNotFound)
	}
	list, err := d.cli.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("label", labelCreationKey+"="+key)),
	})
	if err != nil {
		return "", fmt.Errorf("runtime: find by creation key: %w", err)
	}
	if len(list) == 0 {
		return "", fmt.Errorf("runtime: find by creation key %q: %w", key, ErrNotFound)
	}
	return ID(list[0].ID), nil
}

// ImageUser reports the user the image is configured to run as (the OCI
// config User field: a name, uid, or uid:gid; empty means root). The
// image is pulled if not present locally.
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

// dockerAttachment adapts a hijacked attach connection. Without a TTY,
// Docker multiplexes stdout/stderr over one stream, demuxed here into two
// independently buffered streams so reading only one never stalls the
// other; with a TTY the raw merged stream feeds Stdout.
type dockerAttachment struct {
	cli       *client.Client
	id        string
	tty       bool
	resp      types.HijackedResponse
	stdout    *streamBuffer
	stderr    *streamBuffer
	closeOnce sync.Once
}

func newDockerAttachment(cli *client.Client, id string, tty bool, resp types.HijackedResponse) *dockerAttachment {
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
	err := a.cli.ContainerResize(ctx, a.id, container.ResizeOptions{Width: cols, Height: rows})
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

// hijackStdin writes to the container's stdin stream of one attachment.
// Close half-closes the connection: this attachment supplies no more input,
// but the container's stdin stays open (StdinOnce is false) so the process
// does not see EOF and later attachments can keep writing.
type hijackStdin struct {
	resp types.HijackedResponse
}

func (h hijackStdin) Write(p []byte) (int, error) { return h.resp.Conn.Write(p) }
func (h hijackStdin) Close() error                { return h.resp.CloseWrite() }

// maxStreamBuffer caps how much unread attachment output one stream holds.
const maxStreamBuffer = 8 << 20

// streamBuffer is an in-memory pipe: writes never block, reads block until
// data or close. Writes never block because blocking the demux goroutine on
// a slow reader would stall the sibling stream; consumers that do drain
// continuously (the PTY pump) never approach the cap, while one that stalls
// (a setup shell whose SSH peer stopped reading) would otherwise grow the
// buffer without limit, so past maxStreamBuffer the oldest bytes are dropped.
type streamBuffer struct {
	mu     sync.Mutex
	cond   *sync.Cond
	buf    bytes.Buffer
	closed bool
	err    error
}

func newStreamBuffer() *streamBuffer {
	b := &streamBuffer{}
	b.cond = sync.NewCond(&b.mu)
	return b
}

func (b *streamBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return 0, io.ErrClosedPipe
	}
	n, _ := b.buf.Write(p) // bytes.Buffer.Write cannot fail
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
		return b.buf.Read(p)
	}
	if b.err != nil {
		return 0, b.err
	}
	return 0, io.EOF
}

// CloseWithError ends the stream: buffered data remains readable, then
// readers get err (or io.EOF when err is nil). Only the first close takes
// effect.
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
