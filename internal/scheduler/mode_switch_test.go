package scheduler

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/agentstatus"
	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/store"
)

// fakeSupervisor answers the swap exec as the run supervisor would: it runs
// what the next-command file holds, which the test reads back as the bodies
// it ran ("" for a login shell).
type fakeSupervisor struct {
	coord *fakeCoordinator
	run   domain.RunID

	mu sync.Mutex
	// exitStatus, when set, is the status a resumed terminal exits with
	// as it starts.
	exitStatus string
	// hold, when set, keeps the swap exec waiting until it is closed.
	hold chan struct{}
	// faults fail the next swap execs in order: "down" before the swap
	// reaches the supervisor, "lost" after it swapped.
	faults []string
	bodies []string
	// state is what the supervisor's state file holds.
	state string
}

func (f *fakeSupervisor) exec(_ runtime.ID, argv []string) (int, string, error) {
	if len(argv) == 5 && argv[3] == "aether-switch" {
		f.mu.Lock()
		defer f.mu.Unlock()
		return 0, f.state, nil
	}
	if len(argv) < 6 || argv[3] != "aether-swap" {
		return 0, "", nil
	}
	f.mu.Lock()
	hold := f.hold
	var fault string
	if len(f.faults) > 0 {
		fault, f.faults = f.faults[0], f.faults[1:]
	}
	f.mu.Unlock()
	if hold != nil {
		<-hold
	}
	if fault == "down" {
		return 0, "", errors.New("docker: connection reset")
	}
	raw, err := os.ReadFile(filepath.Join(f.coord.root, string(f.run), coordtransport.NextCommandName))
	if err != nil {
		return 1, "", err
	}
	nonce, body, _ := strings.Cut(string(raw), "\n")
	if nonce != "# "+argv[4] {
		return 2, "", errors.New("the swap does not name the next command's nonce")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bodies = append(f.bodies, body)
	f.state = argv[4] + " started\n"
	if fault == "lost" {
		return 0, "", errors.New("docker: connection reset")
	}
	if body != "" && f.exitStatus != "" {
		f.state = argv[4] + " exited " + f.exitStatus + "\n"
		return 3, f.exitStatus + "\n", nil
	}
	return 0, "", nil
}

func (f *fakeSupervisor) ran() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.bodies)
}

func admitNow(begin func() error) error { return begin() }

type switchEnv struct {
	*testEnv
	rt    *acpRuntime
	coord *fakeCoordinator
	sup   *fakeSupervisor
}

func newSwitchEnv(t *testing.T, opts ...func(*Config)) *switchEnv {
	t.Helper()
	e, rt := newACPEnv(t, append(opts, withServerBinary(fakeServerBinary(t, "#!/bin/sh\necho aether\n")))...)
	installInHome(t, e, "omp")
	coord, _ := withCoordination(t, e)
	sup := &fakeSupervisor{coord: coord}
	rt.execHandler = sup.exec
	return &switchEnv{testEnv: e, rt: rt, coord: coord, sup: sup}
}

func (e *switchEnv) launch(t *testing.T, task string, mode domain.LaunchMode) *domain.Run {
	t.Helper()
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, task, "omp", mode)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	e.sup.run = run.ID
	return run
}

// restart closes the scheduler and recovers the run in a new one, which
// settles any switch the first left unfinished.
func (e *switchEnv) restart(t *testing.T) *Scheduler {
	t.Helper()
	if err := e.sched.Close(); err != nil {
		t.Fatal(err)
	}
	cfg := e.cfg
	cfg.PTY = newFakePTY()
	cfg.PTY.(*fakePTY).logDir = e.pty.logDir
	s2, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	startScheduler(t, s2)
	waitFor(t, "the recovered run", func() bool {
		s2.mu.Lock()
		defer s2.mu.Unlock()
		return s2.runs[e.sup.run] != nil && s2.runs[e.sup.run].switchIntent == nil
	})
	return s2
}

func modeEvents(t *testing.T, sub events.Subscription, run domain.RunID, n int) []events.RunModePayload {
	t.Helper()
	var got []events.RunModePayload
	deadline := time.After(waitTimeout)
	for len(got) < n {
		select {
		case ev, ok := <-sub.Events():
			if !ok {
				t.Fatal("event stream closed while waiting for run.mode")
			}
			if p, isMode := ev.Payload.(events.RunModePayload); isMode && ev.RunID == run {
				got = append(got, p)
			}
		case <-deadline:
			t.Fatalf("timed out waiting for run.mode; got %+v", got)
		}
	}
	return got
}

func (f *fakeCoordinator) fileBody(run domain.RunID, name string) string {
	b, _ := os.ReadFile(filepath.Join(f.root, string(run), name))
	return string(b)
}

func noticeTitled(title string) func([]acphost.Item) bool {
	return func(items []acphost.Item) bool {
		return slices.ContainsFunc(items, func(it acphost.Item) bool {
			return it.Kind == acphost.KindNotice && it.Notice.Title == title
		})
	}
}

func TestSwitchEnhancedRunToStandardAndBack(t *testing.T) {
	t.Parallel()
	e := newSwitchEnv(t)
	run := e.launch(t, "say pong", domain.LaunchACP)
	waitItems(t, e.sched, run.ID, "the task's turn", assistantSaid("pong"))
	session := e.rt.fixture.SessionID()
	sub := e.subscribe(t)

	if err := e.sched.SwitchMode(t.Context(), run.ID, e.member.ID, domain.LaunchTUI, admitNow); err != nil {
		t.Fatalf("switch to Standard: %v", err)
	}
	if got := modeEvents(t, sub, run.ID, 2); got[0] != (events.RunModePayload{Mode: domain.LaunchTUI, Previous: domain.LaunchACP, Switching: domain.LaunchTUI, Reason: "Switching to Standard…"}) ||
		got[1] != (events.RunModePayload{Mode: domain.LaunchTUI, Previous: domain.LaunchACP}) {
		t.Fatalf("run.mode events %+v", got)
	}
	if !e.rt.all()[0].exited() || e.sched.acp.session(run.ID) != nil {
		t.Fatal("the adapter still runs beside the terminal")
	}
	bodies := e.sup.ran()
	if len(bodies) != 1 || !strings.Contains(bodies[0], "unset "+coordtransport.EnhancedEnv+"\n") ||
		!strings.Contains(bodies[0], "exec 'omp' '--auto-approve' '--resume="+session+"' '-e' '/run/aether/status.ts'") {
		t.Fatalf("supervisor ran %q", bodies)
	}
	if got := e.coord.fileBody(run.ID, "status.ts"); got == "" {
		t.Fatal("the terminal's status extension was not written to the run directory")
	}
	row, err := e.db.GetRun(t.Context(), run.ID)
	if err != nil || row.Mode != domain.LaunchTUI || row.ACP {
		t.Fatalf("row mode %q acp %v (%v)", row.Mode, row.ACP, err)
	}
	if sc, err := e.sched.readSidecar(run.ID); err != nil || sc.Mode != domain.LaunchTUI || sc.AgentExec != nil {
		t.Fatalf("sidecar %+v (%v)", sc, err)
	}
	waitItems(t, e.sched, run.ID, "the switch notice", noticeTitled("Switched to Standard"))
	if _, err := e.sched.Inject(t.Context(), run.ID, e.member.ID, "typed into the terminal", false, nil); err != nil {
		t.Fatal(err)
	}
	if injects := e.pty.injected(); len(injects) != 1 || injects[0].message != "typed into the terminal" {
		t.Fatalf("PTY injects %+v", injects)
	}

	if err := e.sched.ReportAgentState(t.Context(), run.ID, agentstatus.Report{State: agentstatus.Working, SessionID: "tui-session"}); err != nil {
		t.Fatal(err)
	}
	if row, _ := e.db.GetRun(t.Context(), run.ID); row.HarnessSessionID != "tui-session" {
		t.Fatalf("harness_session_id %q, want the terminal's", row.HarnessSessionID)
	}

	var idleOffered atomic.Bool
	e.coord.mu.Lock()
	e.coord.onWake = func(r domain.RunID) {
		if r == run.ID && e.sched.IdleEnhanced(r) {
			idleOffered.Store(true)
		}
	}
	e.coord.mu.Unlock()
	if err := e.sched.SwitchMode(t.Context(), run.ID, e.member.ID, domain.LaunchACP, admitNow); err != nil {
		t.Fatalf("switch to Enhanced: %v", err)
	}
	if !idleOffered.Load() {
		t.Fatal("the switch did not offer the idle session the mail held while it ran")
	}
	if got := modeEvents(t, sub, run.ID, 2); got[1] != (events.RunModePayload{Mode: domain.LaunchACP, Previous: domain.LaunchTUI}) {
		t.Fatalf("run.mode events %+v", got)
	}
	if bodies := e.sup.ran(); len(bodies) != 2 || bodies[1] != "" {
		t.Fatalf("supervisor ran %q, want a login shell last", bodies)
	}
	execs := e.rt.all()
	if len(execs) != 2 || !slices.Contains(execs[1].agent.Methods(), acp.AgentMethodSessionResume) {
		t.Fatalf("the new adapter did not resume the session: %+v", execs)
	}
	if spec := e.rt.specs[1]; !slices.Contains(spec.Env, coordtransport.EnhancedEnv+"=1") {
		t.Fatalf("adapter env %v", spec.Env)
	}
	if row, _ := e.db.GetRun(t.Context(), run.ID); row.Mode != domain.LaunchACP || !row.ACP {
		t.Fatalf("row mode %q acp %v", row.Mode, row.ACP)
	}
	if _, err := e.sched.Inject(t.Context(), run.ID, e.member.ID, "say pong", false, nil); err != nil {
		t.Fatal(err)
	}
	waitItems(t, e.sched, run.ID, "a turn after switching back", turnEnded("end_turn", 2))
}

func TestSwitchToStandardRestoresEnhancedWhenTheTerminalExits(t *testing.T) {
	t.Parallel()
	e := newSwitchEnv(t)
	run := e.launch(t, "", domain.LaunchACP)
	waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })
	e.sup.exitStatus = "1"
	sub := e.subscribe(t)

	err := e.sched.SwitchMode(t.Context(), run.ID, e.member.ID, domain.LaunchTUI, admitNow)
	if err == nil || !strings.Contains(err.Error(), "the agent's terminal exited with code 1") {
		t.Fatalf("switch error %v", err)
	}
	got := modeEvents(t, sub, run.ID, 2)
	if got[1].Mode != domain.LaunchACP || got[1].Reason != err.Error() {
		t.Fatalf("final run.mode %+v", got[1])
	}
	if bodies := e.sup.ran(); len(bodies) != 2 || bodies[1] != "" {
		t.Fatalf("supervisor ran %q, want the login shell back", bodies)
	}
	if len(e.rt.all()) != 2 || e.sched.acp.session(run.ID) == nil {
		t.Fatal("the enhanced session was not restored")
	}
	if row, _ := e.db.GetRun(t.Context(), run.ID); row.Mode != domain.LaunchACP || !row.ACP {
		t.Fatalf("row mode %q acp %v", row.Mode, row.ACP)
	}
}

func TestSwitchToStandardKeepsEnhancedWhenTheAdapterDoesNotStop(t *testing.T) {
	t.Parallel()
	e := newSwitchEnv(t)
	run := e.launch(t, "", domain.LaunchACP)
	waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })
	e.rt.all()[0].stopErr = errors.New("docker: exec control failed")

	err := e.sched.SwitchMode(t.Context(), run.ID, e.member.ID, domain.LaunchTUI, admitNow)
	if err == nil || !strings.Contains(err.Error(), "docker: exec control failed") {
		t.Fatalf("switch error %v", err)
	}
	if bodies := e.sup.ran(); len(bodies) != 0 {
		t.Fatalf("supervisor ran %q while the adapter may still run", bodies)
	}
	if len(e.rt.all()) != 2 || e.sched.acp.session(run.ID) == nil {
		t.Fatal("the enhanced session was not restored")
	}
	if row, _ := e.db.GetRun(t.Context(), run.ID); row.Mode != domain.LaunchACP || !row.ACP {
		t.Fatalf("row mode %q acp %v", row.Mode, row.ACP)
	}
}

func TestRestartSettlesAnInterruptedSwitch(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, state string
		want        domain.LaunchMode
	}{
		{"swapped", "n1 started\n", domain.LaunchTUI},
		{"not swapped", "n0 exited 0\n", domain.LaunchACP},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newSwitchEnv(t)
			run := e.launch(t, "", domain.LaunchACP)
			waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })
			e.sched.mu.Lock()
			entry := e.sched.runs[run.ID]
			e.sched.mu.Unlock()
			if err := (modeSwitch{s: e.sched, entry: entry}).recordIntent(&switchIntent{Mode: domain.LaunchTUI, Nonce: "n1", Reporter: harness.ReporterFull}); err != nil {
				t.Fatal(err)
			}
			e.sup.state = tc.state
			before := len(e.rt.all())
			s2 := e.restart(t)
			if row, _ := e.db.GetRun(t.Context(), run.ID); row.Mode != tc.want {
				t.Fatalf("row mode %q, want %q", row.Mode, tc.want)
			}
			if sc, err := s2.readSidecar(run.ID); err != nil || sc.Mode != tc.want || sc.Switch != nil {
				t.Fatalf("sidecar %+v (%v)", sc, err)
			}
			if tc.want == domain.LaunchACP {
				waitFor(t, "resumed session", func() bool { return s2.acp.session(run.ID) != nil })
				return
			}
			if len(e.rt.all()) != before || s2.acp.session(run.ID) != nil {
				t.Fatal("recovery started an ACP server beside the resumed terminal")
			}
		})
	}
}

func TestSwitchToEnhancedRestoresTheTerminalWhenTheAdapterFails(t *testing.T) {
	t.Parallel()
	e := newSwitchEnv(t)
	run := e.launch(t, "fix the bug", domain.LaunchTUI)
	if err := e.sched.ReportAgentState(t.Context(), run.ID, agentstatus.Report{State: agentstatus.Working, SessionID: "tui-session"}); err != nil {
		t.Fatal(err)
	}
	e.rt.startErr = errors.New("exec: no such file")

	err := e.sched.SwitchMode(t.Context(), run.ID, e.member.ID, domain.LaunchACP, admitNow)
	if err == nil || !strings.Contains(err.Error(), "exec: no such file") {
		t.Fatalf("switch error %v", err)
	}
	bodies := e.sup.ran()
	if len(bodies) != 2 || bodies[0] != "" || !strings.Contains(bodies[1], "'--resume=tui-session'") {
		t.Fatalf("supervisor ran %q, want the shell, then the terminal back", bodies)
	}
	if row, _ := e.db.GetRun(t.Context(), run.ID); row.Mode != domain.LaunchTUI || row.ACP {
		t.Fatalf("row mode %q acp %v", row.Mode, row.ACP)
	}
	items := waitItems(t, e.sched, run.ID, "the failure notice", noticeTitled("Switch to Enhanced failed"))
	if !strings.Contains(items[len(items)-1].Notice.Description, "exec: no such file") {
		t.Fatalf("notice %+v", items[len(items)-1])
	}
}

func TestFailedRollbackKeepsTheSwitchForRecovery(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		from, to domain.LaunchMode
		failure  string
	}{
		{domain.LaunchTUI, domain.LaunchACP, "restore the agent's terminal"},
		{domain.LaunchACP, domain.LaunchTUI, "start the login shell"},
	} {
		t.Run(modeName(tc.to), func(t *testing.T) {
			t.Parallel()
			e := newSwitchEnv(t)
			run := e.launch(t, "", tc.from)
			if tc.from == domain.LaunchTUI {
				if err := e.sched.ReportAgentState(t.Context(), run.ID, agentstatus.Report{State: agentstatus.Working, SessionID: "tui-session"}); err != nil {
					t.Fatal(err)
				}
			} else {
				waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })
			}
			e.sup.faults = []string{"lost", "down"}

			err := e.sched.SwitchMode(t.Context(), run.ID, e.member.ID, tc.to, admitNow)
			if err == nil || !strings.Contains(err.Error(), tc.failure) {
				t.Fatalf("switch error %v", err)
			}
			if sc, err := e.sched.readSidecar(run.ID); err != nil || sc.Switch == nil || sc.Switch.Mode != tc.to {
				t.Fatalf("sidecar %+v (%v), want the switch kept", sc, err)
			}

			s2 := e.restart(t)
			if row, _ := e.db.GetRun(t.Context(), run.ID); row.Mode != tc.to {
				t.Fatalf("row mode %q, want %q, the mode the container reached", row.Mode, tc.to)
			}
			if tc.to == domain.LaunchACP {
				waitFor(t, "resumed session", func() bool { return s2.acp.session(run.ID) != nil })
			} else if s2.acp.session(run.ID) != nil {
				t.Fatal("recovery started an ACP server beside the resumed terminal")
			}
		})
	}
}

type failingRunModeStore struct {
	store.Store
	failed atomic.Bool
}

func (s *failingRunModeStore) SetRunMode(ctx context.Context, id domain.RunID, mode domain.LaunchMode, acp bool) error {
	if s.failed.CompareAndSwap(false, true) {
		return errors.New("database is locked")
	}
	return s.Store.SetRunMode(ctx, id, mode, acp)
}

func TestRestartRecordsAModeTheRunRowMissed(t *testing.T) {
	t.Parallel()
	e := newSwitchEnv(t, func(cfg *Config) { cfg.Store = &failingRunModeStore{Store: cfg.Store} })
	run := e.launch(t, "", domain.LaunchACP)
	waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })

	err := e.sched.SwitchMode(t.Context(), run.ID, e.member.ID, domain.LaunchTUI, admitNow)
	if err == nil || !strings.Contains(err.Error(), "record the run's mode: database is locked") {
		t.Fatalf("switch error %v", err)
	}
	if sc, err := e.sched.readSidecar(run.ID); err != nil || sc.Mode != domain.LaunchTUI || sc.Switch == nil {
		t.Fatalf("sidecar %+v (%v), want Standard with the switch kept", sc, err)
	}

	s2 := e.restart(t)
	if row, _ := e.db.GetRun(t.Context(), run.ID); row.Mode != domain.LaunchTUI || row.ACP {
		t.Fatalf("row mode %q acp %v, want the switch recorded on restart", row.Mode, row.ACP)
	}
	if s2.acp.session(run.ID) != nil {
		t.Fatal("recovery started an ACP server beside the resumed terminal")
	}
}

func TestSwitchRefusals(t *testing.T) {
	t.Parallel()
	e := newSwitchEnv(t)
	run := e.launch(t, "fix the bug", domain.LaunchTUI)

	if err := e.sched.SwitchMode(t.Context(), run.ID, e.member.ID, domain.LaunchACP, admitNow); !errors.Is(err, ErrSessionNotReported) ||
		!errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("switch before a session id: %v", err)
	}
	if err := e.sched.ReportAgentState(t.Context(), run.ID, agentstatus.Report{State: agentstatus.Working, SessionID: "tui-session"}); err != nil {
		t.Fatal(err)
	}
	if err := e.sched.SwitchMode(t.Context(), run.ID, e.member.ID, domain.LaunchACP, func(func() error) error {
		return control.ErrStale
	}); !errors.Is(err, control.ErrStale) {
		t.Fatalf("switch refused by the lease: %v", err)
	}
	if err := e.sched.SwitchMode(t.Context(), run.ID, e.member.ID, domain.LaunchTUI, admitNow); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("switch to the current mode: %v", err)
	}
	if err := e.sched.Pause(t.Context(), run.ID, e.member.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.sched.SwitchMode(t.Context(), run.ID, e.member.ID, domain.LaunchACP, admitNow); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("switch while paused: %v", err)
	}
	if len(e.sup.ran()) != 0 || len(e.rt.all()) != 0 || e.sched.Switching(run.ID) != "" {
		t.Fatal("a refused switch touched the run")
	}

	fake, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "t", "fake", domain.LaunchACP)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.sched.SwitchMode(t.Context(), fake.ID, e.member.ID, domain.LaunchTUI, admitNow); !errors.Is(err, ErrNotSwitchable) {
		t.Fatalf("switch of an agent without a verified switch: %v", err)
	}
}

func TestSwitchToEnhancedNeedsTheAdapter(t *testing.T) {
	t.Parallel()
	e := newSwitchEnv(t)
	run := e.launch(t, "fix the bug", domain.LaunchTUI)
	if err := e.sched.ReportAgentState(t.Context(), run.ID, agentstatus.Report{State: agentstatus.Working, SessionID: "tui-session"}); err != nil {
		t.Fatal(err)
	}
	home, err := e.cfg.Homes.Path(e.member.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(home, ".local", "bin", "omp")); err != nil {
		t.Fatal(err)
	}
	if err := e.sched.SwitchMode(t.Context(), run.ID, e.member.ID, domain.LaunchACP, admitNow); !errors.Is(err, ErrAdapterNotInstalled) {
		t.Fatalf("switch without the adapter: %v", err)
	}
	if len(e.sup.ran()) != 0 || e.sched.Switching(run.ID) != "" {
		t.Fatal("a refused switch touched the run")
	}
}

func TestServerDefinitionMakesAnAgentUnswitchable(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(cfg *Config) {
		cfg.Harnesses["claude"] = HarnessSpec{TUIArgs: []string{"claude"}, HeadlessArgs: []string{"claude", "-p", "{task}"}}
	})
	for name, want := range map[string]bool{"claude": false, "omp": true} {
		got, err := e.sched.AgentSwitchable(t.Context(), e.member.ID, e.member.ID, name)
		if err != nil || got != want {
			t.Fatalf("AgentSwitchable(%s) = %v, %v, want %v", name, got, err, want)
		}
	}
}

func TestSwitchingRunRefusesInput(t *testing.T) {
	t.Parallel()
	e := newSwitchEnv(t)
	run := e.launch(t, "", domain.LaunchACP)
	waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })
	hold := make(chan struct{})
	e.sup.hold = hold
	done := make(chan error, 1)
	go func() { done <- e.sched.SwitchMode(t.Context(), run.ID, e.member.ID, domain.LaunchTUI, admitNow) }()
	waitFor(t, "the switch", func() bool { return e.sched.Switching(run.ID) == domain.LaunchTUI })

	if _, err := e.sched.Inject(t.Context(), run.ID, e.member.ID, "hello", false, nil); !errors.Is(err, ErrSwitching) {
		t.Fatalf("Inject during the switch: %v", err)
	}
	close(hold)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if e.sched.Switching(run.ID) != "" {
		t.Fatal("the run is still switching")
	}
}

func TestSwitchingRunIsNotWoken(t *testing.T) {
	t.Parallel()
	e := newSwitchEnv(t)
	run := e.launch(t, "", domain.LaunchACP)
	waitFor(t, "the idle session", func() bool { return e.sched.IdleEnhanced(run.ID) })
	setSwitching := func(mode domain.LaunchMode) {
		e.sched.mu.Lock()
		e.sched.runs[run.ID].switching = mode
		e.sched.mu.Unlock()
	}

	setSwitching(domain.LaunchTUI)
	if e.sched.IdleEnhanced(run.ID) {
		t.Fatal("a switching run is idle")
	}
	if err := e.sched.WakeEnhanced(t.Context(), run.ID, "inbox hint"); !errors.Is(err, errACPBusy) {
		t.Fatalf("wake during the switch: %v, want errACPBusy", err)
	}
	setSwitching("")
	if !e.sched.IdleEnhanced(run.ID) {
		t.Fatal("the run is not idle after the switch")
	}
}

func TestReportedSessionIsRecordedForStandardRunsOnly(t *testing.T) {
	t.Parallel()
	e := newSwitchEnv(t)
	tui := e.launch(t, "t", domain.LaunchTUI)
	if err := e.sched.ReportAgentState(t.Context(), tui.ID, agentstatus.Report{State: agentstatus.Idle, SessionID: "s1"}); err != nil {
		t.Fatal(err)
	}
	if sc, _ := e.sched.readSidecar(tui.ID); sc.AgentSessionID != "s1" {
		t.Fatalf("sidecar session %q", sc.AgentSessionID)
	}
	enhanced := e.launch(t, "", domain.LaunchACP)
	waitFor(t, "session", func() bool { return e.sched.acp.session(enhanced.ID) != nil })
	if err := e.sched.ReportAgentState(t.Context(), enhanced.ID, agentstatus.Report{State: agentstatus.Working, SessionID: "hook-session"}); err != nil {
		t.Fatal(err)
	}
	if row, _ := e.db.GetRun(t.Context(), enhanced.ID); row.HarnessSessionID != e.rt.fixture.SessionID() {
		t.Fatalf("a hook replaced the enhanced session: %q", row.HarnessSessionID)
	}
}

// heldSessionStore holds the write of session held until release closes.
type heldSessionStore struct {
	store.Store
	held    string
	entered chan struct{}
	release chan struct{}
}

func (s *heldSessionStore) SetRunAgentSession(ctx context.Context, id domain.RunID, session string) error {
	if session == s.held {
		close(s.entered)
		<-s.release
	}
	return s.Store.SetRunAgentSession(ctx, id, session)
}

func TestReportedSessionsReachTheRowInReportOrder(t *testing.T) {
	t.Parallel()
	held := &heldSessionStore{held: "s1", entered: make(chan struct{}), release: make(chan struct{})}
	e := newSwitchEnv(t, func(cfg *Config) { held.Store = cfg.Store; cfg.Store = held })
	run := e.launch(t, "t", domain.LaunchTUI)
	report := func(session string) chan error {
		done := make(chan error, 1)
		go func() {
			done <- e.sched.ReportAgentState(t.Context(), run.ID, agentstatus.Report{State: agentstatus.Working, SessionID: session})
		}()
		return done
	}

	first := report("s1")
	<-held.entered
	second := report("s2")
	waitFor(t, "the second report", func() bool {
		e.sched.mu.Lock()
		defer e.sched.mu.Unlock()
		return e.sched.runs[run.ID].agentSessionID == "s2"
	})
	close(held.release)
	for _, done := range []chan error{first, second} {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if row, _ := e.db.GetRun(t.Context(), run.ID); row.HarnessSessionID != "s2" {
		t.Fatalf("harness_session_id %q, want the later report's s2", row.HarnessSessionID)
	}
}
