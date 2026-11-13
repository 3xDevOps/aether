package vm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"path"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/guest"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// dictationPort is the vsock port the helper serves the engine's socket on.
const dictationPort = 2000

const (
	// dictationPids matches the Docker engine container's process limit.
	dictationPids = 256
	// dictationGuest is what the guest kernel and helper take, on top of
	// the engine's own memory cap.
	dictationGuest = 256 << 20
	// dictationStart bounds pulling the engine's image, building its disk
	// and booting its VM.
	dictationStart = 10 * time.Minute
)

func dictationVM(owner string) string { return "dictation-" + owner }

// StartDictation runs the speech-to-text engine in a VM with no network,
// its files mounted read-only. The engine's socket stays in the VM, on a
// directory its user owns, and the host reaches it at the socket the
// Docker engine would have bind-mounted out, which appears once the engine
// listens.
func (r *Runtime) StartDictation(ctx context.Context, spec runtime.DictationSpec) (_ runtime.ID, err error) {
	if spec.GPU {
		return "", errors.New("vm runtime: the dictation engine cannot use a GPU in a VM; install it without --gpu: aether-server dictation install")
	}
	if len(spec.Writable) != 1 || spec.Socket == "" || spec.Socket != filepath.Base(spec.Socket) {
		return "", errors.New("vm runtime: the dictation engine needs one writable directory for its socket, and the socket's name")
	}
	if err = r.RemoveDictation(ctx, spec.Owner); err != nil {
		return "", err
	}
	// A clip that gives up while the first start pulls the image or boots
	// leaves the engine starting for the next, as a container's start does.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dictationStart)
	defer cancel()
	var hostDir, guestDir string
	for host, vm := range spec.Writable {
		hostDir, guestDir = host, vm
	}
	var mounts []runtime.Mount
	for _, src := range slices.Sorted(maps.Keys(spec.ReadOnly)) {
		mounts = append(mounts, runtime.Mount{HostPath: src, ContainerPath: spec.ReadOnly[src], ReadOnly: true})
	}
	key := dictationVM(spec.Owner)
	vmSpec := runtime.Spec{
		Image: spec.Image, Command: spec.Entrypoint, User: spec.User, Mounts: mounts,
		CPULimit: spec.CPUs, MemoryLimitBytes: spec.MemoryBytes + dictationGuest, PidsLimit: dictationPids,
	}
	opts := options{
		Offline: true, OwnedDirs: []string{guestDir},
		Serves: []guest.Socket{{Port: dictationPort, Path: path.Join(guestDir, spec.Socket)}},
	}
	id, err := r.vms.create(ctx, key, vmSpec, opts)
	if err != nil {
		return "", fmt.Errorf("vm runtime: create the dictation engine's VM: %w", err)
	}
	defer func() {
		if err != nil {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), powerOffTimeout)
			defer cancel()
			_ = r.vms.destroy(cleanup, key)
		}
	}()
	if err = r.vms.start(ctx, key); err != nil {
		return "", fmt.Errorf("vm runtime: start the dictation engine's VM: %w", err)
	}
	att, err := r.vms.attach(ctx, key)
	if err != nil {
		return "", err
	}
	go copyOutput(att, spec.Output)
	if err = r.vms.relay(key, dictationPort, filepath.Join(hostDir, spec.Socket)); err != nil {
		return "", err
	}
	return id, nil
}

// copyOutput hands both of the engine's streams to out, which is closed once
// both end.
func copyOutput(att runtime.Attachment, out io.WriteCloser) {
	var wg sync.WaitGroup
	for _, r := range []io.Reader{att.Stdout(), att.Stderr()} {
		wg.Go(func() { _, _ = io.Copy(out, r) })
	}
	wg.Wait()
	_ = att.Close()
	_ = out.Close()
}

// DictationState asks the engine VM's helper. The VM stays up after the
// engine exits, until the dictation service removes it, so its exit code,
// its last output and the guest's out-of-memory kills, which hit the engine
// the wrapper runs as well, can all be read.
func (r *Runtime) DictationState(ctx context.Context, id runtime.ID) (runtime.DictationState, error) {
	key, ok := vmKey(id)
	if !ok {
		return r.Docker.DictationState(ctx, id)
	}
	if err := r.vms.relayErr(key); err != nil {
		return runtime.DictationState{}, err
	}
	info, err := r.vms.inspect(ctx, key)
	if err != nil {
		return runtime.DictationState{}, err
	}
	if info.Exited {
		return runtime.DictationState{ExitCode: info.ExitCode}, nil
	}
	h, err := r.vms.helper(key)
	if err != nil {
		return runtime.DictationState{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, helperCallTimeout)
	defer cancel()
	st, err := h.Process(guest.MainID).Status(ctx)
	if err != nil || !st.Exited {
		return runtime.DictationState{Running: err == nil}, err
	}
	stats, err := h.Stats(ctx)
	if err != nil {
		return runtime.DictationState{}, fmt.Errorf("vm runtime: memory events of the dictation engine: %w", err)
	}
	return runtime.DictationState{ExitCode: st.ExitCode, OOMKilled: stats.OOMKills > 0}, nil
}

// RemoveDictation removes owner's engine VM and any engine container Docker
// still has from before the server ran VMs.
func (r *Runtime) RemoveDictation(ctx context.Context, owner string) error {
	return errors.Join(r.vms.destroy(ctx, dictationVM(owner)), r.Docker.RemoveDictation(ctx, owner))
}
