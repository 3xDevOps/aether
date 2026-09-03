package selfupdate

import (
	"context"
	"os"
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
// precedence. Anything neither side can parse as a version - "dev", a git
// describe with a commit suffix - is not behind, because guessing wrong
// would nag a build that is actually ahead.
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

// semver is one parsed tag. Aether publishes prerelease tags
// (v0.1.2-alpha.12), so the identifiers after the dash decide precedence as
// often as the three numbers do and cannot be dropped.
type semver struct {
	nums [3]int
	// pre is the dot-separated prerelease, empty on a final release.
	pre []string
}

// parseVersion reads vX.Y.Z with an optional prerelease. Build metadata
// (+something) never affects precedence, so it is discarded.
func parseVersion(tag string) (semver, bool) {
	var out semver
	tag = strings.TrimPrefix(tag, "v")
	if i := strings.IndexByte(tag, '+'); i >= 0 {
		tag = tag[:i]
	}
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

// compareVersions orders two tags by semver precedence: the three numbers
// first, then the prerelease, where any prerelease sorts below the release
// it leads up to.
func compareVersions(a, b semver) int {
	for i := range a.nums {
		if a.nums[i] != b.nums[i] {
			return sign(a.nums[i], b.nums[i])
		}
	}
	switch {
	case len(a.pre) == 0 && len(b.pre) == 0:
		return 0
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
	return sign(len(a.pre), len(b.pre))
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

	// mu is held across the network call so a burst of dashboard loads
	// resolves the tag once instead of once per request.
	mu      sync.Mutex
	cached  Check
	err     error
	expires time.Time
}

// NewChecker builds a checker against a GitHub repository base URL such as
// https://github.com/3xDevOps/Aether.
func NewChecker(baseURL string, ttl time.Duration) *Checker {
	return &Checker{baseURL: baseURL, ttl: ttl}
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
func (c *Checker) Check(ctx context.Context) (Check, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if !c.expires.IsZero() && now.Before(c.expires) {
		return c.cached, c.err
	}
	out := Check{
		Version:       version.Version,
		Commit:        version.Commit,
		CanSelfUpdate: runtime.GOOS != "windows",
		CheckedAt:     now,
	}
	switch {
	case version.Version == devVersion:
		out.Dev = true
	case os.Getenv(OptOutEnv) != "":
		out.Disabled = true
	default:
		latest, err := LatestTag(ctx, c.baseURL+"/releases/latest")
		if err != nil {
			c.cached, c.err, c.expires = out, err, now.Add(failureTTL)
			return out, err
		}
		out.Latest = latest
		out.UpdateAvailable = Behind(version.Version, latest)
		out.Asset = Asset(runtime.GOOS, runtime.GOARCH)
		out.ReleaseURL = c.baseURL + "/releases/tag/" + latest
	}
	c.cached, c.err, c.expires = out, nil, now.Add(c.ttl)
	return out, nil
}
