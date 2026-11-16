package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// ServerUpdateStore persists the server's own self-update state: the one
// update waiting for an idle moment, and the outcome of the last attempt.
// Both survive the restart an update causes, which is the point - the
// binary that answers the next status call is not the one that scheduled
// the update.
type ServerUpdateStore interface {
	// GetServerUpdate reads the state. A server that has never been asked
	// to update itself reports a zero ServerUpdate, not ErrNotFound.
	GetServerUpdate(ctx context.Context) (ServerUpdate, error)
	// SetPendingServerUpdate records the one pending update, replacing any
	// earlier one. A nil pending clears it (cancel, or an attempt that
	// finished).
	SetPendingServerUpdate(ctx context.Context, pending *PendingServerUpdate) error
	// SchedulePendingServerUpdate records pending only while none is
	// pending, reporting whether it did, so it never replaces an update a
	// member scheduled.
	SchedulePendingServerUpdate(ctx context.Context, pending PendingServerUpdate) (bool, error)
	// SetLastServerUpdate records the outcome of an attempt and clears the
	// pending row in the same write, so a finished attempt can never be
	// retried by the next idle tick.
	SetLastServerUpdate(ctx context.Context, last ServerUpdateAttempt) error
	// SchemaVersion is the highest migration applied to this database.
	SchemaVersion(ctx context.Context) (int, error)
	// RaiseDeclinedServerUpdate records declined as the build the server
	// no longer schedules by itself, unless a higher build is declined
	// already: the mark only rises, in one statement.
	RaiseDeclinedServerUpdate(ctx context.Context, declined DeclinedServerUpdate) error
	// RetirePendingServerUpdate records last and clears the pending row
	// only while it is still pending, reporting whether it was.
	RetirePendingServerUpdate(ctx context.Context, pending PendingServerUpdate, last ServerUpdateAttempt) (bool, error)
	// ReleaseChannel is the release channel the server follows, "" while
	// a new server has not chosen one.
	ReleaseChannel(ctx context.Context) (string, error)
	// SetReleaseChannel saves the release channel.
	SetReleaseChannel(ctx context.Context, channel string) error
	// ChooseReleaseChannel saves channel only while none is chosen, and
	// returns the channel in effect, so a choice made meanwhile stands.
	ChooseReleaseChannel(ctx context.Context, channel string) (string, error)
}

// Outcome values of ServerUpdateAttempt.
const (
	// ServerUpdateApplied: the new binary booted on the replaced files.
	ServerUpdateApplied = "applied"
	// ServerUpdateFailed: nothing was replaced; Detail says why.
	ServerUpdateFailed = "failed"
	// ServerUpdateRolledBack: the new binary did not boot and the previous
	// one was restored; Detail says why.
	ServerUpdateRolledBack = "rolled_back"
)

// ServerUpdate is the whole state. Each field is nil when unset.
type ServerUpdate struct {
	Pending  *PendingServerUpdate
	Last     *ServerUpdateAttempt
	Declined *DeclinedServerUpdate
}

// DeclinedServerUpdate is the build the server no longer schedules by
// itself: a super admin cancelled it or rolled back from it, or it
// failed. Build is always positive.
type DeclinedServerUpdate struct {
	Version string
	Build   int
	Hotfix  int
}

// PendingServerUpdate is one update waiting for an idle server.
type PendingServerUpdate struct {
	Version     string
	RequestedBy domain.MemberID
	RequestedAt time.Time
}

// ServerUpdateAttempt is the outcome of one update attempt.
type ServerUpdateAttempt struct {
	Version string
	Outcome string
	Detail  string
	At      time.Time
}

func (d *DB) GetServerUpdate(ctx context.Context) (ServerUpdate, error) {
	var (
		out                  ServerUpdate
		pendingVersion       string
		pendingBy            string
		pendingAt            int64
		lastVersion, outcome string
		detail               string
		lastAt               int64
		declined             DeclinedServerUpdate
	)
	err := d.db.QueryRowContext(ctx,
		`SELECT pending_version, pending_requested_by, pending_requested_at,
		        last_version, last_outcome, last_detail, last_at,
		        declined_version, declined_build, declined_hotfix
		 FROM server_update_state WHERE id = 1`,
	).Scan(&pendingVersion, &pendingBy, &pendingAt, &lastVersion, &outcome, &detail, &lastAt,
		&declined.Version, &declined.Build, &declined.Hotfix)
	if errors.Is(err, sql.ErrNoRows) {
		return ServerUpdate{}, nil
	}
	if err != nil {
		return ServerUpdate{}, fmt.Errorf("store: read server update state: %w", err)
	}
	if pendingVersion != "" {
		out.Pending = &PendingServerUpdate{
			Version:     pendingVersion,
			RequestedBy: domain.MemberID(pendingBy),
			RequestedAt: decodeTime(pendingAt),
		}
	}
	if declined.Version != "" {
		out.Declined = &declined
	}
	if outcome != "" {
		out.Last = &ServerUpdateAttempt{
			Version: lastVersion,
			Outcome: outcome,
			Detail:  detail,
			At:      decodeTime(lastAt),
		}
	}
	return out, nil
}

func (d *DB) SetPendingServerUpdate(ctx context.Context, pending *PendingServerUpdate) error {
	var (
		version string
		by      string
		at      int64
	)
	if pending != nil {
		if pending.Version == "" {
			return errors.New("store: pending server update requires a version")
		}
		stamp, err := encodeTime(pending.RequestedAt)
		if err != nil {
			return fmt.Errorf("store: pending server update: %w", err)
		}
		version, by, at = pending.Version, string(pending.RequestedBy), stamp
	}
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO server_update_state (id, pending_version, pending_requested_by, pending_requested_at)
		 VALUES (1, ?, ?, ?)
		 ON CONFLICT (id) DO UPDATE SET
		   pending_version = excluded.pending_version,
		   pending_requested_by = excluded.pending_requested_by,
		   pending_requested_at = excluded.pending_requested_at`,
		version, by, at)
	if err != nil {
		return fmt.Errorf("store: write pending server update: %w", err)
	}
	return nil
}

func (d *DB) RaiseDeclinedServerUpdate(ctx context.Context, declined DeclinedServerUpdate) error {
	if declined.Version == "" || declined.Build < 1 {
		return errors.New("store: declined server update requires a version and a build")
	}
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO server_update_state (id, declined_version, declined_build, declined_hotfix)
		 VALUES (1, ?, ?, ?)
		 ON CONFLICT (id) DO UPDATE SET
		   declined_version = excluded.declined_version,
		   declined_build = excluded.declined_build,
		   declined_hotfix = excluded.declined_hotfix
		 WHERE declined_build < excluded.declined_build
		    OR declined_build = excluded.declined_build AND declined_hotfix <= excluded.declined_hotfix`,
		declined.Version, declined.Build, declined.Hotfix)
	if err != nil {
		return fmt.Errorf("store: write declined server update: %w", err)
	}
	return nil
}

func (d *DB) RetirePendingServerUpdate(ctx context.Context, pending PendingServerUpdate, last ServerUpdateAttempt) (bool, error) {
	at, err := encodeTime(pending.RequestedAt)
	if err != nil {
		return false, fmt.Errorf("store: retire server update: %w", err)
	}
	stamp, err := encodeTime(last.At)
	if err != nil {
		return false, fmt.Errorf("store: retire server update: %w", err)
	}
	res, err := d.db.ExecContext(ctx,
		`UPDATE server_update_state SET
		   last_version = ?, last_outcome = ?, last_detail = ?, last_at = ?,
		   pending_version = '', pending_requested_by = '', pending_requested_at = 0
		 WHERE id = 1 AND pending_version = ? AND pending_requested_by = ? AND pending_requested_at = ?`,
		last.Version, last.Outcome, last.Detail, stamp, pending.Version, string(pending.RequestedBy), at)
	if err != nil {
		return false, fmt.Errorf("store: retire server update: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: retire server update: %w", err)
	}
	return n == 1, nil
}

func (d *DB) SchedulePendingServerUpdate(ctx context.Context, pending PendingServerUpdate) (bool, error) {
	if pending.Version == "" {
		return false, errors.New("store: pending server update requires a version")
	}
	stamp, err := encodeTime(pending.RequestedAt)
	if err != nil {
		return false, fmt.Errorf("store: pending server update: %w", err)
	}
	res, err := d.db.ExecContext(ctx,
		`INSERT INTO server_update_state (id, pending_version, pending_requested_by, pending_requested_at)
		 VALUES (1, ?, ?, ?)
		 ON CONFLICT (id) DO UPDATE SET
		   pending_version = excluded.pending_version,
		   pending_requested_by = excluded.pending_requested_by,
		   pending_requested_at = excluded.pending_requested_at
		 WHERE pending_version = ''`,
		pending.Version, string(pending.RequestedBy), stamp)
	if err != nil {
		return false, fmt.Errorf("store: schedule server update: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: schedule server update: %w", err)
	}
	return n == 1, nil
}

func (d *DB) SetLastServerUpdate(ctx context.Context, last ServerUpdateAttempt) error {
	if last.Outcome == "" {
		return errors.New("store: server update attempt requires an outcome")
	}
	stamp, err := encodeTime(last.At)
	if err != nil {
		return fmt.Errorf("store: server update attempt: %w", err)
	}
	_, err = d.db.ExecContext(ctx,
		`INSERT INTO server_update_state (id, last_version, last_outcome, last_detail, last_at)
		 VALUES (1, ?, ?, ?, ?)
		 ON CONFLICT (id) DO UPDATE SET
		   last_version = excluded.last_version,
		   last_outcome = excluded.last_outcome,
		   last_detail = excluded.last_detail,
		   last_at = excluded.last_at,
		   pending_version = '',
		   pending_requested_by = '',
		   pending_requested_at = 0`,
		last.Version, last.Outcome, last.Detail, stamp)
	if err != nil {
		return fmt.Errorf("store: write server update attempt: %w", err)
	}
	return nil
}

func (d *DB) ReleaseChannel(ctx context.Context) (string, error) {
	return readReleaseChannel(ctx, d.db)
}

func (d *DB) SetReleaseChannel(ctx context.Context, channel string) error {
	return writeReleaseChannel(ctx, d.db, channel, false)
}

func (d *DB) ChooseReleaseChannel(ctx context.Context, channel string) (string, error) {
	if err := writeReleaseChannel(ctx, d.db, channel, true); err != nil {
		return "", err
	}
	return d.ReleaseChannel(ctx)
}

func readReleaseChannel(ctx context.Context, db *sql.DB) (string, error) {
	var channel string
	err := db.QueryRowContext(ctx, `SELECT release_channel FROM server_update_state WHERE id = 1`).Scan(&channel)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("store: read release channel: %w", err)
	}
	return channel, nil
}

// writeReleaseChannel saves channel, or with unset only while none is
// chosen.
func writeReleaseChannel(ctx context.Context, db *sql.DB, channel string, unset bool) error {
	if channel == "" {
		return errors.New("store: release channel requires a channel")
	}
	query := `INSERT INTO server_update_state (id, release_channel) VALUES (1, ?)
		 ON CONFLICT (id) DO UPDATE SET release_channel = excluded.release_channel`
	if unset {
		query += ` WHERE release_channel = ''`
	}
	if _, err := db.ExecContext(ctx, query, channel); err != nil {
		return fmt.Errorf("store: write release channel: %w", err)
	}
	return nil
}

// releaseChannelSchema is the migration that adds release_channel.
const releaseChannelSchema = 113

// ChannelFile is the release channel of a database opened without
// migrating it, for `aether-server update` on the host while the server
// runs; the server reads a change at its next release check.
type ChannelFile struct {
	path string
	db   *sql.DB
}

// OpenChannelFile opens the release channel of the database at path. A
// database from before release channels reads as beta, which its
// migration gives it, and cannot be changed until the server has
// migrated it.
func OpenChannelFile(path string) (*ChannelFile, error) {
	schema, err := ReadSchemaVersion(path)
	if err != nil {
		return nil, err
	}
	f := &ChannelFile{path: path}
	if schema < releaseChannelSchema {
		return f, nil
	}
	if f.db, err = sql.Open("sqlite", "file:"+url.PathEscape(path)+"?_pragma=busy_timeout(5000)"); err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	return f, nil
}

func (f *ChannelFile) ReleaseChannel(ctx context.Context) (string, error) {
	if f.db == nil {
		return "beta", nil
	}
	return readReleaseChannel(ctx, f.db)
}

func (f *ChannelFile) SetReleaseChannel(ctx context.Context, channel string) error {
	if f.db == nil {
		return f.predates()
	}
	return writeReleaseChannel(ctx, f.db, channel, false)
}

func (f *ChannelFile) ChooseReleaseChannel(ctx context.Context, channel string) (string, error) {
	if f.db == nil {
		return f.ReleaseChannel(ctx)
	}
	if err := writeReleaseChannel(ctx, f.db, channel, true); err != nil {
		return "", err
	}
	return f.ReleaseChannel(ctx)
}

func (f *ChannelFile) predates() error {
	return fmt.Errorf("store: the database at %s is at a schema from before release channels (below %d); restart aether-server onto this release first", f.path, releaseChannelSchema)
}

// Close closes the database.
func (f *ChannelFile) Close() error {
	if f.db == nil {
		return nil
	}
	return f.db.Close()
}

func (d *DB) SchemaVersion(ctx context.Context) (int, error) {
	var v int
	if err := d.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&v); err != nil {
		return 0, fmt.Errorf("store: read schema version: %w", err)
	}
	return v, nil
}

// SnapshotFile writes a consistent copy of the database at db to path,
// which must not exist, without opening it as a store. An update takes one
// once the server has closed the store and before a release migrates the
// schema, so a rollback can restore it.
func SnapshotFile(db, path string) error {
	conn, err := sql.Open("sqlite", "file:"+url.PathEscape(db)+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return fmt.Errorf("store: open %s: %w", db, err)
	}
	defer conn.Close() //nolint:errcheck // read-only
	if _, err := conn.Exec(`VACUUM INTO ?`, path); err != nil {
		return fmt.Errorf("store: snapshot %s to %s: %w", db, path, err)
	}
	return nil
}

// ReadSchemaVersion reads the schema version of the database at path
// without opening it as a store, so without migrating it. A rollback uses
// it to decide whether the new binary already migrated the database.
func ReadSchemaVersion(path string) (int, error) {
	db, err := sql.Open("sqlite", "file:"+url.PathEscape(path)+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return 0, fmt.Errorf("store: open %s: %w", path, err)
	}
	defer db.Close() //nolint:errcheck // read-only
	var v int
	if err := db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&v); err != nil {
		return 0, fmt.Errorf("store: read schema version of %s: %w", path, err)
	}
	return v, nil
}
