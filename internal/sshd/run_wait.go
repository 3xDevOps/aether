package sshd

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/watch"
)

// RunWaitService reads and cancels the conditions runs wait on. Satisfied
// by *watch.Service, the same value as Services.Watches.
type RunWaitService interface {
	RunWaitsOf(ctx context.Context, runs []domain.RunID) (map[domain.RunID][]protocol.WaitCondition, error)
	CancelRunWait(ctx context.Context, run domain.RunID, wait string, actor domain.MemberID) (protocol.WaitCondition, error)
}

func init() {
	registerGuarded(protocol.MethodRunWaitCancel, permissions.Steer, runTarget, (*Server).runWaitCancel)
}

func (s *Server) runWaits() RunWaitService {
	w, _ := s.cfg.Services.Watches.(RunWaitService)
	return w
}

// fillWaitConditions sets each of runs' latest wait conditions on out, the
// snapshots of runs in the same order, in one read. Unless member owns the
// run or is an admin, they are redacted. A failed read is logged and leaves
// them out, as a run response never fails on them.
func (s *Server) fillWaitConditions(ctx context.Context, member domain.MemberID, runs []*domain.Run, out []protocol.Run) {
	svc := s.runWaits()
	if svc == nil || len(runs) == 0 {
		return
	}
	ids := make([]domain.RunID, len(runs))
	for i, r := range runs {
		ids[i] = r.ID
	}
	byRun, err := svc.RunWaitsOf(ctx, ids)
	if err != nil {
		slog.Warn("sshd: read the runs' wait conditions", "error", err)
		return
	}
	admin := false
	if m, err := s.cfg.Store.GetMember(ctx, member); err == nil {
		admin = m.Role == domain.RoleAdmin
	}
	for i, r := range runs {
		waits := byRun[r.ID]
		if r.MemberID != member && !admin {
			for j := range waits {
				redactWait(&waits[j])
			}
		}
		out[i].WaitConditions = waits
	}
}

// redactWait leaves out of w what only the run's owner and admins read:
// what its reads found with the owner's GitHub login and the free text of
// how it ended and how its wake failed. Its condition, state, outcome and
// a member's cancel stay.
func redactWait(w *protocol.WaitCondition) {
	w.Observed, w.Summary, w.Detail, w.LastError, w.LastErrorAt = protocol.WaitObserved{}, "", "", "", ""
	w.LastDeliveryError = ""
	if w.EndedCause != protocol.WaitCancelled {
		w.EndedCause = ""
	}
	if !strings.Contains(w.Spec, "@") {
		w.HeadSHA = ""
	}
}

// seesWaitDetails reports whether member reads run's wait conditions in
// full: its owner and admins do.
func (s *Server) seesWaitDetails(ctx context.Context, member domain.MemberID, run *domain.Run) bool {
	if member == run.MemberID {
		return true
	}
	m, err := s.cfg.Store.GetMember(ctx, member)
	return err == nil && m.Role == domain.RoleAdmin
}

func (s *Server) runWaitCancel(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	svc := s.runWaits()
	if svc == nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "watch service not configured"}
	}
	p, perr := decodeParams[protocol.RunWaitCancelParams](params)
	if perr != nil {
		return nil, perr
	}
	w, err := svc.CancelRunWait(ctx, domain.RunID(p.RunID), p.WaitID, member)
	if errors.Is(err, watch.ErrInvalid) {
		return nil, invalidParams(err.Error())
	}
	if err != nil {
		return nil, rpcError(err)
	}
	run, err := s.cfg.Store.GetRun(ctx, domain.RunID(p.RunID))
	if err != nil {
		return nil, rpcError(err)
	}
	if !s.seesWaitDetails(ctx, member, run) {
		redactWait(&w)
	}
	return protocol.RunWaitCancelResult{Wait: w}, nil
}
