package fallback

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/quota"
	"github.com/3xDevOps/Aether/internal/store/storetest"
)

// usageByName answers each account's Claude quota from its display name; an
// account without an entry has no OAuth login.
type usageByName struct {
	t     *testing.T
	names map[domain.MemberID]string
	used  map[string]float64
	reset time.Time
}

func (u usageByName) Read(_ context.Context, account domain.MemberID, refresh bool) ([]quota.Provider, error) {
	if refresh {
		u.t.Errorf("selection refreshed %s's quota; it must use the cache", account)
	}
	used, ok := u.used[u.names[account]]
	if !ok {
		return []quota.Provider{{Provider: "claude", Status: "unauthenticated"}}, nil
	}
	resets := u.reset
	return []quota.Provider{{Provider: "claude", Status: "ok", Windows: []quota.Window{
		{ID: "five_hour", UsedPercent: used, ResetsAt: &resets},
		{ID: "seven_day_opus", UsedPercent: 100, ResetsAt: &resets},
	}}}, nil
}

func TestSelect(t *testing.T) {
	future := time.Now().Add(time.Hour)
	for _, tc := range []struct {
		name      string
		requested string
		harness   string
		order     []string
		disabled  bool
		revoke    string
		used      map[string]float64
		reset     time.Time
		want      string
	}{
		{name: "requested has usage", requested: "Ada", used: map[string]float64{"Ada": 94}, want: "Ada"},
		{name: "exhausted requested moves to most remaining", requested: "Ada",
			used: map[string]float64{"Ada": 95, "Lena": 50, "Bo": 30, "Cy": 80}, want: "Bo"},
		{name: "explicit order is followed", requested: "Ada", order: []string{"Cy", "Bo"},
			used: map[string]float64{"Ada": 99, "Lena": 0, "Bo": 10, "Cy": 80}, want: "Cy"},
		{name: "explicit order excludes unlisted accounts", requested: "Ada", order: []string{"Ada", "Dee", "Cy"},
			used: map[string]float64{"Ada": 99, "Lena": 0, "Bo": 0, "Cy": 96, "Dee": 0}, want: "Ada"},
		{name: "revoked share is skipped", requested: "Ada", revoke: "Bo",
			used: map[string]float64{"Ada": 100, "Lena": 60, "Bo": 0, "Cy": 70}, want: "Lena"},
		{name: "unreadable requested stays", requested: "Ada", used: map[string]float64{"Bo": 0}, want: "Ada"},
		{name: "agent without quota stays", requested: "Ada", harness: "opencode",
			used: map[string]float64{"Ada": 100, "Bo": 0}, want: "Ada"},
		{name: "reset window does not count", requested: "Ada", reset: time.Now().Add(-time.Minute),
			used: map[string]float64{"Ada": 100, "Bo": 0}, want: "Ada"},
		{name: "no candidate qualifies keeps requested", requested: "Ada",
			used: map[string]float64{"Ada": 100, "Lena": 97, "Cy": 96}, want: "Ada"},
		{name: "disabled keeps requested", requested: "Ada", disabled: true,
			used: map[string]float64{"Ada": 100, "Bo": 0}, want: "Ada"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			db, err := storetest.Open(filepath.Join(t.TempDir(), "fallback.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			ids := map[string]domain.MemberID{}
			names := map[domain.MemberID]string{}
			for _, name := range []string{"Lena", "Ada", "Bo", "Cy", "Dee"} {
				m := &domain.Member{DisplayName: name, TailnetLogin: strings.ToLower(name) + "@example.com", Color: "#3cb44b", Role: domain.RoleCollaborator}
				if createErr := db.CreateMember(ctx, m); createErr != nil {
					t.Fatal(createErr)
				}
				ids[name], names[m.ID] = m.ID, name
			}
			launcher := ids["Lena"]
			for _, owner := range []string{"Ada", "Bo", "Cy"} {
				if shareErr := db.ShareAccount(ctx, ids[owner], launcher); shareErr != nil {
					t.Fatal(shareErr)
				}
			}
			order := make([]string, 0, len(tc.order))
			for _, name := range tc.order {
				order = append(order, string(ids[name]))
			}
			policy, err := Policy(order, tc.disabled)
			if err != nil {
				t.Fatal(err)
			}
			harness, reset := tc.harness, tc.reset
			if harness == "" {
				harness = "claude"
			}
			if reset.IsZero() {
				reset = future
			}
			usage := usageByName{t: t, names: names, used: tc.used, reset: reset}
			measured, err := Measure(ctx, db, usage, launcher, string(ids[tc.requested]), harness, policy)
			if err != nil {
				t.Fatal(err)
			}
			// A share revoked after the unlocked read is refused by Select.
			if tc.revoke != "" {
				if revokeErr := db.RevokeAccountShare(ctx, ids[tc.revoke], launcher); revokeErr != nil {
					t.Fatal(revokeErr)
				}
			}
			got, err := Select(ctx, db, measured, launcher, ids[tc.requested], policy)
			if err != nil || got != ids[tc.want] {
				t.Fatalf("Select = %s, %v; want %s", names[got], err, tc.want)
			}
		})
	}
}
