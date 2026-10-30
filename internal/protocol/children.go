package protocol

// Child run methods on the run socket. The caller is the parent; nothing on
// the wire names it.
const (
	MethodChildOptions = "child.options"
	MethodChildStart   = "child.start"
	MethodChildList    = "child.list"
	MethodChildCancel  = "child.cancel"
	MethodChildFetch   = "child.fetch"
)

// Child run methods on the control channel.
const (
	// MethodChildSettings reads, or with fields set changes, the caller's
	// own child-run setting.
	MethodChildSettings = "child.settings"
	// ErrorReasonChildrenUnavailable is the data.reason of the
	// CodeUnavailable a server without child runs answers their methods
	// with.
	ErrorReasonChildrenUnavailable = "children_unavailable"

	// MethodRunChildren lists a run's descendants with their usage.
	MethodRunChildren = "run.children"
	// MethodRunChildrenAllow sets a run's own child-run setting.
	MethodRunChildrenAllow = "run.children.allow"
	// MethodWorkspaceChildLimits reads, or for an admin with fields set
	// changes, a workspace's child-run limits.
	MethodWorkspaceChildLimits = "workspace.child_limits"
)

// Child states. queued, starting, cancelled and refused come from the
// reservation; the rest from the started run.
const (
	ChildQueued    = "queued"
	ChildStarting  = "starting"
	ChildWorking   = "working"
	ChildWaiting   = "waiting"
	ChildNeedsYou  = "needs-you"
	ChildFinished  = "finished"
	ChildFailed    = "failed"
	ChildEnded     = "ended"
	ChildCancelled = "cancelled"
	ChildRefused   = "refused"
)

// Run child-run settings: "" follows the owner's member setting.
const (
	RunChildrenOn  = "on"
	RunChildrenOff = "off"
)

// ChildLimits bound a parent's child runs: MaxLive are busy at once, more
// queue.
type ChildLimits struct {
	MaxLive int `json:"max_live"`
}

// ChildChoice is one agent a child may run, on one account.
type ChildChoice struct {
	AccountMemberID string   `json:"account_member_id"`
	AccountName     string   `json:"account_name"`
	Harness         string   `json:"harness"`
	Modes           []string `json:"modes"`
	DefaultMode     string   `json:"default_mode"`
	// Model is the exact model a swarm choice fixes; empty with Models
	// empty means any model the agent accepts.
	Model  string   `json:"model,omitempty"`
	Models []string `json:"models,omitempty"`
	// Status is "ready", "exhausted" (usage at the fallback threshold),
	// "not-installed" or "not-signed-in"; Detail is the exact sentence.
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// ChildOptionsParams: Harness asks for that agent's model list, which may
// take a few seconds.
type ChildOptionsParams struct {
	Harness string `json:"harness,omitempty" jsonschema:"list this agent's models too"`
}

// ChildOptionsResult says whether this run may start children, why not,
// and with what.
type ChildOptionsResult struct {
	Allowed bool `json:"allowed"`
	// Reason is the exact refusal child.start would return; empty when
	// allowed.
	Reason string `json:"reason,omitempty"`
	// Scope is "swarm" (the swarm's choices) or "member" (the owner's
	// child-run setting).
	Scope   string        `json:"scope"`
	OwnerID string        `json:"owner_member_id"`
	Limits  ChildLimits   `json:"limits"`
	Busy    int           `json:"busy"`
	Queued  int           `json:"queued"`
	Choices []ChildChoice `json:"choices"`
}

// ChildStartParams starts one child run of the calling run.
type ChildStartParams struct {
	Harness         string `json:"harness" jsonschema:"agent from aether_child_options"`
	Model           string `json:"model,omitempty" jsonschema:"model id; empty keeps the agent's default"`
	Mode            string `json:"mode,omitempty" jsonschema:"standard, enhanced or background; empty picks enhanced where installed"`
	AccountMemberID string `json:"account_member_id,omitempty" jsonschema:"account from aether_child_options; empty is the owner's own"`
	Purpose         string `json:"purpose" jsonschema:"3-8 words shown in the dashboard, e.g. adversarial review of auth change"`
	Task            string `json:"task" jsonschema:"everything the child needs: it sees only this text and your last commit"`
	IdempotencyKey  string `json:"idempotency_key" jsonschema:"a stable key you choose; a retry with the same key returns the first receipt"`
}

// ChildReport is the latest outcome a child reported.
type ChildReport struct {
	Outcome string `json:"outcome"`
	Summary string `json:"summary"`
	At      string `json:"at"`
}

// Child is one child run as its parent and the dashboard see it.
type Child struct {
	RunID           string       `json:"run_id"`
	ParentRunID     string       `json:"parent_run_id"`
	RootRunID       string       `json:"root_run_id"`
	Purpose         string       `json:"purpose"`
	Harness         string       `json:"harness"`
	Mode            string       `json:"mode"`
	Model           string       `json:"model,omitempty"`
	OwnerID         string       `json:"owner_member_id"`
	AccountMemberID string       `json:"account_member_id"`
	State           string       `json:"state"`
	Detached        bool         `json:"detached,omitempty"`
	Branch          string       `json:"branch,omitempty"`
	BaseCommit      string       `json:"base_commit,omitempty"`
	Report          *ChildReport `json:"report,omitempty"`
	Error           string       `json:"error,omitempty"`
	CreatedAt       string       `json:"created_at"`
	// CostUSD is nil when the run reported no cost.
	CostUSD *float64 `json:"cost_usd,omitempty"`
}

// ChildReceipt is child.start's result. Next is what the parent does now.
type ChildReceipt struct {
	Child
	Next string `json:"next"`
}

// ChildListParams: RunID narrows the list to that child.
type ChildListParams struct {
	RunID string `json:"run_id,omitempty" jsonschema:"one child's run_id; empty lists every child"`
}

type ChildListResult struct {
	Children []Child `json:"children"`
}

// ChildRunParams address one child of the calling run.
type ChildRunParams struct {
	RunID string `json:"run_id" jsonschema:"the child's run_id"`
}

type ChildFetchResult struct {
	Ref    string `json:"ref"`
	Commit string `json:"commit"`
}

// ChildSettings is a member's child-run setting; child runs are on until
// the member turns them off.
type ChildSettings struct {
	Enabled bool `json:"enabled"`
}

// ChildSettingsParams: a nil field keeps its value.
type ChildSettingsParams struct {
	Enabled *bool `json:"enabled,omitempty"`
}

type RunChildrenParams struct {
	RunID string `json:"run_id"`
}

// ChildUsage adds up a tree's runs. CostUSD covers metered runs only.
type ChildUsage struct {
	Runs          int            `json:"runs"`
	MeteredRuns   int            `json:"metered_runs"`
	UnmeteredRuns int            `json:"unmetered_runs"`
	InputTokens   int64          `json:"input_tokens"`
	OutputTokens  int64          `json:"output_tokens"`
	CostUSD       float64        `json:"cost_usd"`
	ByAccount     []AccountUsage `json:"by_account"`
}

// AccountUsage is one paying account's share of a tree's metered cost.
type AccountUsage struct {
	AccountMemberID string  `json:"account_member_id"`
	Runs            int     `json:"runs"`
	CostUSD         float64 `json:"cost_usd"`
}

// RunChildrenResult is a run's descendants, nearest first, and the usage
// of the run plus all of them.
type RunChildrenResult struct {
	RootRunID string      `json:"root_run_id"`
	Children  []Child     `json:"children"`
	Limits    ChildLimits `json:"limits"`
	Usage     ChildUsage  `json:"usage"`
}

type RunChildrenAllowParams struct {
	RunID    string `json:"run_id"`
	Children string `json:"children"`
}

type RunChildrenAllowResult struct {
	Children string `json:"children"`
}

type WorkspaceChildLimitsParams struct {
	WorkspaceID string `json:"workspace_id"`
	MaxLive     *int   `json:"max_live,omitempty"`
}
