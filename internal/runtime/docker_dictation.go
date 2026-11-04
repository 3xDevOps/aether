package runtime

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"slices"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

const labelDictation = "aether.dictation"

// DictationSpec is the speech-to-text engine's container
// (docs/dictation.md): no network, a read-only root, every capability
// dropped, and only the host paths listed mounted.
type DictationSpec struct {
	Image      string
	Entrypoint []string
	User       string
	// ReadOnly and Writable map host paths to container paths.
	ReadOnly    map[string]string
	Writable    map[string]string
	CPUs        float64
	MemoryBytes int64
	GPU         bool
}

// DictationState is an engine container's state, and the tail of its
// output once it has exited.
type DictationState struct {
	Running   bool
	ExitCode  int
	OOMKilled bool
	Output    string
}

// PullDictationImage pulls ref unless the daemon already has it.
func (d *Docker) PullDictationImage(ctx context.Context, ref string) error {
	_, err := d.cli.ImageInspect(ctx, ref)
	if cerrdefs.IsNotFound(err) {
		return d.pull(ctx, ref)
	}
	if err != nil {
		return fmt.Errorf("runtime: inspect image %s: %w", ref, err)
	}
	return nil
}

// StartDictation removes any earlier engine container, then creates and
// starts one for spec, pulling its image if the daemon lacks it.
func (d *Docker) StartDictation(ctx context.Context, spec DictationSpec) (ID, error) {
	if err := d.RemoveDictation(ctx); err != nil {
		return "", err
	}
	if err := d.PullDictationImage(ctx, spec.Image); err != nil {
		return "", err
	}
	labels := maps.Clone(d.labels)
	if labels == nil {
		labels = make(map[string]string)
	}
	labels[labelManaged] = "true"
	labels[labelDictation] = "true"
	var mounts []mount.Mount
	for _, src := range slices.Sorted(maps.Keys(spec.ReadOnly)) {
		mounts = append(mounts, mount.Mount{Type: mount.TypeBind, Source: src, Target: spec.ReadOnly[src], ReadOnly: true})
	}
	for _, src := range slices.Sorted(maps.Keys(spec.Writable)) {
		mounts = append(mounts, mount.Mount{Type: mount.TypeBind, Source: src, Target: spec.Writable[src]})
	}
	initEnabled := true
	pids := int64(256)
	host := &container.HostConfig{
		Init:           &initEnabled,
		NetworkMode:    "none",
		IpcMode:        "private",
		ReadonlyRootfs: true,
		CapDrop:        []string{"ALL"},
		SecurityOpt:    []string{"no-new-privileges=true"},
		Mounts:         mounts,
		Resources: container.Resources{
			NanoCPUs: nanoCPUs(spec.CPUs), Memory: spec.MemoryBytes, MemorySwap: spec.MemoryBytes, PidsLimit: &pids,
		},
		LogConfig: container.LogConfig{Type: "local", Config: map[string]string{"max-size": "1m", "max-file": "2"}},
	}
	if spec.GPU {
		host.DeviceRequests = []container.DeviceRequest{{Count: -1, Capabilities: [][]string{{"gpu"}}}}
	}
	created, err := d.cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:     &container.Config{Image: spec.Image, User: spec.User, Entrypoint: spec.Entrypoint, Labels: labels},
		HostConfig: host,
		Name:       "aether-dictation",
	})
	if err != nil {
		return "", fmt.Errorf("runtime: create dictation engine container: %w", err)
	}
	id := ID(created.ID)
	if _, err := d.cli.ContainerStart(ctx, string(id), client.ContainerStartOptions{}); err != nil {
		_ = d.Destroy(context.WithoutCancel(ctx), id)
		return "", fmt.Errorf("runtime: start dictation engine container: %w", err)
	}
	return id, nil
}

// DictationState inspects the engine container id.
func (d *Docker) DictationState(ctx context.Context, id ID) (DictationState, error) {
	result, err := d.cli.ContainerInspect(ctx, string(id), client.ContainerInspectOptions{})
	if err != nil {
		return DictationState{}, dockerWaitError("inspect dictation engine", id, err)
	}
	st := result.Container.State
	if st == nil {
		return DictationState{}, fmt.Errorf("runtime: dictation engine %s has no state", id)
	}
	out := DictationState{Running: st.Running, ExitCode: st.ExitCode, OOMKilled: st.OOMKilled}
	if out.Running {
		return out, nil
	}
	logs, err := d.cli.ContainerLogs(ctx, string(id), client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true, Tail: "40"})
	if err != nil {
		return out, fmt.Errorf("runtime: read dictation engine output: %w", err)
	}
	defer func() { _ = logs.Close() }()
	var buf bytes.Buffer
	if _, err := stdcopy.StdCopy(&buf, &buf, logs); err != nil {
		return out, fmt.Errorf("runtime: read dictation engine output: %w", err)
	}
	out.Output = buf.String()
	return out, nil
}

// RemoveDictation force-removes every engine container, a leftover of an
// earlier server included.
func (d *Docker) RemoveDictation(ctx context.Context) error {
	list, err := d.cli.ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: make(client.Filters).Add("label", labelDictation+"=true"),
	})
	if err != nil {
		return fmt.Errorf("runtime: list dictation engine containers: %w", err)
	}
	for _, c := range list.Items {
		if err := d.Destroy(ctx, ID(c.ID)); err != nil {
			return err
		}
	}
	return nil
}
