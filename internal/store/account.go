package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/3xDevOps/Aether/internal/domain"
)

// ShareAccount grants grantee use of owner's agent environment. Repeating an
// existing grant is idempotent so dashboard retries cannot turn success into
// an error.
func (d *DB) ShareAccount(ctx context.Context, owner, grantee domain.MemberID) error {
	if owner == "" || grantee == "" || owner == grantee {
		return fmt.Errorf("store: share account: owner and a different grantee are required")
	}
	_, err := d.db.ExecContext(ctx, `
		INSERT INTO account_shares (owner_member_id, grantee_member_id, created_at)
		VALUES (?, ?, unixepoch())
		ON CONFLICT (owner_member_id, grantee_member_id) DO NOTHING`, owner, grantee)
	if err != nil {
		return fmt.Errorf("store: share account: %w", mapConstraint(err, ErrNotFound))
	}
	return nil
}

// RevokeAccountShare removes one grant. Revoking an absent grant is
// idempotent; callers care that access is gone, not whether it existed.
func (d *DB) RevokeAccountShare(ctx context.Context, owner, grantee domain.MemberID) error {
	if owner == "" || grantee == "" {
		return fmt.Errorf("store: revoke account share: owner and grantee are required")
	}
	if _, err := d.db.ExecContext(ctx,
		`DELETE FROM account_shares WHERE owner_member_id = ? AND grantee_member_id = ?`,
		owner, grantee); err != nil {
		return fmt.Errorf("store: revoke account share: %w", err)
	}
	return nil
}

func (d *DB) AccountSharedWith(ctx context.Context, owner, grantee domain.MemberID) (bool, error) {
	var one int
	err := d.db.QueryRowContext(ctx, `
		SELECT 1 FROM account_shares
		WHERE owner_member_id = ? AND grantee_member_id = ?`, owner, grantee).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("store: check account share: %w", err)
	}
	return true, nil
}

func (d *DB) ListAccountOwners(ctx context.Context, grantee domain.MemberID) ([]*domain.Member, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT m.id, m.display_name, m.public_key, m.tailnet_login, m.pending,
		       m.color, m.role, m.created_at, m.image, m.git_name, m.git_email FROM members m
		JOIN account_shares s ON s.owner_member_id = m.id
		WHERE s.grantee_member_id = ?
		ORDER BY m.display_name, m.id`, grantee)
	if err != nil {
		return nil, fmt.Errorf("store: list account owners: %w", err)
	}
	return collect(rows, scanMember)
}

func (d *DB) ListAccountGrantees(ctx context.Context, owner domain.MemberID) ([]*domain.Member, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT m.id, m.display_name, m.public_key, m.tailnet_login, m.pending,
		       m.color, m.role, m.created_at, m.image, m.git_name, m.git_email FROM members m
		JOIN account_shares s ON s.grantee_member_id = m.id
		WHERE s.owner_member_id = ?
		ORDER BY m.display_name, m.id`, owner)
	if err != nil {
		return nil, fmt.Errorf("store: list account grantees: %w", err)
	}
	return collect(rows, scanMember)
}
