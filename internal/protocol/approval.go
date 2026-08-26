package protocol

// Wave 4 approval-inbox and presence methods.
const (
	// MethodApprovalList lists a workspace's approval inbox.
	MethodApprovalList = "approval.list"
	// MethodApprovalDecide approves or denies one request (Steer on the
	// request's run).
	MethodApprovalDecide = "approval.decide"
	// MethodPresenceHeartbeat refreshes the caller's presence.
	MethodPresenceHeartbeat = "presence.heartbeat"
	// MethodPresenceRoster reports who is online and what they watch.
	MethodPresenceRoster = "presence.roster"
)

// Approval is the wire form of an approval-inbox request. Decision is
// "requested" until somebody decides it, then "approved" or "denied" with
// DecidedBy naming them.
type Approval struct {
	ID          string  `json:"id"`
	WorkspaceID string  `json:"workspace_id"`
	RunID       string  `json:"run_id"`
	Action      string  `json:"action"`
	Detail      string  `json:"detail,omitempty"`
	Decision    string  `json:"decision"`
	DecidedBy   string  `json:"decided_by,omitempty"`
	CreatedAt   string  `json:"created_at"`
	DecidedAt   *string `json:"decided_at,omitempty"`
}

// ApprovalListParams selects one workspace's inbox; All includes already
// decided requests.
type ApprovalListParams struct {
	WorkspaceID string `json:"workspace_id"`
	All         bool   `json:"all,omitempty"`
}

// ApprovalListResult is the inbox, oldest request first.
type ApprovalListResult struct {
	Approvals []Approval `json:"approvals"`
}

// ApprovalDecideParams decides one request. RunID names the request's run
// and is what the steer capability is checked against.
type ApprovalDecideParams struct {
	RunID     string `json:"run_id"`
	RequestID string `json:"request_id"`
	Approve   bool   `json:"approve"`
}

// ApprovalDecideResult carries the decided request.
type ApprovalDecideResult struct {
	Approval Approval `json:"approval"`
}

// PresenceHeartbeatParams refreshes the caller's presence in a workspace.
type PresenceHeartbeatParams struct {
	WorkspaceID string `json:"workspace_id"`
}

// PresenceHeartbeatResult reports how long presence survives without a
// further heartbeat, so clients pick an interval inside it.
type PresenceHeartbeatResult struct {
	TTLSeconds int `json:"ttl_seconds"`
}

// PresenceRosterParams narrows the roster to a workspace, or to the
// watchers of one run.
type PresenceRosterParams struct {
	WorkspaceID string `json:"workspace_id,omitempty"`
	RunID       string `json:"run_id,omitempty"`
}

// PresenceEntry is one present member. State is "watching" when Watching
// is non-empty, "online" otherwise.
type PresenceEntry struct {
	MemberID string   `json:"member_id"`
	State    string   `json:"state"`
	Watching []string `json:"watching,omitempty"`
	LastSeen string   `json:"last_seen"`
}

// PresenceRosterResult is the live roster, by member ID.
type PresenceRosterResult struct {
	Members []PresenceEntry `json:"members"`
}
