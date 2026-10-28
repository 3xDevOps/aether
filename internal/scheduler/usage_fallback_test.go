package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
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
	"github.com/3xDevOps/Aether/internal/fallback"
	"github.com/3xDevOps/Aether/internal/quota"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/secretstore"
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

// codexUsage reports every account's Codex windows as unused.
type codexUsage struct{}

func (codexUsage) Read(context.Context, domain.MemberID, bool) ([]quota.Provider, error) {
	resets := time.Now().Add(time.Hour)
	return []quota.Provider{{Provider: "codex", Status: "ok", Windows: []quota.Window{{ID: "primary", ResetsAt: &resets}}}}, nil
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

// unreadableUsage fails every quota read, as a vendor read that timed out
// does.
type unreadableUsage struct{}

func (unreadableUsage) Read(context.Context, domain.MemberID, bool) ([]quota.Provider, error) {
	return nil, context.DeadlineExceeded
}

// An Enhanced run that hits Ada's usage limit continues on Grace's shared
// account even when Grace's quota cannot be read: unknown usage is not
// exhausted usage.
func TestEnhancedRunContinuesOnAnAccountWithUnknownUsage(t *testing.T) {
	t.Parallel()
	e, _, notes := newFallbackEnv(t, enhancedClaude)
	e.sched.UseUsageFallback(UsageFallback{Usage: unreadableUsage{}, AuthorizationMu: &sync.Mutex{}, Note: notes.post})
	e.share(t)
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "", "claude", domain.LaunchACP)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })
	_, _ = e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: acpmock.PromptUsageLimit}, false, nil)
	e.assertMovedToOwner(t, run.ID, notes)
}

// A run on Grace's shared account whose usage cannot be read is moved to
// Ada's own account only when Ada can sign the agent in there: with no login
// and no key Claude reads it stays; with a workspace ANTHROPIC_API_KEY, or a
// login another member could not borrow because it has a second hard link or
// is reached through a relative symlink, the session continues there; an
// empty login file or a dangling symlink is no login.
func TestUnknownUsageOwnAccountNeedsALoginOrKey(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		secrets  map[string]string
		login    string
		hardLink bool
		symlink  string
		moves    bool
	}{
		{name: "signed out", moves: false},
		{name: "workspace API key", secrets: map[string]string{"ANTHROPIC_API_KEY": "sk-test"}, moves: true},
		{name: "a login with another hard link", login: "{}", hardLink: true, moves: true},
		{name: "an empty login with another hard link", login: "", hardLink: true, moves: false},
		{name: "a login behind a relative symlink", symlink: "login.json", moves: true},
		{name: "a dangling symlink at the login path", symlink: "gone", moves: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e, rt, notes := newFallbackEnv(t, enhancedClaude)
			e.sched.UseUsageFallback(UsageFallback{Usage: unreadableUsage{}, AuthorizationMu: &sync.Mutex{}, Note: notes.post})
			// An empty workspace value masks a key the test process exports.
			secrets := map[string]string{"ANTHROPIC_API_KEY": ""}
			maps.Copy(secrets, tc.secrets)
			e.sched.cfg.Secrets = secretstore.New(t.TempDir())
			if err := e.sched.cfg.Secrets.Put(e.ws.ID, secrets); err != nil {
				t.Fatal(err)
			}
			if tc.hardLink {
				writeHomeFiles(t, e.adaHome, ".claude/.credentials.json")
				if err := os.WriteFile(filepath.Join(e.adaHome, ".claude", ".credentials.json"), []byte(tc.login), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(filepath.Join(e.adaHome, ".claude", ".credentials.json"), filepath.Join(e.adaHome, "credentials-copy")); err != nil {
					t.Fatal(err)
				}
			}
			e.share(t)
			run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.owner.ID, "", "claude", domain.LaunchACP)
			if err != nil {
				t.Fatal(err)
			}
			waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })
			if tc.symlink != "" {
				dir := filepath.Join(e.adaHome, ".claude")
				if err = os.WriteFile(filepath.Join(dir, "login.json"), []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err = os.Remove(filepath.Join(dir, ".credentials.json")); err != nil {
					t.Fatal(err)
				}
				if err = os.Symlink(tc.symlink, filepath.Join(dir, ".credentials.json")); err != nil {
					t.Fatal(err)
				}
			}
			_, _ = e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: acpmock.PromptUsageLimit}, false, nil)
			if tc.moves {
				continued := continuationPrompt("Grace", "Ada")
				waitFor(t, "the continuation prompt on Ada's account", func() bool {
					execs := rt.all()
					prompts := execs[len(execs)-1].wire.prompts(t)
					return len(execs) == 2 && len(prompts) == 1 && prompts[0][0].Text != nil && prompts[0][0].Text.Text == continued
				})
				row, getErr := e.db.GetRun(t.Context(), run.ID)
				c := e.rt.byName(string(run.ID))
				if getErr != nil || row.AccountMemberID != e.member.ID || row.Status.Terminal() || c == nil ||
					c.spec.Env["AETHER_ACCOUNT_MEMBER_ID"] != string(e.member.ID) || c.spec.Env["ANTHROPIC_API_KEY"] != tc.secrets["ANTHROPIC_API_KEY"] {
					t.Fatalf("moved run = %+v, %v; replacement %+v; want it live on Ada's account with the workspace key", row, getErr, c)
				}
				return
			}
			waitFor(t, "the usage-limit reason", func() bool {
				row, getErr := e.db.GetRun(t.Context(), run.ID)
				return getErr == nil && strings.HasPrefix(row.Reason, "Usage limit reached")
			})
			got, err := e.db.GetRun(t.Context(), run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Reason != "Usage limit reached on Grace's account; no account that may have usage left can run claude here" ||
				got.AccountMemberID != e.owner.ID || len(notes.all()) != 0 {
				t.Fatalf("run = reason %q account %s notes %v; want it kept on Grace's account", got.Reason, got.AccountMemberID, notes.all())
			}
		})
	}
}

// Codex signs in from neither OPENAI_API_KEY nor, outside Background runs,
// CODEX_API_KEY, so neither makes Ada's signed-out own account a fallback
// for an Enhanced Codex run whose usage cannot be read.
func TestUnknownUsageCodexIgnoresKeysItDoesNotSignInWith(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"OPENAI_API_KEY", "CODEX_API_KEY"} {
		t.Run(key, func(t *testing.T) {
			t.Parallel()
			e, _, notes := newFallbackEnv(t, func(cfg *Config) {
				cfg.Harnesses["codex"] = HarnessSpec{HeadlessArgs: []string{"fake-agent", "-p", "{task}"}, ACPArgs: []string{"acp-mock"}}
			})
			writeHomeFiles(t, e.ownerHome, ".codex/auth.json")
			e.sched.UseUsageFallback(UsageFallback{Usage: unreadableUsage{}, AuthorizationMu: &sync.Mutex{}, Note: notes.post})
			e.sched.cfg.Secrets = secretstore.New(t.TempDir())
			if err := e.sched.cfg.Secrets.Put(e.ws.ID, map[string]string{key: "sk-test"}); err != nil {
				t.Fatal(err)
			}
			e.share(t)
			run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.owner.ID, "", "codex", domain.LaunchACP)
			if err != nil {
				t.Fatal(err)
			}
			waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })
			_, _ = e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: acpmock.PromptUsageLimit}, false, nil)
			waitFor(t, "the usage-limit reason", func() bool {
				row, getErr := e.db.GetRun(t.Context(), run.ID)
				return getErr == nil && strings.HasPrefix(row.Reason, "Usage limit reached")
			})
			got, err := e.db.GetRun(t.Context(), run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.AccountMemberID != e.owner.ID || len(notes.all()) != 0 {
				t.Fatalf("run = reason %q account %s notes %v; want it kept on Grace's account", got.Reason, got.AccountMemberID, notes.all())
			}
		})
	}
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
		{"no account qualifies", "Usage limit reached on Ada's account; no other account is shared to fall back on", nil, domain.LaunchOptions{}},
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
			// The launch's wait and at most the move's one probe: a live
			// container keeps its single wait.
			time.Sleep(50 * time.Millisecond)
			e.rt.mu.Lock()
			waits := e.rt.waitCalls
			e.rt.mu.Unlock()
			if waits > 2 {
				t.Fatalf("%d waits on the live container, want its one wait and at most one probe", waits)
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
	if got.Reason != "Usage limit reached on Ada's account; no account that may have usage left can run claude here" || len(notes.all()) != 0 {
		t.Fatalf("handed-off run = reason %q, notes %v; want it kept off Grace's account", got.Reason, notes.all())
	}
}

// A container that exits while the move waits to choose an account is
// finalized when the run stays where it is, not left live with nothing
// supervising it.
func TestRunThatExitsDuringAFailedMoveIsFinalized(t *testing.T) {
	t.Parallel()
	testExitDuringFailedMove(t, false)
}

// The move's probe of the exited container fails with a transient runtime
// error. The wait that yielded to the move is gone, so supervision restarts
// and observes the exit itself.
func TestRunThatExitsDuringAFailedMoveIsFinalizedAfterAProbeError(t *testing.T) {
	t.Parallel()
	testExitDuringFailedMove(t, true)
}

func testExitDuringFailedMove(t *testing.T, probeFails bool) {
	t.Helper()
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
	yielded := func() bool {
		e.sched.mu.Lock()
		defer e.sched.mu.Unlock()
		return e.sched.runs[run.ID] != nil && e.sched.runs[run.ID].waitYielded
	}
	waitFor(t, "the wait yielding to the move", yielded)
	if probeFails {
		e.rt.setWaitError(errors.New("runtime unavailable"))
	}
	mu.Unlock()
	if probeFails {
		waitFor(t, "supervision restarted after the failed probe", func() bool { return !yielded() })
		if got, err := e.db.GetRun(t.Context(), run.ID); err != nil || got.Status.Terminal() {
			t.Fatalf("run = %+v, %v; want it live until the runtime answers", got, err)
		}
		e.rt.setWaitError(nil)
	}
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
				if got.Reason != "Usage limit reached on Ada's account; no other account is shared to fall back on" {
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

// A usage-limit report the replacement's agent sends while the move is still
// finishing is followed once the move is done: no account is left, so the
// run stops with that reason instead of staying on an exhausted account.
func TestReplacementLimitReportedDuringAMoveIsFollowed(t *testing.T) {
	t.Parallel()
	e, _, notes := newFallbackEnv(t, func(cfg *Config) { delete(cfg.Harnesses, "claude") })
	e.share(t)
	limit, _ := agentstatus.FromClaudeHook([]byte(`{"session_id":"sess-1","hook_event_name":"StopFailure","error":"rate_limit","last_assistant_message":"You've hit your session limit · resets 3pm (UTC)"}`))
	var run *domain.Run
	var starts atomic.Int32
	launched := make(chan struct{})
	e.rt.mu.Lock()
	e.rt.startHook = func(c *fakeContainer) {
		<-launched
		if c.spec.Name == string(run.ID) && starts.Add(1) == 2 {
			if err := e.sched.ReportAgentState(context.Background(), run.ID, limit); err != nil {
				panic(err)
			}
		}
	}
	e.rt.mu.Unlock()
	var err error
	run, err = e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "fix it", "claude", domain.LaunchTUI)
	if err != nil {
		t.Fatal(err)
	}
	close(launched)
	if err = e.sched.ReportAgentState(t.Context(), run.ID, agentstatus.Report{State: agentstatus.Working, SessionID: "sess-1"}); err != nil {
		t.Fatal(err)
	}
	if err = e.sched.ReportAgentState(t.Context(), run.ID, limit); err != nil {
		t.Fatal(err)
	}
	e.assertMovedToOwner(t, run.ID, notes)
	if got := e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention); !strings.HasPrefix(got.Reason, usageLimitReason) {
		t.Fatalf("reason %q, want the usage limit", got.Reason)
	}
}

// While a move holds the run, a limit reported by the container it replaces
// is a duplicate and one reported by the replacement's is kept.
func TestMoveKeepsOnlyTheReplacementsLimit(t *testing.T) {
	t.Parallel()
	e, _, _ := newFallbackEnv(t, func(cfg *Config) { delete(cfg.Harnesses, "claude") })
	e.share(t)
	limit, _ := agentstatus.FromClaudeHook([]byte(`{"session_id":"sess-1","hook_event_name":"StopFailure","error":"rate_limit","last_assistant_message":"You've hit your session limit · resets 3pm (UTC)"}`))
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "fix it", "claude", domain.LaunchTUI)
	if err != nil {
		t.Fatal(err)
	}
	e.sched.mu.Lock()
	entry := e.sched.runs[run.ID]
	entry.moving, entry.moveFrom = true, entry.containerID
	e.sched.mu.Unlock()
	marked := func() string {
		e.sched.mu.Lock()
		defer e.sched.mu.Unlock()
		return entry.usageLimit
	}
	if err = e.sched.ReportAgentState(t.Context(), run.ID, limit); err != nil || marked() != "" {
		t.Fatalf("old container's report: err %v, mark %q; want it ignored", err, marked())
	}
	e.sched.mu.Lock()
	entry.moveFrom = "old"
	e.sched.mu.Unlock()
	limit.SessionID = "sess-2"
	if err = e.sched.ReportAgentState(t.Context(), run.ID, limit); err != nil || marked() != limit.Reason {
		t.Fatalf("replacement's report: err %v, mark %q; want %q", err, marked(), limit.Reason)
	}
	e.sched.mu.Lock()
	entry.moving = false
	session := entry.agentSessionID
	e.sched.mu.Unlock()
	if session != "sess-2" {
		t.Fatalf("session after the replacement's report = %q, want the one it reported", session)
	}
}

// A Background run moved to another account whose agent refuses the run's
// model ends instead of continuing on the agent's default model, as a fresh
// open does.
func TestBackgroundRunMovedToAnAccountThatRefusesItsModelEnds(t *testing.T) {
	t.Parallel()
	e, rt, notes := newFallbackEnv(t, func(cfg *Config) {
		cfg.Harnesses["codex"] = HarnessSpec{HeadlessArgs: []string{"fake-agent", "-p", "{task}"}, ACPArgs: []string{"acp-mock"}}
	})
	e.share(t)
	writeHomeFiles(t, e.ownerHome, ".codex/auth.json")
	e.sched.UseUsageFallback(UsageFallback{Usage: codexUsage{}, AuthorizationMu: &sync.Mutex{}, Note: notes.post})
	e.rt.execHandler = func(id runtime.ID, argv []string) (int, string, error) {
		c, err := e.rt.get(id)
		if err != nil {
			return 1, "", err
		}
		if strings.Join(argv, " ") == "/bin/sh -c kill -USR2 1" {
			c.exitNow(1)
		}
		return 0, "", nil
	}
	var creates atomic.Int32
	e.rt.createHook = func() {
		if creates.Add(1) == 2 {
			// The restored session lists no configOptions, so the model goes
			// through session/set_model, which the mock agent does not
			// implement.
			rt.fixture.Load.Result = json.RawMessage(`{}`)
		}
	}
	run, err := e.sched.LaunchWithOptions(t.Context(), e.ws.ID, e.member.ID, e.member.ID, acpmock.PromptUsageLimit, "codex", domain.LaunchHeadless, domain.LaunchOptions{Model: "anthropic/claude-sonnet-4-5"})
	if err != nil {
		t.Fatal(err)
	}
	got := e.waitStoreStatus(t, run.ID, domain.RunFailed)
	if !strings.Contains(got.Reason, "model not set: ") {
		t.Fatalf("reason %q, want the refused model", got.Reason)
	}
	for _, exec := range rt.all()[1:] {
		if slices.Contains(exec.agent.Methods(), acp.AgentMethodSessionPrompt) {
			t.Fatal("the continuation ran on the agent's default model")
		}
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
	}{{"new container recorded", "new"}, {"no container recorded", "none"}, {"no container and no move marker", "unmarked"}, {"old container on a moved run", "old"}} {
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
			case "unmarked":
				sc.ContainerID, sc.AccountSwap = "", nil
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
// not failed, its usage limit kept, so the next server moves it. That holds
// for the agent's own exit and for a host shutdown's SIGTERM alike.
func TestShutdownDuringAMoveLeavesTheRunForRecovery(t *testing.T) {
	t.Parallel()
	for _, code := range []int{1, 143} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
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
			e.rt.byName(string(run.ID)).exitNow(code)
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
		})
	}
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

// droppingSub delivers one event after reporting that it dropped older ones.
type droppingSub struct{ ch chan events.Event }

func (d droppingSub) Events() <-chan events.Event { return d.ch }
func (droppingSub) Dropped() uint64               { return 1 }
func (droppingSub) Err() error                    { return nil }
func (droppingSub) Close() error                  { return nil }

// recordingBus answers every subscription with a dropping one and records
// the options it was asked for.
type recordingBus struct {
	events.Bus
	mu   sync.Mutex
	opts []events.SubscribeOptions
}

func (b *recordingBus) Subscribe(_ context.Context, opts events.SubscribeOptions) (events.Subscription, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.opts = append(b.opts, opts)
	sub := droppingSub{ch: make(chan events.Event, 1)}
	sub.ch <- events.Event{Seq: 50}
	return sub, nil
}

// A usage-limit stream that drops events before any was handled replays
// from where the log stood when the stream began, not from the next event.
func TestUsageLimitStreamReplaysADropBeforeTheFirstLimit(t *testing.T) {
	t.Parallel()
	bus := &recordingBus{}
	s := &Scheduler{cfg: Config{Bus: bus, EventSeq: func(context.Context) (uint64, error) { return 41, nil }}}
	first, err := bus.Subscribe(t.Context(), events.SubscribeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.watchUsageLimits(ctx, first, 41)
	}()
	waitFor(t, "the resubscription", func() bool {
		bus.mu.Lock()
		defer bus.mu.Unlock()
		return len(bus.opts) >= 2
	})
	cancel()
	<-done
	bus.mu.Lock()
	defer bus.mu.Unlock()
	if got := bus.opts[1]; !got.Replay || got.AfterSeq != 41 {
		t.Fatalf("resubscribed with %+v, want replay after 41", got)
	}
}

// A move owed for a usage limit that another move has since acted on does
// nothing: no claim and no report, so a run that already moved on is left
// alone.
func TestStaleUsageLimitMoveDoesNothing(t *testing.T) {
	t.Parallel()
	e, _, notes := newFallbackEnv(t, func(cfg *Config) { delete(cfg.Harnesses, "claude") })
	e.share(t)
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "fix it", "claude", domain.LaunchTUI)
	if err != nil {
		t.Fatal(err)
	}
	e.sched.mu.Lock()
	entry := e.sched.runs[run.ID]
	e.sched.mu.Unlock()
	e.sched.continueStandard(entry, "You've hit your session limit · resets 3pm (UTC)")
	row, err := e.db.GetRun(t.Context(), run.ID)
	if err != nil || row.Status != domain.RunRunning || row.AccountMemberID != e.member.ID || len(notes.all()) != 0 {
		t.Fatalf("run = %+v, %v, notes %v; want it untouched", row, err, notes.all())
	}
}

// A second usage-limit report while a move holds the run neither reports
// nor clears the limit: a shutdown then leaves it for the next server.
func TestDuplicateLimitDuringAMoveKeepsTheMark(t *testing.T) {
	t.Parallel()
	e, _, notes := newFallbackEnv(t, func(cfg *Config) { delete(cfg.Harnesses, "claude") })
	e.share(t)
	limit, _ := agentstatus.FromClaudeHook([]byte(`{"session_id":"sess-1","hook_event_name":"StopFailure","error":"rate_limit","last_assistant_message":"You've hit your session limit · resets 3pm (UTC)"}`))
	var run *domain.Run
	stopped := make(chan struct{})
	var session atomic.Value
	e.sched.UseUsageFallback(UsageFallback{Usage: &hookedUsage{claudeUsage: claudeUsage{e.owner.ID: 10}, during: func() {
		// A repeat from the exhausted agent, under another session id: it
		// is the old container's, whatever the move has done so far.
		repeat := limit
		repeat.SessionID = "sess-repeat"
		if err := e.sched.ReportAgentState(context.Background(), run.ID, repeat); err != nil {
			panic(err)
		}
		e.sched.mu.Lock()
		session.Store(e.sched.runs[run.ID].agentSessionID)
		e.sched.mu.Unlock()
		go func() {
			_ = e.sched.Close()
			close(stopped)
		}()
		<-e.sched.superCtx.Done()
	}}, AuthorizationMu: &sync.Mutex{}, Note: notes.post})
	startScheduler(t, e.sched)
	var err error
	run, err = e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "fix it", "claude", domain.LaunchTUI)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.sched.ReportAgentState(t.Context(), run.ID, agentstatus.Report{State: agentstatus.Working, SessionID: "sess-1"}); err != nil {
		t.Fatal(err)
	}
	if err = e.sched.ReportAgentState(t.Context(), run.ID, limit); err != nil {
		t.Fatal(err)
	}
	<-stopped
	row, err := e.db.GetRun(t.Context(), run.ID)
	if err != nil || row.Status != domain.RunRunning {
		t.Fatalf("run = %+v, %v; want it still running", row, err)
	}
	if sc, scErr := e.sched.readSidecar(run.ID); scErr != nil || sc.UsageLimit == "" {
		t.Fatalf("sidecar = %+v, %v; want the usage limit kept", sc, scErr)
	}
	if got := session.Load(); got != "sess-1" {
		t.Fatalf("session after the old agent's repeat = %v, want it taken for the old container's and ignored", got)
	}
}

// A swarm worker whose old container cannot be removed fails with that
// error, stays on its account, and keeps the container for cleanup to retry
// rather than for the swarm.
func TestWorkerWhoseOldContainerCannotBeRemovedFails(t *testing.T) {
	t.Parallel()
	e, _, notes := newFallbackEnv(t, func(cfg *Config) { delete(cfg.Harnesses, "claude") })
	e.share(t)
	startScheduler(t, e.sched)
	run, err := e.sched.LaunchMission(t.Context(), MissionLaunchSpec{
		WorkspaceID: e.ws.ID, RunID: "stuck-worker", Actor: e.member.ID,
		RunOwner: e.member.ID, AccountOwner: e.member.ID, Task: "fix it",
		Harness: "claude", Mode: domain.LaunchHeadless,
	})
	if err != nil {
		t.Fatal(err)
	}
	<-e.sched.RecoveryReady()
	first := e.rt.byName(string(run.ID))
	e.rt.mu.Lock()
	e.rt.destroyErr = errors.New("daemon busy")
	e.rt.mu.Unlock()
	if _, err = e.bus.Publish(t.Context(), events.Event{WorkspaceID: e.ws.ID, RunID: run.ID, Payload: events.RunUsageLimitPayload{
		Message: "You've hit your session limit · resets 3pm (UTC)", SessionID: "sess-1",
	}}); err != nil {
		t.Fatal(err)
	}
	got := e.waitStoreStatus(t, run.ID, domain.RunFailed)
	if got.Reason != "Usage limit reached on Ada's account; continuing on Grace's account failed: remove the container on Ada's account: daemon busy" || got.AccountMemberID != e.member.ID {
		t.Fatalf("worker = reason %q account %s", got.Reason, got.AccountMemberID)
	}
	waitFor(t, "the old container kept for cleanup", func() bool {
		sc, scErr := e.sched.readSidecar(run.ID)
		return scErr == nil && sc.DestroyPending && sc.ContainerID == string(first.id)
	})
	if segments, err := e.db.ListRunAccountSegments(t.Context(), run.ID); err != nil || len(segments) != 0 || len(notes.all()) != 0 {
		t.Fatalf("segments %+v, %v, notes %v; want no move recorded", segments, err, notes.all())
	}
}

// failingSub ends at once with an error, as a replay whose log read fails.
type failingSub struct{ ch chan events.Event }

func (f failingSub) Events() <-chan events.Event { return f.ch }
func (failingSub) Dropped() uint64               { return 0 }
func (failingSub) Err() error                    { return errors.New("read the event log: disk I/O error") }
func (failingSub) Close() error                  { return nil }

type failingBus struct {
	events.Bus
	mu    sync.Mutex
	count int
}

func (b *failingBus) Subscribe(context.Context, events.SubscribeOptions) (events.Subscription, error) {
	b.mu.Lock()
	b.count++
	b.mu.Unlock()
	ch := make(chan events.Event)
	close(ch)
	return failingSub{ch: ch}, nil
}

// A usage-limit stream that keeps failing is retried with a growing delay,
// not in a hot loop.
func TestFailingUsageLimitStreamBacksOff(t *testing.T) {
	t.Parallel()
	bus := &failingBus{}
	s := &Scheduler{cfg: Config{Bus: bus, EventSeq: func(context.Context) (uint64, error) { return 7, nil }}}
	first, err := bus.Subscribe(t.Context(), events.SubscribeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	s.watchUsageLimits(ctx, first, 7)
	bus.mu.Lock()
	defer bus.mu.Unlock()
	if bus.count > 6 {
		t.Fatalf("%d subscriptions in half a second; want a growing delay between them", bus.count)
	}
}

// An Enhanced run whose replacement hits the limit before its move is done
// is followed by another move, which supersedes the continuation the first
// one owed: with no account left, nothing later tells the agent that the
// run continues on Grace's account.
func TestFollowUpMoveDropsTheContinuationItSupersedes(t *testing.T) {
	t.Parallel()
	e, rt, _ := newFallbackEnv(t, enhancedClaude)
	e.share(t)
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "", "claude", domain.LaunchACP)
	if err != nil {
		t.Fatal(err)
	}
	// The replacement reports its limit as soon as it is created.
	e.rt.createHook = func() {
		e.sched.mu.Lock()
		e.sched.setUsageLimitLocked(e.sched.runs[run.ID], "You've hit your weekly limit")
		e.sched.mu.Unlock()
	}
	waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })
	_, _ = e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: acpmock.PromptUsageLimit}, false, nil)

	waitFor(t, "the follow-up move's reason", func() bool {
		got, getErr := e.db.GetRun(t.Context(), run.ID)
		return getErr == nil && got.Status == domain.RunNeedsAttention &&
			got.Reason == "Usage limit reached on Grace's account; no fallback has usage left"
	})
	if sc, scErr := e.sched.readSidecar(run.ID); scErr != nil || sc.AccountSwap != nil {
		t.Fatalf("sidecar = %+v, %v; want no continuation left to send", sc, scErr)
	}
	execs := rt.all()
	if prompts := execs[len(execs)-1].wire.prompts(t); len(prompts) != 0 {
		t.Fatalf("the superseded session was prompted: %v", prompts)
	}
}

// A launcher who changed their git email since launch gets a replacement
// that commits under the new address, and the run records that address as
// its own, so it is not also listed as a co-author.
func TestMoveRecordsTheReplacementsGitAuthor(t *testing.T) {
	t.Parallel()
	e, _, notes := newFallbackEnv(t, enhancedClaude)
	e.share(t)
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "", "claude", domain.LaunchACP)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })
	if err = e.db.UpdateMemberGitIdentity(t.Context(), e.member.ID, "Ada Lovelace", "ada@example.com"); err != nil {
		t.Fatal(err)
	}
	_, _ = e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: acpmock.PromptUsageLimit}, false, nil)
	spec := e.assertMovedToOwner(t, run.ID, notes)
	sc, err := e.sched.readSidecar(run.ID)
	if err != nil || spec.Env["GIT_AUTHOR_EMAIL"] != "ada@example.com" || sc.GitAuthorEmail != "ada@example.com" {
		t.Fatalf("replacement commits as %q, run records %q, %v; want both the new address", spec.Env["GIT_AUTHOR_EMAIL"], sc.GitAuthorEmail, err)
	}
}

// A server that stops while the scheduler reads the event log's position
// stops cleanly; a read that fails for another reason still fails Start.
func TestStartStoppedDuringTheEventLogReadIsClean(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		read func(context.Context, context.CancelFunc) (uint64, error)
		want string
	}{
		{"shutdown", func(ctx context.Context, cancel context.CancelFunc) (uint64, error) {
			cancel()
			return 0, fmt.Errorf("events: read last seq: %w", ctx.Err())
		}, ""},
		{"failed read", func(context.Context, context.CancelFunc) (uint64, error) {
			return 0, errors.New("events: read last seq: disk I/O error")
		}, "scheduler: read the event log's position: events: read last seq: disk I/O error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			e := newTestEnv(t, func(cfg *Config) {
				cfg.EventSeq = func(ctx context.Context) (uint64, error) { return tc.read(ctx, cancel) }
			})
			err := e.sched.Start(ctx)
			if got := fmt.Sprint(err); (tc.want == "" && err != nil) || (tc.want != "" && got != tc.want) {
				t.Fatalf("Start = %v, want %q", err, tc.want)
			}
		})
	}
}

// Each stop reason names why no account took the run.
func TestNoFallbackReason(t *testing.T) {
	ada, grace := domain.MemberID("ada"), domain.MemberID("grace")
	for _, tc := range []struct {
		harness  string
		measured fallback.Measured
		want     string
	}{
		{"opencode", fallback.Measured{ada: 100}, "Aether cannot read opencode's usage, so its runs do not move to another account"},
		{"claude", fallback.Measured{ada: 100}, "no other account is shared to fall back on"},
		{"claude", fallback.Measured{ada: 100, grace: 97}, "no fallback has usage left"},
		{"claude", fallback.Measured{ada: 100, grace: fallback.Unknown}, "no account that may have usage left can run claude here"},
	} {
		if got := noFallbackReason("Ada", tc.harness, tc.measured, ada); got != "Usage limit reached on Ada's account; "+tc.want {
			t.Errorf("%s %v: %q, want the reason to end %q", tc.harness, tc.measured, got, tc.want)
		}
	}
}
