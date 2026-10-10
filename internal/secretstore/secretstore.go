// Package secretstore keeps workspace secret variables on the server's disk,
// outside aether.db: one 0600 JSON file per workspace in a 0700 directory.
package secretstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/3xDevOps/Aether/internal/domain"
)

type Store struct {
	dir string
}

func New(dir string) *Store {
	return &Store{dir: dir}
}

func (s *Store) path(id domain.WorkspaceID) (string, error) {
	name := string(id)
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name {
		return "", fmt.Errorf("secretstore: invalid workspace id %q", id)
	}
	return filepath.Join(s.dir, name+".json"), nil
}

// Get returns the workspace's secrets by variable name, empty when it has none.
func (s *Store) Get(id domain.WorkspaceID) (map[string]string, error) {
	path, err := s.path(id)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("secretstore: read workspace %s: %w", id, err)
	}
	secrets := map[string]string{}
	if err := json.Unmarshal(data, &secrets); err != nil {
		return nil, fmt.Errorf("secretstore: decode workspace %s: %w", id, err)
	}
	return secrets, nil
}

// Put replaces the workspace's secrets; an empty map removes the file.
func (s *Store) Put(id domain.WorkspaceID, secrets map[string]string) error {
	if len(secrets) == 0 {
		return s.Delete(id)
	}
	path, err := s.path(id)
	if err != nil {
		return err
	}
	data, err := json.Marshal(secrets)
	if err != nil {
		return fmt.Errorf("secretstore: encode workspace %s: %w", id, err)
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
		return fmt.Errorf("secretstore: write workspace %s: %w", id, err)
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
		return fmt.Errorf("secretstore: write workspace %s: %w", id, err)
	}
	return nil
}

// Delete removes the workspace's secrets; having none is not an error.
func (s *Store) Delete(id domain.WorkspaceID) error {
	path, err := s.path(id)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("secretstore: remove workspace %s: %w", id, err)
	}
	return nil
}
