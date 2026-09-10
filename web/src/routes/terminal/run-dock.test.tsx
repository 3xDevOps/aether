import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { lookupRoute } from '@/routes/registry'
import '@/routes/terminal'
import type { RunStatus } from '@/lib/types'
import { useStore } from '@/store'
import {
  initialRunShellDock,
  type RunShellDockState,
  unregisterShellSocket,
} from '@/store/terminal'
import { run } from '@/test/fixtures'
import { StubSocket } from '@/test/stub-socket'

vi.mock('@/lib/api', async () => {
  const { fakeApi } = await import('@/test/fixtures')
  return {
    api: {
      ...fakeApi(),
      attachShellSocket: (runID: string, tab: string) =>
        `ws://localhost/ws/attach/${encodeURIComponent(runID)}?shell=${encodeURIComponent(tab)}`,
    },
    API_BASE: '/api/v1',
    ApiError: Error,
  }
})

class NoResizeObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
}

function mount({
  dock = {},
  status = 'running',
}: { dock?: Partial<RunShellDockState>; status?: RunStatus } = {}) {
  const View = lookupRoute('terminal')
  if (!View) throw new Error('terminal route not registered')
  useStore.getState().upsertRun(run({ status }))
  useStore.setState({
    terminals: {},
    pausedRuns: { run_1: false },
    // The dock ships collapsed; these cases are about what it shows open.
    shellDocks: { run_1: { ...initialRunShellDock, collapsed: false, ...dock } },
  })
  return render(<View params={{ runId: 'run_1' }} />)
}
beforeEach(() => {
  for (const tab of ['t1', 't2', 't3', 't4']) unregisterShellSocket('run_1', tab)
  StubSocket.install()
  vi.stubGlobal('ResizeObserver', NoResizeObserver)
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('run-shell dock', () => {
  it('opens a forced writable shell tab for a stalled live run', async () => {
    const view = mount({ status: 'needs-attention' })

    fireEvent.click(screen.getByRole('button', { name: 'Open shell' }))

    const socket = StubSocket.opened[1]
    expect(socket.url).toBe('ws://localhost/ws/attach/run_1?shell=t1')
    act(() => socket.onopen?.())
    expect(socket.frames()[0]).toMatchObject({ write: true })
    view.unmount()
  })

  it('shows the fixed refusal sentence and does not reconnect on denied shells', async () => {
    const view = mount()
    fireEvent.click(screen.getByRole('button', { name: 'Open shell' }))
    await waitFor(() => expect(StubSocket.opened.length).toBeGreaterThanOrEqual(2))

    const socket = StubSocket.opened[1]
    act(() => {
      socket.onopen?.()
      socket.onmessage?.({
        data: JSON.stringify({ ok: false, code: -32001, error: 'permission denied' }),
      })
    })

    const refusal = screen.getByText('You can view this run but not open a shell in it')
    expect(refusal).toBeDefined()
    // The refusal disposes the terminal that had the keyboard. Left on
    // <body>, the reader's next keystroke would reach the shell's shortcuts
    // and leave the run.
    expect(document.activeElement).toBe(refusal)
    await waitFor(
      () => expect(StubSocket.opened).toHaveLength(2),
      { timeout: 100 },
    )
    view.unmount()
  })

  it('removes a tab when the shell socket closes normally', async () => {
    const view = mount()
    fireEvent.click(screen.getByRole('button', { name: 'Open shell' }))
    await waitFor(() => expect(StubSocket.opened.length).toBeGreaterThanOrEqual(2))
    act(() => StubSocket.opened[1].onclose?.({ code: 1000 }))

    expect(useStore.getState().shellDocks.run_1.tabs).toEqual([])
    // The last shell exiting disposes the terminal that had the keyboard, so
    // the body it leaves behind takes it rather than <body>.
    expect(document.activeElement).toBe(
      screen.getByRole('button', { name: 'Open shell' }).parentElement,
    )
    view.unmount()
  }, 20_000)

  it('waits for pause state instead of offering a rejected shell', () => {
    const view = mount({ status: 'needs-attention' })
    act(() => useStore.setState({ pausedRuns: {} }))

    expect(screen.queryByRole('button', { name: 'Open shell' })).toBeNull()
    expect(screen.getByText('Run shell unavailable: waiting for the run pause state.')).toBeDefined()
    view.unmount()
  })

  it('does not offer shell tabs after a completed run loses its container', () => {
    const view = mount({ status: 'completed' })

    expect(screen.queryByRole('button', { name: 'Open shell' })).toBeNull()
    expect(screen.getByText(/Run shell unavailable/)).toBeDefined()
    view.unmount()
  })
})
