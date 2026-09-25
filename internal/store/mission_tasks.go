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
	// A task created already accepted is approved work from the start; the
	// acceptance timestamp is what later self-acceptance checks read.
	var acceptedAt *int64
	if status == domain.TaskRevisionAccepted {
		acceptedAt = &n
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO mission_task_revisions (task_id, revision, title, objective, scope, evidence_requirements, material, status, proposed_by_run_id, created_at, accepted_at) VALUES (?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, r.Title, r.Objective, scope, reqs, r.Material, status, r.ProposedByRunID, n, acceptedAt); err != nil {
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
		Material             bool
		EvidenceRequirements []domain.EvidenceRequirement
		DependsOn            []domain.TaskID `json:",omitempty"`
	}{t.MissionID, t.Revision.Title, t.Revision.Objective, t.Revision.Scope, t.Revision.Material, t.Revision.EvidenceRequirements, t.Revision.DependsOn})
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
	mission, err := lockMissionRow(ctx, tx, t.MissionID)
	if err != nil {
		return nil, false, err
	}
	if phaseErr := requireMissionPhase(mission, "task.propose", domain.MissionPhasePlanning, domain.MissionPhaseClarified, domain.MissionPhaseActive); phaseErr != nil {
		return nil, false, phaseErr
	}
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
	if _, err := tx.ExecContext(ctx, `INSERT INTO mission_task_revisions (task_id,revision,title,objective,scope,evidence_requirements,material,status,proposed_by_run_id,created_at) VALUES (?,1,?,?,?,?,?,?,?,?)`, id, t.Revision.Title, t.Revision.Objective, scope, reqs, t.Revision.Material, status, t.Revision.ProposedByRunID, n); err != nil {
		return nil, false, err
	}
	if err := writeTaskDependencies(ctx, tx, t.MissionID, domain.TaskID(id), 1, t.Revision.DependsOn, n); err != nil {
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

const taskRevisionColumns = `task_id, revision, title, objective, scope, evidence_requirements, material, status, proposed_by_run_id, supersedes_revision, accepted_by_member_id, accepted_by_run_id, created_at, accepted_at`

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
	var material int
	var created int64
	var accepted *int64
	if err := row.Scan(&r.TaskID, &r.Revision, &r.Title, &r.Objective, &scope, &reqs, &material, &r.Status, &proposed, &r.SupersedesRevision, &r.AcceptedByMemberID, &r.AcceptedByRunID, &created, &accepted); err != nil {
		return nil, err
	}
	r.Material = material != 0
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
	r, err := scanTaskRevision(q.QueryRowContext(ctx, `SELECT `+taskRevisionColumns+` FROM mission_task_revisions WHERE task_id = ? AND revision = ?`, id, t.CurrentRevision))
	if err != nil {
		return nil, err
	}
	t.Revision = r
	// The pending revision is what an amendment proposes for this task. It is
	// never the current revision, so nothing can dispatch against it.
	pending, err := scanTaskRevision(q.QueryRowContext(ctx, `SELECT `+taskRevisionColumns+` FROM mission_task_revisions WHERE task_id = ? AND status = 'proposed' AND revision > ? ORDER BY revision DESC LIMIT 1`, id, t.CurrentRevision))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err == nil {
		t.PendingRevision = pending
	}
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
		t.Blockers = []domain.TaskBlocker{{Kind: "proposal", TaskID: t.ID, Action: "accept the current task revision, or submit it in a plan for human approval"}}
		return t, nil
	}
	for _, dep := range t.Dependencies {
		var n int
		err := d.db.QueryRowContext(ctx, `SELECT 1 FROM mission_acceptances a JOIN mission_tasks t ON t.id = a.task_id AND t.current_revision = a.task_revision WHERE a.task_id = ? AND a.mission_id = ?`, dep.DependsOnTaskID, t.MissionID).Scan(&n)
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

// ProposeTaskRevision is the integrator's task.revise. Before the plan is
// approved it rewrites the draft in place; see proposeTaskRevision.
func (d *DB) ProposeTaskRevision(ctx context.Context, id domain.TaskID, r *domain.TaskRevision, key string) (*domain.TaskRevision, error) {
	return d.proposeTaskRevision(ctx, id, r, key, "task.revise", domain.MissionPhasePlanning, domain.MissionPhaseClarified, domain.MissionPhaseActive)
}

// ReviseTask is a worker's task.revise, which only an approved plan admits.
func (d *DB) ReviseTask(ctx context.Context, id domain.TaskID, r *domain.TaskRevision, key string) (*domain.TaskRevision, error) {
	return d.proposeTaskRevision(ctx, id, r, key, "task.revise", domain.MissionPhaseActive)
}

func (d *DB) proposeTaskRevision(ctx context.Context, id domain.TaskID, r *domain.TaskRevision, key, operation string, allowed ...domain.MissionPhase) (*domain.TaskRevision, error) {
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
	mission, err := lockMissionRowForTask(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if phaseErr := requireMissionPhase(mission, operation, allowed...); phaseErr != nil {
		return nil, phaseErr
	}
	missionID := mission.ID
	if _, execErr := tx.ExecContext(ctx, `UPDATE mission_tasks SET updated_at = updated_at WHERE id = ?`, id); execErr != nil {
		return nil, execErr
	}
	var current int
	var abandoned *int64
	if queryErr := tx.QueryRowContext(ctx, `SELECT current_revision, abandoned_at FROM mission_tasks WHERE id = ?`, id).Scan(&current, &abandoned); queryErr != nil {
		return nil, queryErr
	}
	// An abandoned task left the plan a human saw; reviving it is new work
	// that must go through a plan round as a fresh task.
	if abandoned != nil {
		return nil, fmt.Errorf("%w: task %s is abandoned", ErrConflict, id)
	}
	payload, err := mutationPayload(struct {
		TaskID               domain.TaskID
		Title                string
		Objective            string
		Scope                domain.TaskScope
		Material             bool
		EvidenceRequirements []domain.EvidenceRequirement
		DependsOn            []domain.TaskID `json:",omitempty"`
	}{id, r.Title, r.Objective, r.Scope, r.Material, r.EvidenceRequirements, r.DependsOn})
	if err != nil {
		return nil, err
	}
	resultID, resultRevision, replayed, err := mutationReceipt(tx, ctx, missionID, "task.propose", key, payload)
	if err != nil {
		return nil, err
	}
	if replayed {
		existing, scanErr := scanTaskRevision(tx.QueryRowContext(ctx, `SELECT `+taskRevisionColumns+` FROM mission_task_revisions WHERE task_id=? AND revision=?`, resultID, resultRevision))
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
	if _, execErr := tx.ExecContext(ctx, `INSERT INTO mission_task_revisions (task_id, revision, title, objective, scope, evidence_requirements, material, status, proposed_by_run_id, supersedes_revision, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, 'proposed', ?, ?, ?)`, id, next, r.Title, r.Objective, scope, reqs, r.Material, r.ProposedByRunID, current, n); execErr != nil {
		return nil, fmt.Errorf("store: propose task revision: %w", execErr)
	}
	if depErr := writeTaskDependencies(ctx, tx, missionID, id, next, r.DependsOn, n); depErr != nil {
		return nil, depErr
	}
	// Before approval, accept is forbidden, so nothing would ever advance
	// current_revision and the human would review a stale draft. Superseding
	// the never-accepted draft and moving current_revision to the new revision
	// makes the plan the integrator wrote the plan the human reads. No attempt
	// or submission can reference a never-accepted revision, so it is safe.
	// The invariant this holds: in planning, clarified, and plan_review every
	// non-abandoned task's current revision is the plan revision.
	if mission.Phase == domain.MissionPhasePlanning || mission.Phase == domain.MissionPhaseClarified {
		var currentStatus string
		if statusErr := tx.QueryRowContext(ctx, `SELECT status FROM mission_task_revisions WHERE task_id = ? AND revision = ?`, id, current).Scan(&currentStatus); statusErr != nil {
			return nil, statusErr
		}
		if currentStatus == string(domain.TaskRevisionProposed) {
			if _, supersedeErr := tx.ExecContext(ctx, `UPDATE mission_task_revisions SET status = 'superseded' WHERE task_id = ? AND revision = ? AND status = 'proposed'`, id, current); supersedeErr != nil {
				return nil, fmt.Errorf("store: supersede planning task revision: %w", supersedeErr)
			}
			if _, advanceErr := tx.ExecContext(ctx, `UPDATE mission_tasks SET current_revision = ?, updated_at = ? WHERE id = ?`, next, n, id); advanceErr != nil {
				return nil, fmt.Errorf("store: advance planning task revision: %w", advanceErr)
			}
		}
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

// AcceptTaskRevision is the integrator's task.accept. After activation the
// integrator still accepts revisions alone, but only inside the plan a human
// approved: anything that introduces new work, is declared material, reaches
// outside the approved scope, or belongs to a task a human sent back has to go
// through
// mission.plan.submit. acceptedBy records the run that accepted, so a
// self-accepted revision is distinguishable from one a human approved.
func (d *DB) AcceptTaskRevision(ctx context.Context, id domain.TaskID, revision int, expectedGeneration uint64, acceptedBy domain.RunID, key string) error {
	if key == "" || strings.ContainsAny(key, "\r\n\x00") || len(key) > 256 {
		return errors.New("store: task acceptance idempotency_key is invalid")
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	mission, err := lockMissionRowForTask(ctx, tx, id)
	if err != nil {
		return err
	}
	if phaseErr := requireMissionPhase(mission, "task.accept", domain.MissionPhaseActive); phaseErr != nil {
		return phaseErr
	}
	missionID := mission.ID
	if expectedGeneration == 0 || mission.IntegratorGeneration != expectedGeneration {
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
	if boundsErr := selfAcceptanceWithinApprovedPlan(ctx, tx, missionID, id, revision, previous); boundsErr != nil {
		return boundsErr
	}
	res, err := tx.ExecContext(ctx, `UPDATE mission_task_revisions SET status = 'accepted', accepted_at = ?, accepted_by_run_id = ? WHERE task_id = ? AND revision = ? AND status = 'proposed'`, n, acceptedBy, id, revision)
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

// selfAcceptanceWithinApprovedPlan is the whole boundary of what an integrator
// may accept on its own. Each refusal wraps ErrMissionAmendmentRequired and
// names the command that lifts it, because the integrator's next move is
// always the same: submit the revision as an amendment.
func selfAcceptanceWithinApprovedPlan(ctx context.Context, tx *sql.Tx, missionID domain.MissionID, id domain.TaskID, revision, current int) error {
	// "Ever approved" is an acceptance timestamp, not a status: the planning
	// supersede-in-place rule stamps never-approved drafts superseded too.
	var approvedRevisions int
	if countErr := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mission_task_revisions WHERE task_id=? AND accepted_at IS NOT NULL`, id).Scan(&approvedRevisions); countErr != nil {
		return fmt.Errorf("store: count approved revisions of task %s: %w", id, countErr)
	}
	if approvedRevisions == 0 {
		return fmt.Errorf("%w: task %s is new work; submit it with mission plan submit", ErrMissionAmendmentRequired, id)
	}
	target, err := scanTaskRevision(tx.QueryRowContext(ctx, `SELECT `+taskRevisionColumns+` FROM mission_task_revisions WHERE task_id=? AND revision=?`, id, revision))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("store: read task %s revision %d: %w", id, revision, err)
	}
	if target.Material {
		return fmt.Errorf("%w: task %s revision %d is material; submit it with mission plan submit", ErrMissionAmendmentRequired, id, revision)
	}
	approved, err := approvedScopeUnion(ctx, tx, missionID)
	if err != nil {
		return err
	}
	if widening := widenedPaths(approved, target.Scope.ExpectedPaths); len(widening) > 0 {
		return fmt.Errorf("%w: task %s revision %d widens the approved scope to %v; submit it with mission plan submit", ErrMissionAmendmentRequired, id, revision, widening)
	}
	currentScope, err := scanTaskRevision(tx.QueryRowContext(ctx, `SELECT `+taskRevisionColumns+` FROM mission_task_revisions WHERE task_id=? AND revision=?`, id, current))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("store: read task %s revision %d: %w", id, current, err)
	}
	if err == nil && currentScope.Status == domain.TaskRevisionAccepted {
		if dropped := droppedExclusions(currentScope.Scope.Exclusions, target.Scope.Exclusions); len(dropped) > 0 {
			return fmt.Errorf("%w: task %s revision %d drops exclusion %s from the approved scope; submit it with mission plan submit", ErrMissionAmendmentRequired, id, revision, dropped[0])
		}
	}
	// A task whose latest decided round was sent back stays with the human
	// until a round approves it again: the refusal is keyed on the task, not
	// the revision number, so re-proposing the declined change as a fresh
	// revision does not get around the decision.
	var lastDecision string
	var lastRound uint64
	lastErr := tx.QueryRowContext(ctx, `SELECT v.decision, v.plan_version FROM mission_plan_items i
		JOIN mission_plan_reviews v ON v.mission_id=i.mission_id AND v.plan_version=i.plan_version
		WHERE i.task_id=? AND v.decision<>'' ORDER BY v.plan_version DESC LIMIT 1`, id).Scan(&lastDecision, &lastRound)
	if lastErr != nil && !errors.Is(lastErr, sql.ErrNoRows) {
		return fmt.Errorf("store: read plan rounds of task %s: %w", id, lastErr)
	}
	if lastErr == nil && lastDecision == string(domain.MissionPlanRevise) {
		return fmt.Errorf("%w: task %s was sent back in plan version %d; submit its next revision with mission plan submit", ErrMissionAmendmentRequired, id, lastRound)
	}
	return nil
}

// approvedScopeUnion is the expected_paths union over the current revisions of
// every non-abandoned task whose current revision is accepted: the scope a
// human has already signed off. It includes the accepting task's own accepted
// revision, so re-declaring the same paths never widens anything.
func approvedScopeUnion(ctx context.Context, tx *sql.Tx, missionID domain.MissionID) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT r.scope FROM mission_tasks t
		JOIN mission_task_revisions r ON r.task_id=t.id AND r.revision=t.current_revision
		WHERE t.mission_id=? AND t.abandoned_at IS NULL AND r.status='accepted'`, missionID)
	if err != nil {
		return nil, fmt.Errorf("store: read approved mission scope: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var union []string
	for rows.Next() {
		var raw string
		if scanErr := rows.Scan(&raw); scanErr != nil {
			return nil, fmt.Errorf("store: read approved mission scope: %w", scanErr)
		}
		var scope domain.TaskScope
		if unmarshalErr := json.Unmarshal([]byte(raw), &scope); unmarshalErr != nil {
			return nil, fmt.Errorf("store: decode approved mission scope: %w", unmarshalErr)
		}
		union = append(union, scope.ExpectedPaths...)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read approved mission scope: %w", err)
	}
	return union, nil
}

// widenedPaths returns the declared paths the approved union does not cover.
// An empty union covers nothing, so any declared path widens it.
func widenedPaths(approved, declared []string) []string {
	var out []string
	for _, p := range declared {
		if !domain.ScopeCovers(approved, p) {
			out = append(out, p)
		}
	}
	return out
}

// droppedExclusions returns the first approved exclusion the proposed set no
// longer covers, or "" when every one is still excluded. Dropping an exclusion
// widens the approved scope just as adding a path does.
// droppedExclusions lists the approved exclusions a proposal no longer
// carries; each one widens the effective scope exactly as a new path does.
func droppedExclusions(approved, proposed []string) []string {
	var out []string
	for _, e := range approved {
		if !domain.ScopeCovers(proposed, e) {
			out = append(out, e)
		}
	}
	return out
}

// writeTaskDependencies records what a new revision waits for. Each row
// stores the dependency's current revision at write time, but readiness
// (ProjectTask, ReserveAttempt) follows the dependency task's current
// revision, so revising a dependency never leaves the dependent blocked for
// good. Cycle detection walks every live revision of the mission: an edge on a
// proposed revision becomes real once that revision is accepted.
func writeTaskDependencies(ctx context.Context, tx *sql.Tx, missionID domain.MissionID, id domain.TaskID, revision int, dependsOn []domain.TaskID, now int64) error {
	if len(dependsOn) == 0 {
		return nil
	}
	if len(dependsOn) > domain.MaxTaskDependencies {
		return errors.New("store: task revision has too many dependencies")
	}
	adj := make(map[domain.TaskID][]domain.TaskID)
	rows, err := tx.QueryContext(ctx, `SELECT d.task_id, d.depends_on_task_id FROM mission_task_dependencies d JOIN mission_tasks t ON t.id = d.task_id JOIN mission_task_revisions r ON r.task_id = d.task_id AND r.revision = d.task_revision WHERE t.mission_id = ? AND t.abandoned_at IS NULL AND r.status IN ('proposed', 'accepted')`, missionID)
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
	seen := make(map[domain.TaskID]bool, len(dependsOn))
	for _, dep := range dependsOn {
		if seen[dep] {
			return fmt.Errorf("%w: task %s lists dependency %s twice", ErrConflict, id, dep)
		}
		seen[dep] = true
		var depMission domain.MissionID
		var depRevision int
		var abandoned *int64
		if depErr := tx.QueryRowContext(ctx, `SELECT mission_id, current_revision, abandoned_at FROM mission_tasks WHERE id = ?`, dep).Scan(&depMission, &depRevision, &abandoned); errors.Is(depErr, sql.ErrNoRows) {
			return fmt.Errorf("%w: task %s depends on unknown task %s", ErrNotFound, id, dep)
		} else if depErr != nil {
			return depErr
		}
		if depMission != missionID {
			return fmt.Errorf("%w: dependency %s is outside the mission", ErrConflict, dep)
		}
		if abandoned != nil {
			return fmt.Errorf("%w: dependency %s is abandoned", ErrConflict, dep)
		}
		if chain := dependencyChain(adj, dep, id); chain != nil {
			return fmt.Errorf("%w: task %s waits for task %s", ErrMissionCycle, id, strings.Join(chain, ", which waits for task "))
		}
		if _, insertErr := tx.ExecContext(ctx, `INSERT INTO mission_task_dependencies (task_id, task_revision, depends_on_task_id, depends_on_revision, created_at) VALUES (?, ?, ?, ?, ?)`, id, revision, dep, depRevision, now); insertErr != nil {
			return fmt.Errorf("store: write task dependency: %w", insertErr)
		}
		adj[id] = append(adj[id], dep)
	}
	return nil
}

// dependencyChain returns the tasks from one to another along dependency
// edges, nil when none leads there.
func dependencyChain(adj map[domain.TaskID][]domain.TaskID, from, to domain.TaskID) []string {
	visited := map[domain.TaskID]bool{}
	var walk func(domain.TaskID) []string
	walk = func(n domain.TaskID) []string {
		if n == to {
			return []string{string(n)}
		}
		if visited[n] {
			return nil
		}
		visited[n] = true
		for _, next := range adj[n] {
			if chain := walk(next); chain != nil {
				return append([]string{string(n)}, chain...)
			}
		}
		return nil
	}
	return walk(from)
}

// AbandonTask drops a whole task, or, when revision names a pending revision
// above the task's current one, drops only that revision. Dropping a pending
// revision touches neither the task nor accepted_set_version: nothing that was
// approved changes.
func (d *DB) AbandonTask(ctx context.Context, id domain.TaskID, revision int, expectedGeneration uint64, key string) error {
	if key == "" || strings.ContainsAny(key, "\r\n\x00") || len(key) > 256 {
		return errors.New("store: task abandon idempotency_key is invalid")
	}
	if revision < 0 {
		return errors.New("store: task abandon revision must be a pending revision")
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	mission, err := lockMissionRowForTask(ctx, tx, id)
	if err != nil {
		return err
	}
	if phaseErr := requireMissionPhase(mission, "task.abandon", domain.MissionPhasePlanning, domain.MissionPhaseClarified, domain.MissionPhaseActive); phaseErr != nil {
		return phaseErr
	}
	missionID := mission.ID
	if expectedGeneration == 0 || expectedGeneration != mission.IntegratorGeneration {
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
	if revision > 0 {
		if revision <= currentRevision {
			return fmt.Errorf("%w: task %s revision %d is not a pending revision; the current revision is %d", ErrConflict, id, revision, currentRevision)
		}
		res, execErr := tx.ExecContext(ctx, `UPDATE mission_task_revisions SET status='abandoned' WHERE task_id=? AND revision=? AND status='proposed'`, id, revision)
		if execErr != nil {
			return fmt.Errorf("store: abandon task %s revision %d: %w", id, revision, execErr)
		}
		if affected, _ := res.RowsAffected(); affected != 1 {
			return fmt.Errorf("%w: task %s revision %d is not proposed", ErrConflict, id, revision)
		}
		if receiptErr := recordMutationReceipt(tx, ctx, missionID, "task.abandon", key, payload, string(id), revision, n); receiptErr != nil {
			return receiptErr
		}
		return tx.Commit()
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
