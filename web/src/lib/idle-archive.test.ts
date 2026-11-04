import { idleArchivePlan } from '@/lib/idle-archive'
import type { Run } from '@/lib/types'
import { toRecord, type RunRecord } from '@/store/runs'
import { alice, run, stateContext, workspace } from '@/test/fixtures'

const now = Date.parse('2026-08-14T10:20:00Z')
const daysAgo = (days: number) => new Date(now - days * 24 * 60 * 60 * 1000).toISOString()

function live(id: string, over: Partial<Run>, changed = daysAgo(3)): RunRecord {
  return { ...toRecord(run({ id, task: id, status: 'running', member_id: alice.id, ...over })), stateChangedAt: changed }
}

const cap = { hasMethod: () => true, hasLocal: () => false, hasWS: () => true }
const self = { id: alice.id, role: alice.role }

function plan(runs: RunRecord[], over: Parameters<typeof stateContext>[0] = {}) {
  const ctx = stateContext({ now, runs: Object.fromEntries(runs.map((r) => [r.id, r])), ...over })
  const result = idleArchivePlan(runs.map((r) => ({ run: r, workspace })), ctx, cap, self)
  return {
    idle: Object.fromEntries(result.idle.map(({ run: r, reason }) => [r.id, reason])),
    excluded: Object.fromEntries(result.excluded.map(({ run: r, reason }) => [r.id, reason])),
  }
}

describe('idle archive plan', () => {
  it('offers live sessions that replied or ended their turn more than a day ago, saying why', () => {
    const got = plan([
      live('replied', { state: 'answered' }),
      live('ended', { state: 'waiting', cause: 'turn-end' }),
    ])
    expect(got.idle).toEqual({ replied: 'Idle 3d 0h, replied', ended: 'Idle 3d 0h, turn ended without a report' })
    expect(got.excluded).toEqual({})
  })

  it('keeps out what still works, waits, needs you or was active within the day, saying why', () => {
    const got = plan([
      live('working', { state: 'working' }),
      live('ci', { state: 'waiting-ci' }),
      live('agents', { state: 'waiting-agents' }),
      live('reported-wait', { state: 'waiting', cause: 'report', wait: { kind: 'condition' } }),
      live('held', { state: 'answered', cause: 'tasks-held' }),
      live('recent', { state: 'answered' }, new Date(now - 2 * 60 * 60 * 1000).toISOString()),
      live('asks', { status: 'needs-attention', state: 'needs-you', reason: 'needs input: which suite?' }),
      live('review', { state: 'finished', review: 'ready', reason: 'agent reported success' }),
      live('paused', { state: 'answered' }),
    ], { pausedRuns: { paused: true } })
    expect(got.idle).toEqual({})
    expect(got.excluded).toMatchObject({
      working: 'Excluded: working',
      ci: 'Excluded: waiting on CI',
      agents: 'Excluded: waiting on agents',
      'reported-wait': 'Excluded: waiting on a reported condition',
      held: 'Excluded: background tasks still running',
      recent: 'Excluded: active 2h 00m ago',
      asks: 'Excluded: needs you',
      paused: 'Excluded: paused',
    })
    expect(got.excluded.review).toMatch(/^Excluded: /)
  })

  it('keeps out a parent with live subsessions and ignores finished sessions', () => {
    const parent = live('parent', { state: 'answered' })
    const child = live('child', { state: 'working', parent_run_id: 'parent' })
    const closed = live('closed', { status: 'merged', state: '' as never })
    const got = plan([parent, child, closed])
    expect(got.excluded.parent).toBe('Excluded: subsessions still running')
    expect(got.idle.closed).toBeUndefined()
    expect(got.excluded.closed).toBeUndefined()
  })
})
