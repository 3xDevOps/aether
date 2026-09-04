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
  /** Latest terminal title, omitted by older servers and for empty titles. */
  title?: string
  harness: string
  mode: string
  status: RunStatus
  branch: string
  last_commit?: string
  last_commit_at?: string | null
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
  /** The server-owned image behind `neutral_image: true` environments. */
  neutral_image?: string
  /** The published standard environment image the server recommends for
   * new workspaces; absent on servers predating it. */
  standard_image?: string
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
  started_at?: string
  tabs?: string[]
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

export interface GitBranchPayload {
  workspace_id: string
  branch: string
  commit: string
}

export interface RunTitlePayload {
  title: string
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


/** One entry of agent.list; source is who supplied the harness. */
export interface AgentInfo {
  name: string
  source: 'shipped' | 'member'
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

/** The categories a profile preview groups files into, in report order. */
export type ProfileCategory =
  | 'memory'
  | 'skills'
  | 'commands'
  | 'settings'
  | 'mcp'
  | 'plugins'
  | 'other'

/** One category of a profile preview. `paths` is capped server-side, with
 * `truncated` set when it was cut; `files` and `bytes` stay exact. */
export interface ProfilePreviewCategory {
  category: ProfileCategory
  files: number
  bytes: number
  paths: string[]
  truncated: boolean
}

/** Why a file was left out of a profile push. `too-large` and
 * `over-budget` are the server's size caps, applied before the file is
 * ever read. `secret` is a scanner finding in a file the user wrote and
 * refuses the push; `vendored-secret` is one inside third-party content
 * the harness installed, which drops that file and lets the rest sync. */
export type ProfileExcludeReason =
  | 'credential'
  | 'secret'
  | 'vendored-secret'
  | 'ignored'
  | 'symlink'
  | 'too-large'
  | 'over-budget'
  | 'not-regular'

/** One file the guards left out of a profile push, and why. */
export interface ProfileExclusion {
  path: string
  reason: ProfileExcludeReason
  detail: string
}

/**
 * profile.preview: the discovery a push would run, uploading nothing.
 * `present` is false when this machine has no profile root for the harness
 * - a normal answer, not an error. `blocked` is true when the push would
 * be refused outright rather than partially carried; `blocked_reason`,
 * `blocked_path` and `blocked_detail` name which condition and where.
 * Only a `secret` has a CLI override, so a surface offering one must read
 * the reason rather than assume.
 */
export interface ProfilePreview {
  harness: string
  root: string
  present: boolean
  files: number
  bytes: number
  categories?: ProfilePreviewCategory[]
  /** Capped; `excluded_total` is how many there were. */
  excluded?: ProfileExclusion[]
  excluded_total?: number
  blocked: boolean
  blocked_reason?: ProfileExcludeReason
  blocked_path?: string
  blocked_detail?: string
}

/** profile.push: the snapshot the push created, and the files the size
 * caps left behind - the only place the user learns they are not on the
 * server, since the push itself succeeded without them. */
export interface ProfilePushResult {
  harness: string
  skipped?: ProfileExclusion[]
  snapshot_id: string
  digest: string
  files: number
  bytes: number
}

/** One harness in a profile scan's recommendation. A proposal, never an
 * action: the import is a separate profile.push the user approves. */
export interface HarnessRecommendation {
  harness: string
  import: boolean
  categories: ProfileCategory[]
  reason: string
}

/** What a `profile` scan answers instead of a Dockerfile and manifest. */
export interface ProfileRecommendation {
  harnesses: HarnessRecommendation[]
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
  current: boolean
  dirty: boolean
}

/** pull.switch: the run branch now checked out locally. */
export interface PullSwitchResult {
  branch: string
}
/** repo.push: the base branch seeded into the workspace. */
export interface RepoPushResult {
  branch: string
  remote: string
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

// Workspace environments: internal/protocol/environment.go and
// internal/domain/environment.go on the wire, plus the local gateway's
// env.harnesses verb and /ws/envscan frames.

/** Which path produced an environment definition. */
export type EnvironmentSource = 'mirror' | 'repo' | 'standard' | 'manual'

/** Definition lifecycle: saved -> building -> verifying -> active | failed. */
export type EnvironmentStatus =
  | 'saved'
  | 'building'
  | 'verifying'
  | 'active'
  | 'failed'

/**
 * One environment claim: what is installed, at which version, why, which
 * Dockerfile lines install it (1-based, inclusive - removing the item
 * removes those lines), and the command whose output must contain the
 * version during post-build verification.
 */
export interface ManifestItem {
  name: string
  version: string
  reason?: string
  start_line: number
  end_line: number
  check_command: string
}

/** One definition version in an env.status result. The manifest doubles
 * as the human-readable environment summary. */
export interface EnvironmentVersion {
  version: number
  source: EnvironmentSource
  harness?: string
  status: EnvironmentStatus
  failure_detail?: string
  active?: boolean
  manifest: ManifestItem[]
  created_at: string
  updated_at: string
}

/** env.status: every version newest first; active_version is absent while
 * no version is active. */
export interface EnvStatusResult {
  versions: EnvironmentVersion[]
  active_version?: number
}

/** The `environment.build` event payload: one moment of a build. `line`
 * carries engine output while building; `detail` explains a failure. */
export interface EnvironmentBuildPayload {
  version: number
  status: EnvironmentStatus
  line?: string
  detail?: string
}

/** env.get: one stored version in full. `diff` is a git unified diff of
 * the Dockerfile from the diff_against version to this one, present only
 * when requested and the files differ. */
export interface EnvGetResult {
  version: number
  dockerfile: string
  manifest: ManifestItem[]
  source: EnvironmentSource
  harness?: string
  status: EnvironmentStatus
  diff?: string
}

/** Coarse state of a server-side environment edit run; `proposed` and
 * `failed` are terminal. */
export type EnvironmentEditStatus =
  | 'running'
  | 'validating'
  | 'retrying'
  | 'proposed'
  | 'failed'

/** The `environment.edit` event payload: one moment of an edit run.
 * `line` carries agent output while running; `detail` explains a failure;
 * `version` names the proposed definition version, set on `proposed`. */
export interface EnvironmentEditPayload {
  harness: string
  status: EnvironmentEditStatus
  line?: string
  detail?: string
  version?: number
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
  repo_path?: string
}

/** The first frame the client sends on /ws/envscan. A repo scan names the
 * repository folder to read; a refine run carries the pair it starts from
 * and the user's feedback, plus the repo folder when that pair came from
 * a repo scan. A profile scan reads this machine's agent configuration and
 * takes the repo folder optionally. Inventory omits them all. */
export interface EnvScanRequest {
  harness: string
  mode: 'inventory' | 'repo' | 'refine' | 'profile'
  repo_path?: string
  previous_dockerfile?: string
  previous_manifest_json?: string
  feedback?: string
}

/** Coarse scan progress, in the order a run moves through them; a retry
 * re-enters running after retrying. */
export type EnvScanStatus = 'detecting' | 'running' | 'validating' | 'retrying'

/** The validated pair a successful scan produces. */
export interface EnvScanResult {
  dockerfile: string
  manifest: ManifestItem[]
}

/** The terminal success frame. Which half it carries follows the mode: a
 * pair for the three environment scans, a recommendation for `profile`. */
export type EnvScanResultFrame =
  | {
      type: 'result'
      dockerfile: string
      manifest: ManifestItem[]
      recommendation?: undefined
    }
  | {
      type: 'result'
      recommendation: ProfileRecommendation
      dockerfile?: undefined
    }

/** One frame from the gateway on /ws/envscan. `result` and `error` are
 * terminal; closing the socket cancels the scan. */
export type EnvScanFrame =
  | { type: 'output'; line: string }
  | { type: 'status'; status: EnvScanStatus }
  | EnvScanResultFrame
  | { type: 'error'; detail: string; output_tail?: string }
