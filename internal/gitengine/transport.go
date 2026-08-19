package gitengine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// packWaitDelay bounds how long a pack process's exec.Cmd.Wait may block on
// its stdout/stderr copy goroutines after the git process is gone (killed by
// ctx cancellation or exited on its own).
const packWaitDelay = 3 * time.Second

// UploadPack serves a client fetch/clone: it execs git upload-pack on the
// workspace bare repo with stdio streamed to the arguments. Returns the git
// process exit code; err is non-nil only when the process could not be
// resolved or started.
func (e *Engine) UploadPack(ctx context.Context, ws domain.WorkspaceID, stdin io.Reader, stdout, stderr io.Writer) (exitCode int, err error) {
	return e.servePack(ctx, ws, "upload-pack", stdin, stdout, stderr)
}

// ReceivePack serves a client push: it execs git receive-pack on the
// workspace bare repo with stdio streamed to the arguments. Pushes to any
// branch are allowed, except that the repo's update hook (see engine.go)
// refuses non-fast-forward updates to server-owned aether/run-* branches.
// After a successful push it publishes git.branch for any watched run
// branch whose tip changed.
func (e *Engine) ReceivePack(ctx context.Context, ws domain.WorkspaceID, stdin io.Reader, stdout, stderr io.Writer) (exitCode int, err error) {
	before := e.watchedBranchTips(ctx, ws)
	code, err := e.servePack(ctx, ws, "receive-pack", stdin, stdout, stderr)
	if err == nil && code == 0 {
		if repo, rerr := e.existingRepoPath(ws); rerr == nil {
			e.adoptDefaultBranch(ctx, repo)
		}
		after := e.watchedBranchTips(ctx, ws)
		for run, tip := range after {
			if tip != "" && tip != before[run] {
				e.publishBranch(ctx, run, tip)
			}
		}
	}
	return code, err
}

func (e *Engine) servePack(ctx context.Context, ws domain.WorkspaceID, service string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	repo, err := e.existingRepoPath(ws)
	if err != nil {
		return -1, err
	}
	args := []string{"-c", "safe.directory=*"}
	if service == "receive-pack" {
		// The branch is the artifact: no push may delete one. Forced ref
		// updates stay allowed everywhere except aether/run-*, where the
		// repo's update hook refuses non-fast-forwards.
		args = append(args, "-c", "receive.denyDeletes=true")
	}
	cmd := exec.CommandContext(ctx, e.cfg.GitPath, append(args, service, repo)...)
	cmd.Env = gitEnv()
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.WaitDelay = packWaitDelay

	// Feed stdin through our own *os.File pipe instead of handing the
	// reader to exec: exec's managed stdin copy blocks Wait until the
	// reader returns, and not even WaitDelay can abandon it (it closes the
	// pipe, then still waits for the copy goroutine — which is stuck in
	// Read on the SSH channel, not in Write). A client that holds the
	// channel open, or ctx cancellation during a stalled exchange, would
	// pin the handler forever. With an *os.File the child reads the fd
	// directly and Wait never waits on stdin; our copy goroutine unwinds
	// when the channel closes, without blocking servePack's return.
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		return -1, fmt.Errorf("gitengine: stdin pipe: %w", err)
	}
	cmd.Stdin = stdinR
	go func() {
		_, _ = io.Copy(stdinW, stdin)
		_ = stdinW.Close()
	}()
	defer func() {
		_ = stdinR.Close()
		_ = stdinW.Close() // unblocks a copy stuck writing to a full pipe
	}()

	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrWaitDelay) {
			// git exited cleanly; only the stdio copies were abandoned
			// because the client kept the channel open.
			return 0, nil
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode(), nil
		}
		return -1, fmt.Errorf("gitengine: git %s: %w", service, err)
	}
	return 0, nil
}

// watchedBranchTips resolves the current bare-repo tip of every
// registry-known run branch in ws. Unborn branches map to "".
func (e *Engine) watchedBranchTips(ctx context.Context, ws domain.WorkspaceID) map[domain.RunID]string {
	repo, err := e.existingRepoPath(ws)
	if err != nil {
		return nil
	}
	e.mu.Lock()
	branches := make(map[domain.RunID]string)
	for run, info := range e.registry {
		if info.workspace == ws {
			branches[run] = info.branch
		}
	}
	e.mu.Unlock()

	tips := make(map[domain.RunID]string, len(branches))
	for run, branch := range branches {
		tip, err := e.git(ctx, repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
		if err != nil || !isSHA(tip) {
			tip = ""
		}
		tips[run] = tip
	}
	return tips
}

func isSHA(s string) bool {
	if len(s) < 40 {
		return false
	}
	for _, r := range s {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}
