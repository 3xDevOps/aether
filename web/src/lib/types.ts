// Wire types mirroring internal/protocol/wire.go and internal/events.

import type { RunChildrenSetting } from '@/lib/child-types'
import type { DevArtifact } from '@/lib/development-types'
export * from '@/lib/development-types'
export * from '@/lib/run-repository-types'
export * from '@/lib/child-types'
export * from '@/lib/launch-defaults-types'

export type RunStatus =
  | 'queued'
  | 'provisioning'
  | 'running'
  | 'needs-attention'
  | 'completed'
  | 'merged'
  | 'abandoned'
  | 'failed'
  | 'interrupted'

/** What a live run waits on, from the scheduler; set only while `status` is `running` or `needs-attention`. */
/** What chose a live run's state; mirrors events.RunStatusCause in internal/events/event.go. */
// Mirrors protocol.RunLiveness in internal/protocol/wire.go.
export interface RunLiveness {
  phase?: 'setup' | 'awaiting-first-event' | 'streaming' | 'tool-running' | 'awaiting-input' | 'between-turns'
  last_progress_at?: string
  connected_at?: string
  transport: 'open' | 'closed' | 'unknown'
}

export type RunStatusCause =
  | 'turn'
  | 'report'
  | 'turn-end'
  | 'stall'
  | 'wait-expired'
  | 'wait-fault'
  | 'workers'
  | 'tasks-held'
  | 'human'
  | 'disconnect'

/** How far Aether can vouch for a reported wait; mirrors domain.WaitEvidence in internal/domain/wait.go. */
export type WaitKind = 'condition' | 'source_unknown' | 'system_fault' | 'unverified'

/**
 * The evidence behind the reported wait a live run shows, from the conditions its agent named; mirrors
 * domain.RunWait. `detail` and `error` are server text, `error` the source's or delivery's error
 * verbatim; `verified_at` is the oldest last successful source read, absent when one was never read.
 */
export interface RunWait {
  kind: WaitKind
  detail?: string
  verified_at?: string
  error?: string
}

export type RunActivity =
  | 'working'
  | 'waiting-agents'
  | 'waiting-ci'
  | 'waiting-tools'
  | 'waiting'
  | 'needs-you'
  /** Written before `waiting` replaced it; shown as `waiting`. */
  | 'idle'
  | 'finished'
  /** The agent replied with nothing pending; never asks anything of a member. */
  | 'answered'
  | 'failed'
  | 'disconnected'

/** `ready` awaits the owner's Mark complete; `later` is their Not yet. */
export type RunReview = '' | 'ready' | 'later' | 'accepted'

export interface RunInputRequest {
  id: string
  session_id: string
  kind: 'question' | 'permission' | 'form' | 'extension_ui'
}

export interface Run {
  id: string
  workspace_id: string
  member_id: string
  /** Account backing the run; absent on servers predating account sharing. */
  account_member_id?: string
  /** The account the launcher chose, present only when usage fallback moved the run to `account_member_id`. */
  requested_account_member_id?: string
  mission_id?: string
  mission_role?: 'integrator' | 'worker'
  integrator_run_id?: string
  /** The run that started this one, or a worker's current integrator. */
  parent_run_id?: string
  /** The top of the run's tree. */
  root_run_id?: string
  child_purpose?: string
  /** A detached child keeps its lineage but no longer counts toward its parent. */
  detached?: boolean
  /** The run's own child-run setting; absent follows the owner's. */
  children?: RunChildrenSetting
  /** The agent tried to start a child run while child runs were off. */
  child_refused?: boolean
  task: string
  /** Latest terminal title, omitted by older servers and for empty titles. */
  title?: string
  harness: string
  mode: string
  model?: string
  effort?: string
  /** The connector client, such as ChatGPT, the run was launched through. */
  via?: string
  /** The server drives the agent over ACP, so /ws/acp streams its session: every acp run and an ACP-driven headless run. */
  acp?: boolean
  switching?: 'tui' | 'acp'
  status: RunStatus
  state?: RunActivity | ''
  /** What chose `state`; absent on a run that is not live and from older servers. */
  cause?: RunStatusCause
  /** A live Enhanced run's turn, read from its agent session when the run was fetched; not pushed. */
  liveness?: RunLiveness
  /** Set while `state` is a wait the agent reported. */
  wait?: RunWait
  /** Increments on every change of status, state, reason or outcome flags. */
  status_seq: number
  branch: string
  last_commit?: string
  last_commit_at?: string | null
  protected?: boolean
  /** Set while the run is hidden from the board; absent means not archived. */
  archived_at?: string
  created_at: string
  started_at: string | null
  finished_at: string | null
  /** When the run entered its status, or its reason while it needs attention; absent on older gateways and older runs. */
  status_changed_at?: string
  profile_snapshot_id?: string
  /** Server-computed unanswered room questions; absent on older gateways. */
  unanswered_questions?: number
  /** Agent mail addressed to the run that it has not acknowledged. */
  unacked_messages?: number
  oldest_unacked_at?: string
  /** Correlated native requests; independent of execution status. */
  pending_inputs?: RunInputRequest[]
  /** Last run.status reason, sanitized like the event payload. */
  reason?: string
  /** Decorated by the gateway from the scheduler; absent on legacy servers. */
  paused?: boolean
  /** Exact runtime grace deadline; files and results have separate lifetimes. */
  container_retained_until?: string
  cleanup_pending?: boolean
  cleanup_error?: string
  /** Holder of the run's control lease; '' means nobody, absent on older gateways. */
  controller_member_id?: string
  /** Latest holder of that lease, the current one included; absent until someone has held it since the server started, and on older gateways. */
  last_controller_member_id?: string
  /** An agent-reported outcome its owner has not opened yet; the session may remain live. */
  outcome_unseen?: boolean
  /** A finish no member has opened yet; absent on older gateways. */
  finish_unopened?: boolean
  /** The owner's answer to a reported success; absent on older gateways. */
  review?: RunReview
  review_changed_at?: string
  base_commit?: string
  base_branch?: string
  base_source?: string
  base_checked_at?: string | null
  repos?: RunRepo[]
}

export interface RunRepo {
  workspace_id: string
  name: string
  path: string
  branch: string
  primary: boolean
}
/** Only `active` dispatches workers; `completed` and `cancelled` are final. */
export type MissionPhase = 'planning' | 'active' | 'completed' | 'cancelled'
export type MissionTaskStatus = 'ready' | 'working' | 'review' | 'done' | 'proposed' | 'abandoned' | 'blocked'
export type MissionTaskRevisionStatus = 'proposed' | 'accepted' | 'superseded' | 'abandoned'
export type MissionAttemptState =
  | 'reserved'
  | 'launching'
  | 'running'
  | 'unknown'
  | 'submitted'
  | 'completed'
  | 'failed'
  | 'cancelled'
  | 'superseded'
  | 'abandoned'
export type MissionSubmissionState =
  | 'proposed'
  | 'accepted'
  | 'rejected'
  | 'superseded'
  | 'abandoned'

export interface MissionExecutionChoice {
  account_member_id: string
  harness: string
  mode: string
  model?: string
  effort?: string
}

export type MissionIntegrator = MissionExecutionChoice

export interface Mission {
  id: string
  workspace_id: string
  objective: string
  accountable_human_id: string
  integrator: MissionIntegrator
  execution_choices: MissionExecutionChoice[]
  /** Workspaces the integrator and every worker check out at /repos/<name>. */
  extra_workspace_ids?: string[]
  current_integrator_run_id: string
  integrator_generation: number
  accepted_set_version: number
  phase: MissionPhase
  /** Unanswered questions; only mission.show and mission.list compute it. */
  open_questions: number
  /** Why the integrator run last failed to launch; cleared once it launched. */
  integrator_launch_error?: string
  integrator_launch_error_at?: string
  /** True once the current integrator's run row has existed. */
  integrator_run_launched?: boolean
  created_at: string
  updated_at: string
  archived_at?: string
}

export interface MissionQuestion {
  id: string
  mission_id: string
  seq: number
  body: string
  asked_by_run_id: string
  asked_at: string
  answer?: string
  answered_by_member_id?: string
  answered_at?: string | null
}

export interface MissionTaskScope {
  expected_paths?: string[]
  semantic_responsibility?: string
  interfaces?: MissionInterfaceRevision[]
  migrations?: string[]
  shared_tests?: string[]
  base?: string
  target?: string
  exclusions?: string[]
}

export interface MissionInterfaceRevision {
  name: string
  revision: string
}

export interface MissionEvidenceRequirement {
  kind: string
  detail?: string
}

export interface MissionTaskRevision {
  task_id: string
  revision: number
  title: string
  objective: string
  scope: MissionTaskScope
  evidence_requirements: MissionEvidenceRequirement[]
  status: MissionTaskRevisionStatus
  proposed_by_run_id?: string
  supersedes_revision?: number
  accepted_by_member_id?: string
  accepted_by_run_id?: string
  created_at: string
  accepted_at?: string | null
}

export interface MissionTaskDependency {
  task_id: string
  revision: number
  depends_on_task_id: string
  depends_on_revision: number
  output_ref?: string
}

export interface MissionTaskBlocker {
  kind: string
  task_id?: string
  owner_run_id?: string
  action: string
}

export interface MissionTask {
  id: string
  mission_id: string
  current_revision: number
  revision?: MissionTaskRevision | null
  /** A proposed revision above `current_revision`; it cannot be dispatched. */
  pending_revision?: MissionTaskRevision | null
  dependencies?: MissionTaskDependency[]
  status: MissionTaskStatus
  blockers?: MissionTaskBlocker[]
  abandoned_at?: string | null
  created_at: string
  updated_at: string
}

export interface MissionAttempt {
  id: string
  mission_id: string
  task_id: string
  task_revision: number
  number: number
  dispatch_key: string
  harness: string
  mode: string
  state: MissionAttemptState
  run_id: string
  actor_run_id?: string
  authorizing_human_id?: string
  run_owner_id?: string
  account_owner_id?: string
  authority_generation: number
  integrator_generation: number
  created_at: string
  reserved_at: string
  started_at?: string | null
  finished_at?: string | null
  /** Durable hold/takeover state, not inferred from a live lease. */
  takeover_active?: boolean
  takeover_member_id?: string
  takeover_generation?: number
  takeover_since?: string
  orchestration_hold?: boolean
  cancel_requested_at?: string | null
  cancellation_actor_run_id?: string
  cancellation_generation?: number
  last_error?: string
}

export interface MissionSubmissionEvidence {
  kind: string
  ref: string
  available: boolean
  truncated?: boolean
  detail?: string
}

export interface MissionSubmissionRef {
  workspace_id: string
  run_id: string
  evidence_ref: string
  retained_revision: string
}

export interface MissionSubmissionAcceptance {
  scope_disposition?: string
  /** Monotonic position in the mission's accepted set. */
  accepted_set_version?: number
}

export interface MissionSubmission {
  id: string
  mission_id: string
  task_id: string
  task_revision: number
  attempt_id: string
  ref: MissionSubmissionRef
  evidence: MissionSubmissionEvidence[]
  scope_violations?: string[]
  acceptance?: MissionSubmissionAcceptance
  state: MissionSubmissionState
  proposed_by_run_id: string
  integrator_generation: number
  created_at: string
  decided_at?: string | null
  decision_by_run_id?: string
}

export interface MissionScopeDiagnostic {
  task_id: string
  task_revision: number
  run_id: string
  kind: 'intended_overlap' | 'observed_overlap' | 'out_of_scope'
  paths: string[]
  peer_task_id?: string
  peer_run_id?: string
  unavailable?: boolean
  unavailable_why?: string
  detail?: string
}

export interface MissionShowResult {
  mission: Mission
  tasks: MissionTask[]
  attempts?: MissionAttempt[]
  submissions?: MissionSubmission[]
  diagnostics?: MissionScopeDiagnostic[]
  questions?: MissionQuestion[]
}

export interface MissionQuestionResult {
  question: MissionQuestion
}

export interface MissionCancelResult {
  mission: Mission
}

export interface MissionListResult {
  missions: Mission[]
  next_cursor?: string
}

export interface MissionCreateResult {
  mission: Mission
}

export interface MissionReplaceIntegratorResult {
  mission: Mission
  run_id?: string
}
export interface MissionWorkerReleaseResult {
  run_id: string
  takeover_active: boolean
  takeover_generation: number
}
export interface Workspace {
  id: string
  name: string
  base_branch: string
  steer_others?: string
  created_at: string
  /** The upstream git URL a run checkout's `origin` remote points at;
   * absent until a clone with an origin is linked. */
  origin?: string
}

/** `value` is absent for a secret: the server never returns one. */
export interface WorkspaceVariable {
  name: string
  value?: string
  secret?: boolean
}

export interface WorkspaceEnvironment {
  workspace_id: string
  setup_script: string
  variables: WorkspaceVariable[]
  /** Both absent from a server older than egress control. */
  egress?: boolean
  egress_hosts?: string[]
  /** From a save that took effect while its timeline note was not recorded. */
  warning?: string
}

export interface Member {
  id: string
  display_name: string
  color: string
  role: 'viewer' | 'collaborator' | 'admin'
  pending?: boolean
  /** The name commits made in this member's runs are authored as; absent
   * until the member sets one, when the server falls back. */
  git_name?: string
  /** The email address those commits carry; absent until set. */
  git_email?: string
}

export interface AccountAccess {
  /** Accounts the caller may select at launch, including their own. */
  accounts: Member[]
  /** Members the caller has allowed to use their account. */
  shared_with: Member[]
}
export type UsageProviderName = 'claude' | 'codex'

export type UsageProviderStatus =
  | 'ok'
  | 'stale'
  | 'unauthenticated'
  | 'unsupported'
  | 'unavailable'
  | 'error'

export interface UsageWindow {
  id: string
  label: string
  used_percent: number
  resets_at?: string
}

export interface UsageProvider {
  provider: UsageProviderName
  status: UsageProviderStatus
  windows: UsageWindow[]
  plan?: string
  updated_at?: string
  checked_at: string
  retry_at?: string
  error?: string
}

/** account.usage: read-only subscription quota measurements. */
export interface UsageResult {
  account_member_id: string
  providers: UsageProvider[]
}


/** insights.get: one period's outcomes for the caller or everyone. */
export interface InsightsParams {
  workspace_id: string
  scope: 'mine' | 'everyone'
  from: string
  to: string
  time_zone: string
}

export interface InsightsTotals {
  launched: number
  completed: number
  merged: number
  failed: number
  interrupted: number
  abandoned: number
  /** A subset of abandoned. */
  killed: number
  active: number
  /** completed + merged. */
  finished: number
}

export interface InsightsBucket {
  /** A local date, YYYY-MM-DD. */
  start: string
  launched: number
  completed: number
  merged: number
  failed: number
  interrupted: number
  abandoned: number
}

export interface InsightsBreakdown {
  launched: number
  finished: number
  failed: number
}

export interface InsightsRun {
  run_id: string
  title: string
  workspace_id: string
  status: RunStatus
  reason: string
  harness: string
  model: string
  created_at: string
  finished_at: string
  duration_seconds: number
  mission_id: string
  pr_url: string
  pr_state: string
}

export interface InsightsCostAgent {
  harness: string
  metered_runs: number
  unmetered_runs: number
  cost_usd: number
  input_tokens: number
  output_tokens: number
  total_tokens: number
}

export interface InsightsCost extends Omit<InsightsCostAgent, 'harness'> {
  /** Largest total_tokens first. */
  by_agent: InsightsCostAgent[]
}

/** Finished work with a merged pull request, out of all that finished. */
export interface InsightsSuccessCount {
  succeeded: number
  finished: number
}

export interface InsightsOutcome {
  done: number
  failed: number
  abandoned: number
}

export interface InsightsOutcomeBucket {
  /** A local date, YYYY-MM-DD, as in the series. */
  start: string
  runs: InsightsOutcome
  swarms: InsightsOutcome
}

export interface InsightsMemberOutcome {
  member_id: string
  display_name: string
  /** The member's profile color. */
  color: string
  runs: InsightsOutcome
  swarms: InsightsOutcome
  series: InsightsOutcomeBucket[]
}

export interface InsightsOutcomes {
  runs: InsightsOutcome
  swarms: InsightsOutcome
  series: InsightsOutcomeBucket[]
  /** Filled for scope "everyone" only. */
  members: InsightsMemberOutcome[]
}

export interface InsightsResult {
  generated_at: string
  from: string
  to: string
  time_zone: string
  bucket: 'day' | 'week'
  totals: InsightsTotals
  previous: InsightsTotals
  series: InsightsBucket[]
  heatmap: { date: string; runs: number }[]
  by_workspace: (InsightsBreakdown & { workspace_id: string; name: string })[]
  /** model "" is the agent's default model. */
  by_agent: (InsightsBreakdown & { harness: string; model: string })[]
  swarms: { launched: number; completed: number; cancelled: number; agents: number; tasks_accepted: number }
  durations: {
    runs_measured: number
    median_run_seconds: number
    p90_run_seconds: number
    state_runs_measured: number
    median_working_seconds: number
    median_waiting_seconds: number
    median_response_seconds: number
  }
  cost: InsightsCost
  pull_requests: { opened: number; merged: number }
  success: { runs: InsightsSuccessCount; swarms: InsightsSuccessCount }
  outcomes: InsightsOutcomes
  recent: InsightsRun[]
  needs_you: number
  /** "" until Aether has recorded one. */
  coverage: { state_times_since: string; pull_requests_since: string }
}

/** Team memory, mirroring internal/protocol/memory.go. `outcome` notes are
 * written by the server only, from a run's report. */
export type MemoryKind = 'decision' | 'convention' | 'gotcha' | 'preference' | 'outcome'
export type MemoryScope = 'workspace' | 'member'
export type MemorySource = 'agent' | 'member' | 'report' | 'answer'

/** One note with its provenance. `author_id` is the run's owner for agent
 * and report notes, the answering member for answers; the PR fields are the
 * run's latest pull request. */
export interface MemoryItem {
  id: string
  workspace_id: string
  kind: MemoryKind
  scope: MemoryScope
  source: MemorySource
  source_ref?: string
  title: string
  body: string
  files?: string[]
  author_id?: string
  author_name?: string
  run_id?: string
  harness?: string
  commit?: string
  pr_number?: number
  pr_state?: string
  pr_url?: string
  superseded_by?: string
  invalid_at?: string
  pinned_by?: string
  pinned_by_name?: string
  pinned_at?: string
  created_at: string
  updated_at: string
}

/** Scope `member` keeps a pin on the note out of other members' briefs. */
export interface MemoryAddParams {
  workspace_id: string
  kind: Exclude<MemoryKind, 'outcome'>
  text: string
  files?: string[]
  supersedes?: string
  scope?: MemoryScope
  pin?: boolean
}

/** `item` is the live note with the same text when `duplicate` is set. */
export interface MemoryAddResult {
  item: MemoryItem
  duplicate?: boolean
  similar?: MemoryItem[]
}

/** No words lists the newest notes; `all` includes replaced ones; `pinned`
 * keeps only pinned ones. */
export interface MemorySearchParams {
  workspace_id: string
  words?: string
  kind?: MemoryKind
  source?: MemorySource
  file?: string
  all?: boolean
  replaced?: boolean
  pinned?: boolean
  limit?: number
}

export interface MemorySearchResult {
  items: MemoryItem[]
}

/** `chain` is every note it replaced or was replaced by, in replacement order. */
export interface MemoryShowResult {
  item: MemoryItem
  chain?: MemoryItem[]
}

/** `text` is empty when there is nothing to brief. */
export interface MemoryBriefResult {
  text: string
}

export interface MemoryRmResult {
  ids: string[]
}

export interface ServerInfo {
  server_version: string
  protocol_version: string
  time: string
  member: Member
  tailnet_hostname?: string
  /** Absent on servers that predate feature advertising. */
  features?: string[]
  tailnet_identity_auth?: boolean
  /** Why the dashboard web-port asks for is not serving on the tailnet. */
  tailnet_dashboard_error?: string
  disk?: DiskUsage
  /** Client-side failure from the separate disk request. */
  diskError?: string
}

export interface DiskUsage {
  used_bytes: number
  total_bytes: number
  /** What an unprivileged writer can still claim; the scheduler's floor. */
  free_bytes: number
  worktree_bytes: number
  transcript_bytes: number
  database_bytes: number
  /** The bare workspace repos; absent on servers predating the component. */
  repo_bytes?: number
  home_bytes?: number
  cache_bytes?: number
  evidence_bytes?: number
  scratch_bytes?: number
  other_bytes?: number
  /** Included in worktree_bytes, not an additional category. */
  snapshot_bytes?: number
  warnings?: string[]
  docker?: DockerDiskUsage
  entries?: DiskEntry[]
  truncated?: boolean
  /** Every Aether container for an admin, the caller's own otherwise. Not part of any total above. */
  containers?: DiskContainer[]
  /** Absent until the server's first measurement completes. */
  containers_measured_at?: string
  containers_error?: string
}

/** Files a container wrote outside its mounted home, checkout and cache; deleted with the container. */
export interface DiskContainer {
  /** `member` is that member's environment. */
  owner_kind: 'run' | 'member'
  owner_id: string
  bytes: number
}

/** Daemon-wide Docker accounting, including workloads outside Aether. */
export interface DockerDiskUsage {
  used_bytes?: number
  total_bytes?: number
  free_bytes?: number
  images_bytes?: number
  containers_bytes?: number
  volumes_bytes?: number
  build_cache_bytes?: number
  /** Docker's unused classification, not Aether authorization to delete saved images. */
  reclaimable_bytes?: number
  shared_filesystem?: boolean
  error?: string
}

export interface DiskEntry {
  kind: string
  owner_kind: 'run' | 'member' | 'workspace' | 'server'
  owner_id?: string
  pool?: string
  bytes: number
  reclaimable_bytes?: number
  retained_until?: string
  reason: string
  error?: string
}

/** GET /api/v1/capabilities. Legacy remote monitors do not serve it; null
 * means "assume the remote allowlist". */
export interface GatewayCapabilities {
  gateway: string
  methods: string[]
  ws: string[]
  local?: string[]
  /** The CLI build serving this gateway; absent before the field existed. */
  version?: string
  commit?: string
}

export interface TerminalStatusResult {
  running: boolean
  image?: string
  saved_image?: string
  started_at?: string
  tabs?: string[]
}

export interface TerminalHistoryParams {
  run_id: string
  before?: string
  query?: string
  limit?: number
}

export interface TerminalHistoryLine {
  cursor: string
  time: number
  text: string
}

export interface TerminalHistoryResult {
  lines: TerminalHistoryLine[]
  next_cursor?: string
  has_more: boolean
  truncated_before?: boolean
}

export interface EnvSaveResult {
  image: string
}

export interface Event {
  id: string
  seq: number
  time: string
  workspace_id: string
  run_id: string
  actor_id: string
  type: string
  payload: unknown
}
export type RoomMessageKind =
  | 'comment'
  | 'steer_request'
  | 'question'
  | 'reply'
  | 'system'

export type RoomMessageState =
  | 'queued'
  | 'sent'
  | 'not_sent'
  | 'uncertain'
  | 'denied'
  | 'cancelled'

export interface RoomMessageAnchor {
  kind?: string
  path?: string
  start_line?: number
  end_line?: number
  transcript_offset?: number
}

export interface RoomMessageFailure {
  code?: string
  message?: string
  retryable?: boolean
}

export interface RoomMessage {
  id: string
  workspace_id: string
  run_id: string
  actor_id: string
  actor_display_name?: string
  /** The connector client, such as ChatGPT, the actor posted through. */
  via?: string
  kind: RoomMessageKind
  body: string
  attachments?: string[]
  anchor?: RoomMessageAnchor
  correlation_id?: string
  idempotency_key?: string
  state: RoomMessageState
  deliver_after?: string
  decided_by?: string
  decided_at?: string
  delivered_at?: string
  agent_delivery?: 'queued' | 'delivered'
  failure?: RoomMessageFailure
  created_at: string
  updated_at: string
}

export interface RoomMessageListResult {
  messages: RoomMessage[]
  next_before?: string
}

export interface RoomController {
  member_id: string
  /** The holder's control session; absent from a gateway older than the field. */
  control_session_id?: string
  connected: boolean
  acquired_at: string
  expires_at?: string
}

export interface RoomStatusResult {
  workspace_id: string
  run_id: string
  protected: boolean
  controller?: RoomController
  watchers: string[]
  queued_steers: number
}

export interface RoomPostResult {
  message: RoomMessage
  receipt?: RoomDeliveryReceipt
  /** What an enhanced run's agent did with a delivered steer. */
  outcome?: 'sent' | 'queued' | 'injected'
}

export interface RoomDecideResult {
  message: RoomMessage
  receipt?: RoomDeliveryReceipt
}

export type RoomDeliveryReceipt = 'sent' | 'not_sent' | 'uncertain'

export type EvidenceTrigger = 'finish' | 'handoff' | 'report'
export type EvidencePacketAvailability = 'available' | 'expired'

export interface ChangedFileFact {
  path: string
  status?: string
  additions?: number
  deletions?: number
}

export interface EvidenceSourceFact {
  name: string
  available: boolean
  truncated?: boolean
  reason?: string
}

export interface EvidencePacket {
  id: string
  workspace_id: string
  run_id: string
  creator_id: string
  trigger: EvidenceTrigger
  objective: string
  captured_at: string
  expires_at?: string
  availability: EvidencePacketAvailability
  expired_at?: string
  event_boundary: number
  base_revision?: string
  retained_revision?: string
  captures?: DevArtifact[]
  verification_notes?: string
  changed_files?: ChangedFileFact[]
  sources?: EvidenceSourceFact[]
  related_room_message_ids?: string[]
  unresolved_facts?: string[]
  next_action?: string
  provenance?: string
  idempotency_key?: string
  created_at: string
  updated_at: string
}

export interface EvidencePacketListResult {
  packets: EvidencePacket[]
  next_before?: string
}

export interface EvidenceGetResult {
  packet: EvidencePacket
}

export interface EvidencePatchResult {
  packet: EvidencePacket
  patch: string
  truncated: boolean
}

export interface EvidenceTranscriptResult {
  packet: EvidencePacket
  data_base64: string
  truncated: boolean
}


export interface RunInputPayload {
  pending_inputs: RunInputRequest[]
}

export interface RunStatusPayload {
  from?: RunStatus
  to: RunStatus
  reason?: string
  state?: RunActivity | ''
  cause?: RunStatusCause
  /** Only a newer read of the wait's sources; history leaves it out. */
  reread?: boolean
  wait?: RunWait
  status_seq: number
  /** The run's flags after this transition; absent means false. */
  outcome_unseen?: boolean
  finish_unopened?: boolean
  review?: RunReview
  review_changed_at?: string
}

export interface RunReviewPayload {
  review: RunReview
  review_changed_at?: string
  status_seq: number
}

/** Complete runtime ownership metadata; omitted fields clear previous values. */
export interface RunRetentionPayload {
  container_retained_until?: string
  cleanup_pending?: boolean
  cleanup_error?: string
}

export interface GitBranchPayload {
  workspace_id: string
  /** The extra repo's workspace ID, absent for the run's own checkout. */
  repo?: string
  branch: string
  commit: string
  /** The commit the branch held before, or "" for a new branch. */
  previous: string
  forced: boolean
}

export interface RunPRPayload {
  run_id: string
  number: number
  url: string
  state: string
  merged_at?: string
}

export interface IntegrationChangedPayload {
  mission_id: string
  candidate_id: string
  state: string
  version: number
}

export type MissionChange =
  | 'task_proposed'
  | 'task_accepted'
  | 'task_finished'
  | 'question_asked'
  | 'question_answered'
  | 'worker_ended'
  | 'phase_changed'

export interface MissionChangedPayload {
  mission_id: string
  deleted?: boolean
  change?: MissionChange
  task_id?: string
  attempt_id?: string
}

export interface RunTitlePayload {
  title: string
}
export interface RunModelPayload {
  model: string
}
export interface RunEffortPayload {
  effort: string
}
export interface RunProtectedPayload {
  protected: boolean
}
/** An empty member_id means nobody holds the control lease; last_member_id is its latest holder. */
export interface RunControllerPayload {
  member_id: string
  last_member_id?: string
}
/** `requested_account_member_id` is absent once the run is back on the account it asked for. */
export interface RunAccountPayload {
  account_member_id: string
  previous_account_member_id: string
  requested_account_member_id?: string
}
/** Null means the run was restored. */
export interface RunArchivedPayload {
  archived_at: string | null
}

export type RunMessageKind = 'message' | 'question' | 'reply' | 'report'

export interface RunMessage {
  id: string
  workspace_id: string
  mission_id?: string
  from_run_id: string
  to_run_id: string
  kind: RunMessageKind
  correlation_id?: string
  body: string
  created_at: string
  delivered_at?: string
  acked_at?: string
  outcome?: string
  summary?: string
  next_action?: string
}

export interface CoordMessagesListResult {
  messages: RunMessage[]
  next_before?: string
}

export interface CoordMessagePayload {
  message_id: string
  workspace_id: string
  mission_id?: string
  from_run_id: string
  to_run_id: string
  kind: RunMessageKind
  correlation_id?: string
}

export interface CoordMessageAckedPayload {
  message_id: string
  to_run_id: string
  acked_at: string
}

/** One present member. `watching` holds the runs they have attached to. */
export interface PresenceEntry {
  member_id: string
  state: 'online' | 'watching' | 'offline'
  watching?: string[]
  last_seen: string
}

/** While `unmetered_runs` is non-zero every total here is a floor. */
export interface CostRollup {
  runs: number
  metered_runs: number
  unmetered_runs: number
  input_tokens: number
  output_tokens: number
  cost_usd: number
}

export interface Budget {
  workspace_id: string
  limit_usd: number
  warn_usd?: number
  override?: boolean
  updated_by?: string
  updated_at?: string
}

export type BudgetState = 'ok' | 'warn' | 'exceeded'

export interface BudgetReport {
  workspace_id: string
  budget?: Budget
  state: BudgetState
  spend: CostRollup
  advisory?: boolean
}

/** Oldest first. */
export interface TimelinePage {
  events: Event[]
  next_seq: number
  more: boolean
  older_seq?: number
}

export interface TimelineQuery {
  workspace_id: string
  run_id?: string
  mission_id?: string
  member_id?: string
  types?: string[]
  after_seq?: number
  newest?: boolean
  before_seq?: number
  limit?: number
}

/** One file in a `run.diff` snapshot. */
export interface FileDiffStat {
  path: string
  additions: number
  deletions: number
  /** Absent on snapshots from servers that predate it. */
  status?: 'added' | 'deleted' | 'modified'
}

export interface RunDiffPayload {
  /** The extra repo's workspace ID, absent for the run's own checkout. */
  repo?: string
  files: FileDiffStat[]
  /** The run holds more untracked content than a stat set reads: every
   * changed path is in `files`, but the counts sum to a floor. */
  truncated?: boolean
  /** The commit `files` is measured against: the newest upstream commit the
   * run contains. Absent from servers that predate it. */
  base?: string
  /** The checkout's HEAD commit at this snapshot, and its subject line. */
  head?: string
  head_subject?: string
  /** The git tree of the whole worktree at this snapshot. */
  tree?: string
  /** The previous snapshot's tree, or the fork-point tree for the first one.
   * Both trees are absent from servers that predate per-snapshot trees. */
  parent_tree?: string
  history_gap?: boolean
  snapshot_error?: string
}

/** One other active run touching files a run also touches. */
export interface OverlapPeer {
  run_id: string
  member_id: string
  files: string[]
}

/** One run's whole overlap set, as `run.overlaps` reports it. */
export interface Overlap {
  run_id: string
  with: OverlapPeer[]
}

/** The `run.overlap` event: the envelope run's overlaps at the moment they
 * changed, without the member ids the RPC result carries. */
export interface OverlapPayload {
  with?: { run_id: string; files: string[] }[]
}

/** GET /api/v1/run/<id>/patch - the run's diff against its fork point. */
export interface RunPatch {
  run_id: string
  base: string
  patch: string
  truncated: boolean
  recorded?: boolean
  /** The tree of the commit the run forked from; present on renders without
   * `from`. Passed as `from` it diffs the run's current state against its start. */
  fork_tree?: string
}

/** One immediate child returned by files.tree or home.tree. Only home.tree
 * sends `link`, a symlink that is never followed, and `locked`, a credential
 * file that is listed but cannot be read. */
export interface FileTreeEntry {
  name: string
  kind: 'file' | 'dir' | 'link'
  size: number
  locked?: boolean
}

export interface FilesTreeResult {
  entries: FileTreeEntry[]
}

export interface FileRead {
  content: string
  truncated: boolean
  binary: boolean
  size: number
  revision: string
  writable: boolean
}

export interface FileDiff {
  patch: string
  truncated: boolean
}

/** Wire form of a task template (internal/protocol/template.go). */
export interface Template {
  id: string
  workspace_id: string
  name: string
  task: string
  harness: string
  mode: string
  model?: string
  effort?: string
  params?: Record<string, string>
  budget_usd?: number
  created_at: string
}

export interface TemplateLaunch {
  run: Run
  base_commit: string
  base_branch: string
  base_source: string
  base_checked_at: string
}

/** The first line a client sends on /ws/events. */
export interface SubscribeRequest {
  workspace_id?: string
  run_id?: string
  types?: string[]
  replay?: boolean
  after_seq?: number
}

/** Wire form of a cron schedule (internal/protocol/template.go). */
export interface Schedule {
  id: string
  workspace_id: string
  template: string
  cron: string
  member_id: string
  created_at: string
  last_fire_at?: string | null
  next_fire_at?: string | null
}

/** Addresses a workspace by exactly one of id or name. */
export interface WorkspaceSelector {
  id?: string
  name?: string
}

/** The wire names of Standard, Enhanced and Background. */
export type LaunchMode = 'tui' | 'acp' | 'headless'

/** One entry of agent.list; source is who supplied the harness. */
export interface AgentInfo {
  name: string
  /** The vendor's product name for a shipped agent, the name otherwise. */
  display_name?: string
  /** The shipped agent's name, or 'custom' for a member's own definition. */
  glyph?: string
  source: 'shipped' | 'member'
  /** Whether the caller's persistent environment contains the executable. */
  installed?: boolean
  /** How the agent serves the Agent Client Protocol an enhanced run uses. */
  enhanced?: 'native' | 'adapter' | 'none'
  /** Whether the adapter, or the native CLI, resolves for this launch. */
  enhanced_installed?: boolean
  /** The command the agent runs; a member's agent may name it differently from the agent. */
  executable?: string
  /** Whether a login file exists in the home the launch signs in with. Absent when the agent declares none. */
  login_found?: boolean
  /** The launch mode the agent starts in unless asked otherwise. */
  default_mode?: 'tui' | 'acp'
  /** Whether the agent prefers Enhanced, installed yet or not. */
  enhanced_default?: boolean
  /** Why a swarm integrator or worker cannot run the agent in Standard: Aether could not wake it when mail arrives. */
  swarm_standard_refusal?: string
  /** Whether a running run can switch between Standard and Enhanced. */
  switchable?: boolean
  /** Shared account: refused because its owner has no login for this agent. */
  login_missing?: boolean
  /** Shared account: refused because the caller's own definition runs only on their account. */
  own_account_only?: boolean
  /** Shared account: the owner's login exists but cannot be shared. At most
   * one of login_missing, own_account_only and unavailable is set. */
  unavailable?: string
  /** Vendor installer command for shipped harnesses, when available. */
  install_script?: string
  /** install_script followed by the pinned enhanced-mode adapter's install. */
  enhanced_install_script?: string
  /** The levels a launch may send as `effort`, lowest first; absent when the agent has no effort setting. */
  effort_levels?: string[]
}

/** One model agent.models discovered; `id` is what a launch sends as `model`. */
export interface AgentModel {
  id: string
  name: string
  description?: string
}

/** agent.models: `error` is the last failed discovery, even when `models` holds an older or fallback list. */
export interface AgentModelsResult {
  models: AgentModel[]
  default_label?: string
  source: 'agent' | 'cache' | 'fallback' | 'none'
  fetched_at?: string
  error?: string
}

/** agent.install: a failed command is a result with `error`, not a refusal. */
export interface AgentInstallResult {
  log_tail: string
  installed: boolean
  enhanced_installed: boolean
  error?: string
}

/** agent.remove: the `~/.local/bin` links a shipped agent's removal deleted; none for a member's agent. */
export interface AgentRemoveResult {
  removed?: string[]
}

/** A member-supplied custom harness launch definition (agent.register). */
export interface AgentDefinition {
  name: string
  executable?: string
  tui_args?: string[]
  headless_args?: string[]
  acp_args?: string[]
  /** The ACP session mode an Enhanced run starts in. */
  acp_mode?: string
  profile_root?: string
  credential_paths?: string[]
  deny_names?: string[]
}

// POST /local/v1/<verb> results (internal/localgw/local.go); only a gateway
// with the user's repository and SSH key serves these.

/** link.status: whether this gateway has a linked server and repository. */
export interface LinkStatus {
  /** A server address is configured, even when no repository is linked. */
  server_configured: boolean
  linked: boolean
  addr: string
  user: string
  repo: string
  /**
   * Named server profiles from `aether link --name`; absent when none.
   * `repo` is the profile's own clone, absent when it has none.
   */
  links?: { name: string; addr: string; repo?: string }[]
  /** The top-level link, present when one is configured. */
  default?: { name: ''; addr: string; repo?: string }
  /** The profile this gateway runs on; empty on the top-level link. */
  active?: string
  /** The edge and server id of a link through an edge; absent otherwise. */
  edge_url?: string
  server_id?: string
}

/** An account signed in at an edge, as the edge reported it: `id` is the
 * edge's own id for it, the rest the provider identity. */
export interface EdgeAccount {
  id: string
  provider: string
  subject: string
  login?: string
  email?: string
  name?: string
}

/** edge.login, and edge.status's `login`: the sign-in this gateway runs.
 * `edge` is the relay origin and `signin_origin` the origin whose page
 * confirms the code. */
export interface EdgeLogin {
  state: 'pending' | 'signed_in' | 'failed'
  edge: string
  signin_origin: string
  user_code: string
  verification_uri: string
  account?: EdgeAccount
  error?: string
}

/** edge.status: every edge this machine is signed in to. */
export interface EdgeStatus {
  edges: { edge: string; signin_origin?: string; account?: EdgeAccount; error?: string }[]
  login?: EdgeLogin
}

/** A server the signed-in account reaches through the edge. The server
 * enforces `access_policy`; the edge only reports it. */
export interface EdgeServer {
  id: string
  name: string
  online: boolean
  role: string
  access_policy: 'account' | 'approved-devices'
  kind: 'self-hosted' | 'hosted'
}

/** edge.hostkey: the host key a server presents through the edge, checked
 * against its id, read without authenticating. */
export interface EdgeHostKey {
  edge: string
  server_id: string
  fingerprint: string
}

/** A refusal on the edge path, as internal/edge/proto's Refused carries
 * it in the local gateway's error data. Fields beyond `kind` depend on it. */
export interface EdgeRefusal {
  kind:
    | 'device-pending'
    | 'device-revoked'
    | 'not-member'
    | 'too-many-waiting'
    | 'key-of-another-account'
    | 'account-mismatch'
    | 'grant-key-mismatch'
    | 'claim-user-missing'
    | 'claim-account-mismatch'
    | 'claim-wrong'
    | 'claim-expired'
    | 'claim-exhausted'
    | 'claimed'
    | 'claim-member-gone'
    | 'claim-member-not-admin'
    | 'claim-account-of-another-member'
    | 'claim-device-revoked'
    | 'claim-failed'
    | 'join-wrong'
    | 'invitation-needs-link'
    | 'not-signed-in'
    | 'token-revoked'
    | 'server-not-connected'
    | 'server-not-claimed'
    | 'unknown-server'
    | 'too-many-attempts'
    | 'connection-limit'
    | 'server-blocked'
    | 'account-blocked'
    | 'identity-stale'
    | 'edge-refused'
  server?: string
  device?: string
  fingerprint?: string
  account?: string
  member_id?: string
  member_name?: string
  role?: string
  bound_member?: string
  invitation?: string
  waiting?: number
  registered?: boolean
  direct?: boolean
  approval_code?: string
  user?: string
  principal?: string
  refusal?: string
  detail?: string
}

/** Where on the edge path a gateway failure happened, which `aether gui`
 * reports beside `refusal`: the layer that failed, the server id it dialed,
 * the edge's host, and the account signed in there when known. `online`,
 * on a server-registration failure only, lists the account's other servers
 * the edge sees online. */
export interface EdgePath {
  layer: 'client-edge' | 'authorization' | 'server-registration' | 'end-to-end'
  server_id: string
  edge?: string
  account?: string
  online?: EdgeServer[]
}

/** server.edge: the server's edge. `edge_url` is empty while the edge is
 * off, `owner` null while the server has no owner at its edge, and
 * `claimed` whether the edge last reported the server claimed. */
export interface ServerEdge {
  edge_url: string
  server_id: string
  access_policy: 'account' | 'approved-devices'
  owner: { provider: string; login: string } | null
  connected: boolean
  claimed: boolean
}

/** server.edge.claimcode: a claim code bound to the calling admin. */
export interface ServerEdgeClaimCode {
  code: string
  expires_at: string
}

/** edge.link and edge.claim: the link just saved. */
export interface EdgeLinkResult {
  server_id: string
  /** Set by the Link step for a server it linked from the edge's list. */
  server_name?: string
  edge: string
  addr?: string
  user: string
  member: { id: string; display_name: string; role: string }
  /** The link is saved, but the clone's aether remote was not updated. */
  remote_error?: string
}

/** member.device.list: a client install a member reaches the server through
 * an edge with. `account` is the login, else email, of the edge account it
 * signed in as. A device waiting on an invitation has `invitation_id` and
 * an empty `member_id`. A device awaiting approval carries `role`, and
 * `display_name` unless approving it adds a new member: whom approving
 * admits it as, as member.device.lookup gives them. */
export interface Device {
  id: string
  member_id: string
  invitation_id?: string
  provider: string
  account: string
  label: string
  status: 'registered' | 'pending' | 'approved' | 'revoked'
  fingerprint: string
  created_at: string
  last_seen_at?: string
  approved_by?: string
  display_name?: string
  role?: Member['role']
}

/** member.device.lookup: the device an approval code names and whom
 * approving admits it as: its member, or the member a link invitation
 * names, with that member's role. A device waiting on an invitation that
 * adds a new member has no `member_id` and the invited `role`. */
export interface DeviceLookup {
  device: Device
  member_id?: string
  display_name?: string
  role: Member['role']
}

/** member.invitation.list: an edge account that may join. `member_id` is
 * set on an identity link, which binds an existing member and has no role. */
export interface Invitation {
  id: string
  provider?: string
  login?: string
  email?: string
  role?: Member['role']
  member_id?: string
  created_by: string
  created_at: string
  expires_at: string
}

/** member.invitation.create and member.identity.link: the invitation, and
 * its join link, returned only here: the link holds the join secret. */
export interface InvitationResult {
  invitation: Invitation
  link: string
  server_id: string
}

/** member.list. `merged_into` maps each member a merge absorbed, which
 * history still names, to the member it is now. */
export interface MemberList {
  members: Member[]
  merged_into?: Record<string, string>
}

/** An edge account bound to a member. */
export interface Identity {
  provider: string
  subject: string
  login?: string
  email?: string
  created_at: string
}

/** member.identity.list: every way the member signs in, and each device
 * with the account it signed in as, revoked ones included. */
export interface MemberIdentities {
  tailnet_login?: string
  public_key_fingerprint?: string
  identities: Identity[]
  devices: Device[]
}

/** member.merge.start: the code is shown once. */
export interface MergeCode {
  code: string
  expires_at: string
}

export interface MergeSettings {
  display_name: string
  color: string
  git_name?: string
  git_email?: string
  image?: string
  harness_definitions: { name: string; definition?: string }[]
  profile_heads: { harness: string; snapshot_id: string }[]
  account_shared_with: string[]
  mirror_workspaces: string[]
  home: boolean
}

/** One side of a merge as it stands. */
export interface MergeMember {
  id: string
  display_name: string
  role: Member['role']
  tailnet_login?: string
  public_key_fingerprint?: string
  identities: { provider: string; login: string }[]
  devices: { id: string; label: string; status: Device['status'] }[]
  runs: number
  schedules: number
  settings: MergeSettings
}

/** Keeping `keep_settings_of`'s settings absorbs `absorbed_id` and
 * discards its settings in `discarded`. */
export interface MergeChoice {
  keep_settings_of: string
  absorbed_id: string
  discarded: MergeSettings
}

/** member.merge.preview. `minted` minted the code, `proposer` entered it.
 * `role` is the merged member's role; `role_with_keep_admin` the role the
 * admin side gets by asking with keep_admin, required to keep an admin on
 * the server when `keep_admin_required`. */
export interface MergePlan {
  minted: MergeMember
  proposer: MergeMember
  role: Member['role']
  role_with_keep_admin?: Member['role']
  keep_admin_required?: boolean
  sign_ins: string[]
  choices: MergeChoice[]
  plan_digest: string
}

/** member.merge.propose and member.merge.pending. `refusal` says why
 * accepting would now be refused. */
export interface MergeProposal {
  id: string
  minted_id: string
  proposer_id: string
  keep_settings_of: string
  proposer_keep_admin?: boolean
  plan_digest: string
  expires_at: string
  plan?: MergePlan
  refusal?: string
}

/** member.merge.list: the audit record of one merge. */
export interface MemberMerge {
  id: string
  survivor_id: string
  absorbed_id: string
  confirmed_by_kind: string
  confirmed_by: string
  survivor_role_before: Member['role']
  absorbed_role_before: Member['role']
  role_after: Member['role']
  discarded: MergeSettings
  absorbed: MergeMember
  archive_path?: string
  created_at: string
}

/** link.apply: the server identity linked to this local gateway. */
export interface LinkApplyResult {
  addr: string
  user: string
  member: { id: string; display_name: string; role: string }
  key_generated?: string
}

/** link.repo: the repo just linked and the git remote written into it. */
export interface LinkRepoResult {
  repo: string
  remote: string
  url: string
  /** The workspace's upstream origin afterwards, whether it was already
   * recorded or taken from this clone; absent when neither has one. */
  origin?: string
}

/** github.connect: the account the environment terminal is logged in to,
 * and the signing key now registered on it. */
export interface GitHubConnectResult {
  login: string
  signing_key: string
  fingerprint: string
}

export interface GitHubOAuthResult {
  state: 'disconnected' | 'starting' | 'pending' | 'finishing' | 'connected' | 'expired' | 'failed' | 'cancelled'
  session_id?: string
  user_code?: string
  verification_url?: string
  expires_at?: string
  login?: string
  connection?: GitHubConnectResult
  error?: string
}

export interface GitHubAccount {
  id: number
  login: string
}

export interface GitHubRepository {
  id: number
  full_name: string
  name: string
  private: boolean
  default_branch: string
  clone_url: string
  can_push: boolean
}

export interface GitHubRepositoryListResult {
  account: GitHubAccount
  repositories: GitHubRepository[]
  next_page?: number
}

/** github.probe status: gh is usable, absent, present but unrunnable, or
 * older than the login check can read. */
export type GitHubCLIStatus = 'ok' | 'missing' | 'broken' | 'outdated'

/** github.probe. `admin_remedy` is set only when the server's own standard
 * image lacks a usable gh. */
export interface GitHubProbeResult {
  status: GitHubCLIStatus
  version?: string
  minimum: string
  /** What gh, or the container that could not run it, printed. */
  detail?: string
  /** Differs from saved_image while a container outlives the image it should be on. */
  image: string
  saved_image?: string
  /** Where gh resolved, present only when that is a file inside the
   * member's own environment home and therefore outlives every image. */
  path?: string
  remedy?: string
  admin_remedy?: string
}

/** pull: the run branch fetched into the linked repository. */
export interface PullResult {
  branch: string
  ref: string
  output: string
  current: boolean
  dirty: boolean
}

/** git.identity: the `user.name` and `user.email` this machine's git
 * resolves. Either is empty when the key is unset. */
export interface GitIdentity {
  name: string
  email: string
}

/** pull.switch: the run branch now checked out locally. */
export interface PullSwitchResult {
  branch: string
}
/** How the clone's base branch compared with the workspace's copy. */
export type RepoPushState = 'pushed' | 'up-to-date' | 'behind' | 'diverged'

/** repo.push: the comparison the gateway made, and the push if one ran. */
export interface RepoPushResult {
  branch: string
  remote: string
  state: RepoPushState
  local_commit: string
  /** Empty when the workspace has no such branch yet. */
  workspace_commit: string
  ahead: number
  behind: number
  output: string
}

/** repo.fast-forward: the clone's base branch moved up to the workspace. */
export interface RepoFastForwardResult {
  branch: string
  commit: string
  current: boolean
  dirty: boolean
  output: string
}

export type WorkspaceMirrorAuth = 'public' | 'deploy-key' | 'github'

export type WorkspaceMirrorStatus =
  | 'pending'
  | 'refreshing'
  | 'disabling'
  | 'ready'
  | 'auth-failed'
  | 'offline'
  | 'source-missing'
  | 'rewritten'
  | 'diverged'
  | 'error'

/** Public state returned by the workspace mirror administration RPCs. */
export interface WorkspaceMirrorResult {
  enabled: boolean
  source_url?: string
  source_identity?: string
  branch?: string
  auth?: WorkspaceMirrorAuth
  github_member_id?: string
  github_user_id?: number
  generation?: number
  status?: WorkspaceMirrorStatus
  observed_commit?: string
  accepted_commit?: string
  key_fingerprint?: string
  last_error?: string
  created_at?: string
  updated_at?: string
  last_attempt_at?: string | null
  last_success_at?: string | null
  /** Returned only when configuring or reconfiguring deploy-key auth. */
  public_key?: string
  warning?: string
}



/** sync.start / sync.stop: one run's overlay state after the verb. */
export interface SyncSessionState {
  run_id: string
  state: string
}

/** One background sync session as sync.status reports it. */
export interface SyncSessionStatus {
  run_id: string
  state: string
  /** Describes a paused-on-conflict session; null otherwise. */
  conflict: string | null
}

export interface SyncStatusResult {
  sessions: SyncSessionStatus[]
}

/** daemon.install: where the sync-daemon unit landed and how to enable it. */
export interface DaemonInstallResult {
  unit_path: string
  note: string
}

export interface DaemonStatusResult {
  installed: boolean
  unit_path: string
}


/** update.check (internal/selfupdate). `dev` and `disabled` never report an update. */
export interface UpdateCheck {
  /** The running version; "dev" for a local build. */
  version: string
  commit: string
  /** The newest release tag; empty when nothing was checked. */
  latest?: string
  update_available: boolean
  /** The release asset for this platform, aether-<goos>-<goarch>; absent
   * when packaged. */
  asset?: string
  release_url?: string
  dev: boolean
  disabled: boolean
  /** False for a packaged or development build, which cannot replace itself. */
  can_self_update: boolean
  /** The app that installs and updates this CLI, e.g. "Android app". The
   * release is still reported, with no asset and no self-update. */
  packaged?: string
  checked_at: string
}

/** update.check: the CLI answer plus how the server it talks to compares. */
export interface UpdateStatus {
  cli: UpdateCheck
  /** Empty when the server did not answer; server_error then says why. */
  server_version: string
  server_behind: boolean
  /** The CLI half is still answered in full when the SSH hop is down. */
  server_error?: string
  /** The desktop shell spawned this gateway, so it can restart it. */
  supervised: boolean
  /** The last failed desktop-app rebuild's error; absent after a success. */
  shell_build_error?: string
  /** The binary update.apply replaces, symlinks resolved. Absent when the
   * gateway could not probe it; install_method is absent with it. */
  cli_path?: string
  /** `admin-prompt`: macOS asks for the admin password first. `manual`: the
   * member runs `sudo aether update`. Absent when the probe failed. */
  install_method?: 'direct' | 'admin-prompt' | 'manual'
  /** The connected server's release is newer than this client, so it
   * refuses normal calls until the client updates. Absent on older
   * gateways; false when the server did not answer. */
  client_update_required?: boolean
  min_client_version?: string
  /** The release update.apply would install: the server's own release
   * while client_update_required, otherwise the latest, whether or not
   * this binary can install it. Empty while gated means no installable
   * release matches the server; absent on older gateways. */
  target_version?: string
  /** The release lookup failed, verbatim. `cli` then carries only what the
   * binary knows about itself: no latest, no asset. */
  cli_error?: string
  /** update.apply would refuse: the linked server is behind and must update
   * first. */
  server_first?: boolean
  /** update.apply can replace this binary: false on Windows and for a
   * packaged or development build. */
  can_self_update?: boolean
}

/** update.apply: what the self-update replaced and what happens next. */
export interface UpdateApplyResult {
  /** Every binary path replaced, in order. */
  updated: string[]
  version: string
  /** True only under the desktop shell, which respawns the gateway. */
  restarting: boolean
  note?: string
  /** A co-located aether-server keeps the old code until this restarts its unit. */
  restart_command?: string
  /** A desktop-app rebuild started in the background. */
  rebuilding: boolean
}

/** update.status: progress of a desktop-app rebuild in this gateway process. */
export interface UpdateBuildStatus {
  phase:
    | 'idle'
    | 'downloading'
    | 'unpacking'
    | 'fetching node'
    | 'installing dependencies'
    | 'packaging'
    | 'installing'
    | 'done'
    | 'error'
  /** Up to the last 20 lines of the build's own output. */
  lines_tail?: string[]
  /** The real build error; present only when phase is "error". */
  error?: string
}

// server.update is admin only; reading its status is not.

/** One update recorded and waiting for an idle server. */
export interface PendingServerUpdate {
  version: string
  requested_by: string
  requested_at: string
}

/** What a pending update is still waiting for. Paused runs are reported
 * but hold nothing back: a frozen container survives a restart. */
export interface ServerUpdateWaiting {
  runs: number
  paused: number
  shells: number
}

/** The outcome of the last update the server tried. */
export interface ServerUpdateAttempt {
  version: string
  outcome: 'applied' | 'failed'
  /** The real error behind a failed attempt. */
  detail?: string
  at: string
}

/** server.update_status. On the unprivileged install `capable` is false and
 * `manual_commands` says what to run on the server host. */
export interface ServerUpdateStatus {
  server_version: string
  latest?: string
  update_available: boolean
  capable: boolean
  incapable?: string
  pending?: PendingServerUpdate
  waiting?: ServerUpdateWaiting
  last?: ServerUpdateAttempt
  manual_commands?: string[]
  /** The fields below come from servers that check for releases themselves
   * and record each update's phases; older servers omit them. */
  latest_version?: string
  notes_url?: string
  /** RFC3339 time of the server's last release check. */
  checked_at?: string
  /** That check's error, verbatim; empty when it succeeded. */
  check_error?: string
  /** The last update's phase. applied, failed and rolled_back stay until
   * the next attempt; idle means there never was one. */
  phase?: ServerUpdatePhase | 'idle'
  error?: string
  from_version?: string
  to_version?: string
}

/** server.sysbox.status and server.sysbox.install: whether runs can use
 * Docker inside their own containers. `reason` explains `unsupported` and
 * `failed`; `log` is the install output. */
export interface SysboxStatus {
  state: 'on' | 'off' | 'unsupported' | 'installing' | 'failed'
  reason?: string
  log?: string
}

/** Caps on a run's container. A missing field is unset: a run's own limits
 * fall back to the server's, the server's to its automatic values. */
export interface RunLimits {
  cpus?: number
  memory_bytes?: number
  pids?: number
}

/** server.run_limits: `limits` is what an admin set, `effective` what a run
 * with no override of its own gets. */
export interface ServerRunLimits {
  limits: RunLimits
  /** What an unset field gets: the server's flag or automatic value. */
  default: RunLimits
  effective: RunLimits
}

/** When an update applies: immediately, at the next idle moment, or never
 * (which clears the pending one). */
export type ServerUpdateWhen = 'now' | 'idle' | 'cancel'

/** server.update: what the call recorded. The version fields are empty for
 * a cancel. */
export interface ServerUpdateResult {
  status: 'applying' | 'scheduled' | 'cancelled'
  version?: string
  requested_by?: string
  requested_at?: string
}

/** How far one update has got. `restarting` is the last frame the socket
 * carries: the server re-executes there and every connection drops. */
export type ServerUpdatePhase =
  | 'scheduled'
  | 'preflight'
  | 'applying'
  | 'restarting'
  | 'verifying'
  | 'applied'
  | 'failed'
  | 'rolled_back'
  | 'cancelled'

/** The server.update event payload: one moment of a self-update. */
export interface ServerUpdatePayload {
  phase: ServerUpdatePhase
  version?: string
  actor_id?: string
  /** The real error behind a failed or rolled_back phase. */
  detail?: string
  from_version?: string
}


/** env.agents: one setup-capable agent's local availability. */
export interface LocalAgentStatus {
  name: string
  installed: boolean
}

/** env.agents. `repo_path` is set when the link config knows exactly one repository folder. */
export interface EnvAgentsResult {
  agents: LocalAgentStatus[]
  /** The folders the gateway looked in, so an empty result can say where. */
  searched: string[]
  /** Why the login shell could not be asked for its PATH; set only when
   * the probe failed and just the standard folders were checked. */
  warning?: string
  repo_path?: string
}

export interface AssistantNoticePayload {
  member_id: string
  run_id: string
  watch_id?: string
  kind: 'finished' | 'needs_input' | 'needs_approval' | 'needs_attention'
  title: string
  body: string
}

export interface SystemStatsParams {
  /** Only host samples taken after this Unix-millisecond time; 0 or absent returns the whole retained window. */
  since_ms?: number
}

export interface SystemHost {
  cpu_cores: number
  memory_total_bytes: number
  swap_total_bytes: number
  uptime_seconds: number
}

/** CPU figures are percent of all cores; rates are bytes per second; pressure is PSI "some avg10" and absent without /proc/pressure. */
export interface SystemSample {
  t_ms: number
  cpu_user: number
  cpu_system: number
  cpu_iowait: number
  cpu_steal: number
  memory_used_bytes: number
  memory_available_bytes: number
  memory_free_bytes: number
  swap_used_bytes: number
  load1: number
  load5: number
  load15: number
  net_rx_bps: number
  net_tx_bps: number
  disk_read_bps: number
  disk_write_bps: number
  pressure_cpu?: number
  pressure_memory?: number
  pressure_io?: number
}

export interface SystemContainer {
  /** `browser` is a run's browser sidecar and `owner_id` its run; `member` is that member's environment container. */
  owner_kind: 'run' | 'browser' | 'member'
  owner_id: string
  member_id: string
  workspace_id?: string
  title?: string
  status?: string
  /** Absent until two samples exist. */
  cpu_cores?: number
  /** Absent or 0 is unlimited. */
  cpu_limit_cores?: number
  cpu_throttled_percent?: number
  memory_bytes: number
  memory_limit_bytes: number
  pids: number
  pids_limit: number
  net_rx_bps?: number
  net_tx_bps?: number
  disk_read_bps?: number
  disk_write_bps?: number
}

/** Containers the caller may not see, plus Aether containers with no run or member owner. */
export interface SystemOthers {
  containers: number
  cpu_cores: number
  memory_bytes: number
}

export interface SystemStatsResult {
  interval_ms: number
  retention_ms: number
  host: SystemHost
  host_error?: string
  samples: SystemSample[]
  /** 0 or absent: containers not measured yet. */
  containers_at_ms?: number
  containers_error?: string
  containers: SystemContainer[]
  others?: SystemOthers
}

export interface SlackLink {
  user_id: string
  team_id: string
  linked_at: string
}

export interface SlackStatusResult {
  configured: boolean
  connected: boolean
  bot?: string
  team?: string
  error?: string
  link?: SlackLink
}

export interface ConnectorCode {
  url: string
  code: string
  expires_at: string
}

/** A ChatGPT or Claude connector acting as the member (docs/connectors.md). */
export interface Connector {
  id: string
  member_id: string
  client_name: string
  redirect_host: string
  device_id?: string
  scopes: string[]
  created_at: string
  last_used_at?: string
}

export interface SlackLinkResult {
  code: string
  expires_at: string
  bot?: string
  team?: string
}

export interface SlackUnlinkResult {
  link: SlackLink
}

/** What a watch follows (docs/watches.md). */
export type WatchTargetKind = 'run' | 'mission' | 'pr'

export interface WatchTarget {
  kind: WatchTargetKind
  run_id?: string
  mission_id?: string
  /** owner/name */
  repo?: string
  number?: number
}

export type WatchCondition = 'needs_you' | 'finished' | 'pr_merged' | 'ci' | 'delivered' | 'review' | 'merged' | 'closed'

export type WatchState = 'active' | 'paused' | 'error' | 'done' | 'expired'

export interface WatchObserved {
  summary: string
  run_status?: string
  activity?: string
  need?: string
  phase?: string
  pr_state?: string
  head_sha?: string
  checks?: { passed: number; failed: number; pending: number }
  review_state?: string
}

export type WatchDeliveryChannel = 'push' | 'in_app' | 'assistant'

/** pending: not sent yet, with why in detail. cleared: dropped unsent because its need was resolved first or its watch stayed paused. withdrawn: delivered, then replaced on the devices because its need ended. */
export type WatchDeliveryStatus = 'pending' | 'delivered' | 'no_destination' | 'disabled' | 'held_quiet_hours' | 'failed' | 'cleared' | 'withdrawn' | 'skipped'

export interface WatchDelivery {
  channel: WatchDeliveryChannel
  status: WatchDeliveryStatus
  detail?: string
  at: string
}

export interface WatchNotification {
  id: string
  watch_id: string
  condition: WatchCondition
  title: string
  body: string
  link: string
  created_at: string
  deliveries: WatchDelivery[]
}

export interface Watch {
  id: string
  member_id: string
  workspace_id: string
  target: WatchTarget
  target_title: string
  conditions: WatchCondition[]
  state: WatchState
  error?: string
  label?: string
  source: 'dashboard' | 'assistant' | 'mcp'
  created_at: string
  updated_at: string
  expires_at: string
  last_check_at?: string
  next_check_at?: string
  observed: WatchObserved
  last_notification?: WatchNotification
  link: string
}

export interface WatchCreateParams {
  target: WatchTarget
  conditions?: WatchCondition[]
  expires_at?: string
  label?: string
  source?: Watch['source']
  idempotency_key?: string
}

export interface WatchUpdateParams {
  watch_id: string
  paused?: boolean
  conditions?: WatchCondition[]
  expires_at?: string
  label?: string
}

export interface WatchListParams {
  state?: WatchState
  target_kind?: WatchTargetKind
  run_id?: string
  mission_id?: string
}

/** watch.create and watch.update: the watch and one human line saying what it now does. */
export interface WatchResult {
  watch: Watch
  receipt: string
}

export interface WatchGetResult {
  watch: Watch
  notifications: WatchNotification[]
}

export type QuietHours = { start: string; end: string; tz: string }

export interface NotifyPrefs {
  /** Watch notices to Web Push devices. */
  push: boolean
  quiet_hours?: QuietHours
  /** Read-only: where notices go now. */
  destinations: { push_devices: number }
}

/** notify.prefs.set replaces quiet_hours and keeps push as it is when push is left out. */
export interface NotifyPrefsSetParams {
  push?: boolean
  quiet_hours?: QuietHours
}

export interface WatchChangedPayload {
  watch_id: string
  member_id: string
  notification_id?: string
}

// Wire types of preference.* (internal/protocol/preference.go).

export type PreferenceSource = 'assistant' | 'settings'

export interface Preference {
  id: string
  /** Absent: applies in all the member's workspaces. */
  workspace_id?: string
  text: string
  enabled: boolean
  source: PreferenceSource
  /** The assistant message the member said it in. */
  source_ref?: string
  created_at: string
  updated_at: string
  last_used_at?: string
}

export interface PreferenceListParams {
  workspace_id?: string
  query?: string
  include_disabled?: boolean
}

export interface PreferenceListResult {
  preferences: Preference[]
  /** The member's "use my preferences" setting. */
  enabled: boolean
}

export interface PreferenceAddParams {
  text: string
  workspace_id?: string
}

export interface PreferenceUpdateParams {
  id: string
  text?: string
  workspace_id?: string
  clear_workspace?: boolean
  enabled?: boolean
}

export interface PreferenceResult {
  preference: Preference
  /** The row before the change. */
  previous?: Preference
}

export interface PreferenceDeleteResult {
  deleted: Preference
}
