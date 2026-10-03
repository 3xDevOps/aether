package profile

import (
	"fmt"

	"github.com/3xDevOps/Aether/internal/secretscan"
)

// defaultIgnores are the gitignore-style patterns Aether applies to a
// harness before the user's own .aether-profile-ignore. They are the paths
// a harness writes as it runs - transcripts, shell snapshots, telemetry, and
// scratch trees - rather than anything the user configured.
//
// Callers must treat the returned slices as immutable. DefaultIgnores returns
// the definitions directly so policy lookups do not allocate; callers that
// need to append local rules must clone the slice first.
var defaultIgnores = map[string][]string{
	"claude": {
		"projects/", "shell-snapshots/", "statsig/", "todos/",
		"file-history/", "history.jsonl", "daemon/",
	},
	// codex/tmp holds a per-run scratch directory whose apply_patch entry is
	// a symlink out to the codex binary; sessions/ is codex's own transcript
	// archive, the same thing claude keeps in projects/.
	"codex": {"tmp/", ".tmp/", "sessions/"},
	// pi keeps its transcript archive in agent/sessions/ and unpacks
	// extension downloads under agent/tmp/. The rest of its agent directory is
	// configuration, agent/npm/ included: that one holds the extension
	// packages the member installed.
	"pi": {"agent/sessions/", "agent/tmp/"},
	// Keep exact database names: extension-owned databases may be configuration.
	// OMP's stats and model discovery caches use SQLite WAL sidecars.
	"omp": {
		"agent/sessions/", "agent/terminal-sessions/", "agent/cache/",
		"agent/history.db", "agent/history.db-shm", "agent/history.db-wal",
		"agent/models.db", "agent/models.db-shm", "agent/models.db-wal",
		"stats.db", "stats.db-shm", "stats.db-wal",
		"natives/", "cache/", "logs/", "run/", "collab/",
		"puppeteer/", "webcache/",
	},
}

// DefaultIgnores returns the immutable-by-convention runtime/history patterns
// for harnessName. The returned slice must not be mutated.
func DefaultIgnores(harnessName string) []string {
	return defaultIgnores[harnessName]
}

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
