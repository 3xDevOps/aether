import { act, render } from '@testing-library/react'
import { StrictMode } from 'react'
import { LaunchSplash } from '@/components/launch-splash'
import type { AetherDesktop } from '@/components/shell/window-bar'
import { useStore } from '@/store'
import logoSource from '../../public/cloudfield-logo-small.svg?raw'

const shellWindow = window as Window & { aetherDesktop?: AetherDesktop }

describe('LaunchSplash', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    window.sessionStorage.clear()
    shellWindow.aetherDesktop = { platform: 'linux' }
    useStore.setState({ hydrated: false, hydrationError: null })
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
    window.sessionStorage.clear()
    delete shellWindow.aetherDesktop
  })

  it('skips browser tabs without consuming a shell launch', () => {
    delete shellWindow.aetherDesktop

    const { container } = render(<LaunchSplash />)

    expect(container.firstElementChild).toBeNull()
    expect(window.sessionStorage.getItem('aether.launchSplashShown')).toBeNull()
  })

  it('holds a fast launch for 600ms in StrictMode, then fades for 260ms', () => {
    useStore.setState({ hydrated: true })
    const { container } = render(<StrictMode><LaunchSplash /></StrictMode>)
    const splash = container.firstElementChild!

    expect(splash).not.toBeNull()
    act(() => vi.advanceTimersByTime(599))
    expect(splash.classList.contains('launch-splash--leaving')).toBe(false)
    act(() => vi.advanceTimersByTime(1))
    expect(splash.classList.contains('launch-splash--leaving')).toBe(true)
    act(() => vi.advanceTimersByTime(259))
    expect(container.firstElementChild).toBe(splash)
    act(() => vi.advanceTimersByTime(1))
    expect(container.firstElementChild).toBeNull()
  })

  it('dismisses an early hydration error only after the minimum hold', () => {
    const { container } = render(<LaunchSplash />)
    const splash = container.firstElementChild!

    act(() => vi.advanceTimersByTime(100))
    act(() => useStore.setState({ hydrationError: 'dial tcp: connection refused' }))
    act(() => vi.advanceTimersByTime(499))
    expect(splash.classList.contains('launch-splash--leaving')).toBe(false)
    act(() => vi.advanceTimersByTime(1))
    expect(splash.classList.contains('launch-splash--leaving')).toBe(true)
    act(() => vi.advanceTimersByTime(260))
    expect(container.firstElementChild).toBeNull()
  })

  it('waits for hydration beyond the minimum instead of leaving on its own', () => {
    const { container } = render(<LaunchSplash />)
    const splash = container.firstElementChild!

    act(() => vi.advanceTimersByTime(1200))
    expect(splash.classList.contains('launch-splash--leaving')).toBe(false)
    act(() => useStore.setState({ hydrated: true }))
    expect(splash.classList.contains('launch-splash--leaving')).toBe(true)
    act(() => vi.advanceTimersByTime(260))
    expect(container.firstElementChild).toBeNull()
  })

  it('releases a hung launch at 2500ms even without hydration or an error', () => {
    const { container } = render(<LaunchSplash />)
    const splash = container.firstElementChild!

    act(() => vi.advanceTimersByTime(2499))
    expect(splash.classList.contains('launch-splash--leaving')).toBe(false)
    act(() => vi.advanceTimersByTime(1))
    expect(splash.classList.contains('launch-splash--leaving')).toBe(true)
    act(() => vi.advanceTimersByTime(259))
    expect(container.firstElementChild).toBe(splash)
    act(() => vi.advanceTimersByTime(1))
    expect(container.firstElementChild).toBeNull()
    expect(useStore.getState().hydrated).toBe(false)
    expect(useStore.getState().hydrationError).toBeNull()
  })

  it('marks the first shell launch so even a reload during the hold skips it', () => {
    const first = render(<LaunchSplash />)
    expect(first.container.firstElementChild).not.toBeNull()
    expect(window.sessionStorage.getItem('aether.launchSplashShown')).toBe('1')
    first.unmount()

    const reload = render(<StrictMode><LaunchSplash /></StrictMode>)
    expect(reload.container.firstElementChild).toBeNull()
  })

  it('skips reduced motion and keeps the session marked after motion is enabled', () => {
    const media = vi.spyOn(window, 'matchMedia').mockImplementation(
      (query: string) => ({ matches: query.includes('prefers-reduced-motion') }) as MediaQueryList,
    )
    const first = render(<LaunchSplash />)

    expect(first.container.firstElementChild).toBeNull()
    expect(window.sessionStorage.getItem('aether.launchSplashShown')).toBe('1')
    first.unmount()
    media.mockRestore()

    const reload = render(<LaunchSplash />)
    expect(reload.container.firstElementChild).toBeNull()
  })

  it('skips safely when sessionStorage access is blocked', () => {
    vi.spyOn(window, 'sessionStorage', 'get').mockImplementation(() => {
      throw new DOMException('Access denied', 'SecurityError')
    })

    const { container } = render(<StrictMode><LaunchSplash /></StrictMode>)
    expect(container.firstElementChild).toBeNull()
  })

  it('finishes the launch when storage can be read but not written', () => {
    const setItem = Storage.prototype.setItem
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(function (this: Storage, key, value) {
      if (this === window.sessionStorage) {
        throw new DOMException('Storage full', 'QuotaExceededError')
      }
      setItem.call(this, key, value)
    })
    const { container } = render(<StrictMode><LaunchSplash /></StrictMode>)
    expect(container.firstElementChild).not.toBeNull()

    act(() => useStore.setState({ hydrated: true }))
    act(() => vi.advanceTimersByTime(600))
    act(() => vi.advanceTimersByTime(260))
    expect(container.firstElementChild).toBeNull()
  })

  it('draws the checker paths unchanged, staggering each ring from its own start', () => {
    const { getByTestId } = render(<LaunchSplash />)
    const lockup = getByTestId('launch-splash-lockup')
    const source = new DOMParser().parseFromString(logoSource, 'image/svg+xml')
    const sourceDashes = [...source.querySelectorAll('path')].filter((path) => path.getAttribute('d')!.split('M').length === 2)

    const dashes = [...lockup.querySelectorAll<SVGPathElement>('.launch-splash__dash')]
    for (const attr of ['d', 'stroke', 'stroke-width', 'stroke-linejoin', 'fill']) {
      expect(dashes.map((dash) => dash.getAttribute(attr))).toEqual(sourceDashes.map((dash) => dash.getAttribute(attr)))
    }
    expect(dashes.map((dash) => dash.style.animationDelay)).toEqual([
      '30ms', '60ms', '90ms', '120ms', '150ms', '180ms', '210ms', '240ms', '270ms',
      '170ms', '200ms', '230ms', '260ms', '290ms', '320ms', '350ms',
    ])
    expect(lockup.querySelectorAll('.launch-splash__dot')).toHaveLength(1)

    const letters = [...lockup.querySelectorAll<SVGGElement>('.launch-splash__letter')]
    expect(letters).toHaveLength('loudfield'.length)
    expect(letters.map((letter) => letter.style.animationDelay)).toEqual(letters.map((_, n) => `${450 + n * 40}ms`))
    expect(letters[5].querySelector('circle')).not.toBeNull()
  })

  it('shows the logo file as a plain image when it cannot be split', async () => {
    vi.resetModules()
    vi.doMock('../../public/cloudfield-logo-small.svg?raw', () => ({ default: '<svg xmlns="http://www.w3.org/2000/svg"/>' }))
    const { LaunchSplash: Fallback } = await import('@/components/launch-splash')
    const { getByTestId } = render(<Fallback />)

    expect(getByTestId('launch-splash-lockup').querySelector('img')?.getAttribute('src')).toBe('/cloudfield-logo-small.svg')
    vi.doUnmock('../../public/cloudfield-logo-small.svg?raw')
  })

  it('sleeps until the wake, then stops drawing the field when it unmounts', () => {
    const context = {
      setTransform: () => {},
      clearRect: vi.fn(),
      beginPath: () => {},
      moveTo: () => {},
      lineTo: () => {},
      stroke: () => {},
    }
    vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue(context as unknown as CanvasRenderingContext2D)
    const { unmount } = render(<LaunchSplash />)

    act(() => vi.advanceTimersByTime(800))
    expect(context.clearRect).toHaveBeenCalledTimes(1)
    act(() => vi.advanceTimersByTime(200))
    const drawn = context.clearRect.mock.calls.length
    expect(drawn).toBeGreaterThan(1)
    unmount()
    act(() => vi.advanceTimersByTime(100))
    expect(context.clearRect.mock.calls.length).toBe(drawn)
  })
})
