package scheduler

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
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
	rt.dropAdapters(t)

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

func (w *acpWire) setMode(t *testing.T) []string {
	t.Helper()
	w.mu.Lock()
	defer w.mu.Unlock()
	var modes []string
	for line := range bytes.SplitSeq(w.data.Bytes(), []byte("\n")) {
		var request struct {
			Method string `json:"method"`
			Params struct {
				ModeID string `json:"modeId"`
			} `json:"params"`
		}
		if json.Unmarshal(line, &request) == nil && request.Method == acp.AgentMethodSessionSetMode {
			modes = append(modes, request.Params.ModeID)
		}
	}
	return modes
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

func TestComposerModelChoiceIsRecorded(t *testing.T) {
	t.Parallel()
	e, _ := newACPEnv(t, withMockClaude)
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "say pong", "claude", domain.LaunchACP)
	if err != nil {
		t.Fatal(err)
	}
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	sub := e.subscribe(t)
	if err = e.sched.ACPSetOption(t.Context(), run.ID, "mode", "plan"); err != nil {
		t.Fatal(err)
	}
	row, err := e.db.GetRun(t.Context(), run.ID)
	if err != nil || row.Model != "" {
		t.Fatalf("after a mode change the session's model is %q (%v), want none", row.Model, err)
	}
	if err = e.sched.ACPSetOption(t.Context(), run.ID, "model", "opus"); err != nil {
		t.Fatal(err)
	}
	if row, err = e.db.GetRun(t.Context(), run.ID); err != nil || row.Model != "opus" {
		t.Fatalf("after a model change the session's model is %q (%v), want opus", row.Model, err)
	}
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for {
		select {
		case ev := <-sub.Events():
			if p, ok := ev.Payload.(events.RunModelPayload); ok && ev.RunID == run.ID {
				if p.Model != "opus" {
					t.Fatalf("run.model event = %q, want opus", p.Model)
				}
				return
			}
		case <-timer.C:
			t.Fatal("no run.model event")
		}
	}
}

func TestEnhancedRunSetsItsEffortAfterItsModelOnOpenAndRestore(t *testing.T) {
	t.Parallel()
	e, rt := newACPEnv(t, withMockClaude)
	run, err := e.sched.LaunchWithOptions(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "say pong", "claude", domain.LaunchACP, domain.LaunchOptions{Model: "opus", Effort: "low"})
	if err != nil {
		t.Fatal(err)
	}
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	exec := rt.all()[0]
	if got := exec.wire.setOption(t, "effort"); !slices.Equal(got, []any{"low"}) {
		t.Fatalf("effort option set to %v, want [low]", got)
	}
	exec.wire.mu.Lock()
	wire := exec.wire.data.String()
	exec.wire.mu.Unlock()
	if model, effort := strings.Index(wire, `"configId":"model"`), strings.Index(wire, `"configId":"effort"`); model < 0 || effort < model {
		t.Fatalf("the effort was not set after the model: %s", wire)
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
	if got := execs[len(execs)-1].wire.setOption(t, "effort"); !slices.Equal(got, []any{"low"}) {
		t.Fatalf("restored session's effort option set to %v, want [low]", got)
	}
}

func TestEnhancedRunReportsAnEffortTheAgentRefused(t *testing.T) {
	t.Parallel()
	e, rt := newACPEnv(t, withMockClaude)
	// No configOptions, so the agent offers no effort option.
	rt.fixture.SessionNew = json.RawMessage(`{"sessionId":"legacy"}`)
	run, err := e.sched.LaunchWithOptions(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "say pong", "claude", domain.LaunchACP, domain.LaunchOptions{Effort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	waitItems(t, e.sched, run.ID, "the effort notice", func(items []acphost.Item) bool {
		return slices.ContainsFunc(items, func(it acphost.Item) bool {
			return it.Kind == acphost.KindNotice && it.Notice.Severity == "error" && it.Notice.Title == "Effort not set" &&
				it.Notice.Description == "acphost: the agent offers no effort option"
		})
	})
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
}

func TestEnhancedCopilotTakesItsEffortAsAnAdapterFlag(t *testing.T) {
	t.Parallel()
	e, rt := newACPEnv(t)
	run, err := e.sched.LaunchWithOptions(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "say pong", "copilot", domain.LaunchACP, domain.LaunchOptions{Effort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	exec := rt.all()[0]
	if want := []string{"copilot", "--acp", "--allow-all", "--reasoning-effort", "high"}; !slices.Equal(exec.argv, want) {
		t.Fatalf("adapter argv = %v, want %v", exec.argv, want)
	}
	if got := exec.wire.setOption(t, "effort"); len(got) != 0 {
		t.Fatalf("effort option set to %v, want none beside the flag", got)
	}
}

func TestComposerEffortChoiceIsRecorded(t *testing.T) {
	t.Parallel()
	e, _ := newACPEnv(t, withMockClaude)
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "say pong", "claude", domain.LaunchACP)
	if err != nil {
		t.Fatal(err)
	}
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	sub := e.subscribe(t)
	if err = e.sched.ACPSetOption(t.Context(), run.ID, "mode", "plan"); err != nil {
		t.Fatal(err)
	}
	row, err := e.db.GetRun(t.Context(), run.ID)
	if err != nil || row.Effort != "" {
		t.Fatalf("after a mode change the session's effort is %q (%v), want none", row.Effort, err)
	}
	// The adapter's "default" is the agent's own level, which a run records
	// as no effort.
	for _, pick := range []struct{ value, want string }{{"medium", "medium"}, {"default", ""}} {
		if err = e.sched.ACPSetOption(t.Context(), run.ID, "effort", pick.value); err != nil {
			t.Fatal(err)
		}
		if row, err = e.db.GetRun(t.Context(), run.ID); err != nil || row.Effort != pick.want {
			t.Fatalf("after picking %s the session's effort is %q (%v), want %q", pick.value, row.Effort, err, pick.want)
		}
		timer := time.NewTimer(3 * time.Second)
		for published := false; !published; {
			select {
			case ev := <-sub.Events():
				if p, ok := ev.Payload.(events.RunEffortPayload); ok && ev.RunID == run.ID {
					if p.Effort != pick.want {
						t.Fatalf("run.effort event = %q, want %q", p.Effort, pick.want)
					}
					published = true
				}
			case <-timer.C:
				t.Fatalf("no run.effort event after picking %s", pick.value)
			}
		}
		timer.Stop()
	}
}

func TestEnhancedRunOfACustomAgentNamedApartFromItsCommand(t *testing.T) {
	t.Parallel()
	e, rt := newACPEnv(t)
	storeMemberDefinition(t, e, e.member.ID, harness.Definition{
		Name:         "mycursor",
		Executable:   "agent",
		TUIArgs:      []string{"agent", "{task}"},
		HeadlessArgs: []string{"agent", "-p", "{task}"},
		ACPArgs:      []string{"agent", "acp"},
		ACPMode:      "plan",
	})
	if err := e.sched.ValidateLaunch(t.Context(), e.member.ID, e.member.ID, "mycursor", domain.LaunchACP, "opus", ""); err != nil {
		t.Fatalf("ValidateLaunch of an Enhanced launch with a model: %v", err)
	}
	if err := e.sched.ValidateLaunch(t.Context(), e.member.ID, e.member.ID, "mycursor", domain.LaunchTUI, "opus", ""); err == nil {
		t.Fatal("ValidateLaunch accepted a Standard launch with a model for an agent with no model flag")
	}
	run, err := e.sched.LaunchWithOptions(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "say pong", "mycursor", domain.LaunchACP, domain.LaunchOptions{Model: "opus"})
	if err != nil {
		t.Fatal(err)
	}
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	exec := rt.all()[0]
	if !slices.Equal(exec.argv, []string{"agent", "acp"}) {
		t.Fatalf("agent exec argv = %v, want [agent acp]", exec.argv)
	}
	if got := exec.wire.setMode(t); !slices.Equal(got, []string{"plan"}) {
		t.Fatalf("session/set_mode sent %v, want [plan]", got)
	}
	if got := exec.wire.setOption(t, "model"); !slices.Equal(got, []any{"opus"}) {
		t.Fatalf("model option set to %v, want [opus]", got)
	}
	if got := rt.byName(string(run.ID)).spec.Env["AETHER_HARNESS"]; got != "mycursor" {
		t.Fatalf("AETHER_HARNESS = %q, want the agent's name mycursor", got)
	}
}

func TestEnhancedCursorRunsAgentACPWithItsForceFlag(t *testing.T) {
	t.Parallel()
	e, rt := newACPEnv(t)
	if err := e.sched.ValidateLaunch(t.Context(), e.member.ID, e.member.ID, "cursor", domain.LaunchACP, "gpt-5", ""); err != nil {
		t.Fatalf("ValidateLaunch of an Enhanced cursor run with a model: %v", err)
	}
	run, err := e.sched.LaunchWithOptions(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "say pong", "cursor", domain.LaunchACP, domain.LaunchOptions{Model: "gpt-5"})
	if err != nil {
		t.Fatal(err)
	}
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	exec := rt.all()[0]
	if !slices.Equal(exec.argv, []string{"agent", "--force", "acp"}) {
		t.Fatalf("agent exec argv = %v, want [agent --force acp]", exec.argv)
	}
	if got := exec.wire.setMode(t); len(got) != 0 {
		t.Fatalf("session/set_mode sent %v, want none: --force already approves everything", got)
	}
	if got := rt.byName(string(run.ID)).spec.Env["AETHER_HARNESS"]; got != "cursor" {
		t.Fatalf("AETHER_HARNESS = %q, want cursor", got)
	}
}

func TestEnhancedLaunchOfAntigravityIsRefused(t *testing.T) {
	t.Parallel()
	e, _ := newACPEnv(t)
	err := e.sched.ValidateLaunch(t.Context(), e.member.ID, e.member.ID, "antigravity", domain.LaunchACP, "", "")
	if err == nil {
		t.Fatal("ValidateLaunch accepted an Enhanced antigravity launch")
	}
	if _, err = e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "say pong", "antigravity", domain.LaunchACP); err == nil {
		t.Fatal("Launch accepted an Enhanced antigravity run")
	}
}
