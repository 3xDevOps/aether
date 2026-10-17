package selfupdate

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

const (
	// ManifestName is the signed release manifest every release publishes
	// after all of its other assets.
	ManifestName = "aether-release.json"
	// SignatureName holds the std-base64 ed25519 signature over the exact
	// bytes of ManifestName.
	SignatureName = ManifestName + ".sig"
	// ManifestSchema is the manifest format this build reads and writes.
	ManifestSchema = 1
	// maxManifestSize bounds the manifest and signature downloads.
	maxManifestSize = 1 << 20
)

// defaultBaseURL is the repository releases are resolved and downloaded
// from.
const defaultBaseURL = "https://github.com/" + Repo

// ErrNoRelease reports that there is nothing to update to: the repository
// has no release yet, or its newest release is still being published and
// has no signed manifest. A release check treats it as "up to date", not
// as a failure.
var ErrNoRelease = errors.New("no release to update to")

// errUnsigned ends the error for a release without a manifest.
var errUnsigned = errors.New("it predates signed releases or is still being published, and no updater installs it")

// Manifest is a release's verified aether-release.json.
type Manifest struct {
	Schema      int       `json:"schema"`
	Version     string    `json:"version"`
	Commit      string    `json:"commit"`
	PublishedAt time.Time `json:"published_at"`
	NotesURL    string    `json:"notes_url"`
	// StoreSchema is the highest store migration the release's server
	// applies.
	StoreSchema int              `json:"store_schema"`
	Assets      map[string]Asset `json:"assets"`

	baseURL string
}

// Asset is one published file's digest and length.
type Asset struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// AssetURL is where the release publishes the asset name.
func (m Manifest) AssetURL(name string) string {
	base := m.baseURL
	if base == "" {
		base = defaultBaseURL
	}
	return base + "/releases/download/" + m.Version + "/" + name
}

// DesktopAssetName names the prebuilt Windows desktop app for goarch:
// electron-builder's win-unpacked directory, zipped with its contents at the
// root.
func DesktopAssetName(goarch string) string {
	return "aether-desktop-windows-" + goarch + ".zip"
}

// Download streams the asset name into f and checks it against the size
// and sha256 m lists for it.
func (m Manifest) Download(ctx context.Context, name string, f *os.File) error {
	want, ok := m.Assets[name]
	if !ok {
		return fmt.Errorf("release %s does not publish %s", m.Version, name)
	}
	if err := download(ctx, m.AssetURL(name), f, want); err != nil {
		return fmt.Errorf("download %s: %w", name, err)
	}
	return nil
}

// FetchManifest downloads tag's manifest and signature and verifies them.
func FetchManifest(ctx context.Context, tag string) (Manifest, error) {
	return FetchManifestFrom(ctx, defaultBaseURL, tag)
}

// LatestManifest resolves the newest release and fetches its manifest. A
// newest release without one yet is ErrNoRelease.
func LatestManifest(ctx context.Context) (Manifest, error) {
	return LatestManifestFrom(ctx, defaultBaseURL)
}

// FetchManifestFrom is FetchManifest against a repository base URL such
// as Checker.BaseURL.
func FetchManifestFrom(ctx context.Context, baseURL, tag string) (Manifest, error) {
	return fetchManifest(ctx, baseURL, tag)
}

// LatestManifestFrom is LatestManifest against a repository base URL such
// as Checker.BaseURL.
func LatestManifestFrom(ctx context.Context, baseURL string) (Manifest, error) {
	tag, err := LatestTag(ctx, baseURL+"/releases/latest")
	if err != nil {
		return Manifest{}, err
	}
	m, err := fetchManifest(ctx, baseURL, tag)
	if errors.Is(err, errUnsigned) {
		return Manifest{}, fmt.Errorf("%w: %w", ErrNoRelease, err)
	}
	return m, err
}

func fetchManifest(ctx context.Context, baseURL, tag string) (Manifest, error) {
	if !ValidTag(tag) {
		return Manifest{}, fmt.Errorf("%q is not a release tag", tag)
	}
	url := baseURL + "/releases/download/" + tag + "/" + ManifestName
	data, err := fetchSmall(ctx, url)
	if err != nil {
		return Manifest{}, unsigned(tag, err)
	}
	sig, err := fetchSmall(ctx, url+".sig")
	if err != nil {
		return Manifest{}, unsigned(tag, err)
	}
	m, err := VerifyManifest(data, sig, tag)
	if err != nil {
		return Manifest{}, err
	}
	m.baseURL = baseURL
	return m, nil
}

// unsigned names a 404 for the manifest or its signature as a release that
// predates signing; any other failure stays as it was.
func unsigned(tag string, err error) error {
	var status *statusError
	if errors.As(err, &status) && status.code == http.StatusNotFound {
		return fmt.Errorf("release %s has no signed %s (%w): %w", tag, ManifestName, err, errUnsigned)
	}
	return fmt.Errorf("download the %s manifest: %w", tag, err)
}

// VerifyManifest checks sig over the exact bytes of data against the
// trusted release keys, then parses data and checks it describes tag.
func VerifyManifest(data, sig []byte, tag string) (Manifest, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig)))
	if err != nil || len(raw) != ed25519.SignatureSize {
		return Manifest{}, fmt.Errorf("%s for %s is not a base64 ed25519 signature", SignatureName, tag)
	}
	trusted := false
	for _, key := range releaseKeys {
		if ed25519.Verify(key, data, raw) {
			trusted = true
			break
		}
	}
	if !trusted {
		return Manifest{}, fmt.Errorf("%s for %s is not signed by a trusted release key; do not install it", ManifestName, tag)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("parse %s for %s: %w", ManifestName, tag, err)
	}
	if m.Schema != ManifestSchema {
		return Manifest{}, fmt.Errorf("%s for %s has schema %d; this aether reads schema %d", ManifestName, tag, m.Schema, ManifestSchema)
	}
	if m.Version != tag {
		return Manifest{}, fmt.Errorf("%s fetched for %s describes %s; do not install it", ManifestName, tag, m.Version)
	}
	if len(m.Assets) == 0 {
		return Manifest{}, fmt.Errorf("%s for %s lists no assets", ManifestName, tag)
	}
	for name, a := range m.Assets {
		if !sha256Hex.MatchString(a.SHA256) || a.Size <= 0 {
			return Manifest{}, fmt.Errorf("%s for %s has an invalid entry for %s", ManifestName, tag, name)
		}
	}
	return m, nil
}

// SignManifest encodes m and signs the encoded bytes with key, returning
// the contents of ManifestName and SignatureName.
func SignManifest(m Manifest, key ed25519.PrivateKey) (data, sig []byte, err error) {
	data, err = json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	data = append(data, '\n')
	sig = []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(key, data)) + "\n")
	return data, sig, nil
}

// statusError is a response other than 200 OK.
type statusError struct {
	url    string
	status string
	code   int
}

func (e *statusError) Error() string { return "GET " + e.url + ": " + e.status }

// Transient reports whether err is a failure of the network or the release
// host - a transport error, a server error, or rate limiting - rather than
// of the release itself, so a later attempt may succeed.
func Transient(err error) bool {
	if errors.Is(err, errInsecureRedirect) {
		return false
	}
	var status *statusError
	if errors.As(err, &status) {
		return status.code >= http.StatusInternalServerError || status.code == http.StatusTooManyRequests
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}

// retryDelays are the waits between download attempts; their count plus
// one is the number of attempts.
var retryDelays = []time.Duration{time.Second, 4 * time.Second}

// retry runs try until it succeeds, reports an error not worth retrying,
// or runs out of attempts. The last error is returned as it was.
func retry(ctx context.Context, try func() (again bool, err error)) error {
	for attempt := 0; ; attempt++ {
		again, err := try()
		if err == nil || !again || attempt == len(retryDelays) {
			if err != nil && attempt > 0 {
				return fmt.Errorf("after %d attempts: %w", attempt+1, err)
			}
			return err
		}
		timer := time.NewTimer(retryDelays[attempt])
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("%w (previous attempt: %v)", ctx.Err(), err)
		case <-timer.C:
		}
	}
}

// get issues one GET and returns a 200 response. again reports whether the
// failure is worth another attempt: a network error or a server error, but
// not a refused redirect or a 404.
func get(ctx context.Context, url string) (resp *http.Response, again bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, false, err
	}
	resp, err = httpClient.Do(req)
	if err != nil {
		return nil, ctx.Err() == nil && !errors.Is(err, errInsecureRedirect), err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		code := resp.StatusCode
		return nil, code >= 500 || code == http.StatusTooManyRequests, &statusError{url: url, status: resp.Status, code: code}
	}
	return resp, false, nil
}

// fetchSmall downloads a manifest-sized file into memory.
func fetchSmall(ctx context.Context, url string) ([]byte, error) {
	var body []byte
	err := retry(ctx, func() (bool, error) {
		resp, again, err := get(ctx, url)
		if err != nil {
			return again, err
		}
		defer func() { _ = resp.Body.Close() }()
		body, err = io.ReadAll(io.LimitReader(resp.Body, maxManifestSize+1))
		if err != nil {
			return ctx.Err() == nil, fmt.Errorf("GET %s: %w", url, err)
		}
		if len(body) > maxManifestSize {
			return false, fmt.Errorf("GET %s: larger than %d bytes", url, maxManifestSize)
		}
		return false, nil
	})
	return body, err
}

// download streams url into f, starting it over on every attempt, and
// checks the bytes against want. Nothing beyond want.Size is read.
func download(ctx context.Context, url string, f *os.File, want Asset) error {
	return retry(ctx, func() (bool, error) {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return false, err
		}
		if err := f.Truncate(0); err != nil {
			return false, err
		}
		resp, again, err := get(ctx, url)
		if err != nil {
			return again, err
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.ContentLength >= 0 && resp.ContentLength != want.Size {
			return false, fmt.Errorf("size mismatch: %s is %d bytes, the manifest says %d", url, resp.ContentLength, want.Size)
		}
		h := sha256.New()
		n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, want.Size+1))
		if err != nil {
			return ctx.Err() == nil, fmt.Errorf("GET %s: %w", url, err)
		}
		if n != want.Size {
			return n < want.Size, fmt.Errorf("size mismatch: %s sent %d bytes, the manifest says %d", url, n, want.Size)
		}
		if got := hex.EncodeToString(h.Sum(nil)); got != want.SHA256 {
			return false, fmt.Errorf("checksum mismatch for %s: the manifest says %s, got %s", url, want.SHA256, got)
		}
		return false, nil
	})
}

// errInsecureRedirect refuses a redirect that leaves https.
var errInsecureRedirect = errors.New("refusing a redirect away from https")

// HTTPSOnly is the redirect policy for every release download, usable as
// an http.Client's CheckRedirect: GitHub redirects asset URLs to its CDN,
// and a hop from https to anything else would hand the bytes to whoever is
// on the path.
func HTTPSOnly(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	if prev := via[len(via)-1].URL; prev.Scheme == "https" && req.URL.Scheme != "https" {
		return fmt.Errorf("%w: %s redirected to %s", errInsecureRedirect, prev.Redacted(), req.URL.Redacted())
	}
	return nil
}
