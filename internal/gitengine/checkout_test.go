package gitengine

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

func runCheckoutGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

// hostileCheckout builds a run checkout whose .git/config, .gitattributes
// and .git/info/attributes all try to make the server run a program of the
// agent's choosing during CommitAll. Each planted program writes a marker
// file; CommitAll must leave every marker unwritten.
type hostileCheckout struct {
	path    string
	markers map[string]string
}

func newHostileCheckout(t *testing.T, e *Engine, run domain.RunID) hostileCheckout {
	t.Helper()
	checkout := filepath.Join(e.cfg.CheckoutsDir, string(run))
	if err := os.MkdirAll(checkout, 0o755); err != nil {
		t.Fatal(err)
	}
	runCheckoutGit(t, checkout, "init", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(checkout, "payload"), []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runCheckoutGit(t, checkout, "add", "-A")
	runCheckoutGit(t, checkout, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "initial")

	markerDir := t.TempDir()
	markers := map[string]string{
		"hook":   filepath.Join(markerDir, "hook-ran"),
		"filter": filepath.Join(markerDir, "filter-ran"),
		"gpg":    filepath.Join(markerDir, "gpg-ran"),
	}
	hooks := filepath.Join(markerDir, "hooks")
	if err := os.Mkdir(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	hook := filepath.Join(hooks, "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nprintf hook > "+markers["hook"]+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	clean := filepath.Join(markerDir, "clean-filter")
	if err := os.WriteFile(clean, []byte("#!/bin/sh\nprintf filter > "+markers["filter"]+"\ncat\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A signing program the agent chose: git would run it as the server
	// unless CommitAll pins gpg.format, gpg.ssh.program and commit.gpgsign
	// itself. Each planted value is the opposite of the pin, so dropping
	// any one of the three shows up here.
	signer := filepath.Join(markerDir, "signer")
	if err := os.WriteFile(signer, []byte("#!/bin/sh\nprintf gpg > "+markers["gpg"]+"\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	runCheckoutGit(t, checkout, "config", "core.hooksPath", hooks)
	runCheckoutGit(t, checkout, "config", "filter.hostile.clean", clean)
	runCheckoutGit(t, checkout, "config", "filter.hostile.required", "true")
	runCheckoutGit(t, checkout, "config", "commit.gpgsign", "false")
	runCheckoutGit(t, checkout, "config", "gpg.format", "openpgp")
	runCheckoutGit(t, checkout, "config", "gpg.program", signer)
	runCheckoutGit(t, checkout, "config", "gpg.ssh.program", signer)
	if err := os.WriteFile(filepath.Join(checkout, ".gitattributes"), []byte("payload filter=hostile\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// GIT_ATTR_SOURCE does not cover .git/info/attributes; CommitAll must
	// drop it rather than let it select the filter.
	if err := os.MkdirAll(filepath.Join(checkout, ".git", "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, ".git", "info", "attributes"), []byte("payload filter=hostile\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "payload"), []byte("after\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return hostileCheckout{path: checkout, markers: markers}
}

func (h hostileCheckout) assertNothingRan(t *testing.T) {
	t.Helper()
	for name, marker := range h.markers {
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Errorf("planted %s program ran, marker stat = %v", name, err)
		}
	}
}

func TestCommitAllIgnoresCheckoutHooksAndFilters(t *testing.T) {
	e := newUnitEngine(t)
	hostile := newHostileCheckout(t, e, "run-hostile")
	// The keyless path pins commit.gpgsign=false, so this checkout asks
	// for the opposite: dropping the pin makes the commit fail here.
	runCheckoutGit(t, hostile.path, "config", "commit.gpgsign", "true")

	commit, err := e.CommitAll(t.Context(), domain.RunID("run-hostile"), "server commit", domain.GitIdentity{}, nil)
	if err != nil {
		t.Fatalf("CommitAll: %v", err)
	}
	if commit == "" {
		t.Fatal("CommitAll returned an empty commit")
	}
	hostile.assertNothingRan(t)
	if got := runCheckoutGit(t, hostile.path, "show", "--format=", "--no-ext-diff", "HEAD:payload"); got != "after" {
		t.Fatalf("committed payload = %q, want after", got)
	}
	if got := runCheckoutGit(t, hostile.path, "show", "--format=", "--no-ext-diff", "HEAD:.gitattributes"); got != "payload filter=hostile" {
		t.Fatalf("committed attributes = %q, want hostile attribute", got)
	}
	// Without a key the commit is unsigned, whatever the checkout's own
	// config asked for.
	if got := runCheckoutGit(t, hostile.path, "log", "-1", "--format=%G?"); got != "N" {
		t.Fatalf("signature status = %q, want N (unsigned)", got)
	}
}

// The member's key signs the commit, the checkout's planted signing
// program still never runs, and nothing of the key is left on disk.
func TestCommitAllSignsWithTheMemberKey(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen is not on PATH")
	}
	e := newUnitEngine(t)
	hostile := newHostileCheckout(t, e, "run-signed")

	keyDir := t.TempDir()
	keyPath := filepath.Join(keyDir, "signing")
	sign := exec.CommandContext(t.Context(), "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "aether member-1", "-f", keyPath)
	if out, err := sign.CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v: %s", err, out)
	}
	key, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := os.ReadFile(keyPath + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	author := domain.GitIdentity{Name: "Ada Lovelace", Email: "ada@example.com"}
	before := tempSigningKeyFiles(t)

	commit, err := e.CommitAll(t.Context(), domain.RunID("run-signed"), "server commit", author, key)
	if err != nil {
		t.Fatalf("CommitAll: %v", err)
	}
	if commit == "" {
		t.Fatal("CommitAll returned an empty commit")
	}
	hostile.assertNothingRan(t)

	allowed := filepath.Join(keyDir, "allowed_signers")
	if err := os.WriteFile(allowed, []byte(author.Email+" "+string(pub)), 0o600); err != nil {
		t.Fatal(err)
	}
	runCheckoutGit(t, hostile.path, "-c", "gpg.format=ssh", "-c", "gpg.ssh.program=ssh-keygen",
		"-c", "gpg.ssh.allowedSignersFile="+allowed, "verify-commit", commit)

	if after := tempSigningKeyFiles(t); len(after) != len(before) {
		t.Errorf("staged signing keys left behind: %v, want %v", after, before)
	}
}

// A member home is agent-writable, so the key CommitAll is handed can be
// one that needs a passphrase. ssh-keygen then asks for it, and with a
// controlling terminal on the server it asks on /dev/tty and waits there
// forever; gitEnv's askpass pair is what turns that into an error. The
// commit fails either way, and leaves no copy of the key behind.
func TestCommitAllFailsFastOnAPassphraseProtectedKey(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen is not on PATH")
	}
	e := newUnitEngine(t)
	hostile := newHostileCheckout(t, e, "run-locked")
	keyPath := filepath.Join(t.TempDir(), "signing")
	gen := exec.CommandContext(t.Context(), "ssh-keygen", "-q", "-t", "ed25519",
		"-N", "secret", "-C", "aether member-1", "-f", keyPath)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v: %s", err, out)
	}
	key, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	before := tempSigningKeyFiles(t)

	type outcome struct {
		commit string
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		commit, commitErr := e.CommitAll(t.Context(), domain.RunID("run-locked"), "server commit", domain.GitIdentity{}, key)
		done <- outcome{commit, commitErr}
	}()
	select {
	case got := <-done:
		if got.err == nil {
			t.Fatalf("CommitAll with a passphrase-protected key = %q, want git's error", got.commit)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("CommitAll blocked on a passphrase prompt")
	}
	hostile.assertNothingRan(t)
	if after := tempSigningKeyFiles(t); len(after) != len(before) {
		t.Errorf("staged signing keys left behind: %v, want %v", after, before)
	}
}

// tempSigningKeyFiles lists the signing key files CommitAll stages in the
// server's temp directory, so a test can see one is not left behind.
func tempSigningKeyFiles(t *testing.T) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(os.TempDir(), "aether-signing-*"))
	if err != nil {
		t.Fatal(err)
	}
	return matches
}

// A run checkout's `origin` remote is where a push or a pull request from
// inside the run goes. The clone points it at the server-side bare repo
// path, which does not exist in the run container, so a workspace origin
// has to replace it - and no workspace origin has to leave it alone.
func TestCreateRunCheckoutPointsOriginAtTheWorkspaceUpstream(t *testing.T) {
	e := newUnitEngine(t)
	ctx := t.Context()
	repo, err := e.InitWorkspaceRepo(ctx, "ws1")
	if err != nil {
		t.Fatalf("InitWorkspaceRepo: %v", err)
	}
	seedBareMain(t, e, "ws1")

	const upstream = "https://github.com/acme/app.git"
	withOrigin, _, err := e.CreateRunCheckout(ctx, "ws1", "run-origin", "main", "push me", upstream)
	if err != nil {
		t.Fatalf("CreateRunCheckout with an origin: %v", err)
	}
	if got := runCheckoutGit(t, withOrigin, "remote", "get-url", "origin"); got != upstream {
		t.Errorf("origin = %q, want %q", got, upstream)
	}

	bare, _, err := e.CreateRunCheckout(ctx, "ws1", "run-bare", "main", "no upstream", "")
	if err != nil {
		t.Fatalf("CreateRunCheckout without an origin: %v", err)
	}
	if got := runCheckoutGit(t, bare, "remote", "get-url", "origin"); got != repo {
		t.Errorf("origin = %q, want the clone's own %q", got, repo)
	}
}

// CreateRunCheckoutAt pins the captured commit even if the workspace branch
// moves before provisioning reaches the checkout.
func TestCreateRunCheckoutAtPinsCapturedCommit(t *testing.T) {
	e := newUnitEngine(t)
	ctx := t.Context()
	repo, err := e.InitWorkspaceRepo(ctx, "ws1")
	if err != nil {
		t.Fatalf("InitWorkspaceRepo: %v", err)
	}
	seedBareMain(t, e, "ws1")
	first := runCheckoutGit(t, repo, "rev-parse", "--verify", "refs/heads/main")

	source := filepath.Join(t.TempDir(), "source")
	runCheckoutGit(t, t.TempDir(), "clone", repo, source)
	if writeErr := os.WriteFile(filepath.Join(source, "second.txt"), []byte("two\n"), 0o644); writeErr != nil {
		t.Fatal(writeErr)
	}
	runCheckoutGit(t, source, "add", "-A")
	runCheckoutGit(t, source, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "second")
	runCheckoutGit(t, source, "push", "origin", "HEAD:refs/heads/main")
	second := runCheckoutGit(t, repo, "rev-parse", "--verify", "refs/heads/main")
	if second == first {
		t.Fatal("workspace branch did not move")
	}

	checkout, _, err := e.CreateRunCheckoutAt(ctx, "ws1", "run-at", first, "main", "captured base", "")
	if err != nil {
		t.Fatalf("CreateRunCheckoutAt: %v", err)
	}
	if got := runCheckoutGit(t, checkout, "rev-parse", "--verify", "HEAD"); got != first {
		t.Fatalf("checkout HEAD = %q, want captured commit %q", got, first)
	}
	if got, _ := e.git(ctx, checkout, "config", cfgBase); got != first {
		t.Fatalf("aether.base = %q, want captured commit %q", got, first)
	}
	meta, err := e.readRunMeta("run-at")
	if err != nil {
		t.Fatalf("readRunMeta: %v", err)
	}
	if meta.Base != first {
		t.Fatalf("run metadata base = %q, want captured commit %q", meta.Base, first)
	}

	for _, invalid := range []string{first[:7], strings.Repeat("a", 40)} {
		if _, _, err := e.CreateRunCheckoutAt(ctx, "ws1", domain.RunID("invalid-"+invalid[:4]), invalid, "main", "invalid", ""); err == nil {
			t.Errorf("CreateRunCheckoutAt(%q) succeeded, want invalid commit error", invalid)
		}
	}
}

// seedBareMain gives the workspace bare repo one commit on main, which is
// the base branch CreateRunCheckout cuts from.
func seedBareMain(t *testing.T, e *Engine, ws domain.WorkspaceID) {
	t.Helper()
	repo, err := e.repoPath(ws)
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	runCheckoutGit(t, work, "init", "--initial-branch=main", work)
	if err := os.WriteFile(filepath.Join(work, "file.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runCheckoutGit(t, work, "add", "-A")
	runCheckoutGit(t, work, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "initial")
	runCheckoutGit(t, work, "push", repo, "main")
}
