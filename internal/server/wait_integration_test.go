//go:build integration

package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/push"
	"github.com/3xDevOps/Aether/internal/scheduler"
)

// serverWaits stands in for the run-wait condition store behind a real
// server.
type serverWaits struct {
	mu         sync.Mutex
	conditions []scheduler.WaitCondition
}

func (w *serverWaits) RunWaits(context.Context, domain.RunID) ([]scheduler.WaitCondition, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]scheduler.WaitCondition(nil), w.conditions...), nil
}

func (w *serverWaits) set(conditions ...scheduler.WaitCondition) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.conditions = conditions
}

// TestIntegrationAConditionDecidesWhetherAWaitNeedsYou drives one run
// through a real server: a CI wait with a checked condition stays quiet
// long past the point an unchecked wait goes stale, and the same wait whose
// condition resolved without waking the agent needs its owner, in run.get,
// attention.list and the push body alike.
func TestIntegrationAConditionDecidesWhetherAWaitNeedsYou(t *testing.T) {
	requireBinary(t, "git")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	const stall = 200 * time.Millisecond
	waits := &serverWaits{}
	// The run's coordination socket path must fit in 107 bytes.
	dataDir, err := os.MkdirTemp("", "aw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dataDir) })
	srv, err := New(ctx, Config{
		DataDir: dataDir, Addr: "127.0.0.1:0", Runtime: newE2ERuntime(),
		RunContainerTTL: -time.Second, StallThreshold: stall, PollInterval: 50 * time.Millisecond,
		WaitConditions: waits,
	})
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	keyPath, signer := writeClientKey(t)
	member := &domain.Member{DisplayName: "Ada", PublicKey: string(ssh.MarshalAuthorizedKey(signer.PublicKey())), Color: "#e6194b", Role: domain.RoleAdmin}
	ws := &domain.Workspace{Name: "waits", BaseBranch: domain.DefaultBaseBranch}
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
	var launched protocol.RunResult
	if err = ctrl.Call(protocol.MethodRunLaunch, protocol.RunLaunchParams{
		WorkspaceID: string(ws.ID), Task: "wait on CI", Harness: "fake", Mode: string(domain.LaunchTUI),
	}, &launched); err != nil {
		t.Fatalf("run.launch: %v", err)
	}
	runID := domain.RunID(launched.Run.ID)

	read := func() (protocol.Run, []protocol.AttentionItem) {
		t.Helper()
		var got protocol.RunResult
		if callErr := ctrl.Call(protocol.MethodRunGet, protocol.RunIDParams{RunID: string(runID)}, &got); callErr != nil {
			t.Fatalf("run.get: %v", callErr)
		}
		var attention protocol.AttentionListResult
		if callErr := ctrl.Call(protocol.MethodAttentionList, protocol.AttentionListParams{WorkspaceID: string(ws.ID)}, &attention); callErr != nil {
			t.Fatalf("attention.list: %v", callErr)
		}
		return got.Run, attention.Items
	}
	until := func(what string, ok func(protocol.Run) bool) (protocol.Run, []protocol.AttentionItem) {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for {
			run, items := read()
			if ok(run) {
				return run, items
			}
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s; run %s %s by %s %q wait %+v", what, run.Status, run.Activity, run.Cause, run.Reason, run.Wait)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	pushBody := func() string {
		t.Helper()
		row, getErr := srv.Store().GetRun(ctx, runID)
		if getErr != nil {
			t.Fatal(getErr)
		}
		return push.AttentionBody(row)
	}

	now := time.Now().UTC()
	waits.set(scheduler.WaitCondition{Spec: "pr:o/r#12:checks", State: "pending", Summary: "open, 3 checks running",
		LastCheckedAt: now, SourceCheckedAt: now, NextCheckAt: now.Add(time.Hour)})
	if err = srv.sched.ReportWaiting(ctx, runID, "wait-ci", protocol.CoordWaitingForCI, "CI on #12", time.Now()); err != nil {
		t.Fatal(err)
	}
	run, _ := until("the CI wait parked", func(r protocol.Run) bool { return r.Activity == string(domain.ActivityWaitingCI) })
	if run.Cause != string(events.CauseReport) || run.Wait == nil || run.Wait.Kind != domain.WaitCondition ||
		!strings.HasPrefix(run.Wait.Text(time.Now()), "pr:o/r#12:checks: open, 3 checks running (checked ") {
		t.Fatalf("parked CI wait = %s by %s wait %+v", run.Activity, run.Cause, run.Wait)
	}
	// Ten stall thresholds: an unchecked wait goes stale after three.
	time.Sleep(10 * stall)
	run, items := read()
	if run.Activity != string(domain.ActivityWaitingCI) || run.Cause != string(events.CauseReport) || run.Reason != "CI on #12" ||
		run.Wait == nil || run.Wait.Kind != domain.WaitCondition {
		t.Fatalf("checked CI wait after the quiet spell = %s by %s %q wait %+v", run.Activity, run.Cause, run.Reason, run.Wait)
	}
	for _, item := range items {
		if item.RunID == string(runID) {
			t.Fatalf("a checked wait is in Needs you: %+v", item)
		}
	}
	if body := pushBody(); body != "" {
		t.Fatalf("a checked wait would push %q", body)
	}

	resolved := now.Add(-10 * time.Minute)
	waits.set(scheduler.WaitCondition{Spec: "pr:o/r#12:checks", State: "resolved", Summary: "all 3 checks passed",
		LastCheckedAt: resolved, SourceCheckedAt: resolved, NextCheckAt: resolved.Add(time.Minute), ResolvedAt: resolved,
		LastDeliveryError: "coord: inbox write: disk full"})
	const broken = "wait broken: pr:o/r#12:checks resolved, but the agent was not woken"
	run, items = until("the broken wait", func(r protocol.Run) bool { return r.Activity == string(domain.ActivityNeedsYou) })
	if run.Cause != string(events.CauseWaitFault) || run.Reason != broken || run.Wait == nil ||
		run.Wait.Kind != domain.WaitFault || run.Wait.Error != "coord: inbox write: disk full" {
		t.Fatalf("broken wait = %s by %s %q wait %+v", run.Activity, run.Cause, run.Reason, run.Wait)
	}
	listed := false
	for _, item := range items {
		if item.RunID == string(runID) {
			listed = item.Reason == "Wait broken: pr:o/r#12:checks resolved, but the agent was not woken · reply to continue"
		}
	}
	if !listed {
		t.Fatalf("attention.list = %+v, want the broken wait", items)
	}
	if body := pushBody(); body != "Wait broken, send a message to continue: pr:o/r#12:checks resolved, but the agent was not woken" {
		t.Fatalf("push body = %q", body)
	}
}
