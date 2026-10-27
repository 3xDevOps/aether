import type { SessionItem, SessionToolCall } from '@/lib/session-types'
import type { Event } from '@/lib/types'
import { appendItems } from '@/store/session-rows'
import { indexItems, noAgents } from '@/store/subagents'
import { costTotals, deriveTrace, findSpan, traceStats, type Span, type TraceSources } from '@/store/trace'
import { axisAt, axisMarks, buildAxis, idleGaps, tickLabel } from '@/store/trace-axis'
import { barSegments, flattenTrace } from '@/store/trace-rows'
import { item, resetItems, say, tool } from '@/test/acp-stream'

beforeEach(resetItems)

const t0 = Date.parse('2026-10-06T10:00:00Z')
const at = (item: SessionItem) => Date.parse(item.time)

function trace(items: SessionItem[], over: Partial<TraceSources> = {}): Span {
  const { index, main, home } = indexItems(noAgents, items)
  return deriveTrace({
    start: t0,
    end: null,
    runStatus: 'running',
    live: true,
    turns: appendItems([], main, (it) => home.get(it)),
    agents: index,
    statusEvents: [],
    ...over,
  })
}

const run = (turn: number, id: string, status: string, extra: Partial<SessionToolCall> = {}) =>
  tool(turn, id, 'execute', `npm test ${id}`, status, { tool_name: 'Bash', ...extra })
const launch = (turn: number, id: string, status: string, extra: Partial<SessionToolCall> = {}) =>
  tool(turn, id, 'other', `Agent ${id}`, status, { tool_name: 'Agent', subagent: { description: `Job ${id}`, type: 'Explore' }, ...extra })

describe('deriveTrace', () => {
  it('times a tool call from its first snapshot to the one that settles it', () => {
    const start = item('turn_start', 1)
    const opened = run(1, 'c1', 'in_progress')
    const done = run(1, 'c1', 'completed')
    const failed = [run(1, 'c2', 'in_progress'), run(1, 'c2', 'failed', { exit_code: 2 })]
    const root = trace([start, say(1, 'user', 'Fix the tests\nplease'), opened, done, ...failed])
    const turn = root.children[0]!
    expect(turn).toMatchObject({ kind: 'turn', label: 'Fix the tests', sub: 'Turn 1', status: 'running', end: null })
    expect(findSpan(root, '1/call:c1')).toMatchObject({ start: at(opened), end: at(done), status: 'done', command: 'npm test c1', sub: 'Bash' })
    expect(findSpan(root, '1/call:c2')).toMatchObject({ status: 'failed', exitCode: 2 })
  })

  it('stops a call still open when its turn ended, but keeps a live background task running', () => {
    const items = [
      item('turn_start', 1),
      run(1, 'c1', 'in_progress'),
      run(1, 'bg', 'completed', { task: { id: 't1', status: 'running' } }),
      item('turn_end', 1, { stop_reason: 'end_turn' }),
    ]
    const root = trace(items)
    expect(findSpan(root, '1/call:c1')).toMatchObject({ status: 'stopped', end: at(items[3]!) })
    expect(findSpan(root, '1/call:bg')).toMatchObject({ status: 'running', end: null })
    expect(root.children[0]).toMatchObject({ status: 'done', stop: 'Finished', end: null })
    const finished = trace(items, { live: false, end: at(items[3]!) + 1000, runStatus: 'completed' })
    expect(findSpan(finished, '1/call:bg')).toMatchObject({ status: 'stopped' })
  })

  it('keeps a live turn running when its turn_start is not held', () => {
    const root = trace([run(3, 'c1', 'in_progress'), item('request', 3, { request: { id: 'q1', kind: 'permission', title: 'Run it?', status: 'pending' } })])
    expect(root.children[0]).toMatchObject({ status: 'running', end: null, unknownStart: true })
    expect(findSpan(root, '3/call:c1')).toMatchObject({ status: 'running', end: null })
    expect(findSpan(root, '3/wait:q1')).toMatchObject({ status: 'waiting', requestStatus: 'pending' })
  })

  it('nests subagents with their own calls and ends them when they settle', () => {
    const items = [
      item('turn_start', 1),
      launch(1, 'a1', 'in_progress'),
      tool(1, 'r1', 'read', 'Read a.ts', 'completed', { parent_id: 'a1' }),
      launch(1, 'b1', 'in_progress', { parent_id: 'a1' }),
      launch(1, 'b1', 'failed', { parent_id: 'a1' }),
      launch(1, 'a1', 'completed'),
      item('turn_end', 1, { stop_reason: 'end_turn' }),
    ]
    const root = trace(items)
    const agent = findSpan(root, 'agent:a1')!
    expect(agent).toMatchObject({ label: 'Job a1', sub: 'Explore', start: at(items[1]!), end: at(items[5]!), status: 'done', agentID: 'a1' })
    expect(agent.children.map((child) => child.id)).toEqual(['a1/call:r1', 'agent:b1'])
    expect(findSpan(root, 'agent:b1')).toMatchObject({ status: 'failed' })
    expect(findSpan(root, 'a1/call:r1')).toMatchObject({ agentID: 'a1', turn: 1 })
  })

  it('keeps a subagent whose launch is not held, in its turn or under the run when the turn is not held either', () => {
    const items = [
      tool(1, 'r1', 'read', 'Read a.ts', 'completed', { parent_id: 'a1' }),
      tool(1, 'r2', 'read', 'Read b.ts', 'failed', { parent_id: 'a1' }),
      launch(1, 'b1', 'in_progress', { parent_id: 'a1' }),
    ]
    const root = trace(items)
    expect(root.children.map((child) => child.id)).toEqual(['agent:a1'])
    expect(findSpan(root, 'agent:a1')).toMatchObject({ unknownStart: true, start: at(items[0]!) })
    expect(findSpan(root, 'a1/call:r2')).toMatchObject({ status: 'failed' })
    expect(findSpan(root, 'agent:b1')).toBeDefined()
    expect(traceStats(root).errors).toEqual(['a1/call:r2'])
    const held = trace([item('turn_start', 1), ...items.slice(0, 2)])
    expect(held.children[0]!.children.map((child) => child.id)).toEqual(['agent:a1'])
  })

  it('draws a wait from the request opening to its answer, and an open one as needing you', () => {
    const request = { id: 'q1', kind: 'permission' as const, title: 'Run npm test?' }
    const asked = item('request', 1, { request: { ...request, status: 'pending' } })
    const answered = item('request', 1, { request: { ...request, status: 'answered', answer: 'allow_once' } })
    const pending = item('request', 1, { request: { id: 'q2', kind: 'question', title: 'Which branch?', status: 'pending' } })
    const root = trace([item('turn_start', 1), asked, answered, pending])
    expect(findSpan(root, '1/wait:q1')).toMatchObject({ start: at(asked), end: at(answered), status: 'done', answer: 'allow_once', request: 'permission' })
    expect(findSpan(root, '1/wait:q2')).toMatchObject({ status: 'waiting', end: null })
  })

  it('names turns by their prompt, a wake-up, or their number, and reads the stop reason', () => {
    const items = [
      item('turn_start', 1), say(1, 'user', 'Aether has 2 unacknowledged inbox item(s). Read them.'), item('turn_end', 1, { stop_reason: 'max_tokens' }),
      item('turn_start', 2, { autonomous: true }), item('usage', 2, { usage: { used: 45_000, size: 200_000 } }), item('turn_end', 2, { stop_reason: 'cancelled' }),
    ]
    const [first, second] = trace(items, { live: false, end: t0 + 60_000, runStatus: 'completed' }).children
    expect(first).toMatchObject({ label: 'Woken by new agent messages', status: 'failed', stop: 'Stopped: the reply reached its token limit' })
    expect(second).toMatchObject({ label: 'Turn 2', sub: undefined, status: 'stopped', autonomous: true, context: { used: 45_000, size: 200_000 } })
  })

  it('colours the run lane by its status history', () => {
    const event = (seconds: number, to: string, state?: string): Event => ({
      id: `e${seconds}`, seq: seconds, time: new Date(t0 + seconds * 1000).toISOString(), workspace_id: 'w', run_id: 'r1', actor_id: '', type: 'run.status',
      payload: { to, state, status_seq: seconds },
    })
    const reread = { ...event(45, 'needs-attention', 'needs-you'), payload: { to: 'needs-attention', state: 'needs-you', status_seq: 45, reread: true } }
    const root = trace([], { statusEvents: [event(0, 'running', 'working'), event(30, 'needs-attention', 'needs-you'), reread] })
    expect(root.lane).toEqual([
      { from: t0, to: t0 + 30_000, tone: 'working', label: 'Working' },
      { from: t0 + 30_000, to: null, tone: 'needs-you', label: 'Needs you' },
    ])
    expect(trace([], { statusEvents: null, statusError: 'permission denied' })).toMatchObject({ lane: undefined, laneError: 'permission denied' })
  })
})

describe('traceStats', () => {
  it('counts turns, calls and subagents, lists failures, and names the slowest call once three finished', () => {
    const items = [
      item('turn_start', 1),
      run(1, 'c1', 'in_progress'), run(1, 'c1', 'completed'),
      run(1, 'c2', 'in_progress'), item('plan', 1), item('plan', 1), run(1, 'c2', 'completed'),
      launch(1, 'a1', 'in_progress'),
      run(1, 'c3', 'in_progress'), run(1, 'c3', 'failed'),
      item('notice', 1, { notice: { severity: 'error', title: 'Usage limit reached' } }),
    ]
    const stats = traceStats(trace(items))
    expect(stats).toMatchObject({ turns: 1, tools: 3, subagents: 1, slowest: '1/call:c2' })
    expect(stats.errors).toEqual(['1/call:c3', '1/notice:11'])
    expect(traceStats(trace(items.slice(0, 7))).slowest).toBeUndefined()
  })
})

describe('flattenTrace', () => {
  const items = () => [
    item('turn_start', 1), run(1, 'c1', 'in_progress'), run(1, 'c1', 'failed'), run(1, 'c2', 'completed'), item('turn_end', 1, { stop_reason: 'end_turn' }),
    item('turn_start', 2), run(2, 'c3', 'completed'), item('turn_end', 2, { stop_reason: 'end_turn' }),
  ]

  it('folds closed turns but the last, and surfaces a failure under a folded turn', () => {
    const root = trace(items())
    const rows = flattenTrace(root, {})
    expect(rows.map((row) => row.key)).toEqual(['run', 'turn:1', 'turn:1>1/call:c1', 'turn:2', '2/call:c3'])
    expect(rows[1]).toMatchObject({ level: 2, expanded: false, posinset: 1, setsize: 2 })
    expect(rows[2]).toMatchObject({ surfaced: true, level: 3, parentKey: 'turn:1' })
    expect(root.children[0]!.size).toBe(2)
  })

  it('follows the fold state and reuses a closed turn’s rows', () => {
    const root = trace(items())
    const open = { 'turn:1': true, 'turn:2': false }
    const rows = flattenTrace(root, open)
    expect(rows.map((row) => row.key)).toEqual(['run', 'turn:1', '1/call:c1', '1/call:c2', 'turn:2'])
    expect(flattenTrace(root, open)[2]).toBe(rows[2])
  })

  it('opens an integrator’s last turn ahead of its Workers group', () => {
    const root = trace(items(), { live: false, end: t0 + 60_000, runStatus: 'completed', workers: [{ runID: 'w1', label: 'Worker 1', start: t0, end: t0 + 1000, status: 'done' }] })
    expect(flattenTrace(root, {}).map((row) => [row.key, row.expanded])).toEqual([
      ['run', true], ['turn:1', false], ['turn:1>1/call:c1', undefined], ['turn:2', true], ['2/call:c3', undefined], ['workers', true], ['worker:w1', undefined],
    ])
  })

  it('packs a parent bar with tool density and failures', () => {
    const turn = trace(items()).children[0]!
    expect(barSegments(turn).map((segment) => segment.tone)).toEqual(['tool', 'failed'])
  })

  it('surfaces and marks a failed subagent under a folded turn even when its calls succeeded', () => {
    const root = trace([
      item('turn_start', 1), launch(1, 'a1', 'in_progress'), tool(1, 'r1', 'read', 'Read a.ts', 'completed', { parent_id: 'a1' }),
      launch(1, 'a1', 'failed'), item('turn_end', 1, { stop_reason: 'end_turn' }),
      item('turn_start', 2), run(2, 'c3', 'completed'), item('turn_end', 2, { stop_reason: 'end_turn' }),
    ])
    expect(flattenTrace(root, {}).map((row) => row.key)).toContain('turn:1>agent:a1')
    expect(barSegments(root.children[0]!).map((segment) => segment.tone)).toContain('failed')
  })

  it('indents a subagent’s rows by its new depth when its turn loads after it', () => {
    const items = [
      item('turn_start', 1), say(1, 'user', 'Fix the tests'),
      tool(1, 'r1', 'read', 'Read a.ts', 'failed', { parent_id: 'a1' }), tool(1, 'r2', 'read', 'Read b.ts', 'completed', { parent_id: 'b1' }),
    ]
    const sources = { start: t0, end: null, runStatus: 'running' as const, live: true, statusEvents: [] }
    const open = {}
    const failure = (root: Span) => flattenTrace(root, open).find((row) => row.key === 'agent:a1>a1/call:r1')?.level
    const newer = indexItems(noAgents, items.slice(2))
    expect(failure(deriveTrace({ ...sources, turns: [], agents: newer.index }))).toBe(3)
    const older = indexItems(newer.index, items.slice(0, 2), true)
    expect(failure(deriveTrace({ ...sources, turns: appendItems([], older.main), agents: older.index }))).toBe(4)
  })

  it('keeps every failure and tool stretch on a parent bar whatever order its branches are walked in', () => {
    const items = [
      item('turn_start', 1),
      launch(1, 'a1', 'in_progress'),
      run(1, 'c2', 'in_progress'), run(1, 'c2', 'failed'),
      tool(1, 'r1', 'read', 'Read a.ts', 'in_progress', { parent_id: 'a1' }), tool(1, 'r1', 'read', 'Read a.ts', 'failed', { parent_id: 'a1' }),
      launch(1, 'a1', 'completed'),
      run(1, 'c3', 'in_progress'), item('plan', 1), run(1, 'c3', 'completed'),
      run(1, 'c4', 'completed'),
      item('turn_end', 1, { stop_reason: 'end_turn' }),
    ]
    const segments = barSegments(trace(items).children[0]!)
    expect(segments.filter((segment) => segment.tone === 'failed').map((segment) => segment.from)).toEqual([at(items[2]!), at(items[4]!)])
    expect(segments.filter((segment) => segment.tone === 'tool').map((segment) => segment.from)).toEqual([at(items[7]!), at(items[10]!)])
  })

  it('derives and flattens 5,000 spans within budget', () => {
    const big: SessionItem[] = []
    for (let turn = 1; turn <= 200; turn++) {
      big.push(item('turn_start', turn))
      for (let call = 0; call < 24; call++) big.push(run(turn, `c${turn}-${call}`, 'in_progress'), run(turn, `c${turn}-${call}`, 'completed'))
      big.push(item('turn_end', turn, { stop_reason: 'end_turn' }))
    }
    const started = performance.now()
    const root = trace(big)
    const rows = flattenTrace(root, Object.fromEntries(root.children.map((turn) => [turn.id, true])))
    expect(rows.length).toBe(1 + 200 * 25)
    expect(performance.now() - started).toBeLessThan(1000)
  })
})

describe('trace axis', () => {
  const minute = 60_000

  it('compresses idle stretches into breaks and maps time across them', () => {
    const items = [item('turn_start', 1), item('turn_end', 1, { stop_reason: 'end_turn' })]
    const late = { ...item('turn_start', 2), time: new Date(t0 + 120 * minute).toISOString() }
    const lateEnd = { ...item('turn_end', 2, { stop_reason: 'end_turn' }), time: new Date(t0 + 121 * minute).toISOString() }
    const root = trace([...items, late, lateEnd], { live: false, end: t0 + 121 * minute, runStatus: 'completed' })
    const gaps = idleGaps(root, 0, false)
    expect(gaps).toEqual([{ from: at(items[1]!), to: at(late), unknown: false }])
    const axis = buildAxis(root.start, root.end!, gaps, false)
    expect(axisAt(axis, at(late))).toBeCloseTo(at(items[1]!) - t0 + axis.breakMs)
    expect(axis.length).toBeCloseTo(axisAt(axis, root.end!))
    const marks = axisMarks(axis, 0, axis.length, 600 / axis.length)
    expect(marks.find((mark) => mark.kind === 'break')).toMatchObject({ a: at(items[1]!) - t0 + axis.breakMs, label: '1h idle' })
    expect(marks.filter((mark) => mark.kind === 'tick').map((mark) => mark.label)).toContain('2h 0m 30s')
  })

  it('holds an open idle break still as the clock runs', () => {
    const items = [item('turn_start', 1), item('turn_end', 1, { stop_reason: 'end_turn' })]
    const root = trace(items)
    const end = at(items[1]!)
    const early = idleGaps(root, end + 10 * minute, false)
    expect(early).toEqual([{ from: end, to: end + 10 * minute, open: true }])
    const axis = buildAxis(root.start, end + 10 * minute, early, true)
    const later = buildAxis(root.start, end + 11 * minute, idleGaps(root, end + 11 * minute, false), true)
    expect(later.length).toBe(axis.length)
    expect(axisAt(axis, end + 11 * minute)).toBeCloseTo(axisAt(axis, end) + axis.breakMs)
    const marks = axisMarks(axis, 0, axis.length, 600 / axis.length)
    expect(marks.at(-1)).toMatchObject({ kind: 'break', label: '10m idle' })
  })

  it('walks only the ticks in view', () => {
    const day = 24 * 60 * minute
    const axis = buildAxis(t0, t0 + 2 * day, [], false)
    const started = performance.now()
    const marks = axisMarks(axis, day, day + 3000, 600 / 3000)
    expect(marks.map((mark) => mark.label)).toContain('1d 0h 0m 1s')
    expect(performance.now() - started).toBeLessThan(20)
  })

  it('marks history before the first held item as not loaded', () => {
    const items = [{ ...item('turn_start', 5), time: new Date(t0 + 10 * minute).toISOString() }]
    const root = trace(items)
    expect(idleGaps(root, t0 + 11 * minute, true)[0]).toEqual({ from: t0, to: t0 + 10 * minute, unknown: true })
  })

  it('labels ticks in elapsed time down to the second', () => {
    expect(tickLabel(0)).toBe('0s')
    expect(tickLabel(90_000)).toBe('1m 30s')
    expect(tickLabel(3_600_000)).toBe('1h')
    expect(tickLabel(7_210_000)).toBe('2h 0m 10s')
    expect(tickLabel(26 * 3_600_000)).toBe('1d 2h')
  })
})

describe('costTotals', () => {
  const cost = (payload: object, seq: number): Event => ({ id: `c${seq}`, seq, time: '', workspace_id: 'w', run_id: 'r1', actor_id: '', type: 'run.cost', payload })

  it('adds Enhanced increments, replaces headless totals, and ignores unmetered runs', () => {
    expect(costTotals([cost({ metered: false, cost_usd: 0 }, 1)])).toBeUndefined()
    expect(costTotals([
      cost({ metered: true, increment: true, input_tokens: 10, output_tokens: 5, cost_usd: 0.01 }, 1),
      cost({ metered: true, increment: true, input_tokens: 20, output_tokens: 5, cost_usd: 0.02 }, 2),
    ])).toEqual({ input: 30, output: 10, cost: 0.03 })
    expect(costTotals([
      cost({ metered: true, input_tokens: 10, output_tokens: 5, cost_usd: 0.01 }, 1),
      cost({ metered: true, input_tokens: 40, output_tokens: 9, cost_usd: 0.05 }, 2),
    ])).toEqual({ input: 40, output: 9, cost: 0.05 })
  })
})
