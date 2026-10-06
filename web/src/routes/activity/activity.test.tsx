import { act, fireEvent, render, screen, within } from '@testing-library/react'
import type { Api } from '@/lib/api'
import type { Event, RunMessage, TimelinePage, TimelineQuery } from '@/lib/types'
import { ActivityRoute } from '@/routes/activity'
import { olderFeed, openFeed } from '@/routes/team/sync'
import { useStore } from '@/store'
import { toRecord } from '@/store/runs'
import { emptyFilters } from '@/store/timeline'
import { alice, bob, fakeApi, otherWorkspace, run, workspace } from '@/test/fixtures'
import { pickOption } from '@/test/select'
import { sizedViewport } from '@/test/virtual'

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
      if (after >= Number.MAX_SAFE_INTEGER) return { events: [], next_seq: head, more: false }
      if (after < head - window) return { events: olderHistory, next_seq: head - window, more: false }
      return { events, next_seq: head, more: false }
    }),
  })
}

function seed() {
  useStore.setState({
    workspaces: { [workspace.id]: workspace },
    activeWorkspace: workspace.id,
    members: { [alice.id]: alice, [bob.id]: bob },
    runs: { run_1: toRecord(run()), run_2: toRecord(run({ id: 'run_2', task: 'document the checkout API' })) },
    runMessages: {},
    messageLists: {},
    messageErrors: {},
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

function message(over: Partial<RunMessage>): RunMessage {
  return {
    id: 'msg_1',
    workspace_id: workspace.id,
    from_run_id: 'run_1',
    to_run_id: 'run_2',
    kind: 'message',
    body: 'Start with the cart totals.',
    created_at: '2026-08-14T10:05:00Z',
    ...over,
  }
}

const mail: RunMessage[] = [
  message({ id: 'msg_3', kind: 'reply', correlation_id: 'msg_2', body: 'Integer cents.', created_at: '2026-08-14T10:07:00Z', acked_at: '2026-08-14T10:08:00Z' }),
  message({ id: 'msg_2', kind: 'question', from_run_id: 'run_2', to_run_id: 'run_1', body: 'Cents or decimals?', created_at: '2026-08-14T10:06:00Z', delivered_at: '2026-08-14T10:06:01Z' }),
  message({ id: 'msg_1' }),
]

async function openFilter() {
  fireEvent.click(await screen.findByRole('button', { name: /^Filter/ }))
  return screen.findByRole('dialog')
}

async function showMessages() {
  await openFilter()
  await pickOption(screen.getByLabelText('Show'), 'Agent messages')
}

beforeEach(() => sizedViewport())

describe('activity feed', () => {
  it('opens a window at the end of the log and lists it newest first', async () => {
    const client = feedApi()
    seed()
    render(<ActivityRoute params={{}} client={client} />)

    expect(await screen.findByText(/waiting on a question/)).toBeDefined()
    const rows = screen.getAllByRole('listitem')
    expect(rows[0].textContent).toContain('Needs you: waiting on a question')
    expect(rows[1].textContent).toContain('pause')
    expect(within(rows[0]).getByRole('img', { name: bob.display_name }).getAttribute('style')).toContain('border-color')
    expect(windowsAsked(client)).toEqual([head - window])
  })

  it('marks a state change with a dot and leaves other rows without one', async () => {
    seed()
    render(<ActivityRoute params={{}} client={feedApi()} />)
    await screen.findByText(/waiting on a question/)
    const [status, pause] = screen.getAllByRole('listitem')
    expect(status.querySelector('[data-slot="status-dot"]')?.getAttribute('data-tone')).toBe('needs-you')
    expect(pause.querySelector('[data-slot="status-dot"]')).toBeNull()
  })

  it('opens the run a row names on its terminal tab', async () => {
    seed()
    render(<ActivityRoute params={{}} client={feedApi()} />)
    const row = (await screen.findByText(/waiting on a question/)).closest('li') as HTMLElement
    fireEvent.click(within(row).getByRole('button', { name: 'rewrite the checkout flow' }))
    expect(useStore.getState().route).toEqual({ name: 'run', params: { runId: 'run_1' } })
  })

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
    render(<ActivityRoute params={{}} client={client} />)
    expect(await screen.findByText('Server update to v1.3.0 failed: checksum mismatch')).toBeDefined()
  })

  it('names both runs of an agent message and gives a delivery word only once it is known', async () => {
    const client = feedApi([
      {
        id: 'evt_msg',
        seq: 4199,
        time: '2026-08-14T10:06:00Z',
        workspace_id: workspace.id,
        run_id: 'run_2',
        actor_id: '',
        type: 'coord.message',
        payload: { message_id: 'msg_2', workspace_id: workspace.id, from_run_id: 'run_2', to_run_id: 'run_1', kind: 'question' },
      },
    ])
    seed()
    render(<ActivityRoute params={{}} client={client} />)
    const row = (await screen.findByRole('img', { name: 'Question' })).closest('li') as HTMLElement
    expect(within(row).getByRole('button', { name: 'document the checkout API' })).toBeDefined()
    expect(within(row).getByRole('button', { name: 'rewrite the checkout flow' })).toBeDefined()
    expect(row.textContent).not.toMatch(/Sent|Delivered|Acknowledged/)
    expect(row.textContent).not.toContain('message_id')

    act(() => useStore.setState({ runMessages: { msg_2: message({ id: 'msg_2', kind: 'question', acked_at: '2026-08-14T10:08:00Z' }) } }))
    expect(row.textContent).toContain('Acknowledged')
  })

  it('folds taking and releasing control into one visit row per open', async () => {
    const control = (seq: number, member: string): Event => ({
      id: `evt_c${seq}`, seq, time: `2026-08-14T10:0${seq - 4190}:00Z`, workspace_id: workspace.id, run_id: 'run_1', actor_id: '', type: 'run.controller', payload: { member_id: member },
    })
    seed()
    render(<ActivityRoute params={{}} client={feedApi([control(4191, alice.id), control(4192, ''), control(4193, bob.id), control(4194, bob.id)])} />)
    expect(await screen.findByText(/Bob is viewing the run/)).toBeDefined()
    const rows = within(screen.getByRole('region', { name: 'Activity feed' })).getAllByRole('listitem')
    expect(rows.map((row) => row.textContent?.replace(/^.*ago/, ''))).toEqual([
      expect.stringContaining('Bob is viewing the run'),
      expect.stringContaining('Alice viewed the run'),
    ])
    expect(screen.getByText('2 entries')).toBeDefined()
  })

  it('shows each event type and payload as sent under Raw events', async () => {
    seed()
    render(<ActivityRoute params={{}} client={feedApi()} />)
    await screen.findByText(/waiting on a question/)
    const more = screen.getByRole('button', { name: 'More activity options' })
    fireEvent.pointerDown(more, { button: 0, ctrlKey: false, pointerType: 'mouse' })
    fireEvent.click(await screen.findByRole('menuitemcheckbox', { name: 'Raw events' }))
    expect(await screen.findByText('run.status')).toBeDefined()
    expect(screen.getByText('{"to":"needs-attention","reason":"waiting on a question"}')).toBeDefined()
  })

  it('mounts only the rows near the viewport of a long log', async () => {
    const long: Event[] = Array.from({ length: 2000 }, (_, i) => ({
      id: `evt_${i}`,
      seq: head - 2000 + i,
      time: '2026-08-14T10:04:00Z',
      workspace_id: workspace.id,
      run_id: 'run_1',
      actor_id: alice.id,
      type: 'workspace.timeline',
      payload: { kind: 'note', message: `entry ${i}` },
    }))
    seed()
    render(<ActivityRoute params={{}} client={feedApi(long)} />)
    await screen.findByText(/Entry 1999$/)
    const rows = screen.getAllByRole('listitem')
    expect(rows.length).toBeGreaterThan(5)
    expect(rows.length).toBeLessThan(100)
    expect(rows[0].getAttribute('aria-setsize')).toBe('2000')
    expect(rows[0].getAttribute('aria-posinset')).toBe('1')
  })

  it('opens on the active workspace', async () => {
    seed()
    useStore.setState({ workspaces: { [workspace.id]: workspace, [otherWorkspace.id]: otherWorkspace }, activeWorkspace: otherWorkspace.id })
    render(<ActivityRoute params={{}} client={feedApi()} />)
    await vi.waitFor(() => expect(useStore.getState().feedFilters.workspaceID).toBe(otherWorkspace.id))
  })

  it('walks the window back without re-reading what it already has', async () => {
    const client = feedApi()
    seed()
    render(<ActivityRoute params={{}} client={client} />)
    await screen.findByText(/waiting on a question/)

    fireEvent.click(screen.getByRole('button', { name: 'Load older' }))

    // The second window starts another 500 back and stops where the first
    // one began, so the newest end is never re-read and never dropped.
    await vi.waitFor(() => expect(windowsAsked(client)).toEqual([head - window, head - 2 * window]))
    await vi.waitFor(() => expect(screen.getAllByRole('listitem')).toHaveLength(3))
    const rows = screen.getAllByRole('listitem')
    expect(rows[0].textContent).toContain('Needs you: waiting on a question')
    expect(rows[2].textContent).toContain('resume')
  })
})

describe('activity filter', () => {
  it('narrows the query to one event type', async () => {
    const client = feedApi()
    seed()
    render(<ActivityRoute params={{}} client={client} />)
    await screen.findByText(/waiting on a question/)
    await openFilter()
    await pickOption(screen.getByLabelText('Show'), 'Run title')
    await vi.waitFor(() => expect(client.workspaceTimeline).toHaveBeenCalledWith(expect.objectContaining({ types: ['run.title'] })))
    expect(screen.getByRole('button', { name: 'Filter · 1' })).toBeDefined()
  })

  it('narrows the query to one run and one member', async () => {
    const client = feedApi()
    seed()
    render(<ActivityRoute params={{}} client={client} />)
    await screen.findByText(/waiting on a question/)
    await openFilter()
    await pickOption(screen.getByLabelText('Run'), 'rewrite the checkout flow')
    await vi.waitFor(() => expect(document.activeElement).toBe(screen.getByLabelText('Run')))
    await pickOption(screen.getByLabelText('Member'), bob.display_name)
    await vi.waitFor(() =>
      expect(client.workspaceTimeline).toHaveBeenCalledWith(expect.objectContaining({ run_id: 'run_1', member_id: bob.id })),
    )
  })

  it('clears the run filter when the workspace changes', async () => {
    seed()
    useStore.setState({ workspaces: { [workspace.id]: workspace, [otherWorkspace.id]: otherWorkspace } })
    render(<ActivityRoute params={{}} client={feedApi()} />)
    await screen.findByText(/waiting on a question/)
    await openFilter()
    await pickOption(screen.getByLabelText('Run'), 'rewrite the checkout flow')
    await vi.waitFor(() => expect(useStore.getState().feedFilters.runID).toBe('run_1'))
    await vi.waitFor(() => expect(document.activeElement).toBe(screen.getByLabelText('Run')))
    await pickOption(screen.getByLabelText('Workspace'), otherWorkspace.name)
    await vi.waitFor(() => {
      const f = useStore.getState().feedFilters
      expect(f.workspaceID).toBe(otherWorkspace.id)
      expect(f.runID).toBe('')
    })
  })

  it('offers no workspace choice when there is only one', async () => {
    seed()
    render(<ActivityRoute params={{}} client={feedApi()} />)
    await openFilter()
    expect(screen.queryByLabelText('Workspace')).toBeNull()
    expect(screen.getByLabelText('Show')).toBeDefined()
  })
})

describe('agent message history', () => {
  function mailApi(pages: { messages: RunMessage[]; next_before?: string }[] = [{ messages: mail }]) {
    let page = 0
    return fakeApi({
      ...feedApi(),
      coordMessagesList: vi.fn(async (params: { before?: string }) => {
        page = params.before ? page + 1 : 0
        return pages[page]
      }),
    })
  }

  it('lists the workspace history newest first with sender, recipient and delivery', async () => {
    const client = mailApi()
    seed()
    render(<ActivityRoute params={{}} client={client} />)
    await showMessages()

    await vi.waitFor(() => expect(screen.getAllByRole('article')).toHaveLength(3))
    expect(client.coordMessagesList).toHaveBeenCalledWith(expect.objectContaining({ workspace_id: workspace.id, run_id: undefined }))
    const [reply, question, first] = screen.getAllByRole('article')
    expect(reply.textContent).toContain('Integer cents.')
    expect(reply.textContent).toContain('Acknowledged')
    expect(question.textContent).toContain('Delivered')
    expect(first.textContent).toContain('Sent')
    expect(within(question).getAllByRole('button').map((b) => b.textContent)).toEqual([
      'document the checkout API',
      'rewrite the checkout flow',
      'Thread',
    ])
  })

  it('searches the loaded bodies', async () => {
    seed()
    render(<ActivityRoute params={{}} client={mailApi()} />)
    await showMessages()
    await vi.waitFor(() => expect(screen.getAllByRole('article')).toHaveLength(3))
    fireEvent.change(screen.getByRole('searchbox', { name: 'Search agent messages' }), { target: { value: 'cents' } })
    await vi.waitFor(() => expect(screen.getAllByRole('article')).toHaveLength(2))
    expect(screen.getByText('2 of 3')).toBeDefined()
  })

  it('narrows to one thread from a row', async () => {
    seed()
    render(<ActivityRoute params={{}} client={mailApi()} />)
    await showMessages()
    await vi.waitFor(() => expect(screen.getAllByRole('article')).toHaveLength(3))
    fireEvent.click(within(screen.getAllByRole('article')[1]).getByRole('button', { name: 'Thread' }))
    await vi.waitFor(() => expect(screen.getAllByRole('article')).toHaveLength(2))
    expect(screen.getByRole('button', { name: 'Filter · 2' })).toBeDefined()
  })

  it('asks the server for one run and keeps only what it sent', async () => {
    const client = mailApi()
    seed()
    render(<ActivityRoute params={{}} client={client} />)
    await showMessages()
    await vi.waitFor(() => expect(document.activeElement).toBe(screen.getByLabelText('Show')))
    await pickOption(screen.getByLabelText('Sender'), 'document the checkout API')
    await vi.waitFor(() => expect(client.coordMessagesList).toHaveBeenCalledWith(expect.objectContaining({ run_id: 'run_2' })))
    await vi.waitFor(() => expect(screen.getAllByRole('article')).toHaveLength(1))
    expect(screen.getByRole('article').textContent).toContain('Cents or decimals?')
  })

  it('reads every older page on Show all', async () => {
    const client = mailApi([
      { messages: [mail[0]], next_before: 'c1' },
      { messages: [mail[1]], next_before: 'c2' },
      { messages: [mail[2]] },
    ])
    seed()
    render(<ActivityRoute params={{}} client={client} />)
    await showMessages()
    await vi.waitFor(() => expect(screen.getAllByRole('article')).toHaveLength(1))
    fireEvent.click(screen.getByRole('button', { name: 'Show all' }))
    await vi.waitFor(() => expect(screen.getAllByRole('article')).toHaveLength(3))
    expect(vi.mocked(client.coordMessagesList).mock.calls.map(([p]) => p.before)).toEqual([undefined, 'c1', 'c2'])
    expect(screen.queryByRole('button', { name: 'Show all' })).toBeNull()
  })

  it('shows the real error when the history cannot be read', async () => {
    seed()
    const client = fakeApi({
      ...feedApi(),
      coordMessagesList: vi.fn(async () => {
        throw new Error('coord.messages.list: method not found')
      }),
    })
    render(<ActivityRoute params={{}} client={client} />)
    await showMessages()
    expect((await screen.findByRole('alert')).textContent).toContain('method not found')
  })
})

describe('activity reader', () => {
  it('puts the floor back when a load-older read fails, so a retry fills the gap', async () => {
    const client = feedApi()
    seed()
    useStore.setState({ feedFilters: { ...emptyFilters, workspaceID: workspace.id } })
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

  it('reads again on the next event after a failed read, without a reconnect', async () => {
    let fail = true
    const client = fakeApi({
      workspaceTimeline: vi.fn(async (q: TimelineQuery) => {
        if (fail) throw new Error('502 Bad Gateway')
        const after = q.after_seq ?? 0
        if (after >= Number.MAX_SAFE_INTEGER) return { events: [], next_seq: head, more: false }
        return { events: history, next_seq: head, more: false }
      }),
    })
    seed()
    render(<ActivityRoute params={{}} client={client} />)
    expect((await screen.findByRole('alert')).textContent).toContain('502')

    fail = false
    useStore.setState({ lastSeq: 1 })

    expect(await screen.findByText(/waiting on a question/)).toBeDefined()
    expect(screen.queryByRole('alert')).toBeNull()
    expect(windowsAsked(client)).toEqual([head - window])
  })

  it('reads on from the cursor when a later read stopped short', async () => {
    const client = feedApi()
    seed()
    render(<ActivityRoute params={{}} client={client} />)
    expect(await screen.findByText(/waiting on a question/)).toBeDefined()
    act(() => useStore.setState({ feedTruncated: true }))

    act(() => useStore.setState({ lastSeq: 1 }))

    await vi.waitFor(() => expect(windowsAsked(client)).toEqual([head - window, head]))
    await vi.waitFor(() => expect(useStore.getState().feedTruncated).toBe(false))
  })

  it('abandons a read the view has already moved on from', async () => {
    let release = (_: TimelinePage) => {}
    const inFlight = new Promise<TimelinePage>((resolve) => {
      release = resolve
    })
    const client = fakeApi({
      workspaceTimeline: vi.fn(async (q: TimelineQuery) =>
        (q.after_seq ?? 0) >= Number.MAX_SAFE_INTEGER ? { events: [], next_seq: head, more: false } : inFlight,
      ),
    })
    seed()
    useStore.setState({ feedFilters: { ...emptyFilters, workspaceID: workspace.id } })

    const reading = openFeed(useStore, client)
    await vi.waitFor(() => expect(useStore.getState().feedFloor).toBe(head - window))

    // A filter change makes the in-flight page stale: it must write nothing,
    // not even the loading flag the new read owns.
    useStore.getState().beginFeed()
    release({ events: history, next_seq: head, more: false })
    await reading

    expect(useStore.getState().feed).toHaveLength(0)
    expect(useStore.getState().feedLoading).toBe(true)
  })
})
