//go:build integration

package runtime

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
)

const testImage = "busybox:1.36"

func newTestDocker(t *testing.T) *Docker {
	t.Helper()
	// Network mode "none": container networking is irrelevant to what these
	// tests exercise, and CI kernels do not always support veth pairs.
	d, err := NewDocker(
		WithLabels(map[string]string{"aether.test": t.Name()}),
		WithNetworkMode("none"),
	)
	if err != nil {
		t.Fatalf("NewDocker() error: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	if _, err := d.cli.Ping(t.Context()); err != nil {
		t.Fatalf("docker daemon unreachable (integration tests need real Docker): %v", err)
	}
	return d
}

func createContainer(t *testing.T, d *Docker, spec Spec) ID {
	t.Helper()
	id, err := d.Create(t.Context(), spec)
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := d.Destroy(ctx, id); err != nil {
			t.Errorf("cleanup Destroy() error: %v", err)
		}
	})
	return id
}

func createContainerWithoutInit(t *testing.T, d *Docker, spec Spec) ID {
	t.Helper()
	if _, err := d.cli.ImageInspect(t.Context(), spec.Image); err != nil {
		if !cerrdefs.IsNotFound(err) {
			t.Fatalf("inspect image %s: %v", spec.Image, err)
		}
		if err := d.pull(t.Context(), spec.Image); err != nil {
			t.Fatalf("pull image %s: %v", spec.Image, err)
		}
	}
	cfg, hostCfg := d.containerConfig(spec)
	initDisabled := false
	hostCfg.Init = &initDisabled
	resp, err := d.cli.ContainerCreate(t.Context(), cfg, hostCfg, nil, nil, d.namePrefix+spec.Name)
	if err != nil {
		t.Fatalf("ContainerCreate() without init: %v", err)
	}
	id := ID(resp.ID)
	t.Cleanup(func() {
		// Force removal also kills an orphan probe if its context was
		// cancelled while the probe was still polling.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := d.Destroy(ctx, id); err != nil {
			t.Errorf("cleanup Destroy() error: %v", err)
		}
	})
	return id
}

// readLines pumps attachment stdout lines into a channel until EOF.
func readLines(att Attachment) <-chan string {
	lines := make(chan string, 256)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(att.Stdout())
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()
	return lines
}

func waitLine(t *testing.T, lines <-chan string, timeout time.Duration, want string) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatalf("stdout closed before line %q", want)
			}
			if strings.Contains(line, want) {
				return
			}
		case <-deadline:
			t.Fatalf("no line %q within %v", want, timeout)
		}
	}
}

// drain discards buffered lines until the stream stays silent for quiet.
func drain(lines <-chan string, quiet time.Duration) {
	for {
		select {
		case <-lines:
		case <-time.After(quiet):
			return
		}
	}
}

// TestDockerLifecycle drives the full container lifecycle against real
// Docker: create -> attach -> start (setup hook first) -> pause (frozen) ->
// resume -> stop -> wait -> destroy, with the worktree bind mount and env
// vars observable from both sides.
func TestDockerLifecycle(t *testing.T) {
	t.Parallel()
	d := newTestDocker(t)
	worktree := t.TempDir()

	spec := Spec{
		Name:              fmt.Sprintf("it-lifecycle-%d", time.Now().UnixNano()),
		Image:             testImage,
		Env:               map[string]string{"AETHER_TEST_ENV": "from-env"},
		WorktreeHostPath:  worktree,
		WorktreeMountPath: "/workspace",
		WorkingDir:        "/workspace",
		SetupScript:       `echo "setup:$AETHER_TEST_ENV" > setup.txt`,
		Command: []string{"/bin/sh", "-c",
			`test -f setup.txt || exit 42; i=0; while true; do echo "tick $i"; i=$((i+1)); sleep 0.1; done`},
	}
	id := createContainer(t, d, spec)

	att, err := d.Attach(t.Context(), id)
	if err != nil {
		t.Fatalf("Attach() error: %v", err)
	}
	defer att.Close()
	lines := readLines(att)

	if err := d.Start(t.Context(), id); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	// Setup ran before the main command, saw the env and the rw mount.
	got, err := os.ReadFile(filepath.Join(worktree, "setup.txt"))
	if err != nil {
		t.Fatalf("setup script output not visible on host: %v", err)
	}
	if want := "setup:from-env\n"; string(got) != want {
		t.Fatalf("setup.txt = %q, want %q", got, want)
	}

	waitLine(t, lines, 10*time.Second, "tick 1")

	// Pause: the container reports frozen and output stops.
	if err := d.Pause(t.Context(), id); err != nil {
		t.Fatalf("Pause() error: %v", err)
	}
	info, err := d.cli.ContainerInspect(t.Context(), string(id))
	if err != nil {
		t.Fatalf("inspect after pause: %v", err)
	}
	if !info.State.Paused {
		t.Fatal("container not reported paused after Pause()")
	}
	drain(lines, 300*time.Millisecond)
	select {
	case line, ok := <-lines:
		if ok {
			t.Fatalf("output %q while paused, process not frozen", line)
		}
		t.Fatal("stdout closed while paused")
	case <-time.After(700 * time.Millisecond):
	}

	// Resume: output flows again.
	if err := d.Resume(t.Context(), id); err != nil {
		t.Fatalf("Resume() error: %v", err)
	}
	waitLine(t, lines, 5*time.Second, "tick")

	// Stop, then Wait reports the exit.
	if err := d.Stop(t.Context(), id, 2*time.Second); err != nil {
		t.Fatalf("Stop() error: %v", err)
	}
	status, err := d.Wait(t.Context(), id)
	if err != nil {
		t.Fatalf("Wait() error: %v", err)
	}
	if status.Code == 0 {
		t.Errorf("Wait().Code = 0, want nonzero for a signalled loop")
	}
	info, err = d.cli.ContainerInspect(t.Context(), string(id))
	if err != nil {
		t.Fatalf("inspect after stop: %v", err)
	}
	if info.State.Running {
		t.Fatal("container still running after Stop()")
	}

	// Destroy removes it entirely.
	if err := d.Destroy(t.Context(), id); err != nil {
		t.Fatalf("Destroy() error: %v", err)
	}
	if _, err := d.cli.ContainerInspect(t.Context(), string(id)); err == nil {
		t.Fatal("container still inspectable after Destroy()")
	}
	if err := d.Destroy(t.Context(), id); err != nil {
		t.Fatalf("second Destroy() error: %v, want idempotent nil", err)
	}
}

// TestDockerResourceLimits verifies cpu/mem limits land on the created
// container.
func TestDockerResourceLimits(t *testing.T) {
	t.Parallel()
	d := newTestDocker(t)

	spec := Spec{
		Name:             fmt.Sprintf("it-limits-%d", time.Now().UnixNano()),
		Image:            testImage,
		Command:          []string{"sleep", "30"},
		CPULimit:         0.5,
		MemoryLimitBytes: 128 << 20,
	}
	id := createContainer(t, d, spec)

	info, err := d.cli.ContainerInspect(t.Context(), string(id))
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if got := info.HostConfig.NanoCPUs; got != 500_000_000 {
		t.Errorf("NanoCPUs = %d, want 500000000", got)
	}
	if got := info.HostConfig.Memory; got != 128<<20 {
		t.Errorf("Memory = %d, want %d", got, 128<<20)
	}
}

// TestDockerSetupFailure verifies a nonzero setup exit fails Start and the
// main command never runs.
func TestDockerSetupFailure(t *testing.T) {
	t.Parallel()
	d := newTestDocker(t)
	worktree := t.TempDir()

	spec := Spec{
		Name:              fmt.Sprintf("it-setupfail-%d", time.Now().UnixNano()),
		Image:             testImage,
		WorktreeHostPath:  worktree,
		WorktreeMountPath: "/workspace",
		WorkingDir:        "/workspace",
		SetupScript:       "echo doomed >&2; exit 7",
		Command:           []string{"/bin/sh", "-c", "touch /workspace/main-ran; sleep 30"},
	}
	id := createContainer(t, d, spec)

	err := d.Start(t.Context(), id)
	if err == nil {
		t.Fatal("Start() = nil error, want setup failure")
	}
	for _, want := range []string{"exited 7", "doomed"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Start() error %q missing %q", err, want)
		}
	}
	info, ierr := d.cli.ContainerInspect(t.Context(), string(id))
	if ierr != nil {
		t.Fatalf("inspect: %v", ierr)
	}
	if info.State.Running {
		t.Error("container still running after failed setup")
	}
	if _, serr := os.Stat(filepath.Join(worktree, "main-ran")); serr == nil {
		t.Error("main command ran despite setup failure")
	}
}

// TestDockerStdinAndExitCode verifies stdin streaming and that Wait
// reports the real exit code.
func TestDockerStdinAndExitCode(t *testing.T) {
	t.Parallel()
	d := newTestDocker(t)

	spec := Spec{
		Name:    fmt.Sprintf("it-stdin-%d", time.Now().UnixNano()),
		Image:   testImage,
		Command: []string{"/bin/sh", "-c", `read line; echo "got:$line"; exit 5`},
	}
	id := createContainer(t, d, spec)

	att, err := d.Attach(t.Context(), id)
	if err != nil {
		t.Fatalf("Attach() error: %v", err)
	}
	defer att.Close()
	lines := readLines(att)

	if err := d.Start(t.Context(), id); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	if _, err := att.Stdin().Write([]byte("hello over stdin\n")); err != nil {
		t.Fatalf("stdin write: %v", err)
	}
	waitLine(t, lines, 10*time.Second, "got:hello over stdin")

	status, err := d.Wait(t.Context(), id)
	if err != nil {
		t.Fatalf("Wait() error: %v", err)
	}
	if status.Code != 5 {
		t.Errorf("Wait().Code = %d, want 5", status.Code)
	}
}

// TestDockerDetachReattach verifies the Attachment contract: Close()
// detaches without stopping the container or ending its stdin, and a later
// Attach can keep supplying input.
func TestDockerDetachReattach(t *testing.T) {
	t.Parallel()
	d := newTestDocker(t)

	spec := Spec{
		Name:    fmt.Sprintf("it-reattach-%d", time.Now().UnixNano()),
		Image:   testImage,
		Command: []string{"cat"},
	}
	id := createContainer(t, d, spec)

	att, err := d.Attach(t.Context(), id)
	if err != nil {
		t.Fatalf("Attach() error: %v", err)
	}
	if err := d.Start(t.Context(), id); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	lines := readLines(att)
	if _, err := att.Stdin().Write([]byte("first\n")); err != nil {
		t.Fatalf("stdin write: %v", err)
	}
	waitLine(t, lines, 10*time.Second, "first")

	// Detach: the stdin-reading main process must survive.
	if err := att.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}
	time.Sleep(1 * time.Second)
	info, err := d.cli.ContainerInspect(t.Context(), string(id))
	if err != nil {
		t.Fatalf("inspect after detach: %v", err)
	}
	if !info.State.Running {
		t.Fatalf("container %s after detach, want running: Close() must never stop the container", info.State.Status)
	}

	// Re-attach: stdin still works.
	att2, err := d.Attach(t.Context(), id)
	if err != nil {
		t.Fatalf("re-Attach() error: %v", err)
	}
	defer att2.Close()
	lines2 := readLines(att2)
	if _, err := att2.Stdin().Write([]byte("second\n")); err != nil {
		t.Fatalf("stdin write after reattach: %v", err)
	}
	waitLine(t, lines2, 10*time.Second, "second")
}

// TestDockerStderrFloodDoesNotBlockStdout verifies Stdout and Stderr are
// independently buffered: a stdout-only reader still gets its data while
// stderr goes undrained.
func TestDockerStderrFloodDoesNotBlockStdout(t *testing.T) {
	t.Parallel()
	d := newTestDocker(t)

	spec := Spec{
		Name:  fmt.Sprintf("it-demux-%d", time.Now().UnixNano()),
		Image: testImage,
		Command: []string{"/bin/sh", "-c",
			`i=0; while [ $i -lt 2000 ]; do echo "err $i" >&2; i=$((i+1)); done; echo MARKER; sleep 30`},
	}
	id := createContainer(t, d, spec)

	att, err := d.Attach(t.Context(), id)
	if err != nil {
		t.Fatalf("Attach() error: %v", err)
	}
	defer att.Close()
	lines := readLines(att) // reads Stdout only; stderr is never drained

	if err := d.Start(t.Context(), id); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	waitLine(t, lines, 10*time.Second, "MARKER")
}

// TestDockerWaitBeforeStart verifies Wait on a created-but-not-started
// container blocks until the main process actually exits instead of
// reporting a phantom code 0.
func TestDockerWaitBeforeStart(t *testing.T) {
	t.Parallel()
	d := newTestDocker(t)

	spec := Spec{
		Name:    fmt.Sprintf("it-waitcreated-%d", time.Now().UnixNano()),
		Image:   testImage,
		Command: []string{"/bin/sh", "-c", "sleep 1; exit 9"},
	}
	id := createContainer(t, d, spec)

	type result struct {
		status ExitStatus
		err    error
	}
	done := make(chan result, 1)
	go func() {
		st, err := d.Wait(context.Background(), id)
		done <- result{st, err}
	}()

	select {
	case r := <-done:
		t.Fatalf("Wait() returned %+v, %v before Start; want it to block", r.status, r.err)
	case <-time.After(2 * time.Second):
	}

	if err := d.Start(t.Context(), id); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("Wait() error: %v", r.err)
		}
		if r.status.Code != 9 {
			t.Errorf("Wait().Code = %d, want 9", r.status.Code)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Wait() did not return after the process exited")
	}
}

// TestDockerStartCancelDuringSetup verifies a hung setup script cannot
// block Start past ctx cancellation, and that the container is killed.
func TestDockerStartCancelDuringSetup(t *testing.T) {
	t.Parallel()
	d := newTestDocker(t)

	spec := Spec{
		Name:        fmt.Sprintf("it-setupcancel-%d", time.Now().UnixNano()),
		Image:       testImage,
		SetupScript: "sleep 600",
		Command:     []string{"true"},
	}
	id := createContainer(t, d, spec)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- d.Start(ctx, id) }()
	time.Sleep(500 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("Start() = nil error after ctx cancel, want error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start() still blocked 5s after ctx cancel")
	}

	deadline := time.After(10 * time.Second)
	for {
		info, err := d.cli.ContainerInspect(context.Background(), string(id))
		if err != nil {
			t.Fatalf("inspect: %v", err)
		}
		if !info.State.Running {
			return
		}
		select {
		case <-deadline:
			t.Fatal("container still running after cancelled Start; want it killed")
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// lostStartReplyTransport lets the container /start request reach the
// daemon, then reports a cancelled context to the caller - what a Start
// whose ctx is cancelled mid-request sees on a slow machine.
type lostStartReplyTransport struct {
	base http.RoundTripper
}

func (f *lostStartReplyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := f.base.RoundTrip(req)
	if err == nil && req.Method == http.MethodPost &&
		strings.HasSuffix(req.URL.Path, "/start") && strings.Contains(req.URL.Path, "/containers/") {
		resp.Body.Close() //nolint:errcheck // response is discarded by design
		return nil, fmt.Errorf("lost reply: %w", context.Canceled)
	}
	return resp, err
}

// TestDockerStartKillsAfterLostStartReply pins the race the cancel test
// only hits on slow machines: the daemon acts on ContainerStart but the
// client comes back with a ctx error, and Start must still not leave the
// container it launched running.
func TestDockerStartKillsAfterLostStartReply(t *testing.T) {
	t.Parallel()
	sock := strings.TrimPrefix(client.DefaultDockerHost, "unix://")
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		},
	}
	cli, err := client.NewClientWithOpts(
		client.WithHost(client.DefaultDockerHost),
		client.WithHTTPClient(&http.Client{Transport: &lostStartReplyTransport{base: tr}}),
		client.WithAPIVersionNegotiation(),
	)
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	d := &Docker{
		cli:         cli,
		waitClient:  cli,
		namePrefix:  defaultNamePrefix,
		labels:      map[string]string{"aether.test": t.Name()},
		networkMode: "none",
	}
	t.Cleanup(func() { d.Close() })
	if _, err := d.cli.Ping(t.Context()); err != nil {
		t.Fatalf("docker daemon unreachable (integration tests need real Docker): %v", err)
	}

	spec := Spec{
		Name:        fmt.Sprintf("it-startrace-%d", time.Now().UnixNano()),
		Image:       testImage,
		SetupScript: "sleep 600",
		Command:     []string{"true"},
	}
	id := createContainer(t, d, spec)

	if err := d.Start(context.Background(), id); !errors.Is(err, context.Canceled) {
		t.Fatalf("Start() error = %v, want the lost reply's context.Canceled", err)
	}

	deadline := time.After(10 * time.Second)
	for {
		info, err := d.cli.ContainerInspect(context.Background(), string(id))
		if err != nil {
			t.Fatalf("inspect: %v", err)
		}
		if !info.State.Running {
			return
		}
		select {
		case <-deadline:
			t.Fatal("container still running after the failed Start; want it killed")
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// TestDockerStartTwiceSkipsSetup verifies a redundant Start against a live
// run neither reruns a non-idempotent setup script nor kills the container.
func TestDockerStartTwiceSkipsSetup(t *testing.T) {
	t.Parallel()
	d := newTestDocker(t)
	worktree := t.TempDir()

	spec := Spec{
		Name:              fmt.Sprintf("it-doublestart-%d", time.Now().UnixNano()),
		Image:             testImage,
		WorktreeHostPath:  worktree,
		WorktreeMountPath: "/workspace",
		WorkingDir:        "/workspace",
		// Non-idempotent on purpose: rerunning it would exit nonzero.
		SetupScript: "mkdir only-once",
		Command:     []string{"sleep", "30"},
	}
	id := createContainer(t, d, spec)

	if err := d.Start(t.Context(), id); err != nil {
		t.Fatalf("first Start() error: %v", err)
	}
	if err := d.Start(t.Context(), id); err != nil {
		t.Fatalf("second Start() error: %v (setup must not rerun)", err)
	}
	info, err := d.cli.ContainerInspect(t.Context(), string(id))
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if !info.State.Running {
		t.Fatalf("container %s after second Start, want running", info.State.Status)
	}
}

// TestDockerTTY verifies Spec.TTY: merged output arrives on Stdout and
// Resize adjusts the terminal.
func TestDockerTTY(t *testing.T) {
	t.Parallel()
	d := newTestDocker(t)

	spec := Spec{
		Name:    fmt.Sprintf("it-tty-%d", time.Now().UnixNano()),
		Image:   testImage,
		TTY:     true,
		Command: []string{"/bin/sh", "-c", "echo to-stdout; echo to-stderr >&2; sleep 30"},
	}
	id := createContainer(t, d, spec)

	att, err := d.Attach(t.Context(), id)
	if err != nil {
		t.Fatalf("Attach() error: %v", err)
	}
	defer att.Close()
	lines := readLines(att)

	if err := d.Start(t.Context(), id); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	// Both writes arrive merged on the TTY stream, i.e. on Stdout.
	waitLine(t, lines, 10*time.Second, "to-stdout")
	waitLine(t, lines, 10*time.Second, "to-stderr")

	if err := att.Resize(t.Context(), 120, 40); err != nil {
		t.Fatalf("Resize() error: %v", err)
	}
	info, err := d.cli.ContainerInspect(t.Context(), string(id))
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if !info.Config.Tty {
		t.Error("container created without Tty despite Spec.TTY")
	}
}

// TestDockerPullIfMissing removes a pinned tag and verifies Create pulls
// it back. The image must actually be absent before Create, otherwise the
// pull branch is not exercised and the test proves nothing.
func TestDockerPullIfMissing(t *testing.T) {
	t.Parallel()
	d := newTestDocker(t)
	const pullImage = "busybox:1.35"

	if _, err := d.cli.ImageRemove(t.Context(), pullImage, image.RemoveOptions{}); err != nil {
		_, err = d.cli.ImageRemove(t.Context(), pullImage, image.RemoveOptions{Force: true})
		if err != nil && !cerrdefs.IsNotFound(err) {
			t.Logf("force remove %s: %v", pullImage, err)
		}
	}
	if _, err := d.cli.ImageInspect(t.Context(), pullImage); err == nil {
		t.Skipf("image %s still present after removal (referenced by another container?); cannot exercise the pull path", pullImage)
	} else if !cerrdefs.IsNotFound(err) {
		t.Fatalf("inspect %s: %v", pullImage, err)
	}

	spec := Spec{
		Name:    fmt.Sprintf("it-pull-%d", time.Now().UnixNano()),
		Image:   pullImage,
		Command: []string{"true"},
	}
	id := createContainer(t, d, spec)

	if err := d.Start(t.Context(), id); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	if status, err := d.Wait(t.Context(), id); err != nil || status.Code != 0 {
		t.Fatalf("Wait() = %+v, %v; want code 0", status, err)
	}
}

// TestDockerAdditionalMounts verifies additional Mounts land in the
// container with the pinned construction: read-only enforced by the
// per-bind flag, writable mounts writable, rprivate propagation.
func TestDockerAdditionalMounts(t *testing.T) {
	t.Parallel()
	d := newTestDocker(t)
	worktree := t.TempDir()
	cred := t.TempDir()
	roDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(roDir, "seed.txt"), []byte("seeded\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	spec := Spec{
		Name:              fmt.Sprintf("it-mounts-%d", time.Now().UnixNano()),
		Image:             testImage,
		WorktreeHostPath:  worktree,
		WorktreeMountPath: "/workspace",
		WorkingDir:        "/workspace",
		Mounts: []Mount{
			{HostPath: cred, ContainerPath: "/root/.claude"},
			{HostPath: roDir, ContainerPath: "/opt/profile", ReadOnly: true},
		},
		Command: []string{"/bin/sh", "-c",
			`cat /opt/profile/seed.txt; echo refreshed-token > /root/.claude/creds.json; ` +
				`if echo nope > /opt/profile/write.txt 2>/dev/null; then echo RW-LEAK; else echo RO-OK; fi; sleep 1`},
	}
	id := createContainer(t, d, spec)

	att, err := d.Attach(t.Context(), id)
	if err != nil {
		t.Fatalf("Attach() error: %v", err)
	}
	defer att.Close()
	lines := readLines(att)
	if err := d.Start(t.Context(), id); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	waitLine(t, lines, 10*time.Second, "seeded")
	waitLine(t, lines, 10*time.Second, "RO-OK")
	if _, err := d.Wait(t.Context(), id); err != nil {
		t.Fatalf("Wait() error: %v", err)
	}

	// The writable credential mount persisted the container's write.
	got, err := os.ReadFile(filepath.Join(cred, "creds.json"))
	if err != nil || strings.TrimSpace(string(got)) != "refreshed-token" {
		t.Fatalf("credential write did not persist: %q, %v", got, err)
	}
	// The pinned construction is visible on the created container.
	info, err := d.cli.ContainerInspect(t.Context(), string(id))
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if len(info.HostConfig.Mounts) != 3 {
		t.Fatalf("Mounts = %v", info.HostConfig.Mounts)
	}
	for i, m := range info.HostConfig.Mounts {
		if m.BindOptions == nil || m.BindOptions.Propagation != "rprivate" {
			t.Errorf("mount %d propagation = %+v, want rprivate", i, m.BindOptions)
		}
	}
}

// TestDockerFindByCreationKey verifies the crash-window lookup: a created
// (never started) container is found by its unique creation key, a
// missing key reports ErrNotFound, and a destroyed container is gone.
func TestDockerFindByCreationKey(t *testing.T) {
	t.Parallel()
	d := newTestDocker(t)

	key := fmt.Sprintf("it-ck-%d", time.Now().UnixNano())
	spec := Spec{
		Name:        fmt.Sprintf("it-creationkey-%d", time.Now().UnixNano()),
		Image:       testImage,
		Command:     []string{"true"},
		CreationKey: key,
	}
	id := createContainer(t, d, spec)

	found, err := d.FindByCreationKey(t.Context(), key)
	if err != nil {
		t.Fatalf("FindByCreationKey() error: %v", err)
	}
	if found != id {
		t.Fatalf("FindByCreationKey() = %s, want %s", found, id)
	}
	if _, err := d.FindByCreationKey(t.Context(), key+"-absent"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("FindByCreationKey(absent) = %v, want ErrNotFound", err)
	}
	if _, err := d.FindByCreationKey(t.Context(), ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("FindByCreationKey(\"\") = %v, want ErrNotFound", err)
	}
	if err := d.Destroy(t.Context(), id); err != nil {
		t.Fatalf("Destroy() error: %v", err)
	}
	if _, err := d.FindByCreationKey(t.Context(), key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("FindByCreationKey(destroyed) = %v, want ErrNotFound", err)
	}
}

// TestDockerRunUser verifies Spec.User: the main process runs as the
// numeric uid:gid instead of the image default.
func TestDockerRunUser(t *testing.T) {
	t.Parallel()
	d := newTestDocker(t)

	spec := Spec{
		Name:    fmt.Sprintf("it-user-%d", time.Now().UnixNano()),
		Image:   testImage,
		User:    "1000:1000",
		Command: []string{"id", "-u"},
	}
	id := createContainer(t, d, spec)

	att, err := d.Attach(t.Context(), id)
	if err != nil {
		t.Fatalf("Attach() error: %v", err)
	}
	defer att.Close()
	lines := readLines(att)
	if err := d.Start(t.Context(), id); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	waitLine(t, lines, 10*time.Second, "1000")
}

// TestDockerImageUser verifies ImageUser reads the image's configured
// user, pulling the image if absent (busybox has none: empty means root).
func TestDockerImageUser(t *testing.T) {
	t.Parallel()
	d := newTestDocker(t)
	user, err := d.ImageUser(t.Context(), testImage)
	if err != nil {
		t.Fatalf("ImageUser() error: %v", err)
	}
	if user != "" {
		t.Fatalf("ImageUser(%s) = %q, want empty (root)", testImage, user)
	}
}

// TestDockerExecTTY exercises the run-shell seam against real Docker: an
// extra TTY process inside a running container, interactive stdin, resize,
// and the ExecExitError contract for a missing shell (exit 127).
func TestDockerExecTTY(t *testing.T) {
	t.Parallel()
	d := newTestDocker(t)

	worktree := t.TempDir()
	spec := Spec{
		Name:              fmt.Sprintf("it-exectty-%d", time.Now().UnixNano()),
		Image:             testImage,
		TTY:               true,
		WorktreeHostPath:  worktree,
		WorktreeMountPath: "/workspace",
		Command:           []string{"/bin/sh", "-c", "sleep 60"},
	}
	id := createContainer(t, d, spec)
	if err := d.Start(t.Context(), id); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	att, err := d.ExecTTY(t.Context(), id, []string{"/bin/sh", "-l"}, "/workspace", 100, 30)
	if err != nil {
		t.Fatalf("ExecTTY() error: %v", err)
	}
	defer att.Close()
	lines := readLines(att)

	if _, err := att.Stdin().Write([]byte("pwd\n")); err != nil {
		t.Fatalf("stdin write: %v", err)
	}
	waitLine(t, lines, 10*time.Second, "/workspace")

	if err := att.Resize(t.Context(), 120, 40); err != nil {
		t.Fatalf("Resize() error: %v", err)
	}
	if _, err := att.Stdin().Write([]byte("stty size\n")); err != nil {
		t.Fatalf("stdin write: %v", err)
	}
	waitLine(t, lines, 10*time.Second, "40 120")

	// The shell exiting ends the exec stream.
	if _, err := att.Stdin().Write([]byte("exit\n")); err != nil {
		t.Fatalf("stdin write: %v", err)
	}
	deadline := time.After(10 * time.Second)
	for open := true; open; {
		select {
		case _, ok := <-lines:
			open = ok
		case <-deadline:
			t.Fatal("exec stream did not end after the shell exited")
		}
	}

	// A missing executable must surface as ExecExitError so callers can
	// retry with /bin/sh.
	_, err = d.ExecTTY(t.Context(), id, []string{"/bin/bash", "-l"}, "/workspace", 80, 24)
	var exitErr *ExecExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("ExecTTY(missing bash) = %v, want *ExecExitError", err)
	}
	if exitErr.Code != 126 && exitErr.Code != 127 {
		t.Fatalf("ExecExitError.Code = %d, want 126 or 127", exitErr.Code)
	}
}

const (
	orphanDescendantCount = 8
	// The probe shares this budget across all polling phases. At 100ms per
	// iteration, it remains below dockerOrphanZombieCount's context timeout.
	orphanProbePolls = 240
)

func orphanWorkloadCommand() string {
	return fmt.Sprintf(`i=0
while [ "$i" -lt %d ]; do
  /bin/sh -c "/bin/sh -c 'echo \$\$ > /workspace/orphan.pid.$i; while [ ! -e /workspace/reap-release ]; do sleep 0.1; done; : > /workspace/orphan.done.$i' & exit 0" &
  i=$((i + 1))
done
echo $$ > /workspace/main.pid
echo READY
while [ ! -e /workspace/main-release ]; do sleep 0.1; done
exec tail -f /dev/null`, orphanDescendantCount)
}

func orphanReapProbeCommand() string {
	return fmt.Sprintf(`mode="$1"
case "$mode" in
  control|managed) ;;
  *) echo "orphan probe: invalid mode $mode" >&2; exit 2 ;;
esac

main=$(cat /workspace/main.pid 2>/dev/null) || {
  echo "orphan probe: missing /workspace/main.pid" >&2
  exit 1
}
[ -n "$main" ] || {
  echo "orphan probe: /workspace/main.pid is empty" >&2
  exit 1
}

budget=%d
: > /workspace/main-release
main_ready=0
while [ "$budget" -gt 0 ]; do
  if [ -r "/proc/$main/comm" ] && [ "$(cat "/proc/$main/comm" 2>/dev/null)" = "tail" ]; then
    main_ready=1
    break
  fi
  budget=$((budget - 1))
  sleep 0.1
done
if [ "$main_ready" -ne 1 ]; then
  echo "orphan probe: main process did not become tail before deadline" >&2
  exit 1
fi

pid_files_ready=0
while [ "$budget" -gt 0 ]; do
  pid_files_ready=1
  i=0
  while [ "$i" -lt %d ]; do
    if [ ! -s "/workspace/orphan.pid.$i" ]; then
      pid_files_ready=0
    fi
    i=$((i + 1))
  done
  if [ "$pid_files_ready" -eq 1 ]; then
    break
  fi
  budget=$((budget - 1))
  sleep 0.1
done
if [ "$pid_files_ready" -ne 1 ]; then
  echo "orphan probe: descendant pid files did not appear before deadline" >&2
  i=0
  while [ "$i" -lt %d ]; do
    if [ -s "/workspace/orphan.pid.$i" ]; then
      echo "orphan probe: orphan.pid.$i=$(cat "/workspace/orphan.pid.$i")" >&2
    else
      echo "orphan probe: orphan.pid.$i is missing" >&2
    fi
    i=$((i + 1))
  done
  exit 1
fi

# Every tracked descendant must still be alive before the release. This
# makes the later zombie/disappearance check an actual before/after probe.
i=0
while [ "$i" -lt %d ]; do
  pid=$(cat "/workspace/orphan.pid.$i" 2>/dev/null)
  state=
  if [ -r "/proc/$pid/status" ]; then
    state=$(sed -n 's/^State:[[:space:]]*\([^[:space:]]*\).*/\1/p' "/proc/$pid/status" 2>/dev/null)
  fi
  if [ -z "$state" ]; then
    echo "orphan probe: descendant $i pid $pid disappeared before release" >&2
    exit 1
  fi
  if [ "$state" = "Z" ]; then
    echo "orphan probe: descendant $i pid $pid was already a zombie before release" >&2
    exit 1
  fi
  i=$((i + 1))
done

: > /workspace/reap-release
zombies=0
if [ "$mode" = "managed" ]; then
  # Init may expose a short-lived Z state before it reaps the child. Only
  # success after every tracked /proc entry disappears is a clean result.
  all_gone=0
  while [ "$budget" -gt 0 ]; do
    all_gone=1
    i=0
    while [ "$i" -lt %d ]; do
      if [ ! -e "/workspace/orphan.done.$i" ]; then
        all_gone=0
      else
        pid=$(cat "/workspace/orphan.pid.$i" 2>/dev/null)
        if [ -e "/proc/$pid/status" ]; then
          all_gone=0
        fi
      fi
      i=$((i + 1))
    done
    if [ "$all_gone" -eq 1 ]; then
      break
    fi
    budget=$((budget - 1))
    sleep 0.1
  done
  if [ "$all_gone" -ne 1 ]; then
    echo "orphan probe: managed init did not reap descendants before deadline" >&2
    i=0
    while [ "$i" -lt %d ]; do
      pid=$(cat "/workspace/orphan.pid.$i" 2>/dev/null)
      if [ ! -e "/workspace/orphan.done.$i" ]; then
        echo "orphan probe: descendant $i pid $pid has not exited" >&2
      elif [ -e "/proc/$pid/status" ]; then
        state=$(sed -n 's/^State:[[:space:]]*\([^[:space:]]*\).*/\1/p' "/proc/$pid/status" 2>/dev/null)
        echo "orphan probe: descendant $i pid $pid remains in state $state" >&2
      fi
      i=$((i + 1))
    done
    exit 1
  fi
else
  # No init must retain each exited descendant as a zombie. Requiring all
  # tracked states at once avoids counting a transient Z in the managed case.
  all_zombies=0
  while [ "$budget" -gt 0 ]; do
    all_zombies=1
    zombies=0
    i=0
    while [ "$i" -lt %d ]; do
      if [ ! -e "/workspace/orphan.done.$i" ]; then
        all_zombies=0
      else
        pid=$(cat "/workspace/orphan.pid.$i" 2>/dev/null)
        state=
        if [ -r "/proc/$pid/status" ]; then
          state=$(sed -n 's/^State:[[:space:]]*\([^[:space:]]*\).*/\1/p' "/proc/$pid/status" 2>/dev/null)
        fi
        if [ "$state" = "Z" ]; then
          zombies=$((zombies + 1))
        else
          all_zombies=0
        fi
      fi
      i=$((i + 1))
    done
    if [ "$all_zombies" -eq 1 ]; then
      break
    fi
    budget=$((budget - 1))
    sleep 0.1
  done
  if [ "$all_zombies" -ne 1 ]; then
    echo "orphan probe: no-init descendants did not remain zombies before deadline" >&2
    i=0
    while [ "$i" -lt %d ]; do
      pid=$(cat "/workspace/orphan.pid.$i" 2>/dev/null)
      if [ ! -e "/workspace/orphan.done.$i" ]; then
        echo "orphan probe: descendant $i pid $pid has not exited" >&2
      elif [ -r "/proc/$pid/status" ]; then
        state=$(sed -n 's/^State:[[:space:]]*\([^[:space:]]*\).*/\1/p' "/proc/$pid/status" 2>/dev/null)
        echo "orphan probe: descendant $i pid $pid is in state $state" >&2
      else
        echo "orphan probe: descendant $i pid $pid is missing from /proc" >&2
      fi
      i=$((i + 1))
    done
    exit 1
  fi
fi
printf 'zombies:%%d\n' "$zombies"`, orphanProbePolls, orphanDescendantCount, orphanDescendantCount, orphanDescendantCount, orphanDescendantCount, orphanDescendantCount, orphanDescendantCount, orphanDescendantCount)
}

func dockerOrphanZombieCount(t *testing.T, d *Docker, id ID, expectReaped bool) int {
	t.Helper()
	mode := "control"
	if expectReaped {
		mode = "managed"
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	code, stdout, stderr, err := d.Exec(ctx, id, []string{"/bin/sh", "-c", orphanReapProbeCommand(), "orphan-reap-probe", mode}, "/workspace")
	if err != nil {
		t.Fatalf("orphan probe (%s) error: %v (stdout=%q stderr=%q)", mode, err, stdout, stderr)
	}
	if code != 0 {
		t.Fatalf("orphan probe (%s) exit code = %d (stdout=%q stderr=%q)", mode, code, stdout, stderr)
	}
	var zombies int
	if _, err := fmt.Sscanf(strings.TrimSpace(stdout), "zombies:%d", &zombies); err != nil {
		t.Fatalf("orphan probe (%s) output = %q, want zombies count: %v", mode, stdout, err)
	}
	return zombies
}

// TestDockerInitReapsOrphanedDescendants verifies Docker's minimal init
// adopts and reaps descendants left behind by a long-lived agent command.
// A matching no-init container proves the workload really leaves zombies;
// the managed container must keep its TTY and main process alive while
// reaping them, then still stop with a nonzero signal status.
func TestDockerInitReapsOrphanedDescendants(t *testing.T) {
	t.Parallel()
	d := newTestDocker(t)

	controlSpec := Spec{
		Name:              fmt.Sprintf("it-reap-control-%d", time.Now().UnixNano()),
		Image:             testImage,
		TTY:               true,
		WorktreeHostPath:  t.TempDir(),
		WorktreeMountPath: "/workspace",
		WorkingDir:        "/workspace",
		Command:           []string{"/bin/sh", "-c", orphanWorkloadCommand()},
	}
	controlID := createContainerWithoutInit(t, d, controlSpec)
	controlAtt, err := d.Attach(t.Context(), controlID)
	if err != nil {
		t.Fatalf("control Attach() error: %v", err)
	}
	defer controlAtt.Close()
	controlLines := readLines(controlAtt)
	if err := d.Start(t.Context(), controlID); err != nil {
		t.Fatalf("control Start() error: %v", err)
	}
	waitLine(t, controlLines, 10*time.Second, "READY")
	if got := dockerOrphanZombieCount(t, d, controlID, false); got == 0 {
		t.Fatal("no-init control left no zombies; workload did not exercise orphan reaping")
	}
	info, err := d.cli.ContainerInspect(t.Context(), string(controlID))
	if err != nil {
		t.Fatalf("control inspect after probe: %v", err)
	}
	if !info.State.Running {
		t.Fatalf("control container stopped after probe: %s", info.State.Status)
	}
	if err := d.Stop(t.Context(), controlID, 2*time.Second); err != nil {
		t.Fatalf("control Stop() error: %v", err)
	}
	controlStatus, err := d.Wait(t.Context(), controlID)
	if err != nil {
		t.Fatalf("control Wait() error: %v", err)
	}
	if controlStatus.Code == 0 {
		t.Fatalf("control Wait().Code = 0, want signal status")
	}

	managedSpec := controlSpec
	managedSpec.Name = fmt.Sprintf("it-reap-managed-%d", time.Now().UnixNano())
	managedSpec.WorktreeHostPath = t.TempDir()
	managedID := createContainer(t, d, managedSpec)
	managedAtt, err := d.Attach(t.Context(), managedID)
	if err != nil {
		t.Fatalf("managed Attach() error: %v", err)
	}
	defer managedAtt.Close()
	managedLines := readLines(managedAtt)
	if err := d.Start(t.Context(), managedID); err != nil {
		t.Fatalf("managed Start() error: %v", err)
	}
	waitLine(t, managedLines, 10*time.Second, "READY")
	if got := dockerOrphanZombieCount(t, d, managedID, true); got != 0 {
		t.Fatalf("managed orphan probe found %d zombies, want none", got)
	}
	info, err = d.cli.ContainerInspect(t.Context(), string(managedID))
	if err != nil {
		t.Fatalf("managed inspect after probe: %v", err)
	}
	if !info.State.Running {
		t.Fatalf("managed container stopped after probe: %s", info.State.Status)
	}
	if err := d.Stop(t.Context(), managedID, 2*time.Second); err != nil {
		t.Fatalf("managed Stop() error: %v", err)
	}
	managedStatus, err := d.Wait(t.Context(), managedID)
	if err != nil {
		t.Fatalf("managed Wait() error: %v", err)
	}
	if managedStatus.Code == 0 {
		t.Fatalf("managed Wait().Code = 0, want signal status")
	}
}
