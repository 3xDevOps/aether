package sshd

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

type missionControlTestClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *missionControlTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *missionControlTestClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

type missionControlStoreAdapter struct {
	store   store.MissionControlStore
	control *control.Service
}

func (m missionControlStoreAdapter) Takeover(ctx context.Context, run domain.RunID, member domain.MemberID) error {
	_, err := m.store.SetMissionWorkerTakeover(ctx, run, member, true)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	return err
}

func (m missionControlStoreAdapter) Release(ctx context.Context, run domain.RunID, member domain.MemberID) error {
	_, err := m.store.SetMissionWorkerTakeover(ctx, run, member, false)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	return err
}
func (m missionControlStoreAdapter) ReleaseHold(ctx context.Context, run domain.RunID, member domain.MemberID, expectedGeneration uint64) (*domain.MissionWorkerAssignment, error) {
	return m.store.ReleaseMissionWorkerTakeover(ctx, run, member, expectedGeneration)
}

func (m missionControlStoreAdapter) AdmitInput(ctx context.Context, integrator, worker domain.RunID, generation uint64, fn func() error) error {
	if err := m.control.Admit(string(worker), func() error {
		if err := m.store.CheckIntegratorInput(ctx, integrator, worker, generation); err != nil {
			return err
		}
		return fn()
	}); err != nil {
		return err
	}
	return nil
}

// missionWorkerTestEnv creates a real mission, accepted task, and reserved
// worker attempt in the same SQLite store used by the SSH server. The
// MissionControl seam is the only test adapter; persistence and assignment
// resolution remain production code.
func missionWorkerTestEnv(t *testing.T) (*testEnv, *store.DB, *domain.Mission) {
	t.Helper()
	ctx := context.Background()
	var mission *domain.Mission
	e := newTestEnv(t, func(cfg *Config) {
		db := cfg.Store.(*store.DB)
		members, err := db.ListMembers(ctx)
		if err != nil || len(members) == 0 {
			t.Fatalf("list members: %v", err)
		}
		workspaces, err := db.ListWorkspaces(ctx)
		if err != nil || len(workspaces) == 0 {
			t.Fatalf("list workspaces: %v", err)
		}
		runs, err := db.ListRunsByWorkspace(ctx, workspaces[0].ID)
		if err != nil || len(runs) == 0 {
			t.Fatalf("list runs: %v", err)
		}
		member, worker := members[0], runs[0]
		mission = &domain.Mission{
			WorkspaceID:        workspaces[0].ID,
			Objective:          "durable takeover regression",
			AccountableHumanID: member.ID,
			Integrator: domain.MissionIntegrator{
				AccountMemberID: member.ID,
				Harness:         "claude",
				Mode:            domain.LaunchTUI,
			},
			MaxConcurrentAttempts: 1,
			MaxTotalAttempts:      2,
			IdempotencyKey:        "takeover-regression",
		}
		if err := db.CreateMission(ctx, mission); err != nil {
			t.Fatalf("create mission: %v", err)
		}
		task := &domain.Task{
			MissionID: mission.ID,
			Revision: &domain.TaskRevision{
				Title:     "worker",
				Objective: "worker task",
				Status:    domain.TaskRevisionAccepted,
			},
		}
		if err := db.CreateTask(ctx, task); err != nil {
			t.Fatalf("create task: %v", err)
		}
		if _, _, err := db.ReserveAttempt(ctx, &domain.AttemptReservation{
			MissionID:            mission.ID,
			TaskID:               task.ID,
			TaskRevision:         1,
			DispatchKey:          "worker-regression",
			Harness:              "claude",
			Mode:                 domain.LaunchTUI,
			AssignedRunID:        worker.ID,
			ActorRunID:           worker.ID,
			AuthorizingHumanID:   member.ID,
			RunOwnerID:           member.ID,
			AccountOwnerID:       member.ID,
			AuthorityGeneration:  1,
			IntegratorGeneration: mission.IntegratorGeneration,
		}); err != nil {
			t.Fatalf("reserve worker: %v", err)
		}
	})
	return e, e.store.(*store.DB), mission
}

func TestMissionTakeoverSurvivesExpiryAndFailedRelease(t *testing.T) {
	e, db, mission := missionWorkerTestEnv(t)
	clock := &missionControlTestClock{now: time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)}
	e.srv.cfg.Control = control.New(control.Config{Now: clock.Now, ReconnectWindow: time.Second})
	e.srv.cfg.Services.MissionControl = missionControlStoreAdapter{store: db, control: e.srv.cfg.Control}
	e.pty.gate = NewWriteGate(e.store)

	first, ack := rawAttachRequest(t, e, e.signer, protocol.AttachRequest{ControlSessionID: "human"}, true)
	if !ack.OK || !ack.HasControl {
		t.Fatalf("human attach = %+v", ack)
	}
	assignment, err := db.GetMissionWorkerAssignment(context.Background(), e.run.ID)
	if err != nil || !assignment.Active {
		t.Fatalf("takeover assignment = %+v, %v", assignment, err)
	}

	called := false
	err = e.srv.cfg.Services.MissionControl.AdmitInput(context.Background(), mission.CurrentIntegratorRunID, e.run.ID, mission.IntegratorGeneration, func() error {
		called = true
		return nil
	})
	if !errors.Is(err, store.ErrMissionTakeover) || called {
		t.Fatalf("integrator input while held = %v, callback=%v", err, called)
	}

	_ = first.ch.Close()
	select {
	case <-first.exit:
	case <-time.After(5 * time.Second):
		t.Fatal("attach did not finish after client close")
	}
	e.srv.cfg.Control.Disconnect(string(e.run.ID), "human", ack.ControlGeneration)
	clock.Advance(2 * time.Second)
	if _, ok := e.srv.cfg.Control.Status(string(e.run.ID)); ok {
		t.Fatal("expired human lease remained present")
	}
	assignment, err = db.GetMissionWorkerAssignment(context.Background(), e.run.ID)
	if err != nil || !assignment.Active {
		t.Fatalf("takeover after expiry = %+v, %v", assignment, err)
	}

	// A new in-memory control table models a server restart without mutating
	// the live server while its attach goroutines are settling. The same
	// durable mission hold remains visible through the fresh control seam.
	restarted := control.New(control.Config{Now: clock.Now, ReconnectWindow: time.Second})
	restartedMission := missionControlStoreAdapter{store: db, control: restarted}
	restartedLease, _, err := restarted.Acquire(string(e.run.ID), string(e.member.ID), "restart", false)
	if err != nil {
		t.Fatalf("reacquire after restart: %v", err)
	}
	if err = restartedMission.AdmitInput(context.Background(), mission.CurrentIntegratorRunID, e.run.ID, mission.IntegratorGeneration, func() error {
		return nil
	}); !errors.Is(err, store.ErrMissionTakeover) {
		t.Fatalf("takeover after restart = %v, want ErrMissionTakeover", err)
	}
	if err = restarted.Release(string(e.run.ID), e.member.ID, restartedLease.SessionID, restartedLease.Generation); err != nil {
		t.Fatalf("release restart controller: %v", err)
	}
	human, _, err := e.srv.cfg.Control.Acquire(string(e.run.ID), string(e.member.ID), "human", false)
	if err != nil {
		t.Fatalf("reacquire after expiry: %v", err)
	}

	e.srv.cfg.PTY = &admissionFailurePTY{fakePTY: e.pty, err: errors.New("replacement admission failed")}
	_, failed := rawAttachRequest(t, e, e.signer, protocol.AttachRequest{
		ControlSessionID: "human", ControlGeneration: human.Generation, ReleaseControl: true,
	}, true)
	if failed.OK {
		t.Fatalf("failed release ack = %+v", failed)
	}
	assignment, err = db.GetMissionWorkerAssignment(context.Background(), e.run.ID)
	if err != nil || !assignment.Active {
		t.Fatalf("takeover after failed release = %+v, %v", assignment, err)
	}

	e.srv.cfg.PTY = e.pty
	_, released := rawAttachRequest(t, e, e.signer, protocol.AttachRequest{
		ControlSessionID: "human", ControlGeneration: human.Generation, ReleaseControl: true,
	}, true)
	if !released.OK {
		t.Fatalf("successful release ack = %+v", released)
	}
	assignment, err = db.GetMissionWorkerAssignment(context.Background(), e.run.ID)
	if err != nil || assignment.Active {
		t.Fatalf("takeover after explicit release = %+v, %v", assignment, err)
	}

	called = false
	err = e.srv.cfg.Services.MissionControl.AdmitInput(context.Background(), mission.CurrentIntegratorRunID, e.run.ID, mission.IntegratorGeneration, func() error {
		called = true
		return nil
	})
	if err != nil || !called {
		t.Fatalf("integrator input after release = %v, callback=%v", err, called)
	}
}
func TestMissionWorkerReleaseCASWithoutLease(t *testing.T) {
	e, db, _ := missionWorkerTestEnv(t)
	ctx := context.Background()
	adapter := missionControlStoreAdapter{store: db, control: control.New(control.Config{})}
	held, err := db.SetMissionWorkerTakeover(ctx, e.run.ID, e.member.ID, true)
	if err != nil {
		t.Fatalf("set takeover: %v", err)
	}
	attempt, err := db.GetAttemptByRun(ctx, e.run.ID)
	if err != nil {
		t.Fatalf("get worker attempt: %v", err)
	}
	if err = db.UpdateAttemptState(ctx, attempt.ID, attempt.RunID, attempt.AuthorityGeneration, attempt.IntegratorGeneration, domain.AttemptCompleted, "finished"); err != nil {
		t.Fatalf("finish worker attempt: %v", err)
	}
	if _, err = adapter.ReleaseHold(ctx, e.run.ID, e.member.ID, held.Generation+1); !errors.Is(err, store.ErrMissionStale) {
		t.Fatalf("stale release = %v, want ErrMissionStale", err)
	}
	actor := &domain.Member{DisplayName: "Release actor", PublicKey: string(ssh.MarshalAuthorizedKey(newSigner(t).PublicKey())), Role: domain.RoleCollaborator}
	if err = db.CreateMember(ctx, actor); err != nil {
		t.Fatalf("create release actor: %v", err)
	}
	released, err := adapter.ReleaseHold(ctx, e.run.ID, actor.ID, held.Generation)
	if err != nil {
		t.Fatalf("release completed worker without lease: %v", err)
	}
	if released.Active || released.Generation != held.Generation+1 || released.MemberID != actor.ID {
		t.Fatalf("released assignment = %+v, want inactive next generation attributed to actor %s", released, actor.ID)
	}
}

func TestMissionTakeoverSerializesIntegratorAdmission(t *testing.T) {
	e, _, mission := missionWorkerTestEnv(t)
	clock := &missionControlTestClock{now: time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)}
	e.srv.cfg.Control = control.New(control.Config{Now: clock.Now})

	e.srv.cfg.Services.MissionControl = missionControlStoreAdapter{store: e.store.(*store.DB), control: e.srv.cfg.Control}
	entered := make(chan struct{})
	release := make(chan struct{})
	takeoverDone := make(chan error, 1)
	go func() {
		takeoverDone <- e.srv.cfg.Control.Admit(string(e.run.ID), func() error {
			close(entered)
			<-release
			return e.srv.cfg.Services.MissionControl.Takeover(context.Background(), e.run.ID, e.member.ID)
		})
	}()
	<-entered

	called := false
	inputDone := make(chan error, 1)
	go func() {
		inputDone <- e.srv.cfg.Services.MissionControl.AdmitInput(context.Background(), mission.CurrentIntegratorRunID, e.run.ID, mission.IntegratorGeneration, func() error {
			called = true
			return nil
		})
	}()
	select {
	case err := <-inputDone:
		t.Fatalf("integrator admission crossed takeover lock: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-takeoverDone; err != nil {
		t.Fatalf("takeover admission: %v", err)
	}
	if err := <-inputDone; !errors.Is(err, store.ErrMissionTakeover) {
		t.Fatalf("integrator admission after takeover = %v", err)
	}
	if called {
		t.Fatal("integrator callback ran despite durable takeover")
	}
}
