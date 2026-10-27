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

// waitConditions are run's latest wait conditions. Unless full, what
// their reads found with the owner's GitHub login is left out: the
// observation, summary, third-party detail, read head and read errors. A
// failed read is logged and leaves them out, as a run response never
// fails on them.
func (s *Server) waitConditions(run domain.RunID, full bool) []protocol.WaitCondition {
	svc := s.runWaits()
	if svc == nil {
		return nil
	}
	waits, err := svc.RunWaits(context.Background(), run)
	if err != nil {
		slog.Warn("sshd: read the run's wait conditions", "run", run, "error", err)
		return nil
	}
	if !full {
		for i := range waits {
			w := &waits[i]
			w.Observed, w.Summary, w.Detail, w.LastError, w.LastErrorAt = protocol.WaitObserved{}, "", "", "", ""
			if !strings.Contains(w.Spec, "@") {
				w.HeadSHA = ""
			}
		}
	}
	return waits
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
	return protocol.RunWaitCancelResult{Wait: w}, nil
}
