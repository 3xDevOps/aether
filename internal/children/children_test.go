package children

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/fallback"
	agentharness "github.com/3xDevOps/Aether/internal/harness"
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
	closed   []domain.RunID
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

func (f *fakeRuns) StopForParent(ctx context.Context, run, parent domain.RunID) error {
	f.mu.Lock()
	f.killed = append(f.killed, run)
	f.mu.Unlock()
	now := time.Now()
	_, err := f.db.UpdateRunStatus(ctx, run, domain.RunCompleted, "stopped by parent run "+string(parent), "", nil, &now)
	return err
}

func (f *fakeRuns) WakeRefusal(agent string, mode domain.LaunchMode) error {
	return agentharness.WakeRefusal(agent, mode)
}

func (f *fakeRuns) CloseRun(ctx context.Context, run domain.RunID, _ domain.MemberID, outcome domain.RunStatus) error {
	f.mu.Lock()
	f.closed = append(f.closed, run)
	f.mu.Unlock()
	now := time.Now()
	_, err := f.db.UpdateRunStatus(ctx, run, outcome, "closed", "", nil, &now)
	return err
}

func (f *fakeRuns) Review(ctx context.Context, run domain.RunID, _ domain.MemberID, _ string, _ bool) (*domain.Run, error) {
	if r, err := f.db.GetRun(ctx, run); err == nil && r.Status.Terminal() && r.Status != domain.RunCompleted {
		return nil, errors.New("a final run cannot be marked complete")
	}
	f.mu.Lock()
	f.closed = append(f.closed, run)
	f.mu.Unlock()
	_, _, err := f.db.SetRunReview(ctx, run, domain.ReviewAccepted, "")
	if err != nil {
		return nil, err
	}
	return f.db.GetRun(ctx, run)
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
func (g *fakeGit) FetchRunBranch(context.Context, domain.WorkspaceID, string, domain.RunID, string) (string, error) {
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

func TestOnByDefaultAndOffRefuses(t *testing.T) {
	h := newHarness(t)
	parent := h.run(h.ada.ID, domain.LaunchACP, "fix login")
	settings, _ := h.svc.Settings(h.ctx, h.ada.ID)
	if !settings.Enabled {
		t.Fatalf("default settings = %+v, want child runs on", settings)
	}
	h.mustStart(parent.ID, protocol.ChildStartParams{Harness: "codex", Task: "review"})
	off := false
	h.enable(h.ada.ID, protocol.ChildSettingsParams{Enabled: &off})
	_, err := h.start(parent.ID, protocol.ChildStartParams{Harness: "codex", Task: "again"})
	if code(err) != protocol.CodeDenied || !strings.Contains(err.Error(), "Settings → Child runs") {
		t.Fatalf("start with child runs off = %v, want a denial pointing at Settings", err)
	}
	if bob, _ := h.svc.Settings(h.ctx, h.bob.ID); !bob.Enabled {
		t.Fatal("one member's setting changed another's")
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
	off := false
	h.enable(h.bob.ID, protocol.ChildSettingsParams{Enabled: &off})
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
	counts, _ := h.db.ChildCounts(h.ctx, a.ID)
	if counts.Busy != 2 || counts.Queued != 2 {
		t.Fatalf("counts = %+v, want 2 busy, 2 queued", counts)
	}
	kids, _ := h.db.ListChildren(h.ctx, a.ID)
	if _, err := h.db.UpdateRunStatus(h.ctx, kids[0].RunID, domain.RunNeedsAttention, "agent reported success", domain.ActivityFinished, nil, nil); err != nil {
		t.Fatal(err)
	}
	h.settle()
	counts, _ = h.db.ChildCounts(h.ctx, a.ID)
	if counts.Busy != 2 || counts.Queued != 1 {
		t.Fatalf("after one child reported: %+v, want a queued child started", counts)
	}
	for _, opts := range h.runs.launched {
		if opts.FromCommit != strings.Repeat("a", 40) || opts.ReservedRunID == "" {
			t.Fatalf("launch options = %+v, want the parent's commit and a reserved id", opts)
		}
	}
}

func TestAChildCannotStartChildren(t *testing.T) {
	h := newHarness(t)
	root := h.run(h.ada.ID, domain.LaunchACP, "root")
	child := h.mustStart(root.ID, protocol.ChildStartParams{Harness: "codex"})
	h.settle()
	_, err := h.start(domain.RunID(child.RunID), protocol.ChildStartParams{Harness: "claude", Task: "grandchild"})
	if code(err) != protocol.CodeDenied || !strings.Contains(err.Error(), "child runs go one level deep") {
		t.Fatalf("a grandchild = %v, want the one-level refusal", err)
	}
}

func TestParentsMustWakeOnMail(t *testing.T) {
	h := newHarness(t)
	parent := h.run(h.ada.ID, domain.LaunchTUI, "standard gemini")
	parent.Harness = "gemini"
	if err := h.db.UpdateRun(h.ctx, parent); err != nil {
		t.Fatal(err)
	}
	_, err := h.start(parent.ID, protocol.ChildStartParams{Harness: "codex"})
	if code(err) != protocol.CodeDenied || !strings.Contains(err.Error(), "gemini in Standard mode has no idle wake; start it in Enhanced") {
		t.Fatalf("start from a Standard run that cannot wake = %v", err)
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
	if err := h.db.DetachChild(h.ctx, domain.RunID(kept.RunID)); err != nil {
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
		if m.from == domain.RunID(stopped.RunID) && strings.Contains(m.body, "without a final report") {
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

func TestNoticesSurviveAFailedDelivery(t *testing.T) {
	h := newHarness(t)
	h.enable(h.ada.ID, protocol.ChildSettingsParams{Enabled: on()})
	zero := 1
	if _, err := h.svc.SetLimits(h.ctx, protocol.WorkspaceChildLimitsParams{WorkspaceID: string(h.ws.ID), MaxLive: &zero}); err != nil {
		t.Fatal(err)
	}
	parent := h.run(h.ada.ID, domain.LaunchACP, "parent")
	first := h.mustStart(parent.ID, protocol.ChildStartParams{Harness: "codex", Task: "first"})
	h.settle()
	h.mustStart(parent.ID, protocol.ChildStartParams{Harness: "codex", Task: "second"})
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
		if strings.Contains(m.body, first.RunID) && strings.Contains(m.body, "without a final report") {
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

func TestFinishedChildrenOutliveTheirParent(t *testing.T) {
	h := newHarness(t)
	h.enable(h.ada.ID, protocol.ChildSettingsParams{Enabled: on()})
	parent := h.run(h.ada.ID, domain.LaunchACP, "parent")
	done := h.mustStart(parent.ID, protocol.ChildStartParams{Harness: "codex", Task: "done"})
	busy := h.mustStart(parent.ID, protocol.ChildStartParams{Harness: "codex", Task: "busy"})
	h.settle()
	if _, err := h.db.UpdateRunStatus(h.ctx, domain.RunID(done.RunID), domain.RunNeedsAttention, "agent reported success", domain.ActivityFinished, nil, nil); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if _, err := h.db.UpdateRunStatus(h.ctx, parent.ID, domain.RunFailed, "killed", "", nil, &now); err != nil {
		t.Fatal(err)
	}
	h.settle()
	kept, _ := h.db.GetRun(h.ctx, domain.RunID(done.RunID))
	stopped, _ := h.db.GetRun(h.ctx, domain.RunID(busy.RunID))
	if kept.Status.Terminal() || !kept.Detached || !stopped.Status.Terminal() {
		t.Fatalf("finished child %s detached=%v, busy child %s; want the finished one kept for review and the busy one stopped",
			kept.Status, kept.Detached, stopped.Status)
	}
}

func TestClosedParentClosesItsChildren(t *testing.T) {
	h := newHarness(t)
	h.enable(h.ada.ID, protocol.ChildSettingsParams{Enabled: on()})
	parent := h.run(h.ada.ID, domain.LaunchACP, "parent")
	done := h.mustStart(parent.ID, protocol.ChildStartParams{Harness: "codex", Task: "finished"})
	live := h.mustStart(parent.ID, protocol.ChildStartParams{Harness: "codex", Task: "live"})
	h.settle()
	now := time.Now()
	if _, err := h.db.UpdateRunStatus(h.ctx, domain.RunID(done.RunID), domain.RunCompleted, "agent reported success", "", nil, &now); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.UpdateRunStatus(h.ctx, parent.ID, domain.RunMerged, "closed", "", nil, &now); err != nil {
		t.Fatal(err)
	}
	h.settle()
	h.settle()
	for _, id := range []string{done.RunID, live.RunID} {
		r, _ := h.db.GetRun(h.ctx, domain.RunID(id))
		if r.Status != domain.RunMerged {
			t.Fatalf("child %s = %s, want merged with its parent", id, r.Status)
		}
	}
}

func TestKilledParentKeepsItsFinishedChildrenForReview(t *testing.T) {
	h := newHarness(t)
	h.enable(h.ada.ID, protocol.ChildSettingsParams{Enabled: on()})
	parent := h.run(h.ada.ID, domain.LaunchACP, "parent")
	done := h.mustStart(parent.ID, protocol.ChildStartParams{Harness: "codex", Task: "finished"})
	h.settle()
	now := time.Now()
	if _, err := h.db.UpdateRunStatus(h.ctx, domain.RunID(done.RunID), domain.RunCompleted, "agent reported success", "", nil, &now); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.UpdateRunStatus(h.ctx, parent.ID, domain.RunAbandoned, "killed", "", nil, &now); err != nil {
		t.Fatal(err)
	}
	h.settle()
	h.settle()
	if r, _ := h.db.GetRun(h.ctx, domain.RunID(done.RunID)); r.Status != domain.RunCompleted {
		t.Fatalf("child of a killed parent = %s, want it left completed for review", r.Status)
	}
	if left, err := h.db.ChildrenToSettle(h.ctx); err != nil || len(left) != 0 {
		t.Fatalf("children left to settle = %d, %v", len(left), err)
	}
}

func TestClosedParentSettlesAChildStoppedWhileProvisioning(t *testing.T) {
	h := newHarness(t)
	h.enable(h.ada.ID, protocol.ChildSettingsParams{Enabled: on()})
	parent := h.run(h.ada.ID, domain.LaunchACP, "parent")
	child := h.mustStart(parent.ID, protocol.ChildStartParams{Harness: "codex", Task: "slow"})
	h.settle()
	now := time.Now()
	if _, err := h.db.UpdateRunStatus(h.ctx, domain.RunID(child.RunID), domain.RunAbandoned, "stopped by parent run "+string(parent.ID), "", nil, &now); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.UpdateRunStatus(h.ctx, parent.ID, domain.RunCompleted, "agent reported success", "", nil, &now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.db.SetRunReview(h.ctx, parent.ID, domain.ReviewAccepted, ""); err != nil {
		t.Fatal(err)
	}
	h.settle()
	h.settle()
	if left, err := h.db.ChildrenToSettle(h.ctx); err != nil || len(left) != 0 {
		t.Fatalf("children left to settle = %d, %v; want none", len(left), err)
	}
}

func TestChildChecksOutItsParentsExtraRepositories(t *testing.T) {
	h := newHarness(t)
	parent := h.run(h.ada.ID, domain.LaunchACP, "fix login")
	var extra []domain.WorkspaceID
	for _, name := range []string{"lib", "docs"} {
		w := &domain.Workspace{Name: name, BaseBranch: "main"}
		if err := h.db.CreateWorkspace(h.ctx, w); err != nil {
			t.Fatal(err)
		}
		extra = append(extra, w.ID)
	}
	repos := []domain.RunRepo{
		{WorkspaceID: extra[0], Name: "lib", Path: "/repos/lib", BaseBranch: "main", BaseCommit: "abc"},
		{WorkspaceID: extra[1], Name: "docs", Path: "/repos/docs", BaseBranch: "main", BaseCommit: "abc"},
	}
	if err := h.db.SetRunRepos(h.ctx, parent.ID, repos); err != nil {
		t.Fatal(err)
	}
	h.mustStart(parent.ID, protocol.ChildStartParams{Harness: "codex"})
	h.settle()
	h.runs.mu.Lock()
	defer h.runs.mu.Unlock()
	if len(h.runs.launched) != 1 || !slices.Equal(h.runs.launched[0].ExtraWorkspaces, extra) {
		t.Fatalf("child launch = %+v, want the parent's extra repositories %v", h.runs.launched, extra)
	}
}
