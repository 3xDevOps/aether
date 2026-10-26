import { act, fireEvent, render, renderHook, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { toast } from 'sonner'
import { PaletteDialogs } from '@/components/palette/dialogs'
import { api, ApiError } from '@/lib/api'
import type { Event, GatewayCapabilities, Run, TimelinePage } from '@/lib/types'
import { Board } from '@/routes/board'
import { virtualizeAbove } from '@/routes/board/card-list'
import { EdgePopover } from '@/routes/board/lanes/lane-popover'
import { useBoard } from '@/routes/board/selectors'
import '@/routes/diff/conflict-chips'
import '@/routes/missions'
import { useStore } from '@/store'
import { historyBudget } from '@/store/lanes'
import { optimisticDeadlineMs } from '@/store/optimistic'
import { toRecord } from '@/store/runs'
import { applyEvent } from '@/store/sync'
import {
  alice,
  bob,
  fakeApi,
  mission,
  otherWorkspace,
  pendingDevice,
  roomMessage,
  run,
  serverInfo,
  workspace,
} from '@/test/fixtures'
import { sizedViewport } from '@/test/virtual'

vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api')>()
  const { fakeApi } = await import('@/test/fixtures')
  return { ...actual, api: fakeApi() }
})

vi.mock('sonner', () => ({
  toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() },
}))

/** jsdom lays nothing out; the board learns its width from ResizeObserver. */
function atBoardWidth(width: number) {
  vi.stubGlobal('ResizeObserver', class {
    constructor(private readonly report: ResizeObserverCallback) {}
    observe(target: Element) {
      this.report([{ target, contentRect: { width, height: 800 } } as unknown as ResizeObserverEntry], this as unknown as ResizeObserver)
    }
    unobserve() {}
    disconnect() {}
  })
  onTestFinished(() => {
    vi.unstubAllGlobals()
  })
}

const everyMethod: GatewayCapabilities = { gateway: 'remote', methods: ['*'], ws: [] }

function seed(runs: Run[], { active = workspace.id, self = alice } = {}) {
  useStore.setState({
    workspaces: { [workspace.id]: workspace, [otherWorkspace.id]: otherWorkspace },
    activeWorkspace: active,
    capabilities: everyMethod,
    info: { ...serverInfo, member: self },
    members: { [alice.id]: alice, [bob.id]: bob },
    devices: [],
    runs: Object.fromEntries(runs.map((r) => [r.id, toRecord(r)])),
    agentList: null,
    pausedRuns: {},
    roomMessages: {},
    roomStatus: {},
    missions: {},
    missionDetails: {},
    diffs: {},
    overlaps: {},
    mineOnly: false,
    boardViews: {},
    boardMapAllWorkspaces: false,
    boardMapViewports: {},
    hydrated: true,
    hydrationError: null,
    paletteDialog: null,
    lastSeq: 0,
    route: { name: 'board', params: {} },
  })
}

function renderBoard() {
  return render(
    <>
      <Board />
      <PaletteDialogs />
    </>,
  )
}

function column(label: string) {
  return within(screen.getByRole('region', { name: label }))
}

function cardOf(task: string) {
  const title = screen.getByRole('button', { name: task })
  return within(title.closest('article')!)
}

const stopped = run({ id: 'run_stopped', task: 'waiting on you', status: 'needs-attention', state: 'needs-you', reason: 'needs input: Which port?' })
const working = run({ id: 'run_working', task: 'still going', status: 'running' })
const queued = run({
  id: 'run_queued',
  task: 'not started',
  status: 'queued',
  started_at: null,
  created_at: '2026-08-14T11:00:00Z',
})
const merged = run({ id: 'run_merged', task: 'landed already', status: 'merged', finished_at: '2026-08-14T10:30:00Z' })
const failed = run({ id: 'run_failed', task: 'broke', status: 'failed', reason: 'exit 1', finished_at: '2026-08-14T10:20:00Z' })
const archivedMerged = run({
  id: 'run_archived',
  task: 'already archived',
  status: 'merged',
  finished_at: '2020-08-10T10:30:00Z',
  archived_at: '2020-08-14T10:00:00Z',
})

function timeline(kind: 'pause' | 'resume', seq: number) {
  applyEvent(
    useStore,
    {
      id: `evt_${seq}`, seq, time: '2026-08-14T11:30:00Z', workspace_id: workspace.id,
      run_id: working.id, actor_id: alice.id, type: 'workspace.timeline', payload: { kind },
    },
    fakeApi(),
  )
}

beforeEach(() => vi.clearAllMocks())

describe('board columns', () => {
  it('deals runs into Needs you, Working and Finished with a count each', () => {
    seed([stopped, working, queued, merged, failed])
    renderBoard()

    expect(column('Needs you').getByText('waiting on you')).toBeDefined()
    expect(screen.getByRole('heading', { name: 'Needs you 1' })).toBeDefined()
    expect(screen.getByRole('heading', { name: 'Working 2' })).toBeDefined()
    expect(screen.getByRole('heading', { name: 'Finished 2' })).toBeDefined()
    const order = (label: string) =>
      column(label).getAllByRole('article').map((article) => article.querySelector('[data-card-open]')!.textContent)
    expect(order('Working')).toEqual(['not started', 'still going'])
    expect(order('Finished')).toEqual(['broke', 'landed already'])
  })

  it('draws a card as a title and a status footer, and nothing else', async () => {
    seed([merged])
    renderBoard()
    const merge = cardOf('landed already')

    expect(merge.getByText('Merged')).toBeDefined()
    expect(merge.getByRole('img', { name: 'Alice' })).toBeDefined()
    await waitFor(() => expect(merge.getByText('Claude Code')).toBeDefined())
    expect(merge.queryByText(/Details|Protected|tui/)).toBeNull()
    expect(merge.getAllByRole('button')).toHaveLength(1)
    expect(merge.getByRole('img', { name: 'Merged' }).closest('article')!.querySelector('p')).toBeNull()
  })

  it('adds the diff counts once a snapshot names them', () => {
    seed([working])
    useStore.getState().noteDiffSnapshot(working.id, {
      time: '2026-08-14T10:10:00Z',
      files: [{ path: 'a.ts', additions: 12, deletions: 3 }, { path: 'b.ts', additions: 1, deletions: 0 }],
    })
    renderBoard()

    expect(cardOf('still going').getByText('+13')).toBeDefined()
    expect(cardOf('still going').getByText('−3')).toBeDefined()
  })

  it('counts the newest snapshot, not the first one it saw', () => {
    seed([working])
    useStore.getState().noteDiffSnapshot(working.id, {
      time: '2026-08-14T10:10:00Z',
      files: [{ path: 'a.ts', additions: 900, deletions: 0 }],
    })
    useStore.getState().noteDiffSnapshot(working.id, {
      time: '2026-08-14T10:11:00Z',
      files: [{ path: 'a.ts', additions: 4, deletions: 1 }],
    })
    renderBoard()

    expect(cardOf('still going').getByText('+4')).toBeDefined()
    expect(cardOf('still going').queryByText('+900')).toBeNull()
  })

  it('shows the file count alone for a snapshot too large to count', () => {
    seed([working])
    useStore.getState().noteDiffSnapshot(working.id, {
      time: '2026-08-14T10:10:00Z',
      files: [{ path: 'a.ts', additions: 12, deletions: 3 }, { path: 'b.ts', additions: 1, deletions: 0 }],
      truncated: true,
    })
    renderBoard()

    expect(cardOf('still going').getByText('2 files')).toBeDefined()
    expect(cardOf('still going').queryByText('+13')).toBeNull()
  })

  it('opens the run from anywhere on the card', () => {
    seed([working])
    renderBoard()
    fireEvent.click(screen.getByRole('button', { name: 'still going' }))
    expect(useStore.getState().route).toEqual({ name: 'run', params: { runId: working.id } })
  })

  it('lists what needs the viewer from every workspace, naming the other one', () => {
    seed([working, run({ ...stopped, workspace_id: otherWorkspace.id })])
    renderBoard()

    expect(cardOf('waiting on you').getByText(otherWorkspace.name)).toBeDefined()
    expect(column('Working').queryByText(otherWorkspace.name)).toBeNull()
  })

  it('puts a running Enhanced run in Working and one asking a question in Needs you', () => {
    const enhanced = { mode: 'acp', acp: true } as const
    seed([
      run({ ...enhanced, id: 'run_busy', task: 'busy agent', status: 'running', reason: 'working in background: 2 tasks' }),
      run({ ...enhanced, id: 'run_asking', task: 'asking agent', status: 'needs-attention', state: 'needs-you', reason: 'needs input: Which database?' }),
    ])
    renderBoard()

    expect(column('Working').getByText('busy agent')).toBeDefined()
    expect(column('Working').getByText('Working in background: 2 tasks')).toBeDefined()
    expect(column('Needs you').getByText('asking agent')).toBeDefined()
    expect(column('Needs you').getByText('Agent asks: Which database?')).toBeDefined()
  })

  it("keeps another member's stopped run in Working, waiting for its owner", () => {
    seed([run({ ...stopped, member_id: bob.id })])
    renderBoard()
    expect(cardOf('waiting on you').getByRole('img', { name: 'Waiting for Bob' })).toBeDefined()
    expect(cardOf('waiting on you').getByText('Waiting for Bob')).toBeDefined()
  })

  it('shows Paused off the timeline stream and clears it on resume', () => {
    seed([working])
    renderBoard()
    act(() => timeline('pause', 1))
    expect(cardOf('still going').getByText('Paused')).toBeDefined()
    act(() => timeline('resume', 2))
    expect(cardOf('still going').queryByText('Paused')).toBeNull()
  })

  it('shows a swarm as one card with its objective, phase and counts, opening the swarm page', () => {
    const integrator = run({ id: 'run_integrator', task: 'coordinate', mission_id: 'mission_1', mission_role: 'integrator' })
    const worker = (id: string, status: Run['status']) => run({
      id, task: `worker ${id}`, status, mode: 'headless',
      mission_id: 'mission_1', mission_role: 'worker', integrator_run_id: integrator.id,
    })
    seed([integrator, worker('w1', 'running'), worker('w2', 'running'), worker('w3', 'completed'), worker('w4', 'failed')])
    useStore.setState({ missions: { mission_1: mission({ phase: 'active' }) } })
    renderBoard()

    expect(screen.getAllByRole('article')).toHaveLength(1)
    const swarm = cardOf('coordinate checkout work')
    expect(swarm.getByText('Swarm active')).toBeDefined()
    expect(swarm.getByRole('img', { name: '2 working' })).toBeDefined()
    expect(swarm.getByRole('img', { name: '1 done' })).toBeDefined()
    expect(swarm.getByRole('img', { name: '1 failed' })).toBeDefined()
    expect(swarm.queryByRole('img', { name: /needs you/ })).toBeNull()
    expect(screen.queryByText('worker w1')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'coordinate checkout work' }))
    expect(useStore.getState().route).toEqual({ name: 'missions', params: { missionId: 'mission_1' } })
  })

  it('acts on a swarm card for the integrator itself when the integrator needs you', () => {
    const integrator = run({
      id: 'run_integrator', task: 'coordinate', mission_id: 'mission_1', mission_role: 'integrator',
      pending_inputs: [{ id: 'p', session_id: 's', kind: 'permission' }],
    })
    seed([integrator])
    useStore.setState({ missions: { mission_1: mission({ phase: 'active' }) } })
    renderBoard()

    fireEvent.click(cardOf('coordinate checkout work').getByRole('button', { name: 'Open terminal' }))
    expect(useStore.getState().route).toEqual({ name: 'run', params: { runId: 'run_integrator' } })
  })

  it('renders card meta contributions: an overlap count and a swarm conflict count', () => {
    const integrator = run({ id: 'run_integrator', task: 'coordinate', mission_id: 'mission_1', mission_role: 'integrator' })
    seed([integrator, working])
    useStore.setState({
      missions: { mission_1: mission() },
      missionDetails: {
        mission_1: {
          mission: mission(), tasks: [], attempts: [], submissions: [], questions: [],
          diagnostics: [{ task_id: 't1', task_revision: 1, run_id: 'run_w1', kind: 'observed_overlap', paths: ['src/a.ts'] }],
        },
      },
      overlaps: { [working.id]: [{ run_id: 'run_peer', member_id: bob.id, files: ['src/a.ts'] }] },
    })
    renderBoard()

    expect(cardOf('still going').getByRole('button', { name: '1 overlap' })).toBeDefined()
    expect(cardOf('coordinate checkout work').getByRole('button', { name: '1 conflict' })).toBeDefined()
  })

  it('folds archived swarm workers into one card and honours Mine there', () => {
    const archived = (id: string, member = alice.id) =>
      run({ ...archivedMerged, id, task: `archived ${id}`, member_id: member, mission_id: 'mission_1', mission_role: 'worker' })
    seed([archived('a1'), archived('a2'), run({ ...archivedMerged, member_id: bob.id, task: 'bob archived' }), working])
    useStore.setState({ missions: { mission_1: mission({ phase: 'completed' }) }, mineOnly: true })
    renderBoard()

    fireEvent.click(screen.getByRole('button', { name: 'Archived (1)' }))
    expect(column('Finished').getAllByRole('article')).toHaveLength(1)
    expect(cardOf('coordinate checkout work').getByText('Swarm completed')).toBeDefined()
    expect(screen.queryByText('bob archived')).toBeNull()
  })

  it('keeps unchanged cards as the same objects across unrelated updates', () => {
    seed([working, merged])
    const { result } = renderHook(() => useBoard())
    const before = result.current.columns.flatMap((c) => c.cards)
    act(() => useStore.setState({ runs: { ...useStore.getState().runs, [stopped.id]: toRecord(stopped) } }))
    const after = result.current.columns.flatMap((c) => c.cards)
    expect(after.find((card) => card.run.id === working.id)).toBe(before.find((card) => card.run.id === working.id))
    expect(after.find((card) => card.run.id === merged.id)).toBe(before.find((card) => card.run.id === merged.id))
  })
})

describe('Needs you actions', () => {
  it('approves a waiting device by its id, listed ahead of the runs', async () => {
    seed([stopped])
    useStore.setState({ devices: [pendingDevice()] })
    vi.mocked(api.memberDeviceApprove).mockResolvedValueOnce(pendingDevice({ status: 'approved' }))
    renderBoard()

    expect(screen.getByRole('heading', { name: 'Needs you 2' })).toBeDefined()
    const [first] = column('Needs you').getAllByRole('article')
    expect(first!.textContent).toContain('laptop (github mallory) wants to join as collaborator')
    expect(first!.textContent).toContain('SHA256:examplefingerprint')
    fireEvent.click(within(first!).getByRole('button', { name: 'Approve' }))

    await waitFor(() => expect(screen.getByRole('heading', { name: 'Needs you 1' })).toBeDefined())
    expect(api.memberDeviceApprove).toHaveBeenCalledWith('dev_1')
    expect(toast.success).toHaveBeenCalledWith('laptop approved')
  })

  it('labels a waiting device\'s pressed button, locks both, and says when the server is slow', () => {
    seed([stopped])
    useStore.setState({ devices: [pendingDevice()] })
    vi.mocked(api.memberDeviceRevoke).mockImplementationOnce(() => new Promise<never>(() => {}))
    renderBoard()
    const [card] = column('Needs you').getAllByRole('article')

    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    try {
      fireEvent.click(within(card!).getByRole('button', { name: 'Deny' }))
      expect(within(card!).getByRole('button', { name: 'Denying…' })).toHaveProperty('disabled', true)
      expect(within(card!).getByRole('button', { name: 'Approve' })).toHaveProperty('disabled', true)
      act(() => vi.advanceTimersByTime(optimisticDeadlineMs))
      expect(within(card!).getByRole('button', { name: 'Still waiting for the server…' })).toHaveProperty('disabled', true)
    } finally {
      vi.useRealTimers()
    }
  })

  it('says to open the terminal for a request a Standard run answers there', () => {
    seed([run({ ...working, pending_inputs: [{ id: 'p', session_id: 's', kind: 'permission' }] })])
    renderBoard()

    fireEvent.click(cardOf('still going').getByRole('button', { name: 'Open terminal' }))
    expect(useStore.getState().route).toEqual({ name: 'run', params: { runId: working.id } })
  })

  it('opens the changes of an unreviewed failure', () => {
    seed([run({ ...merged, status: 'needs-attention', state: 'failed', reason: 'agent reported failure', outcome_unseen: true, finished_at: null })])
    renderBoard()

    expect(column('Needs you').getByText('Agent reported failure; review the result. Run remains open for follow-up.')).toBeDefined()
    expect(cardOf('landed already').getByRole('img', { name: 'Failed' })).toBeDefined()
    fireEvent.click(cardOf('landed already').getByRole('button', { name: 'Review' }))
    expect(useStore.getState().route).toEqual({ name: 'run', params: { runId: merged.id, view: 'changes' } })
  })

  it("replies to an agent's question from a popover anchored to the card", async () => {
    seed([stopped])
    renderBoard()

    fireEvent.click(cardOf('waiting on you').getByRole('button', { name: 'Reply' }))
    const composer = await screen.findByRole('dialog', { name: 'Reply to waiting on you' })
    expect(screen.getByRole('button', { name: 'waiting on you' }).closest('article')!.dataset.selected).toBe('true')
    await userEvent.type(within(composer).getByRole('textbox', { name: 'Message to the agent' }), 'try the other flag')
    await userEvent.keyboard('{Control>}{Enter}{/Control}')

    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(api.runInject).toHaveBeenCalledWith(stopped.id, 'try the other flag', expect.any(String))
    expect(toast.success).toHaveBeenCalledWith('Message queued')
  })

  it('keeps the draft and shows the server error when a reply fails', async () => {
    seed([stopped])
    vi.mocked(api.runInject).mockRejectedValueOnce(new Error('run is not running'))
    renderBoard()

    fireEvent.click(cardOf('waiting on you').getByRole('button', { name: 'Reply' }))
    const composer = within(await screen.findByRole('dialog'))
    await userEvent.type(composer.getByRole('textbox'), 'hello')
    fireEvent.click(composer.getByRole('button', { name: 'Send' }))

    expect(await composer.findByRole('alert')).toHaveProperty('textContent', 'Send failed: run is not running')
    expect(composer.getByRole('textbox')).toHaveProperty('value', 'hello')
  })

  it('keeps an open reply and its draft while its group starts and stops virtualizing', async () => {
    sizedViewport(600, 100)
    const others = Array.from({ length: virtualizeAbove - 1 }, (_, i) => run({ ...stopped, id: `run_wait_${i}`, task: `ask ${i}` }))
    seed([stopped, ...others])
    renderBoard()

    fireEvent.click(cardOf('waiting on you').getByRole('button', { name: 'Reply' }))
    await userEvent.type(within(await screen.findByRole('dialog')).getByRole('textbox'), 'half a thought')
    const draft = async () => within(await screen.findByRole('dialog', { name: 'Reply to waiting on you' })).getByRole('textbox')

    act(() => useStore.setState({ devices: [pendingDevice()] }))
    expect(await draft()).toHaveProperty('value', 'half a thought')
    act(() => useStore.setState({ devices: [] }))
    expect(await draft()).toHaveProperty('value', 'half a thought')
  })

  it('keeps a pending send and its error while its group starts virtualizing', async () => {
    sizedViewport(600, 100)
    const others = Array.from({ length: virtualizeAbove - 1 }, (_, i) => run({ ...stopped, id: `run_wait_${i}`, task: `ask ${i}` }))
    seed([stopped, ...others])
    let fail!: (err: Error) => void
    vi.mocked(api.runInject).mockReturnValueOnce(new Promise((_, reject) => { fail = reject }))
    renderBoard()

    fireEvent.click(cardOf('waiting on you').getByRole('button', { name: 'Reply' }))
    const first = within(await screen.findByRole('dialog'))
    await userEvent.type(first.getByRole('textbox'), 'hello')
    fireEvent.click(first.getByRole('button', { name: 'Send' }))

    act(() => useStore.setState({ devices: [pendingDevice()] }))
    const composer = within(await screen.findByRole('dialog', { name: 'Reply to waiting on you' }))
    expect(composer.getByRole('button', { name: 'Sending…' })).toHaveProperty('disabled', true)
    await act(async () => fail(new Error('run is not running')))
    expect(await composer.findByRole('alert')).toHaveProperty('textContent', 'Send failed: run is not running')
    expect(composer.getByRole('button', { name: 'Send' })).toHaveProperty('disabled', false)
  })

  it('lets a send close only the reply it was sent from', async () => {
    seed([stopped])
    let deliver!: () => void
    vi.mocked(api.runInject).mockReturnValueOnce(new Promise((resolve) => {
      deliver = () => resolve({ message: roomMessage({ kind: 'steer_request', state: 'queued' }) })
    }))
    renderBoard()

    fireEvent.click(cardOf('waiting on you').getByRole('button', { name: 'Reply' }))
    const first = within(await screen.findByRole('dialog'))
    await userEvent.type(first.getByRole('textbox'), 'first')
    fireEvent.click(first.getByRole('button', { name: 'Send' }))
    fireEvent.click(first.getByRole('button', { name: 'Cancel' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())

    fireEvent.click(cardOf('waiting on you').getByRole('button', { name: 'Reply' }))
    await userEvent.type(within(await screen.findByRole('dialog')).getByRole('textbox'), 'second thought')
    await act(async () => deliver())
    expect(toast.success).toHaveBeenCalledWith('Message queued')
    expect(within(screen.getByRole('dialog')).getByRole('textbox')).toHaveProperty('value', 'second thought')
  })

  it("drops a member's reply and its pending send when another member signs in", async () => {
    useStore.getState().setIdentityKey('alice')
    seed([stopped])
    let fail!: (err: Error) => void
    vi.mocked(api.runInject).mockReturnValueOnce(new Promise((_, reject) => { fail = reject }))
    renderBoard()

    fireEvent.click(cardOf('waiting on you').getByRole('button', { name: 'Reply' }))
    const alices = within(await screen.findByRole('dialog'))
    await userEvent.type(alices.getByRole('textbox'), 'from alice')
    fireEvent.click(alices.getByRole('button', { name: 'Send' }))

    act(() => useStore.getState().setIdentityKey('bob'))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    fireEvent.click(cardOf('waiting on you').getByRole('button', { name: 'Reply' }))
    const bobs = within(await screen.findByRole('dialog'))
    await act(async () => fail(new Error('member signed out')))
    expect(bobs.queryByRole('alert')).toBeNull()
    expect(bobs.getByRole('button', { name: 'Send' })).toHaveProperty('disabled', true)
    expect(bobs.getByRole('textbox')).toHaveProperty('value', '')
  })

  it('answers r and o on the focused card', async () => {
    seed([stopped, working])
    renderBoard()

    screen.getByRole('button', { name: 'waiting on you' }).focus()
    await userEvent.keyboard('r')
    expect(await screen.findByRole('dialog', { name: 'Reply to waiting on you' })).toBeDefined()
    await userEvent.keyboard('{Escape}')
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('button', { name: 'waiting on you' })))

    screen.getByRole('button', { name: 'waiting on you' }).focus()
    await userEvent.keyboard('o')
    expect(useStore.getState().route).toEqual({ name: 'run', params: { runId: stopped.id } })
  })

  it('leaves r and o alone when no card has focus or the card has no such action', () => {
    seed([stopped, run({ ...merged, status: 'completed', outcome_unseen: true })])
    renderBoard()

    expect(fireEvent.keyDown(document.body, { key: 'r' })).toBe(true)
    act(() => screen.getByRole('button', { name: 'landed already' }).focus())
    expect(fireEvent.keyDown(document.activeElement!, { key: 'r' })).toBe(true)
    expect(fireEvent.keyDown(document.activeElement!, { key: 'o' })).toBe(false)
  })

  it('opens the run for a teammate question', () => {
    seed([working])
    useStore.setState({
      roomMessages: {
        [working.id]: [roomMessage({ run_id: working.id, actor_id: bob.id, kind: 'question', body: 'which flag?' })],
      },
    })
    renderBoard()

    expect(column('Needs you').getByText('still going')).toBeDefined()
    fireEvent.click(screen.getByRole('button', { name: 'still going' }))
    expect(useStore.getState().route).toEqual({ name: 'run', params: { runId: working.id } })
  })

  it('offers no action on Working and Finished cards', () => {
    seed([working, merged])
    renderBoard()
    expect(screen.queryByRole('button', { name: /^(Approve|Reply|Open)$/ })).toBeNull()
  })
})

describe('Finished footer', () => {
  it('keeps old archived runs reachable through the Finished toggle', () => {
    seed([merged, archivedMerged])
    renderBoard()

    expect(column('Finished').queryByText('already archived')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Archived (1)' }))
    expect(cardOf('already archived').getByRole('button', { name: 'already archived' })).toBeDefined()
    expect(column('Finished').queryByText('landed already')).toBeNull()

    act(() => useStore.setState({ activeWorkspace: otherWorkspace.id }))
    act(() => useStore.setState({ activeWorkspace: workspace.id }))
    expect(column('Finished').getByText('landed already')).toBeDefined()
  })

  it('archives closed runs through the shared confirmation', async () => {
    seed([merged, run({ ...merged, id: 'run_open', task: 'not closed', status: 'completed' })], { self: bob })
    useStore.setState({ members: { [alice.id]: alice, [bob.id]: { ...bob, role: 'admin' } } })
    renderBoard()

    await userEvent.click(screen.getByRole('button', { name: 'More finished-run actions' }))
    await userEvent.click(await screen.findByRole('menuitem', { name: 'Archive closed runs…' }))
    const dialog = within(await screen.findByRole('dialog'))
    expect(dialog.getByText(/1 run stays: completed but not yet closed/)).toBeDefined()
    fireEvent.click(dialog.getByRole('button', { name: 'Archive 1' }))

    await waitFor(() => expect(toast.success).toHaveBeenCalledWith('Archived 1 run'))
    expect(api.runArchive).toHaveBeenCalledWith(merged.id, true)
  })

  it('offers Free retained containers to admins only', async () => {
    const retained = run({ ...merged, reason: 'closed; retained container' })
    seed([run({ ...retained, member_id: bob.id })], { self: bob })
    const { unmount } = renderBoard()
    await userEvent.click(screen.getByRole('button', { name: 'More finished-run actions' }))
    expect(await screen.findByRole('menuitem', { name: 'Archive closed runs…' })).toBeDefined()
    expect(screen.queryByRole('menuitem', { name: 'Free retained containers…' })).toBeNull()
    await userEvent.keyboard('{Escape}')
    unmount()

    seed([retained])
    renderBoard()
    await userEvent.click(screen.getByRole('button', { name: 'More finished-run actions' }))
    await userEvent.click(await screen.findByRole('menuitem', { name: 'Free retained containers…' }))
    fireEvent.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Free 1' }))
    await waitFor(() => expect(api.runRelease).toHaveBeenCalledWith(retained.id))
  })

  it('stacks into the list with Finished collapsed, and hides List, when three columns do not fit', () => {
    atBoardWidth(600)
    seed([working, merged])
    renderBoard()

    expect(screen.getAllByRole('tab').map((tab) => tab.textContent)).toEqual(['Board', 'Map', 'Lanes'])
    const toggle = screen.getByRole('button', { name: 'Finished 1' })
    expect(toggle.getAttribute('aria-expanded')).toBe('false')
    expect(screen.queryByText('landed already')).toBeNull()
    fireEvent.click(toggle)
    expect(screen.getByText('landed already')).toBeDefined()
  })
})

describe('layouts', () => {
  it('offers List beside Board when the columns fit, and remembers it for the workspace', async () => {
    seed([working, merged])
    renderBoard()
    expect(screen.queryByRole('button', { name: 'Finished 1' })).toBeNull()

    await userEvent.click(screen.getByRole('tab', { name: 'List' }))
    expect(useStore.getState().boardViews).toEqual({ [workspace.id]: 'list' })
    expect(screen.getByRole('button', { name: 'Finished 1' }).getAttribute('aria-expanded')).toBe('false')
    expect(column('Working').getByText('still going')).toBeDefined()
  })

  it('renders a long group only as far as its scroll shows', async () => {
    sizedViewport(600, 100)
    const many = Array.from({ length: virtualizeAbove + 10 }, (_, i) =>
      run({ ...merged, id: `run_done_${i}`, task: `done ${i}`, finished_at: `2026-08-14T10:${String(i % 60).padStart(2, '0')}:00Z` }))
    seed([working, ...many])
    renderBoard()

    const shown = (await column('Finished').findAllByRole('article')).length
    expect(shown).toBeLessThan(many.length)
    expect(column('Working').getAllByRole('article')).toHaveLength(1)
  })

  it('shows the right cards of a long list group after a resize moves the group', async () => {
    const resize = sizedViewport(600, 100)
    let groupTop = 0
    const rect = vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (this: HTMLElement) {
      const group = this.parentElement?.getAttribute('aria-label') === 'Working' && this.firstElementChild?.tagName === 'UL'
      const top = group ? groupTop - this.closest('.overflow-y-auto')!.scrollTop : 0
      return { left: 0, top, width: 800, height: 100, right: 800, bottom: top + 100, x: 0, y: top, toJSON: () => ({}) }
    })
    onTestFinished(() => rect.mockRestore())
    seed(Array.from({ length: virtualizeAbove + 10 }, (_, i) => run({ ...working, id: `run_busy_${String(i).padStart(2, '0')}`, task: `busy ${i}` })))
    useStore.setState({ boardViews: { [workspace.id]: 'list' } })
    renderBoard()
    const list = (await column('Working').findByRole('button', { name: 'busy 0' })).closest<HTMLElement>('.overflow-y-auto')!

    // The groups above rewrap: Working now starts 2000px down, and the list is scrolled 1000px into it.
    groupTop = 2000
    list.scrollTop = 3000
    act(() => resize())
    fireEvent.scroll(list)
    expect(await column('Working').findByRole('button', { name: 'busy 10' })).toBeDefined()
  })

  it('announces how many items need the viewer', () => {
    seed([stopped, working])
    useStore.setState({ devices: [pendingDevice()] })
    renderBoard()
    expect(screen.getByRole('status').textContent).toBe('2 need you')
  })
})

describe('empty board', () => {
  it('asks for a repository when there is no workspace', () => {
    seed([])
    useStore.setState({ workspaces: {}, activeWorkspace: '' })
    renderBoard()

    fireEvent.click(screen.getByRole('button', { name: 'Add your repository' }))
    expect(useStore.getState().route.name).toBe('onboarding')
    expect(useStore.getState().onboardingStep).toBe('Repository')
  })

  it('asks for the base branch when the server cannot read it, with its error', async () => {
    seed([])
    vi.mocked(api.filesTree).mockRejectedValueOnce(
      new ApiError(200, 'files.tree: workspace has no repository yet', -32004),
    )
    renderBoard()

    expect(await screen.findByRole('heading', { name: 'Base branch missing' })).toBeDefined()
    expect(screen.getByText(/workspace has no repository yet/)).toBeDefined()
    fireEvent.click(screen.getByRole('button', { name: 'Push your base branch' }))
    expect(useStore.getState().onboardingStep).toBe('Repository')
  })

  it('checks the base branch again when the window regains focus', async () => {
    seed([])
    vi.mocked(api.filesTree).mockRejectedValueOnce(
      new ApiError(200, 'files.tree: workspace has no repository yet', -32004),
    )
    renderBoard()
    expect(await screen.findByRole('heading', { name: 'Base branch missing' })).toBeDefined()

    act(() => window.dispatchEvent(new Event('focus')))
    expect(await screen.findByRole('heading', { name: 'No runs yet' })).toBeDefined()
  })

  it('shows any other base-branch check failure as an error', async () => {
    seed([])
    vi.mocked(api.filesTree).mockRejectedValueOnce(new Error('files.tree: fetch failed'))
    renderBoard()

    expect((await screen.findByRole('alert')).textContent).toContain('files.tree: fetch failed')
    expect(screen.queryByRole('button', { name: 'Push your base branch' })).toBeNull()
  })

  it("says when Mine hides every run and offers everyone's", async () => {
    seed([run({ ...working, member_id: bob.id })])
    useStore.setState({ mineOnly: true })
    renderBoard()

    fireEvent.click(await screen.findByRole('button', { name: "Show everyone's runs" }))
    expect(column('Working').getByText('still going')).toBeDefined()
  })

  it('asks for an agent when none is installed', async () => {
    seed([])
    vi.mocked(api.agentList).mockResolvedValueOnce([{ name: 'claude', source: 'shipped', installed: false }])
    renderBoard()

    fireEvent.click(await screen.findByRole('button', { name: 'Set up an agent' }))
    expect(useStore.getState().route.name).toBe('agents')
  })

  it('offers New run with the definition of a run once everything is in place', async () => {
    seed([])
    renderBoard()

    expect(await screen.findByText('A run is one agent working on its own branch in its own container.')).toBeDefined()
    fireEvent.click(screen.getByRole('button', { name: 'New run' }))
    expect(useStore.getState().paletteDialog).toBe('launch')
  })

  it('leaves New run out without launch access', async () => {
    seed([])
    useStore.setState({ capabilities: { gateway: 'remote', methods: ['run.list', 'files.tree', 'agent.list'], ws: [] } })
    renderBoard()

    expect(await screen.findByRole('heading', { name: 'No runs yet' })).toBeDefined()
    expect(screen.queryByRole('button', { name: 'New run' })).toBeNull()
  })

  it('names an unreachable server with its error instead of an empty board', () => {
    seed([])
    useStore.setState({ hydrated: false, hydrationError: 'dial tcp: connection refused', streamDead: true })
    renderBoard()
    expect(screen.getByRole('alert').textContent).toContain('dial tcp: connection refused')
  })
})

describe('map view', () => {
  it('switches from an aggregated swarm to individual runs and opens the selected worker', async () => {
    seed([
      run({ id: 'run_integrator', task: 'coordinate', mission_id: 'mission_1', mission_role: 'integrator' }),
      run({ id: 'worker', task: 'implement checkout', mission_id: 'mission_1', mission_role: 'worker', integrator_run_id: 'run_integrator' }),
    ])
    useStore.setState({ missions: { mission_1: mission() } })
    renderBoard()
    expect(screen.getAllByRole('article')).toHaveLength(1)
    await userEvent.click(screen.getByRole('tab', { name: 'Map' }))
    expect(screen.getAllByRole('article').map((card) => card.dataset.runId).sort()).toEqual(['run_integrator', 'worker'])
    await userEvent.click(screen.getByRole('button', { name: 'implement checkout' }))
    expect(useStore.getState().route).toEqual({ name: 'run', params: { runId: 'worker' } })
  })

  it('reveals archived workers without hiding live runs, and keeps the all-workspaces choice out of Board', async () => {
    seed([working, merged, archivedMerged, run({ id: 'other', task: 'other workspace', workspace_id: otherWorkspace.id })])
    renderBoard()
    await userEvent.click(screen.getByRole('tab', { name: 'Map' }))
    await userEvent.click(screen.getByRole('button', { name: 'Archived (1)' }))
    expect(screen.getByRole('button', { name: working.task })).toBeDefined()
    expect(screen.getByRole('button', { name: archivedMerged.task })).toBeDefined()
    expect(screen.queryByRole('button', { name: merged.task })).toBeNull()
    act(() => useStore.getState().setBoardMapAllWorkspaces(true))
    expect(screen.getByRole('button', { name: 'other workspace' })).toBeDefined()
    await userEvent.click(screen.getByRole('tab', { name: 'Board' }))
    expect(screen.queryByRole('button', { name: 'other workspace' })).toBeNull()
    expect(useStore.getState().activeWorkspace).toBe(workspace.id)
  })
  it('shows a waiting device above the map', async () => {
    seed([working])
    useStore.setState({ devices: [pendingDevice()] })
    renderBoard()
    await userEvent.click(screen.getByRole('tab', { name: 'Map' }))
    const waiting = screen.getByRole('region', { name: 'Devices waiting for approval' })
    expect(within(waiting).getByText('laptop (github mallory) wants to join as collaborator')).toBeDefined()
    expect(within(waiting).getByRole('button', { name: 'Approve' })).toBeDefined()
  })
})

describe('lanes view', () => {
  async function openLanes() {
    renderBoard()
    await userEvent.click(screen.getByRole('tab', { name: 'Lanes' }))
    return screen.getByRole('tree', { name: 'Run lanes' })
  }

  it('lists runs as lanes by section, reads their history, and remembers the view', async () => {
    seed([stopped, working, merged])
    const tree = await openLanes()
    expect(useStore.getState().boardViews[workspace.id]).toBe('lanes')
    const items = within(tree).getAllByRole('treeitem').map((item) => item.getAttribute('aria-label'))
    expect(items[0]).toBe('Needs you, 1 run')
    expect(items[1]).toMatch(/^waiting on you, Needs you/)
    expect(items[2]).toBe('Working, 1 run')
    expect(items[4]).toBe('Finished, 0 runs, 1 ended before this window')
    await waitFor(() => expect(api.workspaceTimeline).toHaveBeenCalledWith(expect.objectContaining({
      workspace_id: workspace.id, types: ['run.status', 'coord.message', 'coord.message.acked'], newest: true,
    })))
  })

  it('moves through the tree by keyboard, folds a section and opens a run from its popover', async () => {
    seed([stopped, working])
    const tree = await openLanes()
    const [needs, lane] = within(tree).getAllByRole('treeitem')
    act(() => lane.focus())
    await userEvent.keyboard('{ArrowLeft}')
    await waitFor(() => expect(document.activeElement).toBe(needs))
    await userEvent.keyboard('{ArrowLeft}')
    expect(needs.getAttribute('aria-expanded')).toBe('false')
    expect(within(tree).queryByRole('treeitem', { name: /^waiting on you/ })).toBeNull()
    await userEvent.keyboard('{ArrowRight}{ArrowDown}')
    await waitFor(() => expect(document.activeElement?.getAttribute('aria-label')).toMatch(/^waiting on you/))
    await userEvent.keyboard('{Enter}')
    const dialog = await screen.findByRole('dialog', { name: /^waiting on you/ })
    expect(within(dialog).getByRole('button', { name: 'Reply' })).toBeDefined()
    await userEvent.click(within(dialog).getByRole('button', { name: 'Open run' }))
    expect(useStore.getState().route).toEqual({ name: 'run', params: { runId: stopped.id } })
  })

  it('keeps the reply composer open after Reply in a lane popover', async () => {
    seed([stopped, working])
    const tree = await openLanes()
    act(() => within(tree).getAllByRole('treeitem')[1].focus())
    await userEvent.keyboard('{Enter}')
    const dialog = await screen.findByRole('dialog', { name: /^waiting on you/ })
    await userEvent.click(within(dialog).getByRole('button', { name: 'Reply' }))
    const composer = await screen.findByRole('dialog', { name: /^Reply to / })
    await act(() => new Promise((resolve) => setTimeout(resolve, 50)))
    expect(composer.isConnected).toBe(true)
    expect(document.activeElement).toBe(within(composer).getByRole('textbox', { name: 'Message to the agent' }))
  })

  it('groups a swarm under one lane with its integrator and workers', async () => {
    seed([
      run({ id: 'run_integrator', task: 'coordinate', mission_id: 'mission_1', mission_role: 'integrator' }),
      run({ id: 'worker', task: 'implement checkout', mission_id: 'mission_1', mission_role: 'worker', integrator_run_id: 'run_integrator' }),
    ])
    useStore.setState({ missions: { mission_1: mission() } })
    const tree = await openLanes()
    expect(within(tree).getByRole('treeitem', { name: /^Swarm coordinate checkout work/ }).getAttribute('aria-expanded')).toBe('true')
    const worker = within(tree).getByRole('treeitem', { name: /^implement checkout, Worker/ })
    expect([worker.getAttribute('aria-level'), worker.getAttribute('aria-posinset'), worker.getAttribute('aria-setsize')]).toEqual(['3', '2', '2'])
  })

  it("lists a lane's links in its popover, so the keyboard opens what a click on a link does", async () => {
    const recent = new Date(Date.now() - 10 * 60_000).toISOString()
    seed([
      run({ id: 'run_integrator', task: 'coordinate', mission_id: 'mission_1', mission_role: 'integrator', created_at: recent }),
      run({ id: 'worker', task: 'implement checkout', mission_id: 'mission_1', mission_role: 'worker', integrator_run_id: 'run_integrator', created_at: recent }),
    ])
    useStore.setState({ missions: { mission_1: mission() } })
    const tree = await openLanes()
    act(() => within(tree).getByRole('treeitem', { name: /^implement checkout, Worker/ }).focus())
    await userEvent.keyboard('{Enter}')
    const dialog = await screen.findByRole('dialog', { name: /^implement checkout/ })
    await userEvent.click(within(within(dialog).getByRole('region', { name: 'Links' })).getByRole('button', { name: /^Launched by its integrator/ }))
    const link = await screen.findByRole('dialog', { name: 'Link' })
    expect(within(link).getByText(/^Launched by its integrator/)).toBeDefined()
  })

  it('closes a popover whose lane folds away and focuses the lane that draws its run', async () => {
    seed([working])
    const tree = await openLanes()
    act(() => within(tree).getByRole('treeitem', { name: /^still going/ }).focus())
    await userEvent.keyboard('{Enter}')
    await screen.findByRole('dialog', { name: /^still going/ })
    act(() => useStore.setState((s) => ({ runs: { ...s.runs, [working.id]: toRecord({ ...working, status: 'merged', state: undefined, finished_at: new Date().toISOString() }) } })))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    await waitFor(() => expect(document.activeElement?.getAttribute('aria-label')).toBe('1 finished'))
  })

  it('shows the mail of every sender bundled on a link', async () => {
    seed([run({ id: 'a' }), run({ id: 'b' }), run({ id: 'c' })])
    const sent = async ({ run_id }: { run_id?: string }) => ({ messages: [{
      id: `m_${run_id}`, workspace_id: workspace.id, from_run_id: run_id!, to_run_id: 'c', kind: 'message' as const, body: `from ${run_id}`, created_at: '2026-08-14T10:30:00Z',
    }] })
    vi.mocked(api.coordMessagesList).mockImplementationOnce(sent).mockImplementationOnce(sent)
    render(<EdgePopover edge={{ key: 'mail', kind: 'message', from: { lane: 'swarm', run: 'a', t: 0 }, to: { lane: 'c', run: 'c', t: 0 }, messages: { ids: ['m_a', 'm_b'], senders: ['a', 'b'], acked: false } }} now={Date.now()} />)
    expect(await screen.findByText('from a')).toBeDefined()
    expect(await screen.findByText('from b')).toBeDefined()
  })

  it('shows a failed swarm read with its error and retries it', async () => {
    seed([run({ id: 'run_integrator', task: 'coordinate', mission_id: 'mission_1', mission_role: 'integrator' })])
    vi.mocked(api.missionShow).mockRejectedValueOnce(new ApiError(500, 'mission store unavailable'))
    await openLanes()
    const alert = await screen.findByRole('alert')
    expect(within(alert).getByText('Swarm details')).toBeDefined()
    expect(alert.textContent).toContain('mission store unavailable')
    await userEvent.click(within(alert).getByRole('button', { name: 'Retry' }))
    await waitFor(() => expect(screen.queryByRole('alert')).toBeNull())
  })

  it('keeps the timeline when nothing ran in the window, so it can still pan and zoom', async () => {
    seed([merged])
    await openLanes()
    expect(screen.getByText('Nothing ran in the last 6h.')).toBeDefined()
    expect(screen.getByRole('group', { name: 'Timeline window' })).toBeDefined()
  })

  it('shows a failed history read with its error and retries it', async () => {
    seed([working])
    vi.mocked(api.workspaceTimeline).mockRejectedValueOnce(new ApiError(500, 'timeline unavailable'))
    await openLanes()
    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toContain('timeline unavailable')
    await userEvent.click(within(alert).getByRole('button', { name: 'Retry' }))
    await waitFor(() => expect(screen.queryByRole('alert')).toBeNull())
  })

  it('drops an older history page read while live events trimmed held history, so no gap reads as loaded', async () => {
    seed([working])
    const start = Date.now() - 30 * 60_000
    const mail = (seq: number): Event => ({
      id: `evt_${seq}`, seq, time: new Date(start + seq).toISOString(), workspace_id: workspace.id,
      run_id: working.id, actor_id: '', type: 'coord.message', payload: {},
    })
    const range = (from: number, to: number) => Array.from({ length: to - from + 1 }, (_, i) => mail(from + i))
    let older!: (page: TimelinePage) => void
    vi.mocked(api.workspaceTimeline)
      .mockResolvedValueOnce({ events: range(1001, historyBudget + 999), next_seq: 0, more: true, older_seq: 1001 })
      .mockImplementationOnce(() => new Promise((resolve) => { older = resolve }))
    await openLanes()
    await waitFor(() => expect(api.workspaceTimeline).toHaveBeenCalledTimes(2))
    act(() => {
      useStore.getState().appendLaneEvent(mail(historyBudget + 1000))
      useStore.getState().appendLaneEvent(mail(historyBudget + 1001))
    })
    await act(async () => older({ events: range(1, 1000), next_seq: 0, more: true, older_seq: 1 }))
    expect(await screen.findByText('Older history not loaded')).toBeDefined()
    const history = useStore.getState().laneHistory[workspace.id]
    expect(history.events[0].seq).toBe(1002)
    expect(history.floor).toBe(Date.parse(mail(1002).time))
  })

  it('says when nothing is in scope', async () => {
    seed([])
    renderBoard()
    await userEvent.click(screen.getByRole('tab', { name: 'Lanes' }))
    expect(screen.getByText('No runs in this view.')).toBeDefined()
  })
})
