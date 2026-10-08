import { awaitingReview } from '@/lib/needs-you'
import { groupOf, plainReason, presentRun, runLabel } from '@/lib/status'
import { isTerminal, toRecord } from '@/store/runs'
import { run, stateContext } from '@/test/fixtures'

describe('runLabel', () => {
  it('prefers a terminal title over the task', () => {
    expect(runLabel({ title: 'Fixing the login bug', task: 'fix login' })).toBe(
      'Fixing the login bug',
    )
  })

  it('falls back to the task and then a placeholder', () => {
    expect(runLabel({ title: '', task: '  fix login  ' })).toBe('fix login')
    expect(runLabel({ task: '   ' })).toBe('Untitled run')
  })

  it('bounds an untitled run to the first line of its prompt', () => {
    expect(
      runLabel({ task: '\n  Goal: fix login.\nRead docs/auth.md first.' }),
    ).toBe('Goal: fix login.')
    const words = Array.from({ length: 40 }, (_, i) => `word${i}`).join(' ')
    const label = runLabel({ task: words })
    expect(label.length).toBeLessThanOrEqual(121)
    expect(label.endsWith('\u2026')).toBe(true)
    const kept = label.slice(0, -1)
    expect(words.startsWith(kept)).toBe(true)
    expect(words[kept.length]).toBe(' ')
    expect(runLabel({ title: 'Terminal title', task: words })).toBe(
      'Terminal title',
    )
    const tabbed = runLabel({ task: `${'a'.repeat(70)}\t${'b'.repeat(100)}` })
    expect(tabbed).toBe(`${'a'.repeat(70)}\u2026`)
    const astral = runLabel({ task: `${'a'.repeat(119)}\u{1F600}${'b'.repeat(50)}` })
    expect(astral).toBe(`${'a'.repeat(119)}\u{1F600}\u2026`)
  })
})

describe('run presentation', () => {
  const ctx = stateContext()
  const shown = (over: Parameters<typeof run>[0]) => presentRun(toRecord(run(over)), ctx)

  it('maps every wire status to one of five states', () => {
    expect(shown({ status: 'queued' })).toEqual({ state: 'working', reason: 'Queued' })
    expect(shown({ status: 'provisioning' })).toEqual({ state: 'working', reason: 'Starting' })
    expect(shown({ status: 'running' })).toEqual({ state: 'working', reason: 'Agent working' })
    expect(shown({ status: 'completed' })).toEqual({ state: 'done', reason: 'Finished' })
    expect(shown({ status: 'merged' })).toEqual({ state: 'done', reason: 'Merged' })
    expect(shown({ status: 'abandoned' })).toEqual({ state: 'done', reason: 'Closed without merging' })
    expect(shown({ status: 'failed', reason: 'agent exited 1' })).toEqual({ state: 'failed', reason: 'Failed: Agent exited with code 1' })
    expect(shown({ status: 'interrupted' })).toEqual({ state: 'failed', reason: 'Interrupted' })
    expect(shown({ status: 'needs-attention' }).state).toBe('needs-you')
  })

  it('reads what the agent is doing from its activity', () => {
    const reading = { ...toRecord(run()), activity: { verb: 'Reading', target: 'src/auth.ts', at: '2026-08-14T10:19:00Z' } }
    expect(presentRun(reading, ctx).reason).toBe('Reading src/auth.ts')
  })

  it('names an integrator that has left agent messages unread for over two minutes', () => {
    const integrator = (over: Parameters<typeof run>[0]) =>
      presentRun(toRecord(run({ mission_id: 'mission_1', mission_role: 'integrator', ...over })), ctx)
    expect(integrator({ unacked_messages: 3, oldest_unacked_at: '2026-08-14T10:08:00Z' })).toEqual({
      state: 'working', reason: 'Integrator has not read 3 messages (12 min)', unread: 3,
    })
    expect(integrator({ unacked_messages: 1, oldest_unacked_at: '2026-08-14T10:19:00Z' }).reason).toBe('Agent working')
    expect(presentRun(toRecord(run({ unacked_messages: 3, oldest_unacked_at: '2026-08-14T10:08:00Z' })), ctx).reason).toBe('Agent working')
  })

  it('keeps overdue worker mail visible after reviewing an open integrator outcome', () => {
    const parked = toRecord(run({
      mode: 'acp', acp: true, mission_id: 'mission_1', mission_role: 'integrator',
      status: 'needs-attention', reason: 'agent reported failure', outcome_unseen: false,
      unacked_messages: 3, oldest_unacked_at: '2026-08-14T10:08:00Z',
    }))
    const warning = presentRun(parked, ctx)
    expect(warning).toMatchObject({ state: 'working', unread: 3 })
    expect(groupOf(warning.state)).toBe('working')
    expect(presentRun({ ...parked, unacked_messages: 0, oldest_unacked_at: undefined }, ctx).state).toBe('failed')
  })

  it('shows a paused live run as Paused, grouped under Working', () => {
    const paused = toRecord(run({ status: 'needs-attention', paused: true }))
    expect(presentRun(paused, ctx).state).toBe('paused')
    expect(groupOf('paused')).toBe('working')
  })

  it('groups the five states into three', () => {
    expect(groupOf('needs-you')).toBe('needs-you')
    expect(groupOf('working')).toBe('working')
    expect(groupOf('done')).toBe('finished')
    expect(groupOf('failed')).toBe('finished')
  })

  it.each(['tui', 'acp'] as const)('keeps %s reported work distinct from a finished runtime', (mode) => {
    for (const outcome of ['success', 'failure'] as const) {
      const parked = toRecord(run({
        mode, acp: mode === 'acp', status: 'needs-attention',
        reason: `agent reported ${outcome}`, outcome_unseen: true,
      }))
      const review = presentRun(parked, ctx)
      expect(review.state).toBe('needs-you')
      expect(review.needsYou?.id).toBe('unreviewed-finish')
      expect(review.reason).toContain(`Agent reported ${outcome}`)
      expect(review.reason).toContain('Run remains open for follow-up.')
      expect(isTerminal(parked.status)).toBe(false)
      expect(presentRun({ ...parked, outcome_unseen: false }, ctx)).toEqual({
        state: outcome === 'success' ? 'done' : 'failed',
        reason: `Agent reported ${outcome}. Run remains open for follow-up.`,
      })
      expect(presentRun({ ...parked, status: 'running', reason: 'agent resumed', outcome_unseen: false }, ctx))
        .toEqual({ state: 'working', reason: 'Agent working' })
    }
  })

  it('shows an interactive integrator report without marking its runtime finished', () => {
    expect(shown({
      mode: 'acp', acp: true, mission_role: 'integrator', status: 'needs-attention',
      reason: 'agent reported success',
    }).state).toBe('done')
    for (const over of [{ mode: 'headless' as const }, { mission_role: 'worker' as const }]) {
      expect(shown({ status: 'needs-attention', reason: 'agent reported success', ...over }).state).not.toBe('done')
    }
  })

  it('lists an unreviewed outcome only while it is an agent outcome', () => {
    expect(awaitingReview({ status: 'completed', outcome_unseen: true })).toBe(true)
    expect(awaitingReview({ status: 'failed', outcome_unseen: true })).toBe(true)
    expect(awaitingReview({ status: 'completed' })).toBe(false)
    // A later transition the flag outlived is not an agent outcome to review.
    expect(awaitingReview({ status: 'merged', outcome_unseen: true })).toBe(false)
    expect(awaitingReview({ status: 'running', outcome_unseen: true })).toBe(false)
  })
})

describe('plainReason', () => {
  it.each([
    ['agent reported success; retained container', 'Agent reported success'],
    ['closed; retained container', 'Closed'],
    ['worker finished; retained container', 'Worker finished'],
    ['agent exited; results committed', 'Agent exited and its changes were committed'],
    ['agent exited 1: relation "invoices" does not exist', 'Agent exited with code 1: relation "invoices" does not exist'],
    ['retained container expired', 'Its container was removed when it expired'],
    ['stalled: no output or file changes for 10m0s', 'No output or file changes for 10m0s'],
    ["the agent's turn ended: max_tokens", 'The agent hit its output limit'],
    ['review retained evidence', "Review the worker's saved results"],
    ['killed', 'Stopped'],
    ['disk quota exceeded', 'disk quota exceeded'],
  ])('reads %s', (reason, plain) => {
    expect(plainReason(reason)).toBe(plain)
  })
})
