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
// that hold no member id a merge must rewrite.
var notMemberRefs = map[string]string{
	"member_merges.absorbed_id":          "names the member the merge deleted",
	"member_merges.confirmed_by":         "describes the confirming connection",
	"evidence_packets.publication_owner": "an enum, not a member",
}

// TestMergeHandlesEveryMemberColumn fails when a column that can hold a
// member id is neither rewritten by a merge (memberRefs) nor listed as
// holding none, so no table keeps an absorbed member's id unnoticed.
func TestMergeHandlesEveryMemberColumn(t *testing.T) {
	db := openTestDB(t)
	handled := map[string]bool{}
	for _, ref := range memberRefs {
		handled[ref.table+"."+ref.column] = true
	}
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
			columns[*table+"."+*col] = true
			if *table != "members" && memberNamed(*col) && !handled[*table+"."+*col] && notMemberRefs[*table+"."+*col] == "" {
				t.Errorf("%s.%s looks like a member reference that a merge does not rewrite; add it to memberRefs or notMemberRefs", *table, *col)
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
			if parent == "members" && !handled[*table+"."+from] {
				t.Errorf("%s.%s references members(id) and a merge does not rewrite it; add it to memberRefs", *table, from)
			}
		}
		if err := fks.Close(); err != nil {
			t.Fatal(err)
		}
	}
	for ref := range handled {
		if !columns[ref] {
			t.Errorf("memberRefs lists %s, which the schema does not have", ref)
		}
	}
}

func memberNamed(col string) bool {
	switch {
	case strings.HasSuffix(col, "member_id"), strings.HasSuffix(col, "_human_id"), col == "actor_id", col == "actor_key",
		strings.HasSuffix(col, "_by"), strings.Contains(col, "owner"), col == "creator_id", col == "origin_id",
		strings.HasSuffix(col, "account_id"), col == "survivor_id", col == "absorbed_id":
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

func TestConfirmMergeRewritesEveryReference(t *testing.T) {
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
	plan, err := db.PlanMerge(ctx, "code", b.ID, now)
	if err != nil {
		t.Fatalf("PlanMerge: %v", err)
	}
	if _, err := db.ConfirmMerge(ctx, MergeConfirm{CodeHash: "code", Caller: b.ID, Keep: a.ID, Digest: "stale", Now: now}); !isRefusal(err, "preview again") {
		t.Fatalf("ConfirmMerge with a stale digest = %v", err)
	}
	merge, err := db.ConfirmMerge(ctx, MergeConfirm{CodeHash: "code", Caller: b.ID, Keep: a.ID, Digest: plan.Digest, Now: now,
		ConfirmedByKind: "ssh", ConfirmedBy: "test"})
	if err != nil {
		t.Fatalf("ConfirmMerge: %v", err)
	}

	got, err := db.GetMember(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.TailnetLogin != "ada@" || got.PublicKey != a.PublicKey || got.Role != domain.RoleCollaborator || got.DisplayName != "Ada" {
		t.Fatalf("survivor = %+v", got)
	}
	if _, err := db.GetMember(ctx, b.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("absorbed member still there: %v", err)
	}
	for _, ref := range memberRefs {
		var n int
		mustScan(t, db, &n, `SELECT COUNT(*) FROM `+ref.table+` WHERE instr(`+ref.column+`, ?) > 0`, string(b.ID))
		if n != 0 {
			t.Errorf("%s.%s still names the absorbed member in %d rows", ref.table, ref.column, n)
		}
	}
	if err := checkForeignKeysDB(db); err != nil {
		t.Fatal(err)
	}
	if m, err := db.GetMemberByIdentity(ctx, "github", "42"); err != nil || m.ID != a.ID {
		t.Fatalf("identity resolves to %v, %v", m, err)
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
	if r, err := db.GetRun(ctx, runB.ID); err != nil || r.MemberID != a.ID || r.ProfileSnapshotID != "ps-a" {
		t.Fatalf("absorbed member's run = %+v, %v", r, err)
	}
	var runs int
	mustScan(t, db, &runs, `SELECT runs FROM run_cost_deletions WHERE member_id = ?`, string(a.ID))
	if runs != 5 {
		t.Fatalf("deleted run costs summed to %d runs, want 5", runs)
	}
	mustScan(t, db, &runs, `SELECT COUNT(*) FROM room_messages WHERE actor_id = ?`, string(a.ID))
	if runs != 2 {
		t.Fatalf("room messages of the survivor = %d, want 2", runs)
	}

	d := merge.Discarded
	if d.DisplayName != "Ada on GitHub" || d.GitEmail != "ada@example.com" || d.Image != "aether-member-b:1" ||
		len(d.HarnessDefinitions) != 1 || d.HarnessDefinitions[0].Name != "mine" || len(d.ProfileHeads) != 2 ||
		!slices.Equal(d.AccountSharedWith, []domain.MemberID{a.ID, c.ID}) {
		t.Fatalf("discarded settings = %+v", d)
	}
	merges, err := db.ListMerges(ctx, a.ID)
	if err != nil || len(merges) != 1 || merges[0].Absorbed.Member.ID != b.ID || merges[0].Discarded.Color != "#3cb44b" {
		t.Fatalf("ListMerges = %+v, %v", merges, err)
	}

	replay, err := db.ConfirmMerge(ctx, MergeConfirm{CodeHash: "code", Caller: a.ID, Keep: a.ID, Digest: plan.Digest, Now: now})
	if err != nil || replay.ID != merge.ID {
		t.Fatalf("replayed confirm = %+v, %v", replay, err)
	}
	if _, err := db.ConfirmMerge(ctx, MergeConfirm{CodeHash: "code", Caller: c.ID, Keep: c.ID, Digest: plan.Digest, Now: now}); !isRefusal(err, "already used") {
		t.Fatalf("confirm of a used code by another member = %v", err)
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
	if _, err := db.PlanMerge(ctx, "nope", b.ID, now); !isRefusal(err, "unknown") {
		t.Fatalf("unknown code = %v", err)
	}
	if _, err := db.PlanMerge(ctx, "code", b.ID, now.Add(time.Hour)); !isRefusal(err, "expired") {
		t.Fatalf("expired code = %v", err)
	}
	var same *MergeRefusal
	if _, err := db.PlanMerge(ctx, "code", a.ID, now); !errors.As(err, &same) || !same.SameMember {
		t.Fatalf("own code = %v", err)
	}
	w := mustCreateWorkspace(t, db)
	run := mustCreateRun(t, db, w.ID, b.ID, domain.RunRunning)
	if _, err := db.PlanMerge(ctx, "code", b.ID, now); !isRefusal(err, string(run.ID)) {
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
