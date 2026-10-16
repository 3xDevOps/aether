package serverupdate

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/3xDevOps/Aether/internal/selfupdate"
)

// ErrDowngrade is returned for a target older than the running release
// without allow_downgrade.
var ErrDowngrade = errors.New("refusing to downgrade")

// ErrPreflight is returned when the target release is not complete enough
// for a server to move to it: clients could not follow.
var ErrPreflight = errors.New("release preflight failed")

// clientAssets are what every client installs from. A server only moves
// to a release once all of them are published, because a server that is
// ahead of its clients makes them update before they can work.
var clientAssets = []string{
	"aether-linux-amd64", "aether-linux-arm64",
	"aether-darwin-amd64", "aether-darwin-arm64",
	"aether-windows-amd64.exe", "aether-windows-arm64.exe",
	"aether-android.apk",
}

// Releases is the signed release feed: the newest release's manifest, and
// one tag's. The default is the repository Config.Checker reads.
type Releases interface {
	Latest(ctx context.Context) (selfupdate.Manifest, error)
	Fetch(ctx context.Context, tag string) (selfupdate.Manifest, error)
}

// repository is the release feed of the repository the checker reads.
type repository string

func (r repository) Latest(ctx context.Context) (selfupdate.Manifest, error) {
	return selfupdate.LatestManifestFrom(ctx, string(r))
}

func (r repository) Fetch(ctx context.Context, tag string) (selfupdate.Manifest, error) {
	return selfupdate.FetchManifestFrom(ctx, string(r), tag)
}

func (s *Service) releases() Releases {
	if s.cfg.Releases != nil {
		return s.cfg.Releases
	}
	return repository(s.cfg.Checker.BaseURL())
}

// anonymous asks for release assets the way a client without credentials
// does.
var anonymous = &http.Client{Timeout: 30 * time.Second, CheckRedirect: selfupdate.HTTPSOnly}

// preflight decides whether this server may move to tag, before anything
// is downloaded: the release's signed manifest, the direction, the
// database schema, and every client asset as an anonymous client sees it.
func (s *Service) preflight(ctx context.Context, tag string, allowDowngrade bool) (selfupdate.Manifest, error) {
	m, err := s.releases().Fetch(ctx, tag)
	if err != nil {
		return m, fmt.Errorf("%w: %w", ErrPreflight, retryable{err})
	}
	if selfupdate.Behind(tag, s.core) && !allowDowngrade {
		return m, fmt.Errorf("%w: %s is older than the running %s; pass --allow-downgrade (allow_downgrade) to install it anyway",
			ErrDowngrade, tag, s.core)
	}
	schema, err := s.cfg.Store.SchemaVersion(ctx)
	if err != nil {
		return m, err
	}
	if m.StoreSchema < schema {
		return m, fmt.Errorf("%w: this database is at schema %d and %s only knows schema %d; it could not open it",
			ErrDowngrade, schema, tag, m.StoreSchema)
	}
	for _, name := range clientAssets {
		asset, ok := m.Assets[name]
		if !ok {
			return m, fmt.Errorf("%w: %s does not publish %s", ErrPreflight, tag, name)
		}
		url := m.AssetURL(name)
		if err := probeAsset(ctx, url, asset.Size); err != nil {
			return m, fmt.Errorf("%w: %s (%s): %w", ErrPreflight, name, url, err)
		}
	}
	return m, nil
}

// probeAsset checks that url answers an anonymous request with 200 and
// the manifest's size. HEAD first; a host that refuses HEAD gets a GET
// whose body is never read.
func probeAsset(ctx context.Context, url string, size int64) error {
	var last error
	for _, method := range []string{http.MethodHead, http.MethodGet} {
		req, err := http.NewRequestWithContext(ctx, method, url, nil)
		if err != nil {
			return err
		}
		resp, err := anonymous.Do(req)
		if err != nil {
			return err
		}
		_ = resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusMethodNotAllowed && method == http.MethodHead:
			last = fmt.Errorf("HEAD answered %s", resp.Status)
			continue
		case resp.StatusCode != http.StatusOK:
			return fmt.Errorf("%s answered %s", method, resp.Status)
		case resp.ContentLength != size:
			return fmt.Errorf("Content-Length is %d, the manifest says %d", resp.ContentLength, size)
		}
		return nil
	}
	return last
}
