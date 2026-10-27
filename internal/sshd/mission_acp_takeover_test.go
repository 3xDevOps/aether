package sshd

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/scheduler"
	"github.com/3xDevOps/Aether/internal/store"
)

func missionACPWorkerTestEnv(t *testing.T, reconnectWindow time.Duration) (*testEnv, *store.DB, *domain.Mission, *missionControlTestClock) {
	t.Helper()
	e, db, mission := missionWorkerTestEnv(t)
	if err := db.SetRunMode(t.Context(), e.run.ID, domain.LaunchACP, true); err != nil {
		t.Fatalf("make worker enhanced: %v", err)
	}
	clock := &missionControlTestClock{now: time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)}
	e.srv.cfg.Control = control.New(control.Config{Now: clock.Now, ReconnectWindow: reconnectWindow})
	e.srv.cfg.Services.MissionControl = missionControlStoreAdapter{store: db, control: e.srv.cfg.Control}
	e.srv.cfg.revalidateInterval = time.Hour
	e.runs.acpStream = scheduler.ACPStream{Items: make(chan acphost.Item)}
	return e, db, mission, clock
}

func waitControlDisconnected(t *testing.T, e *testEnv) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if lease, ok := e.srv.cfg.Control.Status(string(e.run.ID)); !ok || !lease.Connected {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("ACP stream close did not disconnect its lease")
}

func integratorAdmitted(t *testing.T, e *testEnv, mission *domain.Mission) error {
	t.Helper()
	return e.srv.cfg.Services.MissionControl.AdmitInput(context.Background(),
		mission.CurrentIntegratorRunID, e.run.ID, mission.IntegratorGeneration, func() error { return nil })
}

// Cause (a): the dashboard took control of an Enhanced worker, then its
// stream dropped without an explicit release. Once the reconnect window
// passes nobody holds the lease, yet the durable hold still refuses the
// integrator. docs/coordination.md currently documents this as intended.
func TestReproACPMissionTakeoverStaysActiveAfterLeaseExpiry(t *testing.T) {
	e, db, mission, clock := missionACPWorkerTestEnv(t, 20*time.Millisecond)
	stream, ack, err := e.srv.Local(e.member.ID).ACP(t.Context(), protocol.ACPStreamRequest{
		RunID: string(e.run.ID), Write: true, ControlSessionID: "acp-tab",
	})
	if err != nil || !ack.HasControl {
		t.Fatalf("take control: %+v, %v", ack, err)
	}
	if held, getErr := db.GetMissionWorkerAssignment(t.Context(), e.run.ID); getErr != nil || !held.Active {
		t.Fatalf("hold after take control = %+v, %v", held, getErr)
	}

	_ = stream.Close()
	waitControlDisconnected(t, e)
	clock.Advance(time.Second)
	time.Sleep(100 * time.Millisecond)
	if lease, ok := e.srv.cfg.Control.Status(string(e.run.ID)); ok {
		t.Fatalf("lease outlived its reconnect window: %+v", lease)
	}

	waitMissionHold(t, db, e.run.ID, false)
	if err = integratorAdmitted(t, e, mission); err != nil {
		t.Errorf("integrator input with nobody in control = %v, want admitted", err)
	}
}

// Cause (b), server half: opening and reopening an Enhanced worker as a
// viewer, the dashboard's header without write, must never take the hold.
func TestReproACPMissionViewerNeverTakesOver(t *testing.T) {
	e, db, mission, _ := missionACPWorkerTestEnv(t, 20*time.Millisecond)
	for i := range 3 {
		stream, ack, err := e.srv.Local(e.member.ID).ACP(t.Context(), protocol.ACPStreamRequest{
			RunID: string(e.run.ID), ControlSessionID: "acp-viewer",
		})
		if err != nil || ack.HasControl {
			t.Fatalf("viewer open %d: %+v, %v", i, ack, err)
		}
		_ = stream.Close()
	}
	if lease, ok := e.srv.cfg.Control.Status(string(e.run.ID)); ok {
		t.Fatalf("viewer left a lease: %+v", lease)
	}
	if assignment, err := db.GetMissionWorkerAssignment(t.Context(), e.run.ID); err == nil && assignment.Active {
		t.Fatalf("viewer set takeover_active: %+v", assignment)
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	if err := integratorAdmitted(t, e, mission); err != nil {
		t.Fatalf("integrator input after viewing = %v", err)
	}
}

// Generation churn: the dashboard reconnects a held stream with write:true
// and its lease generation (acp-stream.ts lease header). Each reconnect runs
// acpStream.acquire again, which records a fresh takeover generation even
// though the same session kept the same lease.
func TestReproACPMissionReconnectBumpsTakeoverGeneration(t *testing.T) {
	e, db, _, _ := missionACPWorkerTestEnv(t, time.Minute)
	stream, ack, err := e.srv.Local(e.member.ID).ACP(t.Context(), protocol.ACPStreamRequest{
		RunID: string(e.run.ID), Write: true, ControlSessionID: "acp-tab",
	})
	if err != nil || !ack.HasControl {
		t.Fatalf("take control: %+v, %v", ack, err)
	}
	first, err := db.GetMissionWorkerAssignment(t.Context(), e.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	lease := ack.ControlGeneration
	for i := range 5 {
		_ = stream.Close()
		waitControlDisconnected(t, e)
		stream, ack, err = e.srv.Local(e.member.ID).ACP(t.Context(), protocol.ACPStreamRequest{
			RunID: string(e.run.ID), Write: true, ControlSessionID: "acp-tab", ControlGeneration: lease,
		})
		if err != nil || !ack.HasControl || ack.ControlGeneration != lease {
			t.Fatalf("reconnect %d: %+v, %v (lease generation %d)", i, ack, err, lease)
		}
	}
	defer func() { _ = stream.Close() }()
	last, err := db.GetMissionWorkerAssignment(t.Context(), e.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if last.Generation != first.Generation {
		t.Errorf("same-session reconnects moved the takeover generation %d -> %d while the lease stayed at %d",
			first.Generation, last.Generation, lease)
	}
}
