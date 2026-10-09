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
  }

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
  })
})
