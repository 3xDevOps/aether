package domain

import "time"

type MissionID string
type TaskID string
type AttemptID string
type SubmissionID string

type MissionExecutionChoice struct {
	AccountMemberID MemberID   `json:"account_member_id"`
	Harness         string     `json:"harness"`
	Mode            LaunchMode `json:"mode"`
}

type MissionIntegrator struct {
	AccountMemberID MemberID   `json:"account_member_id"`
	Harness         string     `json:"harness"`
	Mode            LaunchMode `json:"mode"`
}

type Mission struct {
	ID                           MissionID
	WorkspaceID                  WorkspaceID
	Objective                    string
	AccountableHumanID           MemberID
	Integrator                   MissionIntegrator
	ExecutionChoices             []MissionExecutionChoice
	MaxConcurrentAttempts        int
	MaxTotalAttempts             int
	CurrentIntegratorRunID       RunID
	IntegratorAuthorizingHumanID MemberID
	IntegratorRunOwnerID         MemberID
	IntegratorGeneration         uint64
	AcceptedSetVersion           uint64
	IdempotencyKey               string
	CreatedAt                    time.Time
	UpdatedAt                    time.Time
}

const (
	MaxMissionConcurrentAttempts = 8
	MaxMissionTotalAttempts      = 128
	MaxMissionTasks              = 128
	MaxTaskRevisions             = 64
	MaxTaskDependencies          = 128
	MaxTaskEvidenceRequirements  = 64
	MaxMissionScopeBytes         = 64 << 10
)

type TaskRevisionStatus string

const (
	TaskRevisionProposed   TaskRevisionStatus = "proposed"
	TaskRevisionAccepted   TaskRevisionStatus = "accepted"
	TaskRevisionSuperseded TaskRevisionStatus = "superseded"
	TaskRevisionAbandoned  TaskRevisionStatus = "abandoned"
)

func (s TaskRevisionStatus) Valid() bool {
	return s == TaskRevisionProposed || s == TaskRevisionAccepted || s == TaskRevisionSuperseded || s == TaskRevisionAbandoned
}

type TaskStatus string

const (
	TaskReady     TaskStatus = "ready"
	TaskBlocked   TaskStatus = "blocked"
	TaskWorking   TaskStatus = "working"
	TaskReview    TaskStatus = "review"
	TaskDone      TaskStatus = "done"
	TaskProposed  TaskStatus = "proposed"
	TaskAbandoned TaskStatus = "abandoned"
)

func (s TaskStatus) Valid() bool {
	return s == TaskReady || s == TaskBlocked || s == TaskWorking || s == TaskReview || s == TaskDone || s == TaskProposed || s == TaskAbandoned
}

type InterfaceRevision struct {
	Name     string `json:"name"`
	Revision string `json:"revision"`
}

type EvidenceRequirement struct {
	Kind   string `json:"kind"`
	Detail string `json:"detail,omitempty"`
}

type TaskScope struct {
	ExpectedPaths          []string            `json:"expected_paths,omitempty"`
	SemanticResponsibility string              `json:"semantic_responsibility,omitempty"`
	Interfaces             []InterfaceRevision `json:"interfaces,omitempty"`
	Migrations             []string            `json:"migrations,omitempty"`
	SharedTests            []string            `json:"shared_tests,omitempty"`
	Base                   string              `json:"base,omitempty"`
	Target                 string              `json:"target,omitempty"`
	Exclusions             []string            `json:"exclusions,omitempty"`
}

type TaskRevision struct {
	TaskID               TaskID
	Revision             int
	Title                string
	Objective            string
	Scope                TaskScope
	EvidenceRequirements []EvidenceRequirement
	Status               TaskRevisionStatus
	ProposedByRunID      RunID
	SupersedesRevision   int
	CreatedAt            time.Time
	AcceptedAt           *time.Time
}

type TaskDependency struct {
	TaskID            TaskID
	Revision          int
	DependsOnTaskID   TaskID
	DependsOnRevision int
	OutputRef         string
	CreatedAt         time.Time
}

type TaskBlocker struct {
	Kind       string
	TaskID     TaskID
	OwnerRunID RunID
	Action     string
}

type Task struct {
	ID              TaskID
	MissionID       MissionID
	CurrentRevision int
	Revision        *TaskRevision
	Dependencies    []TaskDependency
	Status          TaskStatus
	Blockers        []TaskBlocker
	AbandonedAt     *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type AttemptState string

const (
	AttemptReserved   AttemptState = "reserved"
	AttemptLaunching  AttemptState = "launching"
	AttemptRunning    AttemptState = "running"
	AttemptUnknown    AttemptState = "unknown"
	AttemptSubmitted  AttemptState = "submitted"
	AttemptCompleted  AttemptState = "completed"
	AttemptFailed     AttemptState = "failed"
	AttemptCancelled  AttemptState = "cancelled"
	AttemptSuperseded AttemptState = "superseded"
	AttemptAbandoned  AttemptState = "abandoned"
)

func (s AttemptState) Valid() bool {
	switch s {
	case AttemptReserved, AttemptLaunching, AttemptRunning, AttemptUnknown, AttemptSubmitted, AttemptCompleted, AttemptFailed, AttemptCancelled, AttemptSuperseded, AttemptAbandoned:
		return true
	default:
		return false
	}
}

func (s AttemptState) HoldsConcurrency() bool {
	return s == AttemptReserved || s == AttemptLaunching || s == AttemptRunning || s == AttemptUnknown || s == AttemptSubmitted
}

type AttemptReservation struct {
	MissionID            MissionID
	TaskID               TaskID
	TaskRevision         int
	DispatchKey          string
	Harness              string
	Mode                 LaunchMode
	AssignedRunID        RunID
	ActorRunID           RunID
	AuthorizingHumanID   MemberID
	RunOwnerID           MemberID
	AccountOwnerID       MemberID
	AuthorityGeneration  uint64
	IntegratorGeneration uint64
}

type Attempt struct {
	ID                     AttemptID
	MissionID              MissionID
	TaskID                 TaskID
	TaskRevision           int
	Number                 int
	DispatchKey            string
	Harness                string
	Mode                   LaunchMode
	State                  AttemptState
	RunID                  RunID
	ActorRunID             RunID
	AuthorizingHumanID     MemberID
	RunOwnerID             MemberID
	AccountOwnerID         MemberID
	AuthorityGeneration    uint64
	IntegratorGeneration   uint64
	CreatedAt              time.Time
	ReservedAt             time.Time
	StartedAt              *time.Time
	FinishedAt             *time.Time
	TakeoverActive         bool
	TakeoverMemberID       MemberID
	TakeoverGeneration     uint64
	CancelRequestedAt      *time.Time
	CancellationActorRunID RunID
	CancellationGeneration uint64
	LastError              string
}

type SubmissionEvidence struct {
	Kind      string `json:"kind"`
	Ref       string `json:"ref"`
	Available bool   `json:"available"`
	Detail    string `json:"detail,omitempty"`
}

type SubmissionRef struct {
	WorkspaceID      WorkspaceID `json:"workspace_id"`
	RunID            RunID       `json:"run_id"`
	EvidenceRef      string      `json:"evidence_ref"`
	RetainedRevision string      `json:"retained_revision"`
}

type SubmissionState string

const (
	SubmissionProposed   SubmissionState = "proposed"
	SubmissionAccepted   SubmissionState = "accepted"
	SubmissionRejected   SubmissionState = "rejected"
	SubmissionSuperseded SubmissionState = "superseded"
	SubmissionAbandoned  SubmissionState = "abandoned"
)

func (s SubmissionState) Valid() bool {
	return s == SubmissionProposed || s == SubmissionAccepted || s == SubmissionRejected || s == SubmissionSuperseded || s == SubmissionAbandoned
}

type Submission struct {
	ID                   SubmissionID
	MissionID            MissionID
	TaskID               TaskID
	TaskRevision         int
	AttemptID            AttemptID
	Ref                  SubmissionRef
	Evidence             []SubmissionEvidence
	Acceptance           *Acceptance
	ScopeViolations      []string
	State                SubmissionState
	ProposedByRunID      RunID
	IntegratorGeneration uint64
	CreatedAt            time.Time
	DecidedAt            *time.Time
	DecisionByRunID      RunID
}

type Acceptance struct {
	SubmissionID         SubmissionID
	MissionID            MissionID
	TaskID               TaskID
	TaskRevision         int
	AcceptedSetVersion   uint64
	IntegratorGeneration uint64
	AcceptedByRunID      RunID
	ScopeDisposition     string
	AcceptedAt           time.Time
}

type IntegratorAssignment struct {
	MissionID  MissionID
	RunID      RunID
	Generation uint64
}

type MissionWorkerAssignment struct {
	MissionID   MissionID
	TaskID      TaskID
	AttemptID   AttemptID
	WorkerRunID RunID
	MemberID    MemberID
	Active      bool
	Generation  uint64
	UpdatedAt   time.Time
}
