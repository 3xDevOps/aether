package store

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

func TestProfileSnapshotCRUD(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	m := mustCreateMember(t, db)

	files := []ProfileFile{
		{Path: "a.txt", Mode: 0o644, Content: []byte("hello")},
		{Path: "b/c.txt", Mode: 0o600, Content: []byte("secret-not")},
	}
	snap := &domain.ProfileSnapshot{
		MemberID: m.ID,
		Harness:  "claude",
		Digest:   "abc123",
	}
	if err := db.SaveProfileSnapshot(ctx, snap, files); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if snap.ID == "" {
		t.Fatal("no id assigned")
	}

	got, err := db.GetProfileSnapshot(ctx, snap.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Digest != "abc123" || got.MemberID != m.ID {
		t.Fatalf("got %+v", got)
	}

	again := &domain.ProfileSnapshot{MemberID: m.ID, Harness: "claude", Digest: "abc123"}
	if err = db.SaveProfileSnapshot(ctx, again, files); err != nil {
		t.Fatalf("Save dedup: %v", err)
	}
	if again.ID != snap.ID {
		t.Fatalf("dedup id %s vs %s", again.ID, snap.ID)
	}

	stored, err := db.GetProfileFiles(ctx, snap.ID)
	if err != nil {
		t.Fatalf("GetFiles: %v", err)
	}
	if len(stored) != 2 {
		t.Fatalf("files = %d", len(stored))
	}
	if !bytes.Equal(stored[0].Content, []byte("hello")) {
		t.Fatalf("content = %q", stored[0].Content)
	}

	head, err := db.GetProfileHead(ctx, m.ID, "claude")
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if head.ID != snap.ID {
		t.Fatalf("head %s want %s", head.ID, snap.ID)
	}
}

func TestProfileRetentionAndRollback(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	m := mustCreateMember(t, db)
	var ids []domain.ProfileSnapshotID
	for i := 0; i < 12; i++ {
		snap := &domain.ProfileSnapshot{
			MemberID: m.ID,
			Harness:  "claude",
			Digest:   string(rune('a'+i)) + "-digest",
		}
		files := []ProfileFile{{Path: "n.txt", Mode: 0o644, Content: []byte{byte(i)}}}
		if err := db.SaveProfileSnapshot(ctx, snap, files); err != nil {
			t.Fatalf("Save %d: %v", i, err)
		}
		ids = append(ids, snap.ID)
		if err := db.PruneProfileSnapshots(ctx, m.ID, "claude", 10); err != nil {
			t.Fatalf("Prune %d: %v", i, err)
		}
	}
	list, err := db.ListProfileSnapshots(ctx, m.ID, "claude")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 10 {
		t.Fatalf("len = %d, want 10", len(list))
	}
	if _, err = db.GetProfileSnapshot(ctx, ids[0]); !errors.Is(err, ErrNotFound) {
		t.Fatalf("oldest still there: %v", err)
	}
	keep := ids[2]
	if err = db.SetProfileHead(ctx, m.ID, "claude", keep); err != nil {
		t.Fatalf("SetHead: %v", err)
	}
	head, err := db.GetProfileHead(ctx, m.ID, "claude")
	if err != nil {
		t.Fatalf("GetHead: %v", err)
	}
	if head.ID != keep {
		t.Fatalf("head %s want %s", head.ID, keep)
	}
	if _, err := db.GetProfileSnapshot(ctx, ids[11]); err != nil {
		t.Fatalf("latest deleted: %v", err)
	}
}

func TestSetRunProfileSnapshot(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	w := mustCreateWorkspace(t, db)
	s := mustCreateSession(t, db, w.ID)
	m := mustCreateMember(t, db)
	r := mustCreateRun(t, db, s.ID, m.ID, domain.RunQueued)

	snap := &domain.ProfileSnapshot{MemberID: m.ID, Harness: "claude", Digest: "d1"}
	if err := db.SaveProfileSnapshot(ctx, snap, []ProfileFile{{Path: "a", Mode: 0o644, Content: []byte("x")}}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := db.SetRunProfileSnapshot(ctx, r.ID, snap.ID); err != nil {
		t.Fatalf("SetRun: %v", err)
	}
	got, err := db.GetRun(ctx, r.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.ProfileSnapshotID != snap.ID {
		t.Fatalf("run pin %q want %q", got.ProfileSnapshotID, snap.ID)
	}
}

func TestProfileBlobDedup(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	m := mustCreateMember(t, db)
	shared := []byte("same-bytes")
	a := &domain.ProfileSnapshot{MemberID: m.ID, Harness: "claude", Digest: "d-a"}
	b := &domain.ProfileSnapshot{MemberID: m.ID, Harness: "claude", Digest: "d-b"}
	if err := db.SaveProfileSnapshot(ctx, a, []ProfileFile{{Path: "a", Mode: 0o644, Content: shared}}); err != nil {
		t.Fatalf("a: %v", err)
	}
	if err := db.SaveProfileSnapshot(ctx, b, []ProfileFile{{Path: "b", Mode: 0o644, Content: shared}}); err != nil {
		t.Fatalf("b: %v", err)
	}
	var n int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM profile_blobs`).Scan(&n); err != nil {
		t.Fatalf("count blobs: %v", err)
	}
	if n != 1 {
		t.Fatalf("blobs = %d, want 1 (content-addressed)", n)
	}
}

func TestPruneKeepsPinnedRunSnapshot(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	w := mustCreateWorkspace(t, db)
	s := mustCreateSession(t, db, w.ID)
	m := mustCreateMember(t, db)
	r := mustCreateRun(t, db, s.ID, m.ID, domain.RunQueued)

	var ids []domain.ProfileSnapshotID
	for i := 0; i < 12; i++ {
		snap := &domain.ProfileSnapshot{
			MemberID: m.ID,
			Harness:  "claude",
			Digest:   string(rune('a'+i)) + "-pin",
		}
		files := []ProfileFile{{Path: "n.txt", Mode: 0o644, Content: []byte{byte(i + 20)}}}
		if err := db.SaveProfileSnapshot(ctx, snap, files); err != nil {
			t.Fatalf("Save %d: %v", i, err)
		}
		ids = append(ids, snap.ID)
	}
	if err := db.SetRunProfileSnapshot(ctx, r.ID, ids[0]); err != nil {
		t.Fatalf("pin: %v", err)
	}
	if err := db.PruneProfileSnapshots(ctx, m.ID, "claude", 10); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if _, err := db.GetProfileSnapshot(ctx, ids[0]); err != nil {
		t.Fatalf("pinned snapshot pruned: %v", err)
	}
}
