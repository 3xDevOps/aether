package push

import (
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

func TestTheLevelRoutesEachKind(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		level string
		now   []string
		later []string
	}{
		{protocol.NotifyBlockers, []string{KindNeed}, []string{KindDone, KindFailed, KindMerged, KindChecks, KindOther}},
		{protocol.NotifyBatched, []string{KindNeed}, []string{KindDone, KindFailed, KindMerged, KindChecks, KindOther}},
		{protocol.NotifyAll, []string{KindNeed, KindDone, KindFailed, KindMerged, KindChecks, KindOther}, nil},
	} {
		p := store.NotifyPrefs{Level: tc.level}
		for _, kind := range tc.now {
			if got := RouteOf(p, kind, now); got != RouteNow {
				t.Errorf("%s at %s = %v, want pushed at once", kind, tc.level, got)
			}
		}
		for _, kind := range tc.later {
			if got := RouteOf(p, kind, now); got != RouteDigest {
				t.Errorf("%s at %s = %v, want the digest", kind, tc.level, got)
			}
		}
		p.QuietStart, p.QuietEnd, p.QuietTZ = "11:00", "13:00", "UTC"
		if got := RouteOf(p, KindNeed, now); got != RouteOvernight {
			t.Errorf("a need in quiet hours at %s = %v, want held for the digest", tc.level, got)
		}
	}
}

func TestDigestsFallDueByLevel(t *testing.T) {
	t.Parallel()
	at := func(s string) time.Time {
		t.Helper()
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	queued := []*store.DigestItem{{CreatedAt: at("2026-11-03T14:07:00Z")}, {CreatedAt: at("2026-11-03T14:40:00Z")}}
	now := at("2026-11-03T14:50:00Z")
	for _, tc := range []struct {
		name string
		p    store.NotifyPrefs
		want string
	}{
		{"batched: the half hour after the oldest", store.NotifyPrefs{Level: protocol.NotifyBatched}, "2026-11-03T14:30:00Z"},
		{"batched: the half hour in the member's zone", store.NotifyPrefs{Level: protocol.NotifyBatched, DigestTZ: "Asia/Kathmandu"}, "2026-11-03T14:15:00Z"},
		{"batched: Local is not the member's zone", store.NotifyPrefs{Level: protocol.NotifyBatched, DigestTZ: "Local"}, "2026-11-03T14:30:00Z"},
		{"blockers: the next digest time in its zone", store.NotifyPrefs{Level: protocol.NotifyBlockers, DigestAt: "09:00", DigestTZ: "Europe/Berlin"}, "2026-11-04T08:00:00Z"},
		{"blockers: UTC without a zone", store.NotifyPrefs{Level: protocol.NotifyBlockers, DigestAt: "16:00"}, "2026-11-03T16:00:00Z"},
		{"quiet hours: when they end", store.NotifyPrefs{Level: protocol.NotifyBatched, QuietStart: "14:00", QuietEnd: "15:15", QuietTZ: "UTC"}, "2026-11-03T15:15:00Z"},
	} {
		if got := digestDue(tc.p, queued, now); !got.Equal(at(tc.want)) {
			t.Errorf("%s: due %s, want %s", tc.name, got.Format(time.RFC3339), tc.want)
		}
	}
	held := []*store.DigestItem{{CreatedAt: at("2026-11-03T14:07:00Z"), Overnight: true}}
	if got := digestDue(store.NotifyPrefs{Level: protocol.NotifyBlockers, DigestAt: "09:00"}, held, now); !got.Equal(now) {
		t.Errorf("held by quiet hours that ended: due %s, want at once", got)
	}
}

func TestTheDigestHasAFixedShape(t *testing.T) {
	t.Parallel()
	items := []*store.DigestItem{
		{Kind: KindDone, Workspace: "api", Title: "Rotate the staging keys", Body: "Agent reported done"},
		{Kind: KindNeed, Workspace: "api", Title: "Fix login test", Body: "asks \"run the migration?\""},
		{Kind: KindFailed, Workspace: "web", Title: "Bump deps across every package in the monorepo", Body: "Failed: checks failed (lint)"},
		{Kind: KindDone, Workspace: "api", Title: "Write\u202e docs", Body: "Comp\u200bleted"},
		{Kind: KindNeed, Workspace: "api", Title: "Launch docs session", Body: "Permission:\nrm -rf\x1b[2J build/"},
		{Kind: KindDone, Workspace: "web", Title: "Add a test", Body: "Agent reported done"},
		{Kind: KindMerged, Title: "acme/web#42: Add a test", Body: "PR #42 merged on GitHub"},
	}
	title, body := digestText(items, digestLines)
	if want := "Aether · 2 need you · 1 failed · 3 done · 1 merged"; title != want {
		t.Errorf("title = %q, want %q", title, want)
	}
	want := `api  Fix login test: asks "run the migration?"  [Answer]
api  Launch docs session: Permission: rm -rf [2J build/  [Answer]
web  Bump deps across every package in the mo…: Failed: checks failed (lint)  [Open]
api  Rotate the staging keys: Agent reported done  [Review]
api  Write docs: Completed  [Review]
+1 done · +1 merged`
	if body != want {
		t.Errorf("body =\n%s\nwant\n%s", body, want)
	}

	items[3].Overnight = true
	_, body = digestText(items, digestLines)
	if want := "Overnight: api  Write docs: Completed  [Review]"; body[:len(want)] != want {
		t.Errorf("body =\n%s\nwant it to lead with %q", body, want)
	}
}

func TestADigestOfLongNamesStillFitsOnePush(t *testing.T) {
	t.Parallel()
	var items []*store.DigestItem
	for range 7 {
		items = append(items, &store.DigestItem{Kind: KindDone, Workspace: strings.Repeat("w", 800), Title: strings.Repeat("ü", 400), Body: strings.Repeat("\u202e€", 400)})
	}
	payload, err := digestPayload(items)
	if err != nil || len(payload) > maxPayload {
		t.Fatalf("digest is %d octets, %v; want at most %d", len(payload), err, maxPayload)
	}
	if !strings.Contains(string(payload), "Aether · 7 done") {
		t.Fatalf("digest %s lost its counts", payload)
	}
}
