//go:build integration

package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/scheduler"
)

// githubAgentScript is the run half of the scenario. It reports the origin
// the checkout was handed, commits with nothing but what the member home's
// .gitconfig and the container's environment give it, pushes the branch to
// that origin, and records git's own verdict on the signature it just made
// - all of it into the worktree, so the run branch is the evidence.
const githubAgentScript = `sleep 1
echo agent-ready
git remote get-url origin > origin.txt
printf 'from the run\n' > pushed.txt
git add pushed.txt
git commit -q -m "agent commit"
git push -q origin HEAD:refs/heads/from-run
printf '%s %s\n' "$GIT_AUTHOR_EMAIL" "$(cat "$HOME/.ssh/aether_signing.pub")" > /tmp/allowed-signers
git -c gpg.ssh.allowedSignersFile=/tmp/allowed-signers log -1 --format='%G?' HEAD > sig.txt
`

// ghStub is the fake gh this scenario puts first on the environment
// terminal's PATH. It records every call, reports one logged-in account,
// writes the credential helper block real gh writes, and files the public
// key it is handed, and lists that key's fingerprint back the way gh does
// - so the run afterwards is driven by exactly what a real connect would
// have left in the home.
const ghStub = `#!/bin/sh
echo "$*" >> "$HOME/gh-calls.log"
case "$1 $2" in
"auth status")
	printf '%s\n' '{"hosts":{"github.com":[{"state":"success","active":true,"host":"github.com","login":"octocat","tokenSource":"oauth_token","scopes":"admin:ssh_signing_key, repo","gitProtocol":"https"}]}}'
	;;
"auth setup-git")
	printf '[credential "https://github.com"]\n\thelper = !gh auth git-credential\n' >> "$HOME/.gitconfig"
	;;
"ssh-key add")
	cp "$3" "$HOME/gh-registered-key"
	;;
"ssh-key list")
	set -- $(ssh-keygen -lf "$HOME/gh-registered-key")
	printf 'aether\t%s\t2026-01-01T00:00:00Z\t1\tsigning\n' "$2"
	;;
*)
	echo "stub gh: unsupported command: $*" >&2
	exit 1
	;;
esac
`

// ghStubExpired answers with the entry gh reports for a token the account
// no longer honors. It exits 0, so the state field alone is what has to
// refuse the connection.
const ghStubExpired = `#!/bin/sh
echo "$*" >> "$HOME/gh-calls.log"
printf '%s\n' '{"hosts":{"github.com":[{"state":"error","active":true,"host":"github.com","login":"octocat"}]}}'
`

// TestIntegrationGitHubConnect drives the GitHub connection end to end
// against real Docker: github.connect finishes the login the member began
// in their environment terminal, and the run that follows pushes its own
// branch to the workspace origin and signs with the key the connection
// left in the member home. Aether's own end-of-run commit is signed with
// the same key.
func TestIntegrationGitHubConnect(t *testing.T) {
	requireBinary(t, "git")
	// The server signs its end-of-run commit itself, on the host.
	requireBinary(t, "ssh-keygen")
	requireBinary(t, "docker")
	if !dockerReachable(t) {
		t.Skip("connecting GitHub needs a real container to run gh and git in; Docker daemon unreachable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	// Before the runtime, so that its cleanup runs after the runtime's
	// container sweep: cleanups run last-in-first-out, and removing an
	// image a live container still holds only untags it.
	image, user := buildGitAgentImage(t)
	rt, verifyNoLeaks, ok := dockerRuntime(t)
	if !ok {
		t.Fatal("the Docker daemon went away after the image was built")
	}
	containerHome := harness.HomeDir(user)

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

	keyPath, signer := writeClientKey(t)
	member := &domain.Member{
		DisplayName: "Octo",
		PublicKey:   string(ssh.MarshalAuthorizedKey(signer.PublicKey())),
		Color:       "#4363d8",
		Role:        domain.RoleAdmin,
	}
	ws := &domain.Workspace{Name: "github", BaseBranch: domain.DefaultBaseBranch}
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
	writeFile(t, filepath.Join(seedDir, "agent.sh"), githubAgentScript)
	runGit(t, seedDir, gitEnv, "add", "-A")
	runGit(t, seedDir, gitEnv, "commit", "-q", "-m", "seed")
	runGit(t, seedDir, gitEnv, "push", "-q", repoURL, "main")

	// The stub gh and the upstream the run pushes to both live in the
	// member home, which is bind-mounted as the container's $HOME.
	home := filepath.Join(dataDir, "homes", string(member.ID))
	installStubGh(t, home, ghStub)
	upstream := filepath.Join(home, "upstream.git")
	runGit(t, home, gitEnv, "init", "--bare", "-q", upstream)
	origin := containerHome + "/upstream.git"

	client := dialSSH(t, addr, signer)
	ctrl := openControl(t, client)

	// The identity the connection writes into the home, and the origin
	// every new run checkout is pointed at - both set the way a member
	// sets them.
	if err = ctrl.Call(protocol.MethodMemberGit, protocol.MemberGitParams{
		Name: "Octo Cat", Email: "octo@example.com",
	}, nil); err != nil {
		t.Fatalf("member.git: %v", err)
	}
	var originSet protocol.WorkspaceOriginResult
	if err = ctrl.Call(protocol.MethodWorkspaceOrigin, protocol.WorkspaceOriginParams{
		WorkspaceID: string(ws.ID), Origin: origin,
	}, &originSet); err != nil {
		t.Fatalf("workspace.origin: %v", err)
	}
	if originSet.Workspace.Origin != origin {
		t.Fatalf("workspace origin = %q, want %q", originSet.Workspace.Origin, origin)
	}

	// gh lives in the member's own container, so the connection needs the
	// environment terminal running.
	term := openTerminal(t, client, "main")
	term.stdin.Write([]byte("echo terminal-ready\n"))
	term.waitOutput(t, "terminal-ready")

	var conn protocol.GitHubConnectResult
	if err = ctrl.Call(protocol.MethodGitHubConnect, struct{}{}, &conn); err != nil {
		t.Fatalf("github.connect: %v", err)
	}
	if conn.Login != "octocat" {
		t.Errorf("connected login = %q, want octocat", conn.Login)
	}
	if !strings.HasPrefix(conn.SigningKey, "ssh-ed25519 ") {
		t.Errorf("signing key = %q, want an ed25519 public key line", conn.SigningKey)
	}
	if !strings.HasPrefix(conn.Fingerprint, "SHA256:") {
		t.Errorf("fingerprint = %q, want a SHA256 fingerprint", conn.Fingerprint)
	}

	// What the connection left in the home on the host.
	info, err := os.Stat(filepath.Join(home, ".ssh", "aether_signing"))
	if err != nil {
		t.Fatalf("stat the signing key: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("signing key mode = %04o, want 0600", perm)
	}
	pub := readHomeFile(t, home, ".ssh/aether_signing.pub")
	if pub != conn.SigningKey+"\n" {
		t.Errorf("signing key file = %q, want the reported key %q", pub, conn.SigningKey)
	}
	if registered := readHomeFile(t, home, "gh-registered-key"); registered != pub {
		t.Errorf("key registered through gh = %q, want the public key %q", registered, pub)
	}
	gitconfig := readHomeFile(t, home, ".gitconfig")
	for _, want := range []string{
		`[credential "https://github.com"]`,
		"helper = !gh auth git-credential",
		"name = Octo Cat",
		"email = octo@example.com",
		"signingkey = ~/.ssh/aether_signing",
		"format = ssh",
		"gpgsign = true",
	} {
		if !strings.Contains(gitconfig, want) {
			t.Errorf("home .gitconfig missing %q:\n%s", want, gitconfig)
		}
	}
	wantCalls := []string{
		"auth status --hostname github.com --active --json hosts",
		"auth setup-git --hostname github.com",
		"ssh-key add .ssh/aether_signing.pub --type signing --title aether " + string(member.ID),
		"ssh-key list",
	}
	if calls := strings.Split(strings.TrimSpace(readHomeFile(t, home, "gh-calls.log")), "\n"); !equalStrings(calls, wantCalls) {
		t.Errorf("gh calls = %q, want %q", calls, wantCalls)
	}

	// The run: it pushes its own branch to the origin the workspace
	// records, signing with the home's settings, and Aether commits what
	// the agent left behind when it exits.
	run := launchRun(t, ctrl, string(ws.ID), "push from the run", "claude")
	waitRunStatus(t, sub, &seen, run.ID, domain.RunCompleted)

	allowedSigners := filepath.Join(t.TempDir(), "allowed_signers")
	writeFile(t, allowedSigners, "octo@example.com "+conn.SigningKey+"\n")
	verify := []string{"-c", "gpg.ssh.allowedSignersFile=" + allowedSigners, "verify-commit"}

	if got := runGit(t, upstream, gitEnv, "show", "from-run:pushed.txt"); got != "from the run\n" {
		t.Errorf("pushed.txt in the upstream repo = %q", got)
	}
	runGit(t, upstream, gitEnv, append(verify, "from-run")...)
	if got := strings.TrimSpace(runGit(t, upstream, gitEnv, "log", "-1", "--format=%cn <%ce>", "from-run")); got != "Octo Cat <octo@example.com>" {
		t.Errorf("pushed commit committer = %q, want the member's identity", got)
	}

	fetchRunBranch(t, ctrl, seedDir, gitEnv, repoURL, run.ID)
	if got := strings.TrimSpace(runGit(t, seedDir, gitEnv, "show", "FETCH_HEAD:origin.txt")); got != origin {
		t.Errorf("origin inside the run = %q, want %q", got, origin)
	}
	if got := strings.TrimSpace(runGit(t, seedDir, gitEnv, "show", "FETCH_HEAD:sig.txt")); got != "G" {
		t.Errorf("git's verdict on the run's own commit = %q, want G", got)
	}
	runGit(t, seedDir, gitEnv, append(verify, "FETCH_HEAD")...)
	const wantTip = "Aether <aether@localhost> committed for Octo Cat <octo@example.com>"
	if got := strings.TrimSpace(runGit(t, seedDir, gitEnv, "log", "-1", "--format=%cn <%ce> committed for %an <%ae>", "FETCH_HEAD")); got != wantTip {
		t.Errorf("run branch tip = %q, want %q", got, wantTip)
	}

	// Without a terminal there is no gh to ask.
	term.close()
	if err = ctrl.Call(protocol.MethodTerminalStop, struct{}{}, nil); err != nil {
		t.Fatalf("terminal.stop: %v", err)
	}
	var pe *protocol.Error
	if err = ctrl.Call(protocol.MethodGitHubConnect, struct{}{}, nil); !errors.As(err, &pe) || pe.Code != protocol.CodeInvalidState {
		t.Fatalf("github.connect with the terminal stopped = %v, want CodeInvalidState", err)
	}

	// A login gh no longer honors is refused, with gh's own answer in the
	// message.
	installStubGh(t, home, ghStubExpired)
	term = openTerminal(t, client, "main")
	term.stdin.Write([]byte("echo terminal-back\n"))
	term.waitOutput(t, "terminal-back")
	err = ctrl.Call(protocol.MethodGitHubConnect, struct{}{}, nil)
	if !errors.As(err, &pe) || pe.Code != protocol.CodeInvalidState {
		t.Fatalf("github.connect with an expired login = %v, want CodeInvalidState", err)
	}
	if !strings.Contains(err.Error(), `"state":"error"`) {
		t.Errorf("refusal = %q, want gh's own output in it", err)
	}
	term.close()
	if err = ctrl.Call(protocol.MethodTerminalStop, struct{}{}, nil); err != nil {
		t.Fatalf("terminal.stop after the refusal: %v", err)
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

// buildGitAgentImage builds the image this scenario's terminal and run
// share: git, ssh-keygen, and the test process's own uid as the container
// user so the bind mounts have one owner on both sides.
func buildGitAgentImage(t *testing.T) (image, user string) {
	t.Helper()
	uid, gid := os.Getuid(), os.Getgid()
	user = fmt.Sprintf("%d:%d", uid, gid)
	image = fmt.Sprintf("aether-e2e-gitagent:%d", os.Getpid())
	build := exec.Command("docker", "build", "-q",
		"--build-arg", fmt.Sprintf("UID=%d", uid),
		"--build-arg", fmt.Sprintf("GID=%d", gid),
		"-t", image, filepath.Join(repoRoot(t), "internal", "server", "testdata", "gitagent"))
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("docker build %s: %v (%s)", image, err, out)
	}
	t.Cleanup(func() {
		if out, err := exec.Command("docker", "rmi", "-f", image).CombinedOutput(); err != nil {
			t.Logf("remove image %s: %v (%s)", image, err, out)
		}
	})
	return image, user
}

// installStubGh writes the stub gh the environment terminal finds first:
// the scheduler puts ~/.local/bin at the front of the container's PATH,
// and docker exec inherits that environment.
func installStubGh(t *testing.T, home, script string) {
	t.Helper()
	dir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatalf("write the stub gh: %v", err)
	}
}

func readHomeFile(t *testing.T, home, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, filepath.FromSlash(name)))
	if err != nil {
		t.Fatalf("read %s from the member home: %v", name, err)
	}
	return string(data)
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
