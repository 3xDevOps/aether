package selfupdate

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var testAssets = map[string][]byte{"aether-linux-amd64": []byte("binary")}

func TestVerifyManifestAcceptsATrustedSignature(t *testing.T) {
	data, sig := signWith(t, manifestFor("v1.3.0", testAssets), testKey)
	m, err := VerifyManifest(data, sig, "v1.3.0")
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != "v1.3.0" || m.Assets["aether-linux-amd64"].Size != int64(len("binary")) {
		t.Fatalf("manifest = %+v", m)
	}
}

func TestVerifyManifestRejectsTamperedBytes(t *testing.T) {
	data, sig := signWith(t, manifestFor("v1.3.0", testAssets), testKey)
	tampered := bytes.Replace(data, []byte(`"size": 6`), []byte(`"size": 7`), 1)
	if bytes.Equal(tampered, data) {
		t.Fatal("the tamper did not change the manifest")
	}
	if _, err := VerifyManifest(tampered, sig, "v1.3.0"); err == nil || !strings.Contains(err.Error(), "not signed by a trusted release key") {
		t.Fatalf("err = %v, want an untrusted signature", err)
	}
}

func TestVerifyManifestRejectsAnUntrustedKey(t *testing.T) {
	other := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{9}, ed25519.SeedSize))
	data, sig := signWith(t, manifestFor("v1.3.0", testAssets), other)
	if _, err := VerifyManifest(data, sig, "v1.3.0"); err == nil || !strings.Contains(err.Error(), "not signed by a trusted release key") {
		t.Fatalf("err = %v, want an untrusted signature", err)
	}
}

func TestVerifyManifestAcceptsANextKeyShippedAhead(t *testing.T) {
	next := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{9}, ed25519.SeedSize))
	restore := TrustKeysForTest(testKey.Public().(ed25519.PublicKey), next.Public().(ed25519.PublicKey))
	defer restore()
	data, sig := signWith(t, manifestFor("v1.3.0", testAssets), next)
	if _, err := VerifyManifest(data, sig, "v1.3.0"); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyManifestRejectsAnotherVersion(t *testing.T) {
	// A validly signed manifest of an older release, replayed under a
	// newer tag, must not install the older binaries.
	data, sig := signWith(t, manifestFor("v1.2.0", testAssets), testKey)
	if _, err := VerifyManifest(data, sig, "v1.3.0"); err == nil || !strings.Contains(err.Error(), "describes v1.2.0") {
		t.Fatalf("err = %v, want a version mismatch", err)
	}
}

func TestVerifyManifestRejectsAnUnknownSchema(t *testing.T) {
	m := manifestFor("v1.3.0", testAssets)
	m.Schema = 2
	data, sig := signWith(t, m, testKey)
	if _, err := VerifyManifest(data, sig, "v1.3.0"); err == nil || !strings.Contains(err.Error(), "schema 2") {
		t.Fatalf("err = %v, want a schema error", err)
	}
}

func TestLatestManifestWithoutAManifestIsNoRelease(t *testing.T) {
	f, srv := newFakeRelease(t, "v1.3.0", testAssets)
	f.unsigned = true
	_, err := LatestManifestFrom(t.Context(), srv.URL)
	if !errors.Is(err, ErrNoRelease) {
		t.Fatalf("err = %v, want ErrNoRelease", err)
	}

	setVersion(t, "v1.2.3", "abc1234")
	got, err := NewChecker(srv.URL, time.Hour).Check(t.Context())
	if err != nil {
		t.Fatalf("a release still being published is not a failed check: %v", err)
	}
	if got.UpdateAvailable || got.Latest != "" {
		t.Fatalf("check = %+v, want no update", got)
	}
}

func TestFetchManifestNamesAnUnsignedTag(t *testing.T) {
	f, srv := newFakeRelease(t, "v1.3.0", testAssets)
	f.unsigned = true
	_, err := fetchManifest(t.Context(), srv.URL, "v0.7.0")
	if err == nil || !strings.Contains(err.Error(), "release v0.7.0 has no signed aether-release.json") ||
		!strings.Contains(err.Error(), "predates signed releases") {
		t.Fatalf("err = %v, want it to name v0.7.0 as unsigned", err)
	}
}

func TestDownloadsRefuseARedirectOffHTTPS(t *testing.T) {
	var hits atomic.Int64
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("served over http"))
	}))
	defer plain.Close()
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, plain.URL+r.URL.Path, http.StatusFound)
	}))
	defer secure.Close()
	old := httpClient
	defer func() { httpClient = old }()
	httpClient = &http.Client{Transport: secure.Client().Transport, CheckRedirect: HTTPSOnly}
	retryDelays = []time.Duration{time.Millisecond, time.Millisecond}
	defer func() { retryDelays = nil }()

	_, err := fetchManifest(t.Context(), secure.URL, "v1.3.0")
	if !errors.Is(err, errInsecureRedirect) {
		t.Fatalf("err = %v, want the redirect refused", err)
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("dialed %d times; a refused redirect is not retried", n)
	}
}

func TestDownloadsRetryServerErrorsWithBackoff(t *testing.T) {
	retryDelays = []time.Duration{time.Millisecond, time.Millisecond}
	defer func() { retryDelays = nil }()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) < 3 {
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	body, err := fetchSmall(t.Context(), srv.URL+"/x")
	if err != nil || string(body) != "ok" {
		t.Fatalf("body = %q, err = %v; want the third attempt's answer", body, err)
	}

	hits.Store(-10)
	_, err = fetchSmall(t.Context(), srv.URL+"/x")
	if err == nil || !strings.Contains(err.Error(), "after 3 attempts") || !strings.Contains(err.Error(), "503 Service Unavailable") {
		t.Fatalf("err = %v, want the real 503 after 3 attempts", err)
	}
}

func TestDownloadsDoNotRetryANotFound(t *testing.T) {
	retryDelays = []time.Duration{time.Millisecond, time.Millisecond}
	defer func() { retryDelays = nil }()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.NotFound(w, r)
	}))
	defer srv.Close()

	if _, err := fetchSmall(t.Context(), srv.URL+"/x"); err == nil {
		t.Fatal("expected the 404")
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("dialed %d times, want 1", n)
	}
}

func TestDownloadRestartsAnInterruptedBody(t *testing.T) {
	retryDelays = []time.Duration{time.Millisecond, time.Millisecond}
	defer func() { retryDelays = nil }()
	body := bytes.Repeat([]byte("0123456789"), 1000)
	want := manifestFor("v1.3.0", map[string][]byte{"bin": body}).Assets["bin"]
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		if hits.Add(1) > 1 {
			_, _ = w.Write(body)
			return
		}
		_, _ = w.Write(body[:len(body)/2])
		w.(http.Flusher).Flush()
		conn, _, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = conn.Close()
	}))
	defer srv.Close()

	f, err := os.CreateTemp(t.TempDir(), "bin")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err = download(t.Context(), srv.URL+"/bin", f, want, nil); err != nil {
		t.Fatal(err)
	}
	if n := hits.Load(); n != 2 {
		t.Fatalf("dialed %d times, want the cut attempt and one retry", n)
	}
	got, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("staged %d bytes, want exactly the %d of the full retry", len(got), len(body))
	}
}

func TestDownloadRejectsAStreamedBodyLongerThanTheManifest(t *testing.T) {
	retryDelays = []time.Duration{time.Millisecond, time.Millisecond}
	defer func() { retryDelays = nil }()
	body := []byte("binary")
	want := manifestFor("v1.3.0", map[string][]byte{"bin": body}).Assets["bin"]
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		// Flushing before the end drops Content-Length, so only the
		// streamed byte count can catch the mismatch.
		_, _ = w.Write(body)
		w.(http.Flusher).Flush()
		_, _ = w.Write([]byte("-and-more"))
	}))
	defer srv.Close()

	f, err := os.CreateTemp(t.TempDir(), "bin")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	err = download(t.Context(), srv.URL+"/bin", f, want, nil)
	if err == nil || !strings.Contains(err.Error(), "sent 7 bytes, the manifest says 6") {
		t.Fatalf("err = %v, want a streamed size mismatch", err)
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("dialed %d times; a body longer than the manifest is not retried", n)
	}
}

func TestLatestTagReturnsAnUnexpectedStatus(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		http.Error(w, "rate limited", http.StatusForbidden)
	}))
	defer srv.Close()

	_, err := LatestTag(t.Context(), srv.URL+"/releases/latest")
	if err == nil || errors.Is(err, ErrNoRelease) || !strings.Contains(err.Error(), "GET "+srv.URL+"/releases/latest: 403 Forbidden") {
		t.Fatalf("err = %v, want the real 403, not ErrNoRelease", err)
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("dialed %d times, want 1", n)
	}
}

func TestLatestTagWithoutAReleaseIsNoRelease(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/releases/latest" {
			http.Redirect(w, r, "/releases", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if _, err := LatestTag(t.Context(), srv.URL+"/releases/latest"); !errors.Is(err, ErrNoRelease) {
		t.Fatalf("err = %v, want ErrNoRelease", err)
	}
}

func TestLatestTagFollowsSeveralRedirects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases/latest":
			http.Redirect(w, r, "/canonical/releases/latest", http.StatusMovedPermanently)
		case "/canonical/releases/latest":
			http.Redirect(w, r, "/canonical/releases/tag/v1.2.3", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	tag, err := LatestTag(t.Context(), srv.URL+"/releases/latest")
	if err != nil || tag != "v1.2.3" {
		t.Fatalf("tag = %q, err = %v; want v1.2.3", tag, err)
	}
}

func TestLatestTagAcceptsATagPageServedDirectly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tag, err := LatestTag(t.Context(), srv.URL+"/releases/tag/v1.2.3")
	if err != nil || tag != "v1.2.3" {
		t.Fatalf("tag = %q, err = %v; want v1.2.3", tag, err)
	}
}

func TestFailureBackoffDoublesToAnHourWithJitter(t *testing.T) {
	want := []time.Duration{5 * time.Minute, 10 * time.Minute, 20 * time.Minute, 40 * time.Minute, time.Hour, time.Hour}
	for failures, base := range want {
		for range 50 {
			got := failureBackoff(failures)
			if got < base-base/10 || got > base+base/10 {
				t.Fatalf("failureBackoff(%d) = %v, want %v ±10%%", failures, got, base)
			}
		}
	}
}

func TestCheckBacksOffAfterFailuresInARow(t *testing.T) {
	setVersion(t, "v1.2.3", "abc1234")
	srv, hits, setTag := tagServer(t, "")
	clock := newClock()
	c := withClock(NewChecker(srv.URL, time.Hour), clock)

	if _, err := c.Check(t.Context()); err == nil {
		t.Fatal("expected a failure")
	}
	// The second failure is cached about ten minutes, not five.
	clock.advance(6 * time.Minute)
	if _, err := c.Check(t.Context()); err == nil {
		t.Fatal("expected a second failure")
	}
	clock.advance(8 * time.Minute)
	if _, err := c.Check(t.Context()); err == nil {
		t.Fatal("expected the cached second failure")
	}
	if n := hits.Load(); n != 2 {
		t.Fatalf("dialed %d times, want 2 while backing off", n)
	}
	last, lastErr, ok := c.Last()
	if !ok || lastErr == nil || last.CheckedAt.IsZero() {
		t.Fatalf("Last = %+v, %v, %v; want the failure and when it happened", last, lastErr, ok)
	}

	// A refresh bypasses the backoff, and a success resets it.
	setTag("v1.3.0")
	got, err := c.CheckFresh(t.Context())
	if err != nil || !got.UpdateAvailable {
		t.Fatalf("refresh = %+v, %v; want the update", got, err)
	}
	if c.failures != 0 {
		t.Fatalf("failures = %d after a success, want 0", c.failures)
	}
}
