package scheduler

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"time"

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

// vmStateDirName holds, in a session's scratch directory, the disk with the
// session's own copy of each agent database directory.
const vmStateDirName = "vm-state"

// vmStateMounts gives a session VM its own copy of each directory where the
// agent keeps the databases its sessions share: a WAL database works only
// among processes of one kernel, and only on a disk of the VM's, not over
// the home's file service. The copies are on a disk of the session's, so
// they outlive a VM replaced by an account move, and the directory in the
// member's home is the mount point. A variable the workspace or profile
// already sets is left alone.
func (s *Scheduler) vmStateMounts(run domain.RunID, member domain.MemberID, profile harness.Profile, home string, env map[string]string) ([]runtime.Mount, error) {
	var mounts []runtime.Mount
	dir := filepath.Join(s.scratchRoot(), string(run), vmStateDirName)
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
		if _, set := env[state.Env]; state.Env != "" && !set {
			env[state.Env] = path.Join(target, state.File)
		}
	}
	return mounts, nil
}

// vmSingleHomes are the homes whose databases a session of profile's CLI
// opens: the launcher's, and the account owner's when the shared login is
// a whole directory, which holds them.
func (s *Scheduler) vmSingleHomes(profile harness.Profile, home, account domain.MemberID) ([]domain.MemberID, error) {
	homes := []domain.MemberID{home}
	if account == "" || account == home || s.cfg.Homes == nil {
		return homes, nil
	}
	paths, err := profile.LoginPaths()
	if err != nil {
		return nil, fmt.Errorf("scheduler: %w", err)
	}
	for _, rel := range paths {
		dir, err := s.cfg.Homes.LoginPathIsDir(account, rel)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("scheduler: %w", err)
		}
		if dir {
			return append(homes, account), nil
		}
	}
	return homes, nil
}

// vmSingleConflict refuses a VM session of a CLI that keeps databases its
// sessions share where no setting moves them, while another live session of
// it, other than except, uses one of the same homes. Sleeping sessions and
// closing ones that still have their VM count: a saved VM keeps the
// databases open in its memory.
func (s *Scheduler) vmSingleConflict(ctx context.Context, profile harness.Profile, agent string, home, account domain.MemberID, except domain.RunID) error {
	if profile.VMSingle == "" || !s.sessionVMs() {
		return nil
	}
	homes, err := s.vmSingleHomes(profile, home, account)
	if err != nil {
		return err
	}
	live, err := s.cfg.Store.ListActiveRuns(ctx)
	if err != nil {
		return fmt.Errorf("scheduler: list live sessions: %w", err)
	}
	s.mu.Lock()
	var closing []domain.RunID
	for id, entry := range s.runs {
		if entry.containerID != "" && !entry.retained && !slices.ContainsFunc(live, func(r *domain.Run) bool { return r.ID == id }) {
			closing = append(closing, id)
		}
	}
	s.mu.Unlock()
	for _, id := range closing {
		if r, getErr := s.cfg.Store.GetRun(ctx, id); getErr == nil {
			live = append(live, r)
		}
	}
	for _, other := range live {
		if other.ID == except || other.Harness != agent {
			continue
		}
		theirs, err := s.vmSingleHomes(profile, other.HomeMember(), other.AccountMember())
		if err != nil {
			return err
		}
		if slices.ContainsFunc(theirs, func(h domain.MemberID) bool { return slices.Contains(homes, h) }) {
			way := "close or kill it first"
			if other.MemberID != home {
				way = "ask them to close it, or launch on another account"
			}
			return fmt.Errorf("scheduler: %s %s, and %s's session %s already runs it on the same home; %s",
				cmp.Or(profile.DisplayName, profile.Name), profile.VMSingle, s.memberName(ctx, other.MemberID), other.ID, way)
		}
	}
	return nil
}

// vmSingleLaunch is the last VM launch of a VMSingle CLI on a home, kept so
// a closed session whose VM held the databases is not reopened over a later
// session's changes.
type vmSingleLaunch struct {
	Run domain.RunID `json:"run"`
	At  time.Time    `json:"at"`
}

func (s *Scheduler) vmSingleLaunchPath(agent string, home domain.MemberID) string {
	return filepath.Join(s.cfg.StateDir, "vm-single", agent+"-"+string(home)+".json")
}

// recordVMSingleLaunch notes that run now holds profile's databases on its
// homes.
func (s *Scheduler) recordVMSingleLaunch(profile harness.Profile, run *domain.Run) error {
	if profile.VMSingle == "" || !s.sessionVMs() {
		return nil
	}
	homes, err := s.vmSingleHomes(profile, run.HomeMember(), run.AccountMember())
	if err != nil {
		return err
	}
	data, err := json.Marshal(vmSingleLaunch{Run: run.ID, At: time.Now().UTC()})
	if err != nil {
		return fmt.Errorf("scheduler: record the %s launch: %w", profile.Name, err)
	}
	for _, home := range homes {
		p := s.vmSingleLaunchPath(run.Harness, home)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return fmt.Errorf("scheduler: record the %s launch: %w", profile.Name, err)
		}
		if err := writeFileIn(filepath.Dir(p), filepath.Base(p), data); err != nil {
			return fmt.Errorf("scheduler: record the %s launch: %w", profile.Name, err)
		}
	}
	return nil
}

// vmSingleRelaunch refuses to reopen a closed session of a VMSingle CLI
// while another runs it on the same home, or after one did since it closed:
// its VM kept the databases open and would write over that session's
// changes.
func (s *Scheduler) vmSingleRelaunch(ctx context.Context, run *domain.Run) error {
	profile, _ := harness.Lookup(run.Harness)
	if profile.VMSingle == "" || !s.sessionVMs() {
		return nil
	}
	if err := s.vmSingleConflict(ctx, profile, run.Harness, run.HomeMember(), run.AccountMember(), run.ID); err != nil {
		return err
	}
	homes, err := s.vmSingleHomes(profile, run.HomeMember(), run.AccountMember())
	if err != nil {
		return err
	}
	for _, home := range homes {
		data, err := os.ReadFile(s.vmSingleLaunchPath(run.Harness, home))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("scheduler: read the last %s launch: %w", profile.Name, err)
		}
		var last vmSingleLaunch
		if err = json.Unmarshal(data, &last); err != nil {
			return fmt.Errorf("scheduler: read the last %s launch: %w", profile.Name, err)
		}
		if last.Run != run.ID && (run.FinishedAt == nil || last.At.After(*run.FinishedAt)) {
			return fmt.Errorf("scheduler: %s %s, and session %s ran it on the same home after this one closed, so this one cannot reopen; launch a new session",
				cmp.Or(profile.DisplayName, profile.Name), profile.VMSingle, last.Run)
		}
	}
	return nil
}
