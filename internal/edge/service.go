// Package edge is the edge's sign-in service: accounts, browser sessions,
// the device authorization flow, server claims, the pages people use and
// the JSON API clients call. The relay in internal/edge/relay calls the
// exported Service methods and implements ServerLink.
package edge

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"time"

	"github.com/3xDevOps/Aether/internal/edge/edgestore"
	"github.com/3xDevOps/Aether/internal/edgeproto"
)

// Config configures a Service. Client ids and secrets come from the
// caller, never from files in the repository.
type Config struct {
	// DataDir holds the database and the edge signing key.
	DataDir string
	// Origin is the edge's public URL, "https://host[:port]".
	Origin string
	// ServerDomain is the domain server hostnames live under:
	// "<server id>.<ServerDomain>".
	ServerDomain string
	// GitHub and Google are the OAuth applications; a nil one is not
	// offered. At least one is required.
	GitHub *OAuthApp
	Google *OAuthApp
	// Clock is time.Now when nil.
	Clock func() time.Time
}

// OAuthApp is one provider's OAuth application. AuthURL, TokenURL and
// APIURL override the provider's endpoints when set; tests point them at a
// fake provider. APIURL is the base of GitHub's REST API or of Google's
// OpenID userinfo endpoint.
type OAuthApp struct {
	ClientID     string
	ClientSecret string
	AuthURL      string
	TokenURL     string
	APIURL       string
}

// ServerLink is what the Service needs from the relay's live control
// channels. *relay.Relay implements it.
type ServerLink interface {
	// Online reports whether the claimed server serverID holds a control
	// channel.
	Online(serverID string) bool
	// Claim forwards a claim code, as a person typed it, for account on
	// device to the one connected server whose id the code names, and
	// waits for its answer. When the server accepts, it calls record,
	// and treats the server as claimed only once record succeeds. It
	// returns that server's id and name once account owns it, an
	// edgeproto.Refusal when the edge or the server refused, and another
	// error, which says what to do next, when the server did not answer
	// or record failed.
	Claim(ctx context.Context, code string, account edgeproto.Account, device edgeproto.Device,
		record func(ctx context.Context, serverID, name string) error) (serverID, name string, err error)
	// Unenroll tells serverID, if connected, that its owner removed it,
	// and closes its control channel.
	Unenroll(serverID string)
	// RevokeDevice closes the device's live connections and tells the
	// servers they reach.
	RevokeDevice(deviceID string)
}

// Service is the edge's sign-in service.
type Service struct {
	origin       string
	serverDomain string
	now          func() time.Time
	store        *edgestore.Store
	key          ed25519.PrivateKey
	link         ServerLink
	providers    []*provider
	pages        *template.Template
	client       *http.Client

	signinLimit *limiter[netip.Prefix]
	startLimit  *limiter[netip.Prefix]
	pollLimit   *limiter[netip.Prefix]
	codeLimit   *limiter[netip.Prefix]
	// codeAccountLimit also counts user-code entries per account, so
	// guesses spread over many address blocks still meet one limit.
	codeAccountLimit *limiter[int64]
	claimLimit       *limiter[netip.Prefix]
}

// New opens the edge's store and signing key in cfg.DataDir, creating both
// on first start.
func New(cfg Config) (*Service, error) {
	origin, err := edgeproto.Origin(cfg.Origin)
	if err != nil {
		return nil, fmt.Errorf("edge: %w", err)
	}
	if !edgeproto.ValidServerDomain(cfg.ServerDomain) {
		return nil, fmt.Errorf("edge: server domain %q is not a lowercase DNS name such as servers.example.com", cfg.ServerDomain)
	}
	if cfg.DataDir == "" {
		return nil, errors.New("edge: data directory is required")
	}
	now := cfg.Clock
	if now == nil {
		now = time.Now
	}
	s := &Service{
		origin:       origin,
		serverDomain: cfg.ServerDomain,
		now:          now,
		client:       &http.Client{Timeout: providerTimeout},
		signinLimit:  newAddrLimiter(20, 6*time.Second, now),
		startLimit:   newAddrLimiter(10, 30*time.Second, now),
		// Four polls a second: twenty sign-ins at once behind one address.
		pollLimit:        newAddrLimiter(30, 250*time.Millisecond, now),
		codeLimit:        newAddrLimiter(10, 6*time.Second, now),
		codeAccountLimit: newLimiter[int64](10, time.Minute, now),
		claimLimit:       newAddrLimiter(5, time.Minute, now),
	}
	if cfg.GitHub != nil {
		s.providers = append(s.providers, githubProvider(*cfg.GitHub, origin))
	}
	if cfg.Google != nil {
		s.providers = append(s.providers, googleProvider(*cfg.Google, origin))
	}
	if len(s.providers) == 0 {
		return nil, errors.New("edge: no sign-in provider is configured; configure a GitHub or Google OAuth application")
	}
	if s.pages, err = parsePages(); err != nil {
		return nil, err
	}
	if err = os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("edge: create data directory: %w", err)
	}
	if s.key, err = loadOrCreateKey(filepath.Join(cfg.DataDir, keyFile)); err != nil {
		return nil, err
	}
	if s.store, err = edgestore.Open(filepath.Join(cfg.DataDir, "edge.db")); err != nil {
		return nil, err
	}
	return s, nil
}

// SetLink connects the Service to the relay. Call it before Handler serves
// a request.
func (s *Service) SetLink(l ServerLink) { s.link = l }

// Close closes the store.
func (s *Service) Close() error { return s.store.Close() }

// Handler serves the pages and the client API. The relay serves
// PathServerControl, PathServerData and PathConnect itself.
func (s *Service) Handler() http.Handler {
	pages := http.NewServeMux()
	pages.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/servers", http.StatusSeeOther)
	})
	pages.HandleFunc("GET /edge.css", serveCSS)
	pages.HandleFunc("GET /signin", s.signinPage)
	pages.HandleFunc("GET /signin/{provider}", s.signinStart)
	pages.HandleFunc("GET /signin/{provider}/callback", s.signinCallback)
	pages.HandleFunc("POST /signout", s.signedIn(s.signout))
	pages.HandleFunc("GET /device", s.signedIn(s.devicePage))
	pages.HandleFunc("POST /device", s.signedIn(s.deviceLookup))
	pages.HandleFunc("POST /device/confirm", s.signedIn(s.deviceDecide))
	pages.HandleFunc("GET /servers", s.signedIn(s.serversPage))
	pages.HandleFunc("POST /servers/remove", s.signedIn(s.serverRemove))
	pages.HandleFunc("GET "+edgeproto.PathAddServer, s.signedIn(s.addServerPage))
	pages.HandleFunc("POST "+edgeproto.PathAddServer, s.signedIn(s.addServer))
	pages.HandleFunc("GET /devices", s.signedIn(s.devicesPage))
	pages.HandleFunc("POST /devices/revoke", s.signedIn(s.deviceRevoke))
	pages.HandleFunc("GET "+edgeproto.PathAuthorize, s.authorizePage)
	pages.HandleFunc("POST "+edgeproto.PathAuthorize, s.signedIn(s.authorize))

	mux := http.NewServeMux()
	mux.Handle("/", s.pageHeaders(http.NewCrossOriginProtection().Handler(pages)))
	mux.HandleFunc("POST "+edgeproto.PathDeviceStart, s.api(s.apiDeviceStart))
	mux.HandleFunc("POST "+edgeproto.PathDeviceToken, s.api(s.apiDeviceToken))
	mux.HandleFunc("POST "+edgeproto.PathLogout, s.api(s.apiLogout))
	mux.HandleFunc("GET "+edgeproto.PathServers, s.api(s.apiServers))
	mux.HandleFunc("POST "+edgeproto.PathClaim, s.api(s.apiClaim))
	mux.HandleFunc("GET "+edgeproto.PathEdgeKey, s.api(s.apiEdgeKey))
	return mux
}
