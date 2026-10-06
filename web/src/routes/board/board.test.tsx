import { act, fireEvent, render, renderHook, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { toast } from 'sonner'
import { PaletteDialogs } from '@/components/palette/dialogs'
import { api, ApiError } from '@/lib/api'
import type { GatewayCapabilities, Run } from '@/lib/types'
import { Board } from '@/routes/board'
import { useBoard } from '@/routes/board/selectors'
import '@/routes/diff/conflict-chips'
import '@/routes/missions'
import { useStore } from '@/store'
import { toRecord } from '@/store/runs'
import { applyEvent } from '@/store/sync'
import {
  alice,
  approval,
  bob,
  fakeApi,
  mission,
  otherWorkspace,
  roomMessage,
  run,
  serverInfo,
  workspace,
} from '@/test/fixtures'
import { atViewport } from '@/test/viewport'

vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api')>()
  const { fakeApi } = await import('@/test/fixtures')
  return { ...actual, api: fakeApi() }
})

vi.mock('sonner', () => ({
  toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() },
}))

const everyMethod: GatewayCapabilities = { gateway: 'remote', methods: ['*'], ws: [] }

function seed(runs: Run[], { active = workspace.id, self = alice } = {}) {
  useStore.setState({
    workspaces: { [workspace.id]: workspace, [otherWorkspace.id]: otherWorkspace },
    activeWorkspace: active,
    capabilities: everyMethod,
    info: { ...serverInfo, member: self },
    members: { [alice.id]: alice, [bob.id]: bob },
    runs: Object.fromEntries(runs.map((r) => [r.id, toRecord(r)])),
    pausedRuns: {},
    inbox: {},
    approvalsByRun: {},
    roomMessages: {},
    roomStatus: {},
    missions: {},
    missionDetails: {},
    diffs: {},
    overlaps: {},
    mineOnly: false,
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

const stopped = run({ id: 'run_stopped', task: 'waiting on you', status: 'needs-attention' })
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
  finished_at: '2026-08-10T10:30:00Z',
  archived_at: '2026-08-14T10:00:00Z',
  deletes_at: '2099-08-28T10:00:00Z',
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

  it('draws a card as a state line, a title and a meta line, and nothing else', async () => {
    seed([merged])
    renderBoard()
    const merge = cardOf('landed already')

    expect(merge.getByText('Merged')).toBeDefined()
    expect(merge.getByRole('img', { name: 'Alice' })).toBeDefined()
    await waitFor(() => expect(merge.getByText('Claude Code')).toBeDefined())
    expect(merge.queryByText(/Details|Protected|tui/)).toBeNull()
    expect(merge.getAllByRole('button')).toHaveLength(1)
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

  it('opens the run from anywhere on the card', () => {
    seed([working])
    renderBoard()
    fireEvent.click(screen.getByRole('button', { name: 'still going' }))
    expect(useStore.getState().route).toEqual({ name: 'terminal', params: { runId: working.id } })
  })

  it('lists what needs the viewer from every workspace, naming the other one', () => {
    seed([working, run({ ...stopped, workspace_id: otherWorkspace.id })])
    renderBoard()

    expect(cardOf('waiting on you').getByText(otherWorkspace.name)).toBeDefined()
    expect(column('Working').queryByText(otherWorkspace.name)).toBeNull()
  })

  it("keeps another member's stopped run in Working, waiting for its owner", () => {
    seed([run({ ...stopped, member_id: bob.id })])
    renderBoard()
    expect(column('Working').getByText('Waiting for Bob')).toBeDefined()
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
    expect(swarm.getByText('2 working · 1 done · 1 failed')).toBeDefined()
    expect(screen.queryByText('worker w1')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'coordinate checkout work' }))
    expect(useStore.getState().route).toEqual({ name: 'missions', params: { missionId: 'mission_1' } })
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
  it('approves in place and moves the card back to Working', async () => {
    seed([working])
    act(() => useStore.getState().setInbox(workspace.id, [approval({ run_id: working.id })]))
    vi.mocked(api.approvalDecide).mockResolvedValueOnce(
      approval({ run_id: working.id, decision: 'approved', decided_by: alice.id, decided_at: '2026-08-14T10:06:00Z' }),
    )
    renderBoard()
    expect(cardOf('still going').getByText('Permission: write src/checkout.ts')).toBeDefined()

    fireEvent.click(cardOf('still going').getByRole('button', { name: 'Approve' }))

    await waitFor(() => expect(column('Working').getByText('still going')).toBeDefined())
    expect(api.approvalDecide).toHaveBeenCalledWith(working.id, 'apr_1', true)
    expect(toast.success).toHaveBeenCalledWith('Approved: write src/checkout.ts')
  })

  it('says to open the terminal for a request a Standard run answers there', () => {
    seed([run({ ...working, pending_inputs: [{ id: 'p', session_id: 's', kind: 'permission' }] })])
    renderBoard()

    fireEvent.click(cardOf('still going').getByRole('button', { name: 'Open terminal' }))
    expect(useStore.getState().route).toEqual({ name: 'terminal', params: { runId: working.id } })
  })

  it('opens the changes of an unreviewed finish', () => {
    seed([run({ ...merged, status: 'completed', outcome_unseen: true })])
    renderBoard()

    expect(column('Needs you').getByText('Finished, review the result')).toBeDefined()
    fireEvent.click(cardOf('landed already').getByRole('button', { name: 'Open' }))
    expect(useStore.getState().route).toEqual({ name: 'diff', params: { runId: merged.id } })
  })

  it('replies to an idle agent from a popover anchored to the card', async () => {
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

  it('answers a, r and o on the focused card', async () => {
    seed([stopped, working])
    act(() => useStore.getState().setInbox(workspace.id, [approval({ run_id: working.id })]))
    renderBoard()

    screen.getByRole('button', { name: 'still going' }).focus()
    await userEvent.keyboard('a')
    await waitFor(() => expect(api.approvalDecide).toHaveBeenCalledWith(working.id, 'apr_1', true))

    screen.getByRole('button', { name: 'waiting on you' }).focus()
    await userEvent.keyboard('r')
    expect(await screen.findByRole('dialog', { name: 'Reply to waiting on you' })).toBeDefined()
    await userEvent.keyboard('{Escape}')
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('button', { name: 'waiting on you' })))

    screen.getByRole('button', { name: 'waiting on you' }).focus()
    await userEvent.keyboard('o')
    expect(useStore.getState().route).toEqual({ name: 'terminal', params: { runId: stopped.id } })
  })

  it('leaves a, r and o alone when no card has focus or the card has no such action', () => {
    seed([stopped, run({ ...merged, status: 'completed', outcome_unseen: true })])
    renderBoard()

    expect(fireEvent.keyDown(document.body, { key: 'a' })).toBe(true)
    act(() => screen.getByRole('button', { name: 'landed already' }).focus())
    expect(fireEvent.keyDown(document.activeElement!, { key: 'a' })).toBe(true)
    expect(fireEvent.keyDown(document.activeElement!, { key: 'r' })).toBe(true)
    expect(fireEvent.keyDown(document.activeElement!, { key: 'o' })).toBe(false)
  })

  it('opens the run for a Run Room question', () => {
    seed([working])
    useStore.setState({
      roomMessages: {
        [working.id]: [roomMessage({ run_id: working.id, actor_id: bob.id, kind: 'question', body: 'which flag?' })],
      },
    })
    renderBoard()

    expect(column('Needs you').getByText('still going')).toBeDefined()
    fireEvent.click(screen.getByRole('button', { name: 'still going' }))
    expect(useStore.getState().route).toEqual({ name: 'terminal', params: { runId: working.id } })
  })

  it('offers no action on Working and Finished cards', () => {
    seed([working, merged])
    renderBoard()
    expect(screen.queryByRole('button', { name: /^(Approve|Reply|Open)$/ })).toBeNull()
  })
})

describe('Finished footer', () => {
  it('swaps Finished for archived runs behind its link, with their deletion date', () => {
    seed([merged, archivedMerged])
    renderBoard()

    expect(column('Finished').queryByText('already archived')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Archived (1)' }))
    expect(cardOf('already archived').getByText(/^Merged, deleted in \d+ days$/)).toBeDefined()
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

  it('collapses Finished by default when the columns stack', () => {
    atViewport(800)
    seed([working, merged])
    renderBoard()

    const toggle = screen.getByRole('button', { name: 'Finished 1' })
    expect(toggle.getAttribute('aria-expanded')).toBe('false')
    expect(screen.queryByText('landed already')).toBeNull()
    fireEvent.click(toggle)
    expect(screen.getByText('landed already')).toBeDefined()
  })
})

describe('empty board', () => {
  it('asks for a repository when there is no workspace', () => {
    seed([])
    useStore.setState({ workspaces: {}, activeWorkspace: '' })
    renderBoard()

    fireEvent.click(screen.getByRole('button', { name: 'Add your repository' }))
    expect(useStore.getState().route.name).toBe('onboarding')
    expect(useStore.getState().onboardingStep).toBe('Workspace')
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
