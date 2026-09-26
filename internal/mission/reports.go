package mission

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

// EvidenceReader is the narrow durable lookup used when a report refers to
// another retained packet. A report must never infer availability from an
// opaque caller-provided string.
type EvidenceReader interface {
	Get(context.Context, domain.WorkspaceID, string) (protocol.EvidencePacket, error)
}

// ValidateReport performs the authority check before coord.report reserves a
// fresh report. Ordinary runs and the current integrator intentionally remain
// on the normal no-submission path; current workers are accepted, while a
// stale worker/coordinator fails closed through resolveAssignment.
func (s *Service) ValidateReport(ctx context.Context, run domain.RunID) error {
	_, _, err := s.resolveAssignment(ctx, run)
	return err
}

// ReconcileReport is called for every finalized report outbox row, including
// ordinary runs and historical mission identities. Only the current worker
// assignment can create a mission submission. A failure report still cancels
// that worker after the attempt is terminal so cancellation and publication
// can retry; other identities remain no-ops.
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
	if m == nil || attempt == nil {
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
	// Reconciliation is replayed both by coord.report retries and by the
	// durable outbox. A submitted attempt is already the exact durable
	// result; never create a second submission row for the same attempt.
	submissions, listErr := s.cfg.Missions.ListSubmissions(ctx, m.ID, attempt.TaskID)
	if listErr != nil {
		return listErr
	}
	for _, prior := range submissions {
		if prior == nil || prior.AttemptID != attempt.ID {
			continue
		}
		if prior.Ref.WorkspaceID != report.WorkspaceID || prior.Ref.RunID != run || prior.Ref.EvidenceRef != packet.ID {
			return errors.New("mission: existing submission identity does not match report")
		}
		if publishErr := s.publishMissionChanged(ctx, m.ID); publishErr != nil {
			return publishErr
		}
		return nil
	}
	switch report.Outcome {
	case store.CoordOutcomeBlocked:
		// The durable coord.report already exists; keep the worker running.
		if publishErr := s.publishMissionChanged(ctx, m.ID); publishErr != nil {
			return publishErr
		}
		return nil
	case store.CoordOutcomeFailure:
		// A replay finds the attempt already failed and only re-cancels.
		return s.failAssignedWorker(ctx, m, attempt, report.Summary)
	case store.CoordOutcomeSuccess:
	default:
		return fmt.Errorf("mission: unsupported report outcome %q", report.Outcome)
	}
	evidence := s.reportEvidence(ctx, report, packet)
	if len(evidence) == 0 {
		return errors.New("mission: evidence packet has no durable facts")
	}
	_, err = s.cfg.Missions.SubmitAttempt(ctx, attempt.ID, attempt.AuthorityGeneration, attempt.IntegratorGeneration, ref, evidence, violations)
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

// A concurrent replay can find an already-failed attempt; cancellation remains
// idempotent even when the state transition lost the race.
func (s *Service) failAssignedWorker(ctx context.Context, m *domain.Mission, attempt *domain.Attempt, detail string) error {
	if m == nil || attempt == nil || attempt.RunID == "" {
		return errors.New("mission: failed worker assignment is required")
	}
	if s.cfg.Cancel == nil {
		return errors.New("mission: scheduler cancel unavailable")
	}
	if attempt.State.HoldsConcurrency() {
		stateErr := s.cfg.Missions.UpdateAttemptState(ctx, attempt.ID, attempt.RunID, attempt.AuthorityGeneration, attempt.IntegratorGeneration, domain.AttemptFailed, detail)
		if stateErr != nil && !errors.Is(stateErr, store.ErrMissionStale) {
			return stateErr
		}
	}
	if cancelErr := s.cfg.Cancel.CancelMission(s.operationContext(ctx), attempt.RunID); cancelErr != nil {
		return fmt.Errorf("mission: cancel failed worker %s: %w", attempt.RunID, cancelErr)
	}
	return s.publishMissionChanged(ctx, m.ID)
}

func (s *Service) reportEvidence(ctx context.Context, report *store.CoordReport, packet protocol.EvidencePacket) []domain.SubmissionEvidence {
	available, detail := packetEvidenceState(s.cfg.Now, packet)
	facts := make([]domain.SubmissionEvidence, 0, len(packet.Sources)+len(report.InputEvidenceRefs)+1)
	facts = append(facts, domain.SubmissionEvidence{Kind: "retained_packet", Ref: packet.ID, Available: available, Detail: detail})
	for _, source := range packet.Sources {
		kind := strings.TrimSpace(source.Name)
		if kind == "" {
			kind = "source"
		}
		ref := packet.ID
		sourceAvailable := available && source.Available && !source.Truncated
		sourceDetail := source.Reason
		if source.Truncated && sourceDetail == "" {
			sourceDetail = "source is truncated"
		} else if !source.Available && sourceDetail == "" {
			sourceDetail = "source is unavailable"
		} else if !available && sourceDetail == "" {
			sourceDetail = detail
		}
		facts = append(facts, domain.SubmissionEvidence{Kind: kind, Ref: ref, Available: sourceAvailable, Detail: sourceDetail})
	}
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
		out.Evidence = append(out.Evidence, protocol.SubmissionEvidence{Kind: e.Kind, Ref: e.Ref, Available: e.Available, Detail: e.Detail})
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
