package localops

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"time"
)

// ErrPushPrecondition marks a push the repository cannot even attempt:
// no commits, no such local branch, no `aether` remote. Callers map it to
// an invalid-state refusal the user fixes locally, as opposed to a push
// git tried and the server rejected.
var ErrPushPrecondition = errors.New("the repository is not ready to push")

// pushRefusal carries one precondition failure. Its own message is the
// whole user-facing sentence, so nothing prefixes it; errors.Is still
// matches ErrPushPrecondition for the caller's status mapping.
type pushRefusal struct{ msg string }

func (e pushRefusal) Error() string { return e.msg }

func (e pushRefusal) Is(target error) bool { return target == ErrPushPrecondition }

// pushTimeout bounds the seeding push. The first push of a real
// repository uploads every object over SSH, so the bound is generous;
// without one a stalled connection would hold the gateway request open
// forever.
const pushTimeout = 10 * time.Minute

// queryTimeout bounds the local git queries around the push. They read
// this machine's repository and answer at once, so a repository on a
// stalled network mount is the only way to exceed it.
const queryTimeout = 10 * time.Second

// Push seeds the workspace by running one `git push -u aether
// refs/heads/<branch>:refs/heads/<branch>` in repo. The refspec is
// fully qualified so the push can carry nothing but that one branch: it
// cannot be read as a force refspec, and it stays unambiguous when a tag
// shares the branch's name. `--no-follow-tags` holds even for a user
// whose `push.followTags` would otherwise send tags along.
//
// The branch must already exist locally and the `aether` remote must
// already be set (link.repo writes it).
//
// It returns everything git printed. On failure the output is folded
// into the error too, so the caller can show git's own words.
func Push(repo, branch string) (string, error) {
	if repo == "" {
		return "", errors.New("localops: repo path is required")
	}
	if !usableBranch(branch) {
		return "", fmt.Errorf("localops: %q is not a usable branch name", branch)
	}
	if err := pushPreflight(repo, branch); err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(context.Background(), pushTimeout)
	defer cancel()
	refspec := "refs/heads/" + branch + ":refs/heads/" + branch
	cmd := exec.CommandContext(ctx, "git", "-C", repo, "push", "--no-follow-tags", "-u", "aether", refspec)
	// Nothing here can answer git's own credential prompt. This does not
	// reach ssh, which asks for a key passphrase on its own terminal; the
	// push timeout is what bounds that case.
	cmd.Env = append(cmd.Environ(), "GIT_TERMINAL_PROMPT=0")
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.CombinedOutput()
	output := string(out)
	if err != nil {
		if ctx.Err() != nil {
			return output, fmt.Errorf("git push: gave up after %s: %s", pushTimeout, strings.TrimSpace(output))
		}
		return output, fmt.Errorf("git push: %w: %s", err, strings.TrimSpace(output))
	}
	return output, nil
}

// usableBranch rejects the names that would change what the refspec
// means rather than name a branch: `+` forces, `:` splits source from
// destination, `-` reads as a flag, and whitespace splits arguments. Git
// itself accepts a `+`-prefixed branch, so the check is not redundant.
func usableBranch(branch string) bool {
	if branch == "" || strings.ContainsAny(branch, "+:\t\n\r ") {
		return false
	}
	return !strings.HasPrefix(branch, "-")
}

// AetherRemoteURL returns the URL repo's `aether` remote points at, or
// "" when the repository has no such remote. The URL carries the
// workspace ID, so a caller can tell which workspace this repository is
// wired to before pushing to it.
func AetherRemoteURL(repo string) (string, error) {
	names, err := remotes(repo)
	if err != nil {
		return "", err
	}
	if !slices.Contains(names, "aether") {
		return "", nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", repo, "remote", "get-url", "aether").Output()
	if err != nil {
		return "", fmt.Errorf("git remote get-url aether: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// pushPreflight refuses, before git dials anything, the states a user
// can only fix in their own repository. Each refusal says what to do
// next.
func pushPreflight(repo, branch string) error {
	if err := gitQuiet(repo, "rev-parse", "--git-dir"); err != nil {
		return pushRefusal{repo + " is not a git repository"}
	}
	if err := gitQuiet(repo, "rev-parse", "--verify", "--quiet", "HEAD"); err != nil {
		return pushRefusal{repo + " has no commits yet; make your first commit, then push"}
	}
	if err := gitQuiet(repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err != nil {
		return pushRefusal{missingBranchMessage(repo, branch)}
	}
	names, err := remotes(repo)
	if err != nil {
		return fmt.Errorf("localops: read git remotes: %w", err)
	}
	if !slices.Contains(names, "aether") {
		return pushRefusal{repo + " has no aether remote yet; add it first with the Add remote button or `aether link --repo`"}
	}
	return nil
}

// missingBranchMessage names the branch the workspace expects and, when
// the repository is on a different one, the branch that is actually
// checked out - the fix is almost always one of the two names.
func missingBranchMessage(repo, branch string) string {
	msg := "there is no local branch named " + branch
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", repo, "branch", "--show-current").Output()
	if current := strings.TrimSpace(string(out)); err == nil && current != "" && current != branch {
		msg += ", but " + current + " is checked out; rename it or create " + branch
		return msg
	}
	return msg + "; create it, or set the workspace base branch to the one you use"
}

// remotes lists the names of repo's git remotes.
func remotes(repo string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", repo, "remote").Output()
	if err != nil {
		return nil, fmt.Errorf("git remote: %w", err)
	}
	listed := strings.TrimSpace(string(out))
	if listed == "" {
		return nil, nil
	}
	names := strings.Split(listed, "\n")
	for i, name := range names {
		names[i] = strings.TrimSpace(name)
	}
	return names, nil
}

// gitQuiet runs a read-only git query, reporting only whether it
// succeeded; the callers turn a failure into their own message.
func gitQuiet(repo string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	return exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...).Run()
}
