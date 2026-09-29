package edge

import (
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

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	edgestore "github.com/3xDevOps/Aether/internal/edge/store"
)

const (
	providerTimeout     = 15 * time.Second
	maxProviderResponse = 1 << 20
	signinTTL           = 10 * time.Minute
	maxNameLength       = 256
	maxEmailLength      = 320
)

// signinPath starts a sign-in with GitHub; GitHub returns to
// signinPath/callback, the callback URL of the edge's OAuth app.
const signinPath = "/signin/github"

func withDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// githubConfig is the OAuth configuration of app on the sign-in origin.
func githubConfig(app OAuthApp, origin string) oauth2.Config {
	return oauth2.Config{
		ClientID:     app.ClientID,
		ClientSecret: app.ClientSecret,
		Endpoint: oauth2.Endpoint{
			AuthURL:  withDefault(app.AuthURL, "https://github.com/login/oauth/authorize"),
			TokenURL: withDefault(app.TokenURL, "https://github.com/login/oauth/access_token"),
		},
		RedirectURL: origin + signinPath + "/callback",
		Scopes:      []string{"user:email"},
	}
}

// signinState is what the sign-in cookie carries between the redirect to
// GitHub and GitHub's callback.
type signinState struct {
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
	s.render(w, http.StatusOK, "signin", s.view(nil, "Sign in", signinPath+"?"+url.Values{"next": {next}}.Encode()))
}

func (s *Service) signinStart(w http.ResponseWriter, r *http.Request) {
	if !s.signinLimit.allow(addrKeys(r)...) {
		s.fail(w, r, nil, edgeproto.RefusalTooMany)
		return
	}
	st := signinState{
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
	http.Redirect(w, r, s.oauth.AuthCodeURL(st.State, oauth2.S256ChallengeOption(st.Verifier)), http.StatusFound)
}

func (s *Service) signinCallback(w http.ResponseWriter, r *http.Request) {
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
	// The state is checked first, so only GitHub answering this browser's
	// own sign-in can put its error text on an edge page.
	if !edgeproto.ValidToken(st.State) ||
		subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(st.State)) != 1 {
		s.fail(w, r, nil, pageErr(http.StatusBadRequest,
			"the sign-in response does not belong to the sign-in this browser started; start again"))
		return
	}
	if e := q.Get("error"); e != "" {
		s.fail(w, r, nil, pageErr(http.StatusForbidden, "GitHub sign-in failed: %s %s",
			cleanName(e), cleanName(q.Get("error_description"))))
		return
	}
	ctx, cancel := context.WithTimeout(context.WithValue(r.Context(), oauth2.HTTPClient, s.client), providerTimeout)
	defer cancel()
	tok, err := s.oauth.Exchange(ctx, q.Get("code"), oauth2.VerifierOption(st.Verifier))
	if err != nil {
		s.fail(w, r, nil, pageErr(http.StatusBadGateway, "GitHub sign-in: exchange code: %v", err))
		return
	}
	account, err := s.githubAccount(ctx, tok.AccessToken)
	if err != nil {
		// An account the edge refuses keeps its 403: only GitHub failing
		// to answer is a 502, and logged.
		status := http.StatusBadGateway
		var refused *statusError
		if errors.As(err, &refused) {
			status = refused.status
		}
		s.fail(w, r, nil, pageErr(status, "GitHub sign-in: %v", err))
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

// githubAccount reads the GitHub user and keeps only a primary, verified
// email.
func (s *Service) githubAccount(ctx context.Context, accessToken string) (edgeproto.Account, error) {
	var user struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
		Name  string `json:"name"`
	}
	if err := s.getJSON(ctx, s.githubAPI+"/user", accessToken, &user); err != nil {
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
	if err := s.getJSON(ctx, s.githubAPI+"/user/emails", accessToken, &emails); err != nil {
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
	if err := a.Validate(); err != nil {
		return edgeproto.Account{}, pageErr(http.StatusForbidden, "GitHub returned an account this edge cannot use: %v", err)
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
