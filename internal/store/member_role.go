package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
)

// ErrLastSuperAdmin refuses a role change or removal that would leave no
// approved super admin to administer the server.
var ErrLastSuperAdmin = errors.New("store: refusing to demote the last approved super admin")

// OwnsWorkspacesError refuses demoting Member below admin while they own
// Workspaces, each "name (id)": a workspace's owner is always an admin.
type OwnsWorkspacesError struct {
	Member     domain.MemberID
	Workspaces []string
}

func (e *OwnsWorkspacesError) Error() string {
	return fmt.Sprintf("store: member %s owns workspaces %s", e.Member, strings.Join(e.Workspaces, ", "))
}

func (d *DB) SetMemberRole(ctx context.Context, id domain.MemberID, role domain.Role, by domain.MemberID) (domain.Role, error) {
	if !role.Valid() {
		return "", fmt.Errorf("store: set member role: invalid role %q", role)
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("store: begin set member role: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err = requireSuperAdmin(ctx, tx, by); err != nil {
		return "", err
	}
	var was domain.Role
	err = tx.QueryRowContext(ctx, `SELECT role FROM members WHERE id = ? AND merged_into = ''`, id).Scan(&was)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("store: set member role: %w", err)
	}
	if was == role {
		return was, nil
	}
	if was == domain.RoleSuperAdmin {
		if err := refuseLastSuperAdmin(ctx, tx, id); err != nil {
			return "", fmt.Errorf("store: set member role: %w", err)
		}
	}
	if !role.Admin() {
		owned, err := ownedWorkspaces(ctx, tx, id)
		if err != nil {
			return "", fmt.Errorf("store: set member role: %w", err)
		}
		if len(owned) > 0 {
			return "", &OwnsWorkspacesError{Member: id, Workspaces: owned}
		}
	}
	if was == domain.RoleSuperAdmin || (was == domain.RoleAdmin && !role.Admin()) {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM identity_invitations WHERE created_by = ? AND consumed_at IS NULL`, id); err != nil {
			return "", fmt.Errorf("store: set member role: revoke invitations: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE members SET role = ? WHERE id = ?`, role, id); err != nil {
		return "", fmt.Errorf("store: set member role: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("store: commit set member role: %w", err)
	}
	return was, nil
}

// requireSuperAdmin refuses a write whose authority is by's super admin
// role unless by, read in the write's transaction, is still an approved
// super admin. An empty by is the machine administrator.
func requireSuperAdmin(ctx context.Context, q queryRower, by domain.MemberID) error {
	if by == "" {
		return nil
	}
	var super bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM members
		WHERE id = ? AND role = 'super_admin' AND pending = 0 AND merged_into = '')`, by).Scan(&super); err != nil {
		return fmt.Errorf("store: read the role of %s: %w", by, err)
	}
	if !super {
		return fmt.Errorf("%w: member %s is no longer a super admin", ErrConflict, by)
	}
	return nil
}

func refuseLastSuperAdmin(ctx context.Context, tx *sql.Tx, id domain.MemberID) error {
	var others int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM members WHERE role = 'super_admin' AND pending = 0 AND merged_into = '' AND id <> ?`,
		id).Scan(&others); err != nil {
		return fmt.Errorf("count super admins: %w", err)
	}
	if others == 0 {
		return ErrLastSuperAdmin
	}
	return nil
}

// refuseRemoval is deleting member id's guard on behalf of by, in the
// deleting transaction: by must still be a super admin, and the last
// approved super admin and a workspace owner stay.
func refuseRemoval(ctx context.Context, tx *sql.Tx, id, by domain.MemberID) error {
	if err := requireSuperAdmin(ctx, tx, by); err != nil {
		return err
	}
	var role domain.Role
	err := tx.QueryRowContext(ctx, `SELECT role FROM members WHERE id = ?`, id).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("read role: %w", err)
	}
	if role == domain.RoleSuperAdmin {
		if err = refuseLastSuperAdmin(ctx, tx, id); err != nil {
			return err
		}
	}
	owned, err := ownedWorkspaces(ctx, tx, id)
	if err != nil {
		return err
	}
	if len(owned) > 0 {
		return &OwnsWorkspacesError{Member: id, Workspaces: owned}
	}
	return nil
}

func ownedWorkspaces(ctx context.Context, tx *sql.Tx, id domain.MemberID) ([]string, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT name || ' (' || id || ')' FROM workspaces WHERE owner_member_id = ? ORDER BY created_at, id`, id)
	if err != nil {
		return nil, fmt.Errorf("owned workspaces: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only iteration
	var owned []string
	for rows.Next() {
		var w string
		if err := rows.Scan(&w); err != nil {
			return nil, fmt.Errorf("owned workspaces: %w", err)
		}
		owned = append(owned, w)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("owned workspaces: %w", err)
	}
	return owned, nil
}
