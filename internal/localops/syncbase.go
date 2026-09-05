package localops

import (
	"context"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"time"
)

// SyncBase fetches branch from origin and advances the matching branch on the
// aether remote without touching the local branch or working tree.
func SyncBase(repo, branch string) (string, error) {
	if repo == "" {
		return "", pushRefusal{"no repository is linked; link a repository before syncing"}
	}
	if !usableBranch(branch) {
		return "", fmt.Errorf("localops: %q is not a usable branch name", branch)
	}
	if err := syncBasePreflight(repo); err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(context.Background(), pushTimeout)
	defer cancel()

	fetch := exec.CommandContext(ctx, "git", "-C", repo, "fetch", "--no-tags", "origin", branch)
	fetch.Env = append(fetch.Environ(), "GIT_TERMINAL_PROMPT=0")
	fetch.WaitDelay = 5 * time.Second
	fetchOut, err := fetch.CombinedOutput()
	output := strings.TrimSpace(string(fetchOut))
	if err != nil {
		if ctx.Err() != nil {
			return output, fmt.Errorf("git fetch: gave up after %s: %s", pushTimeout, output)
		}
		return output, fmt.Errorf("git fetch: %w: %s", err, output)
	}

	refspec := "refs/remotes/origin/" + branch + ":refs/heads/" + branch
	push := exec.CommandContext(ctx, "git", "-C", repo, "push", "aether", refspec)
	push.Env = append(push.Environ(), "GIT_TERMINAL_PROMPT=0")
	push.WaitDelay = 5 * time.Second
	pushOut, err := push.CombinedOutput()
	if pushText := strings.TrimSpace(string(pushOut)); pushText != "" {
		if output == "" {
			output = pushText
		} else {
			output += "\n" + pushText
		}
	}
	if err != nil {
		if ctx.Err() != nil {
			return output, fmt.Errorf("git push: gave up after %s: %s", pushTimeout, output)
		}
		return output, fmt.Errorf("git push: %w: %s", err, output)
	}
	return output, nil
}

func syncBasePreflight(repo string) error {
	if err := gitQuiet(repo, "rev-parse", "--git-dir"); err != nil {
		return pushRefusal{repo + " is not a git repository"}
	}
	names, err := remotes(repo)
	if err != nil {
		return fmt.Errorf("localops: read git remotes: %w", err)
	}
	if !slices.Contains(names, "origin") {
		return pushRefusal{repo + " has no origin remote yet; add it first with `git remote add origin <url>`"}
	}
	if !slices.Contains(names, "aether") {
		return pushRefusal{repo + " has no aether remote yet; add it first with the Add remote button or `aether link --repo`"}
	}
	return nil
}
