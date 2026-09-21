// Wire types. These mirror internal/protocol/wire.go and internal/events;
// field names are the JSON names the server sends.

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

export interface Run {
  id: string
  workspace_id: string
  member_id: string
  /** Account backing the run; absent on servers predating account sharing. */
  account_member_id?: string
  task: string
  /** Latest terminal title, omitted by older servers and for empty titles. */
  title?: string
  harness: string
  mode: string
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
  /** Last run.status reason, sanitized like the event payload. */
  reason?: string
  /** Decorated by the gateway from the scheduler; absent on legacy servers. */
  paused?: boolean
  base_commit?: string
  base_branch?: string
  base_source?: string
  base_checked_at?: string | null
}
/** Release B mission orchestration wire objects. IDs and revisions are server authority. */
/** Where a mission sits in the human plan gate. Only `active` dispatches workers. */
export type MissionPhase = 'planning' | 'plan_review' | 'active' | 'rejected'
export type MissionPlanDecision = 'approve' | 'revise' | 'reject'
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
  max_concurrent_attempts: number
  max_total_attempts: number
  current_integrator_run_id: string
  integrator_generation: number
  accepted_set_version: number
  phase: MissionPhase
  plan_version: number
  /** Unanswered questions; only mission.show and mission.list compute it. */
  open_questions: number
  created_at: string
  updated_at: string
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

export interface MissionPlanReview {
  mission_id: string
  plan_version: number
  summary: string
  submitted_by_run_id: string
  submitted_at: string
  decision?: MissionPlanDecision
  feedback?: string
  decided_by_member_id?: string
  decided_at?: string | null
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
  /** Optional future-compatible field when the backend includes its hold. */
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
  plan_reviews?: MissionPlanReview[]
}

export interface MissionQuestionResult {
  question: MissionQuestion
}

export interface MissionPlanDecideResult {
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
  /** Data-directory usage, when the gateway reports it. */
  disk?: DiskUsage
}

export interface DiskUsage {
  used_bytes: number
  total_bytes: number
  /** What an unprivileged writer can still claim; the scheduler's floor. */
  free_bytes: number
  /** The four directories that grow without bound. */
  worktree_bytes: number
  transcript_bytes: number
  database_bytes: number
  /** The bare workspace repos; absent on servers predating the component. */
  repo_bytes?: number
}

/**
 * GET /api/v1/capabilities - what this gateway can do. Legacy remote
 * monitors do not serve it; a null result means "assume the remote
 * allowlist" on the client.
 */
export interface GatewayCapabilities {
  gateway: string
  methods: string[]
  ws: string[]
  local?: string[]
  /** The CLI build serving this gateway; absent before the field existed. */
  version?: string
  commit?: string
}

/** The member's persistent environment terminal status. */
export interface TerminalStatusResult {
  running: boolean
  image?: string
  saved_image?: string
  started_at?: string
  tabs?: string[]
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
}

export interface RoomDecideResult {
  message: RoomMessage
  receipt?: RoomDeliveryReceipt
}

export type RoomDeliveryReceipt = 'sent' | 'not_sent' | 'uncertain'

export type EvidenceTrigger = 'finish' | 'handoff' | 'report'

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
  event_boundary: number
  base_revision?: string
  retained_revision?: string
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


export interface RunStatusPayload {
  from?: RunStatus
  to: RunStatus
  reason?: string
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
/** Both null means the run was restored. */
export interface RunArchivedPayload {
  archived_at: string | null
  deletes_at: string | null
}

// Team surfaces: the approval inbox, the presence roster, cost and budgets,
// and the workspace timeline (internal/protocol approval.go, cost.go,
// timeline.go).

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

/**
 * Aggregated usage. `unmetered_runs` counts runs whose usage was never
 * measured, so while it is non-zero every total here is a floor.
 */
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

/** A workspace's budget with its state and the spend behind it. */
export interface BudgetReport {
  workspace_id: string
  budget?: Budget
  state: BudgetState
  spend: CostRollup
  advisory?: boolean
}

/** One page of workspace history, oldest first. */
export interface TimelinePage {
  events: Event[]
  next_seq: number
  more: boolean
}

export interface TimelineQuery {
  workspace_id: string
  run_id?: string
  member_id?: string
  types?: string[]
  after_seq?: number
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
  /**
   * The previous snapshot's tree, or the run's fork-point tree for the first
   * snapshot. Diffing `parent_tree` to `tree` is what this interval changed.
   * Both are absent on events from a server that predates per-snapshot trees.
   */
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

/** One files.read response. */
export interface FileRead {
  content: string
  truncated: boolean
  binary: boolean
  size: number
  revision: string
  writable: boolean
}

/** One files.diff response. */
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


/** One entry of agent.list; source is who supplied the harness. */
export interface AgentInfo {
  name: string
  source: 'shipped' | 'member'
  /** Whether the account's persistent environment contains the executable. */
  installed?: boolean
  /** Vendor installer command for shipped harnesses, when available. */
  install_script?: string
}

/** A member-supplied custom harness launch definition (agent.register). */
export interface AgentDefinition {
  name: string
  executable?: string
  tui_args?: string[]
  headless_args?: string[]
  profile_root?: string
  credential_paths?: string[]
  deny_names?: string[]
}

export interface ConfigRoot {
  harness: string
  path: string
  runtime_ignores: string[]
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
  /** Set only when the import stopped after installing one or more files. */
  error?: string
  /** Canonical paths successfully installed before an incomplete import. */
  imported_paths?: string[]
}

// The local gateway's client-machine verbs, POST /local/v1/<verb>
// (internal/localgw/local.go). Only a gateway with the user's repository
// and SSH key serves these; useCapability's hasLocal gates every caller.

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

/** github.probe: the gh in the member's environment terminal, before they
 * are told to log in with it. The two remedies are empty while gh is
 * usable; `admin_remedy` is set only when the server's own standard image
 * is the one without a usable gh. */
export interface GitHubProbeResult {
  status: GitHubCLIStatus
  version?: string
  minimum: string
  /** What gh, or the container that could not run it, printed. */
  detail?: string
  /** The image the terminal container runs, and the member's own saved
   * one when they have it. They differ while a container outlives the
   * image it should be on. */
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


/**
 * update.check: one release-check answer for the CLI on this machine
 * (internal/selfupdate). `dev` and `disabled` both mean no release was
 * resolved - a local build, or AETHER_NO_UPDATE_CHECK set - and neither
 * ever reports an update.
 */
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
  /**
   * Why the server half is unknown. The CLI half is about a binary on this
   * machine, so it is answered in full even when the SSH hop is down.
   */
  server_error?: string
  /** The desktop shell spawned this gateway, so it can restart it. */
  supervised: boolean
  /**
   * The error from the last desktop-app rebuild that failed, persisted by
   * the gateway to a file. Absent when the last rebuild succeeded or none
   * has run.
   */
  shell_build_error?: string
  /** The binary update.apply replaces, symlinks resolved. Absent when the
   * gateway could not probe it; install_method is absent with it. */
  cli_path?: string
  /**
   * How update.apply gets to write cli_path. `direct`: its directory is
   * writable and the update just happens. `admin-prompt`: macOS shows its
   * administrator password dialog first. `manual`: the gateway cannot
   * replace it (a root-owned directory on Linux, or Windows), so the member
   * runs `sudo aether update` in a terminal. Absent when the probe failed.
   */
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
  /**
   * Present when a co-located aether-server was replaced too: the running
   * server keeps the old code until this command restarts its unit.
   */
  restart_command?: string
  /**
   * True when the gateway started a desktop-app rebuild in the background
   * after swapping the CLI binary.
   */
  rebuilding: boolean
}

/** update.status: progress of a desktop-app rebuild running in this gateway
 * process. */
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

// The server's own update, from internal/protocol/serverupdate.go and the
// server.update event payload in internal/events/serverupdate.go. Calling
// server.update is admin only; reading the status is not, so a member who
// cannot press the button can still be told why the server is restarting.

/** One update recorded and waiting for an idle server. */
export interface PendingServerUpdate {
  version: string
  /** The member id that asked for it. */
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

/**
 * server.update_status: whether this server can replace its own binaries,
 * and what update is in flight. `capable` is false on the documented
 * unprivileged install - the binary directory is not writable by the
 * service user - and `manual_commands` then carries what to run on the
 * server host instead.
 */
export interface ServerUpdateStatus {
  server_version: string
  latest?: string
  update_available: boolean
  capable: boolean
  /** Which reason the server cannot update itself. */
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


/** env.harnesses: one setup-capable harness's local availability. */
export interface HarnessStatus {
  name: string
  installed: boolean
}

/** The env.harnesses verb result: the setup-capable harnesses plus, when
 * the saved link config knows exactly one repository folder, a prefill
 * suggestion for the wizard's from-repo input. */
export interface EnvHarnessesResult {
  harnesses: HarnessStatus[]
  /** The folders the gateway looked in, so an empty result can say where. */
  searched: string[]
  /** Why the login shell could not be asked for its PATH; set only when
   * the probe failed and just the standard folders were checked. */
  warning?: string
  repo_path?: string
}
