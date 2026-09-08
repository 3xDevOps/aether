package localops

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/shellquote"
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

	current, opOutput, err := advanceBranch(repo, branch, true)
	result.Current, result.Output = current, result.Output+opOutput
	if err != nil {
		return result, err
	}
	result.Dirty, err = worktreeDirty(repo)
	if err != nil {
		return result, err
	}
	return result, nil
}

// advanceBranch moves branch onto the already fetched
// refs/remotes/aether/<branch>: a fast-forward merge when that branch is
// checked out, and otherwise a ref update that leaves the working tree
// alone. It reports whether the branch was the checked-out one and
// everything git printed.
//
// track points the branch's upstream at the aether remote. A run branch
// wants that; a member's own base branch does not, because its upstream
// is their real remote and moving it would redirect their next git pull.
func advanceBranch(repo, branch string, track bool) (bool, string, error) {
	current := currentBranch(repo) == branch
	if !current {
		if held := branchWorktree(repo, branch); held != nil {
			return false, "", pushRefusal{held.refusal(repo, branch)}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), pushTimeout)
	defer cancel()
	var out []byte
	var err error
	if current {
		out, err = exec.CommandContext(ctx, "git", "-C", repo, "merge", "--ff-only", "aether/"+branch).CombinedOutput()
	} else {
		args := []string{"-C", repo, "branch", "--force"}
		tracked := track && remoteExists(repo, "aether")
		if tracked {
			args = append(args, "--track")
		} else {
			// git's own branch.autoSetupMerge defaults to setting an
			// upstream whenever the start point is a remote-tracking
			// branch, so leaving --track off is not enough to leave the
			// member's upstream alone.
			args = append(args, "--no-track")
		}
		args = append(args, branch, "aether/"+branch)
		out, err = exec.CommandContext(ctx, "git", args...).CombinedOutput()
		if err != nil && tracked {
			fallback := exec.CommandContext(ctx, "git", "-C", repo, "branch", "--force", branch, "aether/"+branch)
			var fallbackOutput []byte
			fallbackOutput, err = fallback.CombinedOutput()
			out = append(out, fallbackOutput...)
		}
	}
	if err != nil {
		action := "create local branch"
		if current {
			action = "fast-forward branch"
		}
		return current, string(out), fmt.Errorf("git %s: %w: %s", action, err, strings.TrimSpace(string(out)))
	}
	return current, string(out), nil
}

// heldWorktree is the worktree that holds a branch this repository may
// not move. Prunable means git has lost the directory but still counts
// the branch as checked out there, which is what decides the fix.
type heldWorktree struct {
	path     string
	prunable bool
}

// refusal is the whole user-facing sentence for a branch held elsewhere.
// A live worktree is a place the member can catch the branch up; a
// prunable one is a directory that no longer exists, so telling them to
// run anything in it would be telling them to run nothing.
//
// The commands are written to be pasted, so every value in one is a shell
// argument: a worktree path holding a space would otherwise split, and a
// branch name may legally carry `$`, `;` and parentheses, which git's own
// ref rules allow and a shell does not ignore. The path in the opening
// sentence is prose rather than a command, so it stays bare.
func (h heldWorktree) refusal(repo, branch string) string {
	msg := branch + " is checked out in the worktree at " + h.path +
		"; git will not move a branch from outside the worktree that holds it. "
	if h.prunable {
		return msg + "Git can no longer find that directory, so run `git -C " + shellquote.Quote(repo) +
			" worktree prune` to drop the record, then try again."
	}
	return msg + "Switch that worktree to another branch, or run `git -C " +
		shellquote.Quote(h.path) + " merge --ff-only " + shellquote.Quote("aether/"+branch) + "` there."
}

// branchWorktree names a linked worktree of repo that has branch checked
// out, or nil when none does. Git refuses `git branch --force` for a
// branch checked out anywhere in the repository, so the move has to be
// refused before it is attempted; this repository's own working tree is
// not one of those, because there the branch is fast-forwarded in place.
// A lookup git itself cannot answer returns nil deliberately: the move
// then runs and git's own refusal is what the member reads.
//
// `-z` is what makes the answer parseable: git prints worktree paths
// raw, so a path holding a newline splits across lines without it.
func branchWorktree(repo, branch string) *heldWorktree {
	out, err := exec.Command("git", "-C", repo, "worktree", "list", "--porcelain", "-z").Output()
	if err != nil {
		return nil
	}
	self, _ := gitLine(repo, "rev-parse", "--show-toplevel")
	var held heldWorktree
	var wanted bool
	// Every record, the last one included, ends in the empty line git
	// writes between them, and `prunable` follows `branch`, so a record
	// is only answered once it is whole.
	for _, line := range strings.Split(string(out), "\x00") {
		switch {
		case line == "":
			if wanted {
				return &held
			}
			held, wanted = heldWorktree{}, false
		case strings.HasPrefix(line, "worktree "):
			held.path = strings.TrimPrefix(line, "worktree ")
		case line == "prunable" || strings.HasPrefix(line, "prunable "):
			held.prunable = true
		case line == "branch refs/heads/"+branch && held.path != self:
			wanted = true
		}
	}
	return nil
}

func remoteExists(repo, remote string) bool {
	return exec.Command("git", "-C", repo, "remote", "get-url", remote).Run() == nil
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
		return errors.New("working tree has uncommitted changes; commit or stash them first")
	}
	out, err := exec.Command("git", "-C", repo, "switch", branch).CombinedOutput()
	if err != nil {
		return fmt.Errorf("git switch %s: %w: %s", branch, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func currentBranch(repo string) string {
	out, err := exec.Command("git", "-C", repo, "symbolic-ref", "--quiet", "--short", "HEAD").CombinedOutput()
	if err != nil {
		return "" // A detached HEAD is not the run branch.
	}
	return strings.TrimSpace(string(out))
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
