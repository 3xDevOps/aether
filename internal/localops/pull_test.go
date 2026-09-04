package localops

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
)

// git runs one git command in dir, failing the test on error.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(cmd.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.invalid",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.invalid",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// requireGit skips when git is not installed.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

// scratchRepos builds a remote repo with one commit on branch and an
// empty local clone target, returning (local, remote).
func scratchRepos(t *testing.T, branch string) (string, string) {
	t.Helper()
	remote := t.TempDir()
	git(t, remote, "init", "-b", branch)
	if err := writeTestFile(filepath.Join(remote, "f.txt"), "hello\n"); err != nil {
		t.Fatal(err)
	}
	git(t, remote, "add", "f.txt")
	git(t, remote, "commit", "-m", "initial")

	local := t.TempDir()
	git(t, local, "init", "-b", branch)
	return local, remote
}

func TestPullFetchLandsTrackingRef(t *testing.T) {
	requireGit(t)
	local, remote := scratchRepos(t, "run-branch")

	ref, output, err := pullFetch(local, remote, "run-branch")
	if err != nil {
		t.Fatalf("pullFetch: %v", err)
	}
	if ref != "refs/remotes/aether/run-branch" {
		t.Fatalf("ref = %q", ref)
	}
	if !strings.Contains(output, "run-branch") {
		t.Fatalf("output does not mention the branch: %q", output)
	}
	want := git(t, remote, "rev-parse", "run-branch")
	got := git(t, local, "rev-parse", "refs/remotes/aether/run-branch")
	if got != want {
		t.Fatalf("tracking ref = %s, want %s", got, want)
	}
}

func TestPullCreatesBranchWhenNotCurrent(t *testing.T) {
	requireGit(t)
	local, remote := scratchRepos(t, "run-branch")
	git(t, local, "switch", "-c", "main")

	result, err := pull(local, remote, "run-branch")
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if result.Branch != "run-branch" || result.Ref != "refs/remotes/aether/run-branch" {
		t.Fatalf("result = %+v", result)
	}
	if result.Current || result.Dirty {
		t.Fatalf("result = %+v, want non-current clean branch", result)
	}
	if got := git(t, local, "rev-parse", "refs/heads/run-branch"); got != git(t, remote, "rev-parse", "run-branch") {
		t.Fatalf("local branch did not point to fetched branch: %s", got)
	}
	if got := git(t, local, "for-each-ref", "--format=%(upstream:short)", "refs/heads/run-branch"); got != "" {
		t.Fatalf("local branch upstream = %q, want none without a remote", got)
	}
}

func TestPullCreatesTrackingBranchWhenRemoteExists(t *testing.T) {
	requireGit(t)
	local, remote := scratchRepos(t, "run-branch")
	git(t, local, "switch", "-c", "main")
	git(t, local, "remote", "add", "aether", remote)

	if _, err := pull(local, remote, "run-branch"); err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if got := git(t, local, "for-each-ref", "--format=%(upstream:short)", "refs/heads/run-branch"); got != "aether/run-branch" {
		t.Fatalf("local branch upstream = %q, want aether/run-branch", got)
	}
}

func TestPullFastForwardsCurrentBranchAndReportsDirty(t *testing.T) {
	requireGit(t)
	local, remote := scratchRepos(t, "run-branch")
	git(t, local, "switch", "-c", "main")
	git(t, local, "remote", "add", "aether", remote)
	first := git(t, remote, "rev-parse", "run-branch")
	git(t, local, "fetch", remote, "run-branch:refs/heads/run-branch")
	git(t, local, "switch", "run-branch")
	writeTestFile(filepath.Join(remote, "second.txt"), "second\n")
	git(t, remote, "add", "second.txt")
	git(t, remote, "commit", "-m", "second")
	second := git(t, remote, "rev-parse", "run-branch")
	if first == second {
		t.Fatal("remote commit did not advance")
	}

	result, err := pull(local, remote, "run-branch")
	if err != nil {
		t.Fatalf("Pull current branch: %v", err)
	}
	if !result.Current || result.Dirty {
		t.Fatalf("result = %+v, want current clean branch", result)
	}
	if got := git(t, local, "rev-parse", "HEAD"); got != second {
		t.Fatalf("current branch tip = %s, want %s", got, second)
	}

	if err := writeTestFile(filepath.Join(local, "uncommitted.txt"), "dirty\n"); err != nil {
		t.Fatal(err)
	}
	result, err = pull(local, remote, "run-branch")
	if err != nil {
		t.Fatalf("Pull dirty current branch: %v", err)
	}
	if !result.Current || !result.Dirty {
		t.Fatalf("result = %+v, want current dirty branch", result)
	}
}

func TestPullSwitchRefusesDirtyWorktree(t *testing.T) {
	requireGit(t)
	local, remote := scratchRepos(t, "run-branch")
	git(t, local, "switch", "-c", "main")
	git(t, local, "remote", "add", "aether", remote)

	if err := writeTestFile(filepath.Join(local, "uncommitted.txt"), "dirty\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := pull(local, remote, "run-branch"); err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if err := SwitchPull(local, "run-branch"); err == nil || !strings.Contains(err.Error(), "working tree has uncommitted changes; commit or stash them first") {
		t.Fatalf("SwitchPull error = %v, want dirty-worktree refusal", err)
	}
	if got := git(t, local, "branch", "--show-current"); got != "main" {
		t.Fatalf("branch after refused switch = %q, want main", got)
	}
}

func TestPullFetchFailureCarriesOutput(t *testing.T) {
	requireGit(t)
	local, _ := scratchRepos(t, "main")

	_, _, err := pullFetch(local, filepath.Join(t.TempDir(), "missing"), "main")
	if err == nil {
		t.Fatal("pullFetch succeeded against a missing remote")
	}
	if !strings.Contains(err.Error(), "git fetch") {
		t.Fatalf("error lacks context: %v", err)
	}
}

func TestPullRequiresBranch(t *testing.T) {
	if _, err := Pull(t.TempDir(), "aether", "host:2222", protocol.RunPullResult{}); err == nil {
		t.Fatal("Pull accepted coordinates without a branch")
	}
	if _, _, err := PullCommand(t.TempDir(), "aether", "host:2222", protocol.RunPullResult{}); err == nil {
		t.Fatal("PullCommand accepted coordinates without a branch")
	}
}

func TestPullCommandShapesFetch(t *testing.T) {
	branch, cmd, err := PullCommand("/repo", "alice", "host:2222", protocol.RunPullResult{
		WorkspaceID: "ws_1",
		Branch:      "aether/run_1",
	})
	if err != nil {
		t.Fatalf("PullCommand: %v", err)
	}
	if branch != "aether/run_1" {
		t.Fatalf("branch = %q", branch)
	}
	want := []string{
		"git", "-C", "/repo", "fetch", "--no-tags",
		"ssh://alice@host:2222/ws_1.git",
		"+refs/heads/aether/run_1:refs/remotes/aether/aether/run_1",
	}
	got := cmd.Args
	if len(got) != len(want) {
		t.Fatalf("argv = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("argv[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
