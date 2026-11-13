package scheduler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/memberhome"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/store"
)

func TestSaveEnvironmentRefusesWithoutARunningTerminal(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	_, err := e.sched.SaveEnvironment(t.Context(), e.member.ID)
	if !errors.Is(err, ErrTerminalNotRunning) || !strings.Contains(err.Error(), "open it with `aether terminal`") {
		t.Fatalf("SaveEnvironment with no terminal open = %v, want ErrTerminalNotRunning naming aether terminal", err)
	}
	if calls := e.rt.commitCalls(); len(calls) != 0 {
		t.Fatalf("commit calls = %+v, want none", calls)
	}
}

// stepClock advances deterministic lifecycle time for tests that need it.
func stepClock(step time.Duration) func() time.Time {
	next := time.Now()
	return func() time.Time {
		now := next
		next = next.Add(step)
		return now
	}
}

func TestSaveEnvironmentCommitsAndReplacesSavedImage(t *testing.T) {
	t.Parallel()
	now := time.Now()
	e := newTestEnv(t, func(cfg *Config) { cfg.Now = func() time.Time { return now } })
	terminal, err := e.sched.EnsureTerminal(t.Context(), e.member.ID)
	if err != nil {
		t.Fatalf("EnsureTerminal: %v", err)
	}
	first, err := e.sched.SaveEnvironment(t.Context(), e.member.ID)
	if err != nil {
		t.Fatalf("first SaveEnvironment: %v", err)
	}
	if !regexp.MustCompile(`^aether/member-` + regexp.QuoteMeta(string(e.member.ID)) + `:[a-z2-7]+$`).MatchString(first) {
		t.Fatalf("first image = %q, want member image tag", first)
	}
	member, err := e.db.GetMember(t.Context(), e.member.ID)
	if err != nil {
		t.Fatalf("GetMember: %v", err)
	}
	if member.Image != first {
		t.Fatalf("member image = %q, want %q", member.Image, first)
	}
	calls := e.rt.commitCalls()
	if len(calls) != 1 || calls[0].id != runtime.ID(terminal.ContainerID) || calls[0].tag != strings.Replace(first, memberImageRepo(e.member.ID), pendingImageRepo(e.member.ID), 1) {
		t.Fatalf("commit calls = %+v, want terminal %q committed as the pending copy of %q", calls, terminal.ContainerID, first)
	}

	second, err := e.sched.SaveEnvironment(t.Context(), e.member.ID)
	if err != nil {
		t.Fatalf("second SaveEnvironment: %v", err)
	}
	if second == first {
		t.Fatalf("second image reused first tag %q", second)
	}
	if !e.rt.hasImage(first) || !e.rt.hasImage(second) {
		t.Fatal("second save did not keep the first image as the previous one")
	}
	member, err = e.db.GetMember(t.Context(), e.member.ID)
	if err != nil {
		t.Fatalf("GetMember after second save: %v", err)
	}
	if member.Image != second {
		t.Fatalf("member image after second save = %q, want %q", member.Image, second)
	}
	for _, purpose := range []EnvironmentPurpose{EnvironmentPurposeRun, EnvironmentPurposeTerminal} {
		plan, planErr := e.sched.BuildEnvironmentPlan(t.Context(), nil, e.ws, member, profileForTerminalTest(), purpose)
		if planErr != nil {
			t.Fatalf("BuildEnvironmentPlan(%q): %v", purpose, planErr)
		}
		if plan.Image != second {
			t.Fatalf("purpose %q image = %q, want %q", purpose, plan.Image, second)
		}
	}
}

func TestResetEnvironmentStopsTerminalAndClearsImage(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	terminal, err := e.sched.EnsureTerminal(t.Context(), e.member.ID)
	if err != nil {
		t.Fatalf("EnsureTerminal: %v", err)
	}
	saved, err := e.sched.SaveEnvironment(t.Context(), e.member.ID)
	if err != nil {
		t.Fatalf("SaveEnvironment: %v", err)
	}
	if err = e.sched.ResetEnvironment(t.Context(), e.member.ID); err != nil {
		t.Fatalf("ResetEnvironment: %v", err)
	}
	if _, rowErr := e.db.GetTerminal(t.Context(), e.member.ID); !errors.Is(rowErr, store.ErrNotFound) {
		t.Fatalf("terminal row after reset = %v, want ErrNotFound", rowErr)
	}
	if _, getErr := e.rt.get(runtime.ID(terminal.ContainerID)); !errors.Is(getErr, runtime.ErrNotFound) {
		t.Fatalf("terminal container after reset = %v, want ErrNotFound", getErr)
	}
	member, err := e.db.GetMember(t.Context(), e.member.ID)
	if err != nil {
		t.Fatalf("GetMember after reset: %v", err)
	}
	if member.Image != "" {
		t.Fatalf("member image after reset = %q, want empty", member.Image)
	}
	if e.rt.hasImage(saved) {
		t.Fatalf("saved image %q still exists after reset", saved)
	}
}

func TestResetEnvironmentWithoutTerminalOrImageIsNoop(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	if err := e.sched.ResetEnvironment(context.Background(), e.member.ID); err != nil {
		t.Fatalf("ResetEnvironment: %v", err)
	}
	member, err := e.db.GetMember(t.Context(), e.member.ID)
	if err != nil {
		t.Fatalf("GetMember: %v", err)
	}
	if member.Image != "" || strings.TrimSpace(member.DisplayName) == "" {
		t.Fatalf("member after no-op reset = %+v", member)
	}
}

func TestSaveEnvironmentRetriesTagsStillInUse(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(cfg *Config) { cfg.Now = stepClock(2 * time.Second) })
	if _, err := e.sched.EnsureTerminal(t.Context(), e.member.ID); err != nil {
		t.Fatalf("EnsureTerminal: %v", err)
	}
	first, err := e.sched.SaveEnvironment(t.Context(), e.member.ID)
	if err != nil {
		t.Fatalf("first SaveEnvironment: %v", err)
	}
	// A run container still holds the first tag when the second save
	// happens: the daemon refuses the removal, the save still succeeds.
	e.rt.holdImage(first, true)
	second, err := e.sched.SaveEnvironment(t.Context(), e.member.ID)
	if err != nil {
		t.Fatalf("second SaveEnvironment: %v", err)
	}
	if !e.rt.hasImage(first) || !e.rt.hasImage(second) {
		t.Fatalf("images after refused removal: first=%v second=%v, want both kept", e.rt.hasImage(first), e.rt.hasImage(second))
	}
	// Once the container is gone, the next reset sweeps every stale tag.
	e.rt.holdImage(first, false)
	if err := e.sched.ResetEnvironment(t.Context(), e.member.ID); err != nil {
		t.Fatalf("ResetEnvironment: %v", err)
	}
	if e.rt.hasImage(first) || e.rt.hasImage(second) {
		t.Fatalf("images after reset: first=%v second=%v, want none", e.rt.hasImage(first), e.rt.hasImage(second))
	}
}

func TestResetEnvironmentDestroysStoppedKeptTerminal(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	terminal, err := e.sched.EnsureTerminal(t.Context(), e.member.ID)
	if err != nil {
		t.Fatalf("EnsureTerminal: %v", err)
	}
	if err = e.sched.StopTerminal(t.Context(), e.member.ID); err != nil {
		t.Fatalf("StopTerminal: %v", err)
	}
	if err = e.sched.ResetEnvironment(t.Context(), e.member.ID); err != nil {
		t.Fatalf("ResetEnvironment: %v", err)
	}
	if _, getErr := e.rt.get(runtime.ID(terminal.ContainerID)); !errors.Is(getErr, runtime.ErrNotFound) {
		t.Fatalf("kept container after reset = %v, want ErrNotFound", getErr)
	}
}

func TestSaveAfterStopReleasesPreviousSaveTag(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(cfg *Config) { cfg.Now = stepClock(2 * time.Second) })
	first, err := e.sched.EnsureTerminal(t.Context(), e.member.ID)
	if err != nil {
		t.Fatalf("EnsureTerminal: %v", err)
	}
	savedFirst, err := e.sched.SaveEnvironment(t.Context(), e.member.ID)
	if err != nil {
		t.Fatalf("first SaveEnvironment: %v", err)
	}
	if err = e.sched.StopTerminal(t.Context(), e.member.ID); err != nil {
		t.Fatalf("StopTerminal: %v", err)
	}
	reopened, err := e.sched.EnsureTerminal(t.Context(), e.member.ID)
	if err != nil || reopened.ContainerID != first.ContainerID {
		t.Fatalf("reopen = %+v, %v, want the kept container", reopened, err)
	}
	savedSecond, err := e.sched.SaveEnvironment(t.Context(), e.member.ID)
	if err != nil {
		t.Fatalf("second SaveEnvironment: %v", err)
	}
	if commits := e.rt.commitCalls(); commits[len(commits)-1].id != runtime.ID(first.ContainerID) {
		t.Fatalf("second save committed %q, want the kept container %q", commits[len(commits)-1].id, first.ContainerID)
	}
	savedThird, err := e.sched.SaveEnvironment(t.Context(), e.member.ID)
	if err != nil {
		t.Fatalf("third SaveEnvironment: %v", err)
	}
	if e.rt.hasImage(savedFirst) || !e.rt.hasImage(savedSecond) || !e.rt.hasImage(savedThird) {
		t.Fatalf("images after third save: first=%v second=%v third=%v, want the second and third", e.rt.hasImage(savedFirst), e.rt.hasImage(savedSecond), e.rt.hasImage(savedThird))
	}
}

func TestStoppedKeptTerminalDoesNotOwnCache(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	if _, err := e.sched.EnsureTerminal(t.Context(), e.member.ID); err != nil {
		t.Fatalf("EnsureTerminal: %v", err)
	}
	owned := func() bool {
		owners := newCacheOwnership()
		info := memberhome.CacheInfo{Member: e.member.ID, Pool: memberhome.CachePoolTerminal}
		if err := e.sched.resolveCacheRuntimeOwners(t.Context(), &info, &owners); err != nil {
			t.Fatalf("resolveCacheRuntimeOwners: %v", err)
		}
		return owners.terminal[e.member.ID]
	}
	if !owned() {
		t.Fatal("running terminal does not own its cache pool")
	}
	if err := e.sched.StopTerminal(t.Context(), e.member.ID); err != nil {
		t.Fatalf("StopTerminal: %v", err)
	}
	if owned() {
		t.Fatal("stopped kept terminal still owns its cache pool")
	}
}

// bootChecker is a runtime that, like the VM runtime, checks an image by
// booting it instead of exporting it.
type bootChecker struct {
	*fakeRuntime
	err    error
	checks []string
}

func (r *bootChecker) CheckImage(_ context.Context, ref, user string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.checks = append(r.checks, ref+" as "+user)
	return r.err
}

func (r *bootChecker) ExportImage(context.Context, string) (io.ReadCloser, error) {
	return nil, errors.New("a runtime that boots images never exports one to check it")
}

func TestSaveEnvironmentBootsTheImageWhenTheRuntimeCan(t *testing.T) {
	t.Parallel()
	var rt *bootChecker
	e := newTestEnv(t, func(cfg *Config) {
		rt = &bootChecker{fakeRuntime: cfg.Runtime.(*fakeRuntime)}
		cfg.Runtime = rt
	})
	terminal, err := e.sched.EnsureTerminal(t.Context(), e.member.ID)
	if err != nil {
		t.Fatalf("EnsureTerminal: %v", err)
	}
	info, err := rt.Inspect(t.Context(), runtime.ID(terminal.ContainerID))
	if err != nil {
		t.Fatal(err)
	}
	saved, err := e.sched.SaveEnvironment(t.Context(), e.member.ID)
	if err != nil {
		t.Fatalf("SaveEnvironment: %v", err)
	}
	pending := strings.Replace(saved, memberImageRepo(e.member.ID), pendingImageRepo(e.member.ID), 1)
	if len(rt.checks) != 1 || rt.checks[0] != pending+" as "+info.User {
		t.Fatalf("checks = %v, want %s booted as the terminal's user %q", rt.checks, pending, info.User)
	}

	rt.err = fmt.Errorf("vm runtime: VM check-1 failed to boot: switch to the root disk: mount: structure needs cleaning: %w", runtime.ErrUnstartableImage)
	_, err = e.sched.SaveEnvironment(t.Context(), e.member.ID)
	if err == nil || !strings.Contains(err.Error(), "would not start") || !strings.Contains(err.Error(), "structure needs cleaning") {
		t.Fatalf("SaveEnvironment of an image that does not boot = %v", err)
	}
	if member, getErr := e.db.GetMember(t.Context(), e.member.ID); getErr != nil || member.Image != saved {
		t.Fatalf("member image = %q, %v, want %q kept", member.Image, getErr, saved)
	}
}

func TestKeptTerminalTheHostCannotStartIsKept(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	first, err := e.sched.EnsureTerminal(t.Context(), e.member.ID)
	if err != nil {
		t.Fatalf("EnsureTerminal: %v", err)
	}
	if err = e.sched.StopTerminal(t.Context(), e.member.ID); err != nil {
		t.Fatalf("StopTerminal: %v", err)
	}
	e.rt.mu.Lock()
	e.rt.startErr = fmt.Errorf("vm runtime: VM terminal-x stopped while booting: %w", runtime.ErrStartLater)
	e.rt.mu.Unlock()
	if _, err = e.sched.EnsureTerminal(t.Context(), e.member.ID); !errors.Is(err, runtime.ErrStartLater) {
		t.Fatalf("EnsureTerminal on a host that cannot start it now = %v", err)
	}
	if _, getErr := e.rt.get(runtime.ID(first.ContainerID)); getErr != nil {
		t.Fatalf("the kept terminal was replaced, unsaved changes and all: %v", getErr)
	}
	e.rt.mu.Lock()
	e.rt.startErr = nil
	e.rt.mu.Unlock()
	reopened, err := e.sched.EnsureTerminal(t.Context(), e.member.ID)
	if err != nil || reopened.ContainerID != first.ContainerID {
		t.Fatalf("reopen = %+v, %v, want the kept terminal %q", reopened, err, first.ContainerID)
	}
}
