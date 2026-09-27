package edge

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strings"

	"github.com/3xDevOps/Aether/internal/edge/edgestore"
	"github.com/3xDevOps/Aether/internal/edgeproto"
)

const (
	sessionCookie = "__Host-aether_edge_session"
	signinCookie  = "__Host-aether_edge_signin"
)

//go:embed templates
var templateFS embed.FS

func parsePages() (*template.Template, error) {
	t, err := template.New("").Funcs(template.FuncMap{
		"fingerprint": keyFingerprint,
	}).ParseFS(templateFS, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("edge: parse templates: %w", err)
	}
	return t, nil
}

func serveCSS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "public, max-age=3600")
	http.ServeFileFS(w, r, templateFS, "templates/edge.css")
}

// pageHeaders sets the headers every page carries. Forms may redirect to
// a server's callback or to the app, so form-action names both.
func (s *Service) pageHeaders(next http.Handler) http.Handler {
	csp := "default-src 'none'; style-src 'self'; img-src 'self'; base-uri 'none'; frame-ancestors 'none'; " +
		"form-action 'self' https://*." + s.serverDomain + " aether:"
	hsts := strings.HasPrefix(s.origin, "https://")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		if hsts {
			h.Set("Strict-Transport-Security", "max-age=63072000")
		}
		next.ServeHTTP(w, r)
	})
}

func setCookie(w http.ResponseWriter, name, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: value, Path: "/", MaxAge: maxAge,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
}

// visitor is a signed-in browser.
type visitor struct {
	edgestore.Session
	csrf string
}

// csrfToken derives a session's CSRF token from its cookie token, which
// only the browser holding the cookie knows.
func csrfToken(sessionToken string) string {
	mac := hmac.New(sha256.New, []byte(sessionToken))
	mac.Write([]byte("aether-edge-csrf"))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// visitor returns the browser's session, or ok false when it has none.
func (s *Service) visitor(r *http.Request) (visitor, bool, error) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || !edgeproto.ValidToken(c.Value) {
		return visitor{}, false, nil
	}
	now := s.now()
	sess, err := s.store.UseSession(r.Context(), edgeproto.HashToken(c.Value), now, now.Add(edgeproto.SessionIdle))
	if errors.Is(err, edgestore.ErrNotFound) {
		return visitor{}, false, nil
	}
	if err != nil {
		return visitor{}, false, err
	}
	return visitor{Session: sess, csrf: csrfToken(c.Value)}, true, nil
}

// signedIn serves h to a signed-in browser and sends anyone else to sign
// in. A POST must carry the session's CSRF token.
func (s *Service) signedIn(h func(http.ResponseWriter, *http.Request, visitor) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v, ok, err := s.visitor(r)
		if err != nil {
			s.fail(w, nil, err)
			return
		}
		if !ok {
			next := "/servers"
			if r.Method == http.MethodGet {
				next = r.URL.RequestURI()
			}
			http.Redirect(w, r, "/signin?"+url.Values{"next": {next}}.Encode(), http.StatusSeeOther)
			return
		}
		if r.Method == http.MethodPost {
			r.Body = http.MaxBytesReader(w, r.Body, edgeproto.MaxRequestBodySize)
			if err := r.ParseForm(); err != nil {
				s.fail(w, &v, pageErr(http.StatusBadRequest, "read form: %v", err))
				return
			}
			if !hmac.Equal([]byte(r.PostForm.Get("csrf")), []byte(v.csrf)) {
				s.fail(w, &v, pageErr(http.StatusForbidden,
					"this form did not come from this edge page or your session changed; reload the page and try again"))
				return
			}
		}
		if err := h(w, r, v); err != nil {
			s.fail(w, &v, err)
		}
	}
}

// statusError is a page or API failure with its HTTP status.
type statusError struct {
	status int
	msg    string
}

func (e *statusError) Error() string { return e.msg }

func pageErr(status int, format string, args ...any) error {
	return &statusError{status: status, msg: fmt.Sprintf(format, args...)}
}

func errorStatus(err error) int {
	var se *statusError
	var ref edgeproto.Refusal
	switch {
	case errors.As(err, &se):
		return se.status
	case errors.As(err, &ref):
		return ref.Status()
	}
	return http.StatusInternalServerError
}

// view is what every page template receives.
type view struct {
	Title   string
	Account *edgeproto.Account
	CSRF    string
	Error   string
	Data    any
}

func (s *Service) view(v *visitor, title string, data any) view {
	out := view{Title: title, Data: data}
	if v != nil {
		out.Account, out.CSRF = &v.Account, v.csrf
	}
	return out
}

func (s *Service) render(w http.ResponseWriter, status int, name string, data view) {
	var buf bytes.Buffer
	if err := s.pages.ExecuteTemplate(&buf, name, data); err != nil {
		http.Error(w, "edge: render "+name+": "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	w.Write(buf.Bytes()) //nolint:errcheck // the client went away
}

func (s *Service) fail(w http.ResponseWriter, v *visitor, err error) {
	status := errorStatus(err)
	page := s.view(v, http.StatusText(status), nil)
	page.Error = err.Error()
	s.render(w, status, "error", page)
}

// localPath returns next when it is a path on this edge, and "/servers"
// otherwise, so a sign-in never sends the browser elsewhere.
func localPath(next string) string {
	u, err := url.Parse(next)
	if err != nil || next == "" || len(next) > 2048 || next[0] != '/' || strings.HasPrefix(next, "//") ||
		strings.ContainsAny(next, "\\\r\n\t") || u.Scheme != "" || u.Host != "" {
		return "/servers"
	}
	return next
}
