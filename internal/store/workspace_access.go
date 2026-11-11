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

// WorkspaceAccessStore holds workspace owners and grants.
type WorkspaceAccessStore interface {
	// MemberWorkspaces returns the workspaces member owns and the ones
	// they were granted.
	MemberWorkspaces(ctx context.Context, member domain.MemberID) (domain.WorkspaceAccess, error)
	ListWorkspaceGrants(ctx context.Context, ws domain.WorkspaceID) ([]*domain.WorkspaceGrant, error)
	// WorkspaceMembers returns the members who own, were granted, or have
	// a session in any of workspaces, a session's extra repositories
	// included, with the members whose account or home a session there
	// uses.
	WorkspaceMembers(ctx context.Context, workspaces []domain.WorkspaceID) ([]domain.MemberID, error)
	// GrantWorkspace lets member into ws on behalf of by; granting an
	// existing grant is a no-op. ErrNotFound when either does not exist.
	// The write itself decides by's authority, refusing with ErrConflict
	// unless by is an approved super admin, or an approved admin who owns
	// ws granting a member below admin. An empty by is the machine
	// administrator.
	GrantWorkspace(ctx context.Context, ws domain.WorkspaceID, member, by domain.MemberID) error
	// RevokeWorkspaceGrant removes member's grant on ws on behalf of by,
	// whose authority is decided as for GrantWorkspace; ErrNotFound when
	// there is no grant.
	RevokeWorkspaceGrant(ctx context.Context, ws domain.WorkspaceID, member, by domain.MemberID) error
	// SetWorkspaceOwner makes owner, an approved admin or super admin,
	// the owner of ws and returns the previous owner, in one transaction:
	// owner's grant on ws goes, and a previous owner who is not a super
	// admin keeps reaching ws through a grant by by. ErrConflict when
	// owner is not an approved admin, or by, unless empty, is not an
	// approved super admin.
	SetWorkspaceOwner(ctx context.Context, ws domain.WorkspaceID, owner, by domain.MemberID) (domain.MemberID, error)
	// JoinMember creates m, which has a public key, with a grant on each
	// of workspaces that still exists and issuer administers.
	JoinMember(ctx context.Context, m *domain.Member, workspaces []domain.WorkspaceID, issuer domain.MemberID) error
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

func (d *DB) WorkspaceMembers(ctx context.Context, workspaces []domain.WorkspaceID) ([]domain.MemberID, error) {
	if len(workspaces) == 0 {
		return nil, nil
	}
	ids, err := json.Marshal(workspaces)
	if err != nil {
		return nil, fmt.Errorf("store: workspace members: %w", err)
	}
	rows, err := d.db.QueryContext(ctx,
		`WITH ws(id) AS (SELECT value FROM json_each(?)),
		 rs AS (SELECT member_id, account_member_id, home_member_id FROM runs WHERE workspace_id IN ws
		     UNION ALL SELECT r.member_id, r.account_member_id, r.home_member_id
		     FROM run_repos rr JOIN runs r ON r.id = rr.run_id WHERE rr.workspace_id IN ws)
		 SELECT owner_member_id FROM workspaces WHERE id IN ws AND owner_member_id <> ''
		 UNION SELECT member_id FROM workspace_grants WHERE workspace_id IN ws
		 UNION SELECT member_id FROM rs
		 UNION SELECT account_member_id FROM rs WHERE account_member_id <> ''
		 UNION SELECT home_member_id FROM rs WHERE home_member_id <> ''`,
		string(ids))
	if err != nil {
		return nil, fmt.Errorf("store: workspace members: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only iteration
	var out []domain.MemberID
	for rows.Next() {
		var id domain.MemberID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("store: workspace members: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: workspace members: %w", err)
	}
	return out, nil
}

// grantAuthority is the SQL condition that ?2, changing the grant of
// member row m on workspace ?1, administers ?1 for m at this moment.
const grantAuthority = `(?2 = '' OR EXISTS (SELECT 1 FROM members c
	WHERE c.id = ?2 AND c.pending = 0 AND c.merged_into = ''
	AND (c.role = 'super_admin' OR (c.role = 'admin' AND m.role NOT IN ('admin', 'super_admin')
		AND EXISTS (SELECT 1 FROM workspaces w WHERE w.id = ?1 AND w.owner_member_id = c.id)))))`

func (d *DB) GrantWorkspace(ctx context.Context, ws domain.WorkspaceID, member, by domain.MemberID) error {
	at, err := encodeTime(time.Now())
	if err != nil {
		return fmt.Errorf("store: grant workspace: %w", err)
	}
	res, err := d.db.ExecContext(ctx,
		`INSERT INTO workspace_grants (workspace_id, member_id, granted_by, created_at)
		 SELECT ?1, m.id, ?2, ?3 FROM members m WHERE m.id = ?4 AND `+grantAuthority+`
		 ON CONFLICT (workspace_id, member_id) DO NOTHING`, ws, by, at, member)
	if err != nil {
		return fmt.Errorf("store: grant workspace: %w", mapConstraint(err, ErrNotFound))
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: grant workspace: %w", err)
	}
	if n > 0 {
		return nil
	}
	m, err := d.GetMember(ctx, member)
	if err != nil {
		return err
	}
	return d.grantRefusal(ctx, ws, m, by)
}

func (d *DB) RevokeWorkspaceGrant(ctx context.Context, ws domain.WorkspaceID, member, by domain.MemberID) error {
	res, err := d.db.ExecContext(ctx,
		`DELETE FROM workspace_grants WHERE workspace_id = ?1 AND member_id = ?3
		 AND EXISTS (SELECT 1 FROM members m WHERE m.id = ?3 AND `+grantAuthority+`)`, ws, by, member)
	if err != nil {
		return fmt.Errorf("store: revoke workspace grant: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: revoke workspace grant: %w", err)
	}
	if n > 0 {
		return nil
	}
	var granted bool
	if err = d.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM workspace_grants WHERE workspace_id = ? AND member_id = ?)`,
		ws, member).Scan(&granted); err != nil {
		return fmt.Errorf("store: revoke workspace grant: %w", err)
	}
	if !granted {
		return ErrNotFound
	}
	m, err := d.GetMember(ctx, member)
	if err != nil {
		return err
	}
	if err := d.grantRefusal(ctx, ws, m, by); err != nil {
		return err
	}
	return fmt.Errorf("%w: member %s's grant on %s changed meanwhile; retry", ErrConflict, member, ws)
}

// grantRefusal explains why grantAuthority refused by changing m's grant
// on ws, read after the refused write; nil when it no longer refuses.
func (d *DB) grantRefusal(ctx context.Context, ws domain.WorkspaceID, m *domain.Member, by domain.MemberID) error {
	if by == "" {
		return nil
	}
	var (
		role         domain.Role
		exists, owns bool
	)
	if err := d.db.QueryRowContext(ctx,
		`SELECT COALESCE((SELECT role FROM members WHERE id = ?2 AND pending = 0 AND merged_into = ''), ''),
		 EXISTS (SELECT 1 FROM workspaces WHERE id = ?1),
		 EXISTS (SELECT 1 FROM workspaces WHERE id = ?1 AND owner_member_id = ?2)`, ws, by).Scan(&role, &exists, &owns); err != nil {
		return fmt.Errorf("store: read the authority of %s on %s: %w", by, ws, err)
	}
	switch {
	case role == domain.RoleSuperAdmin:
		return nil
	case !exists:
		return ErrNotFound
	case role != domain.RoleAdmin || !owns:
		return fmt.Errorf("%w: member %s no longer administers workspace %s", ErrConflict, by, ws)
	case m.Role.Admin():
		return fmt.Errorf("%w: member %s is %s now; only a super admin grants or revokes an admin", ErrConflict, m.ID, m.Role)
	}
	return nil
}

func (d *DB) SetWorkspaceOwner(ctx context.Context, ws domain.WorkspaceID, owner, by domain.MemberID) (domain.MemberID, error) {
	at, err := encodeTime(time.Now())
	if err != nil {
		return "", fmt.Errorf("store: set workspace owner: %w", err)
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("store: begin set workspace owner: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err = requireSuperAdmin(ctx, tx, by); err != nil {
		return "", err
	}
	var previous domain.MemberID
	err = tx.QueryRowContext(ctx, `SELECT owner_member_id FROM workspaces WHERE id = ?`, ws).Scan(&previous)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("store: set workspace owner: %w", err)
	}
	if previous == owner {
		return previous, nil
	}
	err = notFoundOnZeroRows(tx.ExecContext(ctx,
		`UPDATE workspaces SET owner_member_id = ?1 WHERE id = ?2 AND EXISTS (SELECT 1 FROM members
		 WHERE id = ?1 AND role IN ('admin', 'super_admin') AND pending = 0 AND merged_into = '')`, owner, ws))
	if errors.Is(err, ErrNotFound) {
		return "", fmt.Errorf("%w: %s is not an approved admin or super admin", ErrConflict, owner)
	}
	if err != nil {
		return "", fmt.Errorf("store: set workspace owner: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM workspace_grants WHERE workspace_id = ? AND member_id = ?`, ws, owner); err != nil {
		return "", fmt.Errorf("store: set workspace owner: remove the new owner's grant: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO workspace_grants (workspace_id, member_id, granted_by, created_at)
		 SELECT ?, id, ?, ? FROM members WHERE id = ? AND role <> 'super_admin' AND merged_into = ''`,
		ws, by, at, previous); err != nil {
		return "", fmt.Errorf("store: set workspace owner: grant the previous owner: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("store: commit set workspace owner: %w", err)
	}
	return previous, nil
}

// ErrIssuerDemoted refuses an invite code whose issuer is no longer an
// admin or super admin when it is redeemed.
var ErrIssuerDemoted = errors.New("the invite's issuer is no longer an admin")

func (d *DB) JoinMember(ctx context.Context, m *domain.Member, workspaces []domain.WorkspaceID, issuer domain.MemberID) error {
	if m.PublicKey == "" {
		return errors.New("store: join member: a public key is required")
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin join member: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if issuer != "" {
		var admin bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM members WHERE id = ?
			AND role IN ('admin', 'super_admin') AND pending = 0 AND merged_into = '')`, issuer).Scan(&admin); err != nil {
			return fmt.Errorf("store: join member: read the role of %s: %w", issuer, err)
		}
		if !admin {
			return fmt.Errorf("store: join member: %s: %w", issuer, ErrIssuerDemoted)
		}
	}
	if err := insertMember(ctx, tx, m); err != nil {
		return err
	}
	if err := grantAdministered(ctx, tx, m.ID, issuer, workspaces, time.Now()); err != nil {
		return fmt.Errorf("store: join member: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit join member: %w", err)
	}
	return nil
}

// grantAdministered grants member each of workspaces that still exists and
// that issuer administers now: any for a super admin, the ones they own
// for an admin, none for anyone else.
func grantAdministered(ctx context.Context, tx *sql.Tx, member, issuer domain.MemberID, workspaces []domain.WorkspaceID, now time.Time) error {
	if len(workspaces) == 0 {
		return nil
	}
	ids, err := json.Marshal(workspaces)
	if err != nil {
		return fmt.Errorf("grant workspaces: %w", err)
	}
	at, err := encodeTime(now)
	if err != nil {
		return fmt.Errorf("grant workspaces: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO workspace_grants (workspace_id, member_id, granted_by, created_at)
		 SELECT w.id, ?1, i.id, ?2 FROM workspaces w JOIN members i ON i.id = ?3
		 WHERE w.id IN (SELECT value FROM json_each(?4)) AND i.merged_into = '' AND i.pending = 0
		   AND (i.role = 'super_admin' OR (i.role = 'admin' AND w.owner_member_id = i.id))`,
		member, at, issuer, string(ids)); err != nil {
		return fmt.Errorf("grant workspaces: %w", err)
	}
	return nil
}
