package scheduler

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// updateRuntime records every update container the scheduler creates and
// answers the update container's execs like a vendor CLI would.
type updateRuntime struct {
	*fakeRuntime
	mu        sync.Mutex
	specs     []runtime.Spec
	ids       []runtime.ID
	createErr error
	before    string
	after     string
	code      int
	output    string
	// release, when set, holds the updater until it is closed.
	release chan struct{}
	started chan struct{}
	updated bool
}

func newUpdateRuntime(t *testing.T) *updateRuntime {
	r := &updateRuntime{fakeRuntime: newFakeRuntime(), before: "2.1.0 (Claude Code)", after: "2.1.0 (Claude Code)", started: make(chan struct{}, 8)}
	r.execHandler = func(_ runtime.ID, argv []string) (int, string, error) {
		if len(argv) == 2 && argv[1] == "--version" {
			r.mu.Lock()
			defer r.mu.Unlock()
			if r.updated {
				return 0, r.after + "\n", nil
			}
			return 0, r.before + "\n", nil
		}
		r.started <- struct{}{}
		r.mu.Lock()
		release := r.release
		r.mu.Unlock()
		if release != nil {
			<-release
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		r.updated = r.code == 0
		return r.code, r.output, nil
	}
	t.Cleanup(func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.release != nil {
			close(r.release)
			r.release = nil
		}
	})
	return r
}

func (r *updateRuntime) Create(ctx context.Context, spec runtime.Spec) (runtime.ID, error) {
	if !strings.HasPrefix(spec.CreationKey, "harness-update-") {
		return r.fakeRuntime.Create(ctx, spec)
	}
	r.mu.Lock()
	r.specs = append(r.specs, spec)
	err := r.createErr
	r.mu.Unlock()
	if err != nil {
		return "", err
	}
	id, err := r.fakeRuntime.Create(ctx, spec)
	r.mu.Lock()
	r.ids = append(r.ids, id)
	r.mu.Unlock()
	return id, err
}

func (r *updateRuntime) updates() []runtime.Spec {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.specs)
}

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func newUpdateEnv(t *testing.T, mutate func(*Config)) (*testEnv, *updateRuntime, *testClock) {
	t.Helper()
	rt := newUpdateRuntime(t)
	clock := &testClock{now: time.Unix(1_700_000_000, 0)}
	e := newTestEnv(t, func(cfg *Config) {
		cfg.Runtime = rt
		cfg.Now = clock.Now
		if mutate != nil {
			mutate(cfg)
		}
	})
	e.rt = rt.fakeRuntime
	return e, rt, clock
}

// installInHome marks a harness as installed in the member home the way a
// vendor installer does: an executable in ~/.local/bin.
func installInHome(t *testing.T, e *testEnv, exe string) {
	t.Helper()
	home, err := e.cfg.Homes.Path(e.member.ID)
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, exe), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// launchNotes launches harnessName and returns the run with the timeline
// notes published on it before it reached running.
func launchNotes(t *testing.T, e *testEnv, harnessName string, mode domain.LaunchMode) (*domain.Run, []events.Event) {
	t.Helper()
	sub := e.subscribe(t)
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "task", harnessName, mode)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if run.Status != domain.RunRunning {
		t.Fatalf("run status = %s, want running", run.Status)
	}
	var notes []events.Event
	for {
		select {
		case ev := <-sub.Events():
			if ev.RunID != run.ID {
				continue
			}
			if p, ok := ev.Payload.(events.TimelinePayload); ok && p.Kind == events.TimelineNote {
				notes = append(notes, ev)
			}
			if p, ok := ev.Payload.(events.RunStatusPayload); ok && p.To == domain.RunRunning {
				return run, notes
			}
		case <-time.After(waitTimeout):
			t.Fatalf("timed out waiting for run %s to reach running", run.ID)
		}
	}
}

func noteMessages(notes []events.Event) []string {
	var out []string
	for _, n := range notes {
		out = append(out, n.Payload.(events.TimelinePayload).Message)
	}
	return out
}

func TestHarnessUpdateNotesVersionChange(t *testing.T) {
	t.Parallel()
	e, rt, _ := newUpdateEnv(t, nil)
	installInHome(t, e, "claude")
	rt.after = "2.1.1 (Claude Code)"

	run, notes := launchNotes(t, e, "claude", domain.LaunchTUI)
	want := []string{"updated claude before launch, from 2.1.0 (Claude Code) to 2.1.1 (Claude Code)"}
	if got := noteMessages(notes); !slices.Equal(got, want) {
		t.Fatalf("notes = %q, want %q", got, want)
	}
	if notes[0].ActorID != "" {
		t.Errorf("note actor = %q, want the server (empty)", notes[0].ActorID)
	}
	runContainer := e.rt.byName(string(run.ID))
	if runContainer == nil {
		t.Fatal("run container was not created")
	}
	specs := rt.updates()
	if len(specs) != 1 {
		t.Fatalf("update containers = %d, want 1", len(specs))
	}
	spec := specs[0]
	home, _ := e.cfg.Homes.Path(e.member.ID)
	wantMounts := []runtime.Mount{{HostPath: home, ContainerPath: "/root"}}
	if spec.Image != runContainer.spec.Image || spec.User != runContainer.spec.User ||
		!slices.Equal(spec.Mounts, wantMounts) || spec.WorkingDir != "/root" ||
		spec.WorktreeHostPath != "" || spec.SetupScript != "" || spec.TTY || spec.Env["HOME"] != "/root" {
		t.Fatalf("update container spec = %+v, want the run's image, user and home alone", spec)
	}
	var ranUpdate bool
	for _, call := range e.rt.execRuns() {
		if slices.Equal(call.argv, []string{"/bin/sh", "-c", "claude update"}) && call.workDir == "/root" {
			ranUpdate = true
		}
	}
	if !ranUpdate {
		t.Fatalf("execs = %+v, want claude update in the home", e.rt.execRuns())
	}
	if _, err := e.rt.get(rt.ids[0]); !errors.Is(err, runtime.ErrNotFound) {
		t.Fatalf("update container still exists: %v", err)
	}
}

func TestHarnessUpdateCurrentIsQuietAndRechecksAfterInterval(t *testing.T) {
	t.Parallel()
	e, rt, clock := newUpdateEnv(t, nil)
	installInHome(t, e, "claude")

	if _, notes := launchNotes(t, e, "claude", domain.LaunchTUI); len(notes) != 0 {
		t.Fatalf("notes = %q, want none for a current harness", noteMessages(notes))
	}
	clock.advance(harnessUpdateInterval - time.Minute)
	launchNotes(t, e, "claude", domain.LaunchTUI)
	if n := len(rt.updates()); n != 1 {
		t.Fatalf("update containers inside the interval = %d, want 1", n)
	}
	clock.advance(time.Minute)
	launchNotes(t, e, "claude", domain.LaunchTUI)
	if n := len(rt.updates()); n != 2 {
		t.Fatalf("update containers after the interval = %d, want 2", n)
	}
}

func TestHarnessUpdateFailureStillLaunches(t *testing.T) {
	t.Parallel()
	e, rt, clock := newUpdateEnv(t, nil)
	installInHome(t, e, "claude")
	rt.code = 1
	rt.output = "Failed to fetch\x1b[0m latest version:\nnetwork unreachable\n"

	_, notes := launchNotes(t, e, "claude", domain.LaunchTUI)
	want := []string{"could not update claude before launch; starting the installed version 2.1.0 (Claude Code): " +
		"the updater exited 1: Failed to fetch[0m latest version: network unreachable"}
	if got := noteMessages(notes); !slices.Equal(got, want) {
		t.Fatalf("notes = %q, want %q", got, want)
	}
	clock.advance(harnessUpdateRetry - time.Second)
	launchNotes(t, e, "claude", domain.LaunchTUI)
	if n := len(rt.updates()); n != 1 {
		t.Fatalf("update containers before the retry delay = %d, want 1", n)
	}
	clock.advance(time.Second)
	launchNotes(t, e, "claude", domain.LaunchTUI)
	if n := len(rt.updates()); n != 2 {
		t.Fatalf("update containers after the retry delay = %d, want 2", n)
	}
}

func TestHarnessUpdateTimeoutStillLaunches(t *testing.T) {
	t.Parallel()
	e, rt, _ := newUpdateEnv(t, func(cfg *Config) { cfg.harnessUpdateTimeout = 50 * time.Millisecond })
	installInHome(t, e, "claude")
	rt.release = make(chan struct{})

	_, notes := launchNotes(t, e, "claude", domain.LaunchTUI)
	want := []string{"could not update claude before launch; starting the installed version 2.1.0 (Claude Code): " +
		"the updater did not finish within 50ms"}
	if got := noteMessages(notes); !slices.Equal(got, want) {
		t.Fatalf("notes = %q, want %q", got, want)
	}
}

func TestHarnessUpdateCreateFailureStillLaunches(t *testing.T) {
	t.Parallel()
	e, rt, _ := newUpdateEnv(t, nil)
	installInHome(t, e, "claude")
	rt.createErr = errors.New("runtime: create container: no space left on device")

	_, notes := launchNotes(t, e, "claude", domain.LaunchTUI)
	want := []string{"could not update claude before launch; starting the installed version: " +
		"runtime: create container: no space left on device"}
	if got := noteMessages(notes); !slices.Equal(got, want) {
		t.Fatalf("notes = %q, want %q", got, want)
	}
}

func TestHarnessUpdateSkipped(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		harness string
		install string
		mutate  func(*Config)
	}{
		"not installed in the home": {harness: "claude"},
		"opencode":                  {harness: "opencode", install: "opencode"},
		"admin override":            {harness: "claude", install: "claude", mutate: func(cfg *Config) { cfg.Harnesses["claude"] = HarnessSpec{TUIArgs: []string{"claude", "{task}"}} }},
		"updates disabled":          {harness: "claude", install: "claude", mutate: func(cfg *Config) { cfg.HarnessUpdateDisabled = true }},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e, rt, _ := newUpdateEnv(t, tc.mutate)
			if tc.install != "" {
				installInHome(t, e, tc.install)
			}
			launchNotes(t, e, tc.harness, domain.LaunchTUI)
			if specs := rt.updates(); len(specs) != 0 || len(e.rt.execRuns()) != 0 {
				t.Fatalf("update containers = %d, execs = %+v, want none", len(specs), e.rt.execRuns())
			}
		})
	}
}

func TestHarnessUpdateRunsOncePerHome(t *testing.T) {
	t.Parallel()
	e, rt, _ := newUpdateEnv(t, nil)
	installInHome(t, e, "claude")
	rt.release = make(chan struct{})
	sub := e.subscribe(t)

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Go(func() {
			_, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "task", "claude", domain.LaunchTUI)
			errs <- err
		})
	}
	<-rt.started
	waitStatusEvent(t, sub, "", domain.RunProvisioning)
	waitStatusEvent(t, sub, "", domain.RunProvisioning)
	rt.mu.Lock()
	close(rt.release)
	rt.release = nil
	rt.mu.Unlock()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("Launch: %v", err)
		}
	}
	if n := len(rt.updates()); n != 1 {
		t.Fatalf("update containers = %d, want 1 for two launches on one home", n)
	}
}

func TestHarnessUpdateKilledRunEndsKilled(t *testing.T) {
	t.Parallel()
	e, rt, _ := newUpdateEnv(t, nil)
	installInHome(t, e, "claude")
	rt.release = make(chan struct{})
	sub := e.subscribe(t)

	errs := make(chan error, 1)
	go func() {
		_, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "task", "claude", domain.LaunchTUI)
		errs <- err
	}()
	provisioning := waitStatusEvent(t, sub, "", domain.RunProvisioning)
	<-rt.started
	if err := e.sched.Kill(t.Context(), provisioning.RunID, e.member.ID); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	select {
	case err := <-errs:
		if err == nil {
			t.Fatal("Launch succeeded after a kill during the update")
		}
	case <-time.After(waitTimeout):
		t.Fatal("Launch did not return after a kill during the update")
	}
	if r := e.waitStoreStatus(t, provisioning.RunID, domain.RunAbandoned); r.Reason != "killed" {
		t.Fatalf("run reason = %q, want killed", r.Reason)
	}
	if _, err := e.rt.get(rt.ids[0]); !errors.Is(err, runtime.ErrNotFound) {
		t.Fatalf("update container still exists: %v", err)
	}
}

func TestHarnessUpdateHeadless(t *testing.T) {
	t.Parallel()
	e, rt, _ := newUpdateEnv(t, nil)
	installInHome(t, e, "claude")
	rt.after = "2.1.1 (Claude Code)"

	_, notes := launchNotes(t, e, "claude", domain.LaunchHeadless)
	if len(notes) != 1 || len(rt.updates()) != 1 {
		t.Fatalf("headless launch: notes = %q, update containers = %d, want one update", noteMessages(notes), len(rt.updates()))
	}
}
