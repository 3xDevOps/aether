package sshd

import (
	"context"
	"encoding/json"
	"time"

	"github.com/3xDevOps/Aether/internal/cost"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

func init() {
	registerMethod(protocol.MethodCostReport, (*Server).costReport)
	registerMethod(protocol.MethodBudgetGet, (*Server).budgetGet)
	registerGuarded(protocol.MethodBudgetSet, permissions.SessionAdmin, nil, (*Server).budgetSet)
}

// GuardRuns wraps a run controller so a new run is admitted against its
// session's budget before it starts. Runs already running are untouched -
// a budget never stops work in flight. A nil service leaves the
// controller as it is: budgets are a service, not a hard dependency.
func GuardRuns(runs RunController, svc CostService, st store.Store) RunController {
	if svc == nil || st == nil {
		return runs
	}
	return budgetGate{RunController: runs, svc: svc, store: st}
}

// budgetGate is the admission decorator. It adds no method to
// RunController; it re-implements the two that create a run.
type budgetGate struct {
	RunController
	svc   CostService
	store store.Store
}

func (g budgetGate) Launch(ctx context.Context, session domain.SessionID, member domain.MemberID, task, harness string, mode domain.LaunchMode) (*domain.Run, error) {
	if err := g.svc.Admit(ctx, session, member); err != nil {
		return nil, err
	}
	return g.RunController.Launch(ctx, session, member, task, harness, mode)
}

// Relaunch starts a fresh run from a finished one, so it is admitted like
// any other new run.
func (g budgetGate) Relaunch(ctx context.Context, run domain.RunID, actor domain.MemberID) (*domain.Run, error) {
	r, err := g.store.GetRun(ctx, run)
	if err != nil {
		return nil, err
	}
	if err := g.svc.Admit(ctx, r.SessionID, actor); err != nil {
		return nil, err
	}
	return g.RunController.Relaunch(ctx, run, actor)
}

func (s *Server) costs() (CostService, *protocol.Error) {
	if s.cfg.Services.Costs == nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "cost service not configured"}
	}
	return s.cfg.Services.Costs, nil
}

// costSession validates that a params session ID names a real session.
func (s *Server) costSession(ctx context.Context, id string) (domain.SessionID, *protocol.Error) {
	if id == "" {
		return "", invalidParams("session_id is required")
	}
	if _, err := s.cfg.Store.GetSession(ctx, domain.SessionID(id)); err != nil {
		return "", rpcError(err)
	}
	return domain.SessionID(id), nil
}

func (s *Server) costReport(ctx context.Context, _ domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	svc, perr := s.costs()
	if perr != nil {
		return nil, perr
	}
	p, perr := decodeParams[protocol.CostReportParams](params)
	if perr != nil {
		return nil, perr
	}
	session, perr := s.costSession(ctx, p.SessionID)
	if perr != nil {
		return nil, perr
	}
	rep, err := svc.Report(ctx, session)
	if err != nil {
		return nil, rpcError(err)
	}
	out := protocol.CostReportResult{
		SessionID: string(session),
		Total:     rollupWire(rep.Total),
		Members:   make([]protocol.MemberCost, 0, len(rep.Members)),
		Runs:      make([]protocol.RunCost, 0, len(rep.Runs)),
	}
	for _, m := range rep.Members {
		out.Members = append(out.Members, protocol.MemberCost{
			MemberID: string(m.Member),
			Rollup:   rollupWire(m.Rollup),
		})
	}
	for _, c := range rep.Runs {
		out.Runs = append(out.Runs, protocol.RunCost{
			RunID:        string(c.RunID),
			MemberID:     string(c.MemberID),
			InputTokens:  c.InputTokens,
			OutputTokens: c.OutputTokens,
			CostUSD:      c.CostUSD,
			Metered:      c.Metered,
			RecordedAt:   c.RecordedAt.UTC().Format(time.RFC3339),
		})
	}
	return out, nil
}

func (s *Server) budgetGet(ctx context.Context, _ domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	svc, perr := s.costs()
	if perr != nil {
		return nil, perr
	}
	p, perr := decodeParams[protocol.BudgetGetParams](params)
	if perr != nil {
		return nil, perr
	}
	session, perr := s.costSession(ctx, p.SessionID)
	if perr != nil {
		return nil, perr
	}
	st, err := svc.Budget(ctx, session)
	if err != nil {
		return nil, rpcError(err)
	}
	return budgetWire(st), nil
}

func (s *Server) budgetSet(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	svc, perr := s.costs()
	if perr != nil {
		return nil, perr
	}
	p, perr := decodeParams[protocol.BudgetSetParams](params)
	if perr != nil {
		return nil, perr
	}
	session, perr := s.costSession(ctx, p.SessionID)
	if perr != nil {
		return nil, perr
	}
	if p.WarnUSD < 0 || (p.LimitUSD > 0 && p.WarnUSD > p.LimitUSD) {
		return nil, invalidParams("warn_usd must be between 0 and limit_usd")
	}
	st, err := svc.SetBudget(ctx, session, cost.Change{
		LimitUSD: p.LimitUSD,
		WarnUSD:  p.WarnUSD,
		Override: p.Override,
	}, member)
	if err != nil {
		return nil, rpcError(err)
	}
	return budgetWire(st), nil
}

func rollupWire(r cost.Rollup) protocol.CostRollup {
	return protocol.CostRollup{
		Runs:         r.Runs,
		Metered:      r.Metered,
		Unmetered:    r.Unmetered,
		InputTokens:  r.InputTokens,
		OutputTokens: r.OutputTokens,
		CostUSD:      r.CostUSD,
	}
}

func budgetWire(st cost.Status) protocol.BudgetResult {
	out := protocol.BudgetResult{
		SessionID: string(st.Session),
		State:     string(st.State),
		Spend:     rollupWire(st.Spend),
		Advisory:  st.Spend.Advisory(),
	}
	if st.Budget != nil {
		out.Budget = &protocol.Budget{
			SessionID: string(st.Budget.SessionID),
			LimitUSD:  st.Budget.LimitUSD,
			WarnUSD:   st.Budget.WarnUSD,
			Override:  st.Budget.Override,
			UpdatedBy: string(st.Budget.UpdatedBy),
			UpdatedAt: st.Budget.UpdatedAt.UTC().Format(time.RFC3339),
		}
	}
	return out
}
