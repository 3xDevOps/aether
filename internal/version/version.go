// Package version exposes build metadata injected at link time.
//
// Release builds set these values via:
//
//	-ldflags "-X github.com/3xDevOps/Aether/internal/version.Version=v1.2.3 \
//	          -X github.com/3xDevOps/Aether/internal/version.Commit=abc1234"
package version

import "fmt"

var (
	// Version is the semantic version of the build, or "dev" for local builds.
	Version = "dev"
	// Commit is the short git commit hash, or "unknown" for local builds.
	Commit = "unknown"
)

// String renders the version and commit as a single human-readable string.
func String() string {
	return fmt.Sprintf("%s (%s)", Version, Commit)
}
