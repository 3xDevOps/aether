// Package storetest opens test databases that start already migrated.
// Applying every migration takes seconds per database under the race
// detector, so the migrations run once per test binary and each new
// database is a copy of that result.
package storetest

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/3xDevOps/Aether/internal/store"
)

var migrated = sync.OnceValues(func() ([]byte, error) {
	dir, err := os.MkdirTemp("", "aether-storetest-")
	if err != nil {
		return nil, fmt.Errorf("storetest: create template directory: %w", err)
	}
	defer os.RemoveAll(dir) //nolint:errcheck // a leftover temporary directory does not affect the template
	path := filepath.Join(dir, "aether.db")
	db, err := store.Open(path)
	if err != nil {
		return nil, fmt.Errorf("storetest: migrate template: %w", err)
	}
	if err := db.Close(); err != nil {
		return nil, fmt.Errorf("storetest: close template: %w", err)
	}
	// Closing the last connection checkpoints the WAL into the main file and
	// removes it; a WAL left behind would hold schema the copy lacks.
	if _, err := os.Stat(path + "-wal"); err == nil {
		return nil, errors.New("storetest: closing the template left its WAL behind")
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("storetest: check template WAL: %w", err)
	}
	return os.ReadFile(path)
})

// Open is store.Open for tests. A database that does not exist yet starts
// as a copy of a fully migrated one; an existing file is opened as is.
func Open(path string) (*store.DB, error) {
	image, err := migrated()
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	switch {
	case err == nil:
		_, writeErr := f.Write(image)
		if joined := errors.Join(writeErr, f.Close()); joined != nil {
			return nil, fmt.Errorf("storetest: write %s: %w", path, joined)
		}
	case !errors.Is(err, fs.ErrExist):
		return nil, fmt.Errorf("storetest: create %s: %w", path, err)
	}
	return store.Open(path)
}
