package store

import (
	"context"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// Each member keeps their newest AuditKeep entries, so one member's calls
// never evict another's.
func TestAuditKeepsEachMembersNewestEntries(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	add := func(member string) {
		t.Helper()
		if err := db.AddAudit(ctx, &AuditEntry{At: time.Now(), Member: domain.MemberID(member), Via: "cli", Method: "account.share"}); err != nil {
			t.Fatal(err)
		}
	}
	add("mem-quiet")
	for range AuditKeep + 5 {
		add("mem-loud")
	}
	count := func(member string) int {
		t.Helper()
		var n int
		if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit WHERE member_id = ?`, member).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if got := count("mem-loud"); got != AuditKeep {
		t.Fatalf("loud member keeps %d entries, want %d", got, AuditKeep)
	}
	if got, err := db.ListAudit(ctx, AuditQuery{Member: "mem-quiet", Limit: 10}); err != nil || len(got) != 1 {
		t.Fatalf("quiet member's entries = %v %v, want their one entry kept", got, err)
	}
}
