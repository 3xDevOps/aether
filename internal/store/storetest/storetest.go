// Package storetest opens test databases that start already migrated.
// Applying every migration takes seconds per database under the race
// detector, so the migrations run once per test binary and each new
// database is a copy of that result.
package storetest

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/3xDevOps/Aether/internal/domain"
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
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	switch {
	case errors.Is(err, fs.ErrExist):
		return store.Open(path)
	case err != nil:
		return nil, fmt.Errorf("storetest: create %s: %w", path, err)
	}
	image, err := migrated()
	if err == nil {
		_, err = f.Write(image)
	}
	if joined := errors.Join(err, f.Close()); joined != nil {
		_ = os.Remove(path)
		return nil, fmt.Errorf("storetest: write %s: %w", path, joined)
	}
	return store.Open(path)
}

// Unscoped is db as it behaved before workspace-scoped access: every
// member reaches every workspace, an admin as if they owned it and anyone
// else as if granted it. Tests of behaviour that predates the access model
// use it; tests of the model use the store itself.
type Unscoped struct{ *store.DB }

func (u Unscoped) GetMember(ctx context.Context, id domain.MemberID) (*domain.Member, error) {
	m, err := u.DB.GetMember(ctx, id)
	if err != nil {
		return nil, err
	}
	all, err := u.ListWorkspaces(ctx)
	if err != nil {
		return nil, err
	}
	reach := make(map[domain.WorkspaceID]bool, len(all))
	for _, w := range all {
		reach[w.ID] = true
	}
	if m.Role == domain.RoleAdmin {
		m.Access.Owned = reach
	} else {
		m.Access.Granted = reach
	}
	return m, nil
}

// WorkspaceMembers is every member: every member shares every workspace.
func (u Unscoped) WorkspaceMembers(ctx context.Context, _ []domain.WorkspaceID) ([]domain.MemberID, error) {
	all, err := u.ListMembers(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]domain.MemberID, 0, len(all))
	for _, m := range all {
		ids = append(ids, m.ID)
	}
	return ids, nil
}
