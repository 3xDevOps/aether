package server

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/runtime/vm"
	"github.com/3xDevOps/Aether/internal/vmimage"
)

// DefaultVMKernel is where the guest kernel a release ships is installed.
const DefaultVMKernel = "/usr/local/lib/aether/vmlinux"

// newVMRuntime runs sessions in VMs whose monitor is this binary's
// vm-monitor subcommand and whose in-VM helper is this binary too.
func newVMRuntime(dataDir, kernel string, docker *runtime.Docker) (*vm.Runtime, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("server: find the server binary for session VMs: %w", err)
	}
	images, err := vmimage.Open(filepath.Join(dataDir, "vm", "images"), kernel, docker)
	if err != nil {
		return nil, fmt.Errorf("server: open the VM image store: %w", err)
	}
	return vm.New(vm.Config{
		Docker: docker, DataDir: dataDir, Images: images,
		Monitor: []string{exe, vm.MonitorCommand}, Helper: exe,
	})
}
