// Package vm runs each session in its own Cloud Hypervisor virtual machine
// behind runtime.Runtime. Every other container kind, and every image
// operation, still goes to Docker until it moves into a VM as well.
package vm

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// idPrefix marks a VM's runtime.ID; Docker IDs are hex and never carry it.
const idPrefix = "vm-"

// Runtime sends session containers to VMs and everything else to the
// embedded Docker runtime, whose optional methods it keeps.
type Runtime struct {
	*runtime.Docker
	vms *engine
}

var (
	_ runtime.Runtime             = (*Runtime)(nil)
	_ runtime.ManagedExecRuntime  = (*Runtime)(nil)
	_ runtime.SudoRuntime         = (*Runtime)(nil)
	_ runtime.CapacityRuntime     = (*Runtime)(nil)
	_ runtime.InitPIDRuntime      = (*Runtime)(nil)
	_ runtime.MemoryEventsRuntime = (*Runtime)(nil)
	_ runtime.BrowserRuntime      = (*Runtime)(nil)
	_ runtime.SaveRuntime         = (*Runtime)(nil)
)

// Config names what a VM is built from. Monitor is the command that runs
// Monitor inside each VM's systemd unit: the server binary and its
// vm-monitor subcommand.
type Config struct {
	Docker  *runtime.Docker
	DataDir string
	Images  Images
	Monitor []string
	Helper  string
}

// New returns a Runtime that keeps its VMs under DataDir/vm.
func New(cfg Config) (*Runtime, error) {
	if cfg.Docker == nil || cfg.Images == nil || len(cfg.Monitor) == 0 || cfg.Helper == "" {
		return nil, errors.New("vm runtime: Docker, the image store, the monitor command and the helper binary are required")
	}
	return &Runtime{Docker: cfg.Docker, vms: newEngine(cfg, systemdUnits{}, dialHelper)}, nil
}

// isSession is the one routing rule: a session container carries its run ID
// both as creation key and in AETHER_RUN_ID. Environment terminals, the
// assistant, the agent updater and the cleanup helper carry no run ID.
func isSession(spec runtime.Spec) bool {
	return spec.CreationKey != "" && spec.Env[coordtransport.RunIDEnv] == spec.CreationKey
}

func vmKey(id runtime.ID) (string, bool) {
	return strings.CutPrefix(string(id), idPrefix)
}

func (r *Runtime) Create(ctx context.Context, spec runtime.Spec) (runtime.ID, error) {
	if !isSession(spec) {
		return r.Docker.Create(ctx, spec)
	}
	return r.vms.create(ctx, spec)
}

func (r *Runtime) Start(ctx context.Context, id runtime.ID) error {
	if key, ok := vmKey(id); ok {
		return r.vms.start(ctx, key)
	}
	return r.Docker.Start(ctx, id)
}

func (r *Runtime) Pause(ctx context.Context, id runtime.ID) error {
	if key, ok := vmKey(id); ok {
		return r.vms.pause(ctx, key)
	}
	return r.Docker.Pause(ctx, id)
}

func (r *Runtime) Resume(ctx context.Context, id runtime.ID) error {
	if key, ok := vmKey(id); ok {
		return r.vms.resume(ctx, key)
	}
	return r.Docker.Resume(ctx, id)
}

func (r *Runtime) Stop(ctx context.Context, id runtime.ID, grace time.Duration) error {
	if key, ok := vmKey(id); ok {
		return r.vms.stop(ctx, key, grace)
	}
	return r.Docker.Stop(ctx, id, grace)
}

func (r *Runtime) Destroy(ctx context.Context, id runtime.ID) error {
	if key, ok := vmKey(id); ok {
		return r.vms.destroy(ctx, key)
	}
	return r.Docker.Destroy(ctx, id)
}

func (r *Runtime) Attach(ctx context.Context, id runtime.ID) (runtime.Attachment, error) {
	if key, ok := vmKey(id); ok {
		return r.vms.attach(ctx, key)
	}
	return r.Docker.Attach(ctx, id)
}

func (r *Runtime) ContainerIP(ctx context.Context, id runtime.ID) (string, error) {
	if key, ok := vmKey(id); ok {
		return r.vms.containerIP(ctx, key)
	}
	return r.Docker.ContainerIP(ctx, id)
}

func (r *Runtime) ExecTTY(ctx context.Context, id runtime.ID, argv []string, workDir string, cols, rows uint) (runtime.Attachment, error) {
	if key, ok := vmKey(id); ok {
		return r.vms.execTTY(ctx, key, argv, workDir, cols, rows)
	}
	return r.Docker.ExecTTY(ctx, id, argv, workDir, cols, rows)
}

func (r *Runtime) Exec(ctx context.Context, id runtime.ID, argv []string, workDir string) (int, string, string, error) {
	if key, ok := vmKey(id); ok {
		return r.vms.exec(ctx, key, argv, workDir)
	}
	return r.Docker.Exec(ctx, id, argv, workDir)
}

func (r *Runtime) Wait(ctx context.Context, id runtime.ID) (runtime.ExitStatus, error) {
	if key, ok := vmKey(id); ok {
		return r.vms.wait(ctx, key)
	}
	return r.Docker.Wait(ctx, id)
}

func (r *Runtime) Inspect(ctx context.Context, id runtime.ID) (runtime.ContainerInfo, error) {
	if key, ok := vmKey(id); ok {
		return r.vms.inspect(ctx, key)
	}
	return r.Docker.Inspect(ctx, id)
}

// FindByCreationKey looks among VMs first: a session's key names a VM only
// when it was created under this runtime.
func (r *Runtime) FindByCreationKey(ctx context.Context, key string) (runtime.ID, error) {
	id, err := r.vms.find(key)
	if !errors.Is(err, runtime.ErrNotFound) {
		return id, err
	}
	return r.Docker.FindByCreationKey(ctx, key)
}

// EgressGateway is where session VMs reach the egress proxy. It sets up the
// VMs' network whether or not create is set, so the server's start
// rebuilds the firewall of VMs that outlived the last server.
func (r *Runtime) EgressGateway(ctx context.Context, _ bool) (netip.Addr, error) {
	if err := r.vms.ensureNetwork(ctx); err != nil {
		return netip.Addr{}, err
	}
	return proxyAddr, nil
}

// EgressCreationKey answers only for VMs: Docker's egress containers have
// no route to the VMs' proxy address.
func (r *Runtime) EgressCreationKey(_ context.Context, ip netip.Addr) (string, error) {
	return r.vms.egressKey(ip)
}

func (r *Runtime) Commit(ctx context.Context, id runtime.ID, tag string, labels map[string]string) error {
	if _, ok := vmKey(id); ok {
		return errors.New("vm runtime: Commit (Save environment) of a session VM is not supported until work item 9")
	}
	return r.Docker.Commit(ctx, id, tag, labels)
}

func (r *Runtime) InitPID(ctx context.Context, id runtime.ID) (int, error) {
	if key, ok := vmKey(id); ok {
		return r.vms.initPID(ctx, key)
	}
	return r.Docker.InitPID(ctx, id)
}

func (r *Runtime) MemoryEvents(ctx context.Context, id runtime.ID) (runtime.MemoryEvents, bool, error) {
	key, ok := vmKey(id)
	if !ok {
		return runtime.MemoryEvents{}, false, nil
	}
	events, err := r.vms.memoryEvents(ctx, key)
	return events, true, err
}

func (r *Runtime) StartExecTTY(ctx context.Context, id runtime.ID, spec runtime.ExecSpec) (runtime.ManagedExec, error) {
	if key, ok := vmKey(id); ok {
		return r.vms.startManagedExec(ctx, key, spec, true)
	}
	return r.Docker.StartExecTTY(ctx, id, spec)
}

func (r *Runtime) StartExecPipe(ctx context.Context, id runtime.ID, spec runtime.ExecSpec) (runtime.ManagedExec, error) {
	if key, ok := vmKey(id); ok {
		return r.vms.startManagedExec(ctx, key, spec, false)
	}
	return r.Docker.StartExecPipe(ctx, id, spec)
}

func (r *Runtime) RecoverExec(ctx context.Context, identity runtime.ExecIdentity) (runtime.ManagedExec, error) {
	if key, ok := vmKey(identity.ContainerID); ok {
		return r.vms.recoverExec(ctx, key, identity)
	}
	return r.Docker.RecoverExec(ctx, identity)
}

func (r *Runtime) SettingUp(id runtime.ID) bool {
	if key, ok := vmKey(id); ok {
		return r.vms.settingUp(key)
	}
	return r.Docker.SettingUp(id)
}

func (r *Runtime) ExecAttached(ctx context.Context, id runtime.ID, spec runtime.AttachedExec) (int, error) {
	if key, ok := vmKey(id); ok {
		return r.vms.execAttached(ctx, key, spec)
	}
	return r.Docker.ExecAttached(ctx, id, spec)
}

func (r *Runtime) CreateBrowser(ctx context.Context, spec runtime.BrowserSpec) (runtime.ID, error) {
	if _, ok := vmKey(spec.RunContainer); ok {
		return "", errors.New("vm runtime: the development browser cannot run beside a session VM until work item 9 moves it into the VM")
	}
	return r.Docker.CreateBrowser(ctx, spec)
}

func (r *Runtime) Capacity(ctx context.Context) (runtime.HostCapacity, error) {
	capacity, err := r.Docker.Capacity(ctx)
	if err != nil {
		return capacity, err
	}
	fs, err := r.vms.filesystem()
	if err != nil {
		return runtime.HostCapacity{}, fmt.Errorf("vm runtime: VM disk capacity: %w", err)
	}
	capacity.Filesystems = append(capacity.Filesystems, fs)
	return capacity, nil
}

// ContainerStats adds every running VM to Docker's sample. A VM that cannot
// be sampled is left out and named in the error, like a failed container.
func (r *Runtime) ContainerStats(ctx context.Context) ([]runtime.ContainerStat, error) {
	stats, dockerErr := r.Docker.ContainerStats(ctx)
	vms, vmErr := r.vms.stats(ctx)
	return append(stats, vms...), errors.Join(dockerErr, vmErr)
}

func (r *Runtime) Save(ctx context.Context, id runtime.ID) error {
	key, ok := vmKey(id)
	if !ok {
		return errors.New("vm runtime: only a session VM can be saved; Docker containers cannot")
	}
	return r.vms.save(ctx, key)
}

func (r *Runtime) Restore(ctx context.Context, id runtime.ID) error {
	key, ok := vmKey(id)
	if !ok {
		return errors.New("vm runtime: only a session VM can be restored; Docker containers cannot")
	}
	return r.vms.restore(ctx, key)
}

// SessionVMs tells the scheduler that sessions run in VMs, whose host files
// need no ownership pass.
func (r *Runtime) SessionVMs() {}
