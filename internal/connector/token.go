package connector

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/3xDevOps/Aether/internal/store"
)

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
}

// tokenError is an RFC 6749 error response.
type tokenError struct {
	Error       string `json:"error"`
	Description string `json:"error_description"`
}

const reconnect = "; connect again with a new code from aether mcp"

// token is the token endpoint: authorization_code with PKCE, and
// refresh_token with rotation.
func (h *handler) token(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), tokenTimeout)
	defer cancel()
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		tokenFail(w, http.StatusBadRequest, "invalid_request", "the body is not a form: "+err.Error())
		return
	}
	clientID := r.PostForm.Get("client_id")
	if user, _, ok := r.BasicAuth(); ok && clientID == "" {
		// RFC 6749 form-encodes the client_id in Basic authentication.
		clientID, _ = url.QueryUnescape(user)
	}
	switch {
	case clientID == "":
		tokenFail(w, http.StatusUnauthorized, "invalid_client", "client_id is required")
		return
	case r.PostForm.Has("resource") && !h.sameResource(r.PostForm.Get("resource")):
		tokenFail(w, http.StatusBadRequest, "invalid_target", "resource must be "+h.base)
		return
	}
	switch grant := r.PostForm.Get("grant_type"); grant {
	case "authorization_code":
		h.exchangeCode(ctx, w, r, clientID)
	case "refresh_token":
		h.refresh(ctx, w, r, clientID)
	case "":
		tokenFail(w, http.StatusBadRequest, "invalid_request", "grant_type is required")
	default:
		tokenFail(w, http.StatusBadRequest, "unsupported_grant_type", "grant_type "+grant+" is not supported; use authorization_code or refresh_token")
	}
}

func (h *handler) exchangeCode(ctx context.Context, w http.ResponseWriter, r *http.Request, clientID string) {
	code, verifier := r.PostForm.Get("code"), r.PostForm.Get("code_verifier")
	if code == "" || verifier == "" || !r.PostForm.Has("resource") {
		tokenFail(w, http.StatusBadRequest, "invalid_request", "code, code_verifier and resource are required")
		return
	}
	now := h.now()
	ac, ok := h.takeCode(code)
	switch {
	case !ok || !now.Before(ac.expires):
		tokenFail(w, http.StatusBadRequest, "invalid_grant", "the authorization code is wrong, expired or already used")
		return
	case ac.request.ClientID != clientID:
		tokenFail(w, http.StatusBadRequest, "invalid_grant", "the authorization code was issued to another client")
		return
	case r.PostForm.Has("redirect_uri") && r.PostForm.Get("redirect_uri") != ac.request.RedirectURI:
		tokenFail(w, http.StatusBadRequest, "invalid_grant", "redirect_uri does not match the authorization request")
		return
	case !pkceMatches(verifier, ac.request.Challenge):
		tokenFail(w, http.StatusBadRequest, "invalid_grant", "code_verifier does not match the code_challenge")
		return
	}
	if err := h.ssh.CheckConnectorGrant(ctx, ac.grant); err != nil {
		tokenFail(w, http.StatusBadRequest, "invalid_grant", err.Error()+reconnect)
		return
	}
	access, refresh, tokens, err := newTokens(now)
	if err != nil {
		tokenFail(w, http.StatusInternalServerError, "server_error", err.Error())
		return
	}
	conn := &store.Connector{Member: ac.grant.Member, Device: ac.grant.Device, ClientID: clientID, ClientName: ac.request.ClientName,
		RedirectHost: redirectHost(ac.request.RedirectURI), Resource: h.base, Scopes: ac.scopes, CreatedAt: now}
	if err := h.store.CreateConnector(ctx, conn, tokens); err != nil {
		tokenFail(w, http.StatusInternalServerError, "server_error", err.Error())
		return
	}
	slog.Info("connector: created", "connector", conn.ID, "member", conn.Member, "device", conn.Device, "redirect_host", conn.RedirectHost, "scopes", conn.Scopes)
	writeTokens(w, access, refresh)
}

func (h *handler) refresh(ctx context.Context, w http.ResponseWriter, r *http.Request, clientID string) {
	presented := r.PostForm.Get("refresh_token")
	if presented == "" {
		tokenFail(w, http.StatusBadRequest, "invalid_request", "refresh_token is required")
		return
	}
	now := h.now()
	hash := tokenHash(presented)
	c, err := h.store.ConnectorByRefresh(ctx, hash, now)
	if err != nil {
		refreshFail(w, err)
		return
	}
	switch {
	case c.ClientID != clientID:
		tokenFail(w, http.StatusBadRequest, "invalid_grant", "the refresh token was issued to another client")
		return
	case !now.Before(c.RefreshExpiresAt):
		tokenFail(w, http.StatusBadRequest, "invalid_grant", "the refresh token lapsed after 30 days unused"+reconnect)
		return
	case c.Resource != h.base:
		tokenFail(w, http.StatusBadRequest, "invalid_grant", "the refresh token is for "+c.Resource+", not "+h.base+reconnect)
		return
	}
	if err = h.ssh.CheckConnectorGrant(ctx, grantOf(c)); err != nil {
		tokenFail(w, http.StatusBadRequest, "invalid_grant", "connector "+c.ID+": "+err.Error()+reconnect)
		return
	}
	access, refresh, tokens, err := newTokens(now)
	if err != nil {
		tokenFail(w, http.StatusInternalServerError, "server_error", err.Error())
		return
	}
	if err = h.store.RotateConnector(ctx, c.ID, hash, tokens, now); err != nil {
		refreshFail(w, err)
		return
	}
	writeTokens(w, access, refresh)
}

func refreshFail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrRefreshJustRotated):
		tokenFail(w, http.StatusBadRequest, "invalid_grant", "another refresh rotated this refresh token less than a minute ago; use the refresh token it returned")
	case errors.Is(err, store.ErrRefreshReused):
		slog.Warn("connector: a rotated refresh token was used again; connector revoked")
		tokenFail(w, http.StatusBadRequest, "invalid_grant", "this refresh token was already used, so its connector was revoked"+reconnect)
	case errors.Is(err, store.ErrNotFound):
		tokenFail(w, http.StatusBadRequest, "invalid_grant", "no connector holds this refresh token; it lapsed, or the connector was revoked"+reconnect)
	default:
		tokenFail(w, http.StatusInternalServerError, "server_error", err.Error())
	}
}

func newTokens(now time.Time) (access, refresh string, tokens store.ConnectorTokens, err error) {
	access, tokens.Access, err = newToken()
	if err != nil {
		return "", "", tokens, err
	}
	refresh, tokens.Refresh, err = newToken()
	if err != nil {
		return "", "", tokens, err
	}
	tokens.AccessExpiresAt, tokens.RefreshExpiresAt = now.Add(accessTokenTTL), now.Add(refreshTokenTTL)
	return access, refresh, tokens, nil
}

func writeTokens(w http.ResponseWriter, access, refresh string) {
	w.Header().Set("Pragma", "no-cache")
	writeJSON(w, http.StatusOK, tokenResponse{AccessToken: access, TokenType: "Bearer",
		ExpiresIn: int(accessTokenTTL / time.Second), RefreshToken: refresh})
}

func tokenFail(w http.ResponseWriter, status int, code, description string) {
	writeJSON(w, status, tokenError{Error: code, Description: description})
}
