package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/runtime/vm"
	"github.com/3xDevOps/Aether/internal/vmimage"
)

// DefaultVMKernel is where the guest kernel a release ships is installed.
const DefaultVMKernel = "/usr/local/lib/aether/vmlinux"

// newVMRuntime runs sessions in VMs whose monitor is this binary's
// vm-monitor subcommand and whose in-VM helper is this binary too. Their
// checkouts live on disks of their own, which checkouts makes. members are
// this server's: their containers left in Docker are stopped.
func newVMRuntime(ctx context.Context, dataDir, kernel, browserImage string, members []string, docker *runtime.Docker, checkouts vm.CheckoutDisks) (*vm.Runtime, *vmimage.Store, error) {
	if os.Geteuid() != 0 {
		return nil, nil, errors.New("server: session-runtime vm needs the server to run as root: it starts each VM in a systemd unit and sets up its network")
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, nil, fmt.Errorf("server: find the server binary for session VMs: %w", err)
	}
	images, err := vmimage.Open(filepath.Join(dataDir, "vm", "images"), kernel, docker)
	if err != nil {
		return nil, nil, fmt.Errorf("server: open the VM image store: %w", err)
	}
	rt, err := vm.New(vm.Config{
		Docker: docker, DataDir: dataDir, Images: images,
		Monitor: []string{exe, vm.MonitorCommand}, Helper: exe, CheckoutDisks: checkouts, BrowserImage: browserImage,
	})
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, dockerCleanupTimeout)
	defer cancel()
	rt.StopDockerContainers(ctx, members)
	return rt, images, nil
}

// dockerCleanupTimeout bounds stopping the Docker containers VMs replace.
const dockerCleanupTimeout = 2 * time.Minute
