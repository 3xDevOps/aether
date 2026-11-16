package selfupdate

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/envname"
	"github.com/3xDevOps/Aether/internal/version"
)

// OptOutEnv disables the release check when set to a non-empty value.
const OptOutEnv = "AETHER_NO_UPDATE_CHECK"

// PackagedEnv names the app that installs and updates this binary, such as
// "Android app". Only that app sets it; the binary then still reports the
// newest release but never installs one itself: the app is the update unit.
const PackagedEnv = "AETHER_PACKAGED"

// defaultTTL is how long a successful answer is reused.
const defaultTTL = time.Hour

// A failed check is cached for failureTTL, doubling with each failure in a
// row up to maxFailureTTL, give or take a tenth so clients that failed
// together do not retry together.
const (
	failureTTL    = 5 * time.Minute
	maxFailureTTL = time.Hour
)

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
	// LatestBuild and LatestHotfix are Latest's signed build numbers, 0
	// for a release from before channels.
	LatestBuild  int `json:"latest_build,omitempty"`
	LatestHotfix int `json:"latest_hotfix,omitempty"`
	// UpdateAvailable reports that Latest is newer than this build, by
	// Newer.
	UpdateAvailable bool `json:"update_available"`
	// Asset is the release asset for this platform.
	Asset string `json:"asset,omitempty"`
	// ReleaseURL points at Latest's release page.
	ReleaseURL string `json:"release_url,omitempty"`
	// Dev reports a local build; it never reports an update.
	Dev bool `json:"dev"`
	// Disabled reports that OptOutEnv is set and no network was touched.
	Disabled bool `json:"disabled"`
	// Packaged is the app that installs this binary's updates, empty when
	// the binary updates itself. Asset is empty when it is set.
	Packaged string `json:"packaged,omitempty"`
	// CanSelfUpdate is false in a packaged app, which installs its own
	// updates.
	CanSelfUpdate bool `json:"can_self_update"`
	// CheckedAt is when this answer was produced, cached answers included.
	CheckedAt time.Time `json:"checked_at"`
}

// assetName names the release asset for one platform.
func assetName(goos, goarch string) string { return "aether-" + goos + "-" + goarch }

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

// parseVersion reads vX.Y.Z with an optional prerelease and an optional
// `git describe` tail. Build metadata (+something) never affects
// precedence, so it is discarded.
func parseVersion(tag string) (semver, bool) {
	var out semver
	tag = strings.TrimPrefix(tag, "v")
	if i := strings.IndexByte(tag, '+'); i >= 0 {
		tag = tag[:i]
	}
	tag, out.ahead = version.TrimDescribe(tag)
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
			return cmp.Compare(a.nums[i], b.nums[i])
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
	if c := cmp.Compare(len(a.pre), len(b.pre)); c != 0 {
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
		return cmp.Compare(an, bn)
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

// Checker resolves the newest release and caches the answer. It is safe
// for concurrent use: the gateway calls it from HTTP handlers.
type Checker struct {
	baseURL string
	ttl     time.Duration
	// channel is the release channel the checker follows through its
	// signed pointer, with each channel's high-water mark kept in
	// statePath. Empty asks /releases/latest.
	channel   string
	statePath string
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
	// issued numbers lookups and stored holds the number behind the cached
	// answer, so a lookup that started earlier cannot overwrite one that
	// started later. Two that straddle a release would otherwise leave the
	// superseded tag cached for a whole period.
	issued uint64
	stored uint64
	// failures counts the failed checks cached in a row.
	failures int
}

// NewChecker builds a checker against a GitHub repository base URL such as
// https://github.com/3xDevOps/cloudfield.
func NewChecker(baseURL string, ttl time.Duration) *Checker {
	return &Checker{baseURL: baseURL, ttl: ttl, now: time.Now}
}

// NewChannelChecker is NewChecker following channel's signed pointer,
// keeping the channel marks in the JSON file statePath.
func NewChannelChecker(baseURL string, ttl time.Duration, channel, statePath string) *Checker {
	c := NewChecker(baseURL, ttl)
	c.channel, c.statePath = channel, statePath
	return c
}

// DefaultChecker checks the Aether repository's releases.
func DefaultChecker() *Checker {
	return NewChecker("https://github.com/"+Repo, defaultTTL)
}

// BaseURL is the repository this checker resolves releases from; the
// update path downloads assets from the same repository.
func (c *Checker) BaseURL() string { return c.baseURL }

// Check reports whether a newer release exists, reusing a cached answer
// until it expires. Only a release whose signed manifest is published
// counts; until it is, the check reports no update. A dev build and an
// opted-out process never dial out.
// Two callers arriving on an expired cache may both resolve the tag; that
// costs one extra redirect at worst, where serializing them behind the
// fetch would make one caller's cancelled context everyone's wait. The
// loser of that race is handed the winner's answer rather than its own
// error, which is the better information of the two.
func (c *Checker) Check(ctx context.Context) (Check, error) {
	if cached, err, ok := c.fresh(); ok {
		return cached, err
	}
	out, err := c.CheckFresh(ctx)
	if err != nil {
		if cached, cachedErr, ok := c.fresh(); ok {
			return cached, cachedErr
		}
	}
	return out, err
}

// CheckFresh dials for the newest release, ignoring any cached answer, and
// caches what it finds. A dial that fails is this caller's error: the
// install path asks through here.
func (c *Checker) CheckFresh(ctx context.Context) (Check, error) {
	id := c.nextID()
	out := Check{
		Version:       version.Version,
		Commit:        version.Commit,
		CanSelfUpdate: true,
		CheckedAt:     c.now(),
	}
	if packaged := envname.Getenv(PackagedEnv); packaged != "" {
		out.Packaged, out.CanSelfUpdate = packaged, false
	}
	var err error
	switch {
	case version.Version == devVersion:
		out.Dev = true
	case envname.Getenv(OptOutEnv) != "":
		out.Disabled = true
	default:
		var m Manifest
		m, err = c.latest(ctx)
		switch {
		case err == nil:
			out.Latest, out.LatestBuild, out.LatestHotfix = m.Version, m.Build, m.Hotfix
			out.UpdateAvailable = Newer(m.Release(), Running())
			out.ReleaseURL = c.baseURL + "/releases/tag/" + m.Version
			if out.Packaged == "" {
				out.Asset = assetName(runtime.GOOS, runtime.GOARCH)
			}
		case errors.Is(err, ErrNoRelease):
			err = nil
		}
	}
	c.store(id, out, err)
	return out, err
}

// latest is the newest release on the checker's channel.
func (c *Checker) latest(ctx context.Context) (Manifest, error) {
	if c.channel == "" {
		return LatestManifestFrom(ctx, c.baseURL)
	}
	st, err := loadChannelState(c.statePath)
	if err != nil {
		return Manifest{}, err
	}
	m, marks, err := FollowChannel(ctx, c.baseURL, c.channel, st.Pointers)
	if err != nil {
		return m, err
	}
	// Only a verified pointer leaves a mark. Another process may have
	// raised the mark since the read above, so even an unchanged one is
	// checked again under the lock.
	if got, ok := marks[c.channel]; ok {
		if err := c.raiseMark(got); err != nil {
			return Manifest{}, err
		}
	}
	return m, nil
}

// raiseMark records got as the channel's mark. Other processes share the
// file, so it re-reads it under a lock and refuses got when one of them
// has meanwhile accepted a higher pointer; an equal mark is left as it is.
func (c *Checker) raiseMark(got Mark) error {
	if err := os.MkdirAll(filepath.Dir(c.statePath), 0o700); err != nil {
		return fmt.Errorf("save update state: %w", err)
	}
	f, err := os.OpenFile(c.statePath+".lock", os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("lock update state: %w", err)
	}
	defer func() { _ = f.Close() }()
	unlock, err := lockFile(f)
	if err != nil {
		return fmt.Errorf("lock update state %s: %w", f.Name(), err)
	}
	defer unlock()
	st, err := loadChannelState(c.statePath)
	if err != nil {
		return err
	}
	seen, ok := st.Pointers[c.channel]
	switch {
	case ok && got.below(seen):
		return replayed(c.channel, got, seen)
	case ok && got == seen:
		return nil
	}
	st.Pointers = maps.Clone(st.Pointers)
	if st.Pointers == nil {
		st.Pointers = Marks{}
	}
	st.Pointers[c.channel] = got
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	if err := WriteFileAtomic(c.statePath, append(data, '\n')); err != nil {
		return fmt.Errorf("save update state: %w", err)
	}
	return nil
}

// channelState is the client's update state file.
type channelState struct {
	Pointers Marks `json:"pointers,omitempty"`
}

// loadChannelState reads the state file. One that exists but cannot be
// read fails the check and is never overwritten: starting afresh would
// drop the marks.
func loadChannelState(path string) (channelState, error) {
	var st channelState
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err == nil {
		err = json.Unmarshal(data, &st)
	}
	if err != nil {
		return channelState{}, fmt.Errorf("read update state %s: %w; fix or remove it to resume release checks", path, err)
	}
	return st, nil
}

// nextID takes the number this lookup is ordered by.
func (c *Checker) nextID() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.issued++
	return c.issued
}

// fresh returns the cached answer while it is still valid.
func (c *Checker) fresh() (Check, error, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.liveLocked() {
		return c.cached, c.err, true
	}
	return Check{}, nil, false
}

// Last returns the cached answer and its error without dialing, expired or
// not: its CheckedAt is when it was produced. ok is false before the first
// check finishes.
func (c *Checker) Last() (out Check, err error, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cached, c.err, !c.expires.IsZero()
}

func (c *Checker) liveLocked() bool {
	return !c.expires.IsZero() && c.now().Before(c.expires)
}

// store caches the answer of lookup id. A failure never touches an answer
// that is still live, so a cached failure keeps the window it already has
// rather than being pushed out again by every retry behind it, and a lookup
// that started earlier never replaces a resolved answer of one that started
// later, however the two finish.
func (c *Checker) store(id uint64, out Check, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil && c.liveLocked() {
		return
	}
	if id < c.stored && c.err == nil {
		return
	}
	ttl := c.ttl
	if err != nil {
		ttl = failureBackoff(c.failures)
		c.failures++
	} else {
		c.failures = 0
	}
	c.cached, c.err, c.expires, c.stored = out, err, c.now().Add(ttl), id
}

// failureBackoff is how long the failures+1th failure in a row is cached.
func failureBackoff(failures int) time.Duration {
	d := failureTTL
	for i := 0; i < failures && d < maxFailureTTL; i++ {
		d *= 2
	}
	d = min(d, maxFailureTTL)
	return d + time.Duration(rand.Int64N(int64(d)/5+1)) - d/10
}
