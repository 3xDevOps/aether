package localops

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
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

// Push seeds the workspace by running `git -C repo push -u aether
// <branch>`: the one command the onboarding docs tell users to run. It
// never forces and never pushes a second ref. The branch must already
// exist locally and the `aether` remote must already be set (link.repo
// writes it).
//
// It returns everything git printed. On failure the output is folded
// into the error too, so the caller can show git's own words.
func Push(repo, branch string) (string, error) {
	if repo == "" {
		return "", errors.New("localops: repo path is required")
	}
	// A branch name starting with a dash would be read as a git flag.
	if branch == "" || strings.HasPrefix(branch, "-") {
		return "", fmt.Errorf("localops: %q is not a usable branch name", branch)
	}
	if err := pushPreflight(repo, branch); err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(context.Background(), pushTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", repo, "push", "-u", "aether", branch)
	// Nothing can answer a credential or host-key prompt from here, so a
	// missing key must fail loudly instead of hanging on stdin.
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

// pushPreflight refuses, before git dials anything, the three states a
// user can only fix in their own repository. Each refusal says what to
// do next.
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
	if !hasAetherRemote(repo) {
		return pushRefusal{repo + " has no aether remote yet; add it first with the Add remote button or `aether link --repo`"}
	}
	return nil
}

// missingBranchMessage names the branch the workspace expects and, when
// the repository is on a different one, the branch that is actually
// checked out - the fix is almost always one of the two names.
func missingBranchMessage(repo, branch string) string {
	msg := "there is no local branch named " + branch
	out, err := exec.Command("git", "-C", repo, "branch", "--show-current").Output()
	if current := strings.TrimSpace(string(out)); err == nil && current != "" && current != branch {
		msg += ", but " + current + " is checked out; rename it or create " + branch
		return msg
	}
	return msg + "; create it, or set the workspace base branch to the one you use"
}

// hasAetherRemote reports whether repo already carries the remote
// link.repo writes. GitRemote adds it; this only reads.
func hasAetherRemote(repo string) bool {
	out, err := exec.Command("git", "-C", repo, "remote").Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(line) == "aether" {
			return true
		}
	}
	return false
}

// gitQuiet runs a read-only git query, reporting only whether it
// succeeded; the callers turn a failure into their own message.
func gitQuiet(repo string, args ...string) error {
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	return cmd.Run()
}
