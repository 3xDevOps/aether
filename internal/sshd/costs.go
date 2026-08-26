package sshd

import (
	"context"

	"github.com/3xDevOps/Aether/internal/cost"
	"github.com/3xDevOps/Aether/internal/domain"
)

// CostService is the seam for token rollups and workspace budgets.
// Satisfied by *cost.Service.
type CostService interface {
	// Report rolls a workspace's recorded usage up per run and per member.
	Report(ctx context.Context, workspace domain.WorkspaceID) (cost.Report, error)
	// Budget reports a workspace's budget and where its spend sits.
	Budget(ctx context.Context, workspace domain.WorkspaceID) (cost.Status, error)
	// SetBudget applies an admin's change and returns the new state.
	SetBudget(ctx context.Context, workspace domain.WorkspaceID, c cost.Change, by domain.MemberID) (cost.Status, error)
	// Admit reports whether member may start a new run in workspace,
	// returning a denial wrapping cost.ErrBudgetExceeded at the cap.
	Admit(ctx context.Context, workspace domain.WorkspaceID, member domain.MemberID) error
}
