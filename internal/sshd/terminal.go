package sshd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/ptyhost"
)

func init() {
	registerMethod(protocol.MethodTerminalStatus, (*Server).terminalStatus)
	registerMethod(protocol.MethodTerminalStop, (*Server).terminalStop)
}

// serveTerminal serves one member's persistent environment terminal. The
// member identity comes from the authenticated SSH connection, so the header
// cannot select another member's environment.
func (s *Server) serveTerminal(ctx context.Context, member domain.MemberID, st *sessionState, ch ssh.Channel) {
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
	if err := s.checkMember(ctx, member); err != nil {
		e := rpcError(err)
		_ = writeJSONLine(ch, protocol.TerminalResponse{Code: e.Code, Error: e.Message})
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
	if _, err := s.cfg.Runs.EnsureTerminal(ctx, member); err != nil {
		e := rpcError(err)
		_ = writeJSONLine(ch, protocol.TerminalResponse{Code: e.Code, Error: e.Message})
		return
	}
	if err := s.cfg.Runs.EnsureTerminalTab(ctx, member, tab, cols, rows); err != nil {
		e := rpcError(err)
		_ = writeJSONLine(ch, protocol.TerminalResponse{Code: e.Code, Error: e.Message})
		return
	}

	ack := protocol.TerminalResponse{OK: true, Tab: tab, Cols: cols, Rows: rows}
	if err := writeJSONLine(ch, ack); err != nil {
		return
	}

	attachCtx, revoke := context.WithCancelCause(ctx)
	defer revoke(nil)
	// A live attach holds the self-update idle check open: restarting
	// would drop this stream under the member typing into it.
	release := s.cfg.Runs.HoldShell()
	defer release()
	conn := &terminalConn{ch: ch, r: r}
	errCh := make(chan error, 1)
	go func() {
		errCh <- s.cfg.PTY.Attach(attachCtx, ptyhost.TerminalSession(member, tab), member, cols, rows, false, conn, st.resize)
	}()

	// The ack is already on the wire, so unlike serveAttach there is no
	// pre-ack failure window: start the re-validation loop straight away.
	// Terminal writes are always permitted; readOnly=true here means the
	// loop checks membership only and skips run steer policy.
	s.spawn(func() {
		s.revokeAttachOnPolicyChange(attachCtx, revoke, member, "", true)
	})
	attachErr := <-errCh
	switch {
	case errors.Is(context.Cause(attachCtx), errAttachMembershipRevoked):
		sendExitStatus(ch, protocol.AttachExitMembershipRevoked)
	case attachErr == nil:
		sendExitStatus(ch, 0)
	default:
		sendExitStatus(ch, 1)
	}
}

// terminalConn keeps any bytes buffered while reading the JSON header in the
// stream. It is intentionally not an attachConn because terminal acks have a
// different wire type and are sent before PTY.Attach starts.
type terminalConn struct {
	ch ssh.Channel
	r  *bufio.Reader
}

func (c *terminalConn) Read(p []byte) (int, error)  { return c.r.Read(p) }
func (c *terminalConn) Write(p []byte) (int, error) { return c.ch.Write(p) }

var _ io.ReadWriter = (*terminalConn)(nil)

func (s *Server) terminalStatus(ctx context.Context, member domain.MemberID, _ json.RawMessage) (any, *protocol.Error) {
	status, err := s.cfg.Runs.TerminalStatus(ctx, member)
	if err != nil {
		return nil, rpcError(err)
	}
	out := protocol.TerminalStatusResult{Running: status.Running, Image: status.Image, Tabs: status.Tabs}
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
