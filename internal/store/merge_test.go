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
	"memory.superseded_by":               "a note id, not a member",
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
		if _, err := db.ShareAccount(ctx, s[0], s[1]); err != nil {
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
	if err := db.PutNotifyPrefs(ctx, b.ID, NotifyPrefs{Push: false}); err != nil {
		t.Fatal(err)
	}
	launchRevisions := map[domain.MemberID]int64{}
	for m, l := range map[domain.MemberID]LaunchDefaults{
		a.ID: {Agent: "claude", Account: b.ID, FallbackPolicy: "ordered", FallbackOrder: []domain.MemberID{b.ID, c.ID, a.ID}, Source: "member"},
		b.ID: {Agent: "codex", Source: "member"},
		c.ID: {Account: b.ID, Source: "assistant"},
	} {
		if err := db.PutLaunchDefaults(ctx, m, l, 0); err != nil {
			t.Fatal(err)
		}
		saved, err := db.LaunchDefaults(ctx, m)
		if err != nil {
			t.Fatal(err)
		}
		launchRevisions[m] = saved.Revision
	}
	watchB := &Watch{MemberID: b.ID, WorkspaceID: w.ID, TargetKind: "run", RunID: runB.ID, Conditions: []string{"finished"},
		State: "active", Source: "dashboard", CreatedAt: time.Now(), UpdatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}
	if err := db.CreateWatch(ctx, watchB); err != nil {
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
	if _, err = db.ProposeMerge(ctx, MergeProposal{CodeHash: "code", Proposer: b.ID, Keep: a.ID, Digest: "stale", Policy: "account",
		Now: now, Expires: now.Add(time.Minute)}); !isRefusal(err, "preview again") {
		t.Fatalf("ProposeMerge with a stale digest = %v", err)
	}
	pm, err := db.ProposeMerge(ctx, MergeProposal{CodeHash: "code", Proposer: b.ID, Keep: a.ID, Digest: plan.Digest, Policy: "account",
		Now: now, Expires: now.Add(time.Minute)})
	if err != nil {
		t.Fatalf("ProposeMerge: %v", err)
	}
	if _, err = db.PlanMerge(ctx, "code", c.ID, "account", now); !isRefusal(err, "already used") {
		t.Fatalf("a proposed code planned again = %v", err)
	}
	accept := MergeAccept{ProposalID: pm.ProposalID, Caller: b.ID, Digest: plan.Digest, Policy: "account", Now: now,
		ConfirmedByKind: "ssh", ConfirmedBy: "test"}
	if _, err = db.AcceptMerge(ctx, accept); !isRefusal(err, "only member") {
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
	if m, gerr := db.GetMember(ctx, b.ID); !errors.Is(gerr, ErrNotFound) || !strings.Contains(gerr.Error(), "merged into "+string(a.ID)) {
		t.Fatalf("the absorbed member = %+v, %v; want it refused as merged into the survivor", m, gerr)
	}
	if members, gerr := db.ListMembers(ctx); gerr != nil || len(members) != 2 {
		t.Fatalf("ListMembers = %d members, %v; want the survivor and Cy", len(members), gerr)
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
	if err = checkForeignKeysDB(db); err != nil {
		t.Fatal(err)
	}
	if m, gerr := db.GetMemberByIdentity(ctx, "github", "42"); gerr != nil || m.ID != a.ID {
		t.Fatalf("identity resolves to %v, %v", m, gerr)
	}
	if m, gerr := db.GetMemberByTailnetLogin(ctx, "ada@"); gerr != nil || m.ID != a.ID {
		t.Fatalf("tailnet login resolves to %v, %v", m, gerr)
	}
	if d, gerr := db.GetDevice(ctx, dev.ID); gerr != nil || d.Member != a.ID || d.Status != domain.DeviceApproved {
		t.Fatalf("device = %+v, %v", d, gerr)
	}
	if steerers, gerr := db.ListRunSteerers(ctx, runC.ID); gerr != nil || len(steerers) != 1 || steerers[0].ID != a.ID {
		t.Fatalf("steerers = %v, %v", steerers, gerr)
	}
	if owners, gerr := db.ListAccountOwners(ctx, a.ID); gerr != nil || len(owners) != 1 || owners[0].ID != c.ID {
		t.Fatalf("accounts shared with the survivor = %v, %v", owners, gerr)
	}
	if grantees, gerr := db.ListAccountGrantees(ctx, a.ID); gerr != nil || len(grantees) != 0 {
		t.Fatalf("survivor shares with %v, %v", grantees, gerr)
	}
	if p, gerr := db.GetNotifyPrefs(ctx, a.ID); gerr != nil || p.Push {
		t.Fatalf("survivor's notification settings = %+v, %v; want the absorbed member's, push off", p, gerr)
	}
	if l, gerr := db.LaunchDefaults(ctx, a.ID); gerr != nil || l.Agent != "claude" || l.Account != a.ID ||
		!slices.Equal(l.FallbackOrder, []domain.MemberID{a.ID, c.ID}) || l.Revision == launchRevisions[a.ID] {
		t.Fatalf("survivor's launch defaults = %+v, %v; want its own, naming the survivor for the absorbed member, at a new revision", l, gerr)
	}
	if l, gerr := db.LaunchDefaults(ctx, c.ID); gerr != nil || l.Account != a.ID || l.Source != "assistant" || l.Revision == launchRevisions[c.ID] {
		t.Fatalf("Cy's launch defaults = %+v, %v; want the account moved to the survivor at a new revision", l, gerr)
	}
	if perr := db.PutLaunchDefaults(ctx, c.ID, LaunchDefaults{Source: "member"}, launchRevisions[c.ID]); !errors.Is(perr, ErrConflict) {
		t.Fatalf("PutLaunchDefaults with the revision read before the merge = %v, want ErrConflict", perr)
	}
	if got, gerr := db.GetWatch(ctx, watchB.ID); gerr != nil || got.MemberID != a.ID || got.Version == watchB.Version {
		t.Fatalf("absorbed member's watch = %+v, %v; want it moved with a new version", got, gerr)
	}
	if r, gerr := db.GetRun(ctx, runB.ID); gerr != nil || r.MemberID != a.ID {
		t.Fatalf("absorbed member's run = %+v, %v", r, gerr)
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

// A merge that fails after the absorbed member's home was moved and the
// demoted admin's codes revoked puts both back and changes no row.
func TestFailedMergeRestoresTheArchivedHome(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	a := mustCreateMember(t, db)
	a.Role = domain.RoleAdmin
	if err := db.UpdateMember(ctx, a); err != nil {
		t.Fatal(err)
	}
	b := &domain.Member{DisplayName: "B", TailnetLogin: "b@", Color: "#3cb44b", Role: domain.RoleCollaborator}
	admin := &domain.Member{DisplayName: "Root", TailnetLogin: "root@", Color: "#ffe119", Role: domain.RoleAdmin}
	for _, m := range []*domain.Member{b, admin} {
		if err := db.CreateMember(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.CreateMergeCode(ctx, a.ID, "code", now, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	plan, err := db.PlanMerge(ctx, "code", b.ID, "", now)
	if err != nil {
		t.Fatal(err)
	}
	pm, err := db.ProposeMerge(ctx, MergeProposal{CodeHash: "code", Proposer: b.ID, Keep: a.ID, Digest: plan.Digest, Now: now, Expires: now.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `CREATE TRIGGER fail_merge BEFORE INSERT ON member_merges BEGIN SELECT RAISE(ABORT, 'injected'); END`)
	var moved, restored, revoked, reinstated bool
	_, err = db.AcceptMerge(ctx, MergeAccept{ProposalID: pm.ProposalID, Caller: a.ID, Digest: plan.Digest, Now: now,
		Archive: func(string, domain.MemberID) (string, func() error, error) {
			moved = true
			return "/merged/x", func() error { restored = true; return nil }, nil
		},
		Demoted: func(domain.MemberID, domain.MemberID) (func() error, error) {
			revoked = true
			return func() error { reinstated = true; return nil }, nil
		}})
	if err == nil || !moved || !restored || !revoked || !reinstated {
		t.Fatalf("AcceptMerge = %v; home moved %v, restored %v; codes revoked %v, reinstated %v; want a failure that undoes both",
			err, moved, restored, revoked, reinstated)
	}
	if m, err := db.GetMember(ctx, b.ID); err != nil || m.ID != b.ID || m.TailnetLogin != "b@" {
		t.Fatalf("absorbed member after the failed merge = %+v, %v", m, err)
	}
}

// When the merged member stays an admin, the open invitations the absorbed
// admin created become the survivor's, so demoting the survivor revokes
// them.
func TestMergeHandsOpenInvitationsToTheSurvivor(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	a := &domain.Member{DisplayName: "A", TailnetLogin: "a@", Color: "#3cb44b", Role: domain.RoleAdmin}
	b := &domain.Member{DisplayName: "B", PublicKey: testKey(t, "b"), Color: "#ffe119", Role: domain.RoleAdmin}
	for _, m := range []*domain.Member{a, b} {
		if err := db.CreateMember(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	inv := &domain.Invitation{Provider: "github", Email: "x@example.com", Role: domain.RoleAdmin, CreatedBy: b.ID, ExpiresAt: now.Add(time.Hour)}
	if err := db.CreateInvitation(ctx, inv); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateMergeCode(ctx, a.ID, "code", now, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	plan, err := db.PlanMerge(ctx, "code", b.ID, "", now)
	if err != nil {
		t.Fatal(err)
	}
	pm, err := db.ProposeMerge(ctx, MergeProposal{CodeHash: "code", Proposer: b.ID, Keep: a.ID, Digest: plan.Digest, Now: now, Expires: now.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.AcceptMerge(ctx, MergeAccept{ProposalID: pm.ProposalID, Caller: a.ID, Digest: plan.Digest, Now: now}); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetInvitation(ctx, inv.ID)
	if err != nil || got.CreatedBy != a.ID {
		t.Fatalf("invitation after the merge = %+v, %v; want it created by the survivor", got, err)
	}
	if err = db.DeleteInvitationsBy(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.GetInvitation(ctx, inv.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("invitation after demoting the survivor: %v; want it revoked", err)
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
	if _, err := db.UpdateRunStatus(ctx, run.ID, domain.RunCompleted, "", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `INSERT INTO handoff_outbox (id, workspace_id, run_id, actor_id, from_member_id, to_member_id, evidence_state,
		publication_state, created_at, updated_at) VALUES ('ho-1', ?, ?, ?, ?, ?, 'available', 'pending', 1, 1)`,
		w.ID, run.ID, b.ID, b.ID, a.ID)
	if _, err := db.PlanMerge(ctx, "code", b.ID, "", now); !isRefusal(err, "handoffs ho-1") {
		t.Fatalf("pending handoff = %v", err)
	}
	mustExec(t, db, `UPDATE handoff_outbox SET publication_state = 'published', timeline_state = 'published', coauthor_state = 'published'`)
	mustExec(t, db, `INSERT INTO room_messages (id, workspace_id, run_id, actor_id, actor_display_name, kind, body, idempotency_key, state, created_at, updated_at)
		VALUES ('rm-steer', ?, ?, ?, 'B', 'steer_request', 'go', 'k', 'queued', 1, 1)`, w.ID, run.ID, b.ID)
	if _, err := db.PlanMerge(ctx, "code", b.ID, "", now); !isRefusal(err, "queued steer requests rm-steer") {
		t.Fatalf("queued steer request = %v", err)
	}
	mustExec(t, db, `UPDATE room_messages SET state = 'sent'`)
	mustExec(t, db, `INSERT INTO integration_candidates (id, workspace_id, actor_key, idempotency_key, digest, state, version, payload, created_at, expires_at)
		VALUES ('cand-1', ?, 'member:x', 'k', 'd', 'frozen', 1, json_object('delivery_request', json_object('state', 'approved', 'decided_by', ?)), 1, 9999999999999)`,
		w.ID, b.ID)
	if _, err := db.PlanMerge(ctx, "code", b.ID, "", now); !isRefusal(err, "approved deliveries of candidates cand-1") {
		t.Fatalf("approved delivery = %v", err)
	}
	mustExec(t, db, `UPDATE integration_candidates SET state = 'expired'`)
	if _, err := db.PlanMerge(ctx, "code", b.ID, "", now); err != nil {
		t.Fatalf("approved delivery of a deleted candidate = %v, want no refusal", err)
	}
	mustExec(t, db, `DELETE FROM integration_candidates`)
	plan, err := db.PlanMerge(ctx, "code", b.ID, "", now)
	if err != nil {
		t.Fatal(err)
	}
	proposal := MergeProposal{CodeHash: "code", Proposer: b.ID, Keep: a.ID, Digest: plan.Digest, Now: now, Expires: now.Add(time.Minute)}
	first, err := db.ProposeMerge(ctx, proposal)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := db.ProposeMerge(ctx, proposal); err != nil || again.ProposalID != first.ProposalID {
		t.Fatalf("the same proposal made again = %+v, %v; want %s", again, err, first.ProposalID)
	}
	proposal.Keep = b.ID
	if _, err := db.ProposeMerge(ctx, proposal); !isRefusal(err, "already used") {
		t.Fatalf("another proposal on a used code = %v", err)
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
