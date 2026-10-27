//go:build integration

package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

// The scheduler judges a reported wait by the conditions the watch service
// keeps, read from the store from its first judgement: a pending, freshly
// read condition keeps the wait past any stall; cancelled conditions back
// nothing; and a resolved one whose wake stays undelivered breaks it.
func TestIntegrationTheSchedulerJudgesWaitsByTheirConditions(t *testing.T) {
	requireBinary(t, "git")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	const stall = 200 * time.Millisecond
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
	register := func(key string) {
		t.Helper()
		spec := "run:" + string(target.ID) + ":finished"
		now := time.Now()
		w := &store.RunWait{
			Watch: store.Watch{MemberID: owner.MemberID, WorkspaceID: owner.WorkspaceID, TargetKind: protocol.WatchTargetRun,
				TargetTitle: spec, Conditions: []string{protocol.WaitKindRunFinished}, Source: protocol.WatchSourceAgent,
				CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour), NextCheckAt: &now},
			Spec: spec, Kind: protocol.WaitKindRunFinished, TargetRun: target.ID,
		}
		if _, regErr := db.RecordWaitingReport(ctx, &store.CoordStateReport{RunID: owner.ID, IdempotencyKey: key,
			Outcome: protocol.CoordOutcomeWaiting, WaitingFor: protocol.CoordWaitingForAgents, Summary: "the build", Until: []string{spec}}, []*store.RunWait{w}); regErr != nil {
			t.Fatal(regErr)
		}
		if regErr := srv.sched.ReportWaiting(ctx, owner.ID, key, protocol.CoordWaitingForAgents, "the build", time.Now()); regErr != nil {
			t.Fatal(regErr)
		}
		if regErr := db.MarkCoordStateReportApplied(ctx, owner.ID, key); regErr != nil {
			t.Fatal(regErr)
		}
	}

	register("wait-1")
	run := until("the wait judged by its condition", func(r protocol.Run) bool {
		return r.Activity == string(domain.ActivityWaitingAgents) && r.Wait != nil && r.Wait.Kind == domain.WaitCondition
	})
	// Ten stall thresholds: a wait nothing checks goes stale after three.
	time.Sleep(10 * stall)
	run = until("the wait still checked", func(r protocol.Run) bool { return true })
	if run.Activity != string(domain.ActivityWaitingAgents) || run.Cause != string(events.CauseReport) || run.Wait == nil || run.Wait.Kind != domain.WaitCondition {
		t.Fatalf("checked wait after the quiet spell = %s by %s wait %+v", run.Activity, run.Cause, run.Wait)
	}

	if _, err = db.EndRunWaits(ctx, owner.ID, "", protocol.WaitCancelled, "the run reported answered", "", time.Time{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err = srv.sched.WaitChanged(ctx, owner.ID); err != nil {
		t.Fatal(err)
	}
	until("the cancelled condition backing nothing", func(r protocol.Run) bool {
		return r.Wait == nil || r.Wait.Kind == domain.WaitUnverified
	})

	register("wait-2")
	until("the new wait judged by its condition", func(r protocol.Run) bool { return r.Wait != nil && r.Wait.Kind == domain.WaitCondition })
	waits, err := db.RunWaitConditions(ctx, owner.ID)
	if err != nil || len(waits) != 1 {
		t.Fatalf("waits = %+v, %v", waits, err)
	}
	w := waits[0]
	resolved, later := time.Now().Add(-10*time.Minute), time.Now().Add(time.Hour)
	w.WaitState, w.State, w.ResolvedAt, w.EndedAt, w.NextCheckAt = protocol.WaitResolved, protocol.WatchDone, &resolved, &resolved, nil
	w.DeliveredAt, w.DeliveryNextAt, w.DeliveryError, w.DeliveryAttempts = nil, &later, "coord: inbox write: disk full", 3
	if err = db.SetRunWaitDelivery(ctx, w, store.WatchDelivery{Channel: protocol.WatchChannelRunInbox, Status: protocol.WatchStatusPending, At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err = srv.sched.WaitChanged(ctx, owner.ID); err != nil {
		t.Fatal(err)
	}
	run = until("the broken wait", func(r protocol.Run) bool { return r.Activity == string(domain.ActivityNeedsYou) })
	if run.Cause != string(events.CauseWaitFault) || run.Wait == nil || run.Wait.Kind != domain.WaitFault || run.Wait.Error != "coord: inbox write: disk full" {
		t.Fatalf("broken wait = %s by %s %q wait %+v", run.Activity, run.Cause, run.Reason, run.Wait)
	}
}
