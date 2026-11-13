package scheduler

import (
	"cmp"
	"context"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strconv"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// vmSessionRuntime runs each session in its own VM (runtime/vm), whose file
// service serves every host file it mounts as the session user's.
type vmSessionRuntime interface{ SessionVMs() }

func (s *Scheduler) sessionVMs() bool {
	_, ok := s.cfg.Runtime.(vmSessionRuntime)
	return ok
}

// vmStateMounts gives a session VM its own copy of each directory where the
// agent keeps the databases its sessions share, on the VM's data disk: a
// WAL database works only among processes of one kernel. The directory in
// the member's home is the mount point, and a variable the workspace or
// profile already sets is left alone.
func (s *Scheduler) vmStateMounts(run domain.RunID, member domain.MemberID, profile harness.Profile, home string, env map[string]string) ([]runtime.Mount, error) {
	var mounts []runtime.Mount
	for i, state := range profile.VMState {
		if err := s.cfg.Homes.PrepareLoginMountpoint(member, state.Dir, true); err != nil {
			return nil, fmt.Errorf("scheduler: %w", err)
		}
		scratch, err := s.ensureScratch(filepath.Join(string(run), "vm-state", strconv.Itoa(i)))
		if err != nil {
			return nil, err
		}
		target := path.Join(home, state.Dir)
		mounts = append(mounts, runtime.Mount{HostPath: scratch, ContainerPath: target, Kind: runtime.MountScratch})
		if _, set := env[state.Env]; state.Env != "" && !set {
			env[state.Env] = path.Join(target, state.File)
		}
	}
	return mounts, nil
}

// checkVMSingle refuses a VM session of a CLI that keeps databases its
// sessions share where no setting moves them, while another live session of
// it uses the launcher's home or the login owner's. Sleeping sessions count:
// a saved VM keeps the databases open in its memory.
func (s *Scheduler) checkVMSingle(ctx context.Context, run *domain.Run, profile harness.Profile) error {
	if profile.VMSingle == "" || !s.sessionVMs() {
		return nil
	}
	active, err := s.cfg.Store.ListActiveRuns(ctx)
	if err != nil {
		return fmt.Errorf("scheduler: list live sessions: %w", err)
	}
	homes := runHomes(run)
	for _, other := range active {
		if other.ID == run.ID || other.Harness != run.Harness {
			continue
		}
		if slices.ContainsFunc(runHomes(other), func(h domain.MemberID) bool { return slices.Contains(homes, h) }) {
			return fmt.Errorf("scheduler: %s %s, and session %s already runs it on the same home; close or kill that session first",
				cmp.Or(profile.DisplayName, profile.Name), profile.VMSingle, other.ID)
		}
	}
	return nil
}

// runHomes are the homes a session's CLI keeps state in: the launcher's,
// and the account owner's whose login it mounts.
func runHomes(r *domain.Run) []domain.MemberID {
	homes := []domain.MemberID{cmp.Or(r.HomeMemberID, r.MemberID)}
	if r.AccountMemberID != "" {
		homes = append(homes, r.AccountMemberID)
	}
	return homes
}
