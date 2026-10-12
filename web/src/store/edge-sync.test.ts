import { ApiError } from '@/lib/api'
import type { EdgeRefusal, LinkStatus } from '@/lib/types'
import { createRootStore } from '@/store'
import type { UnreachableKind } from '@/store/server'
import { connect, hydrate } from '@/store/sync'
import { fakeApi } from '@/test/fixtures'
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
