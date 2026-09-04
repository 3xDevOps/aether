import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { vi } from 'vitest'
import { api } from '@/lib/api'
import { TerminalDock } from '@/routes/board/terminal-dock'
import { useStore } from '@/store'
import { initialEnvTerminal } from '@/store/env-terminal'

const xterm = vi.hoisted(() => ({
  hostRef: { current: null },
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
vi.mock('@/routes/terminal/attach', () => ({
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
    terminalSocket: vi.fn(() => 'ws://localhost/ws/terminal?tab=main'),
  },
}))


describe('environment terminal dock', () => {
  beforeEach(() => {
    useStore.getState().resetEnvTerminal()
    attach.handlers = null
    vi.clearAllMocks()
    useStore.setState({
      envTerminal: initialEnvTerminal,
      terminalDockHeight: 280,
    })
  })

  it('shows the first-open empty state and opens the main tab', async () => {
    render(<TerminalDock />)

    expect(await screen.findByText('Your environment starts on first open')).toBeDefined()
    fireEvent.click(screen.getByRole('button', { name: 'Open' }))

    await waitFor(() => expect(useStore.getState().envTerminal.activeTab).toBe('main'))
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
})
