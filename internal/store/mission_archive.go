package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// SetMissionArchived archives (at != nil) a completed or cancelled mission,
// or restores (at == nil) an archived one. The reported bool is whether this
// call changed the column.
func (d *DB) SetMissionArchived(ctx context.Context, id domain.MissionID, at *time.Time) (bool, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("store: archive mission: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	m, err := lockMissionRow(ctx, tx, id)
	if err != nil {
		return false, err
	}
	var value sql.NullInt64
	if at != nil {
		if phaseErr := requireMissionPhase(m, "mission.archive", domain.MissionPhaseCompleted, domain.MissionPhaseCancelled); phaseErr != nil {
			return false, phaseErr
		}
		if m.ArchivedAt != nil {
			return false, tx.Commit()
		}
		ts, encErr := encodeTime(*at)
		if encErr != nil {
			return false, fmt.Errorf("store: archive mission: %w", encErr)
		}
		value = sql.NullInt64{Int64: ts, Valid: true}
	} else if m.ArchivedAt == nil {
		return false, tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, `UPDATE missions SET archived_at = ? WHERE id = ?`, value, id); err != nil {
		return false, fmt.Errorf("store: archive mission %s: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("store: archive mission: commit: %w", err)
	}
	return true, nil
}

// DeleteMission removes a cancelled or archived mission with its tasks,
// attempts, submissions, questions and the agent messages stamped with it.
// Its runs stay; the caller deletes them through the scheduler.
func (d *DB) DeleteMission(ctx context.Context, id domain.MissionID) error {
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
	for _, query := range []string{
		`DELETE FROM run_messages WHERE mission_id = ?`,
		// mission_acceptances references mission_submissions without a cascade.
		`DELETE FROM mission_acceptances WHERE mission_id = ?`,
		`DELETE FROM missions WHERE id = ?`,
	} {
		if _, err := tx.ExecContext(ctx, query, id); err != nil {
			return fmt.Errorf("store: delete mission %s: %w", id, mapConstraint(err, ErrInUse))
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: delete mission: commit: %w", err)
	}
	return nil
}
