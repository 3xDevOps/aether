package protocol

// Launch defaults methods act only on the caller's own saved choices,
// admins included. Saved defaults grant nothing: every launch is authorized
// as run.launch is.
const (
	MethodLaunchDefaultsGet = "launch.defaults.get"
	MethodLaunchDefaultsSet = "launch.defaults.set"
)

const (
	LaunchDefaultsMaxAgents   = 32
	LaunchDefaultsMaxFallback = 8
	// LaunchDefaultsMaxAgentName is in bytes.
	LaunchDefaultsMaxAgentName = 64
)

const (
	FallbackAutomatic = "automatic"
	FallbackOrdered   = "ordered"
	FallbackOff       = "off"
)

// The surfaces a save is recorded from: the built-in assistant, or the
// member's own client (the dashboard or the aether CLI).
const (
	LaunchSourceMember    = "member"
	LaunchSourceAssistant = "assistant"
)

// LaunchDefaults are the launch choices a member saved. Empty fields were
// not chosen and keep run.launch's own defaults.
type LaunchDefaults struct {
	// Agent is the agent a launch preselects.
	Agent string `json:"agent,omitempty"`
	// Agents are the mode and model last chosen for each agent, keyed by
	// agent name.
	Agents map[string]AgentLaunchDefault `json:"agents,omitempty"`
	// AccountMemberID is the account to launch on; empty is the caller's
	// own.
	AccountMemberID string                 `json:"account_member_id,omitempty"`
	Fallback        *LaunchFallbackDefault `json:"fallback,omitempty"`
}

// AgentLaunchDefault.Mode is "tui", "headless", "acp" or empty.
type AgentLaunchDefault struct {
	Mode  string `json:"mode,omitempty"`
	Model string `json:"model,omitempty"`
}

// LaunchFallbackDefault is how a launch moves to another account when the
// requested one's usage is exhausted: "automatic" tries every account the
// caller may launch on, "ordered" only Order, and "off" none, as
// run.launch's fallback_account_member_ids and no_fallback.
type LaunchFallbackDefault struct {
	Policy string   `json:"policy"`
	Order  []string `json:"order,omitempty"`
}

// LaunchDefaultsSetParams replaces the caller's saved defaults with
// Defaults, or deletes them with Clear. Revision is the one the caller last
// read, 0 when none were saved; a stale one is refused with CodeConflict
// and the current LaunchDefaultsResult as the error's data.
type LaunchDefaultsSetParams struct {
	Defaults *LaunchDefaults `json:"defaults,omitempty"`
	Clear    bool            `json:"clear,omitempty"`
	Revision int64           `json:"revision"`
}

// LaunchDefaultsResult is the caller's saved defaults, checked when read.
// Revision 0 means none are saved. Invalid lists each saved choice a launch
// could not use now and Unknown each one it could not check; both
// stay saved until replaced.
type LaunchDefaultsResult struct {
	Revision  int64                `json:"revision"`
	Defaults  LaunchDefaults       `json:"defaults"`
	Source    string               `json:"source,omitempty"`
	UpdatedAt string               `json:"updated_at,omitempty"`
	Invalid   []LaunchDefaultIssue `json:"invalid,omitempty"`
	Unknown   []LaunchDefaultIssue `json:"unknown,omitempty"`
}

// LaunchDefaultIssue names a saved field ("agent", "agents.<name>",
// "agents.<name>.mode", "agents.<name>.model", "account_member_id" or
// "fallback.order"), its value and why it is invalid or unknown.
type LaunchDefaultIssue struct {
	Field  string `json:"field"`
	Value  string `json:"value"`
	Reason string `json:"reason"`
}
