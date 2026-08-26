import { render, screen } from '@testing-library/react'
import type { Event, TimelineQuery } from '@/lib/types'
import { RunEvents } from '@/routes/terminal/events'
import { useStore } from '@/store'
import { toRecord } from '@/store/runs'
import { emptyFilters } from '@/store/timeline'
import { alice, fakeApi, run, workspace } from '@/test/fixtures'

const head = 12

const history: Event[] = [
  {
    id: 'evt_1',
    seq: 10,
    time: '2026-08-14T10:03:00Z',
    workspace_id: workspace.id,
    run_id: 'run_1',
    actor_id: '',
    type: 'run.agent',
    payload: { kind: 'tool_call', tool: 'Bash', detail: 'go test ./...' },
  },
  {
    id: 'evt_2',
    seq: 11,
    time: '2026-08-14T10:04:00Z',
    workspace_id: workspace.id,
    run_id: 'run_1',
    actor_id: alice.id,
    type: 'run.status',
    payload: { to: 'needs-attention', reason: 'waiting on a question' },
  },
]

function eventsApi() {
  return fakeApi({
    workspaceTimeline: vi.fn(async (q: TimelineQuery) => {
      const after = q.after_seq ?? 0
      if (after >= Number.MAX_SAFE_INTEGER)
        return { events: [], next_seq: head, more: false }
      return { events: history, next_seq: head, more: false }
    }),
  })
}

function seed() {
  useStore.setState({
    workspaces: { [workspace.id]: workspace },
    activeWorkspace: workspace.id,
    members: { [alice.id]: alice },
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
    route: { name: 'events', params: { runId: 'run_1' } },
  })
}

describe('run events tab', () => {
  it('pins the feed to the run and lists its events newest first', async () => {
    const client = eventsApi()
    seed()
    render(<RunEvents params={{ runId: 'run_1' }} client={client} />)

    expect(await screen.findByText(/waiting on a question/)).toBeDefined()
    const rows = screen.getAllByRole('listitem')
    expect(rows[0].textContent).toContain('run.status')
    expect(rows[1].textContent).toContain('go test ./...')

    // Every page read is scoped to this run.
    for (const [q] of vi.mocked(client.workspaceTimeline).mock.calls) {
      expect(q.workspace_id).toBe(workspace.id)
      expect(q.run_id).toBe('run_1')
    }
  })

  it('hands the shared filters back on unmount', async () => {
    seed()
    const chosen = { ...emptyFilters, workspaceID: 'wsp_other', type: 'run.status' }
    useStore.setState({ feedFilters: chosen })
    const view = render(<RunEvents params={{ runId: 'run_1' }} client={eventsApi()} />)

    expect(await screen.findByText(/waiting on a question/)).toBeDefined()
    expect(useStore.getState().feedFilters.runID).toBe('run_1')

    view.unmount()
    expect(useStore.getState().feedFilters).toEqual(chosen)
  })

  it('says so when the run is unknown', () => {
    seed()
    render(<RunEvents params={{ runId: 'run_missing' }} client={eventsApi()} />)
    expect(screen.getByText('Unknown run.')).toBeDefined()
  })
})
