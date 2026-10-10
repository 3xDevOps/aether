//go:build integration

package server

import (
	"context"
	"encoding/json"
	"errors"
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

// environmentAgentScript records what the setup script left behind by the
// time the agent starts.
const environmentAgentScript = `sleep 1
echo agent-ready
cat setup-seen.txt > agent-saw.txt 2>/dev/null || printf 'absent\n' > agent-saw.txt
`

// TestIntegrationWorkspaceEnvironment drives the workspace setup script,
// variables and secrets end to end against real Docker: an admin sets them
// over the control channel, a collaborator's run finds the script already
// run in its checkout with both kinds of variable, and a failing script
// fails the launch with its output for the launcher alone, the secret masked.
func TestIntegrationWorkspaceEnvironment(t *testing.T) {
	requireBinary(t, "git")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	rt, image, verifyNoLeaks := pickRuntime(t)
	if _, fallback := rt.(*e2eRuntime); fallback {
		t.Skip("a setup script needs a real container; Docker daemon unreachable")
	}
	dataDir := filepath.Join(t.TempDir(), "data")
	srv, err := New(ctx, Config{
		DataDir: dataDir, Addr: "127.0.0.1:0", Runtime: rt,
		StandardImage: image,
		Harnesses: map[string]scheduler.HarnessSpec{
			"claude": {
				TUIArgs:      []string{"sh", "/workspace/agent.sh"},
				HeadlessArgs: []string{"sh", "/workspace/agent.sh"},
			},
		},
	})
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}

	adminKey, adminSigner := writeClientKey(t)
	admin := &domain.Member{
		DisplayName: "Workspace Admin",
		PublicKey:   string(ssh.MarshalAuthorizedKey(adminSigner.PublicKey())),
		Color:       "#4363d8",
		Role:        domain.RoleAdmin,
	}
	_, launcherSigner := writeClientKey(t)
	launcher := &domain.Member{
		DisplayName: "Launcher",
		PublicKey:   string(ssh.MarshalAuthorizedKey(launcherSigner.PublicKey())),
		Color:       "#e6194b",
		Role:        domain.RoleCollaborator,
	}
	ws := &domain.Workspace{Name: "environment", BaseBranch: domain.DefaultBaseBranch}
	for _, m := range []*domain.Member{admin, launcher} {
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
		"GIT_SSH_COMMAND=ssh -i "+adminKey+
			" -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o BatchMode=yes")
	runGit(t, seedDir, gitEnv, "init", "-q", "-b", "main")
	runGit(t, seedDir, gitEnv, "config", "user.name", "E2E")
	runGit(t, seedDir, gitEnv, "config", "user.email", "e2e@localhost")
	runGit(t, seedDir, gitEnv, "config", "commit.gpgsign", "false")
	writeFile(t, filepath.Join(seedDir, "agent.sh"), environmentAgentScript)
	runGit(t, seedDir, gitEnv, "add", "-A")
	runGit(t, seedDir, gitEnv, "commit", "-q", "-m", "seed")
	runGit(t, seedDir, gitEnv, "push", "-q", repoURL, "main")

	adminCtrl := openControl(t, dialSSH(t, addr, adminSigner))
	launcherCtrl := openControl(t, dialSSH(t, addr, launcherSigner))

	const token = "integration-token-51c7e0"
	setEnvironment := func(script string) {
		t.Helper()
		if err := adminCtrl.Call(protocol.MethodWorkspaceEnvironmentSet, protocol.WorkspaceEnvironmentSetParams{
			WorkspaceID: string(ws.ID),
			SetupScript: &script,
			Set: []protocol.WorkspaceVariable{
				{Name: "APP_MODE", Value: "ci"},
				{Name: "API_TOKEN", Value: token, Secret: true},
			},
		}, nil); err != nil {
			t.Fatalf("workspace.environment.set: %v", err)
		}
	}

	// The script runs in the checkout with both variables, and has finished
	// before the agent's first command.
	setEnvironment(`printf '%s %s %s\n' "$PWD" "$APP_MODE" "${#API_TOKEN}" > setup-seen.txt`)
	run := launchRun(t, launcherCtrl, string(ws.ID), "read the setup", "claude")
	waitRunStatus(t, sub, &seen, run.ID, domain.RunCompleted)
	want := fmt.Sprintf("/workspace ci %d\n", len(token))
	for _, name := range []string{"setup-seen.txt", "agent-saw.txt"} {
		if got := fetchRunFile(t, launcherCtrl, seedDir, gitEnv, repoURL, run.ID, name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}

	// A failing script fails the launch. The launcher gets what it printed
	// with the secret masked; the run's reason, which every member reads,
	// carries the exit code and nothing the script printed.
	setEnvironment(`echo "installing in $APP_MODE mode with $API_TOKEN"
echo "no such package" >&2
exit 3`)
	var pe *protocol.Error
	err = launcherCtrl.Call(protocol.MethodRunLaunch, protocol.RunLaunchParams{
		WorkspaceID: string(ws.ID), Task: "never starts", Harness: "claude",
		Mode: string(domain.LaunchHeadless),
	}, nil)
	if !errors.As(err, &pe) {
		t.Fatalf("launch with a failing setup script = %v, want a protocol error", err)
	}
	const reason = "provisioning: start container: runtime: setup script exited 3"
	if pe.Message != reason {
		t.Errorf("launch error = %q, want %q", pe.Message, reason)
	}
	var failure protocol.SetupFailure
	if err = json.Unmarshal(pe.Data, &failure); err != nil {
		t.Fatalf("launch error data %s: %v", pe.Data, err)
	}
	for _, line := range []string{"installing in ci mode with ***", "no such package"} {
		if !strings.Contains(failure.SetupOutput, line) {
			t.Errorf("setup output %q is missing %q", failure.SetupOutput, line)
		}
	}
	if strings.Contains(failure.SetupOutput, token) {
		t.Errorf("setup output carries the secret: %q", failure.SetupOutput)
	}

	var list protocol.RunListResult
	if err = launcherCtrl.Call(protocol.MethodRunList, protocol.RunListParams{WorkspaceID: string(ws.ID)}, &list); err != nil {
		t.Fatalf("run.list: %v", err)
	}
	var failed *protocol.Run
	for i := range list.Runs {
		if list.Runs[i].Task == "never starts" {
			failed = &list.Runs[i]
		}
	}
	if failed == nil || failed.Status != string(domain.RunFailed) || failed.Reason != reason {
		t.Errorf("failed run = %+v, want status failed and reason %q", failed, reason)
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
