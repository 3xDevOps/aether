import { chartSeries, duration, periodRange, successRate, versus } from '@/routes/insights/model'
import { insights } from '@/test/fixtures'

describe('insights model', () => {
  test('a period ends at the next local midnight, and twelve months start on a Monday', () => {
    const now = new Date(2026, 9, 16, 15, 30)
    expect(periodRange('7d', now)).toEqual({ from: new Date(2026, 9, 10), to: new Date(2026, 9, 17) })
    expect(periodRange('12m', now)).toEqual({ from: new Date(2025, 9, 13), to: new Date(2026, 9, 17) })
  })

  test('100% means no run failed', () => {
    const totals = insights().totals
    expect(successRate({ ...totals, finished: 199, failed: 1, interrupted: 0, abandoned: 0 }).percent).toBe(99)
  })

  test('a phone folds more than 30 daily bars into Monday weeks', () => {
    const series = Array.from({ length: 31 }, (_, i) => ({
      start: new Date(Date.UTC(2026, 8, 16 + i)).toISOString().slice(0, 10),
      launched: 1, completed: 1, merged: 0, failed: 0, interrupted: 0, abandoned: 0,
    }))
    const result = insights({ series })
    expect(chartSeries(result, false).series).toHaveLength(31)
    const weekly = chartSeries(result, true)
    expect(weekly.bucket).toBe('week')
    expect(weekly.series[0]).toEqual({ start: '2026-09-14', launched: 5, completed: 5, merged: 0, failed: 0, interrupted: 0, abandoned: 0 })
    expect(weekly.series.reduce((sum, week) => sum + week.launched, 0)).toBe(31)
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
