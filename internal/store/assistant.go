package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// AssistantSession is the agent and harness session a member's assistant
// resumes.
type AssistantSession struct {
	MemberID         domain.MemberID
	Agent            string
	HarnessSessionID string
	UpdatedAt        time.Time
}

// AssistantStore persists each member's assistant session. Removing a member
// removes theirs.
type AssistantStore interface {
	// GetAssistantSession returns ErrNotFound when the member has none.
	GetAssistantSession(ctx context.Context, member domain.MemberID) (*AssistantSession, error)
	PutAssistantSession(ctx context.Context, session *AssistantSession) error
	DeleteAssistantSession(ctx context.Context, member domain.MemberID) error
}

func (d *DB) GetAssistantSession(ctx context.Context, member domain.MemberID) (*AssistantSession, error) {
	var session AssistantSession
	var updatedAt int64
	err := d.db.QueryRowContext(ctx,
		`SELECT member_id, agent, harness_session_id, updated_at FROM assistant_sessions WHERE member_id = ?`, member).
		Scan(&session.MemberID, &session.Agent, &session.HarnessSessionID, &updatedAt)
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
		`INSERT INTO assistant_sessions (member_id, agent, harness_session_id, updated_at)
		 VALUES (?, ?, ?, unixepoch())
		 ON CONFLICT(member_id) DO UPDATE SET
		   agent = excluded.agent,
		   harness_session_id = excluded.harness_session_id,
		   updated_at = excluded.updated_at`,
		session.MemberID, session.Agent, session.HarnessSessionID)
	if err != nil {
		return fmt.Errorf("store: put assistant session: %w", mapConstraint(err, ErrNotFound))
	}
	return nil
}

func (d *DB) DeleteAssistantSession(ctx context.Context, member domain.MemberID) error {
	if _, err := d.db.ExecContext(ctx, `DELETE FROM assistant_sessions WHERE member_id = ?`, member); err != nil {
		return fmt.Errorf("store: delete assistant session: %w", err)
	}
	return nil
}
