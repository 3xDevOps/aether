package protocol

import "time"

// Integration methods expose the candidate verification and delivery surface.
const (
	MethodIntegrationPrepare         = "integration.prepare"
	MethodIntegrationShow            = "integration.show"
	MethodIntegrationList            = "integration.list"
	MethodIntegrationResolve         = "integration.resolve"
	MethodIntegrationVerify          = "integration.verify"
	MethodIntegrationRequestDelivery = "integration.request_delivery"
	MethodIntegrationDecide          = "integration.decide"
	MethodIntegrationDeliver         = "integration.deliver"
	MethodIntegrationPatch           = "integration.patch"
	MethodIntegrationDelete          = "integration.delete"
)

// Candidate and verification payloads are deliberately bounded. The service
// validates these limits before it copies evidence or starts a container.
const (
	IntegrationMaxSubmissions             = 32
	IntegrationMaxVerifications           = 32
	IntegrationMaxOutputBytes             = 64 << 10
	IntegrationMaxArgv                    = 64
	IntegrationMaxVerificationArgBytes    = 16 << 10
	IntegrationMaxPathBytes               = 4 << 10
	IntegrationMaxIdempotencyKeyBytes     = 256
	IntegrationMaxCandidateMutations      = 128
	IntegrationMaxSourceSnapshotBytes     = 1 << 20
	IntegrationMaxCandidatePayloadBytes   = 16 << 20
	IntegrationDefaultVerificationTimeout = 5 * time.Minute
	IntegrationMaxVerificationTimeout     = 30 * time.Minute
	IntegrationDefaultPageSize            = 50
	IntegrationMaxPageSize                = 100
	IntegrationCandidateLifetime          = 30 * 24 * time.Hour
	IntegrationVerificationLifetime       = 24 * time.Hour
	IntegrationTombstoneRetention         = 30 * 24 * time.Hour
)

// SubmissionRef identifies one immutable source submission. EvidenceRef is
// the durable evidence packet ID; RetainedRevision names the source revision
// retained by the candidate-owned Git ref.
type SubmissionRef struct {
	WorkspaceID      string `json:"workspace_id"`
	RunID            string `json:"run_id"`
	EvidenceRef      string `json:"evidence_ref"`
	RetainedRevision string `json:"retained_revision"`
}

// CandidateInput is an immutable snapshot of one submission and its evidence
// metadata. Transcript identifiers are relative, candidate-owned references;
// no host filesystem paths are represented on the wire.
type CandidateInput struct {
	Submission         SubmissionRef  `json:"submission"`
	BaseRevision       string         `json:"base_revision"`
	Packet             EvidencePacket `json:"packet"`
	TranscriptID       string         `json:"transcript_id,omitempty"`
	TranscriptChecksum string         `json:"transcript_checksum,omitempty"`
}

type CandidateState string

const (
	CandidatePreparing   CandidateState = "preparing"
	CandidateConflicted  CandidateState = "conflicted"
	CandidateFrozen      CandidateState = "frozen"
	CandidateUnavailable CandidateState = "unavailable"
	CandidateDeleting    CandidateState = "deleting"
	CandidateExpired     CandidateState = "expired"
)

// CandidateMutation records one authenticated, idempotent aggregate operation.
// These records are bounded by IntegrationMaxCandidateMutations and preserve
// the original result ID for safe retries after a client loses its response.
type CandidateMutation struct {
	ActorKey       string `json:"actor_key"`
	Operation      string `json:"operation"`
	IdempotencyKey string `json:"idempotency_key"`
	Digest         string `json:"digest"`
	ResultID       string `json:"result_id"`
}

// Candidate is the durable aggregate returned by integration methods.
type Candidate struct {
	CandidateID               string              `json:"candidate_id"`
	WorkspaceID               string              `json:"workspace_id"`
	MissionID                 string              `json:"mission_id,omitempty"`
	MissionAcceptedSetVersion uint64              `json:"mission_accepted_set_version,omitempty"`
	Submissions               []SubmissionRef     `json:"submissions"`
	RequiredSources           []string            `json:"required_sources"`
	Inputs                    []CandidateInput    `json:"inputs"`
	TargetRef                 string              `json:"target_ref"`
	ExpectedTargetRevision    string              `json:"expected_target_revision"`
	CandidateRevision         string              `json:"candidate_revision,omitempty"`
	State                     CandidateState      `json:"state"`
	Conflicts                 []string            `json:"conflicts,omitempty"`
	AppliedInputs             int                 `json:"applied_inputs"`
	Verifications             []Verification      `json:"verifications"`
	DeliveryRequest           *DeliveryRequest    `json:"delivery_request,omitempty"`
	DeliveryReceipt           *DeliveryReceipt    `json:"delivery_receipt,omitempty"`
	Mutations                 []CandidateMutation `json:"mutations"`
	CreatedAt                 time.Time           `json:"created_at"`
	ExpiresAt                 time.Time           `json:"expires_at"`
	Error                     string              `json:"error,omitempty"`
	Version                   int64               `json:"version"`
}

// CandidateSummary is the bounded list projection of a candidate aggregate.
// Full inputs, verification output, and mutation history are available only
// from Show so a page cannot exceed the gateway line budget.
type CandidateSummary struct {
	CandidateID            string           `json:"candidate_id"`
	WorkspaceID            string           `json:"workspace_id"`
	State                  CandidateState   `json:"state"`
	CandidateRevision      string           `json:"candidate_revision,omitempty"`
	TargetRef              string           `json:"target_ref"`
	ExpectedTargetRevision string           `json:"expected_target_revision"`
	DeliveryRequest        *DeliveryRequest `json:"delivery_request,omitempty"`
	DeliveryReceipt        *DeliveryReceipt `json:"delivery_receipt,omitempty"`
	CreatedAt              time.Time        `json:"created_at"`
	ExpiresAt              time.Time        `json:"expires_at"`
}

type VerificationStatus string

const (
	VerificationRunning       VerificationStatus = "running"
	VerificationPassed        VerificationStatus = "passed"
	VerificationFailed        VerificationStatus = "failed"
	VerificationTimedOut      VerificationStatus = "timed_out"
	VerificationCancelled     VerificationStatus = "cancelled"
	VerificationError         VerificationStatus = "error"
	VerificationSourceChanged VerificationStatus = "source_changed"
)

// Verification records a server-side execution against a frozen candidate.
// CreationKey and ContainerID are persisted because they are required for
// restart-safe cleanup and recovery of a durable aggregate.
type Verification struct {
	VerificationID    string             `json:"verification_id"`
	CandidateRevision string             `json:"candidate_revision"`
	Argv              []string           `json:"argv"`
	Image             string             `json:"image"`
	ObservedImage     string             `json:"observed_image,omitempty"`
	User              string             `json:"user,omitempty"`
	WorkingDir        string             `json:"working_dir,omitempty"`
	TimeoutSeconds    int                `json:"timeout_seconds,omitempty"`
	CPULimit          float64            `json:"cpu_limit,omitempty"`
	MemoryLimitBytes  int64              `json:"memory_limit_bytes,omitempty"`
	EnvironmentSHA256 string             `json:"environment_sha256,omitempty"`
	SetupScriptSHA256 string             `json:"setup_script_sha256,omitempty"`
	Status            VerificationStatus `json:"status"`
	ExitCode          *int               `json:"exit_code,omitempty"`
	Output            string             `json:"output,omitempty"`
	OutputTruncated   bool               `json:"output_truncated,omitempty"`
	Error             string             `json:"error,omitempty"`
	CreatedAt         time.Time          `json:"created_at"`
	FinishedAt        *time.Time         `json:"finished_at,omitempty"`
	ExpiresAt         time.Time          `json:"expires_at"`
	CreationKey       string             `json:"creation_key"`
	ContainerID       string             `json:"container_id"`
}

type DeliveryAction string

const (
	DeliveryActionUpdateRef DeliveryAction = "update_ref"
	DeliveryActionProposal  DeliveryAction = "proposal"
)

type DeliveryState string

const (
	DeliveryPending    DeliveryState = "pending"
	DeliveryApproved   DeliveryState = "approved"
	DeliveryDenied     DeliveryState = "denied"
	DeliveryDelivering DeliveryState = "delivering"
	DeliveryDelivered  DeliveryState = "delivered"
)

type DeliveryResult string

const (
	DeliveryResultLanded   DeliveryResult = "landed"
	DeliveryResultProposed DeliveryResult = "proposed"
)

// DeliveryRequest binds approval to the exact candidate revision and checks
// that were reviewed. RequestVersion identifies a replacement request and
// remains stable across approval, delivery, and receipt state transitions.
type DeliveryRequest struct {
	RequestID              string         `json:"request_id"`
	RequestVersion         int64          `json:"request_version"`
	CandidateRevision      string         `json:"candidate_revision"`
	VerificationIDs        []string       `json:"verification_ids"`
	TargetRef              string         `json:"target_ref"`
	ExpectedTargetRevision string         `json:"expected_target_revision"`
	Action                 DeliveryAction `json:"action"`
	State                  DeliveryState  `json:"state"`
	RequestedBy            string         `json:"requested_by"`
	DecidedBy              string         `json:"decided_by,omitempty"`
	CreatedAt              time.Time      `json:"created_at"`
	ExpiresAt              time.Time      `json:"expires_at"`
	DecidedAt              *time.Time     `json:"decided_at,omitempty"`
}

type DeliveryReceipt struct {
	ReceiptID         string         `json:"receipt_id"`
	RequestID         string         `json:"request_id"`
	CandidateRevision string         `json:"candidate_revision"`
	TargetRef         string         `json:"target_ref"`
	PreviousRevision  string         `json:"previous_revision"`
	Action            DeliveryAction `json:"action"`
	Result            DeliveryResult `json:"result"`
	ProposalRef       string         `json:"proposal_ref,omitempty"`
	CreatedAt         time.Time      `json:"created_at"`
}

type IntegrationPrepareParams struct {
	WorkspaceID            string          `json:"workspace_id"`
	MissionID              string          `json:"mission_id,omitempty"`
	Submissions            []SubmissionRef `json:"submissions"`
	TargetRef              string          `json:"target_ref"`
	ExpectedTargetRevision string          `json:"expected_target_revision"`
	RequiredSources        []string        `json:"required_sources,omitempty"`
	IdempotencyKey         string          `json:"idempotency_key"`
}

type IntegrationPrepareResult struct {
	Candidate Candidate `json:"candidate"`
}

type IntegrationShowParams struct {
	WorkspaceID string `json:"workspace_id"`
	CandidateID string `json:"candidate_id"`
}

type IntegrationShowResult struct {
	Candidate Candidate `json:"candidate"`
}
type IntegrationListParams struct {
	WorkspaceID string `json:"workspace_id"`
	Limit       int    `json:"limit,omitempty"`
}

type IntegrationListResult struct {
	Candidates []CandidateSummary `json:"candidates"`
}

type CandidateResolution struct {
	Path    string `json:"path"`
	Content string `json:"content,omitempty"`
	Delete  bool   `json:"delete,omitempty"`
}

type IntegrationResolveParams struct {
	WorkspaceID    string                `json:"workspace_id"`
	CandidateID    string                `json:"candidate_id"`
	Files          []CandidateResolution `json:"files"`
	IdempotencyKey string                `json:"idempotency_key"`
}

type IntegrationResolveResult struct {
	Candidate Candidate `json:"candidate"`
}

type IntegrationVerifyParams struct {
	WorkspaceID       string   `json:"workspace_id"`
	CandidateID       string   `json:"candidate_id"`
	CandidateRevision string   `json:"candidate_revision"`
	Argv              []string `json:"argv"`
	TimeoutSeconds    int      `json:"timeout_seconds"`
	IdempotencyKey    string   `json:"idempotency_key"`
}

type IntegrationVerifyResult struct {
	Candidate Candidate `json:"candidate"`
}

type IntegrationRequestDeliveryParams struct {
	WorkspaceID       string         `json:"workspace_id"`
	CandidateID       string         `json:"candidate_id"`
	CandidateRevision string         `json:"candidate_revision"`
	VerificationIDs   []string       `json:"verification_ids"`
	Action            DeliveryAction `json:"action"`
	IdempotencyKey    string         `json:"idempotency_key"`
}

type IntegrationRequestDeliveryResult struct {
	Candidate Candidate `json:"candidate"`
}

type IntegrationDecideParams struct {
	WorkspaceID    string `json:"workspace_id"`
	CandidateID    string `json:"candidate_id"`
	RequestID      string `json:"request_id"`
	RequestVersion int64  `json:"request_version"`
	Approve        bool   `json:"approve"`
}

type IntegrationDecideResult struct {
	Candidate Candidate `json:"candidate"`
}

type IntegrationDeliverParams struct {
	WorkspaceID    string `json:"workspace_id"`
	CandidateID    string `json:"candidate_id"`
	RequestID      string `json:"request_id"`
	RequestVersion int64  `json:"request_version"`
}

type IntegrationDeliverResult struct {
	Candidate Candidate `json:"candidate"`
}

type IntegrationPatchResult struct {
	Patch     string `json:"patch"`
	Truncated bool   `json:"truncated"`
}

type IntegrationDeleteParams struct {
	WorkspaceID string `json:"workspace_id"`
	CandidateID string `json:"candidate_id"`
}

type IntegrationDeleteResult struct{}
