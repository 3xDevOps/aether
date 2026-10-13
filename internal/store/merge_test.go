package store

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// notMemberRefs are the columns a member-reference name pattern matches
// that hold no member id.
var notMemberRefs = map[string]string{
	"member_merges.confirmed_by":         "describes the accepting connection",
	"evidence_packets.publication_owner": "an enum, not a member",
}

// TestMergeClassifiesEveryMemberColumn fails when a column that can hold a
// member id is neither moved by a merge nor kept as history (memberRefs),
// nor listed as holding none, so no new table keeps a merged member's id
// unreviewed.
func TestMergeClassifiesEveryMemberColumn(t *testing.T) {
	db := openTestDB(t)
	tables, err := db.db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	names, err := collect(tables, func(row interface{ Scan(...any) error }) (*string, error) {
		var s string
		return &s, row.Scan(&s)
	})
	if err != nil {
		t.Fatal(err)
	}
	columns := map[string]bool{}
	for _, table := range names {
		cols, err := db.db.Query(`SELECT name FROM pragma_table_info(?)`, *table)
		if err != nil {
			t.Fatal(err)
		}
		colNames, err := collect(cols, func(row interface{ Scan(...any) error }) (*string, error) {
			var s string
			return &s, row.Scan(&s)
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, col := range colNames {
			ref := *table + "." + *col
			columns[ref] = true
			if _, ok := memberRefs[ref]; !ok && memberNamed(*col) && notMemberRefs[ref] == "" && ref != "members.id" {
				t.Errorf("%s looks like a member reference a merge does not classify; add it to memberRefs or notMemberRefs", ref)
			}
		}
		fks, err := db.db.Query(`SELECT "table", "from" FROM pragma_foreign_key_list(?)`, *table)
		if err != nil {
			t.Fatal(err)
		}
		for fks.Next() {
			var parent, from string
			if err := fks.Scan(&parent, &from); err != nil {
				t.Fatal(err)
			}
			if _, ok := memberRefs[*table+"."+from]; parent == "members" && !ok {
				t.Errorf("%s.%s references members(id) and a merge does not classify it; add it to memberRefs", *table, from)
			}
		}
		if err := fks.Close(); err != nil {
			t.Fatal(err)
		}
	}
	for ref := range memberRefs {
		if !columns[ref] {
			t.Errorf("memberRefs lists %s, which the schema does not have", ref)
		}
	}
}

func memberNamed(col string) bool {
	switch {
	case strings.HasSuffix(col, "member_id"), strings.HasSuffix(col, "_human_id"), col == "actor_id", col == "actor_key",
		strings.HasSuffix(col, "_by"), strings.Contains(col, "owner"), col == "creator_id", col == "origin_id",
		strings.HasSuffix(col, "account_id"), col == "survivor_id", col == "absorbed_id", col == "merged_into",
		col == "proposed_by", col == "keep_settings_of":
		return true
	}
	return false
}

func TestMergeMigrationUpgradesV62(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aether.db")
	raw := openLegacy(t, path, 62)
	if _, err := raw.Exec(`
		INSERT INTO members (id, display_name, tailnet_login, color, role, created_at) VALUES ('m1', 'Ada', 'ada@', '#e6194b', 'admin', 1);
		INSERT INTO workspaces (id, name, created_at) VALUES ('w1', 'proj', 1);
		INSERT INTO runs (id, workspace_id, member_id, account_member_id, task, harness, mode, status, branch, worktree, created_at)
			VALUES ('r1', 'w1', 'm1', 'm1', 'task', 'claude', 'tui', 'completed', '', '', 1);
		INSERT INTO member_identities (provider, subject, member_id, created_at) VALUES ('github', '42', 'm1', 1);
	`); err != nil {
		t.Fatalf("seed v62: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	if r, err := db.GetRun(ctx, "r1"); err != nil || r.MemberID != "m1" {
		t.Fatalf("run after upgrade = %+v, %v", r, err)
	}
	now := time.Now()
	if err := db.CreateMergeCode(ctx, "m1", "hash", now, now.Add(time.Minute)); err != nil {
		t.Fatalf("CreateMergeCode after upgrade: %v", err)
	}
	if merges, err := db.ListMerges(ctx, ""); err != nil || len(merges) != 0 {
		t.Fatalf("ListMerges after upgrade = %v, %v", merges, err)
	}
}

func TestAcceptMergeMovesOngoingWorkAndKeepsHistory(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	a := mustCreateMember(t, db)
	a.Role = domain.RoleAdmin
	if err := db.UpdateMember(ctx, a); err != nil {
		t.Fatal(err)
	}
	b := &domain.Member{DisplayName: "Ada on GitHub", TailnetLogin: "ada@", Color: "#3cb44b", Role: domain.RoleCollaborator,
		GitName: "Ada L", GitEmail: "ada@example.com", Image: "aether-member-b:1"}
	c := &domain.Member{DisplayName: "Cy", TailnetLogin: "cy@", Color: "#ffe119", Role: domain.RoleAdmin}
	for _, m := range []*domain.Member{b, c} {
		if err := db.CreateMember(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	w := mustCreateWorkspace(t, db)
	runB := mustCreateRun(t, db, w.ID, b.ID, domain.RunCompleted)
	runC := mustCreateRun(t, db, w.ID, c.ID, domain.RunCompleted)
	if err := db.BindIdentity(ctx, &domain.Identity{Member: b.ID, Provider: "github", Subject: "42", Login: "ada"}); err != nil {
		t.Fatal(err)
	}
	dev := &domain.Device{Member: b.ID, Provider: "github", Subject: "42", Credential: testKey(t, ""), Label: "laptop", Status: domain.DeviceApproved}
	if err := db.RegisterDevice(ctx, dev); err != nil {
		t.Fatal(err)
	}
	for _, s := range [][2]domain.MemberID{{a.ID, b.ID}, {b.ID, a.ID}, {b.ID, c.ID}, {c.ID, b.ID}, {c.ID, a.ID}} {
		if err := db.ShareAccount(ctx, s[0], s[1]); err != nil {
			t.Fatal(err)
		}
	}
	for _, m := range []domain.MemberID{a.ID, b.ID} {
		if _, err := db.AddRunSteerer(ctx, runC.ID, m); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.PutPushSubscription(ctx, &PushSubscription{Endpoint: "https://push.example/b", MemberID: b.ID, P256DH: "k", Auth: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertHarnessDefinition(ctx, &HarnessDefinition{MemberID: b.ID, Name: "mine", Definition: []byte(`{"Name":"mine"}`)}); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `
		UPDATE runs SET account_member_id = ?2, home_member_id = ?2, requested_account_member_id = ?2, fallback_order = '["' || ?2 || '"]' WHERE id = ?1;
		INSERT INTO profile_snapshots (id, member_id, harness, digest, created_at) VALUES ('ps-a', ?3, 'claude', 'd1', 1), ('ps-b', ?2, 'claude', 'd1', 1), ('ps-b2', ?2, 'codex', 'd2', 1);
		INSERT INTO profile_heads (member_id, harness, snapshot_id) VALUES (?2, 'claude', 'ps-b'), (?2, 'codex', 'ps-b2');
		UPDATE runs SET profile_snapshot_id = 'ps-b' WHERE id = ?4;
		INSERT INTO run_costs (run_id, workspace_id, member_id, recorded_at) VALUES (?4, ?5, ?2, 1);
		INSERT INTO run_cost_deletions (workspace_id, member_id, runs) VALUES (?5, ?2, 2), (?5, ?3, 3);
		INSERT INTO room_messages (id, workspace_id, run_id, actor_id, actor_display_name, kind, body, idempotency_key, state, created_at, updated_at)
			VALUES ('rm-a', ?5, ?1, ?3, 'Ada', 'comment', 'x', 'k1', 'sent', 1, 1), ('rm-b', ?5, ?1, ?2, 'Ada', 'comment', 'y', 'k1', 'sent', 1, 1);
		INSERT INTO events (id, ts, workspace_id, run_id, actor_id, type, payload) VALUES ('ev1', 1, ?5, ?4, ?2, 'run.status', '{}');
	`, runC.ID, b.ID, a.ID, runB.ID, w.ID)

	if err := db.CreateMergeCode(ctx, a.ID, "code", now, now.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	plan, err := db.PlanMerge(ctx, "code", b.ID, "account", now)
	if err != nil {
		t.Fatalf("PlanMerge: %v", err)
	}
	if _, err := db.ProposeMerge(ctx, MergeProposal{CodeHash: "code", Proposer: b.ID, Keep: a.ID, Digest: "stale", Policy: "account",
		Now: now, Expires: now.Add(time.Minute)}); !isRefusal(err, "preview again") {
		t.Fatalf("ProposeMerge with a stale digest = %v", err)
	}
	pm, err := db.ProposeMerge(ctx, MergeProposal{CodeHash: "code", Proposer: b.ID, Keep: a.ID, Digest: plan.Digest, Policy: "account",
		Now: now, Expires: now.Add(time.Minute)})
	if err != nil {
		t.Fatalf("ProposeMerge: %v", err)
	}
	if _, err := db.PlanMerge(ctx, "code", c.ID, "account", now); !isRefusal(err, "already used") {
		t.Fatalf("a proposed code planned again = %v", err)
	}
	accept := MergeAccept{ProposalID: pm.ProposalID, Caller: b.ID, Digest: plan.Digest, Policy: "account", Now: now,
		ConfirmedByKind: "ssh", ConfirmedBy: "test"}
	if _, err := db.AcceptMerge(ctx, accept); !isRefusal(err, "only member") {
		t.Fatalf("AcceptMerge by the proposer = %v", err)
	}
	accept.Caller = a.ID
	merge, err := db.AcceptMerge(ctx, accept)
	if err != nil {
		t.Fatalf("AcceptMerge: %v", err)
	}

	got, err := db.GetMember(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.TailnetLogin != "ada@" || got.PublicKey != a.PublicKey || got.Role != domain.RoleCollaborator || got.DisplayName != "Ada" {
		t.Fatalf("survivor = %+v", got)
	}
	if m, err := db.GetMember(ctx, b.ID); err != nil || m.ID != a.ID {
		t.Fatalf("the absorbed member resolves to %+v, %v; want the survivor", m, err)
	}
	if members, err := db.ListMembers(ctx); err != nil || len(members) != 2 {
		t.Fatalf("ListMembers = %d members, %v; want the survivor and Cy", len(members), err)
	}
	for ref, moved := range memberRefs {
		table, column, _ := strings.Cut(ref, ".")
		var n int
		mustScan(t, db, &n, `SELECT COUNT(*) FROM `+table+` WHERE `+column+` = ?`, string(b.ID))
		if moved && n != 0 {
			t.Errorf("%s still names the absorbed member in %d rows", ref, n)
		}
	}
	var n int
	mustScan(t, db, &n, `SELECT COUNT(*) FROM events WHERE actor_id = ?`, string(b.ID))
	if n != 1 {
		t.Fatalf("history rewritten: events by the absorbed member = %d, want 1", n)
	}
	if err := checkForeignKeysDB(db); err != nil {
		t.Fatal(err)
	}
	if m, err := db.GetMemberByIdentity(ctx, "github", "42"); err != nil || m.ID != a.ID {
		t.Fatalf("identity resolves to %v, %v", m, err)
	}
	if m, err := db.GetMemberByTailnetLogin(ctx, "ada@"); err != nil || m.ID != a.ID {
		t.Fatalf("tailnet login resolves to %v, %v", m, err)
	}
	if d, err := db.GetDevice(ctx, dev.ID); err != nil || d.Member != a.ID || d.Status != domain.DeviceApproved {
		t.Fatalf("device = %+v, %v", d, err)
	}
	if steerers, err := db.ListRunSteerers(ctx, runC.ID); err != nil || len(steerers) != 1 || steerers[0].ID != a.ID {
		t.Fatalf("steerers = %v, %v", steerers, err)
	}
	if owners, err := db.ListAccountOwners(ctx, a.ID); err != nil || len(owners) != 1 || owners[0].ID != c.ID {
		t.Fatalf("accounts shared with the survivor = %v, %v", owners, err)
	}
	if grantees, err := db.ListAccountGrantees(ctx, a.ID); err != nil || len(grantees) != 0 {
		t.Fatalf("survivor shares with %v, %v", grantees, err)
	}
	if r, err := db.GetRun(ctx, runB.ID); err != nil || r.MemberID != a.ID {
		t.Fatalf("absorbed member's run = %+v, %v", r, err)
	}

	d := merge.Discarded
	if d.DisplayName != "Ada on GitHub" || d.GitEmail != "ada@example.com" || d.Image != "aether-member-b:1" ||
		len(d.HarnessDefinitions) != 1 || d.HarnessDefinitions[0].Name != "mine" || len(d.ProfileHeads) != 2 ||
		len(d.AccountSharedWith) != 2 || !slices.Contains(d.AccountSharedWith, a.ID) || !slices.Contains(d.AccountSharedWith, c.ID) {
		t.Fatalf("discarded settings = %+v", d)
	}
	merges, err := db.ListMerges(ctx, a.ID)
	if err != nil || len(merges) != 1 || merges[0].Absorbed.Member.ID != b.ID || merges[0].Discarded.Color != "#3cb44b" {
		t.Fatalf("ListMerges = %+v, %v", merges, err)
	}
	replay, err := db.AcceptMerge(ctx, accept)
	if err != nil || replay.ID != merge.ID {
		t.Fatalf("replayed accept = %+v, %v", replay, err)
	}
	accept.Caller = c.ID
	if _, err := db.AcceptMerge(ctx, accept); !isRefusal(err, "accepted already") {
		t.Fatalf("accept of a used proposal by another member = %v", err)
	}
}

func TestPlanMergeRefusals(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	a := mustCreateMember(t, db)
	b := &domain.Member{DisplayName: "B", TailnetLogin: "b@", Color: "#3cb44b", Role: domain.RoleCollaborator}
	if err := db.CreateMember(ctx, b); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateMergeCode(ctx, a.ID, "code", now, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PlanMerge(ctx, "nope", b.ID, "", now); !isRefusal(err, "unknown") {
		t.Fatalf("unknown code = %v", err)
	}
	if _, err := db.PlanMerge(ctx, "code", b.ID, "", now.Add(time.Hour)); !isRefusal(err, "expired") {
		t.Fatalf("expired code = %v", err)
	}
	if _, err := db.PlanMerge(ctx, "code", a.ID, "", now); !isRefusal(err, "already the same member") {
		t.Fatalf("own code = %v", err)
	}
	w := mustCreateWorkspace(t, db)
	run := mustCreateRun(t, db, w.ID, b.ID, domain.RunRunning)
	if _, err := db.PlanMerge(ctx, "code", b.ID, "", now); !isRefusal(err, string(run.ID)) {
		t.Fatalf("live run = %v", err)
	}
}

func isRefusal(err error, contains string) bool {
	var r *MergeRefusal
	return errors.As(err, &r) && strings.Contains(r.Message, contains)
}

func mustExec(t *testing.T, db *DB, query string, args ...any) {
	t.Helper()
	if _, err := db.db.Exec(query, args...); err != nil {
		t.Fatalf("exec: %v", err)
	}
}

func mustScan(t *testing.T, db *DB, dest any, query string, args ...any) {
	t.Helper()
	if err := db.db.QueryRow(query, args...).Scan(dest); err != nil {
		t.Fatalf("scan: %v", err)
	}
}

func checkForeignKeysDB(db *DB) error {
	tx, err := db.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	return checkForeignKeys(tx)
}
