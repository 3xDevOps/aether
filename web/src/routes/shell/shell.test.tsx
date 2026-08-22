import { act, fireEvent, render, screen } from '@testing-library/react'
import { lookupRoute } from '@/routes/registry'
import '@/routes/shell'
import { ShellPane } from '@/routes/shell/pane'
import { useStore } from '@/store'
import type { WorkspaceShellReq } from '@/store/shell'
import { StubSocket } from '@/test/stub-socket'

// vi.mock factories are hoisted above static imports, so the fixture module
// must be loaded inside the factory (same as terminal.test.tsx).
vi.mock('@/lib/api', async () => {
  const { fakeApi } = await import('@/test/fixtures')
  return { api: fakeApi(), API_BASE: '/api/v1', ApiError: Error }
})

// jsdom has no layout engine, so the fit addon has nothing to observe.
class NoResizeObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
}

const req: WorkspaceShellReq = {
  workspace: { id: 'wsp_1', name: 'main-repo' },
  mode: 'agent-setup',
  harness: 'mycli',
}

function attached() {
  act(() => {
    StubSocket.last().onopen?.()
    StubSocket.last().onmessage?.({ data: JSON.stringify({ ok: true }) })
  })
}

beforeEach(() => {
  StubSocket.install()
  vi.stubGlobal('ResizeObserver', NoResizeObserver)
  useStore.setState({ shellRequest: null })
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('shell pane', () => {
  it('shows the clean-exit hint and sends exit on Done', () => {
    const onExit = vi.fn()
    const view = render(<ShellPane req={req} onExit={onExit} />)
    attached()

    expect(screen.getByText(/Exit the shell cleanly \(type exit\)/)).toBeDefined()
    expect(screen.getByText('Agent setup')).toBeDefined()
    expect(screen.getByText('main-repo')).toBeDefined()

    fireEvent.click(screen.getByText('Done'))
    expect(StubSocket.last().frames()[1]).toEqual({ type: 'input', data: 'exit\r' })
    expect(onExit).not.toHaveBeenCalled()
    view.unmount()
  })

  it('banners a clean exit as registered', () => {
    const onExit = vi.fn()
    const view = render(<ShellPane req={req} onExit={onExit} />)
    attached()

    act(() => StubSocket.last().onclose?.({ code: 1000 }))

    expect(screen.getByText('Registered and snapshotted.')).toBeDefined()
    expect(onExit).toHaveBeenCalledWith(true)
    view.unmount()
  })

  it('offers resume and reset after a dirty exit', () => {
    const onExit = vi.fn()
    const view = render(<ShellPane req={req} onExit={onExit} />)
    attached()

    act(() =>
      StubSocket.last().onclose?.({ code: 4001, reason: 'shell exited with status 1' }),
    )

    expect(screen.getByText('shell exited with status 1')).toBeDefined()
    expect(onExit).toHaveBeenCalledWith(false)

    fireEvent.click(screen.getByText('Resume'))
    expect(useStore.getState().shellRequest).toMatchObject({
      mode: 'agent-setup',
      resume: true,
      reset: false,
    })

    fireEvent.click(screen.getByText('Reset'))
    expect(useStore.getState().shellRequest).toMatchObject({
      resume: false,
      reset: true,
    })
    view.unmount()
  })

  it('renders a server refusal verbatim', () => {
    const onExit = vi.fn()
    const view = render(<ShellPane req={req} onExit={onExit} />)
    act(() => {
      StubSocket.last().onopen?.()
      StubSocket.last().onmessage?.({
        data: JSON.stringify({ ok: false, code: -32001, error: 'workspace not found' }),
      })
    })

    expect(screen.getByText('workspace not found')).toBeDefined()
    expect(onExit).toHaveBeenCalledWith(false)
    view.unmount()
  })
})

describe('shell route', () => {
  it('points back to the board when no shell is open', () => {
    const View = lookupRoute('shell')
    if (!View) throw new Error('shell route not registered')
    const view = render(<View params={{}} />)

    expect(screen.getByText(/No shell is open/)).toBeDefined()
    fireEvent.click(screen.getByText('Back to board'))
    expect(useStore.getState().route.name).toBe('board')
    view.unmount()
  })

  it('renders the open shell and offers agents navigation after an agent-setup exit', () => {
    const View = lookupRoute('shell')
    if (!View) throw new Error('shell route not registered')
    useStore.setState({ shellRequest: req })
    const view = render(<View params={{}} />)
    attached()

    expect(screen.queryByText('Back to workspaces')).toBeNull()

    act(() => StubSocket.last().onclose?.({ code: 1000 }))

    // The pane's own banner stays; the route adds where to go next.
    expect(screen.getByText('Registered and snapshotted.')).toBeDefined()
    expect(screen.getByText('Back to workspaces')).toBeDefined()
    fireEvent.click(screen.getByText('Back to agents'))
    expect(useStore.getState().route.name).toBe('agents')
    view.unmount()
  })
})
