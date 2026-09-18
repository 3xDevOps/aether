package mission

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/3xDevOps/Aether/internal/domain"
	feature "github.com/3xDevOps/Aether/internal/integration"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/sshd"
	"github.com/3xDevOps/Aether/internal/store"
)

// integrationAdapter is the server-facing integration service. It keeps the
// candidate engine as the single implementation while making mission context
// authoritative at the boundary (including human prepare requests).
type integrationAdapter struct {
	owner *Service
}

// IntegrationAdapter returns the mission-aware adapter for the generic SSH
// integration service. The underlying engine is resolved lazily from Config,
// which lets server service registration order remain immaterial.
func (s *Service) IntegrationAdapter() sshd.IntegrationService {
	return &integrationAdapter{owner: s}
}

// SetIntegrationService is deliberately a tiny late-binding seam used by the
// server when the integration engine and mission services are registered in
// either order.
func (s *Service) SetIntegrationService(base sshd.IntegrationService) {
	if s == nil || base == nil {
		return
	}
	s.integrationMu.Lock()
	s.integrationBase = base
	s.integrationMu.Unlock()
}

func (a *integrationAdapter) SetIntegrationService(base sshd.IntegrationService) {
	if a != nil && a.owner != nil {
		a.owner.SetIntegrationService(base)
	}
}

func (a *integrationAdapter) engine() (sshd.IntegrationService, error) {
	if a == nil || a.owner == nil {
		return nil, feature.ErrUnavailable
	}
	return a.owner.integrationService()
}

func (a *integrationAdapter) Prepare(ctx context.Context, actor feature.Actor, p protocol.IntegrationPrepareParams) (protocol.Candidate, error) {
	p, err := a.owner.prepareIntegration(ctx, actor, p)
	if err != nil {
		return protocol.Candidate{}, err
	}
	engine, err := a.engine()
	if err != nil {
		return protocol.Candidate{}, err
	}
	return engine.Prepare(ctx, actor, p)
}
func (a *integrationAdapter) Show(ctx context.Context, actor feature.Actor, p protocol.IntegrationShowParams) (protocol.Candidate, error) {
	engine, err := a.engine()
	if err != nil {
		return protocol.Candidate{}, err
	}
	return engine.Show(ctx, actor, p)
}
func (a *integrationAdapter) List(ctx context.Context, actor feature.Actor, p protocol.IntegrationListParams) (protocol.IntegrationListResult, error) {
	engine, err := a.engine()
	if err != nil {
		return protocol.IntegrationListResult{}, err
	}
	return engine.List(ctx, actor, p)
}
func (a *integrationAdapter) Patch(ctx context.Context, actor feature.Actor, p protocol.IntegrationShowParams) (protocol.IntegrationPatchResult, error) {
	engine, err := a.engine()
	if err != nil {
		return protocol.IntegrationPatchResult{}, err
	}
	return engine.Patch(ctx, actor, p)
}
func (a *integrationAdapter) Resolve(ctx context.Context, actor feature.Actor, p protocol.IntegrationResolveParams) (protocol.Candidate, error) {
	engine, err := a.engine()
	if err != nil {
		return protocol.Candidate{}, err
	}
	return engine.Resolve(ctx, actor, p)
}
func (a *integrationAdapter) Verify(ctx context.Context, actor feature.Actor, p protocol.IntegrationVerifyParams) (protocol.Candidate, error) {
	engine, err := a.engine()
	if err != nil {
		return protocol.Candidate{}, err
	}
	return engine.Verify(ctx, actor, p)
}
func (a *integrationAdapter) RequestDelivery(ctx context.Context, actor feature.Actor, p protocol.IntegrationRequestDeliveryParams) (protocol.Candidate, error) {
	engine, err := a.engine()
	if err != nil {
		return protocol.Candidate{}, err
	}
	return engine.RequestDelivery(ctx, actor, p)
}
func (a *integrationAdapter) Decide(ctx context.Context, actor feature.Actor, p protocol.IntegrationDecideParams) (protocol.Candidate, error) {
	engine, err := a.engine()
	if err != nil {
		return protocol.Candidate{}, err
	}
	return engine.Decide(ctx, actor, p)
}
func (a *integrationAdapter) Deliver(ctx context.Context, actor feature.Actor, p protocol.IntegrationDeliverParams) (protocol.Candidate, error) {
	engine, err := a.engine()
	if err != nil {
		return protocol.Candidate{}, err
	}
	return engine.Deliver(ctx, actor, p)
}
func (a *integrationAdapter) Delete(ctx context.Context, actor feature.Actor, p protocol.IntegrationDeleteParams) error {
	engine, err := a.engine()
	if err != nil {
		return err
	}
	return engine.Delete(ctx, actor, p)
}

func (s *Service) integrationService() (sshd.IntegrationService, error) {
	s.integrationMu.RLock()
	base := s.integrationBase
	s.integrationMu.RUnlock()
	if base != nil {
		return base, nil
	}
	if s.cfg.Integration == nil {
		return nil, feature.ErrUnavailable
	}
	base, err := s.cfg.Integration()
	if err != nil {
		return nil, err
	}
	if base == nil {
		return nil, feature.ErrUnavailable
	}
	return base, nil
}

func missionDenied(format string, args ...any) error {
	return fmt.Errorf("%w: %s", feature.ErrUnauthorized, fmt.Sprintf(format, args...))
}
func missionConflict(format string, args ...any) error {
	return fmt.Errorf("%w: %s", feature.ErrConflict, fmt.Sprintf(format, args...))
}

func integrationReadOnly(op string) bool {
	switch op {
	case protocol.MethodIntegrationShow, protocol.MethodIntegrationList, protocol.MethodIntegrationPatch:
		return true
	default:
		return false
	}
}
func integrationCleanup(op string) bool { return op == protocol.MethodIntegrationDelete }

// AdmitIntegration is passed to the candidate engine. It takes the shared
// authorization fence before reading mutable mission/member/account state and
// returns a release function that the engine calls after its durable boundary.
func (s *Service) AdmitIntegration(ctx context.Context, a feature.Admission) (release func(), err error) {
	if s == nil || s.cfg.Store == nil || s.cfg.Missions == nil {
		return nil, feature.ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	mu := s.cfg.AuthorizationMu
	if mu == nil {
		return nil, feature.ErrUnavailable
	}
	mu.Lock()
	locked := true
	unlock := func() {
		if locked {
			locked = false
			mu.Unlock()
		}
	}
	defer func() {
		if err != nil {
			unlock()
		}
	}()

	missionID := domain.MissionID(a.MissionID)
	if a.Candidate != nil && a.Candidate.MissionID != "" {
		if missionID != "" && missionID != domain.MissionID(a.Candidate.MissionID) {
			return nil, missionConflict("candidate mission does not match request")
		}
		missionID = domain.MissionID(a.Candidate.MissionID)
	}
	if missionID == "" && len(a.Submissions) > 0 {
		m, found, findErr := s.missionForRefs(ctx, a.Submissions)
		if findErr != nil {
			return nil, findErr
		}
		if found {
			missionID = m.ID
		}
	}
	if missionID == "" {
		if a.Actor.RunID != "" {
			return nil, missionDenied("run actor has no mission assignment")
		}
		if err := s.authorizeIntegrationActor(ctx, a, ""); err != nil {
			return nil, err
		}
		return func() { unlock() }, nil
	}

	m, getErr := s.cfg.Missions.GetMission(ctx, missionID)
	if getErr != nil {
		return nil, getErr
	}
	if m == nil {
		return nil, store.ErrNotFound
	}
	// Reload while holding the shared fence: acceptance, replacement, and
	// account revocation all linearize through this mutex.
	m, getErr = s.cfg.Missions.GetMission(ctx, missionID)
	if getErr != nil {
		return nil, getErr
	}
	if m == nil {
		return nil, store.ErrNotFound
	}
	if err := s.authorizeMissionIntegration(ctx, a, m); err != nil {
		return nil, err
	}
	if integrationReadOnly(a.Operation) {
		return func() { unlock() }, nil
	}
	if integrationCleanup(a.Operation) {
		// Cleanup remains available to currently authorized human members,
		// including for historical candidates.
		return func() { unlock() }, nil
	}
	if a.Actor.RunID != "" && a.Operation == protocol.MethodIntegrationDecide {
		return nil, missionDenied("agent actors cannot approve delivery")
	}
	refs := a.Submissions
	if a.Candidate != nil {
		refs = a.Candidate.Submissions
		if a.NewCandidate {
			if a.Candidate.MissionID == "" {
				a.Candidate.MissionID = string(m.ID)
			}
			a.Candidate.MissionAcceptedSetVersion = m.AcceptedSetVersion
		} else if a.Candidate.MissionAcceptedSetVersion != m.AcceptedSetVersion {
			return nil, missionConflict("candidate accepted-set version is stale")
		}
	}
	current, currentErr := s.currentAcceptedRefs(ctx, m)
	if currentErr != nil {
		return nil, currentErr
	}
	if !submissionRefsSubset(refs, current) {
		return nil, missionConflict("candidate sources are not current accepted revisions")
	}
	return func() { unlock() }, nil
}
func integrationCapability(op string) permissions.Capability {
	if integrationReadOnly(op) {
		return permissions.View
	}
	return permissions.Push
}

// authorizeIntegrationActor repeats the engine's current member/workspace
// permission check under AuthorizationMu. The engine checks before calling
// the admission callback; this is the race-fenced second check.
func (s *Service) authorizeIntegrationActor(ctx context.Context, a feature.Admission, workspaceID domain.WorkspaceID) error {
	if a.WorkspaceID != "" && workspaceID != "" && a.WorkspaceID != workspaceID {
		return missionDenied("request workspace does not match authorization workspace")
	}
	if a.Candidate != nil && a.Candidate.WorkspaceID != "" {
		if workspaceID != "" && workspaceID != domain.WorkspaceID(a.Candidate.WorkspaceID) {
			return missionDenied("candidate workspace does not match request")
		}
		if a.WorkspaceID != "" && domain.WorkspaceID(a.WorkspaceID) != domain.WorkspaceID(a.Candidate.WorkspaceID) {
			return missionDenied("candidate workspace does not match request")
		}
		workspaceID = domain.WorkspaceID(a.Candidate.WorkspaceID)
	}
	if workspaceID == "" {
		workspaceID = a.WorkspaceID
	}
	if workspaceID == "" {
		return missionDenied("workspace is required")
	}
	ws, err := s.cfg.Store.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return err
	}
	if ws == nil {
		return store.ErrNotFound
	}
	memberID := a.Actor.MemberID
	target := permissions.Target{Workspace: workspaceID, SteerOthers: ws.SteerOthers}
	if a.Actor.RunID != "" {
		run, runErr := s.cfg.Store.GetRun(ctx, a.Actor.RunID)
		if runErr != nil {
			return runErr
		}
		if run == nil || run.WorkspaceID != workspaceID {
			return missionDenied("run actor is outside workspace")
		}
		memberID = run.MemberID
		target.Owner, target.Protected = run.MemberID, run.Protected
	}
	if memberID == "" {
		return feature.ErrUnauthorized
	}
	member, err := s.cfg.Store.GetMember(ctx, memberID)
	if err != nil {
		return err
	}
	if member == nil || member.Pending {
		return missionDenied("actor is unavailable")
	}
	if err := permissions.Check(integrationCapability(a.Operation), permissions.Actor{ID: member.ID, Role: member.Role}, target); err != nil {
		return fmt.Errorf("%w: %v", feature.ErrUnauthorized, err)
	}
	return nil
}

func (s *Service) authorizeMissionIntegration(ctx context.Context, a feature.Admission, m *domain.Mission) error {
	if err := s.authorizeIntegrationActor(ctx, a, m.WorkspaceID); err != nil {
		return err
	}
	if a.Actor.RunID == "" {
		// Human integration keeps the existing workspace capability policy;
		// it does not inherit the mission's delegating human or account.
		return nil
	}
	return s.authorizeMissionAgent(ctx, a, m)
}

func (s *Service) authorizeMissionAgent(ctx context.Context, a feature.Admission, m *domain.Mission) error {
	authorizer := m.IntegratorAuthorizingHumanID
	if authorizer == "" {
		authorizer = m.AccountableHumanID
	}
	if authorizer == "" || m.Integrator.AccountMemberID == "" {
		return missionDenied("mission has incomplete authorization")
	}
	admission, err := sshd.AuthorizeLaunch(ctx, s.cfg.Store, authorizer, string(m.Integrator.AccountMemberID))
	if err != nil {
		return err
	}
	run, err := s.cfg.Store.GetRun(ctx, a.Actor.RunID)
	if err != nil {
		return err
	}
	if run == nil || run.WorkspaceID != m.WorkspaceID || run.ID != a.Actor.RunID {
		return missionDenied("run actor is outside mission workspace")
	}
	owner := m.IntegratorRunOwnerID
	if owner == "" {
		owner = authorizer
	}
	if m.CurrentIntegratorRunID != a.Actor.RunID || run.MemberID != owner || run.AccountMember() != admission.Account.ID {
		return missionDenied("run actor is not the current integrator")
	}
	return nil
}

// prepareIntegration resolves mission context before the candidate engine sees
// the request. A matching source ref is enough to establish mission context;
// omission of mission_id therefore cannot bypass mission policy.
func (s *Service) prepareIntegration(ctx context.Context, actor feature.Actor, p protocol.IntegrationPrepareParams) (protocol.IntegrationPrepareParams, error) {
	var (
		m     *domain.Mission
		found bool
		err   error
	)
	if actor.RunID != "" && p.MissionID == "" {
		m, err = s.cfg.Missions.GetMissionByRun(ctx, actor.RunID)
		if err != nil {
			return p, err
		}
		found = m != nil
	} else {
		m, found, err = s.resolvePrepareMission(ctx, p)
		if err != nil {
			return p, err
		}
	}
	if !found {
		return p, nil
	}
	if p.WorkspaceID != "" && domain.WorkspaceID(p.WorkspaceID) != m.WorkspaceID {
		return p, missionConflict("workspace does not match mission")
	}
	p.WorkspaceID = string(m.WorkspaceID)
	p.MissionID = string(m.ID)
	refs, err := s.currentAcceptedRefs(ctx, m)
	if err != nil {
		return p, err
	}
	if len(p.Submissions) == 0 {
		p.Submissions = refs
	} else if !submissionRefsSubset(p.Submissions, refs) {
		return p, missionConflict("supplied submissions are not current accepted revisions")
	}
	_ = actor // actor is checked by AdmitIntegration after engine authorization.
	return p, nil
}

func (s *Service) resolvePrepareMission(ctx context.Context, p protocol.IntegrationPrepareParams) (*domain.Mission, bool, error) {
	if p.MissionID != "" {
		m, err := s.cfg.Missions.GetMission(ctx, domain.MissionID(p.MissionID))
		if err != nil {
			return nil, false, err
		}
		return m, true, nil
	}
	if len(p.Submissions) == 0 {
		return nil, false, nil
	}
	m, found, err := s.missionForRefs(ctx, p.Submissions)
	return m, found, err
}

func (s *Service) missionForRefs(ctx context.Context, refs []protocol.SubmissionRef) (*domain.Mission, bool, error) {
	if len(refs) == 0 {
		return nil, false, nil
	}
	var (
		found    *domain.Mission
		ordinary bool
	)
	for _, ref := range refs {
		var candidate *domain.Mission
		if ref.RunID == "" {
			ordinary = true
		} else {
			attempt, err := s.cfg.Missions.GetAttemptByRun(ctx, domain.RunID(ref.RunID))
			if err == nil && attempt != nil {
				candidate, err = s.cfg.Missions.GetMission(ctx, attempt.MissionID)
				if err != nil {
					return nil, false, err
				}
			} else {
				if err != nil && !errors.Is(err, store.ErrNotFound) {
					return nil, false, err
				}
				candidate, err = s.cfg.Missions.GetMissionByRun(ctx, domain.RunID(ref.RunID))
				switch {
				case err == nil:
				case errors.Is(err, store.ErrNotFound):
					ordinary = true
				default:
					return nil, false, err
				}
			}
		}
		if candidate == nil {
			continue
		}
		if found == nil {
			found = candidate
		} else if found.ID != candidate.ID {
			return nil, false, missionConflict("source refs belong to different missions")
		}
	}
	if found != nil && ordinary {
		return nil, false, missionConflict("source refs mix mission and ordinary runs")
	}
	if found == nil {
		return nil, false, nil
	}
	return found, true, nil
}
func (s *Service) currentAcceptedRefs(ctx context.Context, m *domain.Mission) ([]protocol.SubmissionRef, error) {
	tasks, err := s.cfg.Missions.ListTasks(ctx, m.ID)
	if err != nil {
		return nil, err
	}
	type accepted struct {
		version uint64
		id      string
		ref     protocol.SubmissionRef
	}
	all := make([]accepted, 0, len(tasks))
	for _, task := range tasks {
		if task == nil || task.AbandonedAt != nil || task.CurrentRevision <= 0 {
			continue
		}
		subs, err := s.cfg.Missions.ListSubmissions(ctx, m.ID, task.ID)
		if err != nil {
			return nil, err
		}
		for _, sub := range subs {
			if sub == nil || sub.Acceptance == nil || sub.State != domain.SubmissionAccepted || sub.TaskRevision != task.CurrentRevision || sub.Acceptance.TaskRevision != task.CurrentRevision {
				continue
			}
			all = append(all, accepted{version: sub.Acceptance.AcceptedSetVersion, id: string(sub.ID), ref: protocol.SubmissionRef{WorkspaceID: string(sub.Ref.WorkspaceID), RunID: string(sub.Ref.RunID), EvidenceRef: sub.Ref.EvidenceRef, RetainedRevision: sub.Ref.RetainedRevision}})
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].version != all[j].version {
			return all[i].version < all[j].version
		}
		return all[i].id < all[j].id
	})
	refs := make([]protocol.SubmissionRef, 0, len(all))
	for _, item := range all {
		refs = append(refs, item.ref)
	}
	return refs, nil
}

func submissionRefEqual(a, b protocol.SubmissionRef) bool {
	return a.WorkspaceID == b.WorkspaceID && a.RunID == b.RunID && a.EvidenceRef == b.EvidenceRef && a.RetainedRevision == b.RetainedRevision
}
func submissionRefsSubset(selected, current []protocol.SubmissionRef) bool {
	seen := make(map[string]struct{}, len(selected))
	for _, ref := range selected {
		key := ref.WorkspaceID + "\x00" + ref.RunID + "\x00" + ref.EvidenceRef + "\x00" + ref.RetainedRevision
		if _, ok := seen[key]; ok {
			return false
		}
		seen[key] = struct{}{}
		found := false
		for _, accepted := range current {
			if submissionRefEqual(ref, accepted) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return len(selected) > 0
}

// HandleAgent is intentionally narrower than the human IntegrationService.
// Agents cannot list, resolve, delete, or approve delivery.
func (s *Service) handleIntegrationAgent(ctx context.Context, run domain.RunID, method string, raw json.RawMessage) (any, error) {
	engine, err := s.integrationService()
	if err != nil {
		return nil, err
	}
	actor := feature.Actor{RunID: run}
	switch method {
	case protocol.MethodIntegrationPrepare:
		var p protocol.IntegrationPrepareParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		p, err = s.prepareIntegration(ctx, actor, p)
		if err != nil {
			return nil, err
		}
		candidate, err := engine.Prepare(ctx, actor, p)
		if err != nil {
			return nil, err
		}
		return protocol.IntegrationPrepareResult{Candidate: candidate}, nil
	case protocol.MethodIntegrationShow:
		var p protocol.IntegrationShowParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		candidate, err := engine.Show(ctx, actor, p)
		if err != nil {
			return nil, err
		}
		return protocol.IntegrationShowResult{Candidate: candidate}, nil
	case protocol.MethodIntegrationVerify:
		var p protocol.IntegrationVerifyParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		candidate, err := engine.Verify(ctx, actor, p)
		if err != nil {
			return nil, err
		}
		return protocol.IntegrationVerifyResult{Candidate: candidate}, nil
	case protocol.MethodIntegrationRequestDelivery:
		var p protocol.IntegrationRequestDeliveryParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		candidate, err := engine.RequestDelivery(ctx, actor, p)
		if err != nil {
			return nil, err
		}
		return protocol.IntegrationRequestDeliveryResult{Candidate: candidate}, nil
	case protocol.MethodIntegrationDeliver:
		var p protocol.IntegrationDeliverParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		candidate, err := engine.Deliver(ctx, actor, p)
		if err != nil {
			return nil, err
		}
		return protocol.IntegrationDeliverResult{Candidate: candidate}, nil
	default:
		return nil, errors.New("mission: integration agent method not found")
	}
}

var _ sshd.IntegrationService = (*integrationAdapter)(nil)
