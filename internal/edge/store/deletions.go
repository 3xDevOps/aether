package edgestore

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
)

// maxPendingDeletions bounds the account deletions owed to one server. A
// server that stays offline while more of its members delete their
// accounts loses the oldest; the server still refuses those accounts'
// device tokens, which the edge deleted, but keeps their identities until
// an admin removes them there.
const maxPendingDeletions = 1000

// oweDeletion records that serverID is owed an edgeproto.AccountDeleted
// for provider and subject, and drops the oldest past
// maxPendingDeletions.
func oweDeletion(ctx context.Context, tx *sql.Tx, serverID, provider, subject string, now time.Time) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO pending_account_deletions (server_id, provider, subject, created_at)
		VALUES (?, ?, ?, ?) ON CONFLICT (server_id, provider, subject) DO UPDATE SET created_at = excluded.created_at`,
		serverID, provider, subject, unix(now)); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM pending_account_deletions WHERE server_id = ? AND rowid NOT IN (
		SELECT rowid FROM pending_account_deletions WHERE server_id = ?
		ORDER BY created_at DESC, rowid DESC LIMIT ?)`, serverID, serverID, maxPendingDeletions)
	return err
}

// PendingDeletions returns the account deletions owed to serverID, oldest
// first.
func (s *Store) PendingDeletions(ctx context.Context, serverID string) ([]edgeproto.AccountDeleted, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT provider, subject FROM pending_account_deletions
		WHERE server_id = ? ORDER BY created_at, rowid`, serverID)
	if err != nil {
		return nil, fmt.Errorf("edgestore: read deletions owed to %s: %w", serverID, err)
	}
	defer rows.Close() //nolint:errcheck // read-only iteration
	var out []edgeproto.AccountDeleted
	for rows.Next() {
		var d edgeproto.AccountDeleted
		if err := rows.Scan(&d.Provider, &d.Subject); err != nil {
			return nil, fmt.Errorf("edgestore: read deletions owed to %s: %w", serverID, err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("edgestore: read deletions owed to %s: %w", serverID, err)
	}
	return out, nil
}

// DeletionApplied forgets a deletion owed to serverID once the server
// applied it.
func (s *Store) DeletionApplied(ctx context.Context, serverID string, d edgeproto.AccountDeleted) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM pending_account_deletions
		WHERE server_id = ? AND provider = ? AND subject = ?`, serverID, d.Provider, d.Subject); err != nil {
		return fmt.Errorf("edgestore: forget deletion applied by %s: %w", serverID, err)
	}
	return nil
}

// RecordReach records that the relay admitted account a to serverID, which
// may then hold a's identity: DeleteAccount owes that server the
// deletion.
func (s *Store) RecordReach(ctx context.Context, serverID string, a edgeproto.Account) error {
	if _, err := s.db.ExecContext(ctx, `INSERT INTO account_reach (provider, subject, server_id) VALUES (?, ?, ?)
		ON CONFLICT DO NOTHING`, a.Provider, a.Subject, serverID); err != nil {
		return fmt.Errorf("edgestore: record that %s %s reached %s: %w", a.Provider, a.Subject, serverID, err)
	}
	return nil
}
