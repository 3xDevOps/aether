package domain

import (
	"path"
	"strings"
	"time"
)

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

// MissionPhase is where a mission is in its life. A mission starts in
// MissionPhasePlanning, where the integrator may ask the accountable human
// questions and proposes tasks; mission.start accepts the proposed tasks and
// moves it to MissionPhaseActive. Completed and cancelled are terminal.
//
//	planning         --mission.start------------------>  active
//	active           --integrator success report------>  completed
//	planning|active  --mission.cancel----------------->  cancelled
type MissionPhase string

const (
	MissionPhasePlanning  MissionPhase = "planning"
	MissionPhaseActive    MissionPhase = "active"
	MissionPhaseCompleted MissionPhase = "completed"
	MissionPhaseCancelled MissionPhase = "cancelled"
)

// Terminal reports whether the mission has ended: no agent of it runs again.
func (p MissionPhase) Terminal() bool {
	return p == MissionPhaseCompleted || p == MissionPhaseCancelled
}

// ScopeCovers reports whether p lies inside one of the declared scope paths:
// an exact match or a directory prefix, both cleaned first. It is the single
// path rule the mission scope diagnostics use.
func ScopeCovers(declared []string, p string) bool {
	p = path.Clean(strings.TrimSpace(p))
	if p == "." || p == "" {
		return false
	}
	for _, raw := range declared {
		base := path.Clean(strings.TrimSpace(raw))
		if base == "." || base == "" {
			continue
		}
		if base == p || strings.HasPrefix(p, base+"/") {
			return true
		}
	}
	return false
}

type MissionQuestionID string

// MissionQuestion is one clarifying question the integrator asked the
// accountable human. AnsweredAt is nil until a human answers.
type MissionQuestion struct {
	ID                 MissionQuestionID
	MissionID          MissionID
	Seq                int
	Body               string
	AskedByRunID       RunID
	AskedAt            time.Time
	Answer             string
	AnsweredByMemberID MemberID
	AnsweredAt         *time.Time
}

// MissionChange names a kind of swarm change an integrator reacts to.
type MissionChange string

const (
	MissionTaskProposed       MissionChange = "task_proposed"
	MissionTaskAccepted       MissionChange = "task_accepted"
	MissionTaskFinished       MissionChange = "task_finished"
	MissionQuestionAsked      MissionChange = "question_asked"
	MissionQuestionAnswered   MissionChange = "question_answered"
	MissionWorkerReport       MissionChange = "worker_report"
	MissionWorkerEnded        MissionChange = "worker_ended"
	MissionIntegratorReplaced MissionChange = "integrator_replaced"
	MissionPhaseChanged       MissionChange = "phase_changed"
)

type Mission struct {
	ID                           MissionID
	WorkspaceID                  WorkspaceID
	Objective                    string
	AccountableHumanID           MemberID
	Integrator                   MissionIntegrator
	ExecutionChoices             []MissionExecutionChoice
	CurrentIntegratorRunID       RunID
	IntegratorAuthorizingHumanID MemberID
	IntegratorRunOwnerID         MemberID
	IntegratorGeneration         uint64
	AcceptedSetVersion           uint64
	Phase                        MissionPhase
	// OpenQuestions is populated only by store.GetMission and
	// store.ListMissionsPage. Transaction-local mission reads leave it zero
	// and no store decision may consult it.
	OpenQuestions int
	// IntegratorLaunchError is why the current integrator run last failed
	// to launch, and IntegratorLaunchErrorAt when that error was first seen;
	// both are empty once the run launches.
	IntegratorLaunchError   string
	IntegratorLaunchErrorAt *time.Time
	// IntegratorRunLaunched is set once the current integrator run's row is
	// known to exist. Reconciliation relaunches a missing row only while it
	// is unset, so a deleted integrator run stays deleted.
	IntegratorRunLaunched bool
	// ChangeSeq counts every change that matters to the integrator.
	// Changes holds, per kind, the ChangeSeq of the latest change of that
	// kind its current integrator run did not make itself.
	ChangeSeq      uint64
	Changes        map[MissionChange]uint64
	IdempotencyKey string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	ArchivedAt     *time.Time
}

const (
	MaxMissionTasks             = 128
	MaxMissionQuestions         = 32
	MaxTaskRevisions            = 64
	MaxTaskDependencies         = 128
	MaxTaskEvidenceRequirements = 64
	MaxMissionScopeBytes        = 64 << 10
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
	// DependsOn is what the proposer declared; the store writes it as the
	// revision's dependency rows and reads it back through Task.Dependencies.
	DependsOn          []TaskID
	Status             TaskRevisionStatus
	ProposedByRunID    RunID
	SupersedesRevision int
	AcceptedByMemberID MemberID
	AcceptedByRunID    RunID
	CreatedAt          time.Time
	AcceptedAt         *time.Time
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
	// PendingRevision is the task's highest-numbered proposed revision above
	// CurrentRevision, nil when there is none. It is never dispatched,
	// because it is not the current revision.
	PendingRevision *TaskRevision
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
	Truncated bool   `json:"truncated,omitempty"`
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
