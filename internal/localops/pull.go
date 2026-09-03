package localops

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// PullResult describes the run branch after it has been fetched and either
// created locally or fast-forwarded.
type PullResult struct {
	Branch  string
	Ref     string
	Output  string
	Current bool
	Dirty   bool
}

// PullCommand builds the fetch that lands a run branch in repo under
// refs/remotes/aether/<branch>. Pull performs the follow-up branch operation.
func PullCommand(repo, user, addr string, coords protocol.RunPullResult) (string, *exec.Cmd, error) {
	if coords.Branch == "" {
		return "", nil, errors.New("run has no branch")
	}
	url := cli.GitURL(user, addr, coords.WorkspaceID)
	return coords.Branch, fetchCommand(repo, url, coords.Branch), nil
}

// Pull fetches a run branch, creates or updates its local branch, and reports
// whether that branch is current and whether the worktree is dirty.
func Pull(repo, user, addr string, coords protocol.RunPullResult) (PullResult, error) {
	if coords.Branch == "" {
		return PullResult{}, errors.New("run has no branch")
	}
	url := cli.GitURL(user, addr, coords.WorkspaceID)
	return pull(repo, url, coords.Branch)
}

// pull is the captured-output core, taking a resolved URL so filesystem
// remotes can exercise the same branch and merge behavior in unit tests.
func pull(repo, url, branch string) (PullResult, error) {
	result := PullResult{
		Branch: branch,
		Ref:    "refs/remotes/aether/" + branch,
	}
	ref, output, err := pullFetch(repo, url, branch)
	result.Ref, result.Output = ref, output
	if err != nil {
		return result, err
	}

	current, err := currentBranch(repo)
	if err != nil {
		return result, err
	}
	result.Current = current == branch

	var opOutput []byte
	if result.Current {
		opOutput, err = exec.Command("git", "-C", repo, "merge", "--ff-only", "aether/"+branch).CombinedOutput()
	} else {
		opOutput, err = exec.Command("git", "-C", repo, "branch", "--force", "--track", branch, "aether/"+branch).CombinedOutput()
	}
	result.Output += string(opOutput)
	if err != nil {
		action := "create local branch"
		if result.Current {
			action = "fast-forward branch"
		}
		return result, fmt.Errorf("git %s: %w: %s", action, err, strings.TrimSpace(string(opOutput)))
	}

	result.Dirty, err = worktreeDirty(repo)
	if err != nil {
		return result, err
	}
	return result, nil
}

// SwitchPull switches to an already fetched run branch. It never risks
// discarding local edits: a dirty worktree must be committed or stashed first.
func SwitchPull(repo, branch string) error {
	if branch == "" {
		return errors.New("run has no branch")
	}
	dirty, err := worktreeDirty(repo)
	if err != nil {
		return err
	}
	if dirty {
		return errors.New("working tree is dirty; commit or stash changes before switching branches")
	}
	out, err := exec.Command("git", "-C", repo, "switch", branch).CombinedOutput()
	if err != nil {
		return fmt.Errorf("git switch %s: %w: %s", branch, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func currentBranch(repo string) (string, error) {
	out, err := exec.Command("git", "-C", repo, "symbolic-ref", "--quiet", "--short", "HEAD").CombinedOutput()
	if err != nil {
		return "", nil // A detached HEAD is not the run branch.
	}
	return strings.TrimSpace(string(out)), nil
}

func worktreeDirty(repo string) (bool, error) {
	out, err := exec.Command("git", "-C", repo, "status", "--porcelain").CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("git status: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return len(strings.TrimSpace(string(out))) != 0, nil
}

// pullFetch is the fetch-only seam used by tests and by Pull.
func pullFetch(repo, url, branch string) (ref, output string, err error) {
	ref = "refs/remotes/aether/" + branch
	out, err := fetchCommand(repo, url, branch).CombinedOutput()
	output = string(out)
	if err != nil {
		return ref, output, fmt.Errorf("git fetch: %w: %s", err, strings.TrimSpace(output))
	}
	return ref, output, nil
}

// fetchCommand is the one fetch both surfaces run: no tags, one refspec
// landing the run branch under the aether remote-tracking namespace.
func fetchCommand(repo, url, branch string) *exec.Cmd {
	refspec := "+refs/heads/" + branch + ":refs/remotes/aether/" + branch
	return exec.Command("git", "-C", repo, "fetch", "--no-tags", url, refspec)
}
