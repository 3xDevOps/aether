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
	id, ts, err := prepareCreate("ques", time.Time{})
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
	if changeErr := recordMissionChange(ctx, tx, missionID, domain.MissionQuestionAsked, askedBy); changeErr != nil {
		return nil, changeErr
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
	if changeErr := recordMissionChange(ctx, tx, missionID, domain.MissionQuestionAnswered, ""); changeErr != nil {
		return nil, changeErr
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

func unansweredQuestions(ctx context.Context, tx *sql.Tx, missionID domain.MissionID) (int, error) {
	var unanswered int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mission_questions WHERE mission_id=? AND answered_at IS NULL`, missionID).Scan(&unanswered); err != nil {
		return 0, fmt.Errorf("store: count unanswered mission questions: %w", err)
	}
	return unanswered, nil
}

// StartMission is the integrator's mission.start: in one transaction it
// accepts every proposed task, recording run as the accepting run, and moves
// the mission from planning to active. While planning, each live task's
// current revision is its latest proposal (see proposeTaskRevision), so those
// revisions are the plan.
func (d *DB) StartMission(ctx context.Context, missionID domain.MissionID, run domain.RunID, key string) (*domain.Mission, error) {
	if missionID == "" || run == "" || !validMutationKey(key) {
		return nil, errors.New("store: mission start requires mission_id, the integrator run, and idempotency_key")
	}
	payload, err := mutationPayload(struct{}{})
	if err != nil {
		return nil, err
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("store: start mission: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	m, err := lockMissionRow(ctx, tx, missionID)
	if err != nil {
		return nil, err
	}
	_, _, replayed, err := mutationReceipt(tx, ctx, missionID, "mission.start", key, payload)
	if err != nil {
		return nil, err
	}
	if replayed {
		if commitErr := tx.Commit(); commitErr != nil {
			return nil, commitErr
		}
		return m, nil
	}
	if phaseErr := requireMissionPhase(m, "mission.start", domain.MissionPhasePlanning); phaseErr != nil {
		return nil, phaseErr
	}
	unanswered, err := unansweredQuestions(ctx, tx, missionID)
	if err != nil {
		return nil, err
	}
	if unanswered > 0 {
		return nil, fmt.Errorf("%w: %d questions are unanswered; wait for answers with mission plan show --wait 30, then start", ErrMissionPhase, unanswered)
	}
	now := missionNow(time.Time{})
	n, err := encodeTime(now)
	if err != nil {
		return nil, err
	}
	res, err := tx.ExecContext(ctx, `UPDATE mission_task_revisions SET status='accepted', accepted_at=?, accepted_by_run_id=?
		WHERE status='proposed' AND (task_id, revision) IN (
			SELECT t.id, t.current_revision FROM mission_tasks t WHERE t.mission_id=? AND t.abandoned_at IS NULL)`, n, run, missionID)
	if err != nil {
		return nil, fmt.Errorf("store: accept proposed tasks of mission %s: %w", missionID, err)
	}
	if accepted, _ := res.RowsAffected(); accepted == 0 {
		return nil, fmt.Errorf("%w: propose at least one task with task propose before starting", ErrMissionPhase)
	}
	if _, updateErr := tx.ExecContext(ctx, `UPDATE missions SET phase=?, updated_at=? WHERE id=? AND phase=?`, domain.MissionPhaseActive, n, missionID, domain.MissionPhasePlanning); updateErr != nil {
		return nil, fmt.Errorf("store: move mission %s to active: %w", missionID, updateErr)
	}
	if receiptErr := recordMutationReceipt(tx, ctx, missionID, "mission.start", key, payload, string(missionID), 0, n); receiptErr != nil {
		return nil, receiptErr
	}
	if enqueueErr := enqueueMissionControlChange(ctx, tx, missionID); enqueueErr != nil {
		return nil, fmt.Errorf("store: enqueue mission change: %w", enqueueErr)
	}
	if changeErr := recordMissionChange(ctx, tx, missionID, domain.MissionPhaseChanged, run); changeErr != nil {
		return nil, changeErr
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return nil, fmt.Errorf("store: start mission: commit: %w", commitErr)
	}
	m.Phase, m.UpdatedAt = domain.MissionPhaseActive, now
	return m, nil
}

// CompleteMission records that run, the mission's current integrator,
// reported success: the mission moves to completed. A report completes its
// mission once, so its replay changes nothing, even after the integrator
// reopened the mission.
func (d *DB) CompleteMission(ctx context.Context, missionID domain.MissionID, run domain.RunID, reportID string) (*domain.Mission, error) {
	if missionID == "" || run == "" || !validMutationKey(reportID) {
		return nil, errors.New("store: mission complete requires mission_id, the integrator run, and its report")
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("store: complete mission: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	m, err := lockMissionRow(ctx, tx, missionID)
	if err != nil {
		return nil, err
	}
	if m.CurrentIntegratorRunID != run {
		return nil, fmt.Errorf("%w: run %s is not the current integrator of mission %s", ErrMissionStale, run, missionID)
	}
	_, _, replayed, err := mutationReceipt(tx, ctx, missionID, "mission.complete", reportID, "")
	if err != nil {
		return nil, err
	}
	if replayed {
		return m, tx.Commit()
	}
	now := missionNow(time.Time{})
	n, err := encodeTime(now)
	if err != nil {
		return nil, err
	}
	if receiptErr := recordMutationReceipt(tx, ctx, missionID, "mission.complete", reportID, "", string(missionID), 0, n); receiptErr != nil {
		return nil, receiptErr
	}
	if m.Phase == domain.MissionPhaseCompleted {
		return m, tx.Commit()
	}
	if phaseErr := requireMissionPhase(m, "mission complete", domain.MissionPhaseActive); phaseErr != nil {
		return nil, phaseErr
	}
	if _, updateErr := tx.ExecContext(ctx, `UPDATE missions SET phase=?, updated_at=? WHERE id=?`, domain.MissionPhaseCompleted, n, missionID); updateErr != nil {
		return nil, fmt.Errorf("store: move mission %s to completed: %w", missionID, updateErr)
	}
	if enqueueErr := enqueueMissionControlChange(ctx, tx, missionID); enqueueErr != nil {
		return nil, fmt.Errorf("store: enqueue mission change: %w", enqueueErr)
	}
	if changeErr := recordMissionChange(ctx, tx, missionID, domain.MissionPhaseChanged, run); changeErr != nil {
		return nil, changeErr
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return nil, fmt.Errorf("store: complete mission: commit: %w", commitErr)
	}
	m.Phase, m.UpdatedAt = domain.MissionPhaseCompleted, now
	return m, nil
}

// reopenCompletedMission moves a completed mission back to active inside the
// caller's transaction when run, its current integrator, takes up new work,
// so a refused call leaves it completed. It waits for the completion's
// teardown: every attempt alive after reopening is then new work.
func reopenCompletedMission(ctx context.Context, tx *sql.Tx, m *domain.Mission, run domain.RunID) error {
	if m.Phase != domain.MissionPhaseCompleted || run != m.CurrentIntegratorRunID {
		return nil
	}
	var leftover domain.RunID
	leftoverErr := tx.QueryRowContext(ctx, `SELECT run_id FROM mission_attempts WHERE mission_id=? AND state IN ('reserved','launching','running','unknown','submitted') LIMIT 1`, m.ID).Scan(&leftover)
	if leftoverErr == nil {
		return fmt.Errorf("%w: mission %s is still stopping worker %s from its completion; retry once aether-internal worker list shows it finished", ErrMissionNotReady, m.ID, leftover)
	}
	if !errors.Is(leftoverErr, sql.ErrNoRows) {
		return leftoverErr
	}
	n, err := encodeTime(missionNow(time.Time{}))
	if err != nil {
		return err
	}
	if _, updateErr := tx.ExecContext(ctx, `UPDATE missions SET phase=?, updated_at=? WHERE id=?`, domain.MissionPhaseActive, n, m.ID); updateErr != nil {
		return fmt.Errorf("store: move mission %s to active: %w", m.ID, updateErr)
	}
	if enqueueErr := enqueueMissionControlChange(ctx, tx, m.ID); enqueueErr != nil {
		return fmt.Errorf("store: enqueue mission change: %w", enqueueErr)
	}
	if changeErr := recordMissionChange(ctx, tx, m.ID, domain.MissionPhaseChanged, run); changeErr != nil {
		return changeErr
	}
	m.Phase = domain.MissionPhaseActive
	return nil
}

// CancelMission ends a planning or active mission by moving it to cancelled;
// the reconcile loop then stops its workers and its integrator. A key names
// one cancellation of one mission.
func (d *DB) CancelMission(ctx context.Context, missionID domain.MissionID, cancelledBy domain.MemberID, key string) (*domain.Mission, error) {
	if missionID == "" || cancelledBy == "" || !validMutationKey(key) {
		return nil, errors.New("store: mission cancel requires mission_id, cancelling member, and idempotency_key")
	}
	payload, err := mutationPayload(struct{ CancelledBy domain.MemberID }{cancelledBy})
	if err != nil {
		return nil, err
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("store: cancel mission: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	m, err := lockMissionRow(ctx, tx, missionID)
	if err != nil {
		return nil, err
	}
	var used int
	if otherErr := tx.QueryRowContext(ctx, `SELECT 1 FROM mission_mutation_receipts WHERE operation=? AND idempotency_key=? AND mission_id<>? LIMIT 1`, "mission.cancel", key, missionID).Scan(&used); otherErr == nil {
		return nil, fmt.Errorf("%w: idempotency_key was already used to cancel another mission", ErrMissionIdempotencyConflict)
	} else if !errors.Is(otherErr, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: cancel mission: read receipts: %w", otherErr)
	}
	_, _, replayed, err := mutationReceipt(tx, ctx, missionID, "mission.cancel", key, payload)
	if err != nil {
		return nil, err
	}
	if replayed {
		if commitErr := tx.Commit(); commitErr != nil {
			return nil, commitErr
		}
		return m, nil
	}
	if phaseErr := requireMissionPhase(m, "mission.cancel", domain.MissionPhasePlanning, domain.MissionPhaseActive); phaseErr != nil {
		return nil, phaseErr
	}
	now := missionNow(time.Time{})
	n, err := encodeTime(now)
	if err != nil {
		return nil, err
	}
	if _, updateErr := tx.ExecContext(ctx, `UPDATE missions SET phase=?, updated_at=? WHERE id=?`, domain.MissionPhaseCancelled, n, missionID); updateErr != nil {
		return nil, fmt.Errorf("store: move mission %s to cancelled: %w", missionID, updateErr)
	}
	if receiptErr := recordMutationReceipt(tx, ctx, missionID, "mission.cancel", key, payload, string(missionID), 0, n); receiptErr != nil {
		return nil, receiptErr
	}
	if enqueueErr := enqueueMissionControlChange(ctx, tx, missionID); enqueueErr != nil {
		return nil, fmt.Errorf("store: enqueue mission change: %w", enqueueErr)
	}
	if changeErr := recordMissionChange(ctx, tx, missionID, domain.MissionPhaseChanged, ""); changeErr != nil {
		return nil, changeErr
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return nil, fmt.Errorf("store: cancel mission: commit: %w", commitErr)
	}
	m.Phase, m.UpdatedAt = domain.MissionPhaseCancelled, now
	return m, nil
}
