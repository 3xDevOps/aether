package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
	agentprofile "github.com/3xDevOps/Aether/internal/profile"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/store"
)

// profileService is the optional scheduler seam for pinning and
// materializing agent profile snapshots. Tests inject a fake; production
// wires profile.Service (or scheduler.New constructs one from *store.DB).
type profileService interface {
	Latest(ctx context.Context, member, harness string) (domain.ProfileSnapshot, error)
	PinRun(ctx context.Context, runID domain.RunID, id domain.ProfileSnapshotID) error
	Materialize(ctx context.Context, id domain.ProfileSnapshotID, destDir string) error
}

func (s *Scheduler) pinLatestProfile(ctx context.Context, run *domain.Run) error {
	if s.cfg.Profiles == nil {
		return nil
	}
	snap, err := s.cfg.Profiles.Latest(ctx, string(run.MemberID), run.Harness)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	}
	if err := s.cfg.Profiles.PinRun(ctx, run.ID, snap.ID); err != nil {
		return err
	}
	run.ProfileSnapshotID = snap.ID
	return nil
}

func (s *Scheduler) runProfileDir(id domain.RunID) string {
	if s.cfg.ProfilesDir == "" {
		return ""
	}
	return filepath.Join(s.cfg.ProfilesDir, "runs", string(id))
}

func (s *Scheduler) cleanupProfile(id domain.RunID) {
	dir := s.runProfileDir(id)
	if dir == "" {
		return
	}
	if err := os.RemoveAll(dir); err != nil {
		slog.Warn("scheduler: remove profile materialization", "run", id, "error", err)
	}
}

// withProfileMounts materializes the pinned snapshot (if any) and returns
// profile parent + credential children, validated together. Missing
// snapshots and empty LocalRoot skip the profile mount.
func (s *Scheduler) withProfileMounts(ctx context.Context, run *domain.Run, profile harness.Profile, containerHome string, creds []runtime.Mount) ([]runtime.Mount, error) {
	profileMount, err := s.profileMount(ctx, run, profile, containerHome)
	if err != nil {
		return nil, err
	}
	mounts, nestings, err := combineProfileCredentials(profileMount, creds, profile.DenyNames)
	if err != nil {
		return nil, err
	}
	if len(mounts) == 0 {
		return nil, nil
	}
	roots := make([]string, 0, 2)
	if s.cfg.HomesDir != "" {
		roots = append(roots, s.cfg.HomesDir)
	}
	if s.cfg.ProfilesDir != "" {
		roots = append(roots, s.cfg.ProfilesDir)
	}
	if err := runtime.ValidateMounts(mounts, runtime.MountPolicy{
		OwnedRoots:        roots,
		WorktreeHostPath:  run.Worktree,
		WorktreeMountPath: s.cfg.WorktreeMount,
		AllowedNestings:   nestings,
	}); err != nil {
		return nil, err
	}
	return mounts, nil
}

func (s *Scheduler) profileMount(ctx context.Context, run *domain.Run, profile harness.Profile, containerHome string) (*runtime.Mount, error) {
	if s.cfg.Profiles == nil || s.cfg.ProfilesDir == "" || s.cfg.HomesDir == "" {
		return nil, nil
	}
	if profile.LocalRoot == "" || run.ProfileSnapshotID == "" {
		return nil, nil
	}
	dest := s.runProfileDir(run.ID)
	if dest == "" {
		return nil, nil
	}
	if err := os.MkdirAll(dest, 0o700); err != nil {
		return nil, fmt.Errorf("create profile dest: %w", err)
	}
	if err := s.cfg.Profiles.Materialize(ctx, run.ProfileSnapshotID, dest); err != nil {
		return nil, fmt.Errorf("materialize profile: %w", err)
	}
	return &runtime.Mount{
		HostPath:      dest,
		ContainerPath: path.Join(containerHome, profile.LocalRoot),
	}, nil
}

// combineProfileCredentials orders the profile parent before credential
// children. When a credential target equals the profile target (today's
// shipped registry: LocalRoot == CredentialPaths), credential DenyNames
// are overlaid as nested file mounts from the persistent home so login
// state stays a separate host path. Proper subdirectory credentials use
// AllowedNestings as-is.
func combineProfileCredentials(profileMount *runtime.Mount, creds []runtime.Mount, denyNames []string) ([]runtime.Mount, map[string]string, error) {
	if profileMount == nil {
		return creds, nil, nil
	}
	parent := path.Clean(profileMount.ContainerPath)
	out := []runtime.Mount{*profileMount}
	nestings := map[string]string{}
	for _, cred := range creds {
		child := path.Clean(cred.ContainerPath)
		switch {
		case child == parent:
			nested, err := overlayDeniedCredentials(parent, cred.HostPath, agentprofile.CredentialFileNames(denyNames))
			if err != nil {
				return nil, nil, err
			}
			for _, n := range nested {
				nestings[path.Clean(n.ContainerPath)] = parent
				out = append(out, n)
			}
		case strings.HasPrefix(child, parent+"/"):
			nestings[child] = parent
			out = append(out, cred)
		default:
			out = append(out, cred)
		}
	}
	return out, nestings, nil
}

func overlayDeniedCredentials(profileContainer, credHost string, denyNames []string) ([]runtime.Mount, error) {
	seen := map[string]struct{}{}
	var names []string
	for _, n := range denyNames {
		if n == "" || strings.Contains(n, "/") || strings.Contains(n, "*") {
			continue
		}
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		names = append(names, n)
	}
	out := make([]runtime.Mount, 0, len(names))
	for _, name := range names {
		host := filepath.Join(credHost, name)
		if err := ensureBindSource(host); err != nil {
			return nil, err
		}
		out = append(out, runtime.Mount{
			HostPath:      host,
			ContainerPath: path.Join(profileContainer, name),
		})
	}
	return out, nil
}

func ensureBindSource(p string) error {
	info, err := os.Lstat(p)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("credential source %q is a symlink", p)
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return nil
		}
		return err
	}
	return f.Close()
}
