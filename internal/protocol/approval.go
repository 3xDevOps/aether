package protocol

// Wave 4 presence methods.
const (
	// MethodPresenceHeartbeat refreshes the caller's presence.
	MethodPresenceHeartbeat = "presence.heartbeat"
	// MethodPresenceRoster reports who is online and what they watch.
	MethodPresenceRoster = "presence.roster"
	// MethodPresenceTyping tells the watchers of a run that the caller is
	// typing in its composer (Steer on the run).
	MethodPresenceTyping = "presence.typing"
)

// PresenceHeartbeatParams refreshes the caller's presence in a workspace.
type PresenceHeartbeatParams struct {
	WorkspaceID string `json:"workspace_id"`
}

// PresenceHeartbeatResult reports how long presence survives without a
// further heartbeat, so clients pick an interval inside it.
type PresenceHeartbeatResult struct {
	TTLSeconds int `json:"ttl_seconds"`
}

// PresenceTypingParams names the run whose composer the caller is typing in.
// Stopped reports that they sent or cleared their draft instead.
type PresenceTypingParams struct {
	RunID   string `json:"run_id"`
	Stopped bool   `json:"stopped,omitempty"`
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
