//go:build integration

package server

import (
	"context"
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
	"github.com/3xDevOps/Aether/internal/memberhome"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/scheduler"
)

// imageAgentScript records whether a file installed outside $HOME in the
// environment terminal is visible inside the run container.
const imageAgentScript = `sleep 1
echo agent-ready
cat /opt/aether-marker.txt > marker-seen.txt 2>/dev/null || printf 'absent\n' > marker-seen.txt
`

// TestIntegrationMemberEnvironmentImage drives the saved environment image
// end to end against real Docker: a file written outside the home in the
// environment terminal is invisible to runs until env.save, visible to the
// member's runs and reopened terminal afterwards, never visible to another
// member, and gone again after env.reset.
func TestIntegrationMemberEnvironmentImage(t *testing.T) {
	requireBinary(t, "git")
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	rt, image, verifyNoLeaks := pickRuntime(t)
	if _, fallback := rt.(*e2eRuntime); fallback {
		t.Skip("saving an environment needs a real container commit; Docker daemon unreachable")
	}
	dataDir := filepath.Join(t.TempDir(), "data")
	srv, err := New(ctx, Config{
		DataDir: dataDir, Addr: "127.0.0.1:0", Runtime: rt,
		StandardImage: image,
		Harnesses: map[string]scheduler.HarnessSpec{
			"claude": {TUIArgs: []string{"sh", "/workspace/agent.sh"}},
		},
	})
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}

	keyA, signerA := writeClientKey(t)
	memberA := &domain.Member{
		DisplayName: "Image Owner",
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
	ws := &domain.Workspace{Name: "images", BaseBranch: domain.DefaultBaseBranch}
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
	writeFile(t, filepath.Join(seedDir, "agent.sh"), imageAgentScript)
	runGit(t, seedDir, gitEnv, "add", "-A")
	runGit(t, seedDir, gitEnv, "commit", "-q", "-m", "seed")
	runGit(t, seedDir, gitEnv, "push", "-q", repoURL, "main")

	clientA := dialSSH(t, addr, signerA)
	ctrlA := openControl(t, clientA)
	ctrlB := openControl(t, dialSSH(t, addr, signerB))
	markerSeen := func(ctrl *protocol.Client, run protocol.Run) string {
		waitRunStatus(t, sub, &seen, run.ID, domain.RunCompleted)
		return fetchRunFile(t, ctrl, seedDir, gitEnv, repoURL, run.ID, "marker-seen.txt")
	}

	// Saving needs an open terminal.
	var pe *protocol.Error
	if err := ctrlA.Call(protocol.MethodEnvSave, struct{}{}, nil); !errors.As(err, &pe) || pe.Code != protocol.CodeInvalidState {
		t.Fatalf("env.save without a terminal = %v, want CodeInvalidState", err)
	}

	// Install something outside the home in the terminal. Runs do not see
	// it until the environment is saved.
	term := openTerminal(t, clientA, "main")
	term.stdin.Write([]byte("mkdir -p /opt && printf 'installed\\n' > /opt/aether-marker.txt && echo step-$((1+1))\n"))
	term.waitOutput(t, "step-2")
	if got := markerSeen(ctrlA, launchRun(t, ctrlA, string(ws.ID), "before save", "claude")); got != "absent\n" {
		t.Fatalf("run before save saw %q, want absent", got)
	}

	// Everything the GitHub connection keeps in the member home, plus a
	// gh token, all of it inside the running container's $HOME mount.
	homes, err := memberhome.New(filepath.Join(dataDir, "homes"))
	if err != nil {
		t.Fatalf("open the member homes: %v", err)
	}
	if _, err = homes.EnsureSigningKey(memberA.ID); err != nil {
		t.Fatalf("generate the signing key: %v", err)
	}
	if err = homes.ConfigureGit(ctx, memberA.ID, memberA.GitIdentity()); err != nil {
		t.Fatalf("configure git in the member home: %v", err)
	}
	homeA, err := homes.Path(memberA.ID)
	if err != nil {
		t.Fatalf("resolve the member home: %v", err)
	}
	if err = os.MkdirAll(filepath.Join(homeA, ".config", "gh"), 0o700); err != nil {
		t.Fatalf("create the gh config directory: %v", err)
	}
	writeFile(t, filepath.Join(homeA, ".config", "gh", "hosts.yml"), "github.com:\n    oauth_token: fake-token\n")

	var saved protocol.EnvSaveResult
	if err := ctrlA.Call(protocol.MethodEnvSave, struct{}{}, &saved); err != nil {
		t.Fatalf("env.save: %v", err)
	}
	t.Cleanup(func() { _ = rt.RemoveImage(context.Background(), saved.Image) })
	if !strings.HasPrefix(saved.Image, "aether/member-"+string(memberA.ID)+":") {
		t.Fatalf("saved image = %q, want the member tag", saved.Image)
	}
	var status protocol.TerminalStatusResult
	if err := ctrlA.Call(protocol.MethodTerminalStatus, struct{}{}, &status); err != nil {
		t.Fatalf("terminal.status: %v", err)
	}
	if status.SavedImage != saved.Image || status.Image != image {
		t.Fatalf("status after save = %+v, want saved %q on the standard container", status, saved.Image)
	}
	if m, err := srv.Store().GetMember(ctx, memberA.ID); err != nil || m.Image != saved.Image {
		t.Fatalf("stored member image = %q (err %v), want %q", m.Image, err, saved.Image)
	}
	assertImageExcludesHome(ctx, t, rt, saved.Image)

	// The member's runs now start from the saved image; another member's
	// runs do not.
	if got := markerSeen(ctrlA, launchRun(t, ctrlA, string(ws.ID), "after save", "claude")); got != "installed\n" {
		t.Fatalf("run after save saw %q, want the installed marker", got)
	}
	if got := markerSeen(ctrlB, launchRun(t, ctrlB, string(ws.ID), "other member", "claude")); got != "absent\n" {
		t.Fatalf("other member's run saw %q, want absent", got)
	}

	// A fresh terminal starts from the saved image too.
	term.close()
	if err := ctrlA.Call(protocol.MethodTerminalStop, struct{}{}, nil); err != nil {
		t.Fatalf("terminal.stop: %v", err)
	}
	term = openTerminal(t, clientA, "main")
	term.stdin.Write([]byte("cat /opt/aether-marker.txt; echo step-$((2+1))\n"))
	term.waitOutput(t, "installed")
	term.waitOutput(t, "step-3")
	if err := ctrlA.Call(protocol.MethodTerminalStatus, struct{}{}, &status); err != nil {
		t.Fatalf("terminal.status after reopen: %v", err)
	}
	if status.Image != saved.Image {
		t.Fatalf("reopened terminal image = %q, want %q", status.Image, saved.Image)
	}

	// Reset stops the terminal, forgets the image, and runs return to the
	// standard image.
	term.close()
	if err := ctrlA.Call(protocol.MethodEnvReset, struct{}{}, nil); err != nil {
		t.Fatalf("env.reset: %v", err)
	}
	var afterReset protocol.TerminalStatusResult
	if err := ctrlA.Call(protocol.MethodTerminalStatus, struct{}{}, &afterReset); err != nil {
		t.Fatalf("terminal.status after reset: %v", err)
	}
	if afterReset.Running || afterReset.SavedImage != "" {
		t.Fatalf("status after reset = %+v, want stopped with no saved image", afterReset)
	}
	if exists, err := rt.ImageExists(ctx, saved.Image); err != nil || exists {
		t.Fatalf("saved image after reset exists=%v (err %v), want removed", exists, err)
	}
	if got := markerSeen(ctrlA, launchRun(t, ctrlA, string(ws.ID), "after reset", "claude")); got != "absent\n" {
		t.Fatalf("run after reset saw %q, want absent", got)
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

// assertImageExcludesHome starts a container from the saved image with no
// member home mounted and reports every credential-shaped file it finds:
// the home is a bind mount, and a bind mount is one thing docker commit
// never captures.
func assertImageExcludesHome(ctx context.Context, t *testing.T, rt runtime.Runtime, image string) {
	t.Helper()
	id, err := rt.Create(ctx, runtime.Spec{
		Name:    "saved-image-scan",
		Image:   image,
		Command: []string{"sleep", "120"},
	})
	if err != nil {
		t.Fatalf("create a container from the saved image: %v", err)
	}
	defer func() {
		if derr := rt.Destroy(context.WithoutCancel(ctx), id); derr != nil {
			t.Errorf("destroy the scan container: %v", derr)
		}
	}()
	if serr := rt.Start(ctx, id); serr != nil {
		t.Fatalf("start a container from the saved image: %v", serr)
	}
	const scan = `for p in /root/.ssh/aether_signing /root/.gitconfig /root/.config/gh/hosts.yml; do
	[ -e "$p" ] && echo "present:$p"
done
grep -rls fake-token /root /home /etc /opt /tmp /var
echo scan-done
`
	code, stdout, stderr, err := rt.Exec(ctx, id, []string{"sh", "-c", scan}, "/")
	if err != nil {
		t.Fatalf("scan the saved image: %v", err)
	}
	if code != 0 || strings.TrimSpace(stdout) != "scan-done" {
		t.Errorf("the saved image carries the member home: exit %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
}
