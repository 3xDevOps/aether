import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type * as apiModule from '@/lib/api'
import { api } from '@/lib/api'
import type { SessionHistory } from '@/lib/session-types'
import type { Run } from '@/lib/types'
import { lookupRoute } from '@/routes/registry'
import '@/routes/run'
import type { TakeoverState } from '@/routes/terminal/attach'
import { useStore } from '@/store'
import { item, resetItems, ScriptedSession, say, state, tool } from '@/test/acp-stream'
import { alice, bob, roomMessage, run, serverInfo, workspace } from '@/test/fixtures'
import { pickOption } from '@/test/select'
import { StubSocket } from '@/test/stub-socket'

const eventRenders = vi.hoisted(() => new Map<string, number>())

vi.mock('@/components/ui/timeline', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/components/ui/timeline')>()
  return {
    ...actual,
    EventRow: (props: Parameters<typeof actual.EventRow>[0]) => {
      const text = typeof props.children === 'string' ? props.children : ''
      eventRenders.set(text, (eventRenders.get(text) ?? 0) + 1)
      return actual.EventRow(props)
    },
  }
})

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

async function cleanupSessionView() {
  cleanup()
  // Unmount closes the stream in a batch. Let its async scope end before
  // resetting the store, so the reset flushes and cancels its held frame/timer.
  await Promise.resolve()
  useStore.setState(useStore.getInitialState(), true)
}

function acpSocket(): ScriptedSession {
  const socket = StubSocket.opened.find((s) => s.url.includes('/ws/acp/'))
  if (!socket) throw new Error('no /ws/acp socket opened')
  return new ScriptedSession(socket)
}

function receiveTakeover(session: ScriptedSession, phase: TakeoverState['phase'], over: Partial<TakeoverState> = {}) {
  const takeover: TakeoverState = {
    id: 'takeover-request',
    requester_member_id: bob.id,
    requester_session_id: 'other-session',
    holder_session_id: String(session.header().control_session_id),
    holder_generation: 4,
    phase,
    hold_started_at: '2026-10-08T12:00:00Z',
    hold_deadline: '2026-10-08T12:00:05Z',
    decision_deadline: '2026-10-08T12:00:12Z',
    server_now: phase === 'holding' ? '2026-10-08T12:00:00Z' : '2026-10-08T12:00:05Z',
    ...over,
  }
  session.send({ type: 'takeover', ok: true, takeover })
}

const occupiedRoom = {
  workspace_id: workspace.id, run_id: 'run_1', protected: false, watchers: [alice.id], queued_steers: 0,
  controller: { member_id: bob.id, connected: true, acquired_at: '2026-10-08T11:00:00Z' },
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

afterEach(async () => {
  await cleanupSessionView()
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

  it('keeps live Enhanced output and approvals usable after a retained-prefix replay reset', async () => {
    open()
    const session = acpSocket().open({
      seq: 20, replay: 1, oldest_seq: 20, truncated_before: true, has_control: true, control_generation: 4,
      state: state({ turn_in_flight: true, pending: [permission] }),
    })
    session.send({ reset: true, epoch: 0 })
    session.items({ ...say(1, 'assistant', 'Current retained response'), seq: 20 })
    const log = await screen.findByRole('log', { name: 'Session' })
    expect(await within(log).findByText('Current retained response')).toBeDefined()
    expect(screen.getByText(/Earlier Enhanced history has expired/)).toBeDefined()
    // The tool-call frame expired; the authoritative pending request still permits an answer,
    // but cannot establish a command-specific title without inventing missing history.
    const card = await screen.findByRole('generic', { name: /Allow this action\? rm -rf build/ })
    const allow = within(card).getByRole('button', { name: 'Allow' })
    expect(allow).toHaveProperty('disabled', false)
    await userEvent.click(allow)
    expect(api.runInputAnswer).toHaveBeenCalledWith('run_1', 'req_1', 'allow', expect.objectContaining({ control_generation: 4 }), undefined)
    session.items({ ...say(1, 'assistant', 'Live output after the gap'), seq: 21 })
    expect(await within(log).findByText('Live output after the gap')).toBeDefined()
    expect(within(log).getAllByText('Current retained response')).toHaveLength(1)
  })

  it('retains the output excerpt when its full Enhanced item expires', async () => {
    const expired = 'acphost: requested session history has expired'
    vi.mocked(api.runACPItem).mockRejectedValue(new Error(expired))
    open()
    const start = item('turn_start', 1)
    const entry = tool(1, 'x1', 'execute', 'npm test', 'completed', { output: 'retained excerpt', exit_code: 0 })
    acpSocket().open({}, [start, { ...entry, truncated: true }])
    const log = await screen.findByRole('log', { name: 'Session' })
    await userEvent.click(await within(log).findByRole('button', { name: /Ran 1 command/ }))
    await userEvent.click(await within(log).findByRole('button', { name: /Ran npm test/ }))
    expect(await within(log).findByText('retained excerpt')).toBeDefined()
    expect((await within(log).findByRole('alert')).textContent).toContain(expired)
    expect(within(log).queryByText('Loading the full output…')).toBeNull()
    expect(api.runACPItem).toHaveBeenCalledWith('run_1', entry.seq)
  })

  it('reads Queued on a message the agent holds behind its turn, then why it was not sent', async () => {
    open()
    acpSocket().open({ has_control: true, control_generation: 4, state: state({ turn_in_flight: true }) }, [item('turn_start', 1)])
    const log = await screen.findByRole('log', { name: 'Session' })
    const queued = roomMessage({ id: 'msg_q', kind: 'steer_request', body: 'and docs', state: 'sent', agent_delivery: 'queued', updated_at: '2026-08-14T10:00:01Z' })
    act(() => useStore.getState().upsertRoomMessage(queued))
    expect(await within(log).findByText('Queued')).toBeDefined()
    act(() => useStore.getState().upsertRoomMessage({ ...queued, agent_delivery: 'delivered', updated_at: '2026-08-14T10:00:02Z' }))
    expect(await within(log).findByText('Sent')).toBeDefined()
    expect(within(log).queryByText('Queued')).toBeNull()
    act(() => useStore.getState().upsertRoomMessage(roomMessage({
      id: 'msg_lost', kind: 'steer_request', body: 'and tests', state: 'not_sent', agent_delivery: 'queued',
      failure: { code: 'agent_disconnected', message: 'acphost: agent connection closed' },
    })))
    expect(await within(log).findByText('Not sent')).toBeDefined()
    expect(within(log).getByText('acphost: agent connection closed')).toBeDefined()
  })

  it('holds an earlier prompt back until the items after it are loaded', async () => {
    const [start, prompt, reply, latest] = [item('turn_start', 1), say(1, 'user', 'Round the totals'), say(1, 'assistant', 'Rounded.'), say(1, 'assistant', 'Tests pass.')]
    const page = Promise.withResolvers<SessionHistory>()
    vi.mocked(api.runACPHistory).mockClear().mockReturnValueOnce(page.promise)
    open()
    acpSocket().open({ oldest_seq: latest.seq }, [latest])
    const log = await screen.findByRole('log', { name: 'Session' })
    act(() => {
      useStore.getState().upsertRoomMessage(roomMessage({ id: 'msg_first', kind: 'steer_request', body: 'Round the totals', agent_delivery: 'delivered', created_at: prompt.time }))
      useStore.getState().upsertRoomMessage(roomMessage({ id: 'msg_queued', kind: 'steer_request', body: 'and docs', agent_delivery: 'queued', created_at: prompt.time }))
    })
    expect(await within(log).findByText('Tests pass.')).toBeDefined()
    expect(await within(log).findByText('and docs')).toBeDefined()
    expect(within(log).queryByText('Round the totals')).toBeNull()
    await act(async () => page.resolve({ frames: [start, prompt, reply].map((it) => ({ seq: it.seq, item: it })), oldest_seq: start.seq }))
    const first = await within(log).findByText('Round the totals')
    expect(first.compareDocumentPosition(within(log).getByText('Rounded.')) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(api.runACPHistory).toHaveBeenCalledTimes(1)
    expect(api.runACPHistory).toHaveBeenCalledWith('run_1', latest.seq, 1000)
  })

  it('keeps a queued copy of the oldest loaded prompt as its own row', async () => {
    const [gone, prompt] = [item('turn_start', 1), say(1, 'user', 'continue')]
    vi.mocked(api.runACPHistory).mockClear().mockReturnValueOnce(Promise.withResolvers<SessionHistory>().promise)
    open()
    acpSocket().open({ oldest_seq: prompt.seq }, [prompt])
    const log = await screen.findByRole('log', { name: 'Session' })
    act(() => {
      useStore.getState().upsertRoomMessage(roomMessage({ id: 'msg_sent', kind: 'steer_request', body: 'continue', agent_delivery: 'delivered', created_at: gone.time }))
      useStore.getState().upsertRoomMessage(roomMessage({
        id: 'msg_queued', actor_id: bob.id, kind: 'steer_request', body: 'continue', agent_delivery: 'queued',
        created_at: new Date(Date.parse(prompt.time) + 1000).toISOString(),
      }))
    })
    expect(await within(log).findByText('Queued')).toBeDefined()
    expect(within(log).getAllByText('continue')).toHaveLength(2)
    expect(within(log).getAllByText('Sent')).toHaveLength(1)
  })

  it('shows a repeated prompt once while its earlier copies are not loaded', async () => {
    const [gone, reply, prompt] = [item('turn_start', 1), say(1, 'assistant', 'Rounded.'), say(1, 'user', 'continue')]
    vi.mocked(api.runACPHistory).mockClear().mockReturnValueOnce(Promise.withResolvers<SessionHistory>().promise)
    open()
    acpSocket().open({ oldest_seq: reply.seq }, [reply, prompt])
    const log = await screen.findByRole('log', { name: 'Session' })
    act(() => {
      useStore.getState().upsertRoomMessage(roomMessage({ id: 'msg_early', kind: 'steer_request', body: 'continue', agent_delivery: 'delivered', created_at: gone.time }))
      useStore.getState().upsertRoomMessage(roomMessage({ id: 'msg_recent', kind: 'steer_request', body: 'continue', agent_delivery: 'delivered', created_at: reply.time }))
    })
    expect(await within(log).findByText('Sent')).toBeDefined()
    expect(within(log).getAllByText('continue')).toHaveLength(1)
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
    expect(within(card).getAllByRole('button').map((b) => b.textContent)).toEqual(['Allow', 'Reject', 'Always Allow'])
    act(() => card.focus())
    await userEvent.keyboard('1')
    await waitFor(() => expect(api.runInputAnswer).toHaveBeenCalledWith(
      'run_1', 'req_1', 'allow', { control_session_id: expect.stringMatching(/^acp-/), control_generation: 4 }, undefined,
    ))
    await userEvent.click(within(card).getByRole('button', { name: 'Reject' }))
    expect(api.runInputAnswer).toHaveBeenLastCalledWith('run_1', 'req_1', 'reject', expect.anything(), undefined)
  })

  it('points Details at the docked request instead of repeating it', async () => {
    open()
    acpSocket().open({ has_control: true, control_generation: 4, state: state({ turn_in_flight: true, pending: [permission] }) }, [
      item('turn_start', 1),
      tool(1, 'x1', 'execute', 'rm -rf build', 'pending'),
      item('request', 1, { request: permission }),
    ])
    expect(await screen.findByText('Waiting for your approval: rm -rf build')).toBeDefined()
    fireEvent.click(screen.getByRole('button', { name: 'Show details' }))
    const needs = within(within(await screen.findByRole('dialog', { name: 'Run details' })).getByRole('region', { name: 'Needs you' }))
    expect(needs.queryByRole('generic', { name: /Allow this command\?/ })).toBeNull()
    fireEvent.click(needs.getByRole('button', { name: '1 request, shown below the timeline' }))
    await waitFor(() => expect(document.activeElement?.id).toBe('session-request-docked'))
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
    await waitFor(() => expect(api.runInject).toHaveBeenCalledWith('run_1', 'also docs', expect.any(String), expect.objectContaining({ steer: true, lease: expect.objectContaining({ control_generation: 4 }) })))
  })

  it('does nothing on Mod+Enter with an empty box, and keeps Interrupt while a request waits', async () => {
    open()
    const session = acpSocket().open({ has_control: true, control_generation: 4, state: state({ turn_in_flight: true, steering: true }) }, [item('turn_start', 1)])
    vi.mocked(api.runACPCancel).mockClear()
    const box = await screen.findByRole('combobox', { name: 'Message the agent' })
    fireEvent.keyDown(box, { key: 'Enter', ctrlKey: true })
    expect(api.runACPCancel).not.toHaveBeenCalled()
    session.items(item('request', 1, { request: permission }))
    act(() => useStore.getState().acpAck('run_1', { ok: true, seq: 100, replay: 0, epoch: 0, live: true, has_control: true, state: state({ turn_in_flight: true, pending: [permission] }) }))
    expect(await screen.findByText('Answer the request above to continue.')).toBeDefined()
    await userEvent.click(screen.getByRole('button', { name: 'Interrupt the agent' }))
    expect(api.runACPCancel).toHaveBeenCalledWith('run_1', expect.objectContaining({ control_generation: 4 }))
  })

  it('keeps Accept off until a form\'s required fields have values, and offers enum fields as a choice', async () => {
    const question = {
      id: 'req_3', kind: 'question' as const, title: 'Which database?', status: 'pending' as const,
      schema: { type: 'object', required: ['db'], properties: { db: { type: 'string', title: 'Database', enum: ['postgres', 'sqlite'] }, port: { type: 'integer', title: 'Port' } } },
      options: [{ id: 'accept', name: 'Accept' }, { id: 'decline', name: 'Decline' }],
    }
    open()
    acpSocket().open({ has_control: true, control_generation: 2, state: state({ turn_in_flight: true, pending: [question] }) }, [item('turn_start', 1), item('request', 1, { request: question })])
    const accept = await screen.findByRole('button', { name: 'Accept' })
    expect((accept as HTMLButtonElement).disabled).toBe(true)
    const port = screen.getByRole('spinbutton', { name: 'Port' })
    await userEvent.type(port, '5')
    await userEvent.clear(port)
    await pickOption(screen.getByRole('combobox', { name: 'Database (required)' }), 'sqlite')
    const enabled = screen.getByRole('button', { name: 'Accept' }) as HTMLButtonElement
    expect(enabled.disabled).toBe(false)
    await userEvent.click(enabled)
    expect(api.runInputAnswer).toHaveBeenCalledWith('run_1', 'req_3', 'accept', expect.anything(), { db: 'sqlite' })
  })

  it('leaves a closed turn\'s rows alone while the next turn streams', async () => {
    open()
    const session = acpSocket().open({ has_control: true, control_generation: 1 }, [
      item('turn_start', 1),
      say(1, 'user', 'first'),
      item('turn_end', 1, { stop_reason: 'end_turn' }),
      item('turn_start', 2),
    ])
    const log = await screen.findByRole('log', { name: 'Session' })
    await within(log).findByText('Finished in 2s')
    const before = eventRenders.get('Finished in 2s')
    for (let i = 0; i < 5; i++) session.items(say(2, 'assistant', `part ${i}`, 'reply'))
    await within(log).findByText(/part 4/)
    expect(eventRenders.get('Finished in 2s')).toBe(before)
  })

  it('lets the owner of a run nobody controls send in one step, taking control first', async () => {
    vi.mocked(api.runInject).mockClear().mockResolvedValue({ message: { id: 'm', run_id: 'run_1', workspace_id: workspace.id, kind: 'steer_request', state: 'sent', actor_id: alice.id, body: 'go', created_at: new Date().toISOString() } } as never)
    open({ controller_member_id: '' })
    const session = acpSocket().open({ has_control: false }, [])
    await userEvent.type(await screen.findByRole('combobox', { name: 'Message the agent' }), 'go')
    await userEvent.click(screen.getByRole('button', { name: /Send/ }))
    const request = session.socket.frames().at(-1) as { type: string; write: boolean; request_id: number }
    expect(request).toMatchObject({ type: 'control', write: true })
    expect(api.runInject).not.toHaveBeenCalled()
    session.send({ type: 'control', request_id: request.request_id, ok: true, has_control: true, control_generation: 5 })
    await waitFor(() => expect(api.runInject).toHaveBeenCalledWith('run_1', 'go', expect.any(String), expect.objectContaining({ steer: false, lease: expect.objectContaining({ control_generation: 5 }) })))
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

describe('Enhanced multiplayer controls', () => {
  it('keeps the controller identity tied to the acknowledged lease while presence catches up', async () => {
    vi.spyOn(api, 'runRoomStatus').mockResolvedValue(occupiedRoom)
    open()
    const session = acpSocket().open({ has_control: true, control_generation: 4 })
    const controller = within(await screen.findByRole('group', { name: 'Run controller' }))
    expect(controller.getByRole('img', { name: alice.display_name })).toBeDefined()
    expect(controller.queryByRole('img', { name: bob.display_name })).toBeNull()
    expect(controller.getByText(/Alice.*You control/)).toBeDefined()
    session.send({ type: 'control', has_control: false, control_generation: 4, revocation_reason: 'takeover' })
    expect(await controller.findByRole('img', { name: bob.display_name })).toBeDefined()
    expect(controller.queryByRole('img', { name: alice.display_name })).toBeNull()
    expect(screen.getByRole('button', { name: 'Take control' })).toBeDefined()
  })

  it('releases the acknowledged lease without closing the session and remains reachable on Terminal', async () => {
    const view = open()
    const session = acpSocket().open({ has_control: true, control_generation: 4 })
    const controls = await screen.findByRole('group', { name: 'Multiplayer controls' })
    await userEvent.click(within(controls).getByRole('button', { name: 'Release' }))
    const request = session.socket.frames().at(-1) as { request_id: number }
    expect(request).toMatchObject({ type: 'control', write: false, control_generation: 4 })
    session.send({ type: 'control', request_id: request.request_id, ok: true, has_control: false, control_generation: 4 })
    expect(await within(controls).findByRole('button', { name: 'Take control' })).toBeDefined()
    const View = lookupRoute('run')!
    view.rerender(<View params={{ runId: 'run_1', view: 'terminal' }} />)
    expect(screen.getByText('No agent terminal')).toBeDefined()
    expect(within(screen.getByRole('group', { name: 'Multiplayer controls' })).getByRole('button', { name: 'Take control' })).toBeDefined()
    expect(session.socket.closed).toBe(false)
  })

  it.each([false, true])('lets a collaborator send without taking the lease (pending request: %s)', async (pending) => {
    vi.spyOn(api, 'runRoomStatus').mockResolvedValue(occupiedRoom)
    const queued = roomMessage({ actor_id: alice.id, kind: 'steer_request', body: 'Please review the migration', state: 'queued', deliver_after: new Date(Date.now() + 45_000).toISOString() })
    vi.spyOn(api, 'runInject').mockResolvedValue({ message: queued })
    vi.mocked(api.runInputAnswer).mockClear()
    open({ member_id: bob.id, controller_member_id: bob.id })
    const session = acpSocket().open({ has_control: false, state: state({ turn_in_flight: true, steering: true, pending: pending ? [permission] : [] }) })
    const box = await screen.findByRole('combobox', { name: 'Message the agent' })
    expect(screen.queryByRole('button', { name: 'Interrupt the agent' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Steer' })).toBeNull()
    await userEvent.type(box, queued.body)
    await userEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(box).toHaveProperty('value', ''))
    expect(useStore.getState().roomMessages.run_1).toContainEqual(queued)
    expect(useStore.getState().acpSessions.run_1?.control?.has_control).toBe(false)
    if (pending) expect(screen.getByRole('button', { name: 'Allow' })).toHaveProperty('disabled', true)
    expect(api.runInputAnswer).not.toHaveBeenCalled()
    expect(session.socket.frames()).not.toContainEqual(expect.objectContaining({ type: 'control', write: true }))
    expect(api.runInject).toHaveBeenCalledWith('run_1', queued.body, expect.any(String), expect.objectContaining({ steer: false, lease: undefined }))
  })

  it('keeps protected-run collaborators from messaging or answering a pending request', async () => {
    useStore.setState({ info: { ...serverInfo, member: bob } })
    open({ protected: true })
    acpSocket().open({ has_control: false, state: state({ turn_in_flight: true, pending: [permission] }) })
    expect(await screen.findByRole('button', { name: 'Allow' })).toHaveProperty('disabled', true)
    expect(screen.queryByRole('combobox', { name: 'Message the agent' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Send' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Interrupt the agent' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Take control' })).toBeNull()
  })

  it('requires the full hold and server grant even while the Enhanced session rerenders', async () => {
    vi.spyOn(api, 'runRoomStatus').mockResolvedValue(occupiedRoom)
    open({ member_id: bob.id, controller_member_id: bob.id })
    const session = acpSocket().open({ has_control: false })
    await waitFor(() => expect(screen.getByText('Bob controls')).toBeDefined())
    const button = screen.getByRole('button', { name: 'Take control' })
    vi.useFakeTimers()
    try {
      fireEvent.keyDown(button, { key: ' ' })
      await act(async () => { await vi.advanceTimersByTimeAsync(180) })
      const start = session.socket.frames().at(-1) as { takeover_id: string }
      expect(start).toMatchObject({ type: 'takeover', action: 'start' })
      const request = { id: start.takeover_id, requester_member_id: alice.id, requester_session_id: String(session.header().control_session_id), holder_session_id: 'holder-session' }
      receiveTakeover(session, 'holding', request)
      session.items(say(1, 'assistant', 'Still working during the hold'))
      await act(async () => { await vi.advanceTimersByTimeAsync(4999) })
      expect(session.socket.frames()).not.toContainEqual(expect.objectContaining({ action: 'confirm' }))
      expect(session.socket.frames()).not.toContainEqual(expect.objectContaining({ action: 'cancel' }))
      expect(screen.queryByRole('button', { name: 'Release' })).toBeNull()
      await act(async () => { await vi.advanceTimersByTimeAsync(41) })
      expect(session.socket.frames().at(-1)).toMatchObject({ type: 'takeover', action: 'confirm', takeover_id: start.takeover_id })
      fireEvent.keyUp(button, { key: ' ' })
      receiveTakeover(session, 'review', request)
      expect(button.getAttribute('aria-disabled')).toBe('true')
      expect(useStore.getState().acpSessions.run_1?.control?.has_control).toBe(false)
      session.send({ type: 'control', ok: true, has_control: true, control_generation: 5 })
      receiveTakeover(session, 'granted', request)
      expect(screen.getByRole('button', { name: 'Release' })).toBeDefined()
      expect(session.socket.frames()).not.toContainEqual(expect.objectContaining({ action: 'cancel' }))
      expect(session.socket.frames()).not.toContainEqual(expect.objectContaining({ type: 'control', write: true }))
    } finally {
      await cleanupSessionView()
      vi.useRealTimers()
    }
  })

  it('cancels an early release instead of confirming a partial hold', async () => {
    vi.spyOn(api, 'runRoomStatus').mockResolvedValue(occupiedRoom)
    open({ member_id: bob.id, controller_member_id: bob.id })
    const session = acpSocket().open({ has_control: false })
    await waitFor(() => expect(screen.getByText('Bob controls')).toBeDefined())
    const button = screen.getByRole('button', { name: 'Take control' })
    vi.useFakeTimers()
    try {
      fireEvent.keyDown(button, { key: ' ' })
      await act(async () => { await vi.advanceTimersByTimeAsync(180) })
      const start = session.socket.frames().at(-1) as { takeover_id: string }
      receiveTakeover(session, 'holding', { id: start.takeover_id, requester_member_id: alice.id, requester_session_id: String(session.header().control_session_id), holder_session_id: 'holder-session' })
      await act(async () => { await vi.advanceTimersByTimeAsync(1000) })
      fireEvent.keyUp(button, { key: ' ' })
      await act(async () => { await vi.advanceTimersByTimeAsync(5000) })
      expect(session.socket.frames()).toContainEqual(expect.objectContaining({ type: 'takeover', action: 'cancel', takeover_id: start.takeover_id }))
      expect(session.socket.frames()).not.toContainEqual(expect.objectContaining({ action: 'confirm' }))
      expect(screen.queryByRole('button', { name: 'Release' })).toBeNull()
    } finally {
      await cleanupSessionView()
      vi.useRealTimers()
    }
  })

  it.each(['accept', 'deny'] as const)('lets the holder %s a takeover above the Session composer', async (decision) => {
    open()
    const session = acpSocket().open({ has_control: true, control_generation: 4 })
    const box = await screen.findByRole('combobox', { name: 'Message the agent' })
    box.focus()
    receiveTakeover(session, 'review')
    const dialog = await screen.findByRole('alertdialog', { name: 'Run control requested' })
    expect(document.activeElement).toBe(within(dialog).getByRole('button', { name: 'Deny' }))
    await userEvent.click(within(dialog).getByRole('button', { name: decision === 'accept' ? 'Accept' : 'Deny' }))
    expect(session.socket.frames().at(-1)).toMatchObject({ type: 'takeover', action: decision, takeover_id: 'takeover-request', control_generation: 4 })
    receiveTakeover(session, decision === 'accept' ? 'granted' : 'denied')
    if (decision === 'accept') session.send({ type: 'control', has_control: false, control_generation: 4, revocation_reason: 'takeover' })
    await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull())
    expect(Boolean(screen.queryByRole('button', { name: 'Release' }))).toBe(decision === 'deny')
    expect(within(screen.getByRole('group', { name: 'Multiplayer controls' })).queryByRole('alert')).toBeNull()
    if (decision === 'deny') await waitFor(() => expect(document.activeElement).toBe(box))
  })
})
