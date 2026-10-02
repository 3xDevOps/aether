package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/runtime"
)

const (
	harnessUpdateInterval       = 6 * time.Hour
	harnessUpdateRetry          = 15 * time.Minute
	defaultHarnessUpdateTimeout = 3 * time.Minute
	harnessUpdateSlow           = 5 * time.Second
	harnessUpdateKillPoll       = 250 * time.Millisecond
	harnessUpdateDestroyTimeout = 30 * time.Second
	maxHarnessVersion           = 64
)

type harnessUpdateKey struct {
	home    string
	harness string
}

type harnessUpdateState struct {
	// lock is a one-slot semaphore a waiter can abandon when its launch is
	// cancelled. next is read and written only while holding it.
	lock chan struct{}
	next time.Time
}

// updateHarness brings the shipped harness installed in the run's member
// home current before the run container exists. Nothing it does can fail
// the launch: the run starts on whatever version is installed afterwards.
func (s *Scheduler) updateHarness(ctx context.Context, entry *supervised, run *domain.Run, plan *EnvironmentPlan, profile harness.Profile) {
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
		state = &harnessUpdateState{lock: make(chan struct{}, 1)}
		s.harnessUpdates[key] = state
	}
	s.mu.Unlock()

	updateCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go s.cancelOnKill(updateCtx, cancel, entry)
	select {
	case state.lock <- struct{}{}:
	case <-updateCtx.Done():
		return
	}
	defer func() { <-state.lock }()
	if s.cfg.Now().Before(state.next) {
		return
	}

	slow := time.AfterFunc(harnessUpdateSlow, func() {
		s.publishTimeline(ctx, run.WorkspaceID, run.ID, "", events.TimelineNote,
			"updating "+profile.Name+" before launch")
	})
	before, after, err := s.runHarnessUpdate(updateCtx, plan, home, exe, profile)
	slow.Stop()
	if updateCtx.Err() != nil {
		return
	}
	if err != nil {
		state.next = s.cfg.Now().Add(harnessUpdateRetry)
		slog.Warn("scheduler: harness update failed", "run", run.ID, "harness", profile.Name, "error", err)
		installed := "the installed version"
		if before != "" {
			installed += " " + before
		}
		s.publishTimeline(ctx, run.WorkspaceID, run.ID, "", events.TimelineNote,
			fmt.Sprintf("could not update %s before launch; starting %s: %s",
				profile.Name, installed, publicRunStatusReason(harnessUpdateText(err.Error()))))
		return
	}
	state.next = s.cfg.Now().Add(harnessUpdateInterval)
	if after == "" || after == before {
		return
	}
	msg := "updated " + profile.Name + " before launch, "
	if before != "" {
		msg += "from " + before + " "
	}
	s.publishTimeline(ctx, run.WorkspaceID, run.ID, "", events.TimelineNote, msg+"to "+after)
}

// cancelOnKill ends a pre-launch update when the run is killed. Kill only
// flags a provisioning run, and an update can outlast the launch's other
// steps by minutes.
func (s *Scheduler) cancelOnKill(ctx context.Context, cancel context.CancelFunc, entry *supervised) {
	tick := time.NewTicker(harnessUpdateKillPoll)
	defer tick.Stop()
	for {
		s.mu.Lock()
		killed := entry.killRequested
		s.mu.Unlock()
		if killed {
			cancel()
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// runHarnessUpdate runs the profile's UpdateScript in a throwaway container
// with the run's image, user, environment and member home, and reports the
// harness version before and after it.
func (s *Scheduler) runHarnessUpdate(ctx context.Context, plan *EnvironmentPlan, home runtime.Mount, exe string, profile harness.Profile) (before, after string, err error) {
	// One name per home and harness: the state lock allows only one such
	// container at a time, so a match is left over from a crashed server.
	name := "harness-update-" + filepath.Base(home.HostPath) + "-" + profile.Name
	if leftover, findErr := s.cfg.Runtime.FindByCreationKey(ctx, name); findErr == nil {
		if destroyErr := s.cfg.Runtime.Destroy(ctx, leftover); destroyErr != nil {
			return "", "", fmt.Errorf("remove the update container a previous server left behind: %w", destroyErr)
		}
	} else if !errors.Is(findErr, runtime.ErrNotFound) {
		return "", "", findErr
	}
	cid, err := s.cfg.Runtime.Create(ctx, runtime.Spec{
		Name:       name,
		Image:      plan.Image,
		Env:        plan.Env,
		Mounts:     []runtime.Mount{home},
		User:       plan.User,
		WorkingDir: plan.Home,
		// Outlives the execs, and ends on its own if the server dies first.
		Command:     []string{"/bin/sh", "-c", "sleep 600"},
		CreationKey: name,
	})
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
	timeout := s.cfg.harnessUpdateTimeout
	if timeout <= 0 {
		timeout = defaultHarnessUpdateTimeout
	}
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	before = s.harnessVersion(execCtx, cid, exe, plan.Home)
	code, stdout, stderr, err := s.cfg.Runtime.Exec(execCtx, cid, []string{"/bin/sh", "-c", profile.UpdateScript}, plan.Home)
	switch {
	case err != nil && ctx.Err() == nil && execCtx.Err() != nil:
		return before, "", fmt.Errorf("the updater did not finish within %s", timeout)
	case err != nil:
		return before, "", err
	case code != 0:
		if output := joinOutput(stdout, stderr); output != "" {
			return before, "", fmt.Errorf("the updater exited %d: %s", code, output)
		}
		return before, "", fmt.Errorf("the updater exited %d", code)
	}
	return before, s.harnessVersion(execCtx, cid, exe, plan.Home), nil
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
