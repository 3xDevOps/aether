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

	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/ptyhost"
	"github.com/3xDevOps/Aether/internal/store"
)

// The reasons re-validation or controller fencing drops a live attach. Each
// maps to its own exit status where a client can act on the distinction.
var (
	errAttachSteerRevoked      = errors.New("sshd: steer permission withdrawn")
	errAttachMembershipRevoked = errors.New("sshd: membership withdrawn")
	errAttachControlRevoked    = errors.New("sshd: control lease fenced")
)

type attachControlLease struct {
	sessionID  string
	generation uint64
	attachID   uint64
}

type controlAttach struct {
	cancel     context.CancelCauseFunc
	id         uint64
	generation uint64
}

func (s *Server) attachControlAck(ack *protocol.AttachResponse, snap control.Snapshot, held bool) {
	ack.ControllerID = string(snap.MemberID)
	ack.ControlGeneration = snap.Generation
	ack.ControlExpiresAt = ""
	if !snap.ExpiresAt.IsZero() {
		ack.ControlExpiresAt = snap.ExpiresAt.UTC().Format(time.RFC3339)
	}
	ack.HasControl = held
}

func attachControlError(err error) (int, string) {
	switch {
	case errors.Is(err, control.ErrOccupied):
		return protocol.CodeConflict, "run control is held by another session"
	case errors.Is(err, control.ErrStale):
		return protocol.CodeConflict, "run control generation is stale"
	case errors.Is(err, control.ErrInvalidSession), errors.Is(err, control.ErrInvalid):
		return protocol.CodeInvalidParams, "control session is invalid"
	case errors.Is(err, permissions.ErrDenied),
		errors.Is(err, errMemberRemoved),
		errors.Is(err, errMemberPending),
		errors.Is(err, store.ErrNotFound):
		return protocol.CodeDenied, err.Error()
	default:
		return protocol.CodeInternal, err.Error()
	}
}

// attachExitForError maps authority failures discovered by the PTY input
// loop to the same statuses used by periodic policy/control revalidation.
func attachExitForError(err error) int {
	switch {
	case errors.Is(err, control.ErrStale), errors.Is(err, control.ErrInvalidSession):
		return protocol.AttachExitControlRevoked
	case errors.Is(err, errMemberRemoved), errors.Is(err, errMemberPending), errors.Is(err, store.ErrNotFound):
		return protocol.AttachExitMembershipRevoked
	case errors.Is(err, permissions.ErrDenied):
		return protocol.AttachExitSteerRevoked
	default:
		return 1
	}
}

func (s *Server) registerControlAttach(run, session string, generation uint64, cancel context.CancelCauseFunc) (uint64, error) {
	s.controlMu.Lock()
	if s.controlAttaches == nil {
		s.controlAttaches = make(map[string]map[string]controlAttach)
	}
	bySession := s.controlAttaches[run]
	if bySession == nil {
		bySession = make(map[string]controlAttach)
		s.controlAttaches[run] = bySession
	}
	id := s.controlAttachID.Add(1)
	bySession[session] = controlAttach{cancel: cancel, id: id, generation: generation}
	s.controlMu.Unlock()

	// Registration happens after lease acquisition. Revalidate after making
	// the callback visible so a concurrent fence either finds this transport
	// or is observed here.
	if err := s.cfg.Control.Validate(run, session, generation); err != nil {
		cancel(errAttachControlRevoked)
		return id, err
	}
	return id, nil
}

func (s *Server) unregisterControlAttach(run, session string, id uint64) {
	s.controlMu.Lock()
	defer s.controlMu.Unlock()
	bySession := s.controlAttaches[run]
	if current, ok := bySession[session]; !ok || current.id != id {
		return
	}
	delete(bySession, session)
	if len(bySession) == 0 {
		delete(s.controlAttaches, run)
	}
}

func (s *Server) cancelControlAttach(run, session string, generation uint64, cause error) {
	s.controlMu.Lock()
	var cancel context.CancelCauseFunc
	if bySession := s.controlAttaches[run]; bySession != nil {
		current := bySession[session]
		if current.generation == generation {
			cancel = current.cancel
		}
	}
	s.controlMu.Unlock()
	if cancel != nil {
		cancel(cause)
	}
}

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
	var controlLease *attachControlLease
	var controlSnap control.Snapshot
	controlHeld := false
	shellTab := req.Shell
	var shellReservation ptyhost.ShellTabReservation
	defer func() {
		if shellReservation == nil {
			return
		}
		if err := shellReservation.Rollback(context.Background()); err != nil {
			slog.Warn("sshd: roll back refused run shell", "run", run.ID, "tab", shellTab, "error", err)
		}
	}()
	_, _, hasClientPTY := st.geometry()
	wantsControl := !req.ReadOnly && hasClientPTY
	releasedControl := false
	if s.cfg.Control != nil {
		if req.ReleaseControl {
			if req.ControlSessionID == "" {
				_ = writeJSONLine(ch, protocol.AttachResponse{Code: protocol.CodeInvalidParams, Error: "control_session_id is required"})
				return
			}
			if req.ControlGeneration == 0 {
				_ = writeJSONLine(ch, protocol.AttachResponse{Code: protocol.CodeInvalidParams, Error: "control_generation is required"})
				return
			}
			// Release is committed only once the replacement has joined the
			// PTY host. Keep the old writer and generation intact while any
			// admission failure is still possible.
			req.ReadOnly = true
			req.ReleaseControl = false
			wantsControl = false
			releasedControl = true
		}
		if !wantsControl {
			if current, present := s.cfg.Control.Status(req.RunID); present {
				controlSnap = current
			}
		}
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
	if wantsControl && s.cfg.Control != nil && req.ControlSessionID == "" {
		_ = writeJSONLine(ch, protocol.AttachResponse{OK: false, Code: protocol.CodeInvalidParams, Error: "control_session_id is required"})
		return
	}
	if req.Shell != "" {
		key = ptyhost.RunShellSession(run.ID, req.Shell)
		if !readOnly {
			if steerErr := checkSteer(ctx, s.cfg.Store, member, run.ID); steerErr != nil {
				e := rpcError(steerErr)
				_ = writeJSONLine(ch, protocol.AttachResponse{OK: false, Code: e.Code, Error: e.Message})
				return
			}
			var ensureErr error
			shellReservation, ensureErr = s.cfg.Runs.EnsureRunShellTabReserved(ctx, run.ID, req.Shell, cols, rows)
			if ensureErr != nil {
				e := rpcError(ensureErr)
				_ = writeJSONLine(ch, protocol.AttachResponse{OK: false, Code: e.Code, Error: e.Message})
				return
			}
		}
	}
	ack := &protocol.AttachResponse{
		OK: true, Cols: cols, Rows: rows, Framed: req.Framed,
	}
	if !wantsControl && controlSnap.MemberID != "" {
		s.attachControlAck(ack, controlSnap, false)
	}
	var controlAcquireErr error
	var controlCommitErr error
	attachCtx, revoke := context.WithCancelCause(ctx)
	defer revoke(nil)
	// The geometry here is only what this client brings; the PTY host
	// overwrites it with the session's own before the ack goes out, and
	authorize := func() error {
		if readOnly {
			return nil
		}
		if wantsControl && s.cfg.Control != nil {
			acquired, displaced, acquireErr := s.cfg.Control.AcquireAuthorized(
				req.RunID, string(member), req.ControlSessionID, req.Takeover,
				req.ControlGeneration,
				func() error { return checkSteer(ctx, s.cfg.Store, member, run.ID) },
			)
			if acquireErr != nil {
				controlAcquireErr = acquireErr
				return acquireErr
			}
			if displaced != nil {
				s.cancelControlAttach(req.RunID, displaced.SessionID, displaced.Generation, errAttachControlRevoked)
			}
			controlSnap, controlHeld = acquired, true
			controlLease = &attachControlLease{sessionID: req.ControlSessionID, generation: acquired.Generation}
			s.attachControlAck(ack, controlSnap, true)
			controlLease.attachID, acquireErr = s.registerControlAttach(req.RunID, controlLease.sessionID, acquired.Generation, revoke)
			if acquireErr != nil {
				controlAcquireErr = acquireErr
				return acquireErr
			}
			return nil
		}
		if err := checkSteer(ctx, s.cfg.Store, member, run.ID); err != nil {
			return err
		}
		return nil
	}
	var commit func(func() error) error
	var beforeAck func() error
	if releasedControl {
		commit = func(admit func() error) error {
			var admissionErr error
			if err := s.cfg.Control.ReleaseAdmitted(
				req.RunID, member, req.ControlSessionID, req.ControlGeneration,
				func() error {
					admissionErr = admit()
					return admissionErr
				},
			); err != nil {
				if admissionErr == nil {
					controlCommitErr = err
				}
				return err
			}
			// The replacement has joined the PTY host and its lease release
			// committed. Fence the old transport only after that boundary.
			s.cancelControlAttach(req.RunID, req.ControlSessionID, req.ControlGeneration, errAttachControlRevoked)
			return nil
		}
		beforeAck = func() error {
			if current, present := s.cfg.Control.Status(req.RunID); present {
				s.attachControlAck(ack, current, false)
			} else {
				s.attachControlAck(ack, control.Snapshot{}, false)
			}
			return nil
		}
	}
	conn := newAttachConn(ch, r, ack, req.Framed, beforeAck)
	defer func() {
		if controlLease == nil {
			return
		}
		s.unregisterControlAttach(req.RunID, controlLease.sessionID, controlLease.attachID)
		if conn.okWritten() {
			s.cfg.Control.Disconnect(req.RunID, controlLease.sessionID, controlLease.generation)
			return
		}
		if err := s.cfg.Control.Release(req.RunID, member, controlLease.sessionID, controlLease.generation); err != nil {
			s.cfg.Control.Disconnect(req.RunID, controlLease.sessionID, controlLease.generation)
		}
	}()
	var inputGuard func() error
	var inputAdmission func(func() error) error
	if wantsControl && s.cfg.Control != nil {
		inputGuard = func() error {
			if controlLease == nil {
				return control.ErrStale
			}
			return s.cfg.Control.Validate(req.RunID, controlLease.sessionID, controlLease.generation)
		}
		inputAdmission = func(accept func() error) error {
			if controlLease == nil {
				return control.ErrStale
			}
			return s.cfg.Control.AdmitMember(req.RunID, member, controlLease.sessionID, controlLease.generation, func() error {
				if err := checkSteer(ctx, s.cfg.Store, member, run.ID); err != nil {
					return err
				}
				return accept()
			})
		}
	}
	errCh := make(chan error, 1)
	go func() {
		errCh <- s.cfg.PTY.Attach(attachCtx, key, ptyhost.AttachClient{
			Member: member,
			SessionGeneration: func() uint64 {
				if shellReservation == nil {
					return 0
				}
				return shellReservation.Generation()
			}(),
			Cols:      cols,
			Rows:      rows,
			ReadOnly:  readOnly,
			Authorize: authorize,
			Commit:    commit,
			OnAttached: func() {
				if shellReservation != nil {
					shellReservation.Adopt()
				}
			},
			InputGuard:     inputGuard,
			InputAdmission: inputAdmission,
			Snapshot:       req.Framed,
			Follow:         req.Follow,
			Resume:         req.Resume,
			Cursor:         req.Cursor,
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
			if controlCommitErr != nil {
				ack := protocol.AttachResponse{OK: false}
				ack.Code, ack.Error = attachControlError(controlCommitErr)
				if current, present := s.cfg.Control.Status(req.RunID); present {
					s.attachControlAck(&ack, current, false)
				}
				_ = writeJSONLine(ch, ack)
				return
			}
			if controlAcquireErr != nil {
				// The member was allowed to create this shell. Keep it alive
				// when another session owns control so this client can
				// immediately reconnect as a read-only mirror.
				if shellReservation != nil &&
					(errors.Is(controlAcquireErr, control.ErrOccupied) ||
						errors.Is(controlAcquireErr, control.ErrStale)) {
					shellReservation.Adopt()
					shellReservation = nil
				}
				ack := protocol.AttachResponse{OK: false}
				ack.Code, ack.Error = attachControlError(controlAcquireErr)
				if current, present := s.cfg.Control.Status(req.RunID); present {
					s.attachControlAck(&ack, current, false)
				}
				_ = writeJSONLine(ch, ack)
				return
			}
			if releasedControl {
				ack := protocol.AttachResponse{OK: false}
				e := rpcError(attachErr)
				ack.Code, ack.Error = e.Code, e.Message
				if current, present := s.cfg.Control.Status(req.RunID); present {
					s.attachControlAck(&ack, current, false)
				}
				_ = writeJSONLine(ch, ack)
				return
			}
			// A finished run has no live session by design; its transcript
			// is the artifact. Serve that instead of a refusal the client
			// can only retry forever - but only for the run's own agent
			// session: a shell tab is a fresh process, and replaying the
			// agent's transcript into it would mislead. Queued,
			// provisioning, and running runs keep the refusal: there a
			// missing session is a transient race (recovery mid-reattach)
			// the client's retry resolves.
			if _, isRunSession := key.Run(); isRunSession && replayableStatus(run.Status) && s.serveReplay(ch, run, cols, rows, req.Framed, controlSnap, controlHeld) {
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
		if controlLease != nil {
			s.spawn(func() {
				s.revokeOnControlChange(attachCtx, revoke, string(run.ID), controlLease.sessionID, controlLease.generation)
			})
		}
		attachErr = <-errCh
	}
	s.publishPresence(run, member, events.PresenceOnline)
	switch cause := context.Cause(attachCtx); {
	case errors.Is(cause, errAttachSteerRevoked):
		ch.exit(protocol.AttachExitSteerRevoked)
	case errors.Is(cause, errAttachMembershipRevoked):
		ch.exit(protocol.AttachExitMembershipRevoked)
	case errors.Is(cause, errAttachControlRevoked):
		ch.exit(protocol.AttachExitControlRevoked)
	case attachErr == nil:
		// Session end: server closes with exit-status 0.
		ch.exit(0)
	default:
		// Attach failed after the ack was already on the wire. Preserve
		// authority-specific failures from the input guard/admission path.
		ch.exit(attachExitForError(attachErr))
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
func (s *Server) serveReplay(ch subsystemConn, run *domain.Run, cols, rows uint, framed bool, controlSnap control.Snapshot, controlHeld bool) bool {
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
	ack := protocol.AttachResponse{
		OK: true, Cols: cols, Rows: rows, Replay: replayBytes, Framed: framed,
	}
	if s.cfg.Control != nil {
		s.attachControlAck(&ack, controlSnap, controlHeld)
	}
	if err := writeJSONLine(ch, ack); err != nil {
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

// revokeOnControlChange fences a live attach when another session takes over,
// releases the lease, or the reconnect window expires. The PTY input guard
// independently checks every buffered read.
func (s *Server) revokeOnControlChange(ctx context.Context, revoke context.CancelCauseFunc, run, session string, generation uint64) {
	if s.cfg.Control == nil {
		return
	}
	ticker := time.NewTicker(s.cfg.revalidateInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.cfg.Control.Validate(run, session, generation); err != nil {
				revoke(errAttachControlRevoked)
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
		if s.checkMember(ctx, member) != nil {
			return errAttachMembershipRevoked
		}
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
	ch        subsystemConn
	r         *bufio.Reader
	ack       any
	framed    bool
	beforeAck func() error
	mu        sync.Mutex
	sent      bool
	first     chan struct{}
	writeErr  error
}

func newAttachConn(ch subsystemConn, r *bufio.Reader, ack any, framed bool, beforeAck func() error) *attachConn {
	switch response := ack.(type) {
	case *protocol.AttachResponse:
		response.Framed = framed
	case *protocol.TerminalResponse:
		response.Framed = framed
	}
	return &attachConn{ch: ch, r: r, ack: ack, framed: framed, beforeAck: beforeAck, first: make(chan struct{})}
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
	if c.beforeAck != nil {
		beforeAck := c.beforeAck
		c.beforeAck = nil
		if err := beforeAck(); err != nil {
			c.writeErr = err
			return
		}
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
func (c *attachConn) okWritten() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sent && c.writeErr == nil
}

func (c *attachConn) Read(p []byte) (int, error) { return c.r.Read(p) }
func (c *attachConn) Close() error               { return c.ch.Close() }

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
