package gitengine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/3xDevOps/Aether/internal/domain"
)

// Git config keys recorded in each run checkout at creation. They describe
// the checkout to whoever works inside it; the server never reads run
// identity back from them. The checkout is bind-mounted into the run
// container, so everything under it - .git/config included - is
// agent-writable. Authoritative identity lives in the sidecar (runMeta).
const (
	cfgBase      = "aether.base"      // fork-point sha (diff baseline)
	cfgBranch    = "aether.branch"    // the run's branch name
	cfgWorkspace = "aether.workspace" // owning workspace id
)

// runMeta is the server-owned identity of a run checkout: the workspace
// that owns it, the branch it publishes to, and the fork-point it diffs
// against. It is stored at <CheckoutsDir>/<runID>.json - beside the
// checkout directory, never inside it, because only
// <CheckoutsDir>/<runID> is mounted into the run container. Living on disk
// keeps every per-run operation self-describing from the run id alone, so
// the engine still needs no registration step after a restart.
type runMeta struct {
	Base      string             `json:"base"`
	Branch    string             `json:"branch"`
	Workspace domain.WorkspaceID `json:"workspace"`
}

func (e *Engine) runMetaPath(run domain.RunID) (string, error) {
	path, err := e.checkoutPath(run)
	if err != nil {
		return "", err
	}
	return path + ".json", nil
}

func (e *Engine) writeRunMeta(run domain.RunID, meta runMeta) error {
	path, err := e.runMetaPath(run)
	if err != nil {
		return err
	}
	data, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("gitengine: encode run metadata for %s: %w", run, err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("gitengine: write run metadata for %s: %w", run, err)
	}
	return nil
}

// readRunMeta loads a run's identity record. Checkouts created before the
// sidecar existed have none: their identity is only in the agent-writable
// checkout, so there is no safe fallback and the run can no longer publish
// or be watched. Its checkout and any already-published branch are kept.
func (e *Engine) readRunMeta(run domain.RunID) (runMeta, error) {
	path, err := e.runMetaPath(run)
	if err != nil {
		return runMeta{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return runMeta{}, fmt.Errorf("gitengine: run %s has no identity record: %w", run, err)
	}
	var meta runMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return runMeta{}, fmt.Errorf("gitengine: decode run metadata for %s: %w", run, err)
	}
	if meta.Base == "" || meta.Branch == "" || meta.Workspace == "" {
		return runMeta{}, fmt.Errorf("gitengine: run %s has an incomplete identity record", run)
	}
	return meta, nil
}

// checkBranchName defers to git for ref-name validity so a damaged identity
// record can never reach a refspec.
func (e *Engine) checkBranchName(ctx context.Context, branch string) error {
	if _, err := e.git(ctx, "", "check-ref-format", "refs/heads/"+branch); err != nil {
		return fmt.Errorf("gitengine: invalid run branch %q: %w", branch, err)
	}
	return nil
}

// CreateRunCheckout clones the workspace bare repo into the run's checkout
// (git clone --local: hard-linked objects, fully self-contained .git that
// works identically inside the run container) and creates the run branch
// from baseBranch. Errors if baseBranch has no commits or the checkout
// already exists.
func (e *Engine) CreateRunCheckout(ctx context.Context, ws domain.WorkspaceID, run domain.RunID, baseBranch, task string) (checkoutPath, branch string, err error) {
	repo, err := e.existingRepoPath(ws)
	if err != nil {
		return "", "", err
	}
	checkoutPath, err = e.checkoutPath(run)
	if err != nil {
		return "", "", err
	}
	if _, statErr := os.Stat(checkoutPath); statErr == nil {
		return "", "", fmt.Errorf("gitengine: checkout for run %s already exists", run)
	}
	base, err := e.git(ctx, repo, "rev-parse", "--verify", "refs/heads/"+baseBranch)
	if err != nil {
		return "", "", fmt.Errorf("gitengine: base branch %q has no commits: %w", baseBranch, err)
	}

	if _, cloneErr := e.git(ctx, "", "clone", "--local", "--no-checkout", repo, checkoutPath); cloneErr != nil {
		return "", "", cloneErr
	}
	cleanup := func() { _ = os.RemoveAll(checkoutPath) }

	branch, err = e.uniqueRunBranch(ctx, repo, base, run, task)
	if err != nil {
		cleanup()
		return "", "", err
	}
	// The name is reserved in the bare repo from here on, so every later
	// failure has to give it back. Leaving it claimed would push the next
	// run of the same task onto the full-ID form forever.
	cleanup = func() {
		_ = os.RemoveAll(checkoutPath)
		if _, delErr := e.git(ctx, repo, "update-ref", "-d", "refs/heads/"+branch, base); delErr != nil {
			slog.Warn("gitengine: release reserved run branch",
				"run", run, "branch", branch, "error", delErr)
		}
	}
	if _, err := e.git(ctx, checkoutPath, "checkout", "-b", branch, base); err != nil {
		cleanup()
		return "", "", err
	}
	for key, val := range map[string]string{
		cfgBase:      base,
		cfgBranch:    branch,
		cfgWorkspace: string(ws),
	} {
		if _, err := e.git(ctx, checkoutPath, "config", key, val); err != nil {
			cleanup()
			return "", "", err
		}
	}
	if err := e.writeRunMeta(run, runMeta{Base: base, Branch: branch, Workspace: ws}); err != nil {
		cleanup()
		return "", "", err
	}
	return checkoutPath, branch, nil
}

// uniqueRunBranch picks and reserves the run's branch name: the readable
// short form (aether/run-<slug>-<short-id>) unless that name is already
// claimed in the workspace, in which case the full run ID disambiguates
// it. Two runs of the same task can only collide on the short ID, and the
// full ID is unique by construction, so this terminates after one retry.
//
// The reservation is a ref write in the workspace bare repo rather than a
// read-then-use check. Publication is a forced fetch, so two runs that
// both saw the name free would not fail: the later one would silently
// overwrite the earlier one's branch, and the branch is the artifact.
// git update-ref --create-reflog with an all-zero old value fails when
// the ref already exists, which makes the claim atomic across concurrent
// provisioning.
func (e *Engine) uniqueRunBranch(ctx context.Context, repo, base string, run domain.RunID, task string) (string, error) {
	short := runBranch(task, shortID(run))
	claimed, err := e.claimBranch(ctx, repo, short, base)
	if err != nil {
		return "", err
	}
	if claimed {
		return short, nil
	}
	full := runBranch(task, string(run))
	claimed, err = e.claimBranch(ctx, repo, full, base)
	if err != nil {
		return "", err
	}
	if !claimed {
		return "", fmt.Errorf("gitengine: branch %s already exists for run %s", full, run)
	}
	return full, nil
}

// claimBranch creates refs/heads/<branch> at base, reporting false when
// the ref already exists. The all-zero old value makes this a create-only
// update: git rejects it rather than moving an existing ref.
func (e *Engine) claimBranch(ctx context.Context, repo, branch, base string) (bool, error) {
	const zero = "0000000000000000000000000000000000000000"
	_, err := e.git(ctx, repo, "update-ref", "refs/heads/"+branch, base, zero)
	if err == nil {
		return true, nil
	}
	exists, existsErr := e.branchExists(ctx, repo, branch)
	if existsErr != nil {
		return false, err
	}
	if exists {
		return false, nil
	}
	return false, err
}

// branchExists reports whether repo holds the exact branch ref. git
// rev-parse exits 1 for an unknown ref, which is an answer, not a failure.
func (e *Engine) branchExists(ctx context.Context, repo, branch string) (bool, error) {
	_, err := e.git(ctx, repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

// WorkspaceBranchExists reports whether the workspace bare repository
// contains the exact branch ref.
func (e *Engine) WorkspaceBranchExists(ctx context.Context, ws domain.WorkspaceID, branch string) (bool, error) {
	repo, err := e.existingRepoPath(ws)
	if err != nil {
		return false, err
	}
	return e.branchExists(ctx, repo, branch)
}

// CommitAll stages and commits everything in the run's checkout with the
// fixed Aether identity. Returns "", nil when the tree is clean.
//
// The checkout - .git included - is agent-writable, so anything in it
// that names a command to run is hostile input that must never execute
// as the server: hooks are pointed at an empty path, fsmonitor is
// disabled, attribute lookups read from the empty tree instead of the
// worktree so a planted .gitattributes cannot select a clean filter, and
// .git/info/attributes (which GIT_ATTR_SOURCE does not override) is
// removed outright - it is plumbing no agent legitimately writes.
func (e *Engine) CommitAll(ctx context.Context, run domain.RunID, message string) (commit string, err error) {
	checkout, err := e.existingCheckoutPath(run)
	if err != nil {
		return "", err
	}
	if rmErr := os.Remove(filepath.Join(checkout, ".git", "info", "attributes")); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
		return "", fmt.Errorf("gitengine: drop checkout attributes override: %w", rmErr)
	}
	// The empty tree exists implicitly in every repository, whatever its
	// object format, so it is resolved here rather than hardcoded.
	emptyTree, err := e.git(ctx, checkout, "hash-object", "-t", "tree", os.DevNull)
	if err != nil {
		return "", err
	}
	env := append(gitEnv(), "GIT_ATTR_SOURCE="+emptyTree)
	// core.fsmonitor in the checkout's config would be executed by add and
	// status; false disables it. Hooks likewise come from writable config.
	neutral := []string{"-c", "core.hooksPath=" + os.DevNull, "-c", "core.fsmonitor=false"}
	git := func(args ...string) (string, error) {
		return e.gitIn(ctx, checkout, env, append(neutral, args...)...)
	}
	if _, addErr := git("add", "-A"); addErr != nil {
		return "", addErr
	}
	status, err := git("status", "--porcelain")
	if err != nil {
		return "", err
	}
	if status == "" {
		return "", nil
	}
	if _, err := git(
		"-c", "user.name=Aether", "-c", "user.email=aether@localhost",
		"commit", "-m", message); err != nil {
		return "", err
	}
	return git("rev-parse", "HEAD")
}

// PublishRunBranch fetches the run's branch from its checkout into the
// workspace bare repo, making it fetchable by clients. Returns the branch
// tip sha. Publishes git.branch when the ref moved and the run's workspace
// is known from the watch registry.
func (e *Engine) PublishRunBranch(ctx context.Context, run domain.RunID) (commit string, err error) {
	checkout, err := e.existingCheckoutPath(run)
	if err != nil {
		return "", err
	}
	meta, err := e.readRunMeta(run)
	if err != nil {
		return "", err
	}
	if nameErr := e.checkBranchName(ctx, meta.Branch); nameErr != nil {
		return "", nameErr
	}
	repo, err := e.existingRepoPath(meta.Workspace)
	if err != nil {
		return "", err
	}

	ref := "refs/heads/" + meta.Branch
	before, _ := e.git(ctx, repo, "rev-parse", "--verify", "--quiet", ref)
	if _, fetchErr := e.git(ctx, repo, "fetch", "--quiet", checkout, "+"+ref+":"+ref); fetchErr != nil {
		return "", fetchErr
	}
	after, err := e.git(ctx, repo, "rev-parse", "--verify", ref)
	if err != nil {
		return "", err
	}
	if after != before {
		e.publishBranch(ctx, run, after)
	}
	return after, nil
}

// RemoveRunCheckout deletes the run's checkout directory, its identity
// sidecar, and its diff-snapshot store. It never touches the branch in the
// bare repo - the branch is the artifact. Idempotent on missing paths.
//
// The snapshot store lives exactly as long as the checkout, so per-interval
// diffs are reclaimed by the same scheduler GC that reclaims checkouts and
// nothing accrues once a run is cleaned up.
func (e *Engine) RemoveRunCheckout(ctx context.Context, run domain.RunID) error {
	checkout, err := e.checkoutPath(run)
	if err != nil {
		return err
	}
	meta, err := e.runMetaPath(run)
	if err != nil {
		return err
	}
	e.StopDiffWatch(run)
	if err := os.RemoveAll(checkout); err != nil {
		return fmt.Errorf("gitengine: remove checkout for run %s: %w", run, err)
	}
	if err := os.Remove(meta); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("gitengine: remove run metadata for %s: %w", run, err)
	}
	return e.removeSnapshotStore(run)
}
