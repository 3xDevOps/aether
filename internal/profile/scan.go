package profile

import (
	"fmt"

	"github.com/3xDevOps/Aether/internal/secretscan"
)

// ScanFiles rejects files that gitleaks flags unless allow[path] is set.
// Paths in allow are slash-separated and relative to the profile root.
func ScanFiles(files []File, allow map[string]bool) error {
	for _, f := range files {
		if allow[f.Path] {
			continue
		}
		hits := secretscan.Scan(f.Path, f.Content)
		if len(hits) == 0 {
			continue
		}
		h := hits[0]
		return fmt.Errorf("%w: secret detected in %s at %s (%s)", ErrDenied, h.Path, h.Location, h.Kind)
	}
	return nil
}
