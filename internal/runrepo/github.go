package runrepo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type PRTarget struct {
	Repository string `json:"repository"`
	BaseBranch string `json:"base_branch"`
	HeadRepository string `json:"head_repository"`
	HeadBranch string `json:"head_branch"`
}

type PullRequest struct {
	Number int `json:"number"`
	URL string `json:"url"`
	State string `json:"state"`
	Title string `json:"title"`
	Draft bool `json:"draft"`
	Head string `json:"head"`
	Target PRTarget `json:"target"`
}

type PRResult struct {
	Identity string `json:"identity"`
	PullRequest *PullRequest `json:"pull_request,omitempty"`
	Created bool `json:"created"`
	Reconciled bool `json:"reconciled"`
	CreationUncertain bool `json:"creation_uncertain"`
	Output CommandOutput `json:"output"`
}

type PRCreateRequest struct {
	Expected Expected `json:"expected"`
	Target PRTarget `json:"target"`
	Title string `json:"title"`
	Body string `json:"body"`
	Draft bool `json:"draft"`
	// Optional confirmation from a previous identity display. An account
	// switch then fails rather than attributing publication to the old login.
	ExpectedLogin string `json:"expected_login,omitempty"`
}

type PRFeedback struct {
	Identity string `json:"identity"`
	PullRequest PullRequest `json:"pull_request"`
	Checks json.RawMessage `json:"checks"`
	Comments json.RawMessage `json:"comments"`
	Reviews json.RawMessage `json:"reviews"`
	ReviewComments json.RawMessage `json:"review_comments"`
}

type githubPR struct {
	Number int `json:"number"`
	URL string `json:"html_url"`
	State string `json:"state"`
	Title string `json:"title"`
	Draft bool `json:"draft"`
	Merged bool `json:"merged"`
	MergedAt *string `json:"merged_at"`
	Base githubRef `json:"base"`
	Head githubRef `json:"head"`
}
type githubRef struct {
	Ref string `json:"ref"`
	SHA string `json:"sha"`
	Repo *struct { FullName string `json:"full_name"` } `json:"repo"`
}

func (pr githubPR) matches(target PRTarget) bool {
	return pr.Number > 0 && pr.Base.Repo != nil && pr.Head.Repo != nil &&
		strings.EqualFold(pr.Base.Repo.FullName, target.Repository) &&
		strings.EqualFold(pr.Head.Repo.FullName, target.HeadRepository) &&
		pr.Base.Ref == target.BaseBranch && pr.Head.Ref == target.HeadBranch
}
func (pr githubPR) public(target PRTarget) *PullRequest {
	state := pr.State
	if pr.Merged || pr.MergedAt != nil { state = "merged" }
	return &PullRequest{Number: pr.Number, URL: pr.URL, State: state, Title: pr.Title, Draft: pr.Draft, Head: pr.Head.SHA, Target: target}
}

func validRepository(repo string) bool {
	parts := strings.Split(repo, "/")
	if len(parts) != 2 { return false }
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || len(part) > 100 || strings.HasPrefix(part, "-") { return false }
		for _, c := range part { if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') { return false } }
	}
	return true
}

func (s *Service) target(ctx context.Context, run Execution, target PRTarget) error {
	if !validRepository(target.Repository) || !validRepository(target.HeadRepository) { return errors.New("runrepo: PR repository and head repository must be explicit owner/name identities") }
	if err := s.branch(ctx, run, target.BaseBranch); err != nil { return err }
	return s.branch(ctx, run, target.HeadBranch)
}

func (s *Service) gh(ctx context.Context, run Execution, mutate bool, args ...string) (CommandOutput, error) {
	return s.command(ctx, run, mutate, append([]string{"gh", "api", "--hostname", "github.com"}, args...)...)
}

// Identity reads the identity actually used by gh's API, including GH_TOKEN
// overrides. It does not infer identity from repository ownership or auth files.
func (s *Service) Identity(ctx context.Context, run Execution) (string, error) {
	out, err := s.gh(ctx, run, false, "user", "--jq", ".login")
	text, err := complete(out, err)
	if err != nil { return "", err }
	login := strings.TrimSpace(text)
	if !cleanText(login, 100) { return "", errors.New("runrepo: GitHub returned no current login") }
	return login, nil
}

func (s *Service) lookup(ctx context.Context, run Execution, target PRTarget, state string) (*PullRequest, CommandOutput, error) {
	query := url.Values{"state": {state}, "head": {strings.SplitN(target.HeadRepository, "/", 2)[0]+":"+target.HeadBranch}, "base": {target.BaseBranch}, "sort": {"created"}, "direction": {"desc"}, "per_page": {"100"}}
	out, err := s.gh(ctx, run, false, "repos/"+target.Repository+"/pulls?"+query.Encode(), "--paginate", "--slurp")
	text, err := complete(out, err)
	if err != nil { return nil, out, err }
	var pages [][]githubPR
	if err := json.Unmarshal([]byte(text), &pages); err != nil { return nil, out, fmt.Errorf("runrepo: decode PR lookup: %w", err) }
	var latest *PullRequest
	var open *PullRequest
	for _, page := range pages { for _, pr := range page {
		if !pr.matches(target) { continue }
		if latest == nil { latest = pr.public(target) }
		if pr.State == "open" {
			if open != nil { return nil, out, errors.New("runrepo: multiple open PRs match the exact head repository and base") }
			open = pr.public(target)
		}
	} }
	if open != nil { return open, out, nil }
	return latest, out, nil
}

// LookupPR discovers GitHub state on every refresh, including PRs created by
// agents using gh directly. A closed/merged PR is returned if none is open.
func (s *Service) LookupPR(ctx context.Context, run Execution, target PRTarget) (PRResult, error) {
	result := PRResult{}
	if err := s.target(ctx, run, target); err != nil { return result, err }
	identity, err := s.Identity(ctx, run)
	result.Identity = identity
	if err != nil { return result, err }
	result.PullRequest, result.Output, err = s.lookup(ctx, run, target, "all")
	return result, err
}

// CreatePR never pushes, retries an external mutation, or changes a branch.
// Every invocation first discovers the exact existing open PR. A failed POST
// is reconciled once by read only; partial/uncertain state and the original
// stderr survive. The caller must retain a successful PushResult independently.
func (s *Service) CreatePR(ctx context.Context, run Execution, req PRCreateRequest) (PRResult, error) {
	result := PRResult{}
	if !cleanText(req.Title, 512) || len(req.Body) > 64<<10 || strings.ContainsRune(req.Body, 0) || req.ExpectedLogin != "" && !cleanText(req.ExpectedLogin, 100) { return result, errors.New("runrepo: invalid PR title, body, or expected login") }
	if err := s.target(ctx, run, req.Target); err != nil { return result, err }
	if _, err := s.checkExpected(ctx, run, req.Expected); err != nil { return result, err }
	identity, err := s.Identity(ctx, run)
	result.Identity = identity
	if err != nil { return result, err }
	if req.ExpectedLogin != "" && !strings.EqualFold(req.ExpectedLogin, identity) { return result, fmt.Errorf("runrepo: GitHub identity changed from %s to %s", req.ExpectedLogin, identity) }
	result.PullRequest, result.Output, err = s.lookup(ctx, run, req.Target, "open")
	if err != nil || result.PullRequest != nil { return result, err }
	// A local commit is not proof the selected fork branch was pushed. Verify
	// the explicit remote head, never gh's implicit origin/branch heuristics.
	out, err := s.gh(ctx, run, false, "repos/"+req.Target.HeadRepository+"/commits/"+url.PathEscape(req.Target.HeadBranch), "--jq", ".sha")
	remoteHead, err := complete(out, err)
	if err != nil { result.Output = out; return result, err }
	if strings.TrimSpace(remoteHead) != req.Expected.Head { return result, fmt.Errorf("runrepo: GitHub head %s/%s is at %s, expected %s; push the selected commit first", req.Target.HeadRepository, req.Target.HeadBranch, strings.TrimSpace(remoteHead), req.Expected.Head) }
	// Refresh identity and local branch immediately before the API mutation.
	currentLogin, err := s.Identity(ctx, run)
	if err != nil { return result, err }
	if currentLogin != identity { return result, fmt.Errorf("runrepo: GitHub identity changed from %s to %s", identity, currentLogin) }
	if _, err := s.checkExpected(ctx, run, req.Expected); err != nil { return result, err }
	args := []string{"repos/"+req.Target.Repository+"/pulls", "--method", "POST", "--raw-field", "title="+req.Title, "--raw-field", "body="+req.Body, "--raw-field", "base="+req.Target.BaseBranch, "--raw-field", "head="+strings.SplitN(req.Target.HeadRepository, "/", 2)[0]+":"+req.Target.HeadBranch, "--field", "draft="+strconv.FormatBool(req.Draft)}
	if !strings.EqualFold(req.Target.Repository, req.Target.HeadRepository) { args = append(args, "--raw-field", "head_repo="+strings.SplitN(req.Target.HeadRepository, "/", 2)[1]) }
	out, createErr := s.gh(ctx, run, true, args...)
	result.Output = out
	if createErr == nil && !out.Truncated {
		var pr githubPR
		if err := json.Unmarshal([]byte(out.Stdout), &pr); err == nil && pr.matches(req.Target) {
			result.PullRequest = pr.public(req.Target)
			result.Created = true
			return result, nil
		}
	}
	if createErr == nil { createErr = &CommandError{Operation: "create PR", Output: out, Cause: errors.New("GitHub creation response is incomplete or does not match the exact target")} }
	result.CreationUncertain = true
	// A cancelled transport can hide a successful POST. Use a fresh bounded
	// read, still subject to current authority, and never replay the POST.
	reconcileCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	found, _, reconcileErr := s.lookup(reconcileCtx, run, req.Target, "open")
	if reconcileErr == nil && found != nil {
		result.PullRequest = found
		result.Reconciled = true
		result.CreationUncertain = false
		return result, nil
	}
	if reconcileErr != nil { return result, errors.Join(createErr, fmt.Errorf("runrepo: reconcile PR creation: %w", reconcileErr)) }
	return result, createErr
}

// PRFeedback returns current checks, issue comments, reviews, and inline review
// comments. All collection endpoints paginate; an over-limit response fails
// explicitly rather than presenting a truncated collection as complete.
func (s *Service) PRFeedback(ctx context.Context, run Execution, target PRTarget, number int) (PRFeedback, error) {
	result := PRFeedback{}
	if err := s.target(ctx, run, target); err != nil { return result, err }
	if number <= 0 { return result, errors.New("runrepo: positive PR number required") }
	identity, err := s.Identity(ctx, run)
	result.Identity = identity
	if err != nil { return result, err }
	prefix := "repos/"+target.Repository
	pullPath := prefix+"/pulls/"+strconv.Itoa(number)
	out, err := s.gh(ctx, run, false, pullPath)
	text, err := complete(out, err)
	if err != nil { return result, err }
	var pr githubPR
	if err := json.Unmarshal([]byte(text), &pr); err != nil { return result, fmt.Errorf("runrepo: decode PR: %w", err) }
	if !pr.matches(target) || pr.Number != number { return result, errors.New("runrepo: PR does not match the selected repository, base, and fork head") }
	result.PullRequest = *pr.public(target)
	for _, collection := range []struct { path string; destination *json.RawMessage }{
		{prefix+"/issues/"+strconv.Itoa(number)+"/comments?per_page=100", &result.Comments},
		{pullPath+"/reviews?per_page=100", &result.Reviews},
		{pullPath+"/comments?per_page=100", &result.ReviewComments},
	} {
		out, err := s.gh(ctx, run, false, collection.path, "--paginate", "--slurp")
		text, err := complete(out, err)
		if err != nil { return result, err }
		var pages []json.RawMessage
		if err := json.Unmarshal([]byte(text), &pages); err != nil { return result, fmt.Errorf("runrepo: decode feedback: %w", err) }
		var all []json.RawMessage
		for _, page := range pages {
			var items []json.RawMessage
			if err := json.Unmarshal(page, &items); err != nil { return result, fmt.Errorf("runrepo: decode feedback page: %w", err) }
			all = append(all, items...)
		}
		if all == nil { all = []json.RawMessage{} }
		*collection.destination, err = json.Marshal(all)
		if err != nil { return result, err }
	}
	// gh combines legacy commit statuses and check runs into one rollup.
	out, err = s.command(ctx, run, false, "gh", "pr", "view", strconv.Itoa(number), "--repo", "github.com/"+target.Repository, "--json", "statusCheckRollup")
	text, err = complete(out, err)
	if err != nil { return result, err }
	var checks struct { Checks json.RawMessage `json:"statusCheckRollup"` }
	if err := json.Unmarshal([]byte(text), &checks); err != nil { return result, fmt.Errorf("runrepo: decode PR checks: %w", err) }
	result.Checks = checks.Checks
	return result, nil
}
