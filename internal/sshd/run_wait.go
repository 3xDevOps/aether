package sshd

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/watch"
)

// RunWaitService reads and cancels the conditions runs wait on. Satisfied
// by *watch.Service, the same value as Services.Watches.
type RunWaitService interface {
	RunWaits(ctx context.Context, run domain.RunID) ([]protocol.WaitCondition, error)
	CancelRunWait(ctx context.Context, run domain.RunID, wait string, actor domain.MemberID) (protocol.WaitCondition, error)
}

func init() {
	registerGuarded(protocol.MethodRunWaitCancel, permissions.Steer, runTarget, (*Server).runWaitCancel)
}

func (s *Server) runWaits() RunWaitService {
	w, _ := s.cfg.Services.Watches.(RunWaitService)
	return w
}

// waitConditions are run's latest wait conditions. A failed read is
// logged and leaves them out, as a run response never fails on them.
func (s *Server) waitConditions(run domain.RunID) []protocol.WaitCondition {
	svc := s.runWaits()
	if svc == nil {
		return nil
	}
	waits, err := svc.RunWaits(context.Background(), run)
	if err != nil {
		slog.Warn("sshd: read the run's wait conditions", "run", run, "error", err)
		return nil
	}
	return waits
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
	return protocol.RunWaitCancelResult{Wait: w}, nil
}
