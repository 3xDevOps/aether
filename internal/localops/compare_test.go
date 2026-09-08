package localops

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// commit adds one file and commits it, returning the new tip.
func commit(t *testing.T, repo, name, contents string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, name), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", name)
	git(t, repo, "commit", "-m", "add "+name)
	return git(t, repo, "rev-parse", "HEAD")
}

// seededClone is the second member's situation: a workspace another
// member already pushed main to, and a clone of it wired the way
// link.repo wires one. It returns the clone, the bare workspace repo,
// and the tip both sides start from.
func seededClone(t *testing.T) (string, string, string) {
	t.Helper()
	first, remote := seedRepos(t, "main")
	if _, err := Push(first, "main"); err != nil {
		t.Fatalf("seeding push: %v", err)
	}
	tip := git(t, first, "rev-parse", "main")

	clone := filepath.Join(t.TempDir(), "clone")
	git(t, t.TempDir(), "clone", "--branch", "main", "--origin", "aether", remote, clone)
	git(t, clone, "config", "user.email", "dev@example.com")
	git(t, clone, "config", "user.name", "dev")
	return clone, remote, tip
}

// advance pushes one new commit onto the workspace's copy of main,
// standing in for the first member's later work.
func advance(t *testing.T, remote string) string {
	t.Helper()
	work := filepath.Join(t.TempDir(), "work")
	git(t, t.TempDir(), "clone", "--branch", "main", remote, work)
	git(t, work, "config", "user.email", "dev@example.com")
	git(t, work, "config", "user.name", "dev")
	tip := commit(t, work, "server.txt", "from the workspace\n")
	git(t, work, "push", "origin", "refs/heads/main:refs/heads/main")
	return tip
}

func TestCompareBranchReportsAWorkspaceWithoutTheBranch(t *testing.T) {
	requireGit(t)
	local, _ := seedRepos(t, "main")

	cmp, err := CompareBranch(local, "main")
	if err != nil {
		t.Fatalf("CompareBranch: %v", err)
	}
	if cmp.State != BranchMissing {
		t.Fatalf("state = %q, want %q", cmp.State, BranchMissing)
	}
	if cmp.Local != git(t, local, "rev-parse", "main") {
		t.Fatalf("local = %q", cmp.Local)
	}
	if cmp.Workspace != "" {
		t.Fatalf("workspace = %q, want empty", cmp.Workspace)
	}
	// There was nothing to count against, so the counts stay zero.
	if cmp.Ahead != 0 || cmp.Behind != 0 {
		t.Fatalf("ahead/behind = %d/%d, want 0/0", cmp.Ahead, cmp.Behind)
	}
}

func TestCompareBranchReportsTheSameCommit(t *testing.T) {
	requireGit(t)
	clone, _, tip := seededClone(t)

	cmp, err := CompareBranch(clone, "main")
	if err != nil {
		t.Fatalf("CompareBranch: %v", err)
	}
	if cmp.State != BranchSame {
		t.Fatalf("state = %q, want %q", cmp.State, BranchSame)
	}
	if cmp.Local != tip || cmp.Workspace != tip {
		t.Fatalf("tips = %q / %q, want both %q", cmp.Local, cmp.Workspace, tip)
	}
	if cmp.Ahead != 0 || cmp.Behind != 0 {
		t.Fatalf("ahead/behind = %d/%d, want 0/0", cmp.Ahead, cmp.Behind)
	}
}

func TestCompareBranchReportsALocalBranchAhead(t *testing.T) {
	requireGit(t)
	clone, _, _ := seededClone(t)
	tip := commit(t, clone, "mine.txt", "my work\n")

	cmp, err := CompareBranch(clone, "main")
	if err != nil {
		t.Fatalf("CompareBranch: %v", err)
	}
	if cmp.State != BranchAhead {
		t.Fatalf("state = %q, want %q", cmp.State, BranchAhead)
	}
	if cmp.Local != tip {
		t.Fatalf("local = %q, want %q", cmp.Local, tip)
	}
	if cmp.Ahead != 1 || cmp.Behind != 0 {
		t.Fatalf("ahead/behind = %d/%d, want 1/0", cmp.Ahead, cmp.Behind)
	}
}

// A member whose clone is behind the workspace. The plain seeding push
// is what git rejects with "fetch first", and the
// comparison is what says so before the push is even attempted.
func TestCompareBranchReportsAWorkspaceAheadOfTheClone(t *testing.T) {
	requireGit(t)
	clone, remote, _ := seededClone(t)
	serverTip := advance(t, remote)

	if _, err := Push(clone, "main"); err == nil {
		t.Fatal("the seeding push succeeded over a workspace that is ahead")
	} else if !strings.Contains(err.Error(), "fetch first") {
		t.Fatalf("git refused for another reason: %v", err)
	}

	cmp, err := CompareBranch(clone, "main")
	if err != nil {
		t.Fatalf("CompareBranch: %v", err)
	}
	if cmp.State != BranchBehind {
		t.Fatalf("state = %q, want %q", cmp.State, BranchBehind)
	}
	if cmp.Workspace != serverTip {
		t.Fatalf("workspace = %q, want %q", cmp.Workspace, serverTip)
	}
	if cmp.Ahead != 0 || cmp.Behind != 1 {
		t.Fatalf("ahead/behind = %d/%d, want 0/1", cmp.Ahead, cmp.Behind)
	}
}

func TestCompareBranchReportsDivergence(t *testing.T) {
	requireGit(t)
	clone, remote, _ := seededClone(t)
	serverTip := advance(t, remote)
	mine := commit(t, clone, "mine.txt", "my work\n")

	cmp, err := CompareBranch(clone, "main")
	if err != nil {
		t.Fatalf("CompareBranch: %v", err)
	}
	if cmp.State != BranchDiverged {
		t.Fatalf("state = %q, want %q", cmp.State, BranchDiverged)
	}
	if cmp.Local != mine || cmp.Workspace != serverTip {
		t.Fatalf("tips = %q / %q, want %q / %q", cmp.Local, cmp.Workspace, mine, serverTip)
	}
	if cmp.Ahead != 1 || cmp.Behind != 1 {
		t.Fatalf("ahead/behind = %d/%d, want 1/1", cmp.Ahead, cmp.Behind)
	}
}

func TestFastForwardAdvancesTheCheckedOutBranch(t *testing.T) {
	requireGit(t)
	clone, remote, _ := seededClone(t)
	serverTip := advance(t, remote)

	result, err := FastForward(clone, "main")
	if err != nil {
		t.Fatalf("FastForward: %v", err)
	}
	if !result.Current || result.Dirty {
		t.Fatalf("result = %+v, want the branch current and clean", result)
	}
	if result.Commit != serverTip {
		t.Fatalf("commit = %q, want %q", result.Commit, serverTip)
	}
	if got := git(t, clone, "rev-parse", "HEAD"); got != serverTip {
		t.Fatalf("HEAD = %s, want %s", got, serverTip)
	}
	// A fast-forward, never a merge: the history stays linear.
	if parents := git(t, clone, "rev-list", "--merges", "--count", "HEAD"); parents != "0" {
		t.Fatalf("the fast-forward created %s merge commits", parents)
	}
	// Nothing was pushed back: the workspace branch is where it was.
	if got := git(t, remote, "rev-parse", "main"); got != serverTip {
		t.Fatalf("workspace main = %s, want %s", got, serverTip)
	}
}

// A member working on their own branch keeps their working tree: only
// the base branch's ref moves, exactly as aether pull already does.
func TestFastForwardUpdatesTheRefWhenAnotherBranchIsCheckedOut(t *testing.T) {
	requireGit(t)
	clone, remote, base := seededClone(t)
	serverTip := advance(t, remote)
	git(t, clone, "switch", "-c", "feature")
	feature := commit(t, clone, "feature.txt", "wip\n")

	result, err := FastForward(clone, "main")
	if err != nil {
		t.Fatalf("FastForward: %v", err)
	}
	if result.Current {
		t.Fatal("result claims main was checked out")
	}
	if result.Commit != serverTip {
		t.Fatalf("commit = %q, want %q", result.Commit, serverTip)
	}
	if got := git(t, clone, "rev-parse", "main"); got != serverTip {
		t.Fatalf("main = %s, want %s", got, serverTip)
	}
	if got := git(t, clone, "rev-parse", "HEAD"); got != feature {
		t.Fatalf("HEAD moved off feature: %s, want %s", got, feature)
	}
	if base == serverTip {
		t.Fatal("the fixture never advanced the workspace")
	}
}

func TestFastForwardReportsADirtyWorktree(t *testing.T) {
	requireGit(t)
	clone, remote, _ := seededClone(t)
	advance(t, remote)
	// An edit git's fast-forward does not have to touch.
	if err := os.WriteFile(filepath.Join(clone, "scratch.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, clone, "add", "scratch.txt")

	result, err := FastForward(clone, "main")
	if err != nil {
		t.Fatalf("FastForward: %v", err)
	}
	if !result.Dirty {
		t.Fatalf("result = %+v, want a dirty worktree", result)
	}
}

func TestFastForwardRefusesDivergedBranches(t *testing.T) {
	requireGit(t)
	clone, remote, _ := seededClone(t)
	advance(t, remote)
	mine := commit(t, clone, "mine.txt", "my work\n")

	_, err := FastForward(clone, "main")
	if !errors.Is(err, ErrPushPrecondition) {
		t.Fatalf("err = %v, want a precondition refusal", err)
	}
	// The message says what resolves it, in the member's own repository.
	if !strings.Contains(err.Error(), "aether/main") {
		t.Fatalf("message = %q", err)
	}
	if got := git(t, clone, "rev-parse", "main"); got != mine {
		t.Fatalf("the refused fast-forward moved main to %s, want %s", got, mine)
	}
}

func TestFastForwardRefusesWhenThereIsNothingToCatchUpWith(t *testing.T) {
	requireGit(t)
	clone, _, _ := seededClone(t)

	_, err := FastForward(clone, "main")
	if !errors.Is(err, ErrPushPrecondition) {
		t.Fatalf("err = %v, want a precondition refusal", err)
	}
	if !strings.Contains(err.Error(), "already matches") {
		t.Fatalf("message = %q", err)
	}
}

// Git guards the working tree, and its refusal is the whole message: the
// member has an edit to a file the fast-forward would overwrite.
func TestFastForwardStopsOnAnEditTheCatchUpWouldOverwrite(t *testing.T) {
	requireGit(t)
	clone, remote, _ := seededClone(t)
	advance(t, remote)
	before := git(t, clone, "rev-parse", "main")
	if err := os.WriteFile(filepath.Join(clone, "server.txt"), []byte("my edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := FastForward(clone, "main")
	if err == nil {
		t.Fatal("FastForward overwrote an uncommitted change")
	}
	if !strings.Contains(err.Error(), "would be overwritten by merge") {
		t.Fatalf("error drops git's own words: %v", err)
	}
	// The member commits or stashes the edit; nothing about this is the
	// server's doing, so it refuses rather than reading as an internal
	// failure.
	if !errors.Is(err, ErrPushPrecondition) {
		t.Fatalf("err = %v, want a precondition refusal", err)
	}
	if after := git(t, clone, "rev-parse", "main"); after != before {
		t.Fatalf("the refused fast-forward moved main to %s, want %s", after, before)
	}
}

// The member's base branch tracks their own remote. Catching it up with
// the workspace moves the ref and nothing else: retargeting the upstream
// would silently redirect their next git pull to the workspace.
func TestFastForwardKeepsTheBranchUpstream(t *testing.T) {
	requireGit(t)
	clone, remote, _ := seededClone(t)
	git(t, clone, "remote", "add", "origin", remote)
	git(t, clone, "config", "branch.main.remote", "origin")
	git(t, clone, "config", "branch.main.merge", "refs/heads/main")
	advance(t, remote)
	git(t, clone, "switch", "-c", "feature")

	if _, err := FastForward(clone, "main"); err != nil {
		t.Fatalf("FastForward: %v", err)
	}
	if got := git(t, clone, "config", "--get", "branch.main.remote"); got != "origin" {
		t.Fatalf("branch.main.remote = %q, want origin", got)
	}
	if got := git(t, clone, "config", "--get", "branch.main.merge"); got != "refs/heads/main" {
		t.Fatalf("branch.main.merge = %q", got)
	}
}

// A member whose base branch is checked out in a second worktree. Git
// refuses to move a branch from outside the worktree that holds it, and
// that is a local state the member resolves, not a server failure.
func TestFastForwardRefusesABranchHeldByAnotherWorktree(t *testing.T) {
	requireGit(t)
	clone, remote, base := seededClone(t)
	advance(t, remote)
	git(t, clone, "switch", "-c", "feature")
	other := filepath.Join(t.TempDir(), "held")
	git(t, clone, "worktree", "add", other, "main")

	_, err := FastForward(clone, "main")
	if err == nil {
		t.Fatal("FastForward moved a branch another worktree has checked out")
	}
	if !errors.Is(err, ErrPushPrecondition) {
		t.Fatalf("err = %v, want a precondition refusal", err)
	}
	if !strings.Contains(err.Error(), other) || !strings.Contains(err.Error(), "main") {
		t.Fatalf("message names neither the worktree nor the branch: %q", err)
	}
	// Nothing was created here, so the message must not blame branch
	// creation, and it must say what the member does next.
	if strings.Contains(err.Error(), "create local branch") {
		t.Fatalf("message blames the wrong action: %q", err)
	}
	if !strings.Contains(err.Error(), "merge --ff-only") {
		t.Fatalf("message does not say what to do next: %q", err)
	}
	if got := git(t, clone, "rev-parse", "main"); got != base {
		t.Fatalf("main moved to %s, want %s", got, base)
	}
	if got := git(t, other, "rev-parse", "HEAD"); got != base {
		t.Fatalf("the other worktree moved to %s, want %s", got, base)
	}
}

// A worktree git can no longer find still holds the branch, so the
// refusal stands - but the directory it names is gone, and telling the
// member to run a merge in it would be telling them to run nothing.
func TestFastForwardNamesPruneForAWorktreeGitLost(t *testing.T) {
	requireGit(t)
	clone, remote, base := seededClone(t)
	advance(t, remote)
	git(t, clone, "switch", "-c", "feature")
	other := filepath.Join(t.TempDir(), "held")
	git(t, clone, "worktree", "add", other, "main")
	if err := os.RemoveAll(other); err != nil {
		t.Fatalf("remove the held worktree: %v", err)
	}

	_, err := FastForward(clone, "main")
	if !errors.Is(err, ErrPushPrecondition) {
		t.Fatalf("err = %v, want a precondition refusal", err)
	}
	if !strings.Contains(err.Error(), "worktree prune") {
		t.Fatalf("message does not name the prune that clears it: %q", err)
	}
	if strings.Contains(err.Error(), "merge --ff-only") {
		t.Fatalf("message sends the member into a directory that is gone: %q", err)
	}
	if got := git(t, clone, "rev-parse", "main"); got != base {
		t.Fatalf("main moved to %s, want %s", got, base)
	}
}

// git prints worktree paths raw, so a newline in one splits the porcelain
// listing across lines. The refusal has to name the whole path.
func TestFastForwardNamesAWorktreePathWithANewline(t *testing.T) {
	requireGit(t)
	clone, remote, _ := seededClone(t)
	advance(t, remote)
	git(t, clone, "switch", "-c", "feature")
	other := filepath.Join(t.TempDir(), "held\nbranch refs/heads/main")
	git(t, clone, "worktree", "add", other, "main")

	_, err := FastForward(clone, "main")
	if !errors.Is(err, ErrPushPrecondition) {
		t.Fatalf("err = %v, want a precondition refusal", err)
	}
	if !strings.Contains(err.Error(), other) {
		t.Fatalf("message does not name the whole worktree path: %q", err)
	}
}
