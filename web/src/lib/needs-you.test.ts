import { needsYou, needsYouConditions, waitingOn, type NeedsYouID } from '@/lib/needs-you'
import { presentRun } from '@/lib/status'
import type { MissionAttempt } from '@/lib/types'
import type { MissionDetail } from '@/store/missions'
import { toRecord, type RunRecord } from '@/store/runs'
import {
  alice,
  approval,
  bob,
  mission,
  missionQuestion,
  roomMessage,
  run,
  runRecords,
  stateContext,
} from '@/test/fixtures'

const record = (over: Parameters<typeof run>[0] = {}): RunRecord => toRecord(run(over))

const integrator = record({ id: 'run_integrator', mission_id: 'mission_1', mission_role: 'integrator' })
const worker = record({
  id: 'run_worker',
  mission_id: 'mission_1',
  mission_role: 'worker',
  integrator_run_id: integrator.id,
  mode: 'headless',
})

function detail(over: Partial<MissionDetail> = {}): MissionDetail {
  return {
    mission: mission(),
    tasks: [],
    attempts: [],
    submissions: [],
    diagnostics: [],
    questions: [],
    ...over,
  }
}

function attempt(over: Partial<MissionAttempt> = {}): MissionAttempt {
  return {
    id: 'attempt_1',
    mission_id: 'mission_1',
    task_id: 'task_1',
    task_revision: 1,
    number: 3,
    dispatch_key: 'dispatch_1',
    harness: 'claude',
    mode: 'headless',
    state: 'running',
    run_id: worker.id,
    authority_generation: 1,
    integrator_generation: 1,
    created_at: '2026-08-14T10:00:00Z',
    reserved_at: '2026-08-14T10:00:00Z',
    ...over,
  } as MissionAttempt
}

const rows: {
  id: NeedsYouID
  run: RunRecord
  ctx: Parameters<typeof stateContext>[0]
  reason: string
}[] = [
  {
    id: 'permission',
    run: record(),
    ctx: { approvalsByRun: { run_1: [approval({ action: 'run `go test ./...`' })] } },
    reason: 'Permission: run `go test ./...`',
  },
  {
    id: 'question',
    run: record({ pending_inputs: [{ id: 'request_1', session_id: 's', kind: 'question' }] }),
    ctx: {},
    reason: 'Question: answer in the terminal',
  },
  {
    id: 'queued-message',
    run: record({ member_id: bob.id }),
    ctx: {
      roomStatus: {
        run_1: {
          workspace_id: 'wsp_1',
          run_id: 'run_1',
          protected: false,
          watchers: [],
          queued_steers: 1,
          controller: { member_id: alice.id, connected: true, acquired_at: '2026-08-14T10:00:00Z' },
        },
      },
      roomMessages: {
        run_1: [roomMessage({ actor_id: bob.id, actor_display_name: 'Bob', kind: 'steer_request', state: 'queued' })],
      },
    },
    reason: 'Bob sent a message, approve to deliver',
  },
  {
    id: 'room-question',
    run: record(),
    ctx: { roomMessages: { run_1: [roomMessage({ actor_id: bob.id, kind: 'question', body: 'which port?' })] } },
    reason: 'Bob asked you: which port?',
  },
  {
    id: 'swarm-question',
    run: integrator,
    ctx: {
      missions: { mission_1: mission() },
      missionDetails: { mission_1: detail({ questions: [missionQuestion()] }) },
    },
    reason: 'The integrator asks: which checkout flow?',
  },
  {
    id: 'integrator-down',
    run: record({ ...integrator, status: 'failed' }),
    ctx: { missions: { mission_1: mission() } },
    reason: 'Integrator stopped, replace it to continue',
  },
  {
    id: 'control-hold',
    run: worker,
    ctx: {
      runs: runRecords(integrator),
      missionDetails: { mission_1: detail({ attempts: [attempt({ takeover_active: true, takeover_member_id: alice.id })] }) },
    },
    reason: 'You hold control of worker 3',
  },
  {
    id: 'enhanced-failure',
    run: record({ status: 'needs-attention', reason: 'enhanced session failed: acphost: initialize: EOF' }),
    ctx: {},
    reason: 'Enhanced unavailable: acphost: initialize: EOF',
  },
  {
    id: 'blocked',
    run: record({ ...worker, status: 'needs-attention', reason: 'blocked: no database access' }),
    ctx: { runs: runRecords(integrator), missions: { mission_1: mission() } },
    reason: 'Worker blocked: no database access',
  },
  {
    id: 'stopped',
    run: { ...record({ status: 'needs-attention', reason: 'stalled: no output or file changes for 10m0s' }), stateChangedAt: '2026-08-14T10:08:00Z', stateChangedAtEstimated: false },
    ctx: {},
    reason: 'No activity for 12 min',
  },
  {
    id: 'unreviewed-finish',
    run: record({ status: 'completed', outcome_unseen: true, finished_at: '2026-08-14T10:10:00Z' }),
    ctx: {},
    reason: 'Finished, review the result',
  },
]

describe('needs you conditions', () => {
  it('has one test row per condition', () => {
    expect(rows.map((row) => row.id).sort()).toEqual(needsYouConditions.map((c) => c.id).sort())
  })

  it.each(rows)('$id needs the member who can resolve it', ({ id, run: r, ctx, reason }) => {
    const context = stateContext(ctx)
    const hit = needsYou(r, context)
    expect(hit?.id).toBe(id)
    expect(hit?.reason(r, context)).toBe(reason)
    expect(presentRun(r, context)).toMatchObject({ state: 'needs-you', reason })
  })

  it('needs nobody for a plain working run', () => {
    expect(needsYou(record(), stateContext())).toBeUndefined()
    expect(presentRun(record({ status: 'queued' }), stateContext()).state).toBe('working')
  })

  it("needs the owner for a standalone run's blocked report", () => {
    const blocked = record({ status: 'needs-attention', reason: 'blocked: need database credentials' })
    expect(presentRun(blocked, stateContext())).toMatchObject({
      state: 'needs-you',
      reason: 'Blocked: need database credentials',
    })
  })

  it('says the agent idled when the turn ended without a stall', () => {
    const idle = {
      ...record({ status: 'needs-attention', reason: 'agent idle' }),
      stateChangedAt: '2026-08-14T10:17:00Z',
      stateChangedAtEstimated: false,
    }
    expect(presentRun(idle, stateContext())).toEqual(
      expect.objectContaining({ state: 'needs-you', reason: 'Agent idle for 3 min' }),
    )
  })

  it.each([
    ["enhanced session ended: the agent's ACP server exited with code 1", "the agent's ACP server exited with code 1"],
    [
      'enhanced turn failed: acphost: session/prompt: {"code":-32000,"message":"Authentication required"}',
      'acphost: session/prompt: {"code":-32000,"message":"Authentication required"}',
    ],
  ])("surfaces the server's reason %s as Enhanced unavailable", (reason, error) => {
    const parked = record({ status: 'needs-attention', reason })
    expect(presentRun(parked, stateContext())).toMatchObject({
      state: 'needs-you',
      reason: `Enhanced unavailable: ${error}`,
    })
  })

  it.each([
    ['permission', 'Permission requested'],
    ['question', 'Question from the agent'],
  ] as const)('gives no terminal instruction for an enhanced run\'s %s', (kind, reason) => {
    const enhanced = record({ mode: 'acp', pending_inputs: [{ id: 'r1', session_id: 's1', kind }] })
    expect(presentRun(enhanced, stateContext())).toMatchObject({ state: 'needs-you', reason })
  })

  it('names no wait for a run parked before the snapshot', () => {
    const reloaded = record({ status: 'needs-attention', reason: 'agent idle', started_at: '2026-08-14T08:00:00Z' })
    expect(presentRun(reloaded, stateContext()).reason).toBe('Agent idle')
  })
})

describe('viewer scoping', () => {
  const bobViewing = { viewerID: bob.id, viewerRole: bob.role }

  it("shows another member's waiting run as Working, waiting for its owner", () => {
    const stopped = record({ status: 'needs-attention', reason: 'agent idle' })
    const ctx = stateContext(bobViewing)
    expect(needsYou(stopped, ctx)).toBeUndefined()
    expect(waitingOn(stopped, ctx)).toBe(alice.id)
    expect(presentRun(stopped, ctx)).toEqual({ state: 'working', reason: 'Waiting for Alice' })
  })

  it("leaves another member's unreviewed finish Done", () => {
    const finished = record({ status: 'completed', outcome_unseen: true })
    expect(presentRun(finished, stateContext(bobViewing))).toEqual({ state: 'done', reason: 'Finished' })
  })

  it('needs the terminal controller for a permission on a run they hold', () => {
    const ctx = stateContext({
      ...bobViewing,
      approvalsByRun: { run_1: [approval()] },
      roomStatus: {
        run_1: {
          workspace_id: 'wsp_1',
          run_id: 'run_1',
          protected: false,
          watchers: [],
          queued_steers: 0,
          controller: { member_id: bob.id, connected: true, acquired_at: '2026-08-14T10:00:00Z' },
        },
      },
    })
    expect(needsYou(record(), ctx)?.id).toBe('permission')
  })

  it("shows a controller the requests on another member's run before the room loads", () => {
    const held = (pending: 'permission' | 'question', controller: string) =>
      record({
        mode: 'acp',
        controller_member_id: controller,
        pending_inputs: [{ id: 'req_1', session_id: 'sess_1', kind: pending }],
      })
    const ctx = stateContext(bobViewing)
    expect(needsYou(held('permission', bob.id), ctx)?.id).toBe('permission')
    expect(needsYou(held('question', bob.id), ctx)?.id).toBe('question')

    // The snapshot names the current holder over a room status read earlier.
    const stale = stateContext({
      ...bobViewing,
      roomStatus: {
        run_1: {
          workspace_id: 'wsp_1',
          run_id: 'run_1',
          protected: false,
          watchers: [],
          queued_steers: 0,
          controller: { member_id: bob.id, connected: true, acquired_at: '2026-08-14T10:00:00Z' },
        },
      },
    })
    expect(needsYou(held('permission', ''), stale)).toBeUndefined()
  })

  it("does not ask the owner to answer their own room question", () => {
    const ctx = stateContext({ roomMessages: { run_1: [roomMessage({ kind: 'question' })] } })
    expect(needsYou(record(), ctx)).toBeUndefined()
  })

  // The board card reads the server's count before the room loads; the run
  // header reads the loaded room. The server counts only questions to the owner.
  it.each([
    { asker: 'the owner', actor: alice.id, count: 0, want: 'stopped' },
    { asker: 'a teammate', actor: bob.id, count: 1, want: 'room-question' },
  ])('agrees between card and header on a question from $asker', ({ actor, count, want }) => {
    const parked = { status: 'needs-attention' as const, reason: 'stalled: no output or file changes for 10m4s' }
    const card = needsYou(record({ ...parked, unanswered_questions: count }), stateContext())
    const header = needsYou(
      record({ ...parked, unanswered_questions: count }),
      stateContext({ roomMessages: { run_1: [roomMessage({ kind: 'question', actor_id: actor })] } }),
    )
    expect(card?.id).toBe(want)
    expect(header?.id).toBe(want)
  })

  it('lets an admin resolve a swarm question for another accountable human', () => {
    const ctx = {
      missions: { mission_1: mission({ accountable_human_id: bob.id, open_questions: 1 }) },
    }
    const theirs = record({ ...integrator, member_id: bob.id })
    expect(needsYou(theirs, stateContext(ctx))?.id).toBe('swarm-question')
    expect(needsYou(theirs, stateContext({ ...ctx, viewerID: 'mem_carol', viewerRole: 'collaborator' }))).toBeUndefined()
  })

  it('needs nobody before the viewer is known', () => {
    const stopped = record({ status: 'needs-attention' })
    expect(needsYou(stopped, stateContext({ viewerID: null, viewerRole: null }))).toBeUndefined()
    expect(presentRun(stopped, stateContext({ viewerID: null, viewerRole: null })).reason).not.toMatch(/^Waiting for/)
  })
})

describe('swarm workers', () => {
  const stoppedWorker = record({ ...worker, mode: 'tui', status: 'needs-attention', reason: 'agent idle' })

  it('leaves a worker to its live integrator', () => {
    const ctx = stateContext({ runs: runRecords(integrator) })
    expect(needsYou(stoppedWorker, ctx)).toBeUndefined()
    expect(presentRun(stoppedWorker, ctx)).toEqual({ state: 'working', reason: 'Waiting for the integrator' })
  })

  it('counts a worker once its integrator is gone', () => {
    const gone = stateContext({ runs: runRecords({ ...integrator, status: 'failed' }) })
    expect(needsYou(stoppedWorker, gone)?.id).toBe('stopped')
  })

  it('counts a worker while a human holds it', () => {
    const held = stateContext({
      runs: runRecords(integrator),
      missionDetails: { mission_1: detail({ attempts: [attempt({ takeover_active: true, takeover_member_id: bob.id })] }) },
    })
    expect(needsYou(stoppedWorker, held)?.id).toBe('stopped')
    expect(needsYou(stoppedWorker, { ...held, viewerID: bob.id, viewerRole: bob.role })?.id).toBe('control-hold')
  })
})

describe('stopped integrators', () => {
  const failed = record({ ...integrator, status: 'failed' })

  it('counts nothing until the swarm record loads', () => {
    expect(needsYou(failed, stateContext())).toBeUndefined()
  })

  it('counts nothing for a replaced integrator', () => {
    const replaced = stateContext({ missions: { mission_1: mission({ current_integrator_run_id: 'run_next' }) } })
    expect(needsYou(failed, replaced)).toBeUndefined()
  })
})

describe('background runs', () => {
  it('reach Needs you on a pending Aether approval', () => {
    const ctx = stateContext({ approvalsByRun: { run_1: [approval()] } })
    expect(needsYou(record({ mode: 'headless' }), ctx)?.id).toBe('permission')
  })

  it('reach Needs you on an unreviewed failure', () => {
    const finished = record({ mode: 'headless', status: 'failed', outcome_unseen: true })
    expect(presentRun(finished, stateContext())).toMatchObject({ state: 'needs-you', reason: 'Failed, review the result' })
  })
})
