package toolenv

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// FileEntry is a digest input and contains no host path.
type FileEntry struct {
	Path string
	Mode uint32
	Size int64
}

// Manifest describes a tree in deterministic lexical order.
type Manifest struct {
	Files []FileEntry
}

// DigestTree hashes relative names, file modes, and file bytes. Symlinks are
// refused because following one would make a server-owned snapshot escape.
func DigestTree(root string) (string, Manifest, error) {
	root = filepath.Clean(root)
	info, err := os.Lstat(root)
	if err != nil {
		return "", Manifest{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", Manifest{}, ErrSymlink
	}
	if !info.IsDir() {
		return "", Manifest{}, fmt.Errorf("toolenv: root is not a directory")
	}
	entries := make([]FileEntry, 0)
	h := sha256.New()
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return ErrSymlink
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == "." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return ErrTraversal
		}
		rel = filepath.ToSlash(rel)
		mode := uint32(info.Mode().Perm())
		entries = append(entries, FileEntry{Path: rel, Mode: mode, Size: info.Size()})
		fmt.Fprintf(h, "%s\x00%o\x00%d\x00", rel, mode, info.Size())
		if info.Mode().IsRegular() {
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(h, f)
			closeErr := f.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		}
		return nil
	})
	if err != nil {
		return "", Manifest{}, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return hex.EncodeToString(h.Sum(nil)), Manifest{Files: entries}, nil
}
