package scheduler

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/3xDevOps/Aether/internal/acphost/acpmock"
	"github.com/3xDevOps/Aether/internal/agentstatus"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/quota"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/store"
)

// claudeUsage reports each account's used share of its Claude five-hour
// window; an account it has no figure for reads 0%.
type claudeUsage map[domain.MemberID]float64

func (u claudeUsage) Read(_ context.Context, account domain.MemberID, _ bool) ([]quota.Provider, error) {
	resets := time.Now().Add(time.Hour)
	return []quota.Provider{{Provider: "claude", Status: "ok", Windows: []quota.Window{
		{ID: "five_hour", UsedPercent: u[account], ResetsAt: &resets},
	}}}, nil
}

type roomNotes struct {
	mu     sync.Mutex
	bodies []string
}

func (n *roomNotes) post(_ context.Context, _ *domain.Run, body, _ string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.bodies = append(n.bodies, body)
	return nil
}

func (n *roomNotes) all() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Clone(n.bodies)
}

const continuedOnGrace = "Your previous turn stopped because Ada's usage ran out. The run continues on Grace's account. Continue the task where you stopped."

// newFallbackEnv is Ada running Claude on her own account with her own
// installation, Grace logged in to Claude, and Grace's usage at 10%. Grace
// has not shared her account yet.
func newFallbackEnv(t *testing.T, mutate func(*Config)) (*shareEnv, *acpRuntime, *roomNotes) {
	t.Helper()
	rt := newACPRuntime(t)
	e := newShareEnv(t, func(cfg *Config) {
		cfg.Runtime = rt
		mutate(cfg)
	})
	rt.images = e.rt.images
	e.rt = rt.fakeRuntime
	for _, name := range []string{"claude", "fake-claude", "acp-mock"} {
		installAgent(t, e.adaHome, name, ".local/bin/"+name, "")
	}
	writeHomeFiles(t, e.ownerHome, ".claude/.credentials.json")
	notes := &roomNotes{}
	e.sched.UseUsageFallback(UsageFallback{
		Usage:           claudeUsage{e.owner.ID: 10},
		AuthorizationMu: &sync.Mutex{},
		Note:            notes.post,
	})
	return e, rt, notes
}

func enhancedClaude(cfg *Config) {
	cfg.Harnesses["claude"] = HarnessSpec{TUIArgs: []string{"fake-claude", "{task}"}, ACPArgs: []string{"acp-mock"}}
}

func (e *shareEnv) share(t *testing.T) {
	t.Helper()
	if err := e.db.ShareAccount(t.Context(), e.owner.ID, e.member.ID); err != nil {
		t.Fatal(err)
	}
}

func (e *shareEnv) assertMovedToOwner(t *testing.T, run domain.RunID, notes *roomNotes) runtime.Spec {
	t.Helper()
	waitFor(t, "the run on Grace's account", func() bool {
		row, err := e.db.GetRun(t.Context(), run)
		return err == nil && row.AccountMemberID == e.owner.ID
	})
	row, err := e.db.GetRun(t.Context(), run)
	if err != nil || row.RequestedAccountMemberID != e.member.ID || row.Status.Terminal() {
		t.Fatalf("moved run = %+v, %v; want it live, requested on Ada's account", row, err)
	}
	segments, err := e.db.ListRunAccountSegments(t.Context(), run)
	if err != nil || len(segments) != 2 || segments[0].AccountMemberID != e.member.ID || segments[0].Reason != store.RunAccountLaunchReason ||
		segments[1].AccountMemberID != e.owner.ID || segments[1].Reason != usageLimitSegment {
		t.Fatalf("segments = %+v, %v", segments, err)
	}
	waitFor(t, "the room note", func() bool {
		return slices.Equal(notes.all(), []string{"Continued on Grace's account because Ada's usage ran out."})
	})
	c := e.rt.byName(string(run))
	if c == nil || e.containerCount() != 1 {
		t.Fatalf("%d containers, want only the run's new one", e.containerCount())
	}
	if c.spec.Env["AETHER_ACCOUNT_MEMBER_ID"] != string(e.owner.ID) {
		t.Fatalf("new container account = %q", c.spec.Env["AETHER_ACCOUNT_MEMBER_ID"])
	}
	e.assertOwnerExposedOnlyAt(t, c.spec, ".claude/.credentials.json")
	return c.spec
}

// An Enhanced run whose turn fails on Ada's usage limit continues on Grace's
// shared account: a new container mounts Grace's login, the agent's session
// is resumed there and told why, the room says so, and Ada keeps the
// segment she spent.
func TestEnhancedRunContinuesOnAFallbackAccount(t *testing.T) {
	t.Parallel()
	e, rt, notes := newFallbackEnv(t, enhancedClaude)
	e.share(t)
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "", "claude", domain.LaunchACP)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })
	first := e.rt.byName(string(run.ID))
	_, _ = e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: acpmock.PromptUsageLimit}, false, nil)

	e.assertMovedToOwner(t, run.ID, notes)
	if e.rt.byName(string(run.ID)) == first {
		t.Fatal("the run kept Ada's container")
	}
	waitFor(t, "the continuation prompt", func() bool {
		execs := rt.all()
		last := execs[len(execs)-1]
		prompts := last.wire.prompts(t)
		return len(prompts) == 1 && prompts[0][0].Text != nil && prompts[0][0].Text.Text == continuedOnGrace
	})
	execs := rt.all()
	if methods := execs[len(execs)-1].agent.Methods(); !slices.Contains(methods, acp.AgentMethodSessionResume) || slices.Contains(methods, acp.AgentMethodSessionNew) {
		t.Fatalf("the moved session opened with %v, want session/resume", methods)
	}
	sc, err := e.sched.readSidecar(run.ID)
	if err != nil || sc.AccountSwap != nil || sc.LoginMember != string(e.owner.ID) {
		t.Fatalf("sidecar = %+v, %v; want the move settled on Grace's login", sc, err)
	}
}

// With no account to continue on, the Enhanced run waits for its owner with
// the real reason, in the container it had.
func TestEnhancedRunWithoutFallbackNeedsAttention(t *testing.T) {
	t.Parallel()
	e, _, notes := newFallbackEnv(t, enhancedClaude)
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "", "claude", domain.LaunchACP)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })
	first := e.rt.byName(string(run.ID))
	_, _ = e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: acpmock.PromptUsageLimit}, false, nil)

	got := e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
	if got.Reason != "Usage limit reached on Ada's account; no fallback has usage left" || got.AccountMemberID != e.member.ID {
		t.Fatalf("run = reason %q account %s", got.Reason, got.AccountMemberID)
	}
	if e.rt.byName(string(run.ID)) != first || first.currentState() != "running" || len(notes.all()) != 0 {
		t.Fatal("a run with no fallback lost its container or posted a move")
	}
}

// A headless Claude run whose output reported Ada's usage limit before it
// exited resumes its session on Grace's account with the continuation as
// its prompt.
func TestHeadlessRunContinuesOnAFallbackAccount(t *testing.T) {
	t.Parallel()
	e, _, notes := newFallbackEnv(t, func(cfg *Config) { delete(cfg.Harnesses, "claude") })
	e.share(t)
	startScheduler(t, e.sched)
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "fix it", "claude", domain.LaunchHeadless)
	if err != nil {
		t.Fatal(err)
	}
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
	for _, payload := range []events.Payload{
		events.AgentEventPayload{Kind: events.AgentSession, HarnessSessionID: "sess-1"},
		events.RunUsageLimitPayload{Message: "You've hit your session limit · resets 3pm (UTC)"},
	} {
		if _, perr := e.bus.Publish(t.Context(), events.Event{WorkspaceID: e.ws.ID, RunID: run.ID, Payload: payload}); perr != nil {
			t.Fatal(perr)
		}
	}
	waitFor(t, "the usage limit seen", func() bool {
		e.sched.mu.Lock()
		defer e.sched.mu.Unlock()
		entry := e.sched.runs[run.ID]
		return entry != nil && entry.usageLimit != ""
	})
	first := e.rt.byName(string(run.ID))
	first.exitNow(1)

	spec := e.assertMovedToOwner(t, run.ID, notes)
	want := []string{"claude", "-p", "--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions", "--resume", "sess-1", continuedOnGrace}
	if !slices.Equal(spec.Command, want) {
		t.Fatalf("command = %q, want %q", spec.Command, want)
	}
	row, err := e.db.GetRun(t.Context(), run.ID)
	if err != nil || row.Status != domain.RunRunning || strings.HasPrefix(row.Reason, exitedFailedReasonPrefix) {
		t.Fatalf("run = %+v, %v; want it running", row, err)
	}
}

// A Standard Claude run whose StopFailure hook reported Ada's usage limit
// reopens its session in Grace's terminal with the continuation as the
// first prompt.
func TestStandardRunContinuesOnAFallbackAccount(t *testing.T) {
	t.Parallel()
	e, _, notes := newFallbackEnv(t, func(cfg *Config) { delete(cfg.Harnesses, "claude") })
	e.share(t)
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "fix it", "claude", domain.LaunchTUI)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.sched.ReportAgentState(t.Context(), run.ID, agentstatus.Report{State: agentstatus.Working, SessionID: "sess-1"}); err != nil {
		t.Fatal(err)
	}
	first := e.rt.byName(string(run.ID))
	limit, ok := agentstatus.FromClaudeHook([]byte(`{"session_id":"sess-1","hook_event_name":"StopFailure","error":"rate_limit","last_assistant_message":"You've hit your session limit · resets 3pm (UTC)"}`))
	if !ok {
		t.Fatal("the StopFailure hook was not mapped")
	}
	if err = e.sched.ReportAgentState(t.Context(), run.ID, limit); err != nil {
		t.Fatal(err)
	}

	spec := e.assertMovedToOwner(t, run.ID, notes)
	if e.rt.byName(string(run.ID)) == first {
		t.Fatal("the run kept Ada's container")
	}
	want := []string{"claude", "--dangerously-skip-permissions", "--resume", "sess-1", continuedOnGrace}
	agent := spec.Command[slices.Index(spec.Command, "aether-run-supervisor")+1:]
	if !slices.Equal(agent[:min(len(want), len(agent))], want) {
		t.Fatalf("command = %q, want it to start %q", spec.Command, want)
	}
	if row, err := e.db.GetRun(t.Context(), run.ID); err != nil || row.Status != domain.RunRunning {
		t.Fatalf("run = %+v, %v; want it running", row, err)
	}
}

// A server that stopped between starting the run's container on Grace's
// account and recording the move records it on restart. One that stopped
// before the sidecar named that container destroys whatever was created and
// interrupts the run, rather than guess which account it runs on.
func TestRestartSettlesAnInterruptedAccountMove(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		started bool
	}{{"container recorded", true}, {"container not recorded", false}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e, _, _ := newFallbackEnv(t, enhancedClaude)
			run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "", "claude", domain.LaunchACP)
			if err != nil {
				t.Fatal(err)
			}
			waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })
			if err = e.sched.Close(); err != nil {
				t.Fatal(err)
			}
			sc, err := e.sched.readSidecar(run.ID)
			if err != nil {
				t.Fatal(err)
			}
			sc.AccountSwap = &accountSwap{From: e.member.ID, To: e.owner.ID, Container: sc.ContainerID}
			if !tc.started {
				sc.AccountSwap.Container, sc.ContainerID = "", ""
			}
			if err = e.sched.writeSidecar(sc); err != nil {
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

			if !tc.started {
				row := e.waitStoreStatus(t, run.ID, domain.RunInterrupted)
				if row.AccountMemberID != e.member.ID || e.containerCount() != 0 {
					t.Fatalf("account %s with %d containers, want Ada's and none", row.AccountMemberID, e.containerCount())
				}
				return
			}
			waitFor(t, "the recovered session", func() bool { return s2.acp.session(run.ID) != nil })
			row, err := e.db.GetRun(t.Context(), run.ID)
			if err != nil || row.AccountMemberID != e.owner.ID || row.Status.Terminal() {
				t.Fatalf("run = %+v, %v; want it live on Grace's account", row, err)
			}
			if segments, err := e.db.ListRunAccountSegments(t.Context(), run.ID); err != nil || len(segments) != 2 {
				t.Fatalf("segments = %+v, %v", segments, err)
			}
			if sc, err := s2.readSidecar(run.ID); err != nil || sc.AccountSwap != nil {
				t.Fatalf("sidecar = %+v, %v; want the move cleared", sc, err)
			}
		})
	}
}
