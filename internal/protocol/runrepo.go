package protocol

const (
	MethodWorkspaceImport   = "workspace.import"
	MethodRunGitStatus      = "run.git.status"
	MethodRunGitDiff        = "run.git.diff"
	MethodRunGitCommit      = "run.git.commit"
	MethodRunGitPush        = "run.git.push"
	MethodRunPRStatus       = "run.pr.status"
	MethodRunPRCreate       = "run.pr.create"
	MethodRunPRFeedback     = "run.pr.feedback"
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
// workspace.mirror.adopt with its returned generation.
type WorkspaceImportParams struct {
	Name        string               `json:"name"`
	Environment WorkspaceEnvironment `json:"environment"`
	SourceURL   string               `json:"source_url"`
	BaseBranch  string               `json:"base_branch"`
	Origin      string               `json:"origin"`
	Auth        string               `json:"auth"`
	KnownHosts  string               `json:"known_hosts,omitempty"`
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
	Branch          string               `json:"branch"`
	Head            string               `json:"head"`
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
	RunID  string   `json:"run_id"`
	Paths  []string `json:"paths,omitempty"`
	Staged bool     `json:"staged,omitempty"`
}

type RunGitDiffResult struct {
	State  RunGitExpected       `json:"state"`
	Output RunRepoCommandOutput `json:"output"`
	Error  string               `json:"error,omitempty"`
}

type RunGitCommitParams struct {
	RunID    string         `json:"run_id"`
	Expected RunGitExpected `json:"expected"`
	Paths    []string       `json:"paths"`
	Message  string         `json:"message"`
}

type RunGitCommitResult struct {
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

// Repository is the explicit push URL of Remote, NOT the source mirror or PR
// repository. HeadBranch is the exact remote destination; force is unsupported.
type RunGitPushTarget struct {
	Remote     string `json:"remote"`
	Repository string `json:"repository"`
	HeadBranch string `json:"head_branch"`
}

type RunGitPushParams struct {
	RunID    string           `json:"run_id"`
	Expected RunGitExpected   `json:"expected"`
	Target   RunGitPushTarget `json:"target"`
}

type RunGitPushResult struct {
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

type RunPRStatusParams struct {
	RunID    string         `json:"run_id"`
	Expected RunGitExpected `json:"expected"`
	Target   RunPRTarget    `json:"target"`
}

type RunPRCreateParams struct {
	RunID         string         `json:"run_id"`
	Expected      RunGitExpected `json:"expected"`
	Target        RunPRTarget    `json:"target"`
	Title         string         `json:"title"`
	Body          string         `json:"body"`
	Draft         bool           `json:"draft,omitempty"`
	ExpectedLogin string         `json:"expected_login,omitempty"`
}

type RunPullRequest struct {
	Number         int    `json:"number"`
	URL            string `json:"url"`
	State          string `json:"state"`
	Title          string `json:"title"`
	Draft          bool   `json:"draft"`
	Repository     string `json:"repository"`
	BaseBranch     string `json:"base_branch"`
	HeadRepository string `json:"head_repository"`
	HeadBranch     string `json:"head_branch"`
	HeadOID        string `json:"head_oid"`
}

// Status always comes from GitHub, including PRs created by native gh.
type RunPRStatusResult struct {
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
	Identity        string               `json:"identity"`
	PullRequest     *RunPullRequest      `json:"pull_request"`
	Checks          []RunPRCheck         `json:"checks"`
	Comments        []RunPRComment       `json:"comments"`
	Reviews         []RunPRReview        `json:"reviews"`
	ReviewComments  []RunPRReviewComment `json:"review_comments"`
	AccountMemberID string               `json:"account_member_id"`
	Actual          *RunGitExpected      `json:"actual,omitempty"`
	Truncated       bool                 `json:"truncated"`
	Output          RunRepoCommandOutput `json:"output"`
	Error           string               `json:"error,omitempty"`
}
