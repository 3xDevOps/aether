package push

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/cost"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
)

type noticeSink struct{ heard chan string }

func (noticeSink) Need(context.Context, *domain.Run, Need) error { return nil }
func (noticeSink) Clear(context.Context, domain.RunID) error     { return nil }
func (s noticeSink) Notice(_ context.Context, member domain.MemberID, n Notice) error {
	s.heard <- fmt.Sprintf("%s hears %s", member, n.Workspace)
	return nil
}

// With the cast - super admin S, admin A owning wsA, admin B owning wsB,
// U granted wsA and N with no grant - a workspace's budget notice reaches
// only its owner and S.
func TestBudgetNoticesReachOnlyTheWorkspaceOwnerAndSuperAdmins(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	if err := h.svc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	heard := noticeSink{heard: make(chan string, 16)}
	h.sink = heard
	h.start()
	cast := map[string]*domain.Member{}
	for name, role := range map[string]domain.Role{"S": domain.RoleSuperAdmin, "A": domain.RoleAdmin, "B": domain.RoleAdmin,
		"U": domain.RoleCollaborator, "N": domain.RoleCollaborator} {
		m := h.member(name)
		m.Role = role
		if err := h.db.UpdateMember(h.ctx, m); err != nil {
			t.Fatalf("UpdateMember: %v", err)
		}
		cast[name] = m
	}
	wsA := h.ws
	if _, err := h.db.SetWorkspaceOwner(h.ctx, wsA.ID, cast["A"].ID, ""); err != nil {
		t.Fatalf("SetWorkspaceOwner: %v", err)
	}
	wsB := &domain.Workspace{Name: "b", BaseBranch: domain.DefaultBaseBranch, OwnerID: cast["B"].ID}
	if err := h.db.CreateWorkspace(h.ctx, wsB); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	if err := h.db.GrantWorkspace(h.ctx, wsA.ID, cast["U"].ID, cast["A"].ID, true); err != nil {
		t.Fatalf("GrantWorkspace: %v", err)
	}

	for _, c := range []struct {
		ws    *domain.Workspace
		owner string
	}{{wsA, "A"}, {wsB, "B"}} {
		run := &domain.Run{WorkspaceID: c.ws.ID, MemberID: cast["U"].ID, Task: "spend", Harness: "claude", Mode: domain.LaunchACP, ACP: true,
			Status: domain.RunRunning}
		if err := h.db.CreateRun(h.ctx, run); err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		if _, err := h.costs.SetBudget(h.ctx, c.ws.ID, cost.Change{LimitUSD: 10, WarnUSD: 8}, cast[c.owner].ID); err != nil {
			t.Fatalf("SetBudget: %v", err)
		}
		h.publish(run, events.RunCostPayload{CostUSD: 8.5, Metered: true})
		want := []string{fmt.Sprintf("%s hears %s", cast[c.owner].ID, c.ws.ID), fmt.Sprintf("%s hears %s", cast["S"].ID, c.ws.ID)}
		var got []string
		for range want {
			select {
			case n := <-heard.heard:
				got = append(got, n)
			case <-time.After(10 * time.Second):
				t.Fatalf("%s budget notices = %q, want %q", c.ws.Name, got, want)
			}
		}
		select {
		case n := <-heard.heard:
			got = append(got, n)
		case <-time.After(testQuiet + 500*time.Millisecond):
		}
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Fatalf("%s budget notices = %q, want only %q", c.ws.Name, got, want)
		}
	}
}
