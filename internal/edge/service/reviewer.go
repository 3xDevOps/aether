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

const PathReviewerSignin = "/connectors/reviewer"

// The reviewer sign-in locks for reviewerLockout after
// reviewerMaxFailures wrong passwords in a row for its login, from any
// address. A wrong login does not count, so only someone who holds the
// login can lock it. At most reviewerMaxChecks password checks run at
// once, from any address, so bcrypt cannot take the edge's CPU.
const (
	reviewerMaxFailures = 10
	reviewerLockout     = 15 * time.Minute
	reviewerMaxChecks   = 4
)

type ReviewerAccount struct {
	Login        string `json:"login"`
	PasswordHash string `json:"password_hash"`
	ServerID     string `json:"server_id"`
}

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

func HashReviewerPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("edge: hash reviewer password: %w", err)
	}
	return string(hash), nil
}

// A check reserves its failure before it runs, so checks at once cannot
// pass reviewerMaxFailures.
type reviewerLock struct {
	mu       sync.Mutex
	failures int
	checking int
	until    time.Time
}

func (l *reviewerLock) locked(now time.Time) time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Before(l.until) {
		return l.until
	}
	return time.Time{}
}

func (l *reviewerLock) reserve(now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Before(l.until) || l.failures+l.checking >= reviewerMaxFailures {
		return false
	}
	l.checking++
	return true
}

func (l *reviewerLock) finish(now time.Time, ok bool) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.checking--
	if ok {
		l.failures = 0
		return 0
	}
	l.failures++
	n := l.failures
	if n >= reviewerMaxFailures {
		l.failures, l.until = 0, now.Add(reviewerLockout)
	}
	return n
}

type reviewerData struct {
	Fields []field
}

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

// reviewerSignin starts no session: the reviewer account reaches no other
// edge page.
func (s *Service) reviewerSignin(w http.ResponseWriter, r *http.Request) {
	ip := clientAddr(r).String()
	r.Body = http.MaxBytesReader(w, r.Body, edgeproto.MaxRequestBodySize)
	if err := r.ParseForm(); err != nil {
		slog.Warn("edge: reviewer sign-in refused: malformed form", "client_ip", ip, "err", err)
		s.fail(w, r, nil, pageErr(http.StatusBadRequest, "read form: %v", err))
		return
	}
	if !s.signinLimit.allow(addrKeys(r)...) {
		slog.Warn("edge: reviewer sign-in refused: rate limited", "client_ip", ip)
		s.fail(w, r, nil, edgeproto.RefusalTooMany)
		return
	}
	now := s.now()
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
		slog.Warn("edge: reviewer sign-in refused: malformed authorization request", "client_ip", ip, "err", err)
		s.fail(w, r, nil, err)
		return
	}
	select {
	case s.reviewerChecks <- struct{}{}:
	default:
		slog.Warn("edge: reviewer sign-in refused: too many password checks at once", "client_ip", ip)
		s.fail(w, r, nil, pageErr(http.StatusTooManyRequests,
			"%d reviewer sign-ins are being checked already; try again in a few seconds", reviewerMaxChecks))
		return
	}
	loginOK := subtle.ConstantTimeCompare([]byte(login), []byte(s.reviewer.Login)) == 1
	if loginOK && !s.reviewerLock.reserve(now) {
		<-s.reviewerChecks
		slog.Warn("edge: reviewer sign-in refused: too many wrong or unfinished passwords", "client_ip", ip)
		s.fail(w, r, nil, pageErr(http.StatusTooManyRequests,
			"the reviewer sign-in is locked, or has %d wrong or unfinished passwords in a row; try again in a few seconds", reviewerMaxFailures))
		return
	}
	// The hash is checked whatever the login, so a wrong login takes as
	// long as a wrong password.
	passwordOK := bcrypt.CompareHashAndPassword([]byte(s.reviewer.PasswordHash), []byte(password)) == nil
	<-s.reviewerChecks
	failures := 0
	if loginOK {
		failures = s.reviewerLock.finish(now, passwordOK)
	}
	if !loginOK || !passwordOK {
		slog.Warn("edge: reviewer sign-in refused: wrong login or password", "client_ip", ip, "failures", failures)
		s.fail(w, r, nil, pageErr(http.StatusForbidden, "wrong reviewer login or password"))
		return
	}
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
