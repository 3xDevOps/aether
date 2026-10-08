// Wire types mirroring internal/protocol/wire.go and internal/events.

import type { DevArtifact } from '@/lib/development-types'
export * from '@/lib/development-types'
export * from '@/lib/run-repository-types'

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
  mission_id?: string
  mission_role?: 'integrator' | 'worker'
  integrator_run_id?: string
  task: string
  /** Latest terminal title, omitted by older servers and for empty titles. */
  title?: string
  harness: string
  mode: string
  /** The server drives the agent over ACP, so /ws/acp streams its session: every acp run and an ACP-driven headless run. */
  acp?: boolean
  switching?: 'tui' | 'acp'
  status: RunStatus
  branch: string
  last_commit?: string
  last_commit_at?: string | null
  protected?: boolean
  /** Set while the run is hidden from the board; absent means not archived. */
  archived_at?: string
  /** When the deletion sweep will remove this run; absent means not archived. */
  deletes_at?: string
  created_at: string
  started_at: string | null
  finished_at: string | null
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
  /** Holder of the run's control lease; '' means nobody, absent on older gateways. */
  controller_member_id?: string
  /** An agent report finished the run and its owner has not opened it yet. */
  outcome_unseen?: boolean
  base_commit?: string
  base_branch?: string
  base_source?: string
  base_checked_at?: string | null
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
}

export type MissionIntegrator = MissionExecutionChoice

export interface Mission {
  id: string
  workspace_id: string
  objective: string
  accountable_human_id: string
  integrator: MissionIntegrator
  execution_choices: MissionExecutionChoice[]
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


export interface ServerInfo {
  server_version: string
  protocol_version: string
  time: string
  member: Member
  tailnet_hostname?: string
  tailnet_identity_auth?: boolean
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
  evidence_bytes?: number
  other_bytes?: number
  /** Included in worktree_bytes, not an additional category. */
  snapshot_bytes?: number
  warnings?: string[]
  docker?: DockerDiskUsage
  entries?: DiskEntry[]
  truncated?: boolean
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
  /** The run's flag after this transition; absent means false. */
  outcome_unseen?: boolean
}

export interface GitBranchPayload {
  workspace_id: string
  branch: string
  commit: string
}

export interface RunTitlePayload {
  title: string
}
export interface RunProtectedPayload {
  protected: boolean
}
/** An empty member_id means nobody holds the control lease. */
export interface RunControllerPayload {
  member_id: string
}
/** Both null means the run was restored. */
export interface RunArchivedPayload {
  archived_at: string | null
  deletes_at: string | null
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

export type ApprovalDecision = 'requested' | 'approved' | 'denied'

export interface Approval {
  id: string
  workspace_id: string
  run_id: string
  action: string
  detail?: string
  decision: ApprovalDecision
  decided_by?: string
  created_at: string
  decided_at?: string
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
}

export interface RunDiffPayload {
  files: FileDiffStat[]
  /** The git tree of the whole worktree at this snapshot. */
  tree?: string
  /** The previous snapshot's tree, or the fork-point tree for the first one.
   * Both trees are absent from servers that predate per-snapshot trees. */
  parent_tree?: string
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
}

/** One immediate child returned by files.tree. */
export interface FileTreeEntry {
  name: string
  kind: 'file' | 'dir'
  size: number
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
  /** Whether a login file exists in the home the launch signs in with. */
  login_found?: boolean
  /** The launch mode the agent starts in unless asked otherwise. */
  default_mode?: 'tui' | 'acp'
  /** Whether the agent prefers Enhanced, installed yet or not. */
  enhanced_default?: boolean
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
}

/** agent.install: a failed command is a result with `error`, not a refusal. */
export interface AgentInstallResult {
  log_tail: string
  installed: boolean
  enhanced_installed: boolean
  error?: string
}

/** A member-supplied custom harness launch definition (agent.register). */
export interface AgentDefinition {
  name: string
  executable?: string
  tui_args?: string[]
  headless_args?: string[]
  acp_args?: string[]
  profile_root?: string
  credential_paths?: string[]
  deny_names?: string[]
}

export interface ConfigRoot {
  harness: string
  display_name?: string
  path: string
  runtime_ignores: string[]
  credential_names: string[]
}

export interface ConfigFile {
  path: string
  content_base64: string
  mode: number
}

export interface ConfigImportParams {
  harness: string
  files: ConfigFile[]
}

export interface ConfigExclusion {
  path: string
  reason: string
  detail?: string
}

export interface ConfigImportResult {
  harness: string
  files: number
  bytes: number
  excluded: ConfigExclusion[]
  /** The import stopped; files and imported_paths identify confirmed writes, possibly none. */
  error?: string
  /** Canonical paths successfully installed before an incomplete import. */
  imported_paths?: string[]
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
  /** The profile this gateway runs on; absent on the top-level link. */
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

/** edge.link and edge.claim: the link just saved. */
export interface EdgeLinkResult {
  server_id: string
  /** Set by the Link step for a server it linked from the edge's list. */
  server_name?: string
  edge: string
  addr?: string
  user: string
  member: { id: string; display_name: string; role: string }
}

/** member.device.list: a client install a member reaches the server through
 * an edge with. `account` is the login, else email, of the edge account it
 * signed in as. A device waiting on an invitation has `invitation_id` and
 * an empty `member_id`. */
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

export type WorkspaceMirrorAuth = 'public' | 'deploy-key'

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
  /** The release asset for this platform, aether-<goos>-<goarch>. */
  asset?: string
  release_url?: string
  dev: boolean
  disabled: boolean
  /** False on Windows, where the binary cannot replace itself. */
  can_self_update: boolean
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
  | 'applying'
  | 'restarting'
  | 'failed'
  | 'cancelled'

/** The server.update event payload: one moment of a self-update. */
export interface ServerUpdatePayload {
  phase: ServerUpdatePhase
  version?: string
  actor_id?: string
  /** The real error behind a failed phase. */
  detail?: string
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
