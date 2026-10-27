package store

import (
	"context"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// Each member keeps their newest auditKeep call entries and, apart from
// them, their newest auditKeep security entries, so neither one member's
// calls nor a flood of a connector's calls, refused ones included, evicts
// another member's entries or a security change.
func TestAuditKeepsEachMembersNewestEntries(t *testing.T) {
	keep := auditKeep
	auditKeep = 50
	t.Cleanup(func() { auditKeep = keep })
	db := openTestDB(t)
	ctx := context.Background()
	add := func(member, method string, code int) {
		t.Helper()
		if err := db.AddAudit(ctx, &AuditEntry{At: time.Now(), Member: domain.MemberID(member), Via: "connector", ConnectorID: "con-loud",
			Method: method, Code: code}); err != nil {
			t.Fatal(err)
		}
	}
	add("mem-quiet", "run.launch", 0)
	add("mem-loud", "connector.create", 0)
	for i := range auditKeep + 5 {
		if i%2 == 0 {
			add("mem-loud", "run.launch", 0)
		} else {
			add("mem-loud", "member.role", -32001)
		}
	}
	count := func(query string) int {
		t.Helper()
		var n int
		if err := db.db.QueryRowContext(ctx, query).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if got := count(`SELECT COUNT(*) FROM audit WHERE member_id = 'mem-loud'`); got != auditKeep+1 {
		t.Fatalf("loud member keeps %d entries, want %d calls and its one security entry", got, auditKeep+1)
	}
	if got := count(`SELECT COUNT(*) FROM audit WHERE method = 'connector.create'`); got != 1 {
		t.Fatalf("connector.create entries = %d, want it kept through the flood", got)
	}
	if got, err := db.ListAudit(ctx, AuditQuery{Member: "mem-quiet", Limit: 10}); err != nil || len(got) != 1 {
		t.Fatalf("quiet member's entries = %v %v, want their one entry kept", got, err)
	}
}
