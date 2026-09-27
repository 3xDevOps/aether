package servergw

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/edgeproto"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
	"github.com/3xDevOps/Aether/internal/webgate"
)

const (
	pathLogin  = "/auth/login"
	pathLogout = "/auth/logout"

	// The __Host- prefix makes a browser refuse these cookies unless they
	// are Secure, have Path=/ and name no Domain, so none can be set for a
	// parent domain that other servers under the edge share.
	deviceCookie  = "__Host-aether_device"
	sessionCookie = "__Host-aether_session"
	signinCookie  = "__Host-aether_signin"

	signinTTL = 10 * time.Minute
	// cookieTTL is the most browsers keep a cookie. The server enforces
	// the session idle expiry itself.
	cookieTTL = 400 * 24 * time.Hour
)

// handleLogin starts a sign-in: it keeps a fresh state and PKCE verifier
// in a short-lived cookie and sends the browser to the edge. return=app is
// the Android shell's variant, which the edge returns to the app instead.
// SameSite=Lax, because the edge's redirect back is a cross-site
// navigation that must carry the cookie.
func (e *Edge) handleLogin(w http.ResponseWriter, r *http.Request) {
	q := url.Values{}
	switch ret := r.URL.Query().Get(edgeproto.ParamReturn); ret {
	case "":
	case edgeproto.ReturnApp:
		q.Set(edgeproto.ParamReturn, edgeproto.ReturnApp)
	default:
		writePage(w, http.StatusBadRequest, fmt.Sprintf("%s must be empty or %q, not %q\n", edgeproto.ParamReturn, edgeproto.ReturnApp, ret))
		return
	}
	state, verifier := edgeproto.NewToken(), edgeproto.NewVerifier()
	signin := state + "." + verifier
	// The edge's redirect to the callback is cross-site, so the Strict
	// device cookie does not reach it: the sign-in cookie carries it.
	if c, err := r.Cookie(deviceCookie); err == nil && edgeproto.ValidToken(c.Value) {
		signin += "." + c.Value
	}
	setCookie(w, signinCookie, signin, signinTTL, http.SameSiteLaxMode)
	q.Set(edgeproto.ParamServer, e.serverID)
	q.Set(edgeproto.ParamState, state)
	q.Set(edgeproto.ParamChallenge, edgeproto.PKCEChallenge(verifier))
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, e.edge+edgeproto.PathAuthorize+"?"+q.Encode(), http.StatusSeeOther)
}

// handleCallback finishes a sign-in: it checks the state against the
// cookie, redeems the code over the control connection with the verifier,
// maps the account to a member, and starts a session on the browser's
// device.
func (e *Edge) handleCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	c, err := r.Cookie(signinCookie)
	if err != nil {
		writePage(w, http.StatusBadRequest, "sign-in expired or was started in another browser; start again at "+pathLogin+"\n")
		return
	}
	// The state cookie is good for one callback, whatever its outcome.
	clearCookie(w, signinCookie, http.SameSiteLaxMode)
	state, rest, _ := strings.Cut(c.Value, ".")
	verifier, deviceToken, _ := strings.Cut(rest, ".")
	q := r.URL.Query()
	code := q.Get(edgeproto.ParamCode)
	if !edgeproto.ValidToken(state) || !edgeproto.ValidToken(verifier) ||
		(deviceToken != "" && !edgeproto.ValidToken(deviceToken)) ||
		subtle.ConstantTimeCompare([]byte(q.Get(edgeproto.ParamState)), []byte(state)) != 1 {
		writePage(w, http.StatusBadRequest, "sign-in state does not match this browser's; start again at "+pathLogin+"\n")
		return
	}
	if !edgeproto.ValidToken(code) {
		writePage(w, http.StatusBadRequest, "sign-in code is missing or malformed; start again at "+pathLogin+"\n")
		return
	}
	grant, err := e.agent.RedeemWebCode(r.Context(), code, verifier)
	if err != nil {
		writePage(w, http.StatusForbidden, "sign-in failed: "+err.Error()+"\n")
		return
	}
	if grant.Kind != edgeproto.KindWeb || grant.ServerID != e.serverID {
		writePage(w, http.StatusForbidden, fmt.Sprintf("sign-in failed: the edge granted a %s sign-in to server %s, this server is %s\n",
			grant.Kind, grant.ServerID, e.serverID))
		return
	}
	m, err := e.ssh.EdgeMember(r.Context(), grant.Account)
	if errors.Is(err, edgeproto.RefusalNotMember) {
		writePage(w, http.StatusForbidden, err.Error()+"\n")
		return
	}
	if err != nil {
		writePage(w, http.StatusInternalServerError, "sign-in failed: "+err.Error()+"\n")
		return
	}
	dev, deviceToken, err := e.browserDevice(r.Context(), m.ID, deviceToken, grant.DeviceLabel)
	if err != nil {
		writePage(w, http.StatusInternalServerError, "sign-in failed: "+err.Error()+"\n")
		return
	}
	token := edgeproto.NewToken()
	sess := &domain.BrowserSession{Device: dev.ID, Credential: edgeproto.HashToken(token)}
	if err := e.ids.CreateBrowserSession(r.Context(), sess); err != nil {
		writePage(w, http.StatusInternalServerError, "sign-in failed: "+err.Error()+"\n")
		return
	}
	slog.Info("servergw: edge sign-in", "member", m.ID, "device", dev.ID, "session", sess.ID, "status", dev.Status,
		"provider", grant.Account.Provider, "subject", grant.Account.Subject)
	setCookie(w, deviceCookie, deviceToken, cookieTTL, http.SameSiteStrictMode)
	setCookie(w, sessionCookie, token, cookieTTL, http.SameSiteStrictMode)
	// A pending browser goes to the dashboard too, which shows the approval
	// commands and reloads to itself: this callback's code is spent.
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// browserDevice returns the device a sign-in of member continues on, with
// its token: the device token names when that is an unrevoked browser
// device of member, and a new device otherwise. Signing in again on an
// approved browser therefore needs no new approval, while a token of
// another member's device, or of a revoked one, never carries an approval
// over.
func (e *Edge) browserDevice(ctx context.Context, member domain.MemberID, token, label string) (*domain.Device, string, error) {
	if token != "" {
		dev, err := e.ids.GetDeviceByCredential(ctx, edgeproto.HashToken(token))
		switch {
		case err == nil && dev.Kind == domain.DeviceBrowser && dev.Member == member && dev.Status != domain.DeviceRevoked:
			return dev, token, nil
		case err != nil && !errors.Is(err, store.ErrNotFound):
			return nil, "", err
		}
	}
	token = edgeproto.NewToken()
	if label == "" {
		label = "browser"
	}
	dev := &domain.Device{Member: member, Kind: domain.DeviceBrowser, Credential: edgeproto.HashToken(token), Label: label}
	if err := e.ids.RegisterDevice(ctx, dev, e.approve); err != nil {
		return nil, "", err
	}
	return dev, token, nil
}

// handleLogout ends the browser's session and closes its WebSockets. The
// device, and its approval, stay: the next sign-in on this browser
// continues on it. A pending session may log out too, so it does not go
// through authorize, but it keeps the same rule for a state change: an
// Origin naming this host.
func (e *Edge) handleLogout(w http.ResponseWriter, r *http.Request) {
	if origin := r.Header.Get("Origin"); origin == "" || !webgate.SameOrigin(origin, r.Host) {
		webgate.WriteError(w, http.StatusForbidden, &protocol.Error{
			Code: protocol.CodeDenied, Message: "log out refused: the request has no Origin header naming this host",
		})
		return
	}
	clearCookie(w, sessionCookie, http.SameSiteStrictMode)
	if c, err := r.Cookie(sessionCookie); err == nil && edgeproto.ValidToken(c.Value) {
		sess, err := e.ids.GetBrowserSessionByCredential(r.Context(), edgeproto.HashToken(c.Value))
		if err == nil {
			err = e.ids.DeleteBrowserSession(r.Context(), sess.ID)
			e.end(sess.ID)
		}
		// Not found is a session already gone.
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			webgate.WriteError(w, http.StatusServiceUnavailable, &protocol.Error{
				Code: protocol.CodeUnavailable, Message: "log out: " + err.Error(),
			})
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func setCookie(w http.ResponseWriter, name, value string, ttl time.Duration, sameSite http.SameSite) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: value, Path: "/", MaxAge: int(ttl / time.Second),
		HttpOnly: true, Secure: true, SameSite: sameSite,
	})
}

func clearCookie(w http.ResponseWriter, name string, sameSite http.SameSite) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: true, SameSite: sameSite,
	})
}

// writePage answers a browser with plain text.
func writePage(w http.ResponseWriter, status int, text string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(text))
}
