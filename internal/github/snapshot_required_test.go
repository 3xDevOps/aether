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

func read(t *testing.T, s *Service, prev *Snapshot) Snapshot {
	t.Helper()
	snap, err := s.PullRequestSnapshot(context.Background(), testToken, "acme/app", 7, prev)
	if err != nil {
		t.Fatal(err)
	}
	return snap
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

	refuse := func(path string, response *http.Response) *Service {
		s, _ := newTestService(t, nativeCredentials(), func(r *http.Request) (*http.Response, error) {
			if r.URL.RequestURI() == path {
				return response, nil
			}
			return f.serve(r)
		})
		return s
	}
	f.bodies[branchPath] = recorded(t, "branch-protected.json")
	snap := read(t, refuse(rulesPath, jsonResponse(http.StatusForbidden, `{"message":"Resource not accessible by personal access token"}`)), nil)
	if c := snap.Checks(); c.RequiredOnly || c.Passed != 17 || !strings.Contains(snap.RequiredUnreadable, "Resource not accessible") {
		t.Fatalf("rulesets refused: %+v, unreadable %q", c, snap.RequiredUnreadable)
	}
	snap = read(t, refuse(branchPath, jsonResponse(http.StatusOK, `{"name":"main","protected":true}`)), nil)
	if snap.Checks().RequiredOnly || !strings.Contains(snap.RequiredUnreadable, "protected but not its protection") || slices.ContainsFunc(snap.Pages, func(p Page) bool { return p.Path == branchPath }) {
		t.Fatalf("hidden protection: %q, pages %+v", snap.RequiredUnreadable, snap.Pages)
	}

	limited := jsonResponse(http.StatusForbidden, `{"message":"API rate limit exceeded"}`)
	limited.Header.Set("X-RateLimit-Remaining", "0")
	_, err := refuse(branchPath, limited).PullRequestSnapshot(context.Background(), testToken, "acme/app", 7, nil)
	var rate *RateLimitError
	if !errors.As(err, &rate) {
		t.Fatalf("rate limit on branch protection = %v, want a RateLimitError", err)
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

func TestPullRequestSnapshotNotFound(t *testing.T) {
	s, _ := newTestService(t, nativeCredentials(), func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusNotFound, `{"message":"Not Found","documentation_url":"https://docs.github.com/rest/pulls/pulls#get-a-pull-request","status":"404"}`), nil
	})
	_, err := s.PullRequestSnapshot(context.Background(), testToken, "acme/app", 7, nil)
	if !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "HTTP 404 Not Found") {
		t.Fatalf("recorded 404 = %v", err)
	}
}
