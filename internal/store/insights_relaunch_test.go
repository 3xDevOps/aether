package store

import (
	"context"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// A relaunched run is one row: it counts in no finished total while live again
// and, once closed again, once on the day of its latest finish.
func TestInsightsCountsARelaunchedRunOnceAtItsLatestFinish(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	owner := mustCreateMember(t, db)
	ws := &domain.Workspace{Name: "relaunch", BaseBranch: "main"}
	if err := db.CreateWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}
	day := func(d int) time.Time { return time.Date(2026, 3, d, 0, 0, 0, 0, time.UTC) }
	created, firstFinish := day(1).Add(time.Hour), day(2).Add(time.Hour)
	run := &domain.Run{ID: "relaunched", WorkspaceID: ws.ID, MemberID: owner.ID, Task: "t", Harness: "claude",
		Mode: domain.LaunchTUI, Status: domain.RunMerged, Reason: "closed; retained container",
		CreatedAt: created, StartedAt: &created, FinishedAt: &firstFinish}
	if err := db.CreateRunWithID(ctx, run); err != nil {
		t.Fatal(err)
	}

	query := func() *Insights {
		t.Helper()
		q := InsightsQuery{From: day(1), To: day(5), Now: day(5)}
		for d := 1; d < 5; d++ {
			q.Buckets = append(q.Buckets, Interval{Start: day(d), End: day(d + 1)})
		}
		q.Previous = Interval{Start: day(1).Add(-4 * 24 * time.Hour), End: day(1)}
		q.Days = []Interval{{Start: day(1), End: day(5)}}
		got, err := db.Insights(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	counts := func(got *Insights) (merged, abandoned []int) {
		for _, s := range got.Series {
			merged, abandoned = append(merged, s.Merged), append(abandoned, s.Abandoned)
		}
		return merged, abandoned
	}
	equal := func(got, want []int) bool {
		for i := range want {
			if got[i] != want[i] {
				return false
			}
		}
		return len(got) == len(want)
	}

	merged, _ := counts(query())
	if !equal(merged, []int{0, 1, 0, 0}) {
		t.Fatalf("first finish merged per day = %v, want [0 1 0 0]", merged)
	}

	relaunched := day(3).Add(time.Hour)
	live := *run
	live.Status, live.Reason, live.StartedAt, live.FinishedAt = domain.RunRunning, "", &relaunched, nil
	if err := db.UpdateRun(ctx, &live); err != nil {
		t.Fatal(err)
	}
	got := query()
	merged, abandoned := counts(got)
	if !equal(merged, []int{0, 0, 0, 0}) || !equal(abandoned, []int{0, 0, 0, 0}) || got.Totals.Merged != 0 || got.Totals.Abandoned != 0 {
		t.Fatalf("relaunched live run is counted as finished: merged %v abandoned %v totals %+v", merged, abandoned, got.Totals)
	}

	secondFinish := day(4).Add(time.Hour)
	done := live
	done.Status, done.Reason, done.FinishedAt = domain.RunAbandoned, "closed; retained container", &secondFinish
	if err := db.UpdateRun(ctx, &done); err != nil {
		t.Fatal(err)
	}
	got = query()
	merged, abandoned = counts(got)
	if !equal(merged, []int{0, 0, 0, 0}) || !equal(abandoned, []int{0, 0, 0, 1}) || got.Totals.Merged != 0 || got.Totals.Abandoned != 1 {
		t.Fatalf("second finish: merged %v abandoned %v totals %+v; want one abandoned on day 4 only", merged, abandoned, got.Totals)
	}
}
