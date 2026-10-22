package connector

import (
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
// signed, once its client and redirect URI checked out.
type authRequest struct {
	ClientID    string `json:"c"`
	ClientName  string `json:"n"`
	RedirectURI string `json:"r"`
	State       string `json:"s,omitempty"`
	Challenge   string `json:"p"`
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
func (h *handler) authorizePage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
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
	req := authRequest{ClientID: clientID, ClientName: doc.ClientName, RedirectURI: redirect, State: q.Get("state"), Challenge: q.Get("code_challenge")}
	switch {
	case q.Get("response_type") != "code":
		h.redirectError(w, r, req, "unsupported_response_type", "response_type must be code")
		return
	case req.Challenge == "" || q.Get("code_challenge_method") != "S256":
		h.redirectError(w, r, req, "invalid_request", "PKCE is required: send code_challenge with code_challenge_method S256")
		return
	case !h.sameResource(q.Get("resource")):
		h.redirectError(w, r, req, "invalid_target", "resource must be "+h.base)
		return
	}
	payload, err := json.Marshal(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.renderConsent(w, http.StatusOK, consentPage{Request: h.sign("consent", payload), Client: clientLabel(req), Write: true})
}

// authorizeAllow spends the pairing code the member pasted and sends the
// client an authorization code bound to the member, the code's device,
// the client, the redirect URI and the PKCE challenge.
func (h *handler) authorizeAllow(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	signed := r.PostFormValue("request")
	payload, ok := h.open("consent", signed)
	var req authRequest
	if !ok || json.Unmarshal(payload, &req) != nil {
		h.renderError(w, "This form was not issued by this server. Start the connection again from ChatGPT or Claude.")
		return
	}
	scopes := grantedScopes(r.PostForm["scope"])
	page := consentPage{Request: signed, Client: clientLabel(req),
		Write: slices.Contains(scopes, string(mcpbridge.ScopeWrite)), Control: slices.Contains(scopes, string(mcpbridge.ScopeControl))}
	now := h.now()
	from := clientPrefix(r)
	if refused := h.attemptRefused(from, now); refused != "" {
		page.Error = refused
		h.renderConsent(w, http.StatusTooManyRequests, page)
		return
	}
	grant, err := h.ssh.RedeemConnectorCode(r.PostFormValue("code"), now)
	if errors.Is(err, sshd.ErrConnectorCode) {
		h.recordWrong(from, now)
		page.Error = "That code is wrong, expired or already used. Run aether mcp for a new one and paste it here."
		h.renderConsent(w, http.StatusForbidden, page)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	code, hash, err := newToken()
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
	slog.Info("connector: authorized", "member", grant.Member, "device", grant.Device, "client", req.ClientID, "scopes", scopes)
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
	q.Set("iss", h.base)
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
<title>{{if .Failed}}Cannot connect{{else}}Connect {{.Client}} to {{.Server}}{{end}}</title>
<style>
body{font:16px/1.5 system-ui,sans-serif;max-width:34rem;margin:3rem auto;padding:0 1rem;color:#1a1a1a}
h1{font-size:1.4rem;font-weight:500}
.error{background:#fdecec;border:1px solid #e5a5a5;padding:.6rem .8rem;border-radius:6px}
input{font:inherit;font-size:1.2rem;letter-spacing:.1em;width:10rem;padding:.4rem;text-transform:uppercase}
button{font:inherit;padding:.45rem 1.4rem;margin-left:.4rem}
fieldset{border:1px solid #ccc;border-radius:6px;padding:.2rem .9rem}
input[type=checkbox]{width:auto;font-size:inherit}
</style>
</head>
<body>
{{if .Failed}}
<h1>Cannot connect to {{.Server}}</h1>
<p class="error">{{.Error}}</p>
{{else}}
<h1>Connect <strong>{{.Client}}</strong> to <strong>{{.Server}}</strong></h1>
<p>It acts as you. It can always list workspaces, agents and runs and read run history; every member can read every run on this server, so it can read all of them.</p>
{{if .Error}}<p class="error" role="alert">{{.Error}}</p>{{end}}
<form method="post">
<input type="hidden" name="request" value="{{.Request}}">
<fieldset>
<legend>Also allow it to:</legend>
<p><label><input type="checkbox" name="scope" value="write"{{if .Write}} checked{{end}}> <strong>Launch and message</strong>: launch runs and message them.</label></p>
<p><label><input type="checkbox" name="scope" value="control"{{if .Control}} checked{{end}}> <strong>Stop</strong>: stop runs. Text an agent wrote that the assistant reads could then talk it into ending work without asking you.</label></p>
</fieldset>
<p><label for="code">Paste the code from <code>aether mcp</code>:</label></p>
<p><input id="code" name="code" autocomplete="one-time-code" autocapitalize="characters" spellcheck="false" required autofocus><button type="submit"><strong>Allow</strong></button></p>
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

// sameResource reports whether resource names the base URL, ignoring a
// trailing slash and the case of the scheme and host.
func (h *handler) sameResource(resource string) bool {
	u, err := url.Parse(resource)
	if err != nil {
		return false
	}
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimSuffix(u.Path, "/")
	u.RawPath = ""
	return u.String() == h.base
}
