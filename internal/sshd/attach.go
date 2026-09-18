package sshd

import (
	"bufio"
	"bytes"
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
	onFence    func()
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

func (s *Server) registerControlAttach(run, session string, generation uint64, cancel context.CancelCauseFunc, onFence ...func()) (uint64, error) {
	var fence func()
	if len(onFence) > 0 {
		fence = onFence[0]
	}
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
	bySession[session] = controlAttach{cancel: cancel, id: id, generation: generation, onFence: fence}
	s.controlMu.Unlock()

	// Registration happens after lease acquisition. Revalidate after making
	// the callback visible so a concurrent fence either finds this transport
	// or is observed here.
	if err := s.cfg.Control.Validate(run, session, generation); err != nil {
		if fence != nil {
			fence()
		} else {
			cancel(errAttachControlRevoked)
		}
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
	var onFence func()
	if bySession := s.controlAttaches[run]; bySession != nil {
		current := bySession[session]
		if current.generation == generation {
			cancel = current.cancel
			onFence = current.onFence
		}
	}
	s.controlMu.Unlock()
	if onFence != nil {
		onFence()
		return
	}
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
	if req.Interactive && (!req.Framed || req.Shell != "") {
		_ = writeJSONLine(ch, protocol.AttachResponse{OK: false, Code: protocol.CodeInvalidParams, Error: "interactive attach requires framed run stream"})
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
	var conn *attachConn
	var controlReady func(bool) error
	var controlReadyMu sync.Mutex
	var controlReadyPending bool
	var leaseMu sync.Mutex
	var revokedGeneration uint64
	var revokedInputErr error
	setControlReady := func(fn func(bool) error) {
		controlReadyMu.Lock()
		defer controlReadyMu.Unlock()
		controlReady = fn
		if controlReadyPending && fn != nil {
			controlReadyPending = false
			_ = fn(true)
		}
	}
	applyControlReady := func(readOnly bool) error {
		controlReadyMu.Lock()
		defer controlReadyMu.Unlock()
		if controlReady == nil {
			controlReadyPending = readOnly
			return errors.New("attach control is not ready")
		}
		if !readOnly {
			controlReadyPending = false
		}
		return controlReady(readOnly)
	}
	// The geometry here is only what this client brings; the PTY host
	// overwrites it with the session's own before the ack goes out.
	var makeInteractiveFence func(*attachControlLease) func()
	var controlFence func()
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
			controlSnap, controlHeld = acquired, true
			lease := &attachControlLease{sessionID: req.ControlSessionID, generation: acquired.Generation}
			leaseMu.Lock()
			controlLease = lease
			leaseMu.Unlock()
			var fence func()
			if req.Interactive {
				fence = makeInteractiveFence(lease)
				leaseMu.Lock()
				controlFence = fence
				leaseMu.Unlock()
			}
			s.attachControlAck(ack, controlSnap, true)
			attachID, registerErr := s.registerControlAttach(req.RunID, lease.sessionID, acquired.Generation, revoke, fence)
			leaseMu.Lock()
			current := controlLease
			registered := registerErr == nil && current == lease
			if registered {
				lease.attachID = attachID
			} else if current == lease {
				controlLease = nil
				controlFence = nil
			}
			leaseMu.Unlock()
			if !registered {
				s.unregisterControlAttach(req.RunID, lease.sessionID, attachID)
				if registerErr == nil {
					registerErr = control.ErrStale
					_ = s.cfg.Control.Release(req.RunID, member, lease.sessionID, lease.generation)
				}
				controlAcquireErr = registerErr
				return registerErr
			}
			if displaced != nil {
				s.cancelControlAttach(req.RunID, displaced.SessionID, displaced.Generation, errAttachControlRevoked)
			}
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
	conn = newAttachConn(ch, r, ack, req.Framed, beforeAck)
	conn.interactive = req.Interactive && req.Shell == ""
	recordRevoked := func(lease *attachControlLease, err error) {
		if lease == nil {
			return
		}
		revokedGeneration = lease.generation
		revokedInputErr = err
	}
	makeInteractiveFence = func(lease *attachControlLease) func() {
		var once sync.Once
		return func() {
			once.Do(func() {
				leaseMu.Lock()
				current := controlLease
				if current == nil || current.sessionID != lease.sessionID || current.generation != lease.generation {
					leaseMu.Unlock()
					return
				}
				controlLease = nil
				controlFence = nil
				if revokedGeneration != lease.generation {
					recordRevoked(lease, control.ErrStale)
				}
				s.unregisterControlAttach(req.RunID, lease.sessionID, lease.attachID)
				_ = applyControlReady(true)
				leaseMu.Unlock()
				record := protocol.DashAttachControl{
					Type: protocol.DashAttachControlFrame, OK: false,
					Error:             "run control was revoked",
					ControlSessionID:  req.ControlSessionID,
					ControlGeneration: lease.generation,
				}
				_ = conn.sendControl(record)
			})
		}
	}
	releaseInteractiveLease := func(reason error, notify bool) {
		var fence func()
		var lease *attachControlLease
		leaseMu.Lock()
		lease = controlLease
		if lease != nil && s.cfg.Control != nil {
			_ = s.cfg.Control.Release(req.RunID, member, lease.sessionID, lease.generation)
			recordRevoked(lease, reason)
			if notify {
				fence = controlFence
			} else {
				controlLease = nil
				controlFence = nil
				s.unregisterControlAttach(req.RunID, lease.sessionID, lease.attachID)
			}
		}
		leaseMu.Unlock()
		if fence != nil {
			fence()
		}
	}
	interactivePolicySteer := func() {
		releaseInteractiveLease(permissions.ErrDenied, true)
	}
	interactivePolicyMembership := func() {
		releaseInteractiveLease(errAttachMembershipRevoked, false)
	}
	if conn.interactive {
		conn.errorHandler = func(err error) {
			_ = conn.sendControl(protocol.DashAttachControl{
				Type: protocol.DashAttachControlFrame, OK: false,
				Code: protocol.CodeParse, Error: "parse error: " + err.Error(),
				ControlSessionID: req.ControlSessionID,
			})
		}
		conn.inputErrorHandler = func(ctl protocol.DashAttachControl, err error) {
			code, message := attachControlError(err)
			_ = conn.sendControl(protocol.DashAttachControl{
				Type: protocol.DashAttachControlFrame, OK: false, Code: code,
				Error: message, ControlSessionID: req.ControlSessionID,
				ControlGeneration: ctl.ControlGeneration,
			})
		}
		conn.inputHandler = func(ctl protocol.DashAttachControl) ([]byte, error) {
			if ctl.Data == "" || ctl.ControlGeneration == 0 || s.cfg.Control == nil {
				return nil, control.ErrStale
			}
			leaseMu.Lock()
			lease := controlLease
			err := control.ErrStale
			if lease != nil && lease.generation == ctl.ControlGeneration {
				err = nil
			} else if revokedGeneration == ctl.ControlGeneration && revokedInputErr != nil {
				err = revokedInputErr
			}
			leaseMu.Unlock()
			if err != nil {
				return nil, err
			}
			return []byte(ctl.Data), nil
		}
		var lastRequestID uint64
		conn.controlHandler = func(ctl protocol.DashAttachControl) {
			record := protocol.DashAttachControl{
				Type: protocol.DashAttachControlFrame, RequestID: ctl.RequestID,
				ControlSessionID: req.ControlSessionID,
			}
			if ctl.RequestID == 0 {
				record.Code = protocol.CodeInvalidParams
				record.Error = "request_id is required"
				_ = conn.sendControl(record)
				return
			}
			if ctl.RequestID <= lastRequestID {
				record.Code = protocol.CodeInvalidParams
				record.Error = "request_id must increase"
				_ = conn.sendControl(record)
				return
			}
			lastRequestID = ctl.RequestID
			if s.cfg.Control == nil {
				record.Code = protocol.CodeDenied
				record.Error = "run control is not enabled"
				_ = conn.sendControl(record)
				return
			}
			if ctl.Write {
				leaseMu.Lock()
				acquired, displaced, err := s.cfg.Control.AcquireAuthorized(
					req.RunID, string(member), req.ControlSessionID, ctl.Takeover,
					ctl.ControlGeneration,
					func() error {
						if err := checkSteer(ctx, s.cfg.Store, member, run.ID); err != nil {
							return err
						}
						return applyControlReady(false)
					},
				)
				if err != nil {
					leaseMu.Unlock()
					record.Code, record.Error = attachControlError(err)
					if current, present := s.cfg.Control.Status(req.RunID); present {
						record.ControlGeneration = current.Generation
					}
					_ = conn.sendControl(record)
					return
				}
				lease := &attachControlLease{sessionID: req.ControlSessionID, generation: acquired.Generation}
				fence := makeInteractiveFence(lease)
				controlLease = lease
				controlFence = fence
				leaseMu.Unlock()
				attachID, registerErr := s.registerControlAttach(req.RunID, lease.sessionID, lease.generation, revoke, fence)
				needFence := false
				leaseMu.Lock()
				current := controlLease
				registered := registerErr == nil && current == lease
				if registered {
					if validateErr := s.cfg.Control.Validate(req.RunID, lease.sessionID, lease.generation); validateErr != nil {
						registerErr = validateErr
						registered = false
						needFence = current == lease
					}
				}
				if registered {
					lease.attachID = attachID
					record.OK = true
					record.HasControl = true
					record.ControlGeneration = acquired.Generation
					_ = conn.sendControl(record)
					leaseMu.Unlock()
					if displaced != nil {
						s.cancelControlAttach(req.RunID, displaced.SessionID, displaced.Generation, errAttachControlRevoked)
					}
					return
				}
				if current == lease && !needFence {
					controlLease = nil
					controlFence = nil
					recordRevoked(lease, control.ErrStale)
				}
				leaseMu.Unlock()
				if needFence {
					fence()
				}
				s.unregisterControlAttach(req.RunID, lease.sessionID, attachID)
				if registerErr == nil {
					registerErr = control.ErrStale
					_ = s.cfg.Control.Release(req.RunID, member, lease.sessionID, lease.generation)
				}
				record.Code, record.Error = attachControlError(registerErr)
				_ = conn.sendControl(record)
				return
			}
			if ctl.Takeover {
				record.Code = protocol.CodeInvalidParams
				record.Error = "takeover requires write"
				_ = conn.sendControl(record)
				return
			}
			if ctl.ControlGeneration == 0 {
				record.Code = protocol.CodeInvalidParams
				record.Error = "control_generation is required"
				_ = conn.sendControl(record)
				return
			}
			leaseMu.Lock()
			err := s.cfg.Control.ReleaseAdmitted(req.RunID, member, req.ControlSessionID, ctl.ControlGeneration, func() error {
				return applyControlReady(true)
			})
			if err != nil {
				leaseMu.Unlock()
				record.Code, record.Error = attachControlError(err)
				record.ControlGeneration = ctl.ControlGeneration
				_ = conn.sendControl(record)
				return
			}
			lease := controlLease
			controlLease = nil
			controlFence = nil
			recordRevoked(lease, control.ErrStale)
			leaseMu.Unlock()
			if lease != nil && lease.generation == ctl.ControlGeneration {
				s.unregisterControlAttach(req.RunID, lease.sessionID, lease.attachID)
			}
			record.OK = true
			record.ControlGeneration = ctl.ControlGeneration
			_ = conn.sendControl(record)
		}
	}
	if conn.interactive {
		conn.startControlWriter(attachCtx, revoke)
		s.spawn(func() { conn.controlWriter() })
	}
	defer func() {
		leaseMu.Lock()
		lease := controlLease
		controlLease = nil
		controlFence = nil
		leaseMu.Unlock()
		if lease == nil {
			return
		}
		s.unregisterControlAttach(req.RunID, lease.sessionID, lease.attachID)
		if errors.Is(context.Cause(attachCtx), errAttachMembershipRevoked) {
			_ = s.cfg.Control.Release(req.RunID, member, lease.sessionID, lease.generation)
			return
		}
		if conn.okWritten() {
			s.cfg.Control.Disconnect(req.RunID, lease.sessionID, lease.generation)
			return
		}
		if err := s.cfg.Control.Release(req.RunID, member, lease.sessionID, lease.generation); err != nil {
			s.cfg.Control.Disconnect(req.RunID, lease.sessionID, lease.generation)
		}
	}()
	// Close before authority cleanup so a blocked control write is released
	// before the lease status is inspected.
	defer func() { _ = conn.Close() }()
	var inputGuard func() error
	var inputAdmission func(func() error) error
	if s.cfg.Control != nil && (wantsControl || conn.interactive) {
		if !conn.interactive {
			inputGuard = func() error {
				leaseMu.Lock()
				lease := controlLease
				leaseMu.Unlock()
				if lease == nil {
					return control.ErrStale
				}
				return s.cfg.Control.Validate(req.RunID, lease.sessionID, lease.generation)
			}
			inputAdmission = func(accept func() error) error {
				leaseMu.Lock()
				lease := controlLease
				leaseMu.Unlock()
				if lease == nil {
					return control.ErrStale
				}
				return s.cfg.Control.AdmitMember(req.RunID, member, lease.sessionID, lease.generation, func() error {
					if err := checkSteer(ctx, s.cfg.Store, member, run.ID); err != nil {
						return err
					}
					return accept()
				})
			}
		} else {
			inputAdmission = func(accept func() error) error {
				ctl, ok := conn.pendingInput()
				if !ok {
					return control.ErrStale
				}
				leaseMu.Lock()
				lease := controlLease
				err := control.ErrStale
				if lease != nil && lease.generation == ctl.ControlGeneration {
					err = nil
				} else if revokedGeneration == ctl.ControlGeneration && revokedInputErr != nil {
					err = revokedInputErr
				}
				leaseMu.Unlock()
				if err != nil {
					conn.reportPendingInputError(err)
					return nil
				}
				var policyErr error
				err = s.cfg.Control.AdmitMember(req.RunID, member, req.ControlSessionID, ctl.ControlGeneration, func() error {
					policyErr = checkSteer(ctx, s.cfg.Store, member, run.ID)
					if policyErr != nil {
						return policyErr
					}
					return accept()
				})
				if policyErr != nil {
					releaseInteractiveLease(policyErr, true)
					conn.reportPendingInputError(policyErr)
					return nil
				}
				if err != nil {
					conn.reportPendingInputError(err)
					if errors.Is(err, control.ErrStale) ||
						errors.Is(err, permissions.ErrDenied) ||
						errors.Is(err, errMemberRemoved) ||
						errors.Is(err, errMemberPending) ||
						errors.Is(err, store.ErrNotFound) {
						return nil
					}
				}
				return err
			}
		}
	}
	errCh := make(chan error, 1)
	go func() {
		errCh <- s.cfg.PTY.Attach(attachCtx, key, ptyhost.AttachClient{
			Cols:     cols,
			Rows:     rows,
			ReadOnly: readOnly,
			Member:   member,
			SessionGeneration: func() uint64 {
				if shellReservation == nil {
					return 0
				}
				return shellReservation.Generation()
			}(),
			OnControlReady: func(setReadOnly func(bool) error) {
				setControlReady(setReadOnly)
			},
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
			Screen:         req.Screen,
			Follow:         req.Follow,
			Resume:         req.Resume,
			Cursor:         req.Cursor,
			ResumeID:       req.ResumeID,
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
			if _, isRunSession := key.Run(); isRunSession && replayableStatus(run.Status) && s.serveReplay(ch, run, cols, rows, req.Framed, req.Screen, controlSnap, controlHeld) {
				return
			}
			e := rpcError(attachErr)
			_ = writeJSONLine(ch, protocol.AttachResponse{OK: false, Code: e.Code, Error: e.Message})
		} else {
			ch.exit(1)
		}
		return
	}
	s.publishPresence(run, member, events.PresenceWatching)
	if !returned {
		leaseMu.Lock()
		lease := controlLease
		leaseMu.Unlock()
		if req.Interactive {
			s.spawn(func() {
				s.revokeOnPolicyChange(attachCtx, revoke, member, run.ID, false, interactivePolicySteer, interactivePolicyMembership)
			})
		} else {
			s.spawn(func() { s.revokeOnPolicyChange(attachCtx, revoke, member, run.ID, readOnly) })
		}
		if req.Interactive {
			s.spawn(func() {
				s.revokeOnCurrentControlChange(attachCtx, revoke, string(run.ID), func() (*attachControlLease, func()) {
					leaseMu.Lock()
					defer leaseMu.Unlock()
					return controlLease, controlFence
				})
			})
		} else if lease != nil {
			s.spawn(func() {
				s.revokeOnControlChange(attachCtx, revoke, string(run.ID), lease.sessionID, lease.generation)
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
func (s *Server) serveReplay(ch subsystemConn, run *domain.Run, cols, rows uint, framed, screen bool, controlSnap control.Snapshot, controlHeld bool) bool {
	var (
		rc          io.ReadCloser
		replayBytes int
		err         error
	)
	if framed {
		snap, snapErr := s.cfg.PTY.Snapshot(run.ID)
		if snapErr != nil {
			slog.Warn("sshd: snapshot finished run", "run", run.ID, "error", snapErr)
			return false
		}
		cols, rows = snap.Cols, snap.Rows
		if screen {
			replayBytes = len(snap.Data)
			rc = io.NopCloser(bytes.NewReader(snap.Data))
		}
	}
	if rc == nil {
		rc, replayBytes, err = s.cfg.PTY.Replay(run.ID)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				slog.Warn("sshd: open transcript for attach replay", "run", run.ID, "error", err)
			}
			return false
		}
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
// the most direct access to a running agent. An interactive attach downgrades
// to a mirror on steer loss and keeps the same watcher for later membership
// loss. The optional callbacks are steer-loss and membership-loss handlers.
func (s *Server) revokeOnPolicyChange(ctx context.Context, revoke context.CancelCauseFunc, member domain.MemberID, run domain.RunID, readOnly bool, callbacks ...func()) {
	ticker := time.NewTicker(s.cfg.revalidateInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if cause := s.attachRevocation(ctx, member, run, readOnly); cause != nil {
				if errors.Is(cause, errAttachMembershipRevoked) {
					revoke(cause)
					if len(callbacks) > 1 && callbacks[1] != nil {
						callbacks[1]()
					}
					return
				}
				if errors.Is(cause, errAttachSteerRevoked) && len(callbacks) > 0 && callbacks[0] != nil {
					callbacks[0]()
					continue
				}
				revoke(cause)
				return
			}
		}
	}
}

// revokeOnControlChange fences a live attach when another session takes over,
// releases the lease, or the reconnect window expires. The PTY input guard
// independently checks every buffered read.
func (s *Server) revokeOnControlChange(ctx context.Context, revoke context.CancelCauseFunc, run, session string, generation uint64, onFence ...func()) {
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
				if len(onFence) > 0 && onFence[0] != nil {
					onFence[0]()
				} else {
					revoke(errAttachControlRevoked)
				}
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
