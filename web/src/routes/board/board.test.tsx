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
  status: 'merged',
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

describe('run board', () => {
  it('deals runs into the three buckets, newest change first', () => {
    seed([stalled, working, queued, merged])
    render(<Board />)

    expect(column('Needs You').getByText('waiting on a question')).toBeDefined()
    expect(column('Done').getByText('landed already')).toBeDefined()

    // queued (11:00) changed after running (10:02), so it sorts above it.
    const tasks = column('Working')
      .getAllByRole('article')
      .map((card) => within(card).getByRole('button').getAttribute('aria-label'))
    expect(tasks).toEqual(['not started', 'still going'])
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
      name: 'run',
      params: { runId: stalled.id },
    })
  })

  it('marks every run seen at once', () => {
    seed([stalled, working])
    render(<Board />)

    expect(screen.getAllByLabelText('Unseen')).toHaveLength(2)
    fireEvent.click(screen.getByTitle('Mark every run seen'))
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
    expect(column('Needs You').getByText('plan approval')).toBeDefined()
  })

  it('deals a running run with a pending approval into Needs You', () => {
    seed([working])
    render(<Board />)
    expect(column('Working').getByText('still going')).toBeDefined()

    // The run still reads `running`; only the inbox says a human is needed.
    act(() =>
      useStore
        .getState()
        .setInbox(workspace.id, [approval({ run_id: working.id })]),
    )

    expect(column('Needs You').getByText('still going')).toBeDefined()
    expect(useStore.getState().runs[working.id].status).toBe('running')
    // No run.status event fired, so the run has no reason; the card's
    // summary is the pending question itself.
    expect(column('Needs You').getByText('write src/checkout.ts')).toBeDefined()

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

    fireEvent.click(screen.getByTitle('Launch a run'))

    // The form is hosted app-wide; the board only asks for it.
    expect(useStore.getState().paletteDialog).toBe('launch')
  })

  it('offers the way in from an empty workspace, keeping the buckets', () => {
    seed([])
    render(<Board />)

    // The columns stay: an empty board is exactly when a new member is
    // learning that the three buckets exist. The notice above them says what
    // a run is and offers the one thing left to do.
    expect(screen.getByRole('region', { name: 'Working' })).toBeDefined()
    const notice = screen.getByText(/No runs yet/).closest('div') as HTMLElement
    expect(within(notice).getByTitle('Launch a run')).toBeDefined()
  })

  it('renders what another feature registered into a card slot', () => {
    registerSlot('card:chips', 'test-chip', ({ run: r }) => <span>chip:{r.id}</span>)
    seed([working])
    render(<Board />)
    expect(screen.getByText(`chip:${working.id}`)).toBeDefined()
  })
})
