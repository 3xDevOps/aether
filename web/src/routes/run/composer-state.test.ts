import { composerBlock, composerDraft, enhancedBlock, pillFor, saveComposerDraft } from '@/routes/run/composer-state'
import { useStore } from '@/store'
import { toRecord } from '@/store/runs'
import { run } from '@/test/fixtures'

describe('composer drafts', () => {
  beforeEach(() => {
    useStore.getState().setIdentityKey('alice')
    useStore.getState().setRuns([run(), run({ id: 'run_2' })])
    saveComposerDraft('run_1', { body: 'first' })
    saveComposerDraft('run_2', { body: 'second', idempotency: { identity: '["second",[]]', key: 'key' } })
  })

  it('holds one draft per run until it is emptied', () => {
    saveComposerDraft('run_2', { body: '' })
    expect(composerDraft('run_1').body).toBe('first')
    expect(composerDraft('run_2')).toEqual({ body: '', images: [] })
  })

  it('saves without a root-store write, which would rewrite aether.ui', () => {
    const stored = vi.spyOn(Storage.prototype, 'setItem')
    saveComposerDraft('run_1', { body: 'first line' })
    expect(stored).not.toHaveBeenCalled()
    stored.mockRestore()
  })

  it('drops the draft of a run that no longer exists', () => {
    useStore.getState().removeRun('run_1')
    expect(composerDraft('run_1').body).toBe('')
    expect(composerDraft('run_2').body).toBe('second')
    useStore.getState().setRuns([])
    expect(composerDraft('run_2').body).toBe('')
  })

  it('drops every draft when another member or server is authenticated', () => {
    useStore.getState().setIdentityKey('bob')
    expect(composerDraft('run_1').body).toBe('')
    expect(composerDraft('run_2').body).toBe('')
  })
})

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
