import { act, fireEvent, render, renderHook, screen, waitFor, within } from '@testing-library/react'
import { toast } from 'sonner'
import { registerSlot } from '@/components/slots'
import { api, ApiError } from '@/lib/api'
import type { GatewayCapabilities, Run } from '@/lib/types'
import { Board } from '@/routes/board'
import { useBoard } from '@/routes/board/selectors'
import { useStore } from '@/store'
import { toRecord } from '@/store/runs'
import { applyEvent } from '@/store/sync'
import {
  alice,
  approval,
  bob,
  fakeApi,
  otherWorkspace,
  run,
  serverInfo,
  workspace,
} from '@/test/fixtures'
import { hintOn } from '@/test/tooltip'

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
    members: { [alice.id]: alice, [bob.id]: bob },
    runs: Object.fromEntries(runs.map((r) => [r.id, toRecord(r)])),
    acked: {},
    pausedRuns: {},
    inbox: {},
    hydrated: true,
    hydrationError: null,
    lastSeq: 0,
    route: { name: 'board', params: {} },
  })
}

const everyMethod: GatewayCapabilities = { gateway: 'remote', methods: ['*'], ws: [] }

/** `seed`, plus the capability and caller identity Clear done reads. */
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
// dots, which carry the same words.
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
 * it on - Clear done relies on this, not on the RPC response, to move a run
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
  it('deals runs into the three buckets, newest first, the working one bouncing', () => {
    seed([stalled, working, queued, merged])
    render(<Board />)

    expect(column('Needs you').getByText('waiting on a question')).toBeDefined()
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
    // A card carries no state in words, so the running one bounces.
    const card = column('Working').getByText('still going').closest('article')
    expect(card?.querySelector('.working-dots')).not.toBeNull()
  })

  it('carries the whole branch name and copies it', async () => {
    seed([working])
    render(<Board />)
    const card = screen.getByRole('article')
    // The name is truncated on the card, so the title carries all of it.
    expect(within(card).getByTitle(working.branch)).toBeDefined()

    const writeText = vi.fn(async () => {})
    vi.stubGlobal('navigator', { clipboard: { writeText } })
    fireEvent.click(
      within(card).getByRole('button', { name: `Copy branch ${working.branch}` }),
    )

    await vi.waitFor(() => expect(writeText).toHaveBeenCalledWith(working.branch))
  })

  it('falls back to selecting the branch name where the clipboard is missing', async () => {
    // jsdom ships no navigator.clipboard - the environment (plain-http
    // origins, older engines) the fallback exists for. The click must not
    // throw; it selects the branch name for a manual copy instead.
    seed([working])
    render(<Board />)

    fireEvent.click(
      screen.getByRole('button', { name: `Copy branch ${working.branch}` }),
    )

    await vi.waitFor(() => {
      const selection = window.getSelection()
      expect(selection?.rangeCount).toBe(1)
      expect(selection?.getRangeAt(0).toString()).toBe(working.branch)
    })
  })

  it('offers no branch chip for a run whose checkout never got one', () => {
    // A run that failed in provisioning is marked failed before its branch is
    // assigned, so the card would carry an empty name and copy an empty string.
    seed([run({ id: 'run_nobranch', task: 'checkout failed', status: 'failed', branch: '' })])
    render(<Board />)

    const card = screen.getByRole('article')
    expect(within(card).queryByRole('button', { name: /^Copy branch/ })).toBeNull()
    expect(card.querySelector('.lucide-git-branch')).toBeNull()
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
    expect(within(card).getByLabelText('Unseen')).toBeDefined()

    fireEvent.click(within(card).getByRole('button', { name: 'waiting on a question' }))

    expect(screen.queryByLabelText('Unseen')).toBeNull()
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

  it('marks every run seen at once', async () => {
    seed([stalled, working])
    render(<Board />)

    expect(screen.getAllByLabelText('Unseen')).toHaveLength(2)
    const markAll = screen.getByRole('button', { name: 'Mark all seen' })
    // The button says "seen"; only the hint says how many runs that is.
    expect(await hintOn(markAll)).toBe('Mark every run seen')

    fireEvent.click(markAll)

    expect(screen.queryByLabelText('Unseen')).toBeNull()
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
    expect(screen.queryByLabelText('Unseen')).toBeNull()

    act(() =>
      useStore
        .getState()
        .applyRunStatus(working.id, 'needs-attention', 'plan approval', '2026-08-14T12:00:00Z'),
    )

    expect(screen.getByLabelText('Unseen')).toBeDefined()
    expect(column('Needs you').getByText('plan approval')).toBeDefined()
  })

  it('deals a running run with a pending approval into Needs you', () => {
    seed([working])
    render(<Board />)
    expect(column('Working').getByText('still going')).toBeDefined()

    // The run still reads `running`; only the inbox says a human is needed.
    act(() =>
      useStore
        .getState()
        .setInbox(workspace.id, [approval({ run_id: working.id })]),
    )

    expect(column('Needs you').getByText('still going')).toBeDefined()
    expect(useStore.getState().runs[working.id].status).toBe('running')
    // No run.status event fired, so the run has no reason; the card's
    // summary is the pending question itself.
    expect(column('Needs you').getByText('write src/checkout.ts')).toBeDefined()

    // Deciding the request sends the card back to Working.
    act(() =>
      useStore
        .getState()
        .setInbox(workspace.id, [
          approval({ run_id: working.id, decision: 'approved' }),
        ]),
    )
    expect(column('Working').getByText('still going')).toBeDefined()
  })
  it('deals unanswered room questions into Needs you with an action summary', () => {
    const questionRun = run({
      id: 'run_room_attention',
      task: 'answer the room',
      status: 'running',
      unanswered_questions: 1,
      reason: '',
    })
    seed([questionRun])
    render(<Board />)

    const needsYou = column('Needs you')
    expect(needsYou.getByText('answer the room')).toBeDefined()
    expect(needsYou.getByText('1 unanswered question - open Run Room to answer')).toBeDefined()

    fireEvent.click(needsYou.getByRole('button', { name: 'answer the room' }))
    expect(useStore.getState().route).toEqual({
      name: 'terminal',
      params: { runId: questionRun.id },
    })
  })

  it('keeps a finished run with unanswered questions in Needs you with lifecycle context', () => {
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

    const needsYou = column('Needs you')
    expect(needsYou.getByText('answer after completion')).toBeDefined()
    expect(needsYou.getByText('1 unanswered question - open Run Room to answer')).toBeDefined()
    expect(needsYou.getByText('Lifecycle: Completed')).toBeDefined()
    expect(column('Done').queryByText('answer after completion')).toBeNull()
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

    const needsYou = column('Needs you')
    expect(needsYou.getByText('1 unanswered question - open Run Room to answer')).toBeDefined()
    expect(needsYou.getByText('Lifecycle: Failed - agent exited unexpectedly')).toBeDefined()
  })

  it('pluralizes the unanswered room question summary', () => {
    const questionRun = run({
      id: 'run_room_attention_plural',
      task: 'answer both rooms',
      status: 'running',
      unanswered_questions: 2,
      reason: '',
    })
    seed([questionRun])
    render(<Board />)

    expect(
      column('Needs you').getByText('2 unanswered questions - open Run Room to answer'),
    ).toBeDefined()
  })


  it('keeps the board identity across an inbox refresh that changed nothing', () => {
    seed([working])
    act(() =>
      useStore.getState().setInbox(workspace.id, [approval({ run_id: working.id })]),
    )
    const { result } = renderHook(() => useBoard())
    const before = result.current

    // A refetch builds fresh approval objects; unchanged content must not
    // rebuild the derived board (and with it, the rendered tree).
    act(() =>
      useStore.getState().setInbox(workspace.id, [approval({ run_id: working.id })]),
    )
    expect(result.current).toBe(before)
  })

  it('opens the launch form from the board header', () => {
    seed([working])
    render(<Board />)

    fireEvent.click(screen.getByRole('button', { name: 'New run' }))

    // The form is hosted app-wide; the board only asks for it.
    expect(useStore.getState().paletteDialog).toBe('launch')
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
    for (const bucket of ['Needs you', 'Working', 'Done']) {
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
    expect(column('Needs you').getByText('Nothing here.')).toBeDefined()
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

  it('renders what another feature registered into a card slot', () => {
    registerSlot('card:chips', 'test-chip', ({ run: r }) => <span>chip:{r.id}</span>)
    seed([working])
    render(<Board />)
    expect(screen.getByText(`chip:${working.id}`)).toBeDefined()
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

describe('Clear done', () => {
  it('archives what a collaborator may act on, skipping a completed run and a protected run owned by someone else', async () => {
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
    render(<Board />)

    fireEvent.click(column('Done').getByRole('button', { name: 'Clear done' }))
    const dialog = within(await screen.findByRole('dialog'))

    expect(dialog.getByText('Archive 1 finished run?')).toBeDefined()
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

    fireEvent.click(column('Done').getByRole('button', { name: 'Clear done' }))
    const dialog = within(await screen.findByRole('dialog'))

    expect(dialog.getByText('Archive 2 finished runs?')).toBeDefined()
    expect(
      dialog.getByText(
        '1 run stays: completed but not yet closed - close them as merged or abandoned first.',
      ),
    ).toBeDefined()
    expect(dialog.queryByText(/you may not act on/)).toBeNull()

    fireEvent.click(dialog.getByRole('button', { name: 'Archive 2' }))

    // someoneElsesProtected finished later, so it archives first: eligible
    // runs go newest first, same as the Done column itself.
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith('Archived 2 runs'))
    expect(api.runArchive).toHaveBeenCalledTimes(2)
    expect(api.runArchive).toHaveBeenNthCalledWith(1, someoneElsesProtected.id, true)
    expect(api.runArchive).toHaveBeenNthCalledWith(2, eligible.id, true)
    expect(api.runArchive).not.toHaveBeenCalledWith(stillOpen.id, true)
  })

  it('archives eligible runs one at a time, newest first, never in parallel', async () => {
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

    fireEvent.click(column('Done').getByRole('button', { name: 'Clear done' }))
    fireEvent.click(
      within(await screen.findByRole('dialog')).getByRole('button', { name: 'Archive 2' }),
    )

    await waitFor(() => expect(calls).toEqual([first.id]))
    expect(calls).not.toContain(second.id)

    resolveFirst?.()
    await waitFor(() => expect(calls).toEqual([first.id, second.id]))
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

    fireEvent.click(column('Done').getByRole('button', { name: 'Clear done' }))
    fireEvent.click(
      within(await screen.findByRole('dialog')).getByRole('button', { name: 'Archive 2' }),
    )

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith('Archived 1, 1 failed: workspace is locked'),
    )
    expect(api.runArchive).toHaveBeenCalledTimes(2)
    expect(toast.success).not.toHaveBeenCalled()
  })

  it('treats a not-found failure as success and removes the run locally', async () => {
    const gone = run({ id: 'run_gone', status: 'merged', finished_at: '2026-08-14T10:10:00Z' })
    seedAs(alice, [gone])
    render(<Board />)

    vi.mocked(api.runArchive).mockRejectedValue(
      new ApiError(404, 'run.archive: not found', -32000),
    )

    fireEvent.click(column('Done').getByRole('button', { name: 'Clear done' }))
    fireEvent.click(
      within(await screen.findByRole('dialog')).getByRole('button', { name: 'Archive 1' }),
    )

    await waitFor(() => expect(toast.success).toHaveBeenCalledWith('Archived 1 run'))
    expect(useStore.getState().runs[gone.id]).toBeUndefined()
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

    fireEvent.click(column('Done').getByRole('button', { name: 'Clear done' }))
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

    fireEvent.click(column('Done').getByRole('button', { name: 'Clear done' }))
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

    fireEvent.click(column('Done').getByRole('button', { name: 'Clear done' }))
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
    expect(dialog.getByText('Archive 2 finished runs?')).toBeDefined()
    expect(dialog.getByRole('button', { name: 'Archive 2' })).toBeDefined()
  })

  it('renders no Clear done button when nothing in Done is eligible', () => {
    const stillOpen = run({ id: 'run_none_eligible', status: 'completed' })
    seedAs(alice, [stillOpen])
    render(<Board />)

    expect(screen.queryByRole('button', { name: 'Clear done' })).toBeNull()
  })

  it('hides Clear done while the Done column is showing its archived runs', () => {
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

    expect(column('Done').getByRole('button', { name: 'Clear done' })).toBeDefined()

    fireEvent.click(screen.getByRole('button', { name: 'Archived 1' }))

    expect(column('Done').queryByRole('button', { name: 'Clear done' })).toBeNull()
  })
})
