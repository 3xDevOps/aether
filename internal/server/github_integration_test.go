//go:build integration

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/gitengine"
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
if [ "$1" = "--version" ]; then
	echo "gh version 2.100.0 (2026-09-03)"
	exit 0
fi
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
if [ "$1" = "--version" ]; then
	echo "gh version 2.100.0 (2026-09-03)"
	exit 0
fi
printf '%s\n' '{"hosts":{"github.com":[{"state":"error","active":true,"host":"github.com","login":"octocat"}]}}'
`

// ghStubOld is Ubuntu 24.04's packaged gh: 2.45.0, with a login that
// works. It answers auth status in the human form 2.45 has and rejects
// only --json, which is the argv the login check sends - so this stub is
// the member whose login is fine and was told it was not.
const ghStubOld = `#!/bin/sh
echo "$*" >> "$HOME/gh-calls.log"
if [ "$1" = "--version" ]; then
	echo "gh version 2.45.0 (2025-07-18 Ubuntu 2.45.0-1ubuntu0.3)"
	exit 0
fi
case "$*" in
*--json*)
	echo "unknown flag: --json" >&2
	exit 1
	;;
"auth status"*)
	echo "github.com"
	echo "  X Logged in to github.com account octocat (/root/.config/gh/hosts.yml)"
	echo "  - Active account: true"
	echo "  - Token scopes: 'admin:ssh_signing_key', 'repo'"
	exit 0
	;;
esac
echo "stub gh: unsupported command: $*" >&2
exit 1
`

// OAuth status must survive the normal cost service's run-controller decorator.
// No Environment or provider call is needed to read a fresh member's state.
func TestIntegrationGitHubOAuthDisconnectedWithBudgetService(t *testing.T) {
	ctx := t.Context()
	srv, err := New(ctx, Config{
		DataDir: t.TempDir(), Addr: "127.0.0.1:0", Runtime: newE2ERuntime(),
	})
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	_, signer := writeClientKey(t)
	member := &domain.Member{
		DisplayName: "GitHub admin",
		PublicKey:   string(ssh.MarshalAuthorizedKey(signer.PublicKey())),
		Role:        domain.RoleAdmin,
	}
	if err := srv.Store().CreateMember(ctx, member); err != nil {
		t.Fatalf("seed member: %v", err)
	}
	raw, perr := srv.ssh.Local(member.ID).Call(ctx, protocol.MethodGitHubOAuthStatus, json.RawMessage(`{}`))
	if perr != nil {
		t.Fatalf("github.oauth.status through assembled server: %v", perr)
	}
	var status protocol.GitHubOAuthResult
	if err := json.Unmarshal(raw, &status); err != nil {
		t.Fatal(err)
	}
	if status.State != "disconnected" || status.SessionID != "" || status.Connection != nil {
		t.Fatalf("fresh member OAuth status = %+v", status)
	}
}

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
			"claude": {
				TUIArgs:      []string{"sh", "/workspace/agent.sh"},
				HeadlessArgs: []string{"sh", "/workspace/agent.sh"},
			},
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
	if err = os.MkdirAll(home, 0o755); err != nil {
		t.Fatalf("create the member home: %v", err)
	}
	upstream := filepath.Join(home, "upstream.git")
	runGit(t, home, gitEnv, "init", "--bare", "-q", upstream)
	origin := containerHome + "/upstream.git"

	client := dialSSH(t, addr, signer)
	ctrl := openControl(t, client)

	// Exercise the normally assembled OAuth service over authenticated SSH,
	// before opening an Environment or installing any provider fixture.
	var oauthStatus protocol.GitHubOAuthResult
	if err = ctrl.Call(protocol.MethodGitHubOAuthStatus, protocol.GitHubOAuthStatusParams{}, &oauthStatus); err != nil {
		t.Fatalf("github.oauth.status: %v", err)
	}
	if oauthStatus.State != "disconnected" {
		t.Fatalf("fresh member OAuth status = %+v", oauthStatus)
	}

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

	// An environment from before the standard image shipped gh. The step
	// used to print a login command that container cannot run, and the
	// connect answered with a login failure; both now name the remedy.
	var pe *protocol.Error
	var missing protocol.GitHubProbeResult
	if err = ctrl.Call(protocol.MethodGitHubProbe, struct{}{}, &missing); err != nil {
		t.Fatalf("github.probe without gh: %v", err)
	}
	if missing.Status != string(domain.GitHubCLIMissing) || missing.Image != image {
		t.Errorf("probe without gh = %+v, want it missing and on %q", missing, image)
	}
	// No gh anywhere means the image is what has to change, and the
	// member still has to reopen the container it starts.
	if missing.Remedy != "aether terminal stop" || missing.AdminRemedy == "" {
		t.Errorf("remedy = (%q, %q), want the reopen and an admin command", missing.Remedy, missing.AdminRemedy)
	}
	err = ctrl.Call(protocol.MethodGitHubConnect, struct{}{}, nil)
	if !errors.As(err, &pe) || !strings.Contains(err.Error(), "gh is not on PATH") {
		t.Fatalf("github.connect without gh = %v, want the missing-gh refusal", err)
	}

	// The gh a member installs by hand after that: Ubuntu 24.04 ships
	// 2.45, which cannot answer the login check at all. That used to be
	// reported as a login failure, which sent the member back to a login
	// that was already good.
	installStubGh(t, home, ghStubOld)
	var outdated protocol.GitHubProbeResult
	if err = ctrl.Call(protocol.MethodGitHubProbe, struct{}{}, &outdated); err != nil {
		t.Fatalf("github.probe with gh 2.45: %v", err)
	}
	if outdated.Status != string(domain.GitHubCLIOutdated) || outdated.Version != "2.45.0" {
		t.Errorf("probe with gh 2.45 = %+v, want it outdated and named", outdated)
	}
	// This stub lives in the member home, first on the container's PATH,
	// where no image the server can hand them reaches it. That is what the
	// answer has to say, rather than sending an admin to refresh an image.
	if want := filepath.Join(containerHome, ".local", "bin", "gh"); outdated.Path != want {
		t.Errorf("probe path = %q, want the gh in the member home %q", outdated.Path, want)
	}
	if want := "rm " + filepath.Join(containerHome, ".local", "bin", "gh"); outdated.Remedy != want {
		t.Errorf("remedy = %q, want %q", outdated.Remedy, want)
	}
	if outdated.AdminRemedy != "" {
		t.Errorf("admin remedy = %q, want none for a gh in the member's own home", outdated.AdminRemedy)
	}
	err = ctrl.Call(protocol.MethodGitHubConnect, struct{}{}, nil)
	if !errors.As(err, &pe) || !strings.Contains(err.Error(), "2.81.0") {
		t.Fatalf("github.connect with gh 2.45 = %v, want the version refusal naming the minimum", err)
	}
	if strings.Contains(err.Error(), "not logged in") {
		t.Errorf("refusal = %q, still reads as a failed login", err)
	}
	// This stub answers auth status in the human form 2.45 has, so its
	// login is good; the refusal has to come without ever asking it.
	if calls := readHomeFile(t, home, "gh-calls.log"); strings.Contains(calls, "auth status") {
		t.Errorf("gh calls = %q, want the refusal before the login was asked about", calls)
	}

	// The gh the current standard image ships.
	installStubGh(t, home, ghStub)
	var current protocol.GitHubProbeResult
	if err = ctrl.Call(protocol.MethodGitHubProbe, struct{}{}, &current); err != nil {
		t.Fatalf("github.probe with a current gh: %v", err)
	}
	if current.Status != string(domain.GitHubCLIOK) || current.Remedy != "" {
		t.Errorf("probe with gh 2.100 = %+v, want it usable with no remedy", current)
	}

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
		"auth status --hostname github.com --json hosts",
		"auth setup-git --hostname github.com",
		"ssh-key add .ssh/aether_signing.pub --type signing --title aether " + string(member.ID),
		"ssh-key list",
	}
	// Every probe and every connect asks the version first; what this
	// pins is the sequence the connect itself runs after that.
	var calls []string
	for _, line := range strings.Split(strings.TrimSpace(readHomeFile(t, home, "gh-calls.log")), "\n") {
		if line != "--version" {
			calls = append(calls, line)
		}
	}
	if !equalStrings(calls, wantCalls) {
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

// TestGitHubBrowserProviderHarness is the subprocess entrypoint used by the
// dashboard journeys. It replaces only the external GitHub HTTP/Git providers;
// server assembly, native Docker exec, credentials, mirrors and adoption are
// the production implementations. No shipped binary reads these variables.
func TestGitHubBrowserProviderHarness(t *testing.T) {
	root := os.Getenv("AETHER_E2E_GITHUB_ROOT")
	if root == "" {
		return
	}
	serverBinary := os.Getenv("AETHER_E2E_GITHUB_SERVER")
	if serverBinary == "" {
		t.Fatal("GitHub browser harness requires the built aether-server binary")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	originalTransport := http.DefaultTransport
	http.DefaultTransport = githubBrowserTransport{fallback: originalTransport}
	defer func() { http.DefaultTransport = originalTransport }()

	originalBuilders := append([]serviceBuilder(nil), serviceBuilders...)
	defer func() { serviceBuilders = originalBuilders }()
	found := false
	for i := range serviceBuilders {
		if serviceBuilders[i].name != "mirror" {
			continue
		}
		found = true
		build := serviceBuilders[i].build
		serviceBuilders[i].build = func(d Deps) (Service, error) {
			engine, err := gitengine.New(gitengine.Config{
				ReposDir:     filepath.Join(d.DataDir, "repos"),
				CheckoutsDir: filepath.Join(d.DataDir, "checkouts"),
				MirrorFetch: func(ctx context.Context, repo string, req gitengine.MirrorRequest, incoming string) error {
					var name string
					switch req.SourceURL {
					case "https://github.com/octocat/first.git":
						name = "first"
					case "https://github.com/team/second.git":
						name = "second"
					default:
						return fmt.Errorf("unexpected GitHub fixture source %q", req.SourceURL)
					}
					if _, err := os.Stat(filepath.Join(root, "fail-fetch")); err == nil {
						return errors.New("GitHub fixture fetch temporarily unavailable")
					}
					cmd := exec.CommandContext(ctx, "git", "-C", repo, "fetch", "--no-tags",
						filepath.Join(root, "repos", name), "+refs/heads/"+req.Branch+":"+incoming)
					cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0")
					if out, err := cmd.CombinedOutput(); err != nil {
						return fmt.Errorf("fixture git fetch: %w: %s", err, out)
					}
					return nil
				},
			})
			if err != nil {
				return nil, err
			}
			t.Cleanup(func() { _ = engine.Close() })
			d.Git = engine
			return build(d)
		}
	}
	if !found {
		t.Fatal("production mirror builder missing")
	}
	srv, err := New(ctx, Config{
		DataDir:       filepath.Join(root, "data"),
		Addr:          os.Getenv("AETHER_E2E_GITHUB_ADDR"),
		StandardImage: os.Getenv("AETHER_E2E_GITHUB_IMAGE"),
		ServerBinary:  serverBinary,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	if err := srv.Run(ctx); err != nil && ctx.Err() == nil {
		t.Fatal(err)
	}
}

type githubBrowserTransport struct {
	fallback http.RoundTripper
}

func (p githubBrowserTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host != "api.github.com" {
		return p.fallback.RoundTrip(req)
	}
	status := http.StatusOK
	header := http.Header{"Content-Type": {"application/json"}}
	var body any
	if req.Method != http.MethodGet || req.Header.Get("Authorization") != "Bearer github-browser-fixture" {
		status, body = http.StatusUnauthorized, map[string]any{"message": "Bad credentials"}
	} else {
		repositories := []map[string]any{
			{"id": 101, "name": "first", "full_name": "octocat/first", "private": true,
				"default_branch": "main", "clone_url": "https://github.com/octocat/first.git",
				"permissions": map[string]bool{"pull": true, "push": false, "admin": false}},
			{"id": 102, "name": "second", "full_name": "team/second", "private": true,
				"default_branch": "trunk", "clone_url": "https://github.com/team/second.git",
				"permissions": map[string]bool{"pull": true, "push": true, "admin": false}},
		}
		switch req.URL.Path {
		case "/user":
			body = map[string]any{"id": 42, "login": "octocat"}
		case "/user/repos":
			switch req.URL.Query().Get("page") {
			case "1":
				body = repositories[:1]
				header.Set("Link", `<https://api.github.com/user/repos?per_page=100&page=2>; rel="next"`)
			case "2":
				body = repositories[1:]
			default:
				status, body = http.StatusBadRequest, map[string]any{"message": "Unexpected fixture page"}
			}
		case "/repos/octocat/first":
			body = repositories[0]
		case "/repos/team/second":
			body = repositories[1]
		default:
			status, body = http.StatusNotFound, map[string]any{"message": "Unknown fixture endpoint"}
		}
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode: status, Header: header,
		Body: io.NopCloser(bytes.NewReader(data)), Request: req,
	}, nil
}
