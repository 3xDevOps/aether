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
	AcceptTaskRevision(context.Context, domain.TaskID, int, uint64, string) error
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
	AbandonTask(context.Context, domain.TaskID, uint64, string) error
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
	integrator_generation, accepted_set_version, idempotency_key, created_at, updated_at`

func scanMission(row interface{ Scan(...any) error }) (*domain.Mission, error) {
	var m domain.Mission
	var choices string
	var runID, authorizingHumanID, runOwnerID sql.NullString
	var created, updated int64
	var mode string
	if err := row.Scan(&m.ID, &m.WorkspaceID, &m.Objective, &m.AccountableHumanID,
		&m.Integrator.AccountMemberID, &m.Integrator.Harness, &mode, &choices,
		&m.MaxConcurrentAttempts, &m.MaxTotalAttempts, &runID, &authorizingHumanID, &runOwnerID,
		&m.IntegratorGeneration, &m.AcceptedSetVersion, &m.IdempotencyKey,
		&created, &updated); err != nil {
		return nil, err
	}
	m.Integrator.Mode = domain.LaunchMode(mode)
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
	_, err = tx.ExecContext(ctx, `INSERT INTO missions (`+missionColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, m.WorkspaceID, m.Objective, m.AccountableHumanID,
		m.Integrator.AccountMemberID, m.Integrator.Harness, m.Integrator.Mode,
		choices, m.MaxConcurrentAttempts, m.MaxTotalAttempts, runID,
		authorizingHumanID, runOwnerID, generation, m.AcceptedSetVersion,
		m.IdempotencyKey, n, n)
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
	var receiptGen uint64
	var receiptRun, receiptAccount, receiptHarness, receiptMode, receiptAuthorizing, receiptOwner string
	err = tx.QueryRowContext(ctx, `SELECT generation, run_id, account_member_id, harness, mode, authorizing_human_id, run_owner_id FROM mission_integrator_replacements WHERE mission_id = ? AND idempotency_key = ?`, id, key).Scan(&receiptGen, &receiptRun, &receiptAccount, &receiptHarness, &receiptMode, &receiptAuthorizing, &receiptOwner)
	if err == nil {
		if receiptAccount != string(choice.AccountMemberID) || receiptHarness != choice.Harness || receiptMode != string(choice.Mode) || receiptAuthorizing != string(authorizingHumanID) || receiptOwner != string(runOwnerID) {
			return nil, ErrMissionIdempotencyConflict
		}
		m, readErr := scanMission(tx.QueryRowContext(ctx, `SELECT `+missionColumns+` FROM missions WHERE id = ?`, id))
		if readErr != nil {
			return nil, readErr
		}
		m.CurrentIntegratorRunID, m.IntegratorAuthorizingHumanID, m.IntegratorRunOwnerID, m.IntegratorGeneration = domain.RunID(receiptRun), authorizingHumanID, runOwnerID, receiptGen
		m.Integrator = choice
		return m, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: replace integrator receipt: %w", err)
	}
	if _, execErr := tx.ExecContext(ctx, `UPDATE missions SET updated_at = updated_at WHERE id = ?`, id); execErr != nil {
		return nil, fmt.Errorf("store: replace integrator lock: %w", execErr)
	}
	m, err := scanMission(tx.QueryRowContext(ctx, `SELECT `+missionColumns+` FROM missions WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
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
func validateTaskRevision(r *domain.TaskRevision) error {
	if r == nil || r.Title == "" || r.Objective == "" {
		return errors.New("store: task revision requires title and objective")
	}
	if len(r.EvidenceRequirements) > domain.MaxTaskEvidenceRequirements {
		return errors.New("store: task revision has too many evidence requirements")
	}
	if r.Status != "" && !r.Status.Valid() {
		return fmt.Errorf("store: invalid task revision status %q", r.Status)
	}
	for _, req := range r.EvidenceRequirements {
		if req.Kind == "" {
			return errors.New("store: evidence requirement kind is required")
		}
	}
	scope, err := missionJSON(r.Scope, "{}")
	if err != nil || len(scope) > domain.MaxMissionScopeBytes {
		return errors.New("store: task scope is too large")
	}
	return nil
}

func (d *DB) CreateTask(ctx context.Context, t *domain.Task) error {
	if t == nil || t.MissionID == "" {
		return errors.New("store: task mission_id is required")
	}
	r := t.Revision
	if r == nil {
		return errors.New("store: task revision is required")
	}
	if err := validateTaskRevision(r); err != nil {
		return err
	}
	status := r.Status
	if status == "" {
		status = domain.TaskRevisionProposed
	}
	id, ts, err := prepareCreate(t.CreatedAt)
	if err != nil {
		return err
	}
	scope, err := missionJSON(r.Scope, "{}")
	if err != nil {
		return err
	}
	reqs, err := missionJSON(r.EvidenceRequirements, "[]")
	if err != nil {
		return err
	}
	n, _ := encodeTime(ts)
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var taskCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mission_tasks WHERE mission_id = ?`, t.MissionID).Scan(&taskCount); err != nil {
		return err
	}
	if taskCount >= domain.MaxMissionTasks {
		return ErrMissionLimit
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO mission_tasks (id, mission_id, current_revision, created_at, updated_at) VALUES (?, ?, 1, ?, ?)`, id, t.MissionID, n, n); err != nil {
		return fmt.Errorf("store: create task: %w", mapConstraint(err, ErrNotFound))
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO mission_task_revisions (task_id, revision, title, objective, scope, evidence_requirements, status, proposed_by_run_id, created_at) VALUES (?, 1, ?, ?, ?, ?, ?, ?, ?)`, id, r.Title, r.Objective, scope, reqs, status, r.ProposedByRunID, n); err != nil {
		return fmt.Errorf("store: create task revision: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: create task commit: %w", err)
	}
	t.ID, t.CurrentRevision, t.CreatedAt, t.UpdatedAt = domain.TaskID(id), 1, ts, ts
	r.TaskID, r.Revision, r.Status, r.CreatedAt = domain.TaskID(id), 1, status, ts
	t.Revision = r
	return nil
}
func (d *DB) CreateTaskWithIdempotency(ctx context.Context, t *domain.Task, key string) (*domain.Task, bool, error) {
	if key == "" || strings.ContainsAny(key, "\r\n\x00") || len(key) > 256 {
		return nil, false, errors.New("store: task create idempotency_key is invalid")
	}
	if t == nil || t.MissionID == "" || t.Revision == nil {
		return nil, false, errors.New("store: task create requires mission and revision")
	}
	if err := validateTaskRevision(t.Revision); err != nil {
		return nil, false, err
	}
	payload, err := mutationPayload(struct {
		MissionID            domain.MissionID
		Title                string
		Objective            string
		Scope                domain.TaskScope
		EvidenceRequirements []domain.EvidenceRequirement
	}{t.MissionID, t.Revision.Title, t.Revision.Objective, t.Revision.Scope, t.Revision.EvidenceRequirements})
	if err != nil {
		return nil, false, err
	}
	status := t.Revision.Status
	if status == "" {
		status = domain.TaskRevisionProposed
	}
	scope, err := missionJSON(t.Revision.Scope, "{}")
	if err != nil {
		return nil, false, err
	}
	reqs, err := missionJSON(t.Revision.EvidenceRequirements, "[]")
	if err != nil {
		return nil, false, err
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback() }()
	var existingID string
	var existingRevision int
	var found bool
	err = tx.QueryRowContext(ctx, `SELECT result_id,result_revision FROM mission_mutation_receipts WHERE mission_id=? AND operation='task.create' AND idempotency_key=?`, t.MissionID, key).Scan(&existingID, &existingRevision)
	if err == nil {
		var stored string
		if scanErr := tx.QueryRowContext(ctx, `SELECT payload FROM mission_mutation_receipts WHERE mission_id=? AND operation='task.create' AND idempotency_key=?`, t.MissionID, key).Scan(&stored); scanErr != nil {
			return nil, false, scanErr
		}
		if stored != payload {
			return nil, false, ErrMissionIdempotencyConflict
		}
		found = true
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}
	if found {
		loaded, loadErr := d.loadTask(ctx, tx, domain.TaskID(existingID))
		if loadErr != nil {
			return nil, false, loadErr
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return nil, false, commitErr
		}
		return loaded, true, nil
	}
	var count int
	if countErr := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mission_tasks WHERE mission_id=?`, t.MissionID).Scan(&count); countErr != nil {
		return nil, false, countErr
	}
	if count >= domain.MaxMissionTasks {
		return nil, false, ErrMissionLimit
	}
	id, ts, err := prepareCreate(t.CreatedAt)
	if err != nil {
		return nil, false, err
	}
	n, _ := encodeTime(ts)
	if _, err := tx.ExecContext(ctx, `INSERT INTO mission_tasks (id,mission_id,current_revision,created_at,updated_at) VALUES (?,?,1,?,?)`, id, t.MissionID, n, n); err != nil {
		return nil, false, fmt.Errorf("store: create task: %w", mapConstraint(err, ErrConflict))
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO mission_task_revisions (task_id,revision,title,objective,scope,evidence_requirements,status,proposed_by_run_id,created_at) VALUES (?,1,?,?,?,?,?,?,?)`, id, t.Revision.Title, t.Revision.Objective, scope, reqs, status, t.Revision.ProposedByRunID, n); err != nil {
		return nil, false, err
	}
	if err := recordMutationReceipt(tx, ctx, t.MissionID, "task.create", key, payload, id, 1, n); err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	t.ID, t.CurrentRevision, t.CreatedAt, t.UpdatedAt = domain.TaskID(id), 1, ts, ts
	t.Revision.TaskID, t.Revision.Revision, t.Revision.Status, t.Revision.CreatedAt = t.ID, 1, status, ts
	return t, false, nil
}

func (d *DB) GetTask(ctx context.Context, id domain.TaskID) (*domain.Task, error) {
	return d.ProjectTask(ctx, id)
}

const taskColumns = `id, mission_id, current_revision, abandoned_at, created_at, updated_at`

func scanTaskBase(row interface{ Scan(...any) error }) (*domain.Task, error) {
	var t domain.Task
	var abandoned, created, updated *int64
	if err := row.Scan(&t.ID, &t.MissionID, &t.CurrentRevision, &abandoned, &created, &updated); err != nil {
		return nil, err
	}
	if abandoned != nil {
		a := decodeTime(*abandoned)
		t.AbandonedAt = &a
	}
	if created != nil {
		t.CreatedAt = decodeTime(*created)
	}
	if updated != nil {
		t.UpdatedAt = decodeTime(*updated)
	}
	return &t, nil
}

func scanTaskRevision(row interface{ Scan(...any) error }) (*domain.TaskRevision, error) {
	var r domain.TaskRevision
	var scope, reqs string
	var proposed string
	var created int64
	var accepted *int64
	if err := row.Scan(&r.TaskID, &r.Revision, &r.Title, &r.Objective, &scope, &reqs, &r.Status, &proposed, &r.SupersedesRevision, &created, &accepted); err != nil {
		return nil, err
	}
	r.ProposedByRunID = domain.RunID(proposed)
	r.CreatedAt = decodeTime(created)
	if accepted != nil {
		a := decodeTime(*accepted)
		r.AcceptedAt = &a
	}
	if err := json.Unmarshal([]byte(scope), &r.Scope); err != nil {
		return nil, fmt.Errorf("store: decode task scope: %w", err)
	}
	if err := json.Unmarshal([]byte(reqs), &r.EvidenceRequirements); err != nil {
		return nil, fmt.Errorf("store: decode task evidence requirements: %w", err)
	}
	return &r, nil
}

func (d *DB) ListTasks(ctx context.Context, missionID domain.MissionID) ([]*domain.Task, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT `+taskColumns+` FROM mission_tasks WHERE mission_id = ? ORDER BY id LIMIT 1025`, missionID)
	if err != nil {
		return nil, fmt.Errorf("store: list tasks: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var ids []domain.TaskID
	for rows.Next() {
		if len(ids) == 1024 {
			return nil, ErrConflict
		}
		t, scanErr := scanTaskBase(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		ids = append(ids, t.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]*domain.Task, 0, len(ids))
	for _, id := range ids {
		t, err := d.ProjectTask(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

func (d *DB) loadTask(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, id domain.TaskID) (*domain.Task, error) {
	t, err := scanTaskBase(q.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM mission_tasks WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	r, err := scanTaskRevision(q.QueryRowContext(ctx, `SELECT task_id, revision, title, objective, scope, evidence_requirements, status, proposed_by_run_id, supersedes_revision, created_at, accepted_at FROM mission_task_revisions WHERE task_id = ? AND revision = ?`, id, t.CurrentRevision))
	if err != nil {
		return nil, err
	}
	t.Revision = r
	rows, err := q.QueryContext(ctx, `SELECT task_id, task_revision, depends_on_task_id, depends_on_revision, output_ref, created_at FROM mission_task_dependencies WHERE task_id = ? AND task_revision = ? ORDER BY depends_on_task_id`, id, t.CurrentRevision)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var dep domain.TaskDependency
		var created int64
		if err := rows.Scan(&dep.TaskID, &dep.Revision, &dep.DependsOnTaskID, &dep.DependsOnRevision, &dep.OutputRef, &created); err != nil {
			return nil, err
		}
		dep.CreatedAt = decodeTime(created)
		t.Dependencies = append(t.Dependencies, dep)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return t, nil
}

func (d *DB) ProjectTask(ctx context.Context, id domain.TaskID) (*domain.Task, error) {
	t, err := d.loadTask(ctx, d.db, id)
	if err != nil {
		return nil, fmt.Errorf("store: project task: %w", err)
	}
	if t.AbandonedAt != nil {
		t.Status = domain.TaskAbandoned
		return t, nil
	}
	if t.Revision.Status != domain.TaskRevisionAccepted {
		t.Status = domain.TaskProposed
		t.Blockers = []domain.TaskBlocker{{Kind: "proposal", TaskID: t.ID, Action: "accept current task revision"}}
		return t, nil
	}
	for _, dep := range t.Dependencies {
		var n int
		err := d.db.QueryRowContext(ctx, `SELECT 1 FROM mission_acceptances a WHERE a.task_id = ? AND a.task_revision = ? AND EXISTS (SELECT 1 FROM missions m WHERE m.id = ? AND m.id = a.mission_id)`, dep.DependsOnTaskID, dep.DependsOnRevision, t.MissionID).Scan(&n)
		if errors.Is(err, sql.ErrNoRows) {
			t.Blockers = append(t.Blockers, domain.TaskBlocker{Kind: "dependency", TaskID: dep.DependsOnTaskID, Action: "accept required dependency output"})
		} else if err != nil {
			return nil, err
		}
	}
	if len(t.Blockers) > 0 {
		t.Status = domain.TaskBlocked
		return t, nil
	}
	var active, review, done int
	if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM mission_attempts WHERE task_id = ? AND task_revision = ? AND state IN ('reserved','launching','running','unknown')`, t.ID, t.CurrentRevision).Scan(&active); err != nil {
		return nil, err
	}
	if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM mission_submissions WHERE task_id = ? AND task_revision = ? AND state = 'proposed'`, t.ID, t.CurrentRevision).Scan(&review); err != nil {
		return nil, err
	}
	if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM mission_acceptances WHERE task_id = ? AND task_revision = ?`, t.ID, t.CurrentRevision).Scan(&done); err != nil {
		return nil, err
	}
	switch {
	case done > 0:
		t.Status = domain.TaskDone
	case review > 0:
		t.Status = domain.TaskReview
	case active > 0:
		t.Status = domain.TaskWorking
	default:
		t.Status = domain.TaskReady
	}
	return t, nil
}

func (d *DB) ProposeTaskRevision(ctx context.Context, id domain.TaskID, r *domain.TaskRevision, key string) (*domain.TaskRevision, error) {
	if key == "" || strings.ContainsAny(key, "\r\n\x00") || len(key) > 256 {
		return nil, errors.New("store: task proposal idempotency_key is invalid")
	}
	if err := validateTaskRevision(r); err != nil {
		return nil, err
	}
	scope, err := missionJSON(r.Scope, "{}")
	if err != nil {
		return nil, err
	}
	reqs, err := missionJSON(r.EvidenceRequirements, "[]")
	if err != nil {
		return nil, err
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, execErr := tx.ExecContext(ctx, `UPDATE mission_tasks SET updated_at = updated_at WHERE id = ?`, id); execErr != nil {
		return nil, execErr
	}
	var missionID domain.MissionID
	var current int
	if queryErr := tx.QueryRowContext(ctx, `SELECT mission_id, current_revision FROM mission_tasks WHERE id = ?`, id).Scan(&missionID, &current); errors.Is(queryErr, sql.ErrNoRows) {
		return nil, ErrNotFound
	} else if queryErr != nil {
		return nil, queryErr
	}
	payload, err := mutationPayload(struct {
		TaskID               domain.TaskID
		Title                string
		Objective            string
		Scope                domain.TaskScope
		EvidenceRequirements []domain.EvidenceRequirement
	}{id, r.Title, r.Objective, r.Scope, r.EvidenceRequirements})
	if err != nil {
		return nil, err
	}
	resultID, resultRevision, replayed, err := mutationReceipt(tx, ctx, missionID, "task.propose", key, payload)
	if err != nil {
		return nil, err
	}
	if replayed {
		existing, scanErr := scanTaskRevision(tx.QueryRowContext(ctx, `SELECT task_id,revision,title,objective,scope,evidence_requirements,status,proposed_by_run_id,supersedes_revision,created_at,accepted_at FROM mission_task_revisions WHERE task_id=? AND revision=?`, resultID, resultRevision))
		if scanErr != nil {
			return nil, scanErr
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return nil, commitErr
		}
		return existing, nil
	}
	var revisionCount int
	if countErr := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mission_task_revisions WHERE task_id = ?`, id).Scan(&revisionCount); countErr != nil {
		return nil, countErr
	}
	if revisionCount >= domain.MaxTaskRevisions {
		return nil, ErrMissionLimit
	}
	var next int
	if nextErr := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(revision), 0) + 1 FROM mission_task_revisions WHERE task_id = ?`, id).Scan(&next); nextErr != nil {
		return nil, nextErr
	}
	if r.Revision != 0 && r.Revision != next {
		return nil, fmt.Errorf("%w: expected task revision %d", ErrMissionStale, next)
	}
	now := missionNow(r.CreatedAt)
	n, _ := encodeTime(now)
	if _, execErr := tx.ExecContext(ctx, `INSERT INTO mission_task_revisions (task_id, revision, title, objective, scope, evidence_requirements, status, proposed_by_run_id, supersedes_revision, created_at) VALUES (?, ?, ?, ?, ?, ?, 'proposed', ?, ?, ?)`, id, next, r.Title, r.Objective, scope, reqs, r.ProposedByRunID, current, n); execErr != nil {
		return nil, fmt.Errorf("store: propose task revision: %w", execErr)
	}
	if receiptErr := recordMutationReceipt(tx, ctx, missionID, "task.propose", key, payload, string(id), next, n); receiptErr != nil {
		return nil, receiptErr
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return nil, commitErr
	}
	r.TaskID, r.Revision, r.Status, r.SupersedesRevision, r.CreatedAt = id, next, domain.TaskRevisionProposed, current, now
	return r, nil
}

func (d *DB) ReviseTask(ctx context.Context, id domain.TaskID, r *domain.TaskRevision, key string) (*domain.TaskRevision, error) {
	return d.ProposeTaskRevision(ctx, id, r, key)
}

func (d *DB) AcceptTaskRevision(ctx context.Context, id domain.TaskID, revision int, expectedGeneration uint64, key string) error {
	if key == "" || strings.ContainsAny(key, "\r\n\x00") || len(key) > 256 {
		return errors.New("store: task acceptance idempotency_key is invalid")
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var missionID domain.MissionID
	var generation uint64
	if missionErr := tx.QueryRowContext(ctx, `SELECT mission_id FROM mission_tasks WHERE id = ?`, id).Scan(&missionID); errors.Is(missionErr, sql.ErrNoRows) {
		return ErrNotFound
	} else if missionErr != nil {
		return missionErr
	}
	if generationErr := tx.QueryRowContext(ctx, `SELECT integrator_generation FROM missions WHERE id = ?`, missionID).Scan(&generation); generationErr != nil {
		return generationErr
	}
	if expectedGeneration == 0 || generation != expectedGeneration {
		return ErrMissionStale
	}
	payload, err := mutationPayload(struct {
		TaskID     domain.TaskID
		Revision   int
		Generation uint64
	}{id, revision, expectedGeneration})
	if err != nil {
		return err
	}
	_, _, replayed, err := mutationReceipt(tx, ctx, missionID, "task.accept", key, payload)
	if err != nil {
		return err
	}
	if replayed {
		return tx.Commit()
	}
	now := missionNow(time.Time{})
	n, _ := encodeTime(now)
	var previous int
	if currentErr := tx.QueryRowContext(ctx, `SELECT current_revision FROM mission_tasks WHERE id = ?`, id).Scan(&previous); currentErr != nil {
		return currentErr
	}
	// A revision change removes the prior output from the current accepted set
	// only when that revision has an accepted output. Proposed work alone is
	// not part of the set and must not advance its version.
	var previousOutput int
	if previous != revision {
		previousErr := tx.QueryRowContext(ctx, `SELECT 1 FROM mission_acceptances WHERE mission_id = ? AND task_id = ? AND task_revision = ?`, missionID, id, previous).Scan(&previousOutput)
		if previousErr != nil && !errors.Is(previousErr, sql.ErrNoRows) {
			return previousErr
		}
	}
	if revision < previous {
		return ErrMissionStale
	}
	if revision == previous {
		var currentStatus string
		if statusErr := tx.QueryRowContext(ctx, `SELECT status FROM mission_task_revisions WHERE task_id=? AND revision=?`, id, revision).Scan(&currentStatus); statusErr != nil {
			return statusErr
		}
		if currentStatus != string(domain.TaskRevisionProposed) {
			return ErrConflict
		}
	}
	res, err := tx.ExecContext(ctx, `UPDATE mission_task_revisions SET status = 'accepted', accepted_at = ? WHERE task_id = ? AND revision = ? AND status = 'proposed'`, n, id, revision)
	if err != nil {
		return err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return ErrConflict
	}
	if previous != revision {
		if _, supersedeErr := tx.ExecContext(ctx, `UPDATE mission_task_revisions SET status = 'superseded' WHERE task_id = ? AND revision = ? AND status = 'accepted'`, id, previous); supersedeErr != nil {
			return supersedeErr
		}
	}
	if _, updateErr := tx.ExecContext(ctx, `UPDATE mission_tasks SET current_revision = ?, updated_at = ? WHERE id = ?`, revision, n, id); updateErr != nil {
		return updateErr
	}
	if receiptErr := recordMutationReceipt(tx, ctx, missionID, "task.accept", key, payload, string(id), revision, n); receiptErr != nil {
		return receiptErr
	}
	if previousOutput == 1 {
		if _, versionErr := tx.ExecContext(ctx, `UPDATE missions SET accepted_set_version = accepted_set_version + 1, updated_at = ? WHERE id = ?`, n, missionID); versionErr != nil {
			return versionErr
		}
	}
	return tx.Commit()
}

func (d *DB) SetTaskDependencies(ctx context.Context, id domain.TaskID, revision int, deps []domain.TaskDependency, key string) error {
	if key == "" || strings.ContainsAny(key, "\r\n\x00") || len(key) > 256 {
		return errors.New("store: task dependency idempotency_key is invalid")
	}
	if revision <= 0 || len(deps) > 128 {
		return errors.New("store: task dependency revision or count is invalid")
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, lockErr := tx.ExecContext(ctx, `UPDATE mission_tasks SET updated_at = updated_at WHERE id = ?`, id); lockErr != nil {
		return lockErr
	}
	var missionID domain.MissionID
	if missionErr := tx.QueryRowContext(ctx, `SELECT mission_id FROM mission_tasks WHERE id = ?`, id).Scan(&missionID); errors.Is(missionErr, sql.ErrNoRows) {
		return ErrNotFound
	} else if missionErr != nil {
		return missionErr
	}
	payload, err := mutationPayload(struct {
		TaskID       domain.TaskID
		Revision     int
		Dependencies []domain.TaskDependency
	}{id, revision, deps})
	if err != nil {
		return err
	}
	_, _, replayed, err := mutationReceipt(tx, ctx, missionID, "task.dependencies", key, payload)
	if err != nil {
		return err
	}
	if replayed {
		return tx.Commit()
	}
	var exists int
	var revisionStatus string
	if revisionErr := tx.QueryRowContext(ctx, `SELECT status FROM mission_task_revisions WHERE task_id = ? AND revision = ?`, id, revision).Scan(&revisionStatus); revisionErr != nil {
		if errors.Is(revisionErr, sql.ErrNoRows) {
			return ErrNotFound
		}
		return revisionErr
	}
	if revisionStatus == string(domain.TaskRevisionAccepted) {
		return ErrConflict
	}
	adj := make(map[domain.TaskID][]domain.TaskID)
	rows, err := tx.QueryContext(ctx, `SELECT d.task_id, d.depends_on_task_id FROM mission_task_dependencies d JOIN mission_tasks t ON t.id = d.task_id WHERE t.mission_id = ? AND NOT (d.task_id = ? AND d.task_revision = ?)`, missionID, id, revision)
	if err != nil {
		return err
	}
	for rows.Next() {
		var from, to domain.TaskID
		if scanErr := rows.Scan(&from, &to); scanErr != nil {
			_ = rows.Close()
			return scanErr
		}
		adj[from] = append(adj[from], to)
	}
	_ = rows.Close()
	for _, dep := range deps {
		if dep.DependsOnTaskID == "" || dep.DependsOnRevision <= 0 || dep.DependsOnTaskID == id {
			return ErrMissionCycle
		}
		var depMission domain.MissionID
		if depErr := tx.QueryRowContext(ctx, `SELECT mission_id FROM mission_tasks WHERE id = ?`, dep.DependsOnTaskID).Scan(&depMission); depErr != nil {
			return ErrNotFound
		}
		if depMission != missionID {
			return ErrConflict
		}
		if revisionErr := tx.QueryRowContext(ctx, `SELECT 1 FROM mission_task_revisions WHERE task_id = ? AND revision = ?`, dep.DependsOnTaskID, dep.DependsOnRevision).Scan(&exists); revisionErr != nil {
			return ErrNotFound
		}
		adj[id] = append(adj[id], dep.DependsOnTaskID)
	}
	visiting := map[domain.TaskID]bool{}
	visited := map[domain.TaskID]bool{}
	var visit func(domain.TaskID) bool
	visit = func(n domain.TaskID) bool {
		if visiting[n] {
			return false
		}
		if visited[n] {
			return true
		}
		visiting[n] = true
		for _, child := range adj[n] {
			if !visit(child) {
				return false
			}
		}
		visiting[n] = false
		visited[n] = true
		return true
	}
	for n := range adj {
		if !visit(n) {
			return ErrMissionCycle
		}
	}
	if _, deleteErr := tx.ExecContext(ctx, `DELETE FROM mission_task_dependencies WHERE task_id = ? AND task_revision = ?`, id, revision); deleteErr != nil {
		return deleteErr
	}
	now := missionNow(time.Time{})
	n, _ := encodeTime(now)
	for _, dep := range deps {
		if _, insertErr := tx.ExecContext(ctx, `INSERT INTO mission_task_dependencies (task_id, task_revision, depends_on_task_id, depends_on_revision, output_ref, created_at) VALUES (?, ?, ?, ?, ?, ?)`, id, revision, dep.DependsOnTaskID, dep.DependsOnRevision, dep.OutputRef, n); insertErr != nil {
			return fmt.Errorf("store: set task dependencies: %w", insertErr)
		}
	}
	if receiptErr := recordMutationReceipt(tx, ctx, missionID, "task.dependencies", key, payload, string(id), revision, n); receiptErr != nil {
		return receiptErr
	}
	return tx.Commit()
}

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
	if _, lockErr := tx.ExecContext(ctx, `UPDATE missions SET updated_at = updated_at WHERE id = ?`, r.MissionID); lockErr != nil {
		return nil, false, lockErr
	}
	m, err := scanMission(tx.QueryRowContext(ctx, `SELECT `+missionColumns+` FROM missions WHERE id = ?`, r.MissionID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, ErrNotFound
	}
	if err != nil {
		return nil, false, err
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

func (d *DB) AbandonTask(ctx context.Context, id domain.TaskID, expectedGeneration uint64, key string) error {
	if key == "" || strings.ContainsAny(key, "\r\n\x00") || len(key) > 256 {
		return errors.New("store: task abandon idempotency_key is invalid")
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var missionID domain.MissionID
	if missionErr := tx.QueryRowContext(ctx, `SELECT mission_id FROM mission_tasks WHERE id=?`, id).Scan(&missionID); errors.Is(missionErr, sql.ErrNoRows) {
		return ErrNotFound
	} else if missionErr != nil {
		return missionErr
	}
	var generation uint64
	if generationErr := tx.QueryRowContext(ctx, `SELECT integrator_generation FROM missions WHERE id=?`, missionID).Scan(&generation); generationErr != nil {
		return generationErr
	}
	if expectedGeneration == 0 || expectedGeneration != generation {
		return ErrMissionStale
	}
	payload, err := mutationPayload(struct {
		TaskID     domain.TaskID
		Generation uint64
	}{id, expectedGeneration})
	if err != nil {
		return err
	}
	_, _, replayed, err := mutationReceipt(tx, ctx, missionID, "task.abandon", key, payload)
	if err != nil {
		return err
	}
	if replayed {
		return tx.Commit()
	}
	now := missionNow(time.Time{})
	n, _ := encodeTime(now)
	var currentRevision int
	if revisionErr := tx.QueryRowContext(ctx, `SELECT current_revision FROM mission_tasks WHERE id=?`, id).Scan(&currentRevision); revisionErr != nil {
		return revisionErr
	}
	// Abandonment removes an output from the current accepted set only when
	// the task's current revision has an acceptance. Proposed work is ignored.
	var currentOutput int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM mission_acceptances WHERE mission_id=? AND task_id=? AND task_revision=?`, missionID, id, currentRevision).Scan(&currentOutput)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE mission_tasks SET abandoned_at=?, updated_at=? WHERE id=? AND abandoned_at IS NULL`, n, n, id)
	if err != nil {
		return err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return ErrConflict
	}
	if _, updateErr := tx.ExecContext(ctx, `UPDATE mission_task_revisions SET status='abandoned' WHERE task_id=? AND revision=(SELECT current_revision FROM mission_tasks WHERE id=?)`, id, id); updateErr != nil {
		return updateErr
	}
	if receiptErr := recordMutationReceipt(tx, ctx, missionID, "task.abandon", key, payload, string(id), 0, n); receiptErr != nil {
		return receiptErr
	}
	if currentOutput == 1 {
		if _, versionErr := tx.ExecContext(ctx, `UPDATE missions SET accepted_set_version=accepted_set_version+1, updated_at=? WHERE id=?`, n, missionID); versionErr != nil {
			return versionErr
		}
	}
	return tx.Commit()
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
