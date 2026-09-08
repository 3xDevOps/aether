package localops

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// BranchState names how the linked clone's branch stands against the
// workspace's copy of it. A seeding push is only honest in two of these
// states; the other two are the user's to resolve, because the whole
// point of the workspace remote is that the member's history stays
// theirs - nothing here merges, rebases or forces on their behalf.
type BranchState string

const (
	// BranchMissing is a workspace whose repository does not carry the
	// branch yet. That is a fresh workspace, and the push seeds it.
	BranchMissing BranchState = "missing"
	// BranchSame is both tips at one commit: the branch is already there.
	BranchSame BranchState = "same"
	// BranchAhead is the ordinary seeding push: the clone carries every
	// commit the workspace has, and more.
	BranchAhead BranchState = "ahead"
	// BranchBehind is a member cloning a workspace someone else seeded.
	// A push would be rejected with "fetch first"; a fast-forward of the
	// local branch is the answer.
	BranchBehind BranchState = "behind"
	// BranchDiverged is both sides carrying commits the other does not.
	BranchDiverged BranchState = "diverged"
)

// BranchComparison is what the workspace and the clone each hold for one
// branch. Ahead and Behind count commits from the clone's point of view
// and are zero unless the state makes them meaningful.
type BranchComparison struct {
	State     BranchState
	Local     string
	Workspace string
	Ahead     int
	Behind    int
	Output    string
}

// CompareBranch fetches the workspace's copy of branch into
// refs/remotes/aether/<branch> and reports how the local branch stands
// against it. It refuses the same local states Push refuses, so a caller
// that compares before pushing reaches those refusals without dialing.
func CompareBranch(repo, branch string) (BranchComparison, error) {
	if repo == "" {
		return BranchComparison{}, pushRefusal{"no repository is linked; link a repository before pushing"}
	}
	if !usableBranch(branch) {
		return BranchComparison{}, fmt.Errorf("localops: %q is not a usable branch name", branch)
	}
	if err := pushPreflight(repo, branch); err != nil {
		return BranchComparison{}, err
	}

	local, err := gitLine(repo, "rev-parse", "refs/heads/"+branch)
	if err != nil {
		return BranchComparison{}, err
	}
	cmp := BranchComparison{State: BranchMissing, Local: local}

	ctx, cancel := context.WithTimeout(context.Background(), pushTimeout)
	defer cancel()
	workspace, err := workspaceTip(ctx, repo, branch)
	if err != nil {
		return cmp, err
	}
	if workspace == "" {
		return cmp, nil
	}
	// ls-remote answered a tip, so the objects behind it are what the
	// ancestry questions below need locally.
	cmp.Output, err = runRemoteGit(ctx, repo, "fetch", "--no-tags", "aether",
		"+refs/heads/"+branch+":refs/remotes/aether/"+branch)
	if err != nil {
		return cmp, err
	}
	cmp.Workspace, err = gitLine(repo, "rev-parse", "refs/remotes/aether/"+branch)
	if err != nil {
		return cmp, err
	}
	if cmp.Workspace == cmp.Local {
		cmp.State = BranchSame
		return cmp, nil
	}
	cmp.Ahead, cmp.Behind, err = aheadBehind(repo, branch)
	if err != nil {
		return cmp, err
	}
	switch {
	case cmp.Behind == 0:
		cmp.State = BranchAhead
	case cmp.Ahead == 0:
		cmp.State = BranchBehind
	default:
		cmp.State = BranchDiverged
	}
	return cmp, nil
}

// FastForwardResult describes the local branch after it caught up with
// the workspace. Current says whether that branch was the checked-out
// one; when it was not, only the ref moved and the working tree was left
// alone.
type FastForwardResult struct {
	Branch  string
	Commit  string
	Current bool
	Dirty   bool
	Output  string
}

// FastForward advances the local branch to the workspace's copy of it.
// It is fast-forward only: every state but BranchBehind is refused, so a
// divergence is resolved by the member, in their own repository, with
// their own choice of rebase or merge.
func FastForward(repo, branch string) (FastForwardResult, error) {
	cmp, err := CompareBranch(repo, branch)
	result := FastForwardResult{Branch: branch, Commit: cmp.Local, Output: cmp.Output}
	if err != nil {
		return result, err
	}
	if cmp.State != BranchBehind {
		return result, pushRefusal{notBehindMessage(cmp.State, branch)}
	}

	// The member's base branch keeps whatever upstream it already had:
	// theirs points at their own remote, and moving it would redirect
	// their next git pull to the workspace.
	current, output, err := advanceBranch(repo, branch, false)
	result.Current, result.Output = current, result.Output+output
	if err != nil {
		// git guards the working tree here, and an uncommitted change
		// the fast-forward would overwrite is the common case. Every
		// failure of this local step is fixed in this repository rather
		// than on the server, so it reads as a refusal, not an internal
		// error - git's own words carry through either way.
		return result, pushRefusal{err.Error()}
	}
	if result.Commit, err = gitLine(repo, "rev-parse", "refs/heads/"+branch); err != nil {
		return result, err
	}
	result.Dirty, err = worktreeDirty(repo)
	return result, err
}

// notBehindMessage says why there is nothing to fast-forward, and what
// the member does instead.
func notBehindMessage(state BranchState, branch string) string {
	switch state {
	case BranchMissing:
		return "the workspace has no branch named " + branch + " yet; push it instead"
	case BranchSame:
		return branch + " already matches the workspace; there is nothing to fast-forward"
	case BranchAhead:
		return branch + " is ahead of the workspace; push it instead"
	default:
		return branch + " and the workspace have both moved on, so no fast-forward is possible; " +
			"rebase onto aether/" + branch + " or merge it, then push"
	}
}

// workspaceTip reads the workspace's tip for branch without moving any
// objects. An empty answer is a workspace whose repository has no such
// branch yet, which is not a failure - that is what a fresh workspace
// looks like. ls-remote matches its pattern against the tail of a ref
// name, so the exact ref is picked out of the answer.
func workspaceTip(ctx context.Context, repo, branch string) (string, error) {
	out, err := runRemoteGit(ctx, repo, "ls-remote", "--heads", "aether", "refs/heads/"+branch)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		sha, ref, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if ok && strings.TrimSpace(ref) == "refs/heads/"+branch {
			return sha, nil
		}
	}
	return "", nil
}

// aheadBehind counts the commits each side carries that the other does
// not, in one query: git prints them as "<ahead>\t<behind>".
func aheadBehind(repo, branch string) (int, int, error) {
	counts, err := gitLine(repo, "rev-list", "--left-right", "--count",
		"refs/heads/"+branch+"...refs/remotes/aether/"+branch)
	if err != nil {
		return 0, 0, err
	}
	left, right, ok := strings.Cut(counts, "\t")
	if !ok {
		return 0, 0, fmt.Errorf("localops: git rev-list --count answered %q", counts)
	}
	ahead, err := strconv.Atoi(strings.TrimSpace(left))
	if err != nil {
		return 0, 0, fmt.Errorf("localops: git rev-list --count answered %q: %w", counts, err)
	}
	behind, err := strconv.Atoi(strings.TrimSpace(right))
	if err != nil {
		return 0, 0, fmt.Errorf("localops: git rev-list --count answered %q: %w", counts, err)
	}
	return ahead, behind, nil
}

// runRemoteGit runs one git command that dials the `aether` remote, under
// the same discipline as the seeding push: the ten-minute bound, and
// GIT_TERMINAL_PROMPT=0 because nothing here can answer git's own
// credential prompt.
func runRemoteGit(ctx context.Context, repo string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...)
	cmd.Env = append(cmd.Environ(), "GIT_TERMINAL_PROMPT=0")
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.CombinedOutput()
	output := string(out)
	if err != nil {
		if ctx.Err() != nil {
			return output, fmt.Errorf("git %s: gave up after %s: %s", args[0], pushTimeout, strings.TrimSpace(output))
		}
		return output, fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(output))
	}
	return output, nil
}

// gitLine runs a read-only git query and returns its trimmed answer,
// carrying git's own words on failure.
func gitLine(repo string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}
