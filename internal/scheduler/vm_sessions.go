package scheduler

import (
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// vmSessionRuntime runs each session in its own VM (runtime/vm), whose file
// service serves every host file it mounts as the session user's.
type vmSessionRuntime interface{ SessionVMs() }

// programBuilder builds the disks session VMs load agent programs from.
type programBuilder interface{ BuildPrograms([]runtime.Program) }

func (s *Scheduler) sessionVMs() bool {
	_, ok := s.cfg.Runtime.(vmSessionRuntime)
	return ok
}

// vmDatabasesDirName holds, in the scheduler's state directory, a directory
// per session for the disk with its own copy of each agent database
// directory. No container can write there: a disk path a guest could plant
// a symlink in would let the VM monitor, which runs as root, open any host
// file.
const vmDatabasesDirName = "vm-databases"

func (s *Scheduler) vmDatabasesRoot() string {
	return filepath.Join(s.cfg.StateDir, vmDatabasesDirName)
}

func (s *Scheduler) removeVMDatabases(run domain.RunID) {
	if err := os.RemoveAll(filepath.Join(s.vmDatabasesRoot(), string(run))); err != nil {
		slog.Warn("scheduler: remove the session's agent database disk", "run", run, "error", err)
	}
}

// vmStateMounts gives a session VM its own copy of each directory where the
// agent keeps the databases its sessions share: a WAL database works only
// among processes of one kernel, and only on a disk of the VM's, not over
// the home's file service. The copies are on a disk of the session's, so
// they outlive a VM replaced by an account move, until the session's VM is
// removed, and the directory in the member's home is the mount point. A variable the workspace or profile
// already sets is left alone.
func (s *Scheduler) vmStateMounts(run domain.RunID, member domain.MemberID, profile harness.Profile, home string, env map[string]string) ([]runtime.Mount, error) {
	var mounts []runtime.Mount
	dir := filepath.Join(s.vmDatabasesRoot(), string(run))
	if len(profile.VMState) > 0 {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("scheduler: create the session's %s database disk: %w", profile.Name, err)
		}
	}
	for i, state := range profile.VMState {
		if err := s.cfg.Homes.PrepareLoginMountpoint(member, state.Dir, true); err != nil {
			return nil, fmt.Errorf("scheduler: %w", err)
		}
		target := path.Join(home, state.Dir)
		mounts = append(mounts, runtime.Mount{
			HostPath: dir, Subpath: strconv.Itoa(i), ContainerPath: target, Kind: runtime.MountState,
		})
		for _, f := range state.Seed {
			mounts = append(mounts, runtime.Mount{HostPath: dir, ContainerPath: path.Join(target, f), Kind: runtime.MountSeed})
		}
		if _, set := env[state.Env]; state.Env != "" && !set {
			env[state.Env] = path.Join(target, state.File)
		}
	}
	return mounts, nil
}

// vmPrograms are the programs a session VM may load from disks of their
// own: the agent's CLI and, when the server drives the agent over ACP, its
// ACP server. Only the member's own ~/.local/bin is looked in, so a session
// that borrows the account owner's installation reads it through the file
// service.
func vmPrograms(homePath, home string, profile harness.Profile, acp bool) []runtime.Program {
	var names []string
	if len(profile.TUIArgs) > 0 {
		names = append(names, profile.TUIArgs[0])
	}
	if acp && len(profile.ACPArgs) > 0 && !slices.Contains(names, profile.ACPArgs[0]) {
		names = append(names, profile.ACPArgs[0])
	}
	var programs []runtime.Program
	for _, name := range names {
		if !strings.Contains(name, "/") {
			programs = append(programs, runtime.Program{Home: homePath, Target: home, Executable: name})
		}
	}
	return programs
}
