// Package releasetest serves fake signed GitHub releases to tests in other
// packages, so an updater under test fetches and verifies a manifest the
// way it does against github.com.
package releasetest

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/selfupdate"
	"github.com/3xDevOps/cloudfield/internal/store"
)

// Key signs every release this package serves. Serve makes selfupdate trust
// it for the rest of the test binary.
var Key = ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x5a}, ed25519.SeedSize))

// NightlyKey is the nightly key Serve makes selfupdate trust, for tests of
// what a nightly key may and may not sign.
var NightlyKey = ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x6b}, ed25519.SeedSize))

var trust sync.Once

// Release is a fake release host. /releases/latest redirects to the latest
// tag, /releases/tag/<tag> answers 200, every tag's download directory
// carries the assets, checksums.txt, and a manifest for them signed with
// Key, and /releases/download/channels/ carries the pointers SetPointer
// publishes.
type Release struct {
	*httptest.Server

	baseURL     string
	mu          sync.Mutex
	latest      string
	assets      map[string][]byte
	served      map[string][]byte
	unsigned    bool
	storeSchema int
	builds      map[string]selfupdate.Release
	pointers    map[string][2][]byte
	requests    map[string]int
}

// Serve starts a release host whose latest release is latest, publishing
// assets under every tag. It is closed when the test ends.
func Serve(t testing.TB, latest string, assets map[string][]byte) *Release {
	t.Helper()
	trust.Do(func() {
		selfupdate.TrustKeysForTest(PublicKey())
		selfupdate.TrustNightlyKeysForTest(NightlyKey.Public().(ed25519.PublicKey))
	})
	r := newRelease(latest, assets)
	r.Server = httptest.NewServer(http.HandlerFunc(r.serveHTTP))
	r.baseURL = r.URL
	t.Cleanup(r.Close)
	return r
}

// Handler is Serve for a command outside a test: it answers as the release
// host at baseURL and trusts nothing, so the binary that talks to it must
// be built to trust PublicKey.
func Handler(baseURL, latest string, assets map[string][]byte) http.Handler {
	r := newRelease(latest, assets)
	r.baseURL = baseURL
	return http.HandlerFunc(r.serveHTTP)
}

// PublicKey is the key whose signature a client must trust to accept a
// release this package serves.
func PublicKey() ed25519.PublicKey { return Key.Public().(ed25519.PublicKey) }

func newRelease(latest string, assets map[string][]byte) *Release {
	return &Release{
		latest:      latest,
		assets:      assets,
		served:      map[string][]byte{},
		storeSchema: store.SchemaVersion(),
		builds:      map[string]selfupdate.Release{},
		pointers:    map[string][2][]byte{},
		requests:    map[string]int{},
	}
}

// SetLatest publishes tag as the latest release; the empty string makes
// /releases/latest answer 503.
func (r *Release) SetLatest(tag string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.latest = tag
}

// Unsign withdraws the manifest and its signature, the shape of a release
// still being published or one from before signing.
func (r *Release) Unsign() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.unsigned = true
}

// SetStoreSchema sets the store schema every manifest records; it starts
// at this build's.
func (r *Release) SetStoreSchema(n int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.storeSchema = n
}

// SetBuild publishes tag on channel with the given build and hotfix; a tag
// without one is a release from before channels.
func (r *Release) SetBuild(tag, channel string, build, hotfix int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.builds[tag] = selfupdate.Release{Channel: channel, Build: build, Hotfix: hotfix}
}

// SignPointer encodes p and signs the encoded bytes with key, returning
// the contents of <channel>.json and its .sig.
func SignPointer(t testing.TB, p selfupdate.Pointer, key ed25519.PrivateKey) (data, sig []byte) {
	t.Helper()
	data, sig, err := selfupdate.SignPointer(p, key)
	if err != nil {
		t.Fatal(err)
	}
	return data, sig
}

// SetPointer serves data and sig as channel's pointer and its signature;
// nil data withdraws both, so they answer 404, and nil sig the signature
// alone.
func (r *Release) SetPointer(channel string, data, sig []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if data == nil {
		delete(r.pointers, channel)
		return
	}
	r.pointers[channel] = [2][]byte{data, sig}
}

// Tamper serves body for the asset name while the manifest keeps
// describing the original.
func (r *Release) Tamper(name string, body []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.served[name] = body
}

// Requests counts the requests made for path.
func (r *Release) Requests(path string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.requests[path]
}

func (r *Release) manifest(tag string) selfupdate.Manifest {
	m := selfupdate.Manifest{
		Schema:      selfupdate.ManifestSchema,
		Channel:     r.builds[tag].Channel,
		Build:       r.builds[tag].Build,
		Hotfix:      r.builds[tag].Hotfix,
		Version:     tag,
		Commit:      "0123456789abcdef0123456789abcdef01234567",
		PublishedAt: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
		NotesURL:    r.baseURL + "/releases/tag/" + tag,
		StoreSchema: r.storeSchema,
		Assets:      map[string]selfupdate.Asset{},
	}
	for name, body := range r.assets {
		sum := sha256.Sum256(body)
		m.Assets[name] = selfupdate.Asset{SHA256: hex.EncodeToString(sum[:]), Size: int64(len(body))}
	}
	return m
}

func (r *Release) serveHTTP(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	path := req.URL.Path
	r.requests[path]++
	switch {
	case path == "/releases/latest":
		if r.latest == "" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		http.Redirect(w, req, "/releases/tag/"+r.latest, http.StatusFound)
	case strings.HasPrefix(path, "/releases/tag/"):
		w.WriteHeader(http.StatusOK)
	case strings.HasPrefix(path, "/releases/download/"):
		tag, name, _ := strings.Cut(strings.TrimPrefix(path, "/releases/download/"), "/")
		body, ok := r.body(tag, name)
		if !ok {
			http.NotFound(w, req)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body)
	default:
		http.NotFound(w, req)
	}
}

func (r *Release) body(tag, name string) ([]byte, bool) {
	if tag == "channels" {
		channel, sig := strings.CutSuffix(name, ".sig")
		p, ok := r.pointers[strings.TrimSuffix(channel, ".json")]
		if !ok || !strings.HasSuffix(channel, ".json") {
			return nil, false
		}
		if sig {
			return p[1], p[1] != nil
		}
		return p[0], true
	}
	switch name {
	case selfupdate.ManifestName, selfupdate.SignatureName:
		if r.unsigned {
			return nil, false
		}
		data, sig, err := selfupdate.SignManifest(r.manifest(tag), Key)
		if err != nil {
			panic(err)
		}
		if name == selfupdate.SignatureName {
			return sig, true
		}
		return data, true
	case "checksums.txt":
		var b strings.Builder
		for asset, m := range r.manifest(tag).Assets {
			fmt.Fprintf(&b, "%s  %s\n", m.SHA256, asset)
		}
		return []byte(b.String()), true
	}
	if body, ok := r.served[name]; ok {
		return body, true
	}
	body, ok := r.assets[name]
	return body, ok
}
