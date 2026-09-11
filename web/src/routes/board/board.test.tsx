import { act, fireEvent, render, renderHook, screen, within } from '@testing-library/react'
import { registerSlot } from '@/components/slots'
import type { Run } from '@/lib/types'
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
  workspace,
} from '@/test/fixtures'
import { hintOn } from '@/test/tooltip'
import { atViewport } from '@/test/viewport'

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

// A stub left standing by a failing assertion would gut navigator for every
// test after it, turning one real failure into a file full of them.
afterEach(() => vi.unstubAllGlobals())

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
})

// The member environment is a desktop setup surface - forward a port, save
// it, reset it - and its expanded xterm would take the board's whole screen.
test('the environment dock is on the board for a mouse and gone on a phone', () => {
  const caps = {
    gateway: 'local' as const,
    methods: ['*'],
    ws: ['events', 'attach', 'terminal'],
  }
  seed([working])
  useStore.setState({ capabilities: caps })
  const { unmount } = render(<Board />)
  expect(screen.getByLabelText('Expand terminal dock')).toBeTruthy()
  unmount()

  atViewport(390, { pointer: 'coarse' })
  seed([working])
  useStore.setState({ capabilities: caps })
  render(<Board />)
  expect(screen.queryByLabelText('Expand terminal dock')).toBeNull()
})
