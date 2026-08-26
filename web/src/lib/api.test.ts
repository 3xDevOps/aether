// The token bootstrap: `aether gui` opens a tokened URL, and the client's
// job is to move the credential into sessionStorage and out of the address
// bar - the one place it would otherwise sit in history and screenshots.

import { api, socketURL } from '@/lib/api'

function fakeFetch() {
  return vi.fn(async (_input: unknown, _init?: RequestInit) => ({
    ok: true,
    json: async () => ({}),
  }))
}

function sentHeaders(fetchSpy: ReturnType<typeof fakeFetch>) {
  return fetchSpy.mock.calls[0][1]?.headers as Record<string, string>
}

describe('token bootstrap', () => {
  beforeEach(() => {
    window.sessionStorage.clear()
    window.history.replaceState({}, '', '/')
  })
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('moves ?token= into sessionStorage and strips it from the address bar', async () => {
    window.history.replaceState({}, '', '/?token=tok_live&view=board')
    const fetchSpy = fakeFetch()
    vi.stubGlobal('fetch', fetchSpy)

    await api.serverInfo()

    expect(sentHeaders(fetchSpy).authorization).toBe('Bearer tok_live')
    expect(window.sessionStorage.getItem('aether.token')).toBe('tok_live')
    // The rest of the URL survives; only the credential is gone.
    expect(window.location.search).toBe('?view=board')
  })

  it('keeps using the stored token once the URL is clean', async () => {
    window.sessionStorage.setItem('aether.token', 'tok_stored')
    const fetchSpy = fakeFetch()
    vi.stubGlobal('fetch', fetchSpy)

    await api.serverInfo()

    expect(sentHeaders(fetchSpy).authorization).toBe('Bearer tok_stored')
    // The sockets cannot send headers, so their URL carries it instead.
    expect(socketURL('/ws/events')).toContain('token=tok_stored')
  })

  it('posts a local verb to /local/v1 with the same bearer', async () => {
    window.sessionStorage.setItem('aether.token', 'tok_stored')
    const fetchSpy = fakeFetch()
    vi.stubGlobal('fetch', fetchSpy)

    await api.localSyncStart('run_1', true)

    expect(fetchSpy.mock.calls[0][0]).toBe('/local/v1/sync.start')
    expect(sentHeaders(fetchSpy).authorization).toBe('Bearer tok_stored')
    expect(fetchSpy.mock.calls[0][1]?.body).toBe(
      JSON.stringify({ run_id: 'run_1', force: true }),
    )
  })
})
