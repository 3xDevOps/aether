import { awaitingReview } from '@/lib/needs-you'
import { plainReason } from '@/lib/reasons'
import { backgroundReason, clipLabel, groupOf, presentRun, runLabel, stateLabel } from '@/lib/status'
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

  it('holds any label to the same length, and leaves one it already held alone', () => {
    const exact = 'a'.repeat(120)
    expect(clipLabel(exact)).toBe(exact)
    const provisional = `${'word '.repeat(24)}\u2026`
    expect(Array.from(provisional)).toHaveLength(121)
    expect(clipLabel(provisional)).toBe(provisional)
    for (const long of [`${exact}b`, Array.from({ length: 40 }, (_, i) => `word${i}`).join(' ')]) {
      const held = clipLabel(long)
      expect(Array.from(held).length).toBeLessThanOrEqual(121)
      expect(held.endsWith('\u2026')).toBe(true)
      expect(clipLabel(held)).toBe(held)
    }
  })
})

describe('run presentation', () => {
  const ctx = stateContext()
  const shown = (over: Parameters<typeof run>[0]) => presentRun(toRecord(run(over)), ctx)

  it('maps every wire status and activity to its state', () => {
    expect(shown({ status: 'queued' })).toEqual({ state: 'working', reason: 'Queued' })
    expect(shown({ status: 'provisioning' })).toEqual({ state: 'working', reason: 'Starting' })
    expect(shown({ status: 'running', state: 'working' })).toEqual({ state: 'working', reason: 'Agent working' })
    expect(shown({ status: 'completed' })).toEqual({ state: 'finished', reason: 'Finished' })
    expect(shown({ status: 'merged' })).toEqual({ state: 'finished', reason: 'Merged' })
    expect(shown({ status: 'abandoned' })).toEqual({ state: 'finished', reason: 'Closed without merging' })
    expect(shown({ status: 'failed', reason: 'agent exited 1' })).toEqual({ state: 'failed', reason: 'Failed: Agent exited with code 1' })
    expect(shown({ status: 'interrupted' })).toEqual({ state: 'failed', reason: 'Interrupted' })
    for (const state of ['working', 'waiting-agents', 'waiting-tools', 'idle', 'finished', 'failed', 'disconnected', 'needs-you'] as const) {
      expect(shown({ status: 'needs-attention', state }).state).toBe(state)
    }
  })

  it('labels a live run without a state by its status, never Working', () => {
    expect(shown({ status: 'running', state: '' })).toEqual({ state: 'running', reason: 'Running' })
    expect(shown({ status: 'needs-attention', state: undefined, reason: 'agent idle' })).toEqual({ state: 'needs-attention', reason: 'Agent idle' })
  })

  it('reads what the agent is doing from its tool activity while it works', () => {
    const reading = { ...toRecord(run({ state: 'working' })), toolActivity: { verb: 'Reading', target: 'src/auth.ts', at: '2026-08-14T10:19:00Z' } }
    expect(presentRun(reading, ctx).reason).toBe('Reading src/auth.ts')
    expect(presentRun({ ...reading, state: 'waiting-agents', reason: 'waiting on 3 workers' }, ctx).reason).toBe('waiting on 3 workers')
  })

  it('counts held background tasks in the reason', () => {
    expect(backgroundReason([])).toBe('Working in background')
    expect(backgroundReason([{ id: 'a', holds: true }, { id: 'b', holds: true }, { id: 'c' }])).toBe('Working in background: 2 tasks')
  })

  it('names an integrator that has left agent messages unread for over two minutes', () => {
    const integrator = (over: Parameters<typeof run>[0]) =>
      presentRun(toRecord(run({ mission_id: 'mission_1', mission_role: 'integrator', state: 'waiting-agents', ...over })), ctx)
    expect(integrator({ unacked_messages: 3, oldest_unacked_at: '2026-08-14T10:08:00Z' })).toEqual({
      state: 'waiting-agents', reason: 'Integrator has not read 3 messages (12 min)', unread: 3,
    })
    expect(integrator({ unacked_messages: 1, oldest_unacked_at: '2026-08-14T10:19:00Z' }).reason).toBe('Waiting on its agents')
    expect(presentRun(toRecord(run({ unacked_messages: 3, oldest_unacked_at: '2026-08-14T10:08:00Z' })), ctx).reason).toBe('Agent working')
  })

  it('shows a paused live run as Paused, grouped under Working', () => {
    const paused = toRecord(run({ status: 'needs-attention', state: 'needs-you', paused: true }))
    expect(presentRun(paused, ctx).state).toBe('paused')
    expect(groupOf({ state: 'paused' })).toBe('working')
  })

  it('groups by whether the viewer is needed, then by state', () => {
    const failed = shown({ status: 'needs-attention', state: 'failed', reason: 'agent reported failure', outcome_unseen: true })
    expect(groupOf(failed)).toBe('needs-you')
    expect(groupOf(shown({ status: 'needs-attention', state: 'disconnected' }))).toBe('needs-you')
    for (const state of ['working', 'waiting-agents', 'waiting-tools', 'idle', 'paused'] as const) expect(groupOf({ state })).toBe('working')
    expect(groupOf({ state: 'finished' })).toBe('finished')
    expect(groupOf({ state: 'failed' })).toBe('finished')
  })

  it.each(['tui', 'acp'] as const)('keeps %s reported work distinct from a finished runtime', (mode) => {
    for (const outcome of ['success', 'failure'] as const) {
      const parked = toRecord(run({
        mode, acp: mode === 'acp', status: 'needs-attention', state: outcome === 'success' ? 'finished' : 'failed',
        reason: `agent reported ${outcome}`, outcome_unseen: true,
      }))
      expect(isTerminal(parked.status)).toBe(false)
      expect(awaitingReview(parked)).toBe(true)
      expect(presentRun(parked, ctx).reason).toContain('Run remains open for follow-up.')
      expect(presentRun({ ...parked, status: 'running', state: 'working', reason: 'agent resumed', outcome_unseen: false }, ctx))
        .toEqual({ state: 'working', reason: 'Agent working' })
    }
  })

  it('lists an unreviewed outcome only while it is an agent outcome', () => {
    expect(awaitingReview({ status: 'completed', outcome_unseen: true })).toBe(true)
    expect(awaitingReview({ status: 'failed', outcome_unseen: true })).toBe(true)
    expect(awaitingReview({ status: 'completed' })).toBe(false)
    // A later transition the flag outlived is not an agent outcome to review.
    expect(awaitingReview({ status: 'merged', outcome_unseen: true })).toBe(false)
    expect(awaitingReview({ status: 'running', state: 'working', outcome_unseen: true })).toBe(false)
  })

  it('labels every state in words, so no state is told by colour alone', () => {
    expect(Object.values(stateLabel)).toEqual([
      'Working', 'Waiting on agents', 'Waiting on tools', 'Needs you', 'Idle', 'Finished', 'Failed', 'Disconnected', 'Paused',
      'Running', 'Parked',
    ])
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
    ['server restarted: container exited 143', 'Its container had stopped with exit code 143 when the server restarted'],
    ['stalled: no output or file changes for 10m0s', 'No output or file changes for 10m0s'],
    ["the agent's turn ended: max_tokens", 'Stopped: the reply reached its token limit'],
    ['needs input: Which database?', 'Agent asks: Which database?'],
    ['turn cancelled', 'Turn cancelled'],
    ['turn interrupted', 'Turn interrupted'],
    ['turn ended: refusal', 'Stopped: the agent refused'],
    ['turn ended: something_new', "The agent's turn ended (something_new)"],
    ['working in background: 1 task', 'Working in background: 1 task'],
    ['review retained evidence', "Review the worker's saved results"],
    ['killed', 'Stopped'],
    ['disk quota exceeded', 'disk quota exceeded'],
  ])('reads %s', (reason, plain) => {
    expect(plainReason(reason)).toBe(plain)
  })
})
