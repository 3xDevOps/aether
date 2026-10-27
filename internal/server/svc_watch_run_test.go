package server

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/github"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
	"github.com/3xDevOps/Aether/internal/store/storetest"
	"github.com/3xDevOps/Aether/internal/watch"
)

type quietRuns struct{}

func (quietRuns) PendingInputs(domain.RunID) []domain.RunInputRequest { return nil }
func (quietRuns) Paused(domain.RunID) bool                            { return false }
func (quietRuns) ACPPending(domain.RunID) []acphost.Request           { return nil }

// pullRequest is a pull request source whose one pull request's checks are
// running until done.
type pullRequest struct{ done atomic.Bool }

func (*pullRequest) Validate(context.Context, domain.MemberID, protocol.WatchTarget) (string, error) {
	return "", nil
}

func (*pullRequest) Evaluate(context.Context, watch.PRCheck) (watch.PRResult, error) {
	return watch.PRResult{}, nil
}

func (p *pullRequest) Snapshot(_ context.Context, _ domain.MemberID, repo string, number int, _ bool) (github.Snapshot, error) {
	run := github.CheckRun{ID: 1, Name: "test", Status: "in_progress", SuiteID: 9, App: "GitHub Actions"}
	if p.done.Load() {
		at := time.Now()
		run.Status, run.Conclusion, run.CompletedAt = "completed", "success", &at
	}
	return github.Snapshot{Repo: repo, Number: number, State: "open", HeadSHA: "1111111111111111111111111111111111111111",
		URL: "https://github.com/acme/app/pull/7", CheckRuns: []github.CheckRun{run}}, nil
}

type fullInbox struct{}

func (fullInbox) NotifyRun(context.Context, domain.RunID, domain.RunID, string, string, string) (bool, error) {
	return false, errors.New("coord: inbox write: disk full")
}

// The scheduler reads a pull request wait as the watch service keeps it: a
// pending one with its fresh source read, and a resolved one whose wake
// cannot be written with that error and no delivery, which #604 judges a
// broken wait.
func TestTheSchedulerReadsTheRunsWaitConditions(t *testing.T) {
	ctx := t.Context()
	db, err := storetest.Open(filepath.Join(t.TempDir(), "aether.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	bus, err := events.NewInProc(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bus.Close() })
	member := &domain.Member{DisplayName: "Ada", TailnetLogin: "ada@example.com", Color: "#e6194b", Role: domain.RoleCollaborator}
	ws := &domain.Workspace{Name: "proj", BaseBranch: domain.DefaultBaseBranch}
	if err = db.CreateMember(ctx, member); err != nil {
		t.Fatal(err)
	}
	if err = db.CreateWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}
	run := &domain.Run{WorkspaceID: ws.ID, MemberID: member.ID, Task: "ship", Harness: "claude", Mode: domain.LaunchACP, ACP: true, Status: domain.RunRunning}
	if err = db.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	var offset atomic.Int64
	now := func() time.Time { return time.Now().Add(time.Duration(offset.Load())) }
	svc, err := watch.New(watch.Config{Store: db, Bus: bus, Runs: quietRuns{}, Tick: 20 * time.Millisecond, Now: now,
		Swarm: func(context.Context, *domain.Run) (*protocol.AttentionItem, domain.MemberID, error) {
			return nil, "", nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	pr := &pullRequest{}
	svc.UsePRSource(pr)
	svc.UseRunNotifier(fullInbox{})
	if err = svc.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })

	spec, err := protocol.ParseWaitSpec("pr:acme/app#7:checks")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.RegisterWait(ctx, run, &store.CoordStateReport{RunID: run.ID, IdempotencyKey: "k", Outcome: protocol.CoordOutcomeWaiting,
		Summary: "CI on #7", Until: []string{spec.Spec}}, []protocol.WaitSpec{spec}); err != nil {
		t.Fatal(err)
	}
	if err = db.MarkCoordStateReportApplied(ctx, run.ID, "k"); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.SettleRunWaits(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	reader := waitConditions{svc}
	conditions, err := reader.RunWaits(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(conditions) != 1 {
		t.Fatalf("conditions = %+v, want one", conditions)
	}
	c := conditions[0]
	if c.Spec != spec.Spec || c.State != protocol.WaitPending || c.SourceCheckedAt.IsZero() || c.LastCheckedAt.IsZero() ||
		!c.NextCheckAt.After(c.SourceCheckedAt) || c.CreatedAt.IsZero() || c.Summary == "" || !c.ResolvedAt.IsZero() {
		t.Fatalf("pending condition = %+v, want pending with its source read and next check", c)
	}

	pr.done.Store(true)
	offset.Add(int64(2 * watch.PRPollIdle))
	deadline := time.Now().Add(10 * time.Second)
	for {
		if conditions, err = reader.RunWaits(ctx, run.ID); err != nil {
			t.Fatal(err)
		}
		c = conditions[0]
		if c.LastDeliveryError != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("condition = %+v, want its wake failing", c)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if c.State != protocol.WaitResolved || c.ResolvedAt.IsZero() || !c.DeliveredAt.IsZero() || c.LastDeliveryError != "coord: inbox write: disk full" {
		t.Fatalf("resolved condition = %+v, want resolved, undelivered, with the inbox error", c)
	}
}
