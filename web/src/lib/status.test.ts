import { awaitingReview } from '@/lib/needs-you'
import { plainReason } from '@/lib/reasons'
import { backgroundReason, clipLabel, groupOf, presentRun, runLabel, stateDetail, stateLabel, stateName, stateTitle, stateTone, stateWords, toneOf } from '@/lib/status'
import { clockTime } from '@/lib/format'
import type { RunActivity, RunWait } from '@/lib/types'
import { isTerminal, toRecord } from '@/store/runs'
import { bob, run, stateContext } from '@/test/fixtures'


/** A state's words as a card reads them: the label, then the detail. */
const stateLine = (shown: Parameters<typeof stateWords>[0]) => {
  const { label, detail } = stateWords(shown)
  return detail ? `${label} · ${detail}` : label
}

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
    expect(shown({ status: 'completed' })).toEqual({ state: 'completed', reason: 'Finished' })
    expect(shown({ status: 'completed', review: 'accepted' })).toEqual({ state: 'completed', reason: 'Marked complete' })
    expect(shown({ status: 'merged' })).toEqual({ state: 'merged', reason: 'Merged' })
    expect(shown({ status: 'abandoned' })).toEqual({ state: 'closed', reason: 'Closed without merging' })
    expect(shown({ status: 'failed', reason: 'agent exited 1' })).toEqual({ state: 'failed', reason: 'Failed: Agent exited with code 1' })
    expect(shown({ status: 'interrupted' })).toEqual({ state: 'interrupted', reason: 'Interrupted' })
    const live: [RunActivity, string][] = [
      ['working', 'Working'],
      ['waiting-agents', 'Waiting for agents'],
      ['waiting-ci', 'Waiting for CI'],
      ['waiting-tools', 'Waiting for process'],
      ['waiting', 'Turn ended'],
      ['answered', 'Answered'],
      ['idle', 'Turn ended'],
      ['finished', 'Ready for review'],
      ['failed', 'Failed'],
      ['disconnected', 'Disconnected'],
      ['needs-you', 'Needs you'],
    ]
    for (const [state, label] of live) {
      expect(stateLabel[shown({ status: 'needs-attention', state }).state]).toBe(label)
    }
  })

  it('shows an unreported turn end, and the idle older servers wrote, as a quiet Turn ended, never Idle', () => {
    const legacy = shown({ status: 'needs-attention', state: 'idle', reason: 'agent idle' })
    expect(legacy).toEqual(shown({ status: 'needs-attention', state: 'waiting', reason: 'turn ended without a report' }))
    expect(legacy).toEqual({ state: 'ended', reason: 'Turn ended without a report' })
    expect(stateLine(legacy)).toBe('Turn ended')
    expect(stateTone[legacy.state]).toBe('ended')
    expect(Object.values(stateLabel).join(' ')).not.toMatch(/idle/i)
    expect(plainReason('agent idle')).toBe('Turn ended without a report')
  })

  it('gives each waiting state the reason the agent reported and what happens next', () => {
    expect(shown({ status: 'running', state: 'waiting-ci', reason: 'waiting for the PR checks' })).toEqual({
      state: 'waiting-ci', reason: 'waiting for the PR checks', next: 'It resumes on its next message.',
    })
    expect(shown({ status: 'running', state: 'waiting-tools', reason: 'deploy running' }).next).toBe('It resumes on its next message.')
    expect(shown({ status: 'running', state: 'waiting-agents' })).toEqual({
      state: 'waiting-agents', reason: 'Waiting for its agents to report', next: 'Mail from its agents can wake it.',
    })
    expect(shown({ status: 'running', state: 'waiting-tools', reason: 'working in background: 1 task' })).toEqual({
      state: 'waiting-tools', reason: 'Working in background: 1 task', next: 'Claude Code resumes it when a background task ends.',
    })
    expect(shown({ status: 'needs-attention', state: 'waiting', reason: 'no update for 12m' }).reason).toBe('No update for 12m')
  })

  it('shows an answered turn calmly, with no call to action', () => {
    expect(shown({ status: 'needs-attention', state: 'answered', reason: 'explained the retry policy' })).toEqual({
      state: 'answered', reason: 'explained the retry policy',
    })
    expect(shown({ status: 'needs-attention', state: 'answered' })).toEqual({ state: 'answered', reason: 'Answered' })
    expect(stateLine(shown({ status: 'needs-attention', state: 'answered' }))).toBe('Answered')
    expect(stateLine(shown({ status: 'needs-attention', state: 'answered', reason: 'explained the retry policy' }))).toBe('Answered · explained the retry policy')
    expect(stateTone.answered).toBe('ended')
    expect(groupOf({ state: 'answered' })).toBe('working')
  })

  it('prints a reason beside its label only where it adds something', () => {
    expect(stateDetail({ state: 'working', reason: 'Agent working' })).toBeUndefined()
    expect(stateDetail({ state: 'ended', reason: 'Turn ended without a report' })).toBeUndefined()
    expect(stateDetail({ state: 'ended', reason: 'No update for 30m: CI on PR 12' })).toBe('No update for 30m: CI on PR 12')
    expect(stateDetail({ state: 'failed', reason: 'Failed: Agent exited with code 1' })).toBe('Agent exited with code 1')
    expect(stateDetail({ state: 'merged', reason: 'Merged' })).toBeUndefined()
  })

  it('titles a state with how long it has held, the reason and the next step', () => {
    const now = Date.parse('2026-08-14T10:20:00Z')
    expect(stateTitle({ state: 'ended', reason: 'Turn ended without a report' }, '2026-08-14T10:15:00Z', now))
      .toBe('Turn ended since 5 minutes ago.')
    expect(stateTitle({ state: 'waiting-ci', reason: 'PR 12 checks', next: 'It resumes on its next message.', cause: 'report' }, '2026-08-14T10:15:00Z', now))
      .toBe('Waiting for CI since 5 minutes ago — PR 12 checks. It resumes on its next message. Why: the agent reported it.')
    expect(stateTitle({ state: 'working', reason: 'Working' }, '2026-08-14T10:15:00Z', now)).toBe('Working since 5 minutes ago.')
  })

  it('reads a reported success as Ready for review until the owner answers it', () => {
    const ready = shown({ status: 'needs-attention', state: 'finished', reason: 'agent reported success', review: 'ready' })
    expect(ready).toMatchObject({ state: 'ready', reason: 'Your agent reports this task is finished' })
    expect(ready.needsYou?.id).toBe('ready')
    expect(groupOf(ready)).toBe('needs-you')
    const later = shown({ status: 'needs-attention', state: 'finished', reason: 'agent reported success', review: 'later' })
    expect(later).toEqual({ state: 'ready', reason: 'Agent reported success', next: 'Mark it complete, or send a message to keep going.' })
    expect(groupOf(later)).toBe('review')
    const headless = shown({ mode: 'headless', status: 'completed', reason: 'agent reported success', review: 'ready' })
    expect(headless.state).toBe('ready')
    expect(groupOf(headless)).toBe('needs-you')
    expect(shown({ status: 'completed', review: 'later' }).state).toBe('ready')
    expect(groupOf(shown({ status: 'completed', review: 'accepted' }))).toBe('finished')
    // Only the owner is asked; a teammate sees the same state in the quiet group.
    const teammate = shown({ status: 'needs-attention', state: 'finished', review: 'ready', member_id: bob.id })
    expect(teammate.state).toBe('ready')
    expect(groupOf(teammate)).toBe('review')
  })

  it('labels a live run without a known state honestly, never Working', () => {
    expect(shown({ status: 'running', state: '' })).toEqual({ state: 'running', reason: 'Running' })
    expect(shown({ status: 'needs-attention', state: undefined, reason: 'agent idle' })).toEqual({
      state: 'ended', reason: 'Turn ended without a report',
    })
    const future = shown({ status: 'running', state: 'compacting' as RunActivity })
    expect(future).toEqual({ state: 'running', reason: 'Running' })
    expect(stateTone[future.state]).toBe('neutral')
  })

  it('reads what the agent is doing from its tool activity while it works', () => {
    const reading = { ...toRecord(run({ state: 'working' })), toolActivity: { verb: 'Reading', target: 'src/auth.ts', at: '2026-08-14T10:19:00Z' } }
    expect(presentRun(reading, ctx).reason).toBe('Reading src/auth.ts')
    expect(presentRun({ ...reading, state: 'waiting-agents', reason: 'waiting on 3 child runs' }, ctx).reason).toBe('waiting on 3 child runs')
  })

  it('counts held background tasks in the reason', () => {
    expect(backgroundReason([])).toBe('Working in background')
    expect(backgroundReason([{ id: 'a', holds: true }, { id: 'b', holds: true }, { id: 'c' }])).toBe('Working in background: 2 tasks')
  })

  it('shows a paused live run as Paused, grouped under Working', () => {
    const paused = toRecord(run({ status: 'needs-attention', state: 'needs-you', paused: true }))
    expect(presentRun(paused, ctx).state).toBe('paused')
    for (const review of ['ready', 'later'] as const) {
      expect(shown({ status: 'needs-attention', state: 'finished', review, paused: true }).state).toBe('paused')
    }
    expect(shown({ status: 'completed', review: 'ready', paused: true }).state).toBe('ready')
    expect(groupOf({ state: 'paused' })).toBe('working')
  })

  it('groups by whether the viewer is needed, then by state', () => {
    const failed = shown({ status: 'needs-attention', state: 'failed', reason: 'agent reported failure', outcome_unseen: true })
    expect(groupOf(failed)).toBe('needs-you')
    expect(groupOf(shown({ status: 'needs-attention', state: 'disconnected' }))).toBe('needs-you')
    for (const state of ['working', 'waiting-agents', 'waiting-ci', 'waiting-tools', 'waiting', 'paused'] as const) expect(groupOf({ state })).toBe('working')
    expect(groupOf({ state: 'ready' })).toBe('review')
    for (const state of ['completed', 'merged', 'closed', 'interrupted', 'failed'] as const) expect(groupOf({ state })).toBe('finished')
  })

  it.each(['tui', 'acp'] as const)('keeps %s reported work distinct from a finished runtime', (mode) => {
    for (const outcome of ['success', 'failure'] as const) {
      const parked = toRecord(run({
        mode, acp: mode === 'acp', status: 'needs-attention', state: outcome === 'success' ? 'finished' : 'failed',
        reason: `agent reported ${outcome}`, outcome_unseen: true,
      }))
      expect(isTerminal(parked.status)).toBe(false)
      expect(awaitingReview(parked)).toBe(true)
      if (outcome === 'success') expect(presentRun(parked, ctx).state).toBe('ready')
      else expect(presentRun(parked, ctx).reason).toContain('Run remains open for follow-up.')
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

  it('labels every state in words and gives each its own dot, so no state is told by colour alone', () => {
    expect(Object.values(stateLabel)).toEqual([
      'Working', 'Waiting for agents', 'Waiting for CI', 'Waiting for process', 'Turn ended', 'Waiting', 'Answered', 'Needs you', 'Ready for review',
      'Failed', 'Disconnected', 'Completed', 'Merged', 'Closed', 'Interrupted', 'Paused', 'Running', 'Possibly stalled',
    ])
    expect(stateTone).toMatchObject({
      'waiting-agents': 'waiting', 'waiting-ci': 'waiting', 'waiting-tools': 'waiting', ended: 'ended', waiting: 'ended',
      'needs-you': 'needs-you', ready: 'review', completed: 'done', failed: 'failed', disconnected: 'disconnected', stalled: 'waiting',
    })
  })
})

describe('plainReason', () => {
  it.each([
    ['agent reported success; retained container', 'Agent reported success'],
    ['closed; retained container', 'Closed'],
    ['agent exited; results committed', 'Agent exited and its changes were committed'],
    ['agent exited 1: relation "invoices" does not exist', 'Agent exited with code 1: relation "invoices" does not exist'],
    ['retained container expired', 'Its container was removed when it expired'],
    ['server restarted: container exited 143', 'Its container had stopped with exit code 143 when the server restarted'],
    ['stalled: no output or file changes for 10m0s', 'No output or file changes for 10m0s'],
    ["stalled: possibly — no progress for 6m0s, awaiting first event, transport open", "No progress for 6m while awaiting the model's first reply; connection open"],
    ['stalled: possibly — no progress for 1h0m0s, tool running, transport open', 'No progress for 1h while a tool was running; connection open'],
    ['stalled: possibly — no progress for 5m30s, streaming, transport closed', 'No progress for 5m30s since the agent last replied; connection closed'],
    ["the agent's turn ended: max_tokens", 'Stopped: the reply reached its token limit'],
    ['needs input: Which database?', 'Agent asks: Which database?'],
    ['turn cancelled', 'Turn cancelled'],
    ['turn interrupted', 'Turn interrupted'],
    ['turn ended: refusal', 'Stopped: the agent refused'],
    ['turn ended: something_new', "The agent's turn ended (something_new)"],
    ['working in background: 1 task', 'Working in background: 1 task'],
    ['no update for 30m: CI on PR 12', 'No update for 30m: CI on PR 12'],
    ['waiting for 1 child run', 'Waiting for 1 child run'],
    ['waiting for 3 workers', 'Waiting for 3 child runs'],
    ['2 unread worker messages', '2 unread child messages'],
    ['review retained evidence', "Review the worker's saved results"],
    ['killed', 'Stopped'],
    ['disk quota exceeded', 'disk quota exceeded'],
  ])('reads %s', (reason, plain) => {
    expect(plainReason(reason)).toBe(plain)
  })
})

describe('state words', () => {
  const ctx = stateContext()
  const shown = (over: Parameters<typeof run>[0]) => presentRun(toRecord(run(over)), ctx)
  const now = Date.parse('2026-08-14T11:00:00Z')
  const since = '2026-08-14T10:18:00Z'

  it('names what a run waits on in place of the generic label', () => {
    const children = shown({ status: 'needs-attention', state: 'waiting-agents', reason: 'waiting for 2 child runs', cause: 'workers' })
    expect(stateWords(children)).toEqual({ label: 'Waiting for 2 child runs' })
    expect(stateLine(children)).toBe('Waiting for 2 child runs')
    expect(stateTitle(children, since, now)).toBe(
      'Waiting for 2 child runs since 42 minutes ago. Mail from its agents can wake it. Why: its child runs are live.',
    )
    expect(stateLine(shown({ status: 'needs-attention', state: 'waiting-agents', reason: 'reviewers on PR 12' }))).toBe('Waiting for agents · reviewers on PR 12')
  })

  it('says how long a CI wait has run where nothing else prints its age', () => {
    const ci = shown({ status: 'needs-attention', state: 'waiting-ci', reason: 'PR 12 checks', cause: 'report' })
    expect(stateWords(ci, since, now)).toEqual({ label: 'Waiting for CI', detail: '42m · PR 12 checks' })
    expect(stateLine(ci)).toBe('Waiting for CI · PR 12 checks')
    const stale = shown({ status: 'needs-attention', state: 'waiting-ci', reason: 'no update for 30m: PR 12 checks', cause: 'wait-expired' })
    expect(stateWords(stale, since, now)).toEqual({ label: 'Waiting for CI', detail: 'Reported waiting on PR 12 checks, no update for 30m' })
    expect(stateLine(shown({ status: 'needs-attention', state: 'waiting-tools', reason: 'deploy to staging' }))).toBe('Waiting for process · deploy to staging')
  })

  it('carries the cause only while the run shows the state the server chose', () => {
    expect(shown({ status: 'needs-attention', state: 'waiting', cause: 'turn-end' }).cause).toBe('turn-end')
    expect(shown({ status: 'completed', cause: 'report' }).cause).toBeUndefined()
    expect(stateTitle({ state: 'ended', reason: 'Turn ended without a report', cause: 'turn-end' }, since, now)).toBe('Turn ended since 42 minutes ago. Why: the turn ended.')
    expect(stateTitle({ state: 'working', reason: 'Agent working', cause: 'something-new' as never }, since, now)).toBe('Working since 42 minutes ago. Why: something-new.')
  })

  it('reads a stall that names its phase as Possibly stalled, never Turn ended or Needs you', () => {
    const reason = 'stalled: possibly — no progress for 6m0s, awaiting first event, transport open'
    const stalled = shown({ status: 'needs-attention', state: 'waiting', reason, cause: 'stall' })
    expect(stalled).toMatchObject({ state: 'stalled', cause: 'stall' })
    expect(stalled.needsYou).toBeUndefined()
    expect(groupOf(stalled)).toBe('working')
    expect(stateName(stalled)).toBe('Possibly stalled')
    expect(toneOf(stalled)).toBe('waiting')
    expect(stateLine(stalled)).toBe("Possibly stalled · No progress for 6m while awaiting the model's first reply; connection open")
    expect(stateTitle(stalled, since, now)).toBe(
      "Possibly stalled since 42 minutes ago — No progress for 6m while awaiting the model's first reply; connection open. It returns to Working with the agent's next update. Why: the run stalled.",
    )
    // A stall with no phase is not judged on progress and keeps its words.
    expect(shown({ status: 'needs-attention', state: 'waiting', reason: 'stalled: no output or file changes for 10m0s', cause: 'stall' }).state).toBe('ended')
    // A turn end after the stall replaces its reason; a stale reason on a running run is not a stall.
    expect(shown({ status: 'running', state: 'working', reason }).state).toBe('working')
  })

  it('calls a reported wait that went stale a stale wait, not a turn that ended', () => {
    const stale = shown({ status: 'needs-attention', state: 'waiting', reason: 'no update for 30m: CI on #582', cause: 'wait-expired' })
    expect(stateWords(stale)).toEqual({ label: 'Stale wait', detail: 'Reported waiting on CI on #582, no update for 30m' })
    expect(stateWords(shown({ status: 'needs-attention', state: 'waiting', reason: 'no update for 1h5m', cause: 'wait-expired' })))
      .toEqual({ label: 'Stale wait', detail: 'Reported waiting, no update for 1h5m' })
    expect(groupOf(stale)).toBe('working')
    expect(toneOf(stale)).toBe('ended')
    // Only the server's stale cause renames it: an agent's summary that reads the same does not.
    expect(stateWords(shown({ status: 'needs-attention', state: 'waiting-agents', reason: 'No update for 2h: from the reviewer', cause: 'report' })))
      .toEqual({ label: 'Waiting for agents', detail: 'No update for 2h: from the reviewer' })
  })

  it('never asks for a message where nothing is required', () => {
    for (const state of ['waiting', 'answered', 'waiting-agents', 'waiting-ci', 'waiting-tools'] as const) {
      const words = stateTitle(shown({ status: 'needs-attention', state }), since, now)
      expect(words).not.toMatch(/send a message/i)
    }
  })
})

describe('checked waits', () => {
  const ctx = stateContext()
  const now = Date.parse('2026-08-14T11:00:00Z')
  const since = '2026-08-14T10:18:00Z'
  const spec = 'pr:acme/web#582:checks'
  const shown = (wait: RunWait, over: Parameters<typeof run>[0] = {}) =>
    presentRun(toRecord(run({ status: 'needs-attention', state: 'waiting-ci', reason: 'CI on #582', cause: 'report', wait, ...over })), { ...ctx, now })

  it('says what a pending condition last read and how long ago, in place of the summary', () => {
    const wait: RunWait = { kind: 'condition', detail: `${spec}: open, 3 checks running`, verified_at: '2026-08-14T10:58:00Z' }
    const ci = shown(wait)
    expect(ci.wait).toEqual(wait)
    expect(stateWords(ci, undefined, now)).toEqual({ label: 'Waiting for CI', detail: `Waiting on ${spec}: open, 3 checks running · checked 2m ago` })
    expect(stateWords(ci, undefined, Date.parse('2026-08-14T10:58:20Z')).detail).toBe(`Waiting on ${spec}: open, 3 checks running · checked just now`)
    expect(stateTitle(ci, since, now)).toBe(
      `Waiting for CI since 42 minutes ago — Waiting on ${spec}: open, 3 checks running · checked 2m ago. ` +
        `Last read ${new Date('2026-08-14T10:58:00Z').toLocaleString()}. The agent reported: CI on #582. It resumes on its next message. Why: the agent reported it.`,
    )
    expect(toneOf(ci)).toBe('waiting')
    expect(groupOf(ci)).toBe('working')
    expect(ci.needsYou).toBeUndefined()
  })

  it('never shows an unreadable source as live: it says since when the status is unknown and the real error', () => {
    const error = 'gh: HTTP 502: Bad Gateway (https://api.github.com/graphql)'
    const unknown = shown({ kind: 'source_unknown', detail: `${spec}: status unknown`, verified_at: '2026-08-14T10:10:00Z', error })
    expect(stateWords(unknown, undefined, now).detail).toBe(
      `Waiting on ${spec}: status unknown since ${clockTime('2026-08-14T10:10:00Z', now)} — ${error}`,
    )
    expect(stateWords(unknown, undefined, now).detail).not.toMatch(/checked|running/)
    expect(toneOf(unknown)).toBe('wait-unknown')
    expect(unknown.needsYou).toBeUndefined()
    const never = shown({ kind: 'source_unknown', detail: `${spec}: status never read`, error: 'gh: Could not resolve to a PullRequest' })
    expect(stateWords(never, undefined, now).detail).toBe(`Waiting on ${spec}: status never read — gh: Could not resolve to a PullRequest`)
  })

  it('shows a fault the agent is being told about in the fault tone, without asking for you', () => {
    const fault = shown({ kind: 'system_fault', detail: `${spec}: error, waking the agent`, error: 'pull request acme/web#582 not found' })
    expect(stateWords(fault, undefined, now)).toEqual({
      label: 'Waiting for CI',
      detail: `Wait fault: ${spec}: error, waking the agent — pull request acme/web#582 not found`,
    })
    expect(toneOf(fault)).toBe('wait-fault')
    expect(groupOf(fault)).toBe('working')
    expect(fault.needsYou).toBeUndefined()
  })

  it('lists a broken wait in Needs you only when the server says so, with the delivery error', () => {
    const wait: RunWait = { kind: 'system_fault', detail: `${spec} resolved, but the agent was not woken`, error: 'deliver wake: session closed' }
    const broken = shown(wait, { state: 'needs-you', reason: `wait broken: ${spec} resolved, but the agent was not woken`, cause: 'wait-fault' })
    expect(broken.needsYou?.id).toBeDefined()
    expect(groupOf(broken)).toBe('needs-you')
    expect(toneOf(broken)).toBe('needs-you')
    expect(stateWords(broken, undefined, now).detail).toBe(
      `Wait broken: ${spec} resolved, but the agent was not woken · reply to continue — deliver wake: session closed`,
    )
    expect(stateTitle({ state: 'needs-you', reason: 'Wait broken', cause: 'wait-fault' }, since, now))
      .toContain('Why: a condition it waits on resolved or broke, and the agent was not woken.')
  })

  it('shows the evidence of a wait whose summary names what it waits for', () => {
    const error = 'gh: HTTP 502: Bad Gateway (https://api.github.com/graphql)'
    const agents = shown({ kind: 'source_unknown', detail: `${spec}: status unknown`, error }, { state: 'waiting-agents', reason: 'Waiting for PR #582' })
    expect(stateWords(agents, undefined, now)).toEqual({ label: 'Waiting for agents', detail: `Waiting on ${spec}: status unknown — ${error}` })
    expect(stateTitle(agents, since, now)).toContain(error)
  })

  it('keeps the summary of a wait that names no condition, and says Aether cannot check it', () => {
    const unverified = shown({ kind: 'unverified' })
    expect(stateWords(unverified, undefined, now)).toEqual({ label: 'Waiting for CI', detail: 'CI on #582' })
    expect(toneOf(unverified)).toBe('waiting')
    expect(stateTitle(unverified, since, now)).toContain('Not verified: the wait names no condition Aether can check.')
  })

  it('drops the wait where the dashboard shows another state than the server', () => {
    const wait: RunWait = { kind: 'source_unknown', detail: `${spec}: status unknown`, error: 'timeout' }
    expect(presentRun(toRecord(run({ status: 'completed', wait })), ctx).wait).toBeUndefined()
    const paused = presentRun(toRecord(run({ status: 'needs-attention', state: 'waiting-ci', wait })), { ...ctx, pausedRuns: { run_1: true } })
    expect(paused.wait).toBeUndefined()
    expect(toneOf(paused)).toBe('paused')
  })
})
