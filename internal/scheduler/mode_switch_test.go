package scheduler

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/agentstatus"
	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/runtime"
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
	hold   chan struct{}
	bodies []string
}

func (f *fakeSupervisor) exec(_ runtime.ID, argv []string) (int, string, error) {
	if len(argv) < 6 || argv[3] != "aether-swap" {
		return 0, "", nil
	}
	f.mu.Lock()
	hold := f.hold
	f.mu.Unlock()
	if hold != nil {
		<-hold
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
	if body != "" && f.exitStatus != "" {
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

func newSwitchEnv(t *testing.T) *switchEnv {
	t.Helper()
	e, rt := newACPEnv(t, withServerBinary(fakeServerBinary(t, "#!/bin/sh\necho aether\n")))
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
	if got := modeEvents(t, sub, run.ID, 2); got[0] != (events.RunModePayload{Mode: domain.LaunchTUI, Previous: domain.LaunchACP, Switching: true, Reason: "Switching to Standard…"}) ||
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
	if _, err := e.sched.Inject(t.Context(), run.ID, e.member.ID, "typed into the terminal", false); err != nil {
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

	if err := e.sched.SwitchMode(t.Context(), run.ID, e.member.ID, domain.LaunchACP, admitNow); err != nil {
		t.Fatalf("switch to Enhanced: %v", err)
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
	if _, err := e.sched.Inject(t.Context(), run.ID, e.member.ID, "say pong", false); err != nil {
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

func TestSwitchRefusals(t *testing.T) {
	t.Parallel()
	e := newSwitchEnv(t)
	run := e.launch(t, "fix the bug", domain.LaunchTUI)

	if err := e.sched.SwitchMode(t.Context(), run.ID, e.member.ID, domain.LaunchACP, admitNow); !errors.Is(err, ErrInvalidTransition) ||
		!strings.Contains(err.Error(), "has not reported its session") {
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

	if _, err := e.sched.Inject(t.Context(), run.ID, e.member.ID, "hello", false); !errors.Is(err, ErrSwitching) {
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
