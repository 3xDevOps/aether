package scheduler

import (
	"bytes"
	"encoding/json"
	"slices"
	"testing"

	acp "github.com/coder/acp-go-sdk"

	"github.com/3xDevOps/Aether/internal/acphost"
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

func (w *acpWire) setOption(t *testing.T, configID string) []any {
	t.Helper()
	w.mu.Lock()
	defer w.mu.Unlock()
	var values []any
	for line := range bytes.SplitSeq(w.data.Bytes(), []byte("\n")) {
		var request struct {
			Method string `json:"method"`
			Params struct {
				ConfigID string `json:"configId"`
				Value    any    `json:"value"`
			} `json:"params"`
		}
		if json.Unmarshal(line, &request) == nil && request.Method == acp.AgentMethodSessionSetConfigOption && request.Params.ConfigID == configID {
			values = append(values, request.Params.Value)
		}
	}
	return values
}

func TestEnhancedRunSetsItsModelOnOpenAndRestore(t *testing.T) {
	t.Parallel()
	e, rt := newACPEnv(t, withMockClaude)
	run, err := e.sched.LaunchWithOptions(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "say pong", "claude", domain.LaunchACP, domain.LaunchOptions{Model: "opus"})
	if err != nil {
		t.Fatal(err)
	}
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	if got := rt.all()[0].wire.setOption(t, "model"); !slices.Equal(got, []any{"opus"}) {
		t.Fatalf("model option set to %v, want [opus]", got)
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
	if got := execs[len(execs)-1].wire.setOption(t, "model"); !slices.Equal(got, []any{"opus"}) {
		t.Fatalf("restored session's model option set to %v, want [opus]", got)
	}
}

func TestEnhancedRunReportsAModelTheAgentRefused(t *testing.T) {
	t.Parallel()
	e, rt := newACPEnv(t, withMockClaude)
	// No configOptions, so the model goes through session/set_model, which
	// the mock agent does not implement.
	rt.fixture.SessionNew = json.RawMessage(`{"sessionId":"legacy"}`)
	run, err := e.sched.LaunchWithOptions(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "say pong", "claude", domain.LaunchACP, domain.LaunchOptions{Model: "opus"})
	if err != nil {
		t.Fatal(err)
	}
	waitItems(t, e.sched, run.ID, "the model notice", func(items []acphost.Item) bool {
		return slices.ContainsFunc(items, func(it acphost.Item) bool {
			return it.Kind == acphost.KindNotice && it.Notice.Severity == "error" && it.Notice.Title == "Model not set"
		})
	})
	if !slices.Contains(rt.all()[0].agent.Methods(), "session/set_model") {
		t.Fatal("the agent was never sent session/set_model")
	}
}
