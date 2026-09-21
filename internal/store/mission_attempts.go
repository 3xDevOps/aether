package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

const attemptColumns = `id, mission_id, task_id, task_revision, number, dispatch_key, harness, mode, state, run_id, actor_run_id, authorizing_human_id, run_owner_id, account_owner_id, authority_generation, integrator_generation, created_at, reserved_at, started_at, finished_at, cancel_requested_at, cancellation_actor_run_id, cancellation_generation, last_error`

func scanAttempt(row interface{ Scan(...any) error }) (*domain.Attempt, error) {
	var a domain.Attempt
	var runID sql.NullString
	var mode, state string
	var created, reserved int64
	var started, finished, cancelRequested *int64
	if err := row.Scan(&a.ID, &a.MissionID, &a.TaskID, &a.TaskRevision, &a.Number, &a.DispatchKey, &a.Harness, &mode, &state, &runID, &a.ActorRunID, &a.AuthorizingHumanID, &a.RunOwnerID, &a.AccountOwnerID, &a.AuthorityGeneration, &a.IntegratorGeneration, &created, &reserved, &started, &finished, &cancelRequested, &a.CancellationActorRunID, &a.CancellationGeneration, &a.LastError); err != nil {
		return nil, err
	}
	a.Mode, a.State = domain.LaunchMode(mode), domain.AttemptState(state)
	if runID.Valid {
		a.RunID = domain.RunID(runID.String)
	}
	a.CreatedAt, a.ReservedAt = decodeTime(created), decodeTime(reserved)
	if started != nil {
		t := decodeTime(*started)
		a.StartedAt = &t
	}
	if finished != nil {
		t := decodeTime(*finished)
		a.FinishedAt = &t
	}
	if cancelRequested != nil {
		t := decodeTime(*cancelRequested)
		a.CancelRequestedAt = &t
	}
	return &a, nil
}

func attemptSemanticallyEqual(a *domain.Attempt, r *domain.AttemptReservation) bool {
	return a.MissionID == r.MissionID && a.TaskID == r.TaskID && a.TaskRevision == r.TaskRevision && a.Harness == r.Harness && a.Mode == r.Mode && a.ActorRunID == r.ActorRunID && a.AuthorizingHumanID == r.AuthorizingHumanID && a.RunOwnerID == r.RunOwnerID && a.AccountOwnerID == r.AccountOwnerID && a.AuthorityGeneration == r.AuthorityGeneration && a.IntegratorGeneration == r.IntegratorGeneration && (r.AssignedRunID == "" || a.RunID == r.AssignedRunID)
}

func (d *DB) ReserveAttempt(ctx context.Context, r *domain.AttemptReservation) (*domain.Attempt, bool, error) {
	if r == nil || r.MissionID == "" || r.TaskID == "" || r.TaskRevision <= 0 || r.DispatchKey == "" || r.Harness == "" || !r.Mode.Valid() {
		return nil, false, errors.New("store: reserve attempt: invalid request")
	}
	// Generate the reserved run ID only after the replay lookup; a retry with
	// an omitted ID must compare against the original reservation.
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback() }()
	m, err := lockMissionRow(ctx, tx, r.MissionID)
	if err != nil {
		return nil, false, err
	}
	// A mission never owns an attempt outside active: dispatch is refused
	// before active, and active is not re-enterable.
	if phaseErr := requireMissionPhase(m, "worker dispatch", domain.MissionPhaseActive); phaseErr != nil {
		return nil, false, phaseErr
	}
	if r.IntegratorGeneration != 0 && r.IntegratorGeneration != m.IntegratorGeneration {
		return nil, false, ErrMissionStale
	}
	var existing *domain.Attempt
	existing, err = scanAttempt(tx.QueryRowContext(ctx, `SELECT `+attemptColumns+` FROM mission_attempts WHERE mission_id = ? AND dispatch_key = ?`, r.MissionID, r.DispatchKey))
	if err == nil {
		if !attemptSemanticallyEqual(existing, r) {
			return nil, false, ErrMissionIdempotencyConflict
		}
		return existing, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}
	if r.AssignedRunID == "" {
		id, idErr := newID()
		if idErr != nil {
			return nil, false, idErr
		}
		r.AssignedRunID = domain.RunID(id)
	}
	var taskMission domain.MissionID
	var taskRevisionStatus string
	if taskErr := tx.QueryRowContext(ctx, `SELECT t.mission_id, r.status FROM mission_tasks t JOIN mission_task_revisions r ON r.task_id=t.id AND r.revision=? WHERE t.id=? AND t.current_revision=?`, r.TaskRevision, r.TaskID, r.TaskRevision).Scan(&taskMission, &taskRevisionStatus); errors.Is(taskErr, sql.ErrNoRows) {
		return nil, false, ErrNotFound
	} else if taskErr != nil {
		return nil, false, taskErr
	}
	if taskMission != r.MissionID {
		return nil, false, ErrConflict
	}
	if taskRevisionStatus != string(domain.TaskRevisionAccepted) {
		return nil, false, ErrMissionNotReady
	}
	var blocked int
	if blockedErr := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mission_task_dependencies d WHERE d.task_id=? AND d.task_revision=? AND NOT EXISTS (SELECT 1 FROM mission_acceptances a WHERE a.task_id=d.depends_on_task_id AND a.task_revision=d.depends_on_revision)`, r.TaskID, r.TaskRevision).Scan(&blocked); blockedErr != nil {
		return nil, false, blockedErr
	}
	if blocked > 0 {
		return nil, false, ErrMissionNotReady
	}
	var takeover int
	if takeoverErr := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mission_worker_takeovers WHERE mission_id=? AND task_id=? AND active=1`, r.MissionID, r.TaskID).Scan(&takeover); takeoverErr != nil {
		return nil, false, takeoverErr
	}
	if takeover > 0 {
		return nil, false, ErrMissionTakeover
	}
	var total int
	if totalErr := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mission_attempts WHERE mission_id = ?`, r.MissionID).Scan(&total); totalErr != nil {
		return nil, false, totalErr
	}
	if total >= m.MaxTotalAttempts {
		return nil, false, ErrMissionLimit
	}
	var active int
	if activeErr := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mission_attempts WHERE mission_id = ? AND state IN ('reserved','launching','running','unknown','submitted')`, r.MissionID).Scan(&active); activeErr != nil {
		return nil, false, activeErr
	}
	if active >= m.MaxConcurrentAttempts {
		return nil, false, ErrMissionLimit
	}
	var number int
	if numberErr := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(number),0)+1 FROM mission_attempts WHERE task_id = ?`, r.TaskID).Scan(&number); numberErr != nil {
		return nil, false, numberErr
	}
	id, err := newID()
	if err != nil {
		return nil, false, err
	}
	now := missionNow(time.Time{})
	n, _ := encodeTime(now)
	_, err = tx.ExecContext(ctx, `INSERT INTO mission_attempts (id,mission_id,task_id,task_revision,number,dispatch_key,harness,mode,state,run_id,actor_run_id,authorizing_human_id,run_owner_id,account_owner_id,authority_generation,integrator_generation,created_at,reserved_at,started_at,finished_at,cancel_requested_at,cancellation_actor_run_id,cancellation_generation,last_error) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,NULL,NULL,NULL,'',0,'')`, id, r.MissionID, r.TaskID, r.TaskRevision, number, r.DispatchKey, r.Harness, r.Mode, domain.AttemptReserved, r.AssignedRunID, r.ActorRunID, r.AuthorizingHumanID, r.RunOwnerID, r.AccountOwnerID, r.AuthorityGeneration, m.IntegratorGeneration, n, n)
	if err != nil {
		return nil, false, fmt.Errorf("store: reserve attempt: %w", mapConstraint(err, ErrConflict))
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return nil, false, commitErr
	}
	a, err := d.getAttempt(ctx, domain.AttemptID(id))
	return a, false, err
}

func (d *DB) enrichAttemptTakeover(ctx context.Context, a *domain.Attempt) error {
	var active int
	err := d.db.QueryRowContext(ctx, `SELECT active, member_id, generation FROM mission_worker_takeovers WHERE worker_run_id=?`, a.RunID).Scan(&active, &a.TakeoverMemberID, &a.TakeoverGeneration)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	a.TakeoverActive = active != 0
	return nil
}

func (d *DB) getAttempt(ctx context.Context, id domain.AttemptID) (*domain.Attempt, error) {
	a, err := scanAttempt(d.db.QueryRowContext(ctx, `SELECT `+attemptColumns+` FROM mission_attempts WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := d.enrichAttemptTakeover(ctx, a); err != nil {
		return nil, err
	}
	return a, nil
}
func (d *DB) BindAttemptRun(ctx context.Context, id domain.AttemptID, runID domain.RunID, authority, integrator uint64) error {
	if id == "" || runID == "" {
		return ErrNotFound
	}
	res, err := d.db.ExecContext(ctx, `UPDATE mission_attempts SET state='launching' WHERE id=? AND run_id=? AND state IN ('reserved','launching') AND authority_generation=? AND integrator_generation=?`, id, runID, authority, integrator)
	if err != nil {
		return err
	}
	affected, _ := res.RowsAffected()
	if affected == 1 {
		return nil
	}
	a, err := d.getAttempt(ctx, id)
	if err != nil {
		return err
	}
	if a.RunID == runID && a.State == domain.AttemptLaunching && a.AuthorityGeneration == authority && a.IntegratorGeneration == integrator {
		return nil
	}
	return ErrMissionStale
}
func (d *DB) RequestAttemptCancellation(ctx context.Context, attemptID domain.AttemptID, actorRunID domain.RunID, generation uint64, key string) (*domain.Attempt, bool, error) {
	if attemptID == "" || actorRunID == "" || generation == 0 || key == "" || len(key) > 256 || strings.ContainsAny(key, "\r\n\x00") {
		return nil, false, errors.New("store: cancel attempt: invalid request")
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback() }()
	var missionID domain.MissionID
	var currentRun domain.RunID
	var currentGen uint64
	if attemptErr := tx.QueryRowContext(ctx, `SELECT a.mission_id,m.current_integrator_run_id,m.integrator_generation FROM mission_attempts a JOIN missions m ON m.id=a.mission_id WHERE a.id=?`, attemptID).Scan(&missionID, &currentRun, &currentGen); errors.Is(attemptErr, sql.ErrNoRows) {
		return nil, false, ErrNotFound
	} else if attemptErr != nil {
		return nil, false, attemptErr
	}
	if currentRun != actorRunID || currentGen != generation {
		return nil, false, ErrMissionStale
	}
	payload, err := mutationPayload(struct {
		AttemptID  domain.AttemptID
		ActorRunID domain.RunID
		Generation uint64
	}{attemptID, actorRunID, generation})
	if err != nil {
		return nil, false, err
	}
	resultID, _, replayed, err := mutationReceipt(tx, ctx, missionID, "attempt.cancel", key, payload)
	if err != nil {
		return nil, false, err
	}
	if replayed {
		a, scanErr := scanAttempt(tx.QueryRowContext(ctx, `SELECT `+attemptColumns+` FROM mission_attempts WHERE id=?`, resultID))
		if scanErr != nil {
			return nil, false, scanErr
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return nil, false, commitErr
		}
		return a, true, nil
	}
	now := missionNow(time.Time{})
	n, _ := encodeTime(now)
	res, err := tx.ExecContext(ctx, `UPDATE mission_attempts SET cancel_requested_at=?, cancellation_actor_run_id=?, cancellation_generation=? WHERE id=? AND cancel_requested_at IS NULL`, n, actorRunID, generation, attemptID)
	if err != nil {
		return nil, false, err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return nil, false, ErrMissionStale
	}
	if receiptErr := recordMutationReceipt(tx, ctx, missionID, "attempt.cancel", key, payload, string(attemptID), 0, n); receiptErr != nil {
		return nil, false, receiptErr
	}
	a, err := scanAttempt(tx.QueryRowContext(ctx, `SELECT `+attemptColumns+` FROM mission_attempts WHERE id=?`, attemptID))
	if err != nil {
		return nil, false, err
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return nil, false, commitErr
	}
	return a, false, nil
}

func (d *DB) GetAttemptByRun(ctx context.Context, runID domain.RunID) (*domain.Attempt, error) {
	if runID == "" {
		return nil, ErrNotFound
	}
	a, err := scanAttempt(d.db.QueryRowContext(ctx, `SELECT `+attemptColumns+` FROM mission_attempts WHERE run_id=? ORDER BY number DESC LIMIT 1`, runID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := d.enrichAttemptTakeover(ctx, a); err != nil {
		return nil, err
	}
	return a, nil
}

func (d *DB) GetAttempt(ctx context.Context, id domain.AttemptID) (*domain.Attempt, error) {
	return d.getAttempt(ctx, id)
}

func (d *DB) ListAttempts(ctx context.Context, missionID domain.MissionID, taskID domain.TaskID) ([]*domain.Attempt, error) {
	query := `SELECT ` + attemptColumns + ` FROM mission_attempts WHERE mission_id = ?`
	args := []any{missionID}
	if taskID != "" {
		query += ` AND task_id = ?`
		args = append(args, taskID)
	}
	query += ` ORDER BY created_at, id LIMIT 1025`
	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]*domain.Attempt, 0, 32)
	for rows.Next() {
		if len(out) == 1024 {
			return nil, ErrConflict
		}
		a, scanErr := scanAttempt(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		if err := d.enrichAttemptTakeover(ctx, a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (d *DB) UpdateAttemptState(ctx context.Context, id domain.AttemptID, runID domain.RunID, authority, integrator uint64, state domain.AttemptState, detail string) error {
	if !state.Valid() {
		return errors.New("store: invalid attempt state")
	}
	if len(detail) > 4096 {
		return errors.New("store: attempt detail exceeds bounded size")
	}
	now := missionNow(time.Time{})
	n, _ := encodeTime(now)
	var query string
	var args []any
	switch state {
	case domain.AttemptRunning:
		query = `UPDATE mission_attempts SET state='running', started_at=COALESCE(started_at, ?), last_error=? WHERE id=? AND run_id=? AND authority_generation=? AND integrator_generation=? AND state IN ('reserved','launching','running','unknown')`
		args = []any{n, detail, id, runID, authority, integrator}
	case domain.AttemptUnknown:
		query = `UPDATE mission_attempts SET state='unknown', last_error=? WHERE id=? AND run_id=? AND authority_generation=? AND integrator_generation=? AND state IN ('reserved','launching','running','unknown')`
		args = []any{detail, id, runID, authority, integrator}
	case domain.AttemptCompleted, domain.AttemptSubmitted, domain.AttemptFailed, domain.AttemptCancelled, domain.AttemptSuperseded, domain.AttemptAbandoned:
		query = `UPDATE mission_attempts SET state=?, finished_at=?, last_error=? WHERE id=? AND run_id=? AND authority_generation=? AND integrator_generation=? AND state IN ('reserved','launching','running','unknown','submitted')`
		args = []any{state, n, detail, id, runID, authority, integrator}
	default:
		return errors.New("store: attempt state cannot transition to reserved or launching")
	}
	res, err := d.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	affected, _ := res.RowsAffected()
	if affected == 1 {
		return nil
	}
	return ErrMissionStale
}

func (d *DB) SubmitAttempt(ctx context.Context, id domain.AttemptID, authority, integrator uint64, ref domain.SubmissionRef, evidence []domain.SubmissionEvidence, scopeViolations []string) (*domain.Submission, error) {
	if id == "" || ref.WorkspaceID == "" || ref.RunID == "" || ref.EvidenceRef == "" || len(evidence) == 0 {
		return nil, errors.New("store: submit attempt: complete submission identity and evidence are required")
	}
	for _, fact := range evidence {
		if fact.Kind == "" || fact.Ref == "" {
			return nil, errors.New("store: submit attempt: evidence facts require kind and ref")
		}
	}
	evidenceJSON, err := missionJSON(evidence, "[]")
	if err != nil {
		return nil, err
	}
	scopeJSON, err := missionJSON(scopeViolations, "[]")
	if err != nil {
		return nil, err
	}
	if len(scopeJSON) > domain.MaxMissionScopeBytes || len(scopeViolations) > domain.MaxTaskEvidenceRequirements {
		return nil, ErrMissionLimit
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var a *domain.Attempt
	a, err = scanAttempt(tx.QueryRowContext(ctx, `SELECT `+attemptColumns+` FROM mission_attempts WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if a.RunID != ref.RunID || a.AuthorityGeneration != authority || a.IntegratorGeneration != integrator || !a.State.HoldsConcurrency() {
		return nil, ErrMissionStale
	}
	var current int
	if currentErr := tx.QueryRowContext(ctx, `SELECT current_revision FROM mission_tasks WHERE id=?`, a.TaskID).Scan(&current); currentErr != nil {
		return nil, currentErr
	}
	if current != a.TaskRevision {
		return nil, ErrMissionStale
	}
	var latest domain.AttemptID
	if latestErr := tx.QueryRowContext(ctx, `SELECT id FROM mission_attempts WHERE task_id=? AND task_revision=? ORDER BY number DESC LIMIT 1`, a.TaskID, a.TaskRevision).Scan(&latest); latestErr != nil || latest != a.ID {
		return nil, ErrMissionStale
	}
	var missionWorkspace domain.WorkspaceID
	if workspaceErr := tx.QueryRowContext(ctx, `SELECT workspace_id FROM missions WHERE id=?`, a.MissionID).Scan(&missionWorkspace); workspaceErr != nil {
		return nil, workspaceErr
	}
	if missionWorkspace != ref.WorkspaceID {
		return nil, ErrConflict
	}
	subID, err := newID()
	if err != nil {
		return nil, err
	}
	now := missionNow(time.Time{})
	n, _ := encodeTime(now)
	if _, insertErr := tx.ExecContext(ctx, `INSERT INTO mission_submissions (id, mission_id, task_id, task_revision, attempt_id, workspace_id, run_id, evidence_ref, retained_revision, evidence, scope_violations, state, proposed_by_run_id, integrator_generation, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'proposed', ?, ?, ?)`, subID, a.MissionID, a.TaskID, a.TaskRevision, a.ID, ref.WorkspaceID, ref.RunID, ref.EvidenceRef, ref.RetainedRevision, evidenceJSON, scopeJSON, a.ActorRunID, a.IntegratorGeneration, n); insertErr != nil {
		return nil, fmt.Errorf("store: submit attempt: %w", mapConstraint(insertErr, ErrConflict))
	}
	if _, updateErr := tx.ExecContext(ctx, `UPDATE mission_attempts SET state='submitted', finished_at=? WHERE id=? AND state IN ('reserved','launching','running','unknown')`, n, id); updateErr != nil {
		return nil, updateErr
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return nil, commitErr
	}
	return d.getSubmission(ctx, domain.SubmissionID(subID))
}

func (d *DB) getSubmission(ctx context.Context, id domain.SubmissionID) (*domain.Submission, error) {
	s, err := scanSubmission(d.db.QueryRowContext(ctx, `SELECT id, mission_id, task_id, task_revision, attempt_id, workspace_id, run_id, evidence_ref, retained_revision, evidence, scope_violations, state, proposed_by_run_id, integrator_generation, created_at, decided_at, decision_by_run_id FROM mission_submissions WHERE id=?`, id))
	if err != nil {
		return nil, err
	}
	if err := d.enrichSubmissionAcceptance(ctx, s); err != nil {
		return nil, err
	}
	return s, nil
}
func (d *DB) GetSubmission(ctx context.Context, id domain.SubmissionID) (*domain.Submission, error) {
	return d.getSubmission(ctx, id)
}
func (d *DB) ListSubmissions(ctx context.Context, missionID domain.MissionID, taskID domain.TaskID) ([]*domain.Submission, error) {
	query := `SELECT id, mission_id, task_id, task_revision, attempt_id, workspace_id, run_id, evidence_ref, retained_revision, evidence, scope_violations, state, proposed_by_run_id, integrator_generation, created_at, decided_at, decision_by_run_id FROM mission_submissions WHERE mission_id=?`
	args := []any{missionID}
	if taskID != "" {
		query += ` AND task_id=?`
		args = append(args, taskID)
	}
	query += ` ORDER BY created_at, id LIMIT 1025`
	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]*domain.Submission, 0, 32)
	for rows.Next() {
		if len(out) == 1024 {
			return nil, ErrConflict
		}
		s, scanErr := scanSubmission(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		if err := d.enrichSubmissionAcceptance(ctx, s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func scanSubmission(row interface{ Scan(...any) error }) (*domain.Submission, error) {
	var s domain.Submission
	var evidence, scopeJSON, state string
	var created int64
	var decided *int64
	if err := row.Scan(&s.ID, &s.MissionID, &s.TaskID, &s.TaskRevision, &s.AttemptID, &s.Ref.WorkspaceID, &s.Ref.RunID, &s.Ref.EvidenceRef, &s.Ref.RetainedRevision, &evidence, &scopeJSON, &state, &s.ProposedByRunID, &s.IntegratorGeneration, &created, &decided, &s.DecisionByRunID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	s.State, s.CreatedAt = domain.SubmissionState(state), decodeTime(created)
	if decided != nil {
		t := decodeTime(*decided)
		s.DecidedAt = &t
	}
	if err := json.Unmarshal([]byte(evidence), &s.Evidence); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(scopeJSON), &s.ScopeViolations); err != nil {
		return nil, err
	}
	return &s, nil
}
func (d *DB) enrichSubmissionAcceptance(ctx context.Context, s *domain.Submission) error {
	var a domain.Acceptance
	var acceptedAt int64
	err := d.db.QueryRowContext(ctx, `SELECT submission_id,mission_id,task_id,task_revision,accepted_set_version,integrator_generation,accepted_by_run_id,scope_disposition,accepted_at FROM mission_acceptances WHERE submission_id=?`, s.ID).Scan(&a.SubmissionID, &a.MissionID, &a.TaskID, &a.TaskRevision, &a.AcceptedSetVersion, &a.IntegratorGeneration, &a.AcceptedByRunID, &a.ScopeDisposition, &acceptedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	a.AcceptedAt = decodeTime(acceptedAt)
	s.Acceptance = &a
	return nil
}

func (d *DB) AcceptSubmission(ctx context.Context, id domain.SubmissionID, acceptedBy domain.RunID, expectedGeneration, expectedSet uint64, scopeDisposition, key string) (*domain.Acceptance, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var missionID domain.MissionID
	var taskID domain.TaskID
	var taskRevision int
	var attemptID domain.AttemptID
	var state string
	var subGen uint64
	var retained, evidenceRef string
	var requiredJSON, evidenceJSON, scopeJSON string
	var currentRun sql.NullString
	var generation, setVersion uint64
	if submissionErr := tx.QueryRowContext(ctx, `SELECT s.mission_id,s.task_id,s.task_revision,s.attempt_id,s.state,s.integrator_generation,s.retained_revision,s.evidence_ref,s.evidence,s.scope_violations,m.integrator_generation,m.accepted_set_version,m.current_integrator_run_id,r.evidence_requirements FROM mission_submissions s JOIN missions m ON m.id=s.mission_id JOIN mission_task_revisions r ON r.task_id=s.task_id AND r.revision=s.task_revision WHERE s.id=?`, id).Scan(&missionID, &taskID, &taskRevision, &attemptID, &state, &subGen, &retained, &evidenceRef, &evidenceJSON, &scopeJSON, &generation, &setVersion, &currentRun, &requiredJSON); errors.Is(submissionErr, sql.ErrNoRows) {
		return nil, ErrNotFound
	} else if submissionErr != nil {
		return nil, submissionErr
	}
	if key == "" || strings.ContainsAny(key, "\r\n\x00") || len(key) > 256 {
		return nil, errors.New("store: submission acceptance idempotency_key is invalid")
	}
	payload, err := mutationPayload(struct {
		SubmissionID domain.SubmissionID
		AcceptedBy   domain.RunID
		Generation   uint64
		SetVersion   uint64
		Disposition  string
	}{id, acceptedBy, expectedGeneration, expectedSet, scopeDisposition})
	if err != nil {
		return nil, err
	}
	resultID, _, replayed, err := mutationReceipt(tx, ctx, missionID, "submission.accept", key, payload)
	if err != nil {
		return nil, err
	}
	if replayed {
		var accepted domain.Acceptance
		var acceptedAt int64
		if acceptanceErr := tx.QueryRowContext(ctx, `SELECT submission_id,mission_id,task_id,task_revision,accepted_set_version,integrator_generation,accepted_by_run_id,scope_disposition,accepted_at FROM mission_acceptances WHERE submission_id=?`, resultID).Scan(&accepted.SubmissionID, &accepted.MissionID, &accepted.TaskID, &accepted.TaskRevision, &accepted.AcceptedSetVersion, &accepted.IntegratorGeneration, &accepted.AcceptedByRunID, &accepted.ScopeDisposition, &acceptedAt); acceptanceErr != nil {
			return nil, acceptanceErr
		}
		accepted.AcceptedAt = decodeTime(acceptedAt)
		if commitErr := tx.Commit(); commitErr != nil {
			return nil, commitErr
		}
		return &accepted, nil
	}
	if !currentRun.Valid || state != string(domain.SubmissionProposed) || acceptedBy == "" || domain.RunID(currentRun.String) != acceptedBy || (expectedGeneration != 0 && expectedGeneration != generation) || (expectedSet != setVersion) {
		return nil, ErrMissionStale
	}
	var currentRevision int
	var abandoned *int64
	if taskErr := tx.QueryRowContext(ctx, `SELECT current_revision, abandoned_at FROM mission_tasks WHERE id=?`, taskID).Scan(&currentRevision, &abandoned); taskErr != nil {
		return nil, taskErr
	}
	if abandoned != nil || currentRevision != taskRevision {
		return nil, ErrMissionStale
	}
	var evidence []domain.SubmissionEvidence
	var reqs []domain.EvidenceRequirement
	var violations []string
	if json.Unmarshal([]byte(evidenceJSON), &evidence) != nil || json.Unmarshal([]byte(requiredJSON), &reqs) != nil || json.Unmarshal([]byte(scopeJSON), &violations) != nil {
		return nil, errors.New("store: invalid submission evidence")
	}
	if retained == "" {
		return nil, ErrMissionNotReady
	}
	facts := make(map[string]bool, len(evidence))
	matchedEvidence := false
	baseline := false
	for _, fact := range evidence {
		facts[fact.Kind] = facts[fact.Kind] || (fact.Available && fact.Ref != "")
		if fact.Kind == "retained_packet" && fact.Available && fact.Ref != "" {
			baseline = true
		}
		if fact.Ref == evidenceRef && fact.Available {
			matchedEvidence = true
		}
	}
	if !baseline || !matchedEvidence {
		return nil, ErrMissionNotReady
	}
	if len(violations) > 0 && strings.TrimSpace(scopeDisposition) == "" {
		return nil, ErrMissionNotReady
	}
	for _, req := range reqs {
		if !facts[req.Kind] {
			return nil, ErrMissionNotReady
		}
	}
	var currentAttempt domain.AttemptID
	if attemptErr := tx.QueryRowContext(ctx, `SELECT id FROM mission_attempts WHERE task_id=? AND task_revision=? ORDER BY number DESC LIMIT 1`, taskID, taskRevision).Scan(&currentAttempt); attemptErr != nil || currentAttempt != attemptID {
		return nil, ErrMissionStale
	}
	now := missionNow(time.Time{})
	n, _ := encodeTime(now)
	newVersion := setVersion + 1
	if _, updateErr := tx.ExecContext(ctx, `UPDATE mission_submissions SET state='accepted', decided_at=?, decision_by_run_id=? WHERE id=? AND state='proposed'`, n, acceptedBy, id); updateErr != nil {
		return nil, updateErr
	}
	if _, insertErr := tx.ExecContext(ctx, `INSERT INTO mission_acceptances (submission_id, mission_id, task_id, task_revision, accepted_set_version, integrator_generation, accepted_by_run_id, scope_disposition, accepted_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, missionID, taskID, taskRevision, newVersion, generation, acceptedBy, scopeDisposition, n); insertErr != nil {
		return nil, fmt.Errorf("store: accept submission: %w", mapConstraint(insertErr, ErrConflict))
	}
	if receiptErr := recordMutationReceipt(tx, ctx, missionID, "submission.accept", key, payload, string(id), taskRevision, n); receiptErr != nil {
		return nil, receiptErr
	}
	if _, versionErr := tx.ExecContext(ctx, `UPDATE missions SET accepted_set_version = accepted_set_version + 1, updated_at = ? WHERE id = ?`, n, missionID); versionErr != nil {
		return nil, versionErr
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return nil, commitErr
	}
	return &domain.Acceptance{SubmissionID: id, MissionID: missionID, TaskID: taskID, TaskRevision: taskRevision, AcceptedSetVersion: newVersion, IntegratorGeneration: generation, AcceptedByRunID: acceptedBy, ScopeDisposition: scopeDisposition, AcceptedAt: now}, nil
}
