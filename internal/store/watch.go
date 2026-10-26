package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// Watch is a member's durable subscription to one target. Fired holds,
// per condition, the dedup keys of its notices last found current. Version
// is bumped by every write; UpdateWatch and CommitWatchEvaluation refuse a
// write whose Version is not the stored one.
type Watch struct {
	ID             string
	MemberID       domain.MemberID
	WorkspaceID    domain.WorkspaceID
	TargetKind     string
	RunID          domain.RunID
	MissionID      domain.MissionID
	Repo           string
	Number         int
	TargetTitle    string
	Conditions     []string
	Label          string
	Source         string
	State          string
	Error          string
	Failures       int
	Observed       json.RawMessage
	Fired          map[string]string
	IdempotencyKey string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	ExpiresAt      time.Time
	LastCheckAt    *time.Time
	NextCheckAt    *time.Time
	EndedAt        *time.Time
	Version        int64
}

// WatchFilter narrows ListWatches; zero fields match everything.
type WatchFilter struct {
	MemberID   domain.MemberID
	State      string
	TargetKind string
	RunID      domain.RunID
	MissionID  domain.MissionID
}

// WatchNotification is one notice a watch recorded. MemberID is the
// watch's owner. PushState is none once push is settled, pending while a
// push is due at PushNextAt, and held while quiet hours hold it until then.
type WatchNotification struct {
	ID           string
	WatchID      string
	MemberID     domain.MemberID
	Condition    string
	Key          string
	Title        string
	Body         string
	Link         string
	CreatedAt    time.Time
	PushState    string
	PushAttempts int
	PushNextAt   *time.Time
	Deliveries   []WatchDelivery
}

// WatchDelivery is one delivery attempt on one channel.
type WatchDelivery struct {
	Channel string
	Status  string
	Detail  string
	At      time.Time
}

// NotifyPrefs is a member's notification preferences. Quiet hours are off
// while QuietTZ is empty.
type NotifyPrefs struct {
	Push       bool
	QuietStart string
	QuietEnd   string
	QuietTZ    string
}

// Push states of a watch notification.
const (
	WatchPushNone    = "none"
	WatchPushPending = "pending"
	WatchPushHeld    = "held"
)

// WatchStore persists watches, their notifications and deliveries, each
// member's notification preferences and the watch service's event cursor.
type WatchStore interface {
	// CreateWatch inserts w and sets its ID and Version. A repeated
	// (member, idempotency key) returns ErrConflict.
	CreateWatch(ctx context.Context, w *Watch) error
	GetWatch(ctx context.Context, id string) (*Watch, error)
	GetWatchByIdempotencyKey(ctx context.Context, member domain.MemberID, key string) (*Watch, error)
	ListWatches(ctx context.Context, f WatchFilter) ([]*Watch, error)
	// UpdateWatch writes w's mutable fields if w.Version is still stored,
	// else returns ErrConflict, and advances w.Version.
	UpdateWatch(ctx context.Context, w *Watch) error
	// CommitWatchEvaluation is UpdateWatch plus the notices the evaluation
	// found, in one transaction. A notice whose (watch, condition, key) is
	// already recorded is skipped; the ones recorded are returned with
	// their deliveries.
	CommitWatchEvaluation(ctx context.Context, w *Watch, notices []*WatchNotification) ([]*WatchNotification, error)
	// TouchWatchCheck records a check that changed nothing, leaving
	// Version as it is.
	TouchWatchCheck(ctx context.Context, id string, at time.Time) error
	DeleteWatch(ctx context.Context, id string) error
	// EvaluableWatchIDs lists the active and failing watches on run or on
	// the swarm run integrates, or on mission when run is empty.
	EvaluableWatchIDs(ctx context.Context, run domain.RunID, mission domain.MissionID) ([]string, error)
	// DueWatchIDs lists the active and failing watches whose next check is
	// at or before now, or all of them when all is set.
	DueWatchIDs(ctx context.Context, now time.Time, all bool) ([]string, error)
	// ExpireWatches marks every live watch past its expiry expired and
	// returns them. A paused watch's unsent pushes are dropped as cleared.
	ExpireWatches(ctx context.Context, now time.Time) ([]*Watch, error)
	// PurgeWatches deletes the done and expired watches that ended before
	// cutoff, with their notifications.
	PurgeWatches(ctx context.Context, cutoff time.Time) (int64, error)

	ListWatchNotifications(ctx context.Context, watch string, limit int) ([]*WatchNotification, error)
	GetWatchNotification(ctx context.Context, id string) (*WatchNotification, error)
	// DueWatchPushes lists the notifications whose push is pending or held
	// until now or earlier, oldest first, leaving out paused watches'.
	DueWatchPushes(ctx context.Context, now time.Time) ([]*WatchNotification, error)
	// ReleaseHeldWatchPushes makes member's held pushes due at now, for
	// their quiet hours to be applied again.
	ReleaseHeldWatchPushes(ctx context.Context, member domain.MemberID, now time.Time) error
	// SetWatchPush records a notification's push state and, when d is not
	// nil, replaces its delivery row on d's channel.
	SetWatchPush(ctx context.Context, id, state string, attempts int, next *time.Time, d *WatchDelivery) error

	GetNotifyPrefs(ctx context.Context, member domain.MemberID) (NotifyPrefs, error)
	PutNotifyPrefs(ctx context.Context, member domain.MemberID, p NotifyPrefs) error

	// MissionDeliveryReceipts are the delivery receipts of every
	// integration candidate of mission, keyed by candidate id.
	MissionDeliveryReceipts(ctx context.Context, workspace domain.WorkspaceID, mission domain.MissionID) (map[string]json.RawMessage, error)

	// MissionFinishedAt is when mission last completed or was cancelled,
	// nil while it never has.
	MissionFinishedAt(ctx context.Context, mission domain.MissionID) (*time.Time, error)

	// WatchCursor is the last event sequence the watch service applied;
	// ok is false before it first saved one.
	WatchCursor(ctx context.Context) (seq uint64, ok bool, err error)
	// SetWatchCursor saves seq unless a later one is saved.
	SetWatchCursor(ctx context.Context, seq uint64) error
}

const watchColumns = `id, member_id, COALESCE(workspace_id, ''), target_kind, COALESCE(run_id, ''), COALESCE(mission_id, ''), repo,
	pr_number, target_title, conditions, label, source, state, error, failures, observed, fired,
	COALESCE(idempotency_key, ''), created_at, updated_at, expires_at, last_check_at, next_check_at, ended_at, version`

func scanWatch(row interface{ Scan(...any) error }) (*Watch, error) {
	var (
		w                          Watch
		conditions, observed, fire string
		created, updated, expires  int64
		last, next, ended          *int64
	)
	if err := row.Scan(&w.ID, &w.MemberID, &w.WorkspaceID, &w.TargetKind, &w.RunID, &w.MissionID, &w.Repo,
		&w.Number, &w.TargetTitle, &conditions, &w.Label, &w.Source, &w.State, &w.Error, &w.Failures,
		&observed, &fire, &w.IdempotencyKey, &created, &updated, &expires, &last, &next, &ended, &w.Version); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(conditions), &w.Conditions); err != nil {
		return nil, fmt.Errorf("watch %s conditions: %w", w.ID, err)
	}
	if err := json.Unmarshal([]byte(fire), &w.Fired); err != nil {
		return nil, fmt.Errorf("watch %s fired: %w", w.ID, err)
	}
	w.Observed = json.RawMessage(observed)
	w.CreatedAt, w.UpdatedAt, w.ExpiresAt = decodeTime(created), decodeTime(updated), decodeTime(expires)
	w.LastCheckAt, w.NextCheckAt, w.EndedAt = decodeTimePtr(last), decodeTimePtr(next), decodeTimePtr(ended)
	return &w, nil
}

// watchArgs encodes w's mutable columns, in the order of watchSet.
func watchArgs(w *Watch) ([]any, error) {
	conditions, err := json.Marshal(w.Conditions)
	if err != nil {
		return nil, err
	}
	if w.Fired == nil {
		w.Fired = map[string]string{}
	}
	fired, err := json.Marshal(w.Fired)
	if err != nil {
		return nil, err
	}
	observed := string(w.Observed)
	if observed == "" {
		observed = "{}"
	}
	updated, err := encodeTime(w.UpdatedAt)
	if err != nil {
		return nil, err
	}
	expires, err := encodeTime(w.ExpiresAt)
	if err != nil {
		return nil, err
	}
	last, err := encodeTimePtr(w.LastCheckAt)
	if err != nil {
		return nil, err
	}
	next, err := encodeTimePtr(w.NextCheckAt)
	if err != nil {
		return nil, err
	}
	ended, err := encodeTimePtr(w.EndedAt)
	if err != nil {
		return nil, err
	}
	return []any{w.TargetTitle, string(conditions), w.Label, w.State, w.Error, w.Failures, observed, string(fired),
		updated, expires, last, next, ended}, nil
}

const watchSet = `target_title = ?, conditions = ?, label = ?, state = ?, error = ?, failures = ?, observed = ?, fired = ?,
	updated_at = ?, expires_at = ?, last_check_at = ?, next_check_at = ?, ended_at = ?`

func nullable[T comparable](v T) any {
	var zero T
	if v == zero {
		return nil
	}
	return v
}

func (d *DB) CreateWatch(ctx context.Context, w *Watch) error {
	id, err := newID("wat")
	if err != nil {
		return err
	}
	args, err := watchArgs(w)
	if err != nil {
		return fmt.Errorf("store: create watch: %w", err)
	}
	created, err := encodeTime(w.CreatedAt)
	if err != nil {
		return fmt.Errorf("store: create watch: %w", err)
	}
	_, err = d.db.ExecContext(ctx,
		`INSERT INTO watches (id, member_id, workspace_id, target_kind, run_id, mission_id, repo, pr_number,
		   source, idempotency_key, created_at, target_title, conditions, label, state, error, failures, observed, fired,
		   updated_at, expires_at, last_check_at, next_check_at, ended_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		append([]any{id, w.MemberID, nullable(w.WorkspaceID), w.TargetKind, nullable(w.RunID), nullable(w.MissionID), w.Repo,
			w.Number, w.Source, nullable(w.IdempotencyKey), created}, args...)...)
	if err != nil {
		return fmt.Errorf("store: create watch: %w", mapConstraint(err, ErrNotFound))
	}
	w.ID, w.Version = id, 1
	return nil
}

func (d *DB) GetWatch(ctx context.Context, id string) (*Watch, error) {
	w, err := scanWatch(d.db.QueryRowContext(ctx, `SELECT `+watchColumns+` FROM watches WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: watch %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("store: get watch: %w", err)
	}
	return w, nil
}

func (d *DB) GetWatchByIdempotencyKey(ctx context.Context, member domain.MemberID, key string) (*Watch, error) {
	w, err := scanWatch(d.db.QueryRowContext(ctx,
		`SELECT `+watchColumns+` FROM watches WHERE member_id = ? AND idempotency_key = ?`, member, key))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: watch with idempotency key %q: %w", key, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("store: get watch by idempotency key: %w", err)
	}
	return w, nil
}

func (d *DB) ListWatches(ctx context.Context, f WatchFilter) ([]*Watch, error) {
	var (
		where []string
		args  []any
	)
	for _, c := range []struct {
		column string
		value  string
	}{
		{"member_id", string(f.MemberID)}, {"state", f.State}, {"target_kind", f.TargetKind},
		{"run_id", string(f.RunID)}, {"mission_id", string(f.MissionID)},
	} {
		if c.value != "" {
			where = append(where, c.column+" = ?")
			args = append(args, c.value)
		}
	}
	query := `SELECT ` + watchColumns + ` FROM watches`
	if len(where) > 0 {
		query += ` WHERE ` + strings.Join(where, " AND ")
	}
	rows, err := d.db.QueryContext(ctx, query+` ORDER BY created_at DESC, id`, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list watches: %w", err)
	}
	return collect(rows, scanWatch)
}

func (d *DB) UpdateWatch(ctx context.Context, w *Watch) error {
	return d.updateWatch(ctx, d.db, w)
}

func (d *DB) updateWatch(ctx context.Context, db execer, w *Watch) error {
	args, err := watchArgs(w)
	if err != nil {
		return fmt.Errorf("store: update watch: %w", err)
	}
	res, err := db.ExecContext(ctx, `UPDATE watches SET `+watchSet+`, version = version + 1 WHERE id = ? AND version = ?`,
		append(args, w.ID, w.Version)...)
	if err != nil {
		return fmt.Errorf("store: update watch: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("store: update watch: %w", err)
	} else if n == 0 {
		return fmt.Errorf("store: update watch %s at version %d: %w", w.ID, w.Version, ErrConflict)
	}
	w.Version++
	return nil
}

func (d *DB) CommitWatchEvaluation(ctx context.Context, w *Watch, notices []*WatchNotification) ([]*WatchNotification, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("store: commit watch evaluation: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	var recorded []*WatchNotification
	for _, n := range notices {
		inserted, err := insertWatchNotification(ctx, tx, w.ID, n)
		if err != nil {
			return nil, err
		}
		if inserted {
			n.WatchID, n.MemberID = w.ID, w.MemberID
			recorded = append(recorded, n)
		}
	}
	version := w.Version
	if err := d.updateWatch(ctx, tx, w); err != nil {
		w.Version = version
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		w.Version = version
		return nil, fmt.Errorf("store: commit watch evaluation: %w", err)
	}
	return recorded, nil
}

func insertWatchNotification(ctx context.Context, tx *sql.Tx, watch string, n *WatchNotification) (bool, error) {
	id, err := newID("wno")
	if err != nil {
		return false, err
	}
	created, err := encodeTime(n.CreatedAt)
	if err != nil {
		return false, fmt.Errorf("store: record watch notification: %w", err)
	}
	next, err := encodeTimePtr(n.PushNextAt)
	if err != nil {
		return false, fmt.Errorf("store: record watch notification: %w", err)
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO watch_notifications (id, watch_id, condition, dedup_key, title, body, link, created_at, push_state, push_next_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (watch_id, condition, dedup_key) DO NOTHING`,
		id, watch, n.Condition, n.Key, n.Title, n.Body, n.Link, created, n.PushState, next)
	if err != nil {
		return false, fmt.Errorf("store: record watch notification: %w", err)
	}
	if count, err := res.RowsAffected(); err != nil || count == 0 {
		return false, err
	}
	n.ID = id
	for _, del := range n.Deliveries {
		if err := insertWatchDelivery(ctx, tx, id, del); err != nil {
			return false, err
		}
	}
	return true, nil
}

func insertWatchDelivery(ctx context.Context, db execer, notification string, del WatchDelivery) error {
	at, err := encodeTime(del.At)
	if err != nil {
		return fmt.Errorf("store: record watch delivery: %w", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO watch_deliveries (notification_id, channel, status, detail, at) VALUES (?, ?, ?, ?, ?)`,
		notification, del.Channel, del.Status, del.Detail, at); err != nil {
		return fmt.Errorf("store: record watch delivery: %w", mapConstraint(err, ErrNotFound))
	}
	return nil
}

func (d *DB) TouchWatchCheck(ctx context.Context, id string, at time.Time) error {
	t, err := encodeTime(at)
	if err != nil {
		return fmt.Errorf("store: touch watch check: %w", err)
	}
	if _, err := d.db.ExecContext(ctx, `UPDATE watches SET last_check_at = ? WHERE id = ?`, t, id); err != nil {
		return fmt.Errorf("store: touch watch check: %w", err)
	}
	return nil
}

func (d *DB) DeleteWatch(ctx context.Context, id string) error {
	return d.execDelete(ctx, "delete watch", `DELETE FROM watches WHERE id = ?`, id)
}

const evaluable = `state IN ('active', 'error')`

func (d *DB) EvaluableWatchIDs(ctx context.Context, run domain.RunID, mission domain.MissionID) ([]string, error) {
	query := `SELECT id FROM watches WHERE ` + evaluable + ` AND mission_id = ?`
	args := []any{mission}
	if run != "" {
		query = `SELECT id FROM watches WHERE ` + evaluable + ` AND (run_id = ?1
			OR mission_id IN (SELECT id FROM missions WHERE current_integrator_run_id = ?1))`
		args = []any{run}
	}
	return d.watchIDs(ctx, query+` ORDER BY id`, args...)
}

func (d *DB) DueWatchIDs(ctx context.Context, now time.Time, all bool) ([]string, error) {
	if all {
		return d.watchIDs(ctx, `SELECT id FROM watches WHERE `+evaluable+` ORDER BY id`)
	}
	at, err := encodeTime(now)
	if err != nil {
		return nil, fmt.Errorf("store: due watches: %w", err)
	}
	return d.watchIDs(ctx, `SELECT id FROM watches WHERE `+evaluable+` AND next_check_at <= ? ORDER BY id`, at)
}

func (d *DB) watchIDs(ctx context.Context, query string, args ...any) ([]string, error) {
	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list watch ids: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only iteration
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("store: list watch ids: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list watch ids: %w", err)
	}
	return out, nil
}

func (d *DB) ExpireWatches(ctx context.Context, now time.Time) ([]*Watch, error) {
	at, err := encodeTime(now)
	if err != nil {
		return nil, fmt.Errorf("store: expire watches: %w", err)
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("store: expire watches: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	const heldByPause = `SELECT n.id FROM watch_notifications n JOIN watches w ON w.id = n.watch_id
		WHERE n.push_state <> 'none' AND w.state = 'paused' AND w.expires_at <= ?1`
	if _, err = tx.ExecContext(ctx,
		`UPDATE watch_deliveries SET status = 'cleared', detail = 'The watch was paused until it expired', at = ?1
		 WHERE channel = 'push' AND notification_id IN (`+heldByPause+`)`, at); err != nil {
		return nil, fmt.Errorf("store: expire watches: drop paused pushes: %w", err)
	}
	if _, err = tx.ExecContext(ctx,
		`UPDATE watch_notifications SET push_state = 'none', push_next_at = NULL WHERE id IN (`+heldByPause+`)`, at); err != nil {
		return nil, fmt.Errorf("store: expire watches: drop paused pushes: %w", err)
	}
	rows, err := tx.QueryContext(ctx,
		`UPDATE watches SET state = 'expired', ended_at = ?1, updated_at = ?1, next_check_at = NULL, version = version + 1
		 WHERE state IN ('active', 'paused', 'error') AND expires_at <= ?1
		 RETURNING `+watchColumns, at)
	if err != nil {
		return nil, fmt.Errorf("store: expire watches: %w", err)
	}
	expired, err := collect(rows, scanWatch)
	if err != nil {
		return nil, fmt.Errorf("store: expire watches: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("store: expire watches: %w", err)
	}
	return expired, nil
}

func (d *DB) PurgeWatches(ctx context.Context, cutoff time.Time) (int64, error) {
	at, err := encodeTime(cutoff)
	if err != nil {
		return 0, fmt.Errorf("store: purge watches: %w", err)
	}
	res, err := d.db.ExecContext(ctx, `DELETE FROM watches WHERE state IN ('done', 'expired') AND ended_at < ?`, at)
	if err != nil {
		return 0, fmt.Errorf("store: purge watches: %w", err)
	}
	return res.RowsAffected()
}

func (d *DB) MissionDeliveryReceipts(ctx context.Context, workspace domain.WorkspaceID, mission domain.MissionID) (map[string]json.RawMessage, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT id, json_extract(payload, '$.delivery_receipt') FROM integration_candidates
		 WHERE workspace_id = ? AND json_extract(payload, '$.mission_id') = ?
		   AND json_extract(payload, '$.delivery_receipt') IS NOT NULL`, workspace, mission)
	if err != nil {
		return nil, fmt.Errorf("store: list delivery receipts: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only iteration
	out := map[string]json.RawMessage{}
	for rows.Next() {
		var id, receipt string
		if err := rows.Scan(&id, &receipt); err != nil {
			return nil, fmt.Errorf("store: list delivery receipts: %w", err)
		}
		out[id] = json.RawMessage(receipt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list delivery receipts: %w", err)
	}
	return out, nil
}

func (d *DB) MissionFinishedAt(ctx context.Context, mission domain.MissionID) (*time.Time, error) {
	var at *int64
	err := d.db.QueryRowContext(ctx, `SELECT finished_at FROM missions WHERE id = ?`, mission).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: mission %s: %w", mission, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("store: read mission finish: %w", err)
	}
	return decodeTimePtr(at), nil
}
