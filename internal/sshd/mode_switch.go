package sshd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/scheduler"
)

func init() {
	registerGuarded(protocol.MethodRunModeSwitch, permissions.Steer, runTarget, (*Server).runModeSwitch)
}

func (s *Server) runModeSwitch(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.RunModeSwitchParams](raw)
	if perr != nil {
		return nil, perr
	}
	mode := domain.LaunchMode(p.Mode)
	if mode != domain.LaunchTUI && mode != domain.LaunchACP {
		return nil, invalidParams(`mode must be "tui" (Standard) or "acp" (Enhanced)`)
	}
	run := domain.RunID(p.RunID)
	err := s.cfg.Runs.SwitchMode(ctx, run, member, mode, func(begin func() error) error {
		return s.admitControl(ctx, member, run, p.ACPLease, begin)
	})
	if err != nil {
		return nil, modeSwitchError(err)
	}
	fresh, err := s.cfg.Store.GetRun(ctx, run)
	if err != nil {
		return nil, rpcError(err)
	}
	return protocol.RunResult{Run: s.runSnapshot(fresh)}, nil
}

// admitControl runs act with Steer rechecked under the run's admission lock:
// for the lease holder when lease is set, and otherwise only while nobody
// holds the run's control.
func (s *Server) admitControl(ctx context.Context, member domain.MemberID, run domain.RunID, lease protocol.ACPLease, act func() error) error {
	admitted := func() error {
		if err := checkSteer(ctx, s.cfg.Store, member, run); err != nil {
			return err
		}
		return act()
	}
	if s.cfg.Control == nil {
		return admitted()
	}
	if lease.ControlSessionID != "" || lease.ControlGeneration != 0 {
		return s.cfg.Control.AdmitMember(string(run), member, lease.ControlSessionID, lease.ControlGeneration, admitted)
	}
	return s.cfg.Control.AdmitSnapshot(string(run), func(holder control.Snapshot, held bool) error {
		if held {
			return fmt.Errorf("%w: member %s holds the run's control; switch from that session, or take control first", control.ErrOccupied, holder.MemberID)
		}
		return admitted()
	})
}

func modeSwitchError(err error) *protocol.Error {
	reason := ""
	switch {
	case errors.Is(err, scheduler.ErrNotSwitchable):
		reason = protocol.ErrorReasonNotSwitchable
	case errors.Is(err, scheduler.ErrSessionNotReported):
		reason = protocol.ErrorReasonSessionNotReported
	case errors.Is(err, scheduler.ErrAdapterNotInstalled):
		reason = protocol.ErrorReasonAdapterNotInstalled
	}
	if reason != "" {
		data, _ := json.Marshal(map[string]string{"reason": reason})
		return &protocol.Error{Code: protocol.CodeInvalidState, Message: err.Error(), Data: data}
	}
	if errors.Is(err, control.ErrOccupied) {
		return &protocol.Error{Code: protocol.CodeConflict, Message: err.Error()}
	}
	return acpError(err)
}
