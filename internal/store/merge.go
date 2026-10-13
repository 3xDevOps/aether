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

// MergeStore keeps member merges: the codes that start them, the plan
// that previews one, the merge itself and its audit record.
type MergeStore interface {
	// CreateMergeCode records the code with hash for member until expires,
	// replacing member's open code, and drops every expired open code.
	// keepAdmin is member's consent, as an admin, to keep the merged
	// member admin.
	CreateMergeCode(ctx context.Context, member domain.MemberID, hash string, keepAdmin bool, now, expires time.Time) error
	// PlanMerge computes what confirming the code with hash from caller's
	// member would do at now. A merge the plan refuses is a *MergeRefusal.
	PlanMerge(ctx context.Context, hash string, caller domain.MemberID, now time.Time) (*MergePlan, error)
	// ConfirmMerge recomputes the plan and, while it still has c.Digest,
	// merges in one transaction; see MergeConfirm.
	ConfirmMerge(ctx context.Context, c MergeConfirm) (*MemberMerge, error)
	// MergeOfCode returns the merge that consumed the code with hash;
	// ErrNotFound while it is open or unknown.
	MergeOfCode(ctx context.Context, hash string) (*MemberMerge, error)
	// ListMerges returns the merges into survivor, or every merge when
	// survivor is empty, newest first.
	ListMerges(ctx context.Context, survivor domain.MemberID) ([]*MemberMerge, error)
}

var _ MergeStore = (*DB)(nil)

// MergeRefusal is a merge the store refuses, with the message to show.
type MergeRefusal struct {
	Message string
	// SameMember is set when the code's member is the caller's.
	SameMember bool
}

func (e *MergeRefusal) Error() string { return e.Message }

func refuseMerge(format string, args ...any) error {
	return &MergeRefusal{Message: fmt.Sprintf(format, args...)}
}

// MergeHarnessDefinition is one discarded custom agent definition.
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
// approval code and no last-seen time.
type MergeSnapshot struct {
	Member     domain.Member      `json:"member"`
	Identities []*domain.Identity `json:"identities"`
	Devices    []*domain.Device   `json:"devices"`
	Runs       int                `json:"runs"`
	Schedules  int                `json:"schedules"`
	Settings   MergeSettings      `json:"settings"`
}

// MergePlan is what confirming a merge code would do. Minted is the
// member that minted the code, Confirming the caller's. MintedKeepAdmin is
// the minter's consent to keep the merged member admin. Admins counts the
// server's admins. Digest is the SHA-256 of all of it.
type MergePlan struct {
	CodeHash        string        `json:"code_hash"`
	Minted          MergeSnapshot `json:"minted"`
	Confirming      MergeSnapshot `json:"confirming"`
	MintedKeepAdmin bool          `json:"minted_keep_admin"`
	Admins          int           `json:"admins"`
	Digest          string        `json:"-"`
}

// KeepAdminConsented reports whether the admin side asked to keep the
// merged member admin: the confirming member is an admin, or an admin
// minted the code with keep_admin. The other side asking cannot raise
// its own role.
func (p *MergePlan) KeepAdminConsented() bool {
	return p.Confirming.Member.Role == domain.RoleAdmin || (p.Minted.Member.Role == domain.RoleAdmin && p.MintedKeepAdmin)
}

// Side returns the plan's snapshot of member, nil when it is neither.
func (p *MergePlan) Side(member domain.MemberID) *MergeSnapshot {
	switch member {
	case p.Minted.Member.ID:
		return &p.Minted
	case p.Confirming.Member.ID:
		return &p.Confirming
	}
	return nil
}

// Other returns the snapshot of the member that is not member.
func (p *MergePlan) Other(member domain.MemberID) *MergeSnapshot {
	if member == p.Minted.Member.ID {
		return &p.Confirming
	}
	return &p.Minted
}

// LeavesNoAdmin reports whether a merge with role as its result leaves
// the server without an admin.
func (p *MergePlan) LeavesNoAdmin(role domain.Role) bool {
	after := p.Admins
	for _, m := range []domain.Member{p.Minted.Member, p.Confirming.Member} {
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
// or admin when one of them is admin and keepAdmin is set. A merge never
// grants a permission neither member had.
func MergedRole(a, b domain.Role, keepAdmin bool) domain.Role {
	if keepAdmin && (a == domain.RoleAdmin || b == domain.RoleAdmin) {
		return domain.RoleAdmin
	}
	if roleRank[a] < roleRank[b] {
		return a
	}
	return b
}

// MergeConfirm is one confirmed merge. Keep is the member whose id and
// settings survive; the other is absorbed. Archive moves the absorbed
// member's home aside for the merge and returns where it went, empty when
// it had none; it runs in the transaction once every row is merged, and
// undo puts the home back when the commit fails.
type MergeConfirm struct {
	CodeHash        string
	Caller          domain.MemberID
	Keep            domain.MemberID
	Digest          string
	KeepAdmin       bool
	ConfirmedByKind string
	ConfirmedBy     string
	Now             time.Time
	Archive         func(mergeID string, absorbed domain.MemberID) (path string, undo func(), err error)
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

func (d *DB) CreateMergeCode(ctx context.Context, member domain.MemberID, hash string, keepAdmin bool, now, expires time.Time) error {
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
		`DELETE FROM member_merge_codes WHERE consumed_at IS NULL AND (member_id = ? OR expires_at <= ?)`,
		member, created); err != nil {
		return fmt.Errorf("store: create merge code: replace open codes: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO member_merge_codes (code_hash, member_id, created_at, expires_at, keep_admin) VALUES (?, ?, ?, ?, ?)`,
		hash, member, created, until, keepAdmin); err != nil {
		return fmt.Errorf("store: create merge code: %w", mapConstraint(err, ErrNotFound))
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit create merge code: %w", err)
	}
	return nil
}

type mergeCode struct {
	member    domain.MemberID
	expires   time.Time
	consumed  bool
	mergeID   string
	keepAdmin bool
}

func getMergeCode(ctx context.Context, tx *sql.Tx, hash string) (*mergeCode, error) {
	var (
		c        mergeCode
		expires  int64
		consumed *int64
	)
	err := tx.QueryRowContext(ctx,
		`SELECT member_id, expires_at, consumed_at, merge_id, keep_admin FROM member_merge_codes WHERE code_hash = ?`, hash).
		Scan(&c.member, &expires, &consumed, &c.mergeID, &c.keepAdmin)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, refuseMerge("merge code unknown: check it, or mint a new one with aether member merge start on your other sign-in")
	}
	if err != nil {
		return nil, fmt.Errorf("store: merge code: %w", err)
	}
	c.expires, c.consumed = decodeTime(expires), consumed != nil
	return &c, nil
}

func (d *DB) PlanMerge(ctx context.Context, hash string, caller domain.MemberID, now time.Time) (*MergePlan, error) {
	tx, err := d.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("store: begin plan merge: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	code, err := getMergeCode(ctx, tx, hash)
	if err != nil {
		return nil, err
	}
	return planInTx(ctx, tx, hash, code, caller, now)
}

func planInTx(ctx context.Context, tx *sql.Tx, hash string, code *mergeCode, caller domain.MemberID, now time.Time) (*MergePlan, error) {
	switch {
	case code.consumed:
		return nil, refuseMerge("merge code already used; mint a new one with aether member merge start")
	case !now.Before(code.expires):
		return nil, refuseMerge("merge code expired at %s; mint a new one with aether member merge start", code.expires.Format(time.RFC3339))
	case code.member == caller:
		return nil, &MergeRefusal{SameMember: true, Message: fmt.Sprintf(
			"already the same member: %s minted this code; enter it from your other sign-in", caller)}
	}
	plan := &MergePlan{CodeHash: hash, MintedKeepAdmin: code.keepAdmin}
	for _, side := range []struct {
		id  domain.MemberID
		out *MergeSnapshot
	}{{code.member, &plan.Minted}, {caller, &plan.Confirming}} {
		snap, err := mergeSnapshot(ctx, tx, side.id)
		if err != nil {
			return nil, err
		}
		*side.out = *snap
	}
	a, b := plan.Minted.Member, plan.Confirming.Member
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
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM members WHERE role = 'admin'`).Scan(&plan.Admins); err != nil {
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
// finished, an Environment terminal, or a role in an open swarm: those
// hold the member's id in memory, beyond what a merge rewrites.
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
	missions, err := queryStrings(ctx, tx, `SELECT id FROM missions m WHERE phase IN ('planning', 'active') AND (
		accountable_human_id IN (?1, ?2) OR integrator_account_member_id IN (?1, ?2)
		OR integrator_authorizing_human_id IN (?1, ?2) OR integrator_run_owner_id IN (?1, ?2)
		OR instr(execution_choices, '"' || ?1 || '"') > 0 OR instr(execution_choices, '"' || ?2 || '"') > 0
		OR EXISTS (SELECT 1 FROM mission_attempts t WHERE t.mission_id = m.id AND (t.authorizing_human_id IN (?1, ?2)
		     OR t.run_owner_id IN (?1, ?2) OR t.account_owner_id IN (?1, ?2) OR t.launch_account_id IN (?1, ?2)))
		OR EXISTS (SELECT 1 FROM mission_worker_takeovers w WHERE w.mission_id = m.id AND w.active = 1 AND w.member_id IN (?1, ?2))
	) ORDER BY id`, a, b)
	if err != nil {
		return err
	}
	var stop []string
	if len(runs) > 0 {
		stop = append(stop, "runs "+strings.Join(runs, ", ")+" (aether kill <run-id>, or aether close <run-id>)")
	}
	for _, m := range terminals {
		stop = append(stop, "the Environment terminal of member "+m+" (aether terminal stop on that sign-in)")
	}
	if len(missions) > 0 {
		stop = append(stop, "swarms "+strings.Join(missions, ", ")+" (aether swarm cancel <swarm-id>, or let them finish)")
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
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, fmt.Errorf("store: plan merge: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func mergeSnapshot(ctx context.Context, tx *sql.Tx, id domain.MemberID) (*MergeSnapshot, error) {
	m, err := scanMember(tx.QueryRowContext(ctx, `SELECT `+memberCols+` FROM members WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, refuseMerge("member %s no longer exists", id)
	}
	if err != nil {
		return nil, fmt.Errorf("store: plan merge: member %s: %w", id, err)
	}
	snap := &MergeSnapshot{Member: *m, Identities: []*domain.Identity{}, Devices: []*domain.Device{}}
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
	if err := tx.QueryRowContext(ctx, `SELECT
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
		if err := row.Scan(&d.Name, &raw); err != nil {
			return nil, err
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
	grantees, err := queryStrings(ctx, tx, `SELECT grantee_member_id FROM account_shares WHERE owner_member_id = ? ORDER BY grantee_member_id`, id)
	if err != nil {
		return nil, err
	}
	s.AccountSharedWith = make([]domain.MemberID, len(grantees))
	for i, g := range grantees {
		s.AccountSharedWith[i] = domain.MemberID(g)
	}
	mirrors, err := queryStrings(ctx, tx, `SELECT workspace_id FROM workspace_mirrors WHERE github_member_id = ? ORDER BY workspace_id`, id)
	if err != nil {
		return nil, err
	}
	s.MirrorWorkspaces = make([]domain.WorkspaceID, len(mirrors))
	for i, w := range mirrors {
		s.MirrorWorkspaces[i] = domain.WorkspaceID(w)
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

func (d *DB) ConfirmMerge(ctx context.Context, c MergeConfirm) (*MemberMerge, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("store: begin merge: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	code, err := getMergeCode(ctx, tx, c.CodeHash)
	if err != nil {
		return nil, err
	}
	if code.consumed && code.mergeID != "" {
		done, err := getMerge(ctx, tx, `?`, code.mergeID)
		if err != nil {
			return nil, err
		}
		if done.SurvivorID == c.Caller {
			return done, nil
		}
	}
	plan, err := planInTx(ctx, tx, c.CodeHash, code, c.Caller, c.Now)
	if err != nil {
		return nil, err
	}
	if plan.Digest != c.Digest {
		return nil, refuseMerge("the members changed since the preview; preview again")
	}
	survivor := plan.Side(c.Keep)
	if survivor == nil {
		return nil, refuseMerge("keep_settings_of must be %s or %s, the two members this code merges", plan.Minted.Member.ID, plan.Confirming.Member.ID)
	}
	absorbed := plan.Other(c.Keep)
	keepAdmin := c.KeepAdmin && MergedRole(survivor.Member.Role, absorbed.Member.Role, true) == domain.RoleAdmin
	if keepAdmin && !plan.KeepAdminConsented() {
		return nil, refuseMerge("keep_admin must come from the admin's sign-in: mint a new code there with aether member merge start --keep-admin")
	}
	role := MergedRole(survivor.Member.Role, absorbed.Member.Role, keepAdmin)
	if plan.LeavesNoAdmin(role) {
		return nil, refuseMerge("refusing to demote the server's last admin: the merged member would be %s; confirm with keep_admin to keep it admin", role)
	}
	merge := &MemberMerge{
		SurvivorID: survivor.Member.ID, AbsorbedID: absorbed.Member.ID,
		ConfirmedByKind: c.ConfirmedByKind, ConfirmedBy: c.ConfirmedBy,
		SurvivorRoleBefore: survivor.Member.Role, AbsorbedRoleBefore: absorbed.Member.Role, RoleAfter: role,
		Discarded: absorbed.Settings, Absorbed: *absorbed, CodeHash: c.CodeHash, CreatedAt: c.Now,
	}
	if merge.ID, err = newID("mrg"); err != nil {
		return nil, err
	}
	if err := mergeRows(ctx, tx, survivor.Member, absorbed.Member, role); err != nil {
		return nil, err
	}
	var undo func()
	if c.Archive != nil {
		if merge.ArchivePath, undo, err = c.Archive(merge.ID, absorbed.Member.ID); err != nil {
			return nil, fmt.Errorf("store: merge: archive the absorbed member's home: %w", err)
		}
	}
	if err := insertMerge(ctx, tx, merge); err != nil {
		return nil, err
	}
	consumed, err := encodeTime(c.Now)
	if err != nil {
		return nil, fmt.Errorf("store: merge: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE member_merge_codes SET consumed_at = ?, merge_id = ? WHERE code_hash = ?`,
		consumed, merge.ID, c.CodeHash); err != nil {
		return nil, fmt.Errorf("store: merge: consume code: %w", err)
	}
	if err := tx.Commit(); err != nil {
		if undo != nil {
			undo()
		}
		return nil, fmt.Errorf("store: commit merge: %w", err)
	}
	return merge, nil
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

func (d *DB) MergeOfCode(ctx context.Context, hash string) (*MemberMerge, error) {
	return getMerge(ctx, d.db, `(SELECT merge_id FROM member_merge_codes WHERE code_hash = ?)`, hash)
}

func (d *DB) ListMerges(ctx context.Context, survivor domain.MemberID) ([]*MemberMerge, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT `+mergeCols+` FROM member_merges
		WHERE (? = '' OR survivor_id = ?) ORDER BY created_at DESC, id DESC`, survivor, survivor)
	if err != nil {
		return nil, fmt.Errorf("store: list merges: %w", err)
	}
	return collect(rows, scanMerge)
}
