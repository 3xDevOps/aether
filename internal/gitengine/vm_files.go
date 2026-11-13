package gitengine

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/guest"
)

// The Files view of a checkout in a VM asks the VM, which answers with the
// same engine code a checkout on the server's disk gets. What comes back is
// checked as everything from a VM is. While the VM does not run, reads come
// from the last recorded snapshot and writes fail: the caller wakes a
// sleeping session first.

func (e *Engine) vmFilesTree(ctx context.Context, run domain.RunID, dir string) ([]TreeEntry, error) {
	if err := ValidatePath(dir); err != nil {
		return nil, err
	}
	g, err := e.vmGit(ctx, run)
	if errors.Is(err, guest.ErrVMNotRunning) {
		return e.recordedFilesTree(ctx, run, dir)
	}
	if err != nil {
		return nil, err
	}
	entries, err := g.Tree(ctx, string(run), dir)
	if err != nil {
		return nil, vmErr(err)
	}
	out := make([]TreeEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Name == "" || strings.Contains(entry.Name, "/") || ValidatePath(entry.Name) != nil ||
			(entry.Kind != "file" && entry.Kind != "dir") || entry.Size < 0 {
			return nil, fmt.Errorf("gitengine: session %s listed an invalid entry %q", run, entry.Name)
		}
		out = append(out, TreeEntry{Name: entry.Name, Kind: entry.Kind, Size: entry.Size})
	}
	slices.SortFunc(out, func(a, b TreeEntry) int { return strings.Compare(a.Name, b.Name) })
	return slices.CompactFunc(out, func(a, b TreeEntry) bool { return a.Name == b.Name }), nil
}

func (e *Engine) vmFilesRead(ctx context.Context, run domain.RunID, path string, maxBytes int) (FileRead, error) {
	if err := ValidatePath(path); err != nil {
		return FileRead{}, err
	}
	if path == "" {
		return FileRead{}, fmt.Errorf("%w: file path is required", ErrInvalidPath)
	}
	if maxBytes <= 0 {
		maxBytes = MaxFileBytes
	}
	g, err := e.vmGit(ctx, run)
	if errors.Is(err, guest.ErrVMNotRunning) {
		return e.recordedFilesRead(ctx, run, path, maxBytes)
	}
	if err != nil {
		return FileRead{}, err
	}
	content, truncated, size, err := g.ReadFile(ctx, string(run), path, maxBytes)
	if err != nil {
		return FileRead{}, vmErr(err)
	}
	if size < int64(len(content)) || truncated != (size > int64(len(content))) {
		return FileRead{}, fmt.Errorf("gitengine: session %s sent %d bytes of a %d-byte file", run, len(content), size)
	}
	return fileRead(content, truncated, size), nil
}

// fileRead is readFile's result for content the server holds.
func fileRead(content []byte, truncated bool, size int64) FileRead {
	binary := isBinary(content) || !utf8.Valid(content)
	result := FileRead{Content: content, Truncated: truncated, Binary: binary, Size: size, Writable: !truncated && !binary}
	if !truncated && !binary {
		result.Revision = fmt.Sprintf("%x", sha256.Sum256(content))
	}
	return result
}

func (e *Engine) vmFilesWrite(ctx context.Context, run domain.RunID, name string, content []byte, revision string) (FileRead, error) {
	g, err := e.vmGit(ctx, run)
	if err != nil {
		return FileRead{}, err
	}
	if err := g.WriteFile(ctx, string(run), name, content, revision); err != nil {
		return FileRead{}, vmErr(err)
	}
	return fileRead(content, false, int64(len(content))), nil
}

func (e *Engine) vmFileDiff(ctx context.Context, run domain.RunID, path string) (Patch, error) {
	g, err := e.vmGit(ctx, run)
	if errors.Is(err, guest.ErrVMNotRunning) {
		return e.recordedFileDiff(ctx, run, path)
	}
	if err != nil {
		return Patch{}, err
	}
	p, err := g.FileDiff(ctx, string(run), path)
	if err != nil {
		return Patch{}, vmErr(err)
	}
	if !validObjectID(p.Base) || len(p.Text) > MaxPatchBytes {
		return Patch{}, fmt.Errorf("gitengine: session %s rendered an invalid diff of %s", run, path)
	}
	return Patch{Base: p.Base, Text: p.Text, Truncated: p.Truncated, ForkTree: p.ForkTree}, nil
}
