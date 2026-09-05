// The token bootstrap: `aether gui` opens a tokened URL, and the client's
// job is to move the credential into sessionStorage and out of the address
// bar - the one place it would otherwise sit in history and screenshots.

import { api } from '@/lib/api'

function fakeFetch(result: unknown = {}) {
  return vi.fn(async (_input: unknown, _init?: RequestInit) => ({
    ok: true,
    json: async () => result,
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

  it('builds a tokened shell attach URL with encoded run and tab', () => {
    window.sessionStorage.setItem('aether.token', 'tok_stored')

    const url = api.attachShellSocket('run/1', 't-2')

    expect(url).toContain('/ws/attach/run%2F1?shell=t-2&token=tok_stored')
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

describe('environment terminal methods', () => {
  beforeEach(() => {
    window.sessionStorage.setItem('aether.token', 'tok_terminal')
  })
  afterEach(() => {
    vi.unstubAllGlobals()
    window.sessionStorage.clear()
  })

  it('posts status and stop RPCs and builds an encoded terminal socket URL', async () => {
    const fetchSpy = fakeFetch({ running: false, tabs: [] })
    vi.stubGlobal('fetch', fetchSpy)

    await api.terminalStatus()
    await api.terminalStop()

    expect(fetchSpy.mock.calls[0][0]).toBe('/api/v1/terminal.status')
    expect(JSON.parse(fetchSpy.mock.calls[0][1]?.body as string)).toEqual({})
    expect(fetchSpy.mock.calls[1][0]).toBe('/api/v1/terminal.stop')
    expect(api.terminalSocket('t 2')).toContain(
      '/ws/terminal?tab=t+2&token=tok_terminal',
    )
  })
})

describe('local setup methods', () => {
  beforeEach(() => {
    window.sessionStorage.setItem('aether.token', 'tok_stored')
  })
  afterEach(() => {
    vi.unstubAllGlobals()
    window.sessionStorage.clear()
  })

  it('reads the detected harnesses over the local gateway', async () => {
    const fetchSpy = fakeFetch({
      harnesses: [{ name: 'claude', installed: true }],
      searched: ['/usr/local/bin'],
      repo_path: '/src/repo',
    })
    vi.stubGlobal('fetch', fetchSpy)

    const detected = await api.envHarnesses()

    expect(fetchSpy.mock.calls[0][0]).toBe('/local/v1/env.harnesses')
    expect(detected).toEqual({
      harnesses: [{ name: 'claude', installed: true }],
      searched: ['/usr/local/bin'],
      repo_path: '/src/repo',
    })
  })
})
