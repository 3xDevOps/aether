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

// EvidenceTrigger mirrors the durable evidence packet trigger vocabulary.
type EvidenceTrigger string

const (
	EvidenceFinish  EvidenceTrigger = "finish"
	EvidenceHandoff EvidenceTrigger = "handoff"
	EvidenceReport  EvidenceTrigger = "report"
)

type EvidenceOriginKind string

const (
	EvidenceOriginHuman  EvidenceOriginKind = "human"
	EvidenceOriginRun    EvidenceOriginKind = "run"
	EvidenceOriginServer EvidenceOriginKind = "server"
)

type EvidenceOrigin struct {
	Kind EvidenceOriginKind `json:"kind"`
	ID   string             `json:"id"`
}

type EvidencePacketAvailability string

const (
	EvidenceAvailable EvidencePacketAvailability = "available"
	EvidenceExpired   EvidencePacketAvailability = "expired"
)

type ChangedFileFact struct {
	Path      string `json:"path"`
	Status    string `json:"status,omitempty"`
	Additions int    `json:"additions,omitempty"`
	Deletions int    `json:"deletions,omitempty"`
}

type EvidenceSourceFact struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
	Truncated bool   `json:"truncated,omitempty"`
	Reason    string `json:"reason,omitempty"`
}
type EvidencePacket struct {
	ID                    string                     `json:"id"`
	WorkspaceID           string                     `json:"workspace_id"`
	RunID                 string                     `json:"run_id"`
	Origin                EvidenceOrigin             `json:"origin"`
	OwnerID               string                     `json:"owner_id,omitempty"`
	CreatorID             string                     `json:"creator_id,omitempty"`
	Trigger               EvidenceTrigger            `json:"trigger"`
	Objective             string                     `json:"objective"`
	CapturedAt            string                     `json:"captured_at"`
	ExpiresAt             *string                    `json:"expires_at,omitempty"`
	Availability          EvidencePacketAvailability `json:"availability"`
	ExpiredAt             *string                    `json:"expired_at,omitempty"`
	EventBoundary         uint64                     `json:"event_boundary"`
	BaseRevision          string                     `json:"base_revision,omitempty"`
	RetainedRevision      string                     `json:"retained_revision,omitempty"`
	ChangedFiles          []ChangedFileFact          `json:"changed_files,omitempty"`
	Sources               []EvidenceSourceFact       `json:"sources,omitempty"`
	RelatedRoomMessageIDs []string                   `json:"related_room_message_ids,omitempty"`
	UnresolvedFacts       []string                   `json:"unresolved_facts,omitempty"`
	NextAction            string                     `json:"next_action,omitempty"`
	Provenance            string                     `json:"provenance,omitempty"`
	IdempotencyKey        string                     `json:"idempotency_key,omitempty"`
	CreatedAt             string                     `json:"created_at"`
	UpdatedAt             string                     `json:"updated_at"`
}

// RoomMessageListResult and EvidencePacketListResult carry bounded pages.
// NextBefore is an opaque cursor returned by the store.
type RoomMessageListResult struct {
	Messages   []RoomMessage `json:"messages"`
	NextBefore string        `json:"next_before,omitempty"`
}

type EvidencePacketListResult struct {
	Packets    []EvidencePacket `json:"packets"`
	NextBefore string           `json:"next_before,omitempty"`
}

// Human-facing collaboration methods on the control channel.
const (
	MethodRunRoomList           = "run.room.list"
	MethodRunRoomStatus         = "run.room.status"
	MethodRunRoomPost           = "run.room.post"
	MethodRunRoomDecide         = "run.room.decide"
	MethodRunEvidenceList       = "run.evidence.list"
	MethodRunEvidenceGet        = "run.evidence.get"
	MethodRunEvidencePatch      = "run.evidence.patch"
	MethodRunEvidenceTranscript = "run.evidence.transcript"
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

type RunEvidenceListParams struct {
	WorkspaceID string `json:"workspace_id"`
	RunID       string `json:"run_id,omitempty"`
	Before      string `json:"before,omitempty"`
	Limit       int    `json:"limit,omitempty"`
}

type RunEvidenceGetParams struct {
	WorkspaceID string `json:"workspace_id"`
	PacketID    string `json:"packet_id"`
}

type RunEvidencePatchParams struct {
	WorkspaceID string `json:"workspace_id"`
	PacketID    string `json:"packet_id"`
	MaxBytes    int    `json:"max_bytes,omitempty"`
}

type RunEvidenceTranscriptParams struct {
	WorkspaceID string `json:"workspace_id"`
	PacketID    string `json:"packet_id"`
	MaxBytes    int    `json:"max_bytes,omitempty"`
}

type RunEvidenceGetResult struct {
	Packet EvidencePacket `json:"packet"`
}

type RunEvidencePatchResult struct {
	Packet    EvidencePacket `json:"packet"`
	Patch     string         `json:"patch"`
	Truncated bool           `json:"truncated"`
}

type RunEvidenceTranscriptResult struct {
	Packet     EvidencePacket `json:"packet"`
	DataBase64 string         `json:"data_base64"`
	Truncated  bool           `json:"truncated"`
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
func evidenceOriginFromStore(origin store.EvidenceOrigin) EvidenceOrigin {
	return EvidenceOrigin{Kind: EvidenceOriginKind(origin.Kind), ID: origin.ID}
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

func changedFileFactFromStore(f store.ChangedFileFact) ChangedFileFact {
	return ChangedFileFact{Path: f.Path, Status: f.Status, Additions: f.Additions, Deletions: f.Deletions}
}

func sourceFactFromStore(f store.EvidenceSourceFact) EvidenceSourceFact {
	return EvidenceSourceFact{Name: f.Name, Available: f.Available, Truncated: f.Truncated, Reason: f.Reason}
}

// EvidencePacketFromStore converts packet metadata. It never includes the
// contents of an evidence source or the server's filesystem path.
func EvidencePacketFromStore(p *store.EvidencePacket) EvidencePacket {
	if p == nil {
		return EvidencePacket{}
	}
	changed := make([]ChangedFileFact, len(p.ChangedFiles))
	for i, f := range p.ChangedFiles {
		changed[i] = changedFileFactFromStore(f)
	}
	sources := make([]EvidenceSourceFact, len(p.Sources))
	for i, f := range p.Sources {
		sources[i] = sourceFactFromStore(f)
	}
	return EvidencePacket{
		ID: p.ID, WorkspaceID: string(p.WorkspaceID), RunID: string(p.RunID),
		Origin: evidenceOriginFromStore(p.Origin), OwnerID: string(p.OwnerID), CreatorID: string(p.CreatorID),
		Trigger: EvidenceTrigger(p.Trigger), Objective: p.Objective, CapturedAt: collaborationTime(p.CapturedAt),
		ExpiresAt: collaborationTimePtr(p.ExpiresAt), Availability: EvidencePacketAvailability(p.Availability),
		ExpiredAt: collaborationTimePtr(p.ExpiredAt), EventBoundary: p.EventBoundary,
		BaseRevision: p.BaseRevision, RetainedRevision: p.RetainedRevision,
		ChangedFiles: changed, Sources: sources,
		RelatedRoomMessageIDs: append([]string(nil), p.RelatedRoomMessageIDs...),
		UnresolvedFacts:       append([]string(nil), p.UnresolvedFacts...), NextAction: p.NextAction,
		Provenance: p.Provenance, IdempotencyKey: p.IdempotencyKey,
		CreatedAt: collaborationTime(p.CreatedAt), UpdatedAt: collaborationTime(p.UpdatedAt),
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

func EvidencePacketPageFromStore(page *store.EvidencePacketPage) EvidencePacketListResult {
	if page == nil {
		return EvidencePacketListResult{}
	}
	out := EvidencePacketListResult{Packets: make([]EvidencePacket, len(page.Items)), NextBefore: page.NextBefore}
	for i, p := range page.Items {
		out.Packets[i] = EvidencePacketFromStore(p)
	}
	return out
}
