package github

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	pageSize = 100
	maxPages = 10
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
	Title    string     `json:"title"`
	URL      string     `json:"html_url"`
	HeadSHA  string     `json:"head_sha"`
	BaseRef  string     `json:"base_ref"`

	CheckRuns []CheckRun     `json:"check_runs"`
	Statuses  []CommitStatus `json:"statuses"`
	Reviews   []Review       `json:"reviews"`

	Pages []Page `json:"pages"`
}

// Page is one conditional GET of a snapshot. Count is how many items of the
// matching Snapshot list the page contributed, in request order.
type Page struct {
	Path  string `json:"path"`
	ETag  string `json:"etag"`
	Count int    `json:"count"`
}

type CheckRun struct {
	ID          int64      `json:"id"`
	Name        string     `json:"name"`
	Status      string     `json:"status"`
	Conclusion  string     `json:"conclusion,omitempty"`
	SuiteID     int64      `json:"check_suite_id"`
	App         string     `json:"app"`
	URL         string     `json:"html_url"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
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
		Head     struct {
			SHA string `json:"sha"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
	}
	old, cached := prev.page(pullPath)
	etag := ""
	if cached {
		etag = old.ETag
	}
	header, notModified, err := s.fetch(ctx, token, pullPath, etag, &pull)
	if err != nil {
		return Snapshot{}, err
	}
	if notModified {
		snap.State, snap.MergedAt, snap.Title, snap.URL, snap.HeadSHA, snap.BaseRef = prev.State, prev.MergedAt, prev.Title, prev.URL, prev.HeadSHA, prev.BaseRef
	} else {
		if pull.Number != number || pull.State != "open" && pull.State != "closed" || !validSHA(pull.Head.SHA) || !strings.HasPrefix(pull.URL, "https://github.com/") {
			return Snapshot{}, errors.New("github: API returned invalid pull request metadata")
		}
		snap.State, snap.MergedAt, snap.Title, snap.URL, snap.HeadSHA, snap.BaseRef = pull.State, pull.MergedAt, pull.Title, pull.URL, pull.Head.SHA, pull.Base.Ref
		if snap.MergedAt != nil {
			snap.State = "merged"
		}
	}
	snap.Pages = append(snap.Pages, Page{Path: pullPath, ETag: etagOf(header, etag, notModified)})

	commit := "/repos/" + repo + "/commits/" + snap.HeadSHA
	var prevRuns []CheckRun
	var prevStatuses []CommitStatus
	var prevReviews []Review
	if prev != nil {
		prevRuns, prevStatuses, prevReviews = prev.CheckRuns, prev.Statuses, prev.Reviews
	}
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
				Name string `json:"name"`
			} `json:"app"`
		} `json:"check_runs"`
	}
	runs, pages, err := paginate(ctx, s, token, commit+"/check-runs?", prev, prevRuns, "check runs", func(page checkRunsPage) []CheckRun {
		out := make([]CheckRun, 0, len(page.CheckRuns))
		for _, r := range page.CheckRuns {
			run := CheckRun{ID: r.ID, Name: r.Name, Status: r.Status, SuiteID: r.Suite.ID, App: r.App.Name, URL: r.URL, StartedAt: r.StartedAt, CompletedAt: r.CompletedAt}
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

	type statusPage struct {
		Statuses []CommitStatus `json:"statuses"`
	}
	statuses, pages, err := paginate(ctx, s, token, commit+"/status?", prev, prevStatuses, "commit statuses", func(page statusPage) []CommitStatus {
		return page.Statuses
	})
	if err != nil {
		return Snapshot{}, err
	}
	snap.Statuses, snap.Pages = statuses, append(snap.Pages, pages...)

	type reviewRecord struct {
		ID          int64      `json:"id"`
		State       string     `json:"state"`
		URL         string     `json:"html_url"`
		SubmittedAt *time.Time `json:"submitted_at"`
		User        *struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	reviews, pages, err := paginate(ctx, s, token, pullPath+"/reviews?", prev, prevReviews, "reviews", func(page []reviewRecord) []Review {
		out := make([]Review, 0, len(page))
		for _, r := range page {
			review := Review{ID: r.ID, State: r.State, URL: r.URL, SubmittedAt: r.SubmittedAt}
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

// paginate walks per_page=100 pages of base, conditionally against the pages
// recorded in prev. A 304 page reuses its items from cached, which holds
// prev's items for the same endpoint in page order.
func paginate[R, T any](ctx context.Context, s *Service, token, base string, prev *Snapshot, cached []T, what string, items func(R) []T) ([]T, []Page, error) {
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
		if reusable {
			etag = old.ETag
		}
		var record R
		header, notModified, err := s.fetch(ctx, token, path, etag, &record)
		if err != nil {
			return nil, nil, err
		}
		var got []T
		_, more := nextLink(header.Values("Link"))
		if notModified {
			got = cached[offset : offset+old.Count]
			// An unchanged full page says nothing about items added after it.
			more = more || old.Count == pageSize
		} else {
			got = items(record)
			if len(got) > pageSize {
				return nil, nil, fmt.Errorf("github: API returned too many %s in one page", what)
			}
		}
		if reusable {
			offset += old.Count
		}
		out = append(out, got...)
		pages = append(pages, Page{Path: path, ETag: etagOf(header, etag, notModified), Count: len(got)})
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

// Checks counts check runs and commit statuses on the head commit.
type Checks struct {
	Passed  int `json:"passed"`
	Failed  int `json:"failed"`
	Pending int `json:"pending"`
	// Final is true once at least one check exists and none is pending. A
	// commit whose checks have not been created yet is not final.
	Final bool `json:"final"`
	// Key changes with the head commit and with every new check run or status
	// ID, so a failed run followed by a passing rerun yields two keys.
	Key string `json:"key"`
}

func (s Snapshot) Checks() Checks {
	var c Checks
	ids := make([]string, 0, len(s.CheckRuns)+len(s.Statuses))
	for _, run := range s.CheckRuns {
		ids = append(ids, "c"+strconv.FormatInt(run.ID, 10))
		switch {
		case run.Status != "completed":
			c.Pending++
		case run.Conclusion == "success" || run.Conclusion == "neutral" || run.Conclusion == "skipped":
			c.Passed++
		default:
			c.Failed++
		}
	}
	for _, status := range s.Statuses {
		ids = append(ids, "s"+strconv.FormatInt(status.ID, 10))
		switch status.State {
		case "success":
			c.Passed++
		case "pending":
			c.Pending++
		default:
			c.Failed++
		}
	}
	c.Final = len(ids) > 0 && c.Pending == 0
	slices.Sort(ids)
	sum := sha256.Sum256([]byte(strings.Join(ids, ",")))
	c.Key = s.HeadSHA + ":" + hex.EncodeToString(sum[:8])
	return c
}
