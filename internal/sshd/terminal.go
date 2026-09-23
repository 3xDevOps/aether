package sshd

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/ptyhost"
)

func init() {
	registerGuarded(protocol.MethodTerminalHistory, permissions.View, terminalHistoryTarget, (*Server).terminalHistory)
	registerMethod(protocol.MethodTerminalStatus, (*Server).terminalStatus)
	registerMethod(protocol.MethodTerminalStop, (*Server).terminalStop)
	registerMethod(protocol.MethodEnvSave, (*Server).environmentSave)
	registerMethod(protocol.MethodEnvReset, (*Server).environmentReset)
}

func terminalHistoryTarget(s *Server, ctx context.Context, params json.RawMessage) (permissions.Target, *protocol.Error) {
	if _, perr := decodeTerminalHistoryParams(params); perr != nil {
		return permissions.Target{}, perr
	}
	return runTarget(s, ctx, params)
}

func decodeTerminalHistoryParams(params json.RawMessage) (protocol.TerminalHistoryParams, *protocol.Error) {
	if len(params) > protocol.MaxTerminalHistoryParamsBytes {
		return protocol.TerminalHistoryParams{}, invalidParams("terminal history params are too large")
	}
	if !utf8.Valid(params) {
		return protocol.TerminalHistoryParams{}, invalidParams("terminal history query must be valid UTF-8")
	}
	p, perr := decodeParams[protocol.TerminalHistoryParams](params)
	if perr != nil {
		return protocol.TerminalHistoryParams{}, perr
	}
	if p.RunID == "" {
		return protocol.TerminalHistoryParams{}, invalidParams("run_id is required")
	}
	if !validTerminalHistoryRunID(p.RunID) {
		return protocol.TerminalHistoryParams{}, invalidParams("invalid run_id")
	}
	if p.Limit < 0 {
		return protocol.TerminalHistoryParams{}, invalidParams("limit must not be negative")
	}
	if p.Limit == 0 {
		p.Limit = protocol.DefaultTerminalHistoryLimit
	} else if p.Limit > protocol.MaxTerminalHistoryLimit {
		p.Limit = protocol.MaxTerminalHistoryLimit
	}
	if !utf8.ValidString(p.Query) {
		return protocol.TerminalHistoryParams{}, invalidParams("terminal history query must be valid UTF-8")
	}
	if len(p.Query) > protocol.MaxTerminalHistoryQueryBytes {
		return protocol.TerminalHistoryParams{}, invalidParams("terminal history query is too long")
	}
	if p.Before != "" && !validTerminalHistoryCursor(p.Before) {
		return protocol.TerminalHistoryParams{}, invalidParams("invalid terminal history cursor")
	}
	return p, nil
}

func validTerminalHistoryCursor(encoded string) bool {
	if encoded == "" || len(encoded) > protocol.MaxTerminalHistoryCursorBytes {
		return false
	}
	decodedLen := base64.RawURLEncoding.DecodedLen(len(encoded))
	if decodedLen <= sha256.Size || decodedLen > 1024 {
		return false
	}
	var decoded [1024]byte
	n, err := base64.RawURLEncoding.Strict().Decode(decoded[:], []byte(encoded))
	return err == nil && n == decodedLen
}

func validTerminalHistoryRunID(id string) bool {
	if id == "" || len(id) > 128 || id[0] == '.' || id[0] == '-' || strings.Contains(id, "..") {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.':
		default:
			return false
		}
	}
	return true
}

const terminalHistoryTimeout = 2 * time.Second

func (s *Server) terminalHistory(ctx context.Context, _ domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeTerminalHistoryParams(params)
	if perr != nil {
		return nil, perr
	}
	runID := domain.RunID(p.RunID)
	history, ok := s.cfg.PTY.(PTYHistoryReader)
	if !ok {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "terminal history is unavailable"}
	}
	type historyResult struct {
		page ptyhost.HistoryPage
		err  error
	}
	historyCtx, cancel := context.WithTimeout(ctx, terminalHistoryTimeout)
	defer cancel()
	result := make(chan historyResult, 1)
	go func() {
		page, err := history.History(historyCtx, runID, p.Before, p.Query, p.Limit)
		result <- historyResult{page: page, err: err}
	}()
	var page ptyhost.HistoryPage
	var err error
	select {
	case historyResult := <-result:
		page, err = historyResult.page, historyResult.err
	case <-historyCtx.Done():
		err = historyCtx.Err()
	}
	if err != nil {
		switch {
		case errors.Is(err, ptyhost.ErrInvalidHistoryCursor):
			return nil, invalidParams("invalid terminal history cursor")
		case errors.Is(err, ptyhost.ErrHistoryQueryTooLong):
			return nil, invalidParams("terminal history query is too long")
		case errors.Is(err, ptyhost.ErrInvalidRunID):
			return nil, invalidParams("invalid run_id")
		case errors.Is(err, os.ErrNotExist):
			return nil, &protocol.Error{Code: protocol.CodeNotFound, Message: "terminal transcript not found"}
		case errors.Is(err, context.Canceled):
			return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "terminal history request canceled"}
		case errors.Is(err, context.DeadlineExceeded), errors.Is(err, os.ErrDeadlineExceeded),
			errors.Is(err, ptyhost.ErrNoSession), errors.Is(err, ptyhost.ErrSessionEnded):
			return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "terminal history is unavailable"}
		default:
			return nil, &protocol.Error{Code: protocol.CodeInternal, Message: "terminal history failed"}
		}
	}
	if len(page.Lines) > p.Limit ||
		page.HasMore != (page.NextCursor != "") ||
		(page.NextCursor != "" && !validTerminalHistoryCursor(page.NextCursor)) {
		return nil, &protocol.Error{Code: protocol.CodeInternal, Message: "terminal history returned an invalid page"}
	}
	resultBytes := len(page.NextCursor)
	lines := make([]protocol.TerminalHistoryLine, len(page.Lines))
	for i := range page.Lines {
		line := &page.Lines[i]
		if !validTerminalHistoryCursor(line.Cursor) ||
			len(line.Text) > protocol.MaxTerminalHistoryLineBytes ||
			len(line.Cursor)+len(line.Text) > protocol.MaxTerminalHistoryResultBytes-resultBytes {
			return nil, &protocol.Error{Code: protocol.CodeInternal, Message: "terminal history returned an invalid page"}
		}
		resultBytes += len(line.Cursor) + len(line.Text)
		lines[i] = protocol.TerminalHistoryLine{
			Cursor: line.Cursor,
			Time:   line.Time,
			Text:   line.Text,
		}
	}
	return protocol.TerminalHistoryResult{
		Lines: lines, NextCursor: page.NextCursor, HasMore: page.HasMore,
	}, nil
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
	conn := newAttachConn(ch, r, ack, req.Framed, nil)
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
