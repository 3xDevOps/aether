// Package version exposes build metadata injected at link time.
//
// Release builds set these values via:
//
//	-ldflags "-X github.com/3xDevOps/Aether/internal/version.Version=v1.2.3 \
//	          -X github.com/3xDevOps/Aether/internal/version.Commit=abc1234"
package version

import (
	"fmt"
	"regexp"
	"strings"
)

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

// describeTail is what `git describe --tags --dirty` appends to a tag.
var describeTail = regexp.MustCompile(`-[0-9]+-g[0-9a-f]{4,}(-dirty)?$`)

// ReleaseCore strips a `git describe` tail from v, leaving the tag a build
// descends from: v0.8.0-alpha.3-5-gabc123-dirty is v0.8.0-alpha.3.
func ReleaseCore(v string) string {
	if loc := describeTail.FindStringIndex(v); loc != nil {
		return v[:loc[0]]
	}
	return strings.TrimSuffix(v, "-dirty")
}
