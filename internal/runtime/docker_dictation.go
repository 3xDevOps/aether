package runtime

import (
	"context"
	"fmt"
	"io"
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
	// Output receives the engine's stdout and stderr while it runs. Docker
	// keeps none of it.
	Output io.Writer
}

// DictationState is an engine container's state.
type DictationState struct {
	Running   bool
	ExitCode  int
	OOMKilled bool
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
		LogConfig: container.LogConfig{Type: "none"},
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
	attached, err := d.cli.ContainerAttach(ctx, string(id), client.ContainerAttachOptions{Stream: true, Stdout: true, Stderr: true})
	if err != nil {
		_ = d.Destroy(context.WithoutCancel(ctx), id)
		return "", fmt.Errorf("runtime: attach dictation engine container: %w", err)
	}
	go func() {
		defer attached.Close()
		_, _ = stdcopy.StdCopy(spec.Output, spec.Output, attached.Reader)
	}()
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
	return DictationState{Running: st.Running, ExitCode: st.ExitCode, OOMKilled: st.OOMKilled}, nil
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
