package localops

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// sizeBudget is what an extraction may still write: left counts down from
// limit across every entry, so a compressed archive cannot expand into an
// unbounded write.
type sizeBudget struct {
	left, limit int64
}

// extractZip unpacks the zip at archive into root, writing at most limit
// bytes. Only files and directories are accepted: the zips extracted here,
// Node's Windows release and the prebuilt Windows desktop app, carry
// nothing else.
func extractZip(archive string, root *os.Root, limit int64) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer func() { _ = zr.Close() }()
	budget := &sizeBudget{left: limit, limit: limit}
	for _, entry := range zr.File {
		name, err := archiveEntry(entry.Name)
		if err != nil {
			return err
		}
		if entry.FileInfo().IsDir() {
			if err = root.MkdirAll(name, 0o755); err != nil {
				return fmt.Errorf("%s: %w", entry.Name, err)
			}
			continue
		}
		if !entry.Mode().IsRegular() {
			return fmt.Errorf("%s: unexpected entry mode %s", entry.Name, entry.Mode())
		}
		src, err := entry.Open()
		if err != nil {
			return fmt.Errorf("%s: %w", entry.Name, err)
		}
		err = writeArchiveFile(root, name, src, entry.Mode().Perm(), budget)
		_ = src.Close()
		if err != nil {
			return fmt.Errorf("%s: %w", entry.Name, err)
		}
	}
	return nil
}

// writeArchiveFile copies one entry into the confined tree, drawing its
// size from the shared budget.
func writeArchiveFile(root *os.Root, name string, src io.Reader, mode fs.FileMode, budget *sizeBudget) error {
	if err := parentDir(root, name); err != nil {
		return err
	}
	if mode == 0 {
		mode = 0o644
	}
	f, err := root.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	written, err := io.Copy(f, io.LimitReader(src, budget.left+1))
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if written > budget.left {
		return fmt.Errorf("the archive expands past %d bytes", budget.limit)
	}
	budget.left -= written
	return nil
}

// parentDir creates the directories an entry needs. Archives are not
// required to list a directory before the files inside it.
func parentDir(root *os.Root, name string) error {
	if dir := path.Dir(name); dir != "." {
		return root.MkdirAll(dir, 0o755)
	}
	return nil
}

// archiveEntry cleans an archive entry name and rejects the ones that
// would land outside the tree. os.Root refuses these as well; checking
// here is what turns a kernel-level "path escapes from parent" into a
// message naming the archive that carried the entry.
func archiveEntry(name string) (string, error) {
	clean := path.Clean(strings.ReplaceAll(name, `\`, "/"))
	if clean == "." {
		return "", errors.New("empty entry name")
	}
	if path.IsAbs(clean) || filepath.IsAbs(name) || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("entry %q would extract outside the target directory", name)
	}
	return clean, nil
}
