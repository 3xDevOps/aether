package scheduler

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/watch"
)

// TestWatchAnnouncesAReportedSuccessOnceThroughItsReview: the owner's
// watch announces the agent's success, and the owner then completing it
// through review closes the run as completed without a second notice.
func TestWatchAnnouncesAReportedSuccessOnceThroughItsReview(t *testing.T) {
	t.Parallel()
	log, err := events.OpenSQLiteLog(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	bus, err := events.NewInProc(context.Background(), log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bus.Close() })
	e := newReportingEnv(t, func(cfg *Config) {
		cfg.StallThreshold = time.Hour
		cfg.Bus = bus
	})
	e.bus = bus
	ctx := t.Context()
	run, _ := e.launchReporting(t)
	e.waitStoreStatus(t, run.ID, domain.RunRunning)

	svc, err := watch.New(watch.Config{
		Store: e.db, Bus: bus, Runs: e.sched, Head: log.LastSeq, Debounce: 20 * time.Millisecond, Tick: 50 * time.Millisecond,
		Swarm: func(context.Context, *domain.Run) (*protocol.AttentionItem, domain.MemberID, error) {
			return nil, "", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	created, err := svc.Create(ctx, e.member.ID, protocol.WatchCreateParams{
		Target:     protocol.WatchTarget{Kind: protocol.WatchTargetRun, RunID: string(run.ID)},
		Conditions: []string{protocol.WatchFinished},
	})
	if err != nil {
		t.Fatal(err)
	}
	notices := func() []string {
		got, getErr := svc.Get(ctx, e.member.ID, created.Watch.ID)
		if getErr != nil {
			t.Fatal(getErr)
		}
		var out []string
		for _, n := range got.Notifications {
			out = append(out, n.Body)
		}
		return out
	}

	if err = e.sched.FinishReported(ctx, run.ID, "report-1", domain.RunCompleted, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err = e.sched.ReportAgentState(ctx, run.ID, waitingForInput); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the reported success announced", func() bool { return len(notices()) == 1 })

	if _, err = e.sched.Review(ctx, run.ID, e.member.ID, protocol.RunReviewComplete); err != nil {
		t.Fatal(err)
	}
	closed := e.waitStoreStatus(t, run.ID, domain.RunCompleted)
	if closed.Reason == domain.ReportedSuccessReason {
		t.Fatalf("completed run reason = %q, want the member's close", closed.Reason)
	}
	waitFor(t, "the close evaluated", func() bool {
		got, getErr := svc.Get(ctx, e.member.ID, created.Watch.ID)
		return getErr == nil && got.Watch.Observed.RunStatus == string(domain.RunCompleted)
	})
	if got := notices(); len(got) != 1 || got[0] != "Agent reported done" {
		t.Fatalf("notices = %q, want Agent reported done once", got)
	}
}
