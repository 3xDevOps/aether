package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// MaxAssistantPromptKeys bounds the accepted prompt keys kept per member.
const MaxAssistantPromptKeys = 128

// AssistantSession is the agent and harness session a member's assistant
// resumes.
type AssistantSession struct {
	MemberID         domain.MemberID
	Agent            string
	HarnessSessionID string
	// ExecIdentity is the JSON identity of the adapter exec that may still
	// run; empty once it is known to be stopped.
	ExecIdentity string
	// Generation counts the member's conversations; a reset starts the next.
	Generation int64
	UpdatedAt  time.Time
}

// AssistantStore persists each member's assistant session. Removing a member
// removes theirs.
type AssistantStore interface {
	// GetAssistantSession returns ErrNotFound when the member has none.
	GetAssistantSession(ctx context.Context, member domain.MemberID) (*AssistantSession, error)
	// PutAssistantSession stores everything but the generation.
	PutAssistantSession(ctx context.Context, session *AssistantSession) error
	// ClearAssistantExec forgets the recorded adapter identity if the row is
	// still in generation and still records identity.
	ClearAssistantExec(ctx context.Context, member domain.MemberID, generation int64, identity string) error
	// ResetAssistantSession starts the member's next conversation: it clears
	// the agent, harness session and prompt keys, and returns the new generation.
	ResetAssistantSession(ctx context.Context, member domain.MemberID) (int64, error)
	// HasAssistantPromptKey reports whether key was accepted in generation.
	HasAssistantPromptKey(ctx context.Context, member domain.MemberID, generation int64, key string) (bool, error)
	// AddAssistantPromptKey records key, keeping the newest MaxAssistantPromptKeys.
	AddAssistantPromptKey(ctx context.Context, member domain.MemberID, generation int64, key string) error
	// AssistantAutoApprove reports whether the member lets the assistant's
	// confirmed tools run without a card; false when never set.
	AssistantAutoApprove(ctx context.Context, member domain.MemberID) (bool, error)
	SetAssistantAutoApprove(ctx context.Context, member domain.MemberID, on bool) error
	// AssistantShareView reports whether the member sends the assistant their
	// dashboard view context; true when never set.
	AssistantShareView(ctx context.Context, member domain.MemberID) (bool, error)
	SetAssistantShareView(ctx context.Context, member domain.MemberID, on bool) error
}

func (d *DB) GetAssistantSession(ctx context.Context, member domain.MemberID) (*AssistantSession, error) {
	var session AssistantSession
	var updatedAt int64
	err := d.db.QueryRowContext(ctx,
		`SELECT member_id, agent, harness_session_id, exec_identity, generation, updated_at
		 FROM assistant_sessions WHERE member_id = ?`, member).
		Scan(&session.MemberID, &session.Agent, &session.HarnessSessionID, &session.ExecIdentity, &session.Generation, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get assistant session: %w", err)
	}
	session.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return &session, nil
}

func (d *DB) PutAssistantSession(ctx context.Context, session *AssistantSession) error {
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO assistant_sessions (member_id, agent, harness_session_id, exec_identity, updated_at)
		 VALUES (?, ?, ?, ?, unixepoch())
		 ON CONFLICT(member_id) DO UPDATE SET
		   agent = excluded.agent,
		   harness_session_id = excluded.harness_session_id,
		   exec_identity = excluded.exec_identity,
		   updated_at = excluded.updated_at`,
		session.MemberID, session.Agent, session.HarnessSessionID, session.ExecIdentity)
	if err != nil {
		return fmt.Errorf("store: put assistant session: %w", mapConstraint(err, ErrNotFound))
	}
	return nil
}

func (d *DB) ClearAssistantExec(ctx context.Context, member domain.MemberID, generation int64, identity string) error {
	_, err := d.db.ExecContext(ctx,
		`UPDATE assistant_sessions SET exec_identity = '', updated_at = unixepoch()
		 WHERE member_id = ? AND generation = ? AND exec_identity = ?`, member, generation, identity)
	if err != nil {
		return fmt.Errorf("store: clear assistant exec: %w", err)
	}
	return nil
}

func (d *DB) ResetAssistantSession(ctx context.Context, member domain.MemberID) (int64, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: reset assistant session: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	var generation int64
	err = tx.QueryRowContext(ctx,
		`INSERT INTO assistant_sessions (member_id, agent, harness_session_id, generation, updated_at)
		 VALUES (?, '', '', 1, unixepoch())
		 ON CONFLICT(member_id) DO UPDATE SET
		   agent = '', harness_session_id = '', generation = generation + 1, updated_at = excluded.updated_at
		 RETURNING generation`, member).Scan(&generation)
	if err != nil {
		return 0, fmt.Errorf("store: reset assistant session: %w", mapConstraint(err, ErrNotFound))
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM assistant_prompt_keys WHERE member_id = ?`, member); err != nil {
		return 0, fmt.Errorf("store: reset assistant session: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: reset assistant session: %w", err)
	}
	return generation, nil
}

func (d *DB) HasAssistantPromptKey(ctx context.Context, member domain.MemberID, generation int64, key string) (bool, error) {
	var found int
	err := d.db.QueryRowContext(ctx,
		`SELECT 1 FROM assistant_prompt_keys WHERE member_id = ? AND generation = ? AND key = ?`,
		member, generation, key).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("store: check assistant prompt key: %w", err)
	}
	return true, nil
}

func (d *DB) AddAssistantPromptKey(ctx context.Context, member domain.MemberID, generation int64, key string) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: add assistant prompt key: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if _, err = tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO assistant_prompt_keys (member_id, generation, key) VALUES (?, ?, ?)`,
		member, generation, key); err != nil {
		return fmt.Errorf("store: add assistant prompt key: %w", mapConstraint(err, ErrNotFound))
	}
	if _, err = tx.ExecContext(ctx,
		`DELETE FROM assistant_prompt_keys WHERE member_id = ? AND rowid NOT IN
		   (SELECT rowid FROM assistant_prompt_keys WHERE member_id = ? ORDER BY rowid DESC LIMIT ?)`,
		member, member, MaxAssistantPromptKeys); err != nil {
		return fmt.Errorf("store: add assistant prompt key: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("store: add assistant prompt key: %w", err)
	}
	return nil
}

func (d *DB) AssistantAutoApprove(ctx context.Context, member domain.MemberID) (bool, error) {
	var on bool
	err := d.db.QueryRowContext(ctx,
		`SELECT auto_approve FROM assistant_settings WHERE member_id = ?`, member).Scan(&on)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("store: get assistant auto-approval: %w", err)
	}
	return on, nil
}

func (d *DB) SetAssistantAutoApprove(ctx context.Context, member domain.MemberID, on bool) error {
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO assistant_settings (member_id, auto_approve, updated_at) VALUES (?, ?, unixepoch())
		 ON CONFLICT(member_id) DO UPDATE SET auto_approve = excluded.auto_approve, updated_at = excluded.updated_at`,
		member, on)
	if err != nil {
		return fmt.Errorf("store: set assistant auto-approval: %w", mapConstraint(err, ErrNotFound))
	}
	return nil
}

func (d *DB) AssistantShareView(ctx context.Context, member domain.MemberID) (bool, error) {
	on := true
	err := d.db.QueryRowContext(ctx,
		`SELECT share_view FROM assistant_settings WHERE member_id = ?`, member).Scan(&on)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("store: get assistant view sharing: %w", err)
	}
	return on, nil
}

func (d *DB) SetAssistantShareView(ctx context.Context, member domain.MemberID, on bool) error {
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO assistant_settings (member_id, share_view, updated_at) VALUES (?, ?, unixepoch())
		 ON CONFLICT(member_id) DO UPDATE SET share_view = excluded.share_view, updated_at = excluded.updated_at`,
		member, on)
	if err != nil {
		return fmt.Errorf("store: set assistant view sharing: %w", mapConstraint(err, ErrNotFound))
	}
	return nil
}
