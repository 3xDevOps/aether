import { act, fireEvent, render, screen } from '@testing-library/react'
import { api } from '@/lib/api'
import { lookupRoute } from '@/routes/registry'
import '@/routes/agents'
import { splitArgv } from '@/routes/agents/wizard'
import { useStore } from '@/store'
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

function mount() {
  const View = lookupRoute('agents')
  if (!View) throw new Error('agents route not registered')
  return render(<View params={{}} />)
}

async function flush() {
  await act(async () => {})
}

beforeEach(() => {
  StubSocket.install()
  vi.stubGlobal('ResizeObserver', NoResizeObserver)
  useStore.setState({
    shellRequest: null,
    capabilities: { gateway: 'local', methods: ['*'], ws: ['events', 'attach', 'shell'] },
  })
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('splitArgv', () => {
  it('splits on spaces and drops empty words', () => {
    expect(splitArgv('mycli -p {task}')).toEqual(['mycli', '-p', '{task}'])
    expect(splitArgv('  mycli  {task} ')).toEqual(['mycli', '{task}'])
  })
})

describe('agents view', () => {
  it('lists agents with shipped and member badges', async () => {
    const view = mount()
    await flush()

    expect(screen.getByText('claude')).toBeDefined()
    expect(screen.getByText('shipped')).toBeDefined()
    expect(screen.getByText('myagent')).toBeDefined()
    expect(screen.getByText('member')).toBeDefined()
    view.unmount()
  })

  it('hides the add button when the gateway lacks the shell socket', async () => {
    useStore.setState({
      capabilities: { gateway: 'remote', methods: ['*'], ws: ['events', 'attach'] },
    })
    const view = mount()
    await flush()

    expect(screen.queryByText('Add agent')).toBeNull()
    view.unmount()
  })

  it('prefills the argv templates from the name and opens the setup shell', async () => {
    const view = mount()
    await flush()

    fireEvent.click(screen.getByText('Add agent'))
    await flush() // the wizard's workspace fetch picks wsp_1
    fireEvent.change(screen.getByPlaceholderText('claude'), {
      target: { value: 'mycli' },
    })

    // The CLI's defaults, following the name until edited.
    expect(screen.getByDisplayValue('mycli {task}')).toBeDefined()
    expect(screen.getByDisplayValue('mycli -p {task}')).toBeDefined()

    fireEvent.click(screen.getByText('Open setup shell'))
    await flush()

    expect(useStore.getState().shellRequest).toEqual({
      workspace: { id: 'wsp_1' },
      mode: 'agent-setup',
      harness: 'mycli',
      tui_args: ['mycli', '{task}'],
      headless_args: ['mycli', '-p', '{task}'],
    })

    // The header frame carries the argv proposals to the socket.
    act(() => StubSocket.last().onopen?.())
    expect(StubSocket.last().frames()[0]).toMatchObject({
      mode: 'agent-setup',
      harness: 'mycli',
      tui_args: ['mycli', '{task}'],
      headless_args: ['mycli', '-p', '{task}'],
    })
    view.unmount()
  })

  it('omits argv proposals for a shipped name', async () => {
    const view = mount()
    await flush()

    fireEvent.click(screen.getByText('Add agent'))
    await flush()
    fireEvent.change(screen.getByPlaceholderText('claude'), {
      target: { value: 'claude' },
    })

    expect(screen.queryByText('TUI command')).toBeNull()

    fireEvent.click(screen.getByText('Open setup shell'))
    expect(useStore.getState().shellRequest).toEqual({
      workspace: { id: 'wsp_1' },
      mode: 'agent-setup',
      harness: 'claude',
    })
    view.unmount()
  })

  it('reports registration and refetches the list on a clean exit', async () => {
    const view = mount()
    await flush()

    fireEvent.click(screen.getByText('Add agent'))
    await flush()
    fireEvent.change(screen.getByPlaceholderText('claude'), {
      target: { value: 'mycli' },
    })
    fireEvent.click(screen.getByText('Open setup shell'))
    await flush()

    const listCalls = vi.mocked(api.agentList).mock.calls.length
    act(() => {
      StubSocket.last().onopen?.()
      StubSocket.last().onmessage?.({ data: JSON.stringify({ ok: true }) })
      StubSocket.last().onclose?.({ code: 1000 })
    })
    await flush()

    expect(screen.getByText('Agent registered')).toBeDefined()
    expect(vi.mocked(api.agentList).mock.calls.length).toBe(listCalls + 1)
    view.unmount()
  })

  it('keeps the wizard on the pane with resume/reset after a dirty exit', async () => {
    const view = mount()
    await flush()

    fireEvent.click(screen.getByText('Add agent'))
    await flush()
    fireEvent.change(screen.getByPlaceholderText('claude'), {
      target: { value: 'mycli' },
    })
    fireEvent.click(screen.getByText('Open setup shell'))
    await flush()

    act(() => {
      StubSocket.last().onopen?.()
      StubSocket.last().onmessage?.({ data: JSON.stringify({ ok: true }) })
      StubSocket.last().onclose?.({ code: 4001, reason: 'login abandoned' })
    })

    expect(screen.queryByText('Agent registered')).toBeNull()
    expect(screen.getByText('login abandoned')).toBeDefined()
    expect(screen.getByText('Resume')).toBeDefined()

    fireEvent.click(screen.getByText('Reset'))
    expect(useStore.getState().shellRequest).toMatchObject({
      harness: 'mycli',
      resume: false,
      reset: true,
    })
    view.unmount()
  })
})
