package selfupdate

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// testKey signs every release this package's tests serve.
var testKey = ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, ed25519.SeedSize))

func TestMain(m *testing.M) {
	TrustKeysForTest(testKey.Public().(ed25519.PublicKey))
	// One attempt per download, so request counts stay exact; the retry
	// tests set their own delays.
	retryDelays = nil
	os.Exit(m.Run())
}

func manifestFor(tag string, assets map[string][]byte) Manifest {
	m := Manifest{
		Schema:      ManifestSchema,
		Version:     tag,
		Commit:      "0123456789abcdef0123456789abcdef01234567",
		PublishedAt: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
		NotesURL:    "https://github.com/" + Repo + "/releases/tag/" + tag,
		StoreSchema: 1,
		Assets:      map[string]Asset{},
	}
	for name, body := range assets {
		sum := sha256.Sum256(body)
		m.Assets[name] = Asset{SHA256: hex.EncodeToString(sum[:]), Size: int64(len(body))}
	}
	return m
}

func signWith(t testing.TB, m Manifest, key ed25519.PrivateKey) (data, sig []byte) {
	t.Helper()
	data, sig, err := SignManifest(m, key)
	if err != nil {
		t.Fatal(err)
	}
	return data, sig
}

// fakeRelease answers the GitHub release URLs the checker and the updater
// use. /releases/latest redirects to the latest tag, or answers 503 while
// it is empty, and every tag's download directory carries assets and a
// manifest listing them signed with testKey.
type fakeRelease struct {
	mu     sync.Mutex
	latest string
	assets map[string][]byte
	// served overrides the bytes sent for an asset, so a download can
	// disagree with the manifest.
	served map[string][]byte
	// unsigned drops the manifest, the shape of a release still being
	// published or one from before signing.
	unsigned bool
	// hits counts /releases/latest lookups.
	hits atomic.Int64
}

func newFakeRelease(t *testing.T, latest string, assets map[string][]byte) (*fakeRelease, *httptest.Server) {
	t.Helper()
	f := &fakeRelease{latest: latest, assets: assets, served: map[string][]byte{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeRelease) setLatest(tag string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.latest = tag
}

func (f *fakeRelease) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch path := r.URL.Path; {
	case path == "/releases/latest":
		f.hits.Add(1)
		if f.latest == "" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		http.Redirect(w, r, "/releases/tag/"+f.latest, http.StatusFound)
	case strings.HasPrefix(path, "/releases/tag/"):
		w.WriteHeader(http.StatusOK)
	case strings.HasPrefix(path, "/releases/download/"):
		tag, name, _ := strings.Cut(strings.TrimPrefix(path, "/releases/download/"), "/")
		if name == ManifestName || name == SignatureName {
			if f.unsigned {
				http.NotFound(w, r)
				return
			}
			data, sig, err := SignManifest(manifestFor(tag, f.assets), testKey)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if name == SignatureName {
				data = sig
			}
			_, _ = w.Write(data)
			return
		}
		body, ok := f.served[name]
		if !ok {
			body, ok = f.assets[name]
		}
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(body)
	default:
		http.NotFound(w, r)
	}
}
