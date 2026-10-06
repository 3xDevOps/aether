package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// SetMissionArchived archives (at != nil) or restores the mission and the
// given runs in one transaction, so the swarm and its runs never disagree.
// Archiving refuses a run that is not Final; a run that no longer exists is
// skipped. It reports whether the mission changed and which runs did.
func (d *DB) SetMissionArchived(ctx context.Context, id domain.MissionID, runs []domain.RunID, at *time.Time) (bool, []domain.RunID, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return false, nil, fmt.Errorf("store: archive mission: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	m, err := lockMissionRow(ctx, tx, id)
	if err != nil {
		return false, nil, err
	}
	var value sql.NullInt64
	if at != nil {
		if phaseErr := requireMissionPhase(m, "mission.archive", domain.MissionPhaseCompleted, domain.MissionPhaseCancelled); phaseErr != nil {
			return false, nil, phaseErr
		}
		ts, encErr := encodeTime(*at)
		if encErr != nil {
			return false, nil, fmt.Errorf("store: archive mission: %w", encErr)
		}
		value = sql.NullInt64{Int64: ts, Valid: true}
	}
	var changedRuns []domain.RunID
	for _, run := range runs {
		var status domain.RunStatus
		var archivedAt sql.NullInt64
		err := tx.QueryRowContext(ctx, `SELECT status, archived_at FROM runs WHERE id = ?`, run).Scan(&status, &archivedAt)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return false, nil, fmt.Errorf("store: archive mission %s: read run %s: %w", id, run, err)
		}
		if at != nil && !status.Final() {
			return false, nil, fmt.Errorf("%w: mission.archive: run %s is %s; only merged, abandoned, failed or interrupted runs can be archived", ErrMissionPhase, run, status)
		}
		if archivedAt.Valid == value.Valid {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE runs SET archived_at = ? WHERE id = ?`, value, run); err != nil {
			return false, nil, fmt.Errorf("store: archive mission %s: run %s: %w", id, run, err)
		}
		changedRuns = append(changedRuns, run)
	}
	missionChanged := (m.ArchivedAt != nil) != value.Valid
	if missionChanged {
		if _, err := tx.ExecContext(ctx, `UPDATE missions SET archived_at = ? WHERE id = ?`, value, id); err != nil {
			return false, nil, fmt.Errorf("store: archive mission %s: %w", id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return false, nil, fmt.Errorf("store: archive mission: commit: %w", err)
	}
	return missionChanged, changedRuns, nil
}

// DeleteMissionSubmissions removes the swarm's submissions, the only
// records that keep its runs from being deleted. The swarm row stays, so
// ListMissionRunIDs still finds every run.
func (d *DB) DeleteMissionSubmissions(ctx context.Context, id domain.MissionID) error {
	return d.deleteMissionRecords(ctx, id,
		// mission_acceptances references mission_submissions without a cascade.
		`DELETE FROM mission_acceptances WHERE mission_id = ?`,
		`DELETE FROM mission_submissions WHERE mission_id = ?`,
	)
}

// DeleteMission leaves the mission's runs; the caller deletes them through
// the scheduler.
func (d *DB) DeleteMission(ctx context.Context, id domain.MissionID) error {
	return d.deleteMissionRecords(ctx, id,
		`DELETE FROM run_messages WHERE mission_id = ?`,
		`DELETE FROM mission_acceptances WHERE mission_id = ?`,
		`DELETE FROM missions WHERE id = ?`,
	)
}

func (d *DB) deleteMissionRecords(ctx context.Context, id domain.MissionID, queries ...string) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: delete mission: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	m, err := lockMissionRow(ctx, tx, id)
	if err != nil {
		return err
	}
	switch {
	case m.Phase == domain.MissionPhasePlanning || m.Phase == domain.MissionPhaseActive:
		return fmt.Errorf("%w: mission.delete: swarm is %s; cancel it first", ErrMissionPhase, m.Phase)
	case m.Phase == domain.MissionPhaseCompleted && m.ArchivedAt == nil:
		return fmt.Errorf("%w: mission.delete: swarm is completed; archive it first", ErrMissionPhase)
	}
	for _, query := range queries {
		if _, err := tx.ExecContext(ctx, query, id); err != nil {
			return fmt.Errorf("store: delete mission %s: %w", id, mapConstraint(err, ErrInUse))
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: delete mission: commit: %w", err)
	}
	return nil
}
