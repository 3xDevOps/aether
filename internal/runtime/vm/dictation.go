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

	"github.com/3xDevOps/Aether/internal/guest"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// dictationPort is the vsock port the helper serves the engine's socket on.
const dictationPort = 2000

// dictationPids matches the Docker engine container's process limit.
const dictationPids = 256

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
		CPULimit: spec.CPUs, MemoryLimitBytes: spec.MemoryBytes, PidsLimit: dictationPids,
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
	// Waiting records the engine's exit and powers its VM off.
	go func() { _, _ = r.vms.wait(context.Background(), key) }()
	r.vms.relay(key, dictationPort, filepath.Join(hostDir, spec.Socket))
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

// DictationState reads the engine's VM. Only the guest kernel's OOM killer
// kills the engine in its VM, so a SIGKILL exit is an out-of-memory kill.
func (r *Runtime) DictationState(ctx context.Context, id runtime.ID) (runtime.DictationState, error) {
	key, ok := vmKey(id)
	if !ok {
		return r.Docker.DictationState(ctx, id)
	}
	info, err := r.vms.inspect(ctx, key)
	if err != nil {
		return runtime.DictationState{}, err
	}
	return runtime.DictationState{Running: !info.Exited, ExitCode: info.ExitCode, OOMKilled: info.Exited && info.ExitCode == killedExitCode}, nil
}

// RemoveDictation removes owner's engine VM and any engine container Docker
// still has from before the server ran VMs.
func (r *Runtime) RemoveDictation(ctx context.Context, owner string) error {
	return errors.Join(r.vms.destroy(ctx, dictationVM(owner)), r.Docker.RemoveDictation(ctx, owner))
}
