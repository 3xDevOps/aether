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
	"github.com/3xDevOps/Aether/internal/scheduler"
)

// profileAgentScript is the profile-lifecycle fake agent: it records what
// its container's harness config directory actually holds - the synced
// profile and the persisted login state - into the worktree, so the run
// branch is the evidence. Task "wait" parks on stdin first, which is how
// the mid-run-push scenario reads its profile after a newer snapshot was
// pushed.
const profileAgentScript = `sleep 1
echo agent-ready
case "$1" in
wait) read line ;;
esac
ls "$HOME/.claude" > claude-ls.txt 2>/dev/null
cat "$HOME/.claude/skill.md" > skill-seen.txt 2>/dev/null
cat "$HOME/.claude/.credentials.json" > cred-seen.txt 2>/dev/null
`

// TestIntegrationProfileSyncAndLogins is the profile-sync lifecycle over
// the wired server: a profile push is materialized into the member's
// persistent home, a pushed profile edit reaches every run using that home,
// and a push carrying a denylisted credential name is refused. It drives a
// registered harness ("claude" - profile root and credential paths intact)
// whose argv is overridden to the scripted agent, and needs a real shell
// in the container, so it runs against Docker only.
func TestIntegrationProfileSyncAndLogins(t *testing.T) {
	requireBinary(t, "git")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	rt, _, verifyNoLeaks := pickRuntime(t)
	if _, fallback := rt.(*e2eRuntime); fallback {
		t.Skip("the profile lifecycle needs a real shell in the container; Docker daemon unreachable")
	}
	dataDir := filepath.Join(t.TempDir(), "data")
	srv, err := New(ctx, Config{
		DataDir: dataDir, Addr: "127.0.0.1:0", Runtime: rt,
		Harnesses: map[string]scheduler.HarnessSpec{
			"claude": {TUIArgs: []string{"sh", "/workspace/agent.sh", "{task}"}},
		},
	})
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}

	keyPath, signer := writeClientKey(t)
	member := &domain.Member{
		DisplayName: "Prof Tester",
		PublicKey:   string(ssh.MarshalAuthorizedKey(signer.PublicKey())),
		Color:       "#4363d8",
		Role:        domain.RoleAdmin,
	}
	ws := &domain.Workspace{
		Name:        "prof",
		Environment: domain.WorkspaceEnvironment{},
		BaseBranch:  domain.DefaultBaseBranch,
	}
	if err = srv.Store().CreateMember(ctx, member); err != nil {
		t.Fatalf("seed member: %v", err)
	}
	if err = srv.Store().CreateWorkspace(ctx, ws); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}

	sub, err := srv.Bus().Subscribe(ctx, events.SubscribeOptions{Buffer: 4096})
	if err != nil {
		t.Fatalf("subscribe bus: %v", err)
	}
	defer func() { _ = sub.Close() }()
	var seen []events.Event

	runDone := make(chan error, 1)
	runCtx, stopServer := context.WithCancel(ctx)
	defer stopServer()
	go func() { runDone <- srv.Run(runCtx) }()
	addr := waitSSHAddr(t, srv)

	seedDir := t.TempDir()
	repoURL := fmt.Sprintf("ssh://aether@%s/%s.git", addr, ws.ID)
	gitEnv := append(os.Environ(),
		"GIT_SSH_COMMAND=ssh -i "+keyPath+
			" -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o BatchMode=yes")
	runGit(t, seedDir, gitEnv, "init", "-q", "-b", "main")
	runGit(t, seedDir, gitEnv, "config", "user.name", "E2E")
	runGit(t, seedDir, gitEnv, "config", "user.email", "e2e@localhost")
	runGit(t, seedDir, gitEnv, "config", "commit.gpgsign", "false")
	writeFile(t, filepath.Join(seedDir, "agent.sh"), profileAgentScript)
	runGit(t, seedDir, gitEnv, "add", "-A")
	runGit(t, seedDir, gitEnv, "commit", "-q", "-m", "seed")
	runGit(t, seedDir, gitEnv, "push", "-q", repoURL, "main")

	client := dialSSH(t, addr, signer)
	ctrl := openControl(t, client)

	// Profile v1 goes up over the control channel; a push naming a
	// denylisted credential file is refused outright.
	pushProfile(t, ctrl, "skill-v1\n")
	memberHome := filepath.Join(dataDir, "homes", string(member.ID), ".claude")
	if err := os.MkdirAll(memberHome, 0o700); err != nil {
		t.Fatalf("create member profile home: %v", err)
	}
	if err := os.WriteFile(filepath.Join(memberHome, ".credentials.json"), []byte("tok-1"), 0o600); err != nil {
		t.Fatalf("seed member login state: %v", err)
	}
	denyErr := ctrl.Call(protocol.MethodProfilePush, protocol.ProfilePushParams{
		Harness: "claude",
		Files: []protocol.ProfileFile{
			{Path: "skill.md", Mode: 0o644, Content: []byte("skill-v1\n")},
			{Path: ".credentials.json", Mode: 0o600, Content: []byte("evil-token")},
		},
	}, nil)
	if denyErr == nil || !strings.Contains(denyErr.Error(), ".credentials.json") {
		t.Fatalf("push with a denylisted name = %v, want the refusal naming it", denyErr)
	}

	// The stored snapshot holds exactly what was pushed - no credential
	// file ever enters it.
	var status protocol.ProfileStatusResult
	if err := ctrl.Call(protocol.MethodProfileStatus, protocol.ProfileStatusParams{Harness: "claude"}, &status); err != nil {
		t.Fatalf("profile.status: %v", err)
	}
	if len(status.Files) != 1 || status.Files[0].Path != "skill.md" {
		t.Fatalf("snapshot files = %+v, want [skill.md]", status.Files)
	}

	// Run 1 uses the member's persistent home, which contains the pushed
	// profile and the seeded login state.
	run1 := launchRun(t, ctrl, string(ws.ID), "wait", "claude")
	att1 := openAttach(t, client, run1.ID)
	att1.waitOutput(t, "agent-ready")
	materialized := filepath.Join(dataDir, "homes", string(member.ID), ".claude")
	if got, rerr := os.ReadFile(filepath.Join(materialized, "skill.md")); rerr != nil || string(got) != "skill-v1\n" {
		t.Fatalf("materialized skill.md = %q (err %v), want the pushed v1", got, rerr)
	}
	if got, rerr := os.ReadFile(filepath.Join(materialized, ".credentials.json")); rerr != nil || string(got) != "tok-1" {
		t.Fatalf("member login state = %q (err %v), want the seeded token", got, rerr)
	}

	// A mid-run push updates the persistent home; the next run and the
	// running run both see the latest profile contents.
	pushProfile(t, ctrl, "skill-v2\n")

	run2 := launchRun(t, ctrl, string(ws.ID), "now", "claude")
	if run2.ProfileSnapshotID == run1.ProfileSnapshotID || run2.ProfileSnapshotID == "" {
		t.Fatalf("run 2 pinned snapshot %q, want a fresh pin distinct from run 1's %q",
			run2.ProfileSnapshotID, run1.ProfileSnapshotID)
	}
	waitRunStatus(t, sub, &seen, run2.ID, domain.RunNeedsAttention)
	got2 := fetchRunFiles(t, ctrl, seedDir, gitEnv, repoURL, run2.ID)
	if got2["skill-seen.txt"] != "skill-v2\n" {
		t.Fatalf("run 2 saw profile %q, want the pushed v2", got2["skill-seen.txt"])
	}
	if got2["cred-seen.txt"] != "tok-1" {
		t.Fatalf("run 2 saw login state %q, want the setup session's token", got2["cred-seen.txt"])
	}

	// Run 1 reads its config only now - after the v2 push - from the same
	// persistent home as run 2.
	if err := ctrl.Call(protocol.MethodRunInject, protocol.RunInjectParams{RunID: run1.ID, Message: "go"}, nil); err != nil {
		t.Fatalf("run.inject: %v", err)
	}
	waitRunStatus(t, sub, &seen, run1.ID, domain.RunNeedsAttention)
	got1 := fetchRunFiles(t, ctrl, seedDir, gitEnv, repoURL, run1.ID)
	if got1["skill-seen.txt"] != "skill-v2\n" {
		t.Fatalf("run 1 saw profile %q after the mid-run push, want current v2", got1["skill-seen.txt"])
	}
	if got1["cred-seen.txt"] != "tok-1" {
		t.Fatalf("run 1 saw login state %q, want the member home token", got1["cred-seen.txt"])
	}
	if !strings.Contains(got1["claude-ls.txt"], "skill.md") {
		t.Fatalf("run 1's config listing = %q, want the synced skill", got1["claude-ls.txt"])
	}

	stopServer()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("server.Run: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("server did not shut down")
	}
	verifyNoLeaks(t)
}

// pushProfile pushes a one-file claude profile snapshot.
func pushProfile(t *testing.T, ctrl *protocol.Client, skill string) {
	t.Helper()
	var res protocol.ProfilePushResult
	if err := ctrl.Call(protocol.MethodProfilePush, protocol.ProfilePushParams{
		Harness: "claude",
		Files:   []protocol.ProfileFile{{Path: "skill.md", Mode: 0o644, Content: []byte(skill)}},
	}, &res); err != nil {
		t.Fatalf("profile.push: %v", err)
	}
	if res.Snapshot.ID == "" {
		t.Fatalf("profile.push result = %+v", res)
	}
}

// launchRun launches a run over the control channel and checks it came up
// running.
func launchRun(t *testing.T, ctrl *protocol.Client, workspace, task, harnessName string) protocol.Run {
	t.Helper()
	var launched protocol.RunResult
	if err := ctrl.Call(protocol.MethodRunLaunch, protocol.RunLaunchParams{
		WorkspaceID: workspace, Task: task, Harness: harnessName,
	}, &launched); err != nil {
		t.Fatalf("run.launch %q: %v", task, err)
	}
	if launched.Run.Status != string(domain.RunRunning) {
		t.Fatalf("run %q status = %q, want running", task, launched.Run.Status)
	}
	return launched.Run
}

// waitRunStatus waits for one run.status transition on the bus.
func waitRunStatus(t *testing.T, sub events.Subscription, seen *[]events.Event, run string, to domain.RunStatus) {
	t.Helper()
	waitEvent(t, sub, seen, fmt.Sprintf("run %s status %s", run, to), func(e events.Event) bool {
		p, ok := e.Payload.(events.RunStatusPayload)
		return ok && string(e.RunID) == run && p.To == to
	})
}

// fetchRunFiles fetches a run's branch over the SSH git transport and
// returns the agent's recorded evidence files by name.
func fetchRunFiles(t *testing.T, ctrl *protocol.Client, dir string, env []string, repoURL, runID string) map[string]string {
	t.Helper()
	var pull protocol.RunPullResult
	if err := ctrl.Call(protocol.MethodRunPull, protocol.RunIDParams{RunID: runID}, &pull); err != nil {
		t.Fatalf("run.pull %s: %v", runID, err)
	}
	runGit(t, dir, env, "fetch", "-q", repoURL, pull.Branch)
	out := make(map[string]string, 3)
	for _, name := range []string{"skill-seen.txt", "cred-seen.txt", "claude-ls.txt"} {
		out[name] = runGit(t, dir, env, "show", "FETCH_HEAD:"+name)
	}
	return out
}
