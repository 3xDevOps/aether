package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
	// SetLastServerUpdate records the outcome of an attempt and clears the
	// pending row in the same write, so a finished attempt can never be
	// retried by the next idle tick.
	SetLastServerUpdate(ctx context.Context, last ServerUpdateAttempt) error
}

// Outcome values of ServerUpdateAttempt.
const (
	// ServerUpdateApplied: the binaries were replaced.
	ServerUpdateApplied = "applied"
	// ServerUpdateFailed: nothing was replaced; Detail says why.
	ServerUpdateFailed = "failed"
)

// ServerUpdate is the whole state. Both fields are nil when unset.
type ServerUpdate struct {
	Pending *PendingServerUpdate
	Last    *ServerUpdateAttempt
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
	)
	err := d.db.QueryRowContext(ctx,
		`SELECT pending_version, pending_requested_by, pending_requested_at,
		        last_version, last_outcome, last_detail, last_at
		 FROM server_update_state WHERE id = 1`,
	).Scan(&pendingVersion, &pendingBy, &pendingAt, &lastVersion, &outcome, &detail, &lastAt)
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
