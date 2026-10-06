import type { Event, Run } from '@/lib/types'
import { createRootStore } from '@/store'
import { batchNotifications } from '@/store/batch'
import { connect } from '@/store/sync'
import { fakeApi, run, workspace } from '@/test/fixtures'
import { StubSocket } from '@/test/stub-socket'

function titleEvent(seq: number): Event {
  return {
    id: `evt_${seq}`,
    seq,
    time: '2026-08-14T11:00:00Z',
    workspace_id: workspace.id,
    run_id: 'run_1',
    actor_id: '',
    type: 'run.title',
    payload: { title: `title ${seq}` },
  }
}

describe('batchNotifications', () => {
  it('keeps getState current while listeners hear one change at the end', async () => {
    const store = createRootStore()
    const heard: [string, string][] = []
    store.subscribe((state, previous) => heard.push([previous.activeWorkspace, state.activeWorkspace]))
    await batchNotifications(store, async () => {
      store.getState().setActiveWorkspace('a')
      expect(store.getState().activeWorkspace).toBe('a')
      await Promise.resolve()
      store.getState().setActiveWorkspace('b')
      expect(heard).toEqual([])
    })
    expect(heard).toEqual([['', 'b']])
  })

  it('lets a batch that outlasts a frame notify without waiting for the end', async () => {
    const store = createRootStore()
    let heard = 0
    store.subscribe(() => heard++)
    const slow = Promise.withResolvers<void>()
    const batch = batchNotifications(store, async () => {
      store.getState().setActiveWorkspace('a')
      await slow.promise
      store.getState().setActiveWorkspace('b')
    })
    await vi.waitFor(() => expect(heard).toBe(1))
    expect(store.getState().activeWorkspace).toBe('a')
    slow.resolve()
    await batch
    expect(heard).toBe(2)
  })
})

describe('drain', () => {
  beforeEach(() => StubSocket.install())
  afterEach(() => vi.unstubAllGlobals())

  it('applies a burst in order and notifies once, even across a fetch', async () => {
    const store = createRootStore()
    let cursorAtFetch = -1
    const runGet = vi.fn(async (): Promise<Run> => {
      cursorAtFetch = store.getState().lastSeq
      return run({ id: 'run_new', status: 'queued' })
    })
    const stop = connect(store, fakeApi({ runGet }))
    try {
      await vi.waitFor(() => expect(StubSocket.opened.length).toBeGreaterThan(0))
      const socket = StubSocket.last()
      socket.onopen?.()
      socket.onmessage?.({ data: JSON.stringify({ ok: true }) })
      await vi.waitFor(() => expect(store.getState().hydrated).toBe(true))

      let heard = 0
      store.subscribe(() => heard++)
      for (let seq = 1; seq <= 50; seq++) {
        const ev =
          seq === 25
            ? { ...titleEvent(seq), run_id: 'run_new', type: 'run.status', payload: { to: 'running' } }
            : titleEvent(seq)
        socket.onmessage?.({ data: JSON.stringify(ev) })
      }
      await vi.waitFor(() => expect(store.getState().lastSeq).toBe(50))

      expect(heard).toBe(1)
      expect(cursorAtFetch).toBe(24)
      expect(store.getState().runs.run_1.title).toBe('title 50')
      expect(store.getState().runs.run_new.status).toBe('running')
    } finally {
      stop()
    }
  })
})
