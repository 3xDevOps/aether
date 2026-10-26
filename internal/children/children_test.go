package children

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/fallback"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

type fakeRuns struct {
	db       *store.DB
	mu       sync.Mutex
	launched []domain.LaunchOptions
	accounts []domain.MemberID
	killed   []domain.RunID
}

func (f *fakeRuns) LaunchWithOptions(ctx context.Context, ws domain.WorkspaceID, member, account domain.MemberID, task, harness string, mode domain.LaunchMode, opts domain.LaunchOptions) (*domain.Run, error) {
	r := &domain.Run{ID: opts.ReservedRunID, WorkspaceID: ws, MemberID: member, AccountMemberID: account, Task: task, Harness: harness,
		Mode: mode, Model: opts.Model, Status: domain.RunRunning, BaseCommit: opts.FromCommit}
	if err := f.db.CreateRunWithID(ctx, r); err != nil {
		return nil, err
	}
	if _, err := f.db.UpdateRunStatus(ctx, r.ID, domain.RunRunning, "", domain.ActivityWorking, nil, nil); err != nil {
		return nil, err
	}
	opts.Created(r.ID)
	opts.Admitted()
	f.mu.Lock()
	f.launched = append(f.launched, opts)
	f.accounts = append(f.accounts, account)
	f.mu.Unlock()
	return r, nil
}

func (f *fakeRuns) Kill(ctx context.Context, run domain.RunID, _ domain.MemberID) error {
	f.mu.Lock()
	f.killed = append(f.killed, run)
	f.mu.Unlock()
	now := time.Now()
	_, err := f.db.UpdateRunStatus(ctx, run, domain.RunFailed, "killed", "", nil, &now)
	return err
}

func (f *fakeRuns) DefaultMode(context.Context, domain.MemberID, domain.MemberID, string) (domain.LaunchMode, error) {
	return domain.LaunchACP, nil
}

func (f *fakeRuns) FallbackLaunchable(context.Context, domain.MemberID, domain.MemberID, domain.MemberID, string, domain.LaunchMode) (bool, error) {
	return true, nil
}

func (f *fakeRuns) AgentModels(context.Context, domain.MemberID, domain.MemberID, string, bool) (protocol.AgentModelsResult, error) {
	return protocol.AgentModelsResult{Models: []protocol.AgentModel{{ID: "gpt-5-codex"}}}, nil
}

type fakeGit struct{ head string }

func (g *fakeGit) PublishRunBranch(context.Context, domain.RunID) (string, error) { return g.head, nil }
func (g *fakeGit) FetchRunBranch(context.Context, domain.RunID, domain.RunID, string) (string, error) {
	return g.head, nil
}

// fakeAgents signs every member in to every agent except those in missing.
type fakeAgents struct{ missing map[domain.MemberID]bool }

func (a fakeAgents) ListAgents(_ context.Context, _, account domain.MemberID) ([]protocol.AgentInfo, error) {
	found := !a.missing[account]
	var out []protocol.AgentInfo
	for _, name := range []string{"claude", "codex"} {
		out = append(out, protocol.AgentInfo{Name: name, Installed: true, EnhancedInstalled: true, LoginFound: &found})
	}
	return out, nil
}

type mail struct {
	from, to domain.RunID
	body     string
}

type fakeMail struct {
	mu   sync.Mutex
	sent []mail
	keys map[string]bool
	fail bool
}

func (m *fakeMail) Notify(_ context.Context, from, to domain.RunID, body, _, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail {
		return errors.New("inbox full")
	}
	if m.keys[key] {
		return nil
	}
	m.keys[key] = true
	m.sent = append(m.sent, mail{from, to, body})
	return nil
}

type harness struct {
	t      *testing.T
	ctx    context.Context
	db     *store.DB
	svc    *Service
	runs   *fakeRuns
	mail   *fakeMail
	agents *fakeAgents
	ws     *domain.Workspace
	ada    *domain.Member
	bob    *domain.Member
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "aether.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	h := &harness{t: t, ctx: ctx, db: db, runs: &fakeRuns{db: db}, mail: &fakeMail{keys: map[string]bool{}}, agents: &fakeAgents{missing: map[domain.MemberID]bool{}}}
	h.ws = &domain.Workspace{Name: "app"}
	if err = db.CreateWorkspace(ctx, h.ws); err != nil {
		t.Fatal(err)
	}
	h.ada, h.bob = h.member("Ada", domain.RoleAdmin), h.member("Bob", domain.RoleCollaborator)
	var mu sync.Mutex
	h.svc, err = New(Config{
		Store: db, Runs: h.runs, Git: &fakeGit{head: strings.Repeat("a", 40)},
		Agents: func() Agents { return h.agents },
		Authorize: func(ctx context.Context, st store.Store, actor domain.MemberID, account string) (domain.MemberID, error) {
			return fallback.ResolveLaunchAccount(ctx, st, actor, account)
		},
		AuthorizationMu: &mu,
	})
	if err != nil {
		t.Fatal(err)
	}
	h.svc.SetMailer(h.mail)
	return h
}

func (h *harness) member(name string, role domain.Role) *domain.Member {
	h.t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		h.t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(pub)
	if err != nil {
		h.t.Fatal(err)
	}
	m := &domain.Member{DisplayName: name, PublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))), Role: role}
	if err := h.db.CreateMember(h.ctx, m); err != nil {
		h.t.Fatal(err)
	}
	return m
}

func (h *harness) run(owner domain.MemberID, mode domain.LaunchMode, task string) *domain.Run {
	h.t.Helper()
	r := &domain.Run{WorkspaceID: h.ws.ID, MemberID: owner, Task: task, Harness: "claude", Mode: mode, Status: domain.RunRunning}
	if err := h.db.CreateRun(h.ctx, r); err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.db.UpdateRunStatus(h.ctx, r.ID, domain.RunRunning, "", domain.ActivityWorking, nil, nil); err != nil {
		h.t.Fatal(err)
	}
	return r
}

func (h *harness) enable(member domain.MemberID, p protocol.ChildSettingsParams) {
	h.t.Helper()
	if _, err := h.svc.SetSettings(h.ctx, member, p); err != nil {
		h.t.Fatal(err)
	}
}

func on() *bool { t := true; return &t }

func (h *harness) start(parent domain.RunID, p protocol.ChildStartParams) (protocol.ChildReceipt, error) {
	if p.Purpose == "" {
		p.Purpose = "adversarial review"
	}
	if p.Task == "" {
		p.Task = "review the last commit"
	}
	if p.IdempotencyKey == "" {
		p.IdempotencyKey = fmt.Sprintf("k-%d", time.Now().UnixNano())
	}
	raw, _ := json.Marshal(p)
	out, err := h.svc.HandleAgent(h.ctx, parent, protocol.MethodChildStart, raw)
	if err != nil {
		return protocol.ChildReceipt{}, err
	}
	return out.(protocol.ChildReceipt), nil
}

func (h *harness) mustStart(parent domain.RunID, p protocol.ChildStartParams) protocol.ChildReceipt {
	h.t.Helper()
	out, err := h.start(parent, p)
	if err != nil {
		h.t.Fatalf("child start: %v", err)
	}
	return out
}

// settle runs reconcile passes until launched goroutines finished.
func (h *harness) settle() {
	h.t.Helper()
	for range 20 {
		h.svc.reconcile(h.ctx)
		launching, err := h.db.ChildrenInState(h.ctx, domain.ChildLaunching)
		if err != nil {
			h.t.Fatal(err)
		}
		if len(launching) == 0 {
			h.svc.reconcile(h.ctx)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.t.Fatal("children still launching")
}

func code(err error) int {
	var pe *protocol.Error
	if errors.As(err, &pe) {
		return pe.Code
	}
	return 0
}

func TestOffByDefaultAndRefusalMarksTheRun(t *testing.T) {
	h := newHarness(t)
	parent := h.run(h.ada.ID, domain.LaunchACP, "fix login")
	_, err := h.start(parent.ID, protocol.ChildStartParams{Harness: "codex"})
	if code(err) != protocol.CodeDenied || !strings.Contains(err.Error(), "Settings → Child runs") {
		t.Fatalf("start with child runs off = %v, want a denial pointing at Settings", err)
	}
	r, _ := h.db.GetRun(h.ctx, parent.ID)
	if !r.ChildRefused {
		t.Fatal("a refused start did not mark the run for the nudge")
	}
	settings, _ := h.svc.Settings(h.ctx, h.ada.ID)
	if settings.Enabled || settings.Configured || !settings.Fallback || settings.SharedWhenMissing || settings.Nudge != protocol.ChildNudgeShow {
		t.Fatalf("default settings = %+v", settings)
	}
}

func TestMemberSettingPersistsAndNudgeSnoozes(t *testing.T) {
	h := newHarness(t)
	for i, want := range []string{protocol.ChildNudgeSnoozed, protocol.ChildNudgeSnoozed, protocol.ChildNudgeNever} {
		got, err := h.svc.SetSettings(h.ctx, h.ada.ID, protocol.ChildSettingsParams{Nudge: protocol.ChildNudgeLater})
		if err != nil || got.Nudge != want || got.Enabled || got.Configured {
			t.Fatalf("later #%d = %+v, %v; want nudge %s, still off and unconfigured", i+1, got, err, want)
		}
	}
	h.enable(h.bob.ID, protocol.ChildSettingsParams{Nudge: protocol.ChildNudgeNever})
	h.enable(h.bob.ID, protocol.ChildSettingsParams{Enabled: on(), SharedWhenMissing: on()})
	got, _ := h.svc.Settings(h.ctx, h.bob.ID)
	if !got.Enabled || !got.Configured || !got.SharedWhenMissing || !got.Fallback {
		t.Fatalf("saved settings = %+v", got)
	}
	ada, _ := h.svc.Settings(h.ctx, h.ada.ID)
	if ada.Enabled {
		t.Fatal("one member's setting enabled another's")
	}
}

func TestRunSettingOverridesOnlyWhileItsSetterOwnsTheRun(t *testing.T) {
	h := newHarness(t)
	parent := h.run(h.ada.ID, domain.LaunchACP, "fix login")
	if _, err := h.svc.Allow(h.ctx, h.bob, parent.ID, domain.ChildrenOn); !errors.Is(err, permissions.ErrDenied) {
		t.Fatalf("a teammate allowing another's run = %v, want denied", err)
	}
	if _, err := h.svc.Allow(h.ctx, h.ada, parent.ID, domain.ChildrenOn); err != nil {
		t.Fatal(err)
	}
	h.mustStart(parent.ID, protocol.ChildStartParams{Harness: "codex"})
	if err := h.db.TransferRun(h.ctx, parent.ID, h.bob.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.start(parent.ID, protocol.ChildStartParams{Harness: "codex"}); code(err) != protocol.CodeDenied {
		t.Fatalf("after a handoff to a member with child runs off = %v, want denied", err)
	}
}

func TestStartIsIdempotentAndQueuesBeyondTheLimit(t *testing.T) {
	h := newHarness(t)
	h.enable(h.ada.ID, protocol.ChildSettingsParams{Enabled: on()})
	two := 2
	if _, err := h.svc.SetLimits(h.ctx, protocol.WorkspaceChildLimitsParams{WorkspaceID: string(h.ws.ID), MaxLive: &two}); err != nil {
		t.Fatal(err)
	}
	a := h.run(h.ada.ID, domain.LaunchACP, "parent a")
	first := h.mustStart(a.ID, protocol.ChildStartParams{Harness: "codex", IdempotencyKey: "review-1"})
	again := h.mustStart(a.ID, protocol.ChildStartParams{Harness: "codex", IdempotencyKey: "review-1"})
	if first.RunID != again.RunID {
		t.Fatalf("retry started %s, first %s", again.RunID, first.RunID)
	}
	if _, err := h.start(a.ID, protocol.ChildStartParams{Harness: "claude", IdempotencyKey: "review-1"}); code(err) != protocol.CodeConflict {
		t.Fatalf("same key, other inputs = %v, want conflict", err)
	}
	for i := range 3 {
		h.mustStart(a.ID, protocol.ChildStartParams{Harness: "codex", Task: fmt.Sprintf("review part %d", i)})
	}
	h.settle()
	counts, _ := h.db.ChildCounts(h.ctx, string(a.ID))
	if counts.Busy != 2 || counts.Queued != 2 || counts.Total != 4 {
		t.Fatalf("counts = %+v, want 2 busy, 2 queued of 4", counts)
	}
	kids, _ := h.db.ListChildren(h.ctx, a.ID, false)
	if _, err := h.db.UpdateRunStatus(h.ctx, kids[0].RunID, domain.RunNeedsAttention, "agent reported success", domain.ActivityFinished, nil, nil); err != nil {
		t.Fatal(err)
	}
	h.settle()
	counts, _ = h.db.ChildCounts(h.ctx, string(a.ID))
	if counts.Busy != 2 || counts.Queued != 1 {
		t.Fatalf("after one child reported: %+v, want a queued child started", counts)
	}
	for _, opts := range h.runs.launched {
		if opts.FromCommit != strings.Repeat("a", 40) || opts.ReservedRunID == "" {
			t.Fatalf("launch options = %+v, want the parent's commit and a reserved id", opts)
		}
	}
}

func TestQueueStartsTheParentWithFewestBusyChildrenFirst(t *testing.T) {
	h := newHarness(t)
	h.enable(h.ada.ID, protocol.ChildSettingsParams{Enabled: on()})
	two := 2
	if _, err := h.svc.SetLimits(h.ctx, protocol.WorkspaceChildLimitsParams{WorkspaceID: string(h.ws.ID), MaxLive: &two}); err != nil {
		t.Fatal(err)
	}
	root := h.run(h.ada.ID, domain.LaunchACP, "root")
	first := h.mustStart(root.ID, protocol.ChildStartParams{Harness: "codex", Task: "child a"})
	h.mustStart(root.ID, protocol.ChildStartParams{Harness: "codex", Task: "child b"})
	h.settle()
	// Child a parks waiting on its own children, which frees its slot; a
	// waiting parent never holds one, so its reviewer cannot deadlock it.
	if _, err := h.db.UpdateRunStatus(h.ctx, domain.RunID(first.RunID), domain.RunNeedsAttention, "waiting for 1 child run", domain.ActivityWaitingAgents, nil, nil); err != nil {
		t.Fatal(err)
	}
	older := h.mustStart(root.ID, protocol.ChildStartParams{Harness: "codex", Task: "child c"})
	grand := h.mustStart(domain.RunID(first.RunID), protocol.ChildStartParams{Harness: "claude", Task: "grandchild"})
	h.settle()
	g, _ := h.db.GetChild(h.ctx, domain.RunID(grand.RunID))
	c, _ := h.db.GetChild(h.ctx, domain.RunID(older.RunID))
	if g.State != domain.ChildStarted || g.Depth != 2 || g.RootRunID != root.ID || g.TreeKey != string(root.ID) || c.State != domain.ChildQueued {
		t.Fatalf("grandchild %s at depth %d in %s, older sibling %s; want the parent with no busy child served first",
			g.State, g.Depth, g.TreeKey, c.State)
	}
}

func TestDepthAndTotalLimitsHoldAcrossNesting(t *testing.T) {
	h := newHarness(t)
	h.enable(h.ada.ID, protocol.ChildSettingsParams{Enabled: on()})
	one, three := 1, 3
	if _, err := h.svc.SetLimits(h.ctx, protocol.WorkspaceChildLimitsParams{WorkspaceID: string(h.ws.ID), MaxDepth: &one, MaxTotal: &three}); err != nil {
		t.Fatal(err)
	}
	root := h.run(h.ada.ID, domain.LaunchACP, "root")
	child := h.mustStart(root.ID, protocol.ChildStartParams{Harness: "codex"})
	h.settle()
	if _, err := h.start(domain.RunID(child.RunID), protocol.ChildStartParams{Harness: "claude"}); code(err) != protocol.CodeDenied || !strings.Contains(err.Error(), "levels deep") {
		t.Fatalf("a grandchild past max depth = %v", err)
	}
	h.mustStart(root.ID, protocol.ChildStartParams{Harness: "codex", Task: "two"})
	h.mustStart(root.ID, protocol.ChildStartParams{Harness: "codex", Task: "three"})
	if _, err := h.start(root.ID, protocol.ChildStartParams{Harness: "codex", Task: "four"}); code(err) != protocol.CodeConflict {
		t.Fatalf("a fourth child past max total = %v, want conflict", err)
	}
	if _, err := h.start(root.ID, protocol.ChildStartParams{Harness: "claude", Task: "root"}); code(err) != protocol.CodeConflict || !strings.Contains(err.Error(), "exact task") {
		t.Fatalf("the parent's own task on its agent = %v, want a loop refusal", err)
	}
}

func TestBackgroundParentsCannotDelegate(t *testing.T) {
	h := newHarness(t)
	h.enable(h.ada.ID, protocol.ChildSettingsParams{Enabled: on()})
	parent := h.run(h.ada.ID, domain.LaunchHeadless, "one turn")
	if _, err := h.start(parent.ID, protocol.ChildStartParams{Harness: "codex"}); code(err) != protocol.CodeDenied || !strings.Contains(err.Error(), "Background") {
		t.Fatalf("start from Background = %v", err)
	}
}

func TestSharedSubscriptionOnlyWhenOwnIsMissingAndShared(t *testing.T) {
	h := newHarness(t)
	h.enable(h.ada.ID, protocol.ChildSettingsParams{Enabled: on()})
	parent := h.run(h.ada.ID, domain.LaunchACP, "fix login")
	if _, err := h.start(parent.ID, protocol.ChildStartParams{Harness: "codex", AccountMemberID: string(h.bob.ID)}); code(err) != protocol.CodeDenied {
		t.Fatalf("a teammate's account without the setting = %v, want denied", err)
	}
	h.agents.missing[h.ada.ID] = true
	if _, err := h.start(parent.ID, protocol.ChildStartParams{Harness: "codex"}); code(err) != protocol.CodeDenied || !strings.Contains(err.Error(), "not signed in") {
		t.Fatalf("no own login and no setting = %v, want not signed in", err)
	}
	h.enable(h.ada.ID, protocol.ChildSettingsParams{SharedWhenMissing: on()})
	if _, err := h.start(parent.ID, protocol.ChildStartParams{Harness: "codex"}); code(err) != protocol.CodeDenied {
		t.Fatalf("setting on but nothing shared = %v, want denied", err)
	}
	if err := h.db.ShareAccount(h.ctx, h.bob.ID, h.ada.ID); err != nil {
		t.Fatal(err)
	}
	got := h.mustStart(parent.ID, protocol.ChildStartParams{Harness: "codex"})
	if got.AccountMemberID != string(h.bob.ID) || got.OwnerID != string(h.ada.ID) {
		t.Fatalf("child = %+v, want Ada's run on Bob's shared account", got)
	}
	if err := h.db.RevokeAccountShare(h.ctx, h.bob.ID, h.ada.ID); err != nil {
		t.Fatal(err)
	}
	h.settle()
	c, _ := h.db.GetChild(h.ctx, domain.RunID(got.RunID))
	if c.State != domain.ChildRefused || len(h.runs.launched) != 0 {
		t.Fatalf("after a revoke the queued child is %s with %d launches, want refused before launch", c.State, len(h.runs.launched))
	}
}

func TestSwarmChildrenUseOnlyTheSwarmsChoices(t *testing.T) {
	h := newHarness(t)
	m := &domain.Mission{
		WorkspaceID: h.ws.ID, Objective: "objective", AccountableHumanID: h.ada.ID, IdempotencyKey: "swarm-1",
		Integrator: domain.MissionIntegrator{AccountMemberID: h.ada.ID, Harness: "claude", Mode: domain.LaunchACP},
		ExecutionChoices: []domain.MissionExecutionChoice{
			{AccountMemberID: h.ada.ID, Harness: "claude", Mode: domain.LaunchACP},
			{AccountMemberID: h.ada.ID, Harness: "codex", Mode: domain.LaunchACP, Model: "gpt-5-codex"},
		},
	}
	if err := h.db.CreateMission(h.ctx, m); err != nil {
		t.Fatalf("mission fixture: %v", err)
	}
	integrator := &domain.Run{ID: m.CurrentIntegratorRunID, WorkspaceID: h.ws.ID, MemberID: h.ada.ID, Task: "objective", Harness: "claude", Mode: domain.LaunchACP, Status: domain.RunRunning}
	if err := h.db.CreateRunWithID(h.ctx, integrator); err != nil {
		t.Fatal(err)
	}
	got := h.mustStart(integrator.ID, protocol.ChildStartParams{Harness: "codex"})
	if got.Model != "gpt-5-codex" {
		t.Fatalf("child model = %q, want the swarm's choice", got.Model)
	}
	if _, err := h.start(integrator.ID, protocol.ChildStartParams{Harness: "codex", Model: "o3"}); code(err) != protocol.CodeDenied {
		t.Fatalf("a model outside the swarm's choices = %v, want denied", err)
	}
	c, _ := h.db.GetChild(h.ctx, domain.RunID(got.RunID))
	if c.TreeKey != "mission:"+string(m.ID) || c.MissionID != m.ID {
		t.Fatalf("swarm child tree = %q mission %q", c.TreeKey, c.MissionID)
	}
}

func TestParentEndCascadesAndChildEndNotifiesOnce(t *testing.T) {
	h := newHarness(t)
	h.enable(h.ada.ID, protocol.ChildSettingsParams{Enabled: on()})
	parent := h.run(h.ada.ID, domain.LaunchACP, "parent")
	kept := h.mustStart(parent.ID, protocol.ChildStartParams{Harness: "codex", Task: "keep"})
	stopped := h.mustStart(parent.ID, protocol.ChildStartParams{Harness: "codex", Task: "stop"})
	h.settle()
	if err := h.svc.ValidateReport(h.ctx, parent.ID, store.CoordOutcomeSuccess); code(err) != protocol.CodeInvalidState {
		t.Fatalf("success with unfinished children = %v, want refused", err)
	}
	if _, err := h.svc.Detach(h.ctx, domain.RunID(kept.RunID)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.HandleAgent(h.ctx, parent.ID, protocol.MethodChildCancel, json.RawMessage(`{"run_id":"`+stopped.RunID+`"}`)); err != nil {
		t.Fatal(err)
	}
	h.settle()
	h.settle()
	if err := h.svc.ValidateReport(h.ctx, parent.ID, store.CoordOutcomeSuccess); err != nil {
		t.Fatalf("success after cancel and detach = %v", err)
	}
	ended := 0
	for _, m := range h.mail.sent {
		if m.from == domain.RunID(stopped.RunID) && strings.Contains(m.body, "without a report") {
			ended++
		}
	}
	if ended != 1 {
		t.Fatalf("parent got %d ended notices, want 1: %+v", ended, h.mail.sent)
	}
	now := time.Now()
	if _, err := h.db.UpdateRunStatus(h.ctx, parent.ID, domain.RunFailed, "killed", "", nil, &now); err != nil {
		t.Fatal(err)
	}
	h.settle()
	k, _ := h.db.GetRun(h.ctx, domain.RunID(kept.RunID))
	if k.Status.Terminal() {
		t.Fatal("a detached child was stopped with its parent")
	}
	attached := h.mustStartAfterEnd(parent.ID)
	if attached {
		t.Fatal("an ended parent started a child")
	}
}

func (h *harness) mustStartAfterEnd(parent domain.RunID) bool {
	_, err := h.start(parent, protocol.ChildStartParams{Harness: "codex", Task: "late"})
	return err == nil
}

func TestTreeUsageCountsEachRunOnceOnThePayingAccount(t *testing.T) {
	h := newHarness(t)
	h.enable(h.ada.ID, protocol.ChildSettingsParams{Enabled: on()})
	parent := h.run(h.ada.ID, domain.LaunchACP, "parent")
	metered := h.mustStart(parent.ID, protocol.ChildStartParams{Harness: "claude", Task: "metered"})
	h.mustStart(parent.ID, protocol.ChildStartParams{Harness: "codex", Task: "unmetered"})
	h.settle()
	for _, c := range []*store.RunCost{
		{RunID: parent.ID, WorkspaceID: h.ws.ID, MemberID: h.ada.ID, CostUSD: 1, InputTokens: 10, Metered: true},
		{RunID: domain.RunID(metered.RunID), WorkspaceID: h.ws.ID, MemberID: h.ada.ID, CostUSD: 0.5, InputTokens: 5, Metered: true},
	} {
		if err := h.db.PutRunCost(h.ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	out, err := h.svc.Children(h.ctx, parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	u := out.Usage
	if u.Runs != 3 || u.MeteredRuns != 2 || u.UnmeteredRuns != 1 || u.CostUSD != 1.5 || u.InputTokens != 15 ||
		len(u.ByAccount) != 1 || u.ByAccount[0].Runs != 2 {
		t.Fatalf("usage = %+v", u)
	}
	for _, c := range out.Children {
		if c.Purpose == "" || (c.RunID == metered.RunID && (c.CostUSD == nil || *c.CostUSD != 0.5)) {
			t.Fatalf("child %+v", c)
		}
	}
}

func TestAdminOffStopsChildrenAndHandoffDetaches(t *testing.T) {
	h := newHarness(t)
	h.enable(h.bob.ID, protocol.ChildSettingsParams{Enabled: on()})
	parent := h.run(h.bob.ID, domain.LaunchACP, "parent")
	if _, err := h.svc.Allow(h.ctx, h.ada, parent.ID, domain.ChildrenOff); err != nil {
		t.Fatal(err)
	}
	if _, err := h.start(parent.ID, protocol.ChildStartParams{Harness: "codex"}); code(err) != protocol.CodeDenied {
		t.Fatalf("start after an admin turned child runs off = %v, want denied", err)
	}
	if _, err := h.svc.Allow(h.ctx, h.bob, parent.ID, ""); err != nil {
		t.Fatal(err)
	}
	child := h.mustStart(parent.ID, protocol.ChildStartParams{Harness: "codex"})
	h.settle()
	if err := h.db.TransferRun(h.ctx, domain.RunID(child.RunID), h.ada.ID); err != nil {
		t.Fatal(err)
	}
	if related, _ := h.svc.Related(h.ctx, parent.ID, domain.RunID(child.RunID)); related {
		t.Fatal("a child handed to another member still messages its old parent outside the usual rules")
	}
	_, err := h.svc.HandleAgent(h.ctx, parent.ID, protocol.MethodChildCancel, json.RawMessage(`{"run_id":"`+child.RunID+`"}`))
	if code(err) != protocol.CodeDenied {
		t.Fatalf("the old parent cancelling a handed-off child = %v, want denied", err)
	}
	if err := h.svc.ValidateReport(h.ctx, parent.ID, store.CoordOutcomeSuccess); err != nil {
		t.Fatalf("a handed-off child still holds its old parent's success: %v", err)
	}
}

func TestSwarmChildrenNeedTheSwarmOwnersRun(t *testing.T) {
	h := newHarness(t)
	m := &domain.Mission{
		WorkspaceID: h.ws.ID, Objective: "objective", AccountableHumanID: h.ada.ID, IdempotencyKey: "swarm-owner",
		Integrator:       domain.MissionIntegrator{AccountMemberID: h.ada.ID, Harness: "claude", Mode: domain.LaunchACP},
		ExecutionChoices: []domain.MissionExecutionChoice{{AccountMemberID: h.ada.ID, Harness: "codex", Mode: domain.LaunchACP}},
	}
	if err := h.db.CreateMission(h.ctx, m); err != nil {
		t.Fatal(err)
	}
	integrator := &domain.Run{ID: m.CurrentIntegratorRunID, WorkspaceID: h.ws.ID, MemberID: h.ada.ID, Task: "objective", Harness: "claude", Mode: domain.LaunchACP, Status: domain.RunRunning}
	if err := h.db.CreateRunWithID(h.ctx, integrator); err != nil {
		t.Fatal(err)
	}
	if err := h.db.TransferRun(h.ctx, integrator.ID, h.bob.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.start(integrator.ID, protocol.ChildStartParams{Harness: "codex"}); code(err) != protocol.CodeDenied {
		t.Fatalf("a swarm run handed to another member starting a child on the swarm's accounts = %v, want denied", err)
	}
}

func TestSettleForgetsEndedTrees(t *testing.T) {
	h := newHarness(t)
	h.enable(h.ada.ID, protocol.ChildSettingsParams{Enabled: on()})
	parent := h.run(h.ada.ID, domain.LaunchACP, "parent")
	h.mustStart(parent.ID, protocol.ChildStartParams{Harness: "codex"})
	h.settle()
	now := time.Now()
	if _, err := h.db.UpdateRunStatus(h.ctx, parent.ID, domain.RunFailed, "killed", "", nil, &now); err != nil {
		t.Fatal(err)
	}
	h.settle()
	h.settle()
	left, err := h.db.ChildrenToSettle(h.ctx)
	if err != nil || len(left) != 0 {
		t.Fatalf("children left to settle = %d, %v; want none once the tree ended", len(left), err)
	}
}

func TestNoticesSurviveAFailedDeliveryAndQueuedChildrenCannotDetach(t *testing.T) {
	h := newHarness(t)
	h.enable(h.ada.ID, protocol.ChildSettingsParams{Enabled: on()})
	zero := 1
	if _, err := h.svc.SetLimits(h.ctx, protocol.WorkspaceChildLimitsParams{WorkspaceID: string(h.ws.ID), MaxLive: &zero}); err != nil {
		t.Fatal(err)
	}
	parent := h.run(h.ada.ID, domain.LaunchACP, "parent")
	first := h.mustStart(parent.ID, protocol.ChildStartParams{Harness: "codex", Task: "first"})
	h.settle()
	queued := h.mustStart(parent.ID, protocol.ChildStartParams{Harness: "codex", Task: "second"})
	if _, err := h.svc.Detach(h.ctx, domain.RunID(queued.RunID)); code(err) != protocol.CodeInvalidState {
		t.Fatalf("detaching a queued child = %v, want invalid state", err)
	}
	h.mail.fail = true
	now := time.Now()
	if _, err := h.db.UpdateRunStatus(h.ctx, domain.RunID(first.RunID), domain.RunFailed, "agent exited 1", "", nil, &now); err != nil {
		t.Fatal(err)
	}
	h.settle()
	h.mail.fail = false
	h.settle()
	notices := 0
	for _, m := range h.mail.sent {
		if strings.Contains(m.body, first.RunID) && strings.Contains(m.body, "without a report") {
			notices++
		}
	}
	if notices != 1 {
		t.Fatalf("parent got %d ended notices after a failed delivery, want 1", notices)
	}
}

func TestRefusalNoticeComesFromTheParent(t *testing.T) {
	h := newHarness(t)
	h.enable(h.ada.ID, protocol.ChildSettingsParams{Enabled: on()})
	parent := h.run(h.ada.ID, domain.LaunchACP, "parent")
	h.mustStart(parent.ID, protocol.ChildStartParams{Harness: "codex"})
	off := false
	h.enable(h.ada.ID, protocol.ChildSettingsParams{Enabled: &off})
	h.settle()
	if len(h.mail.sent) != 1 || h.mail.sent[0].from != parent.ID || !strings.Contains(h.mail.sent[0].body, "did not start") {
		t.Fatalf("refusal notices = %+v, want one from the parent, which exists", h.mail.sent)
	}
}
