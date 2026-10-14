import { backgroundReason, plainReason, presentRun } from '@/lib/status'
import { toRecord } from '@/store/runs'
import { run, stateContext } from '@/test/fixtures'

describe('backgroundReason', () => {
  it('counts the tasks the run holds on, and names none when there are none', () => {
    expect(backgroundReason([])).toBe('Working in background')
    expect(backgroundReason([{ id: 'a', holds: true }])).toBe('Working in background: 1 task')
    expect(backgroundReason([{ id: 'a', holds: true }, { id: 'b', holds: true }, { id: 'c' }])).toBe('Working in background: 2 tasks')
  })
})

describe('presentRun with background work', () => {
  const ctx = stateContext()
  const activity = { verb: 'Reading', target: 'a.go', at: '2026-08-14T10:03:00Z', calls: {} } as never

  it('reads Working with the reason the server gave, over the run activity, and never Needs you', () => {
    const shown = presentRun(toRecord(run({ reason: 'working in background: 2 tasks' })), ctx)
    expect(shown).toEqual({ state: 'working', reason: 'Working in background: 2 tasks' })
    expect(shown.needsYou).toBeUndefined()
    const busy = { ...toRecord(run({ reason: 'working in background: 3 tasks' })), activity }
    expect(presentRun(busy, ctx)).toEqual({ state: 'working', reason: 'Working in background: 3 tasks' })
    expect(presentRun(toRecord(run({ reason: 'working in background' })), ctx)).toEqual({ state: 'working', reason: 'Working in background' })
  })

  it('keeps the existing reason without it', () => {
    expect(presentRun(toRecord(run()), ctx).reason).toBe('Agent working')
  })

  it('does not let it hide a parked run that asks a question', () => {
    const asked = toRecord(run({ status: 'needs-attention', reason: 'needs input: Which port?' }))
    expect(presentRun(asked, ctx)).toMatchObject({ state: 'needs-you', reason: 'Agent asks: Which port?' })
  })

  it('words the reason in Details', () => {
    expect(plainReason('working in background: 1 task')).toBe('Working in background: 1 task')
  })
})
