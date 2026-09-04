import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
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
    shellDocks: { run_1: { ...initialRunShellDock } },
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
})
