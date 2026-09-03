package selfupdate

import (
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/version"
)

// tagServer answers /releases/latest with the redirect GitHub sends,
// counting how many times it was dialed.
func tagServer(t *testing.T, tag string) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, "/releases/tag/"+tag, http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
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
	srv, hits := tagServer(t, "v1.3.0")

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
	srv, _ := tagServer(t, "v1.2.3")

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
	srv, hits := tagServer(t, "v1.3.0")

	c := NewChecker(srv.URL, time.Nanosecond)
	if _, err := c.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
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

	c := NewChecker(srv.URL, time.Hour)
	if _, err := c.Check(t.Context()); err == nil {
		t.Fatal("expected an error when the endpoint does not redirect")
	}
	if _, err := c.Check(t.Context()); err == nil {
		t.Fatal("expected the cached error")
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("dialed %d times, want 1 while the failure is cached", n)
	}
}

func TestCheckDevBuildNeverDials(t *testing.T) {
	setVersion(t, "dev", "unknown")
	srv, hits := tagServer(t, "v1.3.0")

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
	srv, hits := tagServer(t, "v1.3.0")

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
