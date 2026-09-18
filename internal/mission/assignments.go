package mission

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

const (
	missionRoleIntegrator = "integrator"
	missionRoleWorker     = "worker"
	missionPeerLimit      = 8
)

// resolveAssignment is the sole authority lookup for mission coordination.
// A nil mission means that the run is genuinely unrelated to a mission.  A
// mission without an attempt is the current integrator; a mission with an
// attempt is a current worker only when that attempt still owns the task's
// current revision and is the latest attempt for that revision.
//
// In particular, a human takeover row is never used to infer worker
// identity.  Takeover is additional state on an already-authorized attempt,
// not an assignment record.
func (s *Service) resolveAssignment(ctx context.Context, run domain.RunID) (*domain.Mission, *domain.Attempt, error) {
	if run == "" {
		return nil, nil, errors.New("mission: run is required")
	}
	if current, err := s.cfg.Store.GetRun(ctx, run); err != nil {
		return nil, nil, err
	} else if current == nil {
		return nil, nil, store.ErrNotFound
	}

	m, err := s.cfg.Missions.GetMissionByRun(ctx, run)
	if err == nil && m != nil {
		if m.CurrentIntegratorRunID == run {
			return m, nil, nil
		}
		// Some MissionStore implementations return a durable mission for a
		// retired coordinator.  It is not a current authority; continue with
		// the attempt lookup only if this run is an actual worker.
		return s.resolveWorkerAssignment(ctx, run, m)
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		// ErrMissionStale identifies a known retired integrator.  Preserve it
		// so callers fail closed instead of falling through to the radar.
		return nil, nil, err
	}

	// Worker runs are mapped by the durable attempt, independently of the
	// coordinator generation.  This is what keeps an existing worker valid
	// while an integrator is replaced.
	attempt, attemptErr := s.cfg.Missions.GetAttemptByRun(ctx, run)
	if attemptErr == nil && attempt != nil {
		m, err = s.cfg.Missions.GetMission(ctx, attempt.MissionID)
		if err != nil {
			return nil, nil, err
		}
		return s.resolveWorkerAssignment(ctx, run, m)
	}
	if attemptErr != nil && !errors.Is(attemptErr, store.ErrNotFound) {
		return nil, nil, attemptErr
	}

	return nil, nil, nil
}

func (s *Service) resolveWorkerAssignment(ctx context.Context, run domain.RunID, m *domain.Mission) (*domain.Mission, *domain.Attempt, error) {
	if m == nil {
		return nil, nil, errors.New("mission: assignment has no mission")
	}
	if m.CurrentIntegratorRunID == run {
		return m, nil, nil
	}
	attempt, err := s.cfg.Missions.GetAttemptByRun(ctx, run)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return m, nil, fmt.Errorf("%w: run is not a current mission assignment", store.ErrMissionStale)
		}
		return nil, nil, err
	}
	if attempt == nil || attempt.MissionID != m.ID || attempt.RunID != run {
		return m, nil, fmt.Errorf("%w: worker assignment identity mismatch", store.ErrMissionStale)
	}
	task, err := s.cfg.Missions.GetTask(ctx, attempt.TaskID)
	if err != nil {
		return nil, nil, err
	}
	if task == nil || task.MissionID != m.ID || task.Revision == nil || task.CurrentRevision != attempt.TaskRevision {
		return m, nil, fmt.Errorf("%w: worker task revision is stale", store.ErrMissionStale)
	}
	attempts, err := s.cfg.Missions.ListAttempts(ctx, m.ID, attempt.TaskID)
	if err != nil {
		return nil, nil, err
	}
	var latest *domain.Attempt
	for _, candidate := range attempts {
		if candidate == nil || candidate.TaskRevision != task.CurrentRevision {
			continue
		}
		if latest == nil || candidate.Number > latest.Number || (candidate.Number == latest.Number && candidate.ID > latest.ID) {
			latest = candidate
		}
	}
	if latest == nil || latest.ID != attempt.ID || !attempt.State.HoldsConcurrency() {
		return m, nil, fmt.Errorf("%w: worker attempt is no longer current", store.ErrMissionStale)
	}
	return m, attempt, nil
}

// Assignment returns only server-derived mission authority.  Ordinary runs
// receive the zero value and retain the existing coordination surface.
func (s *Service) Assignment(ctx context.Context, run domain.RunID) (protocol.CoordMissionAssignment, error) {
	m, attempt, err := s.resolveAssignment(ctx, run)
	if err != nil {
		return protocol.CoordMissionAssignment{}, err
	}
	if m == nil {
		return protocol.CoordMissionAssignment{}, nil
	}
	attempts, err := s.cfg.Missions.ListAttempts(ctx, m.ID, "")
	if err != nil {
		return protocol.CoordMissionAssignment{}, err
	}
	choices := make([]protocol.MissionExecutionChoice, 0, len(m.ExecutionChoices))
	for _, choice := range m.ExecutionChoices {
		choices = append(choices, protocol.MissionExecutionChoice{AccountMemberID: string(choice.AccountMemberID), Harness: choice.Harness, Mode: string(choice.Mode)})
	}
	out := protocol.CoordMissionAssignment{
		MissionID:             string(m.ID),
		IntegratorRunID:       string(m.CurrentIntegratorRunID),
		IntegratorGeneration:  m.IntegratorGeneration,
		ExecutionChoices:      choices,
		MaxConcurrentAttempts: m.MaxConcurrentAttempts,
		MaxTotalAttempts:      m.MaxTotalAttempts,
		TotalAttempts:         len(attempts),
	}
	for _, candidate := range attempts {
		if candidate != nil && candidate.State.HoldsConcurrency() {
			out.ActiveAttempts++
		}
	}
	if attempt == nil {
		out.Role = missionRoleIntegrator
		out.Capabilities = integratorCapabilities()
		return out, nil
	}
	out.Role = missionRoleWorker
	out.TaskID = string(attempt.TaskID)
	out.TaskRevision = attempt.TaskRevision
	out.AttemptID = string(attempt.ID)
	out.Capabilities = workerCapabilities()
	return out, nil
}

func integratorCapabilities() []string {
	return []string{
		protocol.MethodTaskShow,
		protocol.MethodTaskList,
		protocol.MethodTaskPropose,
		protocol.MethodTaskRevise,
		protocol.MethodTaskAccept,
		protocol.MethodTaskAcceptSubmission,
		protocol.MethodTaskAbandon,
		protocol.MethodWorkerStart,
		protocol.MethodWorkerList,
		protocol.MethodWorkerInspect,
		protocol.MethodWorkerCancel,
		protocol.MethodWorkerRetry,
		protocol.MethodIntegrationPrepare,
		protocol.MethodIntegrationShow,
		protocol.MethodIntegrationVerify,
		protocol.MethodIntegrationRequestDelivery,
		protocol.MethodIntegrationDeliver,
	}
}

func workerCapabilities() []string {
	return []string{
		protocol.MethodTaskShow,
		protocol.MethodTaskList,
		protocol.MethodTaskPropose,
		protocol.MethodTaskRevise,
		protocol.MethodWorkerInspect,
	}
}

// Peers returns the current mission membership, not historical attempts and
// not human takeover rows.  Every member is resolved from its durable run so
// peer authorization is workspace-bound and symmetric.
func (s *Service) Peers(ctx context.Context, run domain.RunID) ([]protocol.CoordPeer, error) {
	m, _, err := s.resolveAssignment(ctx, run)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, nil
	}
	ids := make(map[domain.RunID]struct{}, missionPeerLimit+1)
	ids[m.CurrentIntegratorRunID] = struct{}{}
	attempts, err := s.cfg.Missions.ListAttempts(ctx, m.ID, "")
	if err != nil {
		return nil, err
	}
	for _, a := range attempts {
		if a == nil || !a.State.HoldsConcurrency() {
			continue
		}
		task, taskErr := s.cfg.Missions.GetTask(ctx, a.TaskID)
		if taskErr != nil {
			return nil, taskErr
		}
		if task == nil || task.Revision == nil || task.CurrentRevision != a.TaskRevision {
			continue
		}
		latest := false
		for _, candidate := range attempts {
			if candidate == nil || candidate.TaskID != a.TaskID || candidate.TaskRevision != a.TaskRevision || !candidate.State.HoldsConcurrency() {
				continue
			}
			if candidate.Number > a.Number || (candidate.Number == a.Number && candidate.ID > a.ID) {
				latest = false
				break
			}
			latest = true
		}
		if latest {
			ids[a.RunID] = struct{}{}
		}
	}
	delete(ids, run)
	ordered := make([]domain.RunID, 0, len(ids))
	for id := range ids {
		if id != "" {
			ordered = append(ordered, id)
		}
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	out := make([]protocol.CoordPeer, 0, missionPeerLimit)
	for _, id := range ordered {
		r, runErr := s.cfg.Store.GetRun(ctx, id)
		if errors.Is(runErr, store.ErrNotFound) {
			continue
		}
		if runErr != nil {
			return nil, runErr
		}
		if r == nil || r.WorkspaceID != m.WorkspaceID || r.Status.Final() {
			continue
		}
		if len(out) == missionPeerLimit {
			break
		}
		out = append(out, protocol.CoordPeer{RunID: string(r.ID), MemberID: string(r.MemberID), Task: r.Task, State: protocol.CoordPeerMission})
	}
	return out, nil
}
