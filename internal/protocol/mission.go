package protocol

import "github.com/3xDevOps/Aether/internal/domain"

const (
	MethodMissionCreate            = "mission.create"
	MethodMissionList              = "mission.list"
	MethodMissionShow              = "mission.show"
	MethodMissionReplaceIntegrator = "mission.replace-integrator"
	MethodMissionWorkerRelease     = "mission.worker.release"

	MethodMissionQuestionAsk           = "mission.question.ask"
	MethodMissionQuestionAnswer        = "mission.question.answer"
	MethodMissionClarificationComplete = "mission.clarification.complete"
	MethodMissionPlanShow              = "mission.plan.show"
	MethodMissionPlanSubmit            = "mission.plan.submit"
	MethodMissionPlanDecide            = "mission.plan.decide"

	MethodTaskShow             = "task.show"
	MethodTaskList             = "task.list"
	MethodTaskPropose          = "task.propose"
	MethodTaskRevise           = "task.revise"
	MethodTaskAccept           = "task.accept"
	MethodTaskAcceptSubmission = "task.submission.accept"
	MethodTaskAbandon          = "task.abandon"

	MethodWorkerStart   = "worker.start"
	MethodWorkerList    = "worker.list"
	MethodWorkerInspect = "worker.inspect"
	MethodWorkerCancel  = "worker.cancel"
	MethodWorkerRetry   = "worker.retry"
)

type MissionExecutionChoice struct {
	AccountMemberID string `json:"account_member_id"`
	Harness         string `json:"harness"`
	Mode            string `json:"mode"`
}

type MissionIntegrator struct {
	AccountMemberID string `json:"account_member_id"`
	Harness         string `json:"harness"`
	Mode            string `json:"mode"`
}

type Mission struct {
	ID                           string                   `json:"id"`
	WorkspaceID                  string                   `json:"workspace_id"`
	Objective                    string                   `json:"objective"`
	AccountableHumanID           string                   `json:"accountable_human_id"`
	Integrator                   MissionIntegrator        `json:"integrator"`
	ExecutionChoices             []MissionExecutionChoice `json:"execution_choices"`
	MaxConcurrentAttempts        int                      `json:"max_concurrent_attempts"`
	MaxTotalAttempts             int                      `json:"max_total_attempts"`
	CurrentIntegratorRunID       string                   `json:"current_integrator_run_id,omitempty"`
	IntegratorAuthorizingHumanID string                   `json:"integrator_authorizing_human_id,omitempty"`
	IntegratorRunOwnerID         string                   `json:"integrator_run_owner_id,omitempty"`
	IntegratorGeneration         uint64                   `json:"integrator_generation"`
	AcceptedSetVersion           uint64                   `json:"accepted_set_version"`
	Phase                        string                   `json:"phase"`
	PlanVersion                  uint64                   `json:"plan_version"`
	OpenQuestions                int                      `json:"open_questions"`
	CreatedAt                    string                   `json:"created_at"`
	UpdatedAt                    string                   `json:"updated_at"`
}

// MissionQuestion is one clarifying question the integrator asked the
// accountable human. AnsweredAt is absent until a human answers.
type MissionQuestion struct {
	ID                 string  `json:"id"`
	MissionID          string  `json:"mission_id"`
	Seq                int     `json:"seq"`
	Body               string  `json:"body"`
	AskedByRunID       string  `json:"asked_by_run_id"`
	AskedAt            string  `json:"asked_at"`
	Answer             string  `json:"answer,omitempty"`
	AnsweredByMemberID string  `json:"answered_by_member_id,omitempty"`
	AnsweredAt         *string `json:"answered_at,omitempty"`
}

// MissionPlanReview is one round of plan submission and human decision.
// SubmittedPhase is clarified for an initial plan and active for an amendment.
type MissionPlanReview struct {
	MissionID         string            `json:"mission_id"`
	PlanVersion       uint64            `json:"plan_version"`
	Summary           string            `json:"summary"`
	SubmittedByRunID  string            `json:"submitted_by_run_id"`
	SubmittedAt       string            `json:"submitted_at"`
	SubmittedPhase    string            `json:"submitted_phase"`
	Decision          string            `json:"decision,omitempty"`
	Feedback          string            `json:"feedback,omitempty"`
	DecidedByMemberID string            `json:"decided_by_member_id,omitempty"`
	DecidedAt         *string           `json:"decided_at,omitempty"`
	Items             []MissionPlanItem `json:"items,omitempty"`
}

// MissionPlanItem is one task revision a plan round put in front of a human.
// Widening lists the expected paths and dropped exclusions that reach outside
// the plan the human already approved; it is computed by the server at submit
// and is empty for an initial plan.
type MissionPlanItem struct {
	TaskID             string   `json:"task_id"`
	Revision           int      `json:"revision"`
	NewTask            bool     `json:"new_task"`
	Material           bool     `json:"material"`
	Widening           []string `json:"widening,omitempty"`
	Title              string   `json:"title"`
	SupersedesRevision int      `json:"supersedes_revision,omitempty"`
}

// MissionPlanState is what the integrator sees of its own mission's gate. It
// is deliberately not protocol.Mission: no agent method returns the mission
// record. LatestFeedback is the feedback of the most recent revise decision.
type MissionPlanState struct {
	MissionID            string `json:"mission_id"`
	Phase                string `json:"phase"`
	PlanVersion          uint64 `json:"plan_version"`
	IntegratorGeneration uint64 `json:"integrator_generation"`
	OpenQuestions        int    `json:"open_questions"`
	LatestFeedback       string `json:"latest_feedback,omitempty"`
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

type InterfaceRevision struct {
	Name     string `json:"name"`
	Revision string `json:"revision"`
}

type EvidenceRequirement struct {
	Kind   string `json:"kind"`
	Detail string `json:"detail,omitempty"`
}

// TaskRevision carries Material both ways: the proposer declares it on
// task.propose and task.revise, and it is read back as the audit fact that
// sent the revision through a human plan round.
type TaskRevision struct {
	TaskID               string                `json:"task_id"`
	Revision             int                   `json:"revision"`
	Title                string                `json:"title"`
	Objective            string                `json:"objective"`
	Scope                TaskScope             `json:"scope"`
	Material             bool                  `json:"material,omitempty"`
	EvidenceRequirements []EvidenceRequirement `json:"evidence_requirements"`
	Status               string                `json:"status"`
	ProposedByRunID      string                `json:"proposed_by_run_id,omitempty"`
	SupersedesRevision   int                   `json:"supersedes_revision,omitempty"`
	AcceptedByMemberID   string                `json:"accepted_by_member_id,omitempty"`
	AcceptedByRunID      string                `json:"accepted_by_run_id,omitempty"`
	CreatedAt            string                `json:"created_at"`
	AcceptedAt           *string               `json:"accepted_at,omitempty"`
}

type TaskDependency struct {
	TaskID            string `json:"task_id"`
	Revision          int    `json:"revision"`
	DependsOnTaskID   string `json:"depends_on_task_id"`
	DependsOnRevision int    `json:"depends_on_revision"`
	OutputRef         string `json:"output_ref,omitempty"`
	CreatedAt         string `json:"created_at,omitempty"`
}

type TaskBlocker struct {
	Kind       string `json:"kind"`
	TaskID     string `json:"task_id,omitempty"`
	OwnerRunID string `json:"owner_run_id,omitempty"`
	Action     string `json:"action"`
}

type Task struct {
	ID              string           `json:"id"`
	MissionID       string           `json:"mission_id"`
	CurrentRevision int              `json:"current_revision"`
	Revision        *TaskRevision    `json:"revision,omitempty"`
	PendingRevision *TaskRevision    `json:"pending_revision,omitempty"`
	Dependencies    []TaskDependency `json:"dependencies,omitempty"`
	Status          string           `json:"status"`
	Blockers        []TaskBlocker    `json:"blockers,omitempty"`
	AbandonedAt     *string          `json:"abandoned_at,omitempty"`
	CreatedAt       string           `json:"created_at"`
	UpdatedAt       string           `json:"updated_at"`
}

type Attempt struct {
	ID                     string  `json:"id"`
	MissionID              string  `json:"mission_id"`
	TaskID                 string  `json:"task_id"`
	TaskRevision           int     `json:"task_revision"`
	Number                 int     `json:"number"`
	DispatchKey            string  `json:"dispatch_key"`
	Harness                string  `json:"harness"`
	Mode                   string  `json:"mode"`
	State                  string  `json:"state"`
	RunID                  string  `json:"run_id"`
	ActorRunID             string  `json:"actor_run_id,omitempty"`
	AuthorizingHumanID     string  `json:"authorizing_human_id,omitempty"`
	RunOwnerID             string  `json:"run_owner_id,omitempty"`
	AccountOwnerID         string  `json:"account_owner_id,omitempty"`
	AuthorityGeneration    uint64  `json:"authority_generation"`
	IntegratorGeneration   uint64  `json:"integrator_generation"`
	CreatedAt              string  `json:"created_at"`
	ReservedAt             string  `json:"reserved_at"`
	StartedAt              *string `json:"started_at,omitempty"`
	FinishedAt             *string `json:"finished_at,omitempty"`
	TakeoverActive         bool    `json:"takeover_active,omitempty"`
	TakeoverMemberID       string  `json:"takeover_member_id,omitempty"`
	TakeoverGeneration     uint64  `json:"takeover_generation,omitempty"`
	CancelRequestedAt      *string `json:"cancel_requested_at,omitempty"`
	CancellationActorRunID string  `json:"cancellation_actor_run_id,omitempty"`
	CancellationGeneration uint64  `json:"cancellation_generation,omitempty"`
	LastError              string  `json:"last_error,omitempty"`
}

type SubmissionEvidence struct {
	Kind      string `json:"kind"`
	Ref       string `json:"ref"`
	Available bool   `json:"available"`
	Detail    string `json:"detail,omitempty"`
}

type Submission struct {
	ID                   string               `json:"id"`
	MissionID            string               `json:"mission_id"`
	TaskID               string               `json:"task_id"`
	TaskRevision         int                  `json:"task_revision"`
	AttemptID            string               `json:"attempt_id"`
	Ref                  SubmissionRef        `json:"ref"`
	Evidence             []SubmissionEvidence `json:"evidence"`
	ScopeViolations      []string             `json:"scope_violations,omitempty"`
	Acceptance           *Acceptance          `json:"acceptance,omitempty"`
	State                string               `json:"state"`
	ProposedByRunID      string               `json:"proposed_by_run_id"`
	IntegratorGeneration uint64               `json:"integrator_generation"`
	CreatedAt            string               `json:"created_at"`
	DecidedAt            *string              `json:"decided_at,omitempty"`
	DecisionByRunID      string               `json:"decision_by_run_id,omitempty"`
}

type Acceptance struct {
	SubmissionID         string `json:"submission_id"`
	MissionID            string `json:"mission_id"`
	TaskID               string `json:"task_id"`
	TaskRevision         int    `json:"task_revision"`
	AcceptedSetVersion   uint64 `json:"accepted_set_version"`
	IntegratorGeneration uint64 `json:"integrator_generation"`
	AcceptedByRunID      string `json:"accepted_by_run_id"`
	ScopeDisposition     string `json:"scope_disposition,omitempty"`
	AcceptedAt           string `json:"accepted_at"`
}

type MissionCreateParams struct {
	WorkspaceID           string                   `json:"workspace_id"`
	Objective             string                   `json:"objective"`
	AccountableHumanID    string                   `json:"accountable_human_id"`
	Integrator            MissionIntegrator        `json:"integrator"`
	ExecutionChoices      []MissionExecutionChoice `json:"execution_choices"`
	MaxConcurrentAttempts int                      `json:"max_concurrent_attempts"`
	MaxTotalAttempts      int                      `json:"max_total_attempts"`
	IdempotencyKey        string                   `json:"idempotency_key"`
}

type MissionCreateResult struct {
	Mission Mission `json:"mission"`
}

type MissionListParams struct {
	WorkspaceID string `json:"workspace_id"`
	Limit       int    `json:"limit,omitempty"`
	Before      string `json:"before,omitempty"`
}
type MissionListResult struct {
	Missions   []Mission `json:"missions"`
	NextCursor string    `json:"next_cursor,omitempty"`
}
type MissionShowParams struct {
	MissionID string `json:"mission_id"`
}
type MissionShowResult struct {
	Mission     Mission                  `json:"mission"`
	Tasks       []Task                   `json:"tasks"`
	Attempts    []Attempt                `json:"attempts,omitempty"`
	Submissions []Submission             `json:"submissions,omitempty"`
	Diagnostics []MissionScopeDiagnostic `json:"diagnostics,omitempty"`
	Questions   []MissionQuestion        `json:"questions,omitempty"`
	PlanReviews []MissionPlanReview      `json:"plan_reviews,omitempty"`
}

// MissionQuestionAskParams are the params of mission.question.ask. The mission
// is the integrator's own, resolved from the socket, never a parameter.
type MissionQuestionAskParams struct {
	Body           string `json:"body"`
	IdempotencyKey string `json:"idempotency_key"`
}

// MissionQuestionAnswerParams are the params of mission.question.answer. The
// answering member is the authenticated session, never a parameter.
type MissionQuestionAnswerParams struct {
	QuestionID     string `json:"question_id"`
	Answer         string `json:"answer"`
	IdempotencyKey string `json:"idempotency_key"`
}

// MissionQuestionResult is the result of mission.question.ask and
// mission.question.answer.
type MissionQuestionResult struct {
	Question MissionQuestion `json:"question"`
}

// MissionClarificationCompleteParams are the params of
// mission.clarification.complete. The mission is the integrator's own,
// resolved from the socket, never a parameter.
type MissionClarificationCompleteParams struct {
	IdempotencyKey string `json:"idempotency_key"`
}

type MissionClarificationCompleteResult struct {
	Plan MissionPlanState `json:"plan"`
}

// MissionPlanShowParams are the params of mission.plan.show. WaitSeconds of 0
// or omitted returns immediately; anything outside 0..CoordMaxInboxWaitSeconds
// is invalid params.
type MissionPlanShowParams struct {
	WaitSeconds int `json:"wait_seconds,omitempty"`
}

type MissionPlanShowResult struct {
	Plan        MissionPlanState    `json:"plan"`
	Questions   []MissionQuestion   `json:"questions"`
	PlanReviews []MissionPlanReview `json:"plan_reviews"`
}

type MissionPlanSubmitParams struct {
	Summary        string `json:"summary"`
	IdempotencyKey string `json:"idempotency_key"`
}

type MissionPlanSubmitResult struct {
	Plan MissionPlanState `json:"plan"`
}

// MissionPlanDecideParams are the params of mission.plan.decide. Decision is
// exactly approve, revise, or reject; Feedback is required for revise. The
// deciding member is the authenticated session, never a parameter.
type MissionPlanDecideParams struct {
	MissionID           string `json:"mission_id"`
	ExpectedPlanVersion uint64 `json:"expected_plan_version"`
	Decision            string `json:"decision"`
	Feedback            string `json:"feedback,omitempty"`
	IdempotencyKey      string `json:"idempotency_key"`
}

type MissionPlanDecideResult struct {
	Mission Mission `json:"mission"`
}

type MissionReplaceIntegratorParams struct {
	MissionID          string            `json:"mission_id"`
	ExpectedGeneration uint64            `json:"expected_generation"`
	Integrator         MissionIntegrator `json:"integrator"`
	IdempotencyKey     string            `json:"idempotency_key"`
}
type MissionReplaceIntegratorResult struct {
	Mission Mission `json:"mission"`
	RunID   string  `json:"run_id,omitempty"`
}

type TaskShowParams struct {
	TaskID string `json:"task_id"`
}
type TaskShowResult struct {
	Task Task `json:"task"`
}
type TaskListParams struct {
	MissionID string `json:"mission_id"`
}
type TaskListResult struct {
	Tasks []Task `json:"tasks"`
}
type TaskProposeParams struct {
	MissionID      string       `json:"mission_id"`
	Revision       TaskRevision `json:"revision"`
	IdempotencyKey string       `json:"idempotency_key"`
}
type TaskReviseParams struct {
	TaskID         string       `json:"task_id"`
	Revision       TaskRevision `json:"revision"`
	IdempotencyKey string       `json:"idempotency_key"`
}
type TaskAcceptSubmissionParams struct {
	SubmissionID                 string `json:"submission_id"`
	ExpectedIntegratorGeneration uint64 `json:"expected_integrator_generation"`
	ExpectedAcceptedSetVersion   uint64 `json:"expected_accepted_set_version"`
	ScopeDisposition             string `json:"scope_disposition"`
	IdempotencyKey               string `json:"idempotency_key"`
}
type TaskAcceptParams struct {
	TaskID                       string `json:"task_id"`
	Revision                     int    `json:"revision"`
	ExpectedIntegratorGeneration uint64 `json:"expected_integrator_generation"`
	IdempotencyKey               string `json:"idempotency_key"`
}

// TaskAbandonParams abandons a whole task, or, with Revision set to a pending
// revision, drops only that revision and leaves the task standing.
type TaskAbandonParams struct {
	TaskID                       string `json:"task_id"`
	Revision                     int    `json:"revision,omitempty"`
	ExpectedIntegratorGeneration uint64 `json:"expected_integrator_generation"`
	IdempotencyKey               string `json:"idempotency_key"`
}
type TaskMutationResult struct {
	Task       Task        `json:"task"`
	Acceptance *Acceptance `json:"acceptance,omitempty"`
}
type MissionWorkerReleaseParams struct {
	RunID                      string `json:"run_id"`
	ExpectedTakeoverGeneration uint64 `json:"expected_takeover_generation"`
}

type MissionWorkerReleaseResult struct {
	RunID              string `json:"run_id"`
	TakeoverActive     bool   `json:"takeover_active"`
	TakeoverGeneration uint64 `json:"takeover_generation"`
}

type WorkerStartParams struct {
	MissionID                    string `json:"mission_id"`
	TaskID                       string `json:"task_id"`
	TaskRevision                 int    `json:"task_revision"`
	DispatchKey                  string `json:"dispatch_key"`
	Harness                      string `json:"harness"`
	Mode                         string `json:"mode"`
	AccountOwnerID               string `json:"account_owner_id"`
	RunOwnerID                   string `json:"run_owner_id"`
	ExpectedIntegratorGeneration uint64 `json:"expected_integrator_generation"`
}
type WorkerStartResult struct {
	Attempt  Attempt `json:"attempt"`
	Replayed bool    `json:"replayed,omitempty"`
}
type WorkerListParams struct {
	MissionID string `json:"mission_id"`
	TaskID    string `json:"task_id,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}
type WorkerListResult struct {
	Attempts   []Attempt `json:"attempts"`
	NextCursor string    `json:"next_cursor,omitempty"`
}
type WorkerInspectParams struct {
	AttemptID string `json:"attempt_id"`
}
type WorkerInspectResult struct {
	Attempt    Attempt     `json:"attempt"`
	Submission *Submission `json:"submission,omitempty"`
}
type WorkerCancelParams struct {
	AttemptID                    string `json:"attempt_id"`
	ExpectedIntegratorGeneration uint64 `json:"expected_integrator_generation"`
	IdempotencyKey               string `json:"idempotency_key"`
}
type WorkerRetryParams struct {
	AttemptID                    string `json:"attempt_id"`
	DispatchKey                  string `json:"dispatch_key"`
	ExpectedIntegratorGeneration uint64 `json:"expected_integrator_generation"`
}
type WorkerMutationResult struct {
	Attempt  Attempt `json:"attempt"`
	Replayed bool    `json:"replayed,omitempty"`
}

func MissionFromDomain(m *domain.Mission) Mission {
	out := Mission{ID: string(m.ID), WorkspaceID: string(m.WorkspaceID), Objective: m.Objective, AccountableHumanID: string(m.AccountableHumanID), Integrator: MissionIntegrator{AccountMemberID: string(m.Integrator.AccountMemberID), Harness: m.Integrator.Harness, Mode: string(m.Integrator.Mode)}, MaxConcurrentAttempts: m.MaxConcurrentAttempts, MaxTotalAttempts: m.MaxTotalAttempts, CurrentIntegratorRunID: string(m.CurrentIntegratorRunID), IntegratorAuthorizingHumanID: string(m.IntegratorAuthorizingHumanID), IntegratorRunOwnerID: string(m.IntegratorRunOwnerID), IntegratorGeneration: m.IntegratorGeneration, AcceptedSetVersion: m.AcceptedSetVersion, Phase: string(m.Phase), PlanVersion: m.PlanVersion, OpenQuestions: m.OpenQuestions, CreatedAt: rfc3339(m.CreatedAt), UpdatedAt: rfc3339(m.UpdatedAt)}
	out.ExecutionChoices = make([]MissionExecutionChoice, len(m.ExecutionChoices))
	for i, c := range m.ExecutionChoices {
		out.ExecutionChoices[i] = MissionExecutionChoice{AccountMemberID: string(c.AccountMemberID), Harness: c.Harness, Mode: string(c.Mode)}
	}
	return out
}

func MissionQuestionFromDomain(q *domain.MissionQuestion) MissionQuestion {
	return MissionQuestion{ID: string(q.ID), MissionID: string(q.MissionID), Seq: q.Seq, Body: q.Body, AskedByRunID: string(q.AskedByRunID), AskedAt: rfc3339(q.AskedAt), Answer: q.Answer, AnsweredByMemberID: string(q.AnsweredByMemberID), AnsweredAt: rfc3339Ptr(q.AnsweredAt)}
}

func MissionPlanReviewFromDomain(r *domain.MissionPlanReview) MissionPlanReview {
	out := MissionPlanReview{MissionID: string(r.MissionID), PlanVersion: r.PlanVersion, Summary: r.Summary, SubmittedByRunID: string(r.SubmittedByRunID), SubmittedAt: rfc3339(r.SubmittedAt), SubmittedPhase: string(r.SubmittedPhase), Decision: string(r.Decision), Feedback: r.Feedback, DecidedByMemberID: string(r.DecidedByMemberID), DecidedAt: rfc3339Ptr(r.DecidedAt)}
	if len(r.Items) > 0 {
		out.Items = make([]MissionPlanItem, len(r.Items))
		for i, item := range r.Items {
			out.Items[i] = MissionPlanItem{TaskID: string(item.TaskID), Revision: item.Revision, NewTask: item.NewTask, Material: item.Material, Widening: item.Widening, Title: item.Title, SupersedesRevision: item.SupersedesRevision}
		}
	}
	return out
}
