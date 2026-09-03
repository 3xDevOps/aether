package selfupdate

import (
	"context"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/version"
)

// OptOutEnv disables the release check when set to a non-empty value.
const OptOutEnv = "AETHER_NO_UPDATE_CHECK"

// defaultTTL is how long a successful answer is reused. Releases are rare;
// a dashboard reload must not cost a round trip to GitHub.
const defaultTTL = 6 * time.Hour

// failureTTL caches a failed check so an offline machine is not re-dialed
// on every page load.
const failureTTL = 5 * time.Minute

// devVersion is what version.Version holds in a build without release
// ldflags. Such a build has no release to compare against.
const devVersion = "dev"

// Check is one release-check answer.
type Check struct {
	// Version is the running build ("dev" for local builds).
	Version string `json:"version"`
	// Commit is the running build's short git commit.
	Commit string `json:"commit"`
	// Latest is the newest published release tag, empty when no check ran.
	Latest string `json:"latest,omitempty"`
	// UpdateAvailable reports that Latest is newer than Version.
	UpdateAvailable bool `json:"update_available"`
	// Asset is the release asset for this platform.
	Asset string `json:"asset,omitempty"`
	// ReleaseURL points at Latest's release page.
	ReleaseURL string `json:"release_url,omitempty"`
	// Dev reports a local build; it never reports an update.
	Dev bool `json:"dev"`
	// Disabled reports that OptOutEnv is set and no network was touched.
	Disabled bool `json:"disabled"`
	// CanSelfUpdate is false on Windows, which cannot rename over a
	// running executable.
	CanSelfUpdate bool `json:"can_self_update"`
	// CheckedAt is when this answer was produced, cached answers included.
	CheckedAt time.Time `json:"checked_at"`
}

// Asset names the release asset for one platform.
func Asset(goos, goarch string) string { return "aether-" + goos + "-" + goarch }

// Behind reports whether running is an older release than latest, by semver
// precedence. A version neither side can parse - "dev", a bare commit from
// `git describe --always` on an untagged checkout - is never behind: a
// wrong guess would offer a downgrade.
func Behind(running, latest string) bool {
	a, ok := parseVersion(running)
	if !ok {
		return false
	}
	b, ok := parseVersion(latest)
	if !ok {
		return false
	}
	return compareVersions(a, b) < 0
}

// semver is one parsed version. Aether publishes prerelease tags
// (v0.1.2-alpha.12), so the identifiers after the dash decide precedence as
// often as the three numbers do; and builds from source carry a `git
// describe` tail on top of a tag, which does not.
type semver struct {
	nums [3]int
	// pre is the dot-separated prerelease of the tag itself, empty on a
	// final release. A describe tail is never part of it.
	pre []string
	// ahead reports a build past the tag it names: commits on top of it,
	// or a dirty tree.
	ahead bool
}

// describeTail matches what `git describe --tags --always --dirty` appends
// to the tag it found: the commit count since that tag, the abbreviated
// commit, and an optional dirty marker. It is not a semver prerelease -
// reading it as one ranks a source build *below* the tag it descends from,
// which offers that user a downgrade - so it is stripped and remembered as
// "past that tag" instead.
var describeTail = regexp.MustCompile(`-[0-9]+-g[0-9a-f]{4,}(-dirty)?$`)

// trimDescribe removes a describe tail, reporting whether one was there. A
// bare "-dirty" counts: the tree carries uncommitted changes on top of the
// tag, so it is not that tag any more.
func trimDescribe(tag string) (string, bool) {
	if loc := describeTail.FindStringIndex(tag); loc != nil {
		return tag[:loc[0]], true
	}
	if trimmed := strings.TrimSuffix(tag, "-dirty"); trimmed != tag {
		return trimmed, true
	}
	return tag, false
}

// parseVersion reads vX.Y.Z with an optional prerelease and an optional
// `git describe` tail. Build metadata (+something) never affects
// precedence, so it is discarded.
func parseVersion(tag string) (semver, bool) {
	var out semver
	tag = strings.TrimPrefix(tag, "v")
	if i := strings.IndexByte(tag, '+'); i >= 0 {
		tag = tag[:i]
	}
	tag, out.ahead = trimDescribe(tag)
	core := tag
	if i := strings.IndexByte(tag, '-'); i >= 0 {
		core, out.pre = tag[:i], strings.Split(tag[i+1:], ".")
	}
	fields := strings.Split(core, ".")
	if len(fields) != 3 {
		return semver{}, false
	}
	for i, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil || n < 0 {
			return semver{}, false
		}
		out.nums[i] = n
	}
	for _, id := range out.pre {
		if id == "" {
			return semver{}, false
		}
	}
	return out, true
}

// compareVersions orders two versions by semver precedence: the three
// numbers first, then the prerelease, where any prerelease sorts below the
// release it leads up to, and last a describe tail, which sorts above the
// tag it descends from.
func compareVersions(a, b semver) int {
	for i := range a.nums {
		if a.nums[i] != b.nums[i] {
			return sign(a.nums[i], b.nums[i])
		}
	}
	switch {
	case len(a.pre) == 0 && len(b.pre) == 0:
		return sameTagOrder(a, b)
	case len(a.pre) == 0:
		return 1
	case len(b.pre) == 0:
		return -1
	}
	for i := 0; i < len(a.pre) && i < len(b.pre); i++ {
		if c := comparePrerelease(a.pre[i], b.pre[i]); c != 0 {
			return c
		}
	}
	if c := sign(len(a.pre), len(b.pre)); c != 0 {
		return c
	}
	return sameTagOrder(a, b)
}

// sameTagOrder breaks a tie between two builds of the same tag: one with
// commits on top of it is newer than the tag itself.
func sameTagOrder(a, b semver) int {
	switch {
	case a.ahead == b.ahead:
		return 0
	case a.ahead:
		return 1
	default:
		return -1
	}
}

// comparePrerelease orders one prerelease identifier: numeric identifiers
// compare numerically and always sort below alphanumeric ones, so
// "alpha.2" precedes "alpha.10" rather than following it.
func comparePrerelease(a, b string) int {
	an, aErr := strconv.Atoi(a)
	bn, bErr := strconv.Atoi(b)
	switch {
	case aErr == nil && bErr == nil:
		return sign(an, bn)
	case aErr == nil:
		return -1
	case bErr == nil:
		return 1
	case a == b:
		return 0
	case a < b:
		return -1
	default:
		return 1
	}
}

func sign(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// Checker resolves the newest release and caches the answer. It is safe
// for concurrent use: the gateway calls it from HTTP handlers.
type Checker struct {
	baseURL string
	ttl     time.Duration
	// now reads the clock. Tests drive it so cache expiry does not depend
	// on real time passing: Windows' timer resolution is coarse enough
	// that two calls in a row can read the same instant, which would leave
	// even a nanosecond ttl unexpired.
	now func() time.Time

	// mu guards the cache only, never the network call: a caller whose
	// request context is cancelled must not hold every other caller behind
	// a lock, and must not be the one whose failure everybody caches.
	mu      sync.Mutex
	cached  Check
	err     error
	expires time.Time
}

// NewChecker builds a checker against a GitHub repository base URL such as
// https://github.com/3xDevOps/Aether.
func NewChecker(baseURL string, ttl time.Duration) *Checker {
	return &Checker{baseURL: baseURL, ttl: ttl, now: time.Now}
}

// DefaultChecker checks the Aether repository's releases.
func DefaultChecker() *Checker {
	return NewChecker("https://github.com/"+Repo, defaultTTL)
}

// BaseURL is the repository this checker resolves releases from; the
// update path downloads assets from the same repository.
func (c *Checker) BaseURL() string { return c.baseURL }

// Check reports whether a newer release exists, reusing a cached answer
// until it expires. A dev build and an opted-out process never dial out.
// Two callers arriving on an expired cache may both resolve the tag; that
// costs one extra redirect at worst, where serializing them behind the
// fetch would make one caller's cancelled context everyone's wait.
func (c *Checker) Check(ctx context.Context) (Check, error) {
	if cached, err, ok := c.fresh(); ok {
		return cached, err
	}
	out := Check{
		Version:       version.Version,
		Commit:        version.Commit,
		CanSelfUpdate: runtime.GOOS != "windows",
		CheckedAt:     c.now(),
	}
	var err error
	switch {
	case version.Version == devVersion:
		out.Dev = true
	case os.Getenv(OptOutEnv) != "":
		out.Disabled = true
	default:
		var latest string
		if latest, err = LatestTag(ctx, c.baseURL+"/releases/latest"); err == nil {
			out.Latest = latest
			out.UpdateAvailable = Behind(version.Version, latest)
			out.Asset = Asset(runtime.GOOS, runtime.GOARCH)
			out.ReleaseURL = c.baseURL + "/releases/tag/" + latest
		}
	}
	return c.store(out, err)
}

// fresh returns the cached answer while it is still valid.
func (c *Checker) fresh() (Check, error, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.expires.IsZero() && c.now().Before(c.expires) {
		return c.cached, c.err, true
	}
	return Check{}, nil, false
}

// store caches one resolved answer and returns what the caller should see.
// A failure never overwrites a success another caller resolved while this
// one was dialing: that answer is both better information and the longer
// cache, so this caller is handed it instead of its own error.
func (c *Checker) store(out Check, err error) (Check, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil && c.err == nil && c.now().Before(c.expires) {
		return c.cached, nil
	}
	ttl := c.ttl
	if err != nil {
		ttl = failureTTL
	}
	c.cached, c.err, c.expires = out, err, c.now().Add(ttl)
	return out, err
}
