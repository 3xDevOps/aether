import type { SessionItem } from '@/lib/session-types'
import { createRootStore } from '@/store'
import { keptItems } from '@/store/sessions'
import { loadOlderItems } from '@/store/session-stream'
import { item, resetItems, say, state } from '@/test/acp-stream'
import { fakeApi } from '@/test/fixtures'

const frames = (...items: SessionItem[]) => items.map((it) => ({ seq: it.seq, item: it }))

beforeEach(resetItems)

describe('enhanced sessions in the store', () => {
  it('applies the ack and frames, skips duplicates and keeps requests and options current', () => {
    const store = createRootStore()
    const request = { id: 'q', kind: 'permission' as const, title: 'go test', status: 'pending' as const }
    store.getState().acpAck('run_1', { ok: true, seq: 0, replay: 0, epoch: 0, live: true, has_control: false, state: state() })
    const opened = [item('turn_start', 1), item('request', 1, { request })]
    store.getState().acpFrames('run_1', frames(...opened))
    store.getState().acpFrames('run_1', frames(opened[1]!, item('config_options', 1, { config_options: [{ id: 'mode', name: 'Mode', type: 'select', currentValue: 'plan' }] })))
    let session = store.getState().acpSessions.run_1!
    expect(session.seq).toBe(3)
    expect(session.turns[0]!.items.map((it) => it.seq)).toEqual([1, 2, 3])
    expect(session.pending).toEqual([request])
    expect(session.state).toMatchObject({ turn_in_flight: true, config_options: [{ currentValue: 'plan' }] })

    store.getState().acpFrames('run_1', frames(item('request', 1, { request: { ...request, status: 'answered', answer: 'ok' } })))
    expect(store.getState().acpSessions.run_1!.pending).toEqual([])

    store.getState().acpFrames('run_1', [{ reset: true, epoch: 1 }, ...frames(item('turn_start', 1))])
    session = store.getState().acpSessions.run_1!
    expect(session.epoch).toBe(1)
    expect(session.turns.flatMap((t) => t.items.map((it) => it.seq))).toEqual([5])
  })

  it('starts over when the replay window moved past the held cursor', () => {
    const store = createRootStore()
    store.getState().acpFrames('run_1', frames(item('turn_start', 1)))
    store.getState().acpAck('run_1', { ok: true, seq: 400, replay: 200, oldest_seq: 201, epoch: 0, live: true, has_control: false })
    const session = store.getState().acpSessions.run_1!
    expect(session.turns).toEqual([])
    expect(session.more).toBe(true)
  })

  it('pages older items with run.acp.history before the oldest held', async () => {
    const store = createRootStore()
    store.getState().acpAck('run_1', { ok: true, seq: 0, replay: 0, oldest_seq: 3, epoch: 0, live: true, has_control: false })
    const [a, b, c] = [item('turn_start', 1), say(1, 'user', 'hi'), say(1, 'assistant', 'yo')]
    store.getState().acpFrames('run_1', frames(c!))
    const runACPHistory = vi.fn().mockResolvedValue(frames(a!, b!))
    await loadOlderItems(store, fakeApi({ runACPHistory }), 'run_1')
    expect(runACPHistory).toHaveBeenCalledWith('run_1', 3, 200)
    const session = store.getState().acpSessions.run_1!
    expect(session.turns[0]!.items.map((it) => it.seq)).toEqual([1, 2, 3])
    expect(session.more).toBe(false)
  })

  it('keeps three sessions whole and trims the least recently used to their newest items', () => {
    const store = createRootStore()
    for (const runID of ['a', 'b', 'c', 'd']) {
      store.getState().acpFrames(runID, frames(...Array.from({ length: keptItems + 50 }, () => item('usage', 1))))
      store.getState().touchAcpSession(runID, () => false)
    }
    const sizes = Object.fromEntries(Object.entries(store.getState().acpSessions).map(([id, s]) => [id, s.turns[0]!.items.length]))
    expect(sizes).toEqual({ a: keptItems, b: keptItems + 50, c: keptItems + 50, d: keptItems + 50 })
    expect(store.getState().acpSessions.a!.more).toBe(true)
  })

  it('coalesces a burst of frames into one notification', async () => {
    const store = createRootStore()
    const { batchNotifications } = await import('@/store/batch')
    const listener = vi.fn()
    store.subscribe(listener)
    await batchNotifications(store, async () => {
      for (let i = 0; i < 20; i++) store.getState().acpFrames('run_1', frames(item('usage', 1)))
    })
    await new Promise((resolve) => setTimeout(resolve, 40))
    expect(listener).toHaveBeenCalledTimes(1)
    expect(store.getState().acpSessions.run_1!.seq).toBe(20)
  })
})
