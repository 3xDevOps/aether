package syncd

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

const gitTimeout = 2 * time.Minute

// runBranchRefspec mirrors the server-owned run branches into the
// remote-tracking namespace. Forced (+) because these refs are the
// server's to move; and remote-tracking refs only - the local working
// tree and local branches are never touched.
func runBranchRefspec(remote string) string {
	return "+refs/heads/aether/run-*:refs/remotes/" + remote + "/aether/run-*"
}

// git runs one git subcommand in the local repo and returns trimmed stdout.
func (d *Daemon) git(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, d.cfg.GitPath, append([]string{"-C", d.cfg.RepoPath}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", args[0], msg)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// fetchRuns fetches every aether/run-* branch from the server remote into
// refs/remotes/<remote>/aether/run-*.
func (d *Daemon) fetchRuns(ctx context.Context) error {
	_, err := d.git(ctx, "fetch", "--quiet", "--no-tags", d.cfg.Remote, runBranchRefspec(d.cfg.Remote))
	return err
}

// pushBase pushes the local base branch to the server when its tip moved
// since the last successful push. Not forced: the server refuses
// non-fast-forward pushes to run branches, and the base branch should
// never be rewritten silently either.
func (d *Daemon) pushBase(ctx context.Context) error {
	tip, err := d.git(ctx, "rev-parse", "--quiet", "--verify", "refs/heads/"+d.cfg.BaseBranch)
	if err != nil {
		// No local base branch - nothing to push.
		return nil
	}
	if tip == d.lastPushed {
		return nil
	}
	if _, err := d.git(ctx, "push", "--quiet", d.cfg.Remote,
		"refs/heads/"+d.cfg.BaseBranch+":refs/heads/"+d.cfg.BaseBranch); err != nil {
		return err
	}
	d.lastPushed = tip
	return nil
}

// originRemote is the conventional upstream remote name syncOrigin works
// against. The laptop clone already holds origin's credentials, which the
// server does not have; the fetch lands in the remote-tracking ref and the
// push goes straight from there, so the local branch and the working tree
// are never touched.
const originRemote = "origin"

// forwardOriginBase fast-forwards the server's base branch to the origin
// remote's tip, so runs branched server-side start from upstream's current
// reality instead of the last state a member pushed. The push is not
// forced: a server base ahead of origin (local work pushed up first) or
// diverged from it stays as it is.
func (d *Daemon) forwardOriginBase(ctx context.Context) error {
	if _, err := d.git(ctx, "fetch", "--quiet", "--no-tags", originRemote, d.cfg.BaseBranch); err != nil {
		return err
	}
	_, err := d.git(ctx, "push", "--quiet", d.cfg.Remote,
		"refs/remotes/"+originRemote+"/"+d.cfg.BaseBranch+":refs/heads/"+d.cfg.BaseBranch)
	return err
}
