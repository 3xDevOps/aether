// Package memberhome manages one persistent credential home per member.
package memberhome

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/3xDevOps/Aether/internal/domain"
)

// Manager owns persistent member homes and separately reclaimable cache pools.
type Manager struct {
	root       string
	cacheRoot  string
	removeHome func(ctx context.Context, path string) error
	mu         sync.Mutex
	locks      map[string]*sync.Mutex
	cacheLocks map[domain.MemberID]*sync.Mutex
	// archived holds the members whose home Archive moved, so a write
	// that passed its checks before the move cannot create a new home.
	archived map[domain.MemberID]bool
}

// New creates a manager with separate home and cache roots. Neither root is
// created until needed. removeHome removes owned directories; nil uses
// os.RemoveAll. It must not follow symlinks or change file permissions/owners.
func New(homeRoot, cacheRoot string, removeHome func(ctx context.Context, path string) error) (*Manager, error) {
	if strings.TrimSpace(homeRoot) == "" || strings.TrimSpace(cacheRoot) == "" {
		return nil, fmt.Errorf("memberhome: home and cache roots are required")
	}
	homeRoot, err := filepath.Abs(homeRoot)
	if err != nil {
		return nil, fmt.Errorf("memberhome: home root: %w", err)
	}
	cacheRoot, err = filepath.Abs(cacheRoot)
	if err != nil {
		return nil, fmt.Errorf("memberhome: cache root: %w", err)
	}
	for _, pair := range [][2]string{{homeRoot, cacheRoot}, {cacheRoot, homeRoot}} {
		rel, err := filepath.Rel(pair[0], pair[1])
		if err != nil || rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
			return nil, fmt.Errorf("memberhome: home and cache roots must not overlap")
		}
	}
	if removeHome == nil {
		removeHome = func(_ context.Context, path string) error { return os.RemoveAll(path) }
	}
	return &Manager{
		root: homeRoot, cacheRoot: cacheRoot, removeHome: removeHome,
		locks: make(map[string]*sync.Mutex), cacheLocks: make(map[domain.MemberID]*sync.Mutex),
		archived: make(map[domain.MemberID]bool),
	}, nil
}

// Root returns the manager's root directory.
func (m *Manager) Root() string {
	return m.root
}

// CacheRoot returns the root containing server-owned cache pool metadata.
func (m *Manager) CacheRoot() string {
	return m.cacheRoot
}

// Path validates member and returns its persistent home, creating it on first
// use with owner-only permissions.
func (m *Manager) Path(member domain.MemberID) (string, error) {
	if err := validateMemberID(string(member)); err != nil {
		return "", fmt.Errorf("memberhome: member %q: %w", member, err)
	}
	root, err := m.openHome(member)
	if err != nil {
		return "", err
	}
	if err := root.Close(); err != nil {
		return "", fmt.Errorf("memberhome: close home for %q: %w", member, err)
	}
	return filepath.Join(m.root, string(member)), nil
}

// Remove deletes a member's persistent home. Removing an absent home is a
// successful no-op.
func (m *Manager) Remove(ctx context.Context, member domain.MemberID) error {
	if err := validateMemberID(string(member)); err != nil {
		return fmt.Errorf("memberhome: member %q: %w", member, err)
	}
	root, err := m.openExistingHome(member)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := root.Close(); err != nil {
		return err
	}
	home := filepath.Join(m.root, string(member))
	if err := m.removeHome(ctx, home); err != nil {
		return fmt.Errorf("memberhome: remove home %q: %w", home, err)
	}
	return nil
}

func validateMemberID(id string) error {
	if id == "" || len(id) > 128 {
		return fmt.Errorf("invalid member ID")
	}
	for i := range id {
		switch c := id[i]; {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-' || c == '_' || c == '.':
		default:
			return fmt.Errorf("invalid member ID")
		}
	}
	if id[0] == '.' || id[0] == '-' || strings.Contains(id, "..") {
		return fmt.Errorf("invalid member ID")
	}
	return nil
}

// Archive moves member's home to dest, creating dest's parent owner-only,
// and reports whether the member had a home. dest must not exist. Until
// Unarchive, no home is created for member again: a write that opened the
// home before the move lands in dest, and a later one fails.
func (m *Manager) Archive(member domain.MemberID, dest string) (bool, error) {
	if err := validateMemberID(string(member)); err != nil {
		return false, fmt.Errorf("memberhome: member %q: %w", member, err)
	}
	defer m.lock(string(member))()
	home := filepath.Join(m.root, string(member))
	if _, err := os.Lstat(home); errors.Is(err, fs.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("memberhome: archive home %q: %w", home, err)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return false, fmt.Errorf("memberhome: archive home %q: %w", home, err)
	}
	m.setArchived(member, true)
	if err := os.Rename(home, dest); err != nil {
		m.setArchived(member, false)
		return false, fmt.Errorf("memberhome: archive home %q: %w", home, err)
	}
	return true, nil
}

func (m *Manager) setArchived(member domain.MemberID, archived bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if archived {
		m.archived[member] = true
	} else {
		delete(m.archived, member)
	}
}

func (m *Manager) isArchived(member domain.MemberID) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.archived[member]
}

// Unarchive moves a home Archive moved to src back to member's home.
func (m *Manager) Unarchive(member domain.MemberID, src string) error {
	if err := validateMemberID(string(member)); err != nil {
		return fmt.Errorf("memberhome: member %q: %w", member, err)
	}
	defer m.lock(string(member))()
	if err := os.Rename(src, filepath.Join(m.root, string(member))); err != nil {
		return fmt.Errorf("memberhome: restore home from %q: %w", src, err)
	}
	m.setArchived(member, false)
	return nil
}

// HasHome reports whether member has a home on the server.
func (m *Manager) HasHome(member domain.MemberID) (bool, error) {
	if err := validateMemberID(string(member)); err != nil {
		return false, fmt.Errorf("memberhome: member %q: %w", member, err)
	}
	_, err := os.Lstat(filepath.Join(m.root, string(member)))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}
