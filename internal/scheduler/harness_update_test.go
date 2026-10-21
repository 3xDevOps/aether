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
	// afterFails makes the version probe fail once the update ran.
	afterFails bool
}

func newUpdateRuntime(t *testing.T) *updateRuntime {
	r := &updateRuntime{fakeRuntime: newFakeRuntime(), before: "2.1.0 (Claude Code)", after: "2.1.0 (Claude Code)", started: make(chan struct{}, 8)}
	r.execHandler = func(_ runtime.ID, argv []string) (int, string, error) {
		if len(argv) == 2 && argv[1] == "--version" {
			r.mu.Lock()
			defer r.mu.Unlock()
			if r.updated && r.afterFails {
				return 1, "", nil
			}
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

// launchNotes launches harnessName, lets the harness update it started
// finish, and returns the timeline notes published on the run by then.
func launchNotes(t *testing.T, e *testEnv, harnessName string, mode domain.LaunchMode) []events.Event {
	t.Helper()
	sub := e.subscribe(t)
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "task", harnessName, mode)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if run.Status != domain.RunRunning {
		t.Fatalf("run status = %s, want running", run.Status)
	}
	waitHarnessUpdates(t, e)
	marker, err := e.bus.Publish(t.Context(), events.Event{
		WorkspaceID: e.ws.ID, RunID: run.ID,
		Payload: events.TimelinePayload{Kind: events.TimelineNote, Message: "end of update notes"},
	})
	if err != nil {
		t.Fatalf("publish marker: %v", err)
	}
	var notes []events.Event
	for {
		ev := waitTimelineEvent(t, sub, run.ID, events.TimelineNote)
		if ev.ID == marker.ID {
			return notes
		}
		notes = append(notes, ev)
	}
}

// waitHarnessUpdates waits until no harness update runs.
func waitHarnessUpdates(t *testing.T, e *testEnv) {
	t.Helper()
	waitFor(t, "harness updates to finish", func() bool {
		e.sched.mu.Lock()
		defer e.sched.mu.Unlock()
		for _, state := range e.sched.harnessUpdates {
			if state.running != nil {
				return false
			}
		}
		return true
	})
}

func noteMessages(notes []events.Event) []string {
	var out []string
	for _, n := range notes {
		out = append(out, n.Payload.(events.TimelinePayload).Message)
	}
	return out
}

func TestHarnessUpdateCleansUpContainer(t *testing.T) {
	t.Parallel()
	e, rt, _ := newUpdateEnv(t, nil)
	installInHome(t, e, "claude")
	rt.after = "2.1.1 (Claude Code)"

	launchNotes(t, e, "claude", domain.LaunchTUI)
	if _, err := e.rt.get(rt.ids[0]); !errors.Is(err, runtime.ErrNotFound) {
		t.Fatalf("update container still exists: %v", err)
	}
}

// An update whose new version cannot be read still says it updated, and
// counts as a success.
func TestHarnessUpdateUnreadableNewVersion(t *testing.T) {
	t.Parallel()
	e, rt, clock := newUpdateEnv(t, nil)
	installInHome(t, e, "claude")
	rt.afterFails = true

	notes := launchNotes(t, e, "claude", domain.LaunchTUI)
	want := []string{"updated claude from 2.1.0 (Claude Code) to an unknown version for the next launch"}
	if got := noteMessages(notes); !slices.Equal(got, want) {
		t.Fatalf("notes = %q, want %q", got, want)
	}
	clock.advance(harnessUpdateInterval - time.Minute)
	launchNotes(t, e, "claude", domain.LaunchTUI)
	if n := len(rt.updates()); n != 1 {
		t.Fatalf("update containers inside the interval = %d, want 1", n)
	}
}

func TestHarnessUpdateCurrentIsQuietAndRechecksAfterInterval(t *testing.T) {
	t.Parallel()
	e, rt, clock := newUpdateEnv(t, nil)
	installInHome(t, e, "claude")

	if notes := launchNotes(t, e, "claude", domain.LaunchTUI); len(notes) != 0 {
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

	notes := launchNotes(t, e, "claude", domain.LaunchTUI)
	want := []string{"could not update claude from 2.1.0 (Claude Code): " +
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

	notes := launchNotes(t, e, "claude", domain.LaunchTUI)
	want := []string{"could not update claude from 2.1.0 (Claude Code): " +
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

	notes := launchNotes(t, e, "claude", domain.LaunchTUI)
	want := []string{"could not update claude: " +
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

// launchAsync launches claude in the background and returns its result.
func launchAsync(ctx context.Context, e *testEnv) chan launchResult {
	out := make(chan launchResult, 1)
	go func() {
		run, err := e.sched.Launch(ctx, e.ws.ID, e.member.ID, e.member.ID, "task", "claude", domain.LaunchTUI)
		out <- launchResult{run, err}
	}()
	return out
}

type launchResult struct {
	run *domain.Run
	err error
}

func awaitLaunch(t *testing.T, results chan launchResult) launchResult {
	t.Helper()
	select {
	case got := <-results:
		return got
	case <-time.After(waitTimeout):
		t.Fatal("Launch did not return")
		return launchResult{}
	}
}

func (r *updateRuntime) releaseUpdate() {
	r.mu.Lock()
	defer r.mu.Unlock()
	close(r.release)
	r.release = nil
}

// updateExecs counts the update scripts that have started.
func updateExecs(e *testEnv) int {
	n := 0
	for _, call := range e.rt.execRuns() {
		if len(call.argv) == 3 && call.argv[0] == "/bin/sh" {
			n++
		}
	}
	return n
}

// A launch starts on the installed version while the update it started is
// held, and the update's result lands on that run later.
func TestHarnessUpdateDoesNotDelayLaunch(t *testing.T) {
	t.Parallel()
	e, rt, _ := newUpdateEnv(t, nil)
	installInHome(t, e, "claude")
	rt.after = "2.1.1 (Claude Code)"
	rt.release = make(chan struct{})
	sub := e.subscribe(t)

	results := launchAsync(t.Context(), e)
	<-rt.started
	var launched launchResult
	select {
	case launched = <-results:
	case <-time.After(10 * time.Second):
		t.Fatal("Launch waited for the held update")
	}
	if launched.err != nil || launched.run.Status != domain.RunRunning {
		t.Fatalf("launch = %+v, want running", launched)
	}
	rt.releaseUpdate()
	ev := waitTimelineEvent(t, sub, launched.run.ID, events.TimelineNote)
	if msg := ev.Payload.(events.TimelinePayload).Message; msg != "updated claude from 2.1.0 (Claude Code) to 2.1.1 (Claude Code) for the next launch" {
		t.Fatalf("result note = %q", msg)
	}
}

// While one update is held open, a second launch on the same home starts
// without starting its own.
func TestHarnessUpdateRunsOncePerHome(t *testing.T) {
	t.Parallel()
	e, rt, _ := newUpdateEnv(t, nil)
	installInHome(t, e, "claude")
	rt.release = make(chan struct{})

	first := awaitLaunch(t, launchAsync(t.Context(), e))
	<-rt.started
	second := awaitLaunch(t, launchAsync(t.Context(), e))
	for _, got := range []launchResult{first, second} {
		if got.err != nil || got.run.Status != domain.RunRunning {
			t.Fatalf("launch = %+v, want running", got)
		}
	}
	if n, execs := len(rt.updates()), updateExecs(e); n != 1 || execs != 1 {
		t.Fatalf("while held: update containers = %d, update execs = %d, want 1 and 1", n, execs)
	}
	rt.releaseUpdate()
	waitFor(t, "the held update to finish", func() bool {
		_, err := e.rt.get(rt.ids[0])
		return errors.Is(err, runtime.ErrNotFound)
	})
	if n, execs := len(rt.updates()), updateExecs(e); n != 1 || execs != 1 {
		t.Fatalf("after release: update containers = %d, update execs = %d, want 1 and 1", n, execs)
	}
}

// Killing the run that started an update ends the run, not the install.
func TestHarnessUpdateKillDoesNotStopUpdate(t *testing.T) {
	t.Parallel()
	e, rt, _ := newUpdateEnv(t, nil)
	installInHome(t, e, "claude")
	rt.after = "2.1.1 (Claude Code)"
	rt.release = make(chan struct{})
	sub := e.subscribe(t)

	results := launchAsync(t.Context(), e)
	provisioning := waitStatusEvent(t, sub, "", domain.RunProvisioning)
	<-rt.started
	if err := e.sched.Kill(t.Context(), provisioning.RunID, e.member.ID); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	awaitLaunch(t, results)
	if r := e.waitStoreStatus(t, provisioning.RunID, domain.RunAbandoned); r.Reason != "killed" {
		t.Fatalf("run reason = %q, want killed", r.Reason)
	}
	rt.releaseUpdate()
	for {
		ev := waitTimelineEvent(t, sub, provisioning.RunID, events.TimelineNote)
		if msg := ev.Payload.(events.TimelinePayload).Message; strings.HasPrefix(msg, "updated claude") {
			return
		}
	}
}

func TestHarnessUpdateHeadless(t *testing.T) {
	t.Parallel()
	e, rt, _ := newUpdateEnv(t, nil)
	installInHome(t, e, "claude")
	rt.after = "2.1.1 (Claude Code)"

	notes := launchNotes(t, e, "claude", domain.LaunchHeadless)
	if len(notes) != 1 || len(rt.updates()) != 1 {
		t.Fatalf("headless launch: notes = %q, update containers = %d, want one update", noteMessages(notes), len(rt.updates()))
	}
}
