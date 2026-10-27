package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// MaxPreferences is how many preferences one member may keep.
const MaxPreferences = 200

// ErrPreferenceCap means the member already keeps MaxPreferences.
var ErrPreferenceCap = fmt.Errorf("store: %d preferences is the limit; forget one first", MaxPreferences)

// Preference is one of a member's private assistant preferences. Every
// query is keyed by the member, so another member's id reads as not found.
type Preference struct {
	ID          string
	MemberID    domain.MemberID
	WorkspaceID string
	Text        string
	Enabled     bool
	Source      string
	SourceRef   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	LastUsedAt  time.Time
}

type PreferenceStore interface {
	// ListPreferences returns the member's preferences, newest first.
	ListPreferences(ctx context.Context, member domain.MemberID) ([]Preference, error)
	// GetPreference returns ErrNotFound unless member owns id.
	GetPreference(ctx context.Context, member domain.MemberID, id string) (Preference, error)
	// AddPreference fills p's id and times; ErrPreferenceCap at the limit.
	AddPreference(ctx context.Context, p *Preference) error
	// UpdatePreference applies edit to the member's preference id and
	// stores it. commit runs with the old and new rows before the
	// transaction commits, and its error aborts it.
	UpdatePreference(ctx context.Context, member domain.MemberID, id string, edit func(*Preference) error, commit func(old, cur Preference) error) (old, cur Preference, err error)
	// DeletePreference removes the member's preference id; commit runs
	// before the transaction commits, and its error aborts it.
	DeletePreference(ctx context.Context, member domain.MemberID, id string, commit func(Preference) error) (Preference, error)
	// TouchPreferences sets the last use of the member's preferences ids.
	TouchPreferences(ctx context.Context, member domain.MemberID, ids []string, at time.Time) error
	// ApplyPreferenceTombstones deletes the preferences in deleted and
	// those in edited last updated before the time edited holds for them.
	ApplyPreferenceTombstones(ctx context.Context, deleted []string, edited map[string]time.Time) (int64, error)
}

var _ PreferenceStore = (*DB)(nil)

const preferenceColumns = `id, member_id, COALESCE(workspace_id, ''), text, enabled, source, source_ref, created_at, updated_at, COALESCE(last_used_at, 0)`

func scanPreference(row interface{ Scan(...any) error }) (Preference, error) {
	var p Preference
	var created, updated, used int64
	err := row.Scan(&p.ID, &p.MemberID, &p.WorkspaceID, &p.Text, &p.Enabled, &p.Source, &p.SourceRef, &created, &updated, &used)
	if err != nil {
		return Preference{}, err
	}
	p.CreatedAt, p.UpdatedAt = time.UnixMilli(created).UTC(), time.UnixMilli(updated).UTC()
	if used > 0 {
		p.LastUsedAt = time.UnixMilli(used).UTC()
	}
	return p, nil
}

func (d *DB) ListPreferences(ctx context.Context, member domain.MemberID) ([]Preference, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT `+preferenceColumns+` FROM member_preferences WHERE member_id = ? ORDER BY updated_at DESC, rowid DESC`, member)
	if err != nil {
		return nil, fmt.Errorf("store: list preferences: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only iteration
	var out []Preference
	for rows.Next() {
		p, err := scanPreference(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list preferences: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list preferences: %w", err)
	}
	return out, nil
}

func (d *DB) GetPreference(ctx context.Context, member domain.MemberID, id string) (Preference, error) {
	p, err := scanPreference(d.db.QueryRowContext(ctx,
		`SELECT `+preferenceColumns+` FROM member_preferences WHERE member_id = ? AND id = ?`, member, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Preference{}, ErrNotFound
	}
	if err != nil {
		return Preference{}, fmt.Errorf("store: get preference: %w", err)
	}
	return p, nil
}

func (d *DB) AddPreference(ctx context.Context, p *Preference) error {
	id, err := newID("pref")
	if err != nil {
		return err
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: add preference: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM member_preferences WHERE member_id = ?`, p.MemberID).Scan(&count); err != nil {
		return fmt.Errorf("store: add preference: %w", err)
	}
	if count >= MaxPreferences {
		return ErrPreferenceCap
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	if _, err = tx.ExecContext(ctx,
		`INSERT INTO member_preferences (id, member_id, workspace_id, text, enabled, source, source_ref, created_at, updated_at)
		 VALUES (?, ?, NULLIF(?, ''), ?, ?, ?, ?, ?, ?)`,
		id, p.MemberID, p.WorkspaceID, p.Text, p.Enabled, p.Source, p.SourceRef, now.UnixMilli(), now.UnixMilli()); err != nil {
		return fmt.Errorf("store: add preference: %w", mapConstraint(err, ErrNotFound))
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("store: add preference: %w", err)
	}
	p.ID, p.CreatedAt, p.UpdatedAt, p.LastUsedAt = id, now, now, time.Time{}
	return nil
}

func (d *DB) UpdatePreference(ctx context.Context, member domain.MemberID, id string, edit func(*Preference) error, commit func(old, cur Preference) error) (Preference, Preference, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return Preference{}, Preference{}, fmt.Errorf("store: update preference: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	old, err := scanPreference(tx.QueryRowContext(ctx,
		`SELECT `+preferenceColumns+` FROM member_preferences WHERE member_id = ? AND id = ?`, member, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Preference{}, Preference{}, ErrNotFound
	}
	if err != nil {
		return Preference{}, Preference{}, fmt.Errorf("store: update preference: %w", err)
	}
	cur := old
	if err = edit(&cur); err != nil {
		return Preference{}, Preference{}, err
	}
	// Each change gets a later updated_at, which the tombstone of a text
	// change orders restored copies by.
	cur.UpdatedAt = time.Now().UTC().Truncate(time.Millisecond)
	if !cur.UpdatedAt.After(old.UpdatedAt) {
		cur.UpdatedAt = old.UpdatedAt.Add(time.Millisecond)
	}
	if err = commit(old, cur); err != nil {
		return Preference{}, Preference{}, err
	}
	if _, err = tx.ExecContext(ctx,
		`UPDATE member_preferences SET workspace_id = NULLIF(?, ''), text = ?, enabled = ?, source = ?, source_ref = ?, updated_at = ?
		 WHERE member_id = ? AND id = ?`,
		cur.WorkspaceID, cur.Text, cur.Enabled, cur.Source, cur.SourceRef, cur.UpdatedAt.UnixMilli(), member, id); err != nil {
		return Preference{}, Preference{}, fmt.Errorf("store: update preference: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return Preference{}, Preference{}, fmt.Errorf("store: update preference: %w", err)
	}
	if cur.Text != old.Text {
		if err = d.checkpoint(ctx); err != nil {
			return old, cur, fmt.Errorf("store: preference %s updated, but clearing its old text from the write-ahead log failed: %w", id, err)
		}
	}
	return old, cur, nil
}

func (d *DB) DeletePreference(ctx context.Context, member domain.MemberID, id string, commit func(Preference) error) (Preference, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return Preference{}, fmt.Errorf("store: delete preference: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	p, err := scanPreference(tx.QueryRowContext(ctx,
		`DELETE FROM member_preferences WHERE member_id = ? AND id = ? RETURNING `+preferenceColumns, member, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Preference{}, ErrNotFound
	}
	if err != nil {
		return Preference{}, fmt.Errorf("store: delete preference: %w", err)
	}
	if err = commit(p); err != nil {
		return Preference{}, err
	}
	if err = tx.Commit(); err != nil {
		return Preference{}, fmt.Errorf("store: delete preference: %w", err)
	}
	if err = d.checkpoint(ctx); err != nil {
		return p, fmt.Errorf("store: preference %s deleted, but clearing its text from the write-ahead log failed: %w", id, err)
	}
	return p, nil
}

// checkpoint copies the write-ahead log into the database and truncates it,
// so text secure_delete overwrote in the database is gone from the log too.
func (d *DB) checkpoint(ctx context.Context) error {
	var busy, frames, done int
	if err := d.db.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &frames, &done); err != nil {
		return err
	}
	if busy != 0 {
		return errors.New("a reader held the log; the next checkpoint clears it")
	}
	return nil
}

func (d *DB) TouchPreferences(ctx context.Context, member domain.MemberID, ids []string, at time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	args := []any{at.UnixMilli(), member}
	for _, id := range ids {
		args = append(args, id)
	}
	_, err := d.db.ExecContext(ctx,
		`UPDATE member_preferences SET last_used_at = ? WHERE member_id = ? AND id IN (?`+strings.Repeat(", ?", len(ids)-1)+`)`, args...)
	if err != nil {
		return fmt.Errorf("store: touch preferences: %w", err)
	}
	return nil
}

func (d *DB) ApplyPreferenceTombstones(ctx context.Context, deleted []string, edited map[string]time.Time) (int64, error) {
	if len(deleted) == 0 && len(edited) == 0 {
		return 0, nil
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: apply preference tombstones: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	var removed int64
	exec := func(query string, args ...any) error {
		res, xerr := tx.ExecContext(ctx, query, args...)
		if xerr != nil {
			return xerr
		}
		n, xerr := res.RowsAffected()
		removed += n
		return xerr
	}
	for _, id := range deleted {
		if err = exec(`DELETE FROM member_preferences WHERE id = ?`, id); err != nil {
			break
		}
	}
	for id, at := range edited {
		if err != nil {
			break
		}
		err = exec(`DELETE FROM member_preferences WHERE id = ? AND updated_at < ?`, id, at.UnixMilli())
	}
	if err != nil {
		return 0, fmt.Errorf("store: apply preference tombstones: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: apply preference tombstones: %w", err)
	}
	if removed > 0 {
		if err = d.checkpoint(ctx); err != nil {
			return removed, fmt.Errorf("store: apply preference tombstones: %w", err)
		}
	}
	return removed, nil
}
