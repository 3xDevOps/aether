package sshd

import (
	"context"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

type blockingTakeover struct {
	missionControlStoreAdapter
	entered, proceed chan struct{}
}

func (b blockingTakeover) Takeover(ctx context.Context, run domain.RunID, member domain.MemberID) error {
	close(b.entered)
	<-b.proceed
	return b.missionControlStoreAdapter.Takeover(ctx, run, member)
}

// A fence that lands while Take control commits the hold must not leave the
// hold behind with no lease.
func TestACPMissionHoldClearsWhenFencedWhileTakingControl(t *testing.T) {
	e, db, _, _ := missionACPWorkerTestEnv(t, time.Minute)
	authority := blockingTakeover{
		missionControlStoreAdapter: missionControlStoreAdapter{store: db, control: e.srv.cfg.Control},
		entered:                    make(chan struct{}), proceed: make(chan struct{}),
	}
	e.srv.cfg.Services.MissionControl = authority
	done := make(chan struct{})
	go func() {
		defer close(done)
		stream, _, err := e.srv.Local(e.member.ID).ACP(t.Context(), protocol.ACPStreamRequest{
			RunID: string(e.run.ID), Write: true, ControlSessionID: "acp-tab",
		})
		if err == nil {
			t.Cleanup(func() { _ = stream.Close() })
		}
	}()
	<-authority.entered
	fenced := make(chan struct{})
	go func() {
		e.srv.cfg.Control.Fence(string(e.run.ID))
		close(fenced)
	}()
	time.Sleep(50 * time.Millisecond)
	close(authority.proceed)
	<-fenced
	<-done
	if lease, ok := e.srv.cfg.Control.Status(string(e.run.ID)); ok {
		t.Fatalf("lease survived the fence: %+v", lease)
	}
	waitMissionHold(t, db, e.run.ID, false)
}

// Take control over an open stream whose client leaves before reading the
// reply: the lease lapses after the reconnect window, and the hold with it.
func TestACPMissionHoldClearsWhenClientLeavesBeforeTheReply(t *testing.T) {
	e, db, mission, clock := missionACPWorkerTestEnv(t, 20*time.Millisecond)
	stream, ack, err := e.srv.Local(e.member.ID).ACP(t.Context(), protocol.ACPStreamRequest{
		RunID: string(e.run.ID), ControlSessionID: "acp-tab",
	})
	if err != nil || ack.HasControl {
		t.Fatalf("open as viewer: %+v, %v", ack, err)
	}
	if _, err = stream.Write([]byte(`{"type":"control","request_id":1,"write":true}` + "\n")); err != nil {
		t.Fatal(err)
	}
	waitMissionHold(t, db, e.run.ID, true)
	_ = stream.Close()
	waitControlDisconnected(t, e)
	clock.Advance(time.Second)
	waitMissionHold(t, db, e.run.ID, false)
	if err = integratorAdmitted(t, e, mission); err != nil {
		t.Fatalf("integrator input after the lease lapsed = %v", err)
	}
}
