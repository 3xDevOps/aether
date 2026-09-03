import { ApiError } from '@/lib/api'
import type { Event, Run } from '@/lib/types'
import { board } from '@/routes/board/selectors'
import { createRootStore } from '@/store'
import { capability } from '@/store/hooks'
import { applyEvent, connect, hydrate } from '@/store/sync'
import {
  alice,
  bob,
  fakeApi,
  otherWorkspace,
  run,
  serverInfo as serverInfoFixture,
  workspace,
} from '@/test/fixtures'
import { StubSocket } from '@/test/stub-socket'

function statusEvent(over: Partial<Event> = {}): Event {
  return {
    id: 'evt_1',
    seq: 5,
    time: '2026-08-14T11:00:00Z',
    workspace_id: workspace.id,
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
    expect(Object.keys(s.workspaces)).toHaveLength(2)
    expect(Object.keys(s.members)).toHaveLength(2)
    expect(s.runs.run_1.status).toBe('running')
  })

  it('points the app at a workspace, keeping one the member already chose', async () => {
    // Nothing chosen: the lowest id wins, so two tabs hydrating off the same
    // list land on the same scope.
    const fresh = createRootStore()
    await hydrate(fresh, fakeApi())
    expect(fresh.getState().activeWorkspace).toBe(workspace.id)

    // A choice that still exists is never overridden by a re-hydration.
    const chosen = createRootStore()
    chosen.getState().setActiveWorkspace(otherWorkspace.id)
    await hydrate(chosen, fakeApi())
    expect(chosen.getState().activeWorkspace).toBe(otherWorkspace.id)
  })

  it('re-points a scope whose workspace is gone, so no surface is left blank', async () => {
    const store = createRootStore()
    store.getState().setActiveWorkspace('wsp_deleted')

    await hydrate(
      store,
      fakeApi({ workspaceListFull: vi.fn(async () => [otherWorkspace]) }),
    )

    expect(store.getState().activeWorkspace).toBe(otherWorkspace.id)
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
      workspace: s.activeWorkspace,
      workspaces: s.workspaces,
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
  it('routes to onboarding only for an unboarded local gateway', async () => {
    const cases = [
      { onboarded: false, local: true, onboarding: true },
      { onboarded: true, local: true, onboarding: false },
      { onboarded: false, local: false, onboarding: false },
    ]

    for (const tc of cases) {
      const store = createRootStore()
      store.setState({ onboarded: tc.onboarded, route: { name: 'board', params: {} } })
      const client = fakeApi({
        capabilities: vi.fn(async () => ({
          gateway: tc.local ? 'local' : 'remote',
          methods: ['*'],
          ws: ['events', 'attach'],
          ...(tc.local ? { local: ['link.status'] } : {}),
        })),
      })

      await hydrate(store, client)

      expect(store.getState().route.name).toBe(tc.onboarding ? 'onboarding' : 'board')
    }
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
    // The pre-capabilities allowlist: monitoring and steering verbs pass...
    expect(c.hasMethod('run.list')).toBe(true)
    expect(c.hasMethod('template.launch')).toBe(true)
    // ...but the admin methods a legacy gateway would 403 do not.
    expect(c.hasMethod('member.approve')).toBe(false)
    expect(c.hasMethod('workspace.add')).toBe(false)
    expect(c.hasMethod('template.save')).toBe(false)
    expect(c.hasMethod('agent.list')).toBe(false)
    expect(c.hasWS('attach')).toBe(true)
    expect(c.hasWS('shell')).toBe(false)
    expect(c.hasLocal('worktree.open')).toBe(false)
  })

  it('classifies a fetch that never got an answer as a dead gateway', async () => {
    const store = createRootStore()
    await hydrate(
      store,
      fakeApi({
        serverInfo: vi.fn(async () => {
          // What fetch throws when the origin itself is gone.
          throw new TypeError('Failed to fetch')
        }),
      }),
    )

    expect(store.getState().unreachable).toBe('gateway')
    expect(store.getState().hydrationError).toContain('Failed to fetch')
  })

  it('classifies the gateway naming its SSH backend as a dead server', async () => {
    const store = createRootStore()
    await hydrate(
      store,
      fakeApi({
        serverInfo: vi.fn(async () => {
          throw new ApiError(
            503,
            'server.info: server unreachable: dial tcp 10.0.0.5:22: connect: connection refused',
          )
        }),
      }),
    )

    expect(store.getState().unreachable).toBe('server')
  })

  it('classifies a dead local network as neither the gateway nor the server', async () => {
    const store = createRootStore()
    await hydrate(
      store,
      fakeApi({
        serverInfo: vi.fn(async () => {
          // The gateway dialed and the machine's own network stack refused
          // to try: no route, or DNS is dead. The server is not implicated.
          throw new ApiError(
            503,
            'server.info: network unreachable: dial tcp: lookup aether.example: no such host',
          )
        }),
      }),
    )

    expect(store.getState().unreachable).toBe('network')
  })

  it('clears unreachable when a re-hydration succeeds', async () => {
    const store = createRootStore()
    await hydrate(
      store,
      fakeApi({
        serverInfo: vi.fn(async () => {
          throw new TypeError('Failed to fetch')
        }),
      }),
    )
    expect(store.getState().unreachable).toBe('gateway')

    await hydrate(store, fakeApi())

    expect(store.getState().unreachable).toBeNull()
    expect(store.getState().hydrated).toBe(true)
  })

  it('leaves unreachable null on a failure that is neither hop', async () => {
    const store = createRootStore()
    await hydrate(
      store,
      fakeApi({
        serverInfo: vi.fn(async () => {
          throw new ApiError(500, 'server.info: internal error')
        }),
      }),
    )

    expect(store.getState().unreachable).toBeNull()
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
        type: 'workspace.timeline',
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

  it('re-fetches the workspace list when an event names one it does not know', async () => {
    const store = createRootStore()
    const workspaceListFull = vi
      .fn()
      .mockResolvedValueOnce([])
      .mockResolvedValue([workspace])
    const client = fakeApi({ workspaceListFull })
    await hydrate(store, client)
    expect(store.getState().workspaces[workspace.id]).toBeUndefined()

    await applyEvent(store, statusEvent(), client)

    expect(store.getState().workspaces[workspace.id]).toBeDefined()
  })

  it('follows a server update and never moves it backwards', async () => {
    const store = createRootStore()
    await hydrate(store, fakeApi())
    const updateEvent = (over: Partial<Event> = {}): Event => ({
      id: 'evt_srv',
      seq: 6,
      time: '2026-08-14T11:00:00Z',
      workspace_id: workspace.id,
      run_id: '',
      actor_id: alice.id,
      type: 'server.update',
      payload: { phase: 'applying', version: 'v1.3.0', actor_id: alice.id },
      ...over,
    })

    expect(await applyEvent(store, updateEvent(), fakeApi())).toBe(true)
    expect(store.getState().serverUpdateProgress).toMatchObject({
      phase: 'applying',
      version: 'v1.3.0',
    })

    await applyEvent(
      store,
      updateEvent({
        seq: 7,
        payload: { phase: 'restarting', version: 'v1.3.0' },
      }),
      fakeApi(),
    )
    expect(store.getState().serverUpdateProgress?.phase).toBe('restarting')

    // The same phase is published once per workspace, and the RPC result
    // races the first of them: a late "applying" must not undo the frame
    // that says the server is already on its way down.
    await applyEvent(
      store,
      updateEvent({ seq: 8, payload: { phase: 'applying', version: 'v1.3.0' } }),
      fakeApi(),
    )
    expect(store.getState().serverUpdateProgress?.phase).toBe('restarting')

    // A failure always wins: it is the end of that update.
    await applyEvent(
      store,
      updateEvent({
        seq: 9,
        payload: { phase: 'failed', version: 'v1.3.0', detail: 'checksum mismatch' },
      }),
      fakeApi(),
    )
    expect(store.getState().serverUpdateProgress).toMatchObject({
      phase: 'failed',
      detail: 'checksum mismatch',
    })
  })

  it('drives the environment build slice and ignores frames for older versions', async () => {
    const store = createRootStore()
    await hydrate(store, fakeApi())
    const buildEvent = (over: Partial<Event> = {}): Event => ({
      id: 'evt_env',
      seq: 6,
      time: '2026-08-14T11:00:00Z',
      workspace_id: workspace.id,
      run_id: '',
      actor_id: '',
      type: 'environment.build',
      payload: { version: 2, status: 'building', line: 'step 1/4' },
      ...over,
    })

    expect(await applyEvent(store, buildEvent(), fakeApi())).toBe(true)
    expect(store.getState().envBuilds[workspace.id]).toMatchObject({
      version: 2,
      status: 'building',
    })

    // A frame about an older version is stale and changes nothing.
    await applyEvent(
      store,
      buildEvent({ seq: 7, payload: { version: 1, status: 'active' } }),
      fakeApi(),
    )
    expect(store.getState().envBuilds[workspace.id]).toMatchObject({
      version: 2,
      status: 'building',
    })

    // The failure detail lands with the failed status.
    await applyEvent(
      store,
      buildEvent({
        seq: 8,
        payload: { version: 2, status: 'failed', detail: 'jq missing' },
      }),
      fakeApi(),
    )
    expect(store.getState().envBuilds[workspace.id]).toMatchObject({
      version: 2,
      status: 'failed',
      detail: 'jq missing',
    })
  })

  it('routes environment.edit events into the edit slice', async () => {
    const store = createRootStore()
    await hydrate(store, fakeApi())
    const editEvent = (over: Partial<Event> = {}): Event => ({
      id: 'evt_edit',
      seq: 6,
      time: '2026-08-14T11:00:00Z',
      workspace_id: workspace.id,
      run_id: '',
      actor_id: '',
      type: 'environment.edit',
      payload: { harness: 'claude', status: 'running', line: 'inspecting' },
      ...over,
    })

    expect(await applyEvent(store, editEvent(), fakeApi())).toBe(true)
    expect(store.getState().envEdits[workspace.id]).toMatchObject({
      harness: 'claude',
      status: 'running',
      lines: ['inspecting'],
    })

    await applyEvent(
      store,
      editEvent({
        seq: 7,
        payload: { harness: 'claude', status: 'proposed', version: 2 },
      }),
      fakeApi(),
    )
    expect(store.getState().envEdits[workspace.id]).toMatchObject({
      status: 'proposed',
      version: 2,
    })
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
    expect(store.getState().hydrationError).toContain('aether gui')
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

  it('marks the server hop dead on a -32004 subscribe refusal', async () => {
    const client = fakeApi()
    const store = createRootStore()
    const stop = connect(store, client)

    await vi.waitFor(() => expect(StubSocket.opened).toHaveLength(1))
    const socket = StubSocket.last()
    socket.onopen?.()
    // The local gateway refuses the subscribe when its SSH backend cannot
    // reach aether-server, naming the hop in the frame before it closes.
    socket.onmessage?.({
      data: JSON.stringify({
        ok: false,
        code: -32004,
        error: 'server unreachable: dial tcp 10.0.0.5:22: connect: connection refused',
      }),
    })

    expect(store.getState().unreachable).toBe('server')
    stop()
  })

  it('marks the local network dead on a network-unreachable subscribe refusal', async () => {
    const client = fakeApi()
    const store = createRootStore()
    const stop = connect(store, client)

    await vi.waitFor(() => expect(StubSocket.opened).toHaveLength(1))
    const socket = StubSocket.last()
    socket.onopen?.()
    // Same refusal frame, different hop: the gateway never got off this
    // machine, so the fix is the user's own connection.
    socket.onmessage?.({
      data: JSON.stringify({
        ok: false,
        code: -32004,
        error: 'network unreachable: dial tcp: lookup aether.example: no such host',
      }),
    })

    expect(store.getState().unreachable).toBe('network')
    stop()
  })

  it('records the refusal detail, which is the only account of what failed', async () => {
    const client = fakeApi()
    const store = createRootStore()
    const stop = connect(store, client)

    await vi.waitFor(() => expect(StubSocket.opened).toHaveLength(1))
    const socket = StubSocket.last()
    socket.onopen?.()
    // The stream never goes live, so hydration never runs: this frame is
    // the only description of the failure the client will ever get.
    socket.onmessage?.({
      data: JSON.stringify({
        ok: false,
        code: -32004,
        error: 'server unreachable: cli: dial 10.0.0.5:22: connect: connection refused',
      }),
    })

    expect(store.getState().hydrationError).toContain('connection refused')
    expect(store.getState().hydrated).toBe(false)
    stop()
  })
})
