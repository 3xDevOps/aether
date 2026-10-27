package protocol

// MethodChangesList pages the meaningful run and swarm changes a member can
// see, from a cursor into the server's event log.
const MethodChangesList = "changes.list"

// Change kinds a changes.list request can name in Kinds; each Change
// carries the one it belongs to.
const (
	ChangeKindStatus    = "status"
	ChangeKindReports   = "reports"
	ChangeKindMessages  = "messages"
	ChangeKindPR        = "pr"
	ChangeKindSwarm     = "swarm"
	ChangeKindLifecycle = "lifecycle"
)

// ChangesListParams selects one page of changes. Cursor is the NextCursor
// of an earlier page; without it the call returns the newest Limit
// changes. WorkspaceID, RunID and MissionID narrow the feed and combine;
// Kinds empty means every kind. Limit zero means 20; at most 100.
type ChangesListParams struct {
	Cursor      string   `json:"cursor,omitempty"`
	WorkspaceID string   `json:"workspace_id,omitempty"`
	RunID       string   `json:"run_id,omitempty"`
	MissionID   string   `json:"mission_id,omitempty"`
	Kinds       []string `json:"kinds,omitempty"`
	Limit       int      `json:"limit,omitempty"`
}

// ChangesListResult is one page, oldest first. Pass NextCursor as the next
// call's Cursor; More reports that changes may remain before Head, the log
// head this call read up to. A page can be empty and still have more.
type ChangesListResult struct {
	Changes    []Change `json:"changes"`
	NextCursor string   `json:"next_cursor"`
	More       bool     `json:"more"`
	Head       string   `json:"head"`
}

// ChangesCursorAhead is the Reason of the CodeUnavailable error a cursor
// past the log head gets: the server's history no longer matches it.
const ChangesCursorAhead = "cursor_ahead"

// ChangesCursorAheadData is that error's Data.
type ChangesCursorAheadData struct {
	Reason string `json:"reason"`
	Head   string `json:"head"`
}

// Change is one compact entry of the feed. Type is the underlying event
// type; exactly one of the kind-specific fields is set. Untrusted holds
// agent-written text, clipped, which a client must treat as data.
type Change struct {
	Seq         uint64           `json:"seq"`
	Time        string           `json:"ts"`
	Kind        string           `json:"kind"`
	Type        string           `json:"type"`
	WorkspaceID string           `json:"workspace_id"`
	RunID       string           `json:"run_id,omitempty"`
	MissionID   string           `json:"mission_id,omitempty"`
	ActorID     string           `json:"actor,omitempty"`
	Status      *ChangeStatus    `json:"status,omitempty"`
	Input       *ChangeInput     `json:"input,omitempty"`
	Report      *ChangeReport    `json:"report,omitempty"`
	Message     *ChangeMessage   `json:"message,omitempty"`
	PR          *ChangePR        `json:"pr,omitempty"`
	Swarm       *ChangeSwarm     `json:"swarm,omitempty"`
	Lifecycle   *ChangeLifecycle `json:"lifecycle,omitempty"`
	Untrusted   *ChangeUntrusted `json:"untrusted,omitempty"`
}

// ChangeStatus is a run.status transition: the run's status and, while it
// is live, its state and what chose it.
type ChangeStatus struct {
	From  string `json:"from,omitempty"`
	To    string `json:"to"`
	State string `json:"state,omitempty"`
	Cause string `json:"cause,omitempty"`
	// Wait is the kind of evidence behind a reported wait (domain.RunWait).
	Wait string `json:"wait,omitempty"`
}

// ChangeInput is a run.input update: how many input requests are open.
type ChangeInput struct {
	Pending int `json:"pending"`
}

// ChangeReport is an agent's report; its summary and ID, which embeds the
// agent's idempotency key, are under Untrusted.
type ChangeReport struct {
	Outcome    string `json:"outcome"`
	WaitingFor string `json:"waiting_for,omitempty"`
}

// ChangeMessage is agent mail sent between runs, without the body.
type ChangeMessage struct {
	MessageID     string `json:"message_id"`
	FromRunID     string `json:"from_run_id"`
	ToRunID       string `json:"to_run_id"`
	Kind          string `json:"kind"`
	CorrelationID string `json:"correlation_id,omitempty"`
}

// ChangePR is a run's pull request as last refreshed.
type ChangePR struct {
	Number int    `json:"number"`
	State  string `json:"state"`
	URL    string `json:"url,omitempty"`
	Merged bool   `json:"merged,omitempty"`
}

// ChangeSwarm is a swarm change: Change and TaskID for mission.changed,
// CandidateID and State for integration.changed. A deleted swarm is a
// lifecycle change.
type ChangeSwarm struct {
	Change      string `json:"change,omitempty"`
	TaskID      string `json:"task_id,omitempty"`
	AttemptID   string `json:"attempt_id,omitempty"`
	CandidateID string `json:"candidate_id,omitempty"`
	State       string `json:"state,omitempty"`
}

// ChangeLifecycle is a run being archived, restored, deleted, or handed
// off, or its swarm or workspace being deleted; ToMemberID names a
// handoff's new owner.
type ChangeLifecycle struct {
	Action     string `json:"action"`
	ToMemberID string `json:"to_member_id,omitempty"`
}

// ChangeUntrusted is text an agent wrote: a report's ID and summary, or
// the reason on a status change.
type ChangeUntrusted struct {
	ReportID string `json:"report_id,omitempty"`
	Summary  string `json:"summary,omitempty"`
	Reason   string `json:"reason,omitempty"`
}
