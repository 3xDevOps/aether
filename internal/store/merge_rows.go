package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/3xDevOps/Aether/internal/domain"
)

// memberRef is a column holding a member id. A merge rewrites the
// absorbed member's id to the survivor's in each; mergeRows handles the
// special ones itself before the plain UPDATE of the rest.
type memberRef struct {
	table, column string
	special       bool
}

// memberRefs is every column that holds a member id. A new one is added
// here, or TestMergeHandlesEveryMemberColumn fails.
var memberRefs = []memberRef{
	{"account_shares", "owner_member_id", true},
	{"account_shares", "grantee_member_id", true},
	{"approvals", "decided_by", false},
	{"events", "actor_id", false},
	{"evidence_packets", "origin_id", true},
	{"evidence_packets", "owner_id", false},
	{"evidence_packets", "creator_id", false},
	{"evidence_staging", "origin_id", false},
	{"evidence_staging", "creator_id", false},
	{"handoff_outbox", "actor_id", false},
	{"handoff_outbox", "from_member_id", false},
	{"handoff_outbox", "to_member_id", false},
	{"harness_definitions", "member_id", true},
	{"identity_invitations", "member_id", false},
	{"identity_invitations", "created_by", false},
	{"integration_candidates", "actor_key", true},
	{"member_devices", "member_id", false},
	{"member_devices", "approved_by", false},
	{"member_identities", "member_id", false},
	{"member_merge_codes", "member_id", false},
	{"member_merges", "survivor_id", false},
	{"member_terminals", "member_id", false},
	{"mission_attempts", "authorizing_human_id", false},
	{"mission_attempts", "run_owner_id", false},
	{"mission_attempts", "account_owner_id", false},
	{"mission_attempts", "launch_account_id", false},
	{"mission_create_receipts", "accountable_human_id", false},
	{"mission_create_receipts", "integrator_account_member_id", false},
	{"mission_create_receipts", "execution_choices", true},
	{"mission_integrator_replacements", "account_member_id", false},
	{"mission_integrator_replacements", "authorizing_human_id", false},
	{"mission_integrator_replacements", "run_owner_id", false},
	{"mission_questions", "answered_by_member_id", false},
	{"mission_task_revisions", "accepted_by_member_id", false},
	{"mission_worker_takeovers", "member_id", false},
	{"missions", "accountable_human_id", false},
	{"missions", "integrator_account_member_id", false},
	{"missions", "integrator_authorizing_human_id", false},
	{"missions", "integrator_run_owner_id", false},
	{"missions", "execution_choices", true},
	{"missions", "fallback_order", true},
	{"profile_heads", "member_id", true},
	{"profile_snapshots", "member_id", true},
	{"push_subscriptions", "member_id", false},
	{"room_messages", "actor_id", true},
	{"room_messages", "decided_by", false},
	{"run_account_segments", "account_member_id", false},
	{"run_cost_deletions", "member_id", true},
	{"run_costs", "member_id", false},
	{"run_steerers", "member_id", true},
	{"runs", "member_id", false},
	{"runs", "account_member_id", false},
	{"runs", "home_member_id", false},
	{"runs", "requested_account_member_id", false},
	{"runs", "fallback_order", true},
	{"schedules", "member_id", false},
	{"server_update_state", "pending_requested_by", false},
	{"workspace_budgets", "updated_by", false},
	{"workspace_mirrors", "github_member_id", false},
}

// mergeRows makes every row of absorbed the survivor's, deletes the
// absorbed member and gives the survivor its credentials and role. The
// absorbed member's settings, outbound account shares, harness
// definitions and profile heads are dropped: the caller has recorded
// them. A row the survivor already has is kept once.
func mergeRows(ctx context.Context, tx *sql.Tx, survivor, absorbed domain.Member, role domain.Role) error {
	sur, abs := survivor.ID, absorbed.ID
	steps := []struct {
		what  string
		query string
		args  []any
	}{
		{"account shares", `DELETE FROM account_shares WHERE owner_member_id = ?1
			OR (owner_member_id = ?2 AND grantee_member_id = ?1)`, []any{abs, sur}},
		{"account shares", `UPDATE OR IGNORE account_shares SET grantee_member_id = ?2 WHERE grantee_member_id = ?1`, []any{abs, sur}},
		{"account shares", `DELETE FROM account_shares WHERE grantee_member_id = ?1`, []any{abs}},
		{"run steerers", `UPDATE OR IGNORE run_steerers SET member_id = ?2 WHERE member_id = ?1`, []any{abs, sur}},
		{"run steerers", `DELETE FROM run_steerers WHERE member_id = ?1
			OR (member_id = ?2 AND run_id IN (SELECT id FROM runs WHERE member_id IN (?1, ?2)))`, []any{abs, sur}},
		{"deleted run costs", `INSERT INTO run_cost_deletions (workspace_id, member_id, runs, metered, unmetered, input_tokens, output_tokens, cost_usd)
			SELECT workspace_id, ?2, runs, metered, unmetered, input_tokens, output_tokens, cost_usd
			FROM run_cost_deletions WHERE member_id = ?1
			ON CONFLICT (workspace_id, member_id) DO UPDATE SET runs = runs + excluded.runs,
				metered = metered + excluded.metered, unmetered = unmetered + excluded.unmetered,
				input_tokens = input_tokens + excluded.input_tokens, output_tokens = output_tokens + excluded.output_tokens,
				cost_usd = cost_usd + excluded.cost_usd`, []any{abs, sur}},
		{"deleted run costs", `DELETE FROM run_cost_deletions WHERE member_id = ?1`, []any{abs}},
		{"harness definitions", `DELETE FROM harness_definitions WHERE member_id = ?1`, []any{abs}},
		{"profile heads", `DELETE FROM profile_heads WHERE member_id = ?1`, []any{abs}},
		{"profile snapshots", `UPDATE runs SET profile_snapshot_id = (SELECT s.id FROM profile_snapshots s
				JOIN profile_snapshots a ON a.harness = s.harness AND a.digest = s.digest
				WHERE a.id = runs.profile_snapshot_id AND a.member_id = ?1 AND s.member_id = ?2)
			WHERE profile_snapshot_id IN (SELECT a.id FROM profile_snapshots a JOIN profile_snapshots s
				ON a.harness = s.harness AND a.digest = s.digest WHERE a.member_id = ?1 AND s.member_id = ?2)`, []any{abs, sur}},
		{"profile snapshots", `DELETE FROM profile_snapshots WHERE member_id = ?1 AND EXISTS (SELECT 1 FROM profile_snapshots s
			WHERE s.member_id = ?2 AND s.harness = profile_snapshots.harness AND s.digest = profile_snapshots.digest)`, []any{abs, sur}},
		{"profile snapshots", `UPDATE profile_snapshots SET member_id = ?2 WHERE member_id = ?1`, []any{abs, sur}},
		{"room messages", `UPDATE room_messages SET idempotency_key = idempotency_key || '.merged.' || id
			WHERE actor_id = ?1 AND EXISTS (SELECT 1 FROM room_messages o WHERE o.actor_id = ?2
				AND o.run_id = room_messages.run_id AND o.idempotency_key = room_messages.idempotency_key)`, []any{abs, sur}},
		{"room messages", `UPDATE room_messages SET actor_id = ?2 WHERE actor_id = ?1`, []any{abs, sur}},
		{"evidence packets", `UPDATE evidence_packets SET idempotency_key = idempotency_key || '.merged.' || id
			WHERE origin_id = ?1 AND EXISTS (SELECT 1 FROM evidence_packets o WHERE o.origin_kind = evidence_packets.origin_kind
				AND o.origin_id = ?2 AND o.run_id = evidence_packets.run_id AND o.idempotency_key = evidence_packets.idempotency_key)`, []any{abs, sur}},
		{"evidence packets", `UPDATE evidence_packets SET origin_id = ?2 WHERE origin_id = ?1`, []any{abs, sur}},
		{"integration candidates", `UPDATE integration_candidates SET idempotency_key = idempotency_key || '.merged.' || id
			WHERE actor_key = 'member:' || ?1 AND EXISTS (SELECT 1 FROM integration_candidates o WHERE o.workspace_id = integration_candidates.workspace_id
				AND o.actor_key = 'member:' || ?2 AND o.idempotency_key = integration_candidates.idempotency_key)`, []any{abs, sur}},
		{"integration candidates", `UPDATE integration_candidates SET actor_key = 'member:' || ?2 WHERE actor_key = 'member:' || ?1`, []any{abs, sur}},
		{"swarm accounts", `UPDATE missions SET execution_choices = replace(execution_choices, '"' || ?1 || '"', '"' || ?2 || '"'),
			fallback_order = replace(fallback_order, '"' || ?1 || '"', '"' || ?2 || '"')
			WHERE instr(execution_choices, '"' || ?1 || '"') > 0 OR instr(fallback_order, '"' || ?1 || '"') > 0`, []any{abs, sur}},
		{"swarm accounts", `UPDATE mission_create_receipts SET execution_choices = replace(execution_choices, '"' || ?1 || '"', '"' || ?2 || '"')
			WHERE instr(execution_choices, '"' || ?1 || '"') > 0`, []any{abs, sur}},
		{"run fallback order", `UPDATE runs SET fallback_order = replace(fallback_order, '"' || ?1 || '"', '"' || ?2 || '"')
			WHERE instr(fallback_order, '"' || ?1 || '"') > 0`, []any{abs, sur}},
	}
	if role != domain.RoleAdmin {
		// An invitation grants its role, and only an admin creates one.
		steps = append(steps, struct {
			what  string
			query string
			args  []any
		}{"invitations", `DELETE FROM identity_invitations WHERE member_id IS NULL AND consumed_at IS NULL
			AND created_by IN (?1, ?2)`, []any{abs, sur}})
	}
	for _, s := range steps {
		if _, err := tx.ExecContext(ctx, s.query, s.args...); err != nil {
			return fmt.Errorf("store: merge %s: %w", s.what, err)
		}
	}
	for _, ref := range memberRefs {
		if ref.special {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE `+ref.table+` SET `+ref.column+` = ? WHERE `+ref.column+` = ?`, sur, abs); err != nil {
			return fmt.Errorf("store: merge %s.%s: %w", ref.table, ref.column, err)
		}
	}
	if err := notFoundOnZeroRows(tx.ExecContext(ctx, `DELETE FROM members WHERE id = ?`, abs)); err != nil {
		return fmt.Errorf("store: merge: delete absorbed member %s: %w", abs, mapConstraint(err, ErrInUse))
	}
	if survivor.PublicKey == "" {
		survivor.PublicKey = absorbed.PublicKey
	}
	if survivor.TailnetLogin == "" {
		survivor.TailnetLogin = absorbed.TailnetLogin
	}
	if _, err := tx.ExecContext(ctx, `UPDATE members SET public_key = ?, tailnet_login = ?, role = ? WHERE id = ?`,
		survivor.PublicKey, survivor.TailnetLogin, role, sur); err != nil {
		return fmt.Errorf("store: merge: give %s the merged credentials: %w", sur, mapConstraint(err, ErrConflict))
	}
	return nil
}
