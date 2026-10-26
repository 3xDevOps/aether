package store

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// AuditKeep is how many call entries, and separately how many security
// entries, the store keeps per member; adding one drops that member's
// oldest of its kind beyond it, so no member's calls evict another's and
// no flood of calls evicts a security change.
const AuditKeep = 10_000

// securityPrefixes are the methods whose entries, once they took effect,
// are security entries: connector, account-share, member and Slack link
// changes.
var securityPrefixes = []string{"connector.", "account.", "member.", "slack."}

func securityEntry(e *AuditEntry) bool {
	return e.Code == 0 && slices.ContainsFunc(securityPrefixes, func(p string) bool { return strings.HasPrefix(e.Method, p) })
}

// AuditEntry is one durable record of who changed what and how. Via is
// how the call arrived (dashboard, cli, connector or assistant);
// ConnectorID and Client name the connector of a connector call or of
// an event that befell a connector. Target is the run, swarm, connector,
// member, device, Slack user or merge proposal the call acted on. Code
// is 0 for a call that succeeded and its protocol error code otherwise.
// Entries never hold tokens, message bodies or other params.
type AuditEntry struct {
	Seq         int64
	At          time.Time
	Member      domain.MemberID
	Via         string
	ConnectorID string
	Client      string
	Method      string
	WorkspaceID string
	Target      string
	Code        int
}

// AuditQuery selects audit entries, newest first: Member's alone unless
// empty, ConnectorID's calls and its revocation alone unless empty, and
// only those before seq Before unless it is 0.
type AuditQuery struct {
	Member      domain.MemberID
	ConnectorID string
	Before      int64
	Limit       int
}

type AuditStore interface {
	AddAudit(ctx context.Context, e *AuditEntry) error
	ListAudit(ctx context.Context, q AuditQuery) ([]*AuditEntry, error)
}

var _ AuditStore = (*DB)(nil)

func (d *DB) AddAudit(ctx context.Context, e *AuditEntry) error {
	at, err := encodeTime(e.At)
	if err != nil {
		return fmt.Errorf("store: add audit: %w", err)
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin add audit: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	security := securityEntry(e)
	res, err := tx.ExecContext(ctx, `INSERT INTO audit (ts, member_id, via, connector_id, client, method, workspace_id, target, code, security)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		at, e.Member, e.Via, e.ConnectorID, e.Client, e.Method, e.WorkspaceID, e.Target, e.Code, security)
	if err != nil {
		return fmt.Errorf("store: add audit: %w", err)
	}
	if e.Seq, err = res.LastInsertId(); err != nil {
		return fmt.Errorf("store: add audit: %w", err)
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM audit WHERE member_id = ? AND security = ? AND seq <= (
		SELECT seq FROM audit WHERE member_id = ? AND security = ? ORDER BY seq DESC LIMIT 1 OFFSET ?)`,
		e.Member, security, e.Member, security, AuditKeep); err != nil {
		return fmt.Errorf("store: trim audit: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("store: commit add audit: %w", err)
	}
	return nil
}

func (d *DB) ListAudit(ctx context.Context, q AuditQuery) ([]*AuditEntry, error) {
	query, args := `SELECT seq, ts, member_id, via, connector_id, client, method, workspace_id, target, code FROM audit WHERE 1 = 1`, []any{}
	if q.Member != "" {
		query += ` AND member_id = ?`
		args = append(args, q.Member)
	}
	if q.ConnectorID != "" {
		query += ` AND (connector_id = ? OR (method = 'connector.revoke' AND target = ?))`
		args = append(args, q.ConnectorID, q.ConnectorID)
	}
	if q.Before > 0 {
		query += ` AND seq < ?`
		args = append(args, q.Before)
	}
	rows, err := d.db.QueryContext(ctx, query+` ORDER BY seq DESC LIMIT ?`, append(args, q.Limit)...)
	if err != nil {
		return nil, fmt.Errorf("store: list audit: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []*AuditEntry
	for rows.Next() {
		var (
			e  AuditEntry
			at int64
		)
		if err := rows.Scan(&e.Seq, &at, &e.Member, &e.Via, &e.ConnectorID, &e.Client, &e.Method, &e.WorkspaceID, &e.Target, &e.Code); err != nil {
			return nil, fmt.Errorf("store: scan audit: %w", err)
		}
		e.At = decodeTime(at)
		out = append(out, &e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list audit: %w", err)
	}
	return out, nil
}
