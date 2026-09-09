package selfupdate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/version"
)

// tagServer answers /releases/latest with the redirect GitHub sends,
// counting how many times it was dialed.
// The returned setter publishes a different tag mid-test; the empty string
// breaks the endpoint, which is how a failed lookup is driven.
func tagServer(t *testing.T, tag string) (*httptest.Server, *atomic.Int64, func(string)) {
	t.Helper()
	var hits atomic.Int64
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		mu.Lock()
		latest := tag
		mu.Unlock()
		if latest == "" {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Redirect(w, r, "/releases/tag/"+latest, http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits, func(next string) {
		mu.Lock()
		defer mu.Unlock()
		tag = next
	}
}

// fakeClock is a hand-wound clock, so a test can expire a cache without
// sleeping and without trusting the platform's timer resolution.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// withClock points a checker at a hand-wound clock.
func withClock(c *Checker, clock *fakeClock) *Checker {
	c.now = clock.Now
	return c
}

// setVersion pins the build metadata for one test.
func setVersion(t *testing.T, v, commit string) {
	t.Helper()
	oldV, oldC := version.Version, version.Commit
	t.Cleanup(func() { version.Version, version.Commit = oldV, oldC })
	version.Version, version.Commit = v, commit
}

func TestBehind(t *testing.T) {
	cases := []struct {
		running, latest string
		want            bool
	}{
		// Equal, and the plain release ordering.
		{"v1.2.3", "v1.2.3", false},
		{"v1.2.3", "v1.2.4", true},
		{"v1.2.4", "v1.2.3", false},
		{"v1.9.0", "v1.10.0", true},
		{"v2.0.0", "v10.0.0", true},
		// A version neither side can order never reports an update.
		{"dev", "v1.2.3", false},
		{"v1.2.3", "dev", false},
		{"v1.2", "v1.2.3", false},
		{"", "v1.2.3", false},
		// `git describe --always` on an untagged checkout is a bare commit.
		{"091b5f5", "v1.2.3", false},
		// Aether publishes prerelease tags, so these are the live cases.
		{"v0.1.2-alpha.12", "v0.1.2-alpha.12", false},
		{"v0.1.2-alpha.9", "v0.1.2-alpha.12", true},
		{"v0.1.2-alpha.12", "v0.1.2-alpha.9", false},
		{"v0.0.1", "v0.1.2-alpha.12", true},
		{"v0.1.2-alpha.12", "v0.1.2", true},
		{"v0.1.2", "v0.1.2-alpha.12", false},
		{"v0.1.2-alpha", "v0.1.2-alpha.1", true},
		{"v0.1.2-alpha.1", "v0.1.2-beta.1", true},
		// Build metadata never decides precedence.
		{"v1.2.3+build.5", "v1.2.3", false},
		// A build from source carries a `git describe` tail. It sits past
		// the tag it names, so it is never behind that tag: reading the
		// tail as a prerelease would offer this user a downgrade.
		{"v1.2.3-4-gabc123", "v1.2.3", false},
		{"v1.2.3-4-gabc123-dirty", "v1.2.3", false},
		{"v1.2.3-dirty", "v1.2.3", false},
		// It is still behind a genuinely newer release, on the tag it
		// descends from rather than on the tail.
		{"v1.2.3-4-gabc123", "v1.2.4", true},
		{"v1.2.3-4-gabc123-dirty", "v1.2.4", true},
		// The live shape in this repository: a describe tail on top of a
		// prerelease tag, against the next prerelease. The tail is not a
		// prerelease field, so "12" compares with "13" numerically.
		{"v0.1.2-alpha.12-10-g091b5f5", "v0.1.2-alpha.13", true},
		{"v0.1.2-alpha.12-10-g091b5f5", "v0.1.2-alpha.12", false},
		{"v0.1.2-alpha.12-10-g091b5f5", "v0.1.2", true},
	}
	for _, c := range cases {
		if got := Behind(c.running, c.latest); got != c.want {
			t.Errorf("Behind(%q, %q) = %v, want %v", c.running, c.latest, got, c.want)
		}
	}
}

func TestAssetName(t *testing.T) {
	if got := Asset("linux", "arm64"); got != "aether-linux-arm64" {
		t.Fatalf("Asset = %q, want aether-linux-arm64", got)
	}
}

func TestCheckReportsUpdate(t *testing.T) {
	setVersion(t, "v1.2.3", "abc1234")
	srv, hits, _ := tagServer(t, "v1.3.0")

	c := NewChecker(srv.URL, time.Hour)
	got, err := c.Check(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !got.UpdateAvailable || got.Latest != "v1.3.0" {
		t.Fatalf("check = %+v, want an update to v1.3.0", got)
	}
	if got.Version != "v1.2.3" || got.Commit != "abc1234" {
		t.Errorf("build metadata = %q/%q", got.Version, got.Commit)
	}
	if got.Asset != Asset(runtime.GOOS, runtime.GOARCH) {
		t.Errorf("asset = %q", got.Asset)
	}
	if got.ReleaseURL != srv.URL+"/releases/tag/v1.3.0" {
		t.Errorf("release url = %q", got.ReleaseURL)
	}
	if got.Dev || got.Disabled {
		t.Errorf("dev/disabled set on a release build: %+v", got)
	}
	if got.CanSelfUpdate != (runtime.GOOS != "windows") {
		t.Errorf("can_self_update = %v on %s", got.CanSelfUpdate, runtime.GOOS)
	}

	// A second call inside the ttl must answer from the cache.
	if _, err := c.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("dialed %d times, want 1", n)
	}
}

func TestCheckReportsUpToDate(t *testing.T) {
	setVersion(t, "v1.2.3", "abc1234")
	srv, _, _ := tagServer(t, "v1.2.3")

	got, err := NewChecker(srv.URL, time.Hour).Check(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got.UpdateAvailable {
		t.Fatalf("check = %+v, want no update", got)
	}
	if got.Latest != "v1.2.3" {
		t.Fatalf("latest = %q, want v1.2.3", got.Latest)
	}
}

func TestCheckCacheExpires(t *testing.T) {
	setVersion(t, "v1.2.3", "abc1234")
	srv, hits, _ := tagServer(t, "v1.3.0")

	clock := newClock()
	c := withClock(NewChecker(srv.URL, time.Hour), clock)
	if _, err := c.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Still inside the ttl: the answer comes from the cache.
	clock.advance(59 * time.Minute)
	if _, err := c.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("dialed %d times inside the ttl, want 1", n)
	}

	clock.advance(2 * time.Minute)
	if _, err := c.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n := hits.Load(); n != 2 {
		t.Fatalf("dialed %d times, want 2 after the ttl expired", n)
	}
}

func TestCheckCachesFailures(t *testing.T) {
	setVersion(t, "v1.2.3", "abc1234")
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	clock := newClock()
	c := withClock(NewChecker(srv.URL, time.Hour), clock)
	if _, err := c.Check(t.Context()); err == nil {
		t.Fatal("expected an error when the endpoint does not redirect")
	}
	if _, err := c.Check(t.Context()); err == nil {
		t.Fatal("expected the cached error")
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("dialed %d times, want 1 while the failure is cached", n)
	}

	// A failure is cached far more briefly than a success, so an offline
	// machine retries within the hour rather than at the end of it.
	clock.advance(failureTTL + time.Minute)
	if _, err := c.Check(t.Context()); err == nil {
		t.Fatal("expected another error after the failure cache expired")
	}
	if n := hits.Load(); n != 2 {
		t.Fatalf("dialed %d times, want 2 once the failure cache expired", n)
	}
}

func TestCheckDevBuildNeverDials(t *testing.T) {
	setVersion(t, "dev", "unknown")
	srv, hits, _ := tagServer(t, "v1.3.0")

	got, err := NewChecker(srv.URL, time.Hour).Check(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !got.Dev || got.UpdateAvailable || got.Latest != "" {
		t.Fatalf("check = %+v, want a dev build with no update", got)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("dialed %d times on a dev build", n)
	}
}

func TestCheckOptOutNeverDials(t *testing.T) {
	setVersion(t, "v1.2.3", "abc1234")
	t.Setenv(OptOutEnv, "1")
	srv, hits, _ := tagServer(t, "v1.3.0")

	got, err := NewChecker(srv.URL, time.Hour).Check(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !got.Disabled || got.UpdateAvailable || got.Latest != "" {
		t.Fatalf("check = %+v, want a disabled check", got)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("dialed %d times with %s set", n, OptOutEnv)
	}
}

func TestCheckFreshBypassesAndReplacesTheCache(t *testing.T) {
	setVersion(t, "v1.2.3", "abc1234")
	srv, _, setTag := tagServer(t, "v1.3.0")

	c := NewChecker(srv.URL, time.Hour)
	latest := func(get func(context.Context) (Check, error)) string {
		t.Helper()
		got, err := get(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		return got.Latest
	}

	if tag := latest(c.Check); tag != "v1.3.0" {
		t.Fatalf("latest = %q, want v1.3.0", tag)
	}
	setTag("v1.4.0")
	if tag := latest(c.Check); tag != "v1.3.0" {
		t.Fatalf("cached latest = %q, want the cached v1.3.0", tag)
	}
	if tag := latest(c.CheckFresh); tag != "v1.4.0" {
		t.Fatalf("fresh latest = %q, want v1.4.0", tag)
	}
	// The fresh answer replaced the cached one, so the checks that follow
	// do not hand back the tag it superseded.
	setTag("v1.5.0")
	if tag := latest(c.Check); tag != "v1.4.0" {
		t.Fatalf("cached latest = %q, want the freshly cached v1.4.0", tag)
	}
}

// The install path asks through CheckFresh, so a dial that fails has to
// reach it as an error. Handing back a cached tag instead would install a
// release the check never resolved.
func TestCheckFreshFailureDoesNotBorrowTheCachedAnswer(t *testing.T) {
	setVersion(t, "v1.2.3", "abc1234")
	srv, _, setTag := tagServer(t, "v1.3.0")

	c := NewChecker(srv.URL, time.Hour)
	if _, err := c.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	setTag("")
	got, err := c.CheckFresh(t.Context())
	if err == nil {
		t.Fatalf("CheckFresh = %+v, want the dial failure", got)
	}
	if got.Latest != "" {
		t.Fatalf("latest = %q, want no tag from a failed lookup", got.Latest)
	}
	// The cached success outlives the failure: the periodic checks behind it
	// keep the banner they already had rather than losing it to a blip.
	cached, err := c.Check(t.Context())
	if err != nil || cached.Latest != "v1.3.0" {
		t.Fatalf("Check = %+v, %v, want the cached v1.3.0", cached, err)
	}
}

// Two callers arrive on an expired cache; the slow one's dial fails after
// the other has stored a success. The success is the better information of
// the two, so it is what the loser is handed.
func TestCheckPrefersASuccessResolvedWhileItDialed(t *testing.T) {
	setVersion(t, "v1.2.3", "abc1234")
	arrived, release := make(chan struct{}), make(chan struct{})
	var slow atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if slow.CompareAndSwap(false, true) {
			close(arrived)
			<-release
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Redirect(w, r, "/releases/tag/v1.3.0", http.StatusFound)
	}))
	defer srv.Close()

	c := NewChecker(srv.URL, time.Hour)
	loser := make(chan Check, 1)
	go func() {
		got, err := c.Check(context.Background())
		if err != nil {
			t.Errorf("the loser got its own error: %v", err)
		}
		loser <- got
	}()

	<-arrived
	if _, err := c.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	close(release)
	if got := <-loser; got.Latest != "v1.3.0" {
		t.Fatalf("loser latest = %q, want the answer the winner resolved", got.Latest)
	}
}

// A retry inside a cached failure's window must not push that window out,
// or a member clicking Update while offline keeps every other check on the
// stale error indefinitely.
func TestCheckFailureWindowSurvivesRetries(t *testing.T) {
	setVersion(t, "v1.2.3", "abc1234")
	srv, hits, _ := tagServer(t, "")
	clock := newClock()
	c := withClock(NewChecker(srv.URL, time.Hour), clock)

	if _, err := c.Check(t.Context()); err == nil {
		t.Fatal("expected the endpoint to fail")
	}
	clock.advance(4 * time.Minute)
	if _, err := c.CheckFresh(t.Context()); err == nil {
		t.Fatal("expected the retry to fail too")
	}
	clock.advance(2 * time.Minute)
	if _, err := c.Check(t.Context()); err == nil {
		t.Fatal("expected another failure")
	}
	if n := hits.Load(); n != 3 {
		t.Fatalf("dialed %d times, want the window to have expired 5 minutes after the first failure", n)
	}
}

// The dashboard re-checks on half this period, so the hour is what bounds
// how long a published release waits before a banner names it.
func TestDefaultTTLHoldsASuccessForAnHour(t *testing.T) {
	setVersion(t, "v1.2.3", "abc1234")
	srv, _, setTag := tagServer(t, "v1.3.0")
	clock := newClock()
	c := withClock(NewChecker(srv.URL, defaultTTL), clock)

	if _, err := c.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	setTag("v1.4.0")
	clock.advance(59 * time.Minute)
	got, err := c.Check(t.Context())
	if err != nil || got.Latest != "v1.3.0" {
		t.Fatalf("check = %+v, %v, want the cached v1.3.0 inside the hour", got, err)
	}
	clock.advance(2 * time.Minute)
	if got, err = c.Check(t.Context()); err != nil || got.Latest != "v1.4.0" {
		t.Fatalf("check = %+v, %v, want v1.4.0 once the hour passed", got, err)
	}
}

// Two fresh lookups straddle a release and the one that started first
// answers last, carrying the tag that has since been superseded. The cache
// has to keep the newer answer, or every check behind it advertises the
// older release for a whole period.
func TestCheckFreshDoesNotRegressTheCacheToAnOlderLookup(t *testing.T) {
	setVersion(t, "v1.2.3", "abc1234")
	arrived, release := make(chan struct{}), make(chan struct{})
	var slow atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if slow.CompareAndSwap(false, true) {
			close(arrived)
			<-release
			http.Redirect(w, r, "/releases/tag/v1.3.0", http.StatusFound)
			return
		}
		http.Redirect(w, r, "/releases/tag/v1.4.0", http.StatusFound)
	}))
	defer srv.Close()

	c := NewChecker(srv.URL, time.Hour)
	done := make(chan struct{})
	go func() {
		defer close(done)
		// The caller still gets what it resolved; only the cache is ordered.
		got, err := c.CheckFresh(context.Background())
		if err != nil || got.Latest != "v1.3.0" {
			t.Errorf("first lookup = %+v, %v, want its own v1.3.0", got, err)
		}
	}()

	<-arrived
	if got, err := c.CheckFresh(t.Context()); err != nil || got.Latest != "v1.4.0" {
		t.Fatalf("second lookup = %+v, %v, want v1.4.0", got, err)
	}
	close(release)
	<-done

	cached, err := c.Check(t.Context())
	if err != nil || cached.Latest != "v1.4.0" {
		t.Fatalf("cached = %+v, %v, want the later lookup's answer", cached, err)
	}
}
