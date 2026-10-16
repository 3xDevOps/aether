import { ApiError } from '@/lib/api'
import type { EdgeRefusal, LinkStatus } from '@/lib/types'
import { createRootStore } from '@/store'
import type { UnreachableKind } from '@/store/server'
import { connect, hydrate } from '@/store/sync'
import { fakeApi, run } from '@/test/fixtures'
import { StubSocket } from '@/test/stub-socket'

const edge = 'https://edge.example.test'
const serverID = 'abcdefghijklmnopqrstuvwxyz'

/** A local gateway linked to a server through the fake edge. */
const edgeLink: LinkStatus = {
  server_configured: true,
  linked: true,
  addr: '',
  user: 'aether',
  repo: '/src/repo',
  edge_url: edge,
  server_id: serverID,
}

describe('a local link through an edge', () => {
  // The local gateway prefixes every dial failure with "server unreachable: "
  // and, for a refusal on the edge path, puts its kind in data.refusal. The
  // message is whatever the client said; only the kind classifies.
  const cases: [EdgeRefusal['kind'], UnreachableKind][] = [
    ['server-not-connected', 'edge-server'],
    ['server-not-claimed', 'edge-not-claimed'],
    ['not-signed-in', 'signed-out'],
    ['token-revoked', 'signed-out'],
    ['device-revoked', 'device-revoked'],
    ['device-pending', 'device-pending'],
    ['not-member', 'not-member'],
    ['unknown-server', 'edge-refused'],
    ['server-blocked', 'edge-refused'],
    ['account-blocked', 'edge-refused'],
    ['too-many-attempts', 'edge-refused'],
    ['connection-limit', 'edge-refused'],
    ['edge-refused', 'edge-refused'],
    ['too-many-waiting', 'server-refused'],
    ['key-of-another-account', 'server-refused'],
  ]

  it.each(cases)('names the hop of a %s refusal', async (refused, kind) => {
    const store = createRootStore()
    store.getState().setLinkStatus(edgeLink)
    await hydrate(
      store,
      fakeApi({
        serverInfo: vi.fn(() =>
          Promise.reject(
            new ApiError(503, 'server.info: server unreachable: refused', -32004, { refusal: { kind: refused } }),
          ),
        ),
      }),
    )

    expect(store.getState().unreachable).toBe(kind)
  })

  it('names the edge when it does not answer', async () => {
    const store = createRootStore()
    store.getState().setLinkStatus(edgeLink)
    await hydrate(
      store,
      fakeApi({
        serverInfo: vi.fn(() =>
          Promise.reject(
            new ApiError(
              503,
              `server.info: server unreachable: connect to server ${serverID}: edge.example.test: dial tcp 192.0.2.10:443: connect: connection refused`,
              -32004,
            ),
          ),
        ),
      }),
    )

    expect(store.getState().unreachable).toBe('edge')
  })

  it('keeps the SSH copy for a failure that carries no refusal', async () => {
    const store = createRootStore()
    store.getState().setLinkStatus({ ...edgeLink, addr: 'host:2222', edge_url: undefined, server_id: undefined })
    await hydrate(
      store,
      fakeApi({
        serverInfo: vi.fn(() =>
          Promise.reject(
            new ApiError(
              503,
              'server.info: server unreachable: edge.example.test refused: server is not connected to the edge (HTTP 503)',
              -32004,
            ),
          ),
        ),
      }),
    )

    expect(store.getState().unreachable).toBe('server')
  })

  describe('on the event stream', () => {
    beforeEach(() => {
      StubSocket.install()
    })
    afterEach(() => {
      vi.unstubAllGlobals()
    })

    it('names the edge hop the subscribe refusal carries', async () => {
      // The probe reads the link before the stream opens: a refusal on the
      // very first subscribe has no hydrated link status to go by.
      const store = createRootStore()
      const stop = connect(
        store,
        fakeApi({
          capabilities: vi.fn(async () => ({
            gateway: 'local',
            methods: ['*'],
            ws: ['events'],
            local: ['link.status'],
          })),
          localLinkStatus: vi.fn(async () => edgeLink),
        }),
      )

      await vi.waitFor(() => expect(StubSocket.opened.length).toBeGreaterThan(0))
      const socket = StubSocket.last()
      socket.onopen?.()
      socket.onmessage?.({
        data: JSON.stringify({
          ok: false,
          code: -32004,
          error: `server unreachable: connect to server ${serverID}: edge.example.test refused: server is not connected to the edge (HTTP 503)`,
          data: { refusal: { kind: 'server-not-connected', refusal: 'server is not connected to the edge' } },
        }),
      })

      expect(store.getState().unreachable).toBe('edge-server')
      expect(store.getState().hydrationError).toContain('server is not connected to the edge')
      stop()
    })
  })
})

describe('the connection state on a link through an edge', () => {
  const notConnected = {
    ok: false,
    code: -32004,
    error: `server unreachable: connect to server ${serverID}: edge.example.test refused: server is not connected to the edge (HTTP 503)`,
    data: { refusal: { kind: 'server-not-connected', refusal: 'server is not connected to the edge' } },
  }
  const localEdge = {
    capabilities: vi.fn(async () => ({ gateway: 'local' as const, methods: ['*'], ws: ['events'], local: ['link.status'] })),
    localLinkStatus: vi.fn(async () => edgeLink),
  }

  beforeEach(() => {
    StubSocket.install()
  })
  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
  })

  /** Opens the stream, acknowledges it, and hydrates with a cursor, so the
   * next reconnect replays instead of re-fetching. */
  async function live(client: ReturnType<typeof fakeApi>) {
    const store = createRootStore()
    const stop = connect(store, client)
    await vi.waitFor(() => expect(StubSocket.opened.length).toBe(1))
    const socket = StubSocket.last()
    socket.onopen?.()
    socket.onmessage?.({ data: JSON.stringify({ ok: true }) })
    await vi.waitFor(() => expect(store.getState().hydrated).toBe(true))
    store.getState().noteSeq(7)
    return { store, stop }
  }

  /** Drops the socket and waits for the client to open the next one. */
  async function reopen() {
    const count = StubSocket.opened.length
    StubSocket.last().onclose?.({ code: 1006 })
    await vi.waitFor(() => expect(StubSocket.opened.length).toBe(count + 1), { timeout: 3000 })
    const socket = StubSocket.last()
    socket.onopen?.()
    return socket
  }

  it('clears a stale edge refusal when a replay reconnect goes live without re-hydrating', async () => {
    const client = fakeApi(localEdge)
    const { store, stop } = await live(client)

    ;(await reopen()).onmessage?.({ data: JSON.stringify(notConnected) })
    expect(store.getState().unreachable).toBe('edge-server')
    expect(store.getState().unreachableAt).toEqual({ server_id: serverID })

    ;(await reopen()).onmessage?.({ data: JSON.stringify({ ok: true }) })
    expect(store.getState().connection).toBe('live')
    expect(store.getState().unreachable).toBeNull()
    expect(client.serverInfo).toHaveBeenCalledTimes(1)
    stop()
  })

  it('re-reads at once, not on backoff, when a replay reconnect goes live over a failed read', async () => {
    let reads = 0
    const unreachable = new ApiError(503, `server.info: server unreachable: connect to server ${serverID}: ssh: handshake failed: EOF`, -32004)
    const client = fakeApi({
      ...localEdge,
      serverInfo: vi.fn(async () => {
        reads++
        if (reads === 1) return (await fakeApi().serverInfo())
        throw unreachable
      }),
      runGet: vi.fn(async () => { throw unreachable }),
    })
    const { store, stop } = await live(client)
    vi.useFakeTimers()
    // backoff(n) is then 250ms * 2^n for the hydration retry and the stream alike.
    vi.spyOn(Math, 'random').mockReturnValue(0)

    // An event names a run this client has not loaded; its read fails, and
    // so does the hydration that repairs it.
    StubSocket.last().onmessage?.({
      data: JSON.stringify({ seq: 8, type: 'run.title', run_id: 'run_new', workspace_id: 'ws', time: '', payload: { title: 't' } }),
    })
    await vi.advanceTimersByTimeAsync(0)
    expect(store.getState().unreachable).toBe('server')
    // The first retry fails too; the next one is 500ms out.
    await vi.advanceTimersByTimeAsync(250)
    StubSocket.last().onclose?.({ code: 4000 })
    await vi.advanceTimersByTimeAsync(250)
    const socket = StubSocket.last()
    socket.onopen?.()

    const before = reads
    socket.onmessage?.({ data: JSON.stringify({ ok: true }) })
    await vi.advanceTimersByTimeAsync(0)
    expect(store.getState().connection).toBe('live')
    expect(reads).toBe(before + 1)
    expect(store.getState().unreachable).toBe('server')
    stop()
  })

  it('follows a re-link that a failure reveals, and shows the failure for the new server', async () => {
    const relinked = { ...edgeLink, server_id: 'srvnew' }
    let link = edgeLink
    const client = fakeApi({ ...localEdge, localLinkStatus: vi.fn(async () => link) })
    const { store, stop } = await live(client)
    const epoch = store.getState().connectionEpoch
    link = relinked

    const path = { layer: 'server-registration', server_id: 'srvnew', edge: 'edge.example.test' }
    ;(await reopen()).onmessage?.({ data: JSON.stringify({ ...notConnected, data: { ...notConnected.data, edge_path: path } }) })

    await vi.waitFor(() => expect(store.getState().linkStatus?.server_id).toBe('srvnew'))
    expect(store.getState().connectionEpoch).toBe(epoch + 1)
    expect(store.getState().hydrated).toBe(false)
    expect(store.getState().unreachable).toBe('edge-server')
    expect(store.getState().unreachableAt).toEqual(path)
    stop()
  })

  it('drops a failure that lands after the connection was switched away', async () => {
    const pending = Promise.withResolvers<never>()
    const client = fakeApi({ ...localEdge, runGet: vi.fn(() => pending.promise) })
    const { store, stop } = await live(client)
    StubSocket.last().onmessage?.({
      data: JSON.stringify({ seq: 8, type: 'run.title', run_id: 'run_new', workspace_id: 'ws', time: '', payload: { title: 't' } }),
    })
    await vi.waitFor(() => expect(client.runGet).toHaveBeenCalled())

    stop()
    store.getState().reconnect()
    pending.reject(new ApiError(503, 'run.get: server unreachable: refused', -32004, { refusal: { kind: 'server-not-connected' } }))
    await Promise.resolve()
    await new Promise((resolve) => setTimeout(resolve, 0))

    expect(store.getState().unreachable).toBeNull()
  })

  it('records the latest result, not the first, across an interruption', async () => {
    const client = fakeApi({ ...localEdge, runGet: vi.fn(async () => run({ id: 'run_new' })) })
    const { store, stop } = await live(client)

    ;(await reopen()).onmessage?.({ data: JSON.stringify({ ok: false, code: -32004, error: 'network unreachable: dial tcp: lookup edge.example.test: no such host' }) })
    expect(store.getState().unreachable).toBe('network')
    ;(await reopen()).onmessage?.({ data: JSON.stringify(notConnected) })
    expect(store.getState().unreachable).toBe('edge-server')
    ;(await reopen()).onmessage?.({ data: JSON.stringify({ ok: true }) })
    expect(store.getState().unreachable).toBeNull()
    expect(store.getState().unreachableAt).toBeNull()
    stop()
  })

  it('records the edge path the gateway names', async () => {
    const client = fakeApi(localEdge)
    const { store, stop } = await live(client)
    const path = { layer: 'server-registration', server_id: serverID, edge: 'edge.example.test' }
    ;(await reopen()).onmessage?.({
      data: JSON.stringify({ ...notConnected, data: { refusal: { kind: 'server-not-claimed' }, edge_path: path } }),
    })
    expect(store.getState().unreachable).toBe('edge-not-claimed')
    expect(store.getState().unreachableAt).toEqual(path)
    stop()
  })
})
