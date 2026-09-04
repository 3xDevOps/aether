import { act, render, screen } from '@testing-library/react'
import { LaunchSplash } from '@/components/launch-splash'

describe('LaunchSplash', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('shows the Aether night sky and fades away after launch', () => {
    const { container } = render(<LaunchSplash />)
    const splash = container.firstElementChild

    expect(splash?.classList.contains('launch-splash')).toBe(true)
    expect(screen.getByRole('img', { name: 'Aether' })).toBeTruthy()
    expect(screen.getByTestId('launch-splash-stars')).toBeTruthy()
    expect(screen.getByTestId('launch-splash-shooting-stars')).toBeTruthy()
    act(() => vi.advanceTimersByTime(1500))
    expect(splash?.classList.contains('launch-splash--leaving')).toBe(true)

    act(() => vi.advanceTimersByTime(350))
    expect(container.firstElementChild).toBeNull()
  })
})
