import type { StateContext } from '@/lib/needs-you'
import type { Event, Run } from '@/lib/types'
import { agoLabel, tickStep, ticks, widerWindow, windowSpan } from '@/routes/board/lanes/axis'
import { laneEdges, mailOf } from '@/routes/board/lanes/edges'
import { buildLanes, layoutLanes, fineMetrics, type LanesInput, type FamilyLane, type RunLane } from '@/routes/board/lanes/model'
import { runSegments, segmentTone, statusChanges, strongest } from '@/routes/board/lanes/segments'
import { mapRuns } from '@/routes/board/selectors'
import { toRecord } from '@/store/runs'
import { alice, bob, run, workspace } from '@/test/fixtures'

const t0 = Date.parse('2026-08-14T10:00:00Z')
const min = 60_000
const at = (minutes: number) => new Date(t0 + minutes * min).toISOString()
const now = t0 + 60 * min

function ctxOf(runs: Run[], over: Partial<StateContext> = {}): StateContext {
  return {
    viewerID: alice.id,
    viewerRole: 'admin',
    members: { [alice.id]: alice, [bob.id]: bob },
    mergedInto: {},
    runs: Object.fromEntries(runs.map((r) => [r.id, toRecord(r)])),
    workspaces: { [workspace.id]: workspace },
    roomMessages: {},
    roomStatus: {},
    pausedRuns: {},
    now,
    ...over,
  }
}

function status(runID: string, minute: number, from: string, to: string, state?: string, seq = minute): Event {
  return { id: `e${seq}`, seq, time: at(minute), workspace_id: workspace.id, run_id: runID, actor_id: '', type: 'run.status', payload: { from, to, state, status_seq: seq } }
}

function input(ctx: StateContext, over: Partial<LanesInput> = {}): LanesInput {
  return {
    cards: mapRuns({ workspace: workspace.id, mineOnly: false, ctx }).cards,
    ctx,
    changes: new Map(),
    floor: () => -Infinity,
    window: { start: now - 60 * min, end: now },
    now,
    group: 'status',
    open: new Map(),
    agents: {},
    canShowAgents: () => false,
    archived: false,
    ...over,
  }
}

describe('runSegments', () => {
  const live = toRecord(run({ id: 'r', created_at: at(0), started_at: at(2), status: 'running', state: 'working', stateChangedAt: at(40) } as Partial<Run>))

  it('draws a starting prefix, then each status change, and the viewer\'s state since the last one', () => {
    const changes = statusChanges([status('r', 10, 'running', 'needs-attention', 'needs-you'), status('r', 20, 'needs-attention', 'running', 'working')]).get('r')!
    const segments = runSegments({ run: { ...live, stateChangedAt: at(20) }, shown: 'working', changes, now, floor: -Infinity })
    expect(segments.map((s) => [s.state, (s.start - t0) / min, (s.end - t0) / min])).toEqual([
      ['starting', 0, 2],
      ['running', 2, 10],
      ['needed-attention', 10, 20],
      ['working', 20, 60],
    ])
  })

  it('ignores a wait\'s rereads, which change no state', () => {
    const reread = (minute: number): Event => {
      const event = status('r', minute, 'needs-attention', 'needs-attention', 'waiting-ci')
      return { ...event, payload: { ...(event.payload as object), reread: true } }
    }
    const changes = statusChanges([status('r', 10, 'running', 'needs-attention', 'waiting-ci'), reread(20), reread(30)]).get('r')!
    expect(changes).toHaveLength(1)
    const segments = runSegments({ run: { ...live, status: 'needs-attention', stateChangedAt: at(10) }, shown: 'needs-you', changes, now, floor: -Infinity })
    expect(segments.at(-1)).toEqual({ state: 'needs-you', start: t0 + 10 * min, end: now })
  })

  it('leaves the span before the read history unloaded, even when the last status change is older', () => {
    const segments = runSegments({ run: { ...live, stateChangedAt: at(40) }, shown: 'working', changes: [], now, floor: t0 + 50 * min })
    expect(segments.map((s) => s.state)).toEqual(['unloaded', 'working'])
    expect(segments[0].end).toBe(t0 + 50 * min)
  })

  it('colours a finished run\'s working time by its outcome', () => {
    expect(segmentTone('working', 'completed')).toBe('done')
    expect(segmentTone('running', 'failed')).toBe('failed')
    expect(segmentTone('needed-attention', 'completed')).toBe('needs-you')
    expect(segmentTone('working')).toBe('working')
  })
})

describe('strongest', () => {
  it('keeps the most urgent tone at each instant', () => {
    const strip = strongest([
      { start: 0, end: 10, tone: 'working' },
      { start: 4, end: 6, tone: 'needs-you' },
      { start: 8, end: 12, tone: 'done' },
    ], { 'needs-you': 3, working: 2, done: 1 })
    expect(strip).toEqual([
      { start: 0, end: 4, tone: 'working' },
      { start: 4, end: 6, tone: 'needs-you' },
      { start: 6, end: 10, tone: 'working' },
      { start: 10, end: 12, tone: 'done' },
    ])
  })
})

describe('axis', () => {
  it('steps each preset to at most eight ticks', () => {
    expect(tickStep(windowSpan['1h'])).toBe(10 * min)
    expect(tickStep(windowSpan['6h'])).toBe(60 * min)
    expect(tickStep(windowSpan['24h'])).toBe(180 * min)
    expect(tickStep(windowSpan['7d'])).toBe(24 * 60 * min)
  })

  it('counts back from now within an hour', () => {
    expect(ticks({ start: now - 60 * min, end: now }, now).map((t) => t.label)).toEqual(['−50m', '−40m', '−30m', '−20m', '−10m'])
    expect(agoLabel(80 * min)).toBe('−1h 20m')
    expect(widerWindow(windowSpan['6h'])).toBe('24h')
    expect(widerWindow(windowSpan['7d'])).toBeUndefined()
  })
})

describe('buildLanes', () => {
  it('orders sections and folds Finished into one summary lane', () => {
    const ctx = ctxOf([
      run({ id: 'w1', task: 'older work', created_at: at(5), started_at: at(5) }),
      run({ id: 'w2', task: 'newer work', created_at: at(30), started_at: at(30) }),
      run({ id: 'done', task: 'merged work', status: 'merged', state: undefined, created_at: at(10), finished_at: at(20) }),
      run({ id: 'bad', task: 'failed work', status: 'failed', state: undefined, created_at: at(12), finished_at: at(15) }),
      run({ id: 'old', task: 'long gone', status: 'completed', state: undefined, created_at: at(-200), finished_at: at(-100) }),
    ])
    const { lanes, inWindow, drawn } = buildLanes(input(ctx))
    expect(lanes.map((l) => (l.kind === 'run' ? l.card.run.id : l.kind === 'section' || l.kind === 'summary' ? l.label : l.kind))).toEqual([
      'Working', 'w2', 'w1', 'Finished', '2 finished · 1 failed',
    ])
    expect(inWindow).toBe(4)
    expect(lanes.find((l) => l.kind === 'section' && l.label === 'Finished')).toMatchObject({ before: 1, open: false })
    expect(drawn.get('done')).toBe('section:finished:summary')
  })

  it('groups a run with its child runs as a family, open while live and folded on request', () => {
    const ctx = ctxOf([
      run({ id: 'parent', task: 'parent work', created_at: at(5), started_at: at(5) }),
      run({ id: 'c1', parent_run_id: 'parent', task: 'first', created_at: at(10), started_at: at(10) }),
      run({ id: 'c2', parent_run_id: 'parent', task: 'second', status: 'completed', state: undefined, created_at: at(12), finished_at: at(20) }),
      run({ id: 'g1', parent_run_id: 'c1', task: 'nested', status: 'needs-attention', state: 'needs-you', created_at: at(15), started_at: at(15) }),
    ])
    const { lanes } = buildLanes(input(ctx))
    expect(lanes.map((l) => l.kind === 'section' ? l.label : l.kind === 'run' ? [l.card.run.id, l.level] : l.kind)).toEqual([
      'Needs you', 'family', ['parent', 3], ['c1', 3], ['c2', 3], ['g1', 3],
    ])
    const header = lanes.find((l): l is FamilyLane => l.kind === 'family')!
    expect(header).toMatchObject({ open: true, parentID: 'parent', detail: '3 subsessions · 1 needs you · 1 working · 1 done' })
    const folded = buildLanes(input(ctx, { open: new Map([[header.key, false]]) })).lanes
    expect(folded.filter((l) => l.kind === 'run').map((l) => (l as RunLane).card.run.id)).toEqual(['g1'])
  })

  it('keeps a folded family\'s parent visible while it awaits review', () => {
    const ctx = ctxOf([
      run({ id: 'parent', status: 'completed', state: undefined, review: 'ready', created_at: at(5), started_at: at(5), finished_at: at(40) }),
      run({ id: 'c1', parent_run_id: 'parent', status: 'completed', state: undefined, created_at: at(10), finished_at: at(20) }),
    ])
    const { lanes } = buildLanes(input(ctx))
    expect(lanes.find((l): l is FamilyLane => l.kind === 'family')).toMatchObject({ open: false })
    expect(lanes.filter((l): l is RunLane => l.kind === 'run').map((l) => l.card.run.id)).toEqual(['parent'])
  })

  it('keeps a failure that ended as it started visible on the Finished strip', () => {
    const ctx = ctxOf([
      run({ id: 'done', status: 'merged', state: undefined, created_at: at(10), finished_at: at(40) }),
      run({ id: 'blip', status: 'failed', state: undefined, created_at: at(20), finished_at: at(20) }),
    ])
    const summary = buildLanes(input(ctx)).lanes.find((l) => l.kind === 'summary')
    expect(summary?.kind === 'summary' && summary.strip.map((s) => s.tone)).toEqual(['done', 'failed', 'done'])
  })

  it('colours a completed run awaiting review by its completion, while it reads Ready for review', () => {
    const ctx = ctxOf([run({ id: 'done', status: 'completed', state: undefined, review: 'ready', created_at: at(10), started_at: at(10), finished_at: at(40) })])
    const lane = buildLanes(input(ctx)).lanes.find((l): l is RunLane => l.kind === 'run')!
    expect(lane.card.state).toBe('ready')
    expect(lane.outcome).toBe('completed')
    expect(segmentTone(lane.segments[0].state, lane.outcome)).toBe('done')
  })

  it('opens Finished on request and puts failed runs first', () => {
    const ctx = ctxOf([
      run({ id: 'done', status: 'merged', state: undefined, created_at: at(10), finished_at: at(40) }),
      run({ id: 'bad', status: 'failed', state: undefined, created_at: at(12), finished_at: at(15) }),
    ])
    const { lanes } = buildLanes(input(ctx, { open: new Map([['section:finished', true]]) }))
    expect(lanes.filter((l): l is RunLane => l.kind === 'run').map((l) => l.card.run.id)).toEqual(['bad', 'done'])
  })

  it('folds a finished family onto its parent\'s outcome', () => {
    const ctx = ctxOf([
      run({ id: 'parent', status: 'merged', state: undefined, created_at: at(1), started_at: at(1), finished_at: at(50) }),
      run({ id: 'c', parent_run_id: 'parent', status: 'merged', state: undefined, created_at: at(5), started_at: at(5), finished_at: at(30) }),
    ])
    const family = buildLanes(input(ctx, { open: new Map([['section:finished', true]]) })).lanes.find((l): l is FamilyLane => l.kind === 'family')!
    expect(family).toMatchObject({ open: false, outcome: 'merged', parentID: 'parent' })
  })

  it('draws a folded family\'s parent bar and a strip that shows waiting child runs too', () => {
    const ctx = ctxOf([
      run({ id: 'parent', state: 'waiting-agents', created_at: at(1), started_at: at(1) }),
      run({ id: 'c', parent_run_id: 'parent', state: 'answered', created_at: at(5), started_at: at(5) }),
    ])
    const header = buildLanes(input(ctx)).lanes.find((l): l is FamilyLane => l.kind === 'family')!
    const family = buildLanes(input(ctx, { open: new Map([[header.key, false]]) })).lanes.find((l): l is FamilyLane => l.kind === 'family')!
    expect(family.open).toBe(false)
    expect(family.segments.map((s) => s.state)).toEqual(['waiting-agents'])
    expect(family.strip.map((s) => [s.tone, (s.start - t0) / min, (s.end - t0) / min])).toEqual([['ended', 5, 60]])
  })

  it('sections by owner with the viewer first', () => {
    const ctx = ctxOf([
      run({ id: 'b', member_id: bob.id, created_at: at(10) }),
      run({ id: 'a', member_id: alice.id, created_at: at(20) }),
    ])
    const { lanes } = buildLanes(input(ctx, { group: 'owner' }))
    expect(lanes.filter((l) => l.kind === 'section').map((l) => l.kind === 'section' && l.label)).toEqual([`${alice.display_name} (you)`, bob.display_name])
  })

  it('lays lanes out from fixed heights', () => {
    const ctx = ctxOf([run({ id: 'a', created_at: at(10) })])
    const { lanes } = buildLanes(input(ctx))
    const layout = layoutLanes(lanes, fineMetrics)
    expect(layout.height).toBe(fineMetrics.section + fineMetrics.lane)
  })
})

describe('laneEdges', () => {
  it('bundles mail between the same lanes and marks it acknowledged only when every message is', () => {
    const ctx = ctxOf([run({ id: 'a', created_at: at(0) }), run({ id: 'b', created_at: at(0) })])
    const model = buildLanes(input(ctx))
    const mail = (id: string, minute: number, seq: number): Event => ({
      id, seq, time: at(minute), workspace_id: workspace.id, run_id: 'a', actor_id: '', type: 'coord.message',
      payload: { message_id: id, workspace_id: workspace.id, from_run_id: 'a', to_run_id: 'b', kind: 'question' },
    })
    const ack: Event = { id: 'k', seq: 9, time: at(31), workspace_id: workspace.id, run_id: 'b', actor_id: '', type: 'coord.message.acked', payload: { message_id: 'm1', to_run_id: 'b', acked_at: at(31) } }
    const base = { drawn: model.drawn, window: { start: now - 60 * min, end: now }, now, bucket: 5 * min, overlaps: { a: [{ run_id: 'b', member_id: alice.id, files: ['x.go'] }] }, hidden: new Set<never>() }
    const edges = laneEdges({ ...base, mail: mailOf([mail('m1', 30, 1), mail('m2', 31, 2), ack, mail('m3', 50, 3)]) })
    expect(edges.map((e) => [e.messages?.ids, e.messages?.acked, e.messages?.kind])).toEqual([
      [['m1', 'm2'], false, 'question'],
      [['m3'], false, 'question'],
    ])
    expect(laneEdges({ ...base, mail: mailOf([]), focus: 'a' }).map((e) => [e.kind, e.files])).toEqual([['overlap', ['x.go']]])
  })

  it('names every sender in mail bundled from one folded lane', () => {
    const ctx = ctxOf([run({ id: 'a', created_at: at(0) }), run({ id: 'b', created_at: at(0) }), run({ id: 'c', created_at: at(0) })])
    const model = buildLanes(input(ctx))
    const drawn = new Map(model.drawn).set('b', model.drawn.get('a')!)
    const mail = (id: string, from: string, seq: number): Event => ({
      id, seq, time: at(30), workspace_id: workspace.id, run_id: from, actor_id: '', type: 'coord.message',
      payload: { message_id: id, workspace_id: workspace.id, from_run_id: from, to_run_id: 'c', kind: 'message' },
    })
    const [edge] = laneEdges({
      drawn, mail: mailOf([mail('m1', 'a', 1), mail('m2', 'b', 2)]),
      window: { start: now - 60 * min, end: now }, now, bucket: 5 * min, overlaps: {}, hidden: new Set(),
    })
    expect(edge.messages).toMatchObject({ ids: ['m1', 'm2'], senders: ['a', 'b'] })
  })
})
