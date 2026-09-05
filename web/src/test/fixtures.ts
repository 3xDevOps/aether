import type {
  Api,
  EnvScanSession,
  ProfileScanHandlers,
  ProfileScanRequest,
} from '@/lib/api'
import type {
  AgentInfo,
  Approval,
  BudgetReport,
  Member,
  ProfilePreview,
  Run,
  Schedule,
  ServerInfo,
  ServerUpdateStatus,
  Template,
  UpdateStatus,
  Workspace,
} from '@/lib/types'

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

export const otherWorkspace: Workspace = {
  id: 'wsp_2',
  name: 'docs-site',
  base_branch: 'main',
  created_at: '2026-08-14T07:00:00Z',
}

export function run(over: Partial<Run> = {}): Run {
  return {
    id: 'run_1',
    workspace_id: workspace.id,
    member_id: alice.id,
    task: 'rewrite the checkout flow',
    harness: 'claude',
    mode: 'tui',
    status: 'running',
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

export function approval(over: Partial<Approval> = {}): Approval {
  return {
    id: 'apr_1',
    workspace_id: workspace.id,
    run_id: 'run_1',
    action: 'write src/checkout.ts',
    detail: 'the agent wants to edit a file outside its allowlist',
    decision: 'requested',
    created_at: '2026-08-14T10:03:00Z',
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
    install_script: 'curl -fsSL https://claude.ai/install.sh | bash',
    ...over,
  }
}

/** What profile.preview answers for the harness this machine configured.
 * Other setup-capable harnesses answer present:false, which is a normal
 * answer rather than an error. */
export function profilePreview(
  over: Partial<ProfilePreview> = {},
): ProfilePreview {
  return {
    harness: 'claude',
    root: '/home/alice/.claude',
    present: true,
    files: 42,
    bytes: 183422,
    categories: [
      {
        category: 'skills',
        files: 12,
        bytes: 40201,
        paths: ['skills/pdf/SKILL.md'],
        truncated: false,
      },
      {
        category: 'commands',
        files: 4,
        bytes: 8120,
        paths: ['commands/review.md'],
        truncated: false,
      },
    ],
    excluded: [
      {
        path: '.credentials.json',
        reason: 'credential',
        detail: 'credential file excluded for claude',
      },
    ],
    blocked: false,
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
    ...over,
  }
}

/** One server.update_status answer: a current server that could replace
 * its own binaries if it had to. The banner tests override it. */
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

/** An Api stub; every method is a spy so tests can assert on calls. */
export function fakeApi(over: Partial<Api> = {}): Api {
  return {
    serverInfo: vi.fn(async () => serverInfo),
    workspaceGet: vi.fn(async () => workspace),
    memberList: vi.fn(async () => [alice, bob]),
    runList: vi.fn(async () => [run()]),
    runGet: vi.fn(async () => run()),
    runLaunch: vi.fn(async () => run()),
    runKill: vi.fn(async () => ({})),
    runDelete: vi.fn(async () => ({})),
    runPause: vi.fn(async () => ({})),
    runResume: vi.fn(async () => ({})),
    runInject: vi.fn(async () => ({})),
    runClose: vi.fn(async () => run({ status: 'merged' })),
    runHandoff: vi.fn(async () => ({})),
    approvalList: vi.fn(async () => []),
    approvalDecide: vi.fn(async () => approval()),
    presenceRoster: vi.fn(async () => []),
    presenceHeartbeat: vi.fn(async () => 90),
    budgetGet: vi.fn(async (workspaceID: string) => budget(workspaceID)),
    templateList: vi.fn(async () => [template]),
    templateLaunch: vi.fn(async () => ({
      run: run({ id: 'run_tpl' }),
      base_branch: 'main',
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
    filesRead: vi.fn(async () => ({ content: '', truncated: false, binary: false, size: 0 })),
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
    attachShellSocket: vi.fn(
      (runID: string, tab: string) =>
        `ws://localhost/ws/attach/${runID}?shell=${encodeURIComponent(tab)}`,
    ),
    terminalStatus: vi.fn(async () => ({ running: false, tabs: [] })),
    terminalStop: vi.fn(async () => ({})),
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
    memberRole: vi.fn(async () => bob),
    workspaceAdd: vi.fn(async () => workspace),
    workspaceListFull: vi.fn(async () => [workspace, otherWorkspace]),
    workspaceSettings: vi.fn(async () => workspace),
    budgetSet: vi.fn(async () => budget(workspace.id)),
    templateSave: vi.fn(async () => template),
    templateDelete: vi.fn(async () => ({})),
    scheduleList: vi.fn(async () => [schedule()]),
    scheduleSave: vi.fn(async () => schedule()),
    scheduleDelete: vi.fn(async () => ({})),
    profileStatus: vi.fn(async () => ({
      snapshot: {
        id: 'psn_1',
        harness: 'claude',
        digest: 'sha256:beef5678',
        created_at: '2026-08-14T08:00:00Z',
      },
      snapshots: [
        {
          id: 'psn_1',
          harness: 'claude',
          digest: 'sha256:beef5678',
          created_at: '2026-08-14T08:00:00Z',
        },
      ],
    })),
    profileRollback: vi.fn(async () => ({})),
    localProfilePreview: vi.fn(async (harness: string) =>
      harness === 'claude'
        ? profilePreview()
        : profilePreview({
            harness,
            root: `/home/alice/.${harness}`,
            present: false,
            files: 0,
            bytes: 0,
            categories: [],
            excluded: [],
          }),
    ),
    localProfilePush: vi.fn(async (harness: string) => ({
      harness,
      snapshot_id: 'psn_2',
      digest: 'sha256:cafe9012',
      files: 42,
      bytes: 183422,
    })),
    agentList: vi.fn(async () => [
      agentInfo(),
      agentInfo({ name: 'myagent', source: 'member', install_script: undefined }),
    ]),
    agentRegister: vi.fn(async () => ({})),
    runProtect: vi.fn(async () => ({})),
    runRelaunch: vi.fn(async () => run({ id: 'run_2' })),
    localLinkStatus: vi.fn(async () => ({
      server_configured: true,
      linked: true,
      addr: 'host:2222',
      user: 'alice',
      repo: '/src/repo',
    })),
    localLinkRepo: vi.fn(async () => ({
      repo: '/src/repo',
      remote: 'aether',
      url: 'ssh://alice@host:2222/wsp_1',
    })),
    // Mirrors the gateway: link.switch always refuses with the restart
    // instruction; the SSH identity is process-lifetime.
    localLinkSwitch: vi.fn(async (name: string) => {
      throw new Error(`restart aether gui --server ${name} to switch servers`)
    }),
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
    localRepoPush: vi.fn(async () => ({
      branch: 'main',
      remote: 'aether',
      output:
        'To ssh://alice@host:2222/wsp_1\n * [new branch] main -> main',
    })),
    localRepoSync: vi.fn(async () => ({
      branch: 'main',
      output: 'From origin\nAlready up to date.',
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
    localForwardStart: vi.fn(async (runID: string, port: number) => ({
      run_id: runID,
      port,
      local_port: port,
      state: 'active' as const,
    })),
    localForwardStop: vi.fn(async (runID: string, port: number) => ({
      run_id: runID,
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
    localImageScaffold: vi.fn(async () => ({ written: ['Dockerfile'] })),
    localUpdateCheck: vi.fn(async () => updateStatus()),
    localUpdateApply: vi.fn(async () => ({
      updated: ['/usr/local/bin/aether'],
      version: 'v1.3.0',
      restarting: true,
      rebuilding: false,
    })),
    localUpdateStatus: vi.fn(async () => ({ phase: 'idle' as const })),
    serverUpdateStatus: vi.fn(async () => serverUpdateStatus()),
    serverUpdate: vi.fn(async () => ({
      status: 'scheduled' as const,
      version: 'v1.3.0',
      requested_by: alice.id,
      requested_at: '2026-08-14T10:06:00Z',
    })),
    // The gateway knows one linked repo, so the verb suggests its folder
    // for the wizard's from-repo input.
    envHarnesses: vi.fn(async () => ({
      harnesses: [
        { name: 'claude', installed: true },
        { name: 'codex', installed: false },
        { name: 'pi', installed: false },
        { name: 'amp', installed: false },
      ],
      searched: ['/usr/local/bin', '/home/alice/.local/bin'],
      repo_path: '/src/repo',
    })),
    // A profile scan that recommends the configured harness, like the
    // gateway's fake harness; tests drive other outcomes by overriding.
    openProfileScan: vi.fn(
      (_req: ProfileScanRequest, h: ProfileScanHandlers): EnvScanSession => {
        let closed = false
        queueMicrotask(() => {
          if (closed) return
          h.onStatus('running')
          h.onOutput('fake harness: reading the profile inventory')
          h.onResult({
            harnesses: [
              {
                harness: 'claude',
                import: true,
                categories: ['skills', 'commands'],
                reason: 'your skills and commands match this project',
              },
            ],
          })
        })
        return {
          close: () => {
            closed = true
          },
        }
      },
    ),
    ...over,
  }
}
