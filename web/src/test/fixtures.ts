import type { Api } from '@/lib/api'
import type {
  AgentInfo,
  BudgetReport,
  Device,
  DevController,
  DevControlStatusParams,
  DevTerminal,
  DevTerminalTarget,
  EvidencePacket,
  InsightsResult,
  InsightsRun,
  Member,
  MemoryItem,
  Mission,
  MissionQuestion,
  MissionTask,
  MissionTaskRevision,
  RoomMessage,
  RoomStatusResult,
  Run,
  RunGitStatusResult,
  Schedule,
  ServerInfo,
  ServerUpdateStatus,
  SystemContainer,
  SystemStatsResult,
  Template,
  UpdateStatus,
  UsageResult,
  Workspace,
} from '@/lib/types'
import type { Candidate } from '@/lib/integration-types'
import type { StateContext } from '@/lib/needs-you'
import { toRecord, type RunRecord } from '@/store/runs'

/** A viewer's state context: Alice looking, nothing pending, at 10:20. */
export function stateContext(over: Partial<StateContext> = {}): StateContext {
  return {
    viewerID: alice.id,
    viewerRole: alice.role,
    members: { [alice.id]: alice, [bob.id]: bob },
    mergedInto: {},
    runs: {},
    workspaces: { [workspace.id]: workspace, [otherWorkspace.id]: otherWorkspace },
    roomMessages: {},
    roomStatus: {},
    missions: {},
    missionDetails: {},
    pausedRuns: {},
    now: Date.parse('2026-08-14T10:20:00Z'),
    ...over,
  }
}

export function runRecords(...runs: Run[]): Record<string, RunRecord> {
  return Object.fromEntries(runs.map((r) => [r.id, toRecord(r)]))
}
export const alice: Member = {
  id: 'mem_alice',
  display_name: 'Alice',
  color: '#e6194b',
  role: 'admin',
}

export const bob: Member = {
  id: 'mem_bob',
  display_name: 'Bob',
  color: '#3cb44b',
  role: 'collaborator',
}

export const vera: Member = {
  id: 'mem_vera',
  display_name: 'Vera',
  color: '#f58231',
  role: 'viewer',
}

export const workspace: Workspace = {
  id: 'wsp_1',
  name: 'main-repo',
  base_branch: 'main',
  created_at: '2026-08-14T08:00:00Z',
}

export const integrationCandidate: Candidate = {
  candidate_id: 'candidate_fixture',
  workspace_id: workspace.id,
  submissions: [],
  inputs: [],
  target_ref: 'refs/heads/main',
  expected_target_revision: 'fixture-target-revision',
  state: 'preparing',
  applied_inputs: 0,
  verifications: [],
  mutations: [],
  created_at: '2026-08-14T10:05:00Z',
  expires_at: '2026-09-14T10:05:00Z',
  version: 1,
}

export const otherWorkspace: Workspace = {
  id: 'wsp_2',
  name: 'docs-site',
  base_branch: 'main',
  created_at: '2026-08-14T07:00:00Z',
}
export function mission(over: Partial<Mission> = {}): Mission {
  return {
    id: 'mission_1',
    workspace_id: workspace.id,
    objective: 'coordinate checkout work',
    accountable_human_id: alice.id,
    integrator: { account_member_id: alice.id, harness: 'claude', mode: 'tui' },
    execution_choices: [
      { account_member_id: alice.id, harness: 'claude', mode: 'headless' },
      { account_member_id: alice.id, harness: 'claude', mode: 'tui' },
    ],
    current_integrator_run_id: 'run_integrator',
    integrator_generation: 1,
    accepted_set_version: 0,
    phase: 'active',
    open_questions: 0,
    created_at: '2026-08-14T10:00:00Z',
    updated_at: '2026-08-14T10:00:00Z',
    ...over,
  }
}

export function missionQuestion(over: Partial<MissionQuestion> = {}): MissionQuestion {
  return {
    id: 'question_1',
    mission_id: 'mission_1',
    seq: 1,
    body: 'which checkout flow?',
    asked_by_run_id: 'run_integrator',
    asked_at: '2026-08-14T10:01:00Z',
    ...over,
  }
}

export function missionTaskRevision(over: Partial<MissionTaskRevision> = {}): MissionTaskRevision {
  return {
    task_id: 'task_1',
    revision: 1,
    title: 'rewrite the guest checkout flow',
    objective: 'replace the legacy guest checkout controller',
    scope: { expected_paths: ['web/checkout/'] },
    evidence_requirements: [],
    status: 'accepted',
    created_at: '2026-08-14T10:02:00Z',
    ...over,
  }
}

export function missionTask(over: Partial<MissionTask> = {}): MissionTask {
  return {
    id: 'task_1',
    mission_id: 'mission_1',
    current_revision: 1,
    revision: missionTaskRevision(),
    status: 'ready',
    created_at: '2026-08-14T10:02:00Z',
    updated_at: '2026-08-14T10:02:00Z',
    ...over,
  }
}

export function run(over: Partial<Run> = {}): Run {
  return {
    id: 'run_1',
    workspace_id: workspace.id,
    member_id: alice.id,
    account_member_id: alice.id,
    task: 'rewrite the checkout flow',
    harness: 'claude',
    mode: 'tui',
    status: 'running',
    state: 'working',
    status_seq: 0,
    branch: 'aether/run-1-checkout',
    created_at: '2026-08-14T10:01:00Z',
    started_at: '2026-08-14T10:02:00Z',
    finished_at: null,
    ...over,
  }
}

export const serverInfo: ServerInfo = {
  server_version: '1.2.3',
  protocol_version: '1',
  time: '2026-08-14T10:05:00Z',
  member: alice,
  disk: {
    used_bytes: 512 * 1024 * 1024,
    total_bytes: 2 * 1024 * 1024 * 1024,
    free_bytes: 1536 * 1024 * 1024,
    worktree_bytes: 256 * 1024 * 1024,
    transcript_bytes: 128 * 1024 * 1024,
    database_bytes: 64 * 1024 * 1024,
    repo_bytes: 512 * 1024 * 1024,
  },
}

/** A device waiting on an invitation that adds a new collaborator. */
export function pendingDevice(over: Partial<Device> = {}): Device {
  return {
    id: 'dev_1',
    member_id: '',
    invitation_id: 'inv_1',
    provider: 'github',
    account: 'mallory',
    label: 'laptop',
    status: 'pending',
    fingerprint: 'SHA256:examplefingerprint',
    created_at: '2026-08-14T10:04:00Z',
    role: 'collaborator',
    ...over,
  }
}

export const template: Template = {
  id: 'tpl_1',
  workspace_id: workspace.id,
  name: 'nightly triage',
  task: 'triage the flaky tests',
  harness: 'claude',
  mode: 'headless',
  created_at: '2026-08-14T09:00:00Z',
}

export function schedule(over: Partial<Schedule> = {}): Schedule {
  return {
    id: 'sch_1',
    workspace_id: workspace.id,
    template: template.name,
    cron: '0 3 * * *',
    member_id: alice.id,
    created_at: '2026-08-14T09:30:00Z',
    next_fire_at: '2026-08-15T03:00:00Z',
    ...over,
  }
}


export function agentInfo(over: Partial<AgentInfo> = {}): AgentInfo {
  return {
    name: 'claude',
    source: 'shipped',
    installed: true,
    install_script: 'curl -fsSL https://claude.ai/install.sh | bash',
    ...over,
  }
}

export function budget(
  workspaceID: string,
  over: Partial<BudgetReport> = {},
): BudgetReport {
  return {
    workspace_id: workspaceID,
    state: 'ok',
    spend: {
      runs: 1,
      metered_runs: 1,
      unmetered_runs: 0,
      input_tokens: 1000,
      output_tokens: 200,
      cost_usd: 0.5,
    },
    ...over,
  }
}


/** One update.check answer: a CLI a release behind, a current server. */
export function updateStatus(over: Partial<UpdateStatus> = {}): UpdateStatus {
  return {
    cli: {
      version: 'v1.2.3',
      commit: 'abc1234',
      latest: 'v1.3.0',
      update_available: true,
      asset: 'aether-linux-amd64',
      release_url: 'https://github.com/3xDevOps/Aether/releases/tag/v1.3.0',
      dev: false,
      disabled: false,
      can_self_update: true,
      checked_at: '2026-08-14T10:00:00Z',
    },
    server_version: 'v1.3.0',
    server_behind: false,
    supervised: true,
    cli_path: '/home/user/.local/bin/aether',
    install_method: 'direct',
    can_self_update: true,
    ...over,
  }
}

/** A current server that could replace its own binaries. */
export function serverUpdateStatus(
  over: Partial<ServerUpdateStatus> = {},
): ServerUpdateStatus {
  return {
    server_version: 'v1.2.3',
    latest: 'v1.3.0',
    update_available: false,
    capable: true,
    ...over,
  }
}
export function roomMessage(over: Partial<RoomMessage> = {}): RoomMessage {
  return {
    id: 'message_1',
    workspace_id: workspace.id,
    run_id: 'run_1',
    actor_id: alice.id,
    kind: 'comment',
    body: 'hello',
    state: 'sent',
    created_at: '2026-08-14T10:00:00Z',
    updated_at: '2026-08-14T10:00:00Z',
    ...over,
  }
}

export function evidencePacket(over: Partial<EvidencePacket> = {}): EvidencePacket {
  return {
    id: 'packet_1',
    workspace_id: workspace.id,
    run_id: 'run_1',
    creator_id: alice.id,
    trigger: 'report',
    objective: 'Inspect the change',
    captured_at: '2026-08-14T10:00:00Z',
    availability: 'available',
    event_boundary: 1,
    created_at: '2026-08-14T10:00:00Z',
    updated_at: '2026-08-14T10:00:00Z',
    ...over,
  }
}


export function memoryItem(over: Partial<MemoryItem> = {}): MemoryItem {
  return {
    id: 'note-a1b2c3d4e5',
    workspace_id: workspace.id,
    kind: 'convention',
    scope: 'workspace',
    source: 'member',
    title: 'Use bun in web/; never npm.',
    body: 'Use bun in web/; never npm.',
    author_id: alice.id,
    author_name: alice.display_name,
    created_at: '2026-10-18T09:00:00Z',
    updated_at: '2026-10-18T09:00:00Z',
    ...over,
  }
}

export function insightsRun(over: Partial<InsightsRun> = {}): InsightsRun {
  return {
    run_id: 'run_1',
    title: 'Fix the checkout flow',
    workspace_id: workspace.id,
    status: 'completed',
    reason: '',
    harness: 'claude',
    model: '',
    created_at: '2026-10-14T09:00:00Z',
    finished_at: '2026-10-14T09:21:00Z',
    duration_seconds: 1260,
    mission_id: '',
    pr_url: '',
    pr_state: '',
    ...over,
  }
}

const insightsTotals = { launched: 0, completed: 0, merged: 0, failed: 0, interrupted: 0, abandoned: 0, killed: 0, active: 0, finished: 0 }

/** insights.get for 2026-10-09 to 2026-10-16 in UTC. */
const outcomeSeries = Array.from({ length: 7 }, (_, i) => ({
  start: `2026-10-${String(9 + i).padStart(2, '0')}`,
  runs: { done: 0, failed: 0, abandoned: 0 },
  swarms: { done: 0, failed: 0, abandoned: 0 },
}))

export function insights(over: Partial<InsightsResult> = {}): InsightsResult {
  const series = Array.from({ length: 7 }, (_, i) => ({
    start: `2026-10-${String(9 + i).padStart(2, '0')}`,
    launched: 0, completed: 0, merged: 0, failed: 0, interrupted: 0, abandoned: 0,
  }))
  series[5] = { ...series[5], launched: 6, completed: 4, merged: 1, failed: 1 }
  series[6] = { ...series[6], launched: 4, completed: 2, interrupted: 1 }
  const end = Date.UTC(2026, 9, 15)
  const heatmap = Array.from({ length: 182 }, (_, i) => ({
    date: new Date(end - (181 - i) * 86_400_000).toISOString().slice(0, 10),
    runs: 0,
  }))
  heatmap[181] = { ...heatmap[181], runs: 3 }
  heatmap[180] = { ...heatmap[180], runs: 6 }
  return {
    generated_at: '2026-10-15T18:00:00Z',
    from: '2026-10-09T00:00:00Z',
    to: '2026-10-16T00:00:00Z',
    time_zone: 'UTC',
    bucket: 'day',
    totals: { ...insightsTotals, launched: 10, completed: 6, merged: 1, failed: 1, interrupted: 1, finished: 7, active: 1 },
    previous: { ...insightsTotals, launched: 8, completed: 4, failed: 2, finished: 4 },
    series,
    heatmap,
    by_workspace: [
      { workspace_id: workspace.id, name: workspace.name, launched: 7, finished: 5, failed: 1 },
      { workspace_id: otherWorkspace.id, name: otherWorkspace.name, launched: 3, finished: 2, failed: 1 },
    ],
    by_agent: [
      { harness: 'claude', model: '', launched: 7, finished: 5, failed: 1 },
      { harness: 'codex', model: 'gpt-5.5', launched: 3, finished: 2, failed: 1 },
    ],
    swarms: { launched: 1, completed: 1, cancelled: 0, agents: 5, tasks_accepted: 4 },
    durations: {
      runs_measured: 9,
      median_run_seconds: 1260,
      p90_run_seconds: 4100,
      state_runs_measured: 6,
      median_working_seconds: 900,
      median_waiting_seconds: 240,
      median_response_seconds: 75,
    },
    cost: {
      metered_runs: 3,
      unmetered_runs: 6,
      cost_usd: 4.12,
      input_tokens: 120000,
      output_tokens: 9000,
      total_tokens: 129000,
      by_agent: [
        { harness: 'claude', metered_runs: 2, unmetered_runs: 3, cost_usd: 3.2, input_tokens: 92000, output_tokens: 8000, total_tokens: 100000 },
        { harness: 'codex', metered_runs: 1, unmetered_runs: 3, cost_usd: 0.92, input_tokens: 28000, output_tokens: 1000, total_tokens: 29000 },
      ],
    },
    pull_requests: { opened: 2, merged: 1 },
    success: { runs: { succeeded: 3, finished: 9 }, swarms: { succeeded: 1, finished: 1 } },
    outcomes: {
      runs: { done: 7, failed: 1, abandoned: 1 },
      swarms: { done: 1, failed: 0, abandoned: 0 },
      series: outcomeSeries.map((day, i) => i === 5 ? { ...day, runs: { done: 5, failed: 1, abandoned: 0 } } : i === 6 ? { ...day, runs: { done: 2, failed: 0, abandoned: 1 }, swarms: { done: 1, failed: 0, abandoned: 0 } } : day),
      members: [],
    },
    recent: [
      insightsRun({ run_id: 'run_3', title: 'Retry flaky upload', status: 'interrupted', harness: 'codex', model: 'gpt-5.5', workspace_id: otherWorkspace.id, finished_at: '2026-10-15T11:00:00Z' }),
      insightsRun({ run_id: 'run_2', title: 'Add the export button', status: 'merged', finished_at: '2026-10-15T10:00:00Z', pr_url: 'https://github.com/acme/app/pull/42', pr_state: 'merged' }),
      insightsRun(),
    ],
    needs_you: 2,
    coverage: { state_times_since: '2026-10-12T12:00:00Z', pull_requests_since: '2026-10-12T12:00:00Z' },
    ...over,
  }
}

export function fakeApi(over: Partial<Api> = {}): Api {
  const terminals = new Map<string, Map<string, DevTerminal>>()
  const controllers = new Map<string, DevController>()
  let terminalSequence = 0
  let controlGeneration = 0
  const gitStatus: RunGitStatusResult = {
    branch: run().branch,
    head: '1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b',
    detached: false,
    unborn: false,
    changes: [],
    truncated: false,
    account_member_id: alice.id,
    account_name: alice.display_name,
    identity: 'alice',
    remotes: [],
    output: { exit_code: 0, truncated: false },
  }
  const gitState = { branch: gitStatus.branch, head: gitStatus.head }
  function findTerminal(params: DevTerminalTarget): DevTerminal {
    const terminal = terminals.get(params.run_id)?.get(params.terminal_id)
    if (!terminal || terminal.incarnation !== params.incarnation) {
      throw new Error('Test terminal does not exist or its incarnation changed')
    }
    return terminal
  }
  function controlKey(params: DevControlStatusParams): string {
    return JSON.stringify([params.run_id, params.surface.kind, params.surface.id, params.surface.incarnation])
  }
  function needsOverride(method: string): never {
    throw new Error(`fakeApi: override ${method} to exercise this operation`)
  }
  return {
    serverInfo: vi.fn(async () => serverInfo),
    serverEdge: vi.fn(async () => needsOverride('serverEdge')),
    serverEdgeClaimCode: vi.fn(async () => needsOverride('serverEdgeClaimCode')),
    workspaceGet: vi.fn(async () => workspace),
    memberList: vi.fn(async () => ({ members: [alice, bob] })),
    accountUsage: vi.fn(async (): Promise<UsageResult> => ({
      account_member_id: alice.id,
      providers: [],
    })),
    accountList: vi.fn(async () => ({ accounts: [alice], shared_with: [] })),
    insightsGet: vi.fn(async () => insights()),
    systemStats: vi.fn(async () => systemStats()),
    memorySearch: vi.fn<Api['memorySearch']>(async () => ({ items: [] })),
    memoryShow: vi.fn<Api['memoryShow']>(async () => needsOverride('memoryShow')),
    memoryBrief: vi.fn<Api['memoryBrief']>(async () => ({ text: '' })),
    memoryAdd: vi.fn<Api['memoryAdd']>(async () => needsOverride('memoryAdd')),
    memoryPin: vi.fn<Api['memoryPin']>(async () => needsOverride('memoryPin')),
    memoryUnpin: vi.fn<Api['memoryUnpin']>(async () => needsOverride('memoryUnpin')),
    memoryRm: vi.fn<Api['memoryRm']>(async () => needsOverride('memoryRm')),
    accountShare: vi.fn(async () => ({})),
    accountRevoke: vi.fn(async () => ({})),
    runList: vi.fn(async () => [run()]),
    runGet: vi.fn(async () => run()),
    missionCreate: vi.fn(async () => ({ mission: mission() })),
    missionShow: vi.fn(async () => ({ mission: mission(), tasks: [], attempts: [], submissions: [], diagnostics: [], questions: [] })),
    missionList: vi.fn(async () => ({ missions: [mission()], next_cursor: undefined })),
    missionQuestionAnswer: vi.fn(async () => ({ question: missionQuestion({ answer: 'the guest flow', answered_by_member_id: alice.id, answered_at: '2026-08-14T10:03:00Z' }) })),
    missionCancel: vi.fn(async () => ({ mission: mission({ phase: 'cancelled' }) })),
    missionArchive: vi.fn(async () => mission({ phase: 'completed', archived_at: '2026-10-06T00:00:00Z' })),
    missionUnarchive: vi.fn(async () => mission({ phase: 'completed' })),
    missionDelete: vi.fn(async () => ({})),
    missionWorkerRelease: vi.fn(async () => ({ run_id: 'run_worker', takeover_active: false, takeover_generation: 2 })),
    missionReplaceIntegrator: vi.fn(async () => ({ mission: mission(), run_id: 'run_integrator' })),
    runLaunch: vi.fn(async () => run()),
    runKill: vi.fn(async () => ({})),
    runRelease: vi.fn(async () => ({})),
    runGitStatus: vi.fn<Api['runGitStatus']>(async () => ({
      ...gitStatus, changes: [], remotes: [], output: { ...gitStatus.output },
    })),
    runGitDiff: vi.fn<Api['runGitDiff']>(async () => ({
      state: { ...gitState }, output: { exit_code: 0, truncated: false },
    })),
    runGitLog: vi.fn<Api['runGitLog']>(async () => ({
      branch: gitStatus.branch, base_ref: 'main', base: gitStatus.head, head: gitStatus.head, merge_base: gitStatus.head,
      ahead: 0, behind: 0, commits: [], upstream: [], conflicts: [], truncated: false,
      output: { exit_code: 0, truncated: false },
    })),
    runGitCommit: vi.fn<Api['runGitCommit']>(async () => needsOverride('runGitCommit')),
    runGitPush: vi.fn<Api['runGitPush']>(async () => needsOverride('runGitPush')),
    runPRStatus: vi.fn<Api['runPRStatus']>(async () => ({
      identity: gitStatus.identity,
      account_member_id: alice.id,
      pull_request: null,
      output: { exit_code: 0, truncated: false },
    })),
    runPRCreate: vi.fn<Api['runPRCreate']>(async () => needsOverride('runPRCreate')),
    runPRFeedback: vi.fn<Api['runPRFeedback']>(async () => ({
      identity: gitStatus.identity,
      account_member_id: alice.id,
      pull_request: null,
      checks: [], comments: [], reviews: [], review_comments: [],
      truncated: false,
      output: { exit_code: 0, truncated: false },
    })),
    runPRMerge: vi.fn<Api['runPRMerge']>(async () => needsOverride('runPRMerge')),
    workspaceImport: vi.fn<Api['workspaceImport']>(async () => needsOverride('workspaceImport')),
    devTerminalList: vi.fn<Api['devTerminalList']>(async ({ run_id }) => ({
      terminals: Array.from(terminals.get(run_id)?.values() ?? [], (terminal) => ({
        ...terminal, process: { ...terminal.process },
      })),
    })),
    devTerminalStart: vi.fn<Api['devTerminalStart']>(async ({ run_id, name, cols, rows }) => {
      const inventory = terminals.get(run_id) ?? new Map<string, DevTerminal>()
      if (Array.from(inventory.values()).filter((terminal) => terminal.process.state === 'running').length >= 4) {
        throw new Error('Test run already has four active terminals')
      }
      terminalSequence++
      const terminal: DevTerminal = {
        terminal_id: `terminal_${terminalSequence}`,
        incarnation: `incarnation_${terminalSequence}`,
        name: name || `Terminal ${terminalSequence}`,
        started_by: 'member',
        cols: cols || 80,
        rows: rows || 24,
        process: { state: 'running' },
      }
      inventory.set(terminal.terminal_id, terminal)
      terminals.set(run_id, inventory)
      return { terminal: { ...terminal, process: { ...terminal.process } } }
    }),
    devTerminalOutput: vi.fn<Api['devTerminalOutput']>(async () => needsOverride('devTerminalOutput')),
    devTerminalScreen: vi.fn<Api['devTerminalScreen']>(async () => needsOverride('devTerminalScreen')),
    devTerminalScreenshot: vi.fn<Api['devTerminalScreenshot']>(async () => needsOverride('devTerminalScreenshot')),
    devTerminalInput: vi.fn<Api['devTerminalInput']>(async (params) => {
      if (findTerminal(params).process.state !== 'running') throw new Error('Test terminal is not running')
      return { accepted: true }
    }),
    devTerminalResize: vi.fn<Api['devTerminalResize']>(async (params) => {
      const current = findTerminal(params)
      const terminal = { ...current, cols: params.cols, rows: params.rows }
      terminals.get(params.run_id)!.set(params.terminal_id, terminal)
      return { terminal, screen_revision: 1, geometry_revision: 1 }
    }),
    devTerminalWait: vi.fn<Api['devTerminalWait']>(async () => needsOverride('devTerminalWait')),
    devTerminalStop: vi.fn<Api['devTerminalStop']>(async (params) => {
      const current = findTerminal(params)
      const terminal: DevTerminal = { ...current, process: { state: 'stopped' } }
      terminals.get(params.run_id)!.set(params.terminal_id, terminal)
      return { terminal, stopped: true, timed_out: false }
    }),
    devControlStatus: vi.fn<Api['devControlStatus']>(async (params) => ({
      surface: params.surface, controller: controllers.get(controlKey(params)) ?? null,
    })),
    devControlAcquire: vi.fn<Api['devControlAcquire']>(async (params) => {
      const key = controlKey(params)
      const displaced = controllers.get(key)
      if (displaced && displaced.control_session_id !== params.control_session_id && !params.takeover) {
        throw new Error('Test surface already has a controller')
      }
      const controller: DevController = {
        kind: 'member',
        member_id: alice.id,
        control_session_id: params.control_session_id,
        control_generation: ++controlGeneration,
        connected: true,
        acquired_at: '2026-08-14T10:05:00Z',
      }
      controllers.set(key, controller)
      return { surface: params.surface, controller, ...(displaced ? { displaced } : {}) }
    }),
    devControlRelease: vi.fn<Api['devControlRelease']>(async (params) => {
      const key = controlKey(params)
      const controller = controllers.get(key)
      const released = controller?.control_session_id === params.control_session_id &&
        controller.control_generation === params.control_generation
      if (released) controllers.delete(key)
      return { released }
    }),
    devBrowserStatus: vi.fn<Api['devBrowserStatus']>(async () => ({
      available: false, running: false, state: 'unavailable', reason: 'No browser configured in this test',
    })),
    devBrowserPages: vi.fn<Api['devBrowserPages']>(async () => ({ pages: [] })),
    devBrowserOpen: vi.fn<Api['devBrowserOpen']>(async () => needsOverride('devBrowserOpen')),
    devBrowserNavigate: vi.fn<Api['devBrowserNavigate']>(async () => needsOverride('devBrowserNavigate')),
    devBrowserSnapshot: vi.fn<Api['devBrowserSnapshot']>(async () => needsOverride('devBrowserSnapshot')),
    devBrowserInspect: vi.fn<Api['devBrowserInspect']>(async () => needsOverride('devBrowserInspect')),
    devBrowserAction: vi.fn<Api['devBrowserAction']>(async () => needsOverride('devBrowserAction')),
    devBrowserScreenshot: vi.fn<Api['devBrowserScreenshot']>(async () => needsOverride('devBrowserScreenshot')),
    devBrowserViewport: vi.fn<Api['devBrowserViewport']>(async () => needsOverride('devBrowserViewport')),
    devBrowserWait: vi.fn<Api['devBrowserWait']>(async () => needsOverride('devBrowserWait')),
    devBrowserConsole: vi.fn<Api['devBrowserConsole']>(async () => ({
      entries: [], next: 0, missing_cursor: false, truncated: false,
    })),
    devBrowserNetwork: vi.fn<Api['devBrowserNetwork']>(async () => ({
      entries: [], next: 0, missing_cursor: false, truncated: false,
    })),
    devBrowserReset: vi.fn<Api['devBrowserReset']>(async () => needsOverride('devBrowserReset')),
    devBrowserClose: vi.fn<Api['devBrowserClose']>(async () => needsOverride('devBrowserClose')),
    devArtifactList: vi.fn<Api['devArtifactList']>(async () => ({ artifacts: [], truncated: false })),
    devArtifactGet: vi.fn<Api['devArtifactGet']>(async () => needsOverride('devArtifactGet')),
    devArtifactDelete: vi.fn<Api['devArtifactDelete']>(async () => needsOverride('devArtifactDelete')),
    devArtifactRetain: vi.fn<Api['devArtifactRetain']>(async () => needsOverride('devArtifactRetain')),
    devPortList: vi.fn<Api['devPortList']>(async () => ({ ports: [] })),
    devArtifactDownload: vi.fn<Api['devArtifactDownload']>(async () => needsOverride('devArtifactDownload')),
    runDelete: vi.fn(async () => ({})),
    runPause: vi.fn(async () => ({})),
    runResume: vi.fn(async () => ({})),
    runInject: vi.fn(async () => ({ message: roomMessage({ kind: 'steer_request', state: 'queued' }) })),
    roomImage: vi.fn(async () => needsOverride('roomImage')),
    assistantStatus: vi.fn(async () => needsOverride('assistantStatus')),
    assistantStart: vi.fn(async () => needsOverride('assistantStart')),
    assistantPrompt: vi.fn(async () => needsOverride('assistantPrompt')),
    assistantView: vi.fn(async () => ({})),
    assistantCancel: vi.fn(async () => needsOverride('assistantCancel')),
    assistantAnswer: vi.fn(async () => needsOverride('assistantAnswer')),
    assistantConfirm: vi.fn(async () => needsOverride('assistantConfirm')),
    assistantItem: vi.fn(async () => needsOverride('assistantItem')),
    assistantReset: vi.fn(async () => needsOverride('assistantReset')),
    assistantSettings: vi.fn(async () => ({ auto_approve: false, suggest_auto_approve: false, suggestion_off: false, share_view: true })),
    assistantHistory: vi.fn(async () => needsOverride('assistantHistory')),
    runInputAnswer: vi.fn(async () => needsOverride('runInputAnswer')),
    runACPCancel: vi.fn(async () => needsOverride('runACPCancel')),
    runACPSetOption: vi.fn(async () => needsOverride('runACPSetOption')),
    runACPHistory: vi.fn(async () => needsOverride('runACPHistory')),
    runACPItem: vi.fn(async () => needsOverride('runACPItem')),
    runModeSwitch: vi.fn(async () => needsOverride('runModeSwitch')),
    runClose: vi.fn(async () => run({ status: 'merged' })),
    runReview: vi.fn(async (runID: string, action: 'complete' | 'later') =>
      run({ id: runID, status: action === 'complete' ? 'completed' : 'needs-attention', state: action === 'complete' ? '' : 'finished', review: action === 'complete' ? 'accepted' : 'later' })),
    runHandoff: vi.fn(async () => ({})),
    runRoomList: vi.fn(async () => ({ messages: [] })),
    coordMessagesList: vi.fn(async () => ({ messages: [] })),
    runRoomStatus: vi.fn(async (): Promise<RoomStatusResult> => ({
      workspace_id: workspace.id,
      run_id: 'run_1',
      protected: false,
      watchers: [],
      queued_steers: 0,
    })),
    runRoomPost: vi.fn(async () => ({ message: roomMessage() })),
    runRoomDecide: vi.fn(async () => ({ message: roomMessage({ state: 'denied' }) })),
    runEvidenceList: vi.fn(async () => ({ packets: [] })),
    runEvidenceGet: vi.fn(async () => ({ packet: evidencePacket() })),
    runEvidencePatch: vi.fn(async () => ({ packet: evidencePacket(), patch: '', truncated: false })),
    runEvidenceTranscript: vi.fn(async () => ({ packet: evidencePacket(), data_base64: '', truncated: false })),
    integrationPrepare: vi.fn(async () => ({ candidate: integrationCandidate })),
    integrationShow: vi.fn(async () => ({ candidate: integrationCandidate })),
    integrationList: vi.fn(async () => ({ candidates: [] })),
    integrationResolve: vi.fn(async () => ({ candidate: integrationCandidate })),
    integrationVerify: vi.fn(async () => ({ candidate: integrationCandidate })),
    integrationRequestDelivery: vi.fn(async () => ({ candidate: integrationCandidate })),
    integrationDecide: vi.fn(async () => ({ candidate: integrationCandidate })),
    integrationDeliver: vi.fn(async () => ({ candidate: integrationCandidate })),
    integrationPatch: vi.fn(async () => ({ patch: '', truncated: false })),
    integrationDelete: vi.fn(async () => ({})),
    presenceRoster: vi.fn(async () => []),
    presenceHeartbeat: vi.fn(async () => 90),
    presenceTyping: vi.fn(async () => undefined),
    budgetGet: vi.fn(async (workspaceID: string) => budget(workspaceID)),
    templateList: vi.fn(async () => [template]),
    templateLaunch: vi.fn(async () => ({
      run: run({ id: 'run_tpl' }),
      base_commit: '1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b',
      base_branch: 'main',
      base_source: 'local',
      base_checked_at: '2026-08-14T10:05:00Z',
    })),
    workspaceTimeline: vi.fn(async () => ({
      events: [],
      next_seq: 0,
      more: false,
    })),
    runOverlaps: vi.fn(async () => []),
    runPatch: vi.fn(async (runID: string) => ({
      run_id: runID,
      base: 'basesha0',
      patch: '',
      truncated: false,
    })),
    filesTree: vi.fn(async () => ({ entries: [] })),
    filesRead: vi.fn(async () => ({
      content: '', truncated: false, binary: false, size: 0, revision: '', writable: true,
    })),
    filesWrite: vi.fn(async ({ content }) => ({
      content, truncated: false, binary: false,
      size: new TextEncoder().encode(content).length, revision: 'saved', writable: true,
    })),
    filesDiff: vi.fn(async () => ({ patch: '', truncated: false })),
    disk: vi.fn(async () => ({
      used_bytes: 512 * 1024 * 1024,
      total_bytes: 2 * 1024 * 1024 * 1024,
      free_bytes: 1536 * 1024 * 1024,
      worktree_bytes: 256 * 1024 * 1024,
      transcript_bytes: 128 * 1024 * 1024,
      database_bytes: 64 * 1024 * 1024,
      repo_bytes: 512 * 1024 * 1024,
    })),
    capabilities: vi.fn(async () => ({
      gateway: 'remote',
      methods: ['*'],
      ws: ['events', 'attach', 'terminal'],
    })),
    eventsSocket: vi.fn(() => 'ws://localhost/ws/events'),
    attachSocket: vi.fn((runID: string) => `ws://localhost/ws/attach/${runID}`),
    assistantSocket: vi.fn(() => 'ws://localhost/ws/assistant'),
    acpSocket: vi.fn((runID: string) => `ws://localhost/ws/acp/${runID}`),
    attachShellSocket: vi.fn(
      (runID: string, tab: string) =>
        `ws://localhost/ws/attach/${runID}?shell=${encodeURIComponent(tab)}`,
    ),
    terminalStatus: vi.fn(async () => ({ running: false, tabs: [] })),
    terminalHistory: vi.fn(async () => ({ lines: [], has_more: false })),
    uploadTerminalImage: vi.fn(async () => ({ path: '/home/alice/.aether/uploads/image.png' })),
    uploadTerminalFile: vi.fn(async (file: File) => ({ path: `/home/alice/.aether/terminal-files/${'0'.repeat(32)}/${file.name}` })),
    terminalStop: vi.fn(async () => ({})),
    envSave: vi.fn(async () => ({ image: 'aether/member-1:123' })),
    envReset: vi.fn(async () => ({})),
    githubConnect: vi.fn(async () => ({
      login: 'octocat',
      signing_key: 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI aether mbr_1',
      fingerprint: 'SHA256:9wPnHRtG0DPQNo8VYbC2mSczRRRUYY7NoLgTHTAlYFA',
    })),
    githubOAuthStart: vi.fn(async () => ({ state: 'pending' as const, session_id: 'oauth_test', user_code: 'ABCD-EFGH', verification_url: 'https://github.com/login/device' })),
    githubOAuthStatus: vi.fn(async () => ({ state: 'disconnected' as const })),
    githubOAuthCancel: vi.fn(async () => ({ state: 'cancelled' as const })),
    githubRepositories: vi.fn(async () => ({ account: { id: 1, login: 'octocat' }, repositories: [] })),
    githubProbe: vi.fn(async () => ({
      status: 'ok' as const,
      version: '2.100.0',
      minimum: '2.81.0',
      detail: 'gh version 2.100.0 (2026-09-03)',
      image: 'ghcr.io/3xdevops/aether-standard:latest',
    })),
    terminalSocket: vi.fn(
      (tab: string) => `ws://localhost/ws/terminal?tab=${encodeURIComponent(tab)}`,
    ),
    memberInvite: vi.fn(async () => ({
      code: 'inv-code-1',
      expires_at: '2026-08-15T10:00:00Z',
    })),
    memberApprove: vi.fn(async () => bob),
    memberRemove: vi.fn(async () => ({})),
    memberColor: vi.fn(async () => alice),
    memberRename: vi.fn(async (display_name: string) => ({ ...alice, display_name })),
    memberGit: vi.fn(async (name: string, email: string) => ({
      ...alice,
      git_name: name,
      git_email: email,
    })),
    memberRole: vi.fn(async () => bob),
    memberDeviceList: vi.fn(async () => []),
    memberDeviceLookup: vi.fn(async () => needsOverride('memberDeviceLookup')),
    memberDeviceApprove: vi.fn(async () => needsOverride('memberDeviceApprove')),
    memberDeviceRevoke: vi.fn(async () => needsOverride('memberDeviceRevoke')),
    memberIdentityList: vi.fn(async () => ({ identities: [], devices: [] })),
    memberIdentityLink: vi.fn(async () => needsOverride('memberIdentityLink')),
    memberMergeStart: vi.fn(async () => needsOverride('memberMergeStart')),
    memberMergePreview: vi.fn(async () => needsOverride('memberMergePreview')),
    memberMergePropose: vi.fn(async () => needsOverride('memberMergePropose')),
    memberMergePending: vi.fn(async () => []),
    memberMergeAccept: vi.fn(async () => needsOverride('memberMergeAccept')),
    memberMergeCancel: vi.fn(async () => ({ cancelled: 0 })),
    memberMergeList: vi.fn(async () => []),
    memberInvitationList: vi.fn(async () => []),
    memberInvitationCreate: vi.fn(async () => needsOverride('memberInvitationCreate')),
    memberInvitationRevoke: vi.fn(async () => needsOverride('memberInvitationRevoke')),
    workspaceAdd: vi.fn(async () => workspace),
    workspaceListFull: vi.fn(async () => [workspace, otherWorkspace]),
    workspaceDelete: vi.fn(async () => ({ ok: true as const })),
    workspaceSettings: vi.fn(async () => workspace),
    workspaceEnvironment: vi.fn(async () => ({ workspace_id: workspace.id, setup_script: '', variables: [], egress: false, egress_hosts: [] })),
    workspaceEnvironmentSet: vi.fn(async () => needsOverride('workspaceEnvironmentSet')),
    workspaceMirrorStatus: vi.fn(async () => ({ enabled: false })),
    workspaceMirrorConfigure: vi.fn(async () => ({ enabled: false })),
    workspaceMirrorRefresh: vi.fn(async () => ({ enabled: false })),
    workspaceMirrorAdopt: vi.fn(async () => ({ enabled: false })),
    workspaceMirrorDisable: vi.fn(async () => ({ enabled: false })),
    budgetSet: vi.fn(async () => budget(workspace.id)),
    templateSave: vi.fn(async () => template),
    templateDelete: vi.fn(async () => ({})),
    scheduleList: vi.fn(async () => [schedule()]),
    scheduleSave: vi.fn(async () => schedule()),
    scheduleDelete: vi.fn(async () => ({})),
    homeTree: vi.fn(async () => ({ entries: [] })),
    homeRead: vi.fn(async () => ({
      content: '', size: 0, binary: false, truncated: false, revision: '', writable: true,
    })),
    homeWrite: vi.fn(async ({ content }) => ({
      content, size: new TextEncoder().encode(content).length,
      binary: false, truncated: false, revision: 'saved', writable: true,
    })),
    homeMkdir: vi.fn(async () => ({})),
    homeRename: vi.fn(async () => ({})),
    homeRemove: vi.fn(async () => ({})),
    homeUpload: vi.fn(async () => ({})),
    homeDownload: vi.fn(async () => ({ content_base64: '', size: 0 })),
    agentList: vi.fn(async () => [
      agentInfo({ display_name: 'Claude Code' }),
      agentInfo({ name: 'myagent', source: 'member', install_script: undefined }),
    ]),
    agentModels: vi.fn(async () => ({ models: [], source: 'none' as const })),
    agentRegister: vi.fn(async () => ({})),
    agentInstall: vi.fn(async () => ({ log_tail: 'installed\n', installed: true, enhanced_installed: false })),
    agentRemove: vi.fn(async () => ({})),
    runProtect: vi.fn(async () => ({})),
    runArchive: vi.fn(async (runID: string, archived: boolean) =>
      run({
        id: runID,
        archived_at: archived ? '2026-08-14T10:00:00Z' : undefined,
      }),
    ),
    runRelaunch: vi.fn(async () => run({ id: 'run_2' })),
    runSeen: vi.fn(async (runID: string) => run({ id: runID, status: 'completed' })),
    pushStatus: vi.fn(async () => ({ public_key: 'BA', subscribed: false })),
    pushSubscribe: vi.fn(async () => ({})),
    pushUnsubscribe: vi.fn(async () => ({})),
    pushTest: vi.fn(async () => ({})),
    pushActive: vi.fn(async () => ({})),
    slackStatus: vi.fn(async () => ({ configured: false, connected: false })),
    slackLink: vi.fn(async () => ({ code: 'K7Q2-M9XD', expires_at: '2026-10-25T12:10:00Z' })),
    slackUnlink: vi.fn(async () => ({ link: { user_id: 'U123', team_id: 'T123', linked_at: '2026-10-25T12:00:00Z' } })),
    localWorkspaceSelection: vi.fn(async () => ({ workspace_id: '' })),
    localLinkStatus: vi.fn(async () => ({
      server_configured: true,
      linked: true,
      addr: 'host:2222',
      user: 'alice',
      repo: '/src/repo',
    })),
    localGitIdentity: vi.fn(async () => ({
      name: 'Alice Local',
      email: 'alice@example.invalid',
    })),
    localLinkApply: vi.fn(async () => ({
      addr: 'host:2222',
      user: 'aether',
      member: { id: alice.id, display_name: alice.display_name, role: 'admin' },
    })),
    localLinkRepo: vi.fn(async () => ({
      repo: '/src/repo',
      remote: 'aether',
      url: 'ssh://alice@host:2222/wsp_1',
    })),
    localLinkSwitch: vi.fn(async (name: string) => ({ active: name })),
    localLinkReset: vi.fn(async () => ({ deleted: '/config/aether' })),
    localEdgeStatus: vi.fn(async () => ({ edges: [] })),
    localEdgeLogin: vi.fn(async () => needsOverride('localEdgeLogin')),
    localEdgeServers: vi.fn(async () => needsOverride('localEdgeServers')),
    localEdgeHostKey: vi.fn(async () => needsOverride('localEdgeHostKey')),
    localEdgeLink: vi.fn(async () => needsOverride('localEdgeLink')),
    localEdgeClaim: vi.fn(async () => needsOverride('localEdgeClaim')),
    localEdgeJoin: vi.fn(async () => needsOverride('localEdgeJoin')),
    localPull: vi.fn(async () => ({
      branch: 'aether/run-1-checkout',
      ref: 'refs/heads/aether/run-1-checkout',
      output: '',
      current: false,
      dirty: false,
    })),
    localPullSwitch: vi.fn(async (runID: string) => ({
      branch: runID,
    })),
    localRepoFastForward: vi.fn(async () => ({
      branch: 'main',
      commit: '1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b',
      current: true,
      dirty: false,
      output: 'From ssh://alice@host:2222/wsp_1\n * branch main -> FETCH_HEAD',
    })),
    localRepoPush: vi.fn(async () => ({
      branch: 'main',
      remote: 'aether',
      state: 'pushed' as const,
      local_commit: '9f1c2ab3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9',
      workspace_commit: '',
      ahead: 1,
      behind: 0,
      output:
        'To ssh://alice@host:2222/wsp_1\n * [new branch] main -> main',
    })),
    localSyncStart: vi.fn(async (runID: string) => ({
      run_id: runID,
      state: 'running',
    })),
    localSyncStop: vi.fn(async (runID: string) => ({
      run_id: runID,
      state: 'stopped',
    })),
    localSyncStatus: vi.fn(async () => ({ sessions: [] })),
    localForwardStart: vi.fn(async (target: string, port: number) => ({
      target,
      port,
      local_port: port,
      state: 'active' as const,
    })),
    localForwardStop: vi.fn(async (target: string, port: number) => ({
      target,
      port,
      state: 'stopped' as const,
    })),
    localForwardStatus: vi.fn(async () => ({ forwards: [] })),
    localDaemonInstall: vi.fn(async () => ({
      unit_path: '/home/alice/.config/systemd/user/aether-sync.service',
      note: 'enable with systemctl --user enable --now aether-sync',
    })),
    localDaemonStatus: vi.fn(async () => ({
      installed: false,
      unit_path: '',
    })),
    localUpdateCheck: vi.fn(async () => updateStatus()),
    localUpdateApply: vi.fn(async () => ({
      updated: ['/usr/local/bin/aether'],
      version: 'v1.3.0',
      restarting: true,
      rebuilding: false,
    })),
    localUpdateStatus: vi.fn(async () => ({ phase: 'idle' as const })),
    serverUpdateStatus: vi.fn(async () => serverUpdateStatus()),
    sysboxStatus: vi.fn(async () => ({ state: 'on' as const })),
    sysboxInstall: vi.fn(async () => ({ state: 'installing' as const })),
    serverUpdate: vi.fn(async () => ({
      status: 'scheduled' as const,
      version: 'v1.3.0',
      requested_by: alice.id,
      requested_at: '2026-08-14T10:06:00Z',
    })),
    envAgents: vi.fn(async () => ({
      agents: [
        { name: 'claude', installed: true },
        { name: 'codex', installed: false },
        { name: 'pi', installed: false },
      ],
      searched: ['/usr/local/bin', '/home/alice/.local/bin'],
      repo_path: '/src/repo',
    })),
    ...over,
  }
}

const none = { done: 0, failed: 0, abandoned: 0 }

/** insights.get for the same week with scope everyone: alice's runs from insights() plus bob's. */
export function teamInsights(): InsightsResult {
  const base = insights()
  const bobDay = (i: number) => ({
    start: base.outcomes.series[i].start,
    runs: i === 5 ? { done: 2, failed: 1, abandoned: 0 } : i === 6 ? { done: 1, failed: 0, abandoned: 0 } : none,
    swarms: i === 6 ? { done: 0, failed: 0, abandoned: 1 } : none,
  })
  const bobSeries = base.outcomes.series.map((_, i) => bobDay(i))
  const sum = (a: typeof none, b: typeof none) => ({ done: a.done + b.done, failed: a.failed + b.failed, abandoned: a.abandoned + b.abandoned })
  return insights({
    totals: { ...base.totals, launched: 15, completed: 9, merged: 1, failed: 2, interrupted: 1, finished: 10 },
    swarms: { ...base.swarms, launched: 2, completed: 1, cancelled: 1, agents: 8, tasks_accepted: 6 },
    success: { runs: { succeeded: 5, finished: 13 }, swarms: { succeeded: 1, finished: 2 } },
    outcomes: {
      runs: { done: 10, failed: 2, abandoned: 1 },
      swarms: { done: 1, failed: 0, abandoned: 1 },
      series: base.outcomes.series.map((day, i) => ({ start: day.start, runs: sum(day.runs, bobSeries[i].runs), swarms: sum(day.swarms, bobSeries[i].swarms) })),
      members: [
        { member_id: alice.id, display_name: alice.display_name, color: '#e6194b', runs: base.outcomes.runs, swarms: base.outcomes.swarms, series: base.outcomes.series },
        { member_id: 'mem_bob', display_name: 'Bob', color: '#3cb44b', runs: { done: 3, failed: 1, abandoned: 0 }, swarms: { done: 0, failed: 0, abandoned: 1 }, series: bobSeries },
      ],
    },
  })
}

export function systemContainer(over: Partial<SystemContainer> = {}): SystemContainer {
  return {
    owner_kind: 'run',
    owner_id: 'run_1',
    member_id: alice.id,
    workspace_id: workspace.id,
    title: 'rewrite the checkout flow',
    status: 'running',
    cpu_cores: 2.5,
    cpu_limit_cores: 4,
    memory_bytes: 4 * 1024 ** 3,
    memory_limit_bytes: 8 * 1024 ** 3,
    pids: 40,
    pids_limit: 4096,
    ...over,
  }
}

/** An 8-core, 16 GiB host with one busy run and seven idle ones. */
export function systemStats(over: Partial<SystemStatsResult> = {}): SystemStatsResult {
  const t0 = 1_761_100_000_000
  const sample = (i: number) => ({
    t_ms: t0 + i * 5000,
    cpu_user: 30,
    cpu_system: 10,
    cpu_iowait: 1,
    cpu_steal: 0,
    memory_used_bytes: 8 * 1024 ** 3,
    memory_available_bytes: 8 * 1024 ** 3,
    memory_free_bytes: 2 * 1024 ** 3,
    swap_used_bytes: 0,
    load1: 3.25,
    load5: 2.5,
    load15: 2,
    net_rx_bps: 120_000,
    net_tx_bps: 30_000,
    disk_read_bps: 0,
    disk_write_bps: 400_000,
    pressure_cpu: 2,
    pressure_memory: 0,
    pressure_io: 0.5,
  })
  return {
    interval_ms: 5000,
    retention_ms: 3_600_000,
    host: { cpu_cores: 8, memory_total_bytes: 16 * 1024 ** 3, swap_total_bytes: 2 * 1024 ** 3, uptime_seconds: 273_600 },
    samples: [sample(0), sample(1), sample(2)],
    containers_at_ms: t0 + 10_000,
    containers: [
      systemContainer(),
      ...['a', 'b', 'c', 'd', 'e', 'f', 'g'].map((id) =>
        systemContainer({ owner_id: `run_${id}`, title: `idle run ${id}`, cpu_cores: 0.02, memory_bytes: 256 * 1024 ** 2 }),
      ),
      systemContainer({ owner_kind: 'member', owner_id: bob.id, member_id: bob.id, title: undefined, status: undefined, cpu_cores: 0.3, memory_bytes: 1024 ** 3 }),
    ],
    others: { containers: 3, cpu_cores: 0.8, memory_bytes: 1_500_000_000 },
    ...over,
  }
}
