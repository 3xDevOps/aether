import { ApiError } from '@/lib/api'
import type { Event, Run } from '@/lib/types'
import { board } from '@/routes/board/selectors'
import { createRootStore } from '@/store'
import { configKey } from '@/store/files'
import { capability } from '@/store/hooks'
import { applyEvent, connect, hydrate } from '@/store/sync'
import {
  alice,
  bob,
  evidencePacket,
  fakeApi,
  otherWorkspace,
  roomMessage,
  run,
  serverInfo as serverInfoFixture,
  workspace,
} from '@/test/fixtures'
import { StubSocket } from '@/test/stub-socket'
import { fire } from '@/test/wake'

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

function titleEvent(over: Partial<Event> = {}): Event {
  return {
    id: 'evt_title',
    seq: 6,
    time: '2026-08-14T11:01:00Z',
    workspace_id: workspace.id,
    run_id: 'run_1',
    actor_id: '',
    type: 'run.title',
    payload: { title: 'Fixing the login bug' },
    ...over,
  }
}

function protectedEvent(over: Partial<Event> = {}): Event {
  return {
    id: 'evt_protected',
    seq: 7,
    time: '2026-08-14T11:02:00Z',
    workspace_id: workspace.id,
    run_id: 'run_1',
    actor_id: '',
    type: 'run.protected',
    payload: { protected: true },
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
  it('hydrates server-computed unanswered question counts with the run snapshot', async () => {
    const store = createRootStore()
    await hydrate(
      store,
      fakeApi({
        runList: vi.fn(async () => [
          run({ id: 'run_1', status: 'running', unanswered_questions: 2 }),
        ]),
      }),
    )

    expect(store.getState().runs.run_1.unanswered_questions).toBe(2)
  })


  // Both shells turn `aether://run/<id>` into `<dashboard>?run=<id>`, so the
  // query is the whole deep-link contract on the dashboard's side.
  describe('a ?run= deep link', () => {
    afterEach(() => {
      window.history.replaceState({}, '', '/')
    })

    it('opens that run and leaves the address bar clean', async () => {
      window.history.replaceState({}, '', '/?run=run_1')
      const store = createRootStore()
      await hydrate(store, fakeApi())

      expect(store.getState().route).toEqual({
        name: 'terminal',
        params: { runId: 'run_1' },
      })
      // Stripped, so a reload does not reopen a run the member left.
      expect(window.location.search).toBe('')
    })

    it('stays on the board for a run the member cannot see', async () => {
      window.history.replaceState({}, '', '/?run=run_someone_elses')
      const store = createRootStore()
      await hydrate(store, fakeApi())

      expect(store.getState().route).toEqual({ name: 'board', params: {} })
      expect(window.location.search).toBe('')
    })

    it('does not reopen the run when a reconnect re-hydrates', async () => {
      window.history.replaceState({}, '', '/?run=run_1')
      const store = createRootStore()
      await hydrate(store, fakeApi())
      store.getState().navigate('board')

      await hydrate(store, fakeApi())

      expect(store.getState().route).toEqual({ name: 'board', params: {} })
    })
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

  it('fetches the link status on a local gateway so the chip starts truthful', async () => {
    const store = createRootStore()
    await hydrate(
      store,
      fakeApi({
        capabilities: vi.fn(async () => ({
          gateway: 'local',
          methods: ['*'],
          ws: ['events', 'attach'],
          local: ['link.status'],
        })),
      }),
    )

    expect(store.getState().linkStatus?.linked).toBe(true)
  })

  it('leaves the link status alone on a remote gateway', async () => {
    const store = createRootStore()
    const client = fakeApi()
    await hydrate(store, client)

    expect(client.localLinkStatus).not.toHaveBeenCalled()
    expect(store.getState().linkStatus).toBeNull()
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
      { onboarded: false, local: true, linked: false, onboarding: true },
      { onboarded: false, local: true, linked: true, onboarding: false },
      { onboarded: true, local: true, linked: false, onboarding: false },
      { onboarded: false, local: false, linked: false, onboarding: false },
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
        localLinkStatus: vi.fn(async () => ({
          server_configured: true,
          linked: tc.linked,
          addr: 'host:2222',
          user: 'alice',
          repo: tc.linked ? '/src/repo' : '',
        })),
      })

      await hydrate(store, client)

      expect(store.getState().route.name).toBe(tc.onboarding ? 'onboarding' : 'board')
      if (tc.linked) expect(store.getState().onboarded).toBe(true)
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
    expect(c.hasLocal('worktree.open')).toBe(false)
  })

  it('classifies a fetch that never got an answer as a dead gateway', async () => {
    const store = createRootStore()
    store.getState().setCapabilities({ gateway: 'local', methods: ['*'], ws: ['events'] })
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

  it('classifies a dead fetch on the server gateway as a dead tailnet', async () => {
    const store = createRootStore()
    // The phone reaches the server gateway over the tailnet: there is no
    // local process to restart, so the same TypeError is a dead link. Its
    // own kind, because the desktop's `network` copy names wifi and a VPN.
    store.getState().setCapabilities({ gateway: 'server', methods: ['*'], ws: ['events'] })
    await hydrate(
      store,
      fakeApi({
        serverInfo: vi.fn(async () => {
          throw new TypeError('Failed to fetch')
        }),
      }),
    )

    expect(store.getState().unreachable).toBe('tailnet')
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
    await applyEvent(store, statusEvent({ payload: { to: 'completed' } }), fakeApi())

    expect(store.getState().runs.run_1.finished_at).toBe('2026-08-14T11:00:00Z')
  })

  it('updates a run protection flag from run.protected events', async () => {
    const store = createRootStore()
    await hydrate(store, fakeApi())

    expect(store.getState().runs.run_1.protected).toBeUndefined()
    await applyEvent(store, protectedEvent(), fakeApi())
    expect(store.getState().runs.run_1.protected).toBe(true)

    await applyEvent(
      store,
      protectedEvent({ id: 'evt_unprotected', seq: 8, payload: { protected: false } }),
      fakeApi(),
    )
    expect(store.getState().runs.run_1.protected).toBe(false)
    expect(store.getState().lastSeq).toBe(8)
  })

  it('updates a run title from a run.title event', async () => {
    const store = createRootStore()
    await hydrate(store, fakeApi())

    await applyEvent(store, titleEvent(), fakeApi())

    expect(store.getState().runs.run_1.title).toBe('Fixing the login bug')
    expect(store.getState().lastSeq).toBe(6)
  })
  it('removes a run when the server publishes its deletion', async () => {
    const store = createRootStore()
    await hydrate(store, fakeApi())

    expect(await applyEvent(
      store,
      statusEvent({ seq: 7, type: 'run.deleted', payload: {} }),
      fakeApi(),
    )).toBe(true)

    expect(store.getState().runs.run_1).toBeUndefined()
    expect(store.getState().lastSeq).toBe(7)
  })
  it('accepts a stale status event after local deletion', async () => {
    const store = createRootStore()
    await hydrate(store, fakeApi())
    store.getState().removeRun('run_1')
    const client = fakeApi({
      runGet: vi.fn(async () => {
        throw new ApiError(404, 'run.get: not found')
      }),
    })

    expect(await applyEvent(store, statusEvent({ seq: 8 }), client)).toBe(true)
    expect(client.runGet).toHaveBeenCalledWith('run_1')
    expect(store.getState().lastSeq).toBe(8)
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
  it('refreshes an unopened run count for question and reply events', async () => {
    const store = createRootStore()
    await hydrate(store, fakeApi())
    expect(store.getState().roomMessages.run_1).toBeUndefined()

    const client = fakeApi({
      runGet: vi
        .fn()
        .mockResolvedValueOnce(run({ unanswered_questions: 1 }))
        .mockResolvedValueOnce(run({ unanswered_questions: 0 })),
    })
    const roomEvent = (seq: number, kind: 'question' | 'reply'): Event => ({
      id: `room-${seq}`,
      seq,
      time: '2026-08-14T11:00:00Z',
      workspace_id: workspace.id,
      run_id: 'run_1',
      actor_id: alice.id,
      type: 'workspace.room_message',
      payload: { kind },
    })

    await applyEvent(store, roomEvent(8, 'question'), client)
    expect(store.getState().runs.run_1.unanswered_questions).toBe(1)
    expect(store.getState().roomMessages.run_1).toBeUndefined()

    await applyEvent(store, roomEvent(9, 'reply'), client)
    expect(store.getState().runs.run_1.unanswered_questions).toBe(0)
    expect(store.getState().roomMessages.run_1).toBeUndefined()
    expect(client.runGet).toHaveBeenCalledTimes(2)
  })

  it('keeps a question event unresolved when its attention refresh fails', async () => {
    const store = createRootStore()
    await hydrate(store, fakeApi())
    const client = fakeApi({
      runGet: vi.fn(async () => {
        throw new Error('run snapshot unavailable')
      }),
    })
    const event: Event = {
      id: 'room-refresh-failed',
      seq: 8,
      time: '2026-08-14T11:00:00Z',
      workspace_id: workspace.id,
      run_id: 'run_1',
      actor_id: alice.id,
      type: 'workspace.room_message',
      payload: { kind: 'question' },
    }

    expect(await applyEvent(store, event, client)).toBe(false)
    expect(store.getState().lastSeq).toBe(0)
  })

  it('walks disconnected room pages back to the cached boundary before merging', async () => {
    const store = createRootStore()
    await hydrate(store, fakeApi())
    const message = (index: number) => roomMessage({
      id: `message-${String(index).padStart(3, '0')}`,
      body: `message ${index}`,
      created_at: new Date(Date.UTC(2026, 7, 14, 10, 0, index)).toISOString(),
    })
    const cached = Array.from({ length: 100 }, (_, index) => message(index))
    const arrivals = Array.from({ length: 150 }, (_, index) => message(index + 100))
    store.getState().initializeRoomPagination('run_1')
    store.getState().setRoomPage('run_1', cached)

    const list = vi.fn()
      .mockResolvedValueOnce({ messages: arrivals.slice(50), next_before: 'gap-cursor-1' })
      .mockResolvedValueOnce({ messages: arrivals.slice(0, 100), next_before: 'gap-cursor-2' })
      .mockResolvedValueOnce({ messages: cached })
    const client = fakeApi({ runRoomList: list })
    const event: Event = {
      id: 'room-gap',
      seq: 8,
      time: '2026-08-14T11:00:00Z',
      workspace_id: workspace.id,
      run_id: 'run_1',
      actor_id: alice.id,
      type: 'workspace.room_message',
      payload: {},
    }

    expect(await applyEvent(store, event, client)).toBe(true)
    expect(list.mock.calls.map(([params]) => params.before)).toEqual([
      undefined,
      'gap-cursor-1',
      'gap-cursor-2',
    ])
    expect(store.getState().roomMessages.run_1.map((item) => item.id)).toEqual(
      cached.concat(arrivals).map((item) => item.id),
    )
  })

  it('paginates to an old room message before applying a mutable receipt update', async () => {
    const store = createRootStore()
    await hydrate(store, fakeApi())
    const message = (index: number) => roomMessage({
      id: `message-${String(index).padStart(3, '0')}`,
      body: `message ${index}`,
      created_at: new Date(Date.UTC(2026, 7, 14, 10, 0, index)).toISOString(),
    })
    const cached = Array.from({ length: 200 }, (_, index) => message(index))
    store.getState().initializeRoomPagination('run_1')
    store.getState().setRoomPage('run_1', cached, 'cached-cursor')
    const target = cached[50]
    const updatedTarget = { ...target, state: 'uncertain' as const, updated_at: '2026-08-14T11:00:00Z' }
    const list = vi.fn()
      .mockResolvedValueOnce({ messages: cached.slice(100), next_before: 'old-cursor' })
      .mockResolvedValueOnce({
        messages: [...cached.slice(0, 50), updatedTarget, ...cached.slice(51, 100)],
        next_before: 'oldest-cursor',
      })
    const client = fakeApi({ runRoomList: list })
    const event: Event = {
      id: 'room-old-receipt',
      seq: 8,
      time: '2026-08-14T11:00:00Z',
      workspace_id: workspace.id,
      run_id: 'run_1',
      actor_id: alice.id,
      type: 'workspace.room_message',
      payload: { message_id: target.id },
    }

    expect(await applyEvent(store, event, client)).toBe(true)
    expect(list.mock.calls.map(([params]) => params.before)).toEqual([undefined, 'old-cursor'])
    expect(store.getState().roomMessages.run_1.find((item) => item.id === target.id)).toMatchObject({
      state: 'uncertain',
      updated_at: '2026-08-14T11:00:00Z',
    })
  })

  it('walks every missed evidence page through the cached boundary', async () => {
    const store = createRootStore()
    await hydrate(store, fakeApi())
    const packet = (index: number) => evidencePacket({
      id: `packet-${String(index).padStart(3, '0')}`,
      captured_at: new Date(Date.UTC(2026, 7, 14, 10, 0, index)).toISOString(),
      created_at: new Date(Date.UTC(2026, 7, 14, 10, 0, index)).toISOString(),
      updated_at: new Date(Date.UTC(2026, 7, 14, 10, 0, index)).toISOString(),
    })
    const cached = Array.from({ length: 100 }, (_, index) => packet(index))
    const arrivals = Array.from({ length: 150 }, (_, index) => packet(index + 100))
    store.getState().initializeEvidencePagination('run_1')
    store.getState().setEvidencePage('run_1', cached, 'cached-evidence-cursor')

    const list = vi.fn()
      .mockResolvedValueOnce({ packets: arrivals.slice(50), next_before: 'evidence-gap-1' })
      .mockResolvedValueOnce({ packets: arrivals.slice(0, 100), next_before: 'evidence-gap-2' })
      .mockResolvedValueOnce({ packets: cached })
    const client = fakeApi({ runEvidenceList: list })
    const event: Event = {
      id: 'evidence-gap',
      seq: 8,
      time: '2026-08-14T11:00:00Z',
      workspace_id: workspace.id,
      run_id: 'run_1',
      actor_id: '',
      type: 'workspace.evidence_packet',
      payload: { packet_id: arrivals[149].id },
    }

    expect(await applyEvent(store, event, client)).toBe(true)
    expect(list.mock.calls.map(([params]) => params.before)).toEqual([
      undefined,
      'evidence-gap-1',
      'evidence-gap-2',
    ])
    expect(store.getState().evidencePackets.run_1).toHaveLength(250)
    expect(store.getState().evidenceNextBefore.run_1).toBeUndefined()
    expect(store.getState().evidencePagination.run_1).toEqual({ initialized: true, exhausted: true })
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
  it('shows the gateway refusal instead of an unreachable server', async () => {
    const store = createRootStore()
    const client = fakeApi({
      capabilities: vi.fn(async () => {
        throw new ApiError(
          403,
          'capabilities: tagged tailnet node; the dashboard identifies members by their tailnet login and a tagged node has none',
        )
      }),
    })
    const stop = connect(store, client)

    await vi.waitFor(() => expect(store.getState().hydrationError).toContain('tagged tailnet node'))
    expect(store.getState().hydrationError?.startsWith('tagged tailnet node')).toBe(true)
    expect(store.getState().unreachable).toBe('refused')
    expect(store.getState().hydrated).toBe(false)
    // The stream still opens and keeps retrying; its own failure can only
    // say "unreachable" and never overwrites the recorded reason.
    await vi.waitFor(() => expect(StubSocket.opened.length).toBeGreaterThan(0))
    StubSocket.last().onclose?.({ code: 1006, reason: '' })
    await vi.waitFor(() => expect(StubSocket.opened.length).toBeGreaterThan(1))
    expect(store.getState().hydrationError).toContain('tagged tailnet node')
    stop()
  })

  it('names the tailnet identity outage instead of an unreachable server', async () => {
    const store = createRootStore()
    const client = fakeApi({
      capabilities: vi.fn(async () => {
        throw new ApiError(
          503,
          '/capabilities: tailnet identity unavailable: sshd: tailnet whois: dial unix /var/run/tailscale/tailscaled.sock: connect: no such file or directory',
        )
      }),
    })
    const stop = connect(store, client)

    await vi.waitFor(() => expect(store.getState().unreachable).toBe('identity'))
    expect(store.getState().hydrationError?.startsWith('tailnet identity unavailable: ')).toBe(true)
    expect(store.getState().hydrated).toBe(false)
    stop()
  })

  it('hydrates onboarding directly for an unlinked local gateway', async () => {
    const store = createRootStore()
    const client = fakeApi({
      capabilities: vi.fn(async () => ({
        gateway: 'local',
        methods: ['*'],
        ws: ['events', 'attach'],
        local: ['link.status'],
      })),
      localLinkStatus: vi.fn(async () => ({
        server_configured: false,
        linked: false,
        addr: '',
        user: '',
        repo: '',
      })),
    })
    const stop = connect(store, client)

    await vi.waitFor(() => expect(client.localLinkStatus).toHaveBeenCalled())
    expect(store.getState().hydrated).toBe(true)
    expect(store.getState().route.name).toBe('onboarding')
    expect(store.getState().linkStatus?.server_configured).toBe(false)
    expect(client.serverInfo).not.toHaveBeenCalled()
    expect(StubSocket.opened).toHaveLength(0)
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

  it('revalidates tailnet identity on replay reconnects without losing same-member drafts', async () => {
    let member = alice
    const client = fakeApi({
      capabilities: vi.fn(async () => ({ gateway: 'server', methods: ['*'], ws: ['events'] })),
      serverInfo: vi.fn(async () => ({ ...serverInfoFixture, member })),
    })
    const store = createRootStore()
    const stop = connect(store, client)
    try {
      const socket = await subscribe()
      await vi.waitFor(() => expect(store.getState().hydrated).toBe(true))
      const key = configKey('claude', 'settings.json')
      store.getState().setDocument(key, {
        content: '{}', size: 2, revision: 'original', writable: true, binary: false, truncated: false,
      })
      store.getState().updateDraft(key, '{"private":"draft"}')
      store.getState().openFileTab({
        key, kind: 'config', harness: 'claude', rootPath: '~/.claude', path: 'settings.json', label: 'claude',
      })
      deliver(socket, statusEvent())
      await vi.waitFor(() => expect(store.getState().lastSeq).toBe(5))

      socket.onclose?.({ code: 1006 })
      await vi.waitFor(() => expect(StubSocket.opened).toHaveLength(2), { timeout: 2000 })
      await subscribe()
      await vi.waitFor(() => expect(client.serverInfo).toHaveBeenCalledTimes(2))
      expect(store.getState().drafts[key]?.content).toBe('{"private":"draft"}')

      member = bob
      StubSocket.last().onclose?.({ code: 1006 })
      await vi.waitFor(() => expect(StubSocket.opened).toHaveLength(3), { timeout: 2000 })
      await subscribe()
      await vi.waitFor(() => expect(store.getState().info?.member.id).toBe(bob.id))
      expect(store.getState().documents[key]).toBeUndefined()
      expect(store.getState().drafts[key]).toBeUndefined()
      expect(store.getState().fileTabs).toEqual([])
      expect(store.getState().activeFileKey).toBeNull()
    } finally {
      stop()
    }
  })

  it('reports a rejected credential rather than an unreachable server', async () => {
    // The gateway's own 401 body. A stale or missing token would be rejected
    // the same way on the WebSocket upgrade, where the failure has no voice
    // at all, so the probe is the only place that can say what went wrong.
    const denial = 'a valid gateway token is required; restart `aether gui` for a fresh URL'
    const store = createRootStore()
    const stop = connect(
      store,
      fakeApi({
        capabilities: vi.fn(() => Promise.reject(new ApiError(401, denial))),
      }),
    )

    await vi.waitFor(() => expect(store.getState().streamDead).toBe(true))
    expect(store.getState().connection).toBe('offline')
    // The gateway's words, not a guess about the network.
    expect(store.getState().hydrationError).toBe(denial)
    // Every reconnect would carry the same credential, so nothing is tried.
    await new Promise((resolve) => setTimeout(resolve, 700))
    expect(StubSocket.opened).toHaveLength(0)
    stop()
  })

  it('opens the stream when the capabilities probe fails for any other reason', async () => {
    const store = createRootStore()
    const stop = connect(
      store,
      fakeApi({
        capabilities: vi.fn(() => Promise.reject(new ApiError(500, 'boom'))),
      }),
    )

    await subscribe()
    await vi.waitFor(() => expect(store.getState().hydrated).toBe(true))
    expect(store.getState().streamDead).toBe(false)
    stop()
  })

  it('knows which gateway serves the page before the first hydration fails', async () => {
    // Only the probe read the descriptor; hydration never got far enough to
    // store it, and without it a phone would be told to restart a desktop
    // app it does not have.
    const store = createRootStore()
    const stop = connect(
      store,
      fakeApi({
        capabilities: vi.fn(async () => ({ gateway: 'server', methods: ['*'], ws: ['events'] })),
        serverInfo: vi.fn(async () => {
          throw new TypeError('Failed to fetch')
        }),
      }),
    )

    await subscribe()
    await vi.waitFor(() => expect(store.getState().unreachable).toBe('tailnet'))
    expect(store.getState().hydrated).toBe(false)
    stop()
  })

  it('retries a 1008 close, which no longer means a dead token', async () => {
    const client = fakeApi()
    const store = createRootStore()
    const stop = connect(store, client)

    await subscribe()
    await vi.waitFor(() => expect(store.getState().hydrated).toBe(true))

    // The gateway closes 1008 for a refused subscribe or a transient
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

  it(
    're-hydrates at once when the tab returns with a retry pending',
    async () => {
      let failing = true
      const serverInfo = vi.fn(async () => {
        if (failing) throw new Error('502 Bad Gateway')
        return serverInfoFixture
      })
      const store = createRootStore()
      const stop = connect(store, fakeApi({ serverInfo }))

      await subscribe()
      // Three failures put the next retry seconds out. That timer is the one
      // a frozen tab stops, and the reopened sockets cannot restart it: the
      // stream goes live again with a cursor to replay from, so nothing else
      // re-fetches.
      await vi.waitFor(
        () => expect(serverInfo.mock.calls.length).toBeGreaterThanOrEqual(3),
        { timeout: 6_000 },
      )
      await new Promise((resolve) => setTimeout(resolve, 30))
      const before = serverInfo.mock.calls.length
      failing = false

      fire('visibilitychange')

      // Comfortably inside the pending retry, which is at least two seconds
      // out, and wide enough that a loaded CI runner cannot fail it.
      await vi.waitFor(() => expect(store.getState().hydrated).toBe(true), {
        timeout: 1_000,
      })
      expect(serverInfo.mock.calls.length).toBeGreaterThan(before)
      stop()
    },
    15_000,
  )

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
