package connector

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"time"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/mcpbridge"
	"github.com/3xDevOps/Aether/internal/sshd"
)

const (
	clientDocTimeout = 5 * time.Second
	maxClientDoc     = 16 << 10
	// wrongCodeLimit wrong pairing codes per wrongCodeWindow from one client
	// address before the consent page stops checking that address's codes.
	// A code has 40 bits and lives 10 minutes, so the limit only slows a
	// guesser; keyed by address, it never locks out anyone else.
	wrongCodeLimit  = 10
	wrongCodeWindow = time.Minute
	// serverWrongCodeLimit wrong pairing codes per wrongCodeWindow from all
	// addresses together, so a guesser holding many addresses, such as an
	// IPv6 /48, is still slowed. It takes 30 addresses at their limit.
	serverWrongCodeLimit = 300
)

// clientHosts are the hosts whose client_id metadata documents the server
// trusts: ChatGPT's and Claude's.
var clientHosts = []string{"chatgpt.com", "claude.ai"}

// clientDoc is what the server reads of a client_id metadata document.
type clientDoc struct {
	ClientID     string   `json:"client_id"`
	ClientName   string   `json:"client_name"`
	RedirectURIs []string `json:"redirect_uris"`
}

// fetchClient reads the client_id metadata document clientID names, on
// every authorization request, so a client's change of redirect takes
// effect at once.
func (h *handler) fetchClient(ctx context.Context, clientID string) (clientDoc, error) {
	ctx, cancel := context.WithTimeout(ctx, clientDocTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, clientID, nil)
	if err != nil {
		return clientDoc{}, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := h.fetch.Do(req)
	if err != nil {
		return clientDoc{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return clientDoc{}, fmt.Errorf("%s answered %s", clientID, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxClientDoc+1))
	if err != nil {
		return clientDoc{}, fmt.Errorf("read %s: %w", clientID, err)
	}
	if len(body) > maxClientDoc {
		return clientDoc{}, fmt.Errorf("%s is larger than %d KiB", clientID, maxClientDoc>>10)
	}
	var doc clientDoc
	switch err := json.Unmarshal(body, &doc); {
	case err != nil:
		return clientDoc{}, fmt.Errorf("%s is not a JSON client document: %w", clientID, err)
	case doc.ClientID != clientID:
		return clientDoc{}, fmt.Errorf("%s names client_id %q, not its own URL", clientID, doc.ClientID)
	case doc.ClientName == "" || len(doc.RedirectURIs) == 0:
		return clientDoc{}, fmt.Errorf("%s has no client_name or no redirect_uris", clientID)
	}
	return doc, nil
}

// authRequest is the authorization request the consent form carries,
// signed, once its client and redirect URI checked out. Resource is the
// connector URL it is for. Asserted is set when the edge's assertion
// names a member and the server's policy lets that replace the pairing
// code.
type authRequest struct {
	ClientID    string    `json:"c"`
	ClientName  string    `json:"n"`
	RedirectURI string    `json:"r"`
	State       string    `json:"s,omitempty"`
	Challenge   string    `json:"p"`
	Resource    string    `json:"u"`
	Asserted    *asserted `json:"a,omitempty"`
}

// asserted is the account a ConnectorAssertion named, its nonce, which
// the consent form spends, and when the assertion expires.
type asserted struct {
	Account edgeproto.Account `json:"a"`
	Nonce   string            `json:"n"`
	Expires time.Time         `json:"e"`
}

// authCode is an issued authorization code, kept by the SHA-256 of its
// value and never stored. scopes are the scopes the member checked.
type authCode struct {
	grant   sshd.ConnectorGrant
	request authRequest
	scopes  []string
	expires time.Time
}

// wrongCodes counts the wrong pairing codes sent since since, from one
// client address or from all of them.
type wrongCodes struct {
	since time.Time
	n     int
}

// authorizePage checks an authorization request and shows its consent
// page. Until the client and redirect URI check out there is nowhere safe
// to send an error, so it is shown on the page; later errors go back to
// the client.
func (h *handler) authorizePage(w http.ResponseWriter, r *http.Request, res resource) {
	q := r.URL.Query()
	if res == h.universal && q.Get(edgeproto.ParamServer) != h.serverID {
		h.renderError(w, fmt.Sprintf("This request names server %q, not this server, %s. Start the connection again from ChatGPT or Claude.",
			q.Get(edgeproto.ParamServer), h.serverID))
		return
	}
	clientID, redirect := q.Get("client_id"), q.Get("redirect_uri")
	u, err := url.Parse(clientID)
	if err != nil || u.Scheme != "https" || !slices.Contains(h.clientHosts, u.Host) {
		host := clientID
		if err == nil && u.Host != "" {
			host = u.Host
		}
		h.renderError(w, fmt.Sprintf("Aether connectors work with ChatGPT and Claude; %s is not one of them.", host))
		return
	}
	doc, err := h.fetchClient(r.Context(), clientID)
	if err != nil {
		h.renderError(w, "This client's metadata document cannot be used: "+err.Error())
		return
	}
	ru, err := url.Parse(redirect)
	if err != nil || ru.Scheme != "https" || ru.Host != u.Host || !slices.Contains(doc.RedirectURIs, redirect) {
		h.renderError(w, fmt.Sprintf("The redirect_uri %q is not an https URL on %s that %s lists.", redirect, u.Host, clientID))
		return
	}
	req := authRequest{ClientID: clientID, ClientName: doc.ClientName, RedirectURI: redirect, State: q.Get("state"),
		Challenge: q.Get("code_challenge"), Resource: res.base}
	switch {
	case q.Get("response_type") != "code":
		h.redirectError(w, r, req, "unsupported_response_type", "response_type must be code")
		return
	case req.Challenge == "" || q.Get("code_challenge_method") != "S256":
		h.redirectError(w, r, req, "invalid_request", "PKCE is required: send code_challenge with code_challenge_method S256")
		return
	case !sameResource(q.Get("resource"), res.base):
		h.redirectError(w, r, req, "invalid_target", "resource must be "+res.base)
		return
	}
	page := consentPage{Client: clientLabel(req), Write: true}
	if signed := q.Get(edgeproto.ParamAssertion); signed != "" && res == h.universal && h.ssh.EdgeAccess() == edgeproto.PolicyAccount {
		a, name, aerr := h.assertion(r.Context(), signed, req)
		if aerr != nil {
			h.renderError(w, "Cannot connect as your GitHub account: "+aerr.Error()+". Start the connection again from ChatGPT or Claude.")
			return
		}
		req.Asserted, page.Member, page.Account = a, name, accountLabel(a.Account)
	}
	payload, err := json.Marshal(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	page.Request = h.sign("consent", payload)
	h.renderConsent(w, http.StatusOK, page)
}

// assertion checks the edge's assertion for req and maps its account to
// a member, whose name it returns.
func (h *handler) assertion(ctx context.Context, signed string, req authRequest) (*asserted, string, error) {
	now := h.now()
	a, err := h.ssh.VerifyConnectorAssertion(signed, req.ClientID, req.Challenge, now)
	if err != nil {
		return nil, "", fmt.Errorf("the edge's sign-in for it does not check out: %w", err)
	}
	if h.spentNonce(a.Nonce, now) {
		return nil, "", errors.New("the edge's sign-in for it was already used")
	}
	_, name, err := h.ssh.ConnectorAccountGrant(ctx, a.Account)
	if err != nil {
		return nil, "", err
	}
	return &asserted{Account: a.Account, Nonce: a.Nonce, Expires: a.ExpiresAt.Add(edgeproto.ClockSkew)}, name, nil
}

// spentNonce reports whether a consent form spent nonce.
func (h *handler) spentNonce(nonce string, now time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	expires, ok := h.spent[nonce]
	return ok && now.Before(expires)
}

// spendNonce spends nonce, kept until expires, and reports whether it was
// unspent.
func (h *handler) spendNonce(nonce string, expires, now time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	maps.DeleteFunc(h.spent, func(_ string, e time.Time) bool { return !now.Before(e) })
	if _, ok := h.spent[nonce]; ok {
		return false
	}
	h.spent[nonce] = expires
	return true
}

// accountLabel names an edge account on the consent page: github dana.
func accountLabel(a edgeproto.Account) string {
	return a.Provider + " " + cmp.Or(a.Login, a.Email, a.Subject)
}

// authorizeAllow spends the pairing code the member pasted, or the edge
// assertion the form carries, and sends the client an authorization code
// bound to the member, the code's device or the asserted account, the
// client, the redirect URI and the PKCE challenge.
func (h *handler) authorizeAllow(w http.ResponseWriter, r *http.Request, res resource) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	signed := r.PostFormValue("request")
	payload, ok := h.open("consent", signed)
	var req authRequest
	if !ok || json.Unmarshal(payload, &req) != nil || req.Resource != res.base {
		h.renderError(w, "This form was not issued by this server. Start the connection again from ChatGPT or Claude.")
		return
	}
	scopes := grantedScopes(r.PostForm["scope"])
	page := consentPage{Request: signed, Client: clientLabel(req),
		Write: slices.Contains(scopes, string(mcpbridge.ScopeWrite)), Control: slices.Contains(scopes, string(mcpbridge.ScopeControl))}
	now := h.now()
	if req.Asserted != nil {
		grant, err := h.assertedGrant(r.Context(), req.Asserted, now)
		if err != nil {
			h.renderError(w, "Cannot connect as your GitHub account: "+err.Error()+". Start the connection again from ChatGPT or Claude.")
			return
		}
		h.issueCode(w, r, req, grant, scopes, now)
		return
	}
	from := clientPrefix(r)
	if refused := h.attemptRefused(from, now); refused != "" {
		page.Error = refused
		h.renderConsent(w, http.StatusTooManyRequests, page)
		return
	}
	grant, err := h.ssh.RedeemConnectorCode(r.PostFormValue("code"), now)
	if errors.Is(err, sshd.ErrConnectorCode) {
		h.recordWrong(from, now)
		page.Error = "That code is wrong, expired or already used. Get a new one from Settings → ChatGPT and Claude in the Aether app, or aether mcp, and paste it here."
		h.renderConsent(w, http.StatusForbidden, page)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.issueCode(w, r, req, grant, scopes, now)
}

// assertedGrant spends the assertion a consent form carries and maps its
// account to a member again, as the policy now stands.
func (h *handler) assertedGrant(ctx context.Context, a *asserted, now time.Time) (sshd.ConnectorGrant, error) {
	if !now.Before(a.Expires) {
		return sshd.ConnectorGrant{}, fmt.Errorf("the edge's sign-in for it expired at %s", a.Expires.UTC().Format(time.RFC3339))
	}
	if !h.spendNonce(a.Nonce, a.Expires, now) {
		return sshd.ConnectorGrant{}, errors.New("the edge's sign-in for it was already used")
	}
	grant, _, err := h.ssh.ConnectorAccountGrant(ctx, a.Account)
	return grant, err
}

// issueCode sends the client an authorization code for req, bound to
// grant and scopes.
func (h *handler) issueCode(w http.ResponseWriter, r *http.Request, req authRequest, grant sshd.ConnectorGrant, scopes []string, now time.Time) {
	code, hash, err := newToken(h.prefixOf(req.Resource))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.mu.Lock()
	for k, ac := range h.codes {
		if !now.Before(ac.expires) {
			delete(h.codes, k)
		}
	}
	h.codes[hash] = authCode{grant: grant, request: req, scopes: scopes, expires: now.Add(authCodeTTL)}
	h.mu.Unlock()
	slog.Info("connector: authorized", "member", grant.Member, "device", grant.Device, "subject", grant.Subject,
		"resource", req.Resource, "client", req.ClientID, "scopes", scopes)
	h.redirect(w, r, req, url.Values{"code": {code}})
}

// grantedScopes is read and whichever of write and control the consent
// form checked; anything else in it is ignored.
func grantedScopes(checked []string) []string {
	scopes := []string{string(mcpbridge.ScopeRead)}
	for _, s := range []mcpbridge.Scope{mcpbridge.ScopeWrite, mcpbridge.ScopeControl} {
		if slices.Contains(checked, string(s)) {
			scopes = append(scopes, string(s))
		}
	}
	return scopes
}

// clientPrefix is the address the edge says r came from, widened to its
// /64 for IPv6, where one client holds a whole /64. The edge sets the
// header on every request it relays and replaces any a client sends.
func clientPrefix(r *http.Request) netip.Prefix {
	ap, err := netip.ParseAddrPort(r.Header.Get(edgeproto.HeaderClientAddr))
	if err != nil {
		return netip.Prefix{}
	}
	addr := ap.Addr().Unmap()
	bits := 32
	if addr.Is6() {
		bits = 64
	}
	p, _ := addr.Prefix(bits)
	return p
}

// attemptRefused says why a pairing code from from is not checked now, or
// is empty when it is.
func (h *handler) attemptRefused(from netip.Prefix, now time.Time) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch {
	case h.wrong[from].live(now) >= wrongCodeLimit:
		return fmt.Sprintf("%d wrong codes came from your address in the last minute. Wait a minute, then paste the code again.", wrongCodeLimit)
	case h.wrongAll.live(now) >= serverWrongCodeLimit:
		return fmt.Sprintf("%d wrong codes came to this server from all addresses in the last minute. Wait a minute, then paste the code again.", serverWrongCodeLimit)
	}
	return ""
}

func (h *handler) recordWrong(from netip.Prefix, now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if now.Sub(h.wrongSwept) >= wrongCodeWindow {
		maps.DeleteFunc(h.wrong, func(_ netip.Prefix, wc wrongCodes) bool { return wc.live(now) == 0 })
		h.wrongSwept = now
	}
	h.wrong[from] = h.wrong[from].add(now)
	h.wrongAll = h.wrongAll.add(now)
}

// live is how many wrong codes count at now: none once the window that
// began at since has passed.
func (wc wrongCodes) live(now time.Time) int {
	if now.Sub(wc.since) >= wrongCodeWindow {
		return 0
	}
	return wc.n
}

// add counts one more wrong code at now, starting a new window if the
// last one has passed.
func (wc wrongCodes) add(now time.Time) wrongCodes {
	if wc.live(now) == 0 {
		return wrongCodes{since: now, n: 1}
	}
	wc.n++
	return wc
}

// takeCode spends an authorization code, live or not.
func (h *handler) takeCode(code string) (authCode, bool) {
	hash := tokenHash(code)
	h.mu.Lock()
	defer h.mu.Unlock()
	ac, ok := h.codes[hash]
	delete(h.codes, hash)
	return ac, ok
}

func (h *handler) redirectError(w http.ResponseWriter, r *http.Request, req authRequest, code, description string) {
	h.redirect(w, r, req, url.Values{"error": {code}, "error_description": {description}})
}

// redirect answers an authorization request at its redirect URI, with its
// state and this server as iss (RFC 9207).
func (h *handler) redirect(w http.ResponseWriter, r *http.Request, req authRequest, params url.Values) {
	u, err := url.Parse(req.RedirectURI)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	q := u.Query()
	for k, v := range params {
		q[k] = v
	}
	if req.State != "" {
		q.Set("state", req.State)
	}
	q.Set("iss", req.Resource)
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusSeeOther)
}

func pkceMatches(verifier, challenge string) bool {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:]) == challenge
}

// redirectHost is the host a redirect URI returns to, such as chatgpt.com.
func redirectHost(uri string) string {
	u, err := url.Parse(uri)
	if err != nil {
		return uri
	}
	return u.Host
}

// clientLabel names a client as the consent page and aether mcp show it:
// ChatGPT (chatgpt.com).
func clientLabel(req authRequest) string {
	return req.ClientName + " (" + redirectHost(req.RedirectURI) + ")"
}

type consentPage struct {
	Client  string
	Server  string
	Request string
	Error   string
	// Member and Account are the member and edge account an assertion
	// named, which replace the pairing code.
	Member  string
	Account string
	// Write and Control are whether those scopes' boxes are checked.
	Write   bool
	Control bool
	// Failed shows only Error, for a request that cannot continue.
	Failed bool
}

var consentTemplate = template.Must(template.New("consent").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{if .Failed}}Cannot connect{{else}}Connect {{.Client}} to {{.Server}}{{if .Member}} as {{.Member}}{{end}}{{end}}</title>
<style>
body{font:16px/1.5 system-ui,sans-serif;max-width:34rem;margin:3rem auto;padding:0 1rem;color:#1a1a1a}
h1{font-size:1.4rem;font-weight:500}
.error{background:#fdecec;border:1px solid #e5a5a5;padding:.6rem .8rem;border-radius:6px}
input{font:inherit;font-size:1.2rem;letter-spacing:.1em;width:10rem;padding:.4rem;text-transform:uppercase}
button{font:inherit;padding:.45rem 1.4rem;min-height:2.75rem}
fieldset{border:1px solid #ccc;border-radius:6px;padding:.2rem .9rem}
input[type=checkbox]{width:1.25rem;height:1.25rem;font-size:inherit;vertical-align:middle}
.row{display:flex;flex-wrap:wrap;gap:.5rem;align-items:center}
</style>
</head>
<body>
{{if .Failed}}
<h1>Cannot connect to {{.Server}}</h1>
<p class="error">{{.Error}}</p>
{{else}}
<h1>Connect <strong>{{.Client}}</strong> to <strong>{{.Server}}</strong>{{if .Member}} as <strong>{{.Member}}</strong>{{end}}</h1>
<p>It acts as you. It can always list workspaces, agents and runs and read run history; every member can read every run on this server, so it can read all of them.</p>
{{if .Error}}<p class="error" role="alert">{{.Error}}</p>{{end}}
<form method="post">
<input type="hidden" name="request" value="{{.Request}}">
<fieldset>
<legend>Also allow it to:</legend>
<p><label><input type="checkbox" name="scope" value="write"{{if .Write}} checked{{end}}> <strong>Launch and message</strong>: launch runs and swarms, message runs, answer agents' questions, pause, resume and mark runs reviewed.</label></p>
<p><label><input type="checkbox" name="scope" value="control"{{if .Control}} checked{{end}}> <strong>Stop and approve</strong>: decide approvals and permission requests, stop and close runs, cancel swarms. Text an agent wrote that the assistant reads could then talk it into approving or ending work without asking you.</label></p>
</fieldset>
<p>To change these later, revoke the connector and connect again.</p>
{{if .Member}}<p>You signed in at the edge as {{.Account}}, which this server admits as {{.Member}}.</p>
<p><button type="submit" autofocus><strong>Allow</strong></button></p>
{{else}}<p><label for="code">Paste the code from Settings → ChatGPT and Claude in the Aether app, or <code>aether mcp</code>:</label></p>
<p class="row"><input id="code" name="code" autocomplete="one-time-code" autocapitalize="characters" spellcheck="false" required autofocus><button type="submit"><strong>Allow</strong></button></p>{{end}}
</form>
{{end}}
</body>
</html>
`))

func (h *handler) renderConsent(w http.ResponseWriter, status int, page consentPage) {
	page.Server = h.serverName()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := consentTemplate.Execute(w, page); err != nil {
		slog.Warn("connector: render consent page", "error", err)
	}
}

func (h *handler) renderError(w http.ResponseWriter, message string) {
	h.renderConsent(w, http.StatusBadRequest, consentPage{Failed: true, Error: message})
}

// prefixOf is the prefix of codes minted for the connector URL base.
func (h *handler) prefixOf(base string) string {
	if base == h.universal.base {
		return h.universal.prefix
	}
	return ""
}

// sameResource reports whether resource names base, ignoring a trailing
// slash and the case of the scheme and host.
func sameResource(resource, base string) bool {
	u, err := url.Parse(resource)
	if err != nil {
		return false
	}
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimSuffix(u.Path, "/")
	u.RawPath = ""
	return u.String() == base
}
