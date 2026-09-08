package scheduler

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/runtime"
)

const ghStatusLoggedIn = `{"hosts":{"github.com":[{"state":"success","active":true,"login":"octocat"}]}}`

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
		if slices.Contains(argv, "status") {
			return 0, ghStatusLoggedIn, nil
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
		{"gh", "auth", "status", "--hostname", "github.com", "--active", "--json", "hosts"},
		{"gh", "auth", "setup-git", "--hostname", "github.com"},
		{"gh", "ssh-key", "add", ".ssh/aether_signing.pub", "--type", "signing", "--title", "aether " + string(e.member.ID)},
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
