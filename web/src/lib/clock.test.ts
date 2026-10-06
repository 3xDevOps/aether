import { renderHook } from '@testing-library/react'
import { useClock } from '@/lib/clock'

describe('useClock', () => {
  afterEach(() => vi.useRealTimers())

  it('reads the current time when the first subscriber arrives after a pause', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-10-06T10:00:00Z'))
    const first = renderHook(() => useClock())
    expect(first.result.current).toBe(Date.parse('2026-10-06T10:00:00Z'))
    first.unmount()

    vi.setSystemTime(new Date('2026-10-06T10:05:00Z'))
    const next = renderHook(() => useClock())
    expect(next.result.current).toBe(Date.parse('2026-10-06T10:05:00Z'))
    next.unmount()
  })
})
