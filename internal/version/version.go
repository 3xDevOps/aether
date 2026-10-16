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

// describeTail matches what `git describe --tags --always --dirty` appends
// to the tag it found: the commit count since that tag, the abbreviated
// commit, and an optional dirty marker. It is not a semver prerelease -
// reading it as one ranks a source build *below* the tag it descends from -
// so it is stripped and remembered as "past that tag" instead.
var describeTail = regexp.MustCompile(`-[0-9]+-g[0-9a-f]{4,}(-dirty)?$`)

// TrimDescribe removes a describe tail, reporting whether one was there. A
// bare "-dirty" counts: the tree carries uncommitted changes on top of the
// tag, so it is not that tag any more.
func TrimDescribe(v string) (string, bool) {
	if loc := describeTail.FindStringIndex(v); loc != nil {
		return v[:loc[0]], true
	}
	if trimmed := strings.TrimSuffix(v, "-dirty"); trimmed != v {
		return trimmed, true
	}
	return v, false
}

// ReleaseCore strips build metadata and a `git describe` tail from v,
// leaving the tag a build descends from: v0.8.0-alpha.3-5-gabc123-dirty is
// v0.8.0-alpha.3.
func ReleaseCore(v string) string {
	if i := strings.IndexByte(v, '+'); i >= 0 {
		v = v[:i]
	}
	core, _ := TrimDescribe(v)
	return core
}
