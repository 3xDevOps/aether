package gitengine

import (
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
)

// maxSlugLen caps the task slug embedded in run branch names.
const maxSlugLen = 32

// shortIDLen is how much of the run ID the branch name carries. Run IDs are
// 26-character ULIDs whose trailing characters are the random half, so a
// short tail is what distinguishes two runs of the same task. Six base32
// characters is 30 bits: enough that a collision is rare, not enough that
// one is impossible, which is why uniqueRunBranch falls back to the full ID
// rather than trusting the tail.
const shortIDLen = 6

// slugify turns a task prompt into a branch-safe slug: lowercased, runs of
// [^a-z0-9] collapsed to "-", trimmed of leading/trailing "-", capped at
// maxSlugLen. Returns "" when nothing usable remains.
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

// shortID returns the trailing shortIDLen characters of a run ID, or the
// whole ID when it is already that short (test fixtures use short IDs).
func shortID(run domain.RunID) string {
	if len(run) <= shortIDLen {
		return string(run)
	}
	return string(run)[len(run)-shortIDLen:]
}

// runBranch is the run branch name: aether/run-<slug>-<id>. The task leads
// so the branch reads as what it is in `git branch` output, and the ID
// trails as the disambiguator. Callers pass a short ID; uniqueRunBranch
// passes the full one when a short branch is already taken.
func runBranch(run domain.RunID, task, id string) string {
	if slug := slugify(task); slug != "" {
		return "aether/run-" + slug + "-" + id
	}
	return "aether/run-" + id
}
