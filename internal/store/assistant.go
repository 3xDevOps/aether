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

// AssistantSettings is a member's assistant settings and the state of the
// suggestion to turn auto-approval on.
type AssistantSettings struct {
	// AutoApprove lets the assistant's confirmed tools run without a card.
	AutoApprove bool
	// Approvals counts cards approved by hand since the last decline,
	// timeout or answer to the suggestion.
	Approvals int
	// Snoozes counts the suggestions answered "later".
	Snoozes      int
	SnoozedUntil time.Time
	// NoSuggest stops the suggestion for good.
	NoSuggest bool
	// ShareView sends the assistant the member's dashboard view context.
	ShareView bool
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
	// AssistantSettings returns the member's settings; when never set, the
	// zero value but for ShareView, which is on.
	AssistantSettings(ctx context.Context, member domain.MemberID) (AssistantSettings, error)
	PutAssistantSettings(ctx context.Context, member domain.MemberID, settings AssistantSettings) error
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

func (d *DB) AssistantSettings(ctx context.Context, member domain.MemberID) (AssistantSettings, error) {
	var settings AssistantSettings
	var until int64
	err := d.db.QueryRowContext(ctx,
		`SELECT auto_approve, manual_approvals, suggest_snoozes, suggest_after, suggest_off, share_view
		 FROM assistant_settings WHERE member_id = ?`, member).
		Scan(&settings.AutoApprove, &settings.Approvals, &settings.Snoozes, &until, &settings.NoSuggest, &settings.ShareView)
	if errors.Is(err, sql.ErrNoRows) {
		return AssistantSettings{ShareView: true}, nil
	}
	if err != nil {
		return AssistantSettings{}, fmt.Errorf("store: get assistant settings: %w", err)
	}
	if until > 0 {
		settings.SnoozedUntil = time.Unix(until, 0).UTC()
	}
	return settings, nil
}

func (d *DB) PutAssistantSettings(ctx context.Context, member domain.MemberID, settings AssistantSettings) error {
	var until int64
	if !settings.SnoozedUntil.IsZero() {
		until = settings.SnoozedUntil.Unix()
	}
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO assistant_settings (member_id, auto_approve, manual_approvals, suggest_snoozes, suggest_after, suggest_off, share_view, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, unixepoch())
		 ON CONFLICT(member_id) DO UPDATE SET auto_approve = excluded.auto_approve,
		   manual_approvals = excluded.manual_approvals, suggest_snoozes = excluded.suggest_snoozes,
		   suggest_after = excluded.suggest_after, suggest_off = excluded.suggest_off,
		   share_view = excluded.share_view, updated_at = excluded.updated_at`,
		member, settings.AutoApprove, settings.Approvals, settings.Snoozes, until, settings.NoSuggest, settings.ShareView)
	if err != nil {
		return fmt.Errorf("store: put assistant settings: %w", mapConstraint(err, ErrNotFound))
	}
	return nil
}
