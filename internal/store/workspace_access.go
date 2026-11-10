package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// WorkspaceAccessStore holds workspace owners and grants.
type WorkspaceAccessStore interface {
	// MemberWorkspaces returns the workspaces member owns and the ones
	// they were granted.
	MemberWorkspaces(ctx context.Context, member domain.MemberID) (domain.WorkspaceAccess, error)
	ListWorkspaceGrants(ctx context.Context, ws domain.WorkspaceID) ([]*domain.WorkspaceGrant, error)
	// GrantWorkspace lets member into ws; granting an existing grant is a
	// no-op. ErrNotFound when either does not exist.
	GrantWorkspace(ctx context.Context, ws domain.WorkspaceID, member, by domain.MemberID) error
	// RevokeWorkspaceGrant removes member's grant on ws; ErrNotFound when
	// there is none.
	RevokeWorkspaceGrant(ctx context.Context, ws domain.WorkspaceID, member domain.MemberID) error
	SetWorkspaceOwner(ctx context.Context, ws domain.WorkspaceID, owner domain.MemberID) error
}

func (d *DB) MemberWorkspaces(ctx context.Context, member domain.MemberID) (domain.WorkspaceAccess, error) {
	return memberWorkspaces(ctx, d.db, member)
}

func memberWorkspaces(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, member domain.MemberID) (domain.WorkspaceAccess, error) {
	access := domain.WorkspaceAccess{Owned: map[domain.WorkspaceID]bool{}, Granted: map[domain.WorkspaceID]bool{}}
	rows, err := q.QueryContext(ctx,
		`SELECT id, 1 FROM workspaces WHERE owner_member_id = ?
		 UNION ALL
		 SELECT workspace_id, 0 FROM workspace_grants WHERE member_id = ?`, member, member)
	if err != nil {
		return access, fmt.Errorf("store: member workspaces: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only iteration
	for rows.Next() {
		var (
			ws    domain.WorkspaceID
			owned bool
		)
		if err := rows.Scan(&ws, &owned); err != nil {
			return access, fmt.Errorf("store: member workspaces: %w", err)
		}
		if owned {
			access.Owned[ws] = true
		} else {
			access.Granted[ws] = true
		}
	}
	if err := rows.Err(); err != nil {
		return access, fmt.Errorf("store: member workspaces: %w", err)
	}
	return access, nil
}

func (d *DB) ListWorkspaceGrants(ctx context.Context, ws domain.WorkspaceID) ([]*domain.WorkspaceGrant, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT workspace_id, member_id, granted_by, created_at FROM workspace_grants
		 WHERE workspace_id = ? ORDER BY created_at, member_id`, ws)
	if err != nil {
		return nil, fmt.Errorf("store: list workspace grants: %w", err)
	}
	return collect(rows, func(row interface{ Scan(...any) error }) (*domain.WorkspaceGrant, error) {
		var (
			g  domain.WorkspaceGrant
			at int64
		)
		if err := row.Scan(&g.WorkspaceID, &g.MemberID, &g.GrantedBy, &at); err != nil {
			return nil, err
		}
		g.CreatedAt = decodeTime(at)
		return &g, nil
	})
}

func (d *DB) GrantWorkspace(ctx context.Context, ws domain.WorkspaceID, member, by domain.MemberID) error {
	at, err := encodeTime(time.Now())
	if err != nil {
		return fmt.Errorf("store: grant workspace: %w", err)
	}
	if _, err := d.db.ExecContext(ctx,
		`INSERT INTO workspace_grants (workspace_id, member_id, granted_by, created_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT (workspace_id, member_id) DO NOTHING`, ws, member, by, at); err != nil {
		return fmt.Errorf("store: grant workspace: %w", mapConstraint(err, ErrNotFound))
	}
	return nil
}

func (d *DB) RevokeWorkspaceGrant(ctx context.Context, ws domain.WorkspaceID, member domain.MemberID) error {
	err := notFoundOnZeroRows(d.db.ExecContext(ctx,
		`DELETE FROM workspace_grants WHERE workspace_id = ? AND member_id = ?`, ws, member))
	if err != nil && !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("store: revoke workspace grant: %w", err)
	}
	return err
}

func (d *DB) SetWorkspaceOwner(ctx context.Context, ws domain.WorkspaceID, owner domain.MemberID) error {
	err := notFoundOnZeroRows(d.db.ExecContext(ctx,
		`UPDATE workspaces SET owner_member_id = ? WHERE id = ?`, owner, ws))
	if err != nil && !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("store: set workspace owner: %w", err)
	}
	return err
}
