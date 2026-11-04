package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// AssistantRuleKey is what an approval rule matches: a card passes when its
// method and every field equal the rule's. An empty field matches only an
// empty one.
type AssistantRuleKey struct {
	Method          string
	WorkspaceID     string
	Agent           string
	AccountMemberID string
	Mode            string
	Model           string
	Effort          string
	Permission      string
	NoFallback      bool
}

// AssistantRule is one of a member's approval rules. Wording is the text the
// member clicked and Summary the card it came from.
type AssistantRule struct {
	ID         string
	MemberID   domain.MemberID
	Key        AssistantRuleKey
	Wording    string
	Summary    string
	CreatedAt  time.Time
	LastUsedAt time.Time
	Uses       int
}

const assistantRuleColumns = `id, member_id, method, workspace_id, agent, account_member_id, mode, model, effort, permission, no_fallback,
	wording, summary, created_at, last_used_at, uses`

func scanAssistantRule(row interface{ Scan(...any) error }) (AssistantRule, error) {
	var r AssistantRule
	var created int64
	var used sql.NullInt64
	k := &r.Key
	err := row.Scan(&r.ID, &r.MemberID, &k.Method, &k.WorkspaceID, &k.Agent, &k.AccountMemberID, &k.Mode, &k.Model, &k.Effort, &k.Permission, &k.NoFallback,
		&r.Wording, &r.Summary, &created, &used, &r.Uses)
	if err != nil {
		return AssistantRule{}, err
	}
	r.CreatedAt = time.Unix(created, 0).UTC()
	if used.Valid {
		r.LastUsedAt = time.Unix(used.Int64, 0).UTC()
	}
	return r, nil
}

// AssistantRules returns the member's rules, oldest first.
func (d *DB) AssistantRules(ctx context.Context, member domain.MemberID) ([]AssistantRule, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT `+assistantRuleColumns+` FROM assistant_rules WHERE member_id = ? ORDER BY created_at, rowid`, member)
	if err != nil {
		return nil, fmt.Errorf("store: list assistant rules: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []AssistantRule
	for rows.Next() {
		r, serr := scanAssistantRule(rows)
		if serr != nil {
			return nil, fmt.Errorf("store: list assistant rules: %w", serr)
		}
		out = append(out, r)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list assistant rules: %w", err)
	}
	return out, nil
}

// AddAssistantRule saves a rule for key, or returns the member's rule that
// already has it.
func (d *DB) AddAssistantRule(ctx context.Context, member domain.MemberID, key AssistantRuleKey, wording, summary string) (AssistantRule, error) {
	id, err := newID("rule")
	if err != nil {
		return AssistantRule{}, err
	}
	k := key
	row := d.db.QueryRowContext(ctx,
		`INSERT INTO assistant_rules (id, member_id, method, workspace_id, agent, account_member_id, mode, model, effort, permission, no_fallback, wording, summary, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, unixepoch())
		 ON CONFLICT(member_id, method, workspace_id, agent, account_member_id, mode, model, effort, permission, no_fallback) DO UPDATE SET id = id
		 RETURNING `+assistantRuleColumns,
		id, member, k.Method, k.WorkspaceID, k.Agent, k.AccountMemberID, k.Mode, k.Model, k.Effort, k.Permission, k.NoFallback, wording, summary)
	r, err := scanAssistantRule(row)
	if err != nil {
		return AssistantRule{}, fmt.Errorf("store: add assistant rule: %w", mapConstraint(err, ErrNotFound))
	}
	return r, nil
}

// DeleteAssistantRule removes the member's rule id, or returns ErrNotFound.
func (d *DB) DeleteAssistantRule(ctx context.Context, member domain.MemberID, id string) error {
	res, err := d.db.ExecContext(ctx, `DELETE FROM assistant_rules WHERE member_id = ? AND id = ?`, member, id)
	if err != nil {
		return fmt.Errorf("store: delete assistant rule: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("store: delete assistant rule: %w", err)
	} else if n == 0 {
		return fmt.Errorf("store: delete assistant rule %s: %w", id, ErrNotFound)
	}
	return nil
}

// UseAssistantRule counts one card the member's rule id passed, or returns
// ErrNotFound.
func (d *DB) UseAssistantRule(ctx context.Context, member domain.MemberID, id string) error {
	res, err := d.db.ExecContext(ctx, `UPDATE assistant_rules SET uses = uses + 1, last_used_at = unixepoch() WHERE member_id = ? AND id = ?`, member, id)
	if err != nil {
		return fmt.Errorf("store: use assistant rule: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("store: use assistant rule: %w", err)
	} else if n == 0 {
		return fmt.Errorf("store: use assistant rule %s: %w", id, ErrNotFound)
	}
	return nil
}
