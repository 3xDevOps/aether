package memberhome

import (
	"fmt"
	"path"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
)

const (
	claudeCredentialPath = ".claude/.credentials.json"
	codexCredentialPath  = ".codex/auth.json"
)

// ReadCredential returns one of the vendor credential files from a member's
// home. The file is opened through the home root, so paths planted by a run
// cannot escape the member home or make the server follow a symlink. A missing
// file returns nil, nil. The caller must provide a small explicit size limit.
func (m *Manager) ReadCredential(member domain.MemberID, name string, limit int64) ([]byte, error) {
	if name != claudeCredentialPath && name != codexCredentialPath {
		return nil, fmt.Errorf("memberhome: unsupported credential file %q", name)
	}
	if limit <= 0 {
		return nil, fmt.Errorf("memberhome: credential file limit must be positive")
	}
	if name != path.Clean(name) || strings.HasPrefix(name, "/") || strings.Contains(name, "\x00") {
		return nil, fmt.Errorf("memberhome: unsafe credential file path")
	}
	root, err := m.openHome(member)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	data, err := readRegularFile(root, name, limit)
	if err != nil {
		return nil, fmt.Errorf("memberhome: read credential %q for %q: %w", name, member, err)
	}
	return data, nil
}
