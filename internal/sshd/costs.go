package sshd

import (
	"context"

	"github.com/3xDevOps/Aether/internal/cost"
	"github.com/3xDevOps/Aether/internal/domain"
)

// CostService is the seam for token rollups and session budgets.
// Satisfied by *cost.Service.
type CostService interface {
	// Report rolls a session's recorded usage up per run and per member.
	Report(ctx context.Context, session domain.SessionID) (cost.Report, error)
	// Budget reports a session's budget and where its spend sits.
	Budget(ctx context.Context, session domain.SessionID) (cost.Status, error)
	// SetBudget applies an admin's change and returns the new state.
	SetBudget(ctx context.Context, session domain.SessionID, c cost.Change, by domain.MemberID) (cost.Status, error)
	// Admit reports whether member may start a new run in session,
	// returning a denial wrapping cost.ErrBudgetExceeded at the cap.
	Admit(ctx context.Context, session domain.SessionID, member domain.MemberID) error
}
