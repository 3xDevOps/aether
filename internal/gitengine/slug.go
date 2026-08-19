package gitengine

import (
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
)

// maxSlugLen caps the task slug embedded in run branch names.
const maxSlugLen = 24

// slugify turns a task prompt into a branch-safe slug: lowercased, runs of
// [^a-z0-9] collapsed to "-", trimmed of leading/trailing "-", max 24
// chars. Returns "" when nothing usable remains.
func slugify(task string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(task) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
		default:
			dash = true
		}
	}
	s := b.String()
	if len(s) > maxSlugLen {
		s = strings.TrimRight(s[:maxSlugLen], "-")
	}
	return s
}

// runBranch is the run branch name: aether/run-<run-id>-<slug>, with the
// -<slug> suffix omitted when the task slugs to nothing.
func runBranch(run domain.RunID, task string) string {
	name := "aether/run-" + string(run)
	if slug := slugify(task); slug != "" {
		name += "-" + slug
	}
	return name
}
