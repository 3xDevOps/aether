// Package edge is the edge's sign-in service: accounts, browser sessions,
// the device authorization flow, server ownership as servers report it,
// account deletion, the pages people use and the JSON API clients call.
// Its Handler serves both of the edge's origins: the sign-in origin, and
// the relay origin with the endpoints of internal/edge/relay, which calls
// the Service as its relay.Directory and implements ServerLink.
package edge

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	edgestore "github.com/3xDevOps/Aether/internal/edge/store"
)

// Config configures a Service. Client ids and secrets come from the
// caller, never from files in the repository.
type Config struct {
	// DataDir holds the database and the edge signing key.
	DataDir string
	// SigninOrigin serves the sign-in pages, their session cookie, the
	// OAuth callbacks and the client API. RelayOrigin serves servers'
	// control and data sockets, client connections and PathEdgeInfo.
	// Each is "https://host[:port]", and their host names differ.
	SigninOrigin string
	RelayOrigin  string
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
	// Register adds the relay's endpoints to the relay origin's mux.
	Register(mux *http.ServeMux)
	// Online reports whether the claimed server serverID holds a control
	// channel.
	Online(serverID string) bool
	// Unenroll tells serverID, if connected, that its owner removed it,
	// and closes its control channel.
	Unenroll(serverID string)
	// RevokeDevice closes the device's live connections and tells the
	// servers they reach.
	RevokeDevice(deviceID string)
	// AccountDeleted closes the deleted account's live connections and
	// sends each connected server among servers the deletions it is
	// owed.
	AccountDeleted(account edgeproto.Account, servers []string)
}

// Service is the edge's sign-in service.
type Service struct {
	signinOrigin string
	relayOrigin  string
	// signinHost and relayHost are the origins' host names, which pick
	// the origin a request is for.
	signinHost string
	relayHost  string
	now        func() time.Time
	store      *edgestore.Store
	key        ed25519.PrivateKey
	link       ServerLink
	providers  []*provider
	pages      *template.Template
	client     *http.Client

	signinLimit *limiter[netip.Prefix]
	startLimit  *limiter[netip.Prefix]
	pollLimit   *limiter[netip.Prefix]
	codeLimit   *limiter[netip.Prefix]
	// codeAccountLimit also counts user-code entries per account, so
	// guesses spread over many address blocks still meet one limit.
	codeAccountLimit *limiter[int64]
	// Claim connections are limited per address, per account and per
	// server: a signed-in account holding many addresses still meets
	// one limit, and a server's code meets one limit however many
	// accounts guess at it.
	claimLimit        *limiter[netip.Prefix]
	claimAccountLimit *limiter[edgeproto.Principal]
	claimServerLimit  *limiter[string]
}

// New opens the edge's store and signing key in cfg.DataDir, creating both
// on first start.
func New(cfg Config) (*Service, error) {
	signin, err := edgeproto.Origin(cfg.SigninOrigin)
	if err != nil {
		return nil, fmt.Errorf("edge: sign-in origin: %w", err)
	}
	relayOrigin, err := edgeproto.Origin(cfg.RelayOrigin)
	if err != nil {
		return nil, fmt.Errorf("edge: relay origin: %w", err)
	}
	signinHost, relayHost := hostname(signin), hostname(relayOrigin)
	if signinHost == relayHost {
		return nil, fmt.Errorf("edge: the sign-in origin %s and the relay origin %s must have different host names", signin, relayOrigin)
	}
	if cfg.DataDir == "" {
		return nil, errors.New("edge: data directory is required")
	}
	now := cfg.Clock
	if now == nil {
		now = time.Now
	}
	s := &Service{
		signinOrigin: signin,
		relayOrigin:  relayOrigin,
		signinHost:   signinHost,
		relayHost:    relayHost,
		now:          now,
		client:       &http.Client{Timeout: providerTimeout},
		signinLimit:  newAddrLimiter(20, 6*time.Second, now),
		startLimit:   newAddrLimiter(10, 30*time.Second, now),
		// Four polls a second: twenty sign-ins at once behind one address.
		pollLimit:         newAddrLimiter(30, 250*time.Millisecond, now),
		codeLimit:         newAddrLimiter(10, 6*time.Second, now),
		codeAccountLimit:  newLimiter[int64](10, time.Minute, now),
		claimLimit:        newAddrLimiter(5, time.Minute, now),
		claimAccountLimit: newLimiter[edgeproto.Principal](5, time.Minute, now),
		claimServerLimit:  newLimiter[string](10, 30*time.Second, now),
	}
	if cfg.GitHub != nil {
		s.providers = append(s.providers, githubProvider(*cfg.GitHub, signin))
	}
	if cfg.Google != nil {
		s.providers = append(s.providers, googleProvider(*cfg.Google, signin))
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

// hostname is origin's host name, lowercase, without a port.
func hostname(origin string) string {
	u, _ := url.Parse(origin) // origin is canonical
	return u.Hostname()
}

// requestHost is the host name a request names, lowercase, without a
// port.
func requestHost(r *http.Request) string {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.ToLower(strings.TrimSuffix(strings.Trim(host, "[]"), "."))
}

// Handler serves both origins, picked by the request's host name: on the
// sign-in host the pages and the client API, on the relay host the
// relay's endpoints and PathEdgeInfo. A path of one origin requested on
// the other is refused with 421 Misdirected Request naming the right
// origin, so no page, cookie or API answer is served on the relay host.
// GET /healthz answers on any host. Call SetLink first.
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
	pages.HandleFunc("GET /devices", s.signedIn(s.devicesPage))
	pages.HandleFunc("POST /devices/revoke", s.signedIn(s.deviceRevoke))
	pages.HandleFunc("GET /account", s.signedIn(s.accountPage))
	pages.HandleFunc("POST /account/delete", s.signedIn(s.accountDelete))

	api := http.NewServeMux()
	api.HandleFunc("POST "+edgeproto.PathDeviceStart, s.api(s.apiDeviceStart))
	api.HandleFunc("POST "+edgeproto.PathDeviceToken, s.api(s.apiDeviceToken))
	api.HandleFunc("POST "+edgeproto.PathLogout, s.api(s.apiLogout))
	api.HandleFunc("GET "+edgeproto.PathServers, s.api(s.apiServers))
	api.HandleFunc("GET "+edgeproto.PathAccount, s.api(s.apiAccount))

	signin := http.NewServeMux()
	signin.Handle("/v1/", api)
	signin.Handle("/", s.pageHeaders(http.NewCrossOriginProtection().Handler(pages)))

	relay := http.NewServeMux()
	s.link.Register(relay)
	relay.HandleFunc("GET "+edgeproto.PathEdgeInfo, s.api(s.apiEdgeInfo))

	routes := func(mux *http.ServeMux, r *http.Request) bool {
		_, pattern := mux.Handler(r)
		return pattern != ""
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/healthz" {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte("ok\n"))
			return
		}
		switch host := requestHost(r); host {
		case s.signinHost:
			if routes(relay, r) {
				misdirected(w, r, s.relayOrigin)
				return
			}
			signin.ServeHTTP(w, r)
		case s.relayHost:
			switch {
			case routes(relay, r):
				relay.ServeHTTP(w, r)
			case routes(api, r) || routes(pages, r):
				misdirected(w, r, s.signinOrigin)
			default:
				http.NotFound(w, r)
			}
		default:
			http.Error(w, fmt.Sprintf("this edge serves %s and %s, not the host %q", s.signinOrigin, s.relayOrigin, host),
				http.StatusMisdirectedRequest)
		}
	})
}

// misdirected refuses a request made on the wrong one of the edge's
// origins.
func misdirected(w http.ResponseWriter, r *http.Request, origin string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusMisdirectedRequest, edgeproto.ErrorBody{
		Error: fmt.Sprintf("%s is served on %s, not on this host", r.URL.Path, origin)})
}
