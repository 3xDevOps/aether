import { archiveState, idleArchivePlan, idleRows } from '@/lib/idle-archive'
import type { Run, RunCloseCheck } from '@/lib/types'
import { toRecord, type RunRecord } from '@/store/runs'
import { alice, run, stateContext, workspace } from '@/test/fixtures'

const now = Date.parse('2026-08-14T10:20:00Z')
const ago = (ms: number) => new Date(now - ms).toISOString()
const day = 24 * 60 * 60 * 1000

const live = (id: string, over: Partial<Run>): RunRecord => toRecord(run({ id, task: id, status: 'running', member_id: alice.id, ...over }))

const cap = { hasMethod: () => true, hasLocal: () => false, hasWS: () => true }
const self = { id: alice.id, role: alice.role }

function plan(runs: RunRecord[], over: Parameters<typeof stateContext>[0] = {}) {
  const ctx = stateContext({ now, runs: Object.fromEntries(runs.map((r) => [r.id, r])), ...over })
  return idleArchivePlan(runs.map((r) => ({ run: r, workspace })), ctx, cap, self)
}

const reasons = (list: { run: RunRecord; reason: string }[]) => Object.fromEntries(list.map(({ run: r, reason }) => [r.id, reason]))

describe('idle archive plan', () => {
  it('takes live sessions that replied or ended their turn without a report, noting the session as it saw it', () => {
    const replied = live('replied', { state: 'answered' })
    const got = plan([replied, live('ended', { state: 'waiting', cause: 'turn-end' })])
    expect(reasons(got.done)).toEqual({ replied: 'replied', ended: 'turn ended without a report' })
    expect(got.done[0]!.asked).toBe(archiveState(replied, undefined))
    expect(got.excluded).toEqual([])
  })

  it('keeps out what still works, waits, needs you, is paused or belongs to an open parent, saying why', () => {
    const parent = live('parent', { state: 'working' })
    const got = plan([
      live('ci', { state: 'waiting-ci' }),
      live('agents', { state: 'waiting-agents' }),
      live('reported-wait', { state: 'waiting', cause: 'report', wait: { kind: 'condition' } }),
      live('held', { state: 'answered', cause: 'tasks-held' }),
      live('asks', { status: 'needs-attention', state: 'needs-you', reason: 'needs input: which suite?' }),
      live('paused', { state: 'answered' }),
      live('asked-owner', { state: 'answered', member_id: 'mem_bob', unanswered_questions: 1 }),
      parent,
      live('child', { state: 'answered', parent_run_id: 'parent' }),
      live('detached', { state: 'answered', parent_run_id: 'parent', detached: true }),
      live('steered', { state: 'answered' }),
      { ...live('done-parent', { status: 'completed', review: 'ready' }) },
      live('waiting-child', { state: 'answered', parent_run_id: 'done-parent' }),
    ], { pausedRuns: { paused: true }, roomStatus: { steered: { queued_steers: 1 } as never } })
    expect(reasons(got.excluded)).toMatchObject({
      parent: 'subsessions still running',
      ci: 'waiting on CI',
      agents: 'waiting on agents',
      'reported-wait': 'waiting on a reported condition',
      held: 'background tasks still running',
      asks: 'needs you',
      paused: 'paused',
      'asked-owner': 'a question is waiting for its owner',
      child: 'its parent session is still open',
      steered: 'a queued message waits to reach its agent',
      'waiting-child': 'its parent session is still open',
    })
    expect(reasons(got.done)).toEqual({ detached: 'replied' })
  })

  it('ignores sessions that are already finished', () => {
    const got = plan([live('closed', { status: 'merged' })])
    expect(got.done).toEqual([])
    expect(got.excluded).toEqual([])
  })
})

describe('idle rows', () => {
  const done = [
    { run: live('old', { state: 'answered' }), reason: 'replied', asked: 'a' },
    { run: live('new', { state: 'answered' }), reason: 'replied', asked: 'b' },
    { run: live('pr', { state: 'answered' }), reason: 'replied', asked: 'c' },
    { run: live('legacy', { state: 'answered' }), reason: 'replied', asked: 'd' },
  ]
  const checks = new Map<string, RunCloseCheck>([
    ['old', { run_id: 'old', warnings: [], merged_pull_request: true, idle_since: ago(3 * day) }],
    ['new', { run_id: 'new', warnings: [], merged_pull_request: false, idle_since: ago(2 * 60 * 60 * 1000) }],
    ['pr', { run_id: 'pr', warnings: [{ kind: 'pull_request', detail: 'Pull request acme/shop#4 was open' }], merged_pull_request: false, idle_since: ago(2 * day) }],
    ['legacy', { run_id: 'legacy', warnings: [], idle_since: ago(5 * day) }],
  ])

  it('measures idleness by when the server last saw the session change, and starts only clean, known ones checked', () => {
    const rows = idleRows(done, checks, now)
    expect(reasons(rows.recent)).toEqual({ new: 'active 2h ago' })
    expect(rows.idle.map((row) => [row.run.id, row.reason, row.outcome, row.clean, row.outcomeUnknown])).toEqual([
      ['old', 'idle 3d, replied', 'merged', true, false],
      ['pr', 'idle 2d, replied', 'abandoned', false, false],
      ['legacy', 'idle 5d, replied', 'abandoned', false, true],
    ])
  })

  it('keeps a recently active session out even when its check failed', () => {
    const recent = { run: { ...live('recent-failed', { state: 'answered' }), stateChangedAt: ago(60 * 60 * 1000) }, reason: 'replied', asked: 'e' }
    const rows = idleRows([recent], new Map([['recent-failed', { run_id: 'recent-failed', warnings: [], error: { code: 0, message: 'timed out' } }]]), now)
    expect(rows.idle).toEqual([])
    expect(reasons(rows.recent)).toEqual({ 'recent-failed': 'active 1h ago' })
  })

  it('keeps a session out when an older server cannot say how long it has been idle', () => {
    const old = { run: live('no-idle-since', { state: 'answered' }), reason: 'replied', asked: 'f' }
    const rows = idleRows([old], new Map([['no-idle-since', { run_id: 'no-idle-since', warnings: [], merged_pull_request: false }]]), now)
    expect(rows.idle).toEqual([])
    expect(reasons(rows.recent)).toEqual({ 'no-idle-since': 'this server cannot tell how long it has been idle' })
  })
})
