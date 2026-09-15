package protocol

import (
	"time"

	"github.com/3xDevOps/Aether/internal/store"
)

// RoomMessageKind and RoomMessageState mirror the durable store vocabulary
// without exposing any server-only fields.
type RoomMessageKind string

type RoomMessageState string

const (
	RoomMessageComment      RoomMessageKind = "comment"
	RoomMessageSteerRequest RoomMessageKind = "steer_request"
	RoomMessageQuestion     RoomMessageKind = "question"
	RoomMessageReply        RoomMessageKind = "reply"
	RoomMessageSystem       RoomMessageKind = "system"

	RoomMessageQueued    RoomMessageState = "queued"
	RoomMessageSent      RoomMessageState = "sent"
	RoomMessageNotSent   RoomMessageState = "not_sent"
	RoomMessageUncertain RoomMessageState = "uncertain"
	RoomMessageDenied    RoomMessageState = "denied"
	RoomMessageCancelled RoomMessageState = "cancelled"
)

// RoomMessageAnchor is a safe, bounded reference to a diff interval or
// transcript offset. It has no host filesystem path.
type RoomMessageAnchor struct {
	Kind             string `json:"kind,omitempty"`
	Path             string `json:"path,omitempty"`
	StartLine        int    `json:"start_line,omitempty"`
	EndLine          int    `json:"end_line,omitempty"`
	TranscriptOffset int64  `json:"transcript_offset,omitempty"`
}

// RoomMessageFailure carries only sanitized delivery failure fields.
type RoomMessageFailure struct {
	Code      string `json:"code,omitempty"`
	Message   string `json:"message,omitempty"`
	Retryable bool   `json:"retryable,omitempty"`
}

// RoomMessage is the wire representation of one run-room message. Attachments
// are opaque container-visible references; host paths and secrets are not
// represented by this type.
type RoomMessage struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	RunID       string `json:"run_id"`
	ActorID     string `json:"actor_id"`
	// ActorDisplayName is the immutable display snapshot captured when the
	// message was authored. It remains available after member removal.
	ActorDisplayName string              `json:"actor_display_name,omitempty"`
	Body             string              `json:"body"`
	Kind             RoomMessageKind     `json:"kind"`
	Attachments      []string            `json:"attachments,omitempty"`
	Anchor           *RoomMessageAnchor  `json:"anchor,omitempty"`
	CorrelationID    string              `json:"correlation_id,omitempty"`
	IdempotencyKey   string              `json:"idempotency_key,omitempty"`
	State            RoomMessageState    `json:"state"`
	DeliverAfter     *string             `json:"deliver_after,omitempty"`
	DecidedBy        string              `json:"decided_by,omitempty"`
	DecidedAt        *string             `json:"decided_at,omitempty"`
	DeliveredAt      *string             `json:"delivered_at,omitempty"`
	Failure          *RoomMessageFailure `json:"failure,omitempty"`
	CreatedAt        string              `json:"created_at"`
	UpdatedAt        string              `json:"updated_at"`
}

// RoomMessageListResult carries a bounded page.
// NextBefore is an opaque cursor returned by the store.
type RoomMessageListResult struct {
	Messages   []RoomMessage `json:"messages"`
	NextBefore string        `json:"next_before,omitempty"`
}

// Human-facing collaboration methods on the control channel.
const (
	MethodRunRoomList   = "run.room.list"
	MethodRunRoomStatus = "run.room.status"
	MethodRunRoomPost   = "run.room.post"
	MethodRunRoomDecide = "run.room.decide"
)

// Collaboration list and payload bounds are shared by room and evidence handlers.
const (
	CollaborationDefaultPageSize        = 50
	CollaborationMaxPageSize            = 100
	CollaborationMaxBodyBytes           = 64 << 10
	CollaborationMaxIdempotencyKeyBytes = 256
	CollaborationMaxCursorBytes         = 512
	CollaborationMaxAttachmentBytes     = 512
	CollaborationMaxAttachments         = 8
)

type RunRoomListParams struct {
	WorkspaceID string `json:"workspace_id"`
	RunID       string `json:"run_id"`
	Before      string `json:"before,omitempty"`
	Limit       int    `json:"limit,omitempty"`
}

type RunRoomStatusParams struct {
	WorkspaceID string `json:"workspace_id"`
	RunID       string `json:"run_id"`
}

type RunRoomPostParams struct {
	WorkspaceID       string             `json:"workspace_id"`
	RunID             string             `json:"run_id"`
	Kind              RoomMessageKind    `json:"kind"`
	Body              string             `json:"body"`
	Attachments       []string           `json:"attachments,omitempty"`
	Anchor            *RoomMessageAnchor `json:"anchor,omitempty"`
	CorrelationID     string             `json:"correlation_id,omitempty"`
	IdempotencyKey    string             `json:"idempotency_key"`
	ControlSessionID  string             `json:"control_session_id,omitempty"`
	ControlGeneration uint64             `json:"control_generation,omitempty"`
}

type RunRoomDecideParams struct {
	MessageID         string `json:"message_id"`
	Decision          string `json:"decision"`
	ControlSessionID  string `json:"control_session_id"`
	ControlGeneration uint64 `json:"control_generation"`
}

type RoomController struct {
	MemberID   string `json:"member_id"`
	Connected  bool   `json:"connected"`
	AcquiredAt string `json:"acquired_at"`
	ExpiresAt  string `json:"expires_at,omitempty"`
}

type RunRoomStatusResult struct {
	WorkspaceID  string          `json:"workspace_id"`
	RunID        string          `json:"run_id"`
	Protected    bool            `json:"protected"`
	Controller   *RoomController `json:"controller,omitempty"`
	Watchers     []string        `json:"watchers"`
	QueuedSteers int             `json:"queued_steers"`
}

type RunRoomPostResult struct {
	Message RoomMessage `json:"message"`
	Receipt string      `json:"receipt,omitempty"`
}

type RunRoomDecideResult struct {
	Message RoomMessage `json:"message"`
	Receipt string      `json:"receipt,omitempty"`
}

func collaborationTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func collaborationTimePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := collaborationTime(*t)
	return &s
}

func roomMessageAnchorFromStore(a *store.RoomAnchor) *RoomMessageAnchor {
	if a == nil {
		return nil
	}
	return &RoomMessageAnchor{
		Kind: a.Kind, Path: a.Path, StartLine: a.StartLine,
		EndLine: a.EndLine, TranscriptOffset: a.TranscriptOffset,
	}
}

func roomMessageFailureFromStore(f *store.RoomMessageFailure) *RoomMessageFailure {
	if f == nil {
		return nil
	}
	return &RoomMessageFailure{Code: f.Code, Message: f.Message, Retryable: f.Retryable}
}

// RoomMessageFromStore converts a durable message without adding host paths.
func RoomMessageFromStore(m *store.RoomMessage) RoomMessage {
	if m == nil {
		return RoomMessage{}
	}
	attachments := append([]string(nil), m.Attachments...)
	return RoomMessage{
		ID: m.ID, WorkspaceID: string(m.WorkspaceID), RunID: string(m.RunID), ActorID: string(m.ActorID),
		ActorDisplayName: m.ActorDisplayName,
		Kind:             RoomMessageKind(m.Kind), Body: m.Body, Attachments: attachments,
		Anchor: roomMessageAnchorFromStore(m.Anchor), CorrelationID: m.CorrelationID,
		IdempotencyKey: m.IdempotencyKey, State: RoomMessageState(m.State),
		DeliverAfter: collaborationTimePtr(m.DeliverAfter), DecidedBy: string(m.DecidedBy),
		DecidedAt: collaborationTimePtr(m.DecidedAt), DeliveredAt: collaborationTimePtr(m.DeliveredAt),
		Failure:   roomMessageFailureFromStore(m.Failure),
		CreatedAt: collaborationTime(m.CreatedAt), UpdatedAt: collaborationTime(m.UpdatedAt),
	}
}

func RoomMessagePageFromStore(page *store.RoomMessagePage) RoomMessageListResult {
	if page == nil {
		return RoomMessageListResult{}
	}
	out := RoomMessageListResult{Messages: make([]RoomMessage, len(page.Items)), NextBefore: page.NextBefore}
	for i, m := range page.Items {
		out.Messages[i] = RoomMessageFromStore(m)
	}
	return out
}
