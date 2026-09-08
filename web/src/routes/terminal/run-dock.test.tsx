import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { Terminal } from '@xterm/xterm'
import { lookupRoute } from '@/routes/registry'
import '@/routes/terminal'
import { useStore } from '@/store'
import {
  initialRunShellDock,
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

function mount() {
  const View = lookupRoute('terminal')
  if (!View) throw new Error('terminal route not registered')
  useStore.getState().upsertRun(run())
  useStore.setState({
    terminals: {},
    // The dock ships collapsed; these cases are about what it shows open.
    shellDocks: { run_1: { ...initialRunShellDock, collapsed: false } },
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
  it('opens a write-required shell tab at the encoded run URL', async () => {
    const view = mount()

    fireEvent.click(screen.getByRole('button', { name: 'Open shell' }))
    await waitFor(() => expect(StubSocket.opened.length).toBeGreaterThanOrEqual(2))

    const socket = StubSocket.opened[1]
    expect(socket.url).toBe('ws://localhost/ws/attach/run_1?shell=t1')
    act(() => socket.onopen?.())
    expect(socket.frames()[0]).toMatchObject({ write: true })
    view.unmount()
  })

  it('recreates the terminal host after collapsing and expanding the dock', async () => {
    const open = vi.spyOn(Terminal.prototype, 'open')
    const view = mount()
    fireEvent.click(screen.getByRole('button', { name: 'Open shell' }))
    await waitFor(() => expect(StubSocket.opened.length).toBeGreaterThanOrEqual(2))
    const hostCount = () =>
      view.container.querySelectorAll('div.h-full.min-h-0.bg-background.p-2').length
    const opened = open.mock.calls.length

    expect(hostCount()).toBe(2)
    fireEvent.click(screen.getByRole('button', { name: 'Collapse terminal dock' }))
    expect(hostCount()).toBe(1)
    fireEvent.click(screen.getByRole('button', { name: 'Expand terminal dock' }))

    await waitFor(() => {
      expect(hostCount()).toBe(2)
      expect(open.mock.calls.length).toBeGreaterThan(opened)
    })
    open.mockRestore()
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

    expect(
      screen.getByText('You can view this run but not open a shell in it'),
    ).toBeDefined()
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
    view.unmount()
  })

  it('starts collapsed and opens from the header toggle', () => {
    useStore.getState().upsertRun(run())
    useStore.setState({ terminals: {}, shellDocks: {} })
    const View = lookupRoute('terminal')
    if (!View) throw new Error('terminal route not registered')
    const view = render(<View params={{ runId: 'run_1' }} />)

    expect(screen.queryByRole('button', { name: 'Open shell' })).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Expand terminal dock' }))

    expect(screen.getByRole('button', { name: 'Open shell' })).toBeDefined()
    view.unmount()
  })

  it('names the real tab ceiling when every shell tab is open', async () => {
    const view = mount()
    for (let n = 0; n < 4; n++) {
      fireEvent.click(
        screen.getByRole('button', { name: n === 0 ? 'Open shell' : 'Add terminal tab' }),
      )
      await waitFor(() => expect(useStore.getState().shellDocks.run_1.tabs).toHaveLength(n + 1))
    }

    expect(screen.getByText('At most 4 tabs')).toBeDefined()
    expect(
      (screen.getByRole('button', { name: 'Add terminal tab' }) as HTMLButtonElement).disabled,
    ).toBe(true)
    view.unmount()
  })

  it('does not offer shell tabs after the run container is gone', () => {
    const view = mount()
    act(() => useStore.getState().upsertRun(run({ status: 'needs-attention' })))

    expect(screen.queryByRole('button', { name: 'Open shell' })).toBeNull()
    expect(screen.getByText(/Run shell unavailable/)).toBeDefined()
    view.unmount()
  })
})
