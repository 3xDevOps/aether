package store

import (
	"context"
	"database/sql"
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

const missionPlanReviewColumns = `mission_id, plan_version, summary, submitted_by_run_id, submitted_at, decision, feedback, decided_by_member_id, decided_at`

func scanMissionPlanReview(row interface{ Scan(...any) error }) (*domain.MissionPlanReview, error) {
	var r domain.MissionPlanReview
	var decision string
	var submitted int64
	var decided *int64
	if err := row.Scan(&r.MissionID, &r.PlanVersion, &r.Summary, &r.SubmittedByRunID, &submitted, &decision, &r.Feedback, &r.DecidedByMemberID, &decided); err != nil {
		return nil, err
	}
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
	if phaseErr := requireMissionPhase(m, "mission.question.ask", domain.MissionPhasePlanning); phaseErr != nil {
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

// SubmitMissionPlan freezes the proposed task set as plan version N+1 and
// hands the mission to a human. It refuses until the integrator has asked at
// least one question, every question is answered, and at least one task is
// proposed.
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
	if phaseErr := requireMissionPhase(m, "mission.plan.submit", domain.MissionPhasePlanning); phaseErr != nil {
		return nil, phaseErr
	}
	var questions, unanswered int
	if countErr := tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(answered_at IS NULL),0) FROM mission_questions WHERE mission_id=?`, missionID).Scan(&questions, &unanswered); countErr != nil {
		return nil, fmt.Errorf("store: count mission questions: %w", countErr)
	}
	if questions == 0 {
		return nil, fmt.Errorf("%w: ask at least one question before submitting a plan; the human has not been consulted", ErrMissionPhase)
	}
	if unanswered > 0 {
		return nil, fmt.Errorf("%w: %d questions are unanswered; wait for answers before submitting", ErrMissionPhase, unanswered)
	}
	var proposed int
	if countErr := tx.QueryRowContext(ctx, proposedPlanTaskQuery(`COUNT(*)`), missionID).Scan(&proposed); countErr != nil {
		return nil, fmt.Errorf("store: count proposed mission tasks: %w", countErr)
	}
	if proposed == 0 {
		return nil, fmt.Errorf("%w: propose at least one task before submitting a plan", ErrMissionPhase)
	}
	version := m.PlanVersion + 1
	now := missionNow(time.Time{})
	n, err := encodeTime(now)
	if err != nil {
		return nil, err
	}
	if _, insertErr := tx.ExecContext(ctx, `INSERT INTO mission_plan_reviews (mission_id,plan_version,summary,submitted_by_run_id,submitted_at) VALUES (?,?,?,?,?)`, missionID, version, summary, submittedBy, n); insertErr != nil {
		return nil, fmt.Errorf("store: insert mission plan review: %w", mapConstraint(insertErr, ErrConflict))
	}
	if _, updateErr := tx.ExecContext(ctx, `UPDATE missions SET phase=?, plan_version=?, updated_at=? WHERE id=?`, domain.MissionPhasePlanReview, version, n, missionID); updateErr != nil {
		return nil, fmt.Errorf("store: move mission %s to plan_review: %w", missionID, updateErr)
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
	return &domain.MissionPlanReview{MissionID: missionID, PlanVersion: version, Summary: summary, SubmittedByRunID: submittedBy, SubmittedAt: now}, nil
}

// proposedPlanTaskQuery selects the non-abandoned tasks whose current revision
// is still proposed: exactly the set a plan submission offers and an approval
// accepts.
func proposedPlanTaskQuery(columns string) string {
	return `SELECT ` + columns + ` FROM mission_tasks t
		JOIN mission_task_revisions r ON r.task_id=t.id AND r.revision=t.current_revision
		WHERE t.mission_id=? AND t.abandoned_at IS NULL AND r.status='proposed'`
}

// DecideMissionPlan records the human verdict on one plan version and moves
// the mission to the phase that verdict implies. Approval accepts the plan's
// task revisions inline: AcceptTaskRevision opens its own transaction and
// would block on the SQLite write lock this one holds.
func (d *DB) DecideMissionPlan(ctx context.Context, missionID domain.MissionID, expectedPlanVersion uint64, decision domain.MissionPlanDecision, feedback string, decidedBy domain.MemberID, key string) (*domain.Mission, error) {
	if missionID == "" || decidedBy == "" || expectedPlanVersion == 0 || !validMutationKey(key) {
		return nil, errors.New("store: mission plan decide requires mission_id, plan version, deciding member, and idempotency_key")
	}
	next, ok := domain.MissionPhaseAfterDecision(decision)
	if !ok {
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
	if phaseErr := requireMissionPhase(m, "mission.plan.decide", domain.MissionPhasePlanReview); phaseErr != nil {
		return nil, phaseErr
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
	if decision == domain.MissionPlanApprove {
		if acceptErr := acceptPlanTaskRevisions(ctx, tx, missionID, n); acceptErr != nil {
			return nil, acceptErr
		}
	}
	phaseRes, err := tx.ExecContext(ctx, `UPDATE missions SET phase=?, updated_at=? WHERE id=? AND phase='plan_review'`, next, n, missionID)
	if err != nil {
		return nil, fmt.Errorf("store: move mission %s to %s: %w", missionID, next, err)
	}
	if affected, _ := phaseRes.RowsAffected(); affected != 1 {
		return nil, fmt.Errorf("%w: mission %s left plan_review concurrently", ErrConflict, missionID)
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
	return m, nil
}

// acceptPlanTaskRevisions accepts every proposed current revision of the plan.
// accepted_set_version is untouched: nothing in planning can have produced an
// accepted output, so the current accepted set does not change here.
func acceptPlanTaskRevisions(ctx context.Context, tx *sql.Tx, missionID domain.MissionID, n int64) error {
	rows, err := tx.QueryContext(ctx, proposedPlanTaskQuery(`t.id, t.current_revision`), missionID)
	if err != nil {
		return fmt.Errorf("store: read proposed mission tasks: %w", err)
	}
	type planTask struct {
		id       domain.TaskID
		revision int
	}
	var tasks []planTask
	for rows.Next() {
		var t planTask
		if scanErr := rows.Scan(&t.id, &t.revision); scanErr != nil {
			_ = rows.Close()
			return fmt.Errorf("store: read proposed mission tasks: %w", scanErr)
		}
		tasks = append(tasks, t)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("store: read proposed mission tasks: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("store: read proposed mission tasks: %w", err)
	}
	if len(tasks) == 0 {
		return fmt.Errorf("%w: the plan has no proposed task to accept; request changes to return the mission to planning", ErrMissionPhase)
	}
	for _, t := range tasks {
		res, execErr := tx.ExecContext(ctx, `UPDATE mission_task_revisions SET status='accepted', accepted_at=? WHERE task_id=? AND revision=? AND status='proposed'`, n, t.id, t.revision)
		if execErr != nil {
			return fmt.Errorf("store: accept task %s revision %d: %w", t.id, t.revision, execErr)
		}
		if affected, _ := res.RowsAffected(); affected != 1 {
			return fmt.Errorf("%w: task %s revision %d changed while the plan was approved", ErrConflict, t.id, t.revision)
		}
		if _, updateErr := tx.ExecContext(ctx, `UPDATE mission_tasks SET updated_at=? WHERE id=?`, n, t.id); updateErr != nil {
			return fmt.Errorf("store: touch task %s: %w", t.id, updateErr)
		}
	}
	return nil
}

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
	for rows.Next() {
		r, scanErr := scanMissionPlanReview(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("store: list mission plan reviews: %w", scanErr)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list mission plan reviews: %w", err)
	}
	return out, nil
}
