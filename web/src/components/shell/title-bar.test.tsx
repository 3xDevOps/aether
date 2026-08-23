import { fireEvent, render, screen } from '@testing-library/react'
import type { Mock } from 'vitest'
import type { AetherDesktop, DesktopControls } from '@/components/shell/title-bar'
import { TitleBar } from '@/components/shell/title-bar'

// The bridge is injected by the Electron preload, so a test installs it the
// same way: as a property on the real window.
const shellWindow = window as Window & { aetherDesktop?: AetherDesktop }

/** The unsubscribe the stubbed bridge hands back, so a test can assert on it. */
let unsubscribe: Mock

function stubControls(maximized = false): DesktopControls {
  return {
    minimize: vi.fn(),
    toggleMaximize: vi.fn(),
    close: vi.fn(),
    isMaximized: vi.fn(async () => maximized),
    onMaximizedChange: vi.fn(() => unsubscribe),
  }
}

beforeEach(() => {
  unsubscribe = vi.fn()
})

afterEach(() => {
  delete shellWindow.aetherDesktop
})

describe('TitleBar', () => {
  it('renders nothing in a browser tab, where there is no shell bridge', () => {
    const { container } = render(<TitleBar />)

    expect(container.innerHTML).toBe('')
  })

  it('renders the Aether lockup when the shell bridge is present', () => {
    shellWindow.aetherDesktop = { platform: 'linux', controls: stubControls() }

    render(<TitleBar />)

    expect(screen.getByText('aether')).toBeDefined()
    const mark = document.querySelector('img[src="/aether-mark.png"]')
    expect(mark).not.toBeNull()
    // Decorative: the bar carries the accessible name, the lockup does not.
    expect(mark?.getAttribute('alt')).toBe('')
    expect(screen.getByLabelText('Aether')).toBeDefined()
  })

  it('drives the bridge from the window buttons', async () => {
    const controls = stubControls()
    shellWindow.aetherDesktop = { platform: 'win32', controls }

    render(<TitleBar />)
    await screen.findByRole('button', { name: 'Maximize' })

    fireEvent.click(screen.getByRole('button', { name: 'Minimize' }))
    fireEvent.click(screen.getByRole('button', { name: 'Maximize' }))
    fireEvent.click(screen.getByRole('button', { name: 'Close' }))

    expect(controls.minimize).toHaveBeenCalledTimes(1)
    expect(controls.toggleMaximize).toHaveBeenCalledTimes(1)
    expect(controls.close).toHaveBeenCalledTimes(1)
  })

  it('names the maximize button Restore while the window is maximized', async () => {
    shellWindow.aetherDesktop = { platform: 'linux', controls: stubControls(true) }

    render(<TitleBar />)

    expect(await screen.findByRole('button', { name: 'Restore' })).toBeDefined()
    expect(screen.queryByRole('button', { name: 'Maximize' })).toBeNull()
  })

  it('follows a maximize change reported by the shell', async () => {
    const controls = stubControls()
    shellWindow.aetherDesktop = { platform: 'linux', controls }

    render(<TitleBar />)
    await screen.findByRole('button', { name: 'Maximize' })

    const [report] = vi.mocked(controls.onMaximizedChange).mock.calls[0]
    report(true)

    expect(await screen.findByRole('button', { name: 'Restore' })).toBeDefined()
  })

  it('draws no window buttons on darwin and reserves the traffic lights', () => {
    shellWindow.aetherDesktop = { platform: 'darwin' }

    render(<TitleBar />)

    expect(screen.queryByRole('button')).toBeNull()
    expect(screen.getByLabelText('Aether').style.paddingInlineStart).toBe('78px')
  })

  it('unsubscribes from the shell on unmount', async () => {
    const controls = stubControls()
    shellWindow.aetherDesktop = { platform: 'linux', controls }

    const { unmount } = render(<TitleBar />)
    await screen.findByRole('button', { name: 'Maximize' })
    unmount()

    expect(unsubscribe).toHaveBeenCalledTimes(1)
  })
})
