import { composerBlock, enhancedBlock, pillFor } from '@/routes/run/composer-state'
import { toRecord } from '@/store/runs'
import { run } from '@/test/fixtures'

describe('the composer pill', () => {
  const base = { paused: false, turnRunning: false, steering: true, queueHeld: false, empty: false }
  it.each([
    [{}, 'send'],
    [{ turnRunning: true }, 'steer'],
    [{ turnRunning: true, queueHeld: true }, 'queue'],
    [{ turnRunning: true, steering: false }, 'queue'],
    [{ turnRunning: true, empty: true }, 'interrupt'],
    [{ paused: true, turnRunning: true }, 'resume'],
  ] as const)('%j reads %s', (over, pill) => {
    expect(pillFor({ ...base, ...over })).toBe(pill)
  })
})

describe('the Enhanced composer gate', () => {
  const open = {
    run: toRecord(run({ mode: 'acp', acp: true })),
    maySteer: true,
    canReopen: true,
    stream: 'live' as const,
    sessionLive: true,
    pending: 0,
    hasLease: true,
    controller: null,
  }
  it.each([
    [{}, null],
    [{ run: toRecord(run({ mode: 'acp', acp: true, switching: 'tui' })) }, 'Switching to Standard…'],
    [{ run: toRecord(run({ mode: 'headless', acp: true })) }, 'Background runs take no input.'],
    [{ stream: 'connecting' as const }, 'Connecting to the agent…'],
    [{ sessionLive: false }, 'The agent’s session is not running.'],
    [{ pending: 1 }, 'Answer the request above to continue.'],
    [{ hasLease: false }, 'Take control to message the agent.'],
    [{ hasLease: false, controller: 'self' as const }, 'Your other session still holds control.'],
    [{ hasLease: false, controller: 'other' as const }, 'Someone else controls this run.'],
  ])('%j', (over, reason) => {
    expect(enhancedBlock({ ...open, ...over })?.reason ?? null).toBe(reason)
  })

  it.each(['success', 'failure'] as const)('accepts follow-ups after reported %s without Reopen', (outcome) => {
    for (const mode of ['tui', 'acp'] as const) {
      const parked = toRecord(run({
        mode, acp: mode === 'acp', status: 'needs-attention',
        reason: `agent reported ${outcome}`, outcome_unseen: true,
      }))
      expect(composerBlock(parked, true, false)).toBeNull()
      expect(enhancedBlock({ ...open, run: parked, canReopen: false })).toBeNull()
      expect(composerBlock({ ...parked, outcome_unseen: false }, true, false)).toBeNull()
      expect(composerBlock({ ...parked, status: 'abandoned' }, true, true))
        .toBe('This run has finished. Reopen it from More to message the agent.')
    }
  })

  it('keeps Interrupt for the lease holder while a request waits', () => {
    expect(enhancedBlock({ ...open, pending: 1 })?.interrupt).toBe(true)
    expect(enhancedBlock({ ...open, pending: 1, hasLease: false })?.interrupt).toBe(false)
  })
})
