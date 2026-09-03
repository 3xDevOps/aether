package localops

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedRepos builds a bare "server" repo wired as the `aether` remote of a
// local clone holding one commit on branch, returning (local, remote).
func seedRepos(t *testing.T, branch string) (string, string) {
	t.Helper()
	remote := filepath.Join(t.TempDir(), "wsp_1.git")
	git(t, t.TempDir(), "init", "--bare", "-b", branch, remote)

	local := t.TempDir()
	git(t, local, "init", "-b", branch)
	if err := os.WriteFile(filepath.Join(local, "README.md"), []byte("# demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, local, "add", "README.md")
	git(t, local, "commit", "-m", "seed")
	git(t, local, "remote", "add", "aether", remote)
	return local, remote
}

func TestPushSeedsTheRemoteBranch(t *testing.T) {
	requireGit(t)
	local, remote := seedRepos(t, "trunk")

	output, err := Push(local, "trunk")
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if !strings.Contains(output, "trunk") {
		t.Fatalf("output does not mention the branch: %q", output)
	}
	want := git(t, local, "rev-parse", "trunk")
	if got := git(t, remote, "rev-parse", "trunk"); got != want {
		t.Fatalf("remote trunk = %s, want %s", got, want)
	}
	// -u leaves the branch tracking the remote, so later pushes need no
	// arguments.
	if got := git(t, local, "rev-parse", "--abbrev-ref", "trunk@{upstream}"); got != "aether/trunk" {
		t.Fatalf("upstream = %q", got)
	}
}

func TestPushRefusesWithoutCommits(t *testing.T) {
	requireGit(t)
	local := t.TempDir()
	git(t, local, "init", "-b", "main")
	git(t, local, "remote", "add", "aether", t.TempDir())

	_, err := Push(local, "main")
	if !errors.Is(err, ErrPushPrecondition) {
		t.Fatalf("err = %v, want a precondition refusal", err)
	}
	if !strings.Contains(err.Error(), "no commits yet") {
		t.Fatalf("message = %q", err)
	}
}

func TestPushRefusesMissingBranchAndNamesTheCurrentOne(t *testing.T) {
	requireGit(t)
	local, _ := seedRepos(t, "dev")

	_, err := Push(local, "main")
	if !errors.Is(err, ErrPushPrecondition) {
		t.Fatalf("err = %v, want a precondition refusal", err)
	}
	// The fix is one of the two names, so both appear.
	if !strings.Contains(err.Error(), "main") || !strings.Contains(err.Error(), "dev") {
		t.Fatalf("message names neither branch: %q", err)
	}
}

func TestPushRefusesWithoutTheAetherRemote(t *testing.T) {
	requireGit(t)
	local := t.TempDir()
	git(t, local, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(local, "f.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, local, "add", "f.txt")
	git(t, local, "commit", "-m", "seed")

	_, err := Push(local, "main")
	if !errors.Is(err, ErrPushPrecondition) {
		t.Fatalf("err = %v, want a precondition refusal", err)
	}
	if !strings.Contains(err.Error(), "aether remote") {
		t.Fatalf("message = %q", err)
	}
}

func TestPushRejectionCarriesGitOutput(t *testing.T) {
	requireGit(t)
	local, remote := seedRepos(t, "main")
	// A server-side hook is how branch protection refuses in the real
	// deployment; the user must see the server's own words.
	hook := filepath.Join(remote, "hooks", "pre-receive")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\necho 'main is protected' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	output, err := Push(local, "main")
	if err == nil {
		t.Fatal("Push succeeded against a rejecting remote")
	}
	if !strings.Contains(err.Error(), "main is protected") {
		t.Fatalf("error drops git's message: %v", err)
	}
	if !strings.Contains(output, "main is protected") {
		t.Fatalf("output drops git's message: %q", output)
	}
}

func TestPushRejectsUnusableBranchNames(t *testing.T) {
	if _, err := Push(t.TempDir(), ""); err == nil {
		t.Fatal("Push accepted an empty branch")
	}
	// A leading dash would reach git as a flag rather than a ref.
	if _, err := Push(t.TempDir(), "--force"); err == nil {
		t.Fatal("Push accepted a flag-shaped branch")
	}
}
