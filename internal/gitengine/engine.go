// Package gitengine owns all server-side git mechanics: one bare repo per
// workspace, one self-contained local-clone checkout per run, the SSH git
// transport (upload-pack / receive-pack), and the diff-snapshot watcher that
// turns file-change quiescence into run.diff and git.branch events.
//
// It is policy-free glue around the system git binary: checkout GC policy
// lives in the scheduler (gitengine only provides RemoveRunCheckout), and
// branches are never deleted - the branch is the artifact.
package gitengine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
)

var (
	ErrRepoNotFound     = errors.New("gitengine: workspace repo not found")
	ErrCheckoutNotFound = errors.New("gitengine: run checkout not found")
)

// Config configures an Engine. ReposDir and CheckoutsDir are required; the
// rest defaults sensibly.
type Config struct {
	ReposDir          string     // <data>/repos
	CheckoutsDir      string     // <data>/checkouts
	GitPath           string     // "git"
	Bus               events.Bus // run.diff + git.branch; may be nil in tests
	OnBranchPublished func(run domain.RunID, commit string, at time.Time)
	QuietPeriod       time.Duration // default 2s
	MinInterval       time.Duration // default 10s
	MaxInterval       time.Duration // default 60s
}

// runInfo is a watch-registry entry. Entries are created by StartDiffWatch
// and survive StopDiffWatch for the engine's lifetime: they carry the
// workspace scope git.branch events are published under.
type runInfo struct {
	workspace domain.WorkspaceID
	branch    string
}

// Engine is the git engine. Its exported method set satisfies the
// scheduler's GitEngine and sshd's GitTransport seam interfaces.
type Engine struct {
	cfg Config

	mu       sync.Mutex
	watches  map[domain.RunID]*diffWatch
	registry map[domain.RunID]runInfo
	closed   bool
}

// New validates cfg, applies defaults, and creates the repos and checkouts
// directories.
func New(cfg Config) (*Engine, error) {
	if cfg.ReposDir == "" || cfg.CheckoutsDir == "" {
		return nil, errors.New("gitengine: ReposDir and CheckoutsDir are required")
	}
	if cfg.GitPath == "" {
		cfg.GitPath = "git"
	}
	if cfg.QuietPeriod <= 0 {
		cfg.QuietPeriod = 2 * time.Second
	}
	if cfg.MinInterval <= 0 {
		cfg.MinInterval = 10 * time.Second
	}
	if cfg.MaxInterval <= 0 {
		cfg.MaxInterval = 60 * time.Second
	}
	for _, dir := range []string{cfg.ReposDir, cfg.CheckoutsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("gitengine: create %s: %w", dir, err)
		}
	}
	return &Engine{
		cfg:      cfg,
		watches:  make(map[domain.RunID]*diffWatch),
		registry: make(map[domain.RunID]runInfo),
	}, nil
}

// Close stops all diff watchers and waits for them to finish.
func (e *Engine) Close() error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = true
	watches := make([]*diffWatch, 0, len(e.watches))
	for _, w := range e.watches {
		watches = append(watches, w)
	}
	e.watches = make(map[domain.RunID]*diffWatch)
	e.mu.Unlock()
	for _, w := range watches {
		w.stop()
	}
	return nil
}

// InitWorkspaceRepo creates the workspace's bare repo (git init --bare) and
// applies the repo settings every workspace repo must have (reflogs, the
// run-branch update hook); idempotent, so pre-existing repos converge on
// first touch. Importing content is a normal client git push through
// ReceivePack - there is no separate import API.
func (e *Engine) InitWorkspaceRepo(ctx context.Context, ws domain.WorkspaceID) (string, error) {
	path, err := e.repoPath(ws)
	if err != nil {
		return "", err
	}
	if !isBareRepo(path) {
		if _, err := e.git(ctx, "", "init", "--bare", "--initial-branch=main", path); err != nil {
			return "", err
		}
	}
	if err := e.configureWorkspaceRepo(ctx, path); err != nil {
		return "", err
	}
	return path, nil
}

// updateHook is installed as hooks/update in every workspace bare repo. Run
// branches are server-owned artifacts: a client push may create or
// fast-forward refs/heads/aether/run-* but never rewrite one. Deletions are
// refused wholesale by receive.denyDeletes (transport.go). Update hooks only
// fire under receive-pack, so PublishRunBranch's direct fetch into the bare
// repo rewrites run branches unimpeded.
const updateHook = `#!/bin/sh
# Installed by Aether; do not edit - rewritten when the workspace is touched.
refname="$1" oldrev="$2" newrev="$3"
case "$refname" in refs/heads/aether/run-*) ;; *) exit 0 ;; esac
zero="0000000000000000000000000000000000000000"
[ "$oldrev" = "$zero" ] && exit 0 # branch creation is allowed
[ "$newrev" = "$zero" ] && exit 0 # deletion: receive.denyDeletes decides
git merge-base --is-ancestor "$oldrev" "$newrev" && exit 0
echo "aether: rejected non-fast-forward push to $refname: run branches are owned by the Aether server" >&2
exit 1
`

// configureWorkspaceRepo idempotently applies required workspace-repo
// settings: core.logAllRefUpdates=true (bare repos disable reflogs by
// default, making forced ref rewrites unrecoverable) and the run-branch
// protection update hook. Checks before writing so the steady state is
// read-only: InitWorkspaceRepo runs on every transport touch and concurrent
// pushes must not race a hook rewrite or a git-config lock.
func (e *Engine) configureWorkspaceRepo(ctx context.Context, repo string) error {
	if v, _ := e.git(ctx, repo, "config", "--type=bool", "core.logAllRefUpdates"); v != "true" {
		if _, err := e.git(ctx, repo, "config", "core.logAllRefUpdates", "true"); err != nil {
			return err
		}
	}
	hook := filepath.Join(repo, "hooks", "update")
	if cur, err := os.ReadFile(hook); err == nil && string(cur) == updateHook {
		if fi, err := os.Stat(hook); err == nil && fi.Mode().Perm()&0o100 != 0 {
			return nil
		}
	}
	// Install atomically: a concurrent receive-pack must never exec a
	// half-written hook.
	tmp, err := os.CreateTemp(filepath.Dir(hook), "update-*.tmp")
	if err != nil {
		return fmt.Errorf("gitengine: write update hook: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.WriteString(updateHook); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("gitengine: write update hook: %w", err)
	}
	if err := tmp.Chmod(0o755); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("gitengine: write update hook: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("gitengine: write update hook: %w", err)
	}
	if err := os.Rename(tmp.Name(), hook); err != nil {
		return fmt.Errorf("gitengine: write update hook: %w", err)
	}
	return nil
}

// adoptDefaultBranch points a bare repo's HEAD at an existing branch when
// HEAD is still unborn, so clones of a freshly imported workspace check out
// the imported default branch whatever the client called it. Prefers
// main/master, else the first branch.
func (e *Engine) adoptDefaultBranch(ctx context.Context, repo string) {
	head, err := e.git(ctx, repo, "symbolic-ref", "HEAD")
	if err != nil {
		return
	}
	if _, resolveErr := e.git(ctx, repo, "rev-parse", "--verify", "--quiet", head); resolveErr == nil {
		return // HEAD already points at a real branch
	}
	refs, err := e.git(ctx, repo, "for-each-ref", "--format=%(refname)", "refs/heads")
	if err != nil || refs == "" {
		return
	}
	branches := strings.Split(refs, "\n")
	target := branches[0]
	for _, want := range []string{"refs/heads/main", "refs/heads/master"} {
		if slices.Contains(branches, want) {
			target = want
			break
		}
	}
	_, _ = e.git(ctx, repo, "symbolic-ref", "HEAD", target)
}

// repoPath validates ws and returns the bare repo path without checking
// existence.
func (e *Engine) repoPath(ws domain.WorkspaceID) (string, error) {
	if err := validateID(string(ws)); err != nil {
		return "", fmt.Errorf("gitengine: workspace id %q: %w", ws, err)
	}
	return filepath.Join(e.cfg.ReposDir, string(ws)+".git"), nil
}

// existingRepoPath is repoPath plus an existence check.
func (e *Engine) existingRepoPath(ws domain.WorkspaceID) (string, error) {
	path, err := e.repoPath(ws)
	if err != nil {
		return "", err
	}
	if !isBareRepo(path) {
		return "", fmt.Errorf("%w: %s", ErrRepoNotFound, ws)
	}
	return path, nil
}

// checkoutPath validates run and returns the checkout path without checking
// existence.
func (e *Engine) checkoutPath(run domain.RunID) (string, error) {
	if err := validateID(string(run)); err != nil {
		return "", fmt.Errorf("gitengine: run id %q: %w", run, err)
	}
	return filepath.Join(e.cfg.CheckoutsDir, string(run)), nil
}

// existingCheckoutPath is checkoutPath plus an existence check.
func (e *Engine) existingCheckoutPath(run domain.RunID) (string, error) {
	path, err := e.checkoutPath(run)
	if err != nil {
		return "", err
	}
	if fi, err := os.Stat(path); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("%w: %s", ErrCheckoutNotFound, run)
	}
	return path, nil
}

func isBareRepo(path string) bool {
	fi, err := os.Stat(filepath.Join(path, "HEAD"))
	return err == nil && fi.Mode().IsRegular()
}

// validateID rejects identifiers that could escape the engine's directories
// or otherwise misbehave as a single path element.
func validateID(id string) error {
	if id == "" || len(id) > 128 {
		return errors.New("invalid id")
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.':
		default:
			return errors.New("invalid id")
		}
	}
	if id[0] == '.' || id[0] == '-' || strings.Contains(id, "..") {
		return errors.New("invalid id")
	}
	return nil
}

// git runs a git command with a sanitized environment. Every invocation
// passes -c safe.directory=* to neutralize mixed-ownership checks on
// container-written checkouts. dir may be empty. Returns trimmed stdout.
func (e *Engine) git(ctx context.Context, dir string, args ...string) (string, error) {
	argv := append([]string{"-c", "safe.directory=*"}, args...)
	if dir != "" {
		argv = append([]string{"-C", dir}, argv...)
	}
	cmd := exec.CommandContext(ctx, e.cfg.GitPath, argv...)
	cmd.Env = gitEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("gitengine: git %s: %w: %s",
			strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// gitEnv is the sanitized environment for every git invocation: PATH for
// tool discovery, no user or system config, no inherited GIT_* variables.
func gitEnv() []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.TempDir(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"LC_ALL=C",
	}
}

// publishBranch emits git.branch for a run whose branch tip moved, scoped
// to the workspace recorded in the watch registry or the run metadata
// sidecar. The callback records the commit metadata even when no event bus
// is configured.
func (e *Engine) publishBranch(ctx context.Context, run domain.RunID, commit string) {
	e.mu.Lock()
	info, ok := e.registry[run]
	e.mu.Unlock()
	if !ok {
		meta, err := e.readRunMeta(run)
		if err != nil {
			return
		}
		info = runInfo{workspace: meta.Workspace, branch: meta.Branch}
	}
	if e.cfg.Bus != nil {
		_, err := e.cfg.Bus.Publish(ctx, events.Event{
			WorkspaceID: info.workspace,
			RunID:       run,
			Payload: events.GitBranchPayload{
				WorkspaceID: info.workspace,
				Branch:      info.branch,
				Commit:      commit,
			},
		})
		if err != nil {
			slog.Warn("gitengine: publish branch event failed", "run", string(run), "error", err)
		}
	}
	if e.cfg.OnBranchPublished != nil {
		e.cfg.OnBranchPublished(run, commit, time.Now())
	}
}
