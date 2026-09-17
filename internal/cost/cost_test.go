package cost

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/store"
)

// TestRollUpAttributesPerMemberAndKeepsUnmeteredOut proves the rollup
// math: metered runs add up per member and for the workspace, unmetered
// runs are counted but contribute no numbers, and any of them makes the
// totals advisory.
func TestRollUpAttributesPerMemberAndKeepsUnmeteredOut(t *testing.T) {
	records := []*store.RunCost{
		{RunID: "r1", MemberID: "ada", InputTokens: 1000, OutputTokens: 100, CostUSD: 0.25, Metered: true},
		{RunID: "r2", MemberID: "bob", InputTokens: 500, OutputTokens: 50, CostUSD: 0.10, Metered: true},
		{RunID: "r3", MemberID: "ada", InputTokens: 40, OutputTokens: 4, CostUSD: 0.02, Metered: true},
		// A PTY-only run: never measured, so it adds a run and nothing else.
		{RunID: "r4", MemberID: "bob"},
	}
	rep := Roll("ws1", records)

	if rep.Total.Runs != 4 || rep.Total.Metered != 3 || rep.Total.Unmetered != 1 {
		t.Fatalf("total run counts = %+v, want 4 runs / 3 metered / 1 unmetered", rep.Total)
	}
	if rep.Total.InputTokens != 1540 || rep.Total.OutputTokens != 154 {
		t.Fatalf("total tokens = %d in / %d out, want 1540 / 154", rep.Total.InputTokens, rep.Total.OutputTokens)
	}
	if got := rep.Total.CostUSD; got < 0.3699 || got > 0.3701 {
		t.Fatalf("total cost = %v, want 0.37", got)
	}
	if !rep.Total.Advisory() {
		t.Fatal("a rollup covering an unmetered run is not advisory")
	}

	if len(rep.Members) != 2 || rep.Members[0].Member != domain.MemberID("ada") {
		t.Fatalf("members = %+v, want ada first then bob", rep.Members)
	}
	ada, bob := rep.Members[0].Rollup, rep.Members[1].Rollup
	if ada.Runs != 2 || ada.Unmetered != 0 || ada.InputTokens != 1040 {
		t.Fatalf("ada = %+v, want 2 metered runs and 1040 input tokens", ada)
	}
	if bob.Runs != 2 || bob.Unmetered != 1 || bob.InputTokens != 500 || !bob.Advisory() {
		t.Fatalf("bob = %+v, want 1 metered + 1 unmetered run", bob)
	}

	// An empty workspace is not advisory: nothing is missing.
	if empty := Roll("ws2", nil); empty.Total.Runs != 0 || empty.Total.Advisory() {
		t.Fatalf("empty rollup = %+v, want zero and not advisory", empty.Total)
	}
}

// TestBudgetReflectsCostHistoryAcrossUpdatesAndWorkspaces covers the
// consumer-facing budget result after a metered replacement/replay, an
// unmetered run, deleting a run, clearing the budget, and a second
// workspace's independent spend.
func TestBudgetReflectsCostHistoryAcrossUpdatesAndWorkspaces(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "cost.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	bus, err := events.NewInProc(ctx, nil)
	if err != nil {
		t.Fatalf("NewInProc: %v", err)
	}
	defer func() { _ = bus.Close() }()
	svc, err := New(Config{Store: db, Bus: bus})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	w1 := &domain.Workspace{Name: "one"}
	if err = db.CreateWorkspace(ctx, w1); err != nil {
		t.Fatalf("CreateWorkspace one: %v", err)
	}
	w2 := &domain.Workspace{Name: "two"}
	if err = db.CreateWorkspace(ctx, w2); err != nil {
		t.Fatalf("CreateWorkspace two: %v", err)
	}
	member := &domain.Member{
		DisplayName: "Ada", TailnetLogin: "ada@example", Role: domain.RoleCollaborator,
	}
	if err = db.CreateMember(ctx, member); err != nil {
		t.Fatalf("CreateMember: %v", err)
	}
	newRun := func(workspace domain.WorkspaceID) *domain.Run {
		t.Helper()
		r := &domain.Run{
			WorkspaceID: workspace,
			MemberID:    member.ID,
			Task:        "task",
			Harness:     "claude",
			Mode:        domain.LaunchTUI,
			Status:      domain.RunRunning,
		}
		if createErr := db.CreateRun(ctx, r); createErr != nil {
			t.Fatalf("CreateRun: %v", createErr)
		}
		return r
	}
	r1, r2, r3 := newRun(w1.ID), newRun(w1.ID), newRun(w2.ID)
	put := func(c *store.RunCost) {
		t.Helper()
		if putErr := db.PutRunCost(ctx, c); putErr != nil {
			t.Fatalf("PutRunCost %s: %v", c.RunID, putErr)
		}
	}
	// An unmetered marker is upgraded by the adapter result. Replaying the
	// same metered result must still leave one run in the aggregate.
	put(&store.RunCost{
		RunID: r1.ID, WorkspaceID: w1.ID, MemberID: member.ID,
		InputTokens: 999, OutputTokens: 999, CostUSD: 99,
	})
	put(&store.RunCost{
		RunID: r2.ID, WorkspaceID: w1.ID, MemberID: member.ID,
	})
	put(&store.RunCost{
		RunID: r3.ID, WorkspaceID: w2.ID, MemberID: member.ID,
		InputTokens: 300, OutputTokens: 30, CostUSD: 1.25, Metered: true,
	})
	metered := &store.RunCost{
		RunID: r1.ID, WorkspaceID: w1.ID, MemberID: member.ID,
		InputTokens: 1200, OutputTokens: 340, CostUSD: 8, Metered: true,
	}
	put(metered)
	put(metered)

	got, err := svc.SetBudget(ctx, w1.ID, Change{LimitUSD: 10, WarnUSD: 8}, member.ID)
	if err != nil {
		t.Fatalf("SetBudget: %v", err)
	}
	wantSpend := Rollup{Runs: 2, Metered: 1, Unmetered: 1, InputTokens: 1200, OutputTokens: 340, CostUSD: 8}
	if got.State != events.BudgetWarn || got.Budget == nil || got.Spend != wantSpend {
		t.Fatalf("budget = %+v, want warn with one metered and one unmetered run", got)
	}

	other, err := svc.Budget(ctx, w2.ID)
	if err != nil {
		t.Fatalf("Budget other workspace: %v", err)
	}
	wantOther := Rollup{Runs: 1, Metered: 1, InputTokens: 300, OutputTokens: 30, CostUSD: 1.25}
	if other.Budget != nil || other.State != events.BudgetOK || other.Spend != wantOther {
		t.Fatalf("other workspace budget = %+v, want only its own spend", other)
	}

	// Deleting the unmetered run must not lower the workspace's counted
	// spend or run counts: the deletion folds them into an accumulator the
	// summary still adds in (see store.DeleteRun).
	if err = db.DeleteRun(ctx, r2.ID); err != nil {
		t.Fatalf("DeleteRun: %v", err)
	}
	afterDelete, err := svc.Budget(ctx, w1.ID)
	if err != nil {
		t.Fatalf("Budget after run deletion: %v", err)
	}
	if afterDelete.State != events.BudgetWarn || afterDelete.Spend != wantSpend {
		t.Fatalf("budget after run deletion = %+v, want unchanged from before the delete", afterDelete)
	}

	cleared, err := svc.SetBudget(ctx, w1.ID, Change{}, member.ID)
	if err != nil {
		t.Fatalf("clear budget: %v", err)
	}
	if cleared.Budget != nil || cleared.State != events.BudgetOK || cleared.Spend != wantSpend {
		t.Fatalf("cleared budget = %+v, want no cap with spend preserved", cleared)
	}
}

// TestBudgetStateTransitions walks spend across a budget's thresholds and
// checks both the reported state and whether a new run is admitted.
func TestBudgetStateTransitions(t *testing.T) {
	budget := &store.WorkspaceBudget{WorkspaceID: "ws1", LimitUSD: 10, WarnUSD: 8}
	cases := []struct {
		name    string
		budget  *store.WorkspaceBudget
		spend   float64
		state   events.BudgetState
		admits  bool
		metered bool
	}{
		{name: "no budget", budget: nil, spend: 99, state: events.BudgetOK, admits: true},
		{name: "below warn", budget: budget, spend: 7.99, state: events.BudgetOK, admits: true},
		{name: "at warn", budget: budget, spend: 8, state: events.BudgetWarn, admits: true},
		{name: "below cap", budget: budget, spend: 9.99, state: events.BudgetWarn, admits: true},
		{name: "at cap", budget: budget, spend: 10, state: events.BudgetExceeded, admits: false},
		{name: "past cap", budget: budget, spend: 25, state: events.BudgetExceeded, admits: false},
		{
			name:   "past cap with override",
			budget: &store.WorkspaceBudget{WorkspaceID: "ws1", LimitUSD: 10, WarnUSD: 8, Override: true},
			spend:  25, state: events.BudgetExceeded, admits: true,
		},
		{
			name:   "no warning threshold stays ok until the cap",
			budget: &store.WorkspaceBudget{WorkspaceID: "ws1", LimitUSD: 10},
			spend:  9.99, state: events.BudgetOK, admits: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spend := Rollup{Runs: 1, Metered: 1, CostUSD: tc.spend}
			got := Evaluate(tc.budget, spend)
			if got != tc.state {
				t.Fatalf("state = %q, want %q", got, tc.state)
			}
			st := Status{Workspace: "ws1", Budget: tc.budget, State: got, Spend: spend}
			if st.Admits() != tc.admits {
				t.Fatalf("admits = %v, want %v", st.Admits(), tc.admits)
			}
		})
	}
}

// TestUnmeteredSpendNeverCountsTowardTheCap pins the honesty rule: a cap
// is decided from measured spend only, so unmetered runs cannot push a
// workspace over it - the state is a floor and says so.
func TestUnmeteredSpendNeverCountsTowardTheCap(t *testing.T) {
	budget := &store.WorkspaceBudget{WorkspaceID: "ws1", LimitUSD: 10}
	spend := Roll("ws1", []*store.RunCost{
		{RunID: "r1", MemberID: "ada", CostUSD: 9, Metered: true},
		{RunID: "r2", MemberID: "ada"},
		{RunID: "r3", MemberID: "ada"},
	}).Total
	if state := Evaluate(budget, spend); state != events.BudgetOK {
		t.Fatalf("state = %q, want ok: only measured spend counts", state)
	}
	if !spend.Advisory() {
		t.Fatal("spend with unmetered runs must report itself advisory")
	}
}

// TestDeletedRunSpendStillCountsAgainstTheBudget pins the budget-reopening
// bug: deleting the run whose metered cost pushed a workspace to its cap
// must not reopen that cap, because the workspace's counted spend must
// not drop just because the run that earned it is gone.
func TestDeletedRunSpendStillCountsAgainstTheBudget(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "cost.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	bus, err := events.NewInProc(ctx, nil)
	if err != nil {
		t.Fatalf("NewInProc: %v", err)
	}
	defer func() { _ = bus.Close() }()
	svc, err := New(Config{Store: db, Bus: bus})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	w := &domain.Workspace{Name: "capped"}
	if err = db.CreateWorkspace(ctx, w); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	member := &domain.Member{
		DisplayName: "Ada", TailnetLogin: "ada@example", Role: domain.RoleCollaborator,
	}
	if err = db.CreateMember(ctx, member); err != nil {
		t.Fatalf("CreateMember: %v", err)
	}
	r := &domain.Run{
		WorkspaceID: w.ID, MemberID: member.ID, Task: "task", Harness: "claude",
		Mode: domain.LaunchTUI, Status: domain.RunRunning,
	}
	if err = db.CreateRun(ctx, r); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err = db.PutRunCost(ctx, &store.RunCost{
		RunID: r.ID, WorkspaceID: w.ID, MemberID: member.ID, CostUSD: 10, Metered: true,
	}); err != nil {
		t.Fatalf("PutRunCost: %v", err)
	}
	if _, err = svc.SetBudget(ctx, w.ID, Change{LimitUSD: 10}, member.ID); err != nil {
		t.Fatalf("SetBudget: %v", err)
	}

	if err = svc.Admit(ctx, w.ID, member.ID); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("Admit before delete = %v, want ErrBudgetExceeded", err)
	}

	if err = db.DeleteRun(ctx, r.ID); err != nil {
		t.Fatalf("DeleteRun: %v", err)
	}

	if err = svc.Admit(ctx, w.ID, member.ID); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("Admit after delete = %v, want the cap to remain in force", err)
	}
	status, err := svc.Budget(ctx, w.ID)
	if err != nil {
		t.Fatalf("Budget after delete: %v", err)
	}
	if status.Spend.CostUSD != 10 {
		t.Fatalf("spend after delete = %v, want the deleted run's cost still counted", status.Spend.CostUSD)
	}
}

// TestReportKeepsDeletedRunSpendInTotalsButNotInTheRunList proves the
// per-workspace and per-member breakdown a report shows keeps a deleted
// run's numbers, while the run itself drops out of the per-run listing
// (there is no run left to show).
func TestReportKeepsDeletedRunSpendInTotalsButNotInTheRunList(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "cost.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	bus, err := events.NewInProc(ctx, nil)
	if err != nil {
		t.Fatalf("NewInProc: %v", err)
	}
	defer func() { _ = bus.Close() }()
	svc, err := New(Config{Store: db, Bus: bus})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	w := &domain.Workspace{Name: "team"}
	if err = db.CreateWorkspace(ctx, w); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	ada := &domain.Member{DisplayName: "Ada", TailnetLogin: "ada@example", Role: domain.RoleCollaborator}
	bob := &domain.Member{DisplayName: "Bob", TailnetLogin: "bob@example", Role: domain.RoleCollaborator}
	if err = db.CreateMember(ctx, ada); err != nil {
		t.Fatalf("CreateMember ada: %v", err)
	}
	if err = db.CreateMember(ctx, bob); err != nil {
		t.Fatalf("CreateMember bob: %v", err)
	}
	newRun := func(member domain.MemberID) *domain.Run {
		t.Helper()
		r := &domain.Run{
			WorkspaceID: w.ID, MemberID: member, Task: "task", Harness: "claude",
			Mode: domain.LaunchTUI, Status: domain.RunRunning,
		}
		if createErr := db.CreateRun(ctx, r); createErr != nil {
			t.Fatalf("CreateRun: %v", createErr)
		}
		return r
	}
	adaRun1, adaRun2 := newRun(ada.ID), newRun(bob.ID)
	if err = db.PutRunCost(ctx, &store.RunCost{
		RunID: adaRun1.ID, WorkspaceID: w.ID, MemberID: ada.ID, InputTokens: 10, OutputTokens: 1, CostUSD: 1, Metered: true,
	}); err != nil {
		t.Fatalf("PutRunCost adaRun1: %v", err)
	}
	if err = db.PutRunCost(ctx, &store.RunCost{
		RunID: adaRun2.ID, WorkspaceID: w.ID, MemberID: bob.ID, InputTokens: 20, OutputTokens: 2, CostUSD: 2, Metered: true,
	}); err != nil {
		t.Fatalf("PutRunCost adaRun2: %v", err)
	}

	before, err := svc.Report(ctx, w.ID)
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	wantTotal := Rollup{Runs: 2, Metered: 2, InputTokens: 30, OutputTokens: 3, CostUSD: 3}
	if before.Total != wantTotal || len(before.Runs) != 2 {
		t.Fatalf("report before delete = %+v, want total %+v and 2 runs", before, wantTotal)
	}

	// Ada's own run - the only record of her spend - is deleted.
	if err = db.DeleteRun(ctx, adaRun1.ID); err != nil {
		t.Fatalf("DeleteRun: %v", err)
	}

	after, err := svc.Report(ctx, w.ID)
	if err != nil {
		t.Fatalf("Report after delete: %v", err)
	}
	if after.Total != wantTotal {
		t.Fatalf("report total after delete = %+v, want unchanged %+v", after.Total, wantTotal)
	}
	if len(after.Runs) != 1 || after.Runs[0].RunID != adaRun2.ID {
		t.Fatalf("report runs after delete = %+v, want only bob's surviving run", after.Runs)
	}
	if len(after.Members) != 2 {
		t.Fatalf("report members after delete = %+v, want ada still listed", after.Members)
	}
	var adaRollup, bobRollup Rollup
	for _, m := range after.Members {
		switch m.Member {
		case ada.ID:
			adaRollup = m.Rollup
		case bob.ID:
			bobRollup = m.Rollup
		}
	}
	wantAda := Rollup{Runs: 1, Metered: 1, InputTokens: 10, OutputTokens: 1, CostUSD: 1}
	wantBob := Rollup{Runs: 1, Metered: 1, InputTokens: 20, OutputTokens: 2, CostUSD: 2}
	if adaRollup != wantAda {
		t.Fatalf("ada rollup after delete = %+v, want %+v (her deleted run's spend still counted)", adaRollup, wantAda)
	}
	if bobRollup != wantBob {
		t.Fatalf("bob rollup after delete = %+v, want unaffected %+v", bobRollup, wantBob)
	}
}
