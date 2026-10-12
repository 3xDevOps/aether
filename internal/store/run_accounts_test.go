package store

import (
	"errors"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// A move appends a segment and keeps the account the run first asked for;
// moving to an account no member has is refused and records nothing.
func TestSetRunAccountKeepsHistory(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := t.Context()
	w := mustCreateWorkspace(t, db)
	ada, grace := mustCreateMember(t, db), mustCreateMember(t, db)
	r := mustCreateRun(t, db, w.ID, ada.ID, domain.RunRunning)

	if _, err := db.SetRunAccount(ctx, r.ID, "mem-nobody", "usage limit", time.Now()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("move to an unknown account = %v, want ErrNotFound", err)
	}
	if segments, err := db.ListRunAccountSegments(ctx, r.ID); err != nil || len(segments) != 0 {
		t.Fatalf("segments after a refused move = %+v, %v", segments, err)
	}
	seq, err := db.SetRunAccount(ctx, r.ID, grace.ID, "usage limit", time.Now())
	if err != nil || seq != 1 {
		t.Fatalf("SetRunAccount = %d, %v", seq, err)
	}
	got, err := db.GetRun(ctx, r.ID)
	if err != nil || got.AccountMember() != grace.ID || got.RequestedAccountMemberID != ada.ID {
		t.Fatalf("moved run = %+v, %v", got, err)
	}
	if _, err = db.SetRunAccount(ctx, r.ID, ada.ID, "usage limit", time.Now()); err != nil {
		t.Fatal(err)
	}
	got, err = db.GetRun(ctx, r.ID)
	segments, listErr := db.ListRunAccountSegments(ctx, r.ID)
	if err != nil || listErr != nil || got.RequestedAccountMemberID != "" || len(segments) != 3 ||
		segments[0].AccountMemberID != ada.ID || segments[1].AccountMemberID != grace.ID || segments[2].AccountMemberID != ada.ID {
		t.Fatalf("run back on its requested account = %+v %v, segments %+v %v", got, err, segments, listErr)
	}
}
