package server

import (
	"context"
	"errors"

	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/sshd"
	"github.com/3xDevOps/Aether/internal/store"
)

// missionControlService adapts MissionState's durable worker assignment store
// to the narrow SSH/orchestrator seam. MissionState resolves the worker run
// atomically; ordinary runs have no mission worker and are a no-op.
type missionControlService struct {
	store   store.MissionControlStore
	control *control.Service
}

func (m missionControlService) Takeover(ctx context.Context, workerRun domain.RunID, member domain.MemberID) error {
	_, err := m.store.SetMissionWorkerTakeover(ctx, workerRun, member, true)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	return err
}

func (m missionControlService) Release(ctx context.Context, workerRun domain.RunID, member domain.MemberID) error {
	_, err := m.store.SetMissionWorkerTakeover(ctx, workerRun, member, false)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	return err
}

func (m missionControlService) ReleaseHold(ctx context.Context, workerRun domain.RunID, member domain.MemberID, expectedGeneration uint64) (*domain.MissionWorkerAssignment, error) {
	return m.store.ReleaseMissionWorkerTakeover(ctx, workerRun, member, expectedGeneration)
}

// AdmitInput serializes its check and callback with the worker's control
// admission lock. Callers must not pre-lock workerRun; the integrator run is
// validated by MissionState inside this same boundary.
func (m missionControlService) AdmitInput(ctx context.Context, integratorRun, workerRun domain.RunID, generation uint64, fn func() error) error {
	if fn == nil {
		return errors.New("mission control: nil input callback")
	}
	if m.control == nil {
		return errors.New("mission control: shared control service is unavailable")
	}
	return m.control.Admit(string(workerRun), func() error {
		if err := m.store.CheckIntegratorInput(ctx, integratorRun, workerRun, generation); err != nil {
			return err
		}
		return fn()
	})
}

var _ sshd.MissionControl = missionControlService{}

func init() {
	registerService("mission-control", func(d Deps) (Service, error) {
		missionStore, ok := d.Store.(store.MissionControlStore)
		if !ok {
			// Older/narrow stores and ordinary test deployments have no mission
			// rows. They keep the ordinary run-control path unchanged.
			return nil, nil
		}
		if d.Control == nil {
			return nil, errors.New("mission control needs the shared control service")
		}
		svc := missionControlService{store: missionStore, control: d.Control}
		if d.SSH != nil {
			d.SSH.Services.MissionControl = svc
		}
		return nil, nil
	})
}
