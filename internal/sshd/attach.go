package sshd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/ptyhost"
)

// The two reasons the re-validation drops a live attach. Each maps to its
// own exit status so the client can tell the member why they were
// detached, and whether a read-only attach would still work.
var (
	errAttachSteerRevoked      = errors.New("sshd: steer permission withdrawn")
	errAttachMembershipRevoked = errors.New("sshd: membership withdrawn")
)

// serveAttach wires an aether-attach subsystem channel to the PTY host:
// one header line, an ack, then either raw bytes or ordered terminal records.
// Geometry precedence is pty-req > header > 80x24; an attach without pty-req
// is forced read-only.
func (s *Server) serveAttach(ctx context.Context, member domain.MemberID, st *sessionState, ch subsystemConn) {
	defer func() { _ = ch.Close() }()
	capped := &capReader{r: ch, left: maxSubsystemHeaderBytes}
	r := bufio.NewReaderSize(capped, 4<<10)
	line, err := protocol.ReadLine(r)
	if err != nil {
		return
	}
	capped.left = -1
	var req protocol.AttachRequest
	if uerr := json.Unmarshal(line, &req); uerr != nil {
		_ = writeJSONLine(ch, protocol.AttachResponse{OK: false, Code: protocol.CodeParse, Error: "parse error: " + uerr.Error()})
		return
	}
	if req.RunID == "" {
		_ = writeJSONLine(ch, protocol.AttachResponse{OK: false, Code: protocol.CodeInvalidParams, Error: "run_id is required"})
		return
	}
	if merr := s.checkMember(ctx, member); merr != nil {
		e := rpcError(merr)
		_ = writeJSONLine(ch, protocol.AttachResponse{OK: false, Code: e.Code, Error: e.Message})
		return
	}
	run, err := s.cfg.Store.GetRun(ctx, domain.RunID(req.RunID))
	if err != nil {
		e := rpcError(err)
		_ = writeJSONLine(ch, protocol.AttachResponse{OK: false, Code: e.Code, Error: e.Message})
		return
	}

	cols, rows, hasPTY := st.geometry()
	if !hasPTY {
		cols, rows = req.Cols, req.Rows
	}
	if cols == 0 || rows == 0 {
		cols, rows = 80, 24
	}
	readOnly := req.ReadOnly || !hasPTY
	key := ptyhost.RunSession(run.ID)
	if req.Shell != "" {
		if steerErr := checkSteer(ctx, s.cfg.Store, member, run.ID); steerErr != nil {
			e := rpcError(steerErr)
			_ = writeJSONLine(ch, protocol.AttachResponse{OK: false, Code: e.Code, Error: e.Message})
			return
		}
		if ensureErr := s.cfg.Runs.EnsureRunShellTab(ctx, run.ID, req.Shell, cols, rows); ensureErr != nil {
			e := rpcError(ensureErr)
			_ = writeJSONLine(ch, protocol.AttachResponse{OK: false, Code: e.Code, Error: e.Message})
			return
		}
		key = ptyhost.RunShellSession(run.ID, req.Shell)
		readOnly = false
	}

	// The attach gets its own cancel so the re-validation below can end it
	// with a cause; the cause picks the exit status once Attach returns.
	attachCtx, revoke := context.WithCancelCause(ctx)
	defer revoke(nil)
	// The geometry here is only what this client brings; the PTY host
	// overwrites it with the session's own before the ack goes out, and
	// reports every later change on the same conn.
	ack := &protocol.AttachResponse{OK: true, Cols: cols, Rows: rows}
	conn := newAttachConn(ch, r, ack, req.Framed)
	errCh := make(chan error, 1)
	go func() {
		errCh <- s.cfg.PTY.Attach(attachCtx, key, ptyhost.AttachClient{
			Member:   member,
			Cols:     cols,
			Rows:     rows,
			ReadOnly: readOnly,
			Snapshot: req.Framed,
			Follow:   req.Follow,
			Resume:   req.Resume,
			Cursor:   req.Cursor,
		}, conn, st.resize)
	}()

	// The PTY host acks through the conn once it knows the replay boundary
	// (WriteReplay), or fails before that; there is no third outcome, so
	// acking on a timer would only ever lose the boundary.
	var attachErr error
	returned := false
	select {
	case attachErr = <-errCh:
		returned = true
	case <-conn.first:
	}
	if returned && attachErr != nil {
		if !conn.okSent() {
			// A finished run has no live session by design; its transcript
			// is the artifact. Serve that instead of a refusal the client
			// can only retry forever - but only for the run's own agent
			// session: a shell tab is a fresh process, and replaying the
			// agent's transcript into it would mislead. Queued,
			// provisioning, and running runs keep the refusal: there a
			// missing session is a transient race (recovery mid-reattach)
			// the client's retry resolves.
			if _, isRunSession := key.Run(); isRunSession && replayableStatus(run.Status) && s.serveReplay(ch, run, cols, rows, req.Framed) {
				return
			}
			e := rpcError(attachErr)
			_ = writeJSONLine(ch, protocol.AttachResponse{OK: false, Code: e.Code, Error: e.Message})
		} else {
			ch.exit(1)
		}
		return
	}
	conn.sendOK()

	s.publishPresence(run, member, events.PresenceWatching)
	if !returned {
		s.spawn(func() { s.revokeOnPolicyChange(attachCtx, revoke, member, run.ID, readOnly) })
		attachErr = <-errCh
	}
	s.publishPresence(run, member, events.PresenceOnline)
	switch cause := context.Cause(attachCtx); {
	case errors.Is(cause, errAttachSteerRevoked):
		ch.exit(protocol.AttachExitSteerRevoked)
	case errors.Is(cause, errAttachMembershipRevoked):
		ch.exit(protocol.AttachExitMembershipRevoked)
	case attachErr == nil:
		// Session end: server closes with exit-status 0.
		ch.exit(0)
	default:
		// Attach failed after the ack was already on the wire; a nonzero
		// exit-status is the remaining signal that distinguishes the
		// failure from a clean session end.
		ch.exit(1)
	}
}

// replayableStatus reports whether a run without a PTY session is
// durably sessionless rather than caught in a transient provisioning,
// recovery, or stalled-but-live window.
func replayableStatus(st domain.RunStatus) bool {
	return st.Terminal()
}

// serveReplay streams a finished run's complete recorded transcript and ends
// the channel cleanly (exit-status 0), reporting whether it served. A framed
// dashboard attach still uses the final snapshot's geometry, but its output is
// the same complete history a raw client receives.
func (s *Server) serveReplay(ch subsystemConn, run *domain.Run, cols, rows uint, framed bool) bool {
	if framed {
		snap, err := s.cfg.PTY.Snapshot(run.ID)
		if err != nil {
			slog.Warn("sshd: snapshot finished run geometry", "run", run.ID, "error", err)
			return false
		}
		cols, rows = snap.Cols, snap.Rows
	}
	rc, replayBytes, err := s.cfg.PTY.Replay(run.ID)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Warn("sshd: open transcript for attach replay", "run", run.ID, "error", err)
		}
		return false
	}
	defer func() { _ = rc.Close() }()
	if err := writeJSONLine(ch, protocol.AttachResponse{
		OK: true, Cols: cols, Rows: rows, Replay: replayBytes, Framed: framed,
	}); err != nil {
		return true
	}
	if err := writeTerminalReplay(ch, rc, replayBytes, framed); err != nil {
		slog.Warn("sshd: stream transcript replay", "run", run.ID, "error", err)
		ch.exit(1)
		return true
	}
	ch.exit(0)
	return true
}

// revokeOnPolicyChange re-runs the attach's authorization for as
// long as it is served, the way revokeSyncOnPolicyChange does for the sync
// bridge. The gate consulted at attach time is a snapshot: without this, a
// member demoted, removed, or handed off mid-attach - or whose run was
// protected or whose workspace went admins-only - keeps typing into the
// agent's terminal until they choose to disconnect, the one surface with
// the most direct access to a running agent. Steer loss ends a write
// attach; a read-only attach ends only when the membership itself goes.
// Store reads only, every few seconds per live attach.
func (s *Server) revokeOnPolicyChange(ctx context.Context, revoke context.CancelCauseFunc, member domain.MemberID, run domain.RunID, readOnly bool) {
	ticker := time.NewTicker(s.cfg.revalidateInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if cause := s.attachRevocation(ctx, member, run, readOnly); cause != nil {
				revoke(cause)
				return
			}
		}
	}
}

// attachRevocation reports why a live attach has to end, or nil while it
// is still authorized. Like the sync bridge it fails closed: a store error
// counts as a loss.
func (s *Server) attachRevocation(ctx context.Context, member domain.MemberID, run domain.RunID, readOnly bool) error {
	if s.checkMember(ctx, member) != nil {
		return errAttachMembershipRevoked
	}
	if readOnly {
		return nil
	}
	if checkSteer(ctx, s.cfg.Store, member, run) != nil {
		return errAttachSteerRevoked
	}
	return nil
}

func (s *Server) publishPresence(run *domain.Run, member domain.MemberID, state events.PresenceState) {
	_, _ = s.cfg.Bus.Publish(context.Background(), events.Event{
		WorkspaceID: run.WorkspaceID,
		RunID:       run.ID,
		ActorID:     member,
		Payload:     events.PresencePayload{State: state},
	})
}

// attachConn is the io.ReadWriter handed to PTYAttacher.Attach. It delays
// the acknowledgment until the PTY host identifies the replay boundary.
type attachConn struct {
	ch       subsystemConn
	r        *bufio.Reader
	ack      any
	framed   bool
	mu       sync.Mutex
	sent     bool
	first    chan struct{}
	writeErr error
}

func newAttachConn(ch subsystemConn, r *bufio.Reader, ack any, framed bool) *attachConn {
	switch response := ack.(type) {
	case *protocol.AttachResponse:
		response.Framed = framed
	case *protocol.TerminalResponse:
		response.Framed = framed
	}
	return &attachConn{ch: ch, r: r, ack: ack, framed: framed, first: make(chan struct{})}
}

// SetGeometry takes the session's PTY size from the host. Before the ack
// goes out it is what the ack reports; afterwards framed clients receive a
// geometry record in the same stream as output. Raw clients receive no
// geometry bytes.
func (c *attachConn) SetGeometry(cols, rows uint) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.sent {
		switch ack := c.ack.(type) {
		case *protocol.AttachResponse:
			ack.Cols, ack.Rows = cols, rows
		case *protocol.TerminalResponse:
			ack.Cols, ack.Rows = cols, rows
		}
		return
	}
	if c.framed && c.writeErr == nil {
		if err := protocol.WriteTerminalGeometry(c.ch, cols, rows); err != nil {
			c.writeErr = err
			slog.Warn("sshd: write terminal geometry", "error", err)
			c.ch.exit(1)
			_ = c.ch.Close()
		}
	}
}

// SetResume records how the session answered a resume. It lands in the
// ack, so it must arrive before WriteReplay sends it - Host.Attach calls
// it straight after the client joins, which is where both are decided.
func (c *attachConn) SetResume(cursor uint64, resumed bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sent {
		return
	}
	if ack, ok := c.ack.(*protocol.AttachResponse); ok {
		ack.Cursor, ack.Resumed = cursor, resumed
	}
}

func (c *attachConn) sendOK() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sendOKLocked()
}

func (c *attachConn) sendOKLocked() {
	if c.sent {
		return
	}
	c.sent = true
	c.writeErr = writeJSONLine(c.ch, c.ack)
	close(c.first)
}

func (c *attachConn) setReplayLocked(n int) {
	switch ack := c.ack.(type) {
	case *protocol.AttachResponse:
		ack.Replay = n
	case *protocol.TerminalResponse:
		ack.Replay = n
	}
}

func (c *attachConn) WriteReplay(replay io.Reader, bytes int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.setReplayLocked(bytes)
	c.sendOKLocked()
	if c.writeErr != nil {
		return c.writeErr
	}
	if err := writeTerminalReplay(c.ch, replay, bytes, c.framed); err != nil {
		c.writeErr = err
		return err
	}
	return nil
}

func writeTerminalReplay(w io.Writer, replay io.Reader, bytes int, framed bool) error {
	if !framed {
		written, err := io.CopyN(w, replay, int64(bytes))
		if err == nil && written != int64(bytes) {
			return io.ErrUnexpectedEOF
		}
		return err
	}
	buf := make([]byte, 32<<10)
	remaining := bytes
	for remaining > 0 {
		read := len(buf)
		if read > remaining {
			read = remaining
		}
		if _, err := io.ReadFull(replay, buf[:read]); err != nil {
			return err
		}
		if _, err := protocol.WriteTerminalOutput(w, buf[:read]); err != nil {
			return err
		}
		remaining -= read
	}
	return nil
}

func (c *attachConn) okSent() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sent
}

func (c *attachConn) Read(p []byte) (int, error) { return c.r.Read(p) }

func (c *attachConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sendOKLocked()
	if c.writeErr != nil {
		return 0, c.writeErr
	}
	if c.framed {
		return protocol.WriteTerminalOutput(c.ch, p)
	}
	return c.ch.Write(p)
}
