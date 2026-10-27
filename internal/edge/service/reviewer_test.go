package edge

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// TestReviewerLockReservesFailures starts many password checks for the
// reviewer login at once: only reviewerMaxFailures start, and once they
// all fail the sign-in is locked.
func TestReviewerLockReservesFailures(t *testing.T) {
	t.Parallel()
	var (
		l        reviewerLock
		now      = time.Now()
		wg       sync.WaitGroup
		mu       sync.Mutex
		reserved int
	)
	for range 5 * reviewerMaxFailures {
		wg.Go(func() {
			if l.reserve(now) {
				mu.Lock()
				reserved++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if reserved != reviewerMaxFailures {
		t.Fatalf("%d checks started at once, want %d", reserved, reviewerMaxFailures)
	}
	for range reserved {
		l.finish(now, false)
	}
	if l.locked(now).IsZero() || l.reserve(now) {
		t.Fatal("the sign-in is not locked after the reserved checks failed")
	}
}

// TestReviewerChecksCap refuses a reviewer sign-in with 429 while
// reviewerMaxChecks password checks are under way, and checks it once
// one ends.
func TestReviewerChecksCap(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	hash, err := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	h.svc.reviewer = &ReviewerAccount{Login: "review", PasswordHash: string(hash), ServerID: testServerID(t)}
	signin := func() *httptest.ResponseRecorder {
		form := url.Values{"login": {"review"}, "password": {"pw"}, "client_id": {"c"}, "code_challenge": {"x"}, "redirect_uri": {"https://chatgpt.com/cb"}}
		r := httptest.NewRequest(http.MethodPost, testSignin+PathReviewerSignin, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		h.svc.reviewerSignin(w, r)
		return w
	}
	for range reviewerMaxChecks {
		h.svc.reviewerChecks <- struct{}{}
	}
	if w := signin(); w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), "reviewer sign-ins are being checked already") {
		t.Fatalf("sign-in while %d checks run: %d\n%s", reviewerMaxChecks, w.Code, w.Body)
	}
	<-h.svc.reviewerChecks
	if w := signin(); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "no account claimed this server at this edge") {
		t.Fatalf("sign-in once a check ended: %d\n%s", w.Code, w.Body)
	}
}
