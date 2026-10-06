package scheduler

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/acphost/acpmock"
	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// newBackgroundEnv is an enhanced env whose fake agent also has a headless
// command line. installed puts the agent and its ACP server in the member's
// home, where a background launch looks for them.
func newBackgroundEnv(t *testing.T, installed bool) (*testEnv, *acpRuntime) {
	t.Helper()
	e, rt := newACPEnv(t, func(cfg *Config) {
		cfg.Harnesses["fake"] = HarnessSpec{
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

func (e *testEnv) launchBackground(t *testing.T, task string) *domain.Run {
	t.Helper()
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, task, "fake", domain.LaunchHeadless)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	return run
}

func TestBackgroundRunOverACPFinishesOnEndTurn(t *testing.T) {
	t.Parallel()
	e, rt := newBackgroundEnv(t, true)
	run := e.launchBackground(t, "say pong")
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
	items, err := e.sched.ACPHistory(run.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !assistantSaid("pong")(items) || !turnEnded("end_turn", 1)(items) {
		t.Fatalf("the item log does not hold the one turn: %+v", items)
	}
	if !slices.ContainsFunc(e.rt.execRuns(), func(call fakeExecCall) bool { return slices.Contains(call.argv, "kill -USR1 1") }) {
		t.Fatalf("the container was not ended with SIGUSR1: %+v", e.rt.execRuns())
	}
}

func TestBackgroundRunOverACPAllowsPermissions(t *testing.T) {
	t.Parallel()
	e, _ := newBackgroundEnv(t, true)
	run := e.launchBackground(t, acpmock.PromptAskPermission)
	e.waitStoreStatus(t, run.ID, domain.RunCompleted)
	items, err := e.sched.ACPHistory(run.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
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
	e, _ := newBackgroundEnv(t, true)
	run := e.launchBackground(t, acpmock.PromptWait)
	waitFor(t, "the turn", func() bool {
		return e.sched.acp.session(run.ID) != nil && e.sched.acp.session(run.ID).State().TurnInFlight
	})
	if err := e.sched.ACPCancel(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	got := e.waitStoreStatus(t, run.ID, domain.RunFailed)
	if got.Reason != exitedFailedReasonPrefix+"1" {
		t.Fatalf("reason %q, want %q", got.Reason, exitedFailedReasonPrefix+"1")
	}
}

func TestBackgroundRunOverACPContinuesAfterRestart(t *testing.T) {
	t.Parallel()
	e, _ := newBackgroundEnv(t, true)
	run := e.launchBackground(t, acpmock.PromptWait)
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
	items, err := s2.ACPHistory(run.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(items, func(it acphost.Item) bool {
		return it.Kind == acphost.KindMessage && it.Message.Role == "user" && it.Message.Text == oneShotResume
	}) {
		t.Fatalf("the restored session was not asked to continue: %+v", items)
	}
}

// An agent that cannot restore the interrupted session starts a new one,
// which has never seen the task.
func TestBackgroundRunOverACPResendsTaskToANewSession(t *testing.T) {
	t.Parallel()
	e, rt := newBackgroundEnv(t, true)
	run := e.launchBackground(t, acpmock.PromptWait)
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
		items, err := s2.ACPHistory(run.ID, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		var prompts []string
		reset := false
		for _, it := range items {
			switch {
			case it.Kind == acphost.KindReset:
				reset = true
			case it.Kind == acphost.KindMessage && it.Message.Role == "user":
				prompts = append(prompts, it.Message.Text)
			}
		}
		return reset && slices.Equal(prompts, []string{s2.withCoAuthorInstruction(acpmock.PromptWait), s2.withCoAuthorInstruction(acpmock.PromptWait)})
	})
	if err := s2.ACPCancel(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	e.waitStoreStatus(t, run.ID, domain.RunFailed)
}

func TestBackgroundRunWithoutACPServerKeepsCommandLine(t *testing.T) {
	t.Parallel()
	e, rt := newBackgroundEnv(t, false)
	run := e.launchBackground(t, "say pong")
	if run.ACP {
		t.Fatal("a background run without an installed ACP server is driven over ACP")
	}
	if cmd := e.rt.byName(string(run.ID)).spec.Command; !slices.Equal(cmd, []string{"fake-agent", "-p", e.sched.withCoAuthorInstruction("say pong")}) {
		t.Fatalf("container command %q, want the headless command line", cmd)
	}
	if execs := rt.all(); len(execs) != 0 {
		t.Fatalf("started an ACP server: %+v", execs)
	}
}
