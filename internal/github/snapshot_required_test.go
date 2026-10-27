package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// recorded is a GitHub response saved under testdata. check-runs-cli.json,
// check-suites-cli.json and status-cli.json are cli/cli#14825's head commit;
// branch-protected.json is cli/cli's trunk, rules-required.json
// vercel/next.js's canary, and branch-no-checks.json and
// rules-no-checks.json facebook/react's main.
func recorded(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func recordedPR(t *testing.T, branch, rules string) *fakeGitHub {
	f := newFakeGitHub(t)
	standardPR(f)
	f.bodies[checksPage1] = recorded(t, "check-runs-cli.json")
	f.bodies[suitesPage1] = recorded(t, "check-suites-cli.json")
	f.bodies[statusPage1] = recorded(t, "status-cli.json")
	f.bodies[branchPath] = recorded(t, branch)
	f.bodies[rulesPath] = recorded(t, rules)
	return f
}

// editRun changes the recorded check run called name.
func editRun(t *testing.T, body, name string, edit func(run map[string]any)) string {
	t.Helper()
	var page struct {
		TotalCount int              `json:"total_count"`
		CheckRuns  []map[string]any `json:"check_runs"`
	}
	if err := json.Unmarshal([]byte(body), &page); err != nil {
		t.Fatal(err)
	}
	for _, run := range page.CheckRuns {
		if run["name"] == name {
			edit(run)
		}
	}
	data, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// read is a snapshot with its required checks, as a ci watch reads it.
func read(t *testing.T, s *Service, prev *Snapshot) Snapshot {
	t.Helper()
	snap, err := readRequired(s, prev)
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

func readRequired(s *Service, prev *Snapshot) (Snapshot, error) {
	snap, err := s.PullRequestSnapshot(context.Background(), testToken, "acme/app", 7, prev)
	if err == nil {
		err = s.RequiredChecks(context.Background(), testToken, prev, &snap)
	}
	return snap, err
}

func TestPullRequestSnapshotCountsOnlyRequiredChecks(t *testing.T) {
	f := recordedPR(t, "branch-protected.json", "rules-no-checks.json")
	s := snapshotService(t, f)
	first := read(t, s, nil)
	if c := first.Checks(); !c.RequiredOnly || c.Passed != 3 || c.Failed+c.Pending+c.Cancelled != 0 || len(c.Missing) != 0 || !c.Final || c.All == nil || c.All.Passed != 17 {
		t.Fatalf("cli/cli's 17 checks with 3 required: %+v", c)
	}
	second := read(t, s, &first)
	if !slices.Equal(second.Required, first.Required) || !second.Checks().RequiredOnly {
		t.Fatalf("required checks lost on 304: %+v", second.Required)
	}

	f.bodies[checksPage1] = editRun(t, f.bodies[checksPage1], "build (windows-latest)", func(run map[string]any) { run["conclusion"] = "cancelled" })
	if c := read(t, s, nil).Checks(); c.Cancelled != 1 || c.Failed != 0 || c.Passed != 2 || !c.Final || !slices.Equal(c.CancelledNames, []string{"build (windows-latest)"}) {
		t.Fatalf("cancelled required check: %+v", c)
	}

	f.bodies[checksPage1] = editRun(t, f.bodies[checksPage1], "build (macos-latest)", func(run map[string]any) { run["name"] = "renamed" })
	c := read(t, s, nil).Checks()
	if c.Final || !slices.Equal(c.Missing, []string{"build (macos-latest)"}) {
		t.Fatalf("required check never reported: %+v", c)
	}
	f.bodies[checksPage1] = editRun(t, f.bodies[checksPage1], "label-external / label_issues", func(run map[string]any) { run["conclusion"] = "failure" })
	if c := read(t, s, nil).Checks(); c.Failed != 0 || c.All.Failed != 1 || !slices.Equal(c.All.FailedNames, []string{"label-external / label_issues"}) {
		t.Fatalf("a failed check the branch does not require counted: %+v", c)
	}
}

func TestPullRequestSnapshotReadsRulesetRequiredChecks(t *testing.T) {
	f := recordedPR(t, "branch-no-checks.json", "rules-required.json")
	f.bodies[checksPage1] = checkRunsBody(
		strings.Replace(checkRun(1, "thank you, next", "completed", "success"), `"app":{"name":"GitHub Actions"}`, `"app":{"id":15368,"name":"GitHub Actions"}`, 1),
		strings.Replace(checkRun(2, "thank you, build", "completed", "failure"), `"app":{"name":"GitHub Actions"}`, `"app":{"id":999,"name":"Imposter"}`, 1),
		checkRun(3, "lint", "completed", "failure"))
	f.bodies[suitesPage1] = checkSuitesBody()
	c := read(t, snapshotService(t, f), nil).Checks()
	if !c.RequiredOnly || c.Passed != 1 || c.Failed != 0 || c.Final ||
		!slices.Equal(c.Missing, []string{"Potentially publish release", "thank you, build"}) {
		t.Fatalf("next.js ruleset checks, one reported by another app: %+v", c)
	}
}

func TestPullRequestSnapshotCountsEveryCheckWithoutRequirements(t *testing.T) {
	f := recordedPR(t, "branch-no-checks.json", "rules-no-checks.json")
	if c := read(t, snapshotService(t, f), nil).Checks(); c.RequiredOnly || c.Passed != 17 || !c.Final {
		t.Fatalf("branch requiring nothing: %+v", c)
	}
	f.bodies[branchPath] = `{"name":"main","protected":true}`
	delete(f.bodies, rulesPath)
	snap := read(t, snapshotService(t, f), nil)
	if c := snap.Checks(); c.RequiredOnly || c.Passed != 17 || !strings.Contains(snap.RequiredUnreadable, "protected but not its protection") ||
		!strings.Contains(snap.RequiredUnreadable, "HTTP 404") {
		t.Fatalf("neither source readable: %+v, unreadable %q", c, snap.RequiredUnreadable)
	}
}

// refusing serves f, except path, which it answers with status and
// message, and counts.
func refusing(t *testing.T, f *fakeGitHub, path string, status int, message string, asked *int) *Service {
	s, _ := newTestService(t, nativeCredentials(), func(r *http.Request) (*http.Response, error) {
		if r.URL.RequestURI() == path {
			*asked++
			response := jsonResponse(status, `{"message":"`+message+`"}`)
			if strings.Contains(message, "rate limit") {
				response.Header.Set("X-RateLimit-Remaining", "0")
			}
			return response, nil
		}
		return f.serve(r)
	})
	return s
}

func TestPullRequestSnapshotKeepsRequirementsOfTheReadableSource(t *testing.T) {
	f := recordedPR(t, "branch-protected.json", "rules-required.json")
	first := read(t, snapshotService(t, f), nil)
	if c := first.Checks(); !c.RequiredOnly || !slices.Contains(c.Missing, "thank you, next") {
		t.Fatalf("both sources: %+v", c)
	}
	asked := 0
	s := refusing(t, f, rulesPath, http.StatusForbidden, "Resource not accessible by personal access token", &asked)
	fresh := read(t, s, nil)
	if c := fresh.Checks(); !c.RequiredOnly || c.Passed != 3 || len(c.Missing) != 0 || !c.Final || !strings.Contains(fresh.RequiredUnreadable, "Resource not accessible") {
		t.Fatalf("rulesets refused on a first read: %+v, unreadable %q", c, fresh.RequiredUnreadable)
	}
	kept := read(t, s, &first)
	if c := kept.Checks(); !c.RequiredOnly || !slices.Contains(c.Missing, "thank you, next") || kept.Checks().Key != first.Checks().Key {
		t.Fatalf("rulesets refused after a read dropped their requirements: %+v", c)
	}
	again := read(t, s, &kept)
	if asked != 2 || !slices.Equal(again.Required, kept.Required) || again.RequiredUnreadable != kept.RequiredUnreadable {
		t.Fatalf("refused rulesets asked %d times in two reads and one cached read, required %+v", asked, again.Required)
	}
	for i := range again.Pages {
		if again.Pages[i].Refused != "" {
			again.Pages[i].RefusedAt = again.Pages[i].RefusedAt.Add(-requiredRetry)
		}
	}
	read(t, s, &again)
	if asked != 3 {
		t.Fatalf("refused rulesets asked %d times after requiredRetry, want 3", asked)
	}

	_, err := readRequired(refusing(t, f, branchPath, http.StatusForbidden, "API rate limit exceeded", &asked), nil)
	var rate *RateLimitError
	if !errors.As(err, &rate) {
		t.Fatalf("rate limit on branch protection = %v, want a RateLimitError", err)
	}
}

func TestPullRequestSnapshotMatchesRequiredChecksByContextAndApp(t *testing.T) {
	f := newFakeGitHub(t)
	standardPR(f)
	f.bodies[checksPage1] = checkRunsBody(
		strings.Replace(checkRun(1, "build", "completed", "success"), `"app":{"name":"GitHub Actions"}`, `"app":{"id":15368,"name":"GitHub Actions"}`, 1),
		strings.Replace(checkRun(2, "deploy", "completed", "success"), `"app":{"name":"GitHub Actions"}`, `"app":{"id":15368,"name":"GitHub Actions"}`, 1))
	f.bodies[branchPath] = `{"name":"main","protected":true,"protection":{"enabled":true,"required_status_checks":{"enforcement_level":"everyone","checks":[` +
		`{"context":"build","app_id":15368},{"context":"build","app_id":999},{"context":"deploy","app_id":-1}]}}}`
	c := read(t, snapshotService(t, f), nil).Checks()
	if !c.RequiredOnly || c.Passed != 2 || c.Final || !slices.Equal(c.Missing, []string{"build"}) {
		t.Fatalf("build required from two apps, one reported; deploy from any app: %+v", c)
	}
}

func TestPullRequestSnapshotKeyIgnoresRequirements(t *testing.T) {
	f := recordedPR(t, "branch-protected.json", "rules-no-checks.json")
	s := snapshotService(t, f)
	unread, err := s.PullRequestSnapshot(context.Background(), testToken, "acme/app", 7, nil)
	if err != nil {
		t.Fatal(err)
	}
	required := read(t, s, nil)
	if a, b := unread.Checks(), required.Checks(); a.RequiredOnly || !b.RequiredOnly || a.Key != b.Key {
		t.Fatalf("key changed with the requirements: %s %v, %s %v", a.Key, a.RequiredOnly, b.Key, b.RequiredOnly)
	}
}

func TestPullRequestSnapshotWithoutChecksIsNotFinal(t *testing.T) {
	f := newFakeGitHub(t)
	standardPR(f)
	f.bodies[checksPage1] = `{"total_count":0,"check_runs":[]}`
	f.bodies[suitesPage1] = `{"total_count":0,"check_suites":[]}`
	f.bodies[statusPage1] = `{"state":"pending","statuses":[],"total_count":0}`
	if c := read(t, snapshotService(t, f), nil).Checks(); c.Final || c.Passed+c.Failed+c.Pending+c.Cancelled != 0 {
		t.Fatalf("no checks: %+v", c)
	}
}

func TestPullRequestSnapshotWaitsForRunsItsSuiteCounts(t *testing.T) {
	f := newFakeGitHub(t)
	standardPR(f)
	f.bodies[suitesPage1] = checkSuitesBody(`{"id":9,"status":"completed","conclusion":"failure","latest_check_runs_count":2,"app":{"name":"GitHub Actions","slug":"github-actions"}}`)
	if c := read(t, snapshotService(t, f), nil).Checks(); c.Final || c.Pending != 1 {
		t.Fatalf("completed suite with a job the runs read missed: %+v", c)
	}
}

func TestChecksWithoutSuitesDropsAQueuedSuiteWithoutRuns(t *testing.T) {
	f := newFakeGitHub(t)
	standardPR(f)
	f.bodies[suitesPage1] = checkSuitesBody(
		`{"id":9,"status":"completed","conclusion":"success","latest_check_runs_count":1,"app":{"name":"GitHub Actions","slug":"github-actions"}}`,
		checkSuite(10, "queued", "github-actions", 0),
		`{"id":11,"status":"in_progress","conclusion":null,"latest_check_runs_count":1,"app":{"name":"GitHub Actions","slug":"github-actions"}}`)
	c := read(t, snapshotService(t, f), nil).Checks()
	if c.Pending != 2 || c.Suites != 2 || c.EmptySuites != 1 || c.Final {
		t.Fatalf("queued suite without runs and in-progress suite whose run was not read: %+v", c)
	}
	if empty := c.WithoutSuites(true); empty.Pending != 1 || empty.Suites != 1 || empty.Final {
		t.Fatalf("without the empty suite: %+v", empty)
	}
	if all := c.WithoutSuites(false); all.Pending != 0 || all.Passed != 1 || !all.Final {
		t.Fatalf("without every waiting suite: %+v", all)
	}
}

func TestPullRequestSnapshotTellsPullRequestErrorsApart(t *testing.T) {
	for _, tc := range []struct {
		path         string
		status       int
		sso          string
		pull, ssoErr bool
	}{
		{path: pullPath, status: http.StatusNotFound, pull: true},
		{path: pullPath, status: http.StatusMovedPermanently, pull: true},
		{path: checksPage1, status: http.StatusForbidden},
		{path: pullPath, status: http.StatusForbidden, sso: "required; url=https://github.com/orgs/acme/sso?authorization_request=x", pull: true, ssoErr: true},
		{path: statusPage1, status: http.StatusForbidden, sso: "required; url=https://github.com/orgs/acme/sso?authorization_request=x", ssoErr: true},
	} {
		f := newFakeGitHub(t)
		standardPR(f)
		s, _ := newTestService(t, nativeCredentials(), func(r *http.Request) (*http.Response, error) {
			if r.URL.RequestURI() == tc.path {
				response := jsonResponse(tc.status, `{"message":"Resource protected by organization SAML enforcement. You must grant your Personal Access token access to this organization."}`)
				if tc.sso != "" {
					response.Header.Set("X-GitHub-SSO", tc.sso)
				}
				return response, nil
			}
			return f.serve(r)
		})
		_, err := s.PullRequestSnapshot(context.Background(), testToken, "acme/app", 7, nil)
		var pull *PullRequestError
		var api *APIError
		if !errors.As(err, &api) || api.Status != tc.status || errors.As(err, &pull) != tc.pull || errors.Is(err, ErrSSO) != tc.ssoErr ||
			tc.ssoErr && (!strings.Contains(err.Error(), "SAML single sign-on") || strings.Contains(err.Error(), "authorization_request")) {
			t.Fatalf("HTTP %d on %s, sso %q: %v", tc.status, tc.path, tc.sso, err)
		}
	}
}

func TestPullRequestSnapshotNotFound(t *testing.T) {
	s, _ := newTestService(t, nativeCredentials(), func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusNotFound, `{"message":"Not Found","documentation_url":"https://docs.github.com/rest/pulls/pulls#get-a-pull-request","status":"404"}`), nil
	})
	_, err := s.PullRequestSnapshot(context.Background(), testToken, "acme/app", 7, nil)
	if !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "HTTP 404 Not Found") {
		t.Fatalf("recorded 404 = %v", err)
	}
}
