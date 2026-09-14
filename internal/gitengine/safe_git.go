package gitengine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/rootfs"
)

// scratchGitMu serializes first-time initialization of server-owned scratch
// repositories. Snapshot stores are persistent and a patch request may race
// the first diff snapshot for the same run.

var scratchGitMu sync.Mutex

// checkoutObjectFormat reads only the repository-format declaration needed to
// create a compatible scratch object database. The file is treated as data;
// Git is never pointed at this config and no include is followed.
func (e *Engine) checkoutObjectFormat(workTree string) (string, error) {
	data, err := e.readCheckoutGitFile(workTree, "config", maxCheckoutConfigBytes)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("gitengine: read checkout config: %w", err)
	}
	section := ""
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || section != "extensions" {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(key), "objectformat") &&
			strings.EqualFold(strings.TrimSpace(value), "sha256") {
			return "sha256", nil
		}
	}
	return "", nil
}

// ensureScratchGitDir turns dir into a server-owned bare Git directory. The
// checkout's .git directory is deliberately never used as GIT_DIR: it is
// agent-writable and may contain config, attributes, hooks, or command names.
func (e *Engine) ensureScratchGitDir(ctx context.Context, dir, workTree string) error {
	config := filepath.Join(dir, "config")
	if _, err := os.Stat(config); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("gitengine: inspect scratch git directory: %w", err)
	}
	scratchGitMu.Lock()
	defer scratchGitMu.Unlock()
	if _, err := os.Stat(config); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("gitengine: inspect scratch git directory: %w", err)
	}
	objectFormat, err := e.checkoutObjectFormat(workTree)
	if err != nil {
		return err
	}
	args := []string{"init", "--bare", "--quiet"}
	if objectFormat == "sha256" {
		args = append(args, "--object-format=sha256")
	}
	args = append(args, dir)
	if _, err := e.git(ctx, "", args...); err != nil {
		return fmt.Errorf("gitengine: initialize scratch git directory: %w", err)
	}
	return nil
}

// syncCheckoutExclude copies the checkout's agent-owned info/exclude file
// into the server-owned scratch Git directory. Ignore rules are data and must
// retain their existing semantics, while .git/info/attributes is intentionally
// never copied or read by the hardened runner.
func (e *Engine) syncCheckoutExclude(workTree, gitDir string) error {
	targetDir := filepath.Join(gitDir, "info")
	target := filepath.Join(targetDir, "exclude")
	data, err := e.readCheckoutGitFile(workTree, "info/exclude", maxCheckoutExcludeBytes)
	if errors.Is(err, os.ErrNotExist) {
		if removeErr := os.Remove(target); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return fmt.Errorf("gitengine: clear scratch excludes: %w", removeErr)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("gitengine: read checkout excludes: %w", err)
	}
	if mkdirErr := os.MkdirAll(targetDir, 0o700); mkdirErr != nil {
		return fmt.Errorf("gitengine: create scratch excludes: %w", mkdirErr)
	}
	tmp, err := os.CreateTemp(targetDir, "exclude-*.tmp")
	if err != nil {
		return fmt.Errorf("gitengine: stage scratch excludes: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("gitengine: stage scratch excludes: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("gitengine: stage scratch excludes: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("gitengine: stage scratch excludes: %w", err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		return fmt.Errorf("gitengine: install scratch excludes: %w", err)
	}
	return nil
}

const (
	maxCheckoutConfigBytes     = 1 << 20
	maxCheckoutExcludeBytes    = 1 << 20
	maxCheckoutHeadBytes       = 128
	maxCheckoutRefBytes        = 128
	maxCheckoutPackedRefsBytes = 8 << 20
)

// readCheckoutGitFile opens one checkout metadata file beneath the engine's
// rooted checkout directory. rootfs pins each directory component, rejects
// symlinks and special files before opening, and uses nonblocking no-follow
// handles on Unix; the bounded read prevents an agent-controlled file from
// consuming unbounded memory.
func (e *Engine) readCheckoutGitFile(checkout, name string, limit int64) ([]byte, error) {
	root, err := e.openCheckoutRoot(checkout)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	file, err := rootfs.Open(root, filepath.Join(".git", name))
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("gitengine: stat checkout metadata %s: %w", name, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("gitengine: checkout metadata %s is not a regular file", name)
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, fmt.Errorf("gitengine: read checkout metadata %s: %w", name, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("gitengine: checkout metadata %s exceeds %d bytes", name, limit)
	}
	return data, nil
}

// resolveCheckoutHEAD reads the checkout's HEAD and ref files as inert data.
// A symbolic HEAD is restricted to refs/heads and falls back to packed-refs
// when its loose ref is absent. The returned object id is still validated by
// the caller against the server-owned scratch repository.
func (e *Engine) resolveCheckoutHEAD(checkout string) (string, error) {
	data, err := e.readCheckoutGitFile(checkout, "HEAD", maxCheckoutHeadBytes)
	if err != nil {
		return "", fmt.Errorf("gitengine: read checkout HEAD: %w", err)
	}
	head := strings.TrimSpace(string(data))
	if strings.HasPrefix(head, "ref:") {
		ref := strings.TrimSpace(strings.TrimPrefix(head, "ref:"))
		if !strings.HasPrefix(ref, "refs/heads/") || !validCheckoutRef(ref) {
			return "", fmt.Errorf("gitengine: checkout HEAD has invalid ref %q", ref)
		}
		loose, looseErr := e.readCheckoutGitFile(checkout, ref, maxCheckoutRefBytes)
		switch {
		case looseErr == nil:
			head = strings.TrimSpace(string(loose))
		case !errors.Is(looseErr, os.ErrNotExist):
			return "", fmt.Errorf("gitengine: read checkout HEAD ref %s: %w", ref, looseErr)
		default:
			packed, packedErr := e.readCheckoutGitFile(checkout, "packed-refs", maxCheckoutPackedRefsBytes)
			if packedErr != nil {
				return "", fmt.Errorf("gitengine: resolve checkout HEAD ref %s: %w", ref, packedErr)
			}
			head = packedRefValue(string(packed), ref)
			if head == "" {
				return "", fmt.Errorf("gitengine: checkout HEAD ref %s is unavailable", ref)
			}
		}
	}
	if !validObjectID(head) {
		return "", fmt.Errorf("gitengine: checkout HEAD is not a full object id")
	}
	return head, nil
}

func validCheckoutRef(ref string) bool {
	if !strings.HasPrefix(ref, "refs/heads/") || strings.HasSuffix(ref, "/") {
		return false
	}
	for _, part := range strings.Split(strings.TrimPrefix(ref, "refs/heads/"), "/") {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, "\x00\\") {
			return false
		}
	}
	return true
}

func packedRefValue(data, want string) string {
	for line := range strings.SplitSeq(data, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[1] != want || !validObjectID(fields[0]) {
			continue
		}
		return fields[0]
	}
	return ""
}

// checkoutHead resolves the checkout's current commit without allowing Git
// to inspect its agent-controlled repository configuration. The OID is
// validated in the server-owned scratch store before any caller uses it.
func (e *Engine) checkoutHead(ctx context.Context, run domain.RunID, checkout string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	head, err := e.resolveCheckoutHEAD(checkout)
	if err != nil {
		return "", err
	}
	store, err := e.snapshotStorePath(run)
	if err != nil {
		return "", err
	}
	if _, objectsErr := scratchObjects(store, checkout, run); objectsErr != nil {
		return "", objectsErr
	}
	if ensureErr := e.ensureScratchGitDir(ctx, store, checkout); ensureErr != nil {
		return "", ensureErr
	}
	kind, err := e.git(ctx, store, "cat-file", "-t", head)
	if err != nil {
		return "", fmt.Errorf("gitengine: validate checkout HEAD %s: %w", head, err)
	}
	if strings.TrimSpace(kind) != "commit" {
		return "", fmt.Errorf("gitengine: checkout HEAD %s is a %s, not a commit", head, strings.TrimSpace(kind))
	}
	return head, nil
}

// checkoutHeadSource creates a server-owned scratch ref naming the validated
// checkout HEAD. Publication can fetch this repository without ever asking
// Git to inspect the checkout's own config.
func (e *Engine) checkoutHeadSource(ctx context.Context, run domain.RunID, checkout, ref string) (string, string, error) {
	head, err := e.checkoutHead(ctx, run, checkout)
	if err != nil {
		return "", "", err
	}
	store, err := e.snapshotStorePath(run)
	if err != nil {
		return "", "", err
	}
	if _, err := e.git(ctx, store, "update-ref", ref, head); err != nil {
		return "", "", fmt.Errorf("gitengine: stage checkout HEAD %s: %w", head, err)
	}
	return head, store, nil
}

// gitWorktree runs Git against an explicit, server-owned GIT_DIR and a
// caller-selected index. Its environment and command-line controls make the
// worktree a data source only: checkout-local config, attributes, hooks,
// fsmonitor, filters, external diff, textconv, pagers, and inherited Git
// command variables cannot influence the process.
func (e *Engine) gitWorktree(ctx context.Context, workTree, gitDir, index string, limit int, args ...string) (string, bool, error) {
	if err := e.ensureScratchGitDir(ctx, gitDir, workTree); err != nil {
		return "", false, err
	}
	if err := e.syncCheckoutExclude(workTree, gitDir); err != nil {
		return "", false, err
	}
	emptyTree, err := e.git(ctx, gitDir, "hash-object", "-t", "tree", os.DevNull)
	if err != nil {
		return "", false, fmt.Errorf("gitengine: determine empty attribute tree: %w", err)
	}
	argv := []string{
		"--git-dir", gitDir,
		"--work-tree", workTree,
		"-c", "safe.directory=*",
		"-c", "core.quotePath=false",
		"-c", "core.hooksPath=" + os.DevNull,
		"-c", "core.fsmonitor=false",
		"-c", "diff.external=",
	}
	if len(args) > 0 && args[0] == "diff" {
		args = append([]string{"diff", "--no-ext-diff"}, args[1:]...)
	}
	argv = append(argv, args...)
	cmd := exec.CommandContext(ctx, e.cfg.GitPath, argv...)
	cmd.Env = append(gitEnv(),
		"GIT_INDEX_FILE="+index,
		"GIT_OBJECT_DIRECTORY="+filepath.Join(gitDir, "objects"),
		"GIT_ATTR_SOURCE="+emptyTree,
		"GIT_PAGER=cat",
		"PAGER=cat",
		"GIT_EDITOR=/bin/false",
		"GIT_SEQUENCE_EDITOR=/bin/false",
	)
	out := &boundedBuffer{limit: limit}
	var stderr bytes.Buffer
	cmd.Stdout = out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", false, fmt.Errorf("gitengine: git %s: %w: %s",
			strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return string(out.buf), out.over, nil
}

// gitCheckout is the read-only form used by diff watching and checkout file
// listings. It reads the agent's current index but never lets Git use the
// checkout's .git directory as its repository or configuration source.
func (e *Engine) gitCheckout(ctx context.Context, run domain.RunID, checkout string, args ...string) (string, error) {
	out, _, err := e.gitCheckoutBounded(ctx, run, checkout, -1, args...)
	return strings.TrimSpace(out), err
}

func (e *Engine) gitCheckoutBounded(ctx context.Context, run domain.RunID, checkout string, limit int, args ...string) (string, bool, error) {
	store, err := e.snapshotStorePath(run)
	if err != nil {
		return "", false, err
	}
	if _, objectsErr := scratchObjects(store, checkout, run); objectsErr != nil {
		return "", false, objectsErr
	}
	index := filepath.Join(checkout, ".git", "index")
	out, over, err := e.gitWorktree(ctx, checkout, store, index, limit, args...)
	return out, over, err
}

// gitStaged runs a Git command with a server-owned scratch repository,
// worktree, and index. The checkout's own Git database is never consulted.
func (e *Engine) gitStaged(ctx context.Context, workTree, index string, limit int, args ...string) (string, bool, error) {
	return e.gitWorktree(ctx, workTree, filepath.Dir(index), index, limit, args...)
}

// boundedBuffer keeps the first limit bytes written to it and reports whether
// anything was dropped. A negative limit means unbounded output.
type boundedBuffer struct {
	buf   []byte
	limit int
	over  bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.limit < 0 {
		b.buf = append(b.buf, p...)
		return len(p), nil
	}
	switch room := b.limit - len(b.buf); {
	case room >= len(p):
		b.buf = append(b.buf, p...)
	case room > 0:
		b.buf = append(b.buf, p[:room]...)
		b.over = true
	case len(p) > 0:
		b.over = true
	}
	return len(p), nil
}

func (e *Engine) checkoutRunID(checkout string) (domain.RunID, error) {
	rel, err := filepath.Rel(e.cfg.CheckoutsDir, checkout)
	if err != nil || rel == "." || strings.ContainsRune(rel, filepath.Separator) {
		return "", ErrCheckoutNotFound
	}
	if err := validateID(rel); err != nil {
		return "", fmt.Errorf("gitengine: checkout path has invalid run id: %w", err)
	}
	return domain.RunID(rel), nil
}
