package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// approvalAlphabet leaves out 0, 1, I and O, which people misread.
const approvalAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// approvalCodeLength is 8 characters, 40 bits. The code names a pending
// device; it is not a secret, because approving still needs an approved
// connection of the same member or an admin.
const approvalCodeLength = 8

func newApprovalCode() string {
	var b [approvalCodeLength]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never fails
	for i := range b {
		b[i] = approvalAlphabet[int(b[i])%len(approvalAlphabet)]
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

const deviceCols = `id, member_id, kind, credential, label, status, approval_code, created_at,
	last_seen_at, approved_by`

func scanDevice(row interface{ Scan(...any) error }) (*domain.Device, error) {
	var (
		dev       domain.Device
		createdAt int64
		lastSeen  *int64
	)
	if err := row.Scan(&dev.ID, &dev.Member, &dev.Kind, &dev.Credential, &dev.Label, &dev.Status,
		&dev.ApprovalCode, &createdAt, &lastSeen, &dev.ApprovedBy); err != nil {
		return nil, err
	}
	dev.CreatedAt = decodeTime(createdAt)
	dev.LastSeenAt = decodeTimePtr(lastSeen)
	return &dev, nil
}

func (d *DB) RegisterDevice(ctx context.Context, dev *domain.Device, requireApproval bool) error {
	if dev.Member == "" || dev.Credential == "" {
		return errors.New("store: register device: member and credential are required")
	}
	if dev.Kind != domain.DeviceSSH && dev.Kind != domain.DeviceBrowser {
		return fmt.Errorf("store: register device: invalid kind %q", dev.Kind)
	}
	id, ts, err := prepareCreate(dev.CreatedAt)
	if err != nil {
		return err
	}
	createdAt, err := encodeTime(ts)
	if err != nil {
		return fmt.Errorf("store: register device: %w", err)
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin register device: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var hasDevice bool
	if err = tx.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM member_devices WHERE member_id = ?)`, dev.Member).Scan(&hasDevice); err != nil {
		return fmt.Errorf("store: register device: %w", err)
	}
	status, code := domain.DeviceApproved, ""
	if hasDevice && requireApproval {
		status = domain.DevicePending
		if code, err = unusedApprovalCode(ctx, tx); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO member_devices (id, member_id, kind, credential, label, status, approval_code, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, dev.Member, dev.Kind, dev.Credential, dev.Label, status, code, createdAt,
	); err != nil {
		return fmt.Errorf("store: register device: %w", mapConstraint(err, ErrNotFound))
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit register device: %w", err)
	}
	dev.ID, dev.Status, dev.ApprovalCode, dev.CreatedAt = domain.DeviceID(id), status, code, ts
	dev.LastSeenAt, dev.ApprovedBy = nil, ""
	return nil
}

// unusedApprovalCode draws codes until one names no pending device. A
// collision needs thousands of pending devices to become likely at all, so
// a few draws suffice.
func unusedApprovalCode(ctx context.Context, tx *sql.Tx) (string, error) {
	for range 4 {
		code := newApprovalCode()
		var taken bool
		if err := tx.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM member_devices WHERE approval_code = ?)`, code).Scan(&taken); err != nil {
			return "", fmt.Errorf("store: register device: %w", err)
		}
		if !taken {
			return code, nil
		}
	}
	return "", errors.New("store: register device: no unused approval code")
}

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
	return d.getDevice(ctx, "get device by approval code", `approval_code = ?`, code)
}

func (d *DB) ListDevices(ctx context.Context, member domain.MemberID) ([]*domain.Device, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT `+deviceCols+` FROM member_devices
		 WHERE ? = '' OR member_id = ? ORDER BY created_at, id`, member, member)
	if err != nil {
		return nil, fmt.Errorf("store: list devices: %w", err)
	}
	return collect(rows, scanDevice)
}

func (d *DB) ApproveDevice(ctx context.Context, id domain.DeviceID, approver domain.MemberID) error {
	err := notFoundOnZeroRows(d.db.ExecContext(ctx,
		`UPDATE member_devices SET status = 'approved', approval_code = '', approved_by = ?
		 WHERE id = ? AND status = 'pending'`, approver, id))
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
