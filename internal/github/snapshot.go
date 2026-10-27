package github

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	pageSize      = 100
	maxPages      = 10
	reviewBodyMax = 2000
	// requiredRetry is how long a refused read of branch protection or
	// rulesets stands before it is asked again.
	requiredRetry = 10 * time.Minute
)

// Snapshot is one read of a pull request, the check runs and commit statuses
// on its head commit, and its reviews. Pass the previous Snapshot back so
// unchanged endpoints answer 304, which GitHub does not count against the
// rate limit.
type Snapshot struct {
	Repo     string     `json:"repo"`
	Number   int        `json:"number"`
	State    string     `json:"state"`
	MergedAt *time.Time `json:"merged_at,omitempty"`
	ClosedAt *time.Time `json:"closed_at,omitempty"`
	Title    string     `json:"title"`
	URL      string     `json:"html_url"`
	HeadSHA  string     `json:"head_sha"`
	BaseRef  string     `json:"base_ref"`
	// ReviewRequested is whether any user or team review request is open.
	ReviewRequested bool `json:"review_requested"`
	// HeadSince is when the caller first read HeadSHA; PullRequestSnapshot
	// leaves it unset.
	HeadSince time.Time `json:"head_since,omitzero"`

	CheckRuns []CheckRun     `json:"check_runs"`
	Suites    []CheckSuite   `json:"check_suites"`
	Statuses  []CommitStatus `json:"statuses"`
	Reviews   []Review       `json:"reviews"`
	// Workflows is read only when a cancelled check run shares its app and
	// name with a newer run in another suite.
	Workflows []WorkflowRun `json:"workflow_runs,omitempty"`
	// WorkflowsUnreadable is GitHub's answer when that read was refused, for
	// example a fine-grained token without Actions read. A cancelled run then
	// stays counted.
	WorkflowsUnreadable string `json:"workflows_unreadable,omitempty"`
	// Required is the status checks BaseRef's branch protection and
	// rulesets require, read by RequiredChecks; RequiredRead is whether they
	// were. RequiredUnreadable is GitHub's answer when either source was
	// refused; that source's last read requirements then stand, and every
	// check counts when none are known.
	Required           []RequiredCheck `json:"required_checks,omitempty"`
	RequiredRead       bool            `json:"required_read,omitempty"`
	RequiredUnreadable string          `json:"required_unreadable,omitempty"`

	Pages []Page `json:"pages"`
}

// Page is one conditional GET of a snapshot. Count is how many items of the
// matching Snapshot list the page contributed, in request order; Full is
// whether GitHub returned a whole page before any items were dropped.
// Refused is GitHub's answer when it refused the page at RefusedAt.
type Page struct {
	Path      string    `json:"path"`
	ETag      string    `json:"etag"`
	Count     int       `json:"count"`
	Full      bool      `json:"full,omitempty"`
	Refused   string    `json:"refused,omitempty"`
	RefusedAt time.Time `json:"refused_at,omitzero"`
}

type CheckRun struct {
	ID          int64      `json:"id"`
	Name        string     `json:"name"`
	Status      string     `json:"status"`
	Conclusion  string     `json:"conclusion,omitempty"`
	SuiteID     int64      `json:"check_suite_id"`
	App         string     `json:"app"`
	AppID       int64      `json:"app_id,omitempty"`
	URL         string     `json:"html_url"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

// RequiredCheck is a status check context the base branch requires. AppID,
// when positive, is the app that must report it. Source is "branch" for classic
// branch protection or "rules" for rulesets.
type RequiredCheck struct {
	Context string `json:"context"`
	AppID   int64  `json:"app_id,omitempty"`
	Source  string `json:"source"`
}

// CheckSuite is one app's suite on the head commit. GitHub creates a suite
// before the check runs of jobs gated by needs: or of a queued workflow.
type CheckSuite struct {
	ID         int64  `json:"id"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion,omitempty"`
	App        string `json:"app"`
	AppSlug    string `json:"app_slug"`
	Runs       int    `json:"latest_check_runs_count"`
}

// WorkflowRun ties a GitHub Actions check suite to the workflow it ran.
type WorkflowRun struct {
	ID         int64 `json:"id"`
	WorkflowID int64 `json:"workflow_id"`
	SuiteID    int64 `json:"check_suite_id"`
}

// CommitStatus is the latest status for one context.
type CommitStatus struct {
	ID          int64     `json:"id"`
	Context     string    `json:"context"`
	State       string    `json:"state"`
	Description string    `json:"description,omitempty"`
	TargetURL   string    `json:"target_url,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Review struct {
	ID          int64      `json:"id"`
	State       string     `json:"state"`
	User        string     `json:"user"`
	Body        string     `json:"body,omitempty"`
	URL         string     `json:"html_url"`
	SubmittedAt *time.Time `json:"submitted_at,omitempty"`
}

func (s *Service) PullRequestSnapshot(ctx context.Context, token, repo string, number int, prev *Snapshot) (Snapshot, error) {
	if !validFullName(repo) || number <= 0 {
		return Snapshot{}, errors.New("github: pull request must be an owner/name on github.com and a positive number")
	}
	if prev != nil && (prev.Repo != repo || prev.Number != number) {
		prev = nil
	}
	snap := Snapshot{Repo: repo, Number: number}
	pullPath := "/repos/" + repo + "/pulls/" + strconv.Itoa(number)
	var pull struct {
		Number   int        `json:"number"`
		Title    string     `json:"title"`
		URL      string     `json:"html_url"`
		State    string     `json:"state"`
		MergedAt *time.Time `json:"merged_at"`
		ClosedAt *time.Time `json:"closed_at"`
		Head     struct {
			SHA string `json:"sha"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
		RequestedReviewers []json.RawMessage `json:"requested_reviewers"`
		RequestedTeams     []json.RawMessage `json:"requested_teams"`
	}
	old, cached := prev.page(pullPath)
	etag := ""
	if cached {
		etag = old.ETag
	}
	header, notModified, err := s.fetch(ctx, token, pullPath, etag, &pull)
	if err != nil {
		return Snapshot{}, &PullRequestError{Err: err}
	}
	if notModified {
		snap.State, snap.MergedAt, snap.Title, snap.URL, snap.HeadSHA, snap.BaseRef = prev.State, prev.MergedAt, prev.Title, prev.URL, prev.HeadSHA, prev.BaseRef
		snap.ClosedAt = prev.ClosedAt
		snap.ReviewRequested = prev.ReviewRequested
	} else {
		if pull.Number != number || pull.State != "open" && pull.State != "closed" || !validSHA(pull.Head.SHA) || !strings.HasPrefix(pull.URL, "https://github.com/") {
			return Snapshot{}, errors.New("github: API returned invalid pull request metadata")
		}
		snap.State, snap.MergedAt, snap.Title, snap.URL, snap.HeadSHA, snap.BaseRef = pull.State, pull.MergedAt, pull.Title, pull.URL, pull.Head.SHA, pull.Base.Ref
		snap.ClosedAt = pull.ClosedAt
		snap.ReviewRequested = len(pull.RequestedReviewers)+len(pull.RequestedTeams) > 0
		if snap.MergedAt != nil {
			snap.State = "merged"
		}
	}
	snap.Pages = append(snap.Pages, Page{Path: pullPath, ETag: etagOf(header, etag, notModified)})

	commit := "/repos/" + repo + "/commits/" + snap.HeadSHA
	var prevRuns []CheckRun
	var prevSuites []CheckSuite
	var prevStatuses []CommitStatus
	var prevReviews []Review
	var prevWorkflows []WorkflowRun
	if prev != nil {
		prevRuns, prevSuites, prevStatuses, prevReviews, prevWorkflows = prev.CheckRuns, prev.Suites, prev.Statuses, prev.Reviews, prev.Workflows
	}
	// Suites are read first: a suite read as completed had all its runs
	// created before the runs are read.
	type checkSuitesPage struct {
		CheckSuites []struct {
			ID         int64   `json:"id"`
			Status     string  `json:"status"`
			Conclusion *string `json:"conclusion"`
			Runs       int     `json:"latest_check_runs_count"`
			App        struct {
				Name string `json:"name"`
				Slug string `json:"slug"`
			} `json:"app"`
		} `json:"check_suites"`
	}
	suites, pages, err := paginate(ctx, s, token, commit+"/check-suites?", prev, prevSuites, "check suites", nil, func(page checkSuitesPage) []CheckSuite {
		out := make([]CheckSuite, 0, len(page.CheckSuites))
		for _, r := range page.CheckSuites {
			suite := CheckSuite{ID: r.ID, Status: r.Status, App: r.App.Name, AppSlug: r.App.Slug, Runs: r.Runs}
			if r.Conclusion != nil {
				suite.Conclusion = *r.Conclusion
			}
			out = append(out, suite)
		}
		return out
	})
	if err != nil {
		return Snapshot{}, err
	}
	snap.Suites, snap.Pages = suites, append(snap.Pages, pages...)

	type checkRunsPage struct {
		CheckRuns []struct {
			ID          int64      `json:"id"`
			Name        string     `json:"name"`
			Status      string     `json:"status"`
			Conclusion  *string    `json:"conclusion"`
			URL         string     `json:"html_url"`
			StartedAt   *time.Time `json:"started_at"`
			CompletedAt *time.Time `json:"completed_at"`
			Suite       struct {
				ID int64 `json:"id"`
			} `json:"check_suite"`
			App struct {
				ID   int64  `json:"id"`
				Name string `json:"name"`
			} `json:"app"`
		} `json:"check_runs"`
	}
	runs, pages, err := paginate(ctx, s, token, commit+"/check-runs?", prev, prevRuns, "check runs", nil, func(page checkRunsPage) []CheckRun {
		out := make([]CheckRun, 0, len(page.CheckRuns))
		for _, r := range page.CheckRuns {
			run := CheckRun{ID: r.ID, Name: r.Name, Status: r.Status, SuiteID: r.Suite.ID, App: r.App.Name, AppID: r.App.ID, URL: r.URL, StartedAt: r.StartedAt, CompletedAt: r.CompletedAt}
			if r.Conclusion != nil {
				run.Conclusion = *r.Conclusion
			}
			out = append(out, run)
		}
		return out
	})
	if err != nil {
		return Snapshot{}, err
	}
	snap.CheckRuns, snap.Pages = runs, append(snap.Pages, pages...)

	if slices.ContainsFunc(runs, func(run CheckRun) bool {
		return run.Conclusion == "cancelled" && slices.ContainsFunc(runs, func(newer CheckRun) bool {
			return newer.ID > run.ID && newer.App == run.App && newer.Name == run.Name && newer.SuiteID != run.SuiteID
		})
	}) {
		type workflowRunsPage struct {
			WorkflowRuns []WorkflowRun `json:"workflow_runs"`
		}
		var workflows []WorkflowRun
		workflows, pages, err = paginate(ctx, s, token, "/repos/"+repo+"/actions/runs?head_sha="+snap.HeadSHA+"&", prev, prevWorkflows, "workflow runs", nil, func(page workflowRunsPage) []WorkflowRun {
			return page.WorkflowRuns
		})
		var refused *APIError
		switch {
		case errors.As(err, &refused) && (refused.Status == http.StatusForbidden || refused.Status == http.StatusNotFound):
			snap.WorkflowsUnreadable = refused.Error()
		case err != nil:
			return Snapshot{}, err
		default:
			snap.Workflows, snap.Pages = workflows, append(snap.Pages, pages...)
		}
	}

	type statusPage struct {
		Statuses []CommitStatus `json:"statuses"`
	}
	statuses, pages, err := paginate(ctx, s, token, commit+"/status?", prev, prevStatuses, "commit statuses", nil, func(page statusPage) []CommitStatus {
		return page.Statuses
	})
	if err != nil {
		return Snapshot{}, err
	}
	snap.Statuses, snap.Pages = statuses, append(snap.Pages, pages...)

	type reviewRecord struct {
		ID          int64      `json:"id"`
		State       string     `json:"state"`
		Body        string     `json:"body"`
		URL         string     `json:"html_url"`
		SubmittedAt *time.Time `json:"submitted_at"`
		User        *struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	// A PENDING review is the token owner's unsubmitted draft; submitting it
	// later keeps the ID, so announcing the draft would hide the real review.
	submitted := func(r Review) bool { return r.State != "PENDING" }
	reviews, pages, err := paginate(ctx, s, token, pullPath+"/reviews?", prev, prevReviews, "reviews", submitted, func(page []reviewRecord) []Review {
		out := make([]Review, 0, len(page))
		for _, r := range page {
			review := Review{ID: r.ID, State: r.State, Body: truncate(r.Body, reviewBodyMax), URL: r.URL, SubmittedAt: r.SubmittedAt}
			// GitHub returns a null user for deleted accounts.
			if r.User != nil {
				review.User = r.User.Login
			}
			out = append(out, review)
		}
		return out
	})
	if err != nil {
		return Snapshot{}, err
	}
	snap.Reviews, snap.Pages = reviews, append(snap.Pages, pages...)
	return snap, nil
}

// RequiredChecks reads the checks snap.BaseRef requires from its classic
// branch protection and from the rulesets that apply to it, conditionally
// against prev. Both answer anyone who can read the repository; a 403 or 404
// from either is recorded in RequiredUnreadable, keeps what prev read from
// that source, and is not asked again for requiredRetry.
func (s *Service) RequiredChecks(ctx context.Context, token string, prev, snap *Snapshot) error {
	if prev != nil && (prev.Repo != snap.Repo || prev.BaseRef != snap.BaseRef) {
		prev = nil
	}
	type protection struct {
		Protected  bool `json:"protected"`
		Protection *struct {
			RequiredStatusChecks *struct {
				EnforcementLevel string `json:"enforcement_level"`
				Checks           []struct {
					Context string `json:"context"`
					AppID   *int64 `json:"app_id"`
				} `json:"checks"`
			} `json:"required_status_checks"`
		} `json:"protection"`
	}
	type rule struct {
		Type       string `json:"type"`
		Parameters struct {
			RequiredStatusChecks []struct {
				Context       string `json:"context"`
				IntegrationID int64  `json:"integration_id"`
			} `json:"required_status_checks"`
		} `json:"parameters"`
	}
	snap.Required, snap.RequiredRead, snap.RequiredUnreadable = nil, true, ""
	snap.Pages = slices.DeleteFunc(slices.Clone(snap.Pages), func(p Page) bool {
		return p.Path == snap.requiredPath("branch") || p.Path == snap.requiredPath("rules")
	})
	var refusals []string
	for _, source := range requiredSources {
		path := snap.requiredPath(source)
		var kept []RequiredCheck
		if prev != nil {
			for _, c := range prev.Required {
				if c.Source == source {
					kept = append(kept, c)
				}
			}
		}
		old, cached := prev.page(path)
		if cached && old.Refused != "" && time.Since(old.RefusedAt) < requiredRetry {
			refusals = append(refusals, old.Refused)
			snap.Required, snap.Pages = append(snap.Required, kept...), append(snap.Pages, old)
			continue
		}
		etag := ""
		if cached && old.Refused == "" {
			etag = old.ETag
		}
		var (
			found       []RequiredCheck
			hidden      bool
			header      http.Header
			notModified bool
			err         error
		)
		if source == "branch" {
			var record protection
			header, notModified, err = s.fetch(ctx, token, path, etag, &record)
			hidden = record.Protected && record.Protection == nil
			if record.Protection != nil && record.Protection.RequiredStatusChecks != nil && record.Protection.RequiredStatusChecks.EnforcementLevel != "off" {
				for _, c := range record.Protection.RequiredStatusChecks.Checks {
					check := RequiredCheck{Context: c.Context, Source: source}
					if c.AppID != nil {
						check.AppID = *c.AppID
					}
					found = append(found, check)
				}
			}
		} else {
			var record []rule
			header, notModified, err = s.fetch(ctx, token, path, etag, &record)
			for _, r := range record {
				if r.Type == "required_status_checks" {
					for _, c := range r.Parameters.RequiredStatusChecks {
						found = append(found, RequiredCheck{Context: c.Context, AppID: c.IntegrationID, Source: source})
					}
				}
			}
		}
		refused := ""
		var apiErr *APIError
		switch {
		case errors.As(err, &apiErr) && (apiErr.Status == http.StatusForbidden || apiErr.Status == http.StatusNotFound):
			refused = apiErr.Error()
		case err != nil:
			return err
		case notModified:
			found = kept
		case hidden:
			refused = "GitHub shows branch " + snap.BaseRef + " as protected but not its protection to this login"
		}
		if refused != "" {
			refusals = append(refusals, refused)
			snap.Required = append(snap.Required, kept...)
			snap.Pages = append(snap.Pages, Page{Path: path, Count: len(kept), Refused: refused, RefusedAt: time.Now()})
			continue
		}
		snap.Required = append(snap.Required, found...)
		snap.Pages = append(snap.Pages, Page{Path: path, ETag: etagOf(header, etag, notModified), Count: len(found)})
	}
	snap.RequiredUnreadable = strings.Join(refusals, "; ")
	return nil
}

// requiredSources are RequiredCheck.Source values: classic branch
// protection, then rulesets.
var requiredSources = []string{"branch", "rules"}

func (s *Snapshot) requiredPath(source string) string {
	branch := url.PathEscape(s.BaseRef)
	if source == "rules" {
		return "/repos/" + s.Repo + "/rules/branches/" + branch + "?per_page=" + strconv.Itoa(pageSize)
	}
	return "/repos/" + s.Repo + "/branches/" + branch
}

// KeepRequired carries prev's required checks into s without reading them,
// so a later RequiredChecks still asks conditionally.
func (s *Snapshot) KeepRequired(prev *Snapshot) {
	if prev == nil || prev.Repo != s.Repo || prev.BaseRef != s.BaseRef {
		return
	}
	s.Required, s.RequiredRead, s.RequiredUnreadable = prev.Required, prev.RequiredRead, prev.RequiredUnreadable
	for _, source := range requiredSources {
		if p, ok := prev.page(s.requiredPath(source)); ok {
			s.Pages = append(s.Pages, p)
		}
	}
}

// paginate walks per_page=100 pages of base, conditionally against the pages
// recorded in prev. A 304 page reuses its items from cached, which holds
// prev's items for the same endpoint in page order. keep, when set, drops
// items before they are recorded.
func paginate[R, T any](ctx context.Context, s *Service, token, base string, prev *Snapshot, cached []T, what string, keep func(T) bool, items func(R) []T) ([]T, []Page, error) {
	var out []T
	var pages []Page
	offset, reusable := 0, prev != nil
	for number := 1; ; number++ {
		if number > maxPages {
			return nil, nil, fmt.Errorf("github: pull request has more than %d %s", maxPages*pageSize, what)
		}
		path := base + "per_page=" + strconv.Itoa(pageSize) + "&page=" + strconv.Itoa(number)
		var old Page
		if reusable {
			old, reusable = prev.page(path)
			reusable = reusable && offset+old.Count <= len(cached)
		}
		etag := ""
		// A 304 on the last allowed full page cannot say whether more follow.
		if reusable && (!old.Full || number < maxPages) {
			etag = old.ETag
		}
		var record R
		header, notModified, err := s.fetch(ctx, token, path, etag, &record)
		if err != nil {
			return nil, nil, err
		}
		var got []T
		full := old.Full
		_, more := nextLink(header.Values("Link"))
		if notModified {
			got = cached[offset : offset+old.Count]
			// An unchanged full page says nothing about items added after it.
			more = more || full
		} else {
			got = items(record)
			if len(got) > pageSize {
				return nil, nil, fmt.Errorf("github: API returned too many %s in one page", what)
			}
			full = len(got) == pageSize
			if keep != nil {
				got = slices.DeleteFunc(got, func(item T) bool { return !keep(item) })
			}
		}
		if reusable {
			offset += old.Count
		}
		out = append(out, got...)
		pages = append(pages, Page{Path: path, ETag: etagOf(header, etag, notModified), Count: len(got), Full: full})
		if !more {
			return out, pages, nil
		}
	}
}

func (s *Snapshot) page(path string) (Page, bool) {
	if s == nil {
		return Page{}, false
	}
	for _, p := range s.Pages {
		if p.Path == path {
			return p, true
		}
	}
	return Page{}, false
}

// etagOf keeps the ETag that matched when a 304 omits it. A 200 without an
// ETag records none, so its body is never paired with an older tag.
func etagOf(header http.Header, sent string, notModified bool) string {
	if received := header.Get("ETag"); received != "" || !notModified {
		return received
	}
	return sent
}

func validSHA(sha string) bool {
	if len(sha) != 40 && len(sha) != 64 {
		return false
	}
	for _, c := range sha {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Checks counts the check runs and commit statuses on the head commit
// that the base branch requires, or every one when it requires none or its
// requirements cannot be read.
type Checks struct {
	Passed  int `json:"passed"`
	Failed  int `json:"failed"`
	Pending int `json:"pending"`
	// Cancelled counts cancelled check runs that nothing reran.
	Cancelled int `json:"cancelled"`
	// RequiredOnly is whether only required checks are counted.
	RequiredOnly bool `json:"required_only"`
	// Missing names required checks nothing has reported yet.
	Missing []string `json:"missing,omitempty"`
	// FailedNames, CancelledNames and PendingNames hold one name per
	// counted check run or status; a suite still to create runs has none.
	FailedNames    []string `json:"failed_names,omitempty"`
	CancelledNames []string `json:"cancelled_names,omitempty"`
	PendingNames   []string `json:"pending_names,omitempty"`
	// Suites counts, within Pending, suites that may still create check
	// runs but have none pending; EmptySuites those of them with no check
	// run at all.
	Suites      int `json:"suites,omitempty"`
	EmptySuites int `json:"empty_suites,omitempty"`
	// All counts every check when RequiredOnly; nil otherwise.
	All *Checks `json:"all,omitempty"`
	// DoneAt is when the last counted check finished.
	DoneAt time.Time `json:"done_at,omitzero"`
	// Final is true once at least one check is counted, none is pending
	// and no required check is missing. A commit whose checks have not been
	// created yet is not final.
	Final bool `json:"final"`
	// Key changes with the head commit and with every check run or status ID
	// and final conclusion, so a failed run followed by a passing rerun, or a
	// run updated in place, yields a new key. It covers every check whether
	// or not RequiredOnly, so reading the requirements does not change it.
	Key string `json:"key"`
}

// CurrentRuns drops a check run when a newer one with the same app and name
// is in the same suite (a rerun), or when the older one was cancelled and the
// newer one ran in another suite of the same workflow (superseded by
// concurrency). Separate workflows can share a job name, so a cancelled run
// whose replacement is not known to come from its workflow stays counted.
func (s Snapshot) CurrentRuns() []CheckRun {
	workflow := map[int64]int64{}
	for _, w := range s.Workflows {
		workflow[w.SuiteID] = w.WorkflowID
	}
	return slices.DeleteFunc(slices.Clone(s.CheckRuns), func(run CheckRun) bool {
		return slices.ContainsFunc(s.CheckRuns, func(newer CheckRun) bool {
			return newer.ID > run.ID && newer.App == run.App && newer.Name == run.Name &&
				(newer.SuiteID == run.SuiteID ||
					run.Conclusion == "cancelled" && workflow[run.SuiteID] != 0 && workflow[run.SuiteID] == workflow[newer.SuiteID])
		})
	})
}

// Checks counts CurrentRuns and the commit statuses. A suite that may
// still create check runs counts as one pending check: an incomplete one
// with no pending run when it belongs to GitHub Actions or already has
// runs (other apps can leave a zero-run suite queued forever), and a
// completed one with more runs than were read. When only required checks
// count, such a suite counts only while a required check is missing.
func (s Snapshot) Checks() Checks {
	all := s.tally(false)
	if len(s.Required) == 0 {
		return all
	}
	c := s.tally(true)
	c.All, c.Key = &all, all.Key
	return c
}

// WithoutSuites is c with its waiting suites no longer pending, or only
// those with no check run when emptyOnly. GitHub leaves a GitHub Actions
// suite queued forever when path or branch filters skip every workflow.
func (c Checks) WithoutSuites(emptyOnly bool) Checks {
	drop := c.Suites
	if emptyOnly {
		drop = c.EmptySuites
	}
	c.Pending, c.Suites, c.EmptySuites = c.Pending-drop, c.Suites-drop, 0
	c.Final = c.final()
	return c
}

func (c Checks) final() bool {
	return c.Passed+c.Failed+c.Cancelled > 0 && c.Pending == 0 && len(c.Missing) == 0
}

func (s Snapshot) tally(requiredOnly bool) Checks {
	c := Checks{RequiredOnly: requiredOnly}
	matches := func(r RequiredCheck, name string, appID int64) bool {
		return r.Context == name && (r.AppID <= 0 || appID == 0 || r.AppID == appID)
	}
	required := func(name string, appID int64) bool {
		return !c.RequiredOnly || slices.ContainsFunc(s.Required, func(r RequiredCheck) bool { return matches(r, name, appID) })
	}
	type check struct {
		name  string
		appID int64
	}
	var reported []check
	current := s.CurrentRuns()
	ids := make([]string, 0, len(current)+len(s.Statuses))
	running := map[int64]bool{}
	for _, run := range current {
		if !required(run.Name, run.AppID) {
			continue
		}
		reported = append(reported, check{run.Name, run.AppID})
		ids = append(ids, "c"+strconv.FormatInt(run.ID, 10)+"="+run.Conclusion)
		switch {
		case run.Status != "completed":
			c.Pending++
			c.PendingNames = append(c.PendingNames, run.Name)
			running[run.SuiteID] = true
		case run.Conclusion == "success" || run.Conclusion == "neutral" || run.Conclusion == "skipped":
			c.Passed++
		case run.Conclusion == "cancelled":
			c.Cancelled++
			c.CancelledNames = append(c.CancelledNames, run.Name)
		default:
			c.Failed++
			c.FailedNames = append(c.FailedNames, run.Name)
		}
		if run.CompletedAt != nil && run.CompletedAt.After(c.DoneAt) {
			c.DoneAt = *run.CompletedAt
		}
	}
	for _, status := range s.Statuses {
		if !required(status.Context, 0) {
			continue
		}
		reported = append(reported, check{status.Context, 0})
		ids = append(ids, "s"+strconv.FormatInt(status.ID, 10)+"="+status.State)
		switch status.State {
		case "success":
			c.Passed++
		case "pending":
			c.Pending++
			c.PendingNames = append(c.PendingNames, status.Context)
		default:
			c.Failed++
			c.FailedNames = append(c.FailedNames, status.Context)
		}
		if status.UpdatedAt.After(c.DoneAt) {
			c.DoneAt = status.UpdatedAt
		}
	}
	if c.RequiredOnly {
		for _, r := range s.Required {
			if !slices.ContainsFunc(reported, func(k check) bool { return matches(r, k.name, k.appID) }) && !slices.Contains(c.Missing, r.Context) {
				c.Missing = append(c.Missing, r.Context)
				ids = append(ids, "m"+r.Context)
			}
		}
		slices.Sort(c.Missing)
	}
	if !c.RequiredOnly || len(c.Missing) > 0 {
		read := map[int64]map[string]bool{}
		for _, run := range s.CheckRuns {
			if read[run.SuiteID] == nil {
				read[run.SuiteID] = map[string]bool{}
			}
			read[run.SuiteID][run.App+"/"+run.Name] = true
		}
		for _, suite := range s.Suites {
			incomplete := suite.Status != "completed" && !running[suite.ID] && (suite.AppSlug == "github-actions" || suite.Runs > 0)
			if incomplete || suite.Status == "completed" && len(read[suite.ID]) < suite.Runs {
				c.Pending++
				c.Suites++
				if suite.Runs == 0 {
					c.EmptySuites++
				}
				ids = append(ids, "q"+strconv.FormatInt(suite.ID, 10))
			}
		}
	}
	c.Final = c.final()
	slices.Sort(ids)
	sum := sha256.Sum256([]byte(strings.Join(ids, ",")))
	c.Key = s.HeadSHA + ":" + hex.EncodeToString(sum[:8])
	return c
}
