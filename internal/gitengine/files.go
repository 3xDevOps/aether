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
	"sort"
	"strconv"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
)

// MaxFileBytes is the maximum file or one-file diff response. The dashboard
// reads files inline and reports that larger files were truncated.
const MaxFileBytes = 512 << 10

// ErrInvalidPath identifies a client-supplied path that cannot be read from a
// repository or checkout.
var ErrInvalidPath = errors.New("invalid file path")

// TreeEntry is one immediate child of a repository directory.
type TreeEntry struct {
	Name string
	Kind string
	Size int64
}

// ValidatePath rejects paths that could address data outside the repository's
// logical root. A blank path is the repository root for ListTree only.
func ValidatePath(name string) error {
	if name == "" {
		return nil
	}
	if strings.HasPrefix(name, "/") || strings.HasPrefix(name, `\`) ||
		(len(name) >= 2 && name[1] == ':') || strings.ContainsRune(name, '\x00') {
		return fmt.Errorf("%w: path must be relative", ErrInvalidPath)
	}
	if strings.ContainsRune(name, '\\') {
		return fmt.Errorf("%w: path must use slash separators", ErrInvalidPath)
	}
	for _, component := range strings.Split(name, "/") {
		if component == ".." || component == ".git" {
			return fmt.Errorf("%w: path contains forbidden component", ErrInvalidPath)
		}
	}
	return nil
}

// ListTree lists immediate children of dir. Bare repositories use ref as the
// tree reference. A checkout is identified by its .git directory and uses the
// working tree regardless of ref, so uncommitted files are visible.
func (e *Engine) ListTree(ctx context.Context, repoPath, ref, dir string) ([]TreeEntry, error) {
	if err := ValidatePath(dir); err != nil {
		return nil, err
	}
	if isCheckout(repoPath) {
		return e.listCheckout(ctx, repoPath, dir)
	}
	return e.listBare(ctx, repoPath, ref, dir)
}

func (e *Engine) listBare(ctx context.Context, repoPath, ref, dir string) ([]TreeEntry, error) {
	if strings.TrimSpace(ref) == "" {
		return nil, errors.New("gitengine: repository ref is required")
	}
	args := []string{"ls-tree", "-z", "-l", ref, "--"}
	if dir != "" {
		args = append(args, strings.TrimSuffix(dir, "/")+"/")
	}
	output, err := e.gitBytes(ctx, repoPath, args...)
	if err != nil {
		return nil, err
	}
	entries := make(map[string]TreeEntry)
	prefix := strings.TrimSuffix(dir, "/")
	if prefix != "" {
		prefix += "/"
	}
	for _, record := range bytes.Split(output, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		fields := bytes.SplitN(record, []byte{'\t'}, 2)
		if len(fields) != 2 {
			return nil, errors.New("gitengine: malformed ls-tree output")
		}
		meta := strings.Fields(string(fields[0]))
		if len(meta) < 3 {
			return nil, errors.New("gitengine: malformed ls-tree metadata")
		}
		kind := meta[1]
		if kind != "blob" && kind != "tree" {
			continue
		}
		name := string(fields[1])
		if prefix != "" {
			if !strings.HasPrefix(name, prefix) {
				continue
			}
			name = strings.TrimPrefix(name, prefix)
		}
		if name == "" {
			continue
		}
		child, _, nested := strings.Cut(name, "/")
		entry := TreeEntry{Name: child, Kind: "file"}
		if nested || kind == "tree" {
			entry.Kind = "dir"
		}
		if entry.Kind == "file" && len(meta) >= 4 && meta[3] != "-" {
			entry.Size, err = strconv.ParseInt(meta[3], 10, 64)
			if err != nil {
				return nil, fmt.Errorf("gitengine: parse size for %s: %w", child, err)
			}
		}
		entries[child] = entry
	}
	return sortedTreeEntries(entries), nil
}

func (e *Engine) listCheckout(ctx context.Context, checkout, dir string) ([]TreeEntry, error) {
	args := []string{"ls-files", "-z", "--cached", "--others", "--exclude-standard"}
	if dir != "" {
		args = append(args, "--", strings.TrimSuffix(dir, "/")+"/")
	}
	output, err := e.gitBytes(ctx, checkout, args...)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(checkout)
	if err != nil {
		return nil, fmt.Errorf("gitengine: open checkout: %w", err)
	}
	defer func() { _ = root.Close() }()
	entries := make(map[string]TreeEntry)
	prefix := strings.TrimSuffix(dir, "/")
	if prefix != "" {
		prefix += "/"
	}
	for _, record := range bytes.Split(output, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		name := string(record)
		if err := ValidatePath(name); err != nil {
			continue
		}
		if prefix != "" {
			if !strings.HasPrefix(name, prefix) {
				continue
			}
			name = strings.TrimPrefix(name, prefix)
		}
		child, _, nested := strings.Cut(name, "/")
		if child == "" {
			continue
		}
		entry := TreeEntry{Name: child, Kind: "dir"}
		if !nested {
			statPath := child
			if dir != "" {
				statPath = strings.TrimSuffix(dir, "/") + "/" + child
			}
			info, statErr := root.Stat(statPath)
			if statErr != nil {
				return nil, fmt.Errorf("gitengine: stat checkout file %s: %w", child, statErr)
			}
			entry.Kind = "file"
			entry.Size = info.Size()
		}
		entries[child] = entry
	}
	return sortedTreeEntries(entries), nil
}

func sortedTreeEntries(entries map[string]TreeEntry) []TreeEntry {
	out := make([]TreeEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ReadFile reads path from a bare repository ref or directly from a checkout.
// Checkout reads intentionally use the working tree, not HEAD, so edits that
// have not been committed are visible to the dashboard.
func (e *Engine) ReadFile(ctx context.Context, repoPath, ref, path string, maxBytes int) ([]byte, bool, bool, error) {
	if err := ValidatePath(path); err != nil {
		return nil, false, false, err
	}
	if path == "" {
		return nil, false, false, fmt.Errorf("%w: file path is required", ErrInvalidPath)
	}
	if maxBytes <= 0 {
		maxBytes = MaxFileBytes
	}
	var (
		content   []byte
		truncated bool
		err       error
	)
	if isCheckout(repoPath) {
		content, truncated, err = readCheckoutFile(repoPath, path, maxBytes)
	} else {
		if strings.TrimSpace(ref) == "" {
			return nil, false, false, errors.New("gitengine: repository ref is required")
		}
		content, truncated, err = e.gitBounded(ctx, repoPath, maxBytes, "cat-file", "-p", ref+":"+path)
	}
	if err != nil {
		return nil, false, false, err
	}
	binary := bytes.IndexByte(content[:min(len(content), 8<<10)], 0) >= 0
	return content, truncated, binary, nil
}

func readCheckoutFile(checkout, path string, maxBytes int) ([]byte, bool, error) {
	root, err := os.OpenRoot(checkout)
	if err != nil {
		return nil, false, fmt.Errorf("gitengine: open checkout: %w", err)
	}
	defer func() { _ = root.Close() }()
	file, err := root.Open(path)
	if err != nil {
		return nil, false, fmt.Errorf("gitengine: open checkout file: %w", err)
	}
	defer func() { _ = file.Close() }()
	content, err := io.ReadAll(io.LimitReader(file, int64(maxBytes)+1))
	if err != nil {
		return nil, false, fmt.Errorf("gitengine: read checkout file: %w", err)
	}
	truncated := len(content) > maxBytes
	if truncated {
		content = content[:maxBytes]
	}
	return content, truncated, nil
}

// FileDiff renders only path from a run checkout against its recorded base.
// Like RunPatch, it stages into a scratch index and object directory without
// writing to the checkout's .git database.
func (e *Engine) FileDiff(ctx context.Context, run domain.RunID, path string) (Patch, error) {
	if err := ValidatePath(path); err != nil {
		return Patch{}, err
	}
	if path == "" {
		return Patch{}, fmt.Errorf("%w: file path is required", ErrInvalidPath)
	}
	checkout, err := e.existingCheckoutPath(run)
	if err != nil {
		return Patch{}, err
	}
	meta, err := e.readRunMeta(run)
	if err != nil {
		return Patch{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, snapshotTimeout)
	defer cancel()

	dir, err := os.MkdirTemp("", "aether-file-diff-")
	if err != nil {
		return Patch{}, fmt.Errorf("gitengine: scratch index for run %s: %w", run, err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	index := filepath.Join(dir, "index")
	objects := filepath.Join(dir, "objects")
	if mkErr := os.MkdirAll(filepath.Join(objects, "info"), 0o700); mkErr != nil {
		return Patch{}, fmt.Errorf("gitengine: scratch objects for run %s: %w", run, mkErr)
	}
	alternates := filepath.Join(objects, "info", "alternates")
	if writeErr := os.WriteFile(alternates, []byte(filepath.Join(checkout, ".git", "objects")+"\n"), 0o600); writeErr != nil {
		return Patch{}, fmt.Errorf("gitengine: scratch alternates for run %s: %w", run, writeErr)
	}
	if data, readErr := os.ReadFile(filepath.Join(checkout, ".git", "index")); readErr == nil {
		if seedErr := os.WriteFile(index, data, 0o600); seedErr != nil {
			return Patch{}, fmt.Errorf("gitengine: seed scratch index for run %s: %w", run, seedErr)
		}
	}
	if _, _, addErr := e.gitStaged(ctx, checkout, index, 0, "add", "-A"); addErr != nil {
		return Patch{}, addErr
	}
	text, truncated, err := e.gitStaged(ctx, checkout, index, MaxFileBytes,
		"diff", "--cached", "--no-color", "--no-renames", meta.Base, "--", path)
	if err != nil {
		return Patch{}, err
	}
	if truncated {
		if i := strings.LastIndexByte(text, '\n'); i >= 0 {
			text = text[:i+1]
		}
	}
	return Patch{Base: meta.Base, Text: text, Truncated: truncated}, nil
}

func isCheckout(path string) bool {
	info, err := os.Stat(filepath.Join(path, ".git"))
	return err == nil && (info.IsDir() || info.Mode().IsRegular())
}

func (e *Engine) gitBytes(ctx context.Context, dir string, args ...string) ([]byte, error) {
	argv := append([]string{"-C", dir, "-c", "safe.directory=*"}, args...)
	cmd := exec.CommandContext(ctx, e.cfg.GitPath, argv...)
	cmd.Env = gitEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("gitengine: git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

func (e *Engine) gitBounded(ctx context.Context, dir string, limit int, args ...string) ([]byte, bool, error) {
	argv := append([]string{"-C", dir, "-c", "safe.directory=*"}, args...)
	cmd := exec.CommandContext(ctx, e.cfg.GitPath, argv...)
	cmd.Env = gitEnv()
	out := &boundedBuffer{limit: limit}
	var stderr bytes.Buffer
	cmd.Stdout = out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, false, fmt.Errorf("gitengine: git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out.buf, out.over, nil
}

// FilesTree addresses a workspace base tree or a run's working checkout by
// identifiers. It is the server-facing adapter over ListTree.
func (e *Engine) FilesTree(ctx context.Context, workspace domain.WorkspaceID, run domain.RunID, ref, dir string) ([]TreeEntry, error) {
	if run != "" {
		checkout, err := e.existingCheckoutPath(run)
		if err != nil {
			return nil, err
		}
		return e.ListTree(ctx, checkout, "", dir)
	}
	repo, err := e.existingRepoPath(workspace)
	if err != nil {
		return nil, err
	}
	return e.ListTree(ctx, repo, ref, dir)
}

// FilesRead addresses a workspace base file or a run's working checkout by
// identifiers. It is the server-facing adapter over ReadFile.
func (e *Engine) FilesRead(ctx context.Context, workspace domain.WorkspaceID, run domain.RunID, ref, path string, maxBytes int) ([]byte, bool, bool, error) {
	if run != "" {
		checkout, err := e.existingCheckoutPath(run)
		if err != nil {
			return nil, false, false, err
		}
		return e.ReadFile(ctx, checkout, "", path, maxBytes)
	}
	repo, err := e.existingRepoPath(workspace)
	if err != nil {
		return nil, false, false, err
	}
	return e.ReadFile(ctx, repo, ref, path, maxBytes)
}
