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
	// A leading dash would reach git as a flag rather than a ref, a `+`
	// would force, and a colon would split the refspec in two.
	for _, branch := range []string{"--force", "+main", "main:refs/heads/evil", "main evil"} {
		if _, err := Push(t.TempDir(), branch); err == nil {
			t.Fatalf("Push accepted the branch name %q", branch)
		}
	}
}

// The seeding push carries the base branch and nothing else, even for a
// user whose git is configured to send tags along with every push.
func TestPushLeavesTagsBehind(t *testing.T) {
	requireGit(t)
	local, remote := seedRepos(t, "main")
	git(t, local, "config", "push.followTags", "true")
	git(t, local, "tag", "-a", "v1", "-m", "release")

	if _, err := Push(local, "main"); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if refs := git(t, remote, "for-each-ref", "--format=%(refname)"); strings.Contains(refs, "refs/tags/") {
		t.Fatalf("the push carried tags: %s", refs)
	}
}

// A `+` prefix in a bare refspec means force. Git accepts such a branch
// name, so the qualified refspec is what keeps the push non-forcing.
func TestPushDoesNotForceAPlusPrefixedBranch(t *testing.T) {
	requireGit(t)
	local, remote := seedRepos(t, "main")
	// Both sides get a branch named +main, diverged: a forced push would
	// overwrite the remote's commit, an honest one is refused.
	git(t, local, "branch", "+main")
	git(t, local, "push", "aether", "refs/heads/+main:refs/heads/+main")
	before := git(t, remote, "rev-parse", "+main")
	git(t, local, "commit", "--allow-empty", "-m", "local only")
	git(t, local, "branch", "-f", "+main", "HEAD")

	if _, err := Push(local, "+main"); err == nil {
		t.Fatal("Push accepted a branch name that would read as a force refspec")
	}
	if after := git(t, remote, "rev-parse", "+main"); after != before {
		t.Fatalf("the remote branch moved: %s -> %s", before, after)
	}
}

// An unqualified ref is ambiguous when a tag shares the branch's name;
// the qualified refspec names the branch beyond doubt.
func TestPushSeedsABranchThatSharesATagName(t *testing.T) {
	requireGit(t)
	local, remote := seedRepos(t, "release")
	git(t, local, "tag", "release")

	if _, err := Push(local, "release"); err != nil {
		t.Fatalf("Push: %v", err)
	}
	want := git(t, local, "rev-parse", "refs/heads/release")
	if got := git(t, remote, "rev-parse", "refs/heads/release"); got != want {
		t.Fatalf("remote release = %s, want %s", got, want)
	}
}

func TestAetherRemoteURLReportsWhereTheRepoPoints(t *testing.T) {
	requireGit(t)
	local, remote := seedRepos(t, "main")

	url, err := AetherRemoteURL(local)
	if err != nil {
		t.Fatalf("AetherRemoteURL: %v", err)
	}
	if url != remote {
		t.Fatalf("url = %q, want %q", url, remote)
	}

	// No remote is not an error: the caller refuses with its own message.
	bare := t.TempDir()
	git(t, bare, "init", "-b", "main")
	if url, err = AetherRemoteURL(bare); err != nil || url != "" {
		t.Fatalf("AetherRemoteURL = %q, %v; want empty and no error", url, err)
	}
}
