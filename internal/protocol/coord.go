package protocol

// Coordination wire v3.
//
// This is the whole surface an agent reaches on its run's coordination
// socket. It is versioned separately from the control channel because the
// bridge lives inside a container that can outlive a server restart, and
// carries its version in every coord.status result.
const CoordWireVersion = "v3"

// The coordination method set. The run.report method remains the harness
// lifecycle hook; coord.report is the durable worker outcome report.
const (
	MethodCoordStatus     = "coord.status"
	MethodCoordHookStatus = "coord.hook.status"
	MethodCoordSend       = "coord.send"
	MethodCoordInbox      = "coord.inbox"
	MethodCoordAsk        = "coord.ask"
	MethodCoordReply      = "coord.reply"
	MethodCoordReport     = "coord.report"
	MethodRunReport       = "run.report"
)

// Coordination caps, enforced by the server and published here so a bridge
// can reject an oversized body before it reaches the wire.
const (
	// CoordMaxBodyBytes is the largest message body coord.send, coord.ask,
	// and coord.reply accepts.
	CoordMaxBodyBytes = 4 << 10
	// CoordMaxUnread is the deepest a run's inbox may get before further
	// sends to it are refused outright.
	CoordMaxUnread = 100
	// CoordMaxInboxWaitSeconds bounds one server-side long poll.
	CoordMaxInboxWaitSeconds = 30
	// CoordMaxSummaryBytes bounds a durable outcome summary.
	CoordMaxSummaryBytes = 4 << 10
	// CoordMaxEvidenceRefs bounds the number of evidence references in one
	// report, while CoordMaxEvidenceRefBytes bounds each reference.
	CoordMaxEvidenceRefs     = 32
	CoordMaxEvidenceRefBytes = 512
	// CoordMaxIdempotencyKeyBytes bounds every mutation identity.
	CoordMaxIdempotencyKeyBytes = 256
)

// Coordination message kinds.
const (
	CoordMessageKindMessage  = "message"
	CoordMessageKindQuestion = "question"
	CoordMessageKindReply    = "reply"
)

// Durable worker outcome values.
const (
	CoordOutcomeSuccess = "success"
	CoordOutcomeFailure = "failure"
	CoordOutcomeBlocked = "blocked"
)

// Peer authorization states.
const (
	// CoordPeerActive is a peer the radar currently has this run in file
	// conflict with.
	CoordPeerActive = "active"
	// CoordPeerGrace is a peer whose overlap cleared but whose grace window
	// has not expired yet, so in-flight replies still land.
	CoordPeerGrace = "grace"
	// CoordPeerMission is a peer authorized by the current mission
	// membership/assignment even when no file overlap exists.
	CoordPeerMission = "mission"
)

// CoordMissionAssignment is the live mission authority for the run behind a
// coordination socket. Role is descriptive authority, never a client-provided
// role flag; an omitted assignment means this is an ordinary run.
type CoordMissionAssignment struct {
	MissionID             string                   `json:"mission_id,omitempty"`
	Role                  string                   `json:"role,omitempty"`
	TaskID                string                   `json:"task_id,omitempty"`
	TaskRevision          int                      `json:"task_revision,omitempty"`
	AttemptID             string                   `json:"attempt_id,omitempty"`
	IntegratorRunID       string                   `json:"integrator_run_id,omitempty"`
	IntegratorGeneration  uint64                   `json:"integrator_generation,omitempty"`
	ExecutionChoices      []MissionExecutionChoice `json:"execution_choices,omitempty"`
	MaxConcurrentAttempts int                      `json:"max_concurrent_attempts,omitempty"`
	MaxTotalAttempts      int                      `json:"max_total_attempts,omitempty"`
	ActiveAttempts        int                      `json:"active_attempts,omitempty"`
	TotalAttempts         int                      `json:"total_attempts,omitempty"`
	Phase                 string                   `json:"phase,omitempty"`
	PlanVersion           uint64                   `json:"plan_version,omitempty"`
	OpenQuestions         int                      `json:"open_questions,omitempty"`
	LatestFeedback        string                   `json:"latest_feedback,omitempty"`
	Capabilities          []string                 `json:"capabilities,omitempty"`
}

// Status output limits are intentionally smaller than the request budget:
// status is safe to call at natural checkpoints and must not turn a large
// radar or assignment into an unbounded response.
const (
	CoordMaxStatusPeers     = 32
	CoordMaxStatusFiles     = 16
	CoordMaxStatusTaskBytes = 512
	CoordMaxStatusPathBytes = 512
)

// CoordPeer is one run the caller is authorized to message. Files are the
// paths both runs touch, empty once the overlap has cleared; ExpiresAt is
// set only in the grace state and is RFC3339.
type CoordPeer struct {
	RunID          string   `json:"run_id"`
	MemberID       string   `json:"member_id"`
	Task           string   `json:"task,omitempty"`
	TaskBytes      int      `json:"task_bytes,omitempty"`
	TaskTruncated  bool     `json:"task_truncated,omitempty"`
	Files          []string `json:"files,omitempty"`
	FileTotal      int      `json:"file_total,omitempty"`
	FilesTruncated bool     `json:"files_truncated,omitempty"`
	State          string   `json:"state"`
	ExpiresAt      string   `json:"expires_at,omitempty"`
}

// CoordStatusResult is the result of coord.status: who the caller is,
// exactly the peers it may message, and how many messages are waiting.
type CoordStatusResult struct {
	WireVersion    string                  `json:"wire_version"`
	RunID          string                  `json:"run_id"`
	WorkspaceID    string                  `json:"workspace_id"`
	MemberID       string                  `json:"member_id"`
	Task           string                  `json:"task,omitempty"`
	TaskBytes      int                     `json:"task_bytes,omitempty"`
	TaskTruncated  bool                    `json:"task_truncated,omitempty"`
	Assignment     *CoordMissionAssignment `json:"assignment,omitempty"`
	Peers          []CoordPeer             `json:"peers"`
	PeerTotal      int                     `json:"peer_total,omitempty"`
	PeersTruncated bool                    `json:"peers_truncated,omitempty"`
	Unread         int                     `json:"unread"`
	Capabilities   []string                `json:"capabilities"`
}

// CoordSendParams are the params of coord.send. The sender is the socket,
// never a parameter. IdempotencyKey makes retries return the original
// message receipt without creating another row.
type CoordSendParams struct {
	ToRunID        string `json:"to_run_id"`
	Body           string `json:"body"`
	IdempotencyKey string `json:"idempotency_key"`
}

// CoordSendResult is the result of coord.send.
type CoordSendResult struct {
	MessageID string `json:"message_id"`
}

// CoordAskParams are the params of coord.ask.
type CoordAskParams struct {
	ToRunID        string `json:"to_run_id"`
	Body           string `json:"body"`
	IdempotencyKey string `json:"idempotency_key"`
}

// CoordAskResult is the result of coord.ask.
type CoordAskResult struct {
	QuestionID string `json:"question_id"`
}

// CoordReplyParams are the params of coord.reply. The question ID identifies
// both the recipient and the authorization relationship.
type CoordReplyParams struct {
	QuestionID     string `json:"question_id"`
	Body           string `json:"body"`
	IdempotencyKey string `json:"idempotency_key"`
}

// CoordReplyResult is the result of coord.reply.
type CoordReplyResult struct {
	MessageID string `json:"message_id"`
}

// CoordInboxParams are the params of coord.inbox. AckToken is the token
// of the previous read's batch; omitting it acknowledges nothing, so the
// same batch is delivered again. WaitSeconds requests a bounded server-side
// long poll when no batch is ready.
type CoordInboxParams struct {
	AckToken    string `json:"ack_token,omitempty"`
	WaitSeconds int    `json:"wait_seconds,omitempty"`
}

// CoordMessage is one delivered message.
type CoordMessage struct {
	ID            string `json:"id"`
	Kind          string `json:"kind"`
	CorrelationID string `json:"correlation_id,omitempty"`
	FromRunID     string `json:"from_run_id"`
	Body          string `json:"body"`
	CreatedAt     string `json:"created_at"`
}

// CoordInboxResult is the result of coord.inbox: one batch, oldest first,
// and the token that acknowledges exactly it. An empty inbox returns an
// empty message list and no token.
type CoordInboxResult struct {
	Messages []CoordMessage `json:"messages"`
	AckToken string         `json:"ack_token,omitempty"`
}

// CoordReportParams submits a durable worker outcome for the calling run.
type CoordReportParams struct {
	Outcome        string   `json:"outcome"`
	Summary        string   `json:"summary"`
	EvidenceRefs   []string `json:"evidence_refs,omitempty"`
	IdempotencyKey string   `json:"idempotency_key"`
}

// CoordReportResult is the complete durable outcome receipt. EvidenceRef is
// the automatically captured packet; EvidenceRefs includes caller references
// plus that packet reference, all bounded and validated by the server.
type CoordReportResult struct {
	ReportID     string   `json:"report_id"`
	Outcome      string   `json:"outcome,omitempty"`
	Summary      string   `json:"summary,omitempty"`
	NextAction   string   `json:"next_action,omitempty"`
	EvidenceRef  string   `json:"evidence_ref,omitempty"`
	EvidenceRefs []string `json:"evidence_refs,omitempty"`
}

// RunReportParams are the params of run.report: what the agent behind this
// run's socket says it is doing now, and the user-visible reason it gives
// for needing its member. State is one of the two agentstatus values; the
// run is the socket, never a parameter.
type RunReportParams struct {
	State  string `json:"state"`
	Reason string `json:"reason"`
}

// RunReportResult is the result of run.report. It is empty: the caller is
// a hook inside the container that exits immediately either way.
type RunReportResult struct{}
