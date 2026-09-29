package edgestore

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
)

// TestAccountAccessUsesIndexes checks the servers an account reaches, and
// that finding them reads no server or directory the account has no
// candidate entry on: every signed-in list request runs this query.
func TestAccountAccessUsesIndexes(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	me := owner()
	other := edgeproto.Account{Provider: edgeproto.ProviderGitHub, Subject: "2", Login: "other"}
	claim(t, s, serverA, "alpha", me)
	ids := map[string]string{"beta": serverB, "gamma": "cccccccccccccccccccccccccc",
		"delta": "dddddddddddddddddddddddddd", "omega": "eeeeeeeeeeeeeeeeeeeeeeeeee"}
	entries := map[string]edgeproto.DirectoryEntry{
		"beta":  {Kind: edgeproto.EntryMember, Provider: me.Provider, Subject: me.Subject, Role: "collaborator"},
		"gamma": {Kind: edgeproto.EntryInvitation, Provider: me.Provider, Login: "OWNER", Role: "viewer", ExpiresAt: testNow.Add(time.Hour)},
		"delta": {Kind: edgeproto.EntryInvitation, Email: "OWNER@example.test", Role: "viewer", ExpiresAt: testNow.Add(time.Hour)},
		"omega": {Kind: edgeproto.EntryMember, Provider: other.Provider, Subject: other.Subject, Role: "admin"},
	}
	for name, id := range ids {
		claim(t, s, id, name, other)
		if err := s.ReplaceDirectory(ctx, id, []edgeproto.DirectoryEntry{entries[name]}); err != nil {
			t.Fatal(err)
		}
	}

	all, err := s.AccountAccess(ctx, me)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, acc := range all {
		got = append(got, acc.Name)
	}
	if want := []string{"alpha", "beta", "delta", "gamma"}; !slices.Equal(got, want) || !all[0].Owner || all[1].Owner ||
		len(all[0].Entries) != 0 || len(all[1].Entries) != 1 || all[1].Entries[0].Role != "collaborator" {
		t.Fatalf("access of %s: %+v, want servers %v", me.Login, all, want)
	}

	args := make([]any, strings.Count(accessQuery(accountServers), "?"))
	rows, err := s.db.QueryContext(ctx, `EXPLAIN QUERY PLAN `+accessQuery(accountServers), args...)
	if err != nil {
		t.Fatal(err)
	}
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	for _, step := range plan {
		if strings.HasPrefix(step, "SCAN ") {
			t.Errorf("account access plan scans a table: %q", strings.Join(plan, "; "))
			break
		}
	}
}
