package mission

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/sshd"
	"github.com/3xDevOps/Aether/internal/store"
)

// handleTaskRead serves task reads after resolving the caller's durable
// assignment. The assignment, rather than request fields, determines the
// mission and (for workers) the one task visible to the caller.
func (s *Service) handleTaskRead(ctx context.Context, run domain.RunID, method string, raw json.RawMessage) (any, error) {
	mission, attempt, err := s.resolveAssignment(ctx, run)
	if err != nil {
		return nil, err
	}
	if mission == nil {
		return nil, fmt.Errorf("%w: run has no mission assignment", store.ErrMissionStale)
	}
	switch method {
	case protocol.MethodTaskShow:
		var p protocol.TaskShowParams
		if parseErr := json.Unmarshal(raw, &p); parseErr != nil || strings.TrimSpace(p.TaskID) == "" {
			return nil, errors.New("mission: task.show requires task_id")
		}
		task, err := s.cfg.Missions.ProjectTask(ctx, domain.TaskID(p.TaskID))
		if err != nil {
			return nil, err
		}
		if visibleErr := taskVisibleToAssignment(task, mission, attempt); visibleErr != nil {
			return nil, visibleErr
		}
		return protocol.TaskShowResult{Task: taskWire(task)}, nil
	case protocol.MethodTaskList:
		var p protocol.TaskListParams
		if parseErr := json.Unmarshal(raw, &p); parseErr != nil || strings.TrimSpace(p.MissionID) == "" {
			return nil, errors.New("mission: task.list requires mission_id")
		}
		if domain.MissionID(p.MissionID) != mission.ID {
			return nil, fmt.Errorf("%w: mission is outside assignment", store.ErrMissionStale)
		}
		if attempt != nil {
			task, err := s.cfg.Missions.ProjectTask(ctx, attempt.TaskID)
			if err != nil {
				return nil, err
			}
			if visibleErr := taskVisibleToAssignment(task, mission, attempt); visibleErr != nil {
				return nil, visibleErr
			}
			return protocol.TaskListResult{Tasks: []protocol.Task{taskWire(task)}}, nil
		}
		tasks, err := s.cfg.Missions.ListTasks(ctx, mission.ID)
		if err != nil {
			return nil, err
		}
		out := protocol.TaskListResult{Tasks: make([]protocol.Task, 0, len(tasks))}
		for _, task := range tasks {
			out.Tasks = append(out.Tasks, taskWire(task))
		}
		return out, nil
	default:
		return nil, errors.New("mission: method not found")
	}
}

func taskVisibleToAssignment(task *domain.Task, mission *domain.Mission, attempt *domain.Attempt) error {
	if task == nil || mission == nil || task.MissionID != mission.ID {
		return fmt.Errorf("%w: task is outside assignment", store.ErrMissionStale)
	}
	if attempt != nil && (task.ID != attempt.TaskID || task.CurrentRevision != attempt.TaskRevision) {
		return fmt.Errorf("%w: task is outside worker assignment", store.ErrMissionStale)
	}
	return nil
}

// handleTaskMutation implements the authenticated task state machine. Task
// proposals create server-issued tasks; workers may propose new split tasks or
// revisions for their own assignment, while only integrators can accept or
// abandon and accept submissions.
func (s *Service) handleTaskMutation(ctx context.Context, run domain.RunID, method string, raw json.RawMessage) (any, error) {
	s.cfg.AuthorizationMu.Lock()
	defer s.cfg.AuthorizationMu.Unlock()
	mission, attempt, err := s.resolveAssignment(ctx, run)
	if err != nil {
		return nil, err
	}
	if mission == nil {
		return nil, fmt.Errorf("%w: run has no mission assignment", store.ErrMissionStale)
	}
	switch method {
	case protocol.MethodTaskAcceptSubmission:
		return s.acceptSubmissionLocked(ctx, run, raw, mission, attempt)
	case protocol.MethodTaskPropose:
		var p protocol.TaskProposeParams
		if parseErr := json.Unmarshal(raw, &p); parseErr != nil {
			return nil, parseErr
		}
		if strings.TrimSpace(p.MissionID) == "" || !validTaskKey(p.IdempotencyKey) {
			return nil, errors.New("mission: task.propose requires mission_id, revision, and idempotency_key")
		}
		if domain.MissionID(p.MissionID) != mission.ID {
			return nil, fmt.Errorf("%w: mission is outside assignment", store.ErrMissionStale)
		}
		if p.Revision.TaskID != "" {
			return nil, errors.New("mission: task.propose does not accept task_id")
		}
		if authorizeErr := s.authorizeMissionTaskActor(ctx, mission, attempt); authorizeErr != nil {
			return nil, authorizeErr
		}
		r := revisionFromWire(p.Revision, run)
		// A worker may propose a split or a new task in its mission. The
		t := &domain.Task{MissionID: mission.ID, Revision: r}
		if createErr := s.createTask(ctx, t, p.IdempotencyKey); createErr != nil {
			return nil, createErr
		}
		if publishErr := s.publishMissionChanged(ctx, mission.ID); publishErr != nil {
			return nil, publishErr
		}
		projected, err := s.cfg.Missions.ProjectTask(ctx, t.ID)
		if err != nil {
			return nil, err
		}
		return protocol.TaskMutationResult{Task: taskWire(projected)}, nil

	case protocol.MethodTaskRevise:
		var p protocol.TaskReviseParams
		if parseErr := json.Unmarshal(raw, &p); parseErr != nil {
			return nil, parseErr
		}
		if strings.TrimSpace(p.TaskID) == "" || !validTaskKey(p.IdempotencyKey) {
			return nil, errors.New("mission: task.revise requires task_id, revision, and idempotency_key")
		}
		task, err := s.cfg.Missions.GetTask(ctx, domain.TaskID(p.TaskID))
		if err != nil {
			return nil, err
		}
		if visibleErr := taskVisibleToAssignment(task, mission, attempt); visibleErr != nil {
			return nil, visibleErr
		}
		if authorizeErr := s.authorizeMissionTaskActor(ctx, mission, attempt); authorizeErr != nil {
			return nil, authorizeErr
		}
		if p.Revision.TaskID != "" && domain.TaskID(p.Revision.TaskID) != task.ID {
			return nil, fmt.Errorf("%w: revision task does not match target", store.ErrMissionStale)
		}
		r := revisionFromWire(p.Revision, run)
		r.TaskID = task.ID
		if attempt != nil && task.CurrentRevision != attempt.TaskRevision {
			return nil, fmt.Errorf("%w: worker task revision is not current", store.ErrMissionStale)
		}
		var proposed *domain.TaskRevision
		if attempt == nil {
			proposed, err = s.cfg.Missions.ProposeTaskRevision(ctx, task.ID, r, p.IdempotencyKey)
		} else {
			proposed, err = s.cfg.Missions.ReviseTask(ctx, task.ID, r, p.IdempotencyKey)
		}
		if err != nil {
			return nil, err
		}
		if publishErr := s.publishMissionChanged(ctx, mission.ID); publishErr != nil {
			return nil, publishErr
		}
		projected, err := s.cfg.Missions.ProjectTask(ctx, task.ID)
		if err != nil {
			return nil, err
		}
		if proposed != nil && projected.Revision == nil {
			projected.Revision = proposed
		}
		return protocol.TaskMutationResult{Task: taskWire(projected)}, nil

	case protocol.MethodTaskAccept:
		var p protocol.TaskAcceptParams
		if parseErr := json.Unmarshal(raw, &p); parseErr != nil {
			return nil, parseErr
		}
		if attempt != nil {
			return nil, errors.New("mission: workers cannot accept task revisions")
		}
		if strings.TrimSpace(p.TaskID) == "" || p.Revision <= 0 || !validTaskKey(p.IdempotencyKey) || p.ExpectedIntegratorGeneration == 0 {
			return nil, errors.New("mission: task.accept requires task, revision, expected_integrator_generation, and idempotency_key")
		}
		task, err := s.cfg.Missions.GetTask(ctx, domain.TaskID(p.TaskID))
		if err != nil {
			return nil, err
		}
		if visibleErr := taskVisibleToAssignment(task, mission, nil); visibleErr != nil {
			return nil, visibleErr
		}
		if p.ExpectedIntegratorGeneration != mission.IntegratorGeneration {
			return nil, fmt.Errorf("%w: stale integrator authority", store.ErrMissionStale)
		}
		if authorizeErr := s.authorizeMissionTaskActor(ctx, mission, nil); authorizeErr != nil {
			return nil, authorizeErr
		}
		if acceptErr := s.cfg.Missions.AcceptTaskRevision(ctx, task.ID, p.Revision, p.ExpectedIntegratorGeneration, p.IdempotencyKey); acceptErr != nil {
			return nil, acceptErr
		}
		if publishErr := s.publishMissionChanged(ctx, mission.ID); publishErr != nil {
			return nil, publishErr
		}
		projected, err := s.cfg.Missions.ProjectTask(ctx, task.ID)
		if err != nil {
			return nil, err
		}
		return protocol.TaskMutationResult{Task: taskWire(projected)}, nil

	case protocol.MethodTaskAbandon:
		var p protocol.TaskAbandonParams
		if parseErr := json.Unmarshal(raw, &p); parseErr != nil {
			return nil, parseErr
		}
		if attempt != nil {
			return nil, errors.New("mission: workers cannot abandon tasks")
		}
		if strings.TrimSpace(p.TaskID) == "" || !validTaskKey(p.IdempotencyKey) || p.ExpectedIntegratorGeneration == 0 {
			return nil, errors.New("mission: task.abandon requires task, expected_integrator_generation, and idempotency_key")
		}
		task, err := s.cfg.Missions.GetTask(ctx, domain.TaskID(p.TaskID))
		if err != nil {
			return nil, err
		}
		if visibleErr := taskVisibleToAssignment(task, mission, nil); visibleErr != nil {
			return nil, visibleErr
		}
		if authorizeErr := s.authorizeMissionTaskActor(ctx, mission, nil); authorizeErr != nil {
			return nil, authorizeErr
		}
		if p.ExpectedIntegratorGeneration != mission.IntegratorGeneration {
			return nil, fmt.Errorf("%w: stale integrator authority", store.ErrMissionStale)
		}
		if abandonErr := s.cfg.Missions.AbandonTask(ctx, task.ID, p.ExpectedIntegratorGeneration, p.IdempotencyKey); abandonErr != nil {
			return nil, abandonErr
		}
		if publishErr := s.publishMissionChanged(ctx, mission.ID); publishErr != nil {
			return nil, publishErr
		}
		projected, err := s.cfg.Missions.ProjectTask(ctx, task.ID)
		if err != nil {
			return nil, err
		}
		return protocol.TaskMutationResult{Task: taskWire(projected)}, nil
	default:
		return nil, errors.New("mission: method not found")
	}
}

func (s *Service) acceptSubmissionLocked(ctx context.Context, run domain.RunID, raw json.RawMessage, mission *domain.Mission, attempt *domain.Attempt) (any, error) {
	var p protocol.TaskAcceptSubmissionParams
	if parseErr := json.Unmarshal(raw, &p); parseErr != nil {
		return nil, parseErr
	}
	if !validTaskKey(p.IdempotencyKey) || p.SubmissionID == "" || p.ExpectedIntegratorGeneration == 0 {
		return nil, errors.New("mission: task.submission.accept requires submission_id, expected_integrator_generation, and idempotency_key")
	}
	if mission == nil || attempt != nil {
		return nil, errors.New("mission: only the current integrator may accept submissions")
	}
	if p.ExpectedIntegratorGeneration != mission.IntegratorGeneration {
		return nil, fmt.Errorf("%w: stale integrator authority", store.ErrMissionStale)
	}
	submission, err := s.cfg.Missions.GetSubmission(ctx, domain.SubmissionID(p.SubmissionID))
	if err != nil {
		return nil, err
	}
	if submission == nil || submission.MissionID != mission.ID {
		return nil, fmt.Errorf("%w: submission is outside assignment", store.ErrMissionStale)
	}
	task, err := s.cfg.Missions.GetTask(ctx, submission.TaskID)
	if err != nil {
		return nil, err
	}
	if visibleErr := taskVisibleToAssignment(task, mission, nil); visibleErr != nil {
		return nil, visibleErr
	}
	if authorizeErr := s.authorizeMissionTaskActor(ctx, mission, nil); authorizeErr != nil {
		return nil, authorizeErr
	}
	if submission.State == domain.SubmissionProposed {
		if evidenceErr := s.validateSubmissionEvidence(ctx, mission, task, submission); evidenceErr != nil {
			return nil, evidenceErr
		}
	}
	if len(submission.ScopeViolations) > 0 && strings.TrimSpace(p.ScopeDisposition) == "" {
		return nil, errors.New("mission: scope disposition is required for out-of-scope submission")
	}
	accepted, err := s.cfg.Missions.AcceptSubmission(ctx, submission.ID, run, p.ExpectedIntegratorGeneration, p.ExpectedAcceptedSetVersion, p.ScopeDisposition, p.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	if publishErr := s.publishMissionChanged(ctx, accepted.MissionID); publishErr != nil {
		return nil, publishErr
	}
	return protocol.TaskMutationResult{Acceptance: &protocol.Acceptance{
		SubmissionID: string(accepted.SubmissionID), MissionID: string(accepted.MissionID), TaskID: string(accepted.TaskID),
		TaskRevision: accepted.TaskRevision, AcceptedSetVersion: accepted.AcceptedSetVersion,
		IntegratorGeneration: accepted.IntegratorGeneration, AcceptedByRunID: string(accepted.AcceptedByRunID),
		ScopeDisposition: accepted.ScopeDisposition, AcceptedAt: rfc3339Task(accepted.AcceptedAt),
	}}, nil
}

func (s *Service) authorizeMissionTaskActor(ctx context.Context, mission *domain.Mission, attempt *domain.Attempt) error {
	if mission == nil {
		return fmt.Errorf("%w: mission assignment is unavailable", store.ErrMissionStale)
	}
	account := mission.Integrator.AccountMemberID
	human := mission.IntegratorAuthorizingHumanID
	if attempt != nil {
		account = attempt.AccountOwnerID
		human = attempt.AuthorizingHumanID
	}
	if account == "" || human == "" {
		return errors.New("mission: task actor authorization is unavailable")
	}
	_, err := sshd.AuthorizeLaunch(ctx, s.cfg.Store, human, string(account))
	return err
}

func (s *Service) validateSubmissionEvidence(ctx context.Context, mission *domain.Mission, task *domain.Task, submission *domain.Submission) error {
	if s.cfg.Evidence == nil || mission == nil || task == nil || task.Revision == nil || submission == nil {
		return fmt.Errorf("%w: retained evidence is unavailable", store.ErrMissionNotReady)
	}
	packet, err := s.cfg.Evidence.Get(ctx, mission.WorkspaceID, submission.Ref.EvidenceRef)
	if err != nil {
		return fmt.Errorf("%w: retained evidence lookup failed: %v", store.ErrMissionNotReady, err)
	}
	if packet.ID != submission.Ref.EvidenceRef ||
		packet.WorkspaceID != string(mission.WorkspaceID) ||
		packet.RunID != string(submission.Ref.RunID) ||
		packet.RetainedRevision != submission.Ref.RetainedRevision {
		return fmt.Errorf("%w: retained evidence identity does not match submission", store.ErrMissionNotReady)
	}
	if available, detail := packetEvidenceState(s.cfg.Now, packet); !available {
		if detail == "" {
			detail = "retained evidence is unavailable"
		}
		return fmt.Errorf("%w: %s", store.ErrMissionNotReady, detail)
	}
	validKinds := make(map[string]bool, len(packet.Sources)+2)
	hasRetainedPacket := false
	validKinds["retained_packet"] = true
	for _, fact := range submission.Evidence {
		if fact.Kind == "retained_packet" && fact.Ref == packet.ID {
			hasRetainedPacket = true
		}
	}
	if !hasRetainedPacket {
		return fmt.Errorf("%w: retained packet evidence fact is missing", store.ErrMissionNotReady)
	}
	for _, source := range packet.Sources {
		name := strings.TrimSpace(source.Name)
		if name != "" && source.Available && !source.Truncated {
			validKinds[name] = true
		}
	}
	for _, fact := range submission.Evidence {
		ref := strings.TrimSpace(fact.Ref)
		if ref == "" || ref == packet.ID || fact.Kind == "retained_packet" {
			continue
		}
		other, lookupErr := s.cfg.Evidence.Get(ctx, mission.WorkspaceID, ref)
		if lookupErr != nil || other.ID != ref || other.WorkspaceID != string(mission.WorkspaceID) {
			continue
		}
		if available, _ := packetEvidenceState(s.cfg.Now, other); available {
			validKinds[fact.Kind] = true
		}
	}
	for _, requirement := range task.Revision.EvidenceRequirements {
		if !validKinds[requirement.Kind] {
			return fmt.Errorf("%w: required evidence %q is unavailable", store.ErrMissionNotReady, requirement.Kind)
		}
	}
	return nil
}

// createTask persists a server-issued task and its durable idempotency receipt.
func (s *Service) createTask(ctx context.Context, t *domain.Task, key string) error {
	created, _, err := s.cfg.Missions.CreateTaskWithIdempotency(ctx, t, key)
	if err == nil && created != nil {
		*t = *created
	}
	return err
}
func validTaskKey(key string) bool {
	return key != "" && len(key) <= 256 && !strings.ContainsAny(key, "\r\n\x00")
}

func revisionFromWire(in protocol.TaskRevision, run domain.RunID) *domain.TaskRevision {
	r := &domain.TaskRevision{
		TaskID: domain.TaskID(in.TaskID), Revision: in.Revision, Title: in.Title,
		Objective: in.Objective, Scope: scopeFromWire(in.Scope),
		SupersedesRevision: in.SupersedesRevision, ProposedByRunID: run,
		Status: domain.TaskRevisionProposed,
	}
	if len(in.EvidenceRequirements) > 0 {
		r.EvidenceRequirements = make([]domain.EvidenceRequirement, len(in.EvidenceRequirements))
		for i, e := range in.EvidenceRequirements {
			r.EvidenceRequirements[i] = domain.EvidenceRequirement{Kind: e.Kind, Detail: e.Detail}
		}
	}
	return r
}

func scopeFromWire(in protocol.TaskScope) domain.TaskScope {
	out := domain.TaskScope{SemanticResponsibility: in.SemanticResponsibility, Base: in.Base, Target: in.Target}
	out.ExpectedPaths = append([]string(nil), in.ExpectedPaths...)
	out.Migrations = append([]string(nil), in.Migrations...)
	out.SharedTests = append([]string(nil), in.SharedTests...)
	out.Exclusions = append([]string(nil), in.Exclusions...)
	if len(in.Interfaces) > 0 {
		out.Interfaces = make([]domain.InterfaceRevision, len(in.Interfaces))
		for i, v := range in.Interfaces {
			out.Interfaces[i] = domain.InterfaceRevision{Name: v.Name, Revision: v.Revision}
		}
	}
	return out
}

func taskWire(t *domain.Task) protocol.Task {
	out := protocol.Task{ID: string(t.ID), MissionID: string(t.MissionID), CurrentRevision: t.CurrentRevision, Status: string(t.Status), CreatedAt: rfc3339Task(t.CreatedAt), UpdatedAt: rfc3339Task(t.UpdatedAt)}
	if t.AbandonedAt != nil {
		v := rfc3339Task(*t.AbandonedAt)
		out.AbandonedAt = &v
	}
	if t.Revision != nil {
		r := revisionWire(t.Revision)
		out.Revision = &r
	}
	if len(t.Dependencies) > 0 {
		out.Dependencies = make([]protocol.TaskDependency, len(t.Dependencies))
		for i, d := range t.Dependencies {
			out.Dependencies[i] = protocol.TaskDependency{TaskID: string(d.TaskID), Revision: d.Revision, DependsOnTaskID: string(d.DependsOnTaskID), DependsOnRevision: d.DependsOnRevision, OutputRef: d.OutputRef, CreatedAt: rfc3339Task(d.CreatedAt)}
		}
	}
	if len(t.Blockers) > 0 {
		out.Blockers = make([]protocol.TaskBlocker, len(t.Blockers))
		for i, b := range t.Blockers {
			out.Blockers[i] = protocol.TaskBlocker{Kind: b.Kind, TaskID: string(b.TaskID), OwnerRunID: string(b.OwnerRunID), Action: b.Action}
		}
	}
	return out
}

func revisionWire(r *domain.TaskRevision) protocol.TaskRevision {
	out := protocol.TaskRevision{TaskID: string(r.TaskID), Revision: r.Revision, Title: r.Title, Objective: r.Objective, Scope: scopeWire(r.Scope), Status: string(r.Status), ProposedByRunID: string(r.ProposedByRunID), SupersedesRevision: r.SupersedesRevision, CreatedAt: rfc3339Task(r.CreatedAt)}
	if len(r.EvidenceRequirements) > 0 {
		out.EvidenceRequirements = make([]protocol.EvidenceRequirement, len(r.EvidenceRequirements))
		for i, e := range r.EvidenceRequirements {
			out.EvidenceRequirements[i] = protocol.EvidenceRequirement{Kind: e.Kind, Detail: e.Detail}
		}
	}
	if r.AcceptedAt != nil {
		v := rfc3339Task(*r.AcceptedAt)
		out.AcceptedAt = &v
	}
	return out
}

func scopeWire(in domain.TaskScope) protocol.TaskScope {
	out := protocol.TaskScope{ExpectedPaths: append([]string(nil), in.ExpectedPaths...), SemanticResponsibility: in.SemanticResponsibility, Migrations: append([]string(nil), in.Migrations...), SharedTests: append([]string(nil), in.SharedTests...), Base: in.Base, Target: in.Target, Exclusions: append([]string(nil), in.Exclusions...)}
	if len(in.Interfaces) > 0 {
		out.Interfaces = make([]protocol.InterfaceRevision, len(in.Interfaces))
		for i, v := range in.Interfaces {
			out.Interfaces[i] = protocol.InterfaceRevision{Name: v.Name, Revision: v.Revision}
		}
	}
	return out
}

func rfc3339Task(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}
