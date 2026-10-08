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
