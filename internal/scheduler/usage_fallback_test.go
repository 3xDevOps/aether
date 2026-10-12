package scheduler

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/3xDevOps/Aether/internal/acphost/acpmock"
	"github.com/3xDevOps/Aether/internal/agentstatus"
	"github.com/3xDevOps/Aether/internal/disk"
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
	waitFor(t, "the move settled on Grace's login", func() bool {
		sc, err := e.sched.readSidecar(run.ID)
		return err == nil && sc.AccountSwap == nil && sc.LoginMember == string(e.owner.ID)
	})
}

// When the run does not move, the Enhanced run waits for its owner in the
// container it had, with the real reason: no account qualifies, fallback is
// off for the run, the replacement would not be admitted for capacity, or
// the owner may no longer launch at all.
func TestEnhancedRunWithoutFallbackNeedsAttention(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, reason string
		setup        func(*shareEnv)
		opts         domain.LaunchOptions
	}{
		{"no account qualifies", "Usage limit reached on Ada's account; no fallback has usage left", nil, domain.LaunchOptions{}},
		{"fallback off", "Usage limit reached on Ada's account; fallback is off for this run", nil,
			domain.LaunchOptions{Fallback: domain.AccountFallback{Disabled: true}}},
		{"no capacity for the replacement", "Usage limit reached on Ada's account; cannot continue on another account: ", func(e *shareEnv) {
			e.sched.cfg.filesystemCapacity = func(string) (disk.Usage, error) { return disk.Usage{}, errors.New("statfs failed") }
		}, domain.LaunchOptions{}},
		{"owner demoted to viewer", "Usage limit reached on Ada's account; cannot continue on another account: ", func(e *shareEnv) {
			e.member.Role = domain.RoleViewer
			if err := e.db.UpdateMember(context.Background(), e.member); err != nil {
				panic(err)
			}
		}, domain.LaunchOptions{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e, _, notes := newFallbackEnv(t, enhancedClaude)
			if tc.name != "no account qualifies" {
				e.share(t)
			}
			run, err := e.sched.LaunchWithOptions(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "", "claude", domain.LaunchACP, tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })
			if tc.setup != nil {
				tc.setup(e)
			}
			first := e.rt.byName(string(run.ID))
			_, _ = e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: acpmock.PromptUsageLimit}, false, nil)

			got := e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
			if !strings.HasPrefix(got.Reason, tc.reason) || (!strings.HasSuffix(tc.reason, ": ") && got.Reason != tc.reason) || got.AccountMemberID != e.member.ID {
				t.Fatalf("run = reason %q account %s, want %q", got.Reason, got.AccountMemberID, tc.reason)
			}
			if e.rt.byName(string(run.ID)) != first || first.currentState() != "running" || len(notes.all()) != 0 {
				t.Fatal("a run that did not move lost its container or posted a move")
			}
		})
	}
}

// After a handoff, the run's container still mounts the launcher's home, so
// an account shared only with the new owner is not mounted there.
func TestHandedOffRunMovesOnlyToAccountsSharedWithItsHome(t *testing.T) {
	t.Parallel()
	e, _, notes := newFallbackEnv(t, enhancedClaude)
	lin := e.addRecipient(t)
	if err := e.db.ShareAccount(t.Context(), e.owner.ID, lin.ID); err != nil {
		t.Fatal(err)
	}
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "", "claude", domain.LaunchACP)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })
	if err = e.db.TransferRun(t.Context(), run.ID, lin.ID); err != nil {
		t.Fatal(err)
	}
	_, _ = e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: acpmock.PromptUsageLimit}, false, nil)
	got := e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
	if got.Reason != "Usage limit reached on Ada's account; no fallback has usage left" || len(notes.all()) != 0 {
		t.Fatalf("handed-off run = reason %q, notes %v; want it kept off Grace's account", got.Reason, notes.all())
	}
}

// A container that exits while the move waits to choose an account is
// finalized when the run stays where it is, not left live with nothing
// supervising it.
func TestRunThatExitsDuringAFailedMoveIsFinalized(t *testing.T) {
	t.Parallel()
	e, _, _ := newFallbackEnv(t, enhancedClaude)
	mu := &sync.Mutex{}
	e.sched.UseUsageFallback(UsageFallback{Usage: claudeUsage{}, AuthorizationMu: mu})
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "", "claude", domain.LaunchACP)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })
	mu.Lock()
	_, _ = e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: acpmock.PromptUsageLimit}, false, nil)
	waitFor(t, "the move's claim", func() bool {
		e.sched.mu.Lock()
		defer e.sched.mu.Unlock()
		return e.sched.runs[run.ID].finalizing
	})
	e.rt.byName(string(run.ID)).exitNow(1)
	time.Sleep(50 * time.Millisecond)
	mu.Unlock()
	if got := e.waitStoreStatus(t, run.ID, domain.RunFailed); !strings.HasPrefix(got.Reason, exitedFailedReasonPrefix) {
		t.Fatalf("reason %q, want the exit", got.Reason)
	}
}

// An Enhanced run whose session cannot be restored on the new account waits
// for its owner there with the real error, and the room is not told it
// continued.
func TestEnhancedRunWhoseRestoreFailsNeedsAttention(t *testing.T) {
	t.Parallel()
	e, rt, notes := newFallbackEnv(t, enhancedClaude)
	e.share(t)
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "", "claude", domain.LaunchACP)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })
	rt.mu.Lock()
	rt.startErr = errors.New("exec: no such file")
	rt.mu.Unlock()
	_, _ = e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: acpmock.PromptUsageLimit}, false, nil)
	got := e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
	if !strings.HasPrefix(got.Reason, "Usage limit reached on Ada's account; the run is on Grace's account and its session could not be restored: ") ||
		!strings.Contains(got.Reason, "exec: no such file") || got.AccountMemberID != e.owner.ID || len(notes.all()) != 0 {
		t.Fatalf("run = reason %q account %s notes %v", got.Reason, got.AccountMemberID, notes.all())
	}
}

// A run that hits the limit again on its new account moves on to the next
// one, never back to an account it already spent.
func TestEnhancedRunMovesAgainOnASecondLimit(t *testing.T) {
	t.Parallel()
	e, _, notes := newFallbackEnv(t, enhancedClaude)
	e.share(t)
	lin := e.addRecipient(t)
	linHome, err := e.cfg.Homes.Path(lin.ID)
	if err != nil {
		t.Fatal(err)
	}
	writeHomeFiles(t, linHome, ".claude/.credentials.json")
	if err = e.db.ShareAccount(t.Context(), lin.ID, e.member.ID); err != nil {
		t.Fatal(err)
	}
	e.sched.UseUsageFallback(UsageFallback{Usage: claudeUsage{e.owner.ID: 10, lin.ID: 20}, AuthorizationMu: &sync.Mutex{}, Note: notes.post})
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "", "claude", domain.LaunchACP)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })
	_, _ = e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: acpmock.PromptUsageLimit}, false, nil)
	e.assertMovedToOwner(t, run.ID, notes)
	waitFor(t, "the moved session", func() bool {
		_, err := e.sched.acp.live(run.ID)
		sc, scErr := e.sched.readSidecar(run.ID)
		return err == nil && scErr == nil && sc.AccountSwap == nil
	})
	_, _ = e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: acpmock.PromptUsageLimit}, false, nil)
	waitFor(t, "the run on Lin's account", func() bool {
		row, err := e.db.GetRun(t.Context(), run.ID)
		return err == nil && row.AccountMemberID == lin.ID && !row.Status.Terminal()
	})
	if segments, err := e.db.ListRunAccountSegments(t.Context(), run.ID); err != nil || len(segments) != 3 {
		t.Fatalf("segments = %+v, %v", segments, err)
	}
}

// A headless Claude run whose output reported Ada's usage limit before it
// exited resumes its session on Grace's account with the continuation as
// its prompt, also when it exited while the server was down. The move holds
// the authorization mutex only to record the account, not while it creates
// the container. A container that cannot be created fails the run with that
// error, without a second attempt.
func TestHeadlessRunContinuesOnAFallbackAccount(t *testing.T) {
	t.Parallel()
	for _, tc := range []string{"live", "exited while the server was down", "new container fails", "no fallback"} {
		t.Run(tc, func(t *testing.T) {
			t.Parallel()
			e, _, notes := newFallbackEnv(t, func(cfg *Config) { delete(cfg.Harnesses, "claude") })
			e.share(t)
			mu := &sync.Mutex{}
			seam := UsageFallback{Usage: claudeUsage{e.owner.ID: 10}, AuthorizationMu: mu, Note: notes.post}
			e.sched.UseUsageFallback(seam)
			var locked atomic.Bool
			e.rt.createHook = func() {
				if !mu.TryLock() {
					locked.Store(true)
					return
				}
				mu.Unlock()
			}
			startScheduler(t, e.sched)
			run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "fix it", "claude", domain.LaunchHeadless)
			if err != nil {
				t.Fatal(err)
			}
			e.waitStoreStatus(t, run.ID, domain.RunRunning)
			<-e.sched.RecoveryReady()
			if _, perr := e.bus.Publish(t.Context(), events.Event{WorkspaceID: e.ws.ID, RunID: run.ID, Payload: events.RunUsageLimitPayload{
				Message: "You've hit your session limit · resets 3pm (UTC)", SessionID: "sess-1",
			}}); perr != nil {
				t.Fatal(perr)
			}
			waitFor(t, "the usage limit recorded", func() bool {
				sc, scErr := e.sched.readSidecar(run.ID)
				return scErr == nil && sc.UsageLimit != ""
			})
			sched := e.sched
			switch tc {
			case "exited while the server was down":
				if err = e.sched.Close(); err != nil {
					t.Fatal(err)
				}
				e.rt.byName(string(run.ID)).exitNow(1)
				cfg := e.cfg
				cfg.PTY = newFakePTY()
				cfg.PTY.(*fakePTY).logDir = e.pty.logDir
				if sched, err = New(cfg); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = sched.Close() })
				sched.UseUsageFallback(seam)
				startScheduler(t, sched)
			case "no fallback":
				if err = e.db.RevokeAccountShare(t.Context(), e.owner.ID, e.member.ID); err != nil {
					t.Fatal(err)
				}
				e.rt.byName(string(run.ID)).exitNow(1)
				got := e.waitStoreStatus(t, run.ID, domain.RunFailed)
				if got.Reason != "Usage limit reached on Ada's account; no fallback has usage left" {
					t.Fatalf("reason %q", got.Reason)
				}
				waitFor(t, "the acted-on usage limit cleared", func() bool {
					sc, scErr := e.sched.readSidecar(run.ID)
					return os.IsNotExist(scErr) || (scErr == nil && sc.UsageLimit == "")
				})
				return
			case "new container fails":
				e.rt.mu.Lock()
				e.rt.createErr = errors.New("no space left on device")
				e.rt.mu.Unlock()
				e.rt.byName(string(run.ID)).exitNow(1)
				got := e.waitStoreStatus(t, run.ID, domain.RunFailed)
				if got.Reason != "Usage limit reached on Ada's account; continuing on Grace's account failed: create container: no space left on device" {
					t.Fatalf("reason %q", got.Reason)
				}
				if segments, segErr := e.db.ListRunAccountSegments(t.Context(), run.ID); segErr != nil || len(segments) != 2 {
					t.Fatalf("segments = %+v, %v; want one move", segments, segErr)
				}
				return
			default:
				e.rt.byName(string(run.ID)).exitNow(1)
			}

			spec := e.assertMovedToOwner(t, run.ID, notes)
			want := []string{"claude", "-p", "--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions", "--resume", "sess-1", continuedOnGrace}
			if !slices.Equal(spec.Command, want) {
				t.Fatalf("command = %q, want %q", spec.Command, want)
			}
			row, err := e.db.GetRun(t.Context(), run.ID)
			if err != nil || row.Status != domain.RunRunning || strings.HasPrefix(row.Reason, exitedFailedReasonPrefix) {
				t.Fatalf("run = %+v, %v; want it running", row, err)
			}
			if sc, err := sched.readSidecar(run.ID); err != nil || sc.ExitObserved || sc.UsageLimit != "" || sc.AccountSwap != nil {
				t.Fatalf("sidecar = %+v, %v; want the new container's, with no exit or limit of the old one", sc, err)
			}
			if locked.Load() {
				t.Fatal("the move created its container under the authorization mutex")
			}
		})
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

// A move records the run on Grace's account before it replaces the
// container, and a server can stop anywhere after. With the move's own
// container recorded, the restart resumes the session on Grace's account and
// sends the continuation it was owed. With no container recorded, or with
// Ada's container still there although the run is on Grace's account, the
// restart destroys the container and interrupts the run rather than run it
// on a login it does not record.
func TestRestartSettlesAnInterruptedAccountMove(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		container string
	}{{"new container recorded", "new"}, {"no container recorded", "none"}, {"old container on a moved run", "old"}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e, rt, _ := newFallbackEnv(t, enhancedClaude)
			run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "", "claude", domain.LaunchACP)
			if err != nil {
				t.Fatal(err)
			}
			waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })
			if err = e.sched.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err = e.db.SetRunAccount(t.Context(), run.ID, e.owner.ID, usageLimitSegment, time.Now()); err != nil {
				t.Fatal(err)
			}
			sc, err := e.sched.readSidecar(run.ID)
			if err != nil {
				t.Fatal(err)
			}
			sc.AccountSwap = &accountSwap{From: e.member.ID, To: e.owner.ID, Continuation: continuedOnGrace}
			switch tc.container {
			case "new":
				sc.AccountSwap.Container = sc.ContainerID
			case "none":
				sc.ContainerID = ""
			}
			if err = e.sched.writeSidecar(sc); err != nil {
				t.Fatal(err)
			}
			before := len(rt.all())
			cfg := e.cfg
			cfg.PTY = newFakePTY()
			cfg.PTY.(*fakePTY).logDir = e.pty.logDir
			s2, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s2.Close() })
			startScheduler(t, s2)

			if tc.container != "new" {
				e.waitStoreStatus(t, run.ID, domain.RunInterrupted)
				if e.containerCount() != 0 || len(rt.all()) != before {
					t.Fatalf("%d containers and %d new agents after the restart, want none", e.containerCount(), len(rt.all())-before)
				}
				return
			}
			waitFor(t, "the continuation the move owed", func() bool {
				execs := rt.all()
				if len(execs) != before+1 {
					return false
				}
				prompts := execs[before].wire.prompts(t)
				return len(prompts) == 1 && prompts[0][0].Text != nil && prompts[0][0].Text.Text == continuedOnGrace
			})
			row, err := e.db.GetRun(t.Context(), run.ID)
			if err != nil || row.AccountMemberID != e.owner.ID || row.Status.Terminal() {
				t.Fatalf("run = %+v, %v; want it live on Grace's account", row, err)
			}
			waitFor(t, "the move cleared", func() bool {
				sc, err := s2.readSidecar(run.ID)
				return err == nil && sc.AccountSwap == nil
			})
		})
	}
}

// hookedUsage runs during before the first quota read, which a move makes
// before it takes the authorization mutex.
type hookedUsage struct {
	claudeUsage
	once   sync.Once
	during func()
}

func (u *hookedUsage) Read(ctx context.Context, account domain.MemberID, refresh bool) ([]quota.Provider, error) {
	u.once.Do(u.during)
	return u.claudeUsage.Read(ctx, account, refresh)
}

// What changes while a move reads quota is what it decides on: a handoff to
// Lin, with whom Grace shared nothing, leaves the run where it is; a kill
// stops it there, recorded on the account it ran on.
func TestMoveDecidesOnTheRunAsItIsWhenItRecords(t *testing.T) {
	t.Parallel()
	for _, tc := range []string{"handed off", "killed"} {
		t.Run(tc, func(t *testing.T) {
			t.Parallel()
			e, _, notes := newFallbackEnv(t, enhancedClaude)
			e.share(t)
			lin := e.addRecipient(t)
			run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "", "claude", domain.LaunchACP)
			if err != nil {
				t.Fatal(err)
			}
			waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })
			usage := &hookedUsage{claudeUsage: claudeUsage{e.owner.ID: 10}, during: func() {
				if tc == "killed" {
					if err := e.sched.Kill(context.Background(), run.ID, e.member.ID); err != nil {
						panic(err)
					}
					return
				}
				if err := e.db.TransferRun(context.Background(), run.ID, lin.ID); err != nil {
					panic(err)
				}
			}}
			e.sched.UseUsageFallback(UsageFallback{Usage: usage, AuthorizationMu: &sync.Mutex{}, Note: notes.post})
			_, _ = e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: acpmock.PromptUsageLimit}, false, nil)

			want := domain.RunNeedsAttention
			if tc == "killed" {
				want = domain.RunAbandoned
			}
			got := e.waitStoreStatus(t, run.ID, want)
			if got.AccountMemberID != e.member.ID || len(notes.all()) != 0 {
				t.Fatalf("run = %s on %s (%q), notes %v; want it left on Ada's account", got.Status, got.AccountMemberID, got.Reason, notes.all())
			}
			if segments, err := e.db.ListRunAccountSegments(t.Context(), run.ID); err != nil || len(segments) != 0 {
				t.Fatalf("segments = %+v, %v; want no move recorded", segments, err)
			}
		})
	}
}

// A server that stops while a move reads quota leaves the run as it was:
// not failed, its usage limit kept, so the next server moves it.
func TestShutdownDuringAMoveLeavesTheRunForRecovery(t *testing.T) {
	t.Parallel()
	e, _, notes := newFallbackEnv(t, func(cfg *Config) { delete(cfg.Harnesses, "claude") })
	e.share(t)
	stopped := make(chan struct{})
	usage := &hookedUsage{claudeUsage: claudeUsage{e.owner.ID: 10}, during: func() {
		go func() {
			_ = e.sched.Close()
			close(stopped)
		}()
		<-e.sched.superCtx.Done()
	}}
	e.sched.UseUsageFallback(UsageFallback{Usage: usage, AuthorizationMu: &sync.Mutex{}, Note: notes.post})
	startScheduler(t, e.sched)
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "fix it", "claude", domain.LaunchHeadless)
	if err != nil {
		t.Fatal(err)
	}
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
	<-e.sched.RecoveryReady()
	if _, err = e.bus.Publish(t.Context(), events.Event{WorkspaceID: e.ws.ID, RunID: run.ID, Payload: events.RunUsageLimitPayload{
		Message: "You've hit your session limit · resets 3pm (UTC)", SessionID: "sess-1",
	}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the usage limit recorded", func() bool {
		sc, scErr := e.sched.readSidecar(run.ID)
		return scErr == nil && sc.UsageLimit != ""
	})
	e.rt.byName(string(run.ID)).exitNow(1)
	<-stopped

	row, err := e.db.GetRun(t.Context(), run.ID)
	if err != nil || row.Status != domain.RunRunning {
		t.Fatalf("run after a shutdown mid-move = %+v, %v; want it still running", row, err)
	}
	if sc, scErr := e.sched.readSidecar(run.ID); scErr != nil || sc.UsageLimit == "" {
		t.Fatalf("sidecar = %+v, %v; want the usage limit kept for recovery", sc, scErr)
	}
	cfg := e.cfg
	cfg.PTY = newFakePTY()
	cfg.PTY.(*fakePTY).logDir = e.pty.logDir
	s2, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	s2.UseUsageFallback(UsageFallback{Usage: claudeUsage{e.owner.ID: 10}, AuthorizationMu: &sync.Mutex{}, Note: notes.post})
	startScheduler(t, s2)
	e.assertMovedToOwner(t, run.ID, notes)
}

// A swarm worker whose replacement container cannot be created has no
// container left to retain: it fails at once with the real error and leaves
// the scheduler, so its attempt settles instead of holding a swarm slot.
func TestWorkerWhoseMoveFailsFinishesWithoutRetention(t *testing.T) {
	t.Parallel()
	e, _, notes := newFallbackEnv(t, func(cfg *Config) { delete(cfg.Harnesses, "claude") })
	e.share(t)
	startScheduler(t, e.sched)
	run, err := e.sched.LaunchMission(t.Context(), MissionLaunchSpec{
		WorkspaceID: e.ws.ID, RunID: "moving-worker", Actor: e.member.ID,
		RunOwner: e.member.ID, AccountOwner: e.member.ID, Task: "fix it",
		Harness: "claude", Mode: domain.LaunchHeadless,
	})
	if err != nil {
		t.Fatal(err)
	}
	<-e.sched.RecoveryReady()
	e.rt.mu.Lock()
	e.rt.createErr = errors.New("no space left on device")
	e.rt.mu.Unlock()
	if _, err = e.bus.Publish(t.Context(), events.Event{WorkspaceID: e.ws.ID, RunID: run.ID, Payload: events.RunUsageLimitPayload{
		Message: "You've hit your session limit · resets 3pm (UTC)", SessionID: "sess-1",
	}}); err != nil {
		t.Fatal(err)
	}
	got := e.waitStoreStatus(t, run.ID, domain.RunFailed)
	if got.Reason != "Usage limit reached on Ada's account; continuing on Grace's account failed: create container: no space left on device" {
		t.Fatalf("reason %q", got.Reason)
	}
	waitFor(t, "the worker released", func() bool {
		e.sched.mu.Lock()
		defer e.sched.mu.Unlock()
		return e.sched.runs[run.ID] == nil
	})
	if _, err := e.sched.readSidecar(run.ID); !os.IsNotExist(err) || len(notes.all()) != 0 || e.containerCount() != 0 {
		t.Fatalf("sidecar %v, notes %v, %d containers; want the worker gone without a move", err, notes.all(), e.containerCount())
	}
}

// A move that cannot remove the exhausted container fails the run, which
// keeps owning that container: it may still run with Ada's login mounted.
func TestMoveThatCannotRemoveTheOldContainerKeepsIt(t *testing.T) {
	t.Parallel()
	e, _, notes := newFallbackEnv(t, enhancedClaude)
	e.share(t)
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "", "claude", domain.LaunchACP)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })
	first := e.rt.byName(string(run.ID))
	e.rt.mu.Lock()
	e.rt.destroyErr = errors.New("daemon busy")
	e.rt.mu.Unlock()
	_, _ = e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: acpmock.PromptUsageLimit}, false, nil)

	got := e.waitStoreStatus(t, run.ID, domain.RunFailed)
	if got.Reason != "Usage limit reached on Ada's account; continuing on Grace's account failed: remove the container on Ada's account: daemon busy" {
		t.Fatalf("reason %q", got.Reason)
	}
	waitFor(t, "the old container kept for cleanup", func() bool {
		sc, scErr := e.sched.readSidecar(run.ID)
		return scErr == nil && sc.DestroyPending && sc.ContainerID == string(first.id)
	})
	if e.rt.byName(string(run.ID)) != first || len(notes.all()) != 0 {
		t.Fatal("the old container was let go")
	}
}

// A server that stops while a live run's move reads quota keeps the run's
// usage limit, and the next server moves the run once it has recovered it:
// a swarm worker whose container outlives its agent, a Standard run and an
// Enhanced run.
func TestRestartResumesALiveRunsMove(t *testing.T) {
	t.Parallel()
	for _, tc := range []string{"worker", "standard", "enhanced"} {
		t.Run(tc, func(t *testing.T) {
			t.Parallel()
			mutate := func(cfg *Config) { delete(cfg.Harnesses, "claude") }
			if tc == "enhanced" {
				mutate = enhancedClaude
			}
			e, _, notes := newFallbackEnv(t, mutate)
			e.share(t)
			stopped := make(chan struct{})
			e.sched.UseUsageFallback(UsageFallback{Usage: &hookedUsage{claudeUsage: claudeUsage{e.owner.ID: 10}, during: func() {
				go func() {
					_ = e.sched.Close()
					close(stopped)
				}()
				<-e.sched.superCtx.Done()
			}}, AuthorizationMu: &sync.Mutex{}, Note: notes.post})
			startScheduler(t, e.sched)
			var run *domain.Run
			var err error
			switch tc {
			case "worker":
				run, err = e.sched.LaunchMission(t.Context(), MissionLaunchSpec{
					WorkspaceID: e.ws.ID, RunID: "resumed-worker", Actor: e.member.ID,
					RunOwner: e.member.ID, AccountOwner: e.member.ID, Task: "fix it",
					Harness: "claude", Mode: domain.LaunchHeadless,
				})
			case "standard":
				run, err = e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "fix it", "claude", domain.LaunchTUI)
			default:
				run, err = e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "", "claude", domain.LaunchACP)
			}
			if err != nil {
				t.Fatal(err)
			}
			<-e.sched.RecoveryReady()
			switch tc {
			case "worker":
				_, err = e.bus.Publish(t.Context(), events.Event{WorkspaceID: e.ws.ID, RunID: run.ID, Payload: events.RunUsageLimitPayload{
					Message: "You've hit your session limit · resets 3pm (UTC)", SessionID: "sess-1",
				}})
			case "standard":
				if err = e.sched.ReportAgentState(t.Context(), run.ID, agentstatus.Report{State: agentstatus.Working, SessionID: "sess-1"}); err != nil {
					t.Fatal(err)
				}
				limit, _ := agentstatus.FromClaudeHook([]byte(`{"session_id":"sess-1","hook_event_name":"StopFailure","error":"rate_limit","last_assistant_message":"You've hit your session limit · resets 3pm (UTC)"}`))
				err = e.sched.ReportAgentState(t.Context(), run.ID, limit)
			default:
				waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })
				_, _ = e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: acpmock.PromptUsageLimit}, false, nil)
			}
			if err != nil {
				t.Fatal(err)
			}
			<-stopped

			row, err := e.db.GetRun(t.Context(), run.ID)
			if err != nil || row.Status.Terminal() || row.AccountMemberID != e.member.ID {
				t.Fatalf("run after a shutdown mid-move = %+v, %v; want it live on Ada's account", row, err)
			}
			if sc, scErr := e.sched.readSidecar(run.ID); scErr != nil || sc.UsageLimit == "" {
				t.Fatalf("sidecar = %+v, %v; want the usage limit kept for recovery", sc, scErr)
			}
			cfg := e.cfg
			cfg.PTY = newFakePTY()
			cfg.PTY.(*fakePTY).logDir = e.pty.logDir
			s2, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s2.Close() })
			s2.UseUsageFallback(UsageFallback{Usage: claudeUsage{e.owner.ID: 10}, AuthorizationMu: &sync.Mutex{}, Note: notes.post})
			startScheduler(t, s2)
			e.assertMovedToOwner(t, run.ID, notes)
		})
	}
}
