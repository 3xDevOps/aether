package runtime

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
)

func TestNewDockerDefaults(t *testing.T) {
	d, err := NewDocker()
	if err != nil {
		t.Fatalf("NewDocker() error: %v", err)
	}
	defer func() { _ = d.Close() }()
	if d.namePrefix != defaultNamePrefix {
		t.Errorf("namePrefix = %q, want %q", d.namePrefix, defaultNamePrefix)
	}
	if d.labels != nil {
		t.Errorf("labels = %v, want nil", d.labels)
	}
}

func TestNewDockerOptions(t *testing.T) {
	labels := map[string]string{"aether.session": "s1"}
	d, err := NewDocker(WithNamePrefix("custom-"), WithLabels(labels), WithNetworkMode("none"))
	if err != nil {
		t.Fatalf("NewDocker() error: %v", err)
	}
	defer func() { _ = d.Close() }()
	if d.namePrefix != "custom-" {
		t.Errorf("namePrefix = %q, want %q", d.namePrefix, "custom-")
	}
	if d.networkMode != "none" {
		t.Errorf("networkMode = %q, want %q", d.networkMode, "none")
	}
	labels["aether.session"] = "mutated"
	if d.labels["aether.session"] != "s1" {
		t.Errorf("labels not cloned: %v", d.labels)
	}
}

type fakeDockerWaitClient struct {
	inspect func(context.Context, string) (container.InspectResponse, error)
	wait    func(context.Context, string, container.WaitCondition) (<-chan container.WaitResponse, <-chan error)
}

func (f fakeDockerWaitClient) ContainerInspect(ctx context.Context, id string) (container.InspectResponse, error) {
	return f.inspect(ctx, id)
}

func (f fakeDockerWaitClient) ContainerWait(ctx context.Context, id string, condition container.WaitCondition) (<-chan container.WaitResponse, <-chan error) {
	return f.wait(ctx, id, condition)
}

func dockerWaitInspectResponse() container.InspectResponse {
	return container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{
			State: &container.State{Status: container.StateRunning},
		},
	}
}

func TestDockerWaitNormalizesInspectNotFound(t *testing.T) {
	dockerErr := errors.Join(errors.New("inspect request failed"), cerrdefs.ErrNotFound)
	d := &Docker{waitClient: fakeDockerWaitClient{
		inspect: func(context.Context, string) (container.InspectResponse, error) {
			return container.InspectResponse{}, dockerErr
		},
		wait: func(context.Context, string, container.WaitCondition) (<-chan container.WaitResponse, <-chan error) {
			t.Fatal("ContainerWait called after inspect failed")
			return nil, nil
		},
	}}

	_, err := d.Wait(t.Context(), ID("missing"))
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Wait() error = %v, want ErrNotFound", err)
	}
	if !errors.Is(err, dockerErr) {
		t.Fatalf("Wait() error = %v, want wrapped Docker error", err)
	}
	if !strings.Contains(err.Error(), `inspect container "missing"`) {
		t.Fatalf("Wait() error = %q, want inspect context", err)
	}
}

func TestDockerWaitNormalizesWaitNotFound(t *testing.T) {
	dockerErr := errors.Join(errors.New("wait request failed"), cerrdefs.ErrNotFound)
	d := &Docker{waitClient: fakeDockerWaitClient{
		inspect: func(context.Context, string) (container.InspectResponse, error) {
			return dockerWaitInspectResponse(), nil
		},
		wait: func(_ context.Context, id string, condition container.WaitCondition) (<-chan container.WaitResponse, <-chan error) {
			if id != "missing" {
				t.Errorf("ContainerWait() id = %q, want missing", id)
			}
			if condition != container.WaitConditionNotRunning {
				t.Errorf("ContainerWait() condition = %q, want %q", condition, container.WaitConditionNotRunning)
			}
			respCh := make(chan container.WaitResponse)
			errCh := make(chan error, 1)
			errCh <- dockerErr
			return respCh, errCh
		},
	}}

	_, err := d.Wait(t.Context(), ID("missing"))
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Wait() error = %v, want ErrNotFound", err)
	}
	if !errors.Is(err, dockerErr) {
		t.Fatalf("Wait() error = %v, want wrapped Docker error", err)
	}
	if !strings.Contains(err.Error(), `wait container "missing"`) {
		t.Fatalf("Wait() error = %q, want wait context", err)
	}
}

func TestDockerWaitDoesNotNormalizeOtherErrors(t *testing.T) {
	inspectErr := errors.New("inspect unavailable")
	waitErr := errors.New("wait unavailable")
	tests := []struct {
		name       string
		inspectErr error
		waitErr    error
		want       error
	}{
		{name: "inspect", inspectErr: inspectErr, want: inspectErr},
		{name: "wait", waitErr: waitErr, want: waitErr},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &Docker{waitClient: fakeDockerWaitClient{
				inspect: func(context.Context, string) (container.InspectResponse, error) {
					return dockerWaitInspectResponse(), tt.inspectErr
				},
				wait: func(context.Context, string, container.WaitCondition) (<-chan container.WaitResponse, <-chan error) {
					respCh := make(chan container.WaitResponse)
					errCh := make(chan error, 1)
					errCh <- tt.waitErr
					return respCh, errCh
				},
			}}

			_, err := d.Wait(t.Context(), ID("container-1"))
			if !errors.Is(err, tt.want) {
				t.Fatalf("Wait() error = %v, want wrapped %v", err, tt.want)
			}
			if errors.Is(err, ErrNotFound) {
				t.Fatalf("Wait() error = %v, do not want ErrNotFound", err)
			}
		})
	}
}

func TestContainerConfigPlumbing(t *testing.T) {
	d := &Docker{
		namePrefix:  defaultNamePrefix,
		labels:      map[string]string{"aether.session": "s1"},
		networkMode: "none",
	}
	spec := validSpec()
	spec.SetupScript = ""
	cfg, hostCfg := d.containerConfig(spec)

	if cfg.Image != spec.Image {
		t.Errorf("Image = %q, want %q", cfg.Image, spec.Image)
	}
	if !slices.Equal(cfg.Entrypoint, spec.Command) {
		t.Errorf("Entrypoint = %v, want %v", cfg.Entrypoint, spec.Command)
	}
	if len(cfg.Cmd) != 0 {
		t.Errorf("Cmd = %v, want empty", cfg.Cmd)
	}
	if cfg.WorkingDir != "/workspace" {
		t.Errorf("WorkingDir = %q, want /workspace", cfg.WorkingDir)
	}
	if !slices.Equal(cfg.Env, []string{"FOO=bar"}) {
		t.Errorf("Env = %v, want [FOO=bar]", cfg.Env)
	}
	if !cfg.OpenStdin || cfg.StdinOnce {
		t.Errorf("OpenStdin/StdinOnce = %v/%v, want true/false (detach must not close stdin)", cfg.OpenStdin, cfg.StdinOnce)
	}
	if cfg.Tty {
		t.Error("Tty = true, want false without Spec.TTY")
	}
	if cfg.Labels[labelManaged] != "true" || cfg.Labels["aether.session"] != "s1" {
		t.Errorf("Labels = %v, want managed + runtime labels", cfg.Labels)
	}
	if _, ok := cfg.Labels[labelSetupScript]; ok {
		t.Errorf("Labels = %v, setup label present without setup script", cfg.Labels)
	}

	if hostCfg.NetworkMode != "none" {
		t.Errorf("NetworkMode = %q, want none", hostCfg.NetworkMode)
	}
	if len(hostCfg.Mounts) != 1 {
		t.Fatalf("Mounts = %v, want one worktree mount", hostCfg.Mounts)
	}
	got := hostCfg.Mounts[0]
	if got.Type != mount.TypeBind || got.Source != spec.WorktreeHostPath || got.Target != spec.WorktreeMountPath || got.ReadOnly {
		t.Errorf("worktree mount = %+v", got)
	}
	if got.BindOptions == nil || got.BindOptions.Propagation != mount.PropagationRPrivate {
		t.Errorf("worktree mount bind options = %+v, want rprivate propagation", got.BindOptions)
	}
	if hostCfg.NanoCPUs != 1_500_000_000 {
		t.Errorf("NanoCPUs = %d, want 1500000000", hostCfg.NanoCPUs)
	}
	if hostCfg.Memory != 64<<20 {
		t.Errorf("Memory = %d, want %d", hostCfg.Memory, 64<<20)
	}
}

func TestContainerConfigSetupGate(t *testing.T) {
	d := &Docker{namePrefix: defaultNamePrefix}
	spec := validSpec()
	cfg, _ := d.containerConfig(spec)

	sentinel := cfg.Labels[labelSetupSentinel]
	if !strings.HasPrefix(sentinel, setupSentinelPrefix) || len(sentinel) <= len(setupSentinelPrefix) {
		t.Fatalf("sentinel label = %q, want random path under %q", sentinel, setupSentinelPrefix)
	}
	if !slices.Equal(cfg.Entrypoint, gateEntrypoint(sentinel)) {
		t.Errorf("Entrypoint = %v, want gate entrypoint for %q", cfg.Entrypoint, sentinel)
	}
	if !slices.Equal(cfg.Cmd, spec.Command) {
		t.Errorf("Cmd = %v, want %v", cfg.Cmd, spec.Command)
	}
	if cfg.Labels[labelSetupScript] != spec.SetupScript {
		t.Errorf("setup label = %q, want %q", cfg.Labels[labelSetupScript], spec.SetupScript)
	}

	cfg2, _ := d.containerConfig(spec)
	if cfg2.Labels[labelSetupSentinel] == sentinel {
		t.Error("sentinel path repeated across creates, want unguessable per-container path")
	}
}

func TestContainerConfigTTY(t *testing.T) {
	d := &Docker{namePrefix: defaultNamePrefix}
	spec := validSpec()
	spec.TTY = true
	cfg, _ := d.containerConfig(spec)
	if !cfg.Tty {
		t.Error("Tty = false, want true with Spec.TTY")
	}
}

func TestResizeWithoutTTYFails(t *testing.T) {
	a := &dockerAttachment{tty: false}
	if err := a.Resize(t.Context(), 80, 24); err == nil {
		t.Fatal("Resize() on non-TTY attachment = nil error, want error")
	}
}

func TestContainerConfigNoMountNoLimits(t *testing.T) {
	d := &Docker{namePrefix: defaultNamePrefix}
	cfg, hostCfg := d.containerConfig(Spec{Image: "busybox", Command: []string{"true"}})
	if len(hostCfg.Mounts) != 0 {
		t.Errorf("Mounts = %v, want none", hostCfg.Mounts)
	}
	if hostCfg.NanoCPUs != 0 || hostCfg.Memory != 0 {
		t.Errorf("Resources = %+v, want zero", hostCfg.Resources)
	}
	if cfg.Env != nil {
		t.Errorf("Env = %v, want nil", cfg.Env)
	}
}

func TestDockerEnvSorted(t *testing.T) {
	got := dockerEnv(map[string]string{"ZED": "1", "ALPHA": "2", "MID": "3"})
	want := []string{"ALPHA=2", "MID=3", "ZED=1"}
	if !slices.Equal(got, want) {
		t.Errorf("dockerEnv = %v, want %v", got, want)
	}
}

func TestNanoCPUs(t *testing.T) {
	tests := []struct {
		cores float64
		want  int64
	}{
		{0, 0},
		{0.5, 500_000_000},
		{2, 2_000_000_000},
		{0.001, 1_000_000},
	}
	for _, tt := range tests {
		if got := nanoCPUs(tt.cores); got != tt.want {
			t.Errorf("nanoCPUs(%v) = %d, want %d", tt.cores, got, tt.want)
		}
	}
}

func TestStreamBufferWriteNeverBlocks(t *testing.T) {
	b := newStreamBuffer()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 10_000 {
			if _, err := b.Write([]byte("flood line with nobody reading\n")); err != nil {
				t.Errorf("Write() error: %v", err)
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Write blocked with no reader; streams must buffer independently")
	}
}

func TestStreamBufferDropsOldestPastCap(t *testing.T) {
	b := newStreamBuffer()
	chunk := bytes.Repeat([]byte("a"), 4096)
	for range 4 * maxStreamBuffer / len(chunk) {
		if _, err := b.Write(chunk); err != nil {
			t.Fatalf("Write() error: %v", err)
		}
	}
	last := bytes.Repeat([]byte("z"), len(chunk))
	if _, err := b.Write(last); err != nil {
		t.Fatalf("Write() error: %v", err)
	}
	b.CloseWithError(nil)
	data, err := io.ReadAll(b)
	if err != nil {
		t.Fatalf("ReadAll() error: %v", err)
	}
	if len(data) > maxStreamBuffer {
		t.Errorf("buffered %d bytes with nobody reading, want at most %d", len(data), maxStreamBuffer)
	}
	if !bytes.HasSuffix(data, last) {
		t.Error("newest bytes were dropped; the cap must drop the oldest")
	}
}

func TestStreamBufferReadBlocksUntilData(t *testing.T) {
	b := newStreamBuffer()
	got := make(chan string, 1)
	go func() {
		p := make([]byte, 16)
		n, err := b.Read(p)
		if err != nil {
			t.Errorf("Read() error: %v", err)
		}
		got <- string(p[:n])
	}()
	time.Sleep(50 * time.Millisecond)
	if _, err := b.Write([]byte("hello")); err != nil {
		t.Fatalf("Write() error: %v", err)
	}
	select {
	case s := <-got:
		if s != "hello" {
			t.Errorf("Read = %q, want %q", s, "hello")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Read did not return after Write")
	}
}

func TestStreamBufferCloseDrainsThenEOF(t *testing.T) {
	b := newStreamBuffer()
	if _, err := b.Write([]byte("tail")); err != nil {
		t.Fatalf("Write() error: %v", err)
	}
	b.CloseWithError(nil)
	if _, err := b.Write([]byte("x")); !errors.Is(err, io.ErrClosedPipe) {
		t.Errorf("Write after close = %v, want io.ErrClosedPipe", err)
	}
	data, err := io.ReadAll(b)
	if err != nil {
		t.Fatalf("ReadAll() error: %v", err)
	}
	if string(data) != "tail" {
		t.Errorf("buffered data = %q, want %q", data, "tail")
	}
}

func TestStreamBufferCloseWithError(t *testing.T) {
	b := newStreamBuffer()
	want := errors.New("boom")
	b.CloseWithError(want)
	b.CloseWithError(nil) // only the first close takes effect
	if _, err := b.Read(make([]byte, 1)); !errors.Is(err, want) {
		t.Errorf("Read after CloseWithError = %v, want %v", err, want)
	}
}

func TestStreamBufferConcurrent(t *testing.T) {
	b := newStreamBuffer()
	const writers, lines = 8, 200
	var wg sync.WaitGroup
	for range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range lines {
				if _, err := b.Write([]byte("line\n")); err != nil {
					t.Errorf("Write() error: %v", err)
					return
				}
			}
		}()
	}
	go func() { wg.Wait(); b.CloseWithError(nil) }()
	data, err := io.ReadAll(b)
	if err != nil {
		t.Fatalf("ReadAll() error: %v", err)
	}
	if got, want := len(data), writers*lines*len("line\n"); got != want {
		t.Errorf("read %d bytes, want %d", got, want)
	}
}

func TestCreateRejectsInvalidSpec(t *testing.T) {
	d, err := NewDocker()
	if err != nil {
		t.Fatalf("NewDocker() error: %v", err)
	}
	defer func() { _ = d.Close() }()
	if _, err := d.Create(t.Context(), Spec{}); err == nil {
		t.Fatal("Create(invalid spec) = nil error, want validation error")
	}
}

func TestContainerConfigUserAndCreationKey(t *testing.T) {
	d := &Docker{namePrefix: defaultNamePrefix}
	spec := validSpec()
	spec.User = "1000:1000"
	spec.CreationKey = "run_42"
	cfg, _ := d.containerConfig(spec)
	if cfg.User != "1000:1000" {
		t.Errorf("User = %q, want 1000:1000", cfg.User)
	}
	if cfg.Labels[labelCreationKey] != "run_42" {
		t.Errorf("creation-key label = %q, want run_42", cfg.Labels[labelCreationKey])
	}

	cfg, _ = d.containerConfig(validSpec())
	if cfg.User != "" {
		t.Errorf("User = %q, want empty (image default)", cfg.User)
	}
	if _, ok := cfg.Labels[labelCreationKey]; ok {
		t.Error("creation-key label present without Spec.CreationKey")
	}
}

func TestContainerConfigAdditionalMounts(t *testing.T) {
	d := &Docker{namePrefix: defaultNamePrefix}
	spec := validSpec()
	spec.Mounts = []Mount{
		{HostPath: "/srv/aether/homes/m1/claude/root/.claude", ContainerPath: "/root/.claude"},
		{HostPath: "/srv/aether/profiles/m1", ContainerPath: "/opt/profile", ReadOnly: true},
	}
	_, hostCfg := d.containerConfig(spec)
	if len(hostCfg.Mounts) != 3 {
		t.Fatalf("Mounts = %v, want worktree + 2 additional", hostCfg.Mounts)
	}
	for i, m := range hostCfg.Mounts {
		if m.Type != mount.TypeBind {
			t.Errorf("mount %d type = %q", i, m.Type)
		}
		if m.BindOptions == nil || m.BindOptions.Propagation != mount.PropagationRPrivate {
			t.Errorf("mount %d bind options = %+v, want rprivate", i, m.BindOptions)
		}
	}
	cred, prof := hostCfg.Mounts[1], hostCfg.Mounts[2]
	if cred.Source != spec.Mounts[0].HostPath || cred.Target != "/root/.claude" || cred.ReadOnly {
		t.Errorf("credential mount = %+v", cred)
	}
	if prof.Target != "/opt/profile" || !prof.ReadOnly {
		t.Errorf("profile mount = %+v", prof)
	}
}
