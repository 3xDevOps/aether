package edge

import (
	"fmt"
	"maps"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
)

// serverBlock keys the token routing limit: one client address block
// sending to one server. ChatGPT and Claude call token endpoints from a
// few shared addresses, so a limit per address alone would let one
// server's clients use up every server's.
type serverBlock struct {
	block  netip.Prefix
	server string
}

// registerUniversal adds the universal connector paths to mux, which
// serves the relay origin. The edge serves the metadata and sends the
// browser to choose a server; each server serves the rest, reached by the
// server id its codes and tokens start with.
func (s *Service) registerUniversal(mux *http.ServeMux) {
	base := s.relayOrigin + edgeproto.PathUniversalMCP
	mux.HandleFunc(edgeproto.PathUniversalMCP, s.universalMCP)
	mux.HandleFunc(edgeproto.PathUniversalMCPToken, s.universalToken)
	mux.HandleFunc(edgeproto.PathUniversalMCPAuthorize, s.universalAuthorize)
	mux.Handle("GET "+edgeproto.PathUniversalResourceMetadata, auth.ProtectedResourceMetadataHandler(
		&oauthex.ProtectedResourceMetadata{Resource: base, AuthorizationServers: []string{base}}))
	mux.HandleFunc("GET "+edgeproto.PathUniversalAuthServerMetadata, func(w http.ResponseWriter, _ *http.Request) {
		writeNoStoreJSON(w, http.StatusOK, edgeproto.ConnectorAuthServerMetadata(base))
	})
	mux.HandleFunc("GET "+edgeproto.PathOpenAIChallenge, func(w http.ResponseWriter, r *http.Request) {
		if s.openAIChallenge == "" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(s.openAIChallenge))
	})
}

// universalMCP forwards an MCP request to the server its bearer token
// names.
func (s *Service) universalMCP(w http.ResponseWriter, r *http.Request) {
	metadata := s.relayOrigin + edgeproto.PathUniversalResourceMetadata
	scheme, token, _ := strings.Cut(r.Header.Get("Authorization"), " ")
	if !strings.EqualFold(scheme, "Bearer") || token == "" {
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata=%q`, metadata))
		http.Error(w, "an access token is required", http.StatusUnauthorized)
		return
	}
	id, ok := edgeproto.TokenServer(token)
	if !ok {
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer error="invalid_token", resource_metadata=%q`, metadata))
		http.Error(w, "the access token names no server: it does not start with <server id>.", http.StatusUnauthorized)
		return
	}
	s.forwardUniversal(w, r, id)
}

// universalToken forwards a token request to the server its code or
// refresh token names, within the token routing limit.
func (s *Service) universalToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		tokenFail(w, http.StatusMethodNotAllowed, "invalid_request", "the token endpoint takes POST")
		return
	}
	body, ok := s.link.ReadMCPBody(w, r)
	if !ok {
		return
	}
	form, err := url.ParseQuery(string(body))
	if err != nil {
		tokenFail(w, http.StatusBadRequest, "invalid_request", "the body is not a form: "+err.Error())
		return
	}
	grant := form.Get("code")
	if form.Get("grant_type") == "refresh_token" {
		grant = form.Get("refresh_token")
	}
	id, ok := edgeproto.TokenServer(grant)
	if !ok {
		tokenFail(w, http.StatusBadRequest, "invalid_grant", "the code or refresh token names no server: it does not start with <server id>.")
		return
	}
	keys := make([]serverBlock, 0, 3)
	for _, b := range addrKeys(r) {
		keys = append(keys, serverBlock{b, id})
	}
	if !s.tokenLimit.allow(keys...) {
		tokenFail(w, http.StatusTooManyRequests, "slow_down", fmt.Sprintf("too many token requests for server %s from your address; retry in a few seconds", id))
		return
	}
	s.link.ForwardUniversal(s.guard(w), r, id, body, "")
}

// universalAuthorize sends a browser without a chosen server to choose
// one on the sign-in origin, and forwards one with a server to it, bound
// to the browser by a cookie on the relay origin.
func (s *Service) universalAuthorize(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get(edgeproto.ParamServer)
	if id == "" && r.Method == http.MethodGet {
		http.Redirect(w, r, s.signinOrigin+edgeproto.PathMCPChoose+"?"+r.URL.RawQuery, http.StatusSeeOther)
		return
	}
	body, ok := s.link.ReadMCPBody(w, r)
	if !ok {
		return
	}
	if id == "" && r.Method == http.MethodPost {
		form, err := url.ParseQuery(string(body))
		if err != nil {
			http.Error(w, "the body is not a form: "+err.Error(), http.StatusBadRequest)
			return
		}
		id = form.Get(edgeproto.ParamServer)
	}
	if !edgeproto.ValidServerID(id) {
		http.Error(w, fmt.Sprintf("%s=%q is not a server id; start the connection again from ChatGPT or Claude", edgeproto.ParamServer, id),
			http.StatusBadRequest)
		return
	}
	if !s.chooseLimit.allow(addrKeys(r)...) {
		http.Error(w, string(edgeproto.RefusalTooMany), edgeproto.RefusalTooMany.Status())
		return
	}
	g := s.guard(w)
	browser := ""
	if c, err := r.Cookie(browserCookie); err == nil && edgeproto.ValidToken(c.Value) {
		browser = c.Value
	} else {
		browser = edgeproto.NewToken()
		// SameSite None: the consent page's form posts from the opaque
		// origin its sandbox gives it, a cross-site request.
		g.cookie = &http.Cookie{Name: browserCookie, Value: browser, Path: edgeproto.PathUniversalMCPAuthorize,
			HttpOnly: true, Secure: true, SameSite: http.SameSiteNoneMode}
	}
	s.link.ForwardUniversal(g, r, id, body, edgeproto.BrowserBinding(id, browser))
}

func (s *Service) forwardUniversal(w http.ResponseWriter, r *http.Request, id string) {
	body, ok := s.link.ReadMCPBody(w, r)
	if !ok {
		return
	}
	s.link.ForwardUniversal(s.guard(w), r, id, body, "")
}

// guard limits what a server answers on the universal paths, which every
// server shares one origin for: a Location only to this origin or a
// connector client, and forms only to those. It sets cookie, when set,
// past the relay, which drops every cookie a server sets.
func (s *Service) guard(w http.ResponseWriter) *universalGuard {
	formAction := "form-action 'self'"
	for _, host := range s.clientHosts {
		formAction += " https://" + host
	}
	return &universalGuard{ResponseWriter: w, s: s, formAction: formAction}
}

type universalGuard struct {
	http.ResponseWriter
	s          *Service
	formAction string
	cookie     *http.Cookie
	wrote      bool
	refused    bool
}

func (g *universalGuard) WriteHeader(code int) {
	if code < http.StatusOK {
		g.ResponseWriter.WriteHeader(code)
		return
	}
	if g.wrote {
		return
	}
	g.wrote = true
	h := g.Header()
	if g.cookie != nil {
		http.SetCookie(g.ResponseWriter, g.cookie)
	}
	if csp := h.Get("Content-Security-Policy"); csp != "" {
		h.Set("Content-Security-Policy", csp+"; "+g.formAction)
	}
	if loc := h.Get("Location"); loc != "" && !g.s.allowedLocation(loc) {
		g.refused = true
		h.Del("Location")
		h.Del("Content-Length")
		h.Set("Content-Type", "text/plain; charset=utf-8")
		g.ResponseWriter.WriteHeader(http.StatusBadGateway)
		_, _ = fmt.Fprintf(g.ResponseWriter, "the server sent the browser to %q; on %s%s the edge sends it only to %s or to %s\n",
			loc, g.s.relayOrigin, edgeproto.PathUniversalMCP, g.s.relayOrigin, strings.Join(g.s.clientHosts, " or "))
		return
	}
	g.ResponseWriter.WriteHeader(code)
}

func (g *universalGuard) Write(p []byte) (int, error) {
	if !g.wrote {
		g.WriteHeader(http.StatusOK)
	}
	if g.refused {
		return len(p), nil
	}
	return g.ResponseWriter.Write(p)
}

func (g *universalGuard) Unwrap() http.ResponseWriter { return g.ResponseWriter }

// allowedLocation reports whether loc is a path on the relay origin or
// an https URL there or on a connector client's host name.
func (s *Service) allowedLocation(loc string) bool {
	u, err := url.Parse(loc)
	switch {
	case err != nil || strings.ContainsAny(loc, "\\\r\n\t"):
		return false
	case u.Scheme == "" && u.Host == "":
		return strings.HasPrefix(loc, "/") && !strings.HasPrefix(loc, "//")
	case u.Scheme+"://"+u.Host == s.relayOrigin:
		return true
	}
	return u.Scheme == "https" && u.User == nil && slices.Contains(s.clientHosts, u.Hostname())
}

func writeNoStoreJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, v)
}

func tokenFail(w http.ResponseWriter, status int, code, description string) {
	writeNoStoreJSON(w, status, struct {
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}{code, description})
}

// mcpChoose lists the servers the signed-in account is a member of for
// the universal connector's authorization request. Each it can take has
// a form that posts the request to the relay origin with a
// ConnectorAssertion for that server, so the account always picks the
// server and no URL carries the assertion.
func (s *Service) mcpChoose(w http.ResponseWriter, r *http.Request, v visitor) error {
	if !s.chooseLimit.allow(addrKeys(r)...) {
		return edgeproto.RefusalTooMany
	}
	q := r.URL.Query()
	q.Del(edgeproto.ParamServer)
	q.Del(edgeproto.ParamAssertion)
	if q.Get("client_id") == "" || q.Get("code_challenge") == "" || q.Get("redirect_uri") == "" {
		return pageErr(http.StatusBadRequest, "this authorization request has no client_id, code_challenge or redirect_uri; start the connection again from ChatGPT or Claude")
	}
	rows, err := s.servers(r.Context(), v.Account.Account)
	if err != nil {
		return err
	}
	var fields []field
	for _, k := range slices.Sorted(maps.Keys(q)) {
		for _, val := range q[k] {
			fields = append(fields, field{k, val})
		}
	}
	// The server checks the assertion against its own clock, as it checks
	// grants, which the relay also signs with the wall clock.
	now := time.Now()
	var ready, other []choice
	for _, row := range rows {
		c := choice{ServerInfo: row.ServerInfo}
		switch version := s.link.Version(row.ID); {
		case !row.Member && !row.Owner:
			c.Reason = "you are invited but have not joined; open the invitation link first"
		case version == 0:
			c.Reason = "not connected to this edge"
		case version < edgeproto.VersionConnectorSignIn:
			c.Reason = "its aether-server is too old for this URL; an admin runs sudo aether-server update"
		default:
			c.Assertion, err = edgeproto.SignConnectorAssertion(s.key, edgeproto.ConnectorAssertion{
				Issuer: s.relayOrigin, ServerID: row.ID, Nonce: edgeproto.NewConnID(), Account: v.Account.Account,
				ClientID: q.Get("client_id"), Challenge: q.Get("code_challenge"), RedirectURI: q.Get("redirect_uri"),
				IssuedAt: now, ExpiresAt: now.Add(edgeproto.ConnectorAssertionTTL),
			})
			if err != nil {
				return pageErr(http.StatusBadRequest, "%v", err)
			}
			ready = append(ready, c)
			continue
		}
		other = append(other, c)
	}
	w.Header().Set("Content-Security-Policy", pageCSP+" "+s.relayOrigin)
	s.render(w, http.StatusOK, "choose", s.view(&v, "Choose a server", chooseData{
		Action: s.relayOrigin + edgeproto.PathUniversalMCPAuthorize, Client: redirectHost(q.Get("redirect_uri")),
		Fields: fields, Ready: ready, Other: other,
	}))
	return nil
}

// choice is a server on the choose page, with the assertion its form
// posts or why it cannot be picked.
type choice struct {
	edgeproto.ServerInfo
	Assertion string
	Reason    string
}

type field struct{ Name, Value string }

// chooseData is the choose page: each ready server's form posts Fields,
// the authorization request, to Action. Client is the host the request
// returns to.
type chooseData struct {
	Action, Client string
	Fields         []field
	Ready, Other   []choice
}

func redirectHost(uri string) string {
	u, err := url.Parse(uri)
	if err != nil {
		return uri
	}
	return u.Host
}
