package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

func addPreference(t *testing.T, db *DB, member domain.MemberID, text string) Preference {
	t.Helper()
	p := Preference{MemberID: member, Text: text, Enabled: true, Source: "settings"}
	if err := db.AddPreference(context.Background(), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func noCommit(Preference, Preference) error { return nil }

// Every query is keyed by the member: another member's id reads, updates
// and deletes as not found.
func TestPreferencesAreKeyedByTheirMember(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	alice := mustCreateMember(t, db).ID
	bob := &domain.Member{DisplayName: "Bob", PublicKey: testKey(t, "bob"), Color: "#3cb44b", Role: domain.RoleAdmin}
	if err := db.CreateMember(ctx, bob); err != nil {
		t.Fatal(err)
	}
	p := addPreference(t, db, alice, "Prefer table output")

	if _, err := db.GetPreference(ctx, bob.ID, p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bob reads alice's preference: %v", err)
	}
	if _, _, err := db.UpdatePreference(ctx, bob.ID, p.ID, func(*Preference) error { return nil }, noCommit); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bob updates alice's preference: %v", err)
	}
	if _, err := db.DeletePreference(ctx, bob.ID, p.ID, func(Preference) error { return nil }); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bob deletes alice's preference: %v", err)
	}
	if err := db.TouchPreferences(ctx, bob.ID, []string{p.ID}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if list, err := db.ListPreferences(ctx, bob.ID); err != nil || len(list) != 0 {
		t.Fatalf("bob's list = %+v, %v", list, err)
	}
	got, err := db.GetPreference(ctx, alice, p.ID)
	if err != nil || got.Text != "Prefer table output" || !got.LastUsedAt.IsZero() {
		t.Fatalf("alice's preference = %+v, %v; want it untouched", got, err)
	}
}

func TestPreferenceCapRefusesWithoutEvicting(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	alice := mustCreateMember(t, db).ID
	first := addPreference(t, db, alice, "Prefer table output 0")
	for i := 1; i < MaxPreferences; i++ {
		addPreference(t, db, alice, fmt.Sprintf("Prefer table output %d", i))
	}
	p := Preference{MemberID: alice, Text: "One too many", Enabled: true, Source: "settings"}
	if err := db.AddPreference(context.Background(), &p); !errors.Is(err, ErrPreferenceCap) {
		t.Fatalf("add past the cap: %v", err)
	}
	if _, err := db.GetPreference(context.Background(), alice, first.ID); err != nil {
		t.Fatalf("the oldest preference after the cap: %v", err)
	}
}

// An edit moves updated_at forward even within one millisecond, and a
// failing commit hook leaves the row as it was.
func TestPreferenceUpdateOrdersAndAborts(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	alice := mustCreateMember(t, db).ID
	p := addPreference(t, db, alice, "Prefer table output")
	edit := func(row *Preference) error { row.Text = "Prefer short answers"; return nil }
	if _, _, err := db.UpdatePreference(ctx, alice, p.ID, edit, func(Preference, Preference) error { return errors.New("disk full") }); err == nil {
		t.Fatal("update with a failing commit hook succeeded")
	}
	if got, _ := db.GetPreference(ctx, alice, p.ID); got.Text != "Prefer table output" {
		t.Fatalf("after an aborted update: %q", got.Text)
	}
	old, cur, err := db.UpdatePreference(ctx, alice, p.ID, edit, noCommit)
	if err != nil || old.Text != "Prefer table output" || cur.Text != "Prefer short answers" || !cur.UpdatedAt.After(old.UpdatedAt) {
		t.Fatalf("update = %+v -> %+v, %v", old, cur, err)
	}
	if _, err = db.DeletePreference(ctx, alice, p.ID, func(Preference) error { return errors.New("disk full") }); err == nil {
		t.Fatal("delete with a failing commit hook succeeded")
	}
	if _, err = db.GetPreference(ctx, alice, p.ID); err != nil {
		t.Fatalf("after an aborted delete: %v", err)
	}
}

// After a delete or a text change and a checkpoint the old text is in
// neither the database file nor its write-ahead log.
func TestForgottenPreferenceTextLeavesTheFiles(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "aether.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	alice := mustCreateMember(t, db).ID
	deleted := addPreference(t, db, alice, "Prefer canary-zebra-forgotten output")
	edited := addPreference(t, db, alice, "Prefer canary-lynx-replaced output")
	if _, err = db.DeletePreference(ctx, alice, deleted.ID, func(Preference) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, _, err = db.UpdatePreference(ctx, alice, edited.ID, func(p *Preference) error { p.Text = "Prefer table output"; return nil }, noCommit); err != nil {
		t.Fatal(err)
	}
	if err = db.CheckpointWAL(ctx); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{path, path + "-wal"} {
		b, err := os.ReadFile(file)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		for _, canary := range []string{"canary-zebra-forgotten", "canary-lynx-replaced"} {
			if bytes.Contains(b, []byte(canary)) {
				t.Errorf("%s still holds %q", filepath.Base(file), canary)
			}
		}
	}
}

func TestPreferenceTombstonesDeleteRestoredRows(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	alice := mustCreateMember(t, db).ID
	gone := addPreference(t, db, alice, "Prefer table output")
	stale := addPreference(t, db, alice, "Prefer short answers")
	current := addPreference(t, db, alice, "Prefer dark mode")
	n, err := db.ApplyPreferenceTombstones(ctx, []string{gone.ID, "pref-unknown"}, map[string]time.Time{
		stale.ID:   stale.UpdatedAt,
		current.ID: current.UpdatedAt.Add(-time.Millisecond),
	})
	if err != nil || n != 2 {
		t.Fatalf("apply = %d, %v; want 2 deleted", n, err)
	}
	list, _ := db.ListPreferences(ctx, alice)
	if len(list) != 1 || list[0].ID != current.ID {
		t.Fatalf("after the tombstones: %+v; want only the row newer than the one its edit replaced", list)
	}
}

func TestRemovingAMemberDeletesTheirPreferences(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	alice := mustCreateMember(t, db).ID
	p := addPreference(t, db, alice, "Prefer table output")
	if err := db.DeleteMember(ctx, alice); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM member_preferences WHERE id = ?`, p.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rows after removing the member = %d, %v", count, err)
	}
}

// A removal the member's runs refuse records no preference as deleted.
func TestRefusedRemovalKeepsThePreferences(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	alice := mustCreateMember(t, db)
	p := addPreference(t, db, alice.ID, "Prefer table output")
	run := &domain.Run{WorkspaceID: mustCreateWorkspace(t, db).ID, MemberID: mustCreateMember(t, db).ID, AccountMemberID: alice.ID,
		Mode: domain.LaunchTUI, Status: domain.RunQueued}
	if err := db.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	var recorded []string
	err := db.DeleteMemberWithPreferences(ctx, alice.ID, func(ids []string) error {
		recorded = ids
		return nil
	})
	if !errors.Is(err, ErrInUse) || recorded != nil {
		t.Fatalf("removal = %v, recorded %v; want ErrInUse and nothing recorded", err, recorded)
	}
	if _, err = db.GetPreference(ctx, alice.ID, p.ID); err != nil {
		t.Fatalf("preference after the refused removal: %v", err)
	}
}

func TestMergeMovesPreferencesToTheSurvivor(t *testing.T) {
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
	p := addPreference(t, db, b.ID, "Prefer table output")
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
	if _, err = db.GetPreference(ctx, a.ID, p.ID); err != nil {
		t.Fatalf("the survivor's preference after the merge: %v", err)
	}
	// A save that checked the member before the merge commits after it.
	late := Preference{MemberID: b.ID, Text: "Prefer short answers", Enabled: true, Source: "settings"}
	if err = db.AddPreference(ctx, &late); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a save for the absorbed member = %v, want ErrNotFound", err)
	}
}

func TestTemporaryConversationEndsWithAReset(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	alice := mustCreateMember(t, db).ID
	if err := db.SetAssistantTemporary(ctx, alice, true); err != nil {
		t.Fatal(err)
	}
	if row, err := db.GetAssistantSession(ctx, alice); err != nil || !row.Temporary {
		t.Fatalf("after setting temporary: %+v, %v", row, err)
	}
	if _, err := db.ResetAssistantSession(ctx, alice); err != nil {
		t.Fatal(err)
	}
	if row, err := db.GetAssistantSession(ctx, alice); err != nil || row.Temporary {
		t.Fatalf("after a reset: %+v, %v; want temporary cleared", row, err)
	}
}

func TestRemovingAMemberRecordsItsConnectorsAndTombstonesPreferences(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	alice := mustCreateMember(t, db)
	p := addPreference(t, db, alice.ID, "Prefer table output")
	now := time.Now().UTC()
	conn := &Connector{Member: alice.ID, ClientID: "client", ClientName: "Claude", RedirectHost: "claude.ai", Resource: "https://aether.test/mcp", CreatedAt: now}
	if err := db.CreateConnector(ctx, conn, ConnectorTokens{Access: "a", AccessExpiresAt: now.Add(time.Hour), Refresh: "r", RefreshExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	var recorded []string
	if err := db.DeleteMemberWithPreferences(ctx, alice.ID, func(ids []string) error {
		recorded = ids
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(recorded) != 1 || recorded[0] != p.ID {
		t.Fatalf("tombstoned %v, want %s", recorded, p.ID)
	}
	entries, err := db.ListAudit(ctx, AuditQuery{Member: alice.ID, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Method != AuditConnectorRemoved {
		t.Fatalf("audit after the removal = %+v; want one connector.removed entry", entries)
	}
}
