// The token bootstrap: `aether gui` opens a tokened URL, and the client's
// job is to move the credential into sessionStorage and out of the address
// bar - the one place it would otherwise sit in history and screenshots.

import { api } from '@/lib/api'
import type { EnvScanResult } from '@/lib/types'
import { fakeApi, manifestItem } from '@/test/fixtures'

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

describe('environment methods', () => {
  beforeEach(() => {
    window.sessionStorage.setItem('aether.token', 'tok_stored')
  })
  afterEach(() => {
    vi.unstubAllGlobals()
    window.sessionStorage.clear()
  })

  it('posts env.save with the manifest inline and unwraps the version', async () => {
    const fetchSpy = fakeFetch({ version: 2 })
    vi.stubGlobal('fetch', fetchSpy)

    const version = await api.envSave({
      workspace: { id: 'wsp_1' },
      dockerfile: 'FROM ubuntu:24.04\n',
      manifest: [manifestItem()],
      source: 'mirror',
      harness: 'claude',
    })

    expect(version).toBe(2)
    expect(fetchSpy.mock.calls[0][0]).toBe('/api/v1/env.save')
    expect(JSON.parse(fetchSpy.mock.calls[0][1]?.body as string)).toEqual({
      workspace: { id: 'wsp_1' },
      dockerfile: 'FROM ubuntu:24.04\n',
      manifest: [manifestItem()],
      source: 'mirror',
      harness: 'claude',
    })
  })

  it('posts env.build and env.status against the workspace', async () => {
    const fetchSpy = fakeFetch({ version: 2, versions: [] })
    vi.stubGlobal('fetch', fetchSpy)

    await api.envBuild({ id: 'wsp_1' })
    await api.envStatus({ id: 'wsp_1' })

    expect(fetchSpy.mock.calls[0][0]).toBe('/api/v1/env.build')
    expect(JSON.parse(fetchSpy.mock.calls[0][1]?.body as string)).toEqual({
      workspace: { id: 'wsp_1' },
    })
    expect(fetchSpy.mock.calls[1][0]).toBe('/api/v1/env.status')
  })

  it('posts env.edit with the harness and request', async () => {
    const fetchSpy = fakeFetch({ accepted: true })
    vi.stubGlobal('fetch', fetchSpy)

    const result = await api.envEdit(
      { id: 'wsp_1' },
      'claude',
      'add go 1.24',
    )

    expect(result.accepted).toBe(true)
    expect(fetchSpy.mock.calls[0][0]).toBe('/api/v1/env.edit')
    expect(JSON.parse(fetchSpy.mock.calls[0][1]?.body as string)).toEqual({
      workspace: { id: 'wsp_1' },
      harness: 'claude',
      request: 'add go 1.24',
    })
  })

  it('posts env.get with the version and only sends diff_against when set', async () => {
    const fetchSpy = fakeFetch({
      version: 2,
      dockerfile: 'FROM ubuntu:24.04\n',
      manifest: [manifestItem()],
      source: 'mirror',
      status: 'saved',
    })
    vi.stubGlobal('fetch', fetchSpy)

    await api.envGet({ id: 'wsp_1' }, 2, 1)
    await api.envGet({ id: 'wsp_1' }, 2)

    expect(fetchSpy.mock.calls[0][0]).toBe('/api/v1/env.get')
    expect(JSON.parse(fetchSpy.mock.calls[0][1]?.body as string)).toEqual({
      workspace: { id: 'wsp_1' },
      version: 2,
      diff_against: 1,
    })
    expect(JSON.parse(fetchSpy.mock.calls[1][1]?.body as string)).toEqual({
      workspace: { id: 'wsp_1' },
      version: 2,
    })
  })

  it('posts env.rollback and unwraps the version that is active again', async () => {
    const fetchSpy = fakeFetch({ version: 1 })
    vi.stubGlobal('fetch', fetchSpy)

    const version = await api.envRollback({ id: 'wsp_1' })

    expect(version).toBe(1)
    expect(fetchSpy.mock.calls[0][0]).toBe('/api/v1/env.rollback')
    expect(JSON.parse(fetchSpy.mock.calls[0][1]?.body as string)).toEqual({
      workspace: { id: 'wsp_1' },
    })
  })

  it('reads the detected harnesses over the local gateway', async () => {
    const fetchSpy = fakeFetch({
      harnesses: [{ name: 'claude', installed: true }],
      repo_path: '/src/repo',
    })
    vi.stubGlobal('fetch', fetchSpy)

    const detected = await api.envHarnesses()

    expect(fetchSpy.mock.calls[0][0]).toBe('/local/v1/env.harnesses')
    expect(detected).toEqual({
      harnesses: [{ name: 'claude', installed: true }],
      repo_path: '/src/repo',
    })
  })

  it('is covered end to end by the fixture fakes', async () => {
    const fake = fakeApi()

    const status = await fake.envStatus({ id: 'wsp_1' })
    expect(status.active_version).toBe(status.versions[0].version)
    expect(status.versions[0].manifest.length).toBeGreaterThan(0)

    await expect(
      fake.envSave({
        workspace: { id: 'wsp_1' },
        dockerfile: 'FROM ubuntu:24.04\n',
        manifest: [manifestItem()],
        source: 'mirror',
        harness: 'claude',
      }),
    ).resolves.toBeGreaterThan(0)
    await expect(fake.envBuild({ id: 'wsp_1' })).resolves.toBeGreaterThan(0)
    await expect(fake.envRollback({ id: 'wsp_1' })).resolves.toBeGreaterThan(0)
    await expect(
      fake.envEdit({ id: 'wsp_1' }, 'claude', 'add go 1.24'),
    ).resolves.toEqual({ accepted: true })

    const got = await fake.envGet({ id: 'wsp_1' }, 2, 1)
    expect(got.dockerfile).toContain('FROM ubuntu:24.04')
    expect(got.manifest.length).toBeGreaterThan(0)
    expect(got.diff).toContain('diff --git')

    const detected = await fake.envHarnesses()
    expect(detected.harnesses.map((h) => h.name)).toEqual([
      'claude',
      'codex',
      'pi',
      'amp',
    ])
    expect(detected.repo_path).toBe('/src/repo')

    const results: EnvScanResult[] = []
    fake.openEnvScan(
      { harness: 'fake', mode: 'inventory' },
      {
        onOutput: () => {},
        onStatus: () => {},
        onResult: (r) => results.push(r),
        onError: () => {},
      },
    )
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(results).toHaveLength(1)
    expect(results[0].dockerfile).toContain('FROM ubuntu:24.04')
    expect(results[0].manifest[0].check_command).toBeTruthy()
  })
})
