import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type * as apiModule from '@/lib/api'
import { api } from '@/lib/api'
import type { Run } from '@/lib/types'
import { lookupRoute } from '@/routes/registry'
import '@/routes/run'
import { useStore } from '@/store'
import { agentInfo, alice, bob, roomMessage, run, serverInfo, workspace } from '@/test/fixtures'
import { ApiError } from '@/lib/api'
import { StubSocket } from '@/test/stub-socket'

vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof apiModule>()
  const { fakeApi } = await import('@/test/fixtures')
  return { ...actual, api: fakeApi() }
})

function open(over: Partial<Run> = {}, view?: string) {
  const View = lookupRoute('run')!
  useStore.getState().upsertRun(run(over))
  const params: Record<string, string> = view ? { runId: 'run_1', view } : { runId: 'run_1' }
  useStore.setState({ route: { name: 'run', params } })
  return render(<View params={params} />)
}

/** Acknowledges the agent terminal's attach, with or without the lease. */
function attached(write = false) {
  act(() => {
    const socket = StubSocket.last()
    socket.onopen?.()
    socket.onmessage?.({ data: JSON.stringify({ ok: true, cols: 80, rows: 24, has_control: write, control_generation: write ? 3 : 0 }) })
  })
}

function rerouted(view: string) {
  const View = lookupRoute('run')!
  act(() => useStore.getState().navigate('run', { runId: 'run_1', view }))
  return <View params={useStore.getState().route.params} />
}

beforeEach(() => {
  StubSocket.install()
  useStore.setState({
    runs: {},
    info: serverInfo,
    members: { [alice.id]: alice, [bob.id]: bob },
    workspaces: { [workspace.id]: workspace },
    roomMessages: {},
    roomStatus: {},
    roomStatusControl: {},
    approvalsByRun: {},
    runViewMemory: {},
    sessionLogs: {},
    agentList: null,
    capabilities: { gateway: 'remote', methods: ['*'], ws: ['events', 'attach'] },
  })
})

afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

describe('run frame', () => {
  it('opens a Standard run on its terminal and names the agent and mode', async () => {
    open()
    const views = screen.getByRole('tablist', { name: 'Run views' })
    expect(within(views).getByRole('tab', { name: 'Terminal' }).getAttribute('aria-selected')).toBe('true')
    expect(screen.getByRole('heading', { level: 1, name: 'rewrite the checkout flow' })).toBeDefined()
    await waitFor(() => expect(screen.getByRole('banner').textContent).toContain('Claude Code · Standard'))
    expect(StubSocket.opened).toHaveLength(1)
  })

  it('keeps the agent terminal attached while other views are shown', () => {
    const view = open()
    attached()
    const socket = StubSocket.last()
    view.rerender(rerouted('session'))
    view.rerender(rerouted('changes'))
    view.rerender(rerouted('terminal'))
    expect(StubSocket.opened).toHaveLength(1)
    expect(socket.closed).toBe(false)
    expect(useStore.getState().runViewMemory.run_1).toBe('terminal')
  })

  it('asks for control only once the owner shows the agent terminal', () => {
    const view = open({}, 'session')
    attached()
    const socket = StubSocket.last()
    expect(socket.frames()[0]).not.toMatchObject({ write: true })
    view.rerender(rerouted('changes'))
    expect(socket.frames()).toHaveLength(1)
    view.rerender(rerouted('terminal'))
    expect(socket.frames().at(-1)).toMatchObject({ type: 'control', write: true })
  })

  it('opens an Enhanced run on Session and never attaches an agent terminal', async () => {
    open({ mode: 'acp' })
    const views = screen.getByRole('tablist', { name: 'Run views' })
    expect(within(views).getByRole('tab', { name: 'Session' }).getAttribute('aria-selected')).toBe('true')
    fireEvent.mouseDown(within(views).getByRole('tab', { name: 'Terminal' }))
    expect(await screen.findByText('No agent terminal')).toBeDefined()
    expect(StubSocket.opened).toHaveLength(0)
  })

  it('reads agent.list once across run opens', async () => {
    vi.mocked(api.agentList).mockClear()
    open().unmount()
    await waitFor(() => expect(useStore.getState().agentList).not.toBeNull())
    open()
    expect(api.agentList).toHaveBeenCalledTimes(1)
  })

  it('keeps Changes mounted after a view switch so a Publish draft survives', () => {
    const { rerender } = open({}, 'changes')
    rerender(rerouted('session'))
    const changes = document.querySelector('[role=group][aria-label=Changes]')
    expect(changes?.closest('[inert]')).not.toBeNull()
  })

  it('cycles the views on ] and [', () => {
    open()
    fireEvent.keyDown(window, { key: ']' })
    expect(useStore.getState().route.params.view).toBe('changes')
    fireEvent.keyDown(window, { key: '[' })
    fireEvent.keyDown(window, { key: '[' })
    expect(useStore.getState().route.params.view).toBe('session')
  })
})

describe('switching the mode from More', () => {
  async function openMore() {
    const trigger = screen.getByRole('button', { name: 'More' })
    trigger.focus()
    await userEvent.keyboard('{Enter}')
    return within(await screen.findByRole('menu'))
  }
  const claude = (over = {}) => vi.mocked(api.agentList).mockResolvedValue([
    agentInfo({ display_name: 'Claude Code', switchable: true, enhanced_installed: true, ...over }),
  ])

  it('confirms what happens, then switches', async () => {
    claude()
    vi.mocked(api.runModeSwitch).mockResolvedValueOnce(run({ mode: 'acp', acp: true }))
    open()
    await waitFor(() => expect(useStore.getState().agentList).not.toBeNull())
    fireEvent.click((await openMore()).getByRole('menuitem', { name: 'Switch to Enhanced…' }))
    const dialog = within(await screen.findByRole('alertdialog'))
    expect(dialog.getByText(/restarts in Enhanced mode with the same conversation/)).toBeDefined()
    fireEvent.click(dialog.getByRole('button', { name: 'Switch to Enhanced' }))
    await waitFor(() => expect(api.runModeSwitch).toHaveBeenCalledWith('run_1', 'acp', undefined))
  })

  it('names the missing adapter instead of failing', async () => {
    claude({ enhanced_installed: false })
    open()
    await waitFor(() => expect(useStore.getState().agentList).not.toBeNull())
    expect((await openMore()).getByRole('menuitem', { name: /Enhanced adapter not installed · Set up/ })).toBeDefined()
  })

  it('disables the item with the reason after an early refusal', async () => {
    claude()
    vi.mocked(api.runModeSwitch).mockRejectedValueOnce(new ApiError(200, 'run.mode.switch: scheduler: invalid run state transition: the agent has not reported its session yet', -32002, { reason: 'session_not_reported' }))
    open()
    await waitFor(() => expect(useStore.getState().agentList).not.toBeNull())
    fireEvent.click((await openMore()).getByRole('menuitem', { name: 'Switch to Enhanced…' }))
    fireEvent.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Switch to Enhanced' }))
    await waitFor(() => expect(api.runModeSwitch).toHaveBeenCalled())
    const item = (await openMore()).getByRole('menuitem', { name: /Available after the agent’s first turn/ })
    expect(item.getAttribute('aria-disabled')).toBe('true')
  })

  it('offers no switch for an agent that cannot move a running session', async () => {
    claude({ switchable: false })
    open()
    await waitFor(() => expect(useStore.getState().agentList).not.toBeNull())
    expect((await openMore()).queryByRole('menuitem', { name: /Switch to/ })).toBeNull()
  })
})

describe('the header primary action', () => {
  it('sends a Standard permission to the terminal', () => {
    open({ status: 'needs-attention', pending_inputs: [{ id: 'in_1', session_id: 's', kind: 'permission' }] }, 'session')
    expect(screen.getByText('Permission: answer in the terminal')).toBeDefined()
    fireEvent.click(within(screen.getByRole('toolbar', { name: 'Run actions' })).getByRole('button', { name: 'Open terminal' }))
    expect(useStore.getState().route.params.view).toBe('terminal')
  })

  it('reviews an unseen finish on Changes', () => {
    open({ status: 'completed', outcome_unseen: true })
    fireEvent.click(screen.getByRole('button', { name: 'Review' }))
    expect(useStore.getState().route.params.view).toBe('changes')
  })

  it('answers a teammate question from the Details card it reveals', async () => {
    const question = roomMessage({ id: 'q_1', kind: 'question', actor_id: bob.id, body: 'keep the prefix?' })
    vi.mocked(api.runRoomList).mockResolvedValue({ messages: [question] })
    open({ unanswered_questions: 1 })
    fireEvent.click(await screen.findByRole('button', { name: 'Answer' }))
    const details = await screen.findByRole('dialog', { name: 'Run details' })
    const card = within(details).getByText('Bob asked').closest('[data-slot=request-card]') as HTMLElement
    await userEvent.type(within(card).getByRole('textbox'), 'yes, keep it')
    fireEvent.click(within(card).getByRole('button', { name: 'Reply' }))
    await waitFor(() => expect(api.runRoomPost).toHaveBeenCalledWith(expect.objectContaining({
      kind: 'reply', body: 'yes, keep it', correlation_id: 'q_1',
    })))
  })
})

describe('run details', () => {
  async function details() {
    fireEvent.click(screen.getByRole('button', { name: 'Show details' }))
    return within(await screen.findByRole('dialog', { name: 'Run details' }))
  }

  it('lists what waits on the viewer, the notes and the run record', async () => {
    open({ pending_inputs: [{ id: 'in_1', session_id: 's', kind: 'question' }] })
    const panel = await details()
    expect(panel.getByRole('region', { name: 'Needs you' })).toBeDefined()
    expect(panel.getByText('The agent asks a question')).toBeDefined()
    expect(panel.getByText('Answer in the terminal.')).toBeDefined()
    const facts = panel.getByRole('region', { name: 'Details' })
    expect(within(facts).getByText('Owner')).toBeDefined()
    expect(within(facts).getByText('aether/run-1-checkout')).toBeDefined()
  })

  it('lists a teammate question among the notes', async () => {
    vi.mocked(api.runRoomList).mockResolvedValue({ messages: [roomMessage({ id: 'q_1', kind: 'question', actor_id: bob.id, body: 'which port?' })] })
    open()
    const panel = await details()
    const notes = within(panel.getByRole('region', { name: 'Notes' }))
    expect(await notes.findByText('which port?')).toBeDefined()
    expect(notes.getByText('· question')).toBeDefined()
  })

  it('shows a needs-you condition without a request as one card with its action', async () => {
    vi.mocked(api.runRoomList).mockResolvedValue({ messages: [] })
    open({ status: 'needs-attention', reason: 'agent idle' }, 'terminal')
    const panel = await details()
    const section = within(panel.getByRole('region', { name: 'Needs you' }))
    expect(section.queryByText('Nothing is waiting on you.')).toBeNull()
    expect(section.getByText(/^Agent idle/)).toBeDefined()
    fireEvent.click(section.getByRole('button', { name: 'Reply' }))
    expect(useStore.getState().route.params.view).toBe('session')
  })

  it('lets only the controller decide a teammate message, with its lease', async () => {
    const queued = roomMessage({
      id: 'steer_1', kind: 'steer_request', state: 'queued', actor_id: bob.id, body: 'add a test',
      deliver_after: '2099-01-01T00:00:00Z',
    })
    vi.mocked(api.runRoomList).mockResolvedValue({ messages: [queued] })
    open()
    let panel = await details()
    const card = () => panel.getByText('Bob sent the agent a message').closest('[data-slot=request-card]') as HTMLElement
    await waitFor(() => expect(card()).toBeTruthy())
    expect(within(card()).queryByRole('button', { name: 'Approve' })).toBeNull()
    expect(within(card()).getByRole('button', { name: 'Take control to decide' })).toBeDefined()

    fireEvent.keyDown(document.activeElement ?? document.body, { key: 'Escape' })
    attached(true)
    panel = await details()
    fireEvent.click(within(card()).getByRole('button', { name: 'Approve' }))
    await waitFor(() => expect(api.runRoomDecide).toHaveBeenCalledWith(expect.objectContaining({
      message_id: 'steer_1', decision: 'approve', control_generation: 3,
    })))
  })

  it('adds a note for people, never for the agent', async () => {
    open()
    const panel = await details()
    await userEvent.type(panel.getByRole('textbox', { name: 'Add a note' }), 'reviewing now{Enter}')
    await waitFor(() => expect(api.runRoomPost).toHaveBeenCalledWith(expect.objectContaining({ kind: 'comment', body: 'reviewing now' })))
  })
})

describe('the session composer', () => {
  it('sends on Ctrl+Enter and carries the lease the viewer holds', async () => {
    open({}, 'session')
    attached(true)
    const box = screen.getByRole('textbox', { name: 'Message the agent' })
    expect(screen.getByText('Sends to the agent’s terminal.')).toBeDefined()
    await userEvent.type(box, 'run the tests')
    fireEvent.keyDown(box, { key: 'Enter', ctrlKey: true })
    await waitFor(() => expect(api.runRoomPost).toHaveBeenCalledWith(expect.objectContaining({
      kind: 'steer_request', body: 'run the tests', control_generation: 3,
    })))
    await waitFor(() => expect((box as HTMLTextAreaElement).value).toBe(''))
  })

  it('focuses the composer when the route asks for it', async () => {
    const View = lookupRoute('run')!
    useStore.getState().upsertRun(run())
    const params = { runId: 'run_1', view: 'session', focus: 'composer' }
    useStore.setState({ route: { name: 'run', params } })
    render(<View params={params} />)
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('textbox', { name: 'Message the agent' })))
  })

  it('unwinds Escape from an empty composer to the timeline, then to the board', async () => {
    open({}, 'session')
    const box = screen.getByRole('textbox', { name: 'Message the agent' })
    act(() => box.focus())
    await userEvent.keyboard('{Escape}')
    expect(document.activeElement).not.toBe(box)
    expect(useStore.getState().route.name).toBe('run')
    await userEvent.keyboard('{Escape}')
    expect(useStore.getState().route.name).toBe('board')
  })

  it('says a message waits for delivery when the viewer does not control the run', () => {
    open({}, 'session')
    expect(screen.getByText('Delivers in 45 s unless the controller decides sooner.')).toBeDefined()
  })

  it('shows a failed send at the composer and keeps the draft', async () => {
    vi.mocked(api.runRoomPost).mockRejectedValueOnce(new Error('403 forbidden: steering is not allowed'))
    open({}, 'session')
    const box = screen.getByRole('textbox', { name: 'Message the agent' })
    await userEvent.type(box, 'run the tests')
    fireEvent.keyDown(box, { key: 'Enter', ctrlKey: true })
    expect((await screen.findByRole('alert')).textContent).toContain('403 forbidden: steering is not allowed')
    expect((box as HTMLTextAreaElement).value).toBe('run the tests')
  })

  it.each([
    [{ status: 'completed' }, 'This run has finished. Reopen it from More to message the agent.'],
    [{ status: 'failed', mode: 'headless' }, 'This run has finished.'],
    [{ status: 'provisioning' }, 'The agent is still starting.'],
    [{ member_id: bob.id, protected: true }, 'This run is protected: only its owner or an admin can message the agent.'],
  ] as const)('replaces the composer with the reason it is closed: %j', (over, reason) => {
    useStore.setState({ info: { ...serverInfo, member: { ...alice, role: 'collaborator' } } })
    open(over, 'session')
    expect(screen.getByText(reason)).toBeDefined()
    expect(screen.queryByRole('textbox', { name: 'Message the agent' })).toBeNull()
  })
})
