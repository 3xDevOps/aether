import { act, render, screen } from '@testing-library/react'
import { LaunchSplash } from '@/components/launch-splash'
import type { AetherDesktop } from '@/components/shell/title-bar'
import { useStore } from '@/store'

// The bridge is injected by the Electron preload, so a test installs it the
// same way: as a property on the real window.
const shellWindow = window as Window & { aetherDesktop?: AetherDesktop }

// Read at assert time: a reference captured before the timers run points at a
// detached node once the splash unmounts, and every class check on it passes.
function splashClasses(container: HTMLElement): DOMTokenList {
  expect(container.firstElementChild).not.toBeNull()
  return (container.firstElementChild as HTMLElement).classList
}

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

  it('shows the night sky on a shell launch and fades once the store is hydrated', () => {
    shellWindow.aetherDesktop = { platform: 'linux' }

    const { container } = render(<LaunchSplash />)

    expect(splashClasses(container).contains('launch-splash')).toBe(true)
    expect(screen.getByTestId('launch-splash-stars')).toBeTruthy()
    expect(screen.getByTestId('launch-splash-shooting-stars')).toBeTruthy()

    // Hydration alone does not end it: the minimum visible time still runs.
    act(() => {
      useStore.setState({ hydrated: true })
    })
    expect(splashClasses(container).contains('launch-splash--leaving')).toBe(false)

    act(() => vi.advanceTimersByTime(600))
    expect(splashClasses(container).contains('launch-splash--leaving')).toBe(true)

    act(() => vi.advanceTimersByTime(260))
    expect(container.firstElementChild).toBeNull()
  })

  it('never outlives the cap, hydrated or not', () => {
    shellWindow.aetherDesktop = { platform: 'linux' }

    const { container } = render(<LaunchSplash />)

    // Up to the cap it is still there, so this cannot pass against a splash
    // that leaves on a shorter timer of its own.
    act(() => vi.advanceTimersByTime(2499))
    expect(splashClasses(container).contains('launch-splash--leaving')).toBe(false)

    act(() => vi.advanceTimersByTime(1))
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
