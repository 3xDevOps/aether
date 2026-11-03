package sshd

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/scheduler"
	"github.com/3xDevOps/Aether/internal/store"
)

func TestRunCloseCheckKinds(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	ctx := context.Background()
	db := e.store.(*store.DB)
	committed := time.Now().Add(-2 * time.Hour)
	observed := time.Date(2026, 11, 3, 18, 20, 0, 0, time.UTC)
	newRun := func(title string, mod func(*domain.Run)) *domain.Run {
		r := &domain.Run{WorkspaceID: e.ws.ID, MemberID: e.member.ID, Task: title, Title: title, Harness: "claude",
			Mode: domain.LaunchTUI, Status: domain.RunRunning, Branch: "aether/run-" + title}
		if mod != nil {
			mod(r)
		}
		if err := db.CreateRun(ctx, r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	finished := func(r *domain.Run) { r.Status = domain.RunCompleted }
	pushed := func(r *domain.Run) { r.BaseCommit, r.LastCommit, r.LastCommitAt = "aaaa", "bbbb", committed }
	pr := func(r *domain.Run, number int, state string, merged *time.Time) {
		if err := db.RecordRunPullRequest(ctx, &store.RunPullRequest{RunID: r.ID, Repository: "acme/api", Number: number,
			URL: fmt.Sprintf("https://github.com/acme/api/pull/%d", number), State: state, MergedAt: merged, ObservedAt: observed}); err != nil {
			t.Fatal(err)
		}
	}
	mail := func(from, to *domain.Run, at time.Time) {
		if err := db.AppendRunMessage(ctx, &store.RunMessage{WorkspaceID: e.ws.ID, FromRun: from.ID, ToRun: to.ID, Body: "hi",
			Kind: store.RunMessageKindMessage, CreatedAt: at}, 100); err != nil {
			t.Fatal(err)
		}
	}
	reply := func(from, to *domain.Run) {
		if err := db.AppendRunMessage(ctx, &store.RunMessage{WorkspaceID: e.ws.ID, FromRun: from.ID, ToRun: to.ID, Body: "done",
			Kind: store.RunMessageKindReply, CorrelationID: "q-1", CreatedAt: time.Now().Add(-2 * time.Hour)}, 100); err != nil {
			t.Fatal(err)
		}
	}
	read := func(to *domain.Run) {
		_, token, _, err := db.DeliverRunMessages(ctx, to.ID, "", 100)
		if err == nil {
			_, _, _, err = db.DeliverRunMessages(ctx, to.ID, token, 100)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	await := func(owner, target *domain.Run) {
		now := time.Now()
		key := "wait-" + string(owner.ID)
		spec := "run:" + string(target.ID) + ":finished"
		if _, err := db.RecordWaitingReport(ctx, &store.CoordStateReport{RunID: owner.ID, IdempotencyKey: key, Outcome: protocol.CoordOutcomeWaiting,
			WaitingFor: "agents", Until: []string{spec}}, []*store.RunWait{{
			Watch: store.Watch{MemberID: owner.MemberID, WorkspaceID: owner.WorkspaceID, TargetKind: protocol.WatchTargetRun, TargetTitle: spec,
				Conditions: []string{protocol.WaitKindRunFinished}, Source: protocol.WatchSourceAgent, CreatedAt: now, UpdatedAt: now,
				ExpiresAt: now.Add(time.Hour), NextCheckAt: &now},
			Spec: spec, Kind: protocol.WaitKindRunFinished, TargetRun: target.ID}}); err != nil {
			t.Fatal(err)
		}
		if err := db.MarkCoordStateReportApplied(ctx, owner.ID, key); err != nil {
			t.Fatal(err)
		}
	}
	child := func(parent *domain.Run, title string, status domain.RunStatus) *domain.Run {
		c, _, err := db.ReserveChild(ctx, &domain.ChildReservation{ParentRunID: parent.ID, RootRunID: parent.ID,
			TreeKey: string(parent.ID), WorkspaceID: e.ws.ID, OwnerID: e.member.ID, AccountID: e.member.ID, Harness: "claude",
			Mode: domain.LaunchTUI, Purpose: "help", Task: "help", IdempotencyKey: title, RequestHash: "h"})
		if err != nil {
			t.Fatal(err)
		}
		r := &domain.Run{ID: c.RunID, WorkspaceID: e.ws.ID, MemberID: e.member.ID, Task: title, Title: title, Harness: "claude",
			Mode: domain.LaunchTUI, Status: status, Branch: "aether/run-" + title}
		if err := db.CreateRunWithID(ctx, r); err != nil {
			t.Fatal(err)
		}
		if err := db.SetChildState(ctx, c.RunID, domain.ChildStarted, "", domain.ChildQueued, domain.ChildLaunching); err != nil {
			t.Fatal(err)
		}
		return r
	}

	quiet := newRun("quiet", finished)
	protected := newRun("protected", func(r *domain.Run) { r.Protected = true })
	openPR := newRun("open-pr", pushed)
	pr(openPR, 7, "open", nil)
	unmerged := newRun("unmerged", pushed)
	pr(unmerged, 8, "closed", nil)
	mergedBefore := committed.Add(-time.Hour)
	stale := newRun("stale-merge", pushed)
	pr(stale, 9, "merged", &mergedBefore)
	mergedAfter := committed.Add(time.Hour)
	shipped := newRun("shipped", pushed)
	pr(shipped, 10, "merged", &mergedAfter)
	mailed := newRun("mailed", finished)
	mail(quiet, mailed, time.Now().Add(-5*time.Minute))
	read(mailed)
	mail(quiet, mailed, time.Now().Add(-2*time.Hour))
	mail(quiet, quiet, time.Now())
	mail(quiet, shipped, time.Now().Add(-2*time.Hour))
	read(shipped)
	awaited := newRun("awaited", finished)
	coordinator := newRun("coordinator", nil)
	await(coordinator, awaited)
	ended := newRun("ended", func(r *domain.Run) { r.Status = domain.RunAbandoned })
	await(ended, quiet)
	parent := newRun("parent", nil)
	kid := child(parent, "kid", domain.RunRunning)
	child(parent, "done-kid", domain.RunMerged)
	accepted := child(parent, "accepted-kid", domain.RunCompleted)
	if _, _, err := db.SetRunReview(ctx, accepted.ID, domain.ReviewAccepted, ""); err != nil {
		t.Fatal(err)
	}
	extra := newRun("extra-merge", pushed)
	if err := db.RecordRunPullRequest(ctx, &store.RunPullRequest{RunID: extra.ID, RepoWorkspace: "ws-other", Repository: "acme/web", Number: 3,
		URL: "https://github.com/acme/web/pull/3", State: "merged", MergedAt: &mergedAfter, ObservedAt: observed}); err != nil {
		t.Fatal(err)
	}
	family := newRun("family", nil)
	member := child(family, "family-kid", domain.RunRunning)
	mail(family, member, time.Now().Add(-5*time.Minute))
	read(member)
	reply(quiet, member)
	lead := newRun("lead", nil)
	worker := child(lead, "worker", domain.RunRunning)
	pr(worker, 11, "open", nil)
	helper := child(worker, "helper", domain.RunRunning)
	pr(helper, 12, "open", nil)
	await(helper, worker)
	watcher := newRun("watcher", nil)
	await(watcher, worker)
	await(lead, worker)
	retained := newRun("retained", finished)
	failedCleanup := newRun("failed-cleanup", finished)
	e.runs.mu.Lock()
	e.runs.retentions = map[domain.RunID]scheduler.RetentionInfo{
		retained.ID:      {CleanupPending: true},
		failedCleanup.ID: {CleanupError: "remove container: device busy"},
	}
	e.runs.mu.Unlock()

	for _, c := range []struct {
		run     *domain.Run
		outcome string
		want    []protocol.CloseWarning
	}{
		{quiet, "", nil},
		{protected, "", []protocol.CloseWarning{{Kind: "protected", Detail: "Protected: only its owner or an admin can steer, message or close it"}}},
		{openPR, "", []protocol.CloseWarning{{Kind: "pull_request", Detail: "Pull request acme/api#7 was open, not merged, when Aether last checked 2026-11-03T18:20:00Z", URL: "https://github.com/acme/api/pull/7"}}},
		{unmerged, "", []protocol.CloseWarning{{Kind: "unmerged_work", Detail: "Commits on aether/run-unmerged may not be merged: no pull request recorded for it merged after its last commit"}}},
		{unmerged, "merged", nil},
		{stale, "", []protocol.CloseWarning{{Kind: "unmerged_work", Detail: "Commits on aether/run-stale-merge may not be merged: no pull request recorded for it merged after its last commit"}}},
		{shipped, "", nil},
		{extra, "", []protocol.CloseWarning{{Kind: "unmerged_work", Detail: "Commits on aether/run-extra-merge may not be merged: no pull request recorded for it merged after its last commit"}}},
		{mailed, "", []protocol.CloseWarning{{Kind: "messages", Count: 2, Detail: "2 messages from other sessions awaiting it or sent in the last 30 minutes"}}},
		{member, "", nil},
		{awaited, "", []protocol.CloseWarning{{Kind: "awaited", RunIDs: []string{string(coordinator.ID)},
			Detail: "Waiting for it to finish: " + string(coordinator.ID) + " (coordinator)"}}},
		{parent, "", []protocol.CloseWarning{{Kind: "subsessions", Count: 1, RunIDs: []string{string(kid.ID)},
			Detail: "1 subsession close with it: " + string(kid.ID) + " (kid)"}}},
		{lead, "", []protocol.CloseWarning{
			{Kind: "subsessions", Count: 2, RunIDs: []string{string(worker.ID), string(helper.ID)},
				Detail: "2 subsessions close with it: " + string(worker.ID) + " (worker), " + string(helper.ID) + " (helper)"},
			{Kind: "pull_request", Subsession: string(worker.ID), URL: "https://github.com/acme/api/pull/11",
				Detail: "Subsession " + string(worker.ID) + " (worker): Pull request acme/api#11 was open, not merged, when Aether last checked 2026-11-03T18:20:00Z"},
			{Kind: "awaited", Subsession: string(worker.ID), RunIDs: []string{string(watcher.ID)},
				Detail: "Subsession " + string(worker.ID) + " (worker): Waiting for it to finish: " + string(watcher.ID) + " (watcher)"},
			{Kind: "pull_request", Subsession: string(helper.ID), URL: "https://github.com/acme/api/pull/12",
				Detail: "Subsession " + string(helper.ID) + " (helper): Pull request acme/api#12 was open, not merged, when Aether last checked 2026-11-03T18:20:00Z"},
		}},
		{retained, "", []protocol.CloseWarning{{Kind: "cleanup_pending", Detail: "Cleanup of its container and checkout is still pending"}}},
		{failedCleanup, "", []protocol.CloseWarning{{Kind: "cleanup_pending", Detail: "Cleanup failed: remove container: device busy"}}},
	} {
		t.Run(c.run.Title+"/"+c.outcome, func(t *testing.T) {
			var res protocol.RunCloseCheckResult
			if err := controlClient(t, e).Call(protocol.MethodRunCloseCheck, protocol.RunCloseCheckParams{RunIDs: []string{string(c.run.ID)}, Outcome: c.outcome}, &res); err != nil {
				t.Fatal(err)
			}
			want := c.want
			if want == nil {
				want = []protocol.CloseWarning{}
			}
			if len(res.Runs) != 1 || res.Runs[0].RunID != string(c.run.ID) || res.Runs[0].Error != nil || !reflect.DeepEqual(res.Runs[0].Warnings, want) {
				t.Errorf("run.close_check = %+v, want warnings %+v", res.Runs, want)
			}
		})
	}
}

// run.close_check reads like run.get: any approved member checks any run,
// even another's protected one; an id that names no run fails alone.
func TestRunCloseCheckAuthorization(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	if err := e.store.SetRunProtected(context.Background(), e.run.ID, true); err != nil {
		t.Fatal(err)
	}
	bob, _ := addMember(t, e, "Bob", domain.RoleCollaborator, false)
	c := controlAs(t, e, bob)
	var res protocol.RunCloseCheckResult
	if err := c.Call(protocol.MethodRunCloseCheck, protocol.RunCloseCheckParams{RunIDs: []string{string(e.run.ID), "run-missing", string(e.run.ID)}}, &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Runs) != 2 || res.Runs[0].Error != nil || len(res.Runs[0].Warnings) != 1 || res.Runs[0].Warnings[0].Kind != protocol.CloseWarningProtected {
		t.Fatalf("a collaborator's check of a protected run = %+v", res.Runs)
	}
	if got := res.Runs[1]; got.Error == nil || got.Error.Code != protocol.CodeNotFound || len(got.Warnings) != 0 {
		t.Fatalf("a check of a missing run = %+v, want a not-found error for it alone", got)
	}

	pending, _ := addMember(t, e, "Pat", domain.RoleCollaborator, true)
	var pe *protocol.Error
	if err := controlAs(t, e, pending).Call(protocol.MethodRunCloseCheck, protocol.RunCloseCheckParams{RunIDs: []string{string(e.run.ID)}}, nil); !errors.As(err, &pe) || pe.Code != protocol.CodeDenied {
		t.Fatalf("a pending member's check = %v, want CodeDenied", err)
	}
	ids := make([]string, protocol.MaxCloseCheckRuns+1)
	for i := range ids {
		ids[i] = string(e.run.ID)
	}
	if err := c.Call(protocol.MethodRunCloseCheck, protocol.RunCloseCheckParams{RunIDs: ids}, nil); !errors.As(err, &pe) || pe.Code != protocol.CodeInvalidParams {
		t.Fatalf("a check of %d runs = %v, want CodeInvalidParams", len(ids), err)
	}
}
