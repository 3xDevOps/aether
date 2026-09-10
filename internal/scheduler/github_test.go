package scheduler

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/runtime"
)

const ghStatusLoggedIn = `{"hosts":{"github.com":[{"state":"success","active":true,"login":"octocat","scopes":"gist, read:org, repo, admin:ssh_signing_key"}]}}`

// ghVersionCurrent is what the gh in the standard image answers.
const ghVersionCurrent = "gh version 2.100.0 (2026-09-03)\nhttps://github.com/cli/cli/releases/tag/v2.100.0\n"

// ghVersionUbuntu is Ubuntu 24.04's packaged gh, which predates
// auth status --json and carries the distribution build on the same line.
const ghVersionUbuntu = "gh version 2.45.0 (2025-07-18 Ubuntu 2.45.0-1ubuntu0.3)\nhttps://github.com/cli/cli/releases/tag/v2.45.0\n"

// ghNotFound is what the container runtime answers for an executable the
// image does not have, verbatim from a real docker exec, with exit 127.
const ghNotFound = `OCI runtime exec failed: exec failed: unable to start container process: exec: "gh": executable file not found in $PATH`

// ghWhere is what the container answers when the probe asks whose gh it is:
// the home, then the path gh resolves to.
func ghWhere(path string) string { return "/root\n" + path + "\n" }

// asksWhereGhIs reports the probe's second exec, the one that decides
// whether the gh that answered is a file in the member's own home. The
// argv is matched whole: a probe that asked about some other program
// would otherwise be answered as though it had asked about gh.
func asksWhereGhIs(argv []string) bool {
	return slices.Equal(argv, []string{"sh", "-c", `printf '%s\n%s\n' "$HOME" "$(command -v gh || true)"`})
}

func requireSSHKeygen(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen is not on PATH")
	}
}

func TestConnectGitHubNeedsARunningTerminal(t *testing.T) {
	e := newTestEnv(t, nil)
	_, err := e.sched.ConnectGitHub(t.Context(), e.member.ID)
	if !errors.Is(err, ErrTerminalNotRunning) {
		t.Fatalf("ConnectGitHub error = %v, want %v", err, ErrTerminalNotRunning)
	}
}

func TestConnectGitHubReportsAnUnfinishedLogin(t *testing.T) {
	e := newTestEnv(t, nil)
	const output = `{"hosts":{"github.com":[{"state":"timeout","active":true,"login":"octocat"}]}}`
	e.rt.execHandler = func(_ runtime.ID, argv []string) (int, string, error) {
		if slices.Contains(argv, "--version") {
			return 0, ghVersionCurrent, nil
		}
		if slices.Contains(argv, "status") {
			return 0, output, nil
		}
		t.Errorf("unexpected exec %v after an unfinished login", argv)
		return 0, "", nil
	}
	if _, err := e.sched.EnsureTerminal(t.Context(), e.member.ID); err != nil {
		t.Fatalf("EnsureTerminal: %v", err)
	}
	_, err := e.sched.ConnectGitHub(t.Context(), e.member.ID)
	if !errors.Is(err, ErrGitHubNotLoggedIn) {
		t.Fatalf("ConnectGitHub error = %v, want %v", err, ErrGitHubNotLoggedIn)
	}
	if !strings.Contains(err.Error(), output) {
		t.Errorf("error %q does not carry gh's own output", err)
	}
	if has, herr := e.cfg.Homes.HasSigningKey(e.member.ID); herr != nil || has {
		t.Errorf("signing key after a failed login = (%v, %v), want none", has, herr)
	}
}

func TestConnectGitHubSurfacesSetupGitFailure(t *testing.T) {
	requireSSHKeygen(t)
	e := newTestEnv(t, nil)
	e.rt.execHandler = func(_ runtime.ID, argv []string) (int, string, error) {
		if slices.Contains(argv, "--version") {
			return 0, ghVersionCurrent, nil
		}
		if slices.Contains(argv, "status") {
			return 0, ghStatusLoggedIn, nil
		}
		return 1, "failed to set up git: no credential helper", nil
	}
	if _, err := e.sched.EnsureTerminal(t.Context(), e.member.ID); err != nil {
		t.Fatalf("EnsureTerminal: %v", err)
	}
	_, err := e.sched.ConnectGitHub(t.Context(), e.member.ID)
	if err == nil || !strings.Contains(err.Error(), "no credential helper") {
		t.Fatalf("ConnectGitHub error = %v, want gh's setup-git output", err)
	}
	if has, herr := e.cfg.Homes.HasSigningKey(e.member.ID); herr != nil || has {
		t.Errorf("signing key after a failed setup-git = (%v, %v), want none", has, herr)
	}
}

func TestConnectGitHubRegistersTheSigningKey(t *testing.T) {
	requireSSHKeygen(t)
	e := newTestEnv(t, nil)
	if err := e.db.UpdateMemberGitIdentity(t.Context(), e.member.ID, "Ada Lovelace", "ada@example.com"); err != nil {
		t.Fatalf("UpdateMemberGitIdentity: %v", err)
	}
	e.rt.execHandler = func(_ runtime.ID, argv []string) (int, string, error) {
		switch {
		case slices.Contains(argv, "--version"):
			return 0, ghVersionCurrent, nil
		case slices.Contains(argv, "status"):
			return 0, ghStatusLoggedIn, nil
		case slices.Contains(argv, "list"):
			return 0, "aether\t" + homeKeyFingerprint(t, e) + "\t2026-01-01\t1\tsigning\n", nil
		}
		return 0, "", nil
	}
	terminal, err := e.sched.EnsureTerminal(t.Context(), e.member.ID)
	if err != nil {
		t.Fatalf("EnsureTerminal: %v", err)
	}

	conn, err := e.sched.ConnectGitHub(t.Context(), e.member.ID)
	if err != nil {
		t.Fatalf("ConnectGitHub: %v", err)
	}
	if conn.Login != "octocat" {
		t.Errorf("login = %q, want octocat", conn.Login)
	}
	if !strings.HasPrefix(conn.SigningKey, "ssh-ed25519 ") || !strings.HasSuffix(conn.SigningKey, " aether "+string(e.member.ID)) {
		t.Errorf("signing key = %q, want the member's ed25519 line", conn.SigningKey)
	}
	if !strings.HasPrefix(conn.Fingerprint, "SHA256:") {
		t.Errorf("fingerprint = %q, want a SHA256 fingerprint", conn.Fingerprint)
	}

	home, err := e.cfg.Homes.Path(e.member.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantArgv := [][]string{
		{"gh", "--version"},
		{"gh", "auth", "status", "--hostname", "github.com", "--json", "hosts"},
		{"gh", "auth", "setup-git", "--hostname", "github.com"},
		{"gh", "ssh-key", "add", ".ssh/aether_signing.pub", "--type", "signing", "--title", "aether " + string(e.member.ID)},
		{"gh", "ssh-key", "list"},
	}
	calls := e.rt.execRuns()
	if len(calls) != len(wantArgv) {
		t.Fatalf("exec calls = %+v, want %d", calls, len(wantArgv))
	}
	for i, want := range wantArgv {
		if !slices.Equal(calls[i].argv, want) {
			t.Errorf("exec %d argv = %v, want %v", i, calls[i].argv, want)
		}
		if calls[i].id != runtime.ID(terminal.ContainerID) {
			t.Errorf("exec %d container = %q, want the member terminal %q", i, calls[i].id, terminal.ContainerID)
		}
	}

	pub, err := os.ReadFile(filepath.Join(home, ".ssh", "aether_signing.pub"))
	if err != nil {
		t.Fatalf("read public key: %v", err)
	}
	if strings.TrimSpace(string(pub)) != conn.SigningKey {
		t.Errorf("public key file = %q, want %q", pub, conn.SigningKey)
	}
	if _, statErr := os.Stat(filepath.Join(home, ".ssh", "aether_signing")); statErr != nil {
		t.Fatalf("stat private key: %v", statErr)
	}
	config, err := os.ReadFile(filepath.Join(home, ".gitconfig"))
	if err != nil {
		t.Fatalf("read .gitconfig: %v", err)
	}
	for _, want := range []string{
		"name = Ada Lovelace", "email = ada@example.com",
		"format = ssh", "signingkey = ~/.ssh/aether_signing", "gpgsign = true",
	} {
		if !strings.Contains(string(config), want) {
			t.Errorf(".gitconfig is missing %q:\n%s", want, config)
		}
	}
}

// homeKeyFingerprint is what gh would report for the key the connect just
// wrote into the member's home. The exec handler that calls it runs on its
// own goroutine, so a failure is reported rather than fatal.
func homeKeyFingerprint(t *testing.T, e *testEnv) string {
	t.Helper()
	home, err := e.cfg.Homes.Path(e.member.ID)
	if err != nil {
		t.Errorf("home path: %v", err)
		return ""
	}
	pub, err := os.ReadFile(filepath.Join(home, ".ssh", "aether_signing.pub"))
	if err != nil {
		t.Errorf("read public key: %v", err)
		return ""
	}
	fingerprint, err := signingFingerprint(strings.TrimSpace(string(pub)))
	if err != nil {
		t.Errorf("fingerprint: %v", err)
		return ""
	}
	return fingerprint
}

// A token the account no longer honors leaves gh exiting 0 with its
// complaint in the entry. That sentence is what the member needs, not the
// JSON it arrived in.
func TestConnectGitHubReportsTheAccountsOwnError(t *testing.T) {
	e := newTestEnv(t, nil)
	const output = `{"hosts":{"github.com":[{"state":"error","active":true,"login":"octocat","error":"HTTP 401: Bad credentials"}]}}`
	e.rt.execHandler = func(_ runtime.ID, argv []string) (int, string, error) {
		if slices.Contains(argv, "--version") {
			return 0, ghVersionCurrent, nil
		}
		if slices.Contains(argv, "status") {
			return 0, output, nil
		}
		t.Errorf("unexpected exec %v after a rejected token", argv)
		return 0, "", nil
	}
	if _, err := e.sched.EnsureTerminal(t.Context(), e.member.ID); err != nil {
		t.Fatalf("EnsureTerminal: %v", err)
	}
	_, err := e.sched.ConnectGitHub(t.Context(), e.member.ID)
	if !errors.Is(err, ErrGitHubNotLoggedIn) {
		t.Fatalf("ConnectGitHub error = %v, want %v", err, ErrGitHubNotLoggedIn)
	}
	if !strings.Contains(err.Error(), "HTTP 401: Bad credentials") {
		t.Errorf("error %q does not carry gh's account error", err)
	}
	if strings.Contains(err.Error(), `"hosts"`) {
		t.Errorf("error %q shows the JSON blob", err)
	}
}

// A login granted without admin:ssh_signing_key would fail at the last
// step, after the home had been rewritten. It is refused first, with the
// command that fixes it.
func TestConnectGitHubRefusesALoginWithoutTheSigningScope(t *testing.T) {
	e := newTestEnv(t, nil)
	const output = `{"hosts":{"github.com":[{"state":"success","active":true,"login":"octocat","scopes":"gist, read:org, repo"}]}}`
	e.rt.execHandler = func(_ runtime.ID, argv []string) (int, string, error) {
		if slices.Contains(argv, "--version") {
			return 0, ghVersionCurrent, nil
		}
		if slices.Contains(argv, "status") {
			return 0, output, nil
		}
		t.Errorf("unexpected exec %v with the scope missing", argv)
		return 0, "", nil
	}
	if _, err := e.sched.EnsureTerminal(t.Context(), e.member.ID); err != nil {
		t.Fatalf("EnsureTerminal: %v", err)
	}
	_, err := e.sched.ConnectGitHub(t.Context(), e.member.ID)
	if !errors.Is(err, ErrGitHubScopeMissing) {
		t.Fatalf("ConnectGitHub error = %v, want %v", err, ErrGitHubScopeMissing)
	}
	if errors.Is(err, ErrGitHubNotLoggedIn) {
		t.Errorf("error %q reads as a missing login", err)
	}
	if !strings.Contains(err.Error(), "gh auth refresh -h github.com -s admin:ssh_signing_key") {
		t.Errorf("error %q does not carry the refresh command", err)
	}
	home, err := e.cfg.Homes.Path(e.member.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".ssh", ".gitconfig"} {
		if _, statErr := os.Stat(filepath.Join(home, name)); !os.IsNotExist(statErr) {
			t.Errorf("%s stat = %v, want the home untouched", name, statErr)
		}
	}
}

// The upload names a path the container can rewrite between the two
// steps, so the connection is only reported once the account lists the
// fingerprint the member's own private key produces.
func TestConnectGitHubRefusesAKeyTheAccountDoesNotList(t *testing.T) {
	requireSSHKeygen(t)
	e := newTestEnv(t, nil)
	e.rt.execHandler = func(_ runtime.ID, argv []string) (int, string, error) {
		switch {
		case slices.Contains(argv, "--version"):
			return 0, ghVersionCurrent, nil
		case slices.Contains(argv, "status"):
			return 0, ghStatusLoggedIn, nil
		case slices.Contains(argv, "list"):
			return 0, "someone else\tSHA256:Ry9aBd4mNJmZQMxTS0KaCiKtGCEccCWQyPq9WLm0000\t2026-01-01\t9\tsigning\n", nil
		}
		return 0, "", nil
	}
	if _, err := e.sched.EnsureTerminal(t.Context(), e.member.ID); err != nil {
		t.Fatalf("EnsureTerminal: %v", err)
	}
	_, err := e.sched.ConnectGitHub(t.Context(), e.member.ID)
	if err == nil || !strings.Contains(err.Error(), "does not match this member's key") {
		t.Fatalf("ConnectGitHub error = %v, want the fingerprint mismatch", err)
	}
	if !strings.Contains(err.Error(), homeKeyFingerprint(t, e)) {
		t.Errorf("error %q does not name the member's own fingerprint", err)
	}
}

// An environment saved, or a standard image pulled, before the image
// shipped gh has none. The member used to be handed a login command that
// container cannot run; now the refusal names both halves of the remedy -
// the admin's and their own - and keeps the container's own line.
func TestConnectGitHubReportsAMissingGh(t *testing.T) {
	e := newTestEnv(t, nil)
	e.rt.execHandler = func(_ runtime.ID, argv []string) (int, string, error) {
		switch {
		case slices.Contains(argv, "--version"):
			return 127, ghNotFound, nil
		case asksWhereGhIs(argv):
			return 0, ghWhere("/usr/bin/gh"), nil
		}
		t.Errorf("unexpected exec %v with no gh in the container", argv)
		return 0, "", nil
	}
	if _, err := e.sched.EnsureTerminal(t.Context(), e.member.ID); err != nil {
		t.Fatalf("EnsureTerminal: %v", err)
	}
	_, err := e.sched.ConnectGitHub(t.Context(), e.member.ID)
	if !errors.Is(err, ErrGitHubCLIMissing) {
		t.Fatalf("ConnectGitHub error = %v, want %v", err, ErrGitHubCLIMissing)
	}
	if errors.Is(err, ErrGitHubNotLoggedIn) {
		t.Errorf("error %q reads as a missing login", err)
	}
	if !strings.Contains(err.Error(), ghNotFound) {
		t.Errorf("error %q does not carry the container's own line", err)
	}
	// Repulling the image does not touch a container that is already up,
	// so the refusal has to name the reopen as well.
	for _, want := range []string{"docker pull " + e.cfg.StandardImage, "aether terminal stop"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

// gh 2.45 - Ubuntu 24.04's package, and what a member reaches for after
// "command not found" - rejects auth status --json and exits 1. That used
// to be reported as a failed login, which sent the member back to a login
// that was already good.
func TestConnectGitHubReportsAGhTooOldForTheLoginCheck(t *testing.T) {
	e := newTestEnv(t, nil)
	e.rt.execHandler = func(_ runtime.ID, argv []string) (int, string, error) {
		switch {
		case slices.Contains(argv, "--version"):
			return 0, ghVersionUbuntu, nil
		case asksWhereGhIs(argv):
			return 0, ghWhere("/usr/bin/gh"), nil
		}
		t.Errorf("unexpected exec %v with a gh too old to answer it", argv)
		return 0, "", nil
	}
	if _, err := e.sched.EnsureTerminal(t.Context(), e.member.ID); err != nil {
		t.Fatalf("EnsureTerminal: %v", err)
	}
	_, err := e.sched.ConnectGitHub(t.Context(), e.member.ID)
	if !errors.Is(err, ErrGitHubCLIOutdated) {
		t.Fatalf("ConnectGitHub error = %v, want %v", err, ErrGitHubCLIOutdated)
	}
	if errors.Is(err, ErrGitHubNotLoggedIn) {
		t.Errorf("error %q reads as a missing login", err)
	}
	for _, want := range []string{"2.45.0", "2.81.0"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}
}

// gh reports one account with no active marker when the host holds only
// that account. Dropping --active is what makes the entry visible at all,
// so the check has to accept it.
func TestConnectGitHubAcceptsTheOnlyAccountOnTheHost(t *testing.T) {
	requireSSHKeygen(t)
	e := newTestEnv(t, nil)
	const output = `{"hosts":{"github.com":[{"state":"success","login":"octocat","scopes":"repo, admin:ssh_signing_key"}]}}`
	e.rt.execHandler = func(_ runtime.ID, argv []string) (int, string, error) {
		switch {
		case slices.Contains(argv, "--version"):
			return 0, ghVersionCurrent, nil
		case slices.Contains(argv, "status"):
			return 0, output, nil
		case slices.Contains(argv, "list"):
			return 0, "aether\t" + homeKeyFingerprint(t, e) + "\t2026-01-01\t1\tsigning\n", nil
		}
		return 0, "", nil
	}
	if _, err := e.sched.EnsureTerminal(t.Context(), e.member.ID); err != nil {
		t.Fatalf("EnsureTerminal: %v", err)
	}
	conn, err := e.sched.ConnectGitHub(t.Context(), e.member.ID)
	if err != nil {
		t.Fatalf("ConnectGitHub: %v", err)
	}
	if conn.Login != "octocat" {
		t.Errorf("login = %q, want octocat", conn.Login)
	}
}

// gh exits 0 from auth status --json whatever it thinks of the account, so
// a non-zero exit is a fatal error and never a missing login. Reporting it
// as one would be the same lie in a narrower place.
func TestConnectGitHubDoesNotReadAFatalStatusAsNoLogin(t *testing.T) {
	e := newTestEnv(t, nil)
	const output = "error connecting to api.github.com"
	e.rt.execHandler = func(_ runtime.ID, argv []string) (int, string, error) {
		if slices.Contains(argv, "--version") {
			return 0, ghVersionCurrent, nil
		}
		return 1, output, nil
	}
	if _, err := e.sched.EnsureTerminal(t.Context(), e.member.ID); err != nil {
		t.Fatalf("EnsureTerminal: %v", err)
	}
	_, err := e.sched.ConnectGitHub(t.Context(), e.member.ID)
	if err == nil || !strings.Contains(err.Error(), output) {
		t.Fatalf("ConnectGitHub error = %v, want gh's own output", err)
	}
	if errors.Is(err, ErrGitHubNotLoggedIn) {
		t.Errorf("error %q reads as a missing login", err)
	}
}

// A gh whose version line this cannot read is not an old gh. Refusing it
// would repeat the bug: a working login blamed on something else.
func TestConnectGitHubTrustsAGhWithAnUnreadableVersion(t *testing.T) {
	requireSSHKeygen(t)
	e := newTestEnv(t, nil)
	e.rt.execHandler = func(_ runtime.ID, argv []string) (int, string, error) {
		switch {
		case slices.Contains(argv, "--version"):
			return 0, "gh version built from source\n", nil
		case slices.Contains(argv, "status"):
			return 0, ghStatusLoggedIn, nil
		case slices.Contains(argv, "list"):
			return 0, "aether\t" + homeKeyFingerprint(t, e) + "\t2026-01-01\t1\tsigning\n", nil
		}
		return 0, "", nil
	}
	if _, err := e.sched.EnsureTerminal(t.Context(), e.member.ID); err != nil {
		t.Fatalf("EnsureTerminal: %v", err)
	}
	if _, err := e.sched.ConnectGitHub(t.Context(), e.member.ID); err != nil {
		t.Fatalf("ConnectGitHub: %v", err)
	}
}

// The connect refuses a container the standard image has moved out from
// under with the same one-step answer the probe gives, not the admin's.
func TestConnectGitHubSendsAStaleContainerToReopen(t *testing.T) {
	e := newTestEnv(t, nil)
	e.rt.execHandler = func(_ runtime.ID, argv []string) (int, string, error) {
		switch {
		case slices.Contains(argv, "--version"):
			return 127, ghNotFound, nil
		case asksWhereGhIs(argv):
			return 0, ghWhere("/usr/bin/gh"), nil
		}
		t.Errorf("unexpected exec %v with no gh in the container", argv)
		return 0, "", nil
	}
	if _, err := e.sched.EnsureTerminal(t.Context(), e.member.ID); err != nil {
		t.Fatalf("EnsureTerminal: %v", err)
	}
	e.sched.mu.Lock()
	e.sched.terminals[e.member.ID].image = "ghcr.io/3xdevops/aether-standard:v0.1.0"
	e.sched.mu.Unlock()

	_, err := e.sched.ConnectGitHub(t.Context(), e.member.ID)
	if !errors.Is(err, ErrGitHubCLIMissing) {
		t.Fatalf("ConnectGitHub error = %v, want %v", err, ErrGitHubCLIMissing)
	}
	if !strings.Contains(err.Error(), "still running the image it started from") {
		t.Errorf("error %q does not name the reopen", err)
	}
	if strings.Contains(err.Error(), "server admin") {
		t.Errorf("error %q asks for an admin the member does not need", err)
	}
}

// A member on their own saved image is not told to throw it away: the
// refusal offers the way out that keeps it first.
func TestConnectGitHubKeepsASavedEnvironmentOnOffer(t *testing.T) {
	e := newTestEnv(t, nil)
	if err := e.db.UpdateMemberImage(t.Context(), e.member.ID, "aether/member-test:1"); err != nil {
		t.Fatalf("UpdateMemberImage: %v", err)
	}
	if err := e.rt.Commit(t.Context(), "c-saved", "aether/member-test:1"); err != nil {
		t.Fatalf("seed the saved image: %v", err)
	}
	e.rt.execHandler = func(_ runtime.ID, argv []string) (int, string, error) {
		switch {
		case slices.Contains(argv, "--version"):
			return 0, ghVersionUbuntu, nil
		case asksWhereGhIs(argv):
			return 0, ghWhere("/usr/bin/gh"), nil
		}
		t.Errorf("unexpected exec %v with a gh too old to answer it", argv)
		return 0, "", nil
	}
	if _, err := e.sched.EnsureTerminal(t.Context(), e.member.ID); err != nil {
		t.Fatalf("EnsureTerminal: %v", err)
	}
	_, err := e.sched.ConnectGitHub(t.Context(), e.member.ID)
	if !errors.Is(err, ErrGitHubCLIOutdated) {
		t.Fatalf("ConnectGitHub error = %v, want %v", err, ErrGitHubCLIOutdated)
	}
	for _, want := range []string{"save the environment again", "remove the saved image"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not say %q", err, want)
		}
	}
}

// gh comes from the member's container, which can shadow it with a
// command that never returns. The connect holds that member's terminal
// lock, so it has to give up on its own.
func TestConnectGitHubStopsAtItsDeadline(t *testing.T) {
	e := newTestEnv(t, nil)
	released := make(chan struct{})
	t.Cleanup(func() { close(released) })
	e.rt.execHandler = func(_ runtime.ID, _ []string) (int, string, error) {
		<-released
		return 0, ghStatusLoggedIn, nil
	}
	if _, err := e.sched.EnsureTerminal(t.Context(), e.member.ID); err != nil {
		t.Fatalf("EnsureTerminal: %v", err)
	}
	restore := githubConnectTimeout
	githubConnectTimeout = 20 * time.Millisecond
	t.Cleanup(func() { githubConnectTimeout = restore })

	start := time.Now()
	_, err := e.sched.ConnectGitHub(t.Context(), e.member.ID)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ConnectGitHub error = %v, want the deadline", err)
	}
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Errorf("ConnectGitHub returned after %v, want it to give up at its deadline", elapsed)
	}
}

// The end-of-run commit is signed with the run owner's key once they have
// one, and stays unsigned before that.
func TestCommitAllSignsWithTheRunOwnersKey(t *testing.T) {
	e := newTestEnv(t, nil)
	run, _ := e.launchFake(t, "add OAuth login")

	if _, err := e.sched.commitAll(t.Context(), run.ID, "wip: before a key"); err != nil {
		t.Fatalf("commitAll: %v", err)
	}
	if got := e.git.commitSignings(run.ID); len(got) != 1 || got[0] {
		t.Fatalf("signings before a key = %v, want [false]", got)
	}

	if _, err := e.cfg.Homes.EnsureSigningKey(e.member.ID); err != nil {
		t.Fatalf("EnsureSigningKey: %v", err)
	}
	if _, err := e.sched.commitAll(t.Context(), run.ID, "wip: after a key"); err != nil {
		t.Fatalf("second commitAll: %v", err)
	}
	if got := e.git.commitSignings(run.ID); len(got) != 2 || !got[1] {
		t.Fatalf("signings after a key = %v, want a signed second commit", got)
	}
}

// A key the agent has corrupted from inside its own run costs the
// signature, not the commit.
func TestCommitAllCommitsWithAnUnusableKey(t *testing.T) {
	e := newTestEnv(t, nil)
	run, _ := e.launchFake(t, "add OAuth login")
	home, err := e.cfg.Homes.Path(e.member.ID)
	if err != nil {
		t.Fatal(err)
	}
	if derr := os.Mkdir(filepath.Join(home, ".ssh"), 0o700); derr != nil {
		t.Fatal(derr)
	}
	if werr := os.WriteFile(filepath.Join(home, ".ssh", "aether_signing"), []byte("not a key\n"), 0o600); werr != nil {
		t.Fatal(werr)
	}

	commit, err := e.sched.commitAll(t.Context(), run.ID, "wip: with a corrupt key")
	if err != nil || commit == "" {
		t.Fatalf("commitAll = (%q, %v), want the work committed", commit, err)
	}
	if got := e.git.commitSignings(run.ID); len(got) != 1 || got[0] {
		t.Fatalf("signings = %v, want [false]", got)
	}
}
