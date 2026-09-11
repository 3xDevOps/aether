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
)

const nativeActivityAgentScript = `#!/bin/sh
sleep 1
printf '\033]0;π : native-activity\007'
while IFS= read -r command; do
	case "$command" in
	idle)
		printf '\033]0;π > native-activity\007'
		printf 'unrelated output while idle\n'
		;;
	working)
		printf '\033]0;π : native-activity\007'
		;;
	blocked)
		printf '\033]0;π ! native-activity\007'
		printf 'unrelated output while blocked\n'
		;;
	exit)
		exit 0
		;;
	esac
done
`

const nativeActivityTask = "native title activity"

func TestIntegrationNativeRunActivity(t *testing.T) {
	requireBinary(t, "git")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	rt, image, verifyNoLeaks := pickRuntime(t)
	if _, fallback := rt.(*e2eRuntime); fallback {
		t.Log("native title scenario uses the in-process e2e runtime fallback; no shell is executed")
	} else {
		t.Log("native title scenario uses a real Docker shell")
	}

	srv, err := New(ctx, Config{
		DataDir:        filepath.Join(t.TempDir(), "data"),
		Addr:           "127.0.0.1:0",
		Runtime:        rt,
		StandardImage:  image,
		StallThreshold: 1 * time.Minute,
	})
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}

	keyPath, signer := writeClientKey(t)
	member := &domain.Member{
		DisplayName: "Native Activity Tester",
		PublicKey:   string(ssh.MarshalAuthorizedKey(signer.PublicKey())),
		Color:       "#3cb44b",
		Role:        domain.RoleAdmin,
	}
	if err := srv.Store().CreateMember(ctx, member); err != nil {
		t.Fatalf("seed member: %v", err)
	}
	ws := &domain.Workspace{
		Name:        "native-activity",
		Environment: domain.WorkspaceEnvironment{},
		BaseBranch:  domain.DefaultBaseBranch,
	}
	if err := srv.Store().CreateWorkspace(ctx, ws); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}

	sub, err := srv.Bus().Subscribe(ctx, events.SubscribeOptions{Buffer: 4096})
	if err != nil {
		t.Fatalf("subscribe bus: %v", err)
	}
	defer func() { _ = sub.Close() }()
	var seen []events.Event

	runCtx, stopServer := context.WithCancel(ctx)
	runDone := make(chan error, 1)
	go func() { runDone <- srv.Run(runCtx) }()
	shutdown := sync.OnceFunc(func() {
		stopServer()
		select {
		case runErr := <-runDone:
			if runErr != nil {
				t.Errorf("server.Run: %v", runErr)
			}
		case <-time.After(30 * time.Second):
			t.Error("server did not shut down")
		}
	})
	t.Cleanup(shutdown)
	addr := waitSSHAddr(t, srv)

	seedDir := t.TempDir()
	repoURL := fmt.Sprintf("ssh://aether@%s/%s.git", addr, ws.ID)
	gitEnv := append(os.Environ(),
		"GIT_SSH_COMMAND=ssh -i "+keyPath+
			" -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o BatchMode=yes")
	runGit(t, seedDir, gitEnv, "init", "-q", "-b", "main")
	runGit(t, seedDir, gitEnv, "config", "user.name", "Native Activity E2E")
	runGit(t, seedDir, gitEnv, "config", "user.email", "native-activity@localhost")
	runGit(t, seedDir, gitEnv, "config", "commit.gpgsign", "false")
	writeFile(t, filepath.Join(seedDir, "README.md"), "# native activity seed\n")
	writeFile(t, filepath.Join(seedDir, "native-agent.sh"), nativeActivityAgentScript)
	runGit(t, seedDir, gitEnv, "add", "-A")
	runGit(t, seedDir, gitEnv, "commit", "-q", "-m", "seed")
	runGit(t, seedDir, gitEnv, "push", "-q", repoURL, "main")

	t.Setenv("AETHER_FAKE_AGENT", "sh /workspace/native-agent.sh {task}")
	if fake, ok := rt.(*e2eRuntime); ok {
		fake.script(nativeActivityTask, func(c *e2eContainer) {
			title := func(marker string) {
				c.output("\x1b]0;π " + marker + " native-activity\x07")
			}
			time.Sleep(time.Second)
			title(":")
			for {
				command, ok := c.readStdinLine()
				if !ok {
					return
				}
				switch command {
				case "idle":
					title(">")
					c.output("unrelated output while idle\r\n")
				case "working":
					title(":")
				case "blocked":
					title("!")
					c.output("unrelated output while blocked\r\n")
				case "exit":
					c.exitNow(0)
					return
				}
			}
		})
	}

	client := dialSSH(t, addr, signer)
	ctrl := openControl(t, client)
	var launched protocol.RunResult
	if err := ctrl.Call(protocol.MethodRunLaunch, protocol.RunLaunchParams{
		WorkspaceID: string(ws.ID),
		Task:        nativeActivityTask,
		Harness:     "fake",
	}, &launched); err != nil {
		t.Fatalf("run.launch: %v", err)
	}
	runID := domain.RunID(launched.Run.ID)
	if launched.Run.Status != string(domain.RunRunning) {
		t.Fatalf("run.launch status = %q, want running", launched.Run.Status)
	}

	att := openAttach(t, client, launched.Run.ID)
	inject := func(message string) {
		if err := ctrl.Call(protocol.MethodRunInject, protocol.RunInjectParams{
			RunID: string(runID), Message: message,
		}, nil); err != nil {
			t.Fatalf("run.inject %q: %v", message, err)
		}
	}
	inject("idle")

	statusEvent := func(from, to domain.RunStatus) func(events.Event) bool {
		return func(e events.Event) bool {
			p, ok := e.Payload.(events.RunStatusPayload)
			return ok && e.RunID == runID && p.From == from && p.To == to
		}
	}
	waitEvent(t, sub, &seen, "native idle needs-attention", func(e events.Event) bool {
		p, ok := e.Payload.(events.RunStatusPayload)
		if !ok || e.RunID != runID || p.From != domain.RunRunning || p.To != domain.RunNeedsAttention {
			return false
		}
		reason := strings.ToLower(p.Reason)
		return strings.Contains(reason, "input") && !strings.Contains(reason, "blocked")
	})

	assertNativeRunStatus(t, ctrl, runID, domain.RunNeedsAttention)
	assertNativeListedStatus(t, ctrl, runID, domain.RunNeedsAttention, true)

	assertNativeNoRunningEvent(t, sub, &seen, runID, 1*time.Second)
	assertNativeRunStatus(t, ctrl, runID, domain.RunNeedsAttention)
	inject("working")

	waitEvent(t, sub, &seen, "native working resume", statusEvent(
		domain.RunNeedsAttention, domain.RunRunning))
	inject("blocked")
	waitEvent(t, sub, &seen, "native blocked needs-attention", func(e events.Event) bool {
		p, ok := e.Payload.(events.RunStatusPayload)
		return ok && e.RunID == runID && p.From == domain.RunRunning &&
			p.To == domain.RunNeedsAttention &&
			strings.Contains(strings.ToLower(p.Reason), "blocked")
	})
	assertNativeRunStatus(t, ctrl, runID, domain.RunNeedsAttention)
	assertNativeListedStatus(t, ctrl, runID, domain.RunNeedsAttention, true)
	assertNativeNoRunningEvent(t, sub, &seen, runID, 1*time.Second)
	seen = nil // The next resume must follow this blocker, not the earlier idle.
	inject("working")

	waitEvent(t, sub, &seen, "native working after blocked", statusEvent(
		domain.RunNeedsAttention, domain.RunRunning))
	inject("exit")
	waitEvent(t, sub, &seen, "native clean completion", func(e events.Event) bool {
		p, ok := e.Payload.(events.RunStatusPayload)
		return ok && e.RunID == runID && p.From == domain.RunRunning && p.To == domain.RunCompleted
	})

	assertNativeRunStatus(t, ctrl, runID, domain.RunCompleted)
	assertNativeListedStatus(t, ctrl, runID, domain.RunCompleted, false)
	assertNativeNotListed(t, ctrl, runID, true)
	att.waitEnd(t)
	for _, title := range []string{"π : native-activity", "π > native-activity", "π ! native-activity"} {
		if !strings.Contains(att.output(), title) {
			t.Fatalf("attach stream missing native OMP title %q; output %q", title, att.output())
		}
	}

	shutdown()
	verifyNoLeaks(t)
}

func assertNativeRunStatus(t *testing.T, ctrl *protocol.Client, runID domain.RunID, want domain.RunStatus) {
	t.Helper()
	var result protocol.RunResult
	if err := ctrl.Call(protocol.MethodRunGet, protocol.RunIDParams{RunID: string(runID)}, &result); err != nil {
		t.Fatalf("run.get %s: %v", runID, err)
	}
	if got := domain.RunStatus(result.Run.Status); got != want {
		t.Fatalf("run.get %s status = %q, want %q", runID, got, want)
	}
}

func assertNativeListedStatus(t *testing.T, ctrl *protocol.Client, runID domain.RunID, want domain.RunStatus, activeOnly bool) {
	t.Helper()
	var result protocol.RunListResult
	if err := ctrl.Call(protocol.MethodRunList, protocol.RunListParams{ActiveOnly: activeOnly}, &result); err != nil {
		t.Fatalf("run.list: %v", err)
	}
	for _, run := range result.Runs {
		if run.ID == string(runID) {
			if run.Status != string(want) {
				t.Fatalf("run.list %s status = %q, want %q", runID, run.Status, want)
			}
			return
		}
	}
	t.Fatalf("run.list omitted %s (active_only=%t)", runID, activeOnly)
}

func assertNativeNotListed(t *testing.T, ctrl *protocol.Client, runID domain.RunID, activeOnly bool) {
	t.Helper()
	var result protocol.RunListResult
	if err := ctrl.Call(protocol.MethodRunList, protocol.RunListParams{ActiveOnly: activeOnly}, &result); err != nil {
		t.Fatalf("run.list: %v", err)
	}
	for _, run := range result.Runs {
		if run.ID == string(runID) {
			t.Fatalf("run.list included terminal run %s (active_only=%t)", runID, activeOnly)
		}
	}
}

func assertNativeNoRunningEvent(t *testing.T, sub events.Subscription, seen *[]events.Event, runID domain.RunID, window time.Duration) {
	t.Helper()
	timer := time.NewTimer(window)
	defer timer.Stop()
	for {
		select {
		case e, ok := <-sub.Events():
			if !ok {
				t.Fatalf("bus subscription closed watching run %s: %v", runID, sub.Err())
			}
			*seen = append(*seen, e)
			p, isStatus := e.Payload.(events.RunStatusPayload)
			if isStatus && e.RunID == runID && p.From == domain.RunNeedsAttention && p.To == domain.RunRunning {
				t.Fatalf("unrelated output cleared native waiting state: %s -> %s (%q)", p.From, p.To, p.Reason)
			}
		case <-timer.C:
			return
		}
	}
}
