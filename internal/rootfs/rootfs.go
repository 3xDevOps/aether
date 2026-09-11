// Package rootfs provides descriptor-pinned, symlink-free traversal beneath an
// already-open os.Root.
package rootfs

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"
)

// OpenRoot opens name beneath parent one component at a time. Every component
// is opened as a directory without following a symlink, and the descriptor
// used to acquire it is compared with the descriptor retained by os.Root.
// This closes the replacement window between a path check and OpenRoot.
//
// name may be "." or a clean, relative path. The returned root owns its
// descriptor; parent remains open and owned by the caller.
func OpenRoot(parent *os.Root, name string) (*os.Root, error) {
	if parent == nil {
		return nil, fmt.Errorf("rootfs: parent is nil")
	}
	parts, err := relativeParts(name, true)
	if err != nil {
		return nil, err
	}
	current := parent
	owned := false
	for _, part := range parts {
		before, err := current.Lstat(part)
		if err != nil {
			closeOwned(current, owned)
			return nil, fmt.Errorf("rootfs: inspect directory %q: %w", part, err)
		}
		if before.Mode()&os.ModeSymlink != 0 || !before.IsDir() {
			closeOwned(current, owned)
			return nil, fmt.Errorf("rootfs: %q is not a directory", part)
		}

		handled, err := openDirectory(current, part)
		if err != nil {
			closeOwned(current, owned)
			return nil, fmt.Errorf("rootfs: open directory %q: %w", part, err)
		}
		handledInfo, statErr := handled.Stat()
		if statErr != nil {
			_ = handled.Close()
			closeOwned(current, owned)
			return nil, fmt.Errorf("rootfs: stat directory %q: %w", part, statErr)
		}
		if !handledInfo.IsDir() || !os.SameFile(before, handledInfo) {
			_ = handled.Close()
			closeOwned(current, owned)
			return nil, fmt.Errorf("rootfs: directory %q changed while opening", part)
		}

		next, err := current.OpenRoot(part)
		if err != nil {
			_ = handled.Close()
			closeOwned(current, owned)
			return nil, fmt.Errorf("rootfs: retain directory %q: %w", part, err)
		}
		nextInfo, statErr := next.Stat(".")
		_ = handled.Close()
		if statErr != nil {
			_ = next.Close()
			closeOwned(current, owned)
			return nil, fmt.Errorf("rootfs: stat retained directory %q: %w", part, statErr)
		}
		if !os.SameFile(handledInfo, nextInfo) {
			_ = next.Close()
			closeOwned(current, owned)
			return nil, fmt.Errorf("rootfs: directory %q changed while retaining", part)
		}
		closeOwned(current, owned)
		current, owned = next, true
	}
	if !owned {
		// Return an independent descriptor even for "."; callers commonly close
		// the result and must not be able to close the supplied parent.
		return OpenRoot(parent, ".")
	}
	return current, nil
}

// Open opens a regular file beneath parent read-only. Every directory
// component is pinned by OpenRoot and the leaf is opened without following a
// symlink. FIFOs, devices, and all other non-regular files are refused.
func Open(parent *os.Root, name string) (*os.File, error) {
	if parent == nil {
		return nil, fmt.Errorf("rootfs: parent is nil")
	}
	parts, err := relativeParts(name, false)
	if err != nil {
		return nil, err
	}
	leaf := parts[len(parts)-1]
	parentName := "."
	if len(parts) > 1 {
		parentName = strings.Join(parts[:len(parts)-1], "/")
	}
	pinned, err := OpenRoot(parent, parentName)
	if err != nil {
		return nil, err
	}
	defer func() { _ = pinned.Close() }()

	before, err := pinned.Lstat(leaf)
	if err != nil {
		return nil, fmt.Errorf("rootfs: inspect file %q: %w", name, err)
	}
	if before.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("rootfs: %q is a symlink", name)
	}
	if before.Mode()&os.ModeNamedPipe != 0 || !before.Mode().IsRegular() {
		return nil, fmt.Errorf("rootfs: %q is not a regular file", name)
	}

	f, err := openRegular(pinned, leaf)
	if err != nil {
		return nil, fmt.Errorf("rootfs: open file %q: %w", name, err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("rootfs: stat file %q: %w", name, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeNamedPipe != 0 || !os.SameFile(before, info) {
		_ = f.Close()
		return nil, fmt.Errorf("rootfs: %q changed to a non-regular file", name)
	}
	return f, nil
}

func closeOwned(root *os.Root, owned bool) {
	if owned {
		_ = root.Close()
	}
}

func relativeParts(name string, allowDot bool) ([]string, error) {
	if name == "" || strings.ContainsRune(name, 0) || strings.Contains(name, "\\") || path.IsAbs(name) {
		return nil, fs.ErrInvalid
	}
	clean := path.Clean(name)
	if clean != name {
		return nil, fs.ErrInvalid
	}
	if clean == "." {
		if allowDot {
			return []string{"."}, nil
		}
		return nil, fs.ErrInvalid
	}
	parts := strings.Split(clean, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return nil, fs.ErrInvalid
		}
	}
	return parts, nil
}
