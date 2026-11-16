package selfupdate

import (
	"bytes"
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
	"reflect"
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

// ErrUnsigned ends the error of a manifest fetch that found no manifest
// for the tag, as opposed to any other failure: the release predates
// signed releases, or is still being published.
var ErrUnsigned = errors.New("it predates signed releases or is still being published, and no updater installs it")

// Manifest is a release's verified aether-release.json.
type Manifest struct {
	Schema int `json:"schema"`
	// Channel is the channel the release was published on. It, Build and
	// Hotfix are absent from a release that predates channels.
	Channel string `json:"channel,omitempty"`
	// Build orders releases: it only increases along main, and a release
	// promoted from a nightly carries that nightly's build. Hotfix counts
	// the hotfixes on top of the stable release with the same build.
	Build       int       `json:"build,omitempty"`
	Hotfix      int       `json:"hotfix,omitempty"`
	Version     string    `json:"version"`
	Commit      string    `json:"commit"`
	PublishedAt time.Time `json:"published_at"`
	NotesURL    string    `json:"notes_url"`
	// StoreSchema is the highest store migration the release's server
	// applies.
	StoreSchema int              `json:"store_schema"`
	Assets      map[string]Asset `json:"assets"`
	// Images maps each container image the release publishes, by name
	// (aether-edge), to its index digest.
	Images map[string]string `json:"images,omitempty"`
	Edge   *Edge             `json:"edge,omitempty"`

	baseURL string
}

// Asset is one published file's digest and length.
type Asset struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Edge is what a host updating a container edge needs to know about the
// release, beyond the image itself.
type Edge struct {
	Image       string `json:"image"`
	ImageDigest string `json:"image_digest"`
	// StoreSchema is the highest edge store migration the release applies;
	// StoreCompatFloor is the oldest schema a binary must support to open a
	// database this release has migrated.
	StoreSchema      int          `json:"store_schema"`
	StoreCompatFloor int          `json:"store_compat_floor"`
	Protocol         EdgeProtocol `json:"protocol"`
	// MinUpgradeFrom is the oldest release that may upgrade straight to
	// this one.
	MinUpgradeFrom string       `json:"min_upgrade_from"`
	ManualSteps    []ManualStep `json:"manual_steps"`
}

// EdgeProtocol is the range of edge protocol versions the release speaks.
type EdgeProtocol struct {
	Version    int `json:"version"`
	MinVersion int `json:"min_version"`
}

// ManualStep is an operator action an upgrade across Since needs. A
// blocking step holds an automatic update until the operator has done it.
type ManualStep struct {
	ID       string `json:"id"`
	Since    string `json:"since"`
	Summary  string `json:"summary"`
	DocURL   string `json:"doc_url"`
	Blocking bool   `json:"blocking"`
}

// ImageRelease is the image half of a manifest: what release-manifest
// reads from the file its -images flag names.
type ImageRelease struct {
	Images map[string]string `json:"images"`
	Edge   Edge              `json:"edge"`
}

const EdgeImage = "ghcr.io/3xdevops/aether-edge"

// ImageNames are the images every release publishes.
var ImageNames = []string{"aether-browser", "aether-edge", "aether-standard"}

// Validate checks r names exactly ImageNames, each by digest, and that its
// edge section describes tag and the aether-edge image.
func (r ImageRelease) Validate(tag string) error {
	if len(r.Images) != len(ImageNames) {
		return fmt.Errorf("images: want exactly %s, got %d", strings.Join(ImageNames, ", "), len(r.Images))
	}
	for _, name := range ImageNames {
		if !ValidImageDigest(r.Images[name]) {
			return fmt.Errorf("images: %s has digest %q, not sha256:<64 hex>", name, r.Images[name])
		}
	}
	if r.Edge.Image != EdgeImage {
		return fmt.Errorf("edge: image %s is not %s", r.Edge.Image, EdgeImage)
	}
	if r.Edge.ImageDigest != r.Images["aether-edge"] {
		return fmt.Errorf("edge: image digest %s is not the aether-edge image %s", r.Edge.ImageDigest, r.Images["aether-edge"])
	}
	return r.Edge.Validate(tag)
}

var imageDigest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// ValidImageDigest reports whether d is an OCI sha256 digest.
func ValidImageDigest(d string) bool { return imageDigest.MatchString(d) }

// Validate checks e describes release tag completely.
func (e Edge) Validate(tag string) error {
	switch {
	case e.Image == "":
		return errors.New("edge: no image")
	case !ValidImageDigest(e.ImageDigest):
		return fmt.Errorf("edge: image digest %q is not sha256:<64 hex>", e.ImageDigest)
	case e.StoreSchema < 1 || e.StoreCompatFloor < 1 || e.StoreCompatFloor > e.StoreSchema:
		return fmt.Errorf("edge: store schema %d with compat floor %d", e.StoreSchema, e.StoreCompatFloor)
	case e.Protocol.MinVersion < 1 || e.Protocol.Version < e.Protocol.MinVersion:
		return fmt.Errorf("edge: protocol versions %d to %d", e.Protocol.MinVersion, e.Protocol.Version)
	case !ValidTag(e.MinUpgradeFrom) || Behind(tag, e.MinUpgradeFrom):
		return fmt.Errorf("edge: min_upgrade_from %q is not a release tag at or below %s", e.MinUpgradeFrom, tag)
	}
	ids := map[string]bool{}
	for _, s := range e.ManualSteps {
		switch {
		case s.ID == "" || ids[s.ID]:
			return fmt.Errorf("edge: manual step id %q is empty or repeated", s.ID)
		case !ValidTag(s.Since) || Behind(tag, s.Since):
			return fmt.Errorf("edge: manual step %s: since %q is not a release tag at or below %s", s.ID, s.Since, tag)
		case s.Summary == "" || !strings.HasPrefix(s.DocURL, "https://"):
			return fmt.Errorf("edge: manual step %s needs a summary and an https doc_url", s.ID)
		}
		ids[s.ID] = true
	}
	return nil
}

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

// Download streams the asset name into f, checks it against the size and
// sha256 m lists for it, and returns that entry. A download that runs long
// reports its size to progress; nil keeps it silent.
func (m Manifest) Download(ctx context.Context, name string, f *os.File, progress io.Writer) (Asset, error) {
	want, ok := m.Assets[name]
	if !ok {
		return Asset{}, fmt.Errorf("release %s does not publish %s", m.Version, name)
	}
	if err := download(ctx, m.AssetURL(name), f, want, progress); err != nil {
		return Asset{}, fmt.Errorf("download %s: %w", name, err)
	}
	return want, nil
}

// FetchManifest downloads tag's manifest and signature and verifies them.
func FetchManifest(ctx context.Context, tag string) (Manifest, error) {
	return FetchManifestFrom(ctx, defaultBaseURL, tag)
}

// FetchManifestFrom is FetchManifest against a repository base URL such
// as Checker.BaseURL.
func FetchManifestFrom(ctx context.Context, baseURL, tag string) (Manifest, error) {
	return fetchManifest(ctx, baseURL, tag)
}

// LatestManifestFrom resolves the newest release at a repository base URL
// such as Checker.BaseURL and fetches its manifest. A newest release
// without one yet is ErrNoRelease.
func LatestManifestFrom(ctx context.Context, baseURL string) (Manifest, error) {
	tag, err := LatestTag(ctx, baseURL+"/releases/latest")
	if err != nil {
		return Manifest{}, err
	}
	m, err := fetchManifest(ctx, baseURL, tag)
	if errors.Is(err, ErrUnsigned) {
		return Manifest{}, fmt.Errorf("%w: %w", ErrNoRelease, err)
	}
	return m, err
}

func fetchManifest(ctx context.Context, baseURL, tag string) (Manifest, error) {
	m, _, _, err := FetchSignedManifestFrom(ctx, baseURL, tag)
	return m, err
}

// FetchSignedManifestFrom is FetchManifestFrom that also returns the exact
// manifest and signature bytes it verified, for a caller that keeps them.
func FetchSignedManifestFrom(ctx context.Context, baseURL, tag string) (m Manifest, data, sig []byte, err error) {
	if !ValidTag(tag) {
		return Manifest{}, nil, nil, fmt.Errorf("%q is not a release tag", tag)
	}
	url := baseURL + "/releases/download/" + tag + "/" + ManifestName
	if data, err = fetchSmall(ctx, url); err != nil {
		return Manifest{}, nil, nil, unsigned(tag, err)
	}
	if sig, err = fetchSmall(ctx, url+".sig"); err != nil {
		return Manifest{}, nil, nil, unsigned(tag, err)
	}
	if m, err = VerifyManifest(data, sig, tag); err != nil {
		return Manifest{}, nil, nil, err
	}
	m.baseURL = baseURL
	return m, data, sig, nil
}

// unsigned names a 404 for the manifest or its signature as a release that
// predates signing; any other failure stays as it was.
func unsigned(tag string, err error) error {
	var status *statusError
	if errors.As(err, &status) && status.code == http.StatusNotFound {
		return fmt.Errorf("release %s has no signed %s (%w): %w", tag, ManifestName, err, ErrUnsigned)
	}
	return fmt.Errorf("download the %s manifest: %w", tag, err)
}

// VerifyManifest checks sig over the exact bytes of data against the
// trusted release keys, then parses data and checks it describes tag. A
// nightly key verifies only a manifest whose signed channel is nightly.
func VerifyManifest(data, sig []byte, tag string) (Manifest, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig)))
	if err != nil || len(raw) != ed25519.SignatureSize {
		return Manifest{}, fmt.Errorf("%s for %s is not a base64 ed25519 signature", SignatureName, tag)
	}
	release, nightly := signedBy(releaseKeys, data, raw), signedBy(nightlyKeys, data, raw)
	if !release && !nightly {
		return Manifest{}, fmt.Errorf("%s for %s is not signed by a trusted release key; do not install it", ManifestName, tag)
	}
	if err := canonicalKeys(data, manifestKeys); err != nil {
		return Manifest{}, fmt.Errorf("parse %s for %s: %w; do not install it", ManifestName, tag, err)
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
	if !release && m.Channel != ChannelNightly {
		return Manifest{}, fmt.Errorf("%s for %s is signed by a nightly key but its channel is %q, not nightly; do not install it", ManifestName, tag, m.Channel)
	}
	if err := m.validChannel(); err != nil {
		return Manifest{}, fmt.Errorf("%s for %s: %w; do not install it", ManifestName, tag, err)
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

// manifestKeys are the top-level keys a manifest may carry, exactly as
// spelled.
var manifestKeys = jsonKeys(reflect.TypeFor[Manifest]())

// jsonKeys are the JSON names of t's fields.
func jsonKeys(t reflect.Type) []string {
	var keys []string
	for i := range t.NumField() {
		if name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ","); name != "" && name != "-" {
			keys = append(keys, name)
		}
	}
	return keys
}

// canonicalKeys refuses a top-level key that repeats, or that spells one
// of keys in another case. encoding/json takes the last of
// repeated keys and matches keys case-insensitively, where jq and other
// tooling would read another value from the same signed bytes. Unknown
// keys pass, so a later release may add fields.
func canonicalKeys(data []byte, keys []string) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return errors.New("not a JSON object")
	}
	seen := map[string]bool{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		key := tok.(string)
		if seen[key] {
			return fmt.Errorf("key %q repeats", key)
		}
		seen[key] = true
		for _, want := range keys {
			if key != want && strings.EqualFold(key, want) {
				return fmt.Errorf("key %q is not spelled %q", key, want)
			}
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return err
		}
	}
	return nil
}

func signedBy(keys []ed25519.PublicKey, data, sig []byte) bool {
	for _, key := range keys {
		if ed25519.Verify(key, data, sig) {
			return true
		}
	}
	return false
}

// validChannel checks the channel fields agree with each other and with
// the tag's form: a release carries a channel and a positive build, or,
// from before channels, neither.
func (m Manifest) validChannel() error {
	switch {
	case m.Channel == "" && m.Build == 0 && m.Hotfix == 0:
		return nil
	case m.Channel == "":
		return fmt.Errorf("build %d and hotfix %d without a channel", m.Build, m.Hotfix)
	case m.Build < 1 || m.Hotfix < 0:
		return fmt.Errorf("channel %s with build %d and hotfix %d; the build must be positive and the hotfix not negative", m.Channel, m.Build, m.Hotfix)
	case m.Channel != TagChannel(m.Version):
		return fmt.Errorf("channel %q does not match %s, %s", m.Channel, m.Version, tagForm(m.Version))
	}
	return nil
}

func tagForm(tag string) string {
	if c := TagChannel(tag); c != "" {
		return "a " + c + " tag"
	}
	return "a tag of no channel"
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

// SignPointer encodes p and signs the encoded bytes with key, returning the
// contents of PointerName(p.Channel) and its signature.
func SignPointer(p Pointer, key ed25519.PrivateKey) (data, sig []byte, err error) {
	data, err = json.MarshalIndent(p, "", "  ")
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

// retryDelays are the waits between attempts at a download or a rollback;
// their count plus one is the number of attempts.
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
// checks the bytes against want. Nothing beyond want.Size is read. Each
// attempt counts its progress from zero.
func download(ctx context.Context, url string, f *os.File, want Asset, progress io.Writer) error {
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
		n, err := io.Copy(io.MultiWriter(f, h, NewProgress(progress, want.Size)), io.LimitReader(resp.Body, want.Size+1))
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

// DownloadPinned streams url into f the way a release asset is fetched:
// https redirects only, transient failures retried, and the bytes checked
// against want before it returns.
func DownloadPinned(ctx context.Context, url string, f *os.File, want Asset, progress io.Writer) error {
	return download(ctx, url, f, want, progress)
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
