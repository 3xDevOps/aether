package selfupdate

import (
	"fmt"
	"regexp"
	"strconv"

	"github.com/3xDevOps/Aether/internal/version"
)

// The release channels an install follows. A manifest signs the channel
// its release was published on.
const (
	ChannelNightly = "nightly"
	ChannelBeta    = "beta"
	ChannelStable  = "stable"
)

var channelPrerelease = regexp.MustCompile(`-(nightly|beta)\.[1-9][0-9]*$`)

// TagChannel is the channel a release tag's form belongs to: -nightly.N is
// nightly, -beta.N is beta, no prerelease is stable. Any other form, such
// as -alpha.N, belongs to none and returns "".
func TagChannel(tag string) string {
	if !ValidTag(tag) {
		return ""
	}
	if m := channelPrerelease.FindStringSubmatch(tag); m != nil {
		return m[1]
	}
	if p, ok := parseVersion(tag); ok && len(p.pre) == 0 {
		return ChannelStable
	}
	return ""
}

// AcceptedOn returns nil when an install following channel may take m: on
// stable, stable releases; on beta, beta and stable; on nightly, all
// three. A manifest without a channel predates channels and is accepted
// on beta and nightly.
func (m Manifest) AcceptedOn(channel string) error {
	ok := false
	switch channel {
	case ChannelStable:
		ok = m.Channel == ChannelStable
	case ChannelBeta:
		ok = m.Channel != ChannelNightly
	case ChannelNightly:
		ok = true
	default:
		return fmt.Errorf("%q is not a release channel (nightly, beta or stable)", channel)
	}
	if ok {
		return nil
	}
	published := m.Channel
	if published == "" {
		published = "no channel"
	}
	return fmt.Errorf("%s is a %s release, which an install on %s does not take", m.Version, published, channel)
}

// Release is what ordering needs of a release: its tag, channel and the
// signed build and hotfix numbers. Build 0 is a release from before
// channels, or a side whose build is not known.
type Release struct {
	Version string
	Channel string
	Build   int
	Hotfix  int
}

// Release is m's place in the release order.
func (m Manifest) Release() Release {
	return Release{Version: m.Version, Channel: m.Channel, Build: m.Build, Hotfix: m.Hotfix}
}

// Running is this binary's place in the release order, from the version,
// build and hotfix it was linked with.
func Running() Release {
	return Release{
		Version: version.Version,
		Channel: TagChannel(version.ReleaseCore(version.Version)),
		Build:   linkedNumber(version.Build),
		Hotfix:  linkedNumber(version.Hotfix),
	}
}

func linkedNumber(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// Newer reports whether a is a newer release than b: its (build, hotfix)
// is higher. Two releases from before channels (build 0) compare by semver
// with Behind. A build-0 side whose version does not parse, such as "dev",
// is never ordered against a numbered one: a wrong guess would offer a
// downgrade.
func Newer(a, b Release) bool {
	switch {
	case a.Build == 0 && b.Build == 0:
		return Behind(b.Version, a.Version)
	case a.Build == 0 && !parses(a.Version), b.Build == 0 && !parses(b.Version):
		return false
	case a.Build != b.Build:
		return a.Build > b.Build
	default:
		return a.Hotfix > b.Hotfix
	}
}

// SameCode reports whether a and b carry the same non-zero (build,
// hotfix): one commit published under two labels, such as a nightly and
// the beta promoted from it.
func SameCode(a, b Release) bool {
	return a.Build != 0 && a.Build == b.Build && a.Hotfix == b.Hotfix
}

func parses(v string) bool {
	_, ok := parseVersion(v)
	return ok
}
