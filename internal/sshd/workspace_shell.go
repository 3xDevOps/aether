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
	"github.com/3xDevOps/Aether/internal/store"
)

// serveWorkspaceShell handles both bootstrap-tools and harness-login shells.
// Geometry precedence is pty-req > header > 80x24 and header bytes buffered
// beyond the JSON line are forwarded unchanged to the container stdin.
func (s *Server) serveWorkspaceShell(ctx context.Context, member domain.MemberID, st *sessionState, ch ssh.Channel) {
	defer func() { _ = ch.Close() }()
	capped := &capReader{r: ch, left: maxSubsystemHeaderBytes}
	r := bufio.NewReaderSize(capped, 4<<10)
	line, err := protocol.ReadLine(r)
	if err != nil {
		return
	}
	capped.left = -1
	var req protocol.WorkspaceShellRequest
	if unmarshalErr := json.Unmarshal(line, &req); unmarshalErr != nil {
		_ = writeJSONLine(ch, protocol.WorkspaceShellResponse{OK: false, Code: protocol.CodeParse, Error: "parse error: " + unmarshalErr.Error()})
		return
	}
	if validateErr := req.Validate(); validateErr != nil {
		_ = writeJSONLine(ch, protocol.WorkspaceShellResponse{OK: false, Code: protocol.CodeInvalidParams, Error: validateErr.Error()})
		return
	}
	if memberErr := s.checkMember(ctx, member); memberErr != nil {
		e := rpcError(memberErr)
		_ = writeJSONLine(ch, protocol.WorkspaceShellResponse{OK: false, Code: e.Code, Error: e.Message})
		return
	}
	if _, workspaceErr := s.resolveWorkspaceShellWorkspace(ctx, req.Workspace); workspaceErr != nil {
		e := rpcError(workspaceErr)
		_ = writeJSONLine(ch, protocol.WorkspaceShellResponse{OK: false, Code: e.Code, Error: e.Message})
		return
	}
	actor, err := resolveActor(ctx, s.cfg.Store, member)
	if err != nil {
		e := rpcError(err)
		_ = writeJSONLine(ch, protocol.WorkspaceShellResponse{OK: false, Code: e.Code, Error: e.Message})
		return
	}
	if permissionErr := permissions.Check(permissions.Launch, actor, permissions.Target{}); permissionErr != nil {
		_ = writeJSONLine(ch, protocol.WorkspaceShellResponse{OK: false, Code: protocol.CodeDenied, Error: "workspace shell: " + permissionErr.Error()})
		return
	}
	cols, rows, hasPTY := st.geometry()
	if !hasPTY {
		cols, rows = req.Cols, req.Rows
	}
	if cols == 0 || rows == 0 {
		cols, rows = 80, 24
	}
	_ = writeJSONLine(ch, protocol.WorkspaceShellResponse{OK: true, Workspace: req.Workspace, Mode: req.Mode, Harness: req.Harness, VerificationExecutable: req.VerificationExecutable, Resume: req.Resume, Reset: req.Reset, Cols: cols, Rows: rows})
	conn := &workspaceShellConn{r: r, w: ch}
	if workspaceShellErr := s.cfg.Runs.WorkspaceShell(ctx, member, domain.WorkspaceShellRequest{
		Workspace: domain.WorkspaceSelector{ID: domain.WorkspaceID(req.Workspace.ID), Name: req.Workspace.Name},
		Mode:      req.Mode, Harness: req.Harness, VerificationExecutable: req.VerificationExecutable,
		Resume: req.Resume, Reset: req.Reset, Cols: cols, Rows: rows,
	}, cols, rows, conn, st.resize); workspaceShellErr != nil {
		_, _ = fmt.Fprintf(ch, "\r\naether workspace shell: %v\r\n", workspaceShellErr)
		sendExitStatus(ch, 1)
		return
	}
	sendExitStatus(ch, 0)
}

type workspaceShellConn struct {
	r *bufio.Reader
	w ssh.Channel
}

func (c *workspaceShellConn) Read(p []byte) (int, error)  { return c.r.Read(p) }
func (c *workspaceShellConn) Write(p []byte) (int, error) { return c.w.Write(p) }
func (s *Server) resolveWorkspaceShellWorkspace(ctx context.Context, selector protocol.WorkspaceSelector) (*domain.Workspace, error) {
	if selector.ID != "" {
		return s.cfg.Store.GetWorkspace(ctx, domain.WorkspaceID(selector.ID))
	}
	workspaces, err := s.cfg.Store.ListWorkspaces(ctx)
	if err != nil {
		return nil, err
	}
	for _, workspace := range workspaces {
		if workspace.Name == selector.Name {
			return workspace, nil
		}
	}
	return nil, store.ErrNotFound
}
