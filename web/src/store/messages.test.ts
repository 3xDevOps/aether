import { ApiError } from '@/lib/api'
import type { Event, RunMessage } from '@/lib/types'
import { createRootStore } from '@/store'
import { deliveryWord, groupMessages, loadMessagePage, scopeMessages, type MessageScope } from '@/store/messages'
import { applyEvent, hydrate } from '@/store/sync'
import { fakeApi, run, workspace } from '@/test/fixtures'

function runMessage(over: Partial<RunMessage> = {}): RunMessage {
  return {
    id: 'msg-1',
    workspace_id: workspace.id,
    mission_id: 'mission-1',
    from_run_id: 'run_worker',
    to_run_id: 'run_1',
    kind: 'message',
    body: 'hello',
    created_at: '2026-10-05T10:00:00Z',
    ...over,
  }
}

const workspaceScope: MessageScope = { kind: 'workspace', workspaceID: workspace.id }
const missionScope: MessageScope = { kind: 'mission', workspaceID: workspace.id, missionID: 'mission-1' }
const runScope: MessageScope = { kind: 'run', workspaceID: workspace.id, runID: 'run_1' }

const ids = (messages: RunMessage[]) => messages.map((m) => m.id)

describe('messages slice', () => {
  it('merges pages by id, oldest first, and pages back to exhaustion', () => {
    const store = createRootStore()
    const a = runMessage({ id: 'a', created_at: '2026-10-05T10:00:00.1Z' })
    const b = runMessage({ id: 'b', created_at: '2026-10-05T10:00:00.11Z' })
    const c = runMessage({ id: 'c', created_at: '2026-10-05T10:00:01Z' })

    store.getState().setMessagePage(runScope, [c, b], 'cursor-b')
    expect(ids(scopeMessages(store.getState(), runScope))).toEqual(['b', 'c'])
    expect(store.getState().messageLists['run:run_1'].nextBefore).toBe('cursor-b')

    store.getState().setMessagePage(runScope, [b, a], undefined, true)
    expect(ids(scopeMessages(store.getState(), runScope))).toEqual(['a', 'b', 'c'])
    expect(store.getState().messageLists['run:run_1'].nextBefore).toBeNull()

    // A newest-page refresh that overlaps keeps the older pages and the exhausted cursor.
    const d = runMessage({ id: 'd', created_at: '2026-10-05T10:00:02Z' })
    store.getState().setMessagePage(runScope, [d, c], 'cursor-c')
    expect(ids(scopeMessages(store.getState(), runScope))).toEqual(['a', 'b', 'c', 'd'])
    expect(store.getState().messageLists['run:run_1'].nextBefore).toBeNull()
  })

  it('replaces a list when the newest page skips past everything held', () => {
    const store = createRootStore()
    store.getState().setMessagePage(workspaceScope, [runMessage({ id: 'old' })], undefined)
    const newer = runMessage({ id: 'new', created_at: '2026-10-05T11:00:00Z' })
    store.getState().setMessagePage(workspaceScope, [newer], 'cursor-new')
    expect(ids(scopeMessages(store.getState(), workspaceScope))).toEqual(['new'])
    expect(store.getState().messageLists[`workspace:${workspace.id}`].nextBefore).toBe('cursor-new')
  })

  it('keeps an acknowledgement a stale list read does not know about', () => {
    const store = createRootStore()
    store.getState().setMessagePage(missionScope, [runMessage()], undefined)
    store.getState().applyMessageAcked('msg-1', '2026-10-05T10:05:00Z')
    expect(store.getState().runMessages['msg-1'].acked_at).toBe('2026-10-05T10:05:00Z')

    store.getState().setMessagePage(missionScope, [runMessage({ body: 'hello' })], undefined)
    expect(store.getState().runMessages['msg-1'].acked_at).toBe('2026-10-05T10:05:00Z')
    store.getState().applyMessageAcked('unknown', '2026-10-05T10:05:00Z')
    expect(store.getState().runMessages.unknown).toBeUndefined()
  })

  it('reads a scope with its own filter and records a failed read', async () => {
    const store = createRootStore()
    const list = vi.fn()
      .mockResolvedValueOnce({ messages: [runMessage()], next_before: 'cursor-1' })
      .mockResolvedValueOnce({ messages: [runMessage({ id: 'msg-0', created_at: '2026-10-05T09:00:00Z' })] })
      .mockRejectedValueOnce(new Error('coord.messages.list: server unreachable'))
    const client = fakeApi({ coordMessagesList: list })

    await loadMessagePage(store, client, missionScope)
    await loadMessagePage(store, client, missionScope, true)
    await loadMessagePage(store, client, missionScope, true)
    expect(list.mock.calls.map(([params]) => params)).toEqual([
      { workspace_id: workspace.id, mission_id: 'mission-1', run_id: undefined, before: undefined },
      { workspace_id: workspace.id, mission_id: 'mission-1', run_id: undefined, before: 'cursor-1' },
    ])
    expect(ids(scopeMessages(store.getState(), missionScope))).toEqual(['msg-0', 'msg-1'])

    await loadMessagePage(store, client, missionScope)
    expect(store.getState().messageErrors['mission:mission-1']).toContain('server unreachable')
  })
})

describe('message grouping', () => {
  const at = (minute: number) => `2026-10-05T10:${String(minute).padStart(2, '0')}:00Z`
  const shape = (groups: ReturnType<typeof groupMessages>) =>
    groups.map((g) => (g.kind === 'single' ? g.message.id : g.kind === 'thread' ? `${g.question.id}<${g.replies.map((m) => m.id)}>` : `[${g.messages.map((m) => m.id)}]`))

  it('collapses adjacent plain messages between one pair, never questions, replies or reports', () => {
    const groups = groupMessages([
      runMessage({ id: 'a', created_at: at(1) }),
      runMessage({ id: 'b', created_at: at(2) }),
      runMessage({ id: 'c', created_at: at(3) }),
      runMessage({ id: 'q', kind: 'question', correlation_id: 'q', created_at: at(4) }),
      runMessage({ id: 'd', created_at: at(5) }),
      runMessage({ id: 'r', kind: 'report', created_at: at(6) }),
      runMessage({ id: 'e', created_at: at(7) }),
      runMessage({ id: 'f', from_run_id: 'run_other', created_at: at(8) }),
      runMessage({ id: 'g', from_run_id: 'run_other', created_at: at(9) }),
    ])
    expect(shape(groups)).toEqual(['[a,b,c]', 'q<>', 'd', 'r', 'e', '[f,g]'])
  })

  it('threads a reply under its loaded question, moves the thread to its latest reply and keeps an orphan reply on its own', () => {
    const groups = groupMessages([
      runMessage({ id: 'q', kind: 'question', correlation_id: 'q', created_at: at(1) }),
      runMessage({ id: 'm', created_at: at(2) }),
      runMessage({ id: 'r1', kind: 'reply', correlation_id: 'q', from_run_id: 'run_1', to_run_id: 'run_worker', created_at: at(3) }),
      runMessage({ id: 'r2', kind: 'reply', correlation_id: 'paged-out', created_at: at(4) }),
    ])
    expect(shape(groups)).toEqual(['m', 'q<r1>', 'r2'])
  })

  it('names the delivery state', () => {
    expect(deliveryWord(runMessage())).toBe('Sent')
    expect(deliveryWord(runMessage({ delivered_at: at(1) }))).toBe('Delivered')
    expect(deliveryWord(runMessage({ delivered_at: at(1), acked_at: at(2) }))).toBe('Acknowledged')
  })
})

describe('agent message events', () => {
  const messageEvent = (seq: number, payload: Record<string, unknown>, type = 'coord.message'): Event => ({
    id: `coord-${seq}`,
    seq,
    time: '2026-10-05T10:00:00Z',
    workspace_id: workspace.id,
    run_id: 'run_worker',
    actor_id: '',
    type,
    payload,
  })

  it('re-reads every loaded list the message belongs to and the recipient count', async () => {
    const store = createRootStore()
    await hydrate(store, fakeApi())
    store.getState().setMessagePage(runScope, [], undefined)
    store.getState().setMessagePage(missionScope, [], undefined)
    store.getState().setMessagePage({ kind: 'run', workspaceID: workspace.id, runID: 'run_other' }, [], undefined)

    const arrived = runMessage({ id: 'msg-2', kind: 'question' })
    const list = vi.fn(async (_: { mission_id?: string; run_id?: string }) => ({ messages: [arrived] }))
    const runGet = vi.fn(async () => run({ unacked_messages: 1 }))
    const client = fakeApi({ coordMessagesList: list, runGet })
    const payload = {
      message_id: 'msg-2', workspace_id: workspace.id, mission_id: 'mission-1',
      from_run_id: 'run_worker', to_run_id: 'run_1', kind: 'question',
    }

    expect(await applyEvent(store, messageEvent(8, payload), client)).toBe(true)
    expect(list.mock.calls.map(([params]) => params.mission_id ?? params.run_id).sort()).toEqual(['mission-1', 'run_1'])
    expect(ids(scopeMessages(store.getState(), runScope))).toEqual(['msg-2'])
    await vi.waitFor(() => expect(store.getState().runs.run_1.unacked_messages).toBe(1))

    runGet.mockResolvedValueOnce(run({ unacked_messages: 0 }))
    const acked = { message_id: 'msg-2', to_run_id: 'run_1', acked_at: '2026-10-05T10:01:00Z' }
    expect(await applyEvent(store, messageEvent(9, acked, 'coord.message.acked'), client)).toBe(true)
    expect(store.getState().runMessages['msg-2'].acked_at).toBe('2026-10-05T10:01:00Z')
    await vi.waitFor(() => expect(store.getState().runs.run_1.unacked_messages).toBe(0))
  })

  it('applies the event without waiting for the recipient count', async () => {
    const store = createRootStore()
    await hydrate(store, fakeApi())
    const client = fakeApi({ runGet: vi.fn(() => new Promise<never>(() => {})) })
    const payload = { message_id: 'msg-3', to_run_id: 'run_1', acked_at: '2026-10-05T10:01:00Z' }
    expect(await applyEvent(store, messageEvent(8, payload, 'coord.message.acked'), client)).toBe(true)
    expect(store.getState().lastSeq).toBe(8)
  })

  it('takes only the count from the recipient read', async () => {
    const store = createRootStore()
    await hydrate(store, fakeApi())
    store.getState().applyRunTitle('run_1', 'renamed after the read')
    const client = fakeApi({ runGet: vi.fn(async () => run({ title: 'stale', unacked_messages: 2 })) })
    const payload = { message_id: 'msg-3', to_run_id: 'run_1', acked_at: '2026-10-05T10:01:00Z' }
    expect(await applyEvent(store, messageEvent(8, payload, 'coord.message.acked'), client)).toBe(true)
    await vi.waitFor(() => expect(store.getState().runs.run_1.unacked_messages).toBe(2))
    expect(store.getState().runs.run_1.title).toBe('renamed after the read')
  })

  it('drops a failed recipient count read without marking the server unreachable', async () => {
    const store = createRootStore()
    await hydrate(store, fakeApi())
    const runGet = vi.fn(async () => {
      throw new ApiError(503, 'server unreachable: connection refused')
    })
    const client = fakeApi({ runGet })
    const payload = { message_id: 'msg-3', to_run_id: 'run_1', acked_at: '2026-10-05T10:01:00Z' }
    expect(await applyEvent(store, messageEvent(8, payload, 'coord.message.acked'), client)).toBe(true)
    await vi.waitFor(() => expect(runGet).toHaveBeenCalled())
    await Promise.resolve()
    expect(store.getState().unreachable).toBeNull()
    expect(store.getState().lastSeq).toBe(8)
  })

  it('applies the event when the recipient run is already deleted', async () => {
    const store = createRootStore()
    await hydrate(store, fakeApi())
    const client = fakeApi({
      runGet: vi.fn(async () => {
        throw new ApiError(404, 'run.get: run run_1 not found')
      }),
    })
    const payload = { message_id: 'msg-3', to_run_id: 'run_1', acked_at: '2026-10-05T10:01:00Z' }
    expect(await applyEvent(store, messageEvent(8, payload, 'coord.message.acked'), client)).toBe(true)
    expect(store.getState().lastSeq).toBe(8)
  })
})
