import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { lookupRoute } from '@/routes/registry'
import '@/routes/terminal'
import type { RunStatus } from '@/lib/types'
import type * as apiModule from '@/lib/api'
import { useStore } from '@/store'
import {
  initialRunShellDock,
  type RunShellDockState,
  unregisterShellSocket,
} from '@/store/terminal'
import { run } from '@/test/fixtures'
import { StubSocket } from '@/test/stub-socket'

vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof apiModule>()
  const { fakeApi } = await import('@/test/fixtures')
  return {
    ...actual,
    api: {
      ...fakeApi(),
      attachShellSocket: (runID: string, tab: string) =>
        `ws://localhost/ws/attach/${encodeURIComponent(runID)}?shell=${encodeURIComponent(tab)}`,
    },
  }
})

function mount({
  runID = 'run_1',
  dock = {},
  status = 'running',
}: { runID?: string; dock?: Partial<RunShellDockState>; status?: RunStatus } = {}) {
  const View = lookupRoute('terminal')
  if (!View) throw new Error('terminal route not registered')
  useStore.getState().upsertRun(run({ id: runID, status }))
  useStore.setState({
    terminals: {},
    pausedRuns: { [runID]: false },
    // The dock ships collapsed; these cases are about what it shows open.
    shellDocks: { [runID]: { ...initialRunShellDock, collapsed: false, ...dock } },
  })
  return render(<View params={{ runId: runID }} />)
}
beforeEach(() => {
  for (const runID of ['run_1', 'run_2']) {
    for (const tab of ['t1', 't2', 't3', 't4']) unregisterShellSocket(runID, tab)
  }
  StubSocket.install()
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
  it('rebinds a persistent shell after the terminal route remounts', async () => {
    const first = mount()
    fireEvent.click(screen.getByRole('button', { name: 'Open shell' }))
    await waitFor(() => expect(StubSocket.last().url).toContain('?shell=t1'))
    const shell = StubSocket.last()
    act(() => {
      shell.onopen?.()
      shell.onmessage?.({ data: JSON.stringify({ ok: true, replay: 0 }) })
    })
    first.unmount()

    const second = mount({
      dock: { tabs: ['t1'], activeTab: 't1', collapsed: false },
    })
    const shellsBeforeReopen = () =>
      StubSocket.opened.filter((socket) => socket.url.includes('?shell=t1'))
    await waitFor(() => expect(shellsBeforeReopen()).toHaveLength(2))
    const reopened = shellsBeforeReopen()[1]
    act(() => {
      reopened.onopen?.()
      reopened.onmessage?.({ data: JSON.stringify({ ok: true, replay: 0 }) })
      reopened.onmessage?.({ data: new TextEncoder().encode('remounted shell').buffer })
    })

    await vi.waitFor(() =>
      expect(
        [...document.querySelectorAll('.xterm-rows')].some((rows) =>
          rows.textContent?.includes('remounted shell'),
        ),
      ).toBe(true),
    )
    second.unmount()
  })
  it('ignores late callbacks from a prior run sharing the active shell tab', async () => {
    const View = lookupRoute('terminal')
    if (!View) throw new Error('terminal route not registered')
    const first = mount({
      runID: 'run_1',
      dock: { tabs: ['t1'], activeTab: 't1', collapsed: false },
    })
    const shellFor = (runID: string) =>
      StubSocket.opened.find((socket) => socket.url.includes(`/attach/${runID}?shell=t1`))
    await waitFor(() => expect(shellFor('run_1')).toBeDefined())
    const oldShell = shellFor('run_1')
    act(() => {
      oldShell?.onopen?.()
      oldShell?.onmessage?.({ data: JSON.stringify({ ok: true, replay: 0 }) })
    })

    useStore.getState().upsertRun(run({ id: 'run_2' }))
    useStore.setState({
      terminals: {},
      pausedRuns: { run_1: false, run_2: false },
      shellDocks: {
        run_1: { ...initialRunShellDock, tabs: ['t1'], activeTab: 't1', collapsed: false },
        run_2: { ...initialRunShellDock, tabs: ['t1'], activeTab: 't1', collapsed: false },
      },
    })
    first.rerender(<View params={{ runId: 'run_2' }} />)

    await waitFor(() => expect(shellFor('run_2')).toBeDefined())
    const currentShell = shellFor('run_2')
    act(() => {
      currentShell?.onopen?.()
      currentShell?.onmessage?.({ data: JSON.stringify({ ok: true, replay: 0 }) })
      currentShell?.onmessage?.({ data: new TextEncoder().encode('B output').buffer })
      // These events belong to run_1, but arrive after run_2 accepted t1.
      oldShell?.onmessage?.({ data: JSON.stringify({ ok: true, replay: 0 }) })
      oldShell?.onmessage?.({ data: new TextEncoder().encode('A output').buffer })
      oldShell?.onclose?.({ code: 1000 })
    })

    await vi.waitFor(() =>
      expect(
        [...document.querySelectorAll('.xterm-rows')].some((rows) =>
          rows.textContent?.includes('B output'),
        ),
      ).toBe(true),
    )
    expect(screen.queryByRole('status')).toBeNull()
    expect(useStore.getState().shellDocks.run_2.tabs).toEqual(['t1'])
    expect(currentShell?.closed).toBe(false)
    first.unmount()
  })

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
