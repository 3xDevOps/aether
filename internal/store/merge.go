package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// MergeStore keeps member merges: the code one member mints, the proposal
// the other member makes with it, the minter's acceptance that merges, and
// the audit record.
type MergeStore interface {
	// CreateMergeCode records the code with hash for member until expires,
	// replacing member's open code, and drops every expired open code.
	CreateMergeCode(ctx context.Context, member domain.MemberID, hash string, now, expires time.Time) error
	// PlanMerge computes what merging the open code's member with caller's
	// would do at now. A merge the plan refuses is a *MergeRefusal.
	PlanMerge(ctx context.Context, hash string, caller domain.MemberID, policy string, now time.Time) (*MergePlan, error)
	// ProposeMerge records p on its code while the plan still has
	// p.Digest; the code then takes no other proposal. The same proposal
	// made again returns the one recorded.
	ProposeMerge(ctx context.Context, p MergeProposal) (*PendingMerge, error)
	// PendingMerges returns the open proposals member made or is asked
	// to accept, each with its plan recomputed.
	PendingMerges(ctx context.Context, member domain.MemberID, policy string, now time.Time) ([]*PendingMerge, error)
	// AcceptMerge merges the proposal in one transaction while the plan
	// still has the proposal's digest; see MergeAccept.
	AcceptMerge(ctx context.Context, a MergeAccept) (*MemberMerge, error)
	// CancelMerge drops member's open codes and the open proposals it made
	// or was made, or only proposal when it is not empty, and returns how
	// many.
	CancelMerge(ctx context.Context, member domain.MemberID, proposal string) (int, error)
	// MergeOfProposal returns the merge that accepted proposal; ErrNotFound
	// while it is open or unknown.
	MergeOfProposal(ctx context.Context, proposal string) (*MemberMerge, error)
	// ListMerges returns the merges into survivor, or every merge when
	// survivor is empty, newest first.
	ListMerges(ctx context.Context, survivor domain.MemberID) ([]*MemberMerge, error)
	// MergedInto returns the member a merge absorbed member into, empty
	// when it was not merged; ErrNotFound when it never existed.
	MergedInto(ctx context.Context, member domain.MemberID) (domain.MemberID, error)
	// FinishedRunsOf returns the finished runs that belong to, run on the
	// account of, or use the home of a or b.
	FinishedRunsOf(ctx context.Context, a, b domain.MemberID) ([]*domain.Run, error)
}

var _ MergeStore = (*DB)(nil)

// MergeRefusal is a merge the store refuses, with the message to show.
type MergeRefusal struct {
	Message string
}

func (e *MergeRefusal) Error() string { return e.Message }

func refuseMerge(format string, args ...any) error {
	return &MergeRefusal{Message: fmt.Sprintf(format, args...)}
}

// MergeHarnessDefinition is one custom agent definition.
type MergeHarnessDefinition struct {
	Name       string `json:"name"`
	Definition string `json:"definition"`
}

// MergeProfileHead is a member's current saved profile for one harness.
type MergeProfileHead struct {
	Harness    string                   `json:"harness"`
	SnapshotID domain.ProfileSnapshotID `json:"snapshot_id"`
}

// MergeSettings are the settings a merge keeps from the survivor and
// discards from the absorbed member.
type MergeSettings struct {
	DisplayName        string                   `json:"display_name"`
	Color              string                   `json:"color"`
	GitName            string                   `json:"git_name"`
	GitEmail           string                   `json:"git_email"`
	Image              string                   `json:"image"`
	HarnessDefinitions []MergeHarnessDefinition `json:"harness_definitions"`
	ProfileHeads       []MergeProfileHead       `json:"profile_heads"`
	AccountSharedWith  []domain.MemberID        `json:"account_shared_with"`
	MirrorWorkspaces   []domain.WorkspaceID     `json:"mirror_workspaces"`
}

// MergeSnapshot is one member as a merge plan read it. Devices carry no
// approval code and no last-seen time. SharedWithMe lists the members
// sharing their agent account with it, OpenInvitations the open
// invitations it created or that link to it.
type MergeSnapshot struct {
	Member          domain.Member         `json:"member"`
	Identities      []*domain.Identity    `json:"identities"`
	Devices         []*domain.Device      `json:"devices"`
	Runs            int                   `json:"runs"`
	Schedules       int                   `json:"schedules"`
	Settings        MergeSettings         `json:"settings"`
	SharedWithMe    []domain.MemberID     `json:"shared_with_me"`
	OpenInvitations []domain.InvitationID `json:"open_invitations"`
}

// MergePlan is what merging two members would do. Minted is the member
// that minted the code, Proposer the one that entered it. Policy is the
// server's edge access policy and Admins counts its admins. Digest is the
// SHA-256 of all of it.
type MergePlan struct {
	CodeHash string        `json:"code_hash"`
	Minted   MergeSnapshot `json:"minted"`
	Proposer MergeSnapshot `json:"proposer"`
	Policy   string        `json:"policy"`
	Admins   int           `json:"admins"`
	Digest   string        `json:"-"`
}

// Side returns the plan's snapshot of member, nil when it is neither.
func (p *MergePlan) Side(member domain.MemberID) *MergeSnapshot {
	switch member {
	case p.Minted.Member.ID:
		return &p.Minted
	case p.Proposer.Member.ID:
		return &p.Proposer
	}
	return nil
}

// Other returns the snapshot of the member that is not member.
func (p *MergePlan) Other(member domain.MemberID) *MergeSnapshot {
	if member == p.Minted.Member.ID {
		return &p.Proposer
	}
	return &p.Minted
}

// Role is the merged member's role: the lower of the two, or admin when
// an admin side asked to keep it, the minter on accepting or the proposer
// on proposing. Neither side's flag lifts the other's role.
func (p *MergePlan) Role(mintedKeepAdmin, proposerKeepAdmin bool) domain.Role {
	keep := (mintedKeepAdmin && p.Minted.Member.Role == domain.RoleAdmin) ||
		(proposerKeepAdmin && p.Proposer.Member.Role == domain.RoleAdmin)
	return MergedRole(p.Minted.Member.Role, p.Proposer.Member.Role, keep)
}

// LeavesNoAdmin reports whether a merge with role as its result leaves
// the server without an admin.
func (p *MergePlan) LeavesNoAdmin(role domain.Role) bool {
	after := p.Admins
	for _, m := range []domain.Member{p.Minted.Member, p.Proposer.Member} {
		if m.Role == domain.RoleAdmin {
			after--
		}
	}
	if role == domain.RoleAdmin {
		after++
	}
	return after < 1
}

var roleRank = map[domain.Role]int{domain.RoleViewer: 0, domain.RoleCollaborator: 1, domain.RoleAdmin: 2}

// MergedRole is the role of two members merged: the lower of a and b,
// or admin when one of them is admin and keepAdmin is set.
func MergedRole(a, b domain.Role, keepAdmin bool) domain.Role {
	if keepAdmin && (a == domain.RoleAdmin || b == domain.RoleAdmin) {
		return domain.RoleAdmin
	}
	if roleRank[a] < roleRank[b] {
		return a
	}
	return b
}

// MergeProposal is the proposer's half of a merge: whose settings prevail
// and, from an admin proposer only, keeping the merged member admin. The
// proposal is open until Expires.
type MergeProposal struct {
	CodeHash  string
	Proposer  domain.MemberID
	Keep      domain.MemberID
	Digest    string
	KeepAdmin bool
	Policy    string
	Now       time.Time
	Expires   time.Time
}

// PendingMerge is an open proposal. Plan is recomputed when it is read;
// Refusal is set when the merge would now be refused.
type PendingMerge struct {
	ProposalID        string
	CodeHash          string
	Minted            domain.MemberID
	Proposer          domain.MemberID
	Keep              domain.MemberID
	ProposerKeepAdmin bool
	Digest            string
	ExpiresAt         time.Time
	Plan              *MergePlan
	Refusal           string
}

// MergeAccept is the minter's half: it accepts ProposalID, still with
// Digest, keeping the merged member admin only when the minter is the
// admin and KeepAdmin is set. OwnerProvider and OwnerSubject name the
// server's owner at its edge, empty without one. Archive moves the
// absorbed member's home aside for the merge and returns where it went,
// empty when it had none; it runs in the transaction once every row is
// merged, and undo puts the home back when the merge then fails, its error
// joining the merge's.
type MergeAccept struct {
	ProposalID      string
	Caller          domain.MemberID
	Digest          string
	KeepAdmin       bool
	Policy          string
	OwnerProvider   string
	OwnerSubject    string
	ConfirmedByKind string
	ConfirmedBy     string
	Now             time.Time
	Archive         func(mergeID string, absorbed domain.MemberID) (path string, undo func() error, err error)
	// Demoted runs in the transaction when the merged member loses the
	// admin role either had, to revoke what they issued outside the store;
	// undo puts it back when the merge then fails, and its error joins the
	// merge's.
	Demoted func(a, b domain.MemberID) (undo func() error, err error)
}

// MemberMerge is the audit record of one merge.
type MemberMerge struct {
	ID                 string
	SurvivorID         domain.MemberID
	AbsorbedID         domain.MemberID
	ConfirmedByKind    string
	ConfirmedBy        string
	SurvivorRoleBefore domain.Role
	AbsorbedRoleBefore domain.Role
	RoleAfter          domain.Role
	Discarded          MergeSettings
	Absorbed           MergeSnapshot
	ArchivePath        string
	CodeHash           string
	CreatedAt          time.Time
}

func (d *DB) CreateMergeCode(ctx context.Context, member domain.MemberID, hash string, now, expires time.Time) error {
	created, err := encodeTime(now)
	if err != nil {
		return fmt.Errorf("store: create merge code: %w", err)
	}
	until, err := encodeTime(expires)
	if err != nil {
		return fmt.Errorf("store: create merge code: %w", err)
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin create merge code: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM member_merge_codes WHERE consumed_at IS NULL AND ((member_id = ? AND proposal_id = '') OR expires_at <= ?)`,
		member, created); err != nil {
		return fmt.Errorf("store: create merge code: replace open codes: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO member_merge_codes (code_hash, member_id, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		hash, member, created, until); err != nil {
		return fmt.Errorf("store: create merge code: %w", mapConstraint(err, ErrNotFound))
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit create merge code: %w", err)
	}
	return nil
}

type mergeCode struct {
	hash              string
	member            domain.MemberID
	expires           time.Time
	proposalID        string
	proposer          domain.MemberID
	keep              domain.MemberID
	proposerKeepAdmin bool
	digest            string
	consumed          bool
	mergeID           string
}

const mergeCodeCols = `code_hash, member_id, expires_at, proposal_id, proposed_by, keep_settings_of, proposer_keep_admin,
	plan_digest, consumed_at, merge_id`

func scanMergeCode(row interface{ Scan(...any) error }) (*mergeCode, error) {
	var (
		c        mergeCode
		expires  int64
		consumed *int64
	)
	if err := row.Scan(&c.hash, &c.member, &expires, &c.proposalID, &c.proposer, &c.keep, &c.proposerKeepAdmin,
		&c.digest, &consumed, &c.mergeID); err != nil {
		return nil, err
	}
	c.expires, c.consumed = decodeTime(expires), consumed != nil
	return &c, nil
}

func getMergeCode(ctx context.Context, tx *sql.Tx, where string, arg any) (*mergeCode, error) {
	c, err := scanMergeCode(tx.QueryRowContext(ctx, `SELECT `+mergeCodeCols+` FROM member_merge_codes WHERE `+where, arg))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: merge code: %w", err)
	}
	return c, nil
}

// openCode returns the code with hash while it takes a proposal.
func openCode(ctx context.Context, tx *sql.Tx, hash string, now time.Time) (*mergeCode, error) {
	code, err := getMergeCode(ctx, tx, `code_hash = ?`, hash)
	switch {
	case errors.Is(err, ErrNotFound):
		return nil, refuseMerge("merge code unknown: check it, or mint a new one with aether member merge start on your other sign-in")
	case err != nil:
		return nil, err
	case code.consumed || code.proposalID != "":
		return nil, refuseMerge("merge code already used; mint a new one with aether member merge start")
	case !now.Before(code.expires):
		return nil, refuseMerge("merge code expired at %s; mint a new one with aether member merge start", code.expires.Format(time.RFC3339))
	}
	return code, nil
}

func (d *DB) PlanMerge(ctx context.Context, hash string, caller domain.MemberID, policy string, now time.Time) (*MergePlan, error) {
	tx, err := d.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("store: begin plan merge: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	code, err := openCode(ctx, tx, hash, now)
	if err != nil {
		return nil, err
	}
	return planInTx(ctx, tx, hash, code.member, caller, policy)
}

func planInTx(ctx context.Context, tx *sql.Tx, hash string, minted, proposer domain.MemberID, policy string) (*MergePlan, error) {
	if minted == proposer {
		return nil, refuseMerge("already the same member: %s minted this code; enter it from your other sign-in", proposer)
	}
	plan := &MergePlan{CodeHash: hash, Policy: policy}
	for _, side := range []struct {
		id  domain.MemberID
		out *MergeSnapshot
	}{{minted, &plan.Minted}, {proposer, &plan.Proposer}} {
		snap, err := mergeSnapshot(ctx, tx, side.id)
		if err != nil {
			return nil, err
		}
		*side.out = *snap
	}
	a, b := plan.Minted.Member, plan.Proposer.Member
	for _, m := range []domain.Member{a, b} {
		if m.Pending {
			return nil, refuseMerge("member %s is pending; approve first: aether member approve %s", m.ID, m.ID)
		}
	}
	if a.TailnetLogin != "" && b.TailnetLogin != "" {
		return nil, refuseMerge("both members have a tailnet login (%s: %s, %s: %s) and a member has at most one; these are two tailnet users, not one person's two sign-ins",
			a.ID, a.TailnetLogin, b.ID, b.TailnetLogin)
	}
	if a.PublicKey != "" && b.PublicKey != "" {
		return nil, refuseMerge("both members have an SSH public key (%s and %s) and a member has at most one; reach the server with one key and merge the other sign-in", a.ID, b.ID)
	}
	if err := refuseLiveWork(ctx, tx, a.ID, b.ID); err != nil {
		return nil, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM members WHERE role = 'admin' AND merged_into = ''`).Scan(&plan.Admins); err != nil {
		return nil, fmt.Errorf("store: plan merge: count admins: %w", err)
	}
	body, err := json.Marshal(plan)
	if err != nil {
		return nil, fmt.Errorf("store: plan merge: digest: %w", err)
	}
	sum := sha256.Sum256(body)
	plan.Digest = hex.EncodeToString(sum[:])
	return plan, nil
}

// refuseLiveWork refuses a merge while either member has a run that is not
// finished, an Environment terminal, a handoff
// still publishing, a queued steer request or an approved delivery not yet
// made: those still act as the member, beyond what a merge moves.
func refuseLiveWork(ctx context.Context, tx *sql.Tx, a, b domain.MemberID) error {
	var live []string
	for _, s := range domain.AllRunStatuses {
		if !s.Terminal() {
			live = append(live, "'"+string(s)+"'")
		}
	}
	runs, err := queryStrings(ctx, tx, `SELECT id FROM runs WHERE status IN (`+strings.Join(live, ", ")+`)
		AND (member_id IN (?1, ?2) OR account_member_id IN (?1, ?2) OR home_member_id IN (?1, ?2)
		     OR requested_account_member_id IN (?1, ?2)) ORDER BY id`, a, b)
	if err != nil {
		return err
	}
	terminals, err := queryStrings(ctx, tx, `SELECT member_id FROM member_terminals WHERE member_id IN (?1, ?2) ORDER BY member_id`, a, b)
	if err != nil {
		return err
	}
	handoffs, err := queryStrings(ctx, tx, `SELECT id FROM handoff_outbox
		WHERE (actor_id IN (?1, ?2) OR from_member_id IN (?1, ?2) OR to_member_id IN (?1, ?2))
		AND 'pending' IN (evidence_state, publication_state, timeline_state, coauthor_state) ORDER BY id`, a, b)
	if err != nil {
		return err
	}
	steers, err := queryStrings(ctx, tx, `SELECT id FROM room_messages
		WHERE kind = 'steer_request' AND state = 'queued' AND actor_id IN (?1, ?2) ORDER BY id`, a, b)
	if err != nil {
		return err
	}
	var stop []string
	if len(steers) > 0 {
		stop = append(stop, "queued steer requests "+strings.Join(steers, ", ")+" (approve or deny them in the run's room)")
	}
	if len(handoffs) > 0 {
		stop = append(stop, "handoffs "+strings.Join(handoffs, ", ")+" (they finish publishing on their own; retry in a minute)")
	}
	if len(runs) > 0 {
		stop = append(stop, "runs "+strings.Join(runs, ", ")+" (aether kill <run-id>, or aether close <run-id>)")
	}
	for _, m := range terminals {
		stop = append(stop, "the Environment terminal of member "+m+" (aether terminal stop on that sign-in)")
	}
	if len(stop) > 0 {
		return refuseMerge("stop these first, then merge: %s", strings.Join(stop, "; "))
	}
	return nil
}

func queryStrings(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]string, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: plan merge: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only iteration
	out := []string{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, fmt.Errorf("store: plan merge: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func idsOf[T ~string](in []string) []T {
	out := make([]T, len(in))
	for i, s := range in {
		out[i] = T(s)
	}
	return out
}

func mergeSnapshot(ctx context.Context, tx *sql.Tx, id domain.MemberID) (*MergeSnapshot, error) {
	var mergedInto string
	if err := tx.QueryRowContext(ctx, `SELECT merged_into FROM members WHERE id = ?`, id).Scan(&mergedInto); errors.Is(err, sql.ErrNoRows) {
		return nil, refuseMerge("member %s no longer exists", id)
	} else if err != nil {
		return nil, fmt.Errorf("store: plan merge: member %s: %w", id, err)
	}
	if mergedInto != "" {
		return nil, refuseMerge("member %s was merged into %s already", id, mergedInto)
	}
	m, err := scanMember(tx.QueryRowContext(ctx, `SELECT `+memberCols+` FROM members WHERE id = ?`, id))
	if err != nil {
		return nil, fmt.Errorf("store: plan merge: member %s: %w", id, err)
	}
	snap := &MergeSnapshot{Member: *m}
	s := &snap.Settings
	s.DisplayName, s.Color, s.GitName, s.GitEmail, s.Image = m.DisplayName, m.Color, m.GitName, m.GitEmail, m.Image
	rows, err := tx.QueryContext(ctx, `SELECT `+identityCols+` FROM member_identities WHERE member_id = ? ORDER BY provider, subject`, id)
	if err != nil {
		return nil, fmt.Errorf("store: plan merge: identities: %w", err)
	}
	if snap.Identities, err = collect(rows, scanIdentity); err != nil {
		return nil, err
	}
	if rows, err = tx.QueryContext(ctx, `SELECT `+deviceCols+` FROM member_devices WHERE member_id = ? ORDER BY created_at, id`, id); err != nil {
		return nil, fmt.Errorf("store: plan merge: devices: %w", err)
	}
	if snap.Devices, err = collect(rows, scanDevice); err != nil {
		return nil, err
	}
	for _, dev := range snap.Devices {
		dev.ApprovalCode, dev.LastSeenAt = "", nil
	}
	if err = tx.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM runs WHERE member_id = ?1), (SELECT COUNT(*) FROM schedules WHERE member_id = ?1)`, id).
		Scan(&snap.Runs, &snap.Schedules); err != nil {
		return nil, fmt.Errorf("store: plan merge: counts: %w", err)
	}
	if rows, err = tx.QueryContext(ctx, `SELECT name, definition FROM harness_definitions WHERE member_id = ? ORDER BY name`, id); err != nil {
		return nil, fmt.Errorf("store: plan merge: harness definitions: %w", err)
	}
	defs, err := collect(rows, func(row interface{ Scan(...any) error }) (*MergeHarnessDefinition, error) {
		var d MergeHarnessDefinition
		var raw []byte
		if serr := row.Scan(&d.Name, &raw); serr != nil {
			return nil, serr
		}
		d.Definition = string(raw)
		return &d, nil
	})
	if err != nil {
		return nil, err
	}
	s.HarnessDefinitions = derefAll(defs)
	if rows, err = tx.QueryContext(ctx, `SELECT harness, snapshot_id FROM profile_heads WHERE member_id = ? ORDER BY harness`, id); err != nil {
		return nil, fmt.Errorf("store: plan merge: profile heads: %w", err)
	}
	heads, err := collect(rows, func(row interface{ Scan(...any) error }) (*MergeProfileHead, error) {
		var h MergeProfileHead
		return &h, row.Scan(&h.Harness, &h.SnapshotID)
	})
	if err != nil {
		return nil, err
	}
	s.ProfileHeads = derefAll(heads)
	lists := []struct {
		query string
		set   func([]string)
	}{
		{`SELECT grantee_member_id FROM account_shares WHERE owner_member_id = ? ORDER BY 1`, func(v []string) { s.AccountSharedWith = idsOf[domain.MemberID](v) }},
		{`SELECT owner_member_id FROM account_shares WHERE grantee_member_id = ? ORDER BY 1`, func(v []string) { snap.SharedWithMe = idsOf[domain.MemberID](v) }},
		{`SELECT workspace_id FROM workspace_mirrors WHERE github_member_id = ? ORDER BY 1`, func(v []string) { s.MirrorWorkspaces = idsOf[domain.WorkspaceID](v) }},
		{`SELECT id FROM identity_invitations WHERE consumed_at IS NULL AND (created_by = ?1 OR member_id = ?1) ORDER BY 1`,
			func(v []string) { snap.OpenInvitations = idsOf[domain.InvitationID](v) }},
	}
	for _, l := range lists {
		v, err := queryStrings(ctx, tx, l.query, id)
		if err != nil {
			return nil, err
		}
		l.set(v)
	}
	return snap, nil
}

func derefAll[T any](in []*T) []T {
	out := make([]T, len(in))
	for i, v := range in {
		out[i] = *v
	}
	return out
}

func (d *DB) ProposeMerge(ctx context.Context, p MergeProposal) (*PendingMerge, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("store: begin propose merge: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if made, merr := getMergeCode(ctx, tx, `code_hash = ?`, p.CodeHash); merr == nil && made.proposalID != "" && !made.consumed &&
		made.proposer == p.Proposer && made.keep == p.Keep && made.digest == p.Digest && made.proposerKeepAdmin == p.KeepAdmin &&
		p.Now.Before(made.expires) {
		return &PendingMerge{ProposalID: made.proposalID, CodeHash: made.hash, Minted: made.member, Proposer: made.proposer,
			Keep: made.keep, ProposerKeepAdmin: made.proposerKeepAdmin, Digest: made.digest, ExpiresAt: made.expires}, nil
	}
	code, err := openCode(ctx, tx, p.CodeHash, p.Now)
	if err != nil {
		return nil, err
	}
	plan, err := planInTx(ctx, tx, p.CodeHash, code.member, p.Proposer, p.Policy)
	if err != nil {
		return nil, err
	}
	switch {
	case plan.Digest != p.Digest:
		return nil, refuseMerge("the members changed since the preview; preview again")
	case plan.Side(p.Keep) == nil:
		return nil, refuseMerge("keep_settings_of must be %s or %s, the two members this code merges", code.member, p.Proposer)
	case p.KeepAdmin && plan.Proposer.Member.Role != domain.RoleAdmin:
		return nil, refuseMerge("keep_admin is the admin's to ask for, and you are %s; the admin asks for it when accepting", plan.Proposer.Member.Role)
	}
	id, err := newID("mrp")
	if err != nil {
		return nil, err
	}
	proposed, err := encodeTime(p.Now)
	if err != nil {
		return nil, fmt.Errorf("store: propose merge: %w", err)
	}
	expires, err := encodeTime(p.Expires)
	if err != nil {
		return nil, fmt.Errorf("store: propose merge: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE member_merge_codes SET proposal_id = ?, proposed_by = ?, keep_settings_of = ?,
		proposer_keep_admin = ?, plan_digest = ?, proposed_at = ?, expires_at = ? WHERE code_hash = ?`,
		id, p.Proposer, p.Keep, p.KeepAdmin, plan.Digest, proposed, expires, p.CodeHash); err != nil {
		return nil, fmt.Errorf("store: propose merge: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("store: commit propose merge: %w", err)
	}
	return &PendingMerge{ProposalID: id, CodeHash: p.CodeHash, Minted: code.member, Proposer: p.Proposer, Keep: p.Keep,
		ProposerKeepAdmin: p.KeepAdmin, Digest: plan.Digest, ExpiresAt: p.Expires, Plan: plan}, nil
}

func (d *DB) PendingMerges(ctx context.Context, member domain.MemberID, policy string, now time.Time) ([]*PendingMerge, error) {
	at, err := encodeTime(now)
	if err != nil {
		return nil, fmt.Errorf("store: pending merges: %w", err)
	}
	tx, err := d.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("store: begin pending merges: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT `+mergeCodeCols+` FROM member_merge_codes
		WHERE proposal_id <> '' AND consumed_at IS NULL AND expires_at > ?1 AND (member_id = ?2 OR proposed_by = ?2)
		ORDER BY proposed_at, proposal_id`, at, member)
	if err != nil {
		return nil, fmt.Errorf("store: pending merges: %w", err)
	}
	codes, err := collect(rows, scanMergeCode)
	if err != nil {
		return nil, err
	}
	out := make([]*PendingMerge, 0, len(codes))
	for _, c := range codes {
		pm := &PendingMerge{ProposalID: c.proposalID, CodeHash: c.hash, Minted: c.member, Proposer: c.proposer, Keep: c.keep,
			ProposerKeepAdmin: c.proposerKeepAdmin, Digest: c.digest, ExpiresAt: c.expires}
		pm.Plan, err = planInTx(ctx, tx, c.hash, c.member, c.proposer, policy)
		var refused *MergeRefusal
		switch {
		case errors.As(err, &refused):
			pm.Plan, pm.Refusal = nil, refused.Message
		case err != nil:
			return nil, err
		case pm.Plan.Digest != c.digest:
			pm.Refusal = "the members changed since the proposal; cancel it with aether member merge cancel and propose again"
		}
		out = append(out, pm)
	}
	return out, nil
}

func (d *DB) AcceptMerge(ctx context.Context, a MergeAccept) (_ *MemberMerge, err error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("store: begin merge: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	code, err := getMergeCode(ctx, tx, `proposal_id <> '' AND proposal_id = ?`, a.ProposalID)
	if errors.Is(err, ErrNotFound) {
		return nil, refuseMerge("merge proposal %s unknown or cancelled", a.ProposalID)
	}
	if err != nil {
		return nil, err
	}
	if code.consumed {
		done, gerr := getMerge(ctx, tx, `?`, code.mergeID)
		if gerr != nil {
			return nil, gerr
		}
		if done.SurvivorID == a.Caller {
			return done, nil
		}
		return nil, refuseMerge("merge proposal %s was accepted already, as merge %s", a.ProposalID, done.ID)
	}
	switch {
	case code.member != a.Caller:
		return nil, refuseMerge("only member %s, which minted the code, accepts this proposal: run aether member merge accept on that sign-in", code.member)
	case !a.Now.Before(code.expires):
		return nil, refuseMerge("merge proposal %s expired at %s; mint a new code with aether member merge start", a.ProposalID, code.expires.Format(time.RFC3339))
	case a.Digest != code.digest:
		return nil, refuseMerge("this is not the plan that was proposed; read it again with aether member merge accept")
	}
	plan, err := planInTx(ctx, tx, code.hash, code.member, code.proposer, a.Policy)
	if err != nil {
		return nil, err
	}
	switch {
	case plan.Digest != code.digest:
		return nil, refuseMerge("the members changed since the proposal; cancel it with aether member merge cancel and propose again")
	case a.KeepAdmin && plan.Minted.Member.Role != domain.RoleAdmin:
		return nil, refuseMerge("keep_admin is the admin's to ask for, and you are %s", plan.Minted.Member.Role)
	}
	role := plan.Role(a.KeepAdmin, code.proposerKeepAdmin)
	if plan.LeavesNoAdmin(role) {
		return nil, refuseMerge("refusing to demote the server's last admin: the merged member would be %s; the admin side asks to keep it admin with --keep-admin", role)
	}
	if role != domain.RoleAdmin && a.OwnerSubject != "" {
		for _, side := range []MergeSnapshot{plan.Minted, plan.Proposer} {
			for _, id := range side.Identities {
				if id.Provider == a.OwnerProvider && id.Subject == a.OwnerSubject {
					return nil, refuseMerge("%s account %s owns this server at its edge and the merged member would be %s; transfer ownership to another admin first with aether member transfer <member id>, or keep it admin",
						id.Provider, identityName(id), role)
				}
			}
		}
	}
	survivor, absorbed := plan.Side(code.keep), plan.Other(code.keep)
	merge := &MemberMerge{
		SurvivorID: survivor.Member.ID, AbsorbedID: absorbed.Member.ID,
		ConfirmedByKind: a.ConfirmedByKind, ConfirmedBy: a.ConfirmedBy,
		SurvivorRoleBefore: survivor.Member.Role, AbsorbedRoleBefore: absorbed.Member.Role, RoleAfter: role,
		Discarded: absorbed.Settings, Absorbed: *absorbed, CodeHash: code.hash, CreatedAt: a.Now,
	}
	if merge.ID, err = newID("mrg"); err != nil {
		return nil, err
	}
	committed := false
	demoted := role != domain.RoleAdmin && (survivor.Member.Role == domain.RoleAdmin || absorbed.Member.Role == domain.RoleAdmin)
	if err = mergeRows(ctx, tx, survivor.Member, absorbed.Member, role, demoted); err != nil {
		return nil, err
	}
	if demoted && a.Demoted != nil {
		undo, derr := a.Demoted(survivor.Member.ID, absorbed.Member.ID)
		defer func() {
			if !committed && undo != nil {
				if uerr := undo(); uerr != nil {
					err = errors.Join(err, fmt.Errorf("store: merge: reinstate what the demoted admin issued: %w", uerr))
				}
			}
		}()
		if derr != nil {
			return nil, fmt.Errorf("store: merge: revoke what the demoted admin issued: %w", derr)
		}
	}
	if a.Archive != nil {
		var undo func() error
		if merge.ArchivePath, undo, err = a.Archive(merge.ID, absorbed.Member.ID); err != nil {
			return nil, fmt.Errorf("store: merge: archive the absorbed member's home: %w", err)
		}
		if undo != nil {
			defer func() {
				if !committed {
					if uerr := undo(); uerr != nil {
						err = errors.Join(err, fmt.Errorf("store: merge: put back the absorbed member's home: %w", uerr))
					}
				}
			}()
		}
	}
	if err = insertMerge(ctx, tx, merge); err != nil {
		return nil, err
	}
	consumed, err := encodeTime(a.Now)
	if err != nil {
		return nil, fmt.Errorf("store: merge: %w", err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE member_merge_codes SET consumed_at = ?, merge_id = ? WHERE code_hash = ?`,
		consumed, merge.ID, code.hash); err != nil {
		return nil, fmt.Errorf("store: merge: consume code: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("store: commit merge: %w", err)
	}
	committed = true
	return merge, nil
}

func identityName(id *domain.Identity) string {
	switch {
	case id.Login != "":
		return id.Login
	case id.Email != "":
		return id.Email
	}
	return id.Subject
}

func (d *DB) CancelMerge(ctx context.Context, member domain.MemberID, proposal string) (int, error) {
	res, err := d.db.ExecContext(ctx, `DELETE FROM member_merge_codes WHERE consumed_at IS NULL
		AND (member_id = ?1 OR proposed_by = ?1) AND (?2 = '' OR proposal_id = ?2)`, member, proposal)
	if err != nil {
		return 0, fmt.Errorf("store: cancel merge: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: cancel merge: %w", err)
	}
	return int(n), nil
}

const mergeCols = `id, survivor_id, absorbed_id, confirmed_by_kind, confirmed_by, survivor_role_before,
	absorbed_role_before, role_after, discarded, absorbed, archive_path, code_hash, created_at`

func insertMerge(ctx context.Context, tx *sql.Tx, m *MemberMerge) error {
	discarded, err := json.Marshal(m.Discarded)
	if err != nil {
		return fmt.Errorf("store: merge: encode discarded settings: %w", err)
	}
	absorbed, err := json.Marshal(m.Absorbed)
	if err != nil {
		return fmt.Errorf("store: merge: encode absorbed member: %w", err)
	}
	created, err := encodeTime(m.CreatedAt)
	if err != nil {
		return fmt.Errorf("store: merge: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO member_merges (`+mergeCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.SurvivorID, m.AbsorbedID, m.ConfirmedByKind, m.ConfirmedBy, m.SurvivorRoleBefore, m.AbsorbedRoleBefore,
		m.RoleAfter, string(discarded), string(absorbed), m.ArchivePath, m.CodeHash, created); err != nil {
		return fmt.Errorf("store: merge: record: %w", err)
	}
	return nil
}

func scanMerge(row interface{ Scan(...any) error }) (*MemberMerge, error) {
	var (
		m                   MemberMerge
		discarded, absorbed string
		created             int64
	)
	if err := row.Scan(&m.ID, &m.SurvivorID, &m.AbsorbedID, &m.ConfirmedByKind, &m.ConfirmedBy, &m.SurvivorRoleBefore,
		&m.AbsorbedRoleBefore, &m.RoleAfter, &discarded, &absorbed, &m.ArchivePath, &m.CodeHash, &created); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(discarded), &m.Discarded); err != nil {
		return nil, fmt.Errorf("merge %s: discarded settings: %w", m.ID, err)
	}
	if err := json.Unmarshal([]byte(absorbed), &m.Absorbed); err != nil {
		return nil, fmt.Errorf("merge %s: absorbed member: %w", m.ID, err)
	}
	m.CreatedAt = decodeTime(created)
	return &m, nil
}

// getMerge returns the merge whose id is the SQL expression idExpr.
func getMerge(ctx context.Context, q queryRower, idExpr string, args ...any) (*MemberMerge, error) {
	m, err := scanMerge(q.QueryRowContext(ctx, `SELECT `+mergeCols+` FROM member_merges WHERE id = `+idExpr, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get merge: %w", err)
	}
	return m, nil
}

func (d *DB) MergeOfProposal(ctx context.Context, proposal string) (*MemberMerge, error) {
	return getMerge(ctx, d.db, `(SELECT merge_id FROM member_merge_codes WHERE proposal_id <> '' AND proposal_id = ?)`, proposal)
}

func (d *DB) ListMerges(ctx context.Context, survivor domain.MemberID) ([]*MemberMerge, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT `+mergeCols+` FROM member_merges
		WHERE (? = '' OR survivor_id = ?) ORDER BY created_at DESC, id DESC`, survivor, survivor)
	if err != nil {
		return nil, fmt.Errorf("store: list merges: %w", err)
	}
	return collect(rows, scanMerge)
}

func (d *DB) MergedInto(ctx context.Context, member domain.MemberID) (domain.MemberID, error) {
	var into domain.MemberID
	err := d.db.QueryRowContext(ctx, `SELECT merged_into FROM members WHERE id = ?`, member).Scan(&into)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("store: merged into: %w", err)
	}
	return into, nil
}

func (d *DB) FinishedRunsOf(ctx context.Context, a, b domain.MemberID) ([]*domain.Run, error) {
	var finished []string
	for _, s := range domain.AllRunStatuses {
		if s.Terminal() {
			finished = append(finished, "'"+string(s)+"'")
		}
	}
	return d.listRuns(ctx, fmt.Sprintf("finished runs of %s and %s", a, b), runSnapshotQuery(`runs.status IN (`+strings.Join(finished, ", ")+`)
		AND (runs.member_id IN (?, ?) OR runs.account_member_id IN (?, ?) OR runs.home_member_id IN (?, ?))`)+
		` ORDER BY runs.created_at, runs.rowid`, a, b, a, b, a, b)
}
