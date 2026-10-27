// Package secretstore keeps workspace secret variables and workspace files on
// the server's disk, outside aether.db: per workspace, one 0600 JSON file of
// each in a 0700 directory.
package secretstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/3xDevOps/Aether/internal/domain"
)

// Store's lock orders a change to a workspace's environment, which is its
// row in aether.db and its secret file, against a launch reading both. No
// method takes it.
type Store struct {
	sync.RWMutex
	dir string
}

func New(dir string) *Store {
	return &Store{dir: dir}
}

type kind struct{ name, suffix string }

var (
	secretsKind = kind{"secrets", ".json"}
	filesKind   = kind{"files", ".files.json"}
)

func (s *Store) path(id domain.WorkspaceID, k kind) (string, error) {
	name := string(id)
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name {
		return "", fmt.Errorf("secretstore: invalid workspace id %q", id)
	}
	return filepath.Join(s.dir, name+k.suffix), nil
}

// Get returns an empty map for a workspace with no secrets.
func (s *Store) Get(id domain.WorkspaceID) (map[string]string, error) {
	return s.read(id, secretsKind)
}

// Files maps each workspace file's path to its content, empty for a
// workspace with none.
func (s *Store) Files(id domain.WorkspaceID) (map[string]string, error) {
	return s.read(id, filesKind)
}

// Put removes the file when secrets is empty.
func (s *Store) Put(id domain.WorkspaceID, secrets map[string]string) error {
	return s.write(id, secretsKind, secrets)
}

// PutFiles removes the file when files is empty.
func (s *Store) PutFiles(id domain.WorkspaceID, files map[string]string) error {
	return s.write(id, filesKind, files)
}

func (s *Store) read(id domain.WorkspaceID, k kind) (map[string]string, error) {
	path, err := s.path(id, k)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("secretstore: read workspace %s %s: %w", id, k.name, err)
	}
	values := map[string]string{}
	if err := json.Unmarshal(data, &values); err != nil {
		return nil, fmt.Errorf("secretstore: decode workspace %s %s: %w", id, k.name, err)
	}
	return values, nil
}

func (s *Store) write(id domain.WorkspaceID, k kind, values map[string]string) error {
	if len(values) == 0 {
		return s.remove(id, k)
	}
	path, err := s.path(id, k)
	if err != nil {
		return err
	}
	data, err := json.Marshal(values)
	if err != nil {
		return fmt.Errorf("secretstore: encode workspace %s %s: %w", id, k.name, err)
	}
	if err = os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("secretstore: create directory: %w", err)
	}
	if err = os.Chmod(s.dir, 0o700); err != nil {
		return fmt.Errorf("secretstore: protect directory: %w", err)
	}
	// CreateTemp opens the file 0600, so the value is never readable by
	// another user, not even before the rename.
	tmp, err := os.CreateTemp(s.dir, ".write-*")
	if err != nil {
		return fmt.Errorf("secretstore: write workspace %s %s: %w", id, k.name, err)
	}
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("secretstore: write workspace %s %s: %w", id, k.name, err)
	}
	return s.syncDir()
}

// syncDir makes a rename or removal in the directory survive a power loss.
func (s *Store) syncDir() error {
	dir, err := os.Open(s.dir)
	if err != nil {
		return fmt.Errorf("secretstore: sync directory: %w", err)
	}
	if err = errors.Join(dir.Sync(), dir.Close()); err != nil {
		return fmt.Errorf("secretstore: sync directory: %w", err)
	}
	return nil
}

// Delete removes a workspace's secrets and files. It does not fail for a
// workspace with neither.
func (s *Store) Delete(id domain.WorkspaceID) error {
	if err := s.remove(id, secretsKind); err != nil {
		return err
	}
	return s.remove(id, filesKind)
}

func (s *Store) remove(id domain.WorkspaceID, k kind) error {
	path, err := s.path(id, k)
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("secretstore: remove workspace %s %s: %w", id, k.name, err)
	}
	return s.syncDir()
}
