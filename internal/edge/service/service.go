// Package edge is the edge's sign-in service: accounts, browser sessions,
// the device authorization flow, server ownership as servers report it,
// account deletion, the pages people use and the JSON API clients call.
// Its Handler serves both of the edge's origins: the sign-in origin, and
// the relay origin with the endpoints of internal/edge/relay, which calls
// the Service as its relay.Directory and implements ServerLink.
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
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/oauth2"

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
	// GitHub is the OAuth app people sign in with. It is required.
	GitHub *OAuthApp
	// MorePairs are further origin pairs the edge serves beside
	// SigninOrigin and RelayOrigin, each with its own OAuth app. A
	// request is served by the pair its host name belongs to, and no
	// pair is preferred.
	MorePairs []Pair
	// Clock is time.Now when nil.
	Clock func() time.Time
	// OpenAIChallenge is the token the relay origin serves at
	// edgeproto.PathOpenAIChallenge; empty serves 404.
	OpenAIChallenge string
	// ConnectorClientHosts replaces chatgpt.com and claude.ai as the
	// host names a server may send a browser to from the universal
	// connector paths; for tests that serve their own client.
	ConnectorClientHosts []string
	// Reviewer, when set, is the reviewer account an app directory's
	// reviewer signs in with at PathReviewerSignin. Nil serves 404 there.
	Reviewer *ReviewerAccount
}

// OAuthApp is a GitHub OAuth app. AuthURL, TokenURL and APIURL, the base
// of GitHub's REST API, override GitHub's endpoints when set; tests point
// them at a fake GitHub.
type OAuthApp struct {
	ClientID     string
	ClientSecret string
	AuthURL      string
	TokenURL     string
	APIURL       string
}

// Pair is a sign-in origin and a relay origin the edge serves together,
// with the OAuth app whose callback is on the sign-in origin.
type Pair struct {
	SigninOrigin string
	RelayOrigin  string
	GitHub       *OAuthApp
}

// pair is a Pair as the Service serves it.
type pair struct {
	signin, relay         string
	signinHost, relayHost string
	oauth                 oauth2.Config
	githubAPI             string
	// deviceLogins counts device tokens issued on the sign-in origin.
	deviceLogins atomic.Uint64
}

type pairKey struct{}

// pair is the pair Handler picked for r, or the first for a request
// handed to a handler directly.
func (s *Service) pair(r *http.Request) *pair {
	if p, ok := r.Context().Value(pairKey{}).(*pair); ok {
		return p
	}
	return s.pairs[0]
}

// newPair checks p; noApp is the error for a pair without an OAuth app.
func newPair(p Pair, noApp string) (*pair, error) {
	signin, err := edgeproto.Origin(p.SigninOrigin)
	if err != nil {
		return nil, fmt.Errorf("edge: sign-in origin: %w", err)
	}
	relay, err := edgeproto.Origin(p.RelayOrigin)
	if err != nil {
		return nil, fmt.Errorf("edge: relay origin: %w", err)
	}
	signinHost, relayHost := edgeproto.OriginHost(signin), edgeproto.OriginHost(relay)
	if signinHost == relayHost {
		return nil, fmt.Errorf("edge: the sign-in origin %s and the relay origin %s must have different host names", signin, relay)
	}
	if p.GitHub == nil {
		return nil, errors.New(noApp)
	}
	return &pair{
		signin: signin, relay: relay, signinHost: signinHost, relayHost: relayHost,
		oauth:     githubConfig(*p.GitHub, signin),
		githubAPI: strings.TrimSuffix(withDefault(p.GitHub.APIURL, "https://api.github.com"), "/"),
	}, nil
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
	// Version is the protocol version negotiated with the claimed server
	// serverID, 0 when it holds no control channel.
	Version(serverID string) int
	// Origin is the relay origin the claimed server serverID enrolled
	// at, "" when it holds no control channel.
	Origin(serverID string) string
	// ReadMCPBody reads a connector request's body, or answers the
	// request and returns false.
	ReadMCPBody(w http.ResponseWriter, r *http.Request) ([]byte, bool)
	// ForwardUniversal forwards a universal connector request, whose body
	// ReadMCPBody read, to serverID, with browser, when set, as
	// edgeproto.HeaderBrowser.
	ForwardUniversal(w http.ResponseWriter, r *http.Request, serverID string, body []byte, browser string)
}

// Service is the edge's sign-in service.
type Service struct {
	// pairs are the origin pairs served, the one Config names first.
	// byHost holds each by the host names of both its origins, which
	// pick the pair and the origin a request is for.
	pairs  []*pair
	byHost map[string]*pair
	now    func() time.Time
	store  *edgestore.Store
	key    ed25519.PrivateKey
	link   ServerLink
	pages  *template.Template
	client *http.Client
	// githubErrors counts calls to GitHub that failed.
	githubErrors    atomic.Uint64
	openAIChallenge string
	clientHosts     []string

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
	// chooseLimit limits the universal connector's choose page and its
	// hand-off to a server per address; tokenLimit its token requests
	// per address and server.
	chooseLimit *limiter[netip.Prefix]
	tokenLimit  *limiter[serverBlock]

	reviewer     *ReviewerAccount
	reviewerLock reviewerLock
	// reviewerChecks holds a token for each reviewer password check under
	// way, reviewerMaxChecks at most.
	reviewerChecks chan struct{}
}

// New opens the edge's store and signing key in cfg.DataDir, creating both
// on first start.
func New(cfg Config) (*Service, error) {
	var pairs []*pair
	byHost := map[string]*pair{}
	for i, cp := range append([]Pair{{cfg.SigninOrigin, cfg.RelayOrigin, cfg.GitHub}}, cfg.MorePairs...) {
		noApp := "edge: no GitHub OAuth app is configured; set --github-client-id and its client secret"
		if i > 0 {
			noApp = "edge: no GitHub OAuth app is configured for " + cp.SigninOrigin
		}
		p, err := newPair(cp, noApp)
		if err != nil {
			return nil, err
		}
		for _, host := range []string{p.signinHost, p.relayHost} {
			if other := byHost[host]; other != nil {
				return nil, fmt.Errorf("edge: the origin pairs %s %s and %s %s share the host name %s", other.signin, other.relay, p.signin, p.relay, host)
			}
			byHost[host] = p
		}
		pairs = append(pairs, p)
	}
	if cfg.DataDir == "" {
		return nil, errors.New("edge: data directory is required")
	}
	now := cfg.Clock
	if now == nil {
		now = time.Now
	}
	s := &Service{
		pairs:       pairs,
		byHost:      byHost,
		now:         now,
		client:      &http.Client{Timeout: providerTimeout},
		signinLimit: newAddrLimiter(20, 6*time.Second, now),
		startLimit:  newAddrLimiter(10, 30*time.Second, now),
		// Four polls a second: twenty sign-ins at once behind one address.
		pollLimit:         newAddrLimiter(30, 250*time.Millisecond, now),
		codeLimit:         newAddrLimiter(10, 6*time.Second, now),
		codeAccountLimit:  newLimiter[int64](10, time.Minute, now),
		claimLimit:        newAddrLimiter(5, time.Minute, now),
		claimAccountLimit: newLimiter[edgeproto.Principal](5, time.Minute, now),
		claimServerLimit:  newLimiter[string](10, 30*time.Second, now),
		chooseLimit:       newAddrLimiter(20, 3*time.Second, now),
		tokenLimit:        newLimiter[serverBlock](60, time.Second, now),
		openAIChallenge:   cfg.OpenAIChallenge,
		clientHosts:       cfg.ConnectorClientHosts,
		reviewer:          cfg.Reviewer,
		reviewerChecks:    make(chan struct{}, reviewerMaxChecks),
	}
	s.tokenLimit.scale = func(k serverBlock) int { return edgeproto.RateLimitScale(k.block) }
	if s.clientHosts == nil {
		s.clientHosts = []string{"chatgpt.com", "claude.ai"}
	}
	var err error
	if s.pages, err = parsePages(); err != nil {
		return nil, err
	}
	if err = os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("edge: create data directory: %w", err)
	}
	if s.store, err = edgestore.Open(filepath.Join(cfg.DataDir, "edge.db")); err != nil {
		return nil, err
	}
	if s.key, err = loadKey(cfg.DataDir, s.store); err != nil {
		s.store.Close() //nolint:errcheck // the key error takes precedence
		return nil, err
	}
	return s, nil
}

// SetLink connects the Service to the relay. Call it before Handler serves
// a request.
func (s *Service) SetLink(l ServerLink) { s.link = l }

// Close closes the store.
func (s *Service) Close() error { return s.store.Close() }

// Ping checks that the edge database answers a read.
func (s *Service) Ping(ctx context.Context) error { return s.store.Ping(ctx) }

// GitHubErrors counts calls to GitHub that failed since the edge started.
func (s *Service) GitHubErrors() uint64 { return s.githubErrors.Load() }

// DeviceLogins counts, by sign-in origin, device tokens issued since the
// edge started.
func (s *Service) DeviceLogins() map[string]uint64 {
	out := make(map[string]uint64, len(s.pairs))
	for _, p := range s.pairs {
		out[p.signin] = p.deviceLogins.Load()
	}
	return out
}

// moreRelayOrigins are the relay origins of Config.MorePairs.
func (s *Service) moreRelayOrigins() []string {
	var out []string
	for _, p := range s.pairs[1:] {
		out = append(out, p.relay)
	}
	return out
}

// Handler serves both origins, picked by the request's host name: on the
// sign-in host the pages and the client API, on the relay host the
// relay's endpoints, PathEdgeInfo and the join page. A path of one origin requested on
// the other is refused with 421 Misdirected Request naming the right
// origin, so no signed-in page, cookie or API answer is served on the
// relay host.
// GET /healthz answers on any host. Call SetLink first.
func (s *Service) Handler() http.Handler {
	pages := http.NewServeMux()
	pages.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/servers", http.StatusSeeOther)
	})
	registerAssets(pages)
	pages.HandleFunc("GET /signin", s.signinPage)
	pages.HandleFunc("GET "+signinPath, s.signinStart)
	pages.HandleFunc("GET "+signinPath+"/callback", s.signinCallback)
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
	pages.HandleFunc("GET "+edgeproto.PathMCPChoose, s.signedIn(s.mcpChoose))
	if s.reviewer != nil {
		pages.HandleFunc("GET "+PathReviewerSignin, s.reviewerPage)
		pages.HandleFunc("POST "+PathReviewerSignin, s.reviewerSignin)
	}

	api := http.NewServeMux()
	api.HandleFunc("POST "+edgeproto.PathDeviceStart, s.api(s.apiDeviceStart))
	api.HandleFunc("POST "+edgeproto.PathDeviceToken, s.api(s.apiDeviceToken))
	api.HandleFunc("POST "+edgeproto.PathLogout, s.api(s.apiLogout))
	api.HandleFunc("GET "+edgeproto.PathServers, s.api(s.apiServers))
	api.HandleFunc("GET "+edgeproto.PathAccount, s.api(s.apiAccount))

	signin := http.NewServeMux()
	signin.Handle("/v1/", api)
	signin.Handle("/", s.pageHeaders(pageCSP, http.NewCrossOriginProtection().Handler(pages)))

	join := http.NewServeMux()
	join.HandleFunc("GET /join/{server_id}/{invitation_id}", s.joinPage)
	join.HandleFunc("GET /join.js", serveJoinScript)
	registerAssets(join)
	joinPages := s.pageHeaders(pageCSP+"; script-src 'self'", join)

	relay := http.NewServeMux()
	s.link.Register(relay)
	s.registerUniversal(relay)
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
		host := edgeproto.RequestHost(r)
		p := s.byHost[host]
		if p == nil {
			http.Error(w, fmt.Sprintf("this edge serves %s, not the host %q", s.originList(), host), http.StatusMisdirectedRequest)
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), pairKey{}, p))
		if host == p.signinHost {
			if routes(relay, r) {
				misdirected(w, r, p.relay)
				return
			}
			signin.ServeHTTP(w, r)
			return
		}
		switch {
		case routes(relay, r):
			relay.ServeHTTP(w, r)
		case routes(join, r):
			joinPages.ServeHTTP(w, r)
		case routes(api, r) || routes(pages, r):
			misdirected(w, r, p.signin)
		default:
			http.NotFound(w, r)
		}
	})
}

// originList names every origin the edge serves: "a and b", or
// "a, b, c and d".
func (s *Service) originList() string {
	var origins []string
	for _, p := range s.pairs {
		origins = append(origins, p.signin, p.relay)
	}
	last := len(origins) - 1
	return strings.Join(origins[:last], ", ") + " and " + origins[last]
}

// misdirected refuses a request made on the wrong one of the edge's
// origins.
func misdirected(w http.ResponseWriter, r *http.Request, origin string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusMisdirectedRequest, edgeproto.ErrorBody{
		Error: fmt.Sprintf("%s is served on %s, not on this host", r.URL.Path, origin)})
}
