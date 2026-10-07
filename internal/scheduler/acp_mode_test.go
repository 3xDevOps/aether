package scheduler

import (
	"slices"
	"testing"

	acp "github.com/coder/acp-go-sdk"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
)

func withMockClaude(cfg *Config) {
	cfg.Harnesses["claude"] = HarnessSpec{TUIArgs: []string{"claude", "{task}"}, ACPArgs: []string{"acp-mock"}}
}

func TestEnhancedRunStartsInTheAgentsNoPromptMode(t *testing.T) {
	t.Parallel()
	e, rt := newACPEnv(t, withMockClaude)
	profile, _ := harness.Lookup("claude")
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "say pong", "claude", domain.LaunchACP)
	if err != nil {
		t.Fatal(err)
	}
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	if mode := e.sched.acp.session(run.ID).State().Mode; mode != profile.ACPMode {
		t.Fatalf("a new session runs in mode %q, want %q", mode, profile.ACPMode)
	}
	if !slices.Contains(rt.all()[0].agent.Methods(), acp.AgentMethodSessionSetMode) {
		t.Fatal("the agent was never asked to change its mode")
	}
}

func TestRestoredEnhancedRunKeepsItsRecordedMode(t *testing.T) {
	t.Parallel()
	e, rt := newACPEnv(t, withMockClaude)
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "say pong", "claude", domain.LaunchACP)
	if err != nil {
		t.Fatal(err)
	}
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	if err = e.sched.acp.session(run.ID).SetMode(t.Context(), "auto"); err != nil {
		t.Fatal(err)
	}
	if err = e.sched.Close(); err != nil {
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
	waitFor(t, "resumed session", func() bool { return s2.acp.session(run.ID) != nil })
	execs := rt.all()
	if !slices.Contains(execs[len(execs)-1].agent.Methods(), acp.AgentMethodSessionResume) {
		t.Fatal("the restart did not resume the session")
	}
	if mode := s2.acp.session(run.ID).State().Mode; mode != "auto" {
		t.Fatalf("restored session runs in mode %q, want its recorded auto", mode)
	}
}
