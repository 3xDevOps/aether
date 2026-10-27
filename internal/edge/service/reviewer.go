package edge

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	edgestore "github.com/3xDevOps/Aether/internal/edge/store"
)

// PathReviewerSignin is the reviewer account's sign-in page on the
// sign-in origin. The edge serves it only with a reviewer account file.
const PathReviewerSignin = "/connectors/reviewer"

// The reviewer sign-in locks for reviewerLockout after
// reviewerMaxFailures wrong passwords in a row for its login, from any
// address. A wrong login does not count, so only someone who holds the
// login can lock it.
const (
	reviewerMaxFailures = 10
	reviewerLockout     = 15 * time.Minute
)

// ReviewerAccount is the reviewer account file: the one login and
// bcrypt password hash an app directory's reviewer signs in with, and the
// one server it is offered.
type ReviewerAccount struct {
	Login        string `json:"login"`
	PasswordHash string `json:"password_hash"`
	ServerID     string `json:"server_id"`
}

// LoadReviewerAccount reads and checks the reviewer account file at path.
func LoadReviewerAccount(path string) (*ReviewerAccount, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("edge: reviewer account file: %w", err)
	}
	var a ReviewerAccount
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&a); err != nil {
		return nil, fmt.Errorf("edge: reviewer account file %s: %w", path, err)
	}
	if err = a.Validate(); err != nil {
		return nil, fmt.Errorf("edge: reviewer account file %s: %w", path, err)
	}
	return &a, nil
}

// Validate checks the login, the server id and that the hash is bcrypt.
func (a *ReviewerAccount) Validate() error {
	if !edgeproto.ValidReviewerLogin(a.Login) {
		return fmt.Errorf("login %q is not 1 to 39 lowercase letters, digits and dashes", a.Login)
	}
	if !edgeproto.ValidServerID(a.ServerID) {
		return fmt.Errorf("server_id %q is not a server id", a.ServerID)
	}
	if _, err := bcrypt.Cost([]byte(a.PasswordHash)); err != nil {
		return fmt.Errorf("password_hash is not a bcrypt hash: %w", err)
	}
	return nil
}

// HashReviewerPassword is the bcrypt hash a reviewer account file keeps
// for password.
func HashReviewerPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("edge: hash reviewer password: %w", err)
	}
	return string(hash), nil
}

// reviewerLock counts the reviewer sign-in's failures in a row and when
// it is locked until.
type reviewerLock struct {
	mu       sync.Mutex
	failures int
	until    time.Time
}

// locked returns when the sign-in unlocks, or the zero time.
func (l *reviewerLock) locked(now time.Time) time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Before(l.until) {
		return l.until
	}
	return time.Time{}
}

// fail counts a failure and returns the failures in a row, the lock
// starting once there are reviewerMaxFailures.
func (l *reviewerLock) fail(now time.Time) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.failures++
	n := l.failures
	if n >= reviewerMaxFailures {
		l.failures, l.until = 0, now.Add(reviewerLockout)
	}
	return n
}

func (l *reviewerLock) succeed() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.failures = 0
}

// reviewerData is the reviewer sign-in page: the authorization request it
// posts on, as hidden fields.
type reviewerData struct {
	Fields []field
}

// reviewerPage asks for the reviewer account's login and password for the
// universal connector's authorization request in the query.
func (s *Service) reviewerPage(w http.ResponseWriter, r *http.Request) {
	q, err := chooseRequest(r.URL.Query())
	if err != nil {
		s.fail(w, r, nil, err)
		return
	}
	// A hidden field would come first and win over what the reviewer types.
	q.Del("login")
	q.Del("password")
	s.render(w, http.StatusOK, "reviewer", s.view(nil, "Reviewer sign-in", reviewerData{Fields: requestFields(q)}))
}

// reviewerSignin checks the reviewer account's login and password and
// shows the choose page for the one server the account file names. It
// starts no session: the reviewer account reaches no other edge page.
func (s *Service) reviewerSignin(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, edgeproto.MaxRequestBodySize)
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, nil, pageErr(http.StatusBadRequest, "read form: %v", err))
		return
	}
	if !s.signinLimit.allow(addrKeys(r)...) {
		s.fail(w, r, nil, edgeproto.RefusalTooMany)
		return
	}
	now := s.now()
	ip := clientAddr(r).String()
	if until := s.reviewerLock.locked(now); !until.IsZero() {
		slog.Warn("edge: reviewer sign-in refused: locked", "client_ip", ip, "until", until)
		s.fail(w, r, nil, pageErr(http.StatusTooManyRequests,
			"the reviewer sign-in is locked after %d wrong passwords in a row; try again after %s",
			reviewerMaxFailures, until.UTC().Format(time.RFC3339)))
		return
	}
	form := r.PostForm
	login, password := form.Get("login"), form.Get("password")
	form.Del("login")
	form.Del("password")
	q, err := chooseRequest(form)
	if err != nil {
		s.fail(w, r, nil, err)
		return
	}
	// The hash is checked whatever the login, so a wrong login takes as
	// long as a wrong password.
	loginOK := subtle.ConstantTimeCompare([]byte(login), []byte(s.reviewer.Login)) == 1
	passwordOK := bcrypt.CompareHashAndPassword([]byte(s.reviewer.PasswordHash), []byte(password)) == nil
	if !loginOK || !passwordOK {
		failures := 0
		if loginOK {
			failures = s.reviewerLock.fail(now)
		}
		slog.Warn("edge: reviewer sign-in refused: wrong login or password", "client_ip", ip, "failures", failures)
		s.fail(w, r, nil, pageErr(http.StatusForbidden, "wrong reviewer login or password"))
		return
	}
	s.reviewerLock.succeed()
	slog.Info("edge: reviewer signed in", "login", s.reviewer.Login, "server", s.reviewer.ServerID, "client_ip", ip)
	c := choice{ServerInfo: edgeproto.ServerInfo{ID: s.reviewer.ServerID, Name: s.reviewer.ServerID, Role: "reviewer"}}
	srv, err := s.store.Server(r.Context(), s.reviewer.ServerID)
	switch {
	case errors.Is(err, edgestore.ErrNotFound):
		c.Reason = "no account claimed this server at this edge"
	case err != nil:
		s.fail(w, r, nil, err)
		return
	default:
		c.Name, c.AccessPolicy, c.Kind, c.Online = srv.Name, srv.AccessPolicy, srv.Kind, s.link.Online(srv.ID)
		account := edgeproto.Account{Provider: edgeproto.ProviderReviewer, Subject: s.reviewer.Login, Login: s.reviewer.Login}
		if err = s.offer(&c, account, q, time.Now(), edgeproto.VersionReviewer); err != nil {
			s.fail(w, r, nil, err)
			return
		}
	}
	if c.Reason == "" {
		s.renderChoose(w, nil, q, []choice{c}, nil)
	} else {
		s.renderChoose(w, nil, q, nil, []choice{c})
	}
}
