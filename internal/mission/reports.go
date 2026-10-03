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
	"github.com/3xDevOps/Aether/internal/store"
)

// EvidenceReader resolves retained metadata and revalidates submission sources
// under the evidence locks. The callback must finish acceptance before returning
// so expiry cleanup cannot remove a source between validation and persistence.
type EvidenceReader interface {
	Get(context.Context, domain.WorkspaceID, string) (protocol.EvidencePacket, error)
	WithSubmissionSources(context.Context, domain.WorkspaceID, []string, func([]protocol.EvidencePacket) error) error
}

// ValidateReport performs the authority check before coord.report reserves a
// fresh report. Ordinary runs, the current integrator, and current workers are
// accepted, while a stale worker/coordinator fails closed through
// resolveAssignment. The integrator's success completes its mission, which is
// only possible once the mission is active and no approved delivery is left
// undone: completion is terminal, so nobody could run it afterwards.
func (s *Service) ValidateReport(ctx context.Context, run domain.RunID, outcome store.CoordOutcome) error {
	m, attempt, err := s.resolveAssignment(ctx, run)
	if err != nil {
		return err
	}
	if m == nil || attempt != nil || outcome != store.CoordOutcomeSuccess {
		return nil
	}
	if m.Phase == domain.MissionPhasePlanning {
		return fmt.Errorf("%w: a success report completes the mission, and mission %s has not started; run mission start first", store.ErrMissionPhase, m.ID)
	}
	// Any request that can still run is younger than the verification
	// lifetime, so the newest page covers it.
	candidates, err := s.cfg.Store.ListIntegrationCandidates(ctx, m.WorkspaceID, m.ID, protocol.IntegrationMaxPageSize)
	if err != nil {
		return err
	}
	now := s.cfg.Now()
	delivered := map[string]bool{}
	for _, c := range candidates {
		var request protocol.DeliveryRequest
		if len(c.DeliveryRequest) == 0 || json.Unmarshal(c.DeliveryRequest, &request) != nil {
			continue
		}
		if len(c.DeliveryReceipt) > 0 {
			delivered[c.TargetRef] = true
			continue
		}
		// Newest first: a later delivery to the same ref replaced this request.
		if delivered[c.TargetRef] || c.State != string(protocol.CandidateFrozen) || !now.Before(c.ExpiresAt) || !now.Before(request.ExpiresAt) ||
			(request.State != protocol.DeliveryApproved && request.State != protocol.DeliveryDelivering) {
			continue
		}
		return fmt.Errorf("%w: a success report completes the mission, and candidate %s has an approved delivery that has not run; run aether-internal integration deliver with candidate_id %s, request_id %s and request_version %d first; if it can no longer be delivered, deliver a replacement candidate to %s or report failure",
			store.ErrMissionPhase, c.CandidateID, c.CandidateID, request.RequestID, request.RequestVersion, c.TargetRef)
	}
	return nil
}

// ReconcileReport is called for every finalized report outbox row, including
// ordinary runs and historical mission identities. Only the current worker
// assignment can create a mission submission. A failure report retains that
// worker before settling its attempt. The current integrator's
// success report completes its mission; other identities remain no-ops.
// Callers must release coordination run references before waiting for admission.
func (s *Service) ReconcileReport(ctx context.Context, run domain.RunID, report *store.CoordReport, packet protocol.EvidencePacket) error {
	if report == nil {
		return errors.New("mission: report is required for worker reconciliation")
	}
	m, attempt, err := s.resolveAssignment(ctx, run)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if errors.Is(err, store.ErrMissionStale) {
			return s.reconcileStaleFailedReport(ctx, run, report, packet)
		}
		return err
	}
	if m == nil {
		return nil
	}
	if attempt == nil {
		return s.reconcileIntegratorReport(ctx, m, run, report)
	}
	if report.RunID != run || report.WorkspaceID != m.WorkspaceID || report.WorkspaceID == "" {
		return fmt.Errorf("mission: report identity does not match worker assignment")
	}
	if packet.ID == "" || packet.RunID != string(run) || packet.WorkspaceID != string(m.WorkspaceID) {
		return fmt.Errorf("mission: evidence packet identity does not match worker assignment")
	}
	if !containsString(report.EvidenceRefs, packet.ID) {
		return fmt.Errorf("mission: report does not retain evidence packet %s", packet.ID)
	}
	if packet.Origin.ID != string(run) {
		return fmt.Errorf("mission: evidence packet origin does not match worker assignment")
	}
	if packet.Origin.Kind != protocol.EvidenceOriginRun {
		return fmt.Errorf("mission: evidence packet origin is not a run")
	}
	ref := domain.SubmissionRef{
		WorkspaceID:      m.WorkspaceID,
		RunID:            run,
		EvidenceRef:      packet.ID,
		RetainedRevision: packet.RetainedRevision,
	}
	task, taskErr := s.cfg.Missions.GetTask(ctx, attempt.TaskID)
	if taskErr != nil {
		return taskErr
	}
	if task == nil || task.Revision == nil || task.CurrentRevision != attempt.TaskRevision {
		return fmt.Errorf("%w: worker task revision is stale", store.ErrMissionStale)
	}
	changed := make([]string, 0, len(packet.ChangedFiles))
	for _, fact := range packet.ChangedFiles {
		changed = append(changed, fact.Path)
	}
	violations := scopeViolations(task.Revision.Scope, changed)
	switch report.Outcome {
	case store.CoordOutcomeBlocked:
		// The durable coord.report already exists; keep the worker running.
		if publishErr := s.publishMissionChanged(ctx, m.ID); publishErr != nil {
			return publishErr
		}
		return nil
	case store.CoordOutcomeFailure:
		// Replays finish any incomplete retention without creating a submission.
		return s.failAssignedWorker(ctx, m, attempt, report.Summary)
	case store.CoordOutcomeSuccess:
	default:
		return fmt.Errorf("mission: unsupported report outcome %q", report.Outcome)
	}
	evidence := s.reportEvidence(ctx, report, packet)
	if len(evidence) == 0 {
		return errors.New("mission: evidence packet has no durable facts")
	}
	// Direct retries and the durable outbox must serialize their replay lookup
	// with submission, not merely serialize the insert after a stale lookup.
	s.cfg.AuthorizationMu.Lock()
	submissions, err := s.cfg.Missions.ListSubmissions(ctx, m.ID, attempt.TaskID)
	replayed := false
	if err == nil {
		for _, prior := range submissions {
			if prior == nil || prior.AttemptID != attempt.ID {
				continue
			}
			replayed = true
			if prior.Ref.WorkspaceID != ref.WorkspaceID || prior.Ref.RunID != ref.RunID || prior.Ref.EvidenceRef != ref.EvidenceRef {
				err = errors.New("mission: existing submission identity does not match report")
			}
			break
		}
	}
	if err == nil && !replayed {
		_, err = s.cfg.Missions.SubmitAttempt(ctx, attempt.ID, attempt.AuthorityGeneration, attempt.IntegratorGeneration, ref, evidence, violations)
	}
	s.cfg.AuthorizationMu.Unlock()
	if errors.Is(err, store.ErrMissionStale) {
		// A retry raced a superseding attempt or coordinator state. It is no
		// longer actionable for this outbox row and must not affect the new
		// active task.
		return nil
	}
	if err != nil {
		return err
	}
	if publishErr := s.publishMissionChanged(ctx, m.ID); publishErr != nil {
		return publishErr
	}
	return nil
}

// reconcileIntegratorReport moves the mission of run, its current integrator,
// to completed on a success report; the reconcile loop then stops leftover
// workers. The integrator's run itself finishes through the ordinary
// reported-outcome path, whatever the outcome, so a failure report ends the
// run and leaves the mission where it is for Replace integrator to recover.
// An ended mission takes no report: a replay finds it completed, and a
// cancelled mission stays cancelled.
func (s *Service) reconcileIntegratorReport(ctx context.Context, m *domain.Mission, run domain.RunID, report *store.CoordReport) error {
	if report.Outcome != store.CoordOutcomeSuccess || m.Phase.Terminal() {
		return nil
	}
	s.cfg.AuthorizationMu.Lock()
	_, err := s.cfg.Missions.CompleteMission(ctx, m.ID, run)
	s.cfg.AuthorizationMu.Unlock()
	if err != nil {
		return err
	}
	return s.publishMissionChanged(ctx, m.ID)
}

func (s *Service) reconcileStaleFailedReport(ctx context.Context, run domain.RunID, report *store.CoordReport, packet protocol.EvidencePacket) error {
	if report == nil || report.Outcome != store.CoordOutcomeFailure {
		return nil
	}
	attempt, err := s.cfg.Missions.GetAttemptByRun(ctx, run)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	}
	if attempt == nil || attempt.RunID != run {
		return nil
	}
	m, err := s.cfg.Missions.GetMission(ctx, attempt.MissionID)
	if err != nil {
		return err
	}
	if m == nil {
		return nil
	}
	if report.RunID != run || report.WorkspaceID != m.WorkspaceID || report.WorkspaceID == "" {
		return fmt.Errorf("mission: report identity does not match worker assignment")
	}
	if packet.ID == "" || packet.RunID != string(run) || packet.WorkspaceID != string(m.WorkspaceID) {
		return fmt.Errorf("mission: evidence packet identity does not match worker assignment")
	}
	if !containsString(report.EvidenceRefs, packet.ID) {
		return fmt.Errorf("mission: report does not retain evidence packet %s", packet.ID)
	}
	return s.failAssignedWorker(ctx, m, attempt, report.Summary)
}

// A concurrent replay can find an already-failed attempt; retention remains
// idempotent even when the state transition lost the race.
func (s *Service) failAssignedWorker(ctx context.Context, m *domain.Mission, attempt *domain.Attempt, detail string) error {
	if m == nil || attempt == nil || attempt.RunID == "" {
		return errors.New("mission: failed worker assignment is required")
	}
	if s.cfg.Complete == nil {
		return errors.New("mission: scheduler completion unavailable")
	}
	// Never settle the attempt while the worker can still execute.
	// Scheduler cleanup can need authorization; acquire it for the state write afterward.
	if completeErr := s.cfg.Complete.CompleteMission(s.operationContext(ctx), attempt.RunID, domain.RunFailed); completeErr != nil {
		return fmt.Errorf("mission: retain failed worker %s: %w", attempt.RunID, completeErr)
	}
	if attempt.State.HoldsConcurrency() {
		s.cfg.AuthorizationMu.Lock()
		stateErr := s.cfg.Missions.UpdateAttemptState(ctx, attempt.ID, attempt.RunID, attempt.AuthorityGeneration, attempt.IntegratorGeneration, domain.AttemptFailed, detail)
		s.cfg.AuthorizationMu.Unlock()
		if stateErr != nil && !errors.Is(stateErr, store.ErrMissionStale) {
			return stateErr
		}
	}
	return s.publishMissionChanged(ctx, m.ID)
}

func (s *Service) reportEvidence(ctx context.Context, report *store.CoordReport, packet protocol.EvidencePacket) []domain.SubmissionEvidence {
	facts := packetSubmissionEvidence(s.cfg.Now, packet)
	for _, ref := range report.InputEvidenceRefs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		fact := domain.SubmissionEvidence{Kind: "input", Ref: ref, Available: false, Detail: "reference was not retained by the evidence packet"}
		if s.cfg.Evidence != nil {
			other, err := s.cfg.Evidence.Get(ctx, packetWorkspace(packet), ref)
			if err == nil && other.ID == ref && other.WorkspaceID == packet.WorkspaceID {
				fact.Available, fact.Detail = packetEvidenceState(s.cfg.Now, other)
				if !fact.Available && fact.Detail == "" {
					fact.Detail = "referenced evidence is unavailable"
				}
			}
		}
		facts = append(facts, fact)
	}
	return facts
}

func packetSubmissionEvidence(now func() time.Time, packet protocol.EvidencePacket) []domain.SubmissionEvidence {
	available, detail := packetEvidenceState(now, packet)
	facts := make([]domain.SubmissionEvidence, 0, len(packet.Sources)+1)
	facts = append(facts, domain.SubmissionEvidence{Kind: "retained_packet", Ref: packet.ID, Available: available, Detail: detail})
	for _, source := range packet.Sources {
		kind := strings.TrimSpace(source.Name)
		if kind == "" {
			kind = "source"
		}
		sourceDetail := source.Reason
		if source.Truncated && sourceDetail == "" {
			sourceDetail = "source is truncated"
		} else if !source.Available && sourceDetail == "" {
			sourceDetail = "source is unavailable"
		} else if !available && sourceDetail == "" {
			sourceDetail = detail
		}
		facts = append(facts, domain.SubmissionEvidence{
			Kind: kind, Ref: packet.ID, Available: available && source.Available,
			Truncated: source.Truncated, Detail: sourceDetail,
		})
	}
	return facts
}

func packetWorkspace(packet protocol.EvidencePacket) domain.WorkspaceID {
	return domain.WorkspaceID(packet.WorkspaceID)
}

func packetEvidenceState(now func() time.Time, packet protocol.EvidencePacket) (bool, string) {
	if packet.Availability == protocol.EvidenceExpired {
		return false, "evidence packet has expired"
	}
	if packet.Availability != protocol.EvidenceAvailable {
		return false, "evidence packet is unavailable"
	}
	if packet.ExpiredAt != nil {
		return false, "evidence packet has expired"
	}
	if packet.ExpiresAt != nil {
		expires, err := time.Parse(time.RFC3339Nano, *packet.ExpiresAt)
		if err != nil {
			return false, "evidence packet expiry is unavailable"
		}
		current := time.Now().UTC()
		if now != nil {
			current = now().UTC()
		}
		if !current.Before(expires) {
			return false, "evidence packet has expired"
		}
	}
	if packet.RetainedRevision == "" {
		return false, "retained revision is unavailable"
	}
	return true, ""
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func submissionWire(s *domain.Submission) protocol.Submission {
	if s == nil {
		return protocol.Submission{}
	}
	out := protocol.Submission{
		ID: string(s.ID), MissionID: string(s.MissionID), TaskID: string(s.TaskID), TaskRevision: s.TaskRevision,
		AttemptID: string(s.AttemptID), Ref: protocol.SubmissionRef{WorkspaceID: string(s.Ref.WorkspaceID), RunID: string(s.Ref.RunID), EvidenceRef: s.Ref.EvidenceRef, RetainedRevision: s.Ref.RetainedRevision},
		State: string(s.State), ProposedByRunID: string(s.ProposedByRunID), IntegratorGeneration: s.IntegratorGeneration,
		CreatedAt: s.CreatedAt.UTC().Format(time.RFC3339Nano), DecisionByRunID: string(s.DecisionByRunID),
		ScopeViolations: append([]string(nil), s.ScopeViolations...),
	}
	out.Evidence = make([]protocol.SubmissionEvidence, 0, len(s.Evidence))
	for _, e := range s.Evidence {
		out.Evidence = append(out.Evidence, protocol.SubmissionEvidence{Kind: e.Kind, Ref: e.Ref, Available: e.Available, Truncated: e.Truncated, Detail: e.Detail})
	}
	if s.Acceptance != nil {
		a := s.Acceptance
		out.Acceptance = &protocol.Acceptance{
			SubmissionID:         string(a.SubmissionID),
			MissionID:            string(a.MissionID),
			TaskID:               string(a.TaskID),
			TaskRevision:         a.TaskRevision,
			AcceptedSetVersion:   a.AcceptedSetVersion,
			IntegratorGeneration: a.IntegratorGeneration,
			AcceptedByRunID:      string(a.AcceptedByRunID),
			ScopeDisposition:     a.ScopeDisposition,
			AcceptedAt:           a.AcceptedAt.UTC().Format(time.RFC3339Nano),
		}
	}
	if s.DecidedAt != nil {
		v := s.DecidedAt.UTC().Format(time.RFC3339Nano)
		out.DecidedAt = &v
	}
	return out
}
