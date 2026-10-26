import { glideLimit, isLiveMove, type Placement } from '@/routes/board/glide'
import { toRecord } from '@/store/runs'
import { run } from '@/test/fixtures'

const records = (count: number) =>
  Object.fromEntries(Array.from({ length: count }, (_, i) => [`run_${i}`, toRecord(run({ id: `run_${i}` }))]))

function placement(
  runs: Placement['runs'],
  columns: Record<string, 'needs-you' | 'working' | 'finished'>,
  { scope = 'ws', snapshot = 1 } = {},
): Placement {
  return { scope, snapshot, runs, columns: new Map(Object.entries(columns)) }
}

describe('isLiveMove', () => {
  const runs = records(3)
  const before = placement(runs, { run_0: 'working', run_1: 'working', run_2: 'finished' })

  it('glides a run an event moves to another column', () => {
    const changed = { ...runs, run_0: { ...runs.run_0 } }
    expect(isLiveMove(before, placement(changed, { run_0: 'needs-you', run_1: 'working', run_2: 'finished' }))).toBe(true)
    const lone = records(1)
    expect(isLiveMove(placement(lone, { run_0: 'working' }), placement({ run_0: { ...lone.run_0 } }, { run_0: 'needs-you' }))).toBe(true)
  })

  it('keeps reorders, arrivals and departures still', () => {
    expect(isLiveMove(before, placement(runs, { run_1: 'working', run_0: 'working', run_2: 'finished' }))).toBe(false)
    expect(isLiveMove(before, placement(runs, { run_0: 'working', run_3: 'working' }))).toBe(false)
  })

  it('never glides a new scope or a snapshot, even of one run', () => {
    const moved = { run_0: 'needs-you', run_1: 'working', run_2: 'finished' } as const
    expect(isLiveMove(before, placement(runs, moved, { scope: 'other' }))).toBe(false)
    expect(isLiveMove(before, placement(records(3), moved, { snapshot: 2 }))).toBe(false)
    expect(isLiveMove(placement(records(1), { run_0: 'working' }), placement(records(1), { run_0: 'needs-you' }, { snapshot: 2 }))).toBe(false)
  })

  it(`never glides more than ${glideLimit} moves at once`, () => {
    const many = records(glideLimit + 1)
    const all = (column: 'working' | 'finished') => Object.fromEntries(Object.keys(many).map((id) => [id, column]))
    expect(isLiveMove(placement(many, all('working')), placement(many, all('finished')))).toBe(false)
  })
})
