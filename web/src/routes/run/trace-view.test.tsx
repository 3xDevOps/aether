import { act, fireEvent, render, renderHook, screen, waitFor, within } from '@testing-library/react'
import type * as apiModule from '@/lib/api'
import { api } from '@/lib/api'
import type { SessionItem } from '@/lib/session-types'
import type { Event, Run, TimelinePage, TimelineQuery } from '@/lib/types'
import { useTraceAnnouncement } from '@/routes/run/trace-announce'
import { TraceView } from '@/routes/run/trace-view'
import { useStore } from '@/store'
import type { Span, SpanKind, SpanStatus } from '@/store/trace'
import { item, resetItems, say, state, tool } from '@/test/acp-stream'
import { run } from '@/test/fixtures'
import { sizedViewport } from '@/test/virtual'

vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof apiModule>()
  const { fakeApi } = await import('@/test/fixtures')
  return { ...actual, api: fakeApi() }
})

const nav = { go: vi.fn(), reveal: vi.fn(), focusComposer: vi.fn(), focusRequest: vi.fn() }

function show(items: SessionItem[], over: Partial<Run> = {}, ack: { oldest_seq?: number; truncated_before?: boolean } = {}) {
  const runID = 'run_1'
  useStore.getState().upsertRun(run({ mode: 'acp', acp: true, created_at: '2026-10-06T10:00:00Z', started_at: '2026-10-06T10:00:00Z', ...over }))
  act(() => {
    const store = useStore.getState()
    store.acpAck(runID, { ok: true, seq: items.at(-1)?.seq ?? 0, replay: items.length, epoch: 0, live: true, has_control: false, state: state(), ...ack })
    store.acpFrames(runID, items.map((it) => ({ seq: it.seq, item: it })))
    store.acpStream(runID, 'live')
  })
  return render(<TraceView run={useStore.getState().runs[runID]!} nav={nav} />)
}

const run2 = (turn: number, id: string, status: string) => tool(turn, id, 'execute', `npm test ${id}`, status, { tool_name: 'Bash' })

function twoTurns(): SessionItem[] {
  return [
    item('turn_start', 1), say(1, 'user', 'Fix the tests'), run2(1, 'c1', 'in_progress'), run2(1, 'c1', 'failed'), run2(1, 'c2', 'completed'),
    item('turn_end', 1, { stop_reason: 'end_turn' }),
    item('turn_start', 2), say(2, 'user', 'Now the docs'), run2(2, 'c3', 'in_progress'), run2(2, 'c3', 'completed'),
    item('turn_end', 2, { stop_reason: 'end_turn' }),
  ]
}

const rowNames = () => screen.getAllByRole('treeitem').map((row) => row.getAttribute('aria-label')!.split(',')[0])

const costEvent = (seq: number, tokens: number): Event => ({
  id: `c${seq}`, seq, time: '2026-10-06T10:00:10Z', workspace_id: 'w', run_id: 'run_1', actor_id: '', type: 'run.cost',
  payload: { metered: true, increment: true, input_tokens: tokens, output_tokens: 0, cost_usd: 0.01 * tokens },
})


beforeEach(() => {
  resetItems()
  useStore.setState({ runs: {}, acpSessions: {}, traceLogs: {}, traceViews: {}, connection: 'live' })
  vi.mocked(api.workspaceTimeline).mockResolvedValue({ events: [], next_seq: 0, more: false })
})

describe('TraceView', () => {
  it('folds closed turns but the last and surfaces a failure under a folded turn', async () => {
    show(twoTurns(), { status: 'completed', finished_at: '2026-10-06T10:00:20Z' })
    expect(rowNames()).toEqual(['Session', 'Fix the tests', 'Ran npm test c1', 'Now the docs', 'Ran npm test c3'])
    const folded = screen.getByRole('treeitem', { name: /^Fix the tests/ })
    expect(folded.getAttribute('aria-expanded')).toBe('false')
    expect(within(folded).getByText('+2')).toBeDefined()
    expect(screen.getByRole('treeitem', { name: /^Ran npm test c1/ }).getAttribute('aria-label')).toBe('Ran npm test c1, Tool call, Failed, 1.0s, slowest')
    await waitFor(() => expect(api.workspaceTimeline).toHaveBeenCalledWith(expect.objectContaining({ run_id: 'run_1', types: ['run.status', 'run.cost'], newest: true })))
  })

  it('walks the tree with the arrow keys and folds with Left and Right', () => {
    show(twoTurns(), { status: 'completed', finished_at: '2026-10-06T10:00:20Z' })
    const turn = screen.getByRole('treeitem', { name: /^Fix the tests/ })
    act(() => turn.focus())
    fireEvent.keyDown(turn, { key: 'ArrowRight' })
    expect(rowNames()).toEqual(['Session', 'Fix the tests', 'Ran npm test c1', 'Ran npm test c2', 'Now the docs', 'Ran npm test c3'])
    fireEvent.keyDown(turn, { key: 'ArrowRight' })
    expect(document.activeElement?.getAttribute('aria-label')).toMatch(/^Ran npm test c1/)
    fireEvent.keyDown(document.activeElement!, { key: 'ArrowLeft' })
    expect(document.activeElement).toBe(screen.getByRole('treeitem', { name: /^Fix the tests/ }))
    fireEvent.keyDown(document.activeElement!, { key: 'ArrowLeft' })
    expect(rowNames()).toContain('Ran npm test c1')
    expect(rowNames()).not.toContain('Ran npm test c2')
    fireEvent.keyDown(document.activeElement!, { key: 'End' })
    expect(document.activeElement?.getAttribute('aria-label')).toMatch(/^Ran npm test c3/)
  })

  it('opens a span’s details on Enter, zooms on Shift+Enter, and unwinds Escape one step at a time', async () => {
    show(twoTurns(), { status: 'completed', finished_at: '2026-10-06T10:00:20Z' })
    const call = screen.getByRole('treeitem', { name: /^Ran npm test c3/ })
    act(() => call.focus())
    fireEvent.keyDown(call, { key: 'Enter' })
    const detail = await screen.findByRole('dialog', { name: 'Ran npm test c3 details' })
    expect(within(detail).getByText('Tool call · Bash')).toBeDefined()
    expect(within(detail).getByRole('button', { name: 'Copy command' })).toBeDefined()
    fireEvent.click(within(detail).getByRole('button', { name: /Zoom to this span/ }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(screen.getByRole('button', { name: 'Whole session' })).toBeDefined()
    const row = screen.getByRole('treeitem', { name: /^Ran npm test c3/ })
    const escape = fireEvent.keyDown(row, { key: 'Escape' })
    expect(escape).toBe(false)
    expect(screen.queryByRole('button', { name: 'Whole session' })).toBeNull()
    expect(fireEvent.keyDown(row, { key: 'Escape' })).toBe(true)
  })

  it('steps through failures from the Errors stat, unfolding their turn and opening the failure', async () => {
    show(twoTurns(), { status: 'completed', finished_at: '2026-10-06T10:00:20Z' })
    fireEvent.click(screen.getByRole('button', { name: '1 error, show the next failure' }))
    expect(screen.getByRole('treeitem', { name: /^Fix the tests/ }).getAttribute('aria-expanded')).toBe('true')
    expect(screen.getByRole('treeitem', { name: /^Ran npm test c1/ }).getAttribute('aria-selected')).toBe('true')
    expect(await screen.findByRole('dialog', { name: 'Ran npm test c1 details' })).toBeDefined()
  })

  it('shows tokens and cost only for a metered run, and a failed read as its error', async () => {
    const cost = { id: 'c', seq: 9, time: '2026-10-06T10:00:10Z', workspace_id: 'w', run_id: 'run_1', actor_id: '', type: 'run.cost', payload: { metered: true, increment: true, input_tokens: 1200, output_tokens: 300, cost_usd: 0.25 } }
    vi.mocked(api.workspaceTimeline).mockResolvedValueOnce({ events: [cost], next_seq: 9, more: false })
    const view = show(twoTurns())
    expect(await screen.findByText('$0.25')).toBeDefined()
    expect(screen.getByText('1.5K')).toBeDefined()
    view.unmount()
    useStore.setState({ traceLogs: {} })
    vi.mocked(api.workspaceTimeline).mockRejectedValueOnce(new Error('workspace.timeline: permission denied'))
    show(twoTurns())
    expect(await screen.findByText('Not read')).toBeDefined()
    expect(screen.queryByText('Tokens')).toBeNull()
  })

  it('marks tokens and cost as floors when the history read stops at its cap', async () => {
    let newest = 10_000
    vi.mocked(api.workspaceTimeline).mockImplementation(async () => {
      const events = Array.from({ length: 500 }, (_, i) => costEvent(newest - i, 1)).reverse()
      newest -= 500
      return { events, next_seq: events.at(-1)!.seq, more: true, older_seq: newest + 1 }
    })
    show(twoTurns())
    expect(await screen.findByText('≥$20.00')).toBeDefined()
    expect(screen.getByText('Tokens').nextElementSibling?.textContent).toBe('≥2K')
  })

  it('reads the history from the newest again when an earlier read stopped partway', async () => {
    const timeline = async (query: TimelineQuery): Promise<TimelinePage> => {
      if (query.after_seq !== undefined) return { events: [], next_seq: query.after_seq, more: false }
      return query.before_seq === undefined ? { events: [costEvent(20, 100)], next_seq: 20, more: true, older_seq: 20 } : { events: [costEvent(10, 1000)], next_seq: 10, more: false }
    }
    vi.mocked(api.workspaceTimeline).mockImplementationOnce(timeline).mockRejectedValueOnce(new Error('workspace.timeline: unavailable'))
    const view = show(twoTurns())
    expect(await screen.findByText('Not read')).toBeDefined()
    view.unmount()
    vi.mocked(api.workspaceTimeline).mockClear().mockImplementation(timeline)
    show(twoTurns())
    await waitFor(() => expect(screen.getByText('Tokens').nextElementSibling?.textContent).toBe('1.1K'))
    expect(api.workspaceTimeline).not.toHaveBeenCalledWith(expect.objectContaining({ after_seq: expect.anything() }))
  })

  it('says when history is not held: paging, expired, or failed', () => {
    vi.mocked(api.runACPHistory).mockReturnValue(new Promise(() => {}))
    const view = show(twoTurns().slice(6), {}, { oldest_seq: 7 })
    expect(screen.getByText('Loading earlier history…')).toBeDefined()
    expect(screen.getByText('Turns').nextElementSibling?.textContent).toBe('≥1')
    view.unmount()
    act(() => useStore.getState().acpOlderState('run_1', false, 'run.acp.history: boom'))
    show([], {}, { truncated_before: true })
    expect(screen.getByText('run.acp.history: boom')).toBeDefined()
    expect(screen.getByRole('button', { name: 'Retry' })).toBeDefined()
  })

  it('says when the agent has not started a turn', () => {
    show([])
    expect(screen.getByText('The agent has not started a turn yet.')).toBeDefined()
  })

  it('renders only the rows in view past 200', async () => {
    sizedViewport(600, 30)
    const items: SessionItem[] = [item('turn_start', 1)]
    for (let i = 0; i < 400; i++) items.push(run2(1, `c${i}`, 'completed'))
    show(items)
    await act(async () => {})
    const mounted = screen.getByRole('tree').querySelectorAll('[role=treeitem]').length
    expect(mounted).toBeGreaterThan(0)
    expect(mounted).toBeLessThan(200)
  })
})

describe('useTraceAnnouncement', () => {
  afterEach(() => vi.useRealTimers())

  const span = (id: string, start: number, status: SpanStatus, kind: SpanKind = 'tool', children: Span[] = []): Span =>
    ({ id, kind, label: id, start, end: start, status, children, size: children.length })
  const tree = (...turns: Span[]) => span('run', 0, 'running', 'run', turns)
  const older = span('turn:1', 10, 'done', 'turn', [span('Old call', 20, 'failed')])
  const live = (...more: Span[]) => span('turn:2', 100, 'running', 'turn', [span('call', 110, 'done'), ...more])

  it('announces a failure first seen after the view opened, but not one in older history as it loads', () => {
    vi.useFakeTimers()
    const { result, rerender } = renderHook(({ root }) => useTraceAnnouncement(root), { initialProps: { root: tree(live()) } })
    rerender({ root: tree(older, live()) })
    act(() => vi.advanceTimersByTime(1500))
    expect(result.current).toBe('')
    rerender({ root: tree(older, live(span('Usage limit reached', 120, 'failed', 'notice'))) })
    act(() => vi.advanceTimersByTime(1500))
    expect(result.current).toBe('Usage limit reached: failed')
  })

  it('keeps older history silent when workers that started before it were read first', () => {
    vi.useFakeTimers()
    const workers = span('workers', 5, 'running', 'workers', [span('worker:w1', 5, 'running', 'worker')])
    const { result, rerender } = renderHook(({ root }) => useTraceAnnouncement(root), { initialProps: { root: tree(live()) } })
    rerender({ root: tree(live(), workers) })
    rerender({ root: tree(older, live(), workers) })
    act(() => vi.advanceTimersByTime(1500))
    expect(result.current).toBe('')
  })
})
