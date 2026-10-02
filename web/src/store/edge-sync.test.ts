import { ApiError } from '@/lib/api'
import type { LinkStatus } from '@/lib/types'
import { createRootStore } from '@/store'
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
  // and keeps the client's error after it. These are that error as
  // internal/edge/client and the SSH handshake word it.
  const cases: [string, string, string][] = [
    [
      'the server has no edge connection',
      `connect to server ${serverID}: edge.example.test refused: server is not connected to the edge (HTTP 503)`,
      'edge-server',
    ],
    [
      'this machine never signed in',
      `connect to server ${serverID}: not signed in to edge.example.test; run: aether login --edge ${edge}`,
      'signed-out',
    ],
    [
      'the device token was revoked at the edge',
      `connect to server ${serverID}: edge.example.test refused: device token revoked (HTTP 401); sign in again with: aether login --edge ${edge}`,
      'signed-out',
    ],
    [
      'the server revoked the device',
      'ssh: handshake failed: device "laptop" was revoked on this server',
      'device-revoked',
    ],
    [
      'the server holds the device pending',
      'ssh: handshake failed: device "laptop" is waiting for approval. From a device this account already uses, or as an admin, run:\n  aether device approve ABCD-EFGH',
      'device-pending',
    ],
    [
      'the edge no longer counts the account as a member',
      `connect to server ${serverID}: edge.example.test refused: not a member of this server (HTTP 403)`,
      'not-member',
    ],
    [
      'the server no longer counts the account as a member',
      'ssh handshake with ' + `${serverID}.edge.aether.invalid: ssh: handshake failed: ssh: unable to authenticate\n  server said: octo is not a member of this server`,
      'not-member',
    ],
    ...[
      'unknown server',
      "server is blocked by this edge's operator",
      "account is blocked by this edge's operator",
      'too many attempts',
      'connection limit reached',
    ].map((reason): [string, string, string] => [
      `the edge refuses with "${reason}"`,
      `connect to server ${serverID}: edge.example.test refused: ${reason} (HTTP 403)`,
      'edge-refused',
    ]),
    [
      'the edge does not answer',
      `connect to server ${serverID}: edge.example.test: dial tcp 192.0.2.10:443: connect: connection refused`,
      'edge',
    ],
  ]

  it.each(cases)('names the failure when %s', async (_, detail, kind) => {
    const store = createRootStore()
    store.getState().setLinkStatus(edgeLink)
    await hydrate(
      store,
      fakeApi({
        serverInfo: vi.fn(() =>
          Promise.reject(new ApiError(503, `server.info: server unreachable: ${detail}`, -32004)),
        ),
      }),
    )

    expect(store.getState().unreachable).toBe(kind)
  })

  it('keeps the SSH copy for a link that is not through an edge', async () => {
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
        }),
      })

      expect(store.getState().unreachable).toBe('edge-server')
      expect(store.getState().hydrationError).toContain('server is not connected to the edge')
      stop()
    })
  })
})
