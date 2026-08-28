package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// EnvironmentStore is the workspace environment definition persistence
// surface. Definitions are versioned per workspace: the store assigns
// versions (max+1) at save time, and at most one version per workspace is
// active at a time - activation demotes the previously active version in
// the same transaction.
type EnvironmentStore interface {
	// SaveEnvironmentDefinition persists d as the workspace's next version,
	// filling in d.Version and the timestamps.
	SaveEnvironmentDefinition(ctx context.Context, d *domain.EnvironmentDefinition) error
	GetEnvironmentDefinition(ctx context.Context, workspace domain.WorkspaceID, version int) (*domain.EnvironmentDefinition, error)
	GetActiveEnvironmentDefinition(ctx context.Context, workspace domain.WorkspaceID) (*domain.EnvironmentDefinition, error)
	// ListEnvironmentDefinitions returns the workspace's versions newest
	// first.
	ListEnvironmentDefinitions(ctx context.Context, workspace domain.WorkspaceID) ([]*domain.EnvironmentDefinition, error)
	// SetEnvironmentStatus transitions one version's lifecycle status. The
	// failure detail is always overwritten (empty clears it). Activating a
	// version demotes the previously active one back to saved atomically,
	// so two versions are never active.
	SetEnvironmentStatus(ctx context.Context, workspace domain.WorkspaceID, version int, status domain.EnvironmentStatus, failureDetail string) error
}

const environmentDefinitionCols = `workspace_id, version, definition, status, failure_detail, created_at, updated_at`

func (d *DB) SaveEnvironmentDefinition(ctx context.Context, def *domain.EnvironmentDefinition) error {
	if err := def.Validate(); err != nil {
		return fmt.Errorf("store: save environment definition: %w", err)
	}
	now := time.Now().UTC()
	stamp, err := encodeTime(now)
	if err != nil {
		return fmt.Errorf("store: save environment definition: %w", err)
	}
	blob, err := json.Marshal(def)
	if err != nil {
		return fmt.Errorf("store: encode environment definition: %w", err)
	}
	// The version subquery and the insert run as one statement, so the
	// assignment is atomic under concurrent saves.
	var version int
	err = d.db.QueryRowContext(ctx,
		`INSERT INTO environment_definitions (`+environmentDefinitionCols+`)
		 VALUES (?, (SELECT COALESCE(MAX(version), 0) + 1
		             FROM environment_definitions WHERE workspace_id = ?),
		         ?, ?, ?, ?, ?)
		 RETURNING version`,
		def.WorkspaceID, def.WorkspaceID, string(blob), def.Status, def.FailureDetail, stamp, stamp,
	).Scan(&version)
	if err != nil {
		return fmt.Errorf("store: save environment definition: %w", mapConstraint(err, ErrNotFound))
	}
	def.Version, def.CreatedAt, def.UpdatedAt = version, now, now
	return nil
}

func (d *DB) GetEnvironmentDefinition(ctx context.Context, workspace domain.WorkspaceID, version int) (*domain.EnvironmentDefinition, error) {
	def, err := scanEnvironmentDefinition(d.db.QueryRowContext(ctx,
		`SELECT `+environmentDefinitionCols+` FROM environment_definitions
		 WHERE workspace_id = ? AND version = ?`, workspace, version))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get environment definition: %w", err)
	}
	return def, nil
}

func (d *DB) GetActiveEnvironmentDefinition(ctx context.Context, workspace domain.WorkspaceID) (*domain.EnvironmentDefinition, error) {
	def, err := scanEnvironmentDefinition(d.db.QueryRowContext(ctx,
		`SELECT `+environmentDefinitionCols+` FROM environment_definitions
		 WHERE workspace_id = ? AND status = ?`, workspace, domain.EnvironmentActive))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get active environment definition: %w", err)
	}
	return def, nil
}

func (d *DB) ListEnvironmentDefinitions(ctx context.Context, workspace domain.WorkspaceID) ([]*domain.EnvironmentDefinition, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT `+environmentDefinitionCols+` FROM environment_definitions
		 WHERE workspace_id = ? ORDER BY version DESC`, workspace)
	if err != nil {
		return nil, fmt.Errorf("store: list environment definitions: %w", err)
	}
	return collect(rows, scanEnvironmentDefinition)
}

func (d *DB) SetEnvironmentStatus(ctx context.Context, workspace domain.WorkspaceID, version int, status domain.EnvironmentStatus, failureDetail string) error {
	if !status.Valid() {
		return fmt.Errorf("store: set environment status: unknown status %q", status)
	}
	stamp, err := encodeTime(time.Now().UTC())
	if err != nil {
		return fmt.Errorf("store: set environment status: %w", err)
	}

	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: set environment status: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit

	// Demote before activating so the unique active index never sees two
	// active rows; the rollback on any later error undoes the demotion.
	if status == domain.EnvironmentActive {
		if _, err = tx.ExecContext(ctx,
			`UPDATE environment_definitions
			 SET status = ?, failure_detail = '', updated_at = ?
			 WHERE workspace_id = ? AND status = ? AND version <> ?`,
			domain.EnvironmentSaved, stamp, workspace, domain.EnvironmentActive, version,
		); err != nil {
			return fmt.Errorf("store: demote active environment definition: %w", err)
		}
	}
	err = notFoundOnZeroRows(tx.ExecContext(ctx,
		`UPDATE environment_definitions
		 SET status = ?, failure_detail = ?, updated_at = ?
		 WHERE workspace_id = ? AND version = ?`,
		status, failureDetail, stamp, workspace, version))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return err
		}
		return fmt.Errorf("store: set environment status: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: set environment status: commit: %w", err)
	}
	return nil
}

// scanEnvironmentDefinition decodes one row. The dedicated columns are
// authoritative for the fields they mirror and overwrite whatever the JSON
// blob carried at save time.
func scanEnvironmentDefinition(row interface{ Scan(...any) error }) (*domain.EnvironmentDefinition, error) {
	var (
		def                  domain.EnvironmentDefinition
		blob                 string
		createdAt, updatedAt int64
	)
	if err := row.Scan(&def.WorkspaceID, &def.Version, &blob, &def.Status,
		&def.FailureDetail, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	workspace, version := def.WorkspaceID, def.Version
	status, detail := def.Status, def.FailureDetail
	if err := json.Unmarshal([]byte(blob), &def); err != nil {
		return nil, fmt.Errorf("store: decode environment definition: %w", err)
	}
	def.WorkspaceID, def.Version = workspace, version
	def.Status, def.FailureDetail = status, detail
	def.CreatedAt, def.UpdatedAt = decodeTime(createdAt), decodeTime(updatedAt)
	return &def, nil
}
