import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type * as apiModule from '@/lib/api'
import { api } from '@/lib/api'
import type { Run } from '@/lib/types'
import { lookupRoute } from '@/routes/registry'
import '@/routes/run'
import { useStore } from '@/store'
import { item, resetItems, ScriptedSession, say, state, tool } from '@/test/acp-stream'
import { alice, bob, run, serverInfo, workspace } from '@/test/fixtures'
import { StubSocket } from '@/test/stub-socket'

vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof apiModule>()
  const { fakeApi } = await import('@/test/fixtures')
  return { ...actual, api: fakeApi() }
})

function open(over: Partial<Run> = {}) {
  const View = lookupRoute('run')!
  useStore.getState().upsertRun(run({ mode: 'acp', acp: true, ...over }))
  const params = { runId: 'run_1', view: 'session' }
  useStore.setState({ route: { name: 'run', params } })
  return render(<View params={params} />)
}

function acpSocket(): ScriptedSession {
  const socket = StubSocket.opened.find((s) => s.url.includes('/ws/acp/'))
  if (!socket) throw new Error('no /ws/acp socket opened')
  return new ScriptedSession(socket)
}

const permission = {
  id: 'req_1',
  kind: 'permission' as const,
  title: 'rm -rf build',
  tool_call_id: 'x1',
  status: 'pending' as const,
  options: [
    { id: 'always', name: 'Always Allow', kind: 'allow_always' },
    { id: 'allow', name: 'Allow', kind: 'allow_once' },
    { id: 'reject', name: 'Reject', kind: 'reject_once' },
  ],
}

/** jsdom lays nothing out; the virtualizer learns sizes only from ResizeObserver. */
class SizedObserver {
  constructor(private readonly callback: ResizeObserverCallback) {}
  observe(target: Element) {
    const height = (target as HTMLElement).style.overflowY ? 4000 : 24
    const box = [{ blockSize: height, inlineSize: 736 }]
    this.callback([{ target, contentRect: { height, width: 736 }, borderBoxSize: box, contentBoxSize: box } as unknown as ResizeObserverEntry], this as unknown as ResizeObserver)
  }
  unobserve() {}
  disconnect() {}
}

const offsetParent = Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'offsetParent')!
beforeAll(() => {
  Object.defineProperty(HTMLElement.prototype, 'offsetParent', { configurable: true, get() { return this.parentElement } })
})
afterAll(() => {
  Object.defineProperty(HTMLElement.prototype, 'offsetParent', offsetParent)
})

beforeEach(() => {
  vi.stubGlobal('ResizeObserver', SizedObserver)
  StubSocket.install()
  resetItems()
  useStore.setState({
    runs: {},
    info: serverInfo,
    members: { [alice.id]: alice, [bob.id]: bob },
    workspaces: { [workspace.id]: workspace },
    roomMessages: {},
    roomStatus: {},
    acpSessions: {},
    expandedRows: {},
    capabilities: { gateway: 'remote', methods: ['*'], ws: ['events', 'attach'] },
  })
  vi.mocked(api.runInputAnswer).mockResolvedValue({})
  vi.mocked(api.runACPCancel).mockResolvedValue({})
})

afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

describe('the Enhanced session view', () => {
  it('streams the session into rows and folds tool work into one line', async () => {
    open()
    acpSocket().open({ has_control: true, control_generation: 4 }, [
      item('turn_start', 1),
      say(1, 'user', 'Round the totals'),
      tool(1, 'r1', 'read', 'Read src/billing.js', 'completed', { locations: [{ path: 'src/billing.js' }] }),
      tool(1, 'x1', 'execute', 'npm test', 'completed', { output: 'ok\n', exit_code: 0 }),
      say(1, 'assistant', 'Totals now **round** to cents.'),
      item('turn_end', 1, { stop_reason: 'end_turn' }),
    ])
    const log = await screen.findByRole('log', { name: 'Session' })
    expect(await within(log).findByText('Round the totals')).toBeDefined()
    expect(within(log).getByText('round').tagName).toBe('STRONG')
    const group = within(log).getByRole('button', { name: /Read 1 file and ran 1 command/ })
    await userEvent.click(group)
    await userEvent.click(await within(log).findByRole('button', { name: /Ran npm test/ }))
    expect(await within(log).findByText('$ npm test')).toBeDefined()
    expect(within(log).getByText('Exit code 0')).toBeDefined()
  })

  it('docks a permission on the composer and answers it with the lease, by click or by digit', async () => {
    open()
    acpSocket().open({ has_control: true, control_generation: 4, state: state({ turn_in_flight: true, pending: [permission] }) }, [
      item('turn_start', 1),
      tool(1, 'x1', 'execute', 'rm -rf build', 'pending'),
      item('request', 1, { request: permission }),
    ])
    const card = await screen.findByRole('generic', { name: /Allow this command\? rm -rf build/ })
    expect(screen.getByText('Answer the request above to continue.')).toBeDefined()
    act(() => card.focus())
    await userEvent.keyboard('2')
    await waitFor(() => expect(api.runInputAnswer).toHaveBeenCalledWith(
      'run_1', 'req_1', 'allow', { control_session_id: expect.stringMatching(/^acp-/), control_generation: 4 }, undefined,
    ))
    await userEvent.click(within(card).getByRole('button', { name: 'Reject' }))
    expect(api.runInputAnswer).toHaveBeenLastCalledWith('run_1', 'req_1', 'reject', expect.anything(), undefined)
  })

  it('sends a form answer with its values', async () => {
    const question = { id: 'req_2', kind: 'question' as const, title: 'Which database?', status: 'pending' as const, schema: { type: 'object', properties: { db: { type: 'string', title: 'Database' } } }, options: [{ id: 'accept', name: 'Accept' }, { id: 'decline', name: 'Decline' }] }
    open()
    acpSocket().open({ has_control: true, control_generation: 2, state: state({ turn_in_flight: true, pending: [question] }) }, [item('turn_start', 1), item('request', 1, { request: question })])
    await userEvent.type(await screen.findByRole('textbox', { name: 'Database' }), 'postgres')
    await userEvent.click(screen.getByRole('button', { name: 'Accept' }))
    expect(api.runInputAnswer).toHaveBeenCalledWith('run_1', 'req_2', 'accept', expect.anything(), { db: 'postgres' })
  })

  it('steers a running turn, and interrupts it when the box is empty', async () => {
    vi.mocked(api.runInject).mockResolvedValue({ message: { id: 'm', run_id: 'run_1', workspace_id: workspace.id, kind: 'steer_request', state: 'sent', actor_id: alice.id, body: 'also docs', created_at: new Date().toISOString() } } as never)
    open()
    acpSocket().open({ has_control: true, control_generation: 4, state: state({ turn_in_flight: true, steering: true }) }, [item('turn_start', 1)])
    const box = await screen.findByRole('combobox', { name: 'Message the agent' })
    expect(screen.getByRole('button', { name: 'Interrupt the agent' })).toBeDefined()
    await userEvent.click(screen.getByRole('button', { name: 'Interrupt the agent' }))
    expect(api.runACPCancel).toHaveBeenCalledWith('run_1', expect.objectContaining({ control_generation: 4 }))
    await userEvent.type(box, 'also docs')
    expect(screen.getByText('Added to the current turn.')).toBeDefined()
    fireEvent.keyDown(box, { key: 'Enter', ctrlKey: true })
    await waitFor(() => expect(api.runInject).toHaveBeenCalledWith('run_1', 'also docs', expect.any(String), { steer: true, lease: expect.objectContaining({ control_generation: 4 }) }))
  })

  it('offers Take control instead of the box when the viewer has no lease', async () => {
    open()
    const session = acpSocket().open({ has_control: false }, [])
    await userEvent.click(await screen.findByRole('button', { name: 'Take control' }))
    expect(session.socket.frames().at(-1)).toMatchObject({ type: 'control', write: true })
  })

  it('shows the adapter failure with its stderr and the two ways on', async () => {
    open({ status: 'needs-attention' })
    acpSocket().open({ live: false }, [
      item('notice', 0, { notice: { severity: 'error', title: 'Enhanced session ended', description: "the agent's ACP server exited with code 1; stderr: boom" } }),
    ])
    expect(await screen.findByText(/stderr: boom/, { selector: 'pre' })).toBeDefined()
    expect(screen.getByRole('button', { name: 'Retry Enhanced' })).toBeDefined()
    expect(screen.getByRole('button', { name: 'Open in Standard' })).toBeDefined()
  })
})
