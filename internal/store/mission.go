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

var (
	ErrMissionCycle               = errors.New("store: mission dependency cycle")
	ErrMissionStale               = errors.New("store: stale mission authority")
	ErrMissionIdempotencyConflict = errors.New("store: mission idempotency conflict")
	ErrMissionLimit               = errors.New("store: mission attempt limit")
	ErrMissionNotReady            = errors.New("store: mission task not ready")
	ErrMissionTakeover            = errors.New("store: mission worker under human control")
	ErrMissionPhase               = errors.New("store: mission phase forbids this operation")
	ErrMissionAmendmentRequired   = errors.New("store: revision requires human approval")
)

// MissionStore is intentionally separate from Store. Services can type-assert
type MissionStore interface {
	MissionControlStore
	CreateMission(context.Context, *domain.Mission) error
	GetMission(context.Context, domain.MissionID) (*domain.Mission, error)
	GetMissionByRun(context.Context, domain.RunID) (*domain.Mission, error)
	ListMissions(context.Context, domain.WorkspaceID) ([]*domain.Mission, error)
	ListMissionsPage(context.Context, domain.WorkspaceID, int, string) ([]*domain.Mission, string, error)
	ReplaceIntegrator(context.Context, domain.MissionID, uint64, domain.MissionIntegrator, domain.MemberID, domain.MemberID, string) (*domain.Mission, error)
	CreateTask(context.Context, *domain.Task) error
	CreateTaskWithIdempotency(context.Context, *domain.Task, string) (*domain.Task, bool, error)
	GetTask(context.Context, domain.TaskID) (*domain.Task, error)
	ListTasks(context.Context, domain.MissionID) ([]*domain.Task, error)
	ProjectTask(context.Context, domain.TaskID) (*domain.Task, error)
	ProposeTaskRevision(context.Context, domain.TaskID, *domain.TaskRevision, string) (*domain.TaskRevision, error)
	ReviseTask(context.Context, domain.TaskID, *domain.TaskRevision, string) (*domain.TaskRevision, error)
	AcceptTaskRevision(context.Context, domain.TaskID, int, uint64, domain.RunID, string) error
	SetTaskDependencies(context.Context, domain.TaskID, int, []domain.TaskDependency, string) error
	ReserveAttempt(context.Context, *domain.AttemptReservation) (*domain.Attempt, bool, error)
	GetAttempt(context.Context, domain.AttemptID) (*domain.Attempt, error)
	GetAttemptByRun(context.Context, domain.RunID) (*domain.Attempt, error)
	ListAttempts(context.Context, domain.MissionID, domain.TaskID) ([]*domain.Attempt, error)
	BindAttemptRun(context.Context, domain.AttemptID, domain.RunID, uint64, uint64) error
	RequestAttemptCancellation(context.Context, domain.AttemptID, domain.RunID, uint64, string) (*domain.Attempt, bool, error)
	UpdateAttemptState(context.Context, domain.AttemptID, domain.RunID, uint64, uint64, domain.AttemptState, string) error
	SubmitAttempt(context.Context, domain.AttemptID, uint64, uint64, domain.SubmissionRef, []domain.SubmissionEvidence, []string) (*domain.Submission, error)
	GetSubmission(context.Context, domain.SubmissionID) (*domain.Submission, error)
	ListSubmissions(context.Context, domain.MissionID, domain.TaskID) ([]*domain.Submission, error)
	AcceptSubmission(context.Context, domain.SubmissionID, domain.RunID, uint64, uint64, string, string) (*domain.Acceptance, error)
	PendingMissionControlChange(context.Context, domain.MissionID) (uint64, error)
	AckMissionControlChange(context.Context, domain.MissionID, uint64) error
	AbandonTask(context.Context, domain.TaskID, int, uint64, string) error
	CompleteMissionClarification(context.Context, domain.MissionID, domain.RunID, string) (*domain.Mission, error)
	InsertMissionQuestion(context.Context, domain.MissionID, domain.RunID, string, string) (*domain.MissionQuestion, error)
	AnswerMissionQuestion(context.Context, domain.MissionQuestionID, domain.MemberID, string, string) (*domain.MissionQuestion, error)
	GetMissionQuestion(context.Context, domain.MissionQuestionID) (*domain.MissionQuestion, error)
	ListMissionQuestions(context.Context, domain.MissionID) ([]*domain.MissionQuestion, error)
	SubmitMissionPlan(context.Context, domain.MissionID, domain.RunID, string, string) (*domain.MissionPlanReview, error)
	DecideMissionPlan(context.Context, domain.MissionID, uint64, domain.MissionPlanDecision, string, domain.MemberID, string) (*domain.Mission, error)
	ListMissionPlanReviews(context.Context, domain.MissionID) ([]*domain.MissionPlanReview, error)
}
type MissionControlStore interface {
	GetMissionWorkerAssignment(context.Context, domain.RunID) (*domain.MissionWorkerAssignment, error)
	SetMissionWorkerTakeover(context.Context, domain.RunID, domain.MemberID, bool) (*domain.MissionWorkerAssignment, error)
	ReleaseMissionWorkerTakeover(context.Context, domain.RunID, domain.MemberID, uint64) (*domain.MissionWorkerAssignment, error)
	CheckIntegratorInput(context.Context, domain.RunID, domain.RunID, uint64) error
}

var _ MissionStore = (*DB)(nil)
var _ MissionControlStore = (*DB)(nil)

func missionNow(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now().UTC()
	}
	return t.UTC()
}

func missionJSON(v any, fallback string) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	if len(b) == 0 || string(b) == "null" {
		return fallback, nil
	}
	return string(b), nil
}
func mutationPayload(v any) (string, error) {
	return missionJSON(v, "{}")
}

func mutationReceipt(tx *sql.Tx, ctx context.Context, missionID domain.MissionID, operation, key, payload string) (string, int, bool, error) {
	var stored, resultID string
	var revision int
	err := tx.QueryRowContext(ctx, `SELECT payload,result_id,result_revision FROM mission_mutation_receipts WHERE mission_id=? AND operation=? AND idempotency_key=?`, missionID, operation, key).Scan(&stored, &resultID, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, false, nil
	}
	if err != nil {
		return "", 0, false, err
	}
	if stored != payload {
		return "", 0, false, ErrMissionIdempotencyConflict
	}
	return resultID, revision, true, nil
}

func recordMutationReceipt(tx *sql.Tx, ctx context.Context, missionID domain.MissionID, operation, key, payload, resultID string, revision int, now int64) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO mission_mutation_receipts (mission_id,operation,idempotency_key,payload,result_id,result_revision,created_at) VALUES (?,?,?,?,?,?,?)`, missionID, operation, key, payload, resultID, revision, now)
	return mapConstraint(err, ErrMissionIdempotencyConflict)
}

func missionChoiceValid(c domain.MissionExecutionChoice) bool {
	return c.AccountMemberID != "" && c.Harness != "" && c.Mode.Valid()
}

func validateMission(m *domain.Mission) error {
	if m == nil || m.WorkspaceID == "" || m.Objective == "" || m.AccountableHumanID == "" {
		return errors.New("store: mission requires workspace_id, objective, and accountable_human_id")
	}
	if m.MaxConcurrentAttempts <= 0 || m.MaxConcurrentAttempts > domain.MaxMissionConcurrentAttempts ||
		m.MaxTotalAttempts <= 0 || m.MaxTotalAttempts > domain.MaxMissionTotalAttempts ||
		m.MaxConcurrentAttempts > m.MaxTotalAttempts {
		return errors.New("store: mission attempt limits exceed bounded positive caps")
	}
	if m.Integrator.AccountMemberID == "" || m.Integrator.Harness == "" || !m.Integrator.Mode.Valid() {
		return errors.New("store: mission integrator choice is invalid")
	}
	seen := make(map[string]bool, len(m.ExecutionChoices))
	for _, c := range m.ExecutionChoices {
		if !missionChoiceValid(c) {
			return errors.New("store: mission execution choice is invalid")
		}
		key := string(c.AccountMemberID) + "\x00" + c.Harness + "\x00" + string(c.Mode)
		if seen[key] {
			return errors.New("store: duplicate mission execution choice")
		}
		seen[key] = true
	}
	if m.IdempotencyKey == "" || len(m.IdempotencyKey) > 256 || strings.ContainsAny(m.IdempotencyKey, "\r\n\x00") {
		return errors.New("store: mission idempotency_key is invalid")
	}
	return nil
}

const missionColumns = `id, workspace_id, objective, accountable_human_id,
	integrator_account_member_id, integrator_harness, integrator_mode,
	execution_choices, max_concurrent_attempts, max_total_attempts,
	current_integrator_run_id, integrator_authorizing_human_id, integrator_run_owner_id,
	integrator_generation, accepted_set_version, phase, plan_version,
	idempotency_key, created_at, updated_at`

func scanMission(row interface{ Scan(...any) error }) (*domain.Mission, error) {
	var m domain.Mission
	var choices string
	var runID, authorizingHumanID, runOwnerID sql.NullString
	var created, updated int64
	var mode, phase string
	if err := row.Scan(&m.ID, &m.WorkspaceID, &m.Objective, &m.AccountableHumanID,
		&m.Integrator.AccountMemberID, &m.Integrator.Harness, &mode, &choices,
		&m.MaxConcurrentAttempts, &m.MaxTotalAttempts, &runID, &authorizingHumanID, &runOwnerID,
		&m.IntegratorGeneration, &m.AcceptedSetVersion, &phase, &m.PlanVersion,
		&m.IdempotencyKey, &created, &updated); err != nil {
		return nil, err
	}
	m.Integrator.Mode, m.Phase = domain.LaunchMode(mode), domain.MissionPhase(phase)
	if runID.Valid {
		m.CurrentIntegratorRunID = domain.RunID(runID.String)
	}
	if authorizingHumanID.Valid {
		m.IntegratorAuthorizingHumanID = domain.MemberID(authorizingHumanID.String)
	}
	if runOwnerID.Valid {
		m.IntegratorRunOwnerID = domain.MemberID(runOwnerID.String)
	}
	if err := json.Unmarshal([]byte(choices), &m.ExecutionChoices); err != nil {
		return nil, fmt.Errorf("store: decode mission execution choices: %w", err)
	}
	m.CreatedAt, m.UpdatedAt = decodeTime(created), decodeTime(updated)
	return &m, nil
}

func (d *DB) CreateMission(ctx context.Context, m *domain.Mission) error {
	if err := validateMission(m); err != nil {
		return err
	}
	choices, err := missionJSON(m.ExecutionChoices, "[]")
	if err != nil {
		return fmt.Errorf("store: encode mission choices: %w", err)
	}
	var receiptMission domain.MissionID
	var receiptObjective string
	var receiptAccount, receiptIntAccount, receiptHarness, receiptMode, receiptChoices string
	var receiptMaxConcurrent, receiptMaxTotal int
	receiptErr := d.db.QueryRowContext(ctx, `SELECT mission_id, objective, accountable_human_id, integrator_account_member_id, integrator_harness, integrator_mode, execution_choices, max_concurrent_attempts, max_total_attempts FROM mission_create_receipts WHERE workspace_id=? AND idempotency_key=?`, m.WorkspaceID, m.IdempotencyKey).Scan(&receiptMission, &receiptObjective, &receiptAccount, &receiptIntAccount, &receiptHarness, &receiptMode, &receiptChoices, &receiptMaxConcurrent, &receiptMaxTotal)
	if receiptErr == nil {
		if receiptObjective != m.Objective || receiptAccount != string(m.AccountableHumanID) || receiptIntAccount != string(m.Integrator.AccountMemberID) || receiptHarness != m.Integrator.Harness || receiptMode != string(m.Integrator.Mode) || receiptChoices != choices || receiptMaxConcurrent != m.MaxConcurrentAttempts || receiptMaxTotal != m.MaxTotalAttempts {
			return ErrMissionIdempotencyConflict
		}
		existing, getErr := d.GetMission(ctx, receiptMission)
		if getErr != nil {
			return getErr
		}
		*m = *existing
		return nil
	}
	if !errors.Is(receiptErr, sql.ErrNoRows) {
		return fmt.Errorf("store: create mission receipt lookup: %w", receiptErr)
	}
	if existing, lookupErr := scanMission(d.db.QueryRowContext(ctx, `SELECT `+missionColumns+` FROM missions WHERE workspace_id = ? AND idempotency_key = ?`, m.WorkspaceID, m.IdempotencyKey)); lookupErr == nil {
		*m = *existing
		return nil
	} else if !errors.Is(lookupErr, sql.ErrNoRows) {
		return fmt.Errorf("store: create mission lookup: %w", lookupErr)
	}
	id, ts, err := prepareCreate(m.CreatedAt)
	if err != nil {
		return err
	}
	runID, err := newID()
	if err != nil {
		return err
	}
	n, err := encodeTime(ts)
	if err != nil {
		return err
	}
	generation := m.IntegratorGeneration
	if generation == 0 {
		generation = 1
	}
	authorizingHumanID := m.IntegratorAuthorizingHumanID
	if authorizingHumanID == "" {
		authorizingHumanID = m.AccountableHumanID
	}
	runOwnerID := m.IntegratorRunOwnerID
	if runOwnerID == "" {
		runOwnerID = m.AccountableHumanID
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// A new mission starts behind the human plan gate; plan_version 0 means no
	// plan has been submitted yet.
	_, err = tx.ExecContext(ctx, `INSERT INTO missions (`+missionColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, m.WorkspaceID, m.Objective, m.AccountableHumanID,
		m.Integrator.AccountMemberID, m.Integrator.Harness, m.Integrator.Mode,
		choices, m.MaxConcurrentAttempts, m.MaxTotalAttempts, runID,
		authorizingHumanID, runOwnerID, generation, m.AcceptedSetVersion,
		domain.MissionPhasePlanning, 0, m.IdempotencyKey, n, n)
	if err != nil {
		return fmt.Errorf("store: create mission: %w", mapConstraint(err, ErrNotFound))
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO mission_create_receipts (workspace_id,idempotency_key,mission_id,objective,accountable_human_id,integrator_account_member_id,integrator_harness,integrator_mode,execution_choices,max_concurrent_attempts,max_total_attempts,initial_run_id,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, m.WorkspaceID, m.IdempotencyKey, id, m.Objective, m.AccountableHumanID, m.Integrator.AccountMemberID, m.Integrator.Harness, m.Integrator.Mode, choices, m.MaxConcurrentAttempts, m.MaxTotalAttempts, runID, n); err != nil {
		return fmt.Errorf("store: create mission receipt: %w", mapConstraint(err, ErrConflict))
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: create mission commit: %w", err)
	}
	m.ID, m.CreatedAt, m.UpdatedAt = domain.MissionID(id), ts, ts
	m.CurrentIntegratorRunID, m.IntegratorAuthorizingHumanID, m.IntegratorRunOwnerID, m.IntegratorGeneration = domain.RunID(runID), authorizingHumanID, runOwnerID, generation
	m.Phase, m.PlanVersion, m.OpenQuestions = domain.MissionPhasePlanning, 0, 0
	return nil
}
func (d *DB) GetMissionByRun(ctx context.Context, runID domain.RunID) (*domain.Mission, error) {
	if runID == "" {
		return nil, ErrNotFound
	}
	m, err := scanMission(d.db.QueryRowContext(ctx, `SELECT `+missionColumns+` FROM missions WHERE current_integrator_run_id = ?`, runID))
	if errors.Is(err, sql.ErrNoRows) {
		var initialMission domain.MissionID
		if lookupErr := d.db.QueryRowContext(ctx, `SELECT mission_id FROM mission_create_receipts WHERE initial_run_id=? LIMIT 1`, runID).Scan(&initialMission); lookupErr == nil {
			return nil, ErrMissionStale
		} else if !errors.Is(lookupErr, sql.ErrNoRows) {
			return nil, lookupErr
		}
		var retired int
		if lookupErr := d.db.QueryRowContext(ctx, `SELECT 1 FROM mission_integrator_replacements WHERE run_id=? LIMIT 1`, runID).Scan(&retired); lookupErr == nil {
			return nil, ErrMissionStale
		} else if !errors.Is(lookupErr, sql.ErrNoRows) {
			return nil, lookupErr
		}
		return nil, ErrNotFound
	}
	return m, err
}

func (d *DB) GetMission(ctx context.Context, id domain.MissionID) (*domain.Mission, error) {
	if id == "" {
		return nil, ErrNotFound
	}
	m, err := scanMission(d.db.QueryRowContext(ctx, `SELECT `+missionColumns+` FROM missions WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get mission: %w", err)
	}
	if err := d.populateOpenQuestions(ctx, []*domain.Mission{m}); err != nil {
		return nil, err
	}
	return m, nil
}

func (d *DB) ListMissions(ctx context.Context, workspaceID domain.WorkspaceID) ([]*domain.Mission, error) {
	rows, _, err := d.ListMissionsPage(ctx, workspaceID, 0, "")
	return rows, err
}

func (d *DB) ListMissionsPage(ctx context.Context, workspaceID domain.WorkspaceID, limit int, before string) ([]*domain.Mission, string, error) {
	if workspaceID == "" {
		return nil, "", errors.New("store: list missions: workspace_id is required")
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	query := `SELECT ` + missionColumns + ` FROM missions WHERE workspace_id = ?`
	args := []any{workspaceID}
	if before != "" {
		query += ` AND id < ?`
		args = append(args, before)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, "", fmt.Errorf("store: list missions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]*domain.Mission, 0, limit)
	for rows.Next() {
		m, scanErr := scanMission(rows)
		if scanErr != nil {
			return nil, "", fmt.Errorf("store: list missions: %w", scanErr)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("store: list missions: %w", err)
	}
	next := ""
	if len(out) > limit {
		next = string(out[limit-1].ID)
		out = out[:limit]
	}
	if err := d.populateOpenQuestions(ctx, out); err != nil {
		return nil, "", err
	}
	return out, next, nil
}

func (d *DB) ReplaceIntegrator(ctx context.Context, id domain.MissionID, expected uint64, choice domain.MissionIntegrator, authorizingHumanID, runOwnerID domain.MemberID, key string) (*domain.Mission, error) {
	if id == "" || choice.AccountMemberID == "" || choice.Harness == "" || !choice.Mode.Valid() || key == "" || strings.ContainsAny(key, "\r\n\x00") || len(key) > 256 || expected == 0 {
		return nil, errors.New("store: replace integrator: invalid request")
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("store: replace integrator: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	m, err := lockMissionRow(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	var receiptGen uint64
	var receiptRun, receiptAccount, receiptHarness, receiptMode, receiptAuthorizing, receiptOwner string
	err = tx.QueryRowContext(ctx, `SELECT generation, run_id, account_member_id, harness, mode, authorizing_human_id, run_owner_id FROM mission_integrator_replacements WHERE mission_id = ? AND idempotency_key = ?`, id, key).Scan(&receiptGen, &receiptRun, &receiptAccount, &receiptHarness, &receiptMode, &receiptAuthorizing, &receiptOwner)
	if err == nil {
		if receiptAccount != string(choice.AccountMemberID) || receiptHarness != choice.Harness || receiptMode != string(choice.Mode) || receiptAuthorizing != string(authorizingHumanID) || receiptOwner != string(runOwnerID) {
			return nil, ErrMissionIdempotencyConflict
		}
		m.CurrentIntegratorRunID, m.IntegratorAuthorizingHumanID, m.IntegratorRunOwnerID, m.IntegratorGeneration = domain.RunID(receiptRun), authorizingHumanID, runOwnerID, receiptGen
		m.Integrator = choice
		return m, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: replace integrator receipt: %w", err)
	}
	if phaseErr := requireMissionPhase(m, "mission.replace-integrator", domain.MissionPhasePlanning, domain.MissionPhaseClarified, domain.MissionPhasePlanReview, domain.MissionPhaseActive, domain.MissionPhaseAmendmentReview); phaseErr != nil {
		return nil, phaseErr
	}
	if expected != m.IntegratorGeneration {
		return nil, fmt.Errorf("%w: expected generation %d, current %d", ErrMissionStale, expected, m.IntegratorGeneration)
	}
	newRun, err := newID()
	if err != nil {
		return nil, err
	}
	generation := m.IntegratorGeneration + 1
	now := missionNow(time.Time{})
	n, _ := encodeTime(now)
	if _, execErr := tx.ExecContext(ctx, `UPDATE missions SET integrator_account_member_id = ?, integrator_harness = ?, integrator_mode = ?, current_integrator_run_id = ?, integrator_authorizing_human_id = ?, integrator_run_owner_id = ?, integrator_generation = ?, updated_at = ? WHERE id = ?`, choice.AccountMemberID, choice.Harness, choice.Mode, newRun, authorizingHumanID, runOwnerID, generation, n, id); execErr != nil {
		return nil, fmt.Errorf("store: replace integrator: %w", execErr)
	}
	if _, execErr := tx.ExecContext(ctx, `INSERT INTO mission_integrator_replacements (mission_id, idempotency_key, account_member_id, harness, mode, generation, run_id, authorizing_human_id, run_owner_id, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, key, choice.AccountMemberID, choice.Harness, choice.Mode, generation, newRun, authorizingHumanID, runOwnerID, n); execErr != nil {
		return nil, fmt.Errorf("store: replace integrator receipt: %w", mapConstraint(execErr, ErrConflict))
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return nil, fmt.Errorf("store: replace integrator commit: %w", commitErr)
	}
	m, err = d.GetMission(ctx, id)
	if err != nil {
		return nil, err
	}
	return m, nil
}
func (d *DB) GetMissionWorkerAssignment(ctx context.Context, workerRun domain.RunID) (*domain.MissionWorkerAssignment, error) {
	if workerRun == "" {
		return nil, ErrNotFound
	}
	var a domain.MissionWorkerAssignment
	var active int
	var updated int64
	err := d.db.QueryRowContext(ctx, `SELECT mission_id,task_id,attempt_id,worker_run_id,member_id,active,generation,updated_at FROM mission_worker_takeovers WHERE worker_run_id=?`, workerRun).Scan(&a.MissionID, &a.TaskID, &a.AttemptID, &a.WorkerRunID, &a.MemberID, &active, &a.Generation, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	a.Active, a.UpdatedAt = active != 0, decodeTime(updated)
	return &a, nil
}

func enqueueMissionControlChange(ctx context.Context, tx *sql.Tx, missionID domain.MissionID) error {
	if missionID == "" {
		return ErrNotFound
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO mission_control_changes (mission_id,generation,published_generation) VALUES (?,1,0)
		ON CONFLICT(mission_id) DO UPDATE SET generation=mission_control_changes.generation+1`, missionID)
	return err
}

// lockMissionRowBy takes the mission row's write lock and returns the row, so
// a caller that decides on phase, generation, or plan version cannot be
// overtaken between the read and its own write. idExpr locates the mission
// from arg, which may be the mission id or a subquery over a child row.
//
// The write has to be the transaction's first statement. A read before it
// takes a shared lock that SQLite refuses to upgrade while another writer
// holds the database, and the busy handler does not cover that upgrade.
func lockMissionRowBy(ctx context.Context, tx *sql.Tx, idExpr string, arg any, subject string) (*domain.Mission, error) {
	if _, err := tx.ExecContext(ctx, `UPDATE missions SET updated_at = updated_at WHERE id = `+idExpr, arg); err != nil {
		return nil, fmt.Errorf("store: lock %s: %w", subject, err)
	}
	m, err := scanMission(tx.QueryRowContext(ctx, `SELECT `+missionColumns+` FROM missions WHERE id = `+idExpr, arg))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: read %s: %w", subject, err)
	}
	return m, nil
}

func lockMissionRow(ctx context.Context, tx *sql.Tx, id domain.MissionID) (*domain.Mission, error) {
	if id == "" {
		return nil, ErrNotFound
	}
	return lockMissionRowBy(ctx, tx, `?`, id, "mission "+string(id))
}

func lockMissionRowForTask(ctx context.Context, tx *sql.Tx, id domain.TaskID) (*domain.Mission, error) {
	if id == "" {
		return nil, ErrNotFound
	}
	return lockMissionRowBy(ctx, tx, `(SELECT mission_id FROM mission_tasks WHERE id = ?)`, id, "mission for task "+string(id))
}

// requireMissionPhase refuses operation unless the mission sits in one of the
// allowed phases. Every refusal wraps ErrMissionPhase so both RPC layers can
// classify it without matching on message text.
func requireMissionPhase(m *domain.Mission, operation string, allowed ...domain.MissionPhase) error {
	for _, phase := range allowed {
		if m.Phase == phase {
			return nil
		}
	}
	return fmt.Errorf("%w: %s: mission is in phase %s; %s", ErrMissionPhase, operation, m.Phase, missionPhaseReason(m.Phase))
}

func missionPhaseReason(phase domain.MissionPhase) string {
	switch phase {
	case domain.MissionPhasePlanning:
		return "a human must approve the plan first"
	case domain.MissionPhaseClarified:
		return "the integrator is preparing the plan for review"
	case domain.MissionPhasePlanReview:
		return "the plan is frozen while a human reviews it"
	case domain.MissionPhaseAmendmentReview:
		return "the amendment is frozen while a human reviews it"
	case domain.MissionPhaseRejected:
		return "a human rejected the plan"
	default:
		return "the plan has already been approved"
	}
}

func (d *DB) SetMissionWorkerTakeover(ctx context.Context, workerRun domain.RunID, member domain.MemberID, active bool) (*domain.MissionWorkerAssignment, error) {
	if workerRun == "" {
		return nil, ErrNotFound
	}
	if active && member == "" {
		return nil, errors.New("store: takeover member is required")
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var a domain.MissionWorkerAssignment
	if attemptErr := tx.QueryRowContext(ctx, `SELECT mission_id,task_id,id FROM mission_attempts WHERE run_id=? AND state IN ('reserved','launching','running','unknown','submitted') ORDER BY number DESC LIMIT 1`, workerRun).Scan(&a.MissionID, &a.TaskID, &a.AttemptID); errors.Is(attemptErr, sql.ErrNoRows) {
		return nil, ErrNotFound
	} else if attemptErr != nil {
		return nil, attemptErr
	}
	a.WorkerRunID = workerRun
	var currentGen uint64
	err = tx.QueryRowContext(ctx, `SELECT generation FROM mission_worker_takeovers WHERE worker_run_id=?`, workerRun).Scan(&currentGen)
	if errors.Is(err, sql.ErrNoRows) {
		currentGen = 0
	} else if err != nil {
		return nil, err
	}
	currentGen++
	now := missionNow(time.Time{})
	n, _ := encodeTime(now)
	_, err = tx.ExecContext(ctx, `INSERT INTO mission_worker_takeovers (worker_run_id,mission_id,task_id,attempt_id,member_id,active,generation,updated_at) VALUES (?,?,?,?,?,?,?,?) ON CONFLICT(worker_run_id) DO UPDATE SET member_id=excluded.member_id,active=excluded.active,generation=excluded.generation,updated_at=excluded.updated_at`, workerRun, a.MissionID, a.TaskID, a.AttemptID, member, func() int {
		if active {
			return 1
		}
		return 0
	}(), currentGen, n)
	if err != nil {
		return nil, err
	}
	if enqueueErr := enqueueMissionControlChange(ctx, tx, a.MissionID); enqueueErr != nil {
		return nil, enqueueErr
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return nil, commitErr
	}
	a.MemberID, a.Active, a.Generation, a.UpdatedAt = member, active, currentGen, now
	return &a, nil
}

// ReleaseMissionWorkerTakeover clears a human takeover only when the caller
// presents the observed durable generation. It intentionally does not require
// a live attempt lease or the original holder: authorization is enforced by
// the caller, and the releasing human is recorded in the assignment.
func (d *DB) ReleaseMissionWorkerTakeover(ctx context.Context, workerRun domain.RunID, member domain.MemberID, expectedGeneration uint64) (*domain.MissionWorkerAssignment, error) {
	if workerRun == "" {
		return nil, ErrNotFound
	}
	if member == "" || expectedGeneration == 0 {
		return nil, ErrMissionStale
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	const assignmentQuery = `SELECT t.mission_id,t.task_id,t.attempt_id,t.worker_run_id,t.member_id,t.active,t.generation,t.updated_at
		FROM mission_worker_takeovers t JOIN missions m ON m.id=t.mission_id WHERE t.worker_run_id=?`
	var a domain.MissionWorkerAssignment
	var active int
	var updated int64
	if assignmentErr := tx.QueryRowContext(ctx, assignmentQuery, workerRun).Scan(&a.MissionID, &a.TaskID, &a.AttemptID, &a.WorkerRunID, &a.MemberID, &active, &a.Generation, &updated); errors.Is(assignmentErr, sql.ErrNoRows) {
		return nil, ErrNotFound
	} else if assignmentErr != nil {
		return nil, assignmentErr
	}
	if active == 0 || a.Generation != expectedGeneration {
		return nil, ErrMissionStale
	}
	now := missionNow(time.Time{})
	n, _ := encodeTime(now)
	res, err := tx.ExecContext(ctx, `UPDATE mission_worker_takeovers SET active=0,member_id=?,generation=generation+1,updated_at=? WHERE worker_run_id=? AND active=1 AND generation=?`, member, n, workerRun, expectedGeneration)
	if err != nil {
		return nil, err
	}
	if affected, _ := res.RowsAffected(); affected != 1 {
		return nil, ErrMissionStale
	}
	if enqueueErr := enqueueMissionControlChange(ctx, tx, a.MissionID); enqueueErr != nil {
		return nil, enqueueErr
	}
	if readErr := tx.QueryRowContext(ctx, assignmentQuery, workerRun).Scan(&a.MissionID, &a.TaskID, &a.AttemptID, &a.WorkerRunID, &a.MemberID, &active, &a.Generation, &updated); readErr != nil {
		if errors.Is(readErr, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, readErr
	}
	a.Active, a.UpdatedAt = active != 0, decodeTime(updated)
	if commitErr := tx.Commit(); commitErr != nil {
		return nil, commitErr
	}
	return &a, nil
}
func (d *DB) PendingMissionControlChange(ctx context.Context, missionID domain.MissionID) (uint64, error) {
	if missionID == "" {
		return 0, ErrNotFound
	}
	var generation, published uint64
	err := d.db.QueryRowContext(ctx, `SELECT generation,published_generation FROM mission_control_changes WHERE mission_id=?`, missionID).Scan(&generation, &published)
	if errors.Is(err, sql.ErrNoRows) {
		var exists int
		if existsErr := d.db.QueryRowContext(ctx, `SELECT 1 FROM missions WHERE id=?`, missionID).Scan(&exists); errors.Is(existsErr, sql.ErrNoRows) {
			return 0, ErrNotFound
		} else if existsErr != nil {
			return 0, existsErr
		}
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if generation <= published {
		return 0, nil
	}
	return generation, nil
}

func (d *DB) AckMissionControlChange(ctx context.Context, missionID domain.MissionID, generation uint64) error {
	if missionID == "" {
		return ErrNotFound
	}
	if generation == 0 {
		return ErrMissionStale
	}
	res, err := d.db.ExecContext(ctx, `UPDATE mission_control_changes
		SET published_generation=?
		WHERE mission_id=? AND generation>=? AND published_generation<?`, generation, missionID, generation, generation)
	if err != nil {
		return err
	}
	if affected, _ := res.RowsAffected(); affected != 0 {
		return nil
	}
	var exists int
	if missionErr := d.db.QueryRowContext(ctx, `SELECT 1 FROM missions WHERE id=?`, missionID).Scan(&exists); errors.Is(missionErr, sql.ErrNoRows) {
		return ErrNotFound
	} else if missionErr != nil {
		return missionErr
	}
	return nil
}

func (d *DB) CheckIntegratorInput(ctx context.Context, integratorRun, workerRun domain.RunID, generation uint64) error {
	if integratorRun == "" || workerRun == "" {
		return ErrMissionStale
	}
	var missionID domain.MissionID
	var currentGen uint64
	if err := d.db.QueryRowContext(ctx, `SELECT id,integrator_generation FROM missions WHERE current_integrator_run_id=?`, integratorRun).Scan(&missionID, &currentGen); errors.Is(err, sql.ErrNoRows) {
		return ErrMissionStale
	} else if err != nil {
		return err
	}
	if generation != currentGen {
		return ErrMissionStale
	}
	var attemptMission domain.MissionID
	if err := d.db.QueryRowContext(ctx, `SELECT mission_id FROM mission_attempts WHERE run_id=? AND state IN ('reserved','launching','running','unknown','submitted')`, workerRun).Scan(&attemptMission); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	if attemptMission != missionID {
		return ErrMissionStale
	}
	var active int
	if err := d.db.QueryRowContext(ctx, `SELECT active FROM mission_worker_takeovers WHERE worker_run_id=?`, workerRun).Scan(&active); errors.Is(err, sql.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	} else if active != 0 {
		return ErrMissionTakeover
	}
	return nil
}
