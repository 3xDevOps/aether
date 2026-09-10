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
    vi.restoreAllMocks()
    delete shellWindow.aetherDesktop
  })

  it('renders nothing in a browser tab, where there is no shell bridge', () => {
    const { container } = render(<LaunchSplash />)

    expect(container.innerHTML).toBe('')
  })

  it('shows the branded night sky and shooting stars during a shell launch', () => {
    shellWindow.aetherDesktop = { platform: 'linux' }

    const { container } = render(<LaunchSplash />)

    expect(container.firstElementChild).not.toBeNull()
    expect(screen.getByTestId('launch-splash-mark')).toBeTruthy()
    expect(screen.getByTestId('launch-splash-wordmark')).toBeTruthy()
    expect(screen.getByTestId('launch-splash-stars')).toBeTruthy()
    expect(screen.getByTestId('launch-splash-shooting-stars')).toBeTruthy()
    expect(screen.getByTestId('launch-splash-status')).toBeTruthy()

    // Hydration alone does not end it: the minimum visible time still runs.
    act(() => {
      useStore.setState({ hydrated: true })
    })
    expect(container.firstElementChild).not.toBeNull()

    act(() => vi.advanceTimersByTime(600))
    expect(container.firstElementChild).not.toBeNull()

    // The fade is visible for its full duration before unmounting.
    act(() => vi.advanceTimersByTime(259))
    expect(container.firstElementChild).not.toBeNull()
    act(() => vi.advanceTimersByTime(1))
    expect(container.firstElementChild).toBeNull()
  })

  it('never outlives the cap, hydrated or not', () => {
    shellWindow.aetherDesktop = { platform: 'linux' }

    const { container } = render(<LaunchSplash />)

    // Up to the cap it is still there, so this cannot pass against a splash
    // that leaves on a shorter timer of its own.
    act(() => vi.advanceTimersByTime(2499))
    expect(container.firstElementChild).not.toBeNull()

    act(() => vi.advanceTimersByTime(1))
    expect(container.firstElementChild).not.toBeNull()
    act(() => vi.advanceTimersByTime(260))

    expect(useStore.getState().hydrated).toBe(false)
    expect(useStore.getState().hydrationError).toBeNull()
    expect(container.firstElementChild).toBeNull()
  })

  it('gets out of the way when the hydrate failed', () => {
    shellWindow.aetherDesktop = { platform: 'linux' }

    const { container } = render(<LaunchSplash />)

    act(() => {
      useStore.setState({ hydrationError: 'dial tcp: connection refused' })
    })
    act(() => vi.advanceTimersByTime(600))
    act(() => vi.advanceTimersByTime(260))

    expect(container.firstElementChild).toBeNull()
  })

  it('marks the session on first launch, so a later mount renders nothing', () => {
    shellWindow.aetherDesktop = { platform: 'linux' }

    const first = render(<LaunchSplash />)
    expect(first.container.firstElementChild).not.toBeNull()
    expect(window.sessionStorage.getItem('aether.launchSplashShown')).toBe('1')
    first.unmount()

    const again = render(<LaunchSplash />)

    expect(again.container.innerHTML).toBe('')
  })

  it('renders nothing under reduced motion, and still marks the session', () => {
    shellWindow.aetherDesktop = { platform: 'linux' }
    vi.spyOn(window, 'matchMedia').mockImplementation(
      (query: string) => ({ matches: query.includes('prefers-reduced-motion') }) as MediaQueryList,
    )

    const { container } = render(<LaunchSplash />)

    expect(container.innerHTML).toBe('')
    expect(window.sessionStorage.getItem('aether.launchSplashShown')).toBe('1')
  })
})
