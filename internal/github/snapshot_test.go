package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

const headSHA = "0123456789abcdef0123456789abcdef01234567"

// fakeGitHub answers by path and query. Each body gets an ETag derived from
// its content, and a matching If-None-Match answers 304 like GitHub does.
type fakeGitHub struct {
	t        *testing.T
	bodies   map[string]string
	links    map[string]string
	requests []string
	notMod   int
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	return &fakeGitHub{t: t, bodies: map[string]string{}, links: map[string]string{}}
}

func (f *fakeGitHub) serve(r *http.Request) (*http.Response, error) {
	key := r.URL.RequestURI()
	f.requests = append(f.requests, key)
	if r.Header.Get("Authorization") != "Bearer "+testToken {
		f.t.Fatalf("missing authentication on %s", key)
	}
	body, ok := f.bodies[key]
	if !ok {
		return jsonResponse(http.StatusNotFound, `{"message":"Not Found"}`), nil
	}
	etag := `"` + strconv.Itoa(len(body)) + "-" + strconv.Itoa(int(hash(body))) + `"`
	if r.Header.Get("If-None-Match") == etag {
		f.notMod++
		response := jsonResponse(http.StatusNotModified, "")
		response.Header.Set("ETag", etag)
		return response, nil
	}
	response := jsonResponse(http.StatusOK, body)
	response.Header.Set("ETag", etag)
	if link := f.links[key]; link != "" {
		response.Header.Set("Link", link)
	}
	return response, nil
}

func hash(s string) uint32 {
	var h uint32 = 2166136261
	for i := 0; i < len(s); i++ {
		h = (h ^ uint32(s[i])) * 16777619
	}
	return h
}

func pullBody(state, merged string) string {
	return `{"number":7,"title":"Fix login","html_url":"https://github.com/acme/app/pull/7","state":"` + state + `","merged_at":` + merged +
		`,"head":{"sha":"` + headSHA + `"},"base":{"ref":"main"}}`
}

func checkRun(id int64, name, status, conclusion string) string {
	c := "null"
	if conclusion != "" {
		c = `"` + conclusion + `"`
	}
	return fmt.Sprintf(`{"id":%d,"name":%q,"status":%q,"conclusion":%s,"html_url":"https://github.com/acme/app/runs/%d","started_at":"2026-10-26T10:00:00Z","check_suite":{"id":9},"app":{"name":"GitHub Actions"}}`, id, name, status, c, id)
}

func checkRunsBody(runs ...string) string {
	return `{"total_count":` + strconv.Itoa(len(runs)) + `,"check_runs":[` + strings.Join(runs, ",") + `]}`
}

const (
	pullPath     = "/repos/acme/app/pulls/7"
	commitPath   = "/repos/acme/app/commits/" + headSHA
	checksPage1  = commitPath + "/check-runs?per_page=100&page=1"
	checksPage2  = commitPath + "/check-runs?per_page=100&page=2"
	statusPage1  = commitPath + "/status?per_page=100&page=1"
	reviewsPage1 = pullPath + "/reviews?per_page=100&page=1"
)

func standardPR(f *fakeGitHub) {
	f.bodies[pullPath] = pullBody("open", "null")
	f.bodies[checksPage1] = checkRunsBody(checkRun(1, "build", "completed", "success"))
	f.bodies[statusPage1] = `{"state":"success","statuses":[]}`
	f.bodies[reviewsPage1] = `[]`
}

func snapshotService(t *testing.T, f *fakeGitHub) *Service {
	s, _ := newTestService(t, nativeCredentials(), f.serve)
	return s
}

func TestPullRequestSnapshotPaginatesMixedChecksAndReviews(t *testing.T) {
	f := newFakeGitHub(t)
	standardPR(f)
	page1 := make([]string, 0, 100)
	for i := range 100 {
		page1 = append(page1, checkRun(int64(100+i), "shard-"+strconv.Itoa(i), "completed", "success"))
	}
	f.bodies[checksPage1] = checkRunsBody(page1...)
	f.links[checksPage1] = `<https://api.github.com/repositories/1/commits/` + headSHA + `/check-runs?per_page=100&page=2>; rel="next"`
	f.bodies[checksPage2] = checkRunsBody(checkRun(1, "lint", "completed", "failure"), checkRun(2, "e2e", "in_progress", ""))
	f.bodies[statusPage1] = `{"state":"pending","statuses":[` +
		`{"id":11,"context":"ci/legacy","state":"success","updated_at":"2026-10-26T10:00:00Z"},` +
		`{"id":12,"context":"deploy/preview","state":"pending","updated_at":"2026-10-26T10:00:00Z"},` +
		`{"id":13,"context":"security","state":"error","description":"scanner crashed","updated_at":"2026-10-26T10:00:00Z"}]}`
	f.bodies[reviewsPage1] = `[{"id":5,"state":"APPROVED","html_url":"https://github.com/acme/app/pull/7#pullrequestreview-5","submitted_at":"2026-10-26T11:00:00Z","user":{"login":"mona"}},` +
		`{"id":6,"state":"COMMENTED","html_url":"https://github.com/acme/app/pull/7#pullrequestreview-6","submitted_at":"2026-10-26T11:05:00Z","user":null}]`

	snap, err := snapshotService(t, f).PullRequestSnapshot(context.Background(), testToken, "acme/app", 7, nil)
	if err != nil {
		t.Fatal(err)
	}
	if snap.State != "open" || snap.Title != "Fix login" || snap.HeadSHA != headSHA || snap.BaseRef != "main" || snap.URL != "https://github.com/acme/app/pull/7" {
		t.Fatalf("pull request fields: %+v", snap)
	}
	if len(snap.CheckRuns) != 102 || len(snap.Statuses) != 3 || len(snap.Reviews) != 2 {
		t.Fatalf("lists: runs=%d statuses=%d reviews=%d", len(snap.CheckRuns), len(snap.Statuses), len(snap.Reviews))
	}
	if run := snap.CheckRuns[101]; run.Name != "e2e" || run.Status != "in_progress" || run.Conclusion != "" || run.SuiteID != 9 || run.App != "GitHub Actions" || run.StartedAt == nil {
		t.Fatalf("check run fields: %+v", run)
	}
	if r := snap.Reviews[0]; r.ID != 5 || r.State != "APPROVED" || r.User != "mona" || r.SubmittedAt == nil || snap.Reviews[1].User != "" {
		t.Fatalf("review fields: %+v", snap.Reviews)
	}
	checks := snap.Checks()
	if checks.Passed != 101 || checks.Failed != 2 || checks.Pending != 2 || checks.Final {
		t.Fatalf("summary: %+v", checks)
	}
	if !strings.HasPrefix(checks.Key, headSHA+":") {
		t.Fatalf("key does not carry the head commit: %s", checks.Key)
	}
}

func TestPullRequestSnapshotReusesNotModifiedPages(t *testing.T) {
	f := newFakeGitHub(t)
	standardPR(f)
	s := snapshotService(t, f)
	first, err := s.PullRequestSnapshot(context.Background(), testToken, "acme/app", 7, nil)
	if err != nil {
		t.Fatal(err)
	}
	if f.notMod != 0 || len(first.Pages) != 4 {
		t.Fatalf("first poll: notModified=%d pages=%+v", f.notMod, first.Pages)
	}
	second, err := s.PullRequestSnapshot(context.Background(), testToken, "acme/app", 7, &first)
	if err != nil {
		t.Fatal(err)
	}
	if f.notMod != 4 || second.Title != first.Title || second.HeadSHA != headSHA || len(second.CheckRuns) != 1 || second.CheckRuns[0].ID != 1 {
		t.Fatalf("unchanged poll did not reuse 304 pages: notModified=%d %+v", f.notMod, second)
	}
	if second.Checks() != first.Checks() {
		t.Fatal("reused snapshot summarizes differently")
	}

	f.bodies[reviewsPage1] = `[{"id":8,"state":"CHANGES_REQUESTED","html_url":"https://github.com/acme/app/pull/7#pullrequestreview-8","submitted_at":"2026-10-26T12:00:00Z","user":{"login":"mona"}}]`
	third, err := s.PullRequestSnapshot(context.Background(), testToken, "acme/app", 7, &second)
	if err != nil {
		t.Fatal(err)
	}
	if f.notMod != 7 || len(third.Reviews) != 1 || third.Reviews[0].State != "CHANGES_REQUESTED" || len(third.CheckRuns) != 1 {
		t.Fatalf("changed page not refetched: notModified=%d %+v", f.notMod, third)
	}

	other, err := s.PullRequestSnapshot(context.Background(), testToken, "acme/app", 8, &third)
	if !errors.Is(err, ErrNotFound) || other.Repo != "" {
		t.Fatalf("snapshot of another pull request reused cached pages: %v", err)
	}
}

func TestPullRequestSnapshotMerged(t *testing.T) {
	f := newFakeGitHub(t)
	standardPR(f)
	f.bodies[pullPath] = pullBody("closed", `"2026-10-26T13:00:00Z"`)
	snap, err := snapshotService(t, f).PullRequestSnapshot(context.Background(), testToken, "acme/app", 7, nil)
	if err != nil || snap.State != "merged" || snap.MergedAt == nil {
		t.Fatalf("merged: %+v %v", snap, err)
	}
}

func TestPullRequestSnapshotRerunChangesKey(t *testing.T) {
	f := newFakeGitHub(t)
	standardPR(f)
	s := snapshotService(t, f)
	poll := func() Checks {
		t.Helper()
		snap, err := s.PullRequestSnapshot(context.Background(), testToken, "acme/app", 7, nil)
		if err != nil {
			t.Fatal(err)
		}
		return snap.Checks()
	}

	f.bodies[checksPage1] = checkRunsBody(checkRun(1, "build", "completed", "success"), checkRun(2, "test", "completed", "failure"))
	failed := poll()
	f.bodies[checksPage1] = checkRunsBody(checkRun(1, "build", "completed", "success"), checkRun(3, "test", "queued", ""))
	rerunning := poll()
	f.bodies[checksPage1] = checkRunsBody(checkRun(1, "build", "completed", "success"), checkRun(3, "test", "completed", "success"))
	passed := poll()

	if !failed.Final || failed.Failed != 1 || rerunning.Final || rerunning.Pending != 1 || !passed.Final || passed.Failed != 0 || passed.Passed != 2 {
		t.Fatalf("rerun states: %+v %+v %+v", failed, rerunning, passed)
	}
	if failed.Key == passed.Key {
		t.Fatal("failed attempt and passing rerun share a key")
	}
	if again := poll(); again.Key != passed.Key {
		t.Fatal("key is not stable across identical polls")
	}

	f.bodies[checksPage1] = checkRunsBody()
	if none := poll(); none.Final {
		t.Fatal("a commit without checks reported final")
	}
}

func TestPullRequestSnapshotRateLimitErrors(t *testing.T) {
	now := time.Now()
	reset := now.Add(30 * time.Minute).Truncate(time.Second)
	for _, tc := range []struct {
		name     string
		response func() *http.Response
		after    time.Time
	}{
		{"primary", func() *http.Response {
			r := jsonResponse(http.StatusForbidden, `{"message":"API rate limit exceeded for user ID 1."}`)
			r.Header.Set("X-RateLimit-Remaining", "0")
			r.Header.Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
			return r
		}, reset},
		{"secondary retry-after", func() *http.Response {
			r := jsonResponse(http.StatusForbidden, `{"message":"You have exceeded a secondary rate limit."}`)
			r.Header.Set("Retry-After", "120")
			return r
		}, now.Add(2 * time.Minute)},
		{"secondary bare", func() *http.Response {
			return jsonResponse(http.StatusForbidden, `{"message":"You have exceeded a secondary rate limit."}`)
		}, now.Add(time.Minute)},
		{"429", func() *http.Response { return jsonResponse(http.StatusTooManyRequests, `{}`) }, now.Add(time.Minute)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newTestService(t, nativeCredentials(), func(*http.Request) (*http.Response, error) { return tc.response(), nil })
			_, err := s.PullRequestSnapshot(context.Background(), testToken, "acme/app", 7, nil)
			var limited *RateLimitError
			if !errors.As(err, &limited) {
				t.Fatalf("not a rate limit error: %v", err)
			}
			if d := limited.RetryAt.Sub(tc.after); d < -time.Second || d > 5*time.Second {
				t.Fatalf("retry at %s, want about %s", limited.RetryAt, tc.after)
			}
			if !strings.Contains(err.Error(), "retry after") {
				t.Fatalf("error hides the retry time: %v", err)
			}
		})
	}
}

func TestPullRequestSnapshotTypedErrorsCarryGitHubMessage(t *testing.T) {
	for _, tc := range []struct {
		status  int
		message string
		is      error
	}{
		{http.StatusUnauthorized, "Bad credentials", ErrUnauthorized},
		{http.StatusNotFound, "Not Found", ErrNotFound},
		{http.StatusForbidden, "Resource not accessible by integration", nil},
	} {
		s, _ := newTestService(t, nativeCredentials(), func(*http.Request) (*http.Response, error) {
			return jsonResponse(tc.status, `{"message":"`+tc.message+` `+testToken+`"}`), nil
		})
		_, err := s.PullRequestSnapshot(context.Background(), testToken, "acme/app", 7, nil)
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Status != tc.status || !strings.Contains(err.Error(), tc.message) {
			t.Fatalf("HTTP %d: %v", tc.status, err)
		}
		if tc.is != nil && !errors.Is(err, tc.is) || errors.Is(err, ErrUnauthorized) && tc.is != ErrUnauthorized {
			t.Fatalf("HTTP %d matches the wrong sentinel: %v", tc.status, err)
		}
		if strings.Contains(err.Error(), testToken) {
			t.Fatal("token leaked into the error")
		}
	}
}

func TestPullRequestSnapshotBoundsPagination(t *testing.T) {
	f := newFakeGitHub(t)
	standardPR(f)
	for page := 1; page <= maxPages; page++ {
		key := commitPath + "/check-runs?per_page=100&page=" + strconv.Itoa(page)
		f.bodies[key] = checkRunsBody(checkRun(int64(page), "c", "completed", "success"))
		f.links[key] = `<https://api.github.com/x>; rel="next"`
	}
	_, err := snapshotService(t, f).PullRequestSnapshot(context.Background(), testToken, "acme/app", 7, nil)
	if err == nil || !strings.Contains(err.Error(), "more than 1000 check runs") {
		t.Fatalf("unbounded pagination: %v", err)
	}
}

func TestPullRequestSnapshotRejectsUnsafeInput(t *testing.T) {
	s, _ := newTestService(t, nativeCredentials(), func(r *http.Request) (*http.Response, error) {
		t.Fatalf("unexpected request %s", r.URL.Path)
		return nil, nil
	})
	for _, repo := range []string{"acme", "acme/../x", "acme/app?x=1"} {
		if _, err := s.PullRequestSnapshot(context.Background(), testToken, repo, 7, nil); err == nil {
			t.Fatalf("accepted %q", repo)
		}
	}
	if _, err := s.PullRequestSnapshot(context.Background(), testToken, "acme/app", 0, nil); err == nil {
		t.Fatal("accepted pull request 0")
	}
}
