package sshd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// serveSetup wires an aether-setup subsystem channel to the run
// controller's login container: one header line in, an ack, then a raw
// byte pipe. Geometry precedence is pty-req > header > 80x24.
func (s *Server) serveSetup(ctx context.Context, member domain.MemberID, st *sessionState, ch ssh.Channel) {
	defer func() { _ = ch.Close() }()
	capped := &capReader{r: ch, left: maxSubsystemHeaderBytes}
	r := bufio.NewReaderSize(capped, 4<<10)
	line, err := protocol.ReadLine(r)
	if err != nil {
		return
	}
	capped.left = -1
	var req protocol.SetupRequest
	if uerr := json.Unmarshal(line, &req); uerr != nil {
		_ = writeJSONLine(ch, protocol.SetupResponse{OK: false, Code: protocol.CodeParse, Error: "parse error: " + uerr.Error()})
		return
	}
	if req.Harness == "" {
		_ = writeJSONLine(ch, protocol.SetupResponse{OK: false, Code: protocol.CodeInvalidParams, Error: "harness is required"})
		return
	}
	if merr := s.checkMember(ctx, member); merr != nil {
		e := rpcError(merr)
		_ = writeJSONLine(ch, protocol.SetupResponse{OK: false, Code: e.Code, Error: e.Message})
		return
	}
	// A login container is a container: starting one is the Launch
	// capability, the same gate run.launch goes through, so a viewer never
	// gets an interactive shell on the host's container daemon.
	actor, aerr := resolveActor(ctx, s.cfg.Store, member)
	if aerr != nil {
		e := rpcError(aerr)
		_ = writeJSONLine(ch, protocol.SetupResponse{OK: false, Code: e.Code, Error: e.Message})
		return
	}
	if cerr := permissions.Check(permissions.Launch, actor, permissions.Target{}); cerr != nil {
		_ = writeJSONLine(ch, protocol.SetupResponse{OK: false, Code: protocol.CodeDenied, Error: "setup: " + cerr.Error()})
		return
	}

	cols, rows, hasPTY := st.geometry()
	if !hasPTY {
		cols, rows = req.Cols, req.Rows
	}
	if cols == 0 || rows == 0 {
		cols, rows = 80, 24
	}

	_ = writeJSONLine(ch, protocol.SetupResponse{OK: true, Cols: cols, Rows: rows})
	conn := &setupConn{r: r, w: ch}
	if serr := s.cfg.Runs.SetupLogin(ctx, member, req.Harness, req.Image, cols, rows, conn, st.resize); serr != nil {
		_, _ = fmt.Fprintf(ch, "\r\naether setup: %v\r\n", serr)
		sendExitStatus(ch, 1)
		return
	}
	sendExitStatus(ch, 0)
}

// setupConn is the io.ReadWriter handed to RunController.SetupLogin. It
// reads leftover header-buffer bytes from the channel, then raw input.
// CloseWrite half-closes the channel so end-of-output reaches the client
// while the exit-status request can still be delivered.
type setupConn struct {
	r *bufio.Reader
	w ssh.Channel
}

func (c *setupConn) Read(p []byte) (int, error)  { return c.r.Read(p) }
func (c *setupConn) Write(p []byte) (int, error) { return c.w.Write(p) }
func (c *setupConn) CloseWrite() error           { return c.w.CloseWrite() }
