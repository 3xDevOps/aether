package edge

import (
	"fmt"
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
	s.link.ForwardUniversal(w, r, id, body)
}

// universalAuthorize sends a browser without a chosen server to choose
// one on the sign-in origin, and forwards one with a server to it.
func (s *Service) universalAuthorize(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get(edgeproto.ParamServer)
	if id == "" && r.Method == http.MethodGet {
		http.Redirect(w, r, s.signinOrigin+edgeproto.PathMCPChoose+"?"+r.URL.RawQuery, http.StatusSeeOther)
		return
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
	s.forwardUniversal(w, r, id)
}

func (s *Service) forwardUniversal(w http.ResponseWriter, r *http.Request, id string) {
	body, ok := s.link.ReadMCPBody(w, r)
	if !ok {
		return
	}
	s.link.ForwardUniversal(w, r, id, body)
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

// choice is a server on the choose page, with the link that picks it or
// why it cannot be picked.
type choice struct {
	edgeproto.ServerInfo
	Link   string
	Reason string
}

// mcpChoose lists the servers the signed-in account is a member of for
// the universal connector's authorization request, and sends the browser
// on to the chosen one, at once when only one can take it, with a
// ConnectorAssertion of the account.
func (s *Service) mcpChoose(w http.ResponseWriter, r *http.Request, v visitor) error {
	if !s.chooseLimit.allow(addrKeys(r)...) {
		return edgeproto.RefusalTooMany
	}
	q := r.URL.Query()
	if q.Get("client_id") == "" || q.Get("code_challenge") == "" {
		return pageErr(http.StatusBadRequest, "this authorization request has no client_id or code_challenge; start the connection again from ChatGPT or Claude")
	}
	rows, err := s.servers(r.Context(), v.Account.Account)
	if err != nil {
		return err
	}
	var ready, other []choice
	for _, row := range rows {
		c := choice{ServerInfo: row.ServerInfo}
		switch version := s.link.Version(row.ID); {
		case !row.Member && !row.Owner:
			c.Reason = "you are invited but have not joined; open the invitation link first"
		case version == 0:
			c.Reason = "not connected to this edge"
		case version < edgeproto.VersionUniversalMCP:
			c.Reason = "its aether-server is too old for this URL; an admin runs sudo aether-server update"
		default:
			pick := url.Values{}
			for k, vs := range q {
				pick[k] = vs
			}
			pick.Set(edgeproto.ParamServer, row.ID)
			c.Link = edgeproto.PathMCPChoose + "?" + pick.Encode()
			ready = append(ready, c)
			continue
		}
		other = append(other, c)
	}
	if id := q.Get(edgeproto.ParamServer); id != "" {
		i := slices.IndexFunc(ready, func(c choice) bool { return c.ID == id })
		if i < 0 {
			return pageErr(http.StatusForbidden, "server %s is not one of yours that can take this connection; pick one from the list", id)
		}
		return s.handOff(w, r, v, ready[i].ID)
	}
	if len(ready) == 1 {
		return s.handOff(w, r, v, ready[0].ID)
	}
	s.render(w, http.StatusOK, "choose", s.view(&v, "Choose a server", struct{ Ready, Other []choice }{ready, other}))
	return nil
}

// handOff sends the browser to serverID's consent page with the
// authorization request and an assertion that the visitor chose it.
func (s *Service) handOff(w http.ResponseWriter, r *http.Request, v visitor, serverID string) error {
	q := r.URL.Query()
	// The server checks the assertion against its own clock, as it checks
	// grants, which the relay also signs with the wall clock.
	now := time.Now()
	signed, err := edgeproto.SignConnectorAssertion(s.key, edgeproto.ConnectorAssertion{
		Issuer: s.relayOrigin, ServerID: serverID, Nonce: edgeproto.NewConnID(), Account: v.Account.Account,
		ClientID: q.Get("client_id"), Challenge: q.Get("code_challenge"),
		IssuedAt: now, ExpiresAt: now.Add(edgeproto.ConnectorAssertionTTL),
	})
	if err != nil {
		return pageErr(http.StatusBadRequest, "%v", err)
	}
	q.Set(edgeproto.ParamServer, serverID)
	q.Set(edgeproto.ParamAssertion, signed)
	http.Redirect(w, r, s.relayOrigin+edgeproto.PathUniversalMCPAuthorize+"?"+q.Encode(), http.StatusSeeOther)
	return nil
}
