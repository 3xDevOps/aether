package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// IdentityStore holds what edge access adds to membership: the edge
// identities bound to members, the devices members reach the server
// through, and invitations naming edge accounts. Removing a member removes
// its identities, its devices, and the invitations it created or is linked
// by.
type IdentityStore interface {
	// GetMemberByIdentity returns the member bound to (provider, subject).
	GetMemberByIdentity(ctx context.Context, provider, subject string) (*domain.Member, error)
	ListIdentities(ctx context.Context) ([]*domain.Identity, error)
	// ClaimMember creates m bound to identity, provided the server has no
	// member at all; otherwise it returns ErrConflict and creates nothing.
	ClaimMember(ctx context.Context, m *domain.Member, identity *domain.Identity) error

	CreateInvitation(ctx context.Context, inv *domain.Invitation) error
	GetInvitation(ctx context.Context, id domain.InvitationID) (*domain.Invitation, error)
	// ListInvitations returns the invitations not yet accepted, expired
	// ones included, oldest first.
	ListInvitations(ctx context.Context) ([]*domain.Invitation, error)
	// DeleteInvitation revokes an invitation not yet accepted.
	DeleteInvitation(ctx context.Context, id domain.InvitationID) error
	// DeleteInvitationsBy revokes every invitation creator made that is
	// not yet accepted.
	DeleteInvitationsBy(ctx context.Context, creator domain.MemberID) error
	// AcceptInvitation binds identity to a member in one transaction and
	// returns that member. When identity is already bound it returns its
	// member and consumes nothing. Otherwise the invitation must be
	// unconsumed and unexpired at now: an invitation with a Member binds
	// identity to it, any other creates m with the invitation's role. The
	// invitation is then consumed, so it binds at most one identity, and
	// the devices waiting on it are deleted.
	AcceptInvitation(ctx context.Context, id domain.InvitationID, identity *domain.Identity, m *domain.Member, now time.Time) (*domain.Member, error)
	// AcceptInvitationDevice approves the device id waiting on an
	// invitation, in one transaction: it accepts the invitation for the
	// device's account as AcceptInvitation does, creating m unless the
	// invitation links an existing member, and makes the device that
	// member's, approved by approver. The invitation must be unconsumed
	// and unexpired at now, and the account no member's yet; the other
	// devices waiting on it are deleted.
	AcceptInvitationDevice(ctx context.Context, id domain.DeviceID, approver domain.MemberID, m *domain.Member, now time.Time) (*domain.Member, error)
	// BindIdentity binds identity to its existing Member; ErrConflict when
	// the account is bound already.
	BindIdentity(ctx context.Context, identity *domain.Identity) error

	// RemoveIdentity unbinds (provider, subject) from its member and
	// deletes every device that signed in with that account, returning
	// that member, empty when the account had none, and those devices;
	// ErrNotFound when there was neither. The member, its role and its
	// other credentials stay.
	RemoveIdentity(ctx context.Context, provider, subject string) (domain.MemberID, []domain.DeviceID, error)
	// UnlinkIdentity unbinds (provider, subject) from member, revokes the
	// devices that signed in with it and returns them. The revoked devices
	// stay listed, so their keys stay refused.
	UnlinkIdentity(ctx context.Context, member domain.MemberID, provider, subject string) ([]domain.DeviceID, error)

	// RegisterDevice records a new device of d.Member through its identity
	// d.Provider, d.Subject, with d.Status: approved, or awaiting approval
	// with its ApprovalCode. A pending device is ErrLimit when its account
	// has MaxWaitingDevices pending already.
	RegisterDevice(ctx context.Context, d *domain.Device) error
	// RegisterInvitationDevice records d, pending, waiting on the
	// invitation d.Invitation for the account d.Provider, d.Subject. The
	// invitation must be open at now and the account no member's, and fewer
	// than MaxWaitingDevices may wait on it (ErrLimit). It first deletes the
	// devices waiting on invitations expired at now.
	RegisterInvitationDevice(ctx context.Context, d *domain.Device, now time.Time) error
	GetDevice(ctx context.Context, id domain.DeviceID) (*domain.Device, error)
	GetDeviceByCredential(ctx context.Context, credential string) (*domain.Device, error)
	// GetDeviceByApprovalCode finds the device awaiting approval with
	// code, as a person typed it (any case, with or without the dash). A
	// code naming more than one such device is ErrConflict. It and
	// ListDevices leave out devices waiting on an expired invitation.
	GetDeviceByApprovalCode(ctx context.Context, code string) (*domain.Device, error)
	// ListDevices returns member's devices, or every device, those waiting
	// on an invitation included, when member is empty, oldest first.
	ListDevices(ctx context.Context, member domain.MemberID) ([]*domain.Device, error)
	// ApproveDevice approves a member's device awaiting approval;
	// ErrNotFound when it is approved, revoked or waiting on an invitation.
	ApproveDevice(ctx context.Context, id domain.DeviceID, approver domain.MemberID) error
	// RevokeDevice revokes a device that is not revoked yet. A revoked
	// device still counts as its member's device.
	RevokeDevice(ctx context.Context, id domain.DeviceID) error
	TouchDevice(ctx context.Context, id domain.DeviceID, at time.Time) error
}

var _ IdentityStore = (*DB)(nil)

func (d *DB) GetMemberByIdentity(ctx context.Context, provider, subject string) (*domain.Member, error) {
	m, err := scanMember(d.db.QueryRowContext(ctx,
		`SELECT `+memberCols+` FROM members WHERE id =
		 (SELECT member_id FROM member_identities WHERE provider = ? AND subject = ?)`,
		provider, subject))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get member by identity: %w", err)
	}
	return m, nil
}

const identityCols = `member_id, provider, subject, email, login, created_at`

func scanIdentity(row interface{ Scan(...any) error }) (*domain.Identity, error) {
	var (
		id        domain.Identity
		createdAt int64
	)
	if err := row.Scan(&id.Member, &id.Provider, &id.Subject, &id.Email, &id.Login, &createdAt); err != nil {
		return nil, err
	}
	id.CreatedAt = decodeTime(createdAt)
	return &id, nil
}

func (d *DB) ListIdentities(ctx context.Context) ([]*domain.Identity, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT `+identityCols+` FROM member_identities ORDER BY created_at, provider, subject`)
	if err != nil {
		return nil, fmt.Errorf("store: list identities: %w", err)
	}
	return collect(rows, scanIdentity)
}

func insertIdentity(ctx context.Context, q execer, identity *domain.Identity) error {
	if identity.Member == "" || identity.Provider == "" || identity.Subject == "" {
		return errors.New("store: bind identity: member, provider and subject are required")
	}
	ts := identity.CreatedAt
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	createdAt, err := encodeTime(ts)
	if err != nil {
		return fmt.Errorf("store: bind identity: %w", err)
	}
	if _, err := q.ExecContext(ctx,
		`INSERT INTO member_identities (`+identityCols+`) VALUES (?, ?, ?, ?, ?, ?)`,
		identity.Member, identity.Provider, identity.Subject, identity.Email, identity.Login, createdAt,
	); err != nil {
		return fmt.Errorf("store: bind identity: %w", mapConstraint(err, ErrNotFound))
	}
	identity.CreatedAt = ts
	return nil
}

func (d *DB) ClaimMember(ctx context.Context, m *domain.Member, identity *domain.Identity) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var claimed bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM members)`).Scan(&claimed); err != nil {
		return fmt.Errorf("store: claim: %w", err)
	}
	if claimed {
		return fmt.Errorf("%w: the server already has members", ErrConflict)
	}
	if err := insertMember(ctx, tx, m); err != nil {
		return err
	}
	identity.Member = m.ID
	if err := insertIdentity(ctx, tx, identity); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit claim: %w", err)
	}
	return nil
}

func (d *DB) RemoveIdentity(ctx context.Context, provider, subject string) (domain.MemberID, []domain.DeviceID, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return "", nil, fmt.Errorf("store: begin remove identity: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var member domain.MemberID
	err = tx.QueryRowContext(ctx,
		`SELECT member_id FROM member_identities WHERE provider = ? AND subject = ?`, provider, subject).Scan(&member)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", nil, fmt.Errorf("store: remove identity: %w", err)
	}
	devices, err := accountDevices(ctx, tx, provider, subject, "")
	if err != nil {
		return "", nil, err
	}
	if member == "" && len(devices) == 0 {
		return "", nil, ErrNotFound
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM member_devices WHERE provider = ? AND subject = ?`, provider, subject); err != nil {
		return "", nil, fmt.Errorf("store: remove identity: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM member_identities WHERE provider = ? AND subject = ?`, provider, subject); err != nil {
		return "", nil, fmt.Errorf("store: remove identity: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", nil, fmt.Errorf("store: commit remove identity: %w", err)
	}
	return member, devices, nil
}

func (d *DB) UnlinkIdentity(ctx context.Context, member domain.MemberID, provider, subject string) ([]domain.DeviceID, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("store: begin unlink identity: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	devices, err := accountDevices(ctx, tx, provider, subject, member)
	if err != nil {
		return nil, err
	}
	if err := notFoundOnZeroRows(tx.ExecContext(ctx,
		`DELETE FROM member_identities WHERE provider = ? AND subject = ? AND member_id = ?`,
		provider, subject, member)); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, fmt.Errorf("%w: %s %s is not an identity of member %s", ErrNotFound, provider, subject, member)
		}
		return nil, fmt.Errorf("store: unlink identity: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE member_devices SET status = 'revoked', approval_code = ''
		 WHERE provider = ? AND subject = ? AND member_id = ?`, provider, subject, member); err != nil {
		return nil, fmt.Errorf("store: unlink identity: revoke devices: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("store: commit unlink identity: %w", err)
	}
	return devices, nil
}

// accountDevices returns the ids of the devices that signed in with the
// account provider, subject: member's, or every one when member is empty.
func accountDevices(ctx context.Context, tx *sql.Tx, provider, subject string, member domain.MemberID) ([]domain.DeviceID, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT id FROM member_devices WHERE provider = ? AND subject = ? AND (? = '' OR member_id = ?)`,
		provider, subject, member, member)
	if err != nil {
		return nil, fmt.Errorf("store: devices of %s %s: %w", provider, subject, err)
	}
	ids, err := collect(rows, func(row interface{ Scan(...any) error }) (*domain.DeviceID, error) {
		var id domain.DeviceID
		return &id, row.Scan(&id)
	})
	if err != nil {
		return nil, err
	}
	devices := make([]domain.DeviceID, len(ids))
	for i, id := range ids {
		devices[i] = *id
	}
	return devices, nil
}

func (d *DB) BindIdentity(ctx context.Context, identity *domain.Identity) error {
	if identity.Member == "" {
		return errors.New("store: bind identity: member is required")
	}
	return insertIdentity(ctx, d.db, identity)
}

const invitationCols = `id, provider, login, email, role, member_id, created_by, created_at, expires_at, consumed_at`

func scanInvitation(row interface{ Scan(...any) error }) (*domain.Invitation, error) {
	var (
		inv                  domain.Invitation
		member               sql.NullString
		createdAt, expiresAt int64
		consumedAt           *int64
	)
	if err := row.Scan(&inv.ID, &inv.Provider, &inv.Login, &inv.Email, &inv.Role, &member,
		&inv.CreatedBy, &createdAt, &expiresAt, &consumedAt); err != nil {
		return nil, err
	}
	inv.Member = domain.MemberID(member.String)
	inv.CreatedAt, inv.ExpiresAt = decodeTime(createdAt), decodeTime(expiresAt)
	inv.ConsumedAt = decodeTimePtr(consumedAt)
	return &inv, nil
}

func (d *DB) CreateInvitation(ctx context.Context, inv *domain.Invitation) error {
	switch {
	case (inv.Login == "") == (inv.Email == ""):
		return errors.New("store: create invitation: exactly one of login and email is required")
	case inv.Member == "" && !inv.Role.Valid():
		return fmt.Errorf("store: create invitation: invalid role %q", inv.Role)
	case inv.Member != "" && inv.Role != "":
		return errors.New("store: create invitation: a link keeps its member's role")
	case inv.CreatedBy == "" || inv.ExpiresAt.IsZero():
		return errors.New("store: create invitation: creator and expiry are required")
	}
	id, ts, err := prepareCreate(inv.CreatedAt)
	if err != nil {
		return err
	}
	createdAt, err := encodeTime(ts)
	if err != nil {
		return fmt.Errorf("store: create invitation: %w", err)
	}
	expiresAt, err := encodeTime(inv.ExpiresAt)
	if err != nil {
		return fmt.Errorf("store: create invitation: %w", err)
	}
	var member any
	if inv.Member != "" {
		member = inv.Member
	}
	if _, err := d.db.ExecContext(ctx,
		`INSERT INTO identity_invitations (`+invitationCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)`,
		id, inv.Provider, inv.Login, inv.Email, inv.Role, member, inv.CreatedBy, createdAt, expiresAt,
	); err != nil {
		return fmt.Errorf("store: create invitation: %w", mapConstraint(err, ErrNotFound))
	}
	inv.ID, inv.CreatedAt, inv.ConsumedAt = domain.InvitationID(id), ts, nil
	return nil
}

func (d *DB) GetInvitation(ctx context.Context, id domain.InvitationID) (*domain.Invitation, error) {
	return getInvitation(ctx, d.db, id)
}

type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func getInvitation(ctx context.Context, q queryRower, id domain.InvitationID) (*domain.Invitation, error) {
	inv, err := scanInvitation(q.QueryRowContext(ctx,
		`SELECT `+invitationCols+` FROM identity_invitations WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get invitation: %w", err)
	}
	return inv, nil
}

func (d *DB) ListInvitations(ctx context.Context) ([]*domain.Invitation, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT `+invitationCols+` FROM identity_invitations
		 WHERE consumed_at IS NULL ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("store: list invitations: %w", err)
	}
	return collect(rows, scanInvitation)
}

func (d *DB) DeleteInvitation(ctx context.Context, id domain.InvitationID) error {
	return d.execDelete(ctx, "delete invitation",
		`DELETE FROM identity_invitations WHERE id = ? AND consumed_at IS NULL`, id)
}

func (d *DB) DeleteInvitationsBy(ctx context.Context, creator domain.MemberID) error {
	if _, err := d.db.ExecContext(ctx,
		`DELETE FROM identity_invitations WHERE created_by = ? AND consumed_at IS NULL`, creator); err != nil {
		return fmt.Errorf("store: delete invitations by %s: %w", creator, err)
	}
	return nil
}

func (d *DB) AcceptInvitation(ctx context.Context, id domain.InvitationID, identity *domain.Identity, m *domain.Member, now time.Time) (*domain.Member, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("store: begin accept invitation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	bound, err := scanMember(tx.QueryRowContext(ctx,
		`SELECT `+memberCols+` FROM members WHERE id =
		 (SELECT member_id FROM member_identities WHERE provider = ? AND subject = ?)`,
		identity.Provider, identity.Subject))
	if err == nil {
		return bound, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: accept invitation: %w", err)
	}
	if m, err = acceptInTx(ctx, tx, id, identity, m, now, ""); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("store: commit accept invitation: %w", err)
	}
	return m, nil
}

func (d *DB) AcceptInvitationDevice(ctx context.Context, id domain.DeviceID, approver domain.MemberID, m *domain.Member, now time.Time) (*domain.Member, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("store: begin accept invitation device: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	dev, err := scanDevice(tx.QueryRowContext(ctx, `SELECT `+deviceCols+` FROM member_devices WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: accept invitation device: %w", err)
	}
	if dev.Invitation == "" || dev.Status != domain.DevicePending {
		return nil, fmt.Errorf("%w: device %s is not waiting on an invitation", ErrNotFound, id)
	}
	var bound bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM member_identities WHERE provider = ? AND subject = ?)`,
		dev.Provider, dev.Subject).Scan(&bound); err != nil {
		return nil, fmt.Errorf("store: accept invitation device: %w", err)
	}
	if bound {
		return nil, fmt.Errorf("%w: %s %s is a member's account already", ErrConflict, dev.Provider, dev.Subject)
	}
	identity := &domain.Identity{Provider: dev.Provider, Subject: dev.Subject, Email: dev.Email, Login: dev.Login}
	if m, err = acceptInTx(ctx, tx, dev.Invitation, identity, m, now, id); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE member_devices SET member_id = ?, invitation_id = NULL, status = 'approved', approval_code = '',
		 approved_by = ? WHERE id = ?`, m.ID, approver, id); err != nil {
		return nil, fmt.Errorf("store: accept invitation device: approve: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("store: commit accept invitation device: %w", err)
	}
	return m, nil
}

// acceptInTx accepts the invitation id for identity, which no member
// holds, within tx: it binds identity to the linked member or to m,
// created with the invitation's role, consumes the invitation and deletes
// the devices waiting on it but keep.
func acceptInTx(ctx context.Context, tx *sql.Tx, id domain.InvitationID, identity *domain.Identity, m *domain.Member,
	now time.Time, keep domain.DeviceID) (*domain.Member, error) {
	inv, err := getInvitation(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if inv.ConsumedAt != nil || !now.Before(inv.ExpiresAt) {
		return nil, fmt.Errorf("%w: invitation %s is used or expired", ErrNotFound, id)
	}
	if inv.Member != "" {
		if m, err = scanMember(tx.QueryRowContext(ctx,
			`SELECT `+memberCols+` FROM members WHERE id = ?`, inv.Member)); err != nil {
			return nil, fmt.Errorf("store: accept invitation: linked member: %w", err)
		}
	} else {
		m.Role, m.Pending = inv.Role, false
		if err = insertMember(ctx, tx, m); err != nil {
			return nil, err
		}
	}
	identity.Member = m.ID
	if err = insertIdentity(ctx, tx, identity); err != nil {
		return nil, err
	}
	consumedAt, err := encodeTime(now)
	if err != nil {
		return nil, fmt.Errorf("store: accept invitation: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE identity_invitations SET consumed_at = ? WHERE id = ?`, consumedAt, id); err != nil {
		return nil, fmt.Errorf("store: consume invitation: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM member_devices WHERE invitation_id = ? AND id <> ?`, id, keep); err != nil {
		return nil, fmt.Errorf("store: accept invitation: delete waiting devices: %w", err)
	}
	return m, nil
}
