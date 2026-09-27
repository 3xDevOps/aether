package edge

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/oauth2"

	"github.com/3xDevOps/Aether/internal/edge/edgestore"
	"github.com/3xDevOps/Aether/internal/edgeproto"
)

const (
	providerTimeout     = 15 * time.Second
	maxProviderResponse = 1 << 20
	signinTTL           = 10 * time.Minute
	maxNameLength       = 256
	maxEmailLength      = 320
)

type provider struct {
	id    string
	title string
	conf  oauth2.Config
	api   string
}

func withDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func githubProvider(app OAuthApp, origin string) *provider {
	return &provider{
		id:    edgeproto.ProviderGitHub,
		title: "GitHub",
		api:   strings.TrimSuffix(withDefault(app.APIURL, "https://api.github.com"), "/"),
		conf: oauth2.Config{
			ClientID:     app.ClientID,
			ClientSecret: app.ClientSecret,
			Endpoint: oauth2.Endpoint{
				AuthURL:  withDefault(app.AuthURL, "https://github.com/login/oauth/authorize"),
				TokenURL: withDefault(app.TokenURL, "https://github.com/login/oauth/access_token"),
			},
			RedirectURL: origin + "/signin/" + edgeproto.ProviderGitHub + "/callback",
			Scopes:      []string{"user:email"},
		},
	}
}

func googleProvider(app OAuthApp, origin string) *provider {
	return &provider{
		id:    edgeproto.ProviderGoogle,
		title: "Google",
		api:   strings.TrimSuffix(withDefault(app.APIURL, "https://openidconnect.googleapis.com"), "/"),
		conf: oauth2.Config{
			ClientID:     app.ClientID,
			ClientSecret: app.ClientSecret,
			Endpoint: oauth2.Endpoint{
				AuthURL:  withDefault(app.AuthURL, "https://accounts.google.com/o/oauth2/v2/auth"),
				TokenURL: withDefault(app.TokenURL, "https://oauth2.googleapis.com/token"),
			},
			RedirectURL: origin + "/signin/" + edgeproto.ProviderGoogle + "/callback",
			Scopes:      []string{"openid", "email", "profile"},
		},
	}
}

func (s *Service) provider(id string) *provider {
	for _, p := range s.providers {
		if p.id == id {
			return p
		}
	}
	return nil
}

// signinState is what the sign-in cookie carries between the redirect to
// the provider and the provider's callback.
type signinState struct {
	Provider string `json:"p"`
	State    string `json:"s"`
	Verifier string `json:"v"`
	Next     string `json:"n"`
}

func (s *Service) signinPage(w http.ResponseWriter, r *http.Request) {
	next := localPath(r.URL.Query().Get("next"))
	_, ok, err := s.visitor(r)
	if err != nil {
		s.fail(w, r, nil, err)
		return
	}
	if ok {
		http.Redirect(w, r, next, http.StatusSeeOther)
		return
	}
	type link struct{ Title, URL string }
	var links []link
	for _, p := range s.providers {
		links = append(links, link{p.title, "/signin/" + p.id + "?" + url.Values{"next": {next}}.Encode()})
	}
	s.render(w, http.StatusOK, "signin", s.view(nil, "Sign in", links))
}

func (s *Service) signinStart(w http.ResponseWriter, r *http.Request) {
	p := s.provider(r.PathValue("provider"))
	if p == nil {
		s.fail(w, r, nil, pageErr(http.StatusNotFound, "sign-in with %q is not offered on this edge", r.PathValue("provider")))
		return
	}
	if !s.signinLimit.allow(addrKeys(r)...) {
		s.fail(w, r, nil, edgeproto.RefusalTooMany)
		return
	}
	st := signinState{
		Provider: p.id,
		State:    edgeproto.NewToken(),
		Verifier: edgeproto.NewVerifier(),
		Next:     localPath(r.URL.Query().Get("next")),
	}
	raw, err := json.Marshal(st)
	if err != nil {
		s.fail(w, r, nil, fmt.Errorf("encode sign-in state: %w", err))
		return
	}
	setCookie(w, signinCookie, base64.RawURLEncoding.EncodeToString(raw), int(signinTTL.Seconds()))
	http.Redirect(w, r, p.conf.AuthCodeURL(st.State, oauth2.S256ChallengeOption(st.Verifier)), http.StatusFound)
}

func (s *Service) signinCallback(w http.ResponseWriter, r *http.Request) {
	p := s.provider(r.PathValue("provider"))
	if p == nil {
		s.fail(w, r, nil, pageErr(http.StatusNotFound, "sign-in with %q is not offered on this edge", r.PathValue("provider")))
		return
	}
	if !s.signinLimit.allow(addrKeys(r)...) {
		s.fail(w, r, nil, edgeproto.RefusalTooMany)
		return
	}
	st, err := readSigninState(r)
	setCookie(w, signinCookie, "", -1)
	if err != nil {
		s.fail(w, r, nil, err)
		return
	}
	q := r.URL.Query()
	// The state is checked first, so only the provider answering this
	// browser's own sign-in can put its error text on an edge page.
	if st.Provider != p.id || !edgeproto.ValidToken(st.State) ||
		subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(st.State)) != 1 {
		s.fail(w, r, nil, pageErr(http.StatusBadRequest,
			"the sign-in response does not belong to the sign-in this browser started; start again"))
		return
	}
	if e := q.Get("error"); e != "" {
		s.fail(w, r, nil, pageErr(http.StatusForbidden, "%s sign-in failed: %s %s", p.title,
			cleanName(e), cleanName(q.Get("error_description"))))
		return
	}
	ctx, cancel := context.WithTimeout(context.WithValue(r.Context(), oauth2.HTTPClient, s.client), providerTimeout)
	defer cancel()
	tok, err := p.conf.Exchange(ctx, q.Get("code"), oauth2.VerifierOption(st.Verifier))
	if err != nil {
		s.fail(w, r, nil, pageErr(http.StatusBadGateway, "%s sign-in: exchange code: %v", p.title, err))
		return
	}
	account, err := s.fetchAccount(ctx, p, tok.AccessToken)
	if err != nil {
		// An account the edge refuses keeps its 403: only a provider
		// that failed to answer is a 502, and logged.
		status := http.StatusBadGateway
		var refused *statusError
		if errors.As(err, &refused) {
			status = refused.status
		}
		s.fail(w, r, nil, pageErr(status, "%s sign-in: %v", p.title, err))
		return
	}
	if err := s.startSession(w, r, account); err != nil {
		s.fail(w, r, nil, err)
		return
	}
	http.Redirect(w, r, localPath(st.Next), http.StatusSeeOther)
}

func readSigninState(r *http.Request) (signinState, error) {
	c, err := r.Cookie(signinCookie)
	if err != nil {
		return signinState{}, pageErr(http.StatusBadRequest,
			"this browser has no sign-in in progress (it expires after %s); start again", signinTTL)
	}
	var st signinState
	raw, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err == nil {
		err = json.Unmarshal(raw, &st)
	}
	if err != nil {
		return signinState{}, pageErr(http.StatusBadRequest, "read sign-in cookie: %v", err)
	}
	return st, nil
}

// startSession signs the browser in as account, replacing any session it
// had.
func (s *Service) startSession(w http.ResponseWriter, r *http.Request, account edgeproto.Account) error {
	ctx := r.Context()
	now := s.now()
	if old, ok, err := s.visitor(r); err == nil && ok {
		if err := s.store.DeleteSession(ctx, old.ID); err != nil {
			return err
		}
	}
	accountID, err := s.store.SignIn(ctx, account, now)
	if errors.Is(err, edgestore.ErrAccountBlocked) {
		return edgeproto.RefusalAccountBlocked
	}
	if err != nil {
		return err
	}
	token := edgeproto.NewToken()
	if err := s.store.CreateSession(ctx, edgeproto.NewConnID(), edgeproto.HashToken(token), accountID,
		now, now.Add(edgeproto.SessionIdle)); err != nil {
		return err
	}
	setCookie(w, sessionCookie, token, int(edgeproto.SessionIdle.Seconds()))
	return nil
}

func (s *Service) fetchAccount(ctx context.Context, p *provider, accessToken string) (edgeproto.Account, error) {
	var a edgeproto.Account
	var err error
	switch p.id {
	case edgeproto.ProviderGitHub:
		a, err = s.githubAccount(ctx, p.api, accessToken)
	case edgeproto.ProviderGoogle:
		a, err = s.googleAccount(ctx, p.api, accessToken)
	}
	if err != nil {
		return edgeproto.Account{}, err
	}
	if err := a.Validate(); err != nil {
		return edgeproto.Account{}, pageErr(http.StatusForbidden, "the provider returned an account this edge cannot use: %v", err)
	}
	return a, nil
}

// githubAccount reads the GitHub user and keeps only a primary, verified
// email.
func (s *Service) githubAccount(ctx context.Context, api, accessToken string) (edgeproto.Account, error) {
	var user struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
		Name  string `json:"name"`
	}
	if err := s.getJSON(ctx, api+"/user", accessToken, &user); err != nil {
		return edgeproto.Account{}, err
	}
	if user.ID <= 0 {
		return edgeproto.Account{}, pageErr(http.StatusForbidden, "GitHub user id %d is not positive", user.ID)
	}
	if !validGitHubLogin(user.Login) {
		return edgeproto.Account{}, pageErr(http.StatusForbidden, "GitHub login %q is not a valid login", user.Login)
	}
	var emails []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	if err := s.getJSON(ctx, api+"/user/emails", accessToken, &emails); err != nil {
		return edgeproto.Account{}, err
	}
	a := edgeproto.Account{
		Provider: edgeproto.ProviderGitHub,
		Subject:  strconv.FormatInt(user.ID, 10),
		Login:    user.Login,
		Name:     cleanName(user.Name),
	}
	for _, e := range emails {
		if e.Primary && e.Verified {
			a.Email = cleanEmail(e.Email)
		}
	}
	return a, nil
}

// googleAccount reads Google's OpenID userinfo and keeps the email only
// when email_verified is the JSON value true.
func (s *Service) googleAccount(ctx context.Context, api, accessToken string) (edgeproto.Account, error) {
	var info struct {
		Sub           string          `json:"sub"`
		Email         string          `json:"email"`
		EmailVerified json.RawMessage `json:"email_verified"`
		Name          string          `json:"name"`
	}
	if err := s.getJSON(ctx, api+"/v1/userinfo", accessToken, &info); err != nil {
		return edgeproto.Account{}, err
	}
	a := edgeproto.Account{Provider: edgeproto.ProviderGoogle, Subject: info.Sub, Name: cleanName(info.Name)}
	if bytes.Equal(bytes.TrimSpace(info.EmailVerified), []byte("true")) {
		a.Email = cleanEmail(info.Email)
	}
	return a, nil
}

func (s *Service) getJSON(ctx context.Context, rawURL, accessToken string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", rawURL, err)
	}
	defer resp.Body.Close() //nolint:errcheck // read-only
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxProviderResponse+1))
	if err != nil {
		return fmt.Errorf("GET %s: %w", rawURL, err)
	}
	if len(body) > maxProviderResponse {
		return fmt.Errorf("GET %s: response exceeds %d bytes", rawURL, maxProviderResponse)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s: %s", rawURL, resp.Status, cleanName(string(body)))
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("GET %s: decode: %w", rawURL, err)
	}
	return nil
}

// validGitHubLogin accepts GitHub's login alphabet: 1 to 39 ASCII letters,
// digits and hyphens.
func validGitHubLogin(login string) bool {
	if login == "" || len(login) > 39 {
		return false
	}
	for i := 0; i < len(login); i++ {
		c := login[i]
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

// cleanName drops control characters and invalid UTF-8 from provider text
// and cuts it to maxNameLength bytes on a character boundary.
func cleanName(s string) string {
	s = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, "")))
	for len(s) > maxNameLength {
		_, size := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-size]
	}
	return s
}

// cleanEmail returns email, or "" when it is not something the edge can
// store and match.
func cleanEmail(email string) string {
	if len(email) > maxEmailLength || !strings.Contains(email, "@") || cleanName(email) != email {
		return ""
	}
	return email
}
