package sshd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/ptyhost"
)

func init() {
	registerMethod(protocol.MethodTerminalStatus, (*Server).terminalStatus)
	registerMethod(protocol.MethodTerminalStop, (*Server).terminalStop)
	registerMethod(protocol.MethodEnvSave, (*Server).environmentSave)
	registerMethod(protocol.MethodEnvReset, (*Server).environmentReset)
}

// serveTerminal serves one member's persistent environment terminal. The
// member identity comes from the authenticated SSH connection, so the header
// cannot select another member's environment.
func (s *Server) serveTerminal(ctx context.Context, member domain.MemberID, st *sessionState, ch subsystemConn) {
	defer func() { _ = ch.Close() }()

	capped := &capReader{r: ch, left: maxSubsystemHeaderBytes}
	r := bufio.NewReaderSize(capped, 4<<10)
	line, err := protocol.ReadLine(r)
	if err != nil {
		return
	}
	capped.left = -1

	var req protocol.TerminalRequest
	if err := json.Unmarshal(line, &req); err != nil {
		_ = writeJSONLine(ch, protocol.TerminalResponse{Code: protocol.CodeParse, Error: "parse error: " + err.Error()})
		return
	}
	tab := req.Tab
	if tab == "" {
		tab = "main"
	}
	cols, rows, hasPTY := st.geometry()
	if !hasPTY {
		cols, rows = req.Cols, req.Rows
	}
	if cols == 0 || rows == 0 {
		cols, rows = 80, 24
	}

	s.authorizationMu.Lock()
	admissionErr := s.checkMember(ctx, member)
	if admissionErr == nil {
		_, admissionErr = s.cfg.Runs.EnsureTerminal(ctx, member)
	}
	if admissionErr == nil {
		admissionErr = s.cfg.Runs.EnsureTerminalTab(ctx, member, tab, cols, rows)
	}
	s.authorizationMu.Unlock()
	if admissionErr != nil {
		e := rpcError(admissionErr)
		_ = writeJSONLine(ch, protocol.TerminalResponse{Code: e.Code, Error: e.Message})
		return
	}

	attachCtx, revoke := context.WithCancelCause(ctx)
	defer revoke(nil)
	ack := &protocol.TerminalResponse{OK: true, Tab: tab, Cols: cols, Rows: rows}
	// A live attach holds the self-update idle check open: restarting
	// would drop this stream under the member typing into it.
	release := s.cfg.Runs.HoldShell()
	defer release()
	conn := newAttachConn(ch, r, ack, req.Framed)
	errCh := make(chan error, 1)
	go func() {
		errCh <- s.cfg.PTY.Attach(attachCtx, ptyhost.TerminalSession(member, tab), ptyhost.AttachClient{
			Member:   member,
			Cols:     cols,
			Rows:     rows,
			Follow:   req.Follow,
			Snapshot: req.Framed,
		}, conn, st.resize)
	}()

	var attachErr error
	returned := false
	select {
	case attachErr = <-errCh:
		returned = true
	case <-conn.first:
	}
	if returned && attachErr != nil {
		if !conn.okSent() {
			e := rpcError(attachErr)
			_ = writeJSONLine(ch, protocol.TerminalResponse{OK: false, Code: e.Code, Error: e.Message})
		} else {
			ch.exit(1)
		}
		return
	}
	conn.sendOK()

	// Terminal writes are always permitted; readOnly=true here means the
	// loop checks membership only and skips run steer policy.
	if !returned {
		s.spawn(func() {
			s.revokeOnPolicyChange(attachCtx, revoke, member, "", true)
		})
		attachErr = <-errCh
	}
	switch {
	case errors.Is(context.Cause(attachCtx), errAttachMembershipRevoked):
		ch.exit(protocol.AttachExitMembershipRevoked)
	case attachErr == nil:
		ch.exit(0)
	default:
		ch.exit(1)
	}
}

func (s *Server) terminalStatus(ctx context.Context, member domain.MemberID, _ json.RawMessage) (any, *protocol.Error) {
	status, err := s.cfg.Runs.TerminalStatus(ctx, member)
	if err != nil {
		return nil, rpcError(err)
	}
	out := protocol.TerminalStatusResult{Running: status.Running, Image: status.Image, SavedImage: status.SavedImage, Tabs: status.Tabs}
	if !status.StartedAt.IsZero() {
		out.StartedAt = status.StartedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
	}
	return out, nil
}

func (s *Server) terminalStop(ctx context.Context, member domain.MemberID, _ json.RawMessage) (any, *protocol.Error) {
	if err := s.cfg.Runs.StopTerminal(ctx, member); err != nil {
		return nil, rpcError(err)
	}
	return struct{}{}, nil
}

func (s *Server) environmentSave(ctx context.Context, member domain.MemberID, _ json.RawMessage) (any, *protocol.Error) {
	image, err := s.cfg.Runs.SaveEnvironment(ctx, member)
	if err != nil {
		return nil, rpcError(err)
	}
	return protocol.EnvSaveResult{Image: image}, nil
}

func (s *Server) environmentReset(ctx context.Context, member domain.MemberID, _ json.RawMessage) (any, *protocol.Error) {
	if err := s.cfg.Runs.ResetEnvironment(ctx, member); err != nil {
		return nil, rpcError(err)
	}
	return struct{}{}, nil
}
