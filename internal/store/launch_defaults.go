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

// LaunchDefaults are the launch choices a member saved. Account empty is
// the member's own; FallbackPolicy empty was never chosen. Revision changes
// on every save.
type LaunchDefaults struct {
	Agent          string
	Agents         map[string]AgentLaunchDefault
	Account        domain.MemberID
	FallbackPolicy string
	FallbackOrder  []domain.MemberID
	Source         string
	Revision       int64
	UpdatedAt      time.Time
}

// AgentLaunchDefault is the mode and model a member last chose for one
// agent; empty keeps the agent's default.
type AgentLaunchDefault struct {
	Mode  domain.LaunchMode `json:"mode,omitempty"`
	Model string            `json:"model,omitempty"`
}

// LaunchDefaultsStore keeps one LaunchDefaults row per member. Writes take
// the revision the caller read, 0 for none saved, and return ErrConflict
// when it is no longer current.
type LaunchDefaultsStore interface {
	// LaunchDefaults returns member's saved defaults, or ErrNotFound.
	LaunchDefaults(ctx context.Context, member domain.MemberID) (*LaunchDefaults, error)
	// PutLaunchDefaults replaces member's defaults, ignoring d's Revision
	// and UpdatedAt.
	PutLaunchDefaults(ctx context.Context, member domain.MemberID, d LaunchDefaults, revision int64) error
	DeleteLaunchDefaults(ctx context.Context, member domain.MemberID, revision int64) error
}

func (d *DB) LaunchDefaults(ctx context.Context, member domain.MemberID) (*LaunchDefaults, error) {
	var (
		out           LaunchDefaults
		agents, order string
		updated       int64
	)
	err := d.db.QueryRowContext(ctx,
		`SELECT agent, agents, account_member_id, fallback_policy, fallback_order, source, revision, updated_at
		 FROM launch_defaults WHERE member_id = ?`, member).
		Scan(&out.Agent, &agents, &out.Account, &out.FallbackPolicy, &order, &out.Source, &out.Revision, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: launch defaults: %w", err)
	}
	if err := json.Unmarshal([]byte(agents), &out.Agents); err != nil {
		return nil, fmt.Errorf("store: decode launch defaults agents: %w", err)
	}
	if err := json.Unmarshal([]byte(order), &out.FallbackOrder); err != nil {
		return nil, fmt.Errorf("store: decode launch defaults fallback order: %w", err)
	}
	out.UpdatedAt = time.Unix(updated, 0).UTC()
	return &out, nil
}

// PutLaunchDefaults writes a new row each save: its revision is an
// AUTOINCREMENT key, which SQLite never hands out twice, so a revision read
// before a clear stays stale after it.
func (d *DB) PutLaunchDefaults(ctx context.Context, member domain.MemberID, l LaunchDefaults, revision int64) error {
	if l.Agents == nil {
		l.Agents = map[string]AgentLaunchDefault{}
	}
	if l.FallbackOrder == nil {
		l.FallbackOrder = []domain.MemberID{}
	}
	agents, err := json.Marshal(l.Agents)
	if err != nil {
		return fmt.Errorf("store: encode launch defaults agents: %w", err)
	}
	order, err := json.Marshal(l.FallbackOrder)
	if err != nil {
		return fmt.Errorf("store: encode launch defaults fallback order: %w", err)
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: put launch defaults: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // committed below
	if revision != 0 {
		res, err := tx.ExecContext(ctx, `DELETE FROM launch_defaults WHERE member_id = ? AND revision = ?`, member, revision)
		if err != nil {
			return fmt.Errorf("store: put launch defaults: %w", err)
		}
		if err := staleRevision(res, "put launch defaults"); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO launch_defaults (member_id, agent, agents, account_member_id, fallback_policy, fallback_order, source, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, unixepoch())`,
		member, l.Agent, string(agents), l.Account, l.FallbackPolicy, string(order), l.Source); err != nil {
		return fmt.Errorf("store: put launch defaults: %w", mapConstraint(err, ErrNotFound))
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: put launch defaults: %w", err)
	}
	return nil
}

func (d *DB) DeleteLaunchDefaults(ctx context.Context, member domain.MemberID, revision int64) error {
	if revision == 0 {
		if _, err := d.LaunchDefaults(ctx, member); !errors.Is(err, ErrNotFound) {
			if err != nil {
				return err
			}
			return fmt.Errorf("%w: store: delete launch defaults: revision 0 is not current", ErrConflict)
		}
		return nil
	}
	res, err := d.db.ExecContext(ctx, `DELETE FROM launch_defaults WHERE member_id = ? AND revision = ?`, member, revision)
	if err != nil {
		return fmt.Errorf("store: delete launch defaults: %w", err)
	}
	return staleRevision(res, "delete launch defaults")
}

func staleRevision(res sql.Result, what string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: %s: %w", what, err)
	}
	if n == 0 {
		return fmt.Errorf("%w: store: %s: the revision is not current", ErrConflict, what)
	}
	return nil
}
