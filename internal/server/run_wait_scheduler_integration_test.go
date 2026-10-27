//go:build integration

package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
	"github.com/3xDevOps/Aether/internal/watch"
)

// An agent reports waiting --until through its coordination socket, and the
// scheduler judges the wait by the conditions the watch service keeps, as
// run.wait.changed tells it: a pending pull request checks condition keeps
// the wait past any stall, a newer report's conditions replace it, another
// report cancels them so they back nothing, and a resolved one whose wake
// cannot be written breaks the wait. When one condition resolves the
// report's others are cancelled, so no second one is left to wake the run. The server's pull request source and
// run inboxes are stand-ins, set before it starts.
func TestIntegrationTheSchedulerJudgesWaitsByTheirConditions(t *testing.T) {
	requireBinary(t, "git")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// Long enough for the report to arrive before the run stalls: the fake
	// agent never ends its turn, and a report takes effect in its turn.
	const stall = 5 * time.Second
	dataDir, err := os.MkdirTemp("", "ac")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dataDir) })
	srv, err := New(ctx, Config{
		DataDir: dataDir, Addr: "127.0.0.1:0", Runtime: newE2ERuntime(),
		RunContainerTTL: -time.Second, StallThreshold: stall, PollInterval: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	pr := &pullRequest{}
	for _, n := range srv.services {
		if w, ok := n.svc.(*watch.Service); ok {
			w.UsePRSource(pr)
			w.UseRunNotifier(fullInbox{})
		}
	}
	keyPath, signer := writeClientKey(t)
	member := &domain.Member{DisplayName: "Ada", PublicKey: string(ssh.MarshalAuthorizedKey(signer.PublicKey())), Color: "#e6194b", Role: domain.RoleAdmin}
	ws := &domain.Workspace{Name: "conditions", BaseBranch: domain.DefaultBaseBranch}
	if err = srv.Store().CreateMember(ctx, member); err != nil {
		t.Fatal(err)
	}
	if err = srv.Store().CreateWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}
	runCtx, stop := context.WithCancel(ctx)
	runDone := make(chan error, 1)
	go func() { runDone <- srv.Run(runCtx) }()
	t.Cleanup(func() {
		stop()
		<-runDone
	})
	addr := waitSSHAddr(t, srv)
	seedDir := t.TempDir()
	gitEnv := append(os.Environ(), "GIT_SSH_COMMAND=ssh -i "+keyPath+
		" -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o BatchMode=yes")
	runGit(t, seedDir, gitEnv, "init", "-q", "-b", "main")
	runGit(t, seedDir, gitEnv, "config", "user.name", "E2E")
	runGit(t, seedDir, gitEnv, "config", "user.email", "e2e@localhost")
	runGit(t, seedDir, gitEnv, "config", "commit.gpgsign", "false")
	writeFile(t, filepath.Join(seedDir, "agent.sh"), agentScript)
	runGit(t, seedDir, gitEnv, "add", "-A")
	runGit(t, seedDir, gitEnv, "commit", "-q", "-m", "seed")
	runGit(t, seedDir, gitEnv, "push", "-q", fmt.Sprintf("ssh://aether@%s/%s.git", addr, ws.ID), "main")

	t.Setenv("AETHER_FAKE_AGENT", "sh /workspace/agent.sh")
	ctrl := openControl(t, dialSSH(t, addr, signer))
	launch := func(task string) *domain.Run {
		t.Helper()
		var launched protocol.RunResult
		if callErr := ctrl.Call(protocol.MethodRunLaunch, protocol.RunLaunchParams{
			WorkspaceID: string(ws.ID), Task: task, Harness: "fake", Mode: string(domain.LaunchTUI),
		}, &launched); callErr != nil {
			t.Fatalf("run.launch: %v", callErr)
		}
		run, getErr := srv.Store().GetRun(ctx, domain.RunID(launched.Run.ID))
		if getErr != nil {
			t.Fatal(getErr)
		}
		return run
	}
	owner, target := launch("wait on the build"), launch("build")
	until := func(what string, ok func(protocol.Run) bool) protocol.Run {
		t.Helper()
		deadline := time.Now().Add(45 * time.Second)
		for {
			var got protocol.RunResult
			if callErr := ctrl.Call(protocol.MethodRunGet, protocol.RunIDParams{RunID: string(owner.ID)}, &got); callErr != nil {
				t.Fatalf("run.get: %v", callErr)
			}
			if ok(got.Run) {
				return got.Run
			}
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s; run %s %s by %s %q wait %+v conditions %+v", what,
					got.Run.Status, got.Run.Activity, got.Run.Cause, got.Run.Reason, got.Run.Wait, got.Run.WaitConditions)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	db := srv.Store().(*store.DB)
	socket := waitMissionSocket(t, filepath.Join(dataDir, "coord", string(owner.ID)))
	report := func(key, outcome string, until ...string) {
		t.Helper()
		if callErr := pacedCall(ctx, socket, protocol.MethodCoordReport, protocol.CoordReportParams{
			Outcome: outcome, WaitingFor: map[bool]string{true: protocol.CoordWaitingForCI}[outcome == protocol.CoordOutcomeWaiting],
			Summary: "the build", IdempotencyKey: key, Until: until,
		}, nil); callErr != nil {
			t.Fatalf("coord.report %s %v: %v", outcome, until, callErr)
		}
	}
	const checks = "pr:acme/app#7:checks"

	report("wait-1", protocol.CoordOutcomeWaiting, checks)
	until("the wait judged by its pending checks", func(r protocol.Run) bool {
		return r.Activity == string(domain.ActivityWaitingCI) && r.Wait != nil && r.Wait.Kind == domain.WaitCondition &&
			strings.HasPrefix(r.Wait.Detail, checks)
	})
	// A wait nothing checks goes stale after three stall thresholds.
	time.Sleep(4 * stall)
	run := until("the wait still checked", func(protocol.Run) bool { return true })
	if run.Activity != string(domain.ActivityWaitingCI) || run.Cause != string(events.CauseReport) || run.Wait == nil || run.Wait.Kind != domain.WaitCondition {
		t.Fatalf("checked wait after the quiet spell = %s by %s wait %+v", run.Activity, run.Cause, run.Wait)
	}

	runSpec := "run:" + string(target.ID) + ":finished"
	report("wait-2", protocol.CoordOutcomeWaiting, runSpec)
	until("the newer report's condition in place of the checks", func(r protocol.Run) bool {
		return r.Wait != nil && r.Wait.Kind == domain.WaitCondition && strings.HasPrefix(r.Wait.Detail, runSpec) &&
			len(r.WaitConditions) == 1 && r.WaitConditions[0].Spec == runSpec
	})

	report("answered-1", protocol.CoordOutcomeAnswered)
	until("the cancelled condition backing nothing", func(r protocol.Run) bool {
		return (r.Wait == nil || r.Wait.Kind == domain.WaitUnverified) && len(r.WaitConditions) == 1 && r.WaitConditions[0].State == protocol.WaitCancelled
	})

	report("wait-3", protocol.CoordOutcomeWaiting, runSpec)
	until("the run wait again", func(r protocol.Run) bool { return r.Wait != nil && r.Wait.Kind == domain.WaitCondition })
	if err = srv.sched.Kill(ctx, target.ID, member.ID); err != nil {
		t.Fatalf("kill %s: %v", target.ID, err)
	}
	var w *store.RunWait
	for deadline := time.Now().Add(45 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		waits, listErr := db.RunWaitConditions(ctx, owner.ID)
		if listErr != nil {
			t.Fatal(listErr)
		}
		if len(waits) == 1 && waits[0].WaitState == protocol.WaitResolved && waits[0].DeliveryError != "" {
			w = waits[0]
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("waits = %+v, want the run wait resolved with its wake failing", waits)
		}
	}
	// #604 calls a wake broken once it is 5 minutes late; the resolution is
	// moved back rather than waited out, and announced as the service would.
	resolved, later := time.Now().Add(-10*time.Minute), time.Now().Add(time.Hour)
	w.ResolvedAt, w.DeliveryNextAt = &resolved, &later
	if err = db.SetRunWaitDelivery(ctx, w, store.WatchDelivery{Channel: protocol.WatchChannelRunInbox, Status: protocol.WatchStatusPending, At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err = srv.bus.Publish(ctx, events.Event{WorkspaceID: owner.WorkspaceID, RunID: owner.ID,
		Payload: events.RunWaitChangedPayload{RunID: owner.ID, WorkspaceID: owner.WorkspaceID, WaitID: w.ID, State: w.WaitState}}); err != nil {
		t.Fatal(err)
	}
	run = until("the broken wait", func(r protocol.Run) bool { return r.Activity == string(domain.ActivityNeedsYou) })
	if run.Cause != string(events.CauseWaitFault) || run.Wait == nil || run.Wait.Kind != domain.WaitFault || run.Wait.Error != "coord: inbox write: disk full" {
		t.Fatalf("broken wait = %s by %s %q wait %+v", run.Activity, run.Cause, run.Reason, run.Wait)
	}
}
