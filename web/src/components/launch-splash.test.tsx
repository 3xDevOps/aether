import { act, render, screen } from '@testing-library/react'
import { LaunchSplash } from '@/components/launch-splash'
import type { AetherDesktop } from '@/components/shell/title-bar'
import { useStore } from '@/store'

// The bridge is injected by the Electron preload, so a test installs it the
// same way: as a property on the real window.
const shellWindow = window as Window & { aetherDesktop?: AetherDesktop }

describe('LaunchSplash', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    window.sessionStorage.clear()
    useStore.setState({ hydrated: false, hydrationError: null })
  })

  afterEach(() => {
    vi.useRealTimers()
    delete shellWindow.aetherDesktop
  })

  it('renders nothing in a browser tab, where there is no shell bridge', () => {
    const { container } = render(<LaunchSplash />)

    expect(container.innerHTML).toBe('')
  })

  it('shows the night sky on a shell launch and fades once the store is hydrated', () => {
    shellWindow.aetherDesktop = { platform: 'linux' }

    const { container } = render(<LaunchSplash />)
    const splash = container.firstElementChild

    expect(splash?.classList.contains('launch-splash')).toBe(true)
    expect(container.querySelector('img[src="/aether-mark.png"]')).not.toBeNull()
    expect(screen.getByTestId('launch-splash-stars')).toBeTruthy()
    expect(screen.getByTestId('launch-splash-shooting-stars')).toBeTruthy()

    // Hydration alone does not end it: the minimum visible time still runs.
    act(() => {
      useStore.setState({ hydrated: true })
    })
    expect(splash?.classList.contains('launch-splash--leaving')).toBe(false)

    act(() => vi.advanceTimersByTime(600))
    expect(splash?.classList.contains('launch-splash--leaving')).toBe(true)

    act(() => vi.advanceTimersByTime(250))
    expect(container.firstElementChild).toBeNull()
  })

  it('stays up while the store is still hydrating', () => {
    shellWindow.aetherDesktop = { platform: 'linux' }

    const { container } = render(<LaunchSplash />)
    const splash = container.firstElementChild

    act(() => vi.advanceTimersByTime(5000))

    expect(splash?.classList.contains('launch-splash--leaving')).toBe(false)
  })

  it('gets out of the way when the hydrate failed', () => {
    shellWindow.aetherDesktop = { platform: 'linux' }

    const { container } = render(<LaunchSplash />)

    act(() => {
      useStore.setState({ hydrationError: 'dial tcp: connection refused' })
    })
    act(() => vi.advanceTimersByTime(600))
    act(() => vi.advanceTimersByTime(250))

    expect(container.firstElementChild).toBeNull()
  })

  it('marks the session on first launch and skips the splash on a reload', () => {
    shellWindow.aetherDesktop = { platform: 'linux' }

    const first = render(<LaunchSplash />)
    expect(first.container.firstElementChild).not.toBeNull()
    first.unmount()

    const reload = render(<LaunchSplash />)

    expect(reload.container.innerHTML).toBe('')
  })
})
