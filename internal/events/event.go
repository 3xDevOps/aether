// Package events is the in-process pub/sub backbone: every state change in
// Aether (run status, diffs, presence, approvals, costs, timeline entries)
// is published here and all features are consumers. The Bus interface is a
// deliberate seam - in-process today, externalizable if a control plane ever
// splits out. Workspace-scoped events are additionally persisted append-only
// to an EventLog so late subscribers can replay from a cursor.
package events

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// Type identifies the kind of event carried in an envelope. Each Type has
// exactly one payload Go type.
type Type string

const (
	// TypeRunStatus signals a run lifecycle transition.
	TypeRunStatus Type = "run.status"
	// TypeRunTitle carries the latest terminal title for a run.
	TypeRunTitle Type = "run.title"
	// TypeRunDiff carries a periodic diff snapshot of a run's worktree.
	TypeRunDiff Type = "run.diff"
	// TypeRunCost carries token usage and cost attribution for a run.
	TypeRunCost Type = "run.cost"
	// TypePresence signals a member coming online, going offline, or
	// watching a run.
	TypePresence Type = "workspace.presence"
	// TypeApproval carries approval-inbox activity: a permission request
	// surfacing or a member deciding it.
	TypeApproval Type = "workspace.approval"
	// TypeTimeline carries workspace timeline entries, including steering
	// acts (inject, pause, kill, handoff) and free-form notes.
	TypeTimeline Type = "workspace.timeline"
	// TypeGitBranch signals that a branch in a workspace's bare repo moved.
	// Wave 1 publishes it for run branches only; the envelope's RunID names
	// the run whose branch moved.
	TypeGitBranch Type = "git.branch"
	// TypeAgentEvent carries structured agent activity translated from a
	// harness's machine-readable output by its run adapter: tool calls,
	// tool results, subagent spawns, plan/approval pauses, and resume
	// metadata. Only headless runs whose harness has an adapter emit it;
	// runs without one degrade to the PTY + diff timeline and no feature
	// may hard-require these events.
	TypeAgentEvent Type = "run.agent"
	// TypeProfile records a member+harness profile snapshot change
	// (put, rollback, or pin). Publishing still requires a WorkspaceID;
	// callers without one skip the bus rather than inventing a workspace.
	TypeProfile Type = "profile.change"
	// TypeSyncConflict signals a paused live file overlay: concurrent
	// local and run-worktree edits to the same paths. The overlay writes
	// conflict twins and stops; resuming is rerunning `aether sync`.
	TypeSyncConflict Type = "sync.conflict"
)

// Payload is the typed body of an event. Implementations are plain structs;
// EventType binds each payload type to its envelope Type.
type Payload interface {
	EventType() Type
}

// Event is the envelope every payload travels in.
//
// Seq is the global, monotonically increasing sequence cursor assigned by
// the bus at publish time; it orders all events and is the cursor replay
// resumes from. WorkspaceID is required - publishing without it fails with
// ErrNoWorkspace - so with an EventLog attached every event is persisted
// and sequence cursors remain valid across restarts. (Without a log nothing
// is persisted, replay is unavailable, and cursors are process-local.)
// RunID and ActorID are set where relevant and empty otherwise.
type Event struct {
	ID          string
	Seq         uint64
	Time        time.Time
	WorkspaceID domain.WorkspaceID
	RunID       domain.RunID
	ActorID     domain.MemberID
	Type        Type
	Payload     Payload
}

// RunStatusPayload reports a run lifecycle transition.
type RunStatusPayload struct {
	From domain.RunStatus `json:"from,omitempty"`
	To   domain.RunStatus `json:"to"`
	// Reason is an optional human-readable cause, e.g. "agent exited 1".
	Reason string `json:"reason,omitempty"`
}

// RunTitlePayload reports the latest terminal title for a run.
type RunTitlePayload struct {
	Title string `json:"title"`
}

func (RunTitlePayload) EventType() Type { return TypeRunTitle }

func (RunStatusPayload) EventType() Type { return TypeRunStatus }

// FileDiffStat summarizes changes to one file within a diff snapshot.
type FileDiffStat struct {
	Path      string `json:"path"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
}

// RunDiffPayload is a point-in-time snapshot of a run worktree's diff
// against its base, taken on file-change quiescence.
type RunDiffPayload struct {
	Files []FileDiffStat `json:"files"`
	// Tree is the git tree recorded for this snapshot: the whole worktree's
	// content at that instant. Empty when the tree could not be written, or
	// on events from a server that predates snapshot trees.
	Tree string `json:"tree,omitempty"`
	// ParentTree is the previous snapshot's tree, or the run's fork-point
	// tree for the first snapshot. Diffing ParentTree to Tree is what this
	// interval changed.
	ParentTree string `json:"parent_tree,omitempty"`
}

func (RunDiffPayload) EventType() Type { return TypeRunDiff }

// RunCostPayload reports token usage for a run. Metered is false for
// PTY-only runs where counts are best-effort estimates or unavailable.
type RunCostPayload struct {
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	Metered      bool    `json:"metered"`
}

func (RunCostPayload) EventType() Type { return TypeRunCost }

// PresenceState is a member's presence status within a workspace.
type PresenceState string

const (
	PresenceOnline   PresenceState = "online"
	PresenceOffline  PresenceState = "offline"
	PresenceWatching PresenceState = "watching"
)

// PresencePayload reports a presence change for the acting member. When
// State is PresenceWatching the envelope's RunID names the watched run.
type PresencePayload struct {
	State PresenceState `json:"state"`
}

func (PresencePayload) EventType() Type { return TypePresence }

// ApprovalDecision is the state of an approval-inbox request.
type ApprovalDecision string

const (
	ApprovalRequested ApprovalDecision = "requested"
	ApprovalApproved  ApprovalDecision = "approved"
	ApprovalDenied    ApprovalDecision = "denied"
)

// ApprovalPayload carries approval-inbox activity. RequestID ties the
// decision events back to the originating request.
type ApprovalPayload struct {
	RequestID string           `json:"request_id"`
	Action    string           `json:"action"`
	Decision  ApprovalDecision `json:"decision"`
}

func (ApprovalPayload) EventType() Type { return TypeApproval }

// TimelineKind classifies a workspace timeline entry.
type TimelineKind string

const (
	TimelineSteer   TimelineKind = "steer"
	TimelinePause   TimelineKind = "pause"
	TimelineResume  TimelineKind = "resume"
	TimelineKill    TimelineKind = "kill"
	TimelineHandoff TimelineKind = "handoff"
	TimelineNote    TimelineKind = "note"
)

// TimelinePayload is a workspace timeline / steering entry: a privileged or
// noteworthy act stamped into the audit stream, attributed via the
// envelope's ActorID.
type TimelinePayload struct {
	Kind TimelineKind `json:"kind"`
	// Message is the entry body, e.g. the injected instruction text.
	Message string `json:"message,omitempty"`
}

func (TimelinePayload) EventType() Type { return TypeTimeline }

// GitBranchPayload reports a branch update in a workspace repo.
type GitBranchPayload struct {
	WorkspaceID domain.WorkspaceID `json:"workspace_id"`
	Branch      string             `json:"branch"`
	Commit      string             `json:"commit"`
}

func (GitBranchPayload) EventType() Type { return TypeGitBranch }

// AgentEventKind classifies an agent activity event.
type AgentEventKind string

const (
	// AgentToolCall is the agent invoking a tool.
	AgentToolCall AgentEventKind = "tool_call"
	// AgentToolResult is a tool invocation completing.
	AgentToolResult AgentEventKind = "tool_result"
	// AgentSubagent is the agent spawning a subagent.
	AgentSubagent AgentEventKind = "subagent"
	// AgentPause is the agent pausing for plan review or approval.
	AgentPause AgentEventKind = "pause"
	// AgentSession carries the harness-native session identifier that
	// makes the run resumable after an interruption (e.g. relaunching
	// with claude --resume <id>).
	AgentSession AgentEventKind = "session"
)

// AgentEventPayload reports one structured agent activity item, decoded
// from the harness's native output stream by its adapter. Token usage is
// not carried here: adapters report it as a RunCostPayload.
type AgentEventPayload struct {
	Kind AgentEventKind `json:"kind"`
	// Tool is the tool name for tool_call and subagent kinds; empty for
	// tool_result (the harness's result records carry only the ID).
	Tool string `json:"tool,omitempty"`
	// ToolUseID correlates a tool_result with its originating tool_call
	// or subagent event.
	ToolUseID string `json:"tool_use_id,omitempty"`
	// Detail is a short human-readable summary - the command, file path,
	// subagent task, or plan text - truncated by the adapter.
	Detail string `json:"detail,omitempty"`
	// IsError marks a failed tool_result.
	IsError bool `json:"is_error,omitempty"`
	// HarnessSessionID is the harness-native session identifier
	// (session kind only).
	HarnessSessionID string `json:"harness_session_id,omitempty"`
}

func (AgentEventPayload) EventType() Type { return TypeAgentEvent }

// ProfileAction is a profile.change payload action.
type ProfileAction string

const (
	ProfileActionPut      ProfileAction = "put"
	ProfileActionRollback ProfileAction = "rollback"
	ProfileActionPin      ProfileAction = "pin"
)

// ProfilePayload records a profile snapshot mutation. The bus still
// requires WorkspaceID on the envelope; the profile service skips publish
// when it has no workspace rather than inventing one.
type ProfilePayload struct {
	Member     domain.MemberID          `json:"member"`
	Harness    string                   `json:"harness"`
	SnapshotID domain.ProfileSnapshotID `json:"snapshot_id"`
	Action     ProfileAction            `json:"action"`
}

func (ProfilePayload) EventType() Type { return TypeProfile }

// SyncConflictPayload reports a live-overlay sync conflict. The overlay
// session is paused when this is published; Files are the conflicted
// paths relative to the sync roots, and Members are the affected members
// (the syncing member and the run owner, deduplicated when identical).
type SyncConflictPayload struct {
	RunID         domain.RunID      `json:"run_id"`
	SyncSessionID string            `json:"sync_session_id"`
	Files         []string          `json:"files"`
	Members       []domain.MemberID `json:"members"`
}

func (SyncConflictPayload) EventType() Type { return TypeSyncConflict }

func decodeAs[P Payload](data []byte) (Payload, error) {
	var p P
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	return p, nil
}

var payloadCodecs = map[Type]func([]byte) (Payload, error){
	TypeRunStatus:    decodeAs[RunStatusPayload],
	TypeRunTitle:     decodeAs[RunTitlePayload],
	TypeRunDiff:      decodeAs[RunDiffPayload],
	TypeRunCost:      decodeAs[RunCostPayload],
	TypePresence:     decodeAs[PresencePayload],
	TypeApproval:     decodeAs[ApprovalPayload],
	TypeTimeline:     decodeAs[TimelinePayload],
	TypeGitBranch:    decodeAs[GitBranchPayload],
	TypeAgentEvent:   decodeAs[AgentEventPayload],
	TypeProfile:      decodeAs[ProfilePayload],
	TypeSyncConflict: decodeAs[SyncConflictPayload],
}

// registerPayload registers the decoder for a payload type declared
// outside this file. Call it from an init in the file that declares the
// type, so a new event type needs no edit here.
func registerPayload[P Payload](t Type) {
	if _, dup := payloadCodecs[t]; dup {
		panic("events: duplicate payload codec: " + string(t))
	}
	payloadCodecs[t] = decodeAs[P]
}

// DecodePayload reconstructs the typed payload for t from its JSON
// encoding, as stored by an EventLog.
func DecodePayload(t Type, data []byte) (Payload, error) {
	codec, ok := payloadCodecs[t]
	if !ok {
		return nil, fmt.Errorf("events: unknown event type %q", t)
	}
	p, err := codec(data)
	if err != nil {
		return nil, fmt.Errorf("events: decode %q payload: %w", t, err)
	}
	return p, nil
}

func newEventID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("events: crypto/rand failed: %v", err))
	}
	return "evt_" + hex.EncodeToString(b[:])
}
