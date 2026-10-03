package sshd

import (
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func awaitTakeoverLease(t *testing.T, e *testEnv, ready func(control.Snapshot, bool) bool) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		current, present := e.srv.cfg.Control.Status(string(e.run.ID))
		if ready(current, present) {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("lease did not reach the required state: %+v, present=%v", current, present)
		case <-tick.C:
		}
	}
}

func TestTimedTakeoverRefusesUnacknowledgedHolder(t *testing.T) {
	e := controlAttachEnv(t)
	e.srv.cfg.PTY = &interactiveTestPTY{fakePTY: e.pty}
	holder, _ := openInteractiveSSHAttach(t, e, e.signer, protocol.AttachRequest{ControlSessionID: "holder", ReadOnly: true})
	requester, _ := openInteractiveSSHAttach(t, e, e.signer, protocol.AttachRequest{ControlSessionID: "requester", ReadOnly: true})
	e.srv.controlMu.Lock()
	unlock := sync.OnceFunc(e.srv.controlMu.Unlock)
	defer unlock()
	holder.send(t, protocol.DashAttachControl{Type: protocol.DashAttachControlFrame, RequestID: 1, Write: true})
	awaitTakeoverLease(t, e, func(s control.Snapshot, present bool) bool { return present && s.SessionID == "holder" })
	requester.send(t, takeoverCommand("start", 1, 0))
	_, refused := requester.next(t)
	unlock()
	if refused == nil || refused.OK || refused.Code != protocol.CodeConflict || refused.TakeoverState != nil {
		t.Fatalf("takeover during holder registration = %+v", refused)
	}
	_, granted := holder.next(t)
	if granted == nil || !granted.OK || !granted.HasControl {
		t.Fatalf("holder acquisition = %+v", granted)
	}
	requester.send(t, takeoverCommand("start", 2, 0))
	expectTakeover(t, requester, "holding")
	expectTakeover(t, holder, "holding")
}

func TestTimedTakeoverRefusesPendingInitialAck(t *testing.T) {
	e := controlAttachEnv(t)
	pty := &interactiveTestPTY{fakePTY: e.pty}
	e.srv.cfg.PTY = pty
	requester, _ := openInteractiveSSHAttach(t, e, e.signer, protocol.AttachRequest{ControlSessionID: "requester", ReadOnly: true})
	entered, resume := make(chan struct{}), make(chan struct{})
	proceed := sync.OnceFunc(func() { close(resume) })
	defer proceed()
	pty.mu.Lock()
	pty.beforeAttach = func() { close(entered); <-resume }
	pty.mu.Unlock()
	ready := make(chan *interactiveSSHAttach, 1)
	go func() {
		holder, _ := openInteractiveSSHAttach(t, e, e.signer, protocol.AttachRequest{ControlSessionID: "holder"})
		ready <- holder
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("holder did not reach the initial acknowledgement gap")
	}
	requester.send(t, takeoverCommand("start", 1, 0))
	_, refused := requester.next(t)
	proceed()
	holder := <-ready
	if refused == nil || refused.OK || refused.Code != protocol.CodeConflict || refused.TakeoverState != nil {
		t.Fatalf("takeover before initial acknowledgement = %+v", refused)
	}
	requester.send(t, takeoverCommand("start", 2, 0))
	expectTakeover(t, requester, "holding")
	expectTakeover(t, holder, "holding")
}

func TestTimedTakeoverHolderDisconnectBeforeGrant(t *testing.T) {
	e := controlAttachEnv(t)
	clock := takeoverClock(e)
	holder, requester, generation := openTakeoverPair(t, e)
	beginTakeover(t, holder, requester)
	confirmTakeover(t, holder, requester, clock, 2)
	pending := activeTakeover(t, e)
	entered, resume, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	proceed := sync.OnceFunc(func() { close(resume) })
	defer proceed()
	e.srv.takeovers.mu.Lock()
	grant := pending.requester.grant
	pending.requester.grant = func(takeover *pendingTakeover) error {
		close(entered)
		<-resume
		return grant(takeover)
	}
	e.srv.takeovers.mu.Unlock()
	clock.Advance(takeoverReview)
	go func() { e.srv.advanceTakeover(pending); close(finished) }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("grant did not reach admission gap")
	}
	_ = holder.pipe.Close()
	select {
	case <-pending.holder.ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("holder attachment did not cancel")
	}
	awaitTakeoverLease(t, e, func(s control.Snapshot, present bool) bool { return present && s.SessionID == "holder" && !s.Connected })
	proceed()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("grant did not finish")
	}
	current, present := e.srv.cfg.Control.Status(string(e.run.ID))
	if !present || current.SessionID != "holder" || current.Generation != generation || current.Connected {
		t.Fatalf("disconnect lost the holder's reconnect lease: %+v", current)
	}
}

func TestTimedTakeoverRawHolderDisconnect(t *testing.T) {
	for _, reconnect := range []bool{false, true} {
		t.Run(map[bool]string{false: "reserved", true: "reconnected"}[reconnect], func(t *testing.T) {
			e := controlAttachEnv(t)
			clock := takeoverClock(e)
			e.srv.cfg.PTY = &interactiveTestPTY{fakePTY: e.pty}
			raw, ack := rawAttachRequest(t, e, e.signer, protocol.AttachRequest{ControlSessionID: "cli"}, true)
			if !ack.OK || !ack.HasControl {
				t.Fatalf("raw holder = %+v", ack)
			}
			requester, _ := openInteractiveSSHAttach(t, e, e.signer, protocol.AttachRequest{ControlSessionID: "requester", ReadOnly: true})
			beginTakeover(t, nil, requester)
			confirmTakeover(t, nil, requester, clock, 2)
			pending := activeTakeover(t, e)
			e.srv.takeovers.mu.Lock()
			unlock := sync.OnceFunc(e.srv.takeovers.mu.Unlock)
			defer unlock()
			_ = raw.ch.Close()
			awaitTakeoverLease(t, e, func(s control.Snapshot, present bool) bool { return present && s.SessionID == "cli" && !s.Connected })
			if reconnect {
				replacement, resumed := rawAttachRequest(t, e, e.signer, protocol.AttachRequest{ControlSessionID: "cli"}, true)
				defer func() { _ = replacement.ch.Close() }()
				if !resumed.OK || !resumed.HasControl || resumed.ControlGeneration != ack.ControlGeneration {
					t.Fatalf("raw reconnect = %+v", resumed)
				}
			}
			clock.Advance(takeoverReview)
			unlock()
			e.srv.advanceTakeover(pending)
			expectTakeover(t, requester, "cancelled")
			current, present := e.srv.cfg.Control.Status(string(e.run.ID))
			if !present || current.SessionID != "cli" || current.Generation != ack.ControlGeneration || current.Connected != reconnect {
				t.Fatalf("pending takeover displaced the CLI reconnect lease: %+v", current)
			}
		})
	}
}

func TestTimedTakeoverQueueFailurePreservesHolder(t *testing.T) {
	e := controlAttachEnv(t)
	clock := takeoverClock(e)
	holder, requester, generation := openTakeoverPair(t, e)
	beginTakeover(t, holder, requester)
	confirmTakeover(t, holder, requester, clock, 2)
	pending := activeTakeover(t, e)
	pty := e.srv.cfg.PTY.(*interactiveTestPTY)
	pty.mu.Lock()
	conn := pty.conn.(*attachConn)
	pty.mu.Unlock()
	conn.mu.Lock()
	unlock := sync.OnceFunc(conn.mu.Unlock)
	defer unlock()
	// One record blocks in the writer; all remaining queue slots are occupied.
	for range cap(conn.controlQueue) + 1 {
		conn.controlQueue <- protocol.DashAttachControl{Type: protocol.DashAttachGeometry, Cols: 80, Rows: 24}
	}
	clock.Advance(takeoverReview)
	e.srv.advanceTakeover(pending)
	unlock()
	current, present := e.srv.cfg.Control.Status(string(e.run.ID))
	if !present || current.SessionID != "holder" || current.Generation != generation || !current.Connected {
		t.Fatalf("failed acknowledgement displaced the holder: %+v", current)
	}
	expectTakeover(t, holder, "cancelled")
	holder.send(t, protocol.DashAttachControl{Type: protocol.DashAttachInput, Data: "still-holder", ControlGeneration: generation})
	if data, record := holder.next(t); string(data) != "echo:still-holder" || record != nil {
		t.Fatalf("holder input after refused grant = %q/%+v", data, record)
	}
}
