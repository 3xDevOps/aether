package selfupdate

import "regexp"

// releaseTag matches a published Aether release tag: "v" plus a semver
// core, with an optional prerelease ("v0.1.2", "v0.1.2-alpha.12"). Build
// metadata is not published and is not accepted.
var releaseTag = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$`)

// ValidTag reports whether tag is a release tag the server will fetch.
// The tag is interpolated into the pinned releases URL, so anything that
// is not exactly "v" plus semver - a path, a URL, a `git describe` tail
// from somebody's local build - is refused rather than sanitized.
func ValidTag(tag string) bool {
	if !releaseTag.MatchString(tag) {
		return false
	}
	_, described := trimDescribe(tag)
	return !described
}
