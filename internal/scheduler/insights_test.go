package scheduler

import (
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/store"
)

func TestStatusChangesRecordTransitions(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	ctx := t.Context()
	from := time.Now().UTC()
	run, _ := e.launchFake(t, "measured task")
	if err := e.sched.Kill(ctx, run.ID, e.member.ID); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	e.waitStoreStatus(t, run.ID, domain.RunAbandoned)

	to := time.Now().UTC().Add(time.Second)
	got, err := e.db.Insights(ctx, store.InsightsQuery{
		Member: e.member.ID, From: from, To: to, Now: to,
		Previous: store.Interval{Start: from.Add(-time.Hour), End: from},
	})
	if err != nil {
		t.Fatalf("Insights: %v", err)
	}
	if got.Totals.Killed != 1 || got.Durations.StateRunsMeasured != 1 || got.StateTimesSince == nil || got.StateTimesSince.Before(from) {
		t.Fatalf("insights after a kill = totals %+v, durations %+v, state times since %v; want the session's transitions recorded",
			got.Totals, got.Durations, got.StateTimesSince)
	}
}
