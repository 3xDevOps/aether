import { backgroundReason, presentRun } from '@/lib/status'
import { toRecord } from '@/store/runs'
import { run, stateContext } from '@/test/fixtures'

describe('backgroundReason', () => {
  it('counts the tasks, and names none when the ids are not known', () => {
    expect(backgroundReason([])).toBe('Working in background')
    expect(backgroundReason(['a'])).toBe('Working in background: 1 task')
    expect(backgroundReason(['a', 'b'])).toBe('Working in background: 2 tasks')
  })
})

describe('presentRun with background work', () => {
  const ctx = stateContext()

  it('reads Working with the background reason and never Needs you', () => {
    const shown = presentRun(toRecord(run()), ctx, ['a', 'b'])
    expect(shown).toEqual({ state: 'working', reason: 'Working in background: 2 tasks' })
    expect(shown.needsYou).toBeUndefined()
    expect(presentRun(toRecord(run()), ctx, [])).toMatchObject({ state: 'working', reason: 'Working in background' })
  })

  it('keeps the existing reason without it', () => {
    const idle = presentRun(toRecord(run()), ctx)
    expect(presentRun(toRecord(run()), ctx, null)).toEqual(idle)
    expect(idle.reason).toBe('Agent working')
  })

  it('does not let it hide a parked run', () => {
    const parked = toRecord(run({ status: 'needs-attention', reason: 'turn cancelled' }))
    expect(presentRun(parked, ctx, ['a'])).toEqual(presentRun(parked, ctx))
    expect(presentRun(parked, ctx, ['a']).state).toBe('needs-you')
  })
})
