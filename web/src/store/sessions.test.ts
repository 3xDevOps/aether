import type { Event } from '@/lib/types'
import { createRootStore } from '@/store'
import { toRecord } from '@/store/runs'
import { workSummary } from '@/store/session-rows'
import { readSessionLog, rowsForRun, type SessionRow } from '@/store/sessions'
import { alice, bob, fakeApi, roomMessage, run } from '@/test/fixtures'

let seq = 0
function event(type: string, payload: unknown, time: string, actor = ''): Event {
  seq++
  return { id: `ev_${seq}`, seq, time, workspace_id: 'wsp_1', run_id: 'run_1', actor_id: actor, type, payload }
}

const names: Record<string, string> = { [alice.id]: 'Alice', [bob.id]: 'Bob' }
const memberName = (id: string) => names[id] ?? id

function rows(over: Parameters<typeof rowsForRun>[0]): Omit<SessionRow, 'id' | 'at'>[] {
  return rowsForRun(over).map(({ id: _id, at: _at, ...row }) => row)
}

describe('session rows', () => {
  it('folds consecutive tool calls into one work row with verb-first entries', () => {
    const events = [
      event('run.agent', { kind: 'tool_call', tool: 'Bash', tool_use_id: 't1', detail: 'go test ./...' }, '2026-08-14T10:03:00Z'),
      event('run.agent', { kind: 'tool_result', tool_use_id: 't1' }, '2026-08-14T10:03:01Z'),
      event('run.agent', { kind: 'tool_call', tool: 'Read', tool_use_id: 't2', detail: 'src/auth.ts' }, '2026-08-14T10:03:02Z'),
      event('run.agent', { kind: 'tool_call', tool: 'Read', tool_use_id: 't3', detail: 'src/db.ts' }, '2026-08-14T10:03:03Z'),
      event('run.agent', { kind: 'tool_result', tool_use_id: 't3', is_error: true }, '2026-08-14T10:03:04Z'),
    ]
    const [start, work] = rowsForRun({ run: toRecord(run()), events, room: [], memberName, paused: false })
    expect(start).toMatchObject({ kind: 'event', text: 'Run started' })
    expect(work).toMatchObject({
      kind: 'work',
      summary: 'Ran 1 command and read 2 files',
      entries: [
        { label: 'Ran go test ./...', status: 'done' },
        { label: 'Reading src/auth.ts', status: 'running' },
        { label: 'Read src/db.ts', status: 'failed' },
      ],
    })
  })

  it('names mixed work in one sentence', () => {
    expect(workSummary(['Bash', 'execute'])).toBe('Ran 2 commands')
    expect(workSummary(['Edit', 'subagent', 'Grep'])).toBe('Edited 1 file, delegated 1 task and ran 1 search')
    expect(workSummary(['execute', 'execute', 'read', 'read', 'read'])).toBe('Ran 2 commands and read 3 files')
  })

  it('shows a room message to the agent once, with its delivery word, even after the inject records it', () => {
    const steer = roomMessage({
      id: 'msg_1', kind: 'steer_request', state: 'sent', actor_id: bob.id, body: 'add a test', created_at: '2026-08-14T10:04:00Z',
    })
    const queued = roomMessage({
      id: 'msg_2', kind: 'steer_request', state: 'queued', actor_id: bob.id, body: 'and docs',
      created_at: '2026-08-14T10:05:00Z', deliver_after: '2026-08-14T10:05:45Z',
    })
    const events = [event('workspace.timeline', { kind: 'steer', message: 'add a test' }, '2026-08-14T10:04:01Z', bob.id)]
    const users = rows({ run: toRecord(run()), events, room: [steer, queued], memberName, paused: false }).filter((row) => row.kind === 'user')
    expect(users).toEqual([
      { kind: 'user', authorID: bob.id, body: 'add a test', delivery: 'Sent', deliverAfter: undefined, failure: undefined },
      { kind: 'user', authorID: bob.id, body: 'and docs', delivery: 'Queued', deliverAfter: '2026-08-14T10:05:45Z', failure: undefined },
    ])
  })

  it('turns notes, questions and their replies, and steering entries into their own rows', () => {
    const room = [
      roomMessage({ id: 'c1', kind: 'comment', actor_id: bob.id, body: 'looks good', created_at: '2026-08-14T10:04:00Z' }),
      roomMessage({ id: 'q1', kind: 'question', actor_id: bob.id, body: 'keep the prefix?', created_at: '2026-08-14T10:05:00Z' }),
      roomMessage({ id: 'r1', kind: 'reply', actor_id: alice.id, body: 'yes', correlation_id: 'q1', created_at: '2026-08-14T10:06:00Z' }),
    ]
    const events = [
      event('workspace.timeline', { kind: 'pause' }, '2026-08-14T10:03:00Z', alice.id),
      event('workspace.timeline', { kind: 'handoff', message: bob.id }, '2026-08-14T10:07:00Z', alice.id),
    ]
    expect(rows({ run: toRecord(run({ status: 'needs-attention' })), events, room, memberName, paused: false })).toEqual([
      { kind: 'event', text: 'Run started' },
      { kind: 'event', text: 'Paused by Alice' },
      { kind: 'note', authorID: bob.id, body: 'looks good' },
      { kind: 'request', answer: 'reply', authorID: bob.id, title: 'Bob asked', body: 'keep the prefix?', reply: { authorID: alice.id, body: 'yes' } },
      { kind: 'event', text: 'Alice handed the run to Bob' },
    ])
  })

  it('shows the owner\'s own question as a note with no request', () => {
    const own = roomMessage({ id: 'q1', kind: 'question', actor_id: alice.id, body: 'anyone know the port?', created_at: '2026-08-14T10:05:00Z' })
    expect(rows({ run: toRecord(run({ status: 'needs-attention' })), events: [], room: [own], memberName, paused: false }).at(-1)).toEqual({
      kind: 'note', authorID: alice.id, body: 'anyone know the port?', question: true,
    })
  })

  it('ends a working run with what the agent is doing and where to watch it live', () => {
    const working = { ...toRecord(run()), activity: { verb: 'Reading', target: 'src/billing.js', at: '2026-08-14T10:03:00Z', running: {} } }
    expect(rows({ run: working, events: [], room: [], memberName, paused: false }).slice(-2)).toEqual([
      { kind: 'live', label: 'Reading src/billing.js' },
      { kind: 'terminal-note' },
    ])
    expect(rows({ run: toRecord(run()), events: [], room: [], memberName, paused: false }).at(-2)).toEqual({ kind: 'live', label: 'Working' })
    expect(rows({ run: working, events: [], room: [], memberName, paused: true }).at(-1)).toEqual({ kind: 'event', text: 'Run started' })
  })

  it('ends with the agent\'s pending requests and the finish', () => {
    const waiting = toRecord(run({ pending_inputs: [{ id: 'in_1', session_id: 's', kind: 'permission' }] }))
    expect(rows({ run: waiting, events: [], room: [], memberName, paused: false }).at(-1)).toEqual({
      kind: 'request', answer: 'input', title: 'The agent asks for permission',
    })
    const failed = toRecord(run({ status: 'failed', reason: 'agent exited 1', finished_at: '2026-08-14T10:09:00Z' }))
    expect(rows({ run: failed, events: [], room: [], memberName, paused: false }).at(-1)).toEqual({
      kind: 'finished', text: 'Failed: agent exited 1', tone: 'failed',
    })
    const recorded = [event('run.status', { to: 'completed' }, '2026-08-14T10:09:00Z')]
    const done = toRecord(run({ status: 'completed' }))
    expect(rows({ run: done, events: recorded, room: [], memberName, paused: false }).filter((row) => row.kind === 'finished')).toEqual([
      { kind: 'finished', text: 'Finished', tone: 'done' },
    ])
  })
})

describe('session log', () => {
  const span = (from: number, to: number) =>
    Array.from({ length: to - from + 1 }, (_, i) => ({ ...event('run.agent', { kind: 'tool_call', tool: 'Read' }, '2026-08-14T10:03:00Z'), seq: from + i }))

  it('opens on the newest history, stops at what it keeps, then follows live events', async () => {
    const store = createRootStore()
    const workspaceTimeline = vi.fn()
      .mockResolvedValueOnce({ events: span(2001, 3000), next_seq: 3000, more: true, older_seq: 2001 })
      .mockResolvedValueOnce({ events: span(1001, 2000), next_seq: 3000, more: true, older_seq: 1001 })
    await readSessionLog(store, fakeApi({ workspaceTimeline }), { id: 'run_1', workspace_id: 'wsp_1' })

    expect(workspaceTimeline).toHaveBeenCalledTimes(2)
    expect(workspaceTimeline.mock.calls[0]![0]).toMatchObject({ run_id: 'run_1', newest: true, before_seq: undefined, types: ['run.agent', 'workspace.timeline', 'run.status'] })
    expect(workspaceTimeline.mock.calls[1]![0]).toMatchObject({ newest: true, before_seq: 2001 })
    const log = store.getState().sessionLogs.run_1!
    expect([log.events.length, log.events[0]!.seq, log.cursor]).toEqual([2000, 1001, 3000])

    const live = { ...event('run.status', { to: 'completed' }, '2026-08-14T10:05:00Z'), seq: 3001 }
    store.getState().appendSessionEvent(live)
    store.getState().appendSessionEvent({ ...live, run_id: 'run_other', seq: 3002 })
    expect(store.getState().sessionLogs.run_1!.events.at(-1)!.seq).toBe(3001)
    expect(store.getState().sessionLogs.run_other).toBeUndefined()
  })

  it('catches up forward from its cursor on a later read', async () => {
    const store = createRootStore()
    const workspaceTimeline = vi.fn()
      .mockResolvedValueOnce({ events: span(1, 2), next_seq: 10, more: false })
      .mockResolvedValueOnce({ events: span(11, 11), next_seq: 11, more: true })
      .mockResolvedValueOnce({ events: span(12, 12), next_seq: 12, more: false })
    await readSessionLog(store, fakeApi({ workspaceTimeline }), { id: 'run_1', workspace_id: 'wsp_1' })
    await readSessionLog(store, fakeApi({ workspaceTimeline }), { id: 'run_1', workspace_id: 'wsp_1' })

    expect(workspaceTimeline).toHaveBeenNthCalledWith(2, expect.objectContaining({ after_seq: 10 }))
    expect(workspaceTimeline).toHaveBeenNthCalledWith(3, expect.objectContaining({ after_seq: 11 }))
    expect(store.getState().sessionLogs.run_1!.events.map((e) => e.seq)).toEqual([1, 2, 11, 12])
  })

  it('reads from the head again after the server event log restarts', async () => {
    const store = createRootStore()
    const old = event('run.agent', { kind: 'tool_call', tool: 'Read' }, '2026-08-14T10:03:00Z')
    const workspaceTimeline = vi.fn().mockResolvedValue({ events: [old], next_seq: 500, more: false })
    await readSessionLog(store, fakeApi({ workspaceTimeline }), { id: 'run_1', workspace_id: 'wsp_1' })

    store.getState().resetSeq()
    const restored = { ...old, id: 'ev_restored', seq: 3 }
    workspaceTimeline.mockResolvedValue({ events: [restored], next_seq: 3, more: false })
    await readSessionLog(store, fakeApi({ workspaceTimeline }), { id: 'run_1', workspace_id: 'wsp_1' })

    expect(workspaceTimeline.mock.lastCall![0]).toMatchObject({ newest: true })
    expect(workspaceTimeline.mock.lastCall![0]).not.toHaveProperty('after_seq')
    expect(store.getState().sessionLogs.run_1!.events.map((e) => e.id)).toEqual(['ev_restored'])
  })
})
