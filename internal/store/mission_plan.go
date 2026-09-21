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

const missionQuestionColumns = `id, mission_id, seq, body, asked_by_run_id, asked_at, answer, answered_by_member_id, answered_at`

func scanMissionQuestion(row interface{ Scan(...any) error }) (*domain.MissionQuestion, error) {
	var q domain.MissionQuestion
	var asked int64
	var answered *int64
	if err := row.Scan(&q.ID, &q.MissionID, &q.Seq, &q.Body, &q.AskedByRunID, &asked, &q.Answer, &q.AnsweredByMemberID, &answered); err != nil {
		return nil, err
	}
	q.AskedAt, q.AnsweredAt = decodeTime(asked), decodeTimePtr(answered)
	return &q, nil
}

const missionPlanReviewColumns = `mission_id, plan_version, summary, submitted_by_run_id, submitted_at, submitted_phase, decision, feedback, decided_by_member_id, decided_at`

func scanMissionPlanReview(row interface{ Scan(...any) error }) (*domain.MissionPlanReview, error) {
	var r domain.MissionPlanReview
	var decision, submittedPhase string
	var submitted int64
	var decided *int64
	if err := row.Scan(&r.MissionID, &r.PlanVersion, &r.Summary, &r.SubmittedByRunID, &submitted, &submittedPhase, &decision, &r.Feedback, &r.DecidedByMemberID, &decided); err != nil {
		return nil, err
	}
	r.SubmittedPhase = domain.MissionPhase(submittedPhase)
	r.Decision = domain.MissionPlanDecision(decision)
	r.SubmittedAt, r.DecidedAt = decodeTime(submitted), decodeTimePtr(decided)
	return &r, nil
}

// populateOpenQuestions fills Mission.OpenQuestions for a page of missions
// with one grouped count. Only the non-transactional reads call it; a
// transaction-local mission read leaves the field zero on purpose.
func (d *DB) populateOpenQuestions(ctx context.Context, missions []*domain.Mission) error {
	if len(missions) == 0 {
		return nil
	}
	args := make([]any, 0, len(missions))
	byID := make(map[domain.MissionID]*domain.Mission, len(missions))
	for _, m := range missions {
		args = append(args, m.ID)
		byID[m.ID] = m
	}
	query := `SELECT mission_id, COUNT(*) FROM mission_questions WHERE answered_at IS NULL AND mission_id IN (?` +
		strings.Repeat(`,?`, len(args)-1) + `) GROUP BY mission_id`
	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("store: count open mission questions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id domain.MissionID
		var open int
		if scanErr := rows.Scan(&id, &open); scanErr != nil {
			return fmt.Errorf("store: count open mission questions: %w", scanErr)
		}
		if m, ok := byID[id]; ok {
			m.OpenQuestions = open
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: count open mission questions: %w", err)
	}
	return nil
}

func validMutationKey(key string) bool {
	return key != "" && len(key) <= 256 && !strings.ContainsAny(key, "\r\n\x00")
}

// InsertMissionQuestion records one clarifying question the integrator asks
// the accountable human. Questions are only asked while planning.
func (d *DB) InsertMissionQuestion(ctx context.Context, missionID domain.MissionID, askedBy domain.RunID, body, key string) (*domain.MissionQuestion, error) {
	if missionID == "" || askedBy == "" || strings.TrimSpace(body) == "" || !validMutationKey(key) {
		return nil, errors.New("store: mission question requires mission_id, asking run, body, and idempotency_key")
	}
	payload, err := mutationPayload(struct{ Body string }{body})
	if err != nil {
		return nil, err
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("store: ask mission question: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	m, err := lockMissionRow(ctx, tx, missionID)
	if err != nil {
		return nil, err
	}
	if phaseErr := requireMissionPhase(m, "mission.question.ask", domain.MissionPhasePlanning, domain.MissionPhaseClarified); phaseErr != nil {
		return nil, phaseErr
	}
	resultID, _, replayed, err := mutationReceipt(tx, ctx, missionID, "mission.question.ask", key, payload)
	if err != nil {
		return nil, err
	}
	if replayed {
		q, scanErr := scanMissionQuestion(tx.QueryRowContext(ctx, `SELECT `+missionQuestionColumns+` FROM mission_questions WHERE id=?`, resultID))
		if scanErr != nil {
			return nil, fmt.Errorf("store: replay mission question %s: %w", resultID, scanErr)
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return nil, commitErr
		}
		return q, nil
	}
	var count int
	if countErr := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mission_questions WHERE mission_id=?`, missionID).Scan(&count); countErr != nil {
		return nil, fmt.Errorf("store: count mission questions: %w", countErr)
	}
	if count >= domain.MaxMissionQuestions {
		return nil, fmt.Errorf("%w: mission %s already has %d questions", ErrMissionLimit, missionID, count)
	}
	var seq int
	if seqErr := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0)+1 FROM mission_questions WHERE mission_id=?`, missionID).Scan(&seq); seqErr != nil {
		return nil, fmt.Errorf("store: next mission question seq: %w", seqErr)
	}
	id, ts, err := prepareCreate(time.Time{})
	if err != nil {
		return nil, err
	}
	n, err := encodeTime(ts)
	if err != nil {
		return nil, err
	}
	if _, insertErr := tx.ExecContext(ctx, `INSERT INTO mission_questions (id,mission_id,seq,body,asked_by_run_id,asked_at) VALUES (?,?,?,?,?,?)`, id, missionID, seq, body, askedBy, n); insertErr != nil {
		return nil, fmt.Errorf("store: insert mission question: %w", mapConstraint(insertErr, ErrNotFound))
	}
	if receiptErr := recordMutationReceipt(tx, ctx, missionID, "mission.question.ask", key, payload, id, seq, n); receiptErr != nil {
		return nil, receiptErr
	}
	// Clarification is not a one-way door: asking again reopens it, and the
	// unanswered-question check then holds the mission in planning until the
	// human answers.
	if m.Phase == domain.MissionPhaseClarified {
		if _, reopenErr := tx.ExecContext(ctx, `UPDATE missions SET phase=?, updated_at=? WHERE id=? AND phase=?`, domain.MissionPhasePlanning, n, missionID, domain.MissionPhaseClarified); reopenErr != nil {
			return nil, fmt.Errorf("store: reopen clarification for mission %s: %w", missionID, reopenErr)
		}
	}
	if enqueueErr := enqueueMissionControlChange(ctx, tx, missionID); enqueueErr != nil {
		return nil, fmt.Errorf("store: enqueue mission change: %w", enqueueErr)
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return nil, fmt.Errorf("store: ask mission question: commit: %w", commitErr)
	}
	return &domain.MissionQuestion{ID: domain.MissionQuestionID(id), MissionID: missionID, Seq: seq, Body: body, AskedByRunID: askedBy, AskedAt: ts}, nil
}

// AnswerMissionQuestion records the accountable human's answer. The answering
// member is the authenticated caller, resolved by the service layer.
func (d *DB) AnswerMissionQuestion(ctx context.Context, questionID domain.MissionQuestionID, memberID domain.MemberID, answer, key string) (*domain.MissionQuestion, error) {
	if questionID == "" || memberID == "" || strings.TrimSpace(answer) == "" || !validMutationKey(key) {
		return nil, errors.New("store: mission question answer requires question_id, member, a non-empty answer, and idempotency_key")
	}
	payload, err := mutationPayload(struct {
		QuestionID domain.MissionQuestionID
		Answer     string
		MemberID   domain.MemberID
	}{questionID, answer, memberID})
	if err != nil {
		return nil, err
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("store: answer mission question: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	m, err := lockMissionRowBy(ctx, tx, `(SELECT mission_id FROM mission_questions WHERE id = ?)`, questionID, "mission for question "+string(questionID))
	if err != nil {
		return nil, err
	}
	if phaseErr := requireMissionPhase(m, "mission.question.answer", domain.MissionPhasePlanning); phaseErr != nil {
		return nil, phaseErr
	}
	missionID := m.ID
	resultID, _, replayed, err := mutationReceipt(tx, ctx, missionID, "mission.question.answer", key, payload)
	if err != nil {
		return nil, err
	}
	if replayed {
		q, scanErr := scanMissionQuestion(tx.QueryRowContext(ctx, `SELECT `+missionQuestionColumns+` FROM mission_questions WHERE id=?`, resultID))
		if scanErr != nil {
			return nil, fmt.Errorf("store: replay mission question answer %s: %w", resultID, scanErr)
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return nil, commitErr
		}
		return q, nil
	}
	now := missionNow(time.Time{})
	n, err := encodeTime(now)
	if err != nil {
		return nil, err
	}
	res, err := tx.ExecContext(ctx, `UPDATE mission_questions SET answer=?, answered_by_member_id=?, answered_at=? WHERE id=? AND answered_at IS NULL`, answer, memberID, n, questionID)
	if err != nil {
		return nil, fmt.Errorf("store: answer mission question %s: %w", questionID, err)
	}
	if affected, _ := res.RowsAffected(); affected != 1 {
		return nil, fmt.Errorf("%w: mission question %s is already answered", ErrConflict, questionID)
	}
	if receiptErr := recordMutationReceipt(tx, ctx, missionID, "mission.question.answer", key, payload, string(questionID), 0, n); receiptErr != nil {
		return nil, receiptErr
	}
	if enqueueErr := enqueueMissionControlChange(ctx, tx, missionID); enqueueErr != nil {
		return nil, fmt.Errorf("store: enqueue mission change: %w", enqueueErr)
	}
	q, err := scanMissionQuestion(tx.QueryRowContext(ctx, `SELECT `+missionQuestionColumns+` FROM mission_questions WHERE id=?`, questionID))
	if err != nil {
		return nil, fmt.Errorf("store: read answered mission question %s: %w", questionID, err)
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return nil, fmt.Errorf("store: answer mission question: commit: %w", commitErr)
	}
	return q, nil
}

// GetMissionQuestion reads one question by id. The service layer needs the
// question's mission to check the accountable-human rule before answering.
func (d *DB) GetMissionQuestion(ctx context.Context, questionID domain.MissionQuestionID) (*domain.MissionQuestion, error) {
	if questionID == "" {
		return nil, ErrNotFound
	}
	q, err := scanMissionQuestion(d.db.QueryRowContext(ctx,
		`SELECT `+missionQuestionColumns+` FROM mission_questions WHERE id=?`, questionID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: mission question %s", ErrNotFound, questionID)
	}
	if err != nil {
		return nil, fmt.Errorf("store: read mission question %s: %w", questionID, err)
	}
	return q, nil
}

func (d *DB) ListMissionQuestions(ctx context.Context, missionID domain.MissionID) ([]*domain.MissionQuestion, error) {
	if missionID == "" {
		return nil, ErrNotFound
	}
	rows, err := d.db.QueryContext(ctx, `SELECT `+missionQuestionColumns+` FROM mission_questions WHERE mission_id=? ORDER BY seq`, missionID)
	if err != nil {
		return nil, fmt.Errorf("store: list mission questions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]*domain.MissionQuestion, 0, domain.MaxMissionQuestions)
	for rows.Next() {
		q, scanErr := scanMissionQuestion(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("store: list mission questions: %w", scanErr)
		}
		out = append(out, q)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list mission questions: %w", err)
	}
	return out, nil
}

// CompleteMissionClarification is the integrator declaring that it has the
// answers it needs. Questions are optional, so this is the explicit end of
// clarification rather than a side effect of asking one.
func (d *DB) CompleteMissionClarification(ctx context.Context, missionID domain.MissionID, run domain.RunID, key string) (*domain.Mission, error) {
	if missionID == "" || run == "" || !validMutationKey(key) {
		return nil, errors.New("store: mission clarification complete requires mission_id, the integrator run, and idempotency_key")
	}
	payload, err := mutationPayload(struct{}{})
	if err != nil {
		return nil, err
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("store: complete mission clarification: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	m, err := lockMissionRow(ctx, tx, missionID)
	if err != nil {
		return nil, err
	}
	_, _, replayed, err := mutationReceipt(tx, ctx, missionID, "mission.clarification.complete", key, payload)
	if err != nil {
		return nil, err
	}
	if replayed {
		// The same key can only mean the round it was first used on. Once the
		// mission moved on, replaying it would silently skip the next round's
		// clarification.
		if m.Phase != domain.MissionPhaseClarified {
			return nil, fmt.Errorf("%w: clarification was already completed for an earlier round; complete the next round with a new idempotency key", ErrMissionIdempotencyConflict)
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return nil, commitErr
		}
		return m, nil
	}
	if phaseErr := requireMissionPhase(m, "mission.clarification.complete", domain.MissionPhasePlanning); phaseErr != nil {
		return nil, phaseErr
	}
	unanswered, err := unansweredQuestions(ctx, tx, missionID)
	if err != nil {
		return nil, err
	}
	if unanswered > 0 {
		return nil, fmt.Errorf("%w: %d questions are unanswered; wait for answers, then complete clarification", ErrMissionPhase, unanswered)
	}
	now := missionNow(time.Time{})
	n, err := encodeTime(now)
	if err != nil {
		return nil, err
	}
	if _, updateErr := tx.ExecContext(ctx, `UPDATE missions SET phase=?, updated_at=? WHERE id=? AND phase=?`, domain.MissionPhaseClarified, n, missionID, domain.MissionPhasePlanning); updateErr != nil {
		return nil, fmt.Errorf("store: move mission %s to clarified: %w", missionID, updateErr)
	}
	if receiptErr := recordMutationReceipt(tx, ctx, missionID, "mission.clarification.complete", key, payload, string(missionID), 0, n); receiptErr != nil {
		return nil, receiptErr
	}
	if enqueueErr := enqueueMissionControlChange(ctx, tx, missionID); enqueueErr != nil {
		return nil, fmt.Errorf("store: enqueue mission change: %w", enqueueErr)
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return nil, fmt.Errorf("store: complete mission clarification: commit: %w", commitErr)
	}
	m.Phase, m.UpdatedAt = domain.MissionPhaseClarified, now
	return m, nil
}

func unansweredQuestions(ctx context.Context, tx *sql.Tx, missionID domain.MissionID) (int, error) {
	var unanswered int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mission_questions WHERE mission_id=? AND answered_at IS NULL`, missionID).Scan(&unanswered); err != nil {
		return 0, fmt.Errorf("store: count unanswered mission questions: %w", err)
	}
	return unanswered, nil
}

// pendingPlanRevisionsQuery selects one revision per non-abandoned task: the
// highest-numbered proposed revision at or above the task's current revision.
// A new task's current revision is itself proposed, so the same query covers
// new work and a change to approved work. It is the mission's pending set:
// mission.plan.submit requires it non-empty and records it as the round's
// items, and approval accepts exactly those items.
func pendingPlanRevisionsQuery(columns string) string {
	return `SELECT ` + columns + ` FROM mission_tasks t
		JOIN mission_task_revisions r ON r.task_id=t.id
		WHERE t.mission_id=? AND t.abandoned_at IS NULL AND r.status='proposed'
			AND r.revision >= t.current_revision
			AND r.revision = (SELECT MAX(p.revision) FROM mission_task_revisions p
				WHERE p.task_id=t.id AND p.status='proposed' AND p.revision >= t.current_revision)`
}

// planItem is one row of the pending set as submit reads it, before the
// widening computation turns it into a mission_plan_items row.
type planItem struct {
	taskID   domain.TaskID
	revision int
	material bool
	newTask  bool
	scope    domain.TaskScope
	approved domain.TaskScope
}

// readPendingPlanRevisions returns the mission's pending set with everything
// the round needs to record: whether the task is new, and the scope of both
// the proposed revision and the approved revision it would replace.
func readPendingPlanRevisions(ctx context.Context, tx *sql.Tx, missionID domain.MissionID) ([]planItem, error) {
	rows, err := tx.QueryContext(ctx, pendingPlanRevisionsQuery(`t.id, r.revision, r.material,
		(SELECT COUNT(*) FROM mission_task_revisions a WHERE a.task_id=t.id AND a.accepted_at IS NOT NULL),
		r.scope,
		COALESCE((SELECT c.scope FROM mission_task_revisions c WHERE c.task_id=t.id AND c.revision=t.current_revision AND c.status='accepted'), '{}')`)+` ORDER BY t.id`, missionID)
	if err != nil {
		return nil, fmt.Errorf("store: read pending mission plan revisions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []planItem
	for rows.Next() {
		var item planItem
		var material, approvedRevisions int
		var scope, approved string
		if scanErr := rows.Scan(&item.taskID, &item.revision, &material, &approvedRevisions, &scope, &approved); scanErr != nil {
			return nil, fmt.Errorf("store: read pending mission plan revisions: %w", scanErr)
		}
		item.material, item.newTask = material != 0, approvedRevisions == 0
		if unmarshalErr := json.Unmarshal([]byte(scope), &item.scope); unmarshalErr != nil {
			return nil, fmt.Errorf("store: decode task %s revision %d scope: %w", item.taskID, item.revision, unmarshalErr)
		}
		if unmarshalErr := json.Unmarshal([]byte(approved), &item.approved); unmarshalErr != nil {
			return nil, fmt.Errorf("store: decode task %s approved scope: %w", item.taskID, unmarshalErr)
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read pending mission plan revisions: %w", err)
	}
	return out, nil
}

// SubmitMissionPlan freezes the pending set as plan version N+1 and hands the
// mission to a human. From clarified it is the initial plan; from active it is
// an amendment to a plan the human already approved, and approved work keeps
// running while the human decides.
func (d *DB) SubmitMissionPlan(ctx context.Context, missionID domain.MissionID, submittedBy domain.RunID, summary, key string) (*domain.MissionPlanReview, error) {
	if missionID == "" || submittedBy == "" || strings.TrimSpace(summary) == "" || !validMutationKey(key) {
		return nil, errors.New("store: mission plan submit requires mission_id, submitting run, summary, and idempotency_key")
	}
	payload, err := mutationPayload(struct{ Summary string }{summary})
	if err != nil {
		return nil, err
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("store: submit mission plan: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	m, err := lockMissionRow(ctx, tx, missionID)
	if err != nil {
		return nil, err
	}
	_, replayedVersion, replayed, err := mutationReceipt(tx, ctx, missionID, "mission.plan.submit", key, payload)
	if err != nil {
		return nil, err
	}
	if replayed {
		review, scanErr := scanMissionPlanReview(tx.QueryRowContext(ctx, `SELECT `+missionPlanReviewColumns+` FROM mission_plan_reviews WHERE mission_id=? AND plan_version=?`, missionID, replayedVersion))
		if scanErr != nil {
			return nil, fmt.Errorf("store: replay mission plan submit: %w", scanErr)
		}
		if review.Decision != "" {
			return nil, fmt.Errorf("%w: plan version %d was already decided; submit the next round with a new idempotency key", ErrMissionIdempotencyConflict, review.PlanVersion)
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return nil, commitErr
		}
		return review, nil
	}
	if phaseErr := requireMissionPhase(m, "mission.plan.submit", domain.MissionPhaseClarified, domain.MissionPhaseActive); phaseErr != nil {
		return nil, phaseErr
	}
	// Defence in depth: mission.question.ask returns a clarified mission to
	// planning, so an unanswered question cannot reach this point.
	unanswered, err := unansweredQuestions(ctx, tx, missionID)
	if err != nil {
		return nil, err
	}
	if unanswered > 0 {
		return nil, fmt.Errorf("%w: %d questions are unanswered; wait for answers before submitting", ErrMissionPhase, unanswered)
	}
	pending, err := readPendingPlanRevisions(ctx, tx, missionID)
	if err != nil {
		return nil, err
	}
	if len(pending) == 0 {
		return nil, fmt.Errorf("%w: propose at least one task or revision before submitting", ErrMissionPhase)
	}
	amendment := m.Phase == domain.MissionPhaseActive
	var approvedUnion []string
	if amendment {
		if approvedUnion, err = approvedScopeUnion(ctx, tx, missionID); err != nil {
			return nil, err
		}
	}
	version := m.PlanVersion + 1
	now := missionNow(time.Time{})
	n, err := encodeTime(now)
	if err != nil {
		return nil, err
	}
	if _, insertErr := tx.ExecContext(ctx, `INSERT INTO mission_plan_reviews (mission_id,plan_version,summary,submitted_by_run_id,submitted_at,submitted_phase) VALUES (?,?,?,?,?,?)`, missionID, version, summary, submittedBy, n, m.Phase); insertErr != nil {
		return nil, fmt.Errorf("store: insert mission plan review: %w", mapConstraint(insertErr, ErrConflict))
	}
	for _, item := range pending {
		// An initial plan widens nothing: there is no approved scope yet.
		widening := []string{}
		if amendment {
			widening = append(widening, widenedPaths(approvedUnion, item.scope.ExpectedPaths)...)
			widening = append(widening, droppedExclusions(item.approved.Exclusions, item.scope.Exclusions)...)
		}
		encoded, encodeErr := missionJSON(widening, "[]")
		if encodeErr != nil {
			return nil, encodeErr
		}
		if _, itemErr := tx.ExecContext(ctx, `INSERT INTO mission_plan_items (mission_id,plan_version,task_id,revision,new_task,material,widening) VALUES (?,?,?,?,?,?,?)`, missionID, version, item.taskID, item.revision, item.newTask, item.material, encoded); itemErr != nil {
			return nil, fmt.Errorf("store: insert mission plan item for task %s: %w", item.taskID, mapConstraint(itemErr, ErrConflict))
		}
	}
	next := domain.MissionPhasePlanReview
	if amendment {
		next = domain.MissionPhaseAmendmentReview
	}
	if _, updateErr := tx.ExecContext(ctx, `UPDATE missions SET phase=?, plan_version=?, updated_at=? WHERE id=? AND phase=?`, next, version, n, missionID, m.Phase); updateErr != nil {
		return nil, fmt.Errorf("store: move mission %s to %s: %w", missionID, next, updateErr)
	}
	if receiptErr := recordMutationReceipt(tx, ctx, missionID, "mission.plan.submit", key, payload, string(missionID), int(version), n); receiptErr != nil {
		return nil, receiptErr
	}
	if enqueueErr := enqueueMissionControlChange(ctx, tx, missionID); enqueueErr != nil {
		return nil, fmt.Errorf("store: enqueue mission change: %w", enqueueErr)
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return nil, fmt.Errorf("store: submit mission plan: commit: %w", commitErr)
	}
	return &domain.MissionPlanReview{MissionID: missionID, PlanVersion: version, Summary: summary, SubmittedByRunID: submittedBy, SubmittedAt: now, SubmittedPhase: m.Phase}, nil
}

// DecideMissionPlan records the human verdict on one plan version and moves
// the mission to the phase that verdict implies. Approval accepts the round's
// task revisions inline: AcceptTaskRevision opens its own transaction and
// would block on the SQLite write lock this one holds.
func (d *DB) DecideMissionPlan(ctx context.Context, missionID domain.MissionID, expectedPlanVersion uint64, decision domain.MissionPlanDecision, feedback string, decidedBy domain.MemberID, key string) (*domain.Mission, error) {
	if missionID == "" || decidedBy == "" || expectedPlanVersion == 0 || !validMutationKey(key) {
		return nil, errors.New("store: mission plan decide requires mission_id, plan version, deciding member, and idempotency_key")
	}
	if !decision.Valid() {
		return nil, fmt.Errorf("store: mission plan decision %q is not approve, revise, or reject", decision)
	}
	if decision == domain.MissionPlanRevise && strings.TrimSpace(feedback) == "" {
		return nil, errors.New("store: mission plan revise requires feedback")
	}
	payload, err := mutationPayload(struct {
		PlanVersion uint64
		Decision    domain.MissionPlanDecision
		Feedback    string
		DecidedBy   domain.MemberID
	}{expectedPlanVersion, decision, feedback, decidedBy})
	if err != nil {
		return nil, err
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("store: decide mission plan: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	m, err := lockMissionRow(ctx, tx, missionID)
	if err != nil {
		return nil, err
	}
	_, _, replayed, err := mutationReceipt(tx, ctx, missionID, "mission.plan.decide", key, payload)
	if err != nil {
		return nil, err
	}
	if replayed {
		if commitErr := tx.Commit(); commitErr != nil {
			return nil, commitErr
		}
		return m, nil
	}
	// The phase check is not replaced by the version check: a mission that
	// went back to planning still carries the plan version it was reviewed at.
	if phaseErr := requireMissionPhase(m, "mission.plan.decide", domain.MissionPhasePlanReview, domain.MissionPhaseAmendmentReview); phaseErr != nil {
		return nil, phaseErr
	}
	next, ok := domain.MissionPhaseAfterDecision(m.Phase, decision)
	if !ok {
		return nil, fmt.Errorf("%w: an amendment is approved or sent back for changes; abandon its tasks or revisions to drop it", ErrMissionPhase)
	}
	if m.PlanVersion != expectedPlanVersion {
		return nil, fmt.Errorf("%w: expected plan version %d, current %d", ErrConflict, expectedPlanVersion, m.PlanVersion)
	}
	var openDecision string
	if reviewErr := tx.QueryRowContext(ctx, `SELECT decision FROM mission_plan_reviews WHERE mission_id=? AND plan_version=?`, missionID, expectedPlanVersion).Scan(&openDecision); errors.Is(reviewErr, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: mission %s has no plan version %d to decide", ErrConflict, missionID, expectedPlanVersion)
	} else if reviewErr != nil {
		return nil, fmt.Errorf("store: read mission plan review: %w", reviewErr)
	}
	if openDecision != "" {
		return nil, fmt.Errorf("%w: plan version %d was already decided %s", ErrConflict, expectedPlanVersion, openDecision)
	}
	now := missionNow(time.Time{})
	n, err := encodeTime(now)
	if err != nil {
		return nil, err
	}
	res, err := tx.ExecContext(ctx, `UPDATE mission_plan_reviews SET decision=?, feedback=?, decided_by_member_id=?, decided_at=? WHERE mission_id=? AND plan_version=? AND decision=''`, decision, feedback, decidedBy, n, missionID, expectedPlanVersion)
	if err != nil {
		return nil, fmt.Errorf("store: decide mission plan review: %w", err)
	}
	if affected, _ := res.RowsAffected(); affected != 1 {
		return nil, fmt.Errorf("%w: plan version %d was decided concurrently", ErrConflict, expectedPlanVersion)
	}
	bumped := 0
	if decision == domain.MissionPlanApprove {
		if bumped, err = acceptPlanTaskRevisions(ctx, tx, missionID, expectedPlanVersion, decidedBy, n); err != nil {
			return nil, err
		}
	}
	phaseRes, err := tx.ExecContext(ctx, `UPDATE missions SET phase=?, updated_at=? WHERE id=? AND phase=?`, next, n, missionID, m.Phase)
	if err != nil {
		return nil, fmt.Errorf("store: move mission %s to %s: %w", missionID, next, err)
	}
	if affected, _ := phaseRes.RowsAffected(); affected != 1 {
		return nil, fmt.Errorf("%w: mission %s left %s concurrently", ErrConflict, missionID, m.Phase)
	}
	if receiptErr := recordMutationReceipt(tx, ctx, missionID, "mission.plan.decide", key, payload, string(missionID), int(expectedPlanVersion), n); receiptErr != nil {
		return nil, receiptErr
	}
	if enqueueErr := enqueueMissionControlChange(ctx, tx, missionID); enqueueErr != nil {
		return nil, fmt.Errorf("store: enqueue mission change: %w", enqueueErr)
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return nil, fmt.Errorf("store: decide mission plan: commit: %w", commitErr)
	}
	m.Phase, m.UpdatedAt = next, now
	m.AcceptedSetVersion += uint64(bumped)
	return m, nil
}

// acceptPlanTaskRevisions accepts exactly the revisions this round recorded as
// its items, and reports how many times accepted_set_version was bumped: once
// per task whose superseded revision held an acceptance, because that output
// leaves the current accepted set.
func acceptPlanTaskRevisions(ctx context.Context, tx *sql.Tx, missionID domain.MissionID, version uint64, decidedBy domain.MemberID, n int64) (int, error) {
	rows, err := tx.QueryContext(ctx, `SELECT task_id, revision FROM mission_plan_items WHERE mission_id=? AND plan_version=? ORDER BY task_id`, missionID, version)
	if err != nil {
		return 0, fmt.Errorf("store: read mission plan items: %w", err)
	}
	type item struct {
		id       domain.TaskID
		revision int
	}
	var items []item
	for rows.Next() {
		var it item
		if scanErr := rows.Scan(&it.id, &it.revision); scanErr != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("store: read mission plan items: %w", scanErr)
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, fmt.Errorf("store: read mission plan items: %w", err)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("store: read mission plan items: %w", err)
	}
	if len(items) == 0 {
		return 0, fmt.Errorf("%w: the plan has no proposed task to accept; request changes to return the mission to planning", ErrMissionPhase)
	}
	bumped := 0
	for _, it := range items {
		var previous int
		if currentErr := tx.QueryRowContext(ctx, `SELECT current_revision FROM mission_tasks WHERE id=?`, it.id).Scan(&previous); currentErr != nil {
			return 0, fmt.Errorf("store: read task %s current revision: %w", it.id, currentErr)
		}
		var previousOutput int
		if previous != it.revision {
			outputErr := tx.QueryRowContext(ctx, `SELECT 1 FROM mission_acceptances WHERE mission_id=? AND task_id=? AND task_revision=?`, missionID, it.id, previous).Scan(&previousOutput)
			if outputErr != nil && !errors.Is(outputErr, sql.ErrNoRows) {
				return 0, fmt.Errorf("store: read acceptance of task %s revision %d: %w", it.id, previous, outputErr)
			}
		}
		res, execErr := tx.ExecContext(ctx, `UPDATE mission_task_revisions SET status='accepted', accepted_at=?, accepted_by_member_id=? WHERE task_id=? AND revision=? AND status='proposed'`, n, decidedBy, it.id, it.revision)
		if execErr != nil {
			return 0, fmt.Errorf("store: accept task %s revision %d: %w", it.id, it.revision, execErr)
		}
		if affected, _ := res.RowsAffected(); affected != 1 {
			return 0, fmt.Errorf("%w: task %s revision %d changed while the plan was approved", ErrConflict, it.id, it.revision)
		}
		if previous != it.revision {
			if _, supersedeErr := tx.ExecContext(ctx, `UPDATE mission_task_revisions SET status='superseded' WHERE task_id=? AND revision=? AND status='accepted'`, it.id, previous); supersedeErr != nil {
				return 0, fmt.Errorf("store: supersede task %s revision %d: %w", it.id, previous, supersedeErr)
			}
		}
		if _, supersedeErr := tx.ExecContext(ctx, `UPDATE mission_task_revisions SET status='superseded' WHERE task_id=? AND revision<? AND status='proposed'`, it.id, it.revision); supersedeErr != nil {
			return 0, fmt.Errorf("store: supersede earlier proposals of task %s: %w", it.id, supersedeErr)
		}
		if _, updateErr := tx.ExecContext(ctx, `UPDATE mission_tasks SET current_revision=?, updated_at=? WHERE id=?`, it.revision, n, it.id); updateErr != nil {
			return 0, fmt.Errorf("store: advance task %s to revision %d: %w", it.id, it.revision, updateErr)
		}
		if previousOutput == 1 {
			if _, versionErr := tx.ExecContext(ctx, `UPDATE missions SET accepted_set_version=accepted_set_version+1, updated_at=? WHERE id=?`, n, missionID); versionErr != nil {
				return 0, fmt.Errorf("store: advance accepted set version: %w", versionErr)
			}
			bumped++
		}
	}
	return bumped, nil
}

// ListMissionPlanReviews returns every round oldest first, each carrying the
// items it recorded: what was proposed, by which revision, and what widened
// the approved scope at the moment it was submitted.
func (d *DB) ListMissionPlanReviews(ctx context.Context, missionID domain.MissionID) ([]*domain.MissionPlanReview, error) {
	if missionID == "" {
		return nil, ErrNotFound
	}
	rows, err := d.db.QueryContext(ctx, `SELECT `+missionPlanReviewColumns+` FROM mission_plan_reviews WHERE mission_id=? ORDER BY plan_version`, missionID)
	if err != nil {
		return nil, fmt.Errorf("store: list mission plan reviews: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []*domain.MissionPlanReview
	byVersion := make(map[uint64]*domain.MissionPlanReview)
	for rows.Next() {
		r, scanErr := scanMissionPlanReview(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("store: list mission plan reviews: %w", scanErr)
		}
		out = append(out, r)
		byVersion[r.PlanVersion] = r
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, fmt.Errorf("store: list mission plan reviews: %w", rowsErr)
	}
	if len(out) == 0 {
		return out, nil
	}
	items, err := d.db.QueryContext(ctx, `SELECT i.plan_version, i.task_id, i.revision, i.new_task, i.material, i.widening, r.title, r.supersedes_revision
		FROM mission_plan_items i
		JOIN mission_task_revisions r ON r.task_id=i.task_id AND r.revision=i.revision
		WHERE i.mission_id=? ORDER BY i.plan_version, i.task_id`, missionID)
	if err != nil {
		return nil, fmt.Errorf("store: list mission plan items: %w", err)
	}
	defer func() { _ = items.Close() }()
	for items.Next() {
		var item domain.MissionPlanItem
		var newTask, material int
		var widening string
		if scanErr := items.Scan(&item.PlanVersion, &item.TaskID, &item.Revision, &newTask, &material, &widening, &item.Title, &item.SupersedesRevision); scanErr != nil {
			return nil, fmt.Errorf("store: list mission plan items: %w", scanErr)
		}
		item.NewTask, item.Material = newTask != 0, material != 0
		if unmarshalErr := json.Unmarshal([]byte(widening), &item.Widening); unmarshalErr != nil {
			return nil, fmt.Errorf("store: decode plan item widening for task %s: %w", item.TaskID, unmarshalErr)
		}
		if review, ok := byVersion[item.PlanVersion]; ok {
			review.Items = append(review.Items, item)
		}
	}
	if itemsErr := items.Err(); itemsErr != nil {
		return nil, fmt.Errorf("store: list mission plan items: %w", itemsErr)
	}
	return out, nil
}
