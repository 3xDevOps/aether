package scheduler

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/acphost/acpmock"
	"github.com/3xDevOps/Aether/internal/agentstatus"
	"github.com/3xDevOps/Aether/internal/coordhooks"
	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// installed puts the agent and its ACP server in the member's home, where a
// background launch looks for them.
func newBackgroundEnv(t *testing.T, agent string, installed bool) (*testEnv, *acpRuntime) {
	t.Helper()
	e, rt := newACPEnv(t, func(cfg *Config) {
		cfg.Harnesses[agent] = HarnessSpec{
			TUIArgs:      []string{"fake-agent", "{task}"},
			HeadlessArgs: []string{"fake-agent", "-p", "{task}"},
			ACPArgs:      []string{"acp-mock"},
		}
	})
	if installed {
		home, err := e.cfg.Homes.Path(e.member.ID)
		if err != nil {
			t.Fatal(err)
		}
		bin := filepath.Join(home, ".local", "bin")
		if err := os.MkdirAll(bin, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, exe := range []string{"fake-agent", "acp-mock"} {
			if err := os.WriteFile(filepath.Join(bin, exe), []byte("#!/bin/sh\n"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	// The supervisor exits 0 on SIGUSR1 and 1 on SIGUSR2.
	e.rt.execHandler = func(id runtime.ID, argv []string) (int, string, error) {
		c, err := e.rt.get(id)
		if err != nil {
			return 1, "", err
		}
		switch strings.Join(argv, " ") {
		case "/bin/sh -c kill -USR1 1":
			c.exitNow(0)
		case "/bin/sh -c kill -USR2 1":
			c.exitNow(1)
		}
		return 0, "", nil
	}
	return e, rt
}

// enhancedAgent is a shipped agent whose profile sets ACPDefault.
const enhancedAgent = "opencode"

func (e *testEnv) launchBackground(t *testing.T, agent, task string) *domain.Run {
	t.Helper()
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, task, agent, domain.LaunchHeadless)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	return run
}

func TestBackgroundRunOverACPFinishesOnEndTurn(t *testing.T) {
	t.Parallel()
	e, rt := newBackgroundEnv(t, enhancedAgent, true)
	run := e.launchBackground(t, enhancedAgent, "say pong")
	if !run.ACP {
		t.Fatal("a background run of an agent with an installed ACP server is not driven over ACP")
	}
	c := e.rt.byName(string(run.ID))
	cmd := strings.Join(c.spec.Command, " ")
	if !strings.Contains(cmd, "aether-run-supervisor") || strings.Contains(cmd, "fake-agent") {
		t.Fatalf("container command %q, want the supervisor's login shell only", cmd)
	}
	if c.spec.Env["NO_BROWSER"] != "1" || c.spec.Env[coordtransport.EnhancedEnv] != "" {
		t.Fatalf("env NO_BROWSER=%q %s=%q, want 1 and unset", c.spec.Env["NO_BROWSER"], coordtransport.EnhancedEnv, c.spec.Env[coordtransport.EnhancedEnv])
	}
	if execs := rt.all(); len(execs) != 1 || execs[0].argv[0] != "acp-mock" {
		t.Fatalf("execs %+v, want the ACP server", execs)
	}

	got := e.waitStoreStatus(t, run.ID, domain.RunCompleted)
	if got.Reason != exitedCompletedReason || !got.ACP {
		t.Fatalf("finished run %+v, want completed with %q", got, exitedCompletedReason)
	}
	page, err := e.sched.ACPHistory(run.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	items := page.Items
	if !assistantSaid("pong")(items) || !turnEnded("end_turn", 1)(items) {
		t.Fatalf("the item log does not hold the one turn: %+v", items)
	}
	if !slices.ContainsFunc(e.rt.execRuns(), func(call fakeExecCall) bool { return slices.Contains(call.argv, "kill -USR1 1") }) {
		t.Fatalf("the container was not ended with SIGUSR1: %+v", e.rt.execRuns())
	}
}

func TestBackgroundRunOverACPAllowsPermissions(t *testing.T) {
	t.Parallel()
	e, _ := newBackgroundEnv(t, enhancedAgent, true)
	run := e.launchBackground(t, enhancedAgent, acpmock.PromptAskPermission)
	e.waitStoreStatus(t, run.ID, domain.RunCompleted)
	page, err := e.sched.ACPHistory(run.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	items := page.Items
	if !assistantSaid("permission: allow")(items) {
		t.Fatalf("the permission request was not allowed: %+v", items)
	}
	if !slices.ContainsFunc(items, func(it acphost.Item) bool {
		return it.Kind == acphost.KindRequest && it.Request.Status == acphost.RequestAnswered && it.Request.Answer == "allow"
	}) {
		t.Fatalf("the item log does not show the answered request: %+v", items)
	}
}

func TestBackgroundRunOverACPFailsOnAnotherStop(t *testing.T) {
	t.Parallel()
	e, _ := newBackgroundEnv(t, enhancedAgent, true)
	run := e.launchBackground(t, enhancedAgent, acpmock.PromptWait)
	waitFor(t, "the turn", func() bool {
		return e.sched.acp.session(run.ID) != nil && e.sched.acp.session(run.ID).State().TurnInFlight
	})
	if err := e.sched.ACPCancel(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	got := e.waitStoreStatus(t, run.ID, domain.RunFailed)
	if want := exitedFailedReasonPrefix + "1: the agent's turn ended: cancelled"; got.Reason != want {
		t.Fatalf("reason %q, want %q", got.Reason, want)
	}
}

func TestBackgroundRunOverACPFailsWhenTheModelIsRefused(t *testing.T) {
	t.Parallel()
	e, rt := newBackgroundEnv(t, enhancedAgent, true)
	// No configOptions, so the model goes through session/set_model, which
	// the mock agent does not implement.
	rt.fixture.SessionNew = json.RawMessage(`{"sessionId":"legacy"}`)
	run, err := e.sched.LaunchWithOptions(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "say pong", enhancedAgent, domain.LaunchHeadless, domain.LaunchOptions{Model: "anthropic/claude-sonnet-4-5"})
	if err != nil {
		t.Fatal(err)
	}
	got := e.waitStoreStatus(t, run.ID, domain.RunFailed)
	if want := exitedFailedReasonPrefix + "1: model not set: "; !strings.HasPrefix(got.Reason, want) {
		t.Fatalf("reason %q, want prefix %q", got.Reason, want)
	}
	if slices.Contains(rt.all()[0].agent.Methods(), acp.AgentMethodSessionPrompt) {
		t.Fatal("the task ran on the agent's default model")
	}
}

// A swarm worker is otherwise left running for CompleteMission; one whose
// agent never got its task ends and frees its slot.
func TestBackgroundWorkerOverACPEndsWhenItsTaskNeverStarted(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		task, model string
		legacy      bool
	}{
		"model refused":  {task: "say pong", model: "anthropic/claude-sonnet-4-5", legacy: true},
		"prompt refused": {task: acpmock.PromptRefuse},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e, rt := newBackgroundEnv(t, enhancedAgent, true)
			if tc.legacy {
				rt.fixture.SessionNew = json.RawMessage(`{"sessionId":"legacy"}`)
			}
			run, err := e.sched.LaunchMission(t.Context(), MissionLaunchSpec{
				WorkspaceID: e.ws.ID, RunID: "unstarted-worker", Actor: e.member.ID,
				RunOwner: e.member.ID, AccountOwner: e.member.ID, Task: tc.task,
				Harness: enhancedAgent, Mode: domain.LaunchHeadless, Model: tc.model,
			})
			if err != nil {
				t.Fatal(err)
			}
			e.waitStoreStatus(t, run.ID, domain.RunFailed)
			waitFor(t, "the worker observed as retained", func() bool {
				observed, err := e.sched.ObserveMissionRun(t.Context(), run.ID)
				return err == nil && observed.State == MissionRunRetained
			})
			if tc.legacy && slices.Contains(rt.all()[0].agent.Methods(), acp.AgentMethodSessionPrompt) {
				t.Fatal("the task ran on the agent's default model")
			}
		})
	}
}

// The wait prompt sends no update, so the cancel ends the turn before
// Prompt's grace runs out: the session's acceptance must still count.
func TestBackgroundWorkerOverACPParksWhenItsStartedTurnIsCancelled(t *testing.T) {
	t.Parallel()
	e, _ := newBackgroundEnv(t, enhancedAgent, true)
	run, err := e.sched.LaunchMission(t.Context(), MissionLaunchSpec{
		WorkspaceID: e.ws.ID, RunID: "cancelled-worker", Actor: e.member.ID,
		RunOwner: e.member.ID, AccountOwner: e.member.ID, Task: acpmock.PromptWait,
		Harness: enhancedAgent, Mode: domain.LaunchHeadless,
	})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the turn", func() bool {
		return e.sched.acp.session(run.ID) != nil && e.sched.acp.session(run.ID).State().TurnInFlight
	})
	if err = e.sched.ACPCancel(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
	observed, err := e.sched.ObserveMissionRun(t.Context(), run.ID)
	if err != nil || observed.State != MissionRunActive {
		t.Fatalf("observed %+v, %v; want the attempt still active", observed, err)
	}
}

// A refused continuation is not an unstarted task: the worker keeps the
// lifecycle of any failed turn and is left to CompleteMission.
func TestBackgroundWorkerOverACPParksWhenItsResumeIsRefused(t *testing.T) {
	t.Parallel()
	e, rt := newBackgroundEnv(t, enhancedAgent, true)
	run, err := e.sched.LaunchMission(t.Context(), MissionLaunchSpec{
		WorkspaceID: e.ws.ID, RunID: "resumed-worker", Actor: e.member.ID,
		RunOwner: e.member.ID, AccountOwner: e.member.ID, Task: acpmock.PromptWait,
		Harness: enhancedAgent, Mode: domain.LaunchHeadless,
	})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the turn", func() bool {
		return e.sched.acp.session(run.ID) != nil && e.sched.acp.session(run.ID).State().TurnInFlight
	})
	if err = e.sched.Close(); err != nil {
		t.Fatal(err)
	}
	rt.fixture.RefusePrompts = true

	cfg := e.cfg
	cfg.PTY = newFakePTY()
	cfg.PTY.(*fakePTY).logDir = e.pty.logDir
	s2, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	startScheduler(t, s2)
	e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
	observed, err := s2.ObserveMissionRun(t.Context(), run.ID)
	if err != nil || observed.State != MissionRunActive {
		t.Fatalf("observed %+v, %v; want the attempt still active", observed, err)
	}
}

func TestBackgroundRunOverACPContinuesAfterRestart(t *testing.T) {
	t.Parallel()
	e, _ := newBackgroundEnv(t, enhancedAgent, true)
	run := e.launchBackground(t, enhancedAgent, acpmock.PromptWait)
	waitFor(t, "the turn", func() bool {
		return e.sched.acp.session(run.ID) != nil && e.sched.acp.session(run.ID).State().TurnInFlight
	})
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
	e.waitStoreStatus(t, run.ID, domain.RunCompleted)
	page, err := s2.ACPHistory(run.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	items := page.Items
	if !slices.ContainsFunc(items, func(it acphost.Item) bool {
		return it.Kind == acphost.KindMessage && it.Message.Role == "user" && it.Message.Text == oneShotResume
	}) {
		t.Fatalf("the restored session was not asked to continue: %+v", items)
	}
}

func TestBackgroundRunOverACPResendsTaskToANewSession(t *testing.T) {
	t.Parallel()
	e, rt := newBackgroundEnv(t, enhancedAgent, true)
	run := e.launchBackground(t, enhancedAgent, acpmock.PromptWait)
	waitFor(t, "the turn", func() bool {
		return e.sched.acp.session(run.ID) != nil && e.sched.acp.session(run.ID).State().TurnInFlight
	})
	if err := e.sched.Close(); err != nil {
		t.Fatal(err)
	}
	rt.fixture.Initialize = json.RawMessage(`{"protocolVersion":1,"agentCapabilities":{}}`)

	cfg := e.cfg
	cfg.PTY = newFakePTY()
	cfg.PTY.(*fakePTY).logDir = e.pty.logDir
	s2, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	startScheduler(t, s2)
	waitFor(t, "the task sent to the new session", func() bool {
		page, err := s2.ACPHistory(run.ID, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		var prompts []string
		reset := false
		for _, it := range page.Items {
			switch {
			case it.Kind == acphost.KindReset:
				reset = true
			case it.Kind == acphost.KindMessage && it.Message.Role == "user":
				prompts = append(prompts, it.Message.Text)
			}
		}
		return reset && slices.Equal(prompts, []string{withInstructions(acpmock.PromptWait, s2.runInstructions(coordhooks.Guide{RunID: string(run.ID), Role: coordhooks.RoleInteractive, Wake: true})), withInstructions(acpmock.PromptWait, s2.runInstructions(coordhooks.Guide{RunID: string(run.ID), Role: coordhooks.RoleInteractive, Wake: true}))})
	})
	if err := s2.ACPCancel(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	e.waitStoreStatus(t, run.ID, domain.RunFailed)
}

func TestBackgroundRunKeepsCommandLine(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		agent     string
		installed bool
	}{
		{"no ACP server installed", enhancedAgent, false},
		{"agent defaults to its terminal", "claude", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e, rt := newBackgroundEnv(t, tc.agent, tc.installed)
			run := e.launchBackground(t, tc.agent, "say pong")
			if run.ACP {
				t.Fatal("the background run is driven over ACP")
			}
			if cmd := e.rt.byName(string(run.ID)).spec.Command; !slices.Equal(cmd, []string{"fake-agent", "-p", withInstructions("say pong", e.sched.runInstructions(coordhooks.Guide{Role: coordhooks.RoleBackground, Background: true}))}) {
				t.Fatalf("container command %q, want the headless command line", cmd)
			}
			if execs := rt.all(); len(execs) != 0 {
				t.Fatalf("started an ACP server: %+v", execs)
			}
		})
	}
}

func (e *testEnv) runStatusEvents(sub events.Subscription, run domain.RunID) []events.RunStatusPayload {
	var got []events.RunStatusPayload
	for {
		select {
		case ev := <-sub.Events():
			if p, ok := ev.Payload.(events.RunStatusPayload); ok && ev.RunID == run {
				got = append(got, p)
			}
		default:
			return got
		}
	}
}

// statusTrail collects the run's status events until done accepts them.
func (e *testEnv) statusTrail(t *testing.T, sub events.Subscription, run domain.RunID, done func([]events.RunStatusPayload) bool) []events.RunStatusPayload {
	t.Helper()
	var trail []events.RunStatusPayload
	waitFor(t, "the status events", func() bool {
		trail = append(trail, e.runStatusEvents(sub, run)...)
		return done(trail)
	})
	return trail
}

func (e *testEnv) launchClaudeACP(t *testing.T, task string) *domain.Run {
	t.Helper()
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, task, "claude", domain.LaunchACP)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	return run
}

func parkedIdle(trail []events.RunStatusPayload) bool {
	return len(trail) > 0 && trail[len(trail)-1].To == domain.RunNeedsAttention && trail[len(trail)-1].Reason == agentstatus.ReasonIdle
}

func statusIndex(trail []events.RunStatusPayload, to domain.RunStatus, reason string) int {
	return slices.IndexFunc(trail, func(p events.RunStatusPayload) bool { return p.To == to && p.Reason == reason })
}

func parks(trail []events.RunStatusPayload) int {
	n := 0
	for _, p := range trail {
		if p.To == domain.RunNeedsAttention {
			n++
		}
	}
	return n
}

// An agent override like the test mock's drops the shipped profile's Claude
// flags, so these runs use the shipped claude argv and the runtime's mock.
func TestEnhancedClaudeRunStaysRunningWhileABackgroundTaskRuns(t *testing.T) {
	t.Parallel()
	e, _ := newACPEnv(t)
	sub := e.subscribe(t)
	run := e.launchClaudeACP(t, acpmock.PromptBackground)
	trail := e.statusTrail(t, sub, run.ID, parkedIdle)
	if i := statusIndex(trail, domain.RunRunning, "working in background: 1 task"); i < 0 || trail[i].From != domain.RunRunning {
		t.Fatalf("status events %+v, want running -> running %q", trail, "working in background: 1 task")
	}
	if n := parks(trail); n != 1 {
		t.Fatalf("status events %+v, want the one park after the follow-up", trail)
	}
	if !assistantSaidNow(e, run.ID, "background done") {
		t.Fatal("the run parked before the background follow-up")
	}
	got := e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
	if got.Reason != agentstatus.ReasonIdle {
		t.Fatalf("run reason %q, want %q", got.Reason, agentstatus.ReasonIdle)
	}
}

func TestEnhancedClaudeRunCountsDownItsBackgroundTasks(t *testing.T) {
	t.Parallel()
	e, _ := newACPEnv(t)
	sub := e.subscribe(t)
	run := e.launchClaudeACP(t, acpmock.PromptBackgroundPair)
	trail := e.statusTrail(t, sub, run.ID, parkedIdle)
	two := statusIndex(trail, domain.RunRunning, "working in background: 2 tasks")
	one := statusIndex(trail, domain.RunRunning, "working in background: 1 task")
	if two < 0 || one < two {
		t.Fatalf("status events %+v, want 2 tasks before 1 task", trail)
	}
	if n := parks(trail); n != 1 {
		t.Fatalf("status events %+v, want the one park after both follow-ups", trail)
	}
}

func TestEnhancedClaudeRunParksWhileADevServerRuns(t *testing.T) {
	t.Parallel()
	e, _ := newACPEnv(t)
	sub := e.subscribe(t)
	run := e.launchClaudeACP(t, acpmock.PromptDevServer)
	trail := e.statusTrail(t, sub, run.ID, parkedIdle)
	if i := slices.IndexFunc(trail, func(p events.RunStatusPayload) bool { return strings.HasPrefix(p.Reason, agentstatus.ReasonBackground) }); i >= 0 {
		t.Fatalf("status events %+v, want the dev server never to hold the run working", trail)
	}
}

// Without Claude's task frames the host cannot tell a follow-up from noise,
// so output after the turn leaves the run parked.
func TestEnhancedRunOfAnotherAgentStaysParkedOnOutputAfterItsTurn(t *testing.T) {
	t.Parallel()
	e, _ := newACPEnv(t)
	sub := e.subscribe(t)
	run := e.launchACP(t, acpmock.PromptBackground)
	trail := e.statusTrail(t, sub, run.ID, parkedIdle)
	waitItems(t, e.sched, run.ID, "the late output", func(items []acphost.Item) bool {
		return assistantSaid("background done")(items) && slices.ContainsFunc(items, func(it acphost.Item) bool { return it.Usage != nil })
	})
	trail = append(trail, e.runStatusEvents(sub, run.ID)...)
	if last := trail[len(trail)-1]; last.To != domain.RunNeedsAttention || parks(trail) != 1 {
		t.Fatalf("status events %+v, want one park and nothing after it", trail)
	}
	if i := slices.IndexFunc(trail, func(p events.RunStatusPayload) bool { return strings.HasPrefix(p.Reason, agentstatus.ReasonBackground) }); i >= 0 {
		t.Fatalf("status events %+v, want no background reason", trail)
	}
}

func TestEnhancedClaudeRunRestoredWithATaskPendingIsInterrupted(t *testing.T) {
	delay := acpmock.BackgroundDelay
	acpmock.BackgroundDelay = time.Hour
	t.Cleanup(func() { acpmock.BackgroundDelay = delay })
	e, _ := newACPEnv(t)
	run := e.launchClaudeACP(t, acpmock.PromptBackground)
	waitFor(t, "the turn to end with background work pending", func() bool {
		sess := e.sched.acp.session(run.ID)
		return sess != nil && assistantSaidNow(e, run.ID, "started") && sess.State().Background
	})
	waitFor(t, "the run to report its task", func() bool {
		r, err := e.db.GetRun(t.Context(), run.ID)
		return err == nil && r.Reason == "working in background: 1 task"
	})
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
	waitFor(t, "the restored run to report the interrupted turn", func() bool {
		r, err := e.db.GetRun(t.Context(), run.ID)
		return err == nil && r.Status == domain.RunNeedsAttention && r.Reason == acpInterruptedReason
	})
}

func assistantSaidNow(e *testEnv, run domain.RunID, text string) bool {
	page, err := e.sched.ACPHistory(run, 0, 0)
	return err == nil && assistantSaid(text)(page.Items)
}

func TestEnhancedClaudeRunKeepsAQuestionAskedBeforeItsTurnEndedWithTasksLive(t *testing.T) {
	delay := acpmock.BackgroundDelay
	acpmock.BackgroundDelay = 4 * time.Second
	t.Cleanup(func() { acpmock.BackgroundDelay = delay })
	e, _ := newACPEnv(t)
	withCoordination(t, e)
	sub := e.subscribe(t)
	run := e.launchClaudeACP(t, "")
	waitFor(t, "idle session", func() bool { return e.sched.acp.session(run.ID) != nil })
	sess := e.sched.acp.session(run.ID)
	inject := func(text string) {
		t.Helper()
		if _, err := e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: text}, false, nil); err != nil {
			t.Fatal(err)
		}
	}
	inject(acpmock.PromptBackground)
	waitFor(t, "the run to report its task", func() bool {
		r, err := e.db.GetRun(t.Context(), run.ID)
		return err == nil && r.Reason == "working in background: 1 task"
	})
	inject(acpmock.PromptAskPermission)
	var pending []domain.RunInputRequest
	waitFor(t, "the permission request", func() bool {
		pending = e.sched.PendingInputs(run.ID)
		return len(pending) == 1
	})
	if err := e.sched.ReportNeedsInput(t.Context(), run.ID, "needs-input-1", "which region?", time.Now()); err != nil {
		t.Fatal(err)
	}
	if r, err := e.db.GetRun(t.Context(), run.ID); err != nil || r.Status != domain.RunRunning {
		t.Fatalf("run = %+v, %v; want running until the turn ends", r, err)
	}
	if err := e.sched.ACPAnswer(run.ID, pending[0].ID, "allow", nil); err != nil {
		t.Fatal(err)
	}
	const question = "needs input: which region?"
	trail := e.statusTrail(t, sub, run.ID, func(trail []events.RunStatusPayload) bool {
		return statusIndex(trail, domain.RunNeedsAttention, question) >= 0
	})
	if !sess.State().Background {
		t.Fatalf("the question showed after the task drained, not at the turn end: %+v", sess.State())
	}

	waitItems(t, e.sched, run.ID, "the autonomous follow-up", func(items []acphost.Item) bool {
		return assistantSaid("background done")(items) && slices.ContainsFunc(items, func(it acphost.Item) bool { return it.Usage != nil })
	})
	waitFor(t, "the follow-up to settle", func() bool { return !sess.State().Background && !sess.State().TurnInFlight })
	trail = append(trail, e.runStatusEvents(sub, run.ID)...)
	asked := statusIndex(trail, domain.RunNeedsAttention, question)
	for _, p := range trail[asked+1:] {
		if p.To != domain.RunNeedsAttention || p.Reason != question {
			t.Fatalf("status events %+v, want nothing to move the run off its question", trail[asked:])
		}
	}
	if r, err := e.db.GetRun(t.Context(), run.ID); err != nil || r.Status != domain.RunNeedsAttention || r.Reason != question {
		t.Fatalf("run = %+v, %v; want the question kept after the tasks ended", r, err)
	}

	inject("thanks")
	e.statusTrail(t, sub, run.ID, func(trail []events.RunStatusPayload) bool {
		return slices.ContainsFunc(trail, func(p events.RunStatusPayload) bool { return p.To == domain.RunRunning })
	})
}

func TestEnhancedClaudeRunParksAQuestionAskedBetweenTurnsWithTasksLive(t *testing.T) {
	t.Parallel()
	e, _ := newACPEnv(t)
	withCoordination(t, e)
	run := e.launchClaudeACP(t, acpmock.PromptHold)
	waitFor(t, "the run to report its task", func() bool {
		r, err := e.db.GetRun(t.Context(), run.ID)
		return err == nil && r.Reason == "working in background: 1 task"
	})
	if err := e.sched.ReportNeedsInput(t.Context(), run.ID, "needs-input-1", "which region?", time.Now()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the question", func() bool {
		r, err := e.db.GetRun(t.Context(), run.ID)
		return err == nil && r.Status == domain.RunNeedsAttention && r.Reason == "needs input: which region?"
	})
	if !e.sched.acp.session(run.ID).State().Background {
		t.Fatal("the held task is not live")
	}
}
