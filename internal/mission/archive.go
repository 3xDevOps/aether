package mission

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

// RunRetirer is the scheduler seam behind run.close, run.archive and
// run.delete.
type RunRetirer interface {
	CloseRun(context.Context, domain.RunID, domain.MemberID, domain.RunStatus) error
	SetArchived(context.Context, domain.RunID, domain.MemberID, bool) (*domain.Run, error)
	DeleteRun(context.Context, domain.RunID, domain.MemberID) error
}

// Archive hides a completed or cancelled swarm. Each completed run is first
// closed - merged when it is the integrator of a completed swarm or a worker
// whose work was accepted, without merging otherwise - so every run can be
// archived with it. A swarm with a live run is refused.
func (s *Service) Archive(ctx context.Context, actor domain.MemberID, p protocol.MissionIDParams) (protocol.MissionArchiveResult, error) {
	m, err := s.authorizedMission(ctx, actor, p.MissionID, "archive this swarm")
	if err != nil {
		return protocol.MissionArchiveResult{}, err
	}
	if m.Phase != domain.MissionPhaseCompleted && m.Phase != domain.MissionPhaseCancelled {
		return protocol.MissionArchiveResult{}, fmt.Errorf("%w: mission.archive: swarm is %s; cancel it first", store.ErrMissionPhase, m.Phase)
	}
	retire, err := s.retirer()
	if err != nil {
		return protocol.MissionArchiveResult{}, err
	}
	runs, err := s.stoppedMissionRuns(ctx, m, "mission.archive")
	if err != nil {
		return protocol.MissionArchiveResult{}, err
	}
	merged, err := s.mergedRuns(ctx, m)
	if err != nil {
		return protocol.MissionArchiveResult{}, err
	}
	for _, run := range runs {
		if run.Status == domain.RunCompleted {
			outcome := domain.RunAbandoned
			if merged[run.ID] {
				outcome = domain.RunMerged
			}
			if err := retire.CloseRun(ctx, run.ID, actor, outcome); err != nil {
				return protocol.MissionArchiveResult{}, fmt.Errorf("mission.archive: close run %s: %w", run.ID, err)
			}
		}
		if _, err := retire.SetArchived(ctx, run.ID, actor, true); err != nil {
			return protocol.MissionArchiveResult{}, fmt.Errorf("mission.archive: archive run %s: %w", run.ID, err)
		}
	}
	now := s.cfg.Now().UTC()
	return s.setArchived(ctx, m.ID, &now)
}

// Unarchive restores an archived swarm and every archived run of it. Closed
// runs stay closed.
func (s *Service) Unarchive(ctx context.Context, actor domain.MemberID, p protocol.MissionIDParams) (protocol.MissionArchiveResult, error) {
	m, err := s.authorizedMission(ctx, actor, p.MissionID, "unarchive this swarm")
	if err != nil {
		return protocol.MissionArchiveResult{}, err
	}
	retire, err := s.retirer()
	if err != nil {
		return protocol.MissionArchiveResult{}, err
	}
	ids, err := s.cfg.Missions.ListMissionRunIDs(ctx, m.ID)
	if err != nil {
		return protocol.MissionArchiveResult{}, err
	}
	for _, id := range ids {
		if _, err := retire.SetArchived(ctx, id, actor, false); err != nil && !errors.Is(err, store.ErrNotFound) {
			return protocol.MissionArchiveResult{}, fmt.Errorf("mission.unarchive: restore run %s: %w", id, err)
		}
	}
	return s.setArchived(ctx, m.ID, nil)
}

// Delete removes the swarm's records before its runs: a submission
// references its worker run.
func (s *Service) Delete(ctx context.Context, actor domain.MemberID, p protocol.MissionIDParams) error {
	m, err := s.authorizedMission(ctx, actor, p.MissionID, "delete this swarm")
	if err != nil {
		return err
	}
	retire, err := s.retirer()
	if err != nil {
		return err
	}
	runs, err := s.stoppedMissionRuns(ctx, m, "mission.delete")
	if err != nil {
		return err
	}
	if err := s.cfg.Missions.DeleteMission(ctx, m.ID); err != nil {
		return err
	}
	var errs []error
	for _, run := range runs {
		if err := retire.DeleteRun(ctx, run.ID, actor); err != nil && !errors.Is(err, store.ErrNotFound) {
			errs = append(errs, fmt.Errorf("delete run %s: %w", run.ID, err))
		}
	}
	if err := s.publishMissionDeleted(ctx, m, actor); err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return fmt.Errorf("mission.delete: swarm %s deleted, but: %w", m.ID, errors.Join(errs...))
	}
	return nil
}

// sweepArchived deletes a swarm archived longer than the run retention
// period. Its runs, archived with it, are left to the scheduler's own
// archive sweep, which skips them while this swarm still references them.
func (s *Service) sweepArchived(ctx context.Context, m *domain.Mission) error {
	if m.ArchivedAt == nil || s.cfg.Now().Sub(*m.ArchivedAt) < domain.ArchiveRetention {
		return nil
	}
	if err := s.cfg.Missions.DeleteMission(ctx, m.ID); err != nil {
		return err
	}
	return s.publishMissionDeleted(ctx, m, "")
}

func (s *Service) publishMissionDeleted(ctx context.Context, m *domain.Mission, actor domain.MemberID) error {
	if s.cfg.Bus == nil {
		return nil
	}
	if _, err := s.cfg.Bus.Publish(ctx, events.Event{
		WorkspaceID: m.WorkspaceID,
		ActorID:     actor,
		Payload:     events.MissionChangedPayload{MissionID: m.ID, Deleted: true},
	}); err != nil {
		return fmt.Errorf("publish deleted mission %s: %w", m.ID, err)
	}
	return nil
}

func (s *Service) authorizedMission(ctx context.Context, actor domain.MemberID, id, operation string) (*domain.Mission, error) {
	if id == "" {
		return nil, invalidMissionParams("mission_id is required")
	}
	s.cfg.AuthorizationMu.Lock()
	defer s.cfg.AuthorizationMu.Unlock()
	m, err := s.cfg.Missions.GetMission(ctx, domain.MissionID(id))
	if err != nil {
		return nil, err
	}
	if err := s.authorizeMissionHuman(ctx, actor, m, operation); err != nil {
		return nil, err
	}
	return m, nil
}

func (s *Service) retirer() (RunRetirer, error) {
	if s.cfg.Retire == nil {
		return nil, errors.New("mission: scheduler unavailable")
	}
	return s.cfg.Retire, nil
}

func (s *Service) stoppedMissionRuns(ctx context.Context, m *domain.Mission, operation string) ([]*domain.Run, error) {
	ids, err := s.cfg.Missions.ListMissionRunIDs(ctx, m.ID)
	if err != nil {
		return nil, err
	}
	var runs []*domain.Run
	var live []string
	for _, id := range ids {
		run, err := s.cfg.Store.GetRun(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !run.Status.Terminal() {
			live = append(live, fmt.Sprintf("%s is %s", run.ID, run.Status))
		}
		runs = append(runs, run)
	}
	if len(live) > 0 {
		return nil, fmt.Errorf("%w: %s: run %s; cancel the swarm and wait for its runs to stop", store.ErrMissionPhase, operation, strings.Join(live, ", run "))
	}
	return runs, nil
}

func (s *Service) mergedRuns(ctx context.Context, m *domain.Mission) (map[domain.RunID]bool, error) {
	merged := map[domain.RunID]bool{}
	if m.Phase != domain.MissionPhaseCompleted {
		return merged, nil
	}
	if m.CurrentIntegratorRunID != "" {
		merged[m.CurrentIntegratorRunID] = true
	}
	submissions, err := s.cfg.Missions.ListSubmissions(ctx, m.ID, "")
	if err != nil {
		return nil, err
	}
	for _, submission := range submissions {
		if submission.State == domain.SubmissionAccepted {
			merged[submission.Ref.RunID] = true
		}
	}
	return merged, nil
}

func (s *Service) setArchived(ctx context.Context, id domain.MissionID, at *time.Time) (protocol.MissionArchiveResult, error) {
	changed, err := s.cfg.Missions.SetMissionArchived(ctx, id, at)
	if err != nil {
		return protocol.MissionArchiveResult{}, err
	}
	if changed {
		if publishErr := s.publishMissionChanged(ctx, id); publishErr != nil {
			return protocol.MissionArchiveResult{}, publishErr
		}
	}
	current, err := s.cfg.Missions.GetMission(ctx, id)
	if err != nil {
		return protocol.MissionArchiveResult{}, err
	}
	return protocol.MissionArchiveResult{Mission: protocol.MissionFromDomain(current)}, nil
}
