package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/3xDevOps/Aether/internal/domain"
)

// memberRefs classifies every column that holds a member id. A merge
// moves the reassigned ones, what grants access or carries the person's
// ongoing work, a run's account and home included since reopening it
// reads them, and a finished swarm's owner, to the survivor. History keeps naming the absorbed
// member, which stays as a tombstone that GetMember refuses; member.list
// maps it to the survivor for display. A new column is added here, or
// TestMergeClassifiesEveryMemberColumn fails.
var memberRefs = map[string]bool{
	"account_shares.owner_member_id":                       true,
	"account_shares.grantee_member_id":                     true,
	"member_devices.member_id":                             true,
	"member_identities.member_id":                          true,
	"member_merges.survivor_id":                            true,
	"members.merged_into":                                  true,
	"memory.pinned_by":                                     true,
	"missions.accountable_human_id":                        true,
	"push_subscriptions.member_id":                         true,
	"run_steerers.member_id":                               true,
	"runs.member_id":                                       true,
	"runs.account_member_id":                               true,
	"runs.home_member_id":                                  true,
	"runs.requested_account_member_id":                     true,
	"schedules.member_id":                                  true,
	"assistant_sessions.member_id":                         false,
	"assistant_prompt_keys.member_id":                      false,
	"assistant_follows.member_id":                          true,
	"assistant_settings.member_id":                         false,
	"approvals.decided_by":                                 false,
	"events.actor_id":                                      false,
	"evidence_packets.origin_id":                           false,
	"evidence_packets.owner_id":                            false,
	"evidence_packets.creator_id":                          false,
	"evidence_staging.origin_id":                           false,
	"evidence_staging.creator_id":                          false,
	"handoff_outbox.actor_id":                              false,
	"handoff_outbox.from_member_id":                        false,
	"handoff_outbox.to_member_id":                          false,
	"harness_definitions.member_id":                        false,
	"identity_invitations.member_id":                       false,
	"identity_invitations.created_by":                      false,
	"integration_candidates.actor_key":                     false,
	"member_devices.approved_by":                           false,
	"member_merge_codes.member_id":                         false,
	"member_merge_codes.proposed_by":                       false,
	"member_merge_codes.keep_settings_of":                  false,
	"member_merges.absorbed_id":                            false,
	"member_terminals.member_id":                           false,
	"memory.author_member_id":                              false,
	"mission_attempts.authorizing_human_id":                false,
	"mission_attempts.run_owner_id":                        false,
	"mission_attempts.account_owner_id":                    false,
	"mission_attempts.launch_account_id":                   false,
	"mission_create_receipts.accountable_human_id":         false,
	"mission_create_receipts.integrator_account_member_id": false,
	"mission_integrator_replacements.account_member_id":    false,
	"mission_integrator_replacements.authorizing_human_id": false,
	"mission_integrator_replacements.run_owner_id":         false,
	"mission_questions.answered_by_member_id":              false,
	"mission_task_revisions.accepted_by_member_id":         false,
	"mission_worker_takeovers.member_id":                   false,
	"missions.integrator_account_member_id":                false,
	"missions.integrator_authorizing_human_id":             false,
	"missions.integrator_run_owner_id":                     false,
	"profile_heads.member_id":                              false,
	"profile_snapshots.member_id":                          false,
	"room_messages.actor_id":                               false,
	"room_messages.decided_by":                             false,
	"run_account_segments.account_member_id":               false,
	"run_cost_deletions.member_id":                         false,
	"run_costs.member_id":                                  false,
	"server_update_state.pending_requested_by":             false,
	"workspace_budgets.updated_by":                         false,
	"workspace_mirrors.github_member_id":                   false,
}

// mergeRows moves what grants access or carries ongoing work from absorbed
// to survivor, tombstones absorbed, and gives the survivor its credentials
// and role. Account shares absorbed granted go with its discarded
// settings; shares it received move, once each, and none to itself. Open
// account links of either member are revoked, and when demoted, so are
// the open invitations either created, as member.role does; otherwise
// the survivor becomes their creator, so a later demotion revokes them.
func mergeRows(ctx context.Context, tx *sql.Tx, survivor, absorbed domain.Member, role domain.Role, demoted bool) error {
	sur, abs := survivor.ID, absorbed.ID
	steps := []struct{ what, query string }{
		{"account shares", `DELETE FROM account_shares WHERE owner_member_id = ?1 OR (owner_member_id = ?2 AND grantee_member_id = ?1)`},
		{"account shares", `UPDATE OR IGNORE account_shares SET grantee_member_id = ?2 WHERE grantee_member_id = ?1`},
		{"account shares", `DELETE FROM account_shares WHERE grantee_member_id = ?1`},
		{"run steerers", `UPDATE OR IGNORE run_steerers SET member_id = ?2 WHERE member_id = ?1`},
		{"run steerers", `DELETE FROM run_steerers WHERE member_id = ?1
			OR (member_id = ?2 AND run_id IN (SELECT id FROM runs WHERE member_id IN (?1, ?2)))`},
		{"assistant follows", `UPDATE OR IGNORE assistant_follows SET member_id = ?2 WHERE member_id = ?1`},
		{"assistant follows", `DELETE FROM assistant_follows WHERE member_id = ?1`},
		{"account links", `DELETE FROM identity_invitations WHERE consumed_at IS NULL AND member_id IN (?1, ?2)`},
		{"identities", `UPDATE member_identities SET member_id = ?2 WHERE member_id = ?1`},
		{"devices", `UPDATE member_devices SET member_id = ?2 WHERE member_id = ?1`},
		{"runs", `UPDATE runs SET member_id = ?2 WHERE member_id = ?1`},
		{"run accounts", `UPDATE runs SET account_member_id = ?2 WHERE account_member_id = ?1`},
		{"run homes", `UPDATE runs SET home_member_id = ?2 WHERE home_member_id = ?1`},
		{"run accounts", `UPDATE runs SET requested_account_member_id = ?2 WHERE requested_account_member_id = ?1`},
		{"run fallback order", `UPDATE runs SET fallback_order = replace(fallback_order, '"' || ?1 || '"', '"' || ?2 || '"')
			WHERE instr(fallback_order, '"' || ?1 || '"') > 0`},
		{"schedules", `UPDATE schedules SET member_id = ?2 WHERE member_id = ?1`},
		{"swarm owners", `UPDATE missions SET accountable_human_id = ?2 WHERE accountable_human_id = ?1`},
		{"push subscriptions", `UPDATE push_subscriptions SET member_id = ?2 WHERE member_id = ?1`},
		{"memory pins", `UPDATE memory SET pinned_by = ?2 WHERE pinned_by = ?1`},
		{"merge records", `UPDATE member_merges SET survivor_id = ?2 WHERE survivor_id = ?1`},
		{"tombstones", `UPDATE members SET merged_into = ?2 WHERE merged_into = ?1`},
		{"tombstone", `UPDATE members SET merged_into = ?2, public_key = '', tailnet_login = '' WHERE id = ?1`},
	}
	if demoted {
		steps = append(steps, struct{ what, query string }{"invitations",
			`DELETE FROM identity_invitations WHERE consumed_at IS NULL AND created_by IN (?1, ?2)`})
	}
	steps = append(steps, struct{ what, query string }{"invitations",
		`UPDATE identity_invitations SET created_by = ?2 WHERE consumed_at IS NULL AND created_by = ?1`})
	for _, s := range steps {
		if _, err := tx.ExecContext(ctx, s.query, abs, sur); err != nil {
			return fmt.Errorf("store: merge %s: %w", s.what, err)
		}
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
