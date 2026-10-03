package sshd

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

const (
	takeoverHold   = 5 * time.Second
	takeoverReview = 7 * time.Second
)

// The coordinator lock precedes attach lease locks and control admission. No
// control callback calls back into the coordinator; lease changes wake watchers
// through Snapshot.ConnectionDone instead. Notifications only enqueue bounded records.
type takeoverCoordinator struct {
	mu       sync.Mutex
	attaches map[domain.RunID]map[*takeoverAttach]struct{}
	pending  map[domain.RunID]*pendingTakeover
	now      func() time.Time
}

type takeoverAttach struct {
	ctx     context.Context
	run     *domain.Run
	member  domain.MemberID
	session string
	send    func(protocol.DashAttachControl) error
	owns    func(uint64) bool
	grant   func(*pendingTakeover) error
}

type pendingTakeover struct {
	id               string
	requester        *takeoverAttach
	holder           *takeoverAttach
	lease            control.Snapshot
	phase            string
	started          time.Time
	holdDeadline     time.Time
	decisionDeadline time.Time
	done             chan struct{}
	changed          chan struct{}
}

func (c *takeoverCoordinator) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func (s *Server) registerTakeoverAttach(a *takeoverAttach) func() {
	c := &s.takeovers
	c.mu.Lock()
	if c.attaches == nil {
		c.attaches = make(map[domain.RunID]map[*takeoverAttach]struct{})
		c.pending = make(map[domain.RunID]*pendingTakeover)
	}
	if c.attaches[a.run.ID] == nil {
		c.attaches[a.run.ID] = make(map[*takeoverAttach]struct{})
	}
	c.attaches[a.run.ID][a] = struct{}{}
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		delete(c.attaches[a.run.ID], a)
		if len(c.attaches[a.run.ID]) == 0 {
			delete(c.attaches, a.run.ID)
		}
		if p := c.pending[a.run.ID]; p != nil && (p.requester == a || p.holder == a) {
			c.finish(p, "cancelled", context.Canceled, nil, 0)
		}
	}
}

func (s *Server) authorizeTakeover(a *takeoverAttach) error {
	if err := a.ctx.Err(); err != nil {
		return err
	}
	if err := s.checkMember(a.ctx, a.member); err != nil {
		return err
	}
	current, err := s.cfg.Store.GetRun(a.ctx, a.run.ID)
	if err != nil {
		return err
	}
	if !current.CreatedAt.Equal(a.run.CreatedAt) || current.Status.Terminal() {
		return control.ErrStale
	}
	return checkSteer(a.ctx, s.cfg.Store, a.member, a.run.ID)
}

func (c *takeoverCoordinator) notify(p *pendingTakeover, recipient *takeoverAttach, requestID uint64, err error) {
	state := &protocol.TakeoverState{
		ID: p.id, RequesterMemberID: string(p.requester.member),
		RequesterSessionID: p.requester.session, HolderSessionID: p.lease.SessionID,
		HolderGeneration: p.lease.Generation, Phase: p.phase,
		HoldStartedAt: p.started.UTC().Format(time.RFC3339Nano),
		HoldDeadline:  p.holdDeadline.UTC().Format(time.RFC3339Nano),
		ServerNow:     c.clock().UTC().Format(time.RFC3339Nano),
	}
	if !p.decisionDeadline.IsZero() {
		state.DecisionDeadline = p.decisionDeadline.UTC().Format(time.RFC3339Nano)
	}
	for _, a := range []*takeoverAttach{p.requester, p.holder} {
		if a == nil {
			continue
		}
		record := protocol.DashAttachControl{Type: protocol.DashAttachTakeover, OK: err == nil, TakeoverState: state}
		if a == recipient {
			record.RequestID = requestID
		}
		if err != nil {
			record.Code, record.Error = attachControlError(err)
		}
		_ = a.send(record)
	}
}

func (c *takeoverCoordinator) finish(p *pendingTakeover, phase string, err error, recipient *takeoverAttach, requestID uint64) {
	p.phase = phase
	delete(c.pending, p.requester.run.ID)
	close(p.done)
	c.notify(p, recipient, requestID, err)
}

func (s *Server) validateTakeover(p *pendingTakeover) error {
	if err := s.authorizeTakeover(p.requester); err != nil {
		return err
	}
	if err := s.checkMember(p.requester.ctx, p.lease.MemberID); err != nil {
		return err
	}
	if err := checkSteer(p.requester.ctx, s.cfg.Store, p.lease.MemberID, p.requester.run.ID); err != nil {
		return err
	}
	if p.holder != nil && p.holder.ctx.Err() != nil {
		return control.ErrStale
	}
	if current, present := s.cfg.Control.Status(string(p.requester.run.ID)); !present ||
		!current.Connected || current.ConnectionDone != p.lease.ConnectionDone ||
		current.MemberID != p.lease.MemberID || current.SessionID != p.lease.SessionID || current.Generation != p.lease.Generation {
		return control.ErrStale
	}
	return nil
}

func takeoverRefusal(a *takeoverAttach, requestID uint64, code int, message string) {
	_ = a.send(protocol.DashAttachControl{Type: protocol.DashAttachTakeover, RequestID: requestID, Code: code, Error: message})
}

func (s *Server) handleTakeover(a *takeoverAttach, ctl protocol.DashAttachControl) {
	c := &s.takeovers
	c.mu.Lock()
	defer c.mu.Unlock()
	refuse := func(message string) { takeoverRefusal(a, ctl.RequestID, protocol.CodeConflict, message) }
	if s.cfg.Control == nil {
		takeoverRefusal(a, ctl.RequestID, protocol.CodeDenied, "run control is not enabled")
		return
	}
	if a.session == "" {
		takeoverRefusal(a, ctl.RequestID, protocol.CodeInvalidParams, "control_session_id is required")
		return
	}
	if err := uuid.Validate(ctl.TakeoverID); err != nil {
		takeoverRefusal(a, ctl.RequestID, protocol.CodeInvalidParams, "takeover_id must be a UUID")
		return
	}
	if _, live := c.attaches[a.run.ID][a]; !live || a.ctx.Err() != nil {
		refuse("takeover attachment is no longer live")
		return
	}
	p := c.pending[a.run.ID]
	if p != nil {
		if err := s.validateTakeover(p); err != nil {
			c.finish(p, "cancelled", err, nil, 0)
			p = nil
		}
	}
	if ctl.Action == "start" {
		if p != nil {
			refuse("another takeover is already pending")
			return
		}
		if err := s.authorizeTakeover(a); err != nil {
			code, message := attachControlError(err)
			takeoverRefusal(a, ctl.RequestID, code, message)
			return
		}
		lease, present := s.cfg.Control.Status(string(a.run.ID))
		if !present || (lease.MemberID == a.member && lease.SessionID == a.session) {
			refuse("takeover requires another controller session")
			return
		}
		if !lease.Connected {
			refuse("controller session is disconnected")
			return
		}
		var holder *takeoverAttach
		interactiveHolder := false
		for candidate := range c.attaches[a.run.ID] {
			if candidate.member != lease.MemberID || candidate.session != lease.SessionID {
				continue
			}
			interactiveHolder = true
			if candidate.ctx.Err() == nil && candidate.owns(lease.Generation) {
				holder = candidate
				break
			}
		}
		if interactiveHolder && holder == nil {
			refuse("controller attachment is not ready")
			return
		}
		now := c.clock()
		p = &pendingTakeover{id: ctl.TakeoverID, requester: a, holder: holder, lease: lease, phase: "holding", started: now,
			holdDeadline: now.Add(takeoverHold), done: make(chan struct{}), changed: make(chan struct{}, 1)}
		c.pending[a.run.ID] = p
		c.notify(p, a, ctl.RequestID, nil)
		s.spawn(func() { s.watchTakeover(p) })
		return
	}
	if p == nil || p.id != ctl.TakeoverID {
		refuse("takeover request is stale")
		return
	}
	switch ctl.Action {
	case "confirm":
		if p.requester != a || p.phase != "holding" {
			refuse("only the requesting attachment can confirm its hold")
			return
		}
		if c.clock().Before(p.holdDeadline) {
			refuse("takeover hold has not completed")
			return
		}
		p.phase = "review"
		p.decisionDeadline = c.clock().Add(takeoverReview)
		c.notify(p, a, ctl.RequestID, nil)
		p.changed <- struct{}{}
	case "cancel":
		if p.requester != a {
			refuse("only the requesting attachment can cancel")
			return
		}
		c.finish(p, "cancelled", nil, a, ctl.RequestID)
	case "accept", "deny":
		if p.holder != a || p.phase != "review" || ctl.ControlGeneration != p.lease.Generation {
			refuse("only the targeted controller can decide this takeover")
			return
		}
		expired := false
		err := s.cfg.Control.AdmitMember(string(a.run.ID), a.member, a.session, ctl.ControlGeneration, func() error {
			expired = !c.clock().Before(p.decisionDeadline)
			if expired {
				return nil
			}
			if err := s.authorizeTakeover(a); err != nil {
				return err
			}
			if ctl.Action == "deny" {
				c.finish(p, "denied", nil, a, ctl.RequestID)
			}
			return nil
		})
		if err != nil {
			c.finish(p, "cancelled", err, a, ctl.RequestID)
			return
		}
		if expired {
			s.grantTakeover(p, nil, 0)
			refuse("takeover decision deadline has elapsed")
			return
		}
		if ctl.Action == "accept" {
			s.grantTakeover(p, a, ctl.RequestID)
		}
	default:
		takeoverRefusal(a, ctl.RequestID, protocol.CodeInvalidParams, "unknown takeover action")
	}
}

// grantTakeover holds the coordinator lock through admission and acknowledgement.
// The grant closure rechecks requester authority and the captured holder fence
// in the same control admission boundary as readiness and mission takeover.
func (s *Server) grantTakeover(p *pendingTakeover, recipient *takeoverAttach, requestID uint64) {
	if err := s.validateTakeover(p); err != nil {
		s.takeovers.finish(p, "cancelled", err, recipient, requestID)
		return
	}
	if err := p.requester.grant(p); err != nil {
		s.takeovers.finish(p, "cancelled", err, recipient, requestID)
		return
	}
	s.takeovers.finish(p, "granted", nil, recipient, requestID)
}

// advanceTakeover is shared by the server timer and deterministic clock tests.
func (s *Server) advanceTakeover(p *pendingTakeover) {
	c := &s.takeovers
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pending[p.requester.run.ID] != p {
		return
	}
	if err := s.validateTakeover(p); err != nil {
		c.finish(p, "cancelled", err, nil, 0)
		return
	}
	if p.phase == "review" && !c.clock().Before(p.decisionDeadline) {
		s.grantTakeover(p, nil, 0)
	}
}

func (s *Server) watchTakeover(p *pendingTakeover) {
	interval := s.cfg.revalidateInterval
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var holderDone <-chan struct{}
	if p.holder != nil {
		holderDone = p.holder.ctx.Done()
	}
	timer := time.NewTimer(takeoverReview)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()
	for {
		select {
		case <-p.done:
			return
		case <-p.requester.ctx.Done():
			s.advanceTakeover(p)
		case <-holderDone:
			s.advanceTakeover(p)
		case <-p.lease.ConnectionDone:
			s.advanceTakeover(p)
		case <-ticker.C:
			s.advanceTakeover(p)
		case <-p.changed:
			s.takeovers.mu.Lock()
			remaining := p.decisionDeadline.Sub(s.takeovers.clock())
			s.takeovers.mu.Unlock()
			timer.Reset(remaining)
		case <-timer.C:
			s.advanceTakeover(p)
		}
	}
}
