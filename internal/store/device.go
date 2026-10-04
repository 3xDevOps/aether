package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// approvalAlphabet leaves out 0, 1, I and O, which people misread. It has
// 32 letters, so a hash byte modulo its length is unbiased.
const approvalAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// approvalCodeLength is 8 characters, 40 bits.
const approvalCodeLength = 8

const approvalContext = "aether-device-approval-v1\x00"

// ApprovalCode is the approval code of a device key, an authorized_keys
// line: 8 characters of a domain-separated SHA-256 of the line, shown as
// XXXX-XXXX. Deriving it from the key means approving a code admits the
// key the waiting device holds and no other. It is no secret: the device
// shows it inside SSH and a person passes it to an approver. Device lists
// carry the key's SHA256 fingerprint, a hash without the context string,
// which does not reveal the code. Two keys can share a code, so a code
// that names more than one waiting device approves none of them.
func ApprovalCode(credential string) string {
	sum := sha256.Sum256([]byte(approvalContext + credential))
	var b [approvalCodeLength]byte
	for i := range b {
		b[i] = approvalAlphabet[int(sum[i])%len(approvalAlphabet)]
	}
	return string(b[:4]) + "-" + string(b[4:])
}

// normalizeApprovalCode returns code in its stored form, or "" when it
// cannot be one.
func normalizeApprovalCode(code string) string {
	code = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(code), "-", ""))
	if len(code) != approvalCodeLength {
		return ""
	}
	for i := 0; i < len(code); i++ {
		if !strings.ContainsRune(approvalAlphabet, rune(code[i])) {
			return ""
		}
	}
	return code[:4] + "-" + code[4:]
}

const deviceCols = `id, member_id, invitation_id, provider, subject, email, login, name, credential, label, status,
	approval_code, created_at, last_seen_at, approved_by`

func scanDevice(row interface{ Scan(...any) error }) (*domain.Device, error) {
	var (
		dev                domain.Device
		member, invitation sql.NullString
		createdAt          int64
		lastSeen           *int64
	)
	if err := row.Scan(&dev.ID, &member, &invitation, &dev.Provider, &dev.Subject, &dev.Email, &dev.Login, &dev.Name,
		&dev.Credential, &dev.Label, &dev.Status, &dev.ApprovalCode, &createdAt, &lastSeen, &dev.ApprovedBy); err != nil {
		return nil, err
	}
	dev.Member, dev.Invitation = domain.MemberID(member.String), domain.InvitationID(invitation.String)
	dev.CreatedAt = decodeTime(createdAt)
	dev.LastSeenAt = decodeTimePtr(lastSeen)
	return &dev, nil
}

// MaxWaitingDevices bounds the pending devices of one account and the
// devices waiting on one invitation. Every relayed connection with a new
// key records a device, so without it an edge could grow the table
// without limit.
const MaxWaitingDevices = 10

func (d *DB) RegisterDevice(ctx context.Context, dev *domain.Device) error {
	switch {
	case dev.Member == "" || dev.Provider == "" || dev.Subject == "" || dev.Credential == "":
		return errors.New("store: register device: member, identity and credential are required")
	case dev.Status != domain.DeviceApproved && !dev.Status.AwaitsApproval():
		return fmt.Errorf("store: register device: a new device cannot be %q", dev.Status)
	case dev.Status != domain.DeviceApproved && dev.ApprovedBy != "":
		return errors.New("store: register device: only an approved device has an approver")
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin register device: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if dev.Status == domain.DevicePending {
		if err = checkWaiting(ctx, tx, `provider = ? AND subject = ? AND status = 'pending'`, dev.Provider, dev.Subject); err != nil {
			return fmt.Errorf("store: register device: %s %s: %w", dev.Provider, dev.Subject, err)
		}
	}
	// Nothing is inserted unless the identity is the member's.
	err = insertDevice(ctx, tx, dev,
		`EXISTS (SELECT 1 FROM member_identities WHERE provider = ? AND subject = ? AND member_id = ?)`,
		dev.Provider, dev.Subject, dev.Member)
	if errors.Is(err, ErrNotFound) {
		return fmt.Errorf("store: register device: %s %s is not an identity of member %s: %w",
			dev.Provider, dev.Subject, dev.Member, ErrNotFound)
	}
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit register device: %w", err)
	}
	return nil
}

// checkWaiting returns ErrLimit when MaxWaitingDevices devices match where.
func checkWaiting(ctx context.Context, tx *sql.Tx, where string, args ...any) error {
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM member_devices WHERE `+where, args...).Scan(&n); err != nil {
		return fmt.Errorf("count waiting devices: %w", err)
	}
	if n >= MaxWaitingDevices {
		return fmt.Errorf("%w: %d devices are waiting for approval already", ErrLimit, n)
	}
	return nil
}

func (d *DB) RegisterInvitationDevice(ctx context.Context, dev *domain.Device, now time.Time) error {
	switch {
	case dev.Invitation == "" || dev.Member != "" || dev.Provider == "" || dev.Subject == "" || dev.Credential == "":
		return errors.New("store: register invitation device: invitation, account and credential are required, and no member")
	case dev.Status != domain.DevicePending || dev.ApprovedBy != "":
		return fmt.Errorf("store: register invitation device: it waits for approval, so it is pending, not %q", dev.Status)
	}
	nowNS, err := encodeTime(now)
	if err != nil {
		return fmt.Errorf("store: register invitation device: %w", err)
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin register invitation device: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err = deleteExpiredInvitationDevices(ctx, tx, nowNS); err != nil {
		return err
	}
	if err = checkWaiting(ctx, tx, `invitation_id = ?`, dev.Invitation); err != nil {
		return fmt.Errorf("store: register invitation device: invitation %s: %w", dev.Invitation, err)
	}
	// Nothing is inserted unless the invitation is open and the account
	// is no member's yet.
	err = insertDevice(ctx, tx, dev,
		`EXISTS (SELECT 1 FROM identity_invitations WHERE id = ? AND consumed_at IS NULL AND expires_at > ?)
		 AND NOT EXISTS (SELECT 1 FROM member_identities WHERE provider = ? AND subject = ?)`,
		dev.Invitation, nowNS, dev.Provider, dev.Subject)
	if errors.Is(err, ErrNotFound) {
		return fmt.Errorf("store: register invitation device: invitation %s is used or expired, or %s %s is a member already: %w",
			dev.Invitation, dev.Provider, dev.Subject, ErrNotFound)
	}
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit register invitation device: %w", err)
	}
	return nil
}

// insertDevice inserts dev when the condition cond, with args, holds, and
// returns ErrNotFound when it does not.
func insertDevice(ctx context.Context, q execer, dev *domain.Device, cond string, args ...any) error {
	id, ts, err := prepareCreate("dev", dev.CreatedAt)
	if err != nil {
		return err
	}
	createdAt, err := encodeTime(ts)
	if err != nil {
		return fmt.Errorf("store: register device: %w", err)
	}
	code := ""
	if dev.Status.AwaitsApproval() {
		code = ApprovalCode(dev.Credential)
	}
	var member, invitation any
	if dev.Member != "" {
		member = dev.Member
	}
	if dev.Invitation != "" {
		invitation = dev.Invitation
	}
	err = notFoundOnZeroRows(q.ExecContext(ctx,
		`INSERT INTO member_devices (id, member_id, invitation_id, provider, subject, email, login, name, credential,
		                             label, status, approval_code, created_at, approved_by)
		 SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ? WHERE `+cond,
		append([]any{id, member, invitation, dev.Provider, dev.Subject, dev.Email, dev.Login, dev.Name, dev.Credential,
			dev.Label, dev.Status, code, createdAt, dev.ApprovedBy}, args...)...,
	))
	switch {
	case errors.Is(err, ErrNotFound):
		return err
	case err != nil:
		return fmt.Errorf("store: register device: %w", mapConstraint(err, ErrNotFound))
	}
	dev.ID, dev.ApprovalCode, dev.CreatedAt, dev.LastSeenAt = domain.DeviceID(id), code, ts, nil
	return nil
}

// deleteExpiredInvitationDevices deletes the devices waiting on an
// invitation that expired at nowNS.
func deleteExpiredInvitationDevices(ctx context.Context, q execer, nowNS int64) error {
	if _, err := q.ExecContext(ctx,
		`DELETE FROM member_devices WHERE invitation_id IN
		 (SELECT id FROM identity_invitations WHERE expires_at <= ?)`, nowNS); err != nil {
		return fmt.Errorf("store: delete devices of expired invitations: %w", err)
	}
	return nil
}

// openInvitationDevice is the condition that leaves out a device waiting
// on an invitation that expired at the time bound to its one parameter.
const openInvitationDevice = `(invitation_id IS NULL OR invitation_id IN
	(SELECT id FROM identity_invitations WHERE expires_at > ?))`

func (d *DB) getDevice(ctx context.Context, op, where string, arg any) (*domain.Device, error) {
	dev, err := scanDevice(d.db.QueryRowContext(ctx,
		`SELECT `+deviceCols+` FROM member_devices WHERE `+where, arg))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: %s: %w", op, err)
	}
	return dev, nil
}

func (d *DB) GetDevice(ctx context.Context, id domain.DeviceID) (*domain.Device, error) {
	return d.getDevice(ctx, "get device", `id = ?`, id)
}

func (d *DB) GetDeviceByCredential(ctx context.Context, credential string) (*domain.Device, error) {
	return d.getDevice(ctx, "get device by credential", `credential = ?`, credential)
}

func (d *DB) GetDeviceByApprovalCode(ctx context.Context, code string) (*domain.Device, error) {
	code = normalizeApprovalCode(code)
	if code == "" {
		return nil, ErrNotFound
	}
	now, err := encodeTime(time.Now())
	if err != nil {
		return nil, fmt.Errorf("store: get device by approval code: %w", err)
	}
	rows, err := d.db.QueryContext(ctx,
		`SELECT `+deviceCols+` FROM member_devices WHERE approval_code = ? AND `+openInvitationDevice+` LIMIT 2`,
		code, now)
	if err != nil {
		return nil, fmt.Errorf("store: get device by approval code: %w", err)
	}
	devs, err := collect(rows, scanDevice)
	switch {
	case err != nil:
		return nil, err
	case len(devs) == 0:
		return nil, ErrNotFound
	case len(devs) > 1:
		return nil, fmt.Errorf("%w: approval code %s names more than one waiting device", ErrConflict, code)
	}
	return devs[0], nil
}

func (d *DB) ListDevices(ctx context.Context, member domain.MemberID) ([]*domain.Device, error) {
	now, err := encodeTime(time.Now())
	if err != nil {
		return nil, fmt.Errorf("store: list devices: %w", err)
	}
	rows, err := d.db.QueryContext(ctx,
		`SELECT `+deviceCols+` FROM member_devices
		 WHERE (? = '' OR member_id = ?) AND `+openInvitationDevice+` ORDER BY created_at, id`, member, member, now)
	if err != nil {
		return nil, fmt.Errorf("store: list devices: %w", err)
	}
	return collect(rows, scanDevice)
}

func (d *DB) ApproveDevice(ctx context.Context, id domain.DeviceID, approver domain.MemberID) error {
	err := notFoundOnZeroRows(d.db.ExecContext(ctx,
		`UPDATE member_devices SET status = 'approved', approval_code = '', approved_by = ?
		 WHERE id = ? AND member_id IS NOT NULL AND status IN ('registered', 'pending')`, approver, id))
	if err != nil && !errors.Is(err, ErrNotFound) {
		err = fmt.Errorf("store: approve device: %w", err)
	}
	return err
}

func (d *DB) RevokeDevice(ctx context.Context, id domain.DeviceID) error {
	err := notFoundOnZeroRows(d.db.ExecContext(ctx,
		`UPDATE member_devices SET status = 'revoked', approval_code = ''
		 WHERE id = ? AND status <> 'revoked'`, id))
	if err != nil && !errors.Is(err, ErrNotFound) {
		err = fmt.Errorf("store: revoke device: %w", err)
	}
	return err
}

func (d *DB) TouchDevice(ctx context.Context, id domain.DeviceID, at time.Time) error {
	seen, err := encodeTime(at)
	if err != nil {
		return fmt.Errorf("store: touch device: %w", err)
	}
	err = notFoundOnZeroRows(d.db.ExecContext(ctx,
		`UPDATE member_devices SET last_seen_at = ? WHERE id = ?`, seen, id))
	if err != nil && !errors.Is(err, ErrNotFound) {
		err = fmt.Errorf("store: touch device: %w", err)
	}
	return err
}
