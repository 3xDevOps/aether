import { chartDays, chartMax, dayWords, duration, percent, tokens, weekRange } from '@/routes/insights/model'
import { teamInsights } from '@/test/fixtures'

describe('insights model', () => {
  test('the week ends at the next local midnight, so today counts', () => {
    expect(weekRange(new Date(2026, 9, 16, 15, 30))).toEqual({ from: new Date(2026, 9, 10), to: new Date(2026, 9, 17) })
  })

  test('100% means every finished run or swarm succeeded', () => {
    expect(percent(199, 200)).toBe(99)
    expect(percent(2, 2)).toBe(100)
    expect(percent(0, 0)).toBeNull()
  })

  test('tokens read compactly', () => {
    expect(tokens(100_000)).toBe('100K tokens')
    expect(tokens(1_250_000)).toBe('1.3M tokens')
    expect(tokens(1)).toBe('1 token')
    expect(tokens(0)).toBe('0 tokens')
  })

  test('a team day splits each outcome by the members who have some', () => {
    const days = chartDays(teamInsights(), 'runs')
    expect(days).toHaveLength(7)
    expect(days[5].counts).toEqual({ done: 7, failed: 2, abandoned: 0 })
    expect(days[5].members.done.map((m) => [m.name, m.count])).toEqual([['Alice', 5], ['Bob', 2]])
    expect(days[5].members.abandoned).toEqual([])
    expect(chartMax(days)).toBe(7)
    expect(dayWords(days[5], 'runs')).toBe('Wed, Oct 14: runs 7 done (Alice 5, Bob 2), 2 failed (Alice 1, Bob 1), 0 abandoned')
    expect(chartDays(teamInsights(), 'swarms')[6].counts).toEqual({ done: 1, failed: 0, abandoned: 1 })
  })

  test('an empty week still has a scale', () => {
    expect(chartMax([])).toBe(1)
  })

  test('durations read at the precision that matters', () => {
    expect(duration(45)).toBe('45s')
    expect(duration(75)).toBe('1m 15s')
    expect(duration(1260)).toBe('21m')
    expect(duration(4100)).toBe('1h 8m')
    expect(duration(97200)).toBe('1d 3h')
  })
})
