package protocol

import (
	"regexp"
	"strings"
	"time"
)

const (
	MethodWorkspaceImport   = "workspace.import"
	MethodRunGitStatus      = "run.git.status"
	MethodRunGitDiff        = "run.git.diff"
	MethodRunGitCommit      = "run.git.commit"
	MethodRunGitPush        = "run.git.push"
	MethodRunGitLog         = "run.git.log"
	MethodRunPRStatus       = "run.pr.status"
	MethodRunPRCreate       = "run.pr.create"
	MethodRunPRFeedback     = "run.pr.feedback"
	MethodRunPRMerge        = "run.pr.merge"
	DefaultRunGitLogLimit   = 50
	MaxRunGitLogLimit       = 200
	MaxRunGitPaths          = 256
	MaxRunGitPathBytes      = 4096
	MaxRunGitMessageBytes   = 16 << 10
	MaxRunPRBodyBytes       = 32 << 10
	MaxRunRepoOutputBytes   = 64 << 10
	MaxRunRepoParamsBytes   = 128 << 10
	MaxRunPRFeedbackEntries = 100
)

// WorkspaceImport starts the existing mirror configure/refresh/adopt flow.
// Origin is explicit and independent of SourceURL. Import never implicitly
// accepts a rewritten base: the observed initial candidate is accepted through
// workspace.mirror.adopt with its reviewed generation and observed_commit
// supplied as the required expected_commit.
type WorkspaceImportParams struct {
	Name            string               `json:"name"`
	Environment     WorkspaceEnvironment `json:"environment"`
	SourceURL       string               `json:"source_url"`
	BaseBranch      string               `json:"base_branch"`
	Origin          string               `json:"origin"`
	Auth            string               `json:"auth"`
	KnownHosts      string               `json:"known_hosts,omitempty"`
	GitHubAccountID int64                `json:"github_account_id,omitempty"`
}

// Created remains true if configuration/fetch subsequently fails. Callers
// retain the workspace ID and mirror state rather than retrying creation.
type WorkspaceImportResult struct {
	Workspace Workspace             `json:"workspace"`
	Created   bool                  `json:"created"`
	Mirror    WorkspaceMirrorResult `json:"mirror"`
	Error     string                `json:"error,omitempty"`
}

// These methods are human authenticated entry points. Agents use native Git
// and gh; run_id here must never be passed through the run-socket dispatcher.
type RunGitStatusParams struct {
	Repo  string `json:"repo,omitempty"`
	RunID string `json:"run_id"`
}

type RunGitExpected struct {
	Branch string `json:"branch"`
	Head   string `json:"head"`
}

type RunGitChange struct {
	Path         string `json:"path"`
	OriginalPath string `json:"original_path,omitempty"`
	Index        string `json:"index"`
	Worktree     string `json:"worktree"`
	Untracked    bool   `json:"untracked"`
	Conflicted   bool   `json:"conflicted"`
}

// Remotes describe native checkout configuration, not mirror provenance.
// A push requires explicitly selecting one unambiguous push URL and branch.
type RunGitRemote struct {
	Name      string   `json:"name"`
	FetchURLs []string `json:"fetch_urls"`
	PushURLs  []string `json:"push_urls"`
}

type RunGitUpstream struct {
	Remote string `json:"remote"`
	Branch string `json:"branch"`
}

type RunGitStatusResult struct {
	Repo            string               `json:"repo,omitempty"`
	Branch          string               `json:"branch"`
	Head            string               `json:"head"`
	HeadSubject     string               `json:"head_subject,omitempty"`
	Detached        bool                 `json:"detached"`
	Unborn          bool                 `json:"unborn"`
	Changes         []RunGitChange       `json:"changes"`
	Truncated       bool                 `json:"truncated"`
	AccountMemberID string               `json:"account_member_id"`
	AccountName     string               `json:"account_name"`
	Identity        string               `json:"identity"`
	IdentityError   string               `json:"identity_error,omitempty"`
	Remotes         []RunGitRemote       `json:"remotes"`
	Upstream        *RunGitUpstream      `json:"upstream,omitempty"`
	Output          RunRepoCommandOutput `json:"output"`
	Error           string               `json:"error,omitempty"`
}

// RunRepoCommandOutput preserves real diagnostic output, including failures
// after an external mutation. It must not be discarded when Error is nonempty.
type RunRepoCommandOutput struct {
	ExitCode  int    `json:"exit_code"`
	Stdout    string `json:"stdout,omitempty"`
	Stderr    string `json:"stderr,omitempty"`
	Truncated bool   `json:"truncated"`
}

type RunGitDiffParams struct {
	Repo   string   `json:"repo,omitempty"`
	RunID  string   `json:"run_id"`
	Paths  []string `json:"paths,omitempty"`
	Staged bool     `json:"staged,omitempty"`
}

type RunGitDiffResult struct {
	Repo   string               `json:"repo,omitempty"`
	State  RunGitExpected       `json:"state"`
	Output RunRepoCommandOutput `json:"output"`
	Error  string               `json:"error,omitempty"`
}

type RunGitCommitParams struct {
	Repo     string         `json:"repo,omitempty"`
	RunID    string         `json:"run_id"`
	Expected RunGitExpected `json:"expected"`
	Paths    []string       `json:"paths"`
	Message  string         `json:"message"`
}

type RunGitCommitResult struct {
	Repo         string `json:"repo,omitempty"`
	Committed    bool   `json:"committed"`
	Head         string `json:"head"`
	IndexUpdated bool   `json:"index_updated"`
	// Selected-path commits use commit-tree and an atomic reference transaction.
	// They honor identity/signing, but intentionally do not run commit hooks.
	HooksRun bool                 `json:"hooks_run"`
	Output   RunRepoCommandOutput `json:"output"`
	Error    string               `json:"error,omitempty"`
	Actual   *RunGitExpected      `json:"actual,omitempty"`
}

type RunGitLogParams struct {
	Repo  string `json:"repo,omitempty"`
	RunID string `json:"run_id"`
	Limit int    `json:"limit,omitempty"`
}

type RunGitCommit struct {
	SHA        string   `json:"sha"`
	Short      string   `json:"short"`
	Subject    string   `json:"subject"`
	Author     string   `json:"author"`
	AuthoredAt string   `json:"authored_at"`
	Parents    []string `json:"parents"`
	// Paths, on upstream commits only, are the conflicting paths it touches.
	Paths []string `json:"paths,omitempty"`
}

type RunGitConflict struct {
	Path string `json:"path"`
}

// BaseRef is the checkout's origin/<base branch> when it exists; otherwise
// it is empty and Base is the run's fork point. Commits are MergeBase..Head
// and Upstream MergeBase..Base, newest first. Conflicts are predicted by
// merging Head with Base without touching the worktree.
type RunGitLogResult struct {
	Repo      string           `json:"repo,omitempty"`
	Branch    string           `json:"branch"`
	BaseRef   string           `json:"base_ref"`
	Base      string           `json:"base"`
	Head      string           `json:"head"`
	MergeBase string           `json:"merge_base"`
	Ahead     int              `json:"ahead"`
	Behind    int              `json:"behind"`
	Commits   []RunGitCommit   `json:"commits"`
	Upstream  []RunGitCommit   `json:"upstream"`
	Conflicts []RunGitConflict `json:"conflicts"`
	// ConflictsError is why conflicts could not be predicted; Conflicts is
	// then empty and means unknown, not clean.
	ConflictsError string               `json:"conflicts_error,omitempty"`
	Truncated      bool                 `json:"truncated"`
	Output         RunRepoCommandOutput `json:"output"`
	Error          string               `json:"error,omitempty"`
}

// Repository is the explicit push URL of Remote, NOT the source mirror or PR
// repository. HeadBranch is the exact remote destination; force is unsupported.
type RunGitPushTarget struct {
	Remote     string `json:"remote"`
	Repository string `json:"repository"`
	HeadBranch string `json:"head_branch"`
}

type RunGitPushParams struct {
	Repo     string           `json:"repo,omitempty"`
	RunID    string           `json:"run_id"`
	Expected RunGitExpected   `json:"expected"`
	Target   RunGitPushTarget `json:"target"`
}

type RunGitPushResult struct {
	Repo   string               `json:"repo,omitempty"`
	Pushed bool                 `json:"pushed"`
	Head   string               `json:"head"`
	Target RunGitPushTarget     `json:"target"`
	Output RunRepoCommandOutput `json:"output"`
	Error  string               `json:"error,omitempty"`
	Actual *RunGitExpected      `json:"actual,omitempty"`
}

// Repository and HeadRepository are explicit owner/name identities. The head
// may be a fork; these selections never mutate workspace Origin or mirror.
type RunPRTarget struct {
	Repository     string `json:"repository"`
	BaseBranch     string `json:"base_branch"`
	HeadRepository string `json:"head_repository"`
	HeadBranch     string `json:"head_branch"`
}

// A zero Expected defaults to the checkout's current branch and head, and a
// zero Target to the pull request the dashboard would open: the branch's
// push remote and branch into the run's base branch on the workspace
// source. Once the run's environment is gone, run.pr.status and
// run.pr.feedback report the pull request the run last recorded.
type RunPRStatusParams struct {
	Repo     string         `json:"repo,omitempty"`
	RunID    string         `json:"run_id"`
	Expected RunGitExpected `json:"expected"`
	Target   RunPRTarget    `json:"target"`
}

type RunPRCreateParams struct {
	Repo          string         `json:"repo,omitempty"`
	RunID         string         `json:"run_id"`
	Expected      RunGitExpected `json:"expected"`
	Target        RunPRTarget    `json:"target"`
	Title         string         `json:"title"`
	Body          string         `json:"body"`
	Draft         bool           `json:"draft,omitempty"`
	ExpectedLogin string         `json:"expected_login,omitempty"`
}

type RunPullRequest struct {
	Number         int        `json:"number"`
	URL            string     `json:"url"`
	State          string     `json:"state"`
	Title          string     `json:"title"`
	Draft          bool       `json:"draft"`
	Repository     string     `json:"repository"`
	BaseBranch     string     `json:"base_branch"`
	HeadRepository string     `json:"head_repository"`
	HeadBranch     string     `json:"head_branch"`
	HeadOID        string     `json:"head_oid"`
	MergedAt       *time.Time `json:"merged_at,omitempty"`
}

// Status always comes from GitHub, including PRs created by native gh.
type RunPRStatusResult struct {
	Repo string `json:"repo,omitempty"`
	// Expected and Target are what the lookup used, defaults filled in,
	// for a run.pr.create of exactly that pull request.
	Expected        *RunGitExpected      `json:"expected,omitempty"`
	Target          *RunPRTarget         `json:"target,omitempty"`
	Identity        string               `json:"identity"`
	PullRequest     *RunPullRequest      `json:"pull_request"`
	Output          RunRepoCommandOutput `json:"output"`
	Error           string               `json:"error,omitempty"`
	AccountMemberID string               `json:"account_member_id"`
	Actual          *RunGitExpected      `json:"actual,omitempty"`
}

// A failed/uncertain create never undoes a prior push. CreationUncertain means
// reconcile this exact head/base before retrying; it is not permission to
// blindly replay an external mutation.
type RunPRCreateResult struct {
	Repo              string               `json:"repo,omitempty"`
	Identity          string               `json:"identity"`
	PullRequest       *RunPullRequest      `json:"pull_request"`
	Created           bool                 `json:"created"`
	Reconciled        bool                 `json:"reconciled"`
	CreationUncertain bool                 `json:"creation_uncertain"`
	AccountMemberID   string               `json:"account_member_id"`
	Output            RunRepoCommandOutput `json:"output"`
	Error             string               `json:"error,omitempty"`
	Actual            *RunGitExpected      `json:"actual,omitempty"`
}

type RunPRFeedbackParams struct {
	Repo     string         `json:"repo,omitempty"`
	RunID    string         `json:"run_id"`
	Expected RunGitExpected `json:"expected"`
	Target   RunPRTarget    `json:"target"`
	Limit    int            `json:"limit,omitempty"`
}

type RunPRCheck struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion,omitempty"`
	URL        string `json:"url,omitempty"`
}

type RunPRComment struct {
	ID        string `json:"id"`
	Author    string `json:"author"`
	Body      string `json:"body"`
	URL       string `json:"url"`
	CreatedAt string `json:"created_at"`
}

type RunPRReview struct {
	ID          string `json:"id"`
	Author      string `json:"author"`
	Body        string `json:"body"`
	State       string `json:"state"`
	SubmittedAt string `json:"submitted_at"`
}

type RunPRReviewComment struct {
	ID           string `json:"id"`
	ReviewID     string `json:"review_id"`
	Author       string `json:"author"`
	Body         string `json:"body"`
	URL          string `json:"url"`
	CreatedAt    string `json:"created_at"`
	Path         string `json:"path"`
	Line         *int   `json:"line,omitempty"`
	OriginalLine *int   `json:"original_line,omitempty"`
	Side         string `json:"side,omitempty"`
	StartLine    *int   `json:"start_line,omitempty"`
	StartSide    string `json:"start_side,omitempty"`
	CommitOID    string `json:"commit_oid"`
	InReplyToID  string `json:"in_reply_to_id,omitempty"`
	DiffHunk     string `json:"diff_hunk,omitempty"`
}

type RunPRFeedbackResult struct {
	Repo           string               `json:"repo,omitempty"`
	Identity       string               `json:"identity"`
	PullRequest    *RunPullRequest      `json:"pull_request"`
	Checks         []RunPRCheck         `json:"checks"`
	Comments       []RunPRComment       `json:"comments"`
	Reviews        []RunPRReview        `json:"reviews"`
	ReviewComments []RunPRReviewComment `json:"review_comments"`
	// CommentsUnavailable means Comments and ReviewComments were not read,
	// so their emptiness says nothing about the pull request.
	CommentsUnavailable bool `json:"comments_unavailable,omitempty"`
	// ReviewsUnavailable means reading Reviews failed after Checks were
	// read; Error says why.
	ReviewsUnavailable bool `json:"reviews_unavailable,omitempty"`
	// GitHub's own verdict on the merge: Mergeable is MERGEABLE, CONFLICTING
	// or UNKNOWN, MergeStateStatus is CLEAN, BEHIND, DIRTY, BLOCKED, UNSTABLE,
	// DRAFT, HAS_HOOKS or UNKNOWN.
	Mergeable        string               `json:"mergeable,omitempty"`
	MergeStateStatus string               `json:"merge_state_status,omitempty"`
	AccountMemberID  string               `json:"account_member_id"`
	Actual           *RunGitExpected      `json:"actual,omitempty"`
	Truncated        bool                 `json:"truncated"`
	Output           RunRepoCommandOutput `json:"output"`
	Error            string               `json:"error,omitempty"`
}

// RunPRBlockers counts what a pull request's feedback reports could block
// its merge: checks that failed or have not finished, and reviewers whose
// latest verdict requests changes. Partial means the feedback was cut off
// or its reviews were not read, so the counts are lower bounds.
type RunPRBlockers struct {
	FailingChecks    int  `json:"failing_checks"`
	PendingChecks    int  `json:"pending_checks"`
	ChangesRequested int  `json:"changes_requested"`
	Partial          bool `json:"partial,omitempty"`
}

func (r RunPRFeedbackResult) Blockers() RunPRBlockers {
	b := RunPRBlockers{Partial: r.Truncated || r.ReviewsUnavailable}
	for _, ch := range r.Checks {
		switch strings.ToUpper(ch.Conclusion) {
		case "SUCCESS", "NEUTRAL", "SKIPPED":
		case "":
			b.PendingChecks++
		default:
			b.FailingChecks++
		}
	}
	latest := map[string]string{}
	for _, rv := range r.Reviews {
		// A comment does not withdraw a verdict.
		if rv.State == "APPROVED" || rv.State == "CHANGES_REQUESTED" || rv.State == "DISMISSED" {
			latest[rv.Author] = rv.State
		}
	}
	for _, state := range latest {
		if state == "CHANGES_REQUESTED" {
			b.ChangesRequested++
		}
	}
	return b
}

// Without Repository and Number, run.pr.merge merges the pull request the run
// most recently recorded. GitHub refuses the merge unless the PR head is
// still ExpectedHead, the head_oid the member reviewed.
type RunPRMergeParams struct {
	Repo         string `json:"repo,omitempty"`
	RunID        string `json:"run_id"`
	Method       string `json:"method"`
	ExpectedHead string `json:"expected_head"`
	Repository   string `json:"repository,omitempty"`
	Number       int    `json:"number,omitempty"`
	// ExpectedLogin, as on run.pr.create, fails the merge when gh's login
	// is no longer the one shown.
	ExpectedLogin string `json:"expected_login,omitempty"`
	// ExpectedBase fails the merge, before it is attempted, when the pull
	// request now targets another base branch.
	ExpectedBase string `json:"expected_base,omitempty"`
}

// PullRequest is GitHub's state read after the merge attempt, also when
// it failed; a merge queue can accept a PR without merging it yet.
type RunPRMergeResult struct {
	Repo            string               `json:"repo,omitempty"`
	Identity        string               `json:"identity"`
	PullRequest     *RunPullRequest      `json:"pull_request"`
	Merged          bool                 `json:"merged"`
	AccountMemberID string               `json:"account_member_id"`
	Output          RunRepoCommandOutput `json:"output"`
	Error           string               `json:"error,omitempty"`
}

var githubRemote = regexp.MustCompile(`^(?:https://(?:[^@/]+@)?github\.com/|(?:ssh://)?git@github\.com[:/])([^/]+/[^/]+?)(?:\.git)?/?$`)

// GitHubRemoteRepository is githubRepository in web/src/routes/diff/publish-target.ts:
// the owner/name of a github.com remote URL, empty for any other URL.
func GitHubRemoteRepository(url string) string {
	if m := githubRemote.FindStringSubmatch(url); m != nil {
		return m[1]
	}
	return ""
}

// DefaultPushTarget is the dashboard's defaultPushTarget: the branch's
// upstream, else origin when the workspace sets an Origin. Repository is
// empty unless that remote has exactly one push URL.
func DefaultPushTarget(remotes []RunGitRemote, upstream *RunGitUpstream, branch string, workspaceOrigin bool) RunGitPushTarget {
	var tracked, origin *RunGitRemote
	for i := range remotes {
		if upstream != nil && remotes[i].Name == upstream.Remote {
			tracked = &remotes[i]
		}
		if workspaceOrigin && remotes[i].Name == "origin" {
			origin = &remotes[i]
		}
	}
	remote, head := origin, branch
	if tracked != nil {
		remote, head = tracked, upstream.Branch
	}
	if remote == nil {
		return RunGitPushTarget{HeadBranch: head}
	}
	target := RunGitPushTarget{Remote: remote.Name, HeadBranch: head}
	if len(remote.PushURLs) == 1 {
		target.Repository = remote.PushURLs[0]
	}
	return target
}

// DefaultPRTarget is the dashboard's defaultPRTarget: push into base on the
// workspace source, which for a fork Origin is its upstream. It is zero when
// push is not to a github.com repository.
func DefaultPRTarget(push RunGitPushTarget, base, source string) RunPRTarget {
	head := GitHubRemoteRepository(push.Repository)
	if head == "" {
		return RunPRTarget{}
	}
	repository := GitHubRemoteRepository(source)
	if repository == "" {
		repository = head
	}
	return RunPRTarget{Repository: repository, BaseBranch: base, HeadRepository: head, HeadBranch: push.HeadBranch}
}
