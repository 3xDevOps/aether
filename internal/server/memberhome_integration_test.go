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
	"github.com/3xDevOps/Aether/internal/scheduler"
)

// homeAgentScript is the member-home fake agent: task "write" drops a
// marker into the container's $HOME, task "read" records whether the
// marker is visible, into the worktree so the run branch is the evidence.
const homeAgentScript = `sleep 1
echo agent-ready
case "$1" in
write)
  mkdir -p "$HOME/.local/bin"
  printf 'marker-v1\n' > "$HOME/.local/bin/marker.txt"
  # The container runs as root; loosen modes so the host test user can
  # remove the temp data dir during cleanup.
  chmod -R a+rwX "$HOME/.local"
  ;;
read)
  cat "$HOME/.local/bin/marker.txt" > marker-seen.txt 2>/dev/null || printf 'absent\n' > marker-seen.txt
  ;;
esac
`

// TestMemberHomePersistsAcrossContainers proves the one-home-per-member
// model over the wired server: a file written under $HOME in one run
// container is visible in the same member's next run container, and is
// never visible in another member's run. Real bind mounts are the point,
// so it runs against Docker only.
func TestMemberHomePersistsAcrossContainers(t *testing.T) {
	requireBinary(t, "git")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	rt, _, verifyNoLeaks := pickRuntime(t)
	if _, fallback := rt.(*e2eRuntime); fallback {
		t.Skip("home persistence needs real bind mounts; Docker daemon unreachable")
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

	keyA, signerA := writeClientKey(t)
	memberA := &domain.Member{
		DisplayName: "Home Owner",
		PublicKey:   string(ssh.MarshalAuthorizedKey(signerA.PublicKey())),
		Color:       "#4363d8",
		Role:        domain.RoleAdmin,
	}
	_, signerB := writeClientKey(t)
	memberB := &domain.Member{
		DisplayName: "Other Member",
		PublicKey:   string(ssh.MarshalAuthorizedKey(signerB.PublicKey())),
		Color:       "#e6194b",
		Role:        domain.RoleCollaborator,
	}
	ws := &domain.Workspace{
		Name:        "homes",
		Environment: domain.WorkspaceEnvironment{},
		BaseBranch:  domain.DefaultBaseBranch,
	}
	for _, m := range []*domain.Member{memberA, memberB} {
		if err = srv.Store().CreateMember(ctx, m); err != nil {
			t.Fatalf("seed member: %v", err)
		}
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
		"GIT_SSH_COMMAND=ssh -i "+keyA+
			" -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o BatchMode=yes")
	runGit(t, seedDir, gitEnv, "init", "-q", "-b", "main")
	runGit(t, seedDir, gitEnv, "config", "user.name", "E2E")
	runGit(t, seedDir, gitEnv, "config", "user.email", "e2e@localhost")
	runGit(t, seedDir, gitEnv, "config", "commit.gpgsign", "false")
	writeFile(t, filepath.Join(seedDir, "agent.sh"), homeAgentScript)
	runGit(t, seedDir, gitEnv, "add", "-A")
	runGit(t, seedDir, gitEnv, "commit", "-q", "-m", "seed")
	runGit(t, seedDir, gitEnv, "push", "-q", repoURL, "main")

	ctrlA := openControl(t, dialSSH(t, addr, signerA))
	ctrlB := openControl(t, dialSSH(t, addr, signerB))

	// Run 1 (member A) writes the marker into $HOME; the write lands in
	// the member's persistent home on the host.
	write := launchRun(t, ctrlA, string(ws.ID), "write", "claude")
	waitRunStatus(t, sub, &seen, write.ID, domain.RunNeedsAttention)
	hostMarker := filepath.Join(dataDir, "homes", string(memberA.ID), ".local", "bin", "marker.txt")
	if got, rerr := os.ReadFile(hostMarker); rerr != nil || string(got) != "marker-v1\n" {
		t.Fatalf("host home marker = %q (err %v), want the container write", got, rerr)
	}

	// Run 2 (member A) sees the marker; run 3 (member B) must not.
	readA := launchRun(t, ctrlA, string(ws.ID), "read", "claude")
	waitRunStatus(t, sub, &seen, readA.ID, domain.RunNeedsAttention)
	if got := fetchRunFile(t, ctrlA, seedDir, gitEnv, repoURL, readA.ID, "marker-seen.txt"); got != "marker-v1\n" {
		t.Fatalf("same member's next run saw %q, want the persisted marker", got)
	}
	readB := launchRun(t, ctrlB, string(ws.ID), "read", "claude")
	waitRunStatus(t, sub, &seen, readB.ID, domain.RunNeedsAttention)
	if got := fetchRunFile(t, ctrlB, seedDir, gitEnv, repoURL, readB.ID, "marker-seen.txt"); got != "absent\n" {
		t.Fatalf("other member's run saw %q, want no marker", got)
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

// fetchRunFile fetches a run's branch over the SSH git transport and
// returns one file's content from its tip.
func fetchRunFile(t *testing.T, ctrl *protocol.Client, dir string, env []string, repoURL, runID, name string) string {
	t.Helper()
	var pull protocol.RunPullResult
	if err := ctrl.Call(protocol.MethodRunPull, protocol.RunIDParams{RunID: runID}, &pull); err != nil {
		t.Fatalf("run.pull %s: %v", runID, err)
	}
	runGit(t, dir, env, "fetch", "-q", repoURL, pull.Branch)
	return runGit(t, dir, env, "show", "FETCH_HEAD:"+name)
}
