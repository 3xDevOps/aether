package mission

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

type RunRetirer interface {
	CloseRun(context.Context, domain.RunID, domain.MemberID, domain.RunStatus) error
	SetMissionArchived(context.Context, domain.MissionID, []domain.RunID, domain.MemberID, *time.Time) (bool, error)
	TeardownRun(context.Context, domain.RunID, domain.MemberID) error
	DeleteMission(context.Context, *domain.Mission, []domain.RunID, domain.MemberID) error
}

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
	runs, err := s.retirableRuns(ctx, m, actor, "mission.archive")
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
	}
	now := s.cfg.Now().UTC()
	return s.setArchived(ctx, retire, m.ID, runs, actor, &now)
}

func (s *Service) Unarchive(ctx context.Context, actor domain.MemberID, p protocol.MissionIDParams) (protocol.MissionArchiveResult, error) {
	m, err := s.authorizedMission(ctx, actor, p.MissionID, "unarchive this swarm")
	if err != nil {
		return protocol.MissionArchiveResult{}, err
	}
	if m.ArchivedAt == nil {
		return protocol.MissionArchiveResult{Mission: protocol.MissionFromDomain(m)}, nil
	}
	retire, err := s.retirer()
	if err != nil {
		return protocol.MissionArchiveResult{}, err
	}
	runs, err := s.retirableRuns(ctx, m, actor, "mission.unarchive")
	if err != nil {
		return protocol.MissionArchiveResult{}, err
	}
	return s.setArchived(ctx, retire, m.ID, runs, actor, nil)
}

// Delete tears down every run before removing any row, so a failed teardown
// leaves the swarm, its results and its runs for a retry.
func (s *Service) Delete(ctx context.Context, actor domain.MemberID, p protocol.MissionIDParams) error {
	m, err := s.authorizedMission(ctx, actor, p.MissionID, "delete this swarm")
	if err != nil {
		return err
	}
	retire, err := s.retirer()
	if err != nil {
		return err
	}
	runs, err := s.retirableRuns(ctx, m, actor, "mission.delete")
	if err != nil {
		return err
	}
	ids := make([]domain.RunID, len(runs))
	for i, run := range runs {
		if err := retire.TeardownRun(ctx, run.ID, actor); err != nil && !errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("mission.delete: delete run %s: %w", run.ID, err)
		}
		ids[i] = run.ID
	}
	if err := retire.DeleteMission(ctx, m, ids, actor); err != nil {
		return err
	}
	return s.publishMissionDeleted(ctx, m, actor)
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

// retirableRuns applies run.delete's Kill check to each run.
func (s *Service) retirableRuns(ctx context.Context, m *domain.Mission, actor domain.MemberID, operation string) ([]*domain.Run, error) {
	ids, err := s.cfg.Missions.ListMissionRunIDs(ctx, m.ID)
	if err != nil {
		return nil, err
	}
	member, err := s.cfg.Store.GetMember(ctx, actor)
	if err != nil {
		return nil, err
	}
	ws, err := s.cfg.Store.GetWorkspace(ctx, m.WorkspaceID)
	if err != nil {
		return nil, err
	}
	killer := permissions.Actor{ID: member.ID, Role: member.Role}
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
		target := permissions.Target{Workspace: run.WorkspaceID, Owner: run.MemberID, Protected: run.Protected, SteerOthers: ws.SteerOthers}
		if err := permissions.Check(permissions.Kill, killer, target); err != nil {
			return nil, fmt.Errorf("%s: run %s: %w", operation, run.ID, err)
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

func (s *Service) setArchived(ctx context.Context, retire RunRetirer, id domain.MissionID, runs []*domain.Run, actor domain.MemberID, at *time.Time) (protocol.MissionArchiveResult, error) {
	ids := make([]domain.RunID, len(runs))
	for i, run := range runs {
		ids[i] = run.ID
	}
	changed, err := retire.SetMissionArchived(ctx, id, ids, actor, at)
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
