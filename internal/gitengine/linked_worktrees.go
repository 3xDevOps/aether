package gitengine

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"

	"github.com/3xDevOps/Aether/internal/rootfs"
)

const (
	maxLinkedWorktrees     = 1024
	maxWorktreeRecordBytes = 8 << 10
)

// linkedWorktreeExcludes returns one anchored ignore pattern per linked
// worktree that `git worktree add` placed inside checkout.
//
// A run container mounts the checkout at its own path, and Git writes that
// path into the worktree's .git file. On the server the path names nothing,
// so Git sees no repository boundary there and walks the worktree's whole
// tree as untracked files of the run. The entries under .git/worktrees still
// record each boundary in the container's terms, so every entry is matched
// to the directory whose .git file names it back.
func (e *Engine) linkedWorktreeExcludes(checkout string) ([]byte, error) {
	root, err := e.openCheckoutRoot(checkout)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("gitengine: open checkout for linked worktrees: %w", err)
	}
	defer func() { _ = root.Close() }()
	admin, err := rootfs.OpenRoot(root, ".git/worktrees")
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("gitengine: open linked worktree records: %w", err)
	}
	defer func() { _ = admin.Close() }()
	records, err := admin.Open(".")
	if err != nil {
		return nil, fmt.Errorf("gitengine: list linked worktree records: %w", err)
	}
	entries, err := records.ReadDir(maxLinkedWorktrees + 1)
	_ = records.Close()
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("gitengine: list linked worktree records: %w", err)
	}
	if len(entries) > maxLinkedWorktrees {
		return nil, fmt.Errorf("gitengine: checkout registers more than %d linked worktrees", maxLinkedWorktrees)
	}

	var dirs []string
	for _, entry := range entries {
		name := entry.Name()
		recorded, ok := readWorktreeRecord(admin, name+"/gitdir")
		if !ok {
			continue
		}
		worktree, ok := strings.CutSuffix(recorded, "/.git")
		if !ok {
			continue
		}
		if dir, ok := linkedWorktreeDir(root, name, worktree); ok {
			dirs = append(dirs, dir)
		}
	}
	slices.Sort(dirs)
	var patterns []byte
	for _, dir := range slices.Compact(dirs) {
		// An ignore file is line-oriented and cannot spell these names.
		if strings.ContainsAny(dir, "\r\n") {
			continue
		}
		patterns = append(patterns, '/')
		for i := range len(dir) {
			if strings.IndexByte(`\*?[`, dir[i]) >= 0 {
				patterns = append(patterns, '\\')
			}
			patterns = append(patterns, dir[i])
		}
		patterns = append(patterns, '/', '\n')
	}
	return patterns, nil
}

// linkedWorktreeDir maps the worktree path a record holds to the directory
// inside the checkout whose .git file names that record. A relative path is
// resolved from the record. An absolute one is in the container's terms and
// the mount point is not known here, so each of its suffixes is tried.
func linkedWorktreeDir(root *os.Root, name, worktree string) (string, bool) {
	record := ".git/worktrees/" + name
	candidates := []string{path.Join(record, worktree)}
	if path.IsAbs(worktree) {
		candidates = nil
		parts := strings.Split(strings.Trim(worktree, "/"), "/")
		for i := range parts {
			candidates = append(candidates, strings.Join(parts[i:], "/"))
		}
	}
	for _, dir := range candidates {
		if dir == ".git" || strings.HasPrefix(dir, ".git/") {
			continue
		}
		link, ok := readWorktreeRecord(root, dir+"/.git")
		if !ok {
			continue
		}
		target, ok := strings.CutPrefix(link, "gitdir: ")
		if ok && (target == record || strings.HasSuffix(target, "/"+record)) {
			return dir, true
		}
	}
	return "", false
}

// readWorktreeRecord reads one of the small files Git links a worktree with.
// Both are agent-writable, so anything other than a short regular file is
// simply not a record.
func readWorktreeRecord(root *os.Root, name string) (string, bool) {
	file, err := rootfs.Open(root, name)
	if err != nil {
		return "", false
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, maxWorktreeRecordBytes+1))
	if err != nil || len(data) > maxWorktreeRecordBytes {
		return "", false
	}
	return strings.TrimRight(string(data), " \t\r\n"), true
}
