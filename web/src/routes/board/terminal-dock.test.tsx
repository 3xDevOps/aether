import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { vi } from 'vitest'
import { api } from '@/lib/api'
import { TerminalDock } from '@/routes/board/terminal-dock'
import type * as attachModule from '@/routes/terminal/attach'
import { useStore } from '@/store'
import { initialEnvTerminal } from '@/store/env-terminal'

const xterm = vi.hoisted(() => ({
  hostRef: () => {},
  terminal: { cols: 80, rows: 24, reset: vi.fn(), write: vi.fn() },
}))

const attach = vi.hoisted(() => ({
  handlers: null as {
    onAttached: (write: boolean) => void
    onRefused: (detail: string) => void
  } | null,
}))

vi.mock('@/components/xterm-host', () => ({
  useXterm: () => xterm,
}))
vi.mock('@/routes/terminal/attach', async (importOriginal) => ({
  ...(await importOriginal<typeof attachModule>()),
  connectAttach: (
    _socketURL: () => string,
    handlers: {
      onAttached: (write: boolean) => void
      onRefused: (detail: string) => void
    },
  ) => {
    attach.handlers = handlers
    return { send: vi.fn(), resize: vi.fn(), reopen: vi.fn(), close: vi.fn() }
  },
}))
vi.mock('@/lib/api', () => ({
  api: {
    terminalStatus: vi.fn(async () => ({ running: false, tabs: [] })),
    terminalStop: vi.fn(async () => ({})),
    envSave: vi.fn(async () => ({ image: 'aether/member-1:123' })),
    envReset: vi.fn(async () => ({})),
    terminalSocket: vi.fn(() => 'ws://localhost/ws/terminal?tab=main'),
  },
}))


describe('environment terminal dock', () => {
  beforeEach(() => {
    useStore.getState().resetEnvTerminal()
    attach.handlers = null
    vi.clearAllMocks()
    useStore.setState({
      // The dock ships collapsed; these cases are about what it shows open.
      envTerminal: { ...initialEnvTerminal, collapsed: false },
      terminalDockHeight: 280,
      capabilities: null,
      paletteDialog: null,
      paletteForwardTarget: null,
    })
  })

  it('shows the first-open empty state and opens the main tab', async () => {
    render(<TerminalDock />)

    expect(await screen.findByText('Your environment starts on first open')).toBeDefined()
    fireEvent.click(screen.getByRole('button', { name: 'Open' }))

    await waitFor(() => expect(useStore.getState().envTerminal.activeTab).toBe('main'))
  })
  it('shows Save environment and the unsaved hint after first open attaches', async () => {
    vi.mocked(api.terminalStatus).mockResolvedValue({ running: false, tabs: [] })
    render(<TerminalDock />)

    fireEvent.click(await screen.findByRole('button', { name: 'Open' }))
    await waitFor(() => expect(attach.handlers).not.toBeNull())

    act(() => attach.handlers?.onAttached(true))

    expect(await screen.findByRole('button', { name: 'Save environment' })).toBeDefined()
    expect(screen.getByText('Installs here reach agents after you save.')).toBeDefined()
  })

  it('opens the environment forward dialog when forwarding is available', async () => {
    vi.mocked(api.terminalStatus).mockResolvedValue({ running: true, tabs: ['main'] })
    useStore.setState({
      capabilities: {
        gateway: 'local',
        methods: [],
        ws: [],
        local: ['forward.start'],
      },
    })
    render(<TerminalDock />)

    fireEvent.click(await screen.findByRole('button', { name: 'Forward port' }))

    expect(useStore.getState().paletteDialog).toBe('forward')
    expect(useStore.getState().paletteForwardTarget).toBe('terminal')
  })

  it('confirms before stopping the running environment', async () => {
    vi.mocked(api.terminalStatus).mockResolvedValue({ running: true, tabs: ['main'] })
    render(<TerminalDock />)

    fireEvent.click(await screen.findByRole('button', { name: 'Stop environment' }))
    expect(screen.getByRole('dialog')).toBeDefined()
    const stopButtons = screen.getAllByRole('button', { name: 'Stop environment' })
    fireEvent.click(stopButtons[stopButtons.length - 1])

    await waitFor(() => expect(api.terminalStop).toHaveBeenCalled())
    expect(useStore.getState().envTerminal.tabs).toEqual([])
    // Whether the dock is open is the member's choice, not part of the
    // environment's state, so stopping must not close it under them.
    expect(useStore.getState().envTerminal.collapsed).toBe(false)
  })
  it('saves the running environment and hides the unsaved hint', async () => {
    vi.mocked(api.terminalStatus).mockResolvedValue({ running: true, tabs: ['main'] })
    const save = Promise.withResolvers<{ image: string }>()
    vi.mocked(api.envSave).mockReturnValue(save.promise)
    render(<TerminalDock />)

    const saveButton = await screen.findByRole('button', { name: 'Save environment' })
    expect(screen.getByText('Installs here reach agents after you save.')).toBeDefined()
    fireEvent.click(saveButton)
    expect((screen.getByRole('button', { name: 'Saving...' }) as HTMLButtonElement).disabled).toBe(true)

    save.resolve({ image: 'aether/member-1:123' })
    await waitFor(() =>
      expect(screen.getByText('Saved - new runs use this environment')).toBeDefined(),
    )
    expect(screen.queryByText('Installs here reach agents after you save.')).toBeNull()
    expect(api.envSave).toHaveBeenCalledTimes(1)
  })

  it('surfaces save errors in the dock status', async () => {
    vi.mocked(api.terminalStatus).mockResolvedValue({ running: true, tabs: ['main'] })
    vi.mocked(api.envSave).mockRejectedValue(new Error('could not save'))
    render(<TerminalDock />)

    fireEvent.click(await screen.findByRole('button', { name: 'Save environment' }))
    expect(await screen.findByText('could not save')).toBeDefined()
  })

  it('resets the environment from the stop dialog and clears tabs', async () => {
    vi.mocked(api.terminalStatus).mockResolvedValue({ running: true, tabs: ['main'] })
    render(<TerminalDock />)

    fireEvent.click(await screen.findByRole('button', { name: 'Stop environment' }))
    expect(screen.getByRole('button', { name: 'Reset to standard' })).toBeDefined()
    fireEvent.click(screen.getByRole('button', { name: 'Reset to standard' }))

    await waitFor(() => expect(api.envReset).toHaveBeenCalledTimes(1))
    expect(useStore.getState().envTerminal.tabs).toEqual([])
    expect(useStore.getState().envTerminal.collapsed).toBe(false)
    expect(useStore.getState().envTerminal.status).toEqual({
      running: false,
      tabs: [],
      saved_image: '',
    })
  })

  it('does not show the unsaved hint when an image is saved', async () => {
    vi.mocked(api.terminalStatus).mockResolvedValue({
      running: true,
      tabs: ['main'],
      saved_image: 'aether/member-1:123',
    })
    render(<TerminalDock />)

    await screen.findByRole('button', { name: 'Save environment' })
    expect(screen.queryByText('Installs here reach agents after you save.')).toBeNull()
  })

  it('shows the starting indicator until the terminal attaches', async () => {
    vi.mocked(api.terminalStatus).mockResolvedValue({ running: false, tabs: [] })
    render(<TerminalDock />)
    fireEvent.click(await screen.findByRole('button', { name: 'Open' }))

    expect(await screen.findByRole('status')).toBeDefined()
    expect(screen.getByText('Starting your environment container')).toBeDefined()
    await waitFor(() => expect(attach.handlers).not.toBeNull())

    act(() => attach.handlers?.onAttached(true))

    await waitFor(() => expect(screen.queryByRole('status')).toBeNull())
    expect(screen.queryByText('Starting your environment container')).toBeNull()
  })

  it('does not claim a container start when reattaching to a running one', async () => {
    vi.mocked(api.terminalStatus).mockResolvedValue({ running: false, tabs: [] })
    render(<TerminalDock />)
    fireEvent.click(await screen.findByRole('button', { name: 'Open' }))
    await waitFor(() => expect(attach.handlers).not.toBeNull())
    act(() => attach.handlers?.onAttached(true))
    await waitFor(() => expect(screen.queryByRole('status')).toBeNull())

    // A second tab runs another shell in the container that is already up.
    fireEvent.click(screen.getByRole('button', { name: 'Add terminal tab' }))
    expect(await screen.findByText('Connecting to your environment')).toBeDefined()
    expect(screen.queryByText('Starting your environment container')).toBeNull()
    act(() => attach.handlers?.onAttached(true))
    await waitFor(() => expect(screen.queryByRole('status')).toBeNull())

    // And neither does switching back to the first tab.
    fireEvent.click(screen.getByRole('tab', { name: 'main' }))
    expect(await screen.findByText('Connecting to your environment')).toBeDefined()
    expect(screen.queryByText('Starting your environment container')).toBeNull()
  })

  it('replaces the starting indicator with the real start failure', async () => {
    vi.mocked(api.terminalStatus).mockResolvedValue({ running: false, tabs: [] })
    render(<TerminalDock />)
    fireEvent.click(await screen.findByRole('button', { name: 'Open' }))

    expect(
      await screen.findByText('Starting your environment container'),
    ).toBeDefined()
    await waitFor(() => expect(attach.handlers).not.toBeNull())
    act(() => attach.handlers?.onRefused('start environment: no space left on device'))

    expect(
      await screen.findByText('start environment: no space left on device'),
    ).toBeDefined()
    expect(screen.queryByText('Starting your environment container')).toBeNull()
  })

  it('shows an attach refusal with open tabs and clears it after attaching', async () => {
    vi.mocked(api.terminalStatus).mockResolvedValue({ running: true, tabs: ['main'] })
    render(<TerminalDock />)

    await waitFor(() => expect(attach.handlers).not.toBeNull())
    act(() => attach.handlers?.onRefused('membership withdrawn'))
    expect(screen.getByText('membership withdrawn')).toBeDefined()

    act(() => attach.handlers?.onAttached(true))
    await waitFor(() => expect(screen.queryByText('membership withdrawn')).toBeNull())
  })

  it('starts collapsed and opens from the header toggle', async () => {
    vi.mocked(api.terminalStatus).mockResolvedValue({ running: false, tabs: [] })
    useStore.setState({ envTerminal: initialEnvTerminal })
    render(<TerminalDock />)

    expect(screen.queryByText('Your environment starts on first open')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Expand terminal dock' }))

    expect(await screen.findByText('Your environment starts on first open')).toBeDefined()
  })

  it('opens itself for a caller that mounts it open, and still closes', async () => {
    vi.mocked(api.terminalStatus).mockResolvedValue({ running: true, tabs: ['main'] })
    useStore.setState({ envTerminal: initialEnvTerminal })
    render(<TerminalDock openOnMount />)

    await waitFor(() => expect(useStore.getState().envTerminal.collapsed).toBe(false))

    fireEvent.click(screen.getByRole('button', { name: 'Collapse terminal dock' }))
    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Expand terminal dock' })).toBeDefined(),
    )
    expect(useStore.getState().envTerminal.collapsed).toBe(true)
  })

  it('names the real tab ceiling when every environment tab is open', async () => {
    vi.mocked(api.terminalStatus).mockResolvedValue({ running: true, tabs: ['main'] })
    render(<TerminalDock />)

    const add = await screen.findByRole('button', { name: 'Add terminal tab' })
    for (let n = 0; n < 5; n++) fireEvent.click(add)
    await waitFor(() => expect(useStore.getState().envTerminal.tabs).toHaveLength(6))

    expect(screen.getByText('At most 6 tabs')).toBeDefined()
    expect((add as HTMLButtonElement).disabled).toBe(true)
  })
})
