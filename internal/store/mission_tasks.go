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
