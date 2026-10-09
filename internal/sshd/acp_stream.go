package sshd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/scheduler"
)

const maxACPViewers = 32

var errACPViewerLimit = errors.New("sshd: too many viewers on this enhanced run")

// acpStream is one aether-acp channel: the item stream out, lease and
// takeover control frames both ways.
type acpStream struct {
	s       *Server
	ctx     context.Context
	revoke  context.CancelCauseFunc
	ch      subsystemConn
	member  domain.MemberID
	run     *domain.Run
	session string

	writeMu sync.Mutex

	mu       sync.Mutex
	lease    *control.Snapshot
	attachID uint64
	endpoint *takeoverAttach
}

func (a *acpStream) write(v any) error {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	return writeJSONLine(a.ch, v)
}

func (a *acpStream) sendControl(c protocol.DashAttachControl) error { return a.write(c) }

// serveACP serves an aether-acp channel: one header line, an ack, then one
// protocol.ACPFrame or control frame per line until the session stream ends.
func (s *Server) serveACP(ctx context.Context, member domain.MemberID, ch subsystemConn) {
	defer func() { _ = ch.Close() }()
	capped := &capReader{r: ch, left: maxSubsystemHeaderBytes}
	r := bufio.NewReaderSize(capped, 4<<10)
	line, err := protocol.ReadLine(r)
	if err != nil {
		return
	}
	capped.left = -1
	refuse := func(code int, message string) {
		_ = writeJSONLine(ch, protocol.ACPStreamResponse{Code: code, Error: message})
	}
	var req protocol.ACPStreamRequest
	if err = json.Unmarshal(line, &req); err != nil {
		refuse(protocol.CodeParse, "parse error: "+err.Error())
		return
	}
	if req.RunID == "" {
		refuse(protocol.CodeInvalidParams, "run_id is required")
		return
	}
	if err = s.checkMember(ctx, member); err != nil {
		e := rpcError(err)
		refuse(e.Code, e.Message)
		return
	}
	run, err := s.cfg.Store.GetRun(ctx, domain.RunID(req.RunID))
	if err != nil {
		e := rpcError(err)
		refuse(e.Code, e.Message)
		return
	}
	if !run.ACP {
		refuse(protocol.CodeInvalidParams, "run "+req.RunID+" does not run its agent over ACP")
		return
	}
	if (req.Write || req.ReleaseControl) && req.ControlSessionID == "" {
		refuse(protocol.CodeInvalidParams, "control_session_id is required")
		return
	}
	if !s.acpViewerEnter(run.ID) {
		refuse(protocol.CodeConflict, errACPViewerLimit.Error())
		return
	}
	defer s.acpViewerLeave(run.ID)

	ctx, revoke := context.WithCancelCause(ctx)
	defer revoke(nil)
	a := &acpStream{s: s, ctx: ctx, revoke: revoke, ch: ch, member: member, run: run, session: req.ControlSessionID}
	ack := protocol.ACPStreamResponse{OK: true}
	if s.cfg.Control != nil {
		switch {
		case req.ReleaseControl:
			if err = a.release(req.ControlGeneration); err != nil {
				ack.Code, ack.Error = attachControlError(err)
				ack.OK = false
			}
		case req.Write:
			if err = a.acquire(req.Takeover, req.ControlGeneration, nil); err != nil {
				ack.Code, ack.Error = attachControlError(err)
				ack.OK = false
			}
		}
	}
	defer a.leave()
	if !ack.OK {
		_ = a.write(ack)
		return
	}

	stream, err := s.cfg.Runs.ACPSubscribe(run.ID, req.AfterSeq)
	if err != nil {
		e := rpcError(err)
		_ = a.write(protocol.ACPStreamResponse{Code: e.Code, Error: e.Message})
		return
	}
	defer stream.Cancel()
	ack.Seq, ack.Epoch, ack.Replay, ack.Live = stream.Seq, stream.Epoch, len(stream.Replay), stream.Items != nil
	ack.OldestSeq = stream.OldestSeq
	ack.TruncatedBefore = stream.TruncatedBefore
	if stream.State != nil {
		ack.State, _ = json.Marshal(stream.State)
	}
	a.fillAck(&ack)
	if a.write(ack) != nil {
		return
	}
	if stream.Reset && a.write(protocol.ACPFrame{Reset: true, Epoch: stream.Epoch}) != nil {
		return
	}
	for _, it := range stream.Replay {
		if a.item(it) != nil {
			return
		}
	}

	a.endpoint = &takeoverAttach{
		ctx: ctx, run: run, member: member, session: req.ControlSessionID, send: a.sendControl,
		owns: func(generation uint64) bool {
			a.mu.Lock()
			defer a.mu.Unlock()
			return a.lease != nil && a.lease.Generation == generation
		},
		grant: func(p *pendingTakeover) error {
			if err := a.acquire(true, 0, p); err != nil {
				return err
			}
			// Keep the grant acknowledgement ahead of any later revocation.
			a.writeMu.Lock()
			defer a.writeMu.Unlock()
			a.mu.Lock()
			lease := a.lease
			a.mu.Unlock()
			if lease == nil {
				return control.ErrStale
			}
			return writeJSONLine(a.ch, protocol.DashAttachControl{
				Type: protocol.DashAttachControlFrame, OK: true, HasControl: true,
				ControlSessionID: lease.SessionID, ControlGeneration: lease.Generation,
			})
		},
	}
	unregister := s.registerTakeoverAttach(a.endpoint)
	defer unregister()
	s.publishPresence(run, member, events.PresenceWatching)
	defer s.publishPresence(run, member, events.PresenceOnline)
	s.spawn(func() { a.readControl(r) })
	s.spawn(func() { a.watchPolicy() })

	status := a.pump(stream)
	switch cause := context.Cause(ctx); {
	case errors.Is(cause, errAttachMembershipRevoked):
		status = protocol.AttachExitMembershipRevoked
	case errors.Is(cause, errAttachSteerRevoked):
		status = protocol.AttachExitSteerRevoked
	}
	ch.exit(status)
}

// pump streams live items until the session ends, the viewer falls behind,
// or the channel closes; each makes the client resubscribe.
func (a *acpStream) pump(stream scheduler.ACPStream) int {
	if stream.Items == nil {
		select {
		case <-stream.Started:
		case <-a.ctx.Done():
		}
		return 0
	}
	for {
		select {
		case <-a.ctx.Done():
			return 0
		case it, ok := <-stream.Items:
			if !ok {
				return 0
			}
			if a.item(it) != nil {
				return 1
			}
		}
	}
}

func (a *acpStream) item(it acphost.Item) error {
	b, truncated, err := it.Wire(protocol.ACPWireItemBytes)
	if err != nil {
		return err
	}
	return a.write(protocol.ACPFrame{Seq: it.Seq, Item: b, Truncated: truncated})
}

func (a *acpStream) fillAck(ack *protocol.ACPStreamResponse) {
	if a.s.cfg.Control == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.lease != nil {
		ack.HasControl, ack.ControlSessionID, ack.ControlGeneration = true, a.lease.SessionID, a.lease.Generation
		return
	}
	if current, ok := a.s.cfg.Control.Status(string(a.run.ID)); ok {
		ack.ControlGeneration = current.Generation
	}
}

// acquire takes the run's control lease for this stream, through the timed
// takeover protocol when p is set.
func (a *acpStream) acquire(force bool, generation uint64, p *pendingTakeover) error {
	s, run := a.s, string(a.run.ID)
	var acquired control.Snapshot
	var displaced *control.Snapshot
	var err error
	if p != nil {
		acquired, displaced, err = s.cfg.Control.AcquireTakeoverAuthorized(run, string(a.member), a.session, &p.lease,
			func() error { return s.authorizeTakeover(a.endpoint) })
	} else {
		acquired, displaced, err = s.cfg.Control.AcquireInteractiveAuthorized(run, string(a.member), a.session, force, generation,
			func() error { return checkSteer(a.ctx, s.cfg.Store, a.member, a.run.ID) })
	}
	if err != nil {
		return err
	}
	if mission := s.cfg.Services.MissionControl; mission != nil {
		if err = mission.Takeover(a.ctx, a.run.ID, a.member); err != nil {
			_ = s.cfg.Control.Release(run, a.member, acquired.SessionID, acquired.Generation)
			return err
		}
	}
	lease := acquired
	id, err := s.registerControlAttach(run, a.session, acquired.Generation, a.revoke, func() { a.fence(&lease) })
	if err != nil {
		s.unregisterControlAttach(run, a.session, id)
		_ = s.cfg.Control.Release(run, a.member, acquired.SessionID, acquired.Generation)
		return err
	}
	a.mu.Lock()
	a.lease, a.attachID = &lease, id
	a.mu.Unlock()
	if displaced != nil {
		s.cancelControlAttach(run, displaced.SessionID, displaced.Generation, errAttachControlRevoked)
	}
	return nil
}

// release hands the lease back; the stream stays a viewer.
func (a *acpStream) release(generation uint64) error {
	s := a.s
	err := s.cfg.Control.ReleaseAdmitted(string(a.run.ID), a.member, a.session, generation, func() error {
		if mission := s.cfg.Services.MissionControl; mission != nil {
			return mission.Release(a.ctx, a.run.ID, a.member)
		}
		return nil
	})
	if err != nil {
		return err
	}
	a.mu.Lock()
	lease := a.lease
	a.lease = nil
	a.mu.Unlock()
	if lease != nil && lease.Generation == generation {
		s.unregisterControlAttach(string(a.run.ID), a.session, a.attachID)
	}
	s.cancelControlAttach(string(a.run.ID), a.session, generation, errAttachControlRevoked)
	return nil
}

// fence drops a lease another session took or the server revoked, and tells
// the client; the stream stays open as a viewer.
func (a *acpStream) fence(lease *control.Snapshot) {
	a.mu.Lock()
	if a.lease == nil || a.lease.Generation != lease.Generation {
		a.mu.Unlock()
		return
	}
	a.lease = nil
	id := a.attachID
	a.mu.Unlock()
	a.s.unregisterControlAttach(string(a.run.ID), a.session, id)
	_ = a.sendControl(protocol.DashAttachControl{
		Type: protocol.DashAttachControlFrame, Error: "run control was revoked",
		ControlSessionID: a.session, ControlGeneration: lease.Generation,
		RevocationReason: string(lease.RevocationReason()),
	})
}

// leave disconnects a held lease so the same session can reclaim it within
// the reconnect window.
func (a *acpStream) leave() {
	a.mu.Lock()
	lease := a.lease
	a.lease = nil
	a.mu.Unlock()
	if lease == nil {
		return
	}
	a.s.unregisterControlAttach(string(a.run.ID), a.session, a.attachID)
	if errors.Is(context.Cause(a.ctx), errAttachMembershipRevoked) {
		_ = a.s.cfg.Control.Release(string(a.run.ID), a.member, lease.SessionID, lease.Generation)
		return
	}
	a.s.cfg.Control.Disconnect(string(a.run.ID), lease.SessionID, lease.Generation)
}

// readControl answers the client's control and takeover frames, the same
// frames an interactive attach takes.
func (a *acpStream) readControl(r *bufio.Reader) {
	defer a.revoke(nil)
	var last uint64
	for {
		line, err := protocol.ReadLine(r)
		if err != nil {
			return
		}
		var ctl protocol.DashAttachControl
		if err = json.Unmarshal(line, &ctl); err != nil {
			_ = a.sendControl(protocol.DashAttachControl{Type: protocol.DashAttachControlFrame, Code: protocol.CodeParse, Error: "parse error: " + err.Error()})
			continue
		}
		record := protocol.DashAttachControl{Type: ctl.Type, RequestID: ctl.RequestID, ControlSessionID: a.session}
		switch {
		case ctl.Type != protocol.DashAttachControlFrame && ctl.Type != protocol.DashAttachTakeover:
			record.Code, record.Error = protocol.CodeInvalidParams, "unknown frame type"
		case a.s.cfg.Control == nil:
			record.Code, record.Error = protocol.CodeDenied, "run control is not enabled"
		case a.session == "":
			record.Code, record.Error = protocol.CodeInvalidParams, "control_session_id is required"
		case ctl.RequestID <= last:
			record.Code, record.Error = protocol.CodeInvalidParams, "request_id must increase"
		case ctl.Type == protocol.DashAttachTakeover:
			last = ctl.RequestID
			a.s.handleTakeover(a.endpoint, ctl)
			continue
		case ctl.Write:
			last = ctl.RequestID
			err = a.acquire(ctl.Takeover, ctl.ControlGeneration, nil)
		default:
			last = ctl.RequestID
			err = a.release(ctl.ControlGeneration)
		}
		if record.Error == "" {
			if err != nil {
				record.Code, record.Error = attachControlError(err)
			} else {
				record.OK = true
			}
			a.mu.Lock()
			if a.lease != nil {
				record.HasControl, record.ControlGeneration = true, a.lease.Generation
			}
			a.mu.Unlock()
		}
		_ = a.sendControl(record)
	}
}

// watchPolicy ends the stream when the member loses membership and drops
// the lease, keeping the stream, when they lose Steer or the lease expires.
func (a *acpStream) watchPolicy() {
	interval := a.s.cfg.revalidateInterval
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
		}
		a.mu.Lock()
		lease := a.lease
		a.mu.Unlock()
		switch cause := a.s.attachRevocation(a.ctx, a.member, a.run.ID, lease == nil); {
		case errors.Is(cause, errAttachMembershipRevoked):
			a.revoke(cause)
			return
		case cause != nil:
			_ = a.s.cfg.Control.RevokeMember(string(a.run.ID), a.member, lease.SessionID, lease.Generation)
			a.fence(lease)
		case lease != nil:
			if err := a.s.cfg.Control.Validate(string(a.run.ID), lease.SessionID, lease.Generation); err != nil {
				a.fence(lease)
			}
		}
	}
}

func (s *Server) acpViewerEnter(run domain.RunID) bool {
	s.acpViewersMu.Lock()
	defer s.acpViewersMu.Unlock()
	if s.acpViewers == nil {
		s.acpViewers = make(map[domain.RunID]int)
	}
	if s.acpViewers[run] >= maxACPViewers {
		return false
	}
	s.acpViewers[run]++
	return true
}

func (s *Server) acpViewerLeave(run domain.RunID) {
	s.acpViewersMu.Lock()
	defer s.acpViewersMu.Unlock()
	if s.acpViewers[run]--; s.acpViewers[run] <= 0 {
		delete(s.acpViewers, run)
	}
}
