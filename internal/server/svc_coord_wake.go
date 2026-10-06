package server

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/coord"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/scheduler"
	"github.com/3xDevOps/Aether/internal/sshd"
	"github.com/3xDevOps/Aether/internal/store"
)

type coordWakeMission interface {
	Assignment(context.Context, domain.RunID) (protocol.CoordMissionAssignment, error)
	ValidateWake(context.Context, domain.RunID) error
}

type coordWakeAdmission struct {
	store           coord.Runs
	control         *control.Service
	mission         coordWakeMission
	authorizationMu *sync.Mutex
	missionControl  func() sshd.MissionControl
	observe         func(context.Context, domain.RunID) (scheduler.MissionRunObservation, error)
}

func newCoordWakeAdmission(d Deps) coord.WakeAdmission {
	admission := coordWakeAdmission{
		store: d.Store, control: d.Control, mission: lazyMission{ssh: d.SSH},
		missionControl: func() sshd.MissionControl {
			if d.SSH == nil {
				return nil
			}
			return d.SSH.Services.MissionControl
		},
	}
	if d.SSH != nil {
		admission.authorizationMu = d.SSH.AuthorizationMu
	}
	if d.Runs != nil {
		admission.observe = d.Runs.ObserveMissionRun
	}
	return admission.Admit
}

func (a coordWakeAdmission) Admit(ctx context.Context, run domain.RunID, dispatch func() error) error {
	if a.control == nil || a.store == nil || a.mission == nil || a.observe == nil || dispatch == nil {
		return errors.New("coord wake: admission authority unavailable")
	}
	// This first lookup only selects which shared lock path to enter. Every
	// authority and the live process are checked again inside that boundary.
	initial, err := a.mission.Assignment(ctx, run)
	if err != nil {
		return err
	}
	if initial.MissionID != "" {
		if a.authorizationMu == nil {
			return errors.New("coord wake: mission authorization unavailable")
		}
		// Mission cancellation/reconciliation already takes authorization
		// before Control. Keep that order through the complete wake frame.
		if !a.authorizationMu.TryLock() {
			return errors.New("coord wake: mission authorization is busy")
		}
		defer a.authorizationMu.Unlock()
	}
	admit := func() error {
		checkCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		current, err := a.store.GetRun(checkCtx, run)
		if err != nil {
			return err
		}
		if current == nil || current.ID != run || !current.Mode.Interactive() || current.Protected ||
			(current.Status != domain.RunRunning && current.Status != domain.RunNeedsAttention) {
			return errors.New("coord wake: run is not an eligible live interactive run")
		}
		live, err := a.observe(checkCtx, run)
		if err != nil {
			return err
		}
		if live.State != scheduler.MissionRunActive {
			return errors.New("coord wake: run is not active")
		}
		fresh, err := a.mission.Assignment(checkCtx, run)
		if err != nil {
			return err
		}
		if !sameWakeAssignment(initial, fresh) {
			return store.ErrMissionStale
		}
		if err := a.mission.ValidateWake(checkCtx, run); err != nil {
			return err
		}
		if err := checkCtx.Err(); err != nil {
			return err
		}
		return dispatch()
	}
	if initial.MissionID != "" && initial.Role == "worker" {
		if a.missionControl == nil {
			return errors.New("coord wake: mission control unavailable")
		}
		missionControl, ok := a.missionControl().(interface {
			TryAdmitInput(context.Context, domain.RunID, domain.RunID, uint64, func() error) error
		})
		if !ok {
			return errors.New("coord wake: nonblocking mission control unavailable")
		}
		return missionControl.TryAdmitInput(ctx, domain.RunID(initial.IntegratorRunID), run, initial.IntegratorGeneration, admit)
	}
	if initial.MissionID != "" && (initial.Role != "integrator" || initial.IntegratorRunID != string(run)) {
		return store.ErrMissionStale
	}
	return a.control.TryAdmit(string(run), admit)
}

func sameWakeAssignment(a, b protocol.CoordMissionAssignment) bool {
	return a.MissionID == b.MissionID && a.Role == b.Role && a.TaskID == b.TaskID &&
		a.TaskRevision == b.TaskRevision && a.AttemptID == b.AttemptID &&
		a.IntegratorRunID == b.IntegratorRunID && a.IntegratorGeneration == b.IntegratorGeneration
}

func (l lazyMission) ValidateWake(ctx context.Context, run domain.RunID) error {
	service, err := l.service()
	if err != nil {
		return err
	}
	wake, ok := service.(interface {
		ValidateWake(context.Context, domain.RunID) error
	})
	if !ok {
		return errors.New("coord wake: mission wake authority unavailable")
	}
	return wake.ValidateWake(ctx, run)
}
