import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { toast } from 'sonner'
import { api, ApiError } from '@/lib/api'
import type { GatewayCapabilities, Run } from '@/lib/types'
import { Board } from '@/routes/board'
import { RunCard } from '@/routes/board/run-card'
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
  run,
  serverInfo,
  workspace,
} from '@/test/fixtures'

vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api')>()
  const { fakeApi } = await import('@/test/fixtures')
  return { ...actual, api: fakeApi() }
})

vi.mock('sonner', () => ({
  toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() },
}))

function seed(runs: Run[], active = workspace.id) {
  useStore.setState({
    workspaces: { [workspace.id]: workspace, [otherWorkspace.id]: otherWorkspace },
    activeWorkspace: active,
    boardView: 'cards',
    capabilities: everyMethod,
    info: { ...serverInfo, member: alice },
    members: { [alice.id]: alice, [bob.id]: bob },
    runs: Object.fromEntries(runs.map((r) => [r.id, toRecord(r)])),
    acked: {},
    pausedRuns: {},
    inbox: {},
    roomMessages: {},
    hydrated: true,
    overlaps: {},
    missionDetails: {},
    hydrationError: null,
    lastSeq: 0,
    route: { name: 'board', params: {} },
  })
}

const everyMethod: GatewayCapabilities = { gateway: 'remote', methods: ['*'], ws: [] }

/** `seed`, plus the capability and caller identity bulk actions read. */
function seedAs(self: typeof alice, runs: Run[]) {
  seed(runs)
  useStore.setState({ capabilities: everyMethod, info: { ...serverInfo, member: self } })
}

const stalled = run({
  id: 'run_stalled',
  task: 'waiting on a question',
  status: 'needs-attention',
  finished_at: null,
})
const working = run({ id: 'run_working', task: 'still going', status: 'running' })
const queued = run({
  id: 'run_queued',
  task: 'not started',
  status: 'queued',
  started_at: null,
  created_at: '2026-08-14T11:00:00Z',
})
const merged = run({
  id: 'run_merged',
  task: 'landed already',
  status: 'completed',
  finished_at: '2026-08-14T10:30:00Z',
})
const elsewhere = run({
  id: 'run_elsewhere',
  task: 'another workspace entirely',
  status: 'running',
  workspace_id: otherWorkspace.id,
})
const archivedMerged = run({
  id: 'run_archived',
  task: 'already archived',
  status: 'merged',
  finished_at: '2026-08-10T10:30:00Z',
  archived_at: '2026-08-14T10:00:00Z',
  deletes_at: '2026-08-28T10:00:00Z',
})

// The columns are landmarks; a plain label lookup would also hit the state
// chips, which carry the same words.
function column(label: string) {
  return within(screen.getByRole('region', { name: label }))
}

/** One real workspace.timeline steering entry, through the real event path. */
function timeline(kind: 'pause' | 'resume', seq: number) {
  applyEvent(
    useStore,
    {
      id: `evt_${seq}`,
      seq,
      time: '2026-08-14T11:30:00Z',
      workspace_id: workspace.id,
      run_id: working.id,
      actor_id: alice.id,
      type: 'workspace.timeline',
      payload: { kind },
    },
    fakeApi(),
  )
}

/**
 * One real run.archived event, through the same path the server publishes
 * it on. Bulk archive relies on this, not the RPC response, to move a run
 * in the store.
 */
function archived(
  runID: string,
  seq: number,
  payload: { archived_at: string | null; deletes_at: string | null },
) {
  return applyEvent(
    useStore,
    {
      id: `evt_archived_${seq}`,
      seq,
      time: '2026-08-14T11:00:00Z',
      workspace_id: workspace.id,
      run_id: runID,
      actor_id: '',
      type: 'run.archived',
      payload,
    },
    fakeApi(),
  )
}

// A stub left standing by a failing assertion would gut navigator for every
// test after it, turning one real failure into a file full of them.
afterEach(() => vi.unstubAllGlobals())
beforeEach(() => vi.clearAllMocks())

describe('board', () => {
  it('deals runs into the three buckets, newest first', () => {
    seed([stalled, working, queued, merged])
    render(<Board />)

    expect(column('Idle').getByText('waiting on a question')).toBeDefined()
    expect(column('Done').getByText('landed already')).toBeDefined()

    // queued (11:00) changed after running (10:02), so it sorts above it.
    const tasks = column('Working')
      .getAllByRole('article')
      // Named for the run it opens, which is what the sort is asserting;
      // getByRole still throws if a card grows a second such button.
      .map((card) =>
        within(card)
          .getByRole('button', { name: /^(not started|still going)$/ })
          .getAttribute('aria-label'),
      )
    expect(tasks).toEqual(['not started', 'still going'])
  })

  it.each(['cards', 'map'] as const)('copies the full disclosed branch without navigating or acknowledging in %s', async (variant) => {
    seed([working])
    useStore.setState({ boardView: variant })
    render(<Board />)
    const card = screen.getByRole('article')
    expect(within(card).queryByRole('button', { name: /^Copy branch/ })).toBeNull()
    fireEvent.click(within(card).getByRole('button', { name: /^Show details/ }))
    const details = variant === 'map' ? await screen.findByRole('dialog') : card
    const branch = within(details).getByText(working.branch)
    fireEvent.click(branch)
    expect(useStore.getState().route).toEqual({ name: 'board', params: {} })

    const writeText = vi.fn(async () => {})
    vi.stubGlobal('navigator', { clipboard: { writeText } })
    fireEvent.click(
      within(details).getByRole('button', { name: `Copy branch ${working.branch}` }),
    )

    await vi.waitFor(() => expect(writeText).toHaveBeenCalledWith(working.branch))
    expect(useStore.getState().route).toEqual({ name: 'board', params: {} })
    expect(useStore.getState().acked[working.id]).toBeUndefined()
  })

  it('discloses the full task and reason without opening the run', () => {
    const detailed = run({
      ...working,
      title: 'Checkout redesign',
      task: 'Replace the checkout flow while retaining payment retries and saved addresses.',
      reason: 'Checking the final migration before publishing.',
    })
    seed([detailed])
    render(<Board />)

    expect(screen.queryByText(detailed.task)).toBeNull()
    expect(screen.queryByText(detailed.reason!)).toBeNull()
    const disclosure = screen.getByRole('button', { name: 'Show details for Checkout redesign' })
    disclosure.focus()
    fireEvent.click(disclosure)

    expect(disclosure.getAttribute('aria-expanded')).toBe('true')
    expect(screen.getByText(detailed.task)).toBeDefined()
    expect(screen.getByText(detailed.reason!)).toBeDefined()
    expect(document.activeElement).toBe(disclosure)
    expect(useStore.getState().route).toEqual({ name: 'board', params: {} })
    fireEvent.click(screen.getByText(detailed.task))
    expect(useStore.getState().route).toEqual({ name: 'board', params: {} })

    fireEvent.click(disclosure)
    expect(disclosure.getAttribute('aria-expanded')).toBe('false')
    expect(screen.queryByText(detailed.task)).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Checkout redesign' }))
    expect(useStore.getState().route).toEqual({
      name: 'terminal',
      params: { runId: detailed.id },
    })
  })

  it.each(['cards', 'map'] as const)('keeps overlap and mission warnings actionable on collapsed %s cards', async (variant) => {
    const peer = run({ id: 'run_peer', member_id: bob.id })
    seed([working, peer])
    const activeMission = mission()
    useStore.setState({
      overlaps: {
        [working.id]: [{
          run_id: peer.id,
          member_id: bob.id,
          files: ['src/checkout.ts', 'src/payment.ts'],
        }],
      },
      missionDetails: {
        [activeMission.id]: {
          mission: activeMission,
          tasks: [],
          attempts: [],
          submissions: [],
          questions: [],
          plan_reviews: [],
          diagnostics: [{
            kind: 'observed_overlap',
            task_id: 'task_checkout',
            task_revision: 1,
            run_id: working.id,
            peer_run_id: peer.id,
            paths: ['src/checkout.ts'],
            detail: 'Checkout changes overlap the payment worker.',
          }],
        },
      },
    })
    render(
      <RunCard
        variant={variant}
        card={{ run: toRecord(working), state: 'working', owner: alice, unseen: false, paused: false }}
      />,
    )

    const disclosure = screen.getByRole('button', { name: `Show details for ${working.task}` })
    expect(disclosure.getAttribute('aria-expanded')).toBe('false')
    const fileWarning = screen.getByRole('button', { name: 'File overlap warnings: 1 other run' })
    const missionWarning = screen.getByRole('button', { name: 'Mission conflict warnings: 1' })
    fireEvent.click(fileWarning)
    const overlaps = await screen.findByRole('dialog', { name: 'File overlap warnings' })
    fireEvent.click(within(overlaps).getByRole('button', { name: '2 overlapping files with Bob, open their run' }))
    expect(useStore.getState().route).toEqual({ name: 'terminal', params: { runId: peer.id } })
    fireEvent.keyDown(overlaps, { key: 'Escape' })
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'File overlap warnings' })).toBeNull())

    act(() => useStore.setState({ route: { name: 'board', params: {} } }))
    fireEvent.click(missionWarning)
    const conflicts = await screen.findByRole('dialog', { name: 'Mission conflict warnings' })
    expect(within(conflicts).getByText('Checkout changes overlap the payment worker.')).toBeDefined()
    expect(useStore.getState().route).toEqual({ name: 'board', params: {} })
    fireEvent.click(within(conflicts).getByRole('button', { name: 'observed overlap · 1 path' }))
    expect(useStore.getState().route).toEqual({ name: 'terminal', params: { runId: peer.id } })
    expect(disclosure.getAttribute('aria-expanded')).toBe('false')
    fireEvent.keyDown(conflicts, { key: 'Escape' })
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Mission conflict warnings' })).toBeNull())

    act(() => useStore.setState({ overlaps: {}, missionDetails: {} }))
    expect(screen.queryByRole('button', { name: /^File overlap warnings:/ })).toBeNull()
    expect(screen.queryByRole('button', { name: /^Mission conflict warnings:/ })).toBeNull()
  })

  it.each([undefined, ''])('discloses a full multiline task with title %s without navigating', (title) => {
    const task = [
      'Review the checkout implementation and preserve existing payment retries, saved addresses, discount calculations, and receipt delivery.',
      '',
      '  Keep the migration reversible until the final verification is complete.',
    ].join('\n')
    const detailed = run({ ...working, title, task })
    seed([detailed])
    render(<Board />)

    expect(screen.queryByText(task, { exact: true, collapseWhitespace: false })).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: /^Show details for / }))

    const fullTask = screen.getByText(task, { exact: true, collapseWhitespace: false })
    expect(fullTask.textContent).toBe(task)
    fireEvent.click(fullTask)
    expect(useStore.getState().route).toEqual({ name: 'board', params: {} })
  })

  it('opens map details in a dismissible dialog and returns focus to the card', async () => {
    const detailed = run({
      ...working,
      title: 'Map checkout',
      task: 'Keep the full implementation notes available without navigating away.',
    })
    seed([detailed])
    render(
      <RunCard
        variant="map"
        card={{ run: toRecord(detailed), state: 'working', owner: alice, unseen: false, paused: false }}
      />,
    )
    const disclosure = screen.getByRole('button', { name: 'Show details for Map checkout' })
    disclosure.focus()
    fireEvent.click(disclosure)

    const dialog = await screen.findByRole('dialog', { name: 'Map checkout' })
    expect(within(dialog).getByText(detailed.task)).toBeDefined()
    expect(within(dialog).getByText(detailed.branch)).toBeDefined()
    fireEvent.click(within(dialog).getByText(detailed.task))
    expect(useStore.getState().route).toEqual({ name: 'board', params: {} })
    fireEvent.keyDown(dialog, { key: 'Escape' })

    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(document.activeElement).toBe(disclosure)
    expect(disclosure.getAttribute('aria-expanded')).toBe('false')
  })

  it('falls back to selecting the branch name where the clipboard is missing', async () => {
    // jsdom ships no navigator.clipboard - the environment (plain-http
    // origins, older engines) the fallback exists for. The click must not
    // throw; it selects the branch name for a manual copy instead.
    seed([working])
    render(<Board />)
    fireEvent.click(screen.getByRole('button', { name: /^Show details/ }))

    fireEvent.click(
      screen.getByRole('button', { name: `Copy branch ${working.branch}` }),
    )

    await vi.waitFor(() => {
      const selection = window.getSelection()
      expect(selection?.rangeCount).toBe(1)
      expect(selection?.getRangeAt(0).toString()).toBe(working.branch)
    })
    expect(useStore.getState().route).toEqual({ name: 'board', params: {} })
  })

  it('offers no branch copy in Details for a run whose checkout never got one', () => {
    // A run that failed in provisioning is marked failed before its branch is
    // assigned, so the card would carry an empty name and copy an empty string.
    seed([run({ id: 'run_nobranch', task: 'checkout failed', status: 'failed', branch: '' })])
    render(<Board />)

    const card = screen.getByRole('article')
    fireEvent.click(within(card).getByRole('button', { name: /^Show details/ }))
    expect(within(card).queryByRole('button', { name: /^Copy branch/ })).toBeNull()
  })

  it('does not open a card when selecting its attention explanation', () => {
    const attention = run({
      id: 'run_attention',
      task: 'waiting on a question',
      status: 'needs-attention',
      reason: 'stalled: no output or fi',
    })
    seed([attention])
    render(<Board />)

    const card = screen.getByRole('article')
    const explanation = within(card).getByText(attention.reason!)
    const range = document.createRange()
    range.selectNodeContents(explanation)
    const selection = window.getSelection()
    selection?.removeAllRanges()
    selection?.addRange(range)

    fireEvent.click(card)
    expect(useStore.getState().route).toEqual({ name: 'board', params: {} })

    selection?.removeAllRanges()
    fireEvent.click(card)
    expect(useStore.getState().route).toEqual({
      name: 'terminal',
      params: { runId: attention.id },
    })
  })

  it('shows only the active workspace, and follows a switch', () => {
    seed([working, elsewhere])
    render(<Board />)

    expect(column('Working').getByText('still going')).toBeDefined()
    expect(column('Working').queryByText('another workspace entirely')).toBeNull()

    act(() => useStore.getState().setActiveWorkspace(otherWorkspace.id))

    expect(column('Working').getByText('another workspace entirely')).toBeDefined()
    expect(column('Working').queryByText('still going')).toBeNull()
  })

  it('shows every run before hydration has named a workspace', () => {
    seed([working, elsewhere], '')
    render(<Board />)

    expect(column('Working').getByText('still going')).toBeDefined()
    expect(column('Working').getByText('another workspace entirely')).toBeDefined()
  })

  it('mutes a card once its run is acknowledged', () => {
    seed([stalled])
    render(<Board />)

    const card = screen.getByRole('article')
    expect(within(card).getByRole('button', { name: stalled.task, description: 'Unseen' })).toBeDefined()

    fireEvent.click(within(card).getByRole('button', { name: 'waiting on a question' }))

    expect(within(card).queryByRole('button', { name: stalled.task, description: 'Unseen' })).toBeNull()
    // The ack is app-wide, and the click reveals the run.
    expect(useStore.getState().acked[stalled.id]).toEqual({
      status: stalled.status,
      at: stalled.started_at,
    })
    expect(useStore.getState().route).toEqual({
      name: 'terminal',
      params: { runId: stalled.id },
    })
  })

  it('marks every run seen at once', () => {
    seed([stalled, working])
    render(<Board />)

    expect(screen.getAllByRole('button', { description: 'Unseen' })).toHaveLength(2)
    const markAll = screen.getByRole('button', { name: 'Mark all seen' })

    fireEvent.click(markAll)

    expect(screen.queryAllByRole('button', { description: 'Unseen' })).toEqual([])
    expect(Object.keys(useStore.getState().acked).sort()).toEqual([stalled.id, working.id].sort())
  })

  it('badges a paused run off the timeline stream, and clears it on resume', () => {
    seed([working])
    render(<Board />)
    expect(screen.queryByTitle('Paused')).toBeNull()

    // The whole point of the derivation: the run still reads `running`, and
    // only the steering entry says otherwise. Drive the real event path.
    act(() => timeline('pause', 1))
    expect(screen.getByTitle('Paused')).toBeDefined()
    expect(useStore.getState().runs[working.id].status).toBe('running')

    act(() => timeline('resume', 2))
    expect(screen.queryByTitle('Paused')).toBeNull()
  })
  it('marks a protected run with its access restriction badge', () => {
    const protectedRun = run({ protected: true })
    seed([protectedRun])
    render(<Board />)

    expect(
      screen.getByTitle('Protected: only the owner or an admin can steer or kill this run'),
    ).toBeDefined()
  })

  it('re-emphasizes an acknowledged run when it changes state again', () => {
    seed([working])
    render(<Board />)

    act(() => useStore.getState().ackRun(working.id))
    expect(screen.queryByRole('button', { name: working.task, description: 'Unseen' })).toBeNull()

    act(() =>
      useStore
        .getState()
        .applyRunStatus(working.id, 'needs-attention', 'plan approval', '2026-08-14T12:00:00Z'),
    )

    expect(column('Idle').getByRole('button', { name: working.task, description: 'Unseen' })).toBeDefined()
    expect(column('Idle').getByText('plan approval')).toBeDefined()
  })

  it('keeps a busy run in Working while its approval opens and closes', () => {
    seed([working])
    render(<Board />)
    expect(column('Working').getByText('still going')).toBeDefined()

    // The run still reads `running`; only the inbox says a human is needed.
    act(() =>
      useStore
        .getState()
        .setInbox(workspace.id, [approval({ run_id: working.id })]),
    )

    expect(column('Working').getByText('still going')).toBeDefined()
    expect(column('Working').getByRole('button', { name: /Needs input: 1 approval/ })).toBeDefined()
    expect(useStore.getState().runs[working.id].status).toBe('running')
    // No run.status event fired, so the run has no reason; the card's
    // summary is the pending question itself.
    expect(column('Working').getByText('write src/checkout.ts')).toBeDefined()
    fireEvent.click(column('Working').getByRole('button', { name: /Needs input: 1 approval/ }))
    expect(useStore.getState().route).toEqual({ name: 'approvals', params: {} })
    expect(useStore.getState().acked[working.id]).toBeUndefined()

    // Deciding the request clears the indicator without moving the card.
    act(() =>
      useStore
        .getState()
        .setInbox(workspace.id, [
          approval({ run_id: working.id, decision: 'approved' }),
        ]),
    )
    expect(column('Working').getByText('still going')).toBeDefined()
    expect(column('Working').queryByRole('button', { name: /Needs input:/ })).toBeNull()
  })

  it('closes native requests individually without moving a busy card out of Working', async () => {
    const first = { id: 'question', session_id: 'session-1', kind: 'question' as const }
    const second = { id: 'permission', session_id: 'session-2', kind: 'permission' as const }
    seed([working, stalled])
    render(<Board />)
    const input = (pending_inputs: (typeof first | typeof second)[], seq: number) =>
      applyEvent(useStore, {
        id: `input-${seq}`, seq, time: '2026-08-14T12:00:00Z', workspace_id: workspace.id,
        run_id: working.id, actor_id: '', type: 'run.input', payload: { pending_inputs },
      }, fakeApi())
    expect(column('Idle').queryByRole('button', { name: /Needs input:/ })).toBeNull()
    await act(() => input([first, second], 1))
    expect(column('Working').getByRole('button', { name: /Needs input: 1 question.*1 permission/ })).toBeDefined()
    await act(() => input([second], 2))
    expect(column('Working').getByRole('button', { name: /^Needs input: 1 permission request in Terminal$/ })).toBeDefined()
    await act(() => input([], 3))
    expect(column('Working').queryByRole('button', { name: /Needs input:/ })).toBeNull()
    expect(column('Working').getByText(working.task)).toBeDefined()
  })
  it('keeps unanswered room questions actionable without hiding ongoing work', () => {
    const questionRun = run({
      id: 'run_room_attention',
      task: 'answer the room',
      status: 'running',
      unanswered_questions: 1,
      reason: '',
    })
    seed([questionRun])
    render(<Board />)

    const active = column('Working')
    expect(active.getByText('answer the room')).toBeDefined()
    expect(active.getByRole('button', { name: /Needs input: 1 unanswered question/ })).toBeDefined()
    expect(active.getByText('1 unanswered question - open Run Room to answer')).toBeDefined()

    fireEvent.click(active.getByRole('button', { name: /Needs input:/ }))
    expect(useStore.getState().route).toEqual({
      name: 'terminal',
      params: { runId: questionRun.id },
    })
  })

  it('keeps a finished run in Done with its unanswered question actionable', () => {
    const finished = run({
      id: 'run_finished_question',
      task: 'answer after completion',
      status: 'completed',
      unanswered_questions: 1,
      reason: '',
      finished_at: '2026-08-14T10:30:00Z',
    })
    seed([finished])
    render(<Board />)

    const done = column('Done')
    expect(done.getByText('answer after completion')).toBeDefined()
    expect(done.getByText('1 unanswered question - open Run Room to answer')).toBeDefined()
    expect(done.getByRole('button', { name: /Needs input:/ })).toBeDefined()
    expect(column('Idle').queryByText('answer after completion')).toBeNull()
    expect(useStore.getState().runs[finished.id].status).toBe('completed')
  })
  it('keeps the unanswered-question action ahead of a failed lifecycle reason', () => {
    const failed = run({
      id: 'run_failed_question',
      task: 'answer after failure',
      status: 'failed',
      unanswered_questions: 1,
      reason: 'agent exited unexpectedly',
      finished_at: '2026-08-14T10:30:00Z',
    })
    seed([failed])
    render(<Board />)

    const done = column('Done')
    expect(done.getByText('1 unanswered question - open Run Room to answer')).toBeDefined()
    expect(done.getByText('Failed')).toBeDefined()
    fireEvent.click(done.getByRole('button', { name: 'Show details for answer after failure' }))
    expect(done.getByText('Lifecycle: Failed - agent exited unexpectedly')).toBeDefined()
  })

  it('deals an unreviewed agent outcome into Idle with its finished state, until it is seen', () => {
    const success = run({
      id: 'run_reported_success',
      task: 'agent says done',
      status: 'completed',
      reason: 'agent reported success',
      outcome_unseen: true,
      finished_at: '2026-08-14T10:30:00Z',
    })
    const failure = run({
      id: 'run_reported_failure',
      task: 'agent says stuck',
      status: 'failed',
      reason: 'agent reported failure; retained container',
      outcome_unseen: true,
      finished_at: '2026-08-14T10:31:00Z',
    })
    seed([success, failure])
    render(<Board />)

    const idle = column('Idle')
    expect(idle.getByText('The agent reported success; open the run to review it.')).toBeDefined()
    expect(idle.getByText('The agent reported failure; open the run to review it.')).toBeDefined()
    // The real state, not the amber needs-attention one.
    expect(idle.getByLabelText('Done')).toBeDefined()
    expect(idle.getByLabelText('Failed')).toBeDefined()
    expect(idle.queryByLabelText('Idle')).toBeNull()
    // A report to review is not a structured input request.
    expect(idle.queryByRole('button', { name: /Needs input:/ })).toBeNull()
    expect(column('Done').queryByText('agent says done')).toBeNull()

    act(() => useStore.getState().applyOutcomeSeen(success.id))
    expect(column('Done').getByText('agent says done')).toBeDefined()
    expect(column('Done').queryByText(/open the run to review it/)).toBeNull()
    expect(idle.getByText('agent says stuck')).toBeDefined()
  })

  it('keeps the launch action in the empty-board notice only', () => {
    seedAs(alice, [working])
    render(<Board />)

    expect(screen.queryByRole('button', { name: 'New run' })).toBeNull()
    act(() => useStore.setState({ runs: {} }))
    fireEvent.click(screen.getByRole('button', { name: 'New run' }))

    // The form is hosted app-wide; the board only asks for it.
    expect(useStore.getState().paletteDialog).toBe('launch')
  })

  it('does not offer the empty-board launch action without launch capability', () => {
    seedAs(alice, [])
    useStore.setState({ capabilities: { gateway: 'remote', methods: [], ws: [] } })
    render(<Board />)
    expect(screen.getByText('No runs yet')).toBeDefined()
    expect(screen.queryByRole('button', { name: 'New run' })).toBeNull()
  })

  it('says an empty workspace is empty once, not four times', () => {
    seed([])
    render(<Board />)

    // The notice replaces the column row rather than sitting above it: three
    // empty buckets each saying "Nothing here." add nothing to the one notice
    // that says what a run is and offers the way to start one.
    const notice = screen.getByText(/No runs yet/).closest('div') as HTMLElement
    expect(within(notice).getByRole('button', { name: 'New run' })).toBeDefined()
    expect(screen.queryAllByText('Nothing here.')).toHaveLength(0)
    for (const bucket of ['Idle', 'Working', 'Done']) {
      expect(screen.queryByRole('region', { name: bucket })).toBeNull()
    }
  })

  it('says nothing in a bucket it has not heard about yet', () => {
    // A cold load: not hydrated, and useDelayed holds the skeletons back
    // 200ms, so this is the window where the buckets used to print the same
    // "Nothing here." three times that the one notice exists to replace.
    seed([])
    useStore.setState({ hydrated: false })
    render(<Board />)

    expect(screen.queryAllByText('Nothing here.')).toHaveLength(0)
    expect(screen.queryByText(/No runs yet/)).toBeNull()
    // The buckets themselves are there - only their placeholder is withheld.
    expect(screen.getByRole('region', { name: 'Working' })).toBeDefined()
  })

  it('says "Nothing here." only in the buckets a filled board left empty', () => {
    // The other two placeholder states. One run means the notice is gone, so
    // the empty buckets are worth labelling: they are empty, not unknown.
    seed([working])
    render(<Board />)

    expect(column('Working').getByText('still going')).toBeDefined()
    expect(column('Idle').getByText('Nothing here.')).toBeDefined()
    expect(column('Done').getByText('Nothing here.')).toBeDefined()
    expect(column('Working').queryByText('Nothing here.')).toBeNull()
  })

  it('shows skeletons once a slow load has run past the delay', () => {
    vi.useFakeTimers()
    try {
      seed([])
      useStore.setState({ hydrated: false })
      render(<Board />)
      expect(document.querySelectorAll('[data-slot="skeleton"]')).toHaveLength(0)

      // useDelayed flips at 200ms, which is what keeps the skeletons from
      // flashing on a load that was never slow.
      act(() => vi.advanceTimersByTime(200))

      expect(document.querySelectorAll('[data-slot="skeleton"]').length).toBeGreaterThan(0)
      expect(screen.queryAllByText('Nothing here.')).toHaveLength(0)
    } finally {
      vi.useRealTimers()
    }
  })

  it('returns to the notice when the last run is deleted', () => {
    // The other way a member reaches the notice, and the transition the
    // empty-to-populated test above does not cover.
    seed([working])
    render(<Board />)
    expect(screen.queryByText(/No runs yet/)).toBeNull()

    act(() => useStore.setState({ runs: {} }))

    expect(screen.getByText(/No runs yet/)).toBeDefined()
    expect(screen.queryAllByText('Nothing here.')).toHaveLength(0)
  })

  it('brings the buckets back as soon as a run lands', () => {
    seed([])
    render(<Board />)
    expect(screen.queryByRole('region', { name: 'Working' })).toBeNull()

    act(() =>
      useStore.setState({ runs: { [working.id]: toRecord(working) } }),
    )

    expect(screen.getByRole('region', { name: 'Working' })).toBeDefined()
    expect(screen.queryByText(/No runs yet/)).toBeNull()
  })


  it('hides an archived run from Done, and reveals it with its deletion badge behind the toggle', () => {
    vi.useFakeTimers()
    try {
      // 8 whole days before archivedMerged.deletes_at.
      vi.setSystemTime(new Date('2026-08-20T10:00:00Z'))
      seed([merged, archivedMerged])
      render(<Board />)

      expect(column('Done').getByText('landed already')).toBeDefined()
      expect(column('Done').queryByText('already archived')).toBeNull()

      const toggle = screen.getByRole('button', { name: 'Archived 1' })
      expect(toggle.getAttribute('aria-pressed')).toBe('false')

      fireEvent.click(toggle)

      expect(column('Done').getByText('already archived')).toBeDefined()
      expect(column('Done').queryByText('landed already')).toBeNull()
      expect(column('Done').getByText('deleted in 8 days')).toBeDefined()
      expect(toggle.getAttribute('aria-pressed')).toBe('true')
    } finally {
      vi.useRealTimers()
    }
  })

  it('switches archived runs from the Map Runs strip without hiding active runs', () => {
    const finished = run({ ...merged, status: 'merged' })
    seedAs(alice, [working, finished, archivedMerged])
    useStore.setState({ boardView: 'map' })
    render(<Board />)
    const header = screen.getByRole('heading', { name: 'Runs' }).closest('header')!
    expect(within(header).getByRole('button', { name: 'Archive closed runs...' })).toBeDefined()
    const toggle = within(header).getByRole('button', { name: 'Archived 1' })
    fireEvent.click(toggle)
    expect(screen.getByRole('button', { name: working.task })).toBeDefined()
    expect(screen.getByRole('button', { name: archivedMerged.task })).toBeDefined()
    expect(screen.queryByRole('button', { name: finished.task })).toBeNull()
    expect(within(header).queryByRole('button', { name: 'Archive closed runs...' })).toBeNull()
    fireEvent.click(toggle)
    expect(screen.getByRole('button', { name: finished.task })).toBeDefined()
    expect(screen.queryByRole('button', { name: archivedMerged.task })).toBeNull()
    expect(within(header).getByRole('button', { name: 'Archive closed runs...' })).toBeDefined()
  })

  it('resets the toggle once the last archived run is restored', () => {
    seed([merged, archivedMerged])
    render(<Board />)
    fireEvent.click(screen.getByRole('button', { name: 'Archived 1' }))
    expect(column('Done').getByText('already archived')).toBeDefined()

    act(() => useStore.getState().applyRunArchived(archivedMerged.id, null, null))

    expect(screen.queryByRole('button', { name: /^Archived/ })).toBeNull()
    expect(column('Done').getByText('landed already')).toBeDefined()
  })

  it('renders the grid and its toggle when every run in scope is archived', () => {
    // Nothing lands in a bucket, so `total` must still count the archived
    // runs behind the toggle - otherwise the board falls into the
    // empty-workspace notice and the toggle (the only way back to them)
    // never mounts.
    seed([archivedMerged])
    render(<Board />)

    expect(screen.queryByText(/No runs yet/)).toBeNull()
    expect(screen.getByRole('region', { name: 'Done' })).toBeDefined()
    const toggle = screen.getByRole('button', { name: 'Archived 1' })

    fireEvent.click(toggle)
    expect(column('Done').getByText('already archived')).toBeDefined()
  })

  it('resets the archived toggle when the active workspace changes', () => {
    const archivedElsewhere = run({
      id: 'run_archived_elsewhere',
      task: 'archived over there',
      status: 'merged',
      workspace_id: otherWorkspace.id,
      finished_at: '2026-08-10T10:30:00Z',
      archived_at: '2026-08-14T10:00:00Z',
      deletes_at: '2026-08-28T10:00:00Z',
    })
    seed([merged, archivedMerged, archivedElsewhere])
    render(<Board />)

    const toggle = screen.getByRole('button', { name: 'Archived 1' })
    fireEvent.click(toggle)
    expect(toggle.getAttribute('aria-pressed')).toBe('true')

    act(() => useStore.getState().setActiveWorkspace(otherWorkspace.id))

    const otherToggle = screen.getByRole('button', { name: 'Archived 1' })
    expect(otherToggle.getAttribute('aria-pressed')).toBe('false')
    expect(column('Done').queryByText('archived over there')).toBeNull()
  })

  it('never hides a run carrying archived_at unless its status is final', () => {
    // A defensive guard: the server only sets archived_at on a final run, but
    // the board must not hide a live one even so.
    const stillLive = run({
      id: 'run_still_live',
      task: 'live despite archived_at',
      status: 'running',
      archived_at: '2026-08-14T10:00:00Z',
      deletes_at: '2026-08-28T10:00:00Z',
    })
    seed([stillLive])
    render(<Board />)

    expect(column('Working').getByText('live despite archived_at')).toBeDefined()
    expect(screen.queryByRole('button', { name: /^Archived/ })).toBeNull()
  })
})

describe('archive closed runs', () => {
  it.each(['cards', 'map'] as const)('archives only eligible collaborator runs from %s, skipping completed and protected runs', async (view) => {
    const eligible = run({
      id: 'run_clear_eligible',
      status: 'merged',
      member_id: alice.id,
      finished_at: '2026-08-14T10:10:00Z',
    })
    const stillOpen = run({
      id: 'run_clear_completed',
      status: 'completed',
      member_id: alice.id,
      finished_at: '2026-08-14T10:05:00Z',
    })
    const someoneElsesProtected = run({
      id: 'run_clear_protected',
      status: 'merged',
      member_id: alice.id,
      protected: true,
      finished_at: '2026-08-14T10:15:00Z',
    })
    seedAs(bob, [eligible, stillOpen, someoneElsesProtected])
    useStore.setState({ boardView: view })
    render(<Board />)

    fireEvent.click(screen.getByRole('button', { name: 'Archive closed runs...' }))
    const dialog = within(await screen.findByRole('dialog'))

    expect(
      dialog.getByText(
        '1 run stays: completed but not yet closed - close them as merged or abandoned first.',
      ),
    ).toBeDefined()
    expect(dialog.getByText('1 run stays: you may not act on it.')).toBeDefined()

    fireEvent.click(dialog.getByRole('button', { name: 'Archive 1' }))

    await waitFor(() => expect(toast.success).toHaveBeenCalledWith('Archived 1 run'))
    expect(api.runArchive).toHaveBeenCalledTimes(1)
    expect(api.runArchive).toHaveBeenCalledWith(eligible.id, true)
    expect(api.runArchive).not.toHaveBeenCalledWith(stillOpen.id, true)
    expect(api.runArchive).not.toHaveBeenCalledWith(someoneElsesProtected.id, true)
  })

  it('lets an admin archive every eligible run regardless of ownership or protection', async () => {
    const eligible = run({
      id: 'run_clear_eligible2',
      status: 'merged',
      member_id: bob.id,
      finished_at: '2026-08-14T10:10:00Z',
    })
    const stillOpen = run({
      id: 'run_clear_completed2',
      status: 'completed',
      member_id: bob.id,
      finished_at: '2026-08-14T10:05:00Z',
    })
    const someoneElsesProtected = run({
      id: 'run_clear_protected2',
      status: 'merged',
      member_id: bob.id,
      protected: true,
      finished_at: '2026-08-14T10:15:00Z',
    })
    seedAs(alice, [eligible, stillOpen, someoneElsesProtected])
    render(<Board />)

    fireEvent.click(column('Done').getByRole('button', { name: 'Archive closed runs...' }))
    const dialog = within(await screen.findByRole('dialog'))

    expect(
      dialog.getByText(
        '1 run stays: completed but not yet closed - close them as merged or abandoned first.',
      ),
    ).toBeDefined()
    expect(dialog.queryByText(/you may not act on/)).toBeNull()

    fireEvent.click(dialog.getByRole('button', { name: 'Archive 2' }))

    // someoneElsesProtected finished later, so its call is issued first:
    // eligible runs go newest first, same as the Done column itself.
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith('Archived 2 runs'))
    expect(api.runArchive).toHaveBeenCalledTimes(2)
    expect(api.runArchive).toHaveBeenNthCalledWith(1, someoneElsesProtected.id, true)
    expect(api.runArchive).toHaveBeenNthCalledWith(2, eligible.id, true)
    expect(api.runArchive).not.toHaveBeenCalledWith(stillOpen.id, true)
  })

  it('keeps at most six archives in flight and starts the next as one settles', async () => {
    const runs = Array.from({ length: 8 }, (_, i) =>
      run({
        id: `run_pool_${i}`,
        status: 'merged',
        finished_at: `2026-08-14T10:${String(50 - i).padStart(2, '0')}:00Z`,
      }),
    )
    seedAs(alice, runs)
    render(<Board />)

    const calls: string[] = []
    const settle = new Map<string, { resolve: () => void; reject: (err: unknown) => void }>()
    vi.mocked(api.runArchive).mockImplementation((id: string) => {
      calls.push(id)
      return new Promise((resolve, reject) => {
        settle.set(id, {
          resolve: () => resolve(run({ id, status: 'merged', archived_at: '2026-08-14T11:00:00Z' })),
          reject,
        })
      })
    })

    fireEvent.click(column('Done').getByRole('button', { name: 'Archive closed runs...' }))
    fireEvent.click(
      within(await screen.findByRole('dialog')).getByRole('button', { name: 'Archive 8' }),
    )

    const ids = runs.map((r) => r.id)
    await waitFor(() => expect(calls).toEqual(ids.slice(0, 6)))

    // A failure frees its slot like a success does, and the run after the
    // cap fails too: the toast still names the earlier one in Done order.
    settle.get(ids[3])?.reject(new ApiError(500, 'fourth failed', -32001))
    await waitFor(() => expect(calls).toEqual(ids.slice(0, 7)))
    settle.get(ids[6])?.reject(new ApiError(500, 'seventh failed', -32001))
    await waitFor(() => expect(calls).toEqual(ids))
    expect(toast.error).not.toHaveBeenCalled()

    for (const id of ids) settle.get(id)?.resolve()
    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith('Archived 6, 2 failed: fourth failed'),
    )
    expect(api.runArchive).toHaveBeenCalledTimes(8)
  })

  it('issues archives without waiting on each other, newest first', async () => {
    const first = run({
      id: 'run_seq_first',
      status: 'failed',
      finished_at: '2026-08-14T10:20:00Z',
    })
    const second = run({
      id: 'run_seq_second',
      status: 'merged',
      finished_at: '2026-08-14T10:10:00Z',
    })
    seedAs(alice, [first, second])
    render(<Board />)

    const calls: string[] = []
    let resolveFirst: (() => void) | undefined
    vi.mocked(api.runArchive).mockImplementation((id: string) => {
      calls.push(id)
      if (calls.length === 1) {
        return new Promise((resolve) => {
          resolveFirst = () =>
            resolve(run({ id, status: 'failed', archived_at: '2026-08-14T11:00:00Z' }))
        })
      }
      return Promise.resolve(run({ id, status: 'merged', archived_at: '2026-08-14T11:00:00Z' }))
    })

    fireEvent.click(column('Done').getByRole('button', { name: 'Archive closed runs...' }))
    fireEvent.click(
      within(await screen.findByRole('dialog')).getByRole('button', { name: 'Archive 2' }),
    )

    // The second call goes out while the first is still pending.
    await waitFor(() => expect(calls).toEqual([first.id, second.id]))
    expect(toast.success).not.toHaveBeenCalled()

    resolveFirst?.()
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith('Archived 2 runs'))
  })

  it('keeps going past a failure and reports the real server error once', async () => {
    const ok = run({ id: 'run_fail_ok', status: 'merged', finished_at: '2026-08-14T10:20:00Z' })
    const bad = run({ id: 'run_fail_bad', status: 'failed', finished_at: '2026-08-14T10:10:00Z' })
    seedAs(alice, [ok, bad])
    render(<Board />)

    // A different code than the not-found one below, so a pass here proves
    // the match is on the code and not just on being an ApiError.
    vi.mocked(api.runArchive).mockImplementation(async (id: string) => {
      if (id === bad.id) throw new ApiError(500, 'workspace is locked', -32001)
      return run({ id, status: 'merged', archived_at: '2026-08-14T11:00:00Z' })
    })

    fireEvent.click(column('Done').getByRole('button', { name: 'Archive closed runs...' }))
    fireEvent.click(
      within(await screen.findByRole('dialog')).getByRole('button', { name: 'Archive 2' }),
    )

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith('Archived 1, 1 failed: workspace is locked'),
    )
    expect(api.runArchive).toHaveBeenCalledTimes(2)
    expect(toast.success).not.toHaveBeenCalled()
  })

  it('names the first failure in Done order even when a later one fails sooner', async () => {
    const newest = run({ id: 'run_ord_newest', status: 'failed', finished_at: '2026-08-14T10:30:00Z' })
    const middle = run({ id: 'run_ord_middle', status: 'merged', finished_at: '2026-08-14T10:20:00Z' })
    const oldest = run({ id: 'run_ord_oldest', status: 'failed', finished_at: '2026-08-14T10:10:00Z' })
    seedAs(alice, [newest, middle, oldest])
    render(<Board />)

    let failNewest: (() => void) | undefined
    vi.mocked(api.runArchive).mockImplementation((id: string) => {
      if (id === newest.id) {
        return new Promise((_, reject) => {
          failNewest = () => reject(new ApiError(500, 'newest failed', -32001))
        })
      }
      if (id === oldest.id) return Promise.reject(new ApiError(500, 'oldest failed', -32001))
      return Promise.resolve(run({ id, status: 'merged', archived_at: '2026-08-14T11:00:00Z' }))
    })

    fireEvent.click(column('Done').getByRole('button', { name: 'Archive closed runs...' }))
    fireEvent.click(
      within(await screen.findByRole('dialog')).getByRole('button', { name: 'Archive 3' }),
    )

    await waitFor(() => expect(api.runArchive).toHaveBeenCalledTimes(3))
    expect(toast.error).not.toHaveBeenCalled()

    failNewest?.()
    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith('Archived 1, 2 failed: newest failed'),
    )
  })

  it('treats a not-found failure as success and removes that run locally', async () => {
    const kept = run({ id: 'run_kept', status: 'merged', finished_at: '2026-08-14T10:20:00Z' })
    const gone = run({ id: 'run_gone', status: 'merged', finished_at: '2026-08-14T10:10:00Z' })
    seedAs(alice, [kept, gone])
    render(<Board />)

    // Only the older run is gone, so a removal keyed to the wrong call
    // would drop `kept` instead.
    let resolveKept: (() => void) | undefined
    vi.mocked(api.runArchive).mockImplementation((id: string) => {
      if (id === gone.id) return Promise.reject(new ApiError(404, 'run.archive: not found', -32000))
      return new Promise((resolve) => {
        resolveKept = () =>
          resolve(run({ id, status: 'merged', archived_at: '2026-08-14T11:00:00Z' }))
      })
    })

    fireEvent.click(column('Done').getByRole('button', { name: 'Archive closed runs...' }))
    fireEvent.click(
      within(await screen.findByRole('dialog')).getByRole('button', { name: 'Archive 2' }),
    )

    // The gone run leaves the board without waiting on the pending archive.
    await waitFor(() => expect(useStore.getState().runs[gone.id]).toBeUndefined())
    expect(useStore.getState().runs[kept.id]).toBeDefined()
    expect(toast.success).not.toHaveBeenCalled()

    resolveKept?.()
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith('Archived 2 runs'))
    expect(useStore.getState().runs[kept.id]).toBeDefined()
  })

  it('moves focus to the Done heading once a not-found removal leaves nothing archived to fall back on', async () => {
    const gone = run({ id: 'run_gone_focus', status: 'merged', finished_at: '2026-08-14T10:10:00Z' })
    // A run left in Working, so removing `gone` leaves Done empty rather
    // than emptying the whole board - the Done heading has to stay mounted
    // for this to be a meaningful fallback target.
    const other = run({ id: 'run_other_focus', status: 'running' })
    seedAs(alice, [gone, other])
    render(<Board />)

    vi.mocked(api.runArchive).mockRejectedValue(
      new ApiError(404, 'run.archive: not found', -32000),
    )

    fireEvent.click(column('Done').getByRole('button', { name: 'Archive closed runs...' }))
    fireEvent.click(
      within(await screen.findByRole('dialog')).getByRole('button', { name: 'Archive 1' }),
    )

    await waitFor(() => expect(toast.success).toHaveBeenCalledWith('Archived 1 run'))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(screen.queryByRole('button', { name: /^Archived/ })).toBeNull()
    await waitFor(() =>
      expect(document.activeElement).toBe(screen.getByRole('heading', { name: 'Done' })),
    )
  })

  it('moves focus to the Archived toggle once a successful clear archives the last live card', async () => {
    const solo = run({ id: 'run_solo_focus', status: 'merged', finished_at: '2026-08-14T10:10:00Z' })
    seedAs(alice, [solo])
    render(<Board />)

    // The RPC succeeds but carries no store update of its own; the run only
    // moves once its run.archived event lands, same as a live gateway.
    vi.mocked(api.runArchive).mockImplementation(async (id: string) => {
      await archived(id, 1, { archived_at: '2026-08-14T11:00:00Z', deletes_at: '2026-08-28T10:00:00Z' })
      return run({ id, status: 'merged' })
    })

    fireEvent.click(column('Done').getByRole('button', { name: 'Archive closed runs...' }))
    fireEvent.click(
      within(await screen.findByRole('dialog')).getByRole('button', { name: 'Archive 1' }),
    )

    await waitFor(() => expect(toast.success).toHaveBeenCalledWith('Archived 1 run'))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    const toggle = await screen.findByRole('button', { name: 'Archived 1' })
    await waitFor(() => expect(document.activeElement).toBe(toggle))
  })

  it('keeps the dialog open on the plan it started with while an archive it already ran shrinks Done underneath it', async () => {
    const first = run({
      id: 'run_snap_first',
      status: 'failed',
      finished_at: '2026-08-14T10:20:00Z',
    })
    const second = run({
      id: 'run_snap_second',
      status: 'merged',
      finished_at: '2026-08-14T10:10:00Z',
    })
    seedAs(alice, [first, second])
    render(<Board />)

    let resolveFirst: (() => void) | undefined
    vi.mocked(api.runArchive).mockImplementation((id: string) => {
      if (id === first.id) {
        return new Promise((resolve) => {
          resolveFirst = () => {
            // The RPC settling is not what shrinks Done - its run.archived
            // event, fired here the way the server would, is.
            void archived(id, 1, { archived_at: '2026-08-14T11:00:00Z', deletes_at: null })
            resolve(run({ id, status: 'failed' }))
          }
        })
      }
      // The second call is never meant to settle within this test: only
      // that the dialog survives the first one landing matters here.
      return new Promise(() => {})
    })

    fireEvent.click(column('Done').getByRole('button', { name: 'Archive closed runs...' }))
    fireEvent.click(
      within(await screen.findByRole('dialog')).getByRole('button', { name: 'Archive 2' }),
    )

    resolveFirst?.()
    await waitFor(() => expect(api.runArchive).toHaveBeenCalledWith(second.id, true))
    // Done has shrunk underneath the dialog: `first` carries archived_at now.
    await waitFor(() =>
      expect(useStore.getState().runs[first.id]?.archived_at).toBe('2026-08-14T11:00:00Z'),
    )

    const dialog = within(screen.getByRole('dialog'))
    expect(dialog.getByRole('button', { name: 'Archive 2' })).toBeDefined()
  })

  it('renders no bulk archive button when nothing in Done is eligible', () => {
    const stillOpen = run({ id: 'run_none_eligible', status: 'completed' })
    seedAs(alice, [stillOpen])
    render(<Board />)

    expect(screen.queryByRole('button', { name: 'Archive closed runs...' })).toBeNull()
  })

  it('hides bulk archive while the Done column is showing archived runs', () => {
    const eligible = run({
      id: 'run_arch_view_eligible',
      status: 'merged',
      finished_at: '2026-08-14T10:10:00Z',
    })
    const archived = run({
      id: 'run_arch_view_archived',
      status: 'merged',
      finished_at: '2026-08-10T10:00:00Z',
      archived_at: '2026-08-14T10:00:00Z',
      deletes_at: '2026-08-28T10:00:00Z',
    })
    seedAs(alice, [eligible, archived])
    render(<Board />)

    expect(column('Done').getByRole('button', { name: 'Archive closed runs...' })).toBeDefined()

    fireEvent.click(screen.getByRole('button', { name: 'Archived 1' }))

    expect(column('Done').queryByRole('button', { name: 'Archive closed runs...' })).toBeNull()
  })
})

describe('release finished resources', () => {
  it.each(['cards', 'map'] as const)('includes archived history in %s without releasing active or forbidden runs', async (view) => {
    const done = run({ id: 'release_done', status: 'completed', reason: 'agent reported success; retained container' })
    const archived = run({
      id: 'release_archived', status: 'merged', reason: 'closed; retained container',
      archived_at: '2026-08-14T10:00:00Z', deletes_at: '2026-08-28T10:00:00Z',
    })
    const blocked = run({
      id: 'release_blocked', status: 'failed', reason: 'agent reported failure; retained container',
      protected: true, member_id: alice.id,
    })
    const waiting = run({ id: 'release_waiting', status: 'needs-attention', reason: 'worker finished; retained container' })
    const elsewhere = run({
      id: 'release_elsewhere', status: 'merged', reason: 'closed; retained container',
      workspace_id: otherWorkspace.id,
    })
    seedAs(bob, [done, archived, blocked, waiting, elsewhere])
    useStore.setState({ boardView: view })
    render(<Board />)
    fireEvent.click(screen.getByRole('button', { name: 'Archived 1' }))
    fireEvent.click(screen.getByRole('button', { name: 'Release finished resources...' }))
    const dialog = within(await screen.findByRole('dialog'))
    expect(dialog.getByRole('button', { name: 'Release 2' })).toBeDefined()
    fireEvent.click(dialog.getByRole('button', { name: 'Release 2' }))
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith('Released resources for 2 runs'))
    expect(api.runRelease).toHaveBeenCalledTimes(2)
    expect(api.runRelease).toHaveBeenCalledWith(done.id)
    expect(api.runRelease).toHaveBeenCalledWith(archived.id)
    expect(useStore.getState().runs[archived.id]?.archived_at).toBeDefined()
    expect(api.runArchive).not.toHaveBeenCalled()
  })

  it('bounds concurrency, continues after errors and reports the first real refusal', async () => {
    const runs = Array.from({ length: 8 }, (_, i) => run({
      id: `release_pool_${i}`, status: 'merged', reason: 'closed; retained container',
    }))
    seedAs(alice, runs)
    render(<Board />)
    const pending: Array<{ resolve: () => void; reject: (err: unknown) => void }> = []
    vi.mocked(api.runRelease).mockImplementation(() => {
      const { promise, resolve, reject } = Promise.withResolvers<Record<string, never>>()
      pending.push({ resolve: () => resolve({}), reject })
      return promise
    })
    fireEvent.click(screen.getByRole('button', { name: 'Release finished resources...' }))
    fireEvent.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Release 8' }))
    await waitFor(() => expect(api.runRelease).toHaveBeenCalledTimes(6))
    pending[1].reject(new ApiError(409, 'retained evidence unavailable', -32001))
    await waitFor(() => expect(api.runRelease).toHaveBeenCalledTimes(7))
    pending[6].reject(new ApiError(409, 'container gone', -32001))
    await waitFor(() => expect(api.runRelease).toHaveBeenCalledTimes(8))
    for (const job of pending) job.resolve()
    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith('Released 6, 2 failed: retained evidence unavailable'),
    )
    expect(useStore.getState().runs[runs[0].id]).toBeDefined()
  })

  it('is absent for released history or gateways without run.release', () => {
    seedAs(alice, [run({ status: 'merged', reason: 'retained container unavailable' })])
    const view = render(<Board />)
    expect(screen.queryByRole('button', { name: 'Release finished resources...' })).toBeNull()
    view.unmount()
    seedAs(alice, [run({ status: 'merged', reason: 'closed; retained container' })])
    useStore.setState({ capabilities: { gateway: 'remote', methods: ['run.archive'], ws: [] } })
    render(<Board />)
    expect(screen.queryByRole('button', { name: 'Release finished resources...' })).toBeNull()
  })
})
