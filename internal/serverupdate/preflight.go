package serverupdate

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/3xDevOps/Aether/internal/selfupdate"
	"github.com/3xDevOps/Aether/internal/store"
	"github.com/3xDevOps/Aether/internal/version"
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

// feed is releases, or the repository at baseURL when there is none.
func feed(releases Releases, baseURL string) Releases {
	if releases != nil {
		return releases
	}
	return repository(baseURL)
}

func (s *Service) releases() Releases {
	return feed(s.cfg.Releases, s.cfg.Checker.BaseURL())
}

// Plan is what moving an installation to a release needs: where it lives
// and where releases come from. The server fills it from its own state;
// `aether-server update` fills it from the config file.
type Plan struct {
	// Releases is the signed release feed. Nil reads the repository at
	// BaseURL.
	Releases Releases
	BaseURL  string
	// Self is the aether-server binary to replace, symlinks resolved.
	Self     string
	StateDir string
	// Database is the store's file, snapshotted before a release that
	// migrates it. Empty skips the snapshot.
	Database string
	// Schema reads the database's current schema version.
	Schema      func(context.Context) (int, error)
	Now         func() time.Time
	RequestedBy string
}

func (s *Service) plan(pending store.PendingServerUpdate) Plan {
	return Plan{
		Releases:    s.cfg.Releases,
		BaseURL:     s.cfg.Checker.BaseURL(),
		Self:        s.self,
		StateDir:    s.cfg.StateDir,
		Database:    s.cfg.Database,
		Schema:      s.cfg.Store.SchemaVersion,
		Now:         s.cfg.Now,
		RequestedBy: string(pending.RequestedBy),
	}
}

// anonymous asks for release assets the way a client without credentials
// does.
var anonymous = &http.Client{Timeout: 30 * time.Second, CheckRedirect: selfupdate.HTTPSOnly}

// preflight decides whether this server may move to tag, before anything
// is downloaded: the release's signed manifest, the direction, the
// database schema, and every client asset as an anonymous client sees it.
func preflight(ctx context.Context, p Plan, tag string, allowDowngrade bool) (selfupdate.Manifest, error) {
	if tag == version.Version {
		return selfupdate.Manifest{}, fmt.Errorf("%w: this server already runs %s", ErrPreflight, tag)
	}
	m, err := feed(p.Releases, p.BaseURL).Fetch(ctx, tag)
	if err != nil {
		return m, fmt.Errorf("%w: %w", ErrPreflight, retryable{err})
	}
	core := version.ReleaseCore(version.Version)
	if selfupdate.Behind(tag, core) && !allowDowngrade {
		return m, fmt.Errorf("%w: %s is older than the running %s; pass --allow-downgrade (allow_downgrade) to install it anyway",
			ErrDowngrade, tag, core)
	}
	schema, err := p.Schema(ctx)
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

// probeRetryDelay is the wait before probeAsset asks again after a
// failure of the network or the release host.
var probeRetryDelay = time.Second

// probeAsset checks that url answers an anonymous request with 200 and
// the manifest's size, asking twice when the first answer is a failure of
// the network or the host.
func probeAsset(ctx context.Context, url string, size int64) error {
	err := probeOnce(ctx, url, size)
	if !errors.As(err, new(retryable)) {
		return err
	}
	select {
	case <-ctx.Done():
		return err
	case <-time.After(probeRetryDelay):
	}
	return probeOnce(ctx, url, size)
}

// probeOnce asks HEAD first; a host that refuses HEAD gets a GET whose
// body is never read. Both ask for the uncompressed body, whose length is
// what the manifest records.
func probeOnce(ctx context.Context, url string, size int64) error {
	var last error
	for _, method := range []string{http.MethodHead, http.MethodGet} {
		req, err := http.NewRequestWithContext(ctx, method, url, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Accept-Encoding", "identity")
		resp, err := anonymous.Do(req)
		if err != nil {
			if selfupdate.Transient(err) {
				return retryable{err}
			}
			return err
		}
		_ = resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusMethodNotAllowed && method == http.MethodHead:
			last = fmt.Errorf("HEAD answered %s", resp.Status)
			continue
		case resp.StatusCode >= http.StatusInternalServerError || resp.StatusCode == http.StatusTooManyRequests:
			return retryable{fmt.Errorf("%s answered %s", method, resp.Status)}
		case resp.StatusCode != http.StatusOK:
			return fmt.Errorf("%s answered %s", method, resp.Status)
		case resp.ContentLength != size:
			return fmt.Errorf("Content-Length is %d, the manifest says %d", resp.ContentLength, size)
		}
		return nil
	}
	return last
}
