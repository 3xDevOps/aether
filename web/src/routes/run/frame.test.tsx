import { act, fireEvent, render, renderHook, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type * as apiModule from '@/lib/api'
import { api } from '@/lib/api'
import type { Run } from '@/lib/types'
import { ShortcutsDialog } from '@/components/shortcuts'
import { openRunLink } from '@/routes/browser/open'
import { openInFiles } from '@/routes/files/open'
import { lookupRoute } from '@/routes/registry'
import { useRunRoom } from '@/routes/run/room'
import '@/routes/run'
import { useStore } from '@/store'
import { optimisticDeadlineMs } from '@/store/optimistic'
import { applyEvent } from '@/store/sync'
import { agentInfo, alice, bob, fakeApi, roomMessage, run, serverInfo, workspace } from '@/test/fixtures'
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
    hydrated: true,
    runs: {},
    info: serverInfo,
    members: { [alice.id]: alice, [bob.id]: bob },
    workspaces: { [workspace.id]: workspace },
    roomMessages: {},
    noteDrafts: {},
    noteKeys: {},
    failedNotes: {},
    roomStatus: {},
    roomStatusControl: {},
    runViewMemory: {},
    browserBeside: false,
    browserBesideWidth: null,
    browserRequests: {},
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

  it('names the account a usage fallback moved the run to', async () => {
    open({ account_member_id: bob.id, requested_account_member_id: alice.id })
    await waitFor(() => expect(screen.getByRole('banner').textContent).toContain("On Bob's account (fallback)"))
  })

  it('names the fallback account once a run.account event moves the run', async () => {
    open({ account_member_id: alice.id })
    await act(() => applyEvent(useStore, {
      id: 'evt_1', seq: 1, time: '2026-08-14T11:00:00Z', workspace_id: workspace.id, run_id: 'run_1', actor_id: '',
      type: 'run.account',
      payload: { account_member_id: bob.id, previous_account_member_id: alice.id, requested_account_member_id: alice.id },
    }, api))
    await waitFor(() => expect(screen.getByRole('banner').textContent).toContain("On Bob's account (fallback)"))
  })

  it('keeps the state time on the state line when no reason shows', async () => {
    open({ paused: true })
    await waitFor(() => expect(screen.getByTitle(/^Paused since /)).toBeDefined())
  })

  it('keeps the model and effort out of the state line and in the agent tooltip', async () => {
    open({ model: 'opus', effort: 'high' })
    await waitFor(() => expect(screen.getByTitle('Claude Code · Standard · opus · high')).toBeDefined())
    expect(screen.getByRole('banner').textContent).not.toMatch(/opus|high/)
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

describe('the Trace view', () => {
  const views = () => within(screen.getByRole('tablist', { name: 'Run views' }))

  it('follows Session for an Enhanced run and mounts only while shown', () => {
    const view = open({ mode: 'acp', acp: true })
    expect(views().getAllByRole('tab').map((tab) => tab.textContent)).toEqual(['Session', 'Trace', 'Terminal', 'Changes', 'Files', 'Browser'])
    expect(screen.queryByRole('tree', { name: 'Trace' })).toBeNull()
    view.rerender(rerouted('trace'))
    expect(screen.getByRole('tree', { name: 'Trace' })).toBeDefined()
    expect(screen.getByText('Tool calls')).toBeDefined()
    view.rerender(rerouted('session'))
    expect(screen.queryByRole('tree', { name: 'Trace' })).toBeNull()
  })

  it('is not offered for a Standard run, even by URL', () => {
    open({}, 'trace')
    expect(views().queryByRole('tab', { name: 'Trace' })).toBeNull()
    expect(views().getByRole('tab', { name: 'Terminal' }).getAttribute('aria-selected')).toBe('true')
  })
})

describe('the Files view', () => {
  const views = () => within(screen.getByRole('tablist', { name: 'Run views' }))

  it('sits between Changes and Browser, and stays for a finished run that still has its checkout', () => {
    const live = open()
    expect(views().getAllByRole('tab').map((tab) => tab.textContent)).toEqual(['Session', 'Terminal', 'Changes', 'Files', 'Browser'])
    live.unmount()
    open({ status: 'completed' })
    expect(views().getAllByRole('tab').map((tab) => tab.textContent)).toEqual(['Session', 'Terminal', 'Changes', 'Files'])
  })

  it('is not offered once the checkout is gone or by a gateway that cannot list files', () => {
    const merged = open({ status: 'merged' }, 'files')
    expect(views().queryByRole('tab', { name: 'Files' })).toBeNull()
    expect(views().getByRole('tab', { name: 'Terminal' }).getAttribute('aria-selected')).toBe('true')
    merged.unmount()
    useStore.setState({ capabilities: { gateway: 'remote', methods: ['run.list'], ws: ['events', 'attach'] } })
    open()
    expect(views().queryByRole('tab', { name: 'Files' })).toBeNull()
  })

  it('opens a file a link names without leaving the run, and keeps its editor behind the other views', async () => {
    vi.mocked(api.filesRead).mockResolvedValue({ content: 'one\ntwo\n', truncated: false, binary: false, size: 8, revision: 'r1', writable: true })
    const view = open({}, 'session')
    const View = lookupRoute('run')!
    act(() => openInFiles('run_1', { path: 'notes.md', line: 2 }))
    view.rerender(<View params={useStore.getState().route.params} />)
    expect(views().getByRole('tab', { name: 'Files' }).getAttribute('aria-selected')).toBe('true')
    const editor = await screen.findByRole('textbox', { name: 'Edit notes.md' })
    await waitFor(() => expect(document.querySelector('.cm-marked-line')?.textContent).toBe('two'))

    view.rerender(rerouted('session'))
    expect(editor.isConnected).toBe(true)
    expect(editor.closest('[inert]')).not.toBeNull()
  })

  it('leaves the session’s control strip off, where no lease is used', () => {
    const view = open({ mode: 'acp' }, 'session')
    expect(screen.getByRole('group', { name: 'Multiplayer controls' })).toBeDefined()
    view.rerender(rerouted('files'))
    expect(screen.queryByRole('group', { name: 'Multiplayer controls' })).toBeNull()
  })
})

describe('the Browser view', () => {
  const views = () => within(screen.getByRole('tablist', { name: 'Run views' }))
  /** Lays the run's frame out at `width`, which jsdom never does. */
  function measured(width: number) {
    vi.stubGlobal('ResizeObserver', class {
      constructor(private readonly report: ResizeObserverCallback) {}
      observe(target: Element) {
        this.report([{ target, contentRect: { width, height: 700 } } as unknown as ResizeObserverEntry], this as unknown as ResizeObserver)
      }
      unobserve() {}
      disconnect() {}
    })
  }

  it('is offered for every live run the gateway can serve it for, before any browser has started', () => {
    open()
    expect(views().getByRole('tab', { name: 'Browser' })).toBeDefined()
    expect(api.devBrowserOpen).not.toHaveBeenCalled()
  })

  it('is not offered for a finished run or by a gateway without the browser', () => {
    const finished = open({ status: 'completed' })
    expect(views().queryByRole('tab', { name: 'Browser' })).toBeNull()
    finished.unmount()
    useStore.setState({ capabilities: { gateway: 'remote', methods: ['run.list'], ws: ['events', 'attach'] } })
    open()
    expect(views().queryByRole('tab', { name: 'Browser' })).toBeNull()
  })

  it('leaves the session’s control strip off the Browser view', async () => {
    const view = open({ mode: 'acp' }, 'session')
    expect(await screen.findByRole('group', { name: 'Multiplayer controls' })).toBeDefined()
    view.rerender(rerouted('browser'))
    expect(screen.queryByRole('group', { name: 'Multiplayer controls' })).toBeNull()
    expect(screen.getByRole('toolbar', { name: 'Browser' })).toBeDefined()
  })

  it('takes a run-local terminal link, and leaves every other link alone', () => {
    const view = open()
    expect(openRunLink('run_1', 'https://example.com/docs')).toBe(false)
    expect(openRunLink('run_missing', 'http://localhost:3000/')).toBe(false)
    let taken = false
    act(() => { taken = openRunLink('run_1', 'http://localhost:3000/app') })
    expect(taken).toBe(true)
    expect(useStore.getState().route.params.view).toBe('browser')
    view.unmount()
    open({ status: 'completed' })
    expect(openRunLink('run_1', 'http://localhost:3000/app')).toBe(false)
  })

  it('shows the Browser for the address-bar shortcut from another view, but not from a terminal', () => {
    const view = open({ mode: 'acp' }, 'session')
    const inShell = document.createElement('div')
    inShell.className = 'xterm'
    inShell.tabIndex = 0
    document.body.append(inShell)
    const kept = new KeyboardEvent('keydown', { key: 'l', ctrlKey: true, bubbles: true, cancelable: true })
    act(() => { inShell.dispatchEvent(kept) })
    expect(kept.defaultPrevented).toBe(false)
    expect(useStore.getState().route.params.view).toBe('session')
    inShell.remove()

    const press = new KeyboardEvent('keydown', { key: 'l', ctrlKey: true, bubbles: true, cancelable: true })
    act(() => { document.body.dispatchEvent(press) })
    expect(press.defaultPrevented).toBe(true)
    expect(useStore.getState().route.params.view).toBe('browser')
    expect(useStore.getState().browserRequests).toEqual({ run_1: '' })
    view.unmount()
  })

  it('sits beside the other views on a wide frame, from one remembered toggle', async () => {
    measured(1200)
    const view = open({ mode: 'acp' }, 'browser')
    fireEvent.click(await screen.findByRole('button', { name: 'Show beside Session' }))
    expect(useStore.getState().browserBeside).toBe(true)
    view.rerender(rerouted(useStore.getState().route.params.view!))
    expect(views().queryByRole('tab', { name: 'Browser' })).toBeNull()
    expect(views().getByRole('tab', { name: 'Session' }).getAttribute('aria-selected')).toBe('true')
    const beside = within(screen.getByRole('region', { name: 'Browser' }))
    expect(beside.getByRole('toolbar', { name: 'Browser' })).toBeDefined()
    expect(screen.getByRole('group', { name: 'Multiplayer controls' })).toBeDefined()

    const divider = screen.getByRole('separator', { name: 'Resize the Browser' })
    expect(divider.getAttribute('aria-valuenow')).toBe('600')
    fireEvent.keyDown(divider, { key: 'ArrowLeft' })
    expect(useStore.getState().browserBesideWidth).toBe(616)
    fireEvent.keyDown(divider, { key: 'End' })
    expect(useStore.getState().browserBesideWidth).toBe(880)

    act(() => { openRunLink('run_1', 'http://localhost:3000/') })
    expect(useStore.getState().route.params.view).toBe('session')

    fireEvent.click(beside.getByRole('button', { name: 'Show the Browser as a tab' }))
    view.rerender(rerouted('browser'))
    expect(useStore.getState().browserBeside).toBe(false)
    expect(views().getByRole('tab', { name: 'Browser' }).getAttribute('aria-selected')).toBe('true')
    expect(screen.queryByRole('separator', { name: 'Resize the Browser' })).toBeNull()
  })

  it('stays a tab, with no toggle, on a frame too narrow for two panes', async () => {
    measured(600)
    useStore.setState({ browserBeside: true })
    open({}, 'browser')
    expect(views().getByRole('tab', { name: 'Browser' }).getAttribute('aria-selected')).toBe('true')
    expect(await screen.findByRole('toolbar', { name: 'Browser' })).toBeDefined()
    expect(screen.queryByRole('button', { name: /Show beside|as a tab/ })).toBeNull()
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

  it('reviews an unseen failure on Changes', () => {
    open({ status: 'needs-attention', state: 'failed', outcome_unseen: true })
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

  it('shows the size of the run\'s own container and no other', async () => {
    useStore.setState({
      info: {
        ...serverInfo,
        disk: {
          ...serverInfo.disk!,
          containers: [
            { owner_kind: 'run', owner_id: 'run_2', bytes: 9 * 1024 * 1024 * 1024 },
            { owner_kind: 'run', owner_id: 'run_1', bytes: 3 * 1024 * 1024 * 1024 },
          ],
        },
      },
    })
    open()
    const facts = within((await details()).getByRole('region', { name: 'Details' }))
    expect(facts.getByText('Container size').nextElementSibling?.textContent).toBe('3.0 GB outside the mounted home, checkout and cache')
  })

  it('lists a teammate question among the notes', async () => {
    vi.mocked(api.runRoomList).mockResolvedValue({ messages: [roomMessage({ id: 'q_1', kind: 'question', actor_id: bob.id, body: 'which port?' })] })
    open()
    const panel = await details()
    const notes = within(panel.getByRole('region', { name: 'Notes' }))
    expect(await notes.findByText('which port?')).toBeDefined()
    expect(notes.getByText('question')).toBeDefined()
  })

  it('shows a needs-you condition without a request as one card with its action', async () => {
    vi.mocked(api.runRoomList).mockResolvedValue({ messages: [] })
    open({ status: 'needs-attention', state: 'needs-you', reason: 'needs input: Which port?' }, 'terminal')
    const panel = await details()
    const section = within(panel.getByRole('region', { name: 'Needs you' }))
    expect(section.queryByText('Nothing is waiting on you.')).toBeNull()
    expect(section.getByText('Agent asks: Which port?')).toBeDefined()
    fireEvent.click(section.getByRole('button', { name: 'Reply' }))
    expect(useStore.getState().route.params.view).toBe('session')
  })

  it('leads the workflow with the request that needs the viewer, and leaves the sheet for it', async () => {
    vi.mocked(api.runRoomList).mockResolvedValue({ messages: [] })
    open({ status: 'needs-attention', state: 'needs-you', reason: 'needs input: Which port?' }, 'terminal')
    fireEvent.click(screen.getByRole('button', { name: 'Workflow: Needs you' }))
    const sheet = within(await screen.findByRole('dialog', { name: 'Workflow' }))
    const request = sheet.getByRole('treeitem', { name: /^Agent asks: Which port\?, Needs you/ })
    expect(request.getAttribute('aria-level')).toBe('2')
    fireEvent.click(request)
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Workflow' })).toBeNull())
    expect(useStore.getState().route.params.view).toBe('session')
  })

  it('shows a Failed run that needs review as its card', async () => {
    vi.mocked(api.runRoomList).mockResolvedValue({ messages: [] })
    open({ status: 'needs-attention', state: 'failed', reason: 'enhanced turn failed: EOF', outcome_unseen: true }, 'terminal')
    const section = within((await details()).getByRole('region', { name: 'Needs you' }))
    expect(section.queryByText('Nothing is waiting on you.')).toBeNull()
    expect(section.getByText('Turn failed: EOF')).toBeDefined()
    expect(section.getByRole('button', { name: 'Review' })).toBeDefined()
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

  async function waitingCards() {
    vi.mocked(api.runRoomList).mockResolvedValue({ messages: [
      roomMessage({ id: 'q_1', kind: 'question', actor_id: bob.id, body: 'keep the prefix?' }),
      roomMessage({ id: 'steer_1', kind: 'steer_request', state: 'queued', actor_id: bob.id, body: 'add a test', deliver_after: '2099-01-01T00:00:00Z' }),
    ] })
    open()
    attached(true)
    const panel = await details()
    const card = (title: string) => within(panel.getByText(title).closest('[data-slot=request-card]') as HTMLElement)
    await waitFor(() => card('Bob asked'))
    return { panel, question: card('Bob asked'), steer: card('Bob sent the agent a message') }
  }
  const never = () => new Promise<never>(() => {})

  it('keeps Reply and a teammate message\'s Approve live while a note is sending', async () => {
    const post = heldPost()
    const { panel, question, steer } = await waitingCards()
    await userEvent.type(panel.getByRole('textbox', { name: 'Add a note' }), 'reviewing now{Enter}')
    await userEvent.type(question.getByRole('textbox'), 'yes')
    expect(question.getByRole('button', { name: 'Reply' })).toHaveProperty('disabled', false)
    expect(steer.getByRole('button', { name: 'Approve' })).toHaveProperty('disabled', false)
    await act(async () => post.resolve({ message: savedNote() }))
  })

  it('shows Replying… on the reply alone while it is sent', async () => {
    const { question, steer } = await waitingCards()
    vi.mocked(api.runRoomPost).mockImplementationOnce(never)
    await userEvent.type(question.getByRole('textbox'), 'yes')
    fireEvent.click(question.getByRole('button', { name: 'Reply' }))
    expect(question.getByRole('button', { name: 'Replying…' })).toHaveProperty('disabled', true)
    expect(steer.getByRole('button', { name: 'Approve' })).toHaveProperty('disabled', false)
  })

  it('shows Denying… on the pressed decision, then still waiting past the deadline with no Cancel', async () => {
    const { question, steer } = await waitingCards()
    vi.mocked(api.runRoomDecide).mockImplementationOnce(never)
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    try {
      fireEvent.click(steer.getByRole('button', { name: 'Deny' }))
      expect(steer.getByRole('button', { name: 'Denying…' })).toHaveProperty('disabled', true)
      expect(steer.getByRole('button', { name: 'Approve' })).toHaveProperty('disabled', true)
      fireEvent.change(question.getByRole('textbox'), { target: { value: 'yes' } })
      expect(question.getByRole('button', { name: 'Reply' })).toHaveProperty('disabled', false)
      act(() => vi.advanceTimersByTime(optimisticDeadlineMs))
      expect(steer.getByRole('button', { name: 'Still waiting for the server…' })).toHaveProperty('disabled', true)
      expect(steer.getByRole('button', { name: 'Approve' })).toHaveProperty('disabled', true)
      expect(steer.queryByRole('button', { name: 'Cancel' })).toBeNull()
    } finally {
      vi.useRealTimers()
    }
  })

  type Posted = { message: ReturnType<typeof roomMessage> }
  function heldPost() {
    const held = { resolve: (_: Posted) => {}, reject: (_: Error) => {} }
    vi.mocked(api.runRoomPost).mockImplementationOnce(() => new Promise<Posted>((resolve, reject) => Object.assign(held, { resolve, reject })))
    return held
  }
  const refusal = (text: string) => new ApiError(403, text, -32001)
  const savedNote = () => roomMessage({
    id: 'note_1', kind: 'comment', body: 'reviewing now', idempotency_key: vi.mocked(api.runRoomPost).mock.lastCall![0].idempotency_key,
  })

  it('shows a note at once as sending, then as saved once', async () => {
    const post = heldPost()
    open()
    const panel = await details()
    const box = panel.getByRole('textbox', { name: 'Add a note' })
    await userEvent.type(box, 'reviewing now{Enter}')
    const notes = within(panel.getByRole('region', { name: 'Notes' }))
    expect(box).toHaveProperty('value', '')
    expect(notes.getByText('reviewing now')).toBeDefined()
    expect(notes.getByText('Sending…')).toBeDefined()
    expect(api.runRoomPost).toHaveBeenCalledWith(expect.objectContaining({ kind: 'comment', body: 'reviewing now' }))
    await act(async () => post.resolve({ message: savedNote() }))
    expect(notes.getAllByText('reviewing now')).toHaveLength(1)
    expect(notes.queryByText('Sending…')).toBeNull()
  })

  it('takes a refused note back into the input and shows the server error', async () => {
    vi.mocked(api.runRoomPost).mockRejectedValueOnce(refusal('run.room.post: 403 forbidden: notes are closed on this run'))
    open()
    const panel = await details()
    const box = panel.getByRole('textbox', { name: 'Add a note' })
    await userEvent.type(box, 'reviewing now{Enter}')
    expect((await panel.findByRole('alert')).textContent).toContain('403 forbidden: notes are closed on this run')
    expect(box).toHaveProperty('value', 'reviewing now')
    expect(within(panel.getByRole('region', { name: 'Notes' })).getAllByText('reviewing now')).toHaveLength(1)
  })

  it('keeps a note refused while Details was closed, with its text and error', async () => {
    const post = heldPost()
    open()
    let panel = await details()
    await userEvent.type(panel.getByRole('textbox', { name: 'Add a note' }), 'reviewing now{Enter}')
    fireEvent.keyDown(document.activeElement ?? document.body, { key: 'Escape' })
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Run details' })).toBeNull())
    await act(async () => post.reject(refusal('run.room.post: 403 forbidden: notes are closed on this run')))
    panel = await details()
    expect(panel.getByRole('alert').textContent).toContain('403 forbidden: notes are closed on this run')
    expect(panel.getByRole('textbox', { name: 'Add a note' })).toHaveProperty('value', 'reviewing now')
  })

  it('drops a note\'s refusal that arrives after another member signed in', async () => {
    const post = heldPost()
    useStore.setState({ identityKey: 'alice' })
    open()
    const panel = await details()
    await userEvent.type(panel.getByRole('textbox', { name: 'Add a note' }), 'reviewing now{Enter}')
    expect(panel.getByText('reviewing now')).toBeDefined()
    act(() => {
      useStore.setState({ identityKey: 'bob' })
      useStore.getState().resetNotes()
    })
    expect((await details()).queryByText('reviewing now')).toBeNull()
    await act(async () => post.reject(refusal('run.room.post: 403 forbidden: notes are closed on this run')))
    expect(useStore.getState().failedNotes).toEqual({})
    expect(useStore.getState().noteKeys).toEqual({})
  })

  it('keeps the text of every refused note', async () => {
    const first = heldPost()
    const second = heldPost()
    open()
    const panel = await details()
    const box = panel.getByRole('textbox', { name: 'Add a note' })
    await userEvent.type(box, 'first note{Enter}')
    await userEvent.type(box, 'second note{Enter}')
    await userEvent.type(box, 'still typing')
    await act(async () => first.reject(refusal('403 forbidden: first')))
    await act(async () => second.reject(refusal('403 forbidden: second')))
    expect(panel.getAllByRole('alert')).toHaveLength(2)
    const notes = within(panel.getByRole('region', { name: 'Notes' }))
    expect(notes.getByText('first note')).toBeDefined()
    expect(notes.getByText('second note')).toBeDefined()
    expect(box).toHaveProperty('value', 'still typing')
  })

  it('keeps the key of a note whose post got no answer, so Add again does not post it twice', async () => {
    vi.mocked(api.runRoomPost).mockRejectedValueOnce(new TypeError('Failed to fetch'))
    const resent = heldPost()
    open()
    const panel = await details()
    const box = panel.getByRole('textbox', { name: 'Add a note' })
    await userEvent.type(box, 'reviewing now{Enter}')
    expect((await panel.findByRole('alert')).textContent).toContain('Failed to fetch')
    expect(panel.getByRole('button', { name: 'Retry' })).toBeDefined()
    expect(box).toHaveProperty('value', 'reviewing now')
    fireEvent.submit(box.closest('form')!)
    const [first, second] = vi.mocked(api.runRoomPost).mock.calls.slice(-2)
    expect(second![0].idempotency_key).toBe(first![0].idempotency_key)
    await act(async () => resent.resolve({ message: savedNote() }))
  })

  it('resends a refused note under its first key, since a refused post may still have been saved', async () => {
    vi.mocked(api.runRoomPost).mockRejectedValueOnce(refusal('run.room.post: publish: event bus closed'))
    const resent = heldPost()
    open()
    const panel = await details()
    const box = panel.getByRole('textbox', { name: 'Add a note' })
    await userEvent.type(box, 'reviewing now{Enter}')
    expect((await panel.findByRole('alert')).textContent).toContain('event bus closed')
    fireEvent.submit(box.closest('form')!)
    const [first, second] = vi.mocked(api.runRoomPost).mock.calls.slice(-2)
    expect(second![0].idempotency_key).toBe(first![0].idempotency_key)
    await act(async () => resent.resolve({ message: savedNote() }))
  })

  it('sends a note whose text names an Object property under a fresh string key', async () => {
    open()
    const panel = await details()
    const box = panel.getByRole('textbox', { name: 'Add a note' })
    await userEvent.type(box, 'reviewing now{Enter}')
    await userEvent.type(box, 'constructor{Enter}')
    expect(vi.mocked(api.runRoomPost).mock.lastCall![0].idempotency_key).toEqual(expect.any(String))
  })

  it('resends a dismissed unanswered note under its key, so its late answer shows once', async () => {
    const late = heldPost()
    const resent = heldPost()
    open()
    const panel = await details()
    const box = panel.getByRole('textbox', { name: 'Add a note' })
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    try {
      fireEvent.change(box, { target: { value: 'reviewing now' } })
      fireEvent.submit(box.closest('form')!)
      act(() => vi.advanceTimersByTime(optimisticDeadlineMs))
      await act(async () => {})
      fireEvent.click(within(panel.getByRole('region', { name: 'Notes' })).getByRole('button', { name: 'Dismiss' }))
      fireEvent.submit(box.closest('form')!)
      const [first, second] = vi.mocked(api.runRoomPost).mock.calls.slice(-2)
      expect(second![0].idempotency_key).toBe(first![0].idempotency_key)
      await act(async () => late.resolve({ message: savedNote() }))
      await act(async () => resent.resolve({ message: savedNote() }))
      expect(within(panel.getByRole('region', { name: 'Notes' })).getAllByText('reviewing now')).toHaveLength(1)
      expect(panel.queryByRole('alert')).toBeNull()
    } finally {
      vi.useRealTimers()
    }
  })

  it('replaces an unanswered note\'s timeout with the refusal that comes later', async () => {
    const post = heldPost()
    open()
    const panel = await details()
    const box = panel.getByRole('textbox', { name: 'Add a note' })
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    try {
      fireEvent.change(box, { target: { value: 'reviewing now' } })
      fireEvent.submit(box.closest('form')!)
      act(() => vi.advanceTimersByTime(optimisticDeadlineMs))
      await act(async () => {})
      await act(async () => post.reject(refusal('run.room.post: 403 forbidden: notes are closed on this run')))
      expect(panel.getByRole('alert').textContent).toContain('403 forbidden: notes are closed on this run')
      expect(panel.queryByRole('button', { name: 'Retry' })).toBeNull()
      expect(box).toHaveProperty('value', 'reviewing now')
    } finally {
      vi.useRealTimers()
    }
  })

  it('keeps a note\'s key while a retry is unanswered, even once the first post is refused', async () => {
    const first = heldPost()
    heldPost()
    open()
    const panel = await details()
    const box = panel.getByRole('textbox', { name: 'Add a note' })
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    try {
      fireEvent.change(box, { target: { value: 'reviewing now' } })
      fireEvent.submit(box.closest('form')!)
      act(() => vi.advanceTimersByTime(optimisticDeadlineMs))
      await act(async () => {})
      fireEvent.click(panel.getByRole('button', { name: 'Retry' }))
      await act(async () => first.reject(refusal('run.room.post: 503 busy')))
      act(() => vi.advanceTimersByTime(optimisticDeadlineMs))
      await act(async () => {})
      fireEvent.click(panel.getByRole('button', { name: 'Retry' }))
      const keys = vi.mocked(api.runRoomPost).mock.calls.slice(-3).map(([post]) => post.idempotency_key)
      expect(new Set(keys).size).toBe(1)
    } finally {
      vi.useRealTimers()
    }
  })

  it('shows no error for an unanswered note whose saved copy already arrived', async () => {
    heldPost()
    open()
    const panel = await details()
    const box = panel.getByRole('textbox', { name: 'Add a note' })
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    try {
      fireEvent.change(box, { target: { value: 'reviewing now' } })
      fireEvent.submit(box.closest('form')!)
      act(() => useStore.getState().upsertRoomMessage(savedNote()))
      act(() => vi.advanceTimersByTime(optimisticDeadlineMs))
      await act(async () => {})
      expect(panel.queryByRole('alert')).toBeNull()
      expect(within(panel.getByRole('region', { name: 'Notes' })).getAllByText('reviewing now')).toHaveLength(1)
      expect(box).toHaveProperty('value', '')
    } finally {
      vi.useRealTimers()
    }
  })

  it('reverts an unanswered note with Retry, then shows a late answer once without the error', async () => {
    const post = heldPost()
    open()
    const panel = await details()
    const box = panel.getByRole('textbox', { name: 'Add a note' })
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    try {
      fireEvent.change(box, { target: { value: 'reviewing now' } })
      fireEvent.submit(box.closest('form')!)
      act(() => vi.advanceTimersByTime(optimisticDeadlineMs))
      await act(async () => {})
      expect(panel.getByRole('alert').textContent).toContain('The server did not answer in 5 seconds.')
      expect(panel.getByRole('button', { name: 'Retry' })).toBeDefined()
      expect(box).toHaveProperty('value', 'reviewing now')
      const notes = within(panel.getByRole('region', { name: 'Notes' }))
      expect(notes.getAllByText('reviewing now')).toHaveLength(1)

      await act(async () => post.resolve({ message: savedNote() }))
      act(() => useStore.getState().upsertRoomMessage(savedNote()))
      const { idempotency_key: key } = savedNote()
      act(() => useStore.getState().upsertRoomMessage({ ...savedNote(), id: key! }))
      expect(notes.getAllByText('reviewing now')).toHaveLength(1)
      expect(panel.queryByRole('alert')).toBeNull()
      expect(box).toHaveProperty('value', '')
    } finally {
      vi.useRealTimers()
    }
  })

  it('keeps the server order of same-instant notes across a reconnect walk of several pages', async () => {
    const note = (id: string) => roomMessage({ id })
    const client = fakeApi({
      runRoomList: vi.fn()
        .mockResolvedValueOnce({ messages: [note('a')] })
        .mockResolvedValueOnce({ messages: [note('d'), note('c')], next_before: 'c' })
        .mockResolvedValueOnce({ messages: [note('b'), note('a')] }),
    })
    const { result } = renderHook(() => useRunRoom(run(), undefined, client))
    await waitFor(() => expect(useStore.getState().roomMessages.run_1).toHaveLength(1))
    await act(() => result.current.refresh())
    expect(useStore.getState().roomMessages.run_1.map((m) => m.id)).toEqual(['a', 'b', 'c', 'd'])
  })
})

describe('the session composer', () => {
  it('sends on Enter and carries the lease the viewer holds', async () => {
    open({}, 'session')
    attached(true)
    const box = screen.getByRole('textbox', { name: 'Message the agent' })
    expect(screen.getByText('Sends to the agent’s terminal.')).toBeDefined()
    const tooMany = Array.from({ length: 9 }, (_, index) => new File(['png'], `extra-${index}.png`, { type: 'image/png' }))
    await userEvent.upload(screen.getByLabelText('Choose files to attach'), tooMany)
    expect(screen.getByRole('alert').textContent).toContain('Attach at most 8 files.')
    await userEvent.type(box, 'run the tests')
    const posts = vi.mocked(api.runRoomPost).mock.calls.length
    fireEvent.keyDown(box, { key: 'Enter' })
    await waitFor(() => expect(api.runRoomPost).toHaveBeenCalledWith(expect.objectContaining({
      kind: 'steer_request', body: 'run the tests', control_generation: 3,
    })))
    await waitFor(() => expect((box as HTMLTextAreaElement).value).toBe(''))
    expect(vi.mocked(api.runRoomPost).mock.calls.length).toBe(posts + 1)
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('lists Enter as Send in the shortcuts reference while the composer has focus', async () => {
    open({}, 'session')
    attached(true)
    render(<ShortcutsDialog />)
    act(() => screen.getByRole('textbox', { name: 'Message the agent' }).focus())
    act(() => useStore.getState().setShortcutsOpen(true))
    const reference = await screen.findByRole('dialog', { name: 'Keyboard shortcuts' })
    expect(within(reference).getByText('Send the message, or steer the running turn')).toBeDefined()
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
    fireEvent.keyDown(box, { key: 'Enter' })
    expect((await screen.findByRole('alert')).textContent).toContain('403 forbidden: steering is not allowed')
    expect((box as HTMLTextAreaElement).value).toBe('run the tests')
  })

  it('waits for a send that outlives it, and sends a failed one again under the same key', async () => {
    const View = lookupRoute('run')!
    let settle = { resolve: (_: { message: ReturnType<typeof roomMessage> }) => {}, reject: (_: Error) => {} }
    const pending = () => new Promise<{ message: ReturnType<typeof roomMessage> }>((resolve, reject) => { settle = { resolve, reject } })
    vi.mocked(api.runRoomPost).mockReset().mockImplementationOnce(pending).mockImplementationOnce(pending)
    const view = open({}, 'session')
    await userEvent.type(screen.getByRole('textbox', { name: 'Message the agent' }), 'run the tests')
    await userEvent.click(screen.getByRole('button', { name: 'Send' }))
    view.rerender(<p>Board</p>)
    view.rerender(<View params={{ runId: 'run_1', view: 'session' }} />)
    const waiting = screen.getByRole('textbox', { name: 'Message the agent' })
    expect(waiting).toMatchObject({ value: 'run the tests', readOnly: true })
    expect(screen.getByRole('button', { name: 'Sending…' })).toHaveProperty('disabled', true)
    fireEvent.keyDown(waiting, { key: 'Enter' })
    act(() => settle.reject(new Error('403 forbidden: steering is not allowed')))
    await waitFor(() => expect(waiting).toMatchObject({ value: 'run the tests', readOnly: false }))

    await userEvent.click(screen.getByRole('button', { name: 'Send' }))
    act(() => settle.resolve({ message: roomMessage() }))
    await waitFor(() => expect(waiting).toHaveProperty('value', ''))
    const [failed, resent] = vi.mocked(api.runRoomPost).mock.calls
    expect(vi.mocked(api.runRoomPost)).toHaveBeenCalledTimes(2)
    expect(resent![0].idempotency_key).toBe(failed![0].idempotency_key)
  })

  it('says it is still waiting past the deadline, and Cancel unlocks the draft for a resend under the same key', async () => {
    const hang = () => new Promise<never>(() => {})
    vi.mocked(api.runRoomPost).mockReset().mockImplementationOnce(hang).mockImplementationOnce(hang)
    open({}, 'session')
    const box = screen.getByRole('textbox', { name: 'Message the agent' })
    await userEvent.type(box, 'run the tests')
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    try {
      fireEvent.keyDown(box, { key: 'Enter' })
      expect(screen.getByRole('button', { name: 'Sending…' })).toHaveProperty('disabled', true)
      act(() => vi.advanceTimersByTime(optimisticDeadlineMs))
      expect(screen.getByRole('button', { name: 'Still waiting for the server…' })).toHaveProperty('disabled', true)
      fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
      expect(box).toMatchObject({ value: 'run the tests', readOnly: false })
      fireEvent.keyDown(box, { key: 'Enter' })
      const [first, second] = vi.mocked(api.runRoomPost).mock.calls
      expect(second![0].idempotency_key).toBe(first![0].idempotency_key)
    } finally {
      vi.useRealTimers()
    }
  })

  it.each([
    { status: 'completed' },
    { status: 'failed', mode: 'headless' },
    { status: 'provisioning' },
    { member_id: bob.id, protected: true },
  ] as const)('does not accept messages when the run is unavailable: %j', (over) => {
    useStore.setState({ info: { ...serverInfo, member: { ...alice, role: 'collaborator' } } })
    open(over, 'session')
    expect(screen.queryByRole('textbox', { name: 'Message the agent' })).toBeNull()
  })
})
