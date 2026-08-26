// Wire types. These mirror internal/protocol/wire.go and internal/events;
// field names are the JSON names the server sends.

export type RunStatus =
  | 'queued'
  | 'provisioning'
  | 'running'
  | 'needs-attention'
  | 'merged'
  | 'abandoned'
  | 'failed'
  | 'interrupted'

export interface Run {
  id: string
  workspace_id: string
  member_id: string
  task: string
  harness: string
  mode: string
  status: RunStatus
  branch: string
  protected?: boolean
  created_at: string
  started_at: string | null
  finished_at: string | null
  profile_snapshot_id?: string
  /** Last run.status reason, sanitized like the event payload. */
  reason?: string
  /** Decorated by the gateway from the scheduler; absent on legacy servers. */
  paused?: boolean
}

export interface Workspace {
  id: string
  name: string
  base_branch: string
  steer_others?: string
  created_at: string
}

export interface Member {
  id: string
  display_name: string
  color: string
  role: 'viewer' | 'collaborator' | 'admin'
  pending?: boolean
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
  /** The three directories that grow without bound. */
  worktree_bytes: number
  transcript_bytes: number
  database_bytes: number
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

export interface RunStatusPayload {
  from?: RunStatus
  to: RunStatus
  reason?: string
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
  base_branch: string
  base_age?: string
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

/** Stable executable metadata; never a server filesystem path. */
export interface ToolManifest {
  executable?: string
  version?: string
  metadata?: Record<string, string>
}

/** Immutable member/workspace tool snapshot (internal/protocol/tools.go). */
export interface ToolSnapshot {
  id: string
  workspace_id: string
  member_id: string
  digest: string
  manifest: ToolManifest
  created_at: string
  /** Set on the snapshot currently active for the workspace. */
  active?: boolean
}

/** One entry of agent.list; source is who supplied the harness. */
export interface AgentInfo {
  name: string
  source: 'shipped' | 'member'
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

/** One immutable member+harness profile snapshot. */
export interface ProfileSnapshot {
  id: string
  harness: string
  digest: string
  created_at: string
}

/** One path in a profile snapshot listing (no content). */
export interface ProfileFileMeta {
  path: string
  digest: string
  mode?: number
}

/**
 * profile.status: the latest snapshot, its file list, and recent snapshots
 * for a rollback UI (internal/protocol ProfileStatusResult). `snapshot` is
 * absent when none exists.
 */
export interface ProfileStatus {
  snapshot?: ProfileSnapshot
  files?: ProfileFileMeta[]
  snapshots: ProfileSnapshot[]
}

// The local gateway's client-machine verbs, POST /local/v1/<verb>
// (internal/localgw/local.go). Only a gateway with the user's repository
// and SSH key serves these; useCapability's hasLocal gates every caller.

/** link.status: whether this gateway has a linked repository. */
export interface LinkStatus {
  linked: boolean
  addr: string
  user: string
  repo: string
  /** Named server profiles from `aether link --name`; absent when none. */
  links?: { name: string; addr: string }[]
  /** The profile this gateway runs on; absent on the top-level link. */
  active?: string
}

/** link.repo: the repo just linked and the git remote written into it. */
export interface LinkRepoResult {
  repo: string
  remote: string
  url: string
}

/** pull: the run branch fetched into the linked repository. */
export interface PullResult {
  branch: string
  ref: string
  output: string
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

/** image.scaffold: the files written (existing files are never overwritten). */
export interface ImageScaffoldResult {
  written: string[]
}
