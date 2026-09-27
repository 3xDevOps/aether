package sshd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/ptyhost"
)

// serveDevelopmentAttach uses the broker's authoritative registry, including
// terminals first started by an agent. Shell is the stable terminal ID; the
// supplied incarnation fences a reconnect against explicit replacement.
func (s *Server) serveDevelopmentAttach(ctx context.Context, member domain.MemberID, st *sessionState, ch subsystemConn, reader *bufio.Reader, req protocol.AttachRequest) {
	refuse := func(err error) {
		perr := rpcError(err)
		_ = writeJSONLine(ch, protocol.AttachResponse{Code: perr.Code, Error: perr.Message})
	}
	if req.Interactive {
		refuse(invalidParams("development attaches use dev.control methods and reconnect for control changes"))
		return
	}
	run, principal, authorize, authorityErr := s.developmentAuthority(ctx, member, req.RunID)
	if authorityErr != nil {
		refuse(authorityErr)
		return
	}
	if s.cfg.Control == nil {
		refuse(&protocol.Error{Code: protocol.CodeUnavailable, Message: "development control unavailable"})
		return
	}
	cols, rows, hasPTY := st.geometry()
	if cols == 0 || rows == 0 {
		cols, rows = req.Cols, req.Rows
	}
	if cols == 0 || rows == 0 {
		cols, rows = 80, 24
	}
	readOnly := req.ReadOnly || !hasPTY || req.ReleaseControl
	call := func(method string, params any, result any) error {
		raw, err := json.Marshal(params)
		if err != nil {
			return err
		}
		out, err := s.cfg.Services.Development.Call(ctx, *run, principal, method, raw, authorize)
		if err != nil {
			return err
		}
		encoded, err := json.Marshal(out)
		if err != nil {
			return err
		}
		return json.Unmarshal(encoded, result)
	}
	var list protocol.DevTerminalListResult
	if err := call(protocol.MethodDevTerminalList, protocol.DevTerminalListParams{DevRunParams: protocol.DevRunParams{RunID: req.RunID}}, &list); err != nil {
		refuse(err)
		return
	}
	var terminal protocol.DevTerminal
	for _, candidate := range list.Terminals {
		if candidate.TerminalID == req.Shell {
			terminal = candidate
			break
		}
	}
	if terminal.TerminalID == "" && !readOnly && req.Incarnation == "" {
		var started protocol.DevTerminalStartResult
		if err := call(protocol.MethodDevTerminalStart, protocol.DevTerminalStartParams{DevRunParams: protocol.DevRunParams{RunID: req.RunID}, Name: req.Shell, Cols: cols, Rows: rows}, &started); err != nil {
			refuse(err)
			return
		}
		terminal = started.Terminal
	}
	if terminal.TerminalID == "" {
		refuse(&protocol.Error{Code: protocol.CodeNotFound, Message: "development terminal not found"})
		return
	}
	if req.Incarnation != "" && req.Incarnation != terminal.Incarnation {
		refuse(&protocol.Error{Code: protocol.CodeConflict, Message: "development terminal incarnation changed"})
		return
	}
	if terminal.Process.State != "running" {
		refuse(&protocol.Error{Code: protocol.CodeUnavailable, Message: "development terminal is " + terminal.Process.State})
		return
	}
	target := protocol.DevTerminalTarget{DevRunParams: protocol.DevRunParams{RunID: req.RunID}, TerminalID: terminal.TerminalID, Incarnation: terminal.Incarnation}
	surface := control.Surface{Kind: control.SurfaceTerminal, ID: terminal.TerminalID, Incarnation: terminal.Incarnation}
	ack := &protocol.AttachResponse{OK: true, Framed: req.Framed, Cols: terminal.Cols, Rows: terminal.Rows, TerminalID: terminal.TerminalID, Incarnation: terminal.Incarnation}
	var lease control.SurfaceSnapshot
	if req.ReleaseControl {
		if err := s.cfg.Control.ReleaseSurface(req.RunID, surface, principal, req.ControlSessionID, req.ControlGeneration, func() error { return authorize(ctx) }); err != nil {
			refuse(developmentControlError(err))
			return
		}
	}
	if !readOnly {
		var acquireErr error
		lease, _, acquireErr = s.cfg.Control.AcquireSurface(req.RunID, surface, principal, req.ControlSessionID, req.Takeover, req.ControlGeneration, func() error { return authorize(ctx) })
		if acquireErr != nil {
			refuse(developmentControlError(acquireErr))
			return
		}
		defer s.cfg.Control.DisconnectSurface(req.RunID, surface, principal, lease.SessionID, lease.Generation)
		ack.ControlGeneration, ack.HasControl = lease.Generation, true
	}
	if current, present := s.cfg.Control.SurfaceStatus(req.RunID, surface); present {
		ack.ControllerID = string(current.Principal.MemberID)
		if readOnly {
			ack.ControlGeneration = current.Generation
		}
	}
	fence := protocol.DevControlFence{ControlSessionID: lease.SessionID, ControlGeneration: lease.Generation}
	streamAuthorize := func(ctx context.Context) error {
		if err := authorize(ctx); err != nil {
			return err
		}
		if !readOnly {
			current, ok := s.cfg.Control.SurfaceStatus(req.RunID, surface)
			if !ok || !current.Connected || current.Principal != principal || current.SessionID != lease.SessionID || current.Generation != lease.Generation {
				return errAttachControlRevoked
			}
		}
		return nil
	}
	ctx, stop := developmentStreamLifetime(ctx, ch, streamAuthorize)
	defer stop(-1)
	conn := newAttachConn(ch, reader, ack, req.Framed, func() error { return streamAuthorize(ctx) })
	position := req.ResumePosition()
	client := ptyhost.AttachClient{Member: member, Cols: cols, Rows: rows, ReadOnly: readOnly, Screen: req.Screen, Snapshot: req.Framed, Follow: req.Follow, Resume: req.Resume,
		Position:  ptyhost.TerminalPosition{Epoch: ptyhost.TerminalEpoch(position.Epoch), Sequence: ptyhost.TerminalSequence(position.Sequence)},
		Authorize: func() error { return streamAuthorize(ctx) },
	}
	if !readOnly {
		client.Commit = func(admit func() error) error {
			return s.cfg.Control.AdmitSurface(req.RunID, surface, principal, lease.SessionID, lease.Generation, func() error {
				if err := authorize(ctx); err != nil {
					return err
				}
				if err := admit(); err != nil {
					return err
				}
				if mission := s.cfg.Services.MissionControl; mission != nil {
					return mission.Takeover(ctx, run.ID, member)
				}
				return nil
			})
		}
	}
	attachErr := s.cfg.Services.Development.AttachTerminal(ctx, *run, principal, target, fence, client, conn, st.resize, authorize)
	// The lifetime monitor already named authority revocation before closing
	// the channel; do not overwrite that status with context.Canceled.
	if ctx.Err() != nil {
		return
	}
	if attachErr != nil && !conn.okSent() {
		refuse(developmentControlError(attachErr))
		return
	}
	if attachErr == nil || errors.Is(attachErr, io.EOF) {
		ch.exit(0)
	} else {
		ch.exit(attachExitForError(attachErr))
	}
}

func developmentControlError(err error) error {
	if errors.Is(err, control.ErrStale) || errors.Is(err, control.ErrOccupied) || errors.Is(err, control.ErrInvalidSession) || errors.Is(err, control.ErrInvalid) {
		code, message := attachControlError(err)
		return &protocol.Error{Code: code, Message: message}
	}
	return err
}
