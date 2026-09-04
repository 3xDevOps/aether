// Package memberhome manages one persistent credential home per member.
package memberhome

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
)

// Manager owns the per-member home directories under <data>/homes.
type Manager struct {
	root string
}

// New creates a manager rooted at root. The root is created when the first
// member home is requested.
func New(root string) (*Manager, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("memberhome: root is required")
	}
	return &Manager{root: filepath.Clean(root)}, nil
}

// Root returns the manager's root directory.
func (m *Manager) Root() string {
	return m.root
}

// Path validates member and returns its persistent home, creating it on first
// use with owner-only permissions.
func (m *Manager) Path(member domain.MemberID) (string, error) {
	if err := validateMemberID(string(member)); err != nil {
		return "", fmt.Errorf("memberhome: member %q: %w", member, err)
	}
	home := filepath.Join(m.root, string(member))
	if err := os.MkdirAll(home, 0o700); err != nil {
		return "", fmt.Errorf("memberhome: create home %q: %w", home, err)
	}
	return home, nil
}

// Remove deletes a member's persistent home. Removing an absent home is a
// successful no-op.
func (m *Manager) Remove(member domain.MemberID) error {
	if err := validateMemberID(string(member)); err != nil {
		return fmt.Errorf("memberhome: member %q: %w", member, err)
	}
	home := filepath.Join(m.root, string(member))
	if err := os.RemoveAll(home); err != nil {
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
