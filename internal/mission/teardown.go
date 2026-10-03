package mission

import (
	"context"
	"errors"
	"fmt"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/store"
)

// stopEndedMission tears down what an ended mission still runs: every live
// worker attempt is stopped and recorded cancelled, except a submitted worker,
// which finished its work and is retained and recorded completed as
// reconcileSubmitted would, and a cancelled mission's integrator is stopped. A completed mission's integrator finishes on its own
// success report. Ending a mission only records the durable phase; stopping
// processes is the reconcile loop's job, retried every pass until nothing is
// left, so a lost stop survives a server restart. No per-run Kill check is
// needed: these are the mission's own reserved runs, and the mission was
// ended by its accountable human, an admin, or its integrator's report.
// control.AdmitInput is deliberately not used: it fences integrator-to-worker
// input, and a human takeover must not keep an ended mission's worker alive.
func (s *Service) stopEndedMission(ctx context.Context, mission *domain.Mission) error {
	var errs []error
	if mission.Phase == domain.MissionPhaseCancelled && mission.CurrentIntegratorRunID != "" {
		if err := s.stopMissionRun(ctx, mission.CurrentIntegratorRunID); err != nil {
			errs = append(errs, fmt.Errorf("stop integrator: %w", err))
		}
	}
	attempts, err := s.cfg.Missions.ListAttempts(ctx, mission.ID, "")
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	for _, attempt := range attempts {
		if attempt == nil || !attempt.State.HoldsConcurrency() {
			continue
		}
		if stopErr := s.stopEndedAttempt(ctx, mission, attempt); stopErr != nil {
			errs = append(errs, fmt.Errorf("stop attempt %s: %w", attempt.ID, stopErr))
		}
	}
	return errors.Join(errs...)
}

// stopMissionRun kills run unless it is gone or already terminal.
func (s *Service) stopMissionRun(ctx context.Context, run domain.RunID) error {
	current, err := s.cfg.Store.GetRun(ctx, run)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if current.Status.Terminal() {
		return nil
	}
	if s.cfg.Cancel == nil {
		return errors.New("mission: scheduler cancel unavailable")
	}
	s.dispatchMu.Lock()
	defer s.dispatchMu.Unlock()
	return s.cfg.Cancel.CancelMission(s.operationContext(ctx), run)
}

// stopEndedAttempt stops one live worker of an ended mission and releases
// its capacity once the scheduler has settled the run, or once a reservation
// is known never to have launched.
func (s *Service) stopEndedAttempt(ctx context.Context, mission *domain.Mission, attempt *domain.Attempt) error {
	submitted := attempt.State == domain.AttemptSubmitted
	if attempt.RunID != "" {
		run, err := s.cfg.Store.GetRun(ctx, attempt.RunID)
		switch {
		case err == nil && !run.Status.Terminal() && submitted:
			if s.cfg.Complete == nil {
				return errors.New("mission: scheduler completion unavailable")
			}
			s.dispatchMu.Lock()
			defer s.dispatchMu.Unlock()
			return s.cfg.Complete.CompleteMission(s.operationContext(ctx), attempt.RunID, domain.RunCompleted)
		case err == nil && !run.Status.Terminal():
			return s.stopMissionRun(ctx, attempt.RunID)
		case err != nil && !errors.Is(err, store.ErrNotFound):
			return err
		}
		s.dispatchMu.Lock()
		obs, observeErr := s.observeMissionRun(ctx, attempt.RunID)
		s.dispatchMu.Unlock()
		if observeErr != nil && !errors.Is(observeErr, store.ErrNotFound) {
			return observeErr
		}
		if observeErr == nil && !obs.Settled() {
			return nil
		}
	}
	state, detail := domain.AttemptCancelled, "mission "+string(mission.Phase)
	if submitted {
		state, detail = domain.AttemptCompleted, "retained"
	}
	stateErr := s.cfg.Missions.UpdateAttemptState(ctx, attempt.ID, attempt.RunID, attempt.AuthorityGeneration, attempt.IntegratorGeneration, state, detail)
	if stateErr != nil && !errors.Is(stateErr, store.ErrMissionStale) {
		return stateErr
	}
	return s.publishMissionChanged(ctx, mission.ID)
}
