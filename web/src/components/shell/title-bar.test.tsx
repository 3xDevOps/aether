import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import type { Mock } from 'vitest'
import type { AetherDesktop, DesktopControls } from '@/components/shell/title-bar'
import { TitleBar } from '@/components/shell/title-bar'
import { PaletteDialogs } from '@/components/palette/dialogs'
import { api } from '@/lib/api'
import { useStore } from '@/store'
import { hydrate } from '@/store/sync'
import { agentInfo, bob, otherWorkspace, serverInfo, vera } from '@/test/fixtures'
import { atViewport } from '@/test/viewport'

// Vitest hoists this factory before static fixture bindings are initialized.
vi.mock('@/lib/api', async () => {
  const { fakeApi } = await import('@/test/fixtures')
  return { api: fakeApi(), API_BASE: '/api/v1', ApiError: Error }
})

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

beforeEach(async () => {
  unsubscribe = vi.fn()
  useStore.setState({
    activeWorkspace: '',
    sidebarDrawerOpen: false,
    paletteDialog: null,
    paletteOpen: false,
    route: { name: 'board', params: {} },
    lastHarnessByAccount: {},
  })
  await hydrate(useStore, api)
  vi.mocked(api.agentList).mockResolvedValue([agentInfo()])
  vi.clearAllMocks()
})

afterEach(() => {
  delete shellWindow.aetherDesktop
})

describe('TitleBar', () => {
  it('renders the command bar in a browser without window controls', () => {
    render(<TitleBar />)

    expect(screen.getByLabelText('Aether')).toBeDefined()
    expect(screen.getByRole('button', { name: 'Search runs and commands' })).toBeDefined()
    expect(screen.queryByRole('button', { name: 'Minimize' })).toBeNull()
  })

  it('cannot open shell overlays while the connection surface replaces the shell', () => {
    atViewport(390, { pointer: 'coarse' })
    useStore.setState({
      capabilities: { gateway: 'local', methods: ['*'], ws: [] },
      info: { ...serverInfo, member: bob },
    })
    render(<TitleBar commandPaletteDisabled />)
    const search = screen.getByRole('button', { name: 'Search runs and commands' })
    const drawer = screen.getByRole('button', { name: 'Expand sidebar' })
    expect(search).toHaveProperty('disabled', true)
    expect(drawer).toHaveProperty('disabled', true)
    expect(screen.queryByRole('button', { name: 'New run' })).toBeNull()
    fireEvent.click(search)
    fireEvent.click(drawer)
    expect(useStore.getState().sidebarDrawerOpen).toBe(false)
    expect(useStore.getState().paletteDialog).toBeNull()
    expect(useStore.getState().paletteOpen).toBe(false)
  })

  it('launches in the selected workspace rather than the focused run workspace', async () => {
    useStore.getState().setActiveWorkspace(otherWorkspace.id)
    useStore.setState({ route: { name: 'terminal', params: { runId: 'run_1' } } })
    render(<><TitleBar /><PaletteDialogs /></>)
    fireEvent.click(screen.getByRole('button', { name: 'New run' }))
    expect(await screen.findByRole('dialog', { name: 'Launch a run' })).toBeDefined()
    const launch = screen.getByRole('button', { name: 'Launch' })
    await waitFor(() => expect(launch).toHaveProperty('disabled', false))
    fireEvent.click(launch)
    await waitFor(() => expect(api.runLaunch).toHaveBeenCalledWith(expect.objectContaining({
      workspace_id: otherWorkspace.id,
    })))
  })

  it('offers launch to an authorized member and withdraws it when permission changes', () => {
    useStore.setState({ info: { ...serverInfo, member: bob } })
    render(<TitleBar />)
    fireEvent.click(screen.getByRole('button', { name: 'New run' }))
    expect(useStore.getState().paletteDialog).toBe('launch')

    act(() => useStore.setState({ info: { ...serverInfo, member: vera } }))
    expect(screen.queryByRole('button', { name: 'New run' })).toBeNull()
    act(() => useStore.setState({
      info: { ...serverInfo, member: bob },
      capabilities: { gateway: 'remote', methods: ['run.list'], ws: [] },
    }))
    expect(screen.queryByRole('button', { name: 'New run' })).toBeNull()
  })

  it('keeps desktop window controls after the launch action in keyboard order', async () => {
    shellWindow.aetherDesktop = { platform: 'linux', controls: stubControls() }
    render(<TitleBar />)
    await screen.findByRole('button', { name: 'Maximize' })
    const buttons = screen.getAllByRole('button')
    expect(buttons.slice(-4).map((button) => button.getAttribute('aria-label') ?? button.textContent))
      .toEqual(['New run', 'Minimize', 'Maximize', 'Close'])
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

  it('draws the command bar without native controls on darwin', () => {
    shellWindow.aetherDesktop = { platform: 'darwin' }

    render(<TitleBar />)

    expect(screen.getByRole('button', { name: 'Search runs and commands' })).toBeDefined()
    expect(screen.queryByRole('button', { name: 'Minimize' })).toBeNull()
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
