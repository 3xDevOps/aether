package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/3xDevOps/Aether/internal/agentstatus"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/runtime"
)

const (
	harnessUpdateInterval = 6 * time.Hour
	harnessUpdateRetry    = 15 * time.Minute
	// defaultHarnessUpdateWait keeps a launch well inside the 60 seconds the
	// dashboard gateway allows one control call (internal/localgw).
	defaultHarnessUpdateWait    = 25 * time.Second
	defaultHarnessUpdateTimeout = 10 * time.Minute
	harnessUpdateDestroyTimeout = 30 * time.Second
	harnessVersionTimeout       = 15 * time.Second
	maxHarnessVersion           = 64
)

type harnessUpdateKey struct {
	home    string
	harness string
}

// harnessUpdateState is guarded by Scheduler.mu.
type harnessUpdateState struct {
	next    time.Time
	running *harnessUpdateRun
}

// harnessUpdateRun is one update in flight; done closes when it finishes.
// before is guarded by Scheduler.mu.
type harnessUpdateRun struct {
	done   chan struct{}
	before string
}

// updateHarness waits a bounded time for the update, which runs detached from
// the launch so a dropped request or kill never stops an install halfway and
// nothing it does can fail the launch.
func (s *Scheduler) updateHarness(ctx context.Context, run *domain.Run, plan *EnvironmentPlan, profile harness.Profile) {
	if s.cfg.HarnessUpdateDisabled || profile.UpdateScript == "" || len(profile.TUIArgs) == 0 {
		return
	}
	i := slices.IndexFunc(plan.Mounts, func(m runtime.Mount) bool { return m.ContainerPath == plan.Home })
	if i < 0 {
		return
	}
	home, exe := plan.Mounts[i], profile.TUIArgs[0]
	if !installedInHome(home.HostPath, exe) {
		return
	}
	key := harnessUpdateKey{home: home.HostPath, harness: profile.Name}
	s.mu.Lock()
	if s.harnessUpdates == nil {
		s.harnessUpdates = make(map[harnessUpdateKey]*harnessUpdateState)
	}
	state := s.harnessUpdates[key]
	if state == nil {
		state = &harnessUpdateState{}
		s.harnessUpdates[key] = state
	}
	update := state.running
	installing := slices.Contains(slices.Collect(maps.Values(s.agentInstalls)), home.HostPath)
	if update == nil && !installing && !s.cfg.Now().Before(state.next) {
		update = &harnessUpdateRun{done: make(chan struct{})}
		state.running = update
		// One name per home and harness: only one update per key runs at a
		// time, so a match is left over from a crashed server.
		name := "harness-update-" + filepath.Base(home.HostPath) + "-" + profile.Name
		mounts := make([]runtime.Mount, 1, 2)
		mounts[0] = home
		if s.cfg.ServerBinary != "" {
			mounts = append(mounts, runtime.Mount{
				HostPath: s.cfg.ServerBinary, ContainerPath: agentstatus.ReporterCommand, ReadOnly: true,
			})
		}
		spec := runtime.Spec{
			Name:  name,
			Image: plan.Image,
			// The launch goes on to add coordination variables to plan.Env.
			Env:        maps.Clone(plan.Env),
			Mounts:     mounts,
			User:       plan.User,
			WorkingDir: plan.Home,
			// Outlives the execs, and ends on its own if the server dies first.
			Command:          []string{"/bin/sh", "-c", "sleep 900"},
			CreationKey:      name,
			CPULimit:         s.cfg.RunCPULimit,
			MemoryLimitBytes: s.cfg.RunMemoryBytes,
			PidsLimit:        s.cfg.RunPidsLimit,
		}
		go s.runHarnessUpdate(state, update, run.WorkspaceID, run.ID, spec, exe, profile)
	}
	s.mu.Unlock()
	if update == nil {
		return
	}
	wait := s.cfg.harnessUpdateWait
	if wait <= 0 {
		wait = defaultHarnessUpdateWait
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-update.done:
		return
	case <-ctx.Done():
		return
	case <-timer.C:
	}
	s.mu.Lock()
	before := update.before
	s.mu.Unlock()
	msg := "starting the installed version "
	if before != "" {
		msg += before + " "
	}
	s.publishTimeline(ctx, run.WorkspaceID, run.ID, "", events.TimelineNote, msg+"while "+profile.Name+" updates")
}

func (s *Scheduler) runHarnessUpdate(state *harnessUpdateState, update *harnessUpdateRun, workspace domain.WorkspaceID, runID domain.RunID, spec runtime.Spec, exe string, profile harness.Profile) {
	timeout := s.cfg.harnessUpdateTimeout
	if timeout <= 0 {
		timeout = defaultHarnessUpdateTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	before, after, err := s.updateInContainer(ctx, update, spec, exe, profile.UpdateScript)
	if err != nil && ctx.Err() != nil {
		err = fmt.Errorf("the updater did not finish within %s", timeout)
	}
	next := harnessUpdateInterval
	switch {
	case err != nil:
		next = harnessUpdateRetry
		slog.Warn("scheduler: harness update failed", "run", runID, "harness", profile.Name, "error", err)
		msg := "could not update " + profile.Name
		if before != "" {
			msg += " from " + before
		}
		s.publishTimeline(context.Background(), workspace, runID, "", events.TimelineNote,
			msg+": "+publicRunStatusReason(harnessUpdateText(err.Error())))
	case after != before:
		msg := "updated " + profile.Name
		if before != "" {
			msg += " from " + before
		}
		if after == "" {
			after = "an unknown version"
		}
		s.publishTimeline(context.Background(), workspace, runID, "", events.TimelineNote, msg+" to "+after)
	}
	s.mu.Lock()
	state.next = s.cfg.Now().Add(next)
	state.running = nil
	close(update.done)
	s.mu.Unlock()
}

// updateInContainer runs script in a throwaway container with the run's
// image, user, environment and member home, and reports the harness version
// before and after it.
func (s *Scheduler) updateInContainer(ctx context.Context, update *harnessUpdateRun, spec runtime.Spec, exe, script string) (before, after string, err error) {
	if leftover, findErr := s.cfg.Runtime.FindByCreationKey(ctx, spec.CreationKey); findErr == nil {
		if destroyErr := s.cfg.Runtime.Destroy(ctx, leftover); destroyErr != nil {
			return "", "", fmt.Errorf("remove the update container a previous server left behind: %w", destroyErr)
		}
	} else if !errors.Is(findErr, runtime.ErrNotFound) {
		return "", "", findErr
	}
	// Resolve /proc/self/exe before another process interprets the mount source.
	if err = checkCoordinationMounts(spec.Mounts[1:]); err != nil {
		return "", "", fmt.Errorf("resolve updater helper: %w", err)
	}
	release, err := s.reserveCapacity(ctx)
	if err != nil {
		return "", "", err
	}
	defer release()
	cid, err := s.cfg.Runtime.Create(ctx, spec)
	if err != nil {
		return "", "", err
	}
	defer func() {
		destroyCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), harnessUpdateDestroyTimeout)
		defer cancel()
		if destroyErr := s.cfg.Runtime.Destroy(destroyCtx, cid); destroyErr != nil {
			slog.Warn("scheduler: destroy harness update container", "container", cid, "error", destroyErr)
		}
	}()
	if err = s.cfg.Runtime.Start(ctx, cid); err != nil {
		return "", "", err
	}
	release()
	before = s.harnessVersion(ctx, cid, exe, spec.WorkingDir)
	s.mu.Lock()
	update.before = before
	s.mu.Unlock()
	code, stdout, stderr, err := s.cfg.Runtime.Exec(ctx, cid, []string{"/bin/sh", "-c", script}, spec.WorkingDir)
	switch {
	case err != nil:
		return before, "", err
	case code != 0:
		if output := joinOutput(stdout, stderr); output != "" {
			return before, "", fmt.Errorf("the updater exited %d: %s", code, output)
		}
		return before, "", fmt.Errorf("the updater exited %d", code)
	}
	// A script that succeeds near the deadline must still get its version read.
	probeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), harnessVersionTimeout)
	defer cancel()
	return before, s.harnessVersion(probeCtx, cid, exe, spec.WorkingDir), nil
}

// harnessVersion is the first line of "<exe> --version", or "" when the
// probe fails: an unreadable version does not make the update fail.
func (s *Scheduler) harnessVersion(ctx context.Context, cid runtime.ID, exe, workDir string) string {
	code, stdout, _, err := s.cfg.Runtime.Exec(ctx, cid, []string{exe, "--version"}, workDir)
	if err != nil || code != 0 {
		return ""
	}
	line, _, _ := strings.Cut(stdout, "\n")
	version := []rune(harnessUpdateText(line))
	return string(version[:min(len(version), maxHarnessVersion)])
}

// installedInHome reports whether exe sits in the home's .local/bin. The
// final link is not followed, because vendor installers write absolute
// container-path symlinks, and os.Root keeps a member-planted link in an
// intermediate directory from reaching outside the home.
func installedInHome(homeHostPath, exe string) bool {
	root, err := os.OpenRoot(homeHostPath)
	if err != nil {
		return false
	}
	defer func() { _ = root.Close() }()
	_, err = root.Lstat(filepath.Join(".local", "bin", exe))
	return err == nil
}

// harnessUpdateText makes command output safe for one timeline line:
// control bytes dropped and every run of whitespace a single space.
func harnessUpdateText(text string) string {
	return strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && !unicode.IsSpace(r) {
			return -1
		}
		return r
	}, text)), " ")
}
