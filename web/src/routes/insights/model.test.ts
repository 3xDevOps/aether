import { duration, periodRange, versus } from '@/routes/insights/model'

describe('insights model', () => {
  test('a period ends at the next local midnight, so today counts', () => {
    const now = new Date(2026, 9, 16, 15, 30)
    expect(periodRange('7d', now)).toEqual({ from: new Date(2026, 9, 10), to: new Date(2026, 9, 17) })
    expect(periodRange('12m', now)).toEqual({ from: new Date(2025, 9, 17), to: new Date(2026, 9, 17) })
  })

  test('compares in raw numbers', () => {
    expect(versus(12, 8, 'the 7 days before')).toBe('Up from 8 the 7 days before')
    expect(versus(3, 5, 'the 7 days before')).toBe('Down from 5 the 7 days before')
    expect(versus(0, 0, 'the 7 days before')).toBe('Same as the 7 days before')
  })

  test('durations read at the precision that matters', () => {
    expect(duration(45)).toBe('45s')
    expect(duration(75)).toBe('1m 15s')
    expect(duration(1260)).toBe('21m')
    expect(duration(4100)).toBe('1h 8m')
    expect(duration(97200)).toBe('1d 3h')
  })
})
