package selfupdate

import (
	"context"
	"fmt"
	"regexp"
	"strconv"

	"github.com/3xDevOps/cloudfield/internal/version"
)

// The release channels an install follows. A manifest signs the channel
// its release was published on.
const (
	ChannelNightly = "nightly"
	ChannelBeta    = "beta"
	ChannelStable  = "stable"
)

var channelNumber = regexp.MustCompile(`^[1-9][0-9]*$`)

// TagChannel is the channel a release tag's form belongs to: -nightly.N is
// nightly, -beta.N is beta, no prerelease is stable. Any other form, such
// as -alpha.N, belongs to none and returns "".
func TagChannel(tag string) string {
	if !ValidTag(tag) {
		return ""
	}
	p, ok := parseVersion(tag)
	switch {
	case !ok:
		return ""
	case len(p.pre) == 0:
		return ChannelStable
	case len(p.pre) == 2 && (p.pre[0] == ChannelNightly || p.pre[0] == ChannelBeta) && channelNumber.MatchString(p.pre[1]):
		return p.pre[0]
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

// A binary linked with a build or hotfix that is not a number refuses to
// start rather than ordering itself as build 0.
func init() { Running() }

// Running is this binary's place in the release order, from the version,
// build and hotfix it was linked with.
func Running() Release {
	return Release{
		Version: version.Version,
		Channel: TagChannel(version.ReleaseCore(version.Version)),
		Build:   linkedNumber("Build", version.Build),
		Hotfix:  linkedNumber("Hotfix", version.Hotfix),
	}
}

func linkedNumber(name, s string) int {
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		panic(fmt.Sprintf("selfupdate: this binary was linked with version.%s=%q, which is not a non-negative integer", name, s))
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

// Downgrade is why installing target would not move running forward:
// running is newer, or target is the same code on the same channel. The
// same code under another channel's label installs. Two releases from
// before channels compare by semver against running's release core, so a
// build past a tag may install that tag.
func Downgrade(running, target Release) error {
	running.Version = version.ReleaseCore(running.Version)
	switch {
	case Newer(running, target):
		return fmt.Errorf("%s is older than the running %s", label(target), label(running))
	case SameCode(running, target) && running.Channel == target.Channel:
		return fmt.Errorf("%s is the same build as the running %s", label(target), label(running))
	}
	return nil
}

func label(r Release) string {
	switch {
	case r.Build == 0:
		return r.Version
	case r.Hotfix == 0:
		return fmt.Sprintf("%s (build %d)", r.Version, r.Build)
	}
	return fmt.Sprintf("%s (build %d, hotfix %d)", r.Version, r.Build, r.Hotfix)
}

// RefuseDowngrade fetches tag's signed manifest and refuses it, by
// Downgrade, when installing it would not move this binary forward. An
// update target this binary picked itself goes through it; a version the
// user named does not.
func RefuseDowngrade(ctx context.Context, baseURL, tag string) error {
	m, err := FetchManifestFrom(ctx, baseURL, tag)
	if err != nil {
		return err
	}
	if err := Downgrade(Running(), m.Release()); err != nil {
		return fmt.Errorf("refusing to update: %w; name it with --version to install it anyway", err)
	}
	return nil
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
