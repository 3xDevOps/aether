import { fireEvent, render, screen } from '@testing-library/react'
import type { Api } from '@/lib/api'
import type { Event, TimelinePage, TimelineQuery } from '@/lib/types'
import { olderFeed, openFeed } from '@/routes/team/sync'
import { TimelineFeed } from '@/routes/team/timeline'
import { useStore } from '@/store'
import { toRecord } from '@/store/runs'
import { emptyFilters } from '@/store/timeline'
import {
  alice,
  bob,
  fakeApi,
  otherWorkspace,
  run,
  workspace,
} from '@/test/fixtures'

// Well past the 500-seq window, so the arithmetic is visible: an
// implementation that ignored the probe and started at zero would fail.
const head = 4200
const window = 500

const history: Event[] = [
  {
    id: 'evt_1',
    seq: 4198,
    time: '2026-08-14T10:03:00Z',
    workspace_id: workspace.id,
    run_id: 'run_1',
    actor_id: alice.id,
    type: 'workspace.timeline',
    payload: { kind: 'pause' },
  },
  {
    id: 'evt_2',
    seq: 4199,
    time: '2026-08-14T10:04:00Z',
    workspace_id: workspace.id,
    run_id: 'run_1',
    actor_id: bob.id,
    type: 'run.status',
    payload: { to: 'needs-attention', reason: 'waiting on a question' },
  },
]

// The stretch below the first window: what "load older" reads.
const olderHistory: Event[] = [
  {
    id: 'evt_0',
    seq: 3400,
    time: '2026-08-14T09:00:00Z',
    workspace_id: workspace.id,
    run_id: 'run_1',
    actor_id: alice.id,
    type: 'workspace.timeline',
    payload: { kind: 'resume' },
  },
]

// The reader pages forward only, so the view first asks for a page past the
// end: that answer carries the log head the window opens back from.
function feedApi(events = history) {
  return fakeApi({
    workspaceTimeline: vi.fn(async (q: TimelineQuery) => {
      const after = q.after_seq ?? 0
      if (after >= Number.MAX_SAFE_INTEGER)
        return { events: [], next_seq: head, more: false }
      if (after < head - window)
        return { events: olderHistory, next_seq: head - window, more: false }
      return { events, next_seq: head, more: false }
    }),
  })
}

function seed() {
  useStore.setState({
    workspaces: { [workspace.id]: workspace },
    activeWorkspace: workspace.id,
    members: { [alice.id]: alice, [bob.id]: bob },
    runs: { run_1: toRecord(run()) },
    feed: [],
    feedFilters: emptyFilters,
    feedFloor: 0,
    feedCursor: 0,
    feedOlder: false,
    feedRequest: 0,
    feedLoading: false,
    feedError: null,
    feedTruncated: false,
    lastSeq: 0,
    route: { name: 'timeline', params: {} },
  })
}

/** The after_seq of every page request, ignoring the head probe. */
function windowsAsked(client: Api): number[] {
  return vi
    .mocked(client.workspaceTimeline)
    .mock.calls.map(([q]) => q.after_seq ?? 0)
    .filter((seq) => seq < Number.MAX_SAFE_INTEGER)
}

describe('workspace activity feed', () => {
  it('opens a window at the end of the log and lists it newest first', async () => {
    const client = feedApi()
    seed()
    render(<TimelineFeed params={{}} client={client} />)

    expect(await screen.findByText(/waiting on a question/)).toBeDefined()
    const rows = screen.getAllByRole('listitem')
    expect(rows[0].textContent).toContain('needs-attention')
    expect(rows[1].textContent).toContain('pause')
    // Bob acted last, so his colour is the dot on the newest entry.
    expect(rows[0].querySelector('span')?.getAttribute('style')).toContain(
      'background-color',
    )
    expect(windowsAsked(client)).toEqual([head - window])
  })

  // A server update is an admin act like any other, so it lands in the
  // same feed: without its own case the row would render a bare type and
  // say nothing about which phase it reached.
  it('describes a server update phase', async () => {
    const client = feedApi([
      {
        id: 'evt_srv',
        seq: 4199,
        time: '2026-08-14T10:04:00Z',
        workspace_id: workspace.id,
        run_id: '',
        actor_id: alice.id,
        type: 'server.update',
        payload: { phase: 'failed', version: 'v1.3.0', detail: 'checksum mismatch' },
      },
    ])
    seed()
    render(<TimelineFeed params={{}} client={client} />)

    expect(
      await screen.findByText('failed - v1.3.0 - checksum mismatch'),
    ).toBeDefined()
  })

  // The sidebar names the scope everywhere else; here it is only the
  // default, because comparing workspaces is what an activity log is for.
  it('opens on the active workspace', async () => {
    const client = feedApi()
    seed()
    useStore.setState({
      workspaces: {
        [workspace.id]: workspace,
        [otherWorkspace.id]: otherWorkspace,
      },
      activeWorkspace: otherWorkspace.id,
    })
    render(<TimelineFeed params={{}} client={client} />)

    await vi.waitFor(() =>
      expect(useStore.getState().feedFilters.workspaceID).toBe(otherWorkspace.id),
    )
  })

  it('walks the window back without re-reading what it already has', async () => {
    const client = feedApi()
    seed()
    render(<TimelineFeed params={{}} client={client} />)
    await screen.findByText(/waiting on a question/)

    fireEvent.click(screen.getByRole('button', { name: 'Load older' }))

    // The second window starts another 500 back and stops where the first
    // one began, so the newest end is never re-read and never dropped.
    await vi.waitFor(() =>
      expect(windowsAsked(client)).toEqual([head - window, head - 2 * window]),
    )
    // The older stretch lands before what was already loaded, oldest first,
    // so the view still renders newest first.
    await vi.waitFor(() =>
      expect(useStore.getState().feed.map((e) => e.seq)).toEqual([3400, 4198, 4199]),
    )
    const rows = screen.getAllByRole('listitem')
    expect(rows[0].textContent).toContain('needs-attention')
    expect(rows[2].textContent).toContain('resume')
  })

  it('clears the run filter when the workspace changes', async () => {
    const client = feedApi()
    seed()
    useStore.setState({
      workspaces: {
        [workspace.id]: workspace,
        [otherWorkspace.id]: otherWorkspace,
      },
    })
    render(<TimelineFeed params={{}} client={client} />)
    await screen.findByText(/waiting on a question/)

    fireEvent.change(screen.getByLabelText('Run'), { target: { value: 'run_1' } })
    await vi.waitFor(() =>
      expect(useStore.getState().feedFilters.runID).toBe('run_1'),
    )

    // A run belongs to one workspace: keeping the filter would query the new
    // workspace for a run it does not have.
    fireEvent.change(screen.getByLabelText('Workspace'), {
      target: { value: otherWorkspace.id },
    })

    await vi.waitFor(() => {
      const f = useStore.getState().feedFilters
      expect(f.workspaceID).toBe(otherWorkspace.id)
      expect(f.runID).toBe('')
    })
  })

  it('narrows the query when a filter changes', async () => {
    const client = feedApi()
    seed()
    render(<TimelineFeed params={{}} client={client} />)
    await screen.findByText(/waiting on a question/)

    fireEvent.change(screen.getByLabelText('Type'), {
      target: { value: 'run.status' },
    })

    await vi.waitFor(() =>
      expect(client.workspaceTimeline).toHaveBeenCalledWith(
        expect.objectContaining({ types: ['run.status'] }),
      ),
    )
  })

  it('puts the floor back when a load-older read fails, so a retry fills the gap', async () => {
    const client = feedApi()
    seed()
    useStore.setState({
      feedFilters: { ...emptyFilters, workspaceID: workspace.id },
    })
    await openFeed(useStore, client)
    expect(useStore.getState().feedFloor).toBe(head - window)

    const failing = fakeApi({
      workspaceTimeline: vi.fn(async () => {
        throw new Error('502 Bad Gateway')
      }),
    })
    await olderFeed(useStore, failing)

    // The stretch never loaded, so the floor must not move past it: leaving
    // it advanced would make the next click skip the gap forever.
    expect(useStore.getState().feedError).toContain('502')
    expect(useStore.getState().feedFloor).toBe(head - window)

    await olderFeed(useStore, client)
    expect(useStore.getState().feedFloor).toBe(head - 2 * window)
    expect(useStore.getState().feed.map((e) => e.seq)).toContain(3400)
  })

  it('abandons a read the view has already moved on from', async () => {
    let release = (_: TimelinePage) => {}
    const inFlight = new Promise<TimelinePage>((resolve) => {
      release = resolve
    })
    const client = fakeApi({
      workspaceTimeline: vi.fn(async (q: TimelineQuery) =>
        (q.after_seq ?? 0) >= Number.MAX_SAFE_INTEGER
          ? { events: [], next_seq: head, more: false }
          : inFlight,
      ),
    })
    seed()
    useStore.setState({
      feedFilters: { ...emptyFilters, workspaceID: workspace.id },
    })

    const reading = openFeed(useStore, client)
    await vi.waitFor(() =>
      expect(useStore.getState().feedFloor).toBe(head - window),
    )

    // The user changes a filter while that page is still in flight. When it
    // lands it belongs to a query nobody is looking at any more, so it must
    // write nothing - not the events, and not the loading flag the new read
    // now owns.
    useStore.getState().beginFeed()
    release({ events: history, next_seq: head, more: false })
    await reading

    expect(useStore.getState().feed).toHaveLength(0)
    expect(useStore.getState().feedLoading).toBe(true)
  })

  it('describes run title events with their title', async () => {
    const client = feedApi([
      {
        id: 'evt_title',
        seq: 4199,
        time: '2026-08-14T10:04:00Z',
        workspace_id: workspace.id,
        run_id: 'run_1',
        actor_id: bob.id,
        type: 'run.title',
        payload: { title: 'Session title' },
      },
    ])
    seed()
    render(<TimelineFeed params={{}} client={client} />)

    expect(await screen.findByText('Session title')).toBeDefined()
  })

})
