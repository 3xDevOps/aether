import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { vi } from 'vitest'
import { api } from '@/lib/api'
import { TerminalDock } from '@/routes/board/terminal-dock'
import type { AttachHandlers } from '@/routes/terminal/attach'
import type * as attachModule from '@/routes/terminal/attach'
import { initialEnvTerminal } from '@/store/env-terminal'
import { useStore } from '@/store'
import type * as apiModule from '@/lib/api'

const xterm = vi.hoisted(() => ({
  hostRef: () => {},
  terminal: { cols: 80, rows: 24, reset: vi.fn(), write: vi.fn(), focus: vi.fn() },
  ready: true,
  search: null,
  findOpen: false,
  setFindOpen: vi.fn(),
  focusTerminal: vi.fn(),
}))

const attach = vi.hoisted(() => ({
  handlers: null as AttachHandlers | null,
}))

vi.mock('@/components/xterm-host', () => ({
  // A disabled hook has no terminal, which is what a collapsed dock gets and
  // what stops its attach effect from running.
  useXterm: (options?: { enabled?: boolean }) =>
    options?.enabled === false ? { ...xterm, terminal: null, ready: false } : xterm,
}))
vi.mock('@/routes/terminal/attach', async (importOriginal) => ({
  ...(await importOriginal<typeof attachModule>()),
  connectAttach: (
    _socketURL: () => string,
    handlers: AttachHandlers,
  ) => {
    attach.handlers = handlers
    return {
      send: vi.fn(),
      resize: vi.fn(),
      reopen: vi.fn(),
      rebind: (next: AttachHandlers) => {
        attach.handlers = next
      },
      close: vi.fn(),
    }
  },
}))
vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof apiModule>()
  return {
    ...actual,
    api: {
      ...actual.api,
      terminalStatus: vi.fn(async () => ({ running: false, tabs: [] })),
      terminalStop: vi.fn(async () => ({})),
      envSave: vi.fn(async () => ({ image: 'aether/member-1:123' })),
      envReset: vi.fn(async () => ({})),
      terminalSocket: vi.fn(() => 'ws://localhost/ws/terminal?tab=main'),
    },
  }
})


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
  it('rebinds a persistent main socket after the dock remounts', async () => {
    vi.mocked(api.terminalStatus).mockResolvedValue({ running: true, tabs: ['main'] })
    const first = render(<TerminalDock />)
    await waitFor(() => expect(attach.handlers).not.toBeNull())
    first.unmount()

    render(<TerminalDock />)
    await waitFor(() => expect(attach.handlers).not.toBeNull())
    act(() => attach.handlers?.onAttached(true))

    await waitFor(() => expect(screen.queryByRole('status')).toBeNull())
  })
  it('ignores late callbacks from a prior tab after switching terminals', async () => {
    vi.mocked(api.terminalStatus).mockResolvedValue({ running: true, tabs: ['main'] })
    render(<TerminalDock />)
    await waitFor(() => expect(attach.handlers).not.toBeNull())
    const oldHandlers = attach.handlers
    act(() => oldHandlers?.onAttached(true))

    xterm.terminal.write.mockClear()
    fireEvent.click(screen.getByRole('button', { name: 'Add terminal tab' }))
    await waitFor(() => {
      expect(useStore.getState().envTerminal.activeTab).toBe('t2')
      expect(attach.handlers).not.toBe(oldHandlers)
    })
    const currentHandlers = attach.handlers
    act(() => currentHandlers?.onAttached(true))
    await waitFor(() => expect(screen.queryByRole('status')).toBeNull())

    const currentOutput = new TextEncoder().encode('current output')
    const oldOutput = new TextEncoder().encode('old output')
    act(() => {
      currentHandlers?.onData?.(currentOutput, 'live')
      oldHandlers?.onData?.(oldOutput, 'live')
      oldHandlers?.onState('offline')
      oldHandlers?.onExit?.()
    })

    expect(useStore.getState().envTerminal.activeTab).toBe('t2')
    expect(screen.queryByRole('status')).toBeNull()
    const writes = xterm.terminal.write.mock.calls.map(([chunk]) => chunk)
    expect(writes).toEqual([currentOutput])
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
    expect(screen.getByRole('alertdialog')).toBeDefined()
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

  // Stopping the container and throwing the saved image away are different
  // decisions, so the stop dialog offers stopping alone.
  it('keeps discarding the saved image out of the stop dialog', async () => {
    vi.mocked(api.terminalStatus).mockResolvedValue({
      running: true,
      tabs: ['main'],
      saved_image: 'aether/member-1:123',
    })
    render(<TerminalDock />)

    fireEvent.click(await screen.findByRole('button', { name: 'Stop environment' }))
    const dialog = within(screen.getByRole('alertdialog'))
    expect(dialog.queryByRole('button', { name: 'Reset to standard' })).toBeNull()
  })

  it('offers no environment actions before the first open', async () => {
    vi.mocked(api.terminalStatus).mockResolvedValue({ running: false, tabs: [] })
    render(<TerminalDock />)

    await screen.findByText('Your environment starts on first open')
    expect(screen.queryByRole('button', { name: 'Save environment' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Stop environment' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Reset to standard' })).toBeNull()
  })

  it('does not offer a reset when no image is saved', async () => {
    vi.mocked(api.terminalStatus).mockResolvedValue({ running: true, tabs: ['main'] })
    render(<TerminalDock />)

    await screen.findByRole('button', { name: 'Stop environment' })
    expect(screen.queryByRole('button', { name: 'Reset to standard' })).toBeNull()
  })

  it('keeps the saved image after a stop, and promises no second stop', async () => {
    vi.mocked(api.terminalStatus).mockResolvedValue({
      running: true,
      tabs: ['main'],
      saved_image: 'aether/member-1:123',
    })
    render(<TerminalDock />)

    fireEvent.click(await screen.findByRole('button', { name: 'Stop environment' }))
    const dialog = within(screen.getByRole('alertdialog'))
    fireEvent.click(dialog.getByRole('button', { name: 'Stop environment' }))
    await waitFor(() => expect(api.terminalStop).toHaveBeenCalled())
    expect(useStore.getState().envTerminal.status?.saved_image).toBe('aether/member-1:123')

    // Throwing the image away must not mean starting the container it deletes.
    fireEvent.click(await screen.findByRole('button', { name: 'Reset to standard' }))
    expect(screen.queryByRole('button', { name: 'Stop environment' })).toBeNull()

    // The container is already stopped, so the confirmation must not promise
    // a stop that will not happen.
    const resetDialog = within(screen.getByRole('alertdialog'))
    expect(
      resetDialog.getByText(/aether\/member-1:123 is deleted\. Your home files remain/),
    ).toBeDefined()
    expect(resetDialog.queryByText(/environment container stops/)).toBeNull()
  })

  it('keeps the stop dialog open while the stop is in flight', async () => {
    vi.mocked(api.terminalStatus).mockResolvedValue({ running: true, tabs: ['main'] })
    const stop = Promise.withResolvers<never>()
    vi.mocked(api.terminalStop).mockReturnValue(stop.promise)
    render(<TerminalDock />)

    fireEvent.click(await screen.findByRole('button', { name: 'Stop environment' }))
    const dialog = within(screen.getByRole('alertdialog'))
    fireEvent.click(dialog.getByRole('button', { name: 'Stop environment' }))
    await screen.findByRole('button', { name: 'Stopping...' })

    fireEvent.keyDown(document, { key: 'Escape' })
    expect(screen.getByRole('alertdialog')).toBeDefined()

    stop.reject(new Error('stop container: daemon is down'))
    expect(await dialog.findByText('stop container: daemon is down')).toBeDefined()
  })

  it('keeps the saved image when the shell exits on its own', async () => {
    vi.mocked(api.terminalStatus).mockResolvedValue({
      running: true,
      tabs: ['main'],
      saved_image: 'aether/member-1:123',
    })
    render(<TerminalDock />)
    await waitFor(() => expect(attach.handlers).not.toBeNull())
    act(() => attach.handlers?.onAttached(true))

    act(() => attach.handlers?.onExit?.())

    expect(useStore.getState().envTerminal.status).toEqual({
      running: false,
      tabs: [],
      saved_image: 'aether/member-1:123',
    })
    expect(await screen.findByRole('button', { name: 'Reset to standard' })).toBeDefined()
  })

  it('shows a failed stop inside the stop dialog', async () => {
    vi.mocked(api.terminalStatus).mockResolvedValue({ running: true, tabs: ['main'] })
    vi.mocked(api.terminalStop).mockRejectedValueOnce(new Error('stop container: daemon is down'))
    render(<TerminalDock />)

    fireEvent.click(await screen.findByRole('button', { name: 'Stop environment' }))
    const dialog = within(screen.getByRole('alertdialog'))
    fireEvent.click(dialog.getByRole('button', { name: 'Stop environment' }))

    expect(await dialog.findByText('stop container: daemon is down')).toBeDefined()
    expect(useStore.getState().envTerminal.statusError).toBeNull()
  })

  it('shows a failed reset inside the reset dialog', async () => {
    vi.mocked(api.terminalStatus).mockResolvedValue({
      running: true,
      tabs: ['main'],
      saved_image: 'aether/member-1:123',
    })
    vi.mocked(api.envReset).mockRejectedValueOnce(new Error('remove image: image is in use'))
    render(<TerminalDock />)

    fireEvent.click(await screen.findByRole('button', { name: 'Reset to standard' }))
    const dialog = within(screen.getByRole('alertdialog'))
    fireEvent.click(dialog.getByRole('button', { name: 'Reset to standard' }))

    expect(await dialog.findByText('remove image: image is in use')).toBeDefined()
    expect(useStore.getState().envTerminal.statusError).toBeNull()
  })

  it('names the saved image in its own reset confirmation', async () => {
    vi.mocked(api.terminalStatus).mockResolvedValue({
      running: true,
      tabs: ['main'],
      saved_image: 'aether/member-1:123',
    })
    render(<TerminalDock />)

    fireEvent.click(await screen.findByRole('button', { name: 'Reset to standard' }))
    const dialog = within(screen.getByRole('alertdialog'))
    expect(
      dialog.getByText(/aether\/member-1:123 is deleted and the environment container stops\./),
    ).toBeDefined()
    expect(dialog.queryByRole('button', { name: 'Stop environment' })).toBeNull()
    fireEvent.click(dialog.getByRole('button', { name: 'Reset to standard' }))

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

  it('opens the dock when a collapsed strip is asked for a tab', async () => {
    vi.mocked(api.terminalStatus).mockResolvedValue({ running: true, tabs: ['main'] })
    useStore.setState({ envTerminal: initialEnvTerminal })
    render(<TerminalDock />)

    // The strip's controls stay live while the dock is shut, and a tab with
    // no mounted terminal never attaches, so + has to open the dock too.
    fireEvent.click(await screen.findByRole('button', { name: 'Add terminal tab' }))

    await waitFor(() => expect(useStore.getState().envTerminal.collapsed).toBe(false))
    expect(useStore.getState().envTerminal.tabs).toContain('main')
  })

  it('opens the dock when a collapsed strip tab is picked', async () => {
    vi.mocked(api.terminalStatus).mockResolvedValue({ running: true, tabs: ['main'] })
    render(<TerminalDock />)
    await waitFor(() => expect(useStore.getState().envTerminal.tabs).toContain('main'))
    useStore.getState().setEnvTerminalCollapsed(true)

    fireEvent.click(screen.getByRole('tab', { name: 'main' }))

    await waitFor(() => expect(useStore.getState().envTerminal.collapsed).toBe(false))
  })

  it('closes the find bar when the tab under it changes', async () => {
    vi.mocked(api.terminalStatus).mockResolvedValue({ running: true, tabs: ['main'] })
    render(<TerminalDock />)
    await waitFor(() => expect(useStore.getState().envTerminal.tabs).toContain('main'))
    xterm.setFindOpen.mockClear()

    // The query and its answer belong to the buffer find was opened over.
    fireEvent.click(screen.getByRole('button', { name: 'Add terminal tab' }))

    await waitFor(() => expect(xterm.setFindOpen).toHaveBeenCalledWith(false))
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
