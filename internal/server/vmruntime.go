package server

import (
	"context"
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
// vm-monitor subcommand and whose in-VM helper is this binary too.
// members are this server's: their containers left in Docker are stopped.
func newVMRuntime(ctx context.Context, dataDir, kernel string, members []string, docker *runtime.Docker) (*vm.Runtime, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("server: find the server binary for session VMs: %w", err)
	}
	images, err := vmimage.Open(filepath.Join(dataDir, "vm", "images"), kernel, docker)
	if err != nil {
		return nil, fmt.Errorf("server: open the VM image store: %w", err)
	}
	rt, err := vm.New(vm.Config{
		Docker: docker, DataDir: dataDir, Images: images,
		Monitor: []string{exe, vm.MonitorCommand}, Helper: exe,
	})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, dockerCleanupTimeout)
	defer cancel()
	rt.StopDockerContainers(ctx, members)
	return rt, nil
}

// dockerCleanupTimeout bounds stopping the Docker containers VMs replace.
const dockerCleanupTimeout = 2 * time.Minute
