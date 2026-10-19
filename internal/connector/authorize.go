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
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/sshd"
)

const (
	clientDocTimeout = 5 * time.Second
	maxClientDoc     = 16 << 10
	// wrongCodeLimit wrong pairing codes per wrongCodeWindow, across the
	// whole server, before the consent page stops checking codes.
	wrongCodeLimit  = 10
	wrongCodeWindow = time.Minute
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
// value and never stored.
type authCode struct {
	grant   sshd.ConnectorGrant
	request authRequest
	expires time.Time
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
	h.renderConsent(w, http.StatusOK, consentPage{Request: h.sign("consent", payload), Client: clientLabel(req)})
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
	page := consentPage{Request: signed, Client: clientLabel(req)}
	now := h.now()
	if !h.attemptAllowed(now) {
		page.Error = "Too many wrong codes were tried on this server in the last minute. Wait a minute, then paste the code again."
		h.renderConsent(w, http.StatusTooManyRequests, page)
		return
	}
	grant, err := h.ssh.RedeemConnectorCode(r.PostFormValue("code"), now)
	if errors.Is(err, sshd.ErrConnectorCode) {
		h.recordWrong(now)
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
	h.codes[hash] = authCode{grant: grant, request: req, expires: now.Add(authCodeTTL)}
	h.mu.Unlock()
	slog.Info("connector: authorized", "member", grant.Member, "device", grant.Device, "client", req.ClientID)
	h.redirect(w, r, req, url.Values{"code": {code}})
}

func (h *handler) attemptAllowed(now time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if now.Sub(h.wrongSince) >= wrongCodeWindow {
		h.wrongSince, h.wrong = now, 0
	}
	return h.wrong < wrongCodeLimit
}

func (h *handler) recordWrong(now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if now.Sub(h.wrongSince) >= wrongCodeWindow {
		h.wrongSince, h.wrong = now, 0
	}
	h.wrong++
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
</style>
</head>
<body>
{{if .Failed}}
<h1>Cannot connect to {{.Server}}</h1>
<p class="error">{{.Error}}</p>
{{else}}
<h1>Connect <strong>{{.Client}}</strong> to <strong>{{.Server}}</strong></h1>
<p>It acts as you and can list workspaces, agents and runs, read run history, launch runs, message runs and stop runs.</p>
{{if .Error}}<p class="error" role="alert">{{.Error}}</p>{{end}}
<form method="post">
<input type="hidden" name="request" value="{{.Request}}">
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
