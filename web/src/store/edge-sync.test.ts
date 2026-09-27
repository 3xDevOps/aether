import { ApiError, pendingApprovalCode, signInRequired } from '@/lib/api'
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

/** The edge gateway's answer to a browser without a session. */
function signInRefusal() {
  return new ApiError(401, '/capabilities: sign in required', -32001, { login: '/auth/login' })
}

describe('edge gateway refusals', () => {
  it('tells a sign-in refusal from the local gateway expired token', () => {
    expect(signInRequired(signInRefusal())).toBe(true)
    // The local gateway's 401 names no sign-in: its token died with the
    // process, and a sign-in page would be the wrong way out.
    expect(signInRequired(new ApiError(401, 'a valid gateway token is required', -32001))).toBe(false)
    expect(
      signInRequired(new ApiError(403, 'denied', -32001, { login: '/auth/login' })),
    ).toBe(false)
  })

  it('reads the approval code only from a 403 that carries one', () => {
    expect(
      pendingApprovalCode(new ApiError(403, 'pending', -32001, { approval_code: 'ABCD-EFGH' })),
    ).toBe('ABCD-EFGH')
    expect(pendingApprovalCode(new ApiError(403, 'tagged tailnet node', -32001))).toBeNull()
    expect(
      pendingApprovalCode(new ApiError(401, 'pending', -32001, { approval_code: 'ABCD-EFGH' })),
    ).toBeNull()
  })
})

describe('connect on the edge gateway', () => {
  beforeEach(() => {
    StubSocket.install()
  })
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('asks for a sign-in and opens no stream when the probe answers 401', async () => {
    const store = createRootStore()
    const stop = connect(
      store,
      fakeApi({ capabilities: vi.fn(() => Promise.reject(signInRefusal())) }),
    )

    await vi.waitFor(() => expect(store.getState().edgeAccess).toEqual({ state: 'signed-out' }))
    expect(store.getState().connection).toBe('offline')
    // Not the expired-link page, which tells the reader to run aether gui.
    expect(store.getState().streamDead).toBe(false)
    await new Promise((resolve) => setTimeout(resolve, 100))
    expect(StubSocket.opened).toHaveLength(0)
    stop()
  })

  it('holds a browser waiting for approval with its code', async () => {
    const store = createRootStore()
    const stop = connect(
      store,
      fakeApi({
        capabilities: vi.fn(() =>
          Promise.reject(
            new ApiError(403, '/capabilities: device is pending approval', -32001, {
              approval_code: 'ABCD-EFGH',
            }),
          ),
        ),
      }),
    )

    await vi.waitFor(() =>
      expect(store.getState().edgeAccess).toEqual({ state: 'pending', approvalCode: 'ABCD-EFGH' }),
    )
    expect(store.getState().unreachable).toBeNull()
    expect(StubSocket.opened).toHaveLength(0)
    stop()
  })

  it('asks for a sign-in when a session ends mid-use, and stops retrying', async () => {
    const store = createRootStore()
    store.getState().setCapabilities({ gateway: 'edge', methods: ['*'], ws: ['events'] })
    store.getState().setHydrated(true)

    const ok = await hydrate(
      store,
      fakeApi({ serverInfo: vi.fn(() => Promise.reject(signInRefusal())) }),
    )

    expect(ok).toBe(false)
    expect(store.getState().edgeAccess).toEqual({ state: 'signed-out' })
    expect(store.getState().streamDead).toBe(true)
  })

  it('asks for a sign-in when the socket of a live session is refused', async () => {
    let signedIn = true
    const capabilities = vi.fn(() =>
      signedIn
        ? Promise.resolve({ gateway: 'edge' as const, methods: ['*'], ws: ['events'] })
        : Promise.reject(signInRefusal()),
    )
    const store = createRootStore()
    const stop = connect(store, fakeApi({ capabilities }))

    await vi.waitFor(() => expect(StubSocket.opened).toHaveLength(1))
    const first = StubSocket.last()
    first.onopen?.()
    first.onmessage?.({ data: JSON.stringify({ ok: true }) })
    await vi.waitFor(() => expect(store.getState().hydrated).toBe(true))

    // The gateway ends the session and refuses every later handshake before
    // the upgrade: the browser sees a close and nothing else.
    signedIn = false
    first.onclose?.({ code: 1006 })

    await vi.waitFor(() => expect(store.getState().edgeAccess).toEqual({ state: 'signed-out' }))
    expect(store.getState().streamDead).toBe(true)
    expect(store.getState().connection).toBe('offline')
    const opened = StubSocket.opened.length
    await new Promise((resolve) => setTimeout(resolve, 1000))
    expect(StubSocket.opened).toHaveLength(opened)
    stop()
  })

  it('names a silent edge origin as the relay, not Tailscale', async () => {
    const store = createRootStore()
    store.getState().setCapabilities({ gateway: 'edge', methods: ['*'], ws: ['events'] })
    await hydrate(
      store,
      fakeApi({ serverInfo: vi.fn(() => Promise.reject(new TypeError('Failed to fetch'))) }),
    )

    expect(store.getState().unreachable).toBe('relay')
  })
})

describe('a local link through an edge', () => {
  // The local gateway prefixes every dial failure with "server unreachable: "
  // and keeps the client's error after it. These are that error as
  // internal/edgeclient and the SSH handshake word it.
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
