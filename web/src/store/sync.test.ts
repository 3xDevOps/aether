import type { Event, Run } from '@/lib/types'
import { board } from '@/routes/board/selectors'
import { createRootStore } from '@/store'
import { capability } from '@/store/hooks'
import { applyEvent, connect, hydrate } from '@/store/sync'
import {
  alice,
  bob,
  fakeApi,
  run,
  serverInfo as serverInfoFixture,
  session,
} from '@/test/fixtures'
import { StubSocket } from '@/test/stub-socket'

function statusEvent(over: Partial<Event> = {}): Event {
  return {
    id: 'evt_1',
    seq: 5,
    time: '2026-08-14T11:00:00Z',
    session_id: session.id,
    run_id: 'run_1',
    actor_id: '',
    type: 'run.status',
    payload: { from: 'running', to: 'needs-attention', reason: 'plan review' },
    ...over,
  }
}

describe('hydrate', () => {
  it('fills the store from one round of fetches', async () => {
    const store = createRootStore()
    await hydrate(store, fakeApi())

    const s = store.getState()
    expect(s.hydrated).toBe(true)
    expect(s.info?.server_version).toBe('1.2.3')
    expect(Object.keys(s.sessions)).toHaveLength(2)
    expect(Object.keys(s.members)).toHaveLength(2)
    expect(s.runs.run_1.status).toBe('running')
  })

  it('records the failure instead of throwing', async () => {
    const store = createRootStore()
    await hydrate(
      store,
      fakeApi({
        serverInfo: vi.fn(async () => {
          throw new Error('502 Bad Gateway')
        }),
      }),
    )

    expect(store.getState().hydrated).toBe(false)
    expect(store.getState().hydrationError).toContain('502')
  })

  it('seeds pausedRuns from the run list, so paused survives a reload', async () => {
    const store = createRootStore()
    await hydrate(
      store,
      fakeApi({
        runList: vi.fn(async () => [
          run({ id: 'run_1', paused: true }),
          run({ id: 'run_2', paused: false }),
          run({ id: 'run_3' }), // a legacy gateway sends no paused field
        ]),
      }),
    )

    // No timeline event has arrived, yet the snapshot already knows.
    const s = store.getState()
    expect(s.pausedRuns).toEqual({ run_1: true, run_2: false })
    expect(s.pausedRuns.run_3).toBeUndefined()

    // And the board card carries the badge straight from the snapshot.
    const { columns } = board({
      sessions: s.sessions,
      runs: s.runs,
      members: s.members,
      acked: s.acked,
      pausedRuns: s.pausedRuns,
      pending: new Set<string>(),
    })
    const working = columns.find((c) => c.key === 'working')
    expect(working?.cards.find((c) => c.run.id === 'run_1')?.paused).toBe(true)
    expect(working?.cards.find((c) => c.run.id === 'run_2')?.paused).toBe(false)
  })

  it('replaces the paused map wholesale on re-hydration', async () => {
    const store = createRootStore()
    store.getState().setPaused('run_gone', true)
    await hydrate(
      store,
      fakeApi({ runList: vi.fn(async () => [run({ id: 'run_1', paused: true })]) }),
    )

    expect(store.getState().pausedRuns).toEqual({ run_1: true })
  })

  it('keeps the wire reason across a refetch that changed the status', async () => {
    const store = createRootStore()
    await hydrate(store, fakeApi())
    expect(store.getState().runs.run_1.reason).toBeUndefined()

    await hydrate(
      store,
      fakeApi({
        runList: vi.fn(async () => [
          run({ status: 'needs-attention', reason: 'plan review' }),
        ]),
      }),
    )

    expect(store.getState().runs.run_1.status).toBe('needs-attention')
    expect(store.getState().runs.run_1.reason).toBe('plan review')
  })

  it('stores the gateway capabilities', async () => {
    const store = createRootStore()
    await hydrate(store, fakeApi())

    const caps = store.getState().capabilities
    expect(caps?.gateway).toBe('remote')
    const c = capability(caps)
    expect(c.hasMethod('run.pause')).toBe(true) // "*" covers everything
    expect(c.hasWS('events')).toBe(true)
    expect(c.hasLocal('worktree.open')).toBe(false)
  })

  it('treats a missing capabilities endpoint as the legacy remote monitor', async () => {
    const store = createRootStore()
    await hydrate(
      store,
      fakeApi({
        capabilities: vi.fn(async () => {
          throw new Error('404 Not Found')
        }),
      }),
    )

    expect(store.getState().hydrated).toBe(true)
    expect(store.getState().capabilities).toBeNull()
    const c = capability(store.getState().capabilities)
    expect(c.hasMethod('run.list')).toBe(true)
    expect(c.hasWS('attach')).toBe(true)
    expect(c.hasLocal('worktree.open')).toBe(false)
  })
})


describe('applyEvent', () => {
  it('moves a run to its new state and remembers the reason', async () => {
    const store = createRootStore()
    await hydrate(store, fakeApi())

    await applyEvent(store, statusEvent(), fakeApi())

    const record = store.getState().runs.run_1
    expect(record.status).toBe('needs-attention')
    expect(record.reason).toBe('plan review')
    expect(record.stateChangedAt).toBe('2026-08-14T11:00:00Z')
    expect(store.getState().lastSeq).toBe(5)
  })

  it('stamps finished_at on a terminal transition', async () => {
    const store = createRootStore()
    await hydrate(store, fakeApi())

    await applyEvent(store, statusEvent({ payload: { to: 'merged' } }), fakeApi())

    expect(store.getState().runs.run_1.finished_at).toBe('2026-08-14T11:00:00Z')
  })

  it('ignores an event already applied, so replay is idempotent', async () => {
    const store = createRootStore()
    await hydrate(store, fakeApi())

    await applyEvent(store, statusEvent({ seq: 9 }), fakeApi())
    await applyEvent(
      store,
      statusEvent({ seq: 9, payload: { to: 'failed' } }),
      fakeApi(),
    )

    expect(store.getState().runs.run_1.status).toBe('needs-attention')
    expect(store.getState().lastSeq).toBe(9)
  })

  it('fetches a run it has never seen before applying its status', async () => {
    const store = createRootStore()
    await hydrate(store, fakeApi({ runList: vi.fn(async () => []) }))
    const client = fakeApi({ runGet: vi.fn(async () => run({ id: 'run_9' })) })

    expect(await applyEvent(store, statusEvent({ run_id: 'run_9' }), client)).toBe(
      true,
    )

    expect(client.runGet).toHaveBeenCalledWith('run_9')
    expect(store.getState().runs.run_9).toBeDefined()
    expect(store.getState().lastSeq).toBe(5)
  })

  it('reports the event unresolved when that fetch fails, cursor untouched', async () => {
    const store = createRootStore()
    await hydrate(store, fakeApi({ runList: vi.fn(async () => []) }))
    const client = fakeApi({
      runGet: vi.fn(async () => {
        throw new Error('502 Bad Gateway')
      }),
    })

    expect(await applyEvent(store, statusEvent({ run_id: 'run_9' }), client)).toBe(
      false,
    )

    expect(store.getState().lastSeq).toBe(0)
  })

  it('re-reads a run when it is handed off, so ownership follows', async () => {
    const store = createRootStore()
    await hydrate(store, fakeApi())
    expect(store.getState().runs.run_1.member_id).toBe(alice.id)
    const client = fakeApi({
      runGet: vi.fn(async () => run({ member_id: bob.id })),
    })

    // A handoff publishes only a timeline entry, no run.status event.
    await applyEvent(
      store,
      statusEvent({
        type: 'session.timeline',
        payload: { kind: 'handoff', message: bob.id },
      }),
      client,
    )

    expect(client.runGet).toHaveBeenCalledWith('run_1')
    expect(store.getState().runs.run_1.member_id).toBe(bob.id)
    expect(store.getState().lastSeq).toBe(5)
  })

  it('resets the cursor and asks for a fresh snapshot when the log restarts', async () => {
    const store = createRootStore()
    await hydrate(store, fakeApi())
    await applyEvent(store, statusEvent({ seq: 9 }), fakeApi())

    // A sequence below the cursor means the server's event log restarted
    // (fresh or restored data dir); replaying our cursor would drop every
    // event forever.
    expect(await applyEvent(store, statusEvent({ seq: 3 }), fakeApi())).toBe(false)

    expect(store.getState().lastSeq).toBe(0)
  })

  it('re-fetches the member list when an event names an actor it does not know', async () => {
    const store = createRootStore()
    await hydrate(store, fakeApi({ memberList: vi.fn(async () => [alice]) }))
    expect(store.getState().members[bob.id]).toBeUndefined()

    await applyEvent(store, statusEvent({ actor_id: bob.id }), fakeApi())

    expect(store.getState().members[bob.id]).toBeDefined()
  })

  it('re-fetches the session list when an event names one it does not know', async () => {
    const store = createRootStore()
    const sessionList = vi
      .fn()
      .mockResolvedValueOnce([])
      .mockResolvedValue([session])
    const client = fakeApi({ sessionList })
    await hydrate(store, client)
    expect(store.getState().sessions[session.id]).toBeUndefined()

    await applyEvent(store, statusEvent(), client)

    expect(store.getState().sessions[session.id]).toBeDefined()
  })
})

describe('connect', () => {
  beforeEach(() => {
    StubSocket.install()
  })
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  /** Opens the newest socket and acknowledges the subscription on it. */
  async function subscribe() {
    await vi.waitFor(() => expect(StubSocket.opened.length).toBeGreaterThan(0))
    const socket = StubSocket.last()
    socket.onopen?.()
    socket.onmessage?.({ data: JSON.stringify({ ok: true }) })
    return socket
  }

  function deliver(socket: StubSocket, ev: Event) {
    socket.onmessage?.({ data: JSON.stringify(ev) })
  }

  it('waits for the subscription acknowledgement before it hydrates', async () => {
    const client = fakeApi()
    const store = createRootStore()
    const stop = connect(store, client)

    await vi.waitFor(() => expect(StubSocket.opened).toHaveLength(1))
    StubSocket.last().onopen?.()
    // An open socket is not a subscription: anything happening now would be
    // missed by a snapshot taken here.
    expect(client.serverInfo).not.toHaveBeenCalled()

    StubSocket.last().onmessage?.({ data: JSON.stringify({ ok: true }) })

    await vi.waitFor(() => expect(store.getState().hydrated).toBe(true))
    stop()
  })

  it('holds events that land mid-hydration and applies them after the snapshot', async () => {
    let resolveRuns: (runs: Run[]) => void = () => {}
    const client = fakeApi({
      runList: vi.fn(
        () =>
          new Promise<Run[]>((resolve) => {
            resolveRuns = resolve
          }),
      ),
    })
    const store = createRootStore()
    const stop = connect(store, client)

    const socket = await subscribe()
    await vi.waitFor(() => expect(client.runList).toHaveBeenCalled())
    deliver(socket, statusEvent({ seq: 7, payload: { to: 'failed' } }))

    resolveRuns([run()]) // the older snapshot still says running

    await vi.waitFor(() =>
      expect(store.getState().runs.run_1?.status).toBe('failed'),
    )
    expect(store.getState().lastSeq).toBe(7)
    stop()
  })

  it('never advances the cursor past an event still waiting on a fetch', async () => {
    const pending: ((r: Run) => void)[] = []
    const client = fakeApi({
      runList: vi.fn(async () => []),
      runGet: vi.fn(
        () =>
          new Promise<Run>((resolve) => {
            pending.push(resolve)
          }),
      ),
    })
    const store = createRootStore()
    const stop = connect(store, client)

    const socket = await subscribe()
    await vi.waitFor(() => expect(store.getState().hydrated).toBe(true))

    // A brand new run: two transitions and then a later, unrelated event.
    deliver(socket, statusEvent({ seq: 10, run_id: 'run_9', payload: { to: 'provisioning' } }))
    deliver(socket, statusEvent({ seq: 11, run_id: 'run_9', payload: { to: 'running' } }))
    deliver(socket, {
      ...statusEvent({ seq: 12, run_id: 'run_9' }),
      type: 'run.diff',
      payload: { files: [] },
    })

    await vi.waitFor(() => expect(client.runGet).toHaveBeenCalledTimes(1))
    // One fetch outstanding, so nothing behind it may move the cursor.
    expect(store.getState().lastSeq).toBe(0)

    pending[0](run({ id: 'run_9', status: 'running' }))

    await vi.waitFor(() => expect(store.getState().lastSeq).toBe(12))
    // The later transition won, and the fetch ran once for both of them.
    expect(store.getState().runs.run_9.status).toBe('running')
    expect(client.runGet).toHaveBeenCalledTimes(1)
    stop()
  })

  it('re-fetches on a reconnect that has no cursor to replay from', async () => {
    const client = fakeApi()
    const store = createRootStore()
    const stop = connect(store, client)

    await subscribe()
    await vi.waitFor(() => expect(store.getState().hydrated).toBe(true))
    StubSocket.last().onclose?.({ code: 1006 })

    await vi.waitFor(() => expect(StubSocket.opened).toHaveLength(2), {
      timeout: 2000,
    })
    await subscribe()

    await vi.waitFor(() => expect(client.serverInfo).toHaveBeenCalledTimes(2))
    stop()
  })

  it('stops on a dead-token close and says how to recover', async () => {
    const client = fakeApi()
    const store = createRootStore()
    const stop = connect(store, client)

    await subscribe()
    await vi.waitFor(() => expect(store.getState().hydrated).toBe(true))

    // The gateway's token watch names the dead token in its close reason, and
    // every reconnect would carry the same dead token.
    StubSocket.last().onclose?.({
      code: 1008,
      reason: 'dashboard token revoked or expired',
    })

    expect(store.getState().connection).toBe('offline')
    expect(store.getState().hydrationError).toContain('aether dash')
    // The panes key on this to say "dead token" rather than "retrying".
    expect(store.getState().streamDead).toBe(true)
    await new Promise((resolve) => setTimeout(resolve, 700))
    expect(StubSocket.opened).toHaveLength(1)
    stop()
  })

  it('retries a 1008 close that is not the token watch', async () => {
    const client = fakeApi()
    const store = createRootStore()
    const stop = connect(store, client)

    await subscribe()
    await vi.waitFor(() => expect(store.getState().hydrated).toBe(true))

    // The gateway also closes 1008 for a refused subscribe or a transient
    // membership check failure; the next reconnect can outlive those.
    StubSocket.last().onclose?.({ code: 1008, reason: 'subscribe refused' })

    await vi.waitFor(() => expect(StubSocket.opened.length).toBeGreaterThan(1))
    expect(store.getState().hydrationError).toBeNull()
    stop()
  })

  it('retries a hydration the server refused', async () => {
    const serverInfo = vi
      .fn()
      .mockRejectedValueOnce(new Error('502 Bad Gateway'))
      .mockResolvedValue(serverInfoFixture)
    const store = createRootStore()
    const stop = connect(store, fakeApi({ serverInfo }))

    await subscribe()
    await vi.waitFor(() =>
      expect(store.getState().hydrationError).toContain('502'),
    )
    await vi.waitFor(() => expect(store.getState().hydrated).toBe(true), {
      timeout: 3000,
    })
    expect(store.getState().hydrationError).toBeNull()
    stop()
  })
})
