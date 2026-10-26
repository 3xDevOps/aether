import type { SessionUsage } from '@/lib/session-types'
import { contextSegments, formatCost, formatShare, percentUsed, previousTurnUsage } from '@/routes/run/context-usage'
import type { Turn } from '@/store/session-rows'
import { item, resetItems } from '@/test/acp-stream'

const usage = (used: number, size = 200_000): SessionUsage => ({ used, size })
const turn = (n: number, ...used: number[]): Turn => ({ turn: n, closed: true, items: [item('message', n), ...used.map((u) => item('usage', n, { usage: usage(u) }))] })

beforeEach(resetItems)

describe('previousTurnUsage', () => {
  it('reads the last report of the turn before the newest one with a report', () => {
    expect(previousTurnUsage([turn(1, 10_000, 40_000), turn(2, 60_000, 90_000)])).toEqual(usage(40_000))
  })

  it('skips turns that reported nothing', () => {
    expect(previousTurnUsage([turn(1, 40_000), turn(2), turn(3, 90_000), turn(4)])).toEqual(usage(40_000))
  })

  it.each([
    ['no turns', []],
    ['one turn with a report', [turn(1, 40_000)]],
    ['no report at all', [turn(1), turn(2)]],
  ])('has no baseline with %s', (_, turns) => {
    expect(previousTurnUsage(turns)).toBeUndefined()
  })
})

describe('contextSegments', () => {
  const split = (segments: ReturnType<typeof contextSegments>) => segments.map(({ key, tokens, cells }) => [key, tokens, cells])

  it('splits the window into used and free cells', () => {
    expect(split(contextSegments(usage(86_000)))).toEqual([['used', 86_000, 93], ['free', 114_000, 123]])
  })

  it('splits used into earlier and the latest turn when the window grew from a baseline', () => {
    expect(split(contextSegments(usage(86_000), usage(50_000)))).toEqual([['earlier', 50_000, 54], ['latest', 36_000, 39], ['free', 114_000, 123]])
  })

  it.each([
    ['usage that fell after a compaction', usage(120_000)],
    ['no growth', usage(86_000)],
    ['a report for another window size', usage(50_000, 100_000)],
  ])('keeps one used segment for %s', (_, previous) => {
    expect(contextSegments(usage(86_000), previous).map((s) => s.key)).toEqual(['used', 'free'])
  })

  it('draws at least one cell for a small report and for a small latest turn', () => {
    expect(contextSegments(usage(100))[0]!.cells).toBe(1)
    expect(contextSegments(usage(100_100), usage(100_000)).map((s) => s.cells)).toEqual([107, 1, 108])
    expect(contextSegments(usage(0))[0]!.cells).toBe(0)
  })

  it('fills the grid and drops free once the window is full or exceeded', () => {
    const full = contextSegments(usage(250_000))
    expect(split(full)).toEqual([['used', 250_000, 216]])
    expect(contextSegments(usage(200_000)).map((s) => s.key)).toEqual(['used'])
  })
})

it('clamps the percent at the window', () => {
  expect([percentUsed(usage(86_000)), percentUsed(usage(190_000)), percentUsed(usage(250_000))]).toEqual([43, 95, 100])
})

it('shows a decimal of share only below 10 percent', () => {
  expect([formatShare(1_000, 200_000), formatShare(34_000, 200_000), formatShare(33_800, 200_000)]).toEqual(['0.5', '17', '17'])
})

it('formats a cost in its currency, and keeps a code Intl does not know', () => {
  expect(formatCost(1.42, 'USD')).toContain('1.42')
  expect(formatCost(1.5, 'credits')).toBe('1.5 credits')
})

describe('previousTurnUsage across a compaction', () => {
  it('has no baseline when the newest turn reported less than before', () => {
    expect(previousTurnUsage([turn(1, 50_000), turn(2, 150_000, 70_000)])).toBeUndefined()
  })

  it('has no baseline when the window changed within the newest turn', () => {
    const resized: Turn = { turn: 2, closed: false, items: [item('usage', 2, { usage: usage(60_000, 400_000) })] }
    expect(previousTurnUsage([turn(1, 50_000), resized])).toBeUndefined()
  })

  it('keeps the baseline while the newest turn only grows', () => {
    expect(previousTurnUsage([turn(1, 50_000), turn(2, 60_000, 70_000)])).toEqual(usage(50_000))
  })
})
