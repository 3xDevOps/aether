package sshd

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

const testTakeoverID = "3c108774-c2e6-40e0-af51-2825ca226135"

func takeoverCommand(action string, requestID, generation uint64) protocol.DashAttachControl {
	return protocol.DashAttachControl{Type: protocol.DashAttachTakeover, Action: action,
		RequestID: requestID, TakeoverID: testTakeoverID, ControlGeneration: generation}
}

func expectTakeover(t *testing.T, wire *interactiveSSHAttach, phase string) *protocol.DashAttachControl {
	t.Helper()
	_, record := wire.next(t)
	if record == nil || record.Type != protocol.DashAttachTakeover || record.TakeoverState == nil || record.TakeoverState.Phase != phase {
		t.Fatalf("takeover = %+v, want phase %s", record, phase)
	}
	return record
}

func takeoverClock(e *testEnv) *missionControlTestClock {
	clock := &missionControlTestClock{now: time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)}
	e.srv.takeovers.now = clock.Now
	e.srv.cfg.revalidateInterval = time.Hour
	return clock
}

func activeTakeover(t *testing.T, e *testEnv) *pendingTakeover {
	t.Helper()
	e.srv.takeovers.mu.Lock()
	defer e.srv.takeovers.mu.Unlock()
	p := e.srv.takeovers.pending[e.run.ID]
	if p == nil {
		t.Fatal("no pending takeover")
	}
	return p
}

func openTakeoverPair(t *testing.T, e *testEnv) (*interactiveSSHAttach, *interactiveSSHAttach, uint64) {
	t.Helper()
	e.srv.cfg.PTY = &interactiveTestPTY{fakePTY: e.pty}
	holder, ack := openInteractiveSSHAttach(t, e, e.signer, protocol.AttachRequest{ControlSessionID: "holder"})
	if !ack.OK || !ack.HasControl {
		t.Fatalf("holder attach = %+v", ack)
	}
	requester, mirror := openInteractiveSSHAttach(t, e, e.signer, protocol.AttachRequest{ControlSessionID: "requester", ReadOnly: true})
	if !mirror.OK || mirror.HasControl {
		t.Fatalf("requester attach = %+v", mirror)
	}
	return holder, requester, ack.ControlGeneration
}

func beginTakeover(t *testing.T, holder, requester *interactiveSSHAttach) {
	t.Helper()
	requester.send(t, takeoverCommand("start", 1, 0))
	started := expectTakeover(t, requester, "holding")
	if !started.OK || started.RequestID != 1 {
		t.Fatalf("start acknowledgement = %+v", started)
	}
	if holder != nil {
		observed := expectTakeover(t, holder, "holding")
		if !observed.OK || observed.RequestID != 0 || *observed.TakeoverState != *started.TakeoverState {
			t.Fatalf("holder start notification = %+v", observed)
		}
	}
}

func confirmTakeover(t *testing.T, holder, requester *interactiveSSHAttach, clock *missionControlTestClock, requestID uint64) {
	t.Helper()
	clock.Advance(takeoverHold)
	requester.send(t, takeoverCommand("confirm", requestID, 0))
	expectTakeover(t, requester, "review")
	if holder != nil {
		expectTakeover(t, holder, "review")
	}
}

func TestTimedTakeoverRequiresFullHoldAndExactAttachments(t *testing.T) {
	e := controlAttachEnv(t)
	clock := takeoverClock(e)
	holder, requester, generation := openTakeoverPair(t, e)
	beginTakeover(t, holder, requester)
	requester.send(t, takeoverCommand("confirm", 2, 0))
	_, early := requester.next(t)
	if early == nil || early.OK || early.Code != protocol.CodeConflict || early.TakeoverState != nil {
		t.Fatalf("early confirm = %+v", early)
	}
	// Knowing the authenticated member/session ID is not enough: this mirror
	// has never owned the targeted transport generation.
	impostor, ack := openInteractiveSSHAttach(t, e, e.signer, protocol.AttachRequest{ControlSessionID: "holder", ReadOnly: true})
	if !ack.OK {
		t.Fatal(ack.Error)
	}
	impostor.send(t, takeoverCommand("confirm", 1, 0))
	_, foreign := impostor.next(t)
	if foreign == nil || foreign.OK || foreign.Code != protocol.CodeConflict {
		t.Fatalf("foreign confirm = %+v", foreign)
	}
	confirmTakeover(t, holder, requester, clock, 3)
	impostor.send(t, takeoverCommand("accept", 2, generation))
	_, foreign = impostor.next(t)
	if foreign == nil || foreign.OK || foreign.Code != protocol.CodeConflict {
		t.Fatalf("foreign decision = %+v", foreign)
	}
	holder.send(t, takeoverCommand("accept", 1, generation+1))
	_, stale := holder.next(t)
	if stale == nil || stale.OK || stale.Code != protocol.CodeConflict {
		t.Fatalf("stale decision = %+v", stale)
	}
	wrongID := takeoverCommand("accept", 2, generation)
	wrongID.TakeoverID = "762fbbad-c6a2-4c06-bca2-a6d28af54528"
	holder.send(t, wrongID)
	_, stale = holder.next(t)
	if stale == nil || stale.OK {
		t.Fatalf("stale request decision = %+v", stale)
	}
	holder.send(t, takeoverCommand("deny", 3, generation))
	expectTakeover(t, holder, "denied")
	expectTakeover(t, requester, "denied")
	if err := e.srv.cfg.Control.Validate(string(e.run.ID), "holder", generation); err != nil {
		t.Fatalf("refusals displaced holder: %v", err)
	}
}

func TestTimedTakeoverCancelAndDisconnectNeverGrant(t *testing.T) {
	for _, phase := range []string{"holding", "review"} {
		for _, action := range []string{"cancel", "disconnect"} {
			t.Run(phase+"/"+action, func(t *testing.T) {
				e := controlAttachEnv(t)
				clock := takeoverClock(e)
				holder, requester, generation := openTakeoverPair(t, e)
				beginTakeover(t, holder, requester)
				nextID := uint64(2)
				if phase == "review" {
					confirmTakeover(t, holder, requester, clock, nextID)
					nextID++
				}
				pending := activeTakeover(t, e)
				if action == "cancel" {
					requester.send(t, takeoverCommand("cancel", nextID, 0))
					expectTakeover(t, requester, "cancelled")
				} else {
					_ = requester.pipe.Close()
				}
				expectTakeover(t, holder, "cancelled")
				clock.Advance(time.Minute)
				e.srv.advanceTakeover(pending)
				if err := e.srv.cfg.Control.Validate(string(e.run.ID), "holder", generation); err != nil {
					t.Fatalf("cancelled takeover changed holder: %v", err)
				}
			})
		}
	}
}

func TestTimedTakeoverAcceptAndServerTimeoutAcknowledgeBeforeGrant(t *testing.T) {
	for _, decision := range []string{"accept", "timeout", "racing-accept"} {
		t.Run(decision, func(t *testing.T) {
			e := controlAttachEnv(t)
			clock := takeoverClock(e)
			holder, requester, generation := openTakeoverPair(t, e)
			beginTakeover(t, holder, requester)
			pending := activeTakeover(t, e)
			clock.Advance(time.Minute)
			e.srv.advanceTakeover(pending)
			if err := e.srv.cfg.Control.Validate(string(e.run.ID), "holder", generation); err != nil {
				t.Fatalf("unconfirmed hold auto-granted: %v", err)
			}
			confirmTakeover(t, holder, requester, clock, 2)
			if decision == "accept" {
				holder.send(t, takeoverCommand("accept", 1, generation))
			} else {
				clock.Advance(takeoverReview)
				if decision == "racing-accept" {
					holder.send(t, takeoverCommand("accept", 1, generation))
				}
				e.srv.advanceTakeover(pending)
			}
			_, grant := requester.next(t)
			if grant == nil || grant.Type != protocol.DashAttachControlFrame || !grant.OK || !grant.HasControl || grant.RequestID != 0 || grant.ControlGeneration != generation+1 {
				t.Fatalf("grant acknowledgement = %+v", grant)
			}
			if end := expectTakeover(t, requester, "granted"); !end.OK {
				t.Fatalf("grant end = %+v", end)
			}
			_, revoked := holder.next(t)
			if revoked == nil || revoked.Type != protocol.DashAttachControlFrame || revoked.HasControl || revoked.RevocationReason != "takeover" || revoked.ControlGeneration != generation {
				t.Fatalf("holder revocation = %+v", revoked)
			}
			expectTakeover(t, holder, "granted")
			e.srv.advanceTakeover(pending)
			current, _ := e.srv.cfg.Control.Status(string(e.run.ID))
			if current.Generation != generation+1 || current.SessionID != "requester" {
				t.Fatalf("deadline/accept double grant = %+v", current)
			}
			requester.send(t, protocol.DashAttachControl{Type: protocol.DashAttachInput, Data: "new-writer", ControlGeneration: grant.ControlGeneration})
			if data, ctl := requester.next(t); string(data) != "echo:new-writer" || ctl != nil {
				t.Fatalf("granted input = %q/%+v", data, ctl)
			}
		})
	}
}

func TestTimedTakeoverLeaseReplacementCancelsWithoutSuccessorPrompt(t *testing.T) {
	e := controlAttachEnv(t)
	clock := takeoverClock(e)
	holder, requester, _ := openTakeoverPair(t, e)
	beginTakeover(t, holder, requester)
	confirmTakeover(t, holder, requester, clock, 2)
	pending := activeTakeover(t, e)
	replacement, _, err := e.srv.cfg.Control.Acquire(string(e.run.ID), string(e.member.ID), "successor", true)
	if err != nil {
		t.Fatal(err)
	}
	// Revocation wakes the coordinator even though periodic checks are hourly.
	expectTakeover(t, requester, "cancelled")
	expectTakeover(t, holder, "cancelled")
	clock.Advance(takeoverReview)
	e.srv.advanceTakeover(pending)
	if err := e.srv.cfg.Control.Validate(string(e.run.ID), "successor", replacement.Generation); err != nil {
		t.Fatalf("stale review displaced successor: %v", err)
	}
}

func TestTimedTakeoverRawHolderAndInteractiveBypass(t *testing.T) {
	e := controlAttachEnv(t)
	clock := takeoverClock(e)
	e.srv.cfg.PTY = &interactiveTestPTY{fakePTY: e.pty}
	raw, ack := rawAttachRequest(t, e, e.signer, protocol.AttachRequest{ControlSessionID: "cli"}, true)
	if !ack.OK || !ack.HasControl {
		t.Fatalf("raw holder = %+v", ack)
	}
	defer func() { _ = raw.ch.Close() }()
	requester, ack := openInteractiveSSHAttach(t, e, e.signer, protocol.AttachRequest{ControlSessionID: "dashboard", ReadOnly: true})
	if !ack.OK {
		t.Fatal(ack.Error)
	}
	requester.send(t, protocol.DashAttachControl{Type: protocol.DashAttachControlFrame, RequestID: 1, Write: true, Takeover: true, ControlGeneration: ack.ControlGeneration})
	_, refused := requester.next(t)
	if refused == nil || refused.OK || refused.HasControl || refused.Code != protocol.CodeConflict {
		t.Fatalf("interactive bypass = %+v", refused)
	}
	requester.send(t, takeoverCommand("start", 2, 0))
	expectTakeover(t, requester, "holding")
	confirmTakeover(t, nil, requester, clock, 3)
	pending := activeTakeover(t, e)
	clock.Advance(takeoverReview)
	// Wake the actual server timer using the advanced deterministic clock.
	select {
	case pending.changed <- struct{}{}:
	default:
	}
	_, granted := requester.next(t)
	if granted == nil || !granted.OK || !granted.HasControl || granted.Type != protocol.DashAttachControlFrame {
		t.Fatalf("raw-holder timeout grant = %+v", granted)
	}
	expectTakeover(t, requester, "granted")
}

func TestTimedTakeoverPermissionAndRunInvalidation(t *testing.T) {
	for _, reason := range []string{"viewer", "permission-loss", "run-finished"} {
		t.Run(reason, func(t *testing.T) {
			e := controlAttachEnv(t)
			clock := takeoverClock(e)
			e.srv.cfg.PTY = &interactiveTestPTY{fakePTY: e.pty}
			holder, ack := openInteractiveSSHAttach(t, e, e.signer, protocol.AttachRequest{ControlSessionID: "holder"})
			if !ack.OK {
				t.Fatal(ack.Error)
			}
			role := domain.RoleCollaborator
			if reason == "viewer" {
				role = domain.RoleViewer
			}
			signer, member := addMember(t, e, "Requester", role, false)
			requester, ack := openInteractiveSSHAttach(t, e, signer, protocol.AttachRequest{ControlSessionID: "requester", ReadOnly: true})
			if !ack.OK {
				t.Fatal(ack.Error)
			}
			if reason == "viewer" {
				requester.send(t, takeoverCommand("start", 1, 0))
				_, refused := requester.next(t)
				if refused == nil || refused.OK || refused.Code != protocol.CodeDenied {
					t.Fatalf("viewer start = %+v", refused)
				}
				return
			}
			beginTakeover(t, holder, requester)
			confirmTakeover(t, holder, requester, clock, 2)
			pending := activeTakeover(t, e)
			if reason == "permission-loss" {
				var result protocol.MemberRoleResult
				if err := controlClient(t, e).Call(protocol.MethodMemberRole, protocol.MemberRoleParams{MemberID: string(member.ID), Role: string(domain.RoleViewer)}, &result); err != nil {
					t.Fatal(err)
				}
			} else if err := e.store.UpdateRunStatus(context.Background(), e.run.ID, domain.RunCompleted, "", nil, nil); err != nil {
				t.Fatal(err)
			}
			clock.Advance(takeoverReview)
			e.srv.advanceTakeover(pending)
			expectTakeover(t, requester, "cancelled")
			expectTakeover(t, holder, "cancelled")
			current, present := e.srv.cfg.Control.Status(string(e.run.ID))
			if present && current.SessionID != "holder" {
				t.Fatalf("invalidated requester acquired %+v", current)
			}
		})
	}
}

func TestTimedTakeoverMissionAndReadinessFailurePreserveHolder(t *testing.T) {
	for _, failure := range []string{"readiness", "mission"} {
		t.Run(failure, func(t *testing.T) {
			e, db, mission := missionWorkerTestEnv(t)
			e.srv.cfg.Control = control.New(control.Config{})
			clock := takeoverClock(e)
			authority := &failingInteractiveMissionControl{missionControlStoreAdapter: missionControlStoreAdapter{store: db, control: e.srv.cfg.Control}}
			authority.failTakeover.Store(failure == "mission")
			e.srv.cfg.Services.MissionControl = authority
			interactive := &interactiveTestPTY{fakePTY: e.pty, readyError: func(readOnly bool) error {
				if !readOnly && failure == "readiness" {
					return errors.New("readiness refused")
				}
				return nil
			}}
			e.srv.cfg.PTY = interactive
			lease, _, err := e.srv.cfg.Control.Acquire(string(e.run.ID), string(e.member.ID), "holder", false)
			if err != nil {
				t.Fatal(err)
			}
			requester, ack := openInteractiveSSHAttach(t, e, e.signer, protocol.AttachRequest{ControlSessionID: "requester", ReadOnly: true})
			if !ack.OK {
				t.Fatal(ack.Error)
			}
			beginTakeover(t, nil, requester)
			confirmTakeover(t, nil, requester, clock, 2)
			pending := activeTakeover(t, e)
			clock.Advance(takeoverReview)
			e.srv.advanceTakeover(pending)
			_, refused := requester.next(t)
			if refused == nil || refused.Type != protocol.DashAttachControlFrame || refused.OK || refused.HasControl {
				t.Fatalf("failed admission = %+v", refused)
			}
			expectTakeover(t, requester, "cancelled")
			if err := e.srv.cfg.Control.Validate(string(e.run.ID), "holder", lease.Generation); err != nil {
				t.Fatalf("failed admission displaced holder: %v", err)
			}
			assertInteractiveMissionState(t, e, db, mission, interactive, false, false)
		})
	}
}

func TestTimedTakeoverSharesRequestFenceAndRequiresOccupiedTarget(t *testing.T) {
	e := controlAttachEnv(t)
	e.srv.cfg.PTY = &interactiveTestPTY{fakePTY: e.pty}
	wire, ack := openInteractiveSSHAttach(t, e, e.signer, protocol.AttachRequest{ControlSessionID: "tab", ReadOnly: true})
	if !ack.OK {
		t.Fatal(ack.Error)
	}
	wire.send(t, takeoverCommand("start", 1, 0))
	_, empty := wire.next(t)
	if empty == nil || empty.OK || empty.Code != protocol.CodeConflict {
		t.Fatalf("unoccupied takeover = %+v", empty)
	}
	wire.send(t, protocol.DashAttachControl{Type: protocol.DashAttachControlFrame, RequestID: 2, Write: true})
	_, grant := wire.next(t)
	if grant == nil || !grant.OK || !grant.HasControl {
		t.Fatalf("normal acquisition = %+v", grant)
	}
	wire.send(t, takeoverCommand("start", 3, 0))
	_, own := wire.next(t)
	if own == nil || own.OK || own.Code != protocol.CodeConflict {
		t.Fatalf("own-session takeover = %+v", own)
	}
	wire.send(t, protocol.DashAttachControl{Type: protocol.DashAttachControlFrame, RequestID: 3, ControlGeneration: grant.ControlGeneration})
	_, stale := wire.next(t)
	if stale == nil || stale.OK || stale.Code != protocol.CodeInvalidParams || !stale.HasControl {
		t.Fatalf("reused takeover request ID released control: %+v", stale)
	}
}

func TestInteractiveReconnectForceRetainsExactSessionReplacement(t *testing.T) {
	e := controlAttachEnv(t)
	e.srv.cfg.PTY = &interactiveTestPTY{fakePTY: e.pty}
	original, ack := openInteractiveSSHAttach(t, e, e.signer, protocol.AttachRequest{ControlSessionID: "tab"})
	if !ack.OK || !ack.HasControl {
		t.Fatalf("original = %+v", ack)
	}
	_, rejected := openInteractiveSSHAttach(t, e, e.signer, protocol.AttachRequest{
		ControlSessionID: "foreign-tab", Takeover: true, ControlGeneration: ack.ControlGeneration,
	})
	if rejected.OK || rejected.Code != protocol.CodeConflict {
		t.Fatalf("cross-session header bypass = %+v", rejected)
	}
	replacement, resumed := openInteractiveSSHAttach(t, e, e.signer, protocol.AttachRequest{
		ControlSessionID: "tab", Takeover: true, ControlGeneration: ack.ControlGeneration,
	})
	if !resumed.OK || !resumed.HasControl || resumed.ControlGeneration <= ack.ControlGeneration {
		t.Fatalf("same-session transport replacement = %+v", resumed)
	}
	original.send(t, protocol.DashAttachControl{Type: protocol.DashAttachInput, Data: "old", ControlGeneration: ack.ControlGeneration})
	_, revoked := original.next(t)
	if revoked == nil || revoked.HasControl || revoked.ControlGeneration != ack.ControlGeneration {
		t.Fatalf("old transport authority = %+v", revoked)
	}
	replacement.send(t, protocol.DashAttachControl{Type: protocol.DashAttachInput, Data: "resumed", ControlGeneration: resumed.ControlGeneration})
	if data, ctl := replacement.next(t); string(data) != "echo:resumed" || ctl != nil {
		t.Fatalf("resumed transport input = %q/%+v", data, ctl)
	}
}
