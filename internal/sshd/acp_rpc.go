package sshd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/scheduler"
)

// acpOptionTimeout bounds a config option change, which waits for the
// agent's answer while the run's admission lock is held.
const acpOptionTimeout = 15 * time.Second

func init() {
	registerGuarded(protocol.MethodRunInputAnswer, permissions.Steer, runTarget, (*Server).runInputAnswer)
	registerGuarded(protocol.MethodRunACPCancel, permissions.Steer, runTarget, (*Server).runACPCancel)
	registerGuarded(protocol.MethodRunACPSetOption, permissions.Steer, runTarget, (*Server).runACPSetOption)
	registerGuarded(protocol.MethodRunACPHistory, permissions.View, runTarget, (*Server).runACPHistory)
	registerGuarded(protocol.MethodRunACPItem, permissions.View, runTarget, (*Server).runACPItem)
}

func (s *Server) runInputAnswer(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.RunInputAnswerParams](raw)
	if perr != nil {
		return nil, perr
	}
	if p.RequestID == "" || p.OptionID == "" {
		return nil, invalidParams("request_id and option_id are required")
	}
	run, perr := s.acpRun(ctx, p.RunID)
	if perr != nil {
		return nil, perr
	}
	return struct{}{}, s.admitACP(ctx, member, run, p.ACPLease, func() error {
		return s.cfg.Runs.ACPAnswer(run, p.RequestID, p.OptionID, p.Values)
	})
}

func (s *Server) runACPCancel(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.RunACPCancelParams](raw)
	if perr != nil {
		return nil, perr
	}
	run, perr := s.acpRun(ctx, p.RunID)
	if perr != nil {
		return nil, perr
	}
	return struct{}{}, s.admitACP(ctx, member, run, p.ACPLease, func() error {
		return s.cfg.Runs.ACPCancel(ctx, run)
	})
}

func (s *Server) runACPSetOption(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.RunACPSetOptionParams](raw)
	if perr != nil {
		return nil, perr
	}
	var value any
	if err := json.Unmarshal(p.Value, &value); err != nil || p.OptionID == "" {
		return nil, invalidParams("option_id and value are required")
	}
	switch value.(type) {
	case string, bool:
	default:
		return nil, invalidParams("value must be a string or a boolean")
	}
	run, perr := s.acpRun(ctx, p.RunID)
	if perr != nil {
		return nil, perr
	}
	return struct{}{}, s.admitACP(ctx, member, run, p.ACPLease, func() error {
		optionCtx, cancel := context.WithTimeout(ctx, acpOptionTimeout)
		defer cancel()
		return s.cfg.Runs.ACPSetOption(optionCtx, run, p.OptionID, value)
	})
}

func (s *Server) runACPHistory(ctx context.Context, _ domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.RunACPHistoryParams](raw)
	if perr != nil {
		return nil, perr
	}
	if p.Limit <= 0 || p.Limit > protocol.ACPHistoryMaxLimit {
		p.Limit = protocol.ACPHistoryMaxLimit
	}
	run, perr := s.acpRun(ctx, p.RunID)
	if perr != nil {
		return nil, perr
	}
	page, err := s.cfg.Runs.ACPHistory(run, p.BeforeSeq, p.Limit)
	if err != nil {
		return nil, acpError(err)
	}
	frames := make([]protocol.ACPFrame, len(page.Items))
	for i, it := range page.Items {
		b, truncated, err := it.Wire(protocol.ACPWireItemBytes)
		if err != nil {
			return nil, rpcError(err)
		}
		frames[i] = protocol.ACPFrame{Seq: it.Seq, Item: b, Truncated: truncated}
	}
	return protocol.RunACPHistoryResult{Frames: frames, OldestSeq: page.OldestSeq, TruncatedBefore: page.TruncatedBefore}, nil
}

func (s *Server) runACPItem(ctx context.Context, _ domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.RunACPItemParams](raw)
	if perr != nil {
		return nil, perr
	}
	run, perr := s.acpRun(ctx, p.RunID)
	if perr != nil {
		return nil, perr
	}
	it, err := s.cfg.Runs.ACPItem(run, p.Seq)
	if err != nil {
		return nil, acpError(err)
	}
	b, err := json.Marshal(it)
	if err != nil {
		return nil, rpcError(err)
	}
	return protocol.RunACPItemResult{Item: b}, nil
}

func (s *Server) acpRun(ctx context.Context, id string) (domain.RunID, *protocol.Error) {
	run, err := s.cfg.Store.GetRun(ctx, domain.RunID(id))
	if err != nil {
		return "", rpcError(err)
	}
	if !run.ACP {
		return "", invalidParams(fmt.Sprintf("run %s does not run its agent over ACP", id))
	}
	return run.ID, nil
}

// admitACP runs act for the holder of the run's control lease, with Steer
// rechecked under the same admission lock a takeover takes.
func (s *Server) admitACP(ctx context.Context, member domain.MemberID, run domain.RunID, lease protocol.ACPLease, act func() error) *protocol.Error {
	admitted := func() error {
		if err := checkSteer(ctx, s.cfg.Store, member, run); err != nil {
			return err
		}
		return act()
	}
	if s.cfg.Control == nil {
		return acpError(admitted())
	}
	if lease.ControlSessionID == "" || lease.ControlGeneration == 0 {
		return invalidParams("control_session_id and control_generation are required: take the run's control first")
	}
	return acpError(s.cfg.Control.AdmitMember(string(run), member, lease.ControlSessionID, lease.ControlGeneration, admitted))
}

func acpError(err error) *protocol.Error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, acphost.ErrAlreadyAnswered):
		data, _ := json.Marshal(map[string]string{"reason": protocol.ErrorReasonAlreadyAnswered})
		return &protocol.Error{Code: protocol.CodeConflict, Message: err.Error(), Data: data}
	case errors.Is(err, acphost.ErrHistoryExpired):
		return &protocol.Error{Code: protocol.CodeUnavailable, Message: "earlier agent session history has expired"}
	case errors.Is(err, acphost.ErrUnknownRequest), errors.Is(err, scheduler.ErrACPItemNotFound):
		return &protocol.Error{Code: protocol.CodeNotFound, Message: err.Error()}
	case errors.Is(err, acphost.ErrUnknownOption):
		return invalidParams(err.Error())
	case errors.Is(err, scheduler.ErrACPNotRunning), errors.Is(err, acphost.ErrClosed):
		return &protocol.Error{Code: protocol.CodeUnavailable, Message: err.Error()}
	case errors.Is(err, control.ErrStale), errors.Is(err, control.ErrOccupied), errors.Is(err, control.ErrInvalidSession):
		code, message := attachControlError(err)
		return &protocol.Error{Code: code, Message: message}
	}
	return rpcError(err)
}
