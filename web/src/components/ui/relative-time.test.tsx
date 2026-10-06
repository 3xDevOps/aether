import { act, render, screen } from '@testing-library/react'
import { RelativeTime } from '@/components/ui/relative-time'

describe('RelativeTime', () => {
  it('advances on the shared clock without its parent re-rendering', () => {
    vi.useFakeTimers({ now: new Date('2026-08-14T11:01:00Z') })
    try {
      let parentRenders = 0
      function Row() {
        parentRenders++
        return <RelativeTime at="2026-08-14T11:00:00Z" />
      }
      render(<Row />)
      const time = screen.getByText('1 minute ago')
      expect(time.getAttribute('dateTime')).toBe('2026-08-14T11:00:00Z')

      act(() => {
        vi.advanceTimersByTime(60_000)
      })
      expect(screen.getByText('2 minutes ago')).toBe(time)
      expect(parentRenders).toBe(1)
    } finally {
      vi.useRealTimers()
    }
  })
})
