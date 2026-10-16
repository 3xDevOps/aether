import { describe, expect, it, vi } from 'vitest'
import { ApiError, onUpgradeRequired, upgradeRequired } from '@/lib/api'
import { createRootStore } from '@/store'
import {
  applyClientUpdate,
  checkForUpdates,
  clientTarget,
  SERVER_UPDATE_TIMEOUT_MS,
  serverTarget,
} from '@/store/updates'
import { fakeApi, serverInfo, serverUpdateStatus, updateStatus } from '@/test/fixtures'

function store() {
  const s = createRootStore()
  s.setState({ info: serverInfo })
  return s
}

const refusal = () =>
  new ApiError(426, 'workspace.list: this aether is v1.2.3 and the server runs v1.3.0; update this client first', -32005, {
    client_version: 'v1.2.3',
    server_version: 'v1.3.0',
    min_client_version: 'v1.3.0',
    command: 'aether update',
  })

describe('upgradeRequired', () => {
  it('reads the code or the 426, keeping the server words without the method', () => {
    expect(upgradeRequired(refusal())).toEqual({
      message: 'this aether is v1.2.3 and the server runs v1.3.0; update this client first',
      client_version: 'v1.2.3',
      server_version: 'v1.3.0',
      min_client_version: 'v1.3.0',
      command: 'aether update',
    })
    expect(upgradeRequired(new ApiError(426, 'x: old', undefined, undefined))?.command).toBe('aether update')
    expect(upgradeRequired(new ApiError(503, 'x: down', -32000))).toBeNull()
    expect(upgradeRequired(new Error('x'))).toBeNull()
  })

  it('reaches the listener from any call', async () => {
    const heard = vi.fn()
    onUpgradeRequired(heard)
    vi.stubGlobal(
      'fetch',
      vi.fn(async () =>
        new Response(JSON.stringify({ error: { code: -32005, message: 'update first', data: { server_version: 'v1.3.0' } } }), {
          status: 426,
        }),
      ),
    )
    const { api } = await import('@/lib/api')
    await expect(api.runList()).rejects.toThrow('update first')
    expect(heard).toHaveBeenCalledWith(expect.objectContaining({ message: 'update first', server_version: 'v1.3.0' }))
    vi.unstubAllGlobals()
    onUpgradeRequired(() => {})
  })
})

describe('clientTarget', () => {
  it('walks unknown, up to date, available, check failed and required', () => {
    const s = store()
    expect(clientTarget(s.getState())).toEqual({ state: 'unknown' })

    const current = updateStatus({ cli: { ...updateStatus().cli, latest: 'v1.2.3', update_available: false } })
    s.getState().setUpdate(current)
    expect(clientTarget(s.getState())).toEqual({ state: 'up_to_date', version: 'v1.2.3', checkedAt: '2026-08-14T10:00:00Z' })

    s.setState({ clientCheckError: 'update.check: github.com: no such host' })
    expect(clientTarget(s.getState())).toMatchObject({ state: 'check_failed', error: 'update.check: github.com: no such host' })

    s.getState().setUpdate(updateStatus())
    expect(clientTarget(s.getState())).toMatchObject({ state: 'available', latest: 'v1.3.0', notesUrl: expect.stringContaining('v1.3.0') })

    s.getState().setUpdateRequired(upgradeRequired(refusal()))
    expect(clientTarget(s.getState())).toMatchObject({ state: 'required', server_version: 'v1.3.0', min_client_version: 'v1.3.0' })
  })

  it('takes required from update.check before any call is refused', () => {
    const s = store()
    s.getState().setUpdate(updateStatus({ client_update_required: true, min_client_version: 'v1.3.0' }))
    expect(clientTarget(s.getState())).toMatchObject({ state: 'required', client_version: 'v1.2.3', min_client_version: 'v1.3.0' })
  })
})

describe('serverTarget', () => {
  it('puts the server first through every phase and ends on the version it was about', () => {
    const s = store()
    const now = Date.now()
    s.getState().setServerUpdate(serverUpdateStatus({ update_available: true, notes_url: 'https://example.invalid/notes' }))
    expect(serverTarget(s.getState(), now)).toMatchObject({ state: 'available', version: 'v1.2.3', latest: 'v1.3.0', notesUrl: 'https://example.invalid/notes' })

    s.getState().applyServerUpdate({ phase: 'preflight', version: 'v1.3.0' })
    expect(serverTarget(s.getState(), now)).toMatchObject({ state: 'applying', step: 'preflight' })
    s.getState().applyServerUpdate({ phase: 'restarting', version: 'v1.3.0' })
    // A late frame from earlier in the same update does not move it back.
    s.getState().applyServerUpdate({ phase: 'applying', version: 'v1.3.0' })
    expect(serverTarget(s.getState(), now)).toMatchObject({ state: 'restarting', latest: 'v1.3.0' })

    s.getState().setServerUpdate(
      serverUpdateStatus({ server_version: 'v1.3.0', latest_version: 'v1.3.0', update_available: false, phase: 'applied', checked_at: '2026-08-14T11:00:00Z' }),
    )
    expect(s.getState().serverUpdateProgress).toBeNull()
    expect(serverTarget(s.getState(), now)).toEqual({ state: 'up_to_date', version: 'v1.3.0', checkedAt: '2026-08-14T11:00:00Z' })
  })

  it('ends an older server on a newer version, not only an exact match', () => {
    const s = store()
    s.getState().applyServerUpdate({ phase: 'restarting', version: 'v1.3.0' })
    s.getState().setServerUpdate(serverUpdateStatus({ server_version: 'v1.3.1', update_available: false }))
    expect(s.getState().serverUpdateProgress).toBeNull()
  })

  it('times a restart that never comes back out into the last known error', () => {
    const s = store()
    s.getState().setServerUpdate(serverUpdateStatus({ update_available: true }))
    s.getState().applyServerUpdate({ phase: 'restarting', version: 'v1.3.0' })
    s.getState().setServerUpdateFailed('server.update_status: server unreachable: connection refused')
    const started = s.getState().serverUpdateProgressAt
    expect(serverTarget(s.getState(), started + SERVER_UPDATE_TIMEOUT_MS - 1).state).toBe('restarting')
    expect(serverTarget(s.getState(), started + SERVER_UPDATE_TIMEOUT_MS + 1)).toEqual({
      state: 'failed',
      latest: 'v1.3.0',
      error:
        'The server did not finish updating to v1.3.0 within 10 minutes. Last error: server.update_status: server unreachable: connection refused',
    })
  })

  it('shows a rollback and a failed check verbatim', () => {
    const s = store()
    s.getState().setServerUpdate(
      serverUpdateStatus({ update_available: true, phase: 'rolled_back', error: 'new process exited: migrate: no such table', to_version: 'v1.3.0' }),
    )
    expect(serverTarget(s.getState(), Date.now())).toEqual({ state: 'rolled_back', latest: 'v1.3.0', error: 'new process exited: migrate: no such table' })

    s.getState().setServerUpdate(
      serverUpdateStatus({ latest: 'v1.2.3', update_available: false, check_error: 'api.github.com: 403 rate limited', checked_at: '2026-08-14T09:00:00Z' }),
    )
    expect(serverTarget(s.getState(), Date.now())).toEqual({
      state: 'check_failed',
      error: 'api.github.com: 403 rate limited',
      checkedAt: '2026-08-14T09:00:00Z',
      version: 'v1.2.3',
    })
  })

  it('reads a status in flight as progress the timeout can end', () => {
    const s = store()
    s.getState().setServerUpdate(serverUpdateStatus({ update_available: true, phase: 'verifying', to_version: 'v1.3.0' }))
    expect(s.getState().serverUpdateProgress).toEqual({ phase: 'verifying', version: 'v1.3.0' })
    expect(serverTarget(s.getState(), Date.now()).state).toBe('restarting')
  })
})

describe('checkForUpdates', () => {
  it('skips both caches and records a failure instead of swallowing it', async () => {
    const s = store()
    const client = fakeApi({
      localUpdateCheck: vi.fn(async () => {
        throw new ApiError(502, 'update.check: github.com: no such host')
      }),
    })
    await checkForUpdates(s, client, { client: true, server: true })
    expect(client.localUpdateCheck).toHaveBeenCalledWith(true)
    expect(client.serverUpdateStatus).toHaveBeenCalledWith(true)
    expect(s.getState().clientCheckError).toBe('update.check: github.com: no such host')
    expect(s.getState().clientChecking).toBe(false)
    expect(s.getState().serverChecking).toBe(false)
  })
})

describe('checkClient', () => {
  it('keeps the last release when the lookup fails, and says why', async () => {
    const s = store()
    s.getState().setUpdate(updateStatus())
    const client = fakeApi({
      localUpdateCheck: vi.fn(async () => ({
        ...updateStatus({ client_update_required: false }),
        cli: { ...updateStatus().cli, latest: undefined, update_available: false },
        cli_error: 'check for releases: api.github.com: 403 rate limit exceeded',
      })),
    })
    await checkForUpdates(s, client, { client: true, server: false })
    expect(s.getState().update?.cli.latest).toBe('v1.3.0')
    expect(s.getState().clientCheckError).toBe('check for releases: api.github.com: 403 rate limit exceeded')
    expect(clientTarget(s.getState()).state).toBe('available')
  })
})

describe('applyClientUpdate', () => {
  it('applies on a gated client even when the release list has nothing newer', async () => {
    const s = store()
    s.getState().setUpdateRequired(upgradeRequired(refusal()))
    const current = updateStatus({ cli: { ...updateStatus().cli, update_available: false } })
    const client = fakeApi({
      localUpdateCheck: vi.fn(async () => current),
      localUpdateApply: vi.fn(async () => ({ updated: ['/usr/local/bin/aether'], version: 'v1.3.0', restarting: false, rebuilding: false })),
    })
    await applyClientUpdate(s, client)
    expect(client.localUpdateApply).toHaveBeenCalled()
    expect(clientTarget(s.getState())).toEqual({ state: 'ready_to_restart', latest: 'v1.3.0' })
  })

  it('keeps the real error', async () => {
    const s = store()
    const client = fakeApi({
      localUpdateApply: vi.fn(async () => {
        throw new ApiError(500, 'update.apply: aether-release.json: no signed manifest for v1.3.0; the tag predates signed releases')
      }),
    })
    await applyClientUpdate(s, client)
    expect(clientTarget(s.getState())).toMatchObject({
      state: 'failed',
      error: 'update.apply: aether-release.json: no signed manifest for v1.3.0; the tag predates signed releases',
    })
  })
})
