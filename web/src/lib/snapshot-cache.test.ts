import { describe, expect, it } from 'vitest'
import { clearSnapshot, paintSnapshot, saveSnapshot } from '@/lib/snapshot-cache'
import type { ServerInfo } from '@/lib/types'
import { createRootStore } from '@/store'
import { run, serverInfo } from '@/test/fixtures'

describe('snapshot cache', () => {
  it('paints a saved snapshot as stale, without member git identity or disk usage', () => {
    window.localStorage.clear()
    const live = createRootStore()
    live.getState().setInfo({ ...serverInfo, member: { ...serverInfo.member, git_email: 'me@example.test' }, disk: { used_bytes: 1 } as ServerInfo['disk'] })
    live.getState().setIdentityKey('origin\u0000host\u0000m1')
    live.getState().setMembers([{ id: 'm1', display_name: 'Ann', color: '', role: 'admin', git_email: 'ann@example.test' }])
    live.getState().setHydrated(true)
    saveSnapshot(live)

    const raw = window.localStorage.getItem(`aether.snapshot.v1:${window.location.origin}`) ?? ''
    expect(raw).not.toContain('ann@example.test')
    expect(raw).not.toContain('me@example.test')
    expect(raw).not.toContain('used_bytes')

    const fresh = createRootStore()
    paintSnapshot(fresh)
    expect(fresh.getState().hydrated).toBe(true)
    expect(fresh.getState().staleSince).not.toBeNull()
    expect(fresh.getState().identityKey).toBe('origin\u0000host\u0000m1')

    clearSnapshot()
    const empty = createRootStore()
    paintSnapshot(empty)
    expect(empty.getState().hydrated).toBe(false)
  })

  it('keeps nothing in Edge mode, paints only a cold start, and drops tool activity and git origins', () => {
    window.localStorage.clear()
    const live = createRootStore()
    live.getState().setInfo(serverInfo)
    live.getState().setIdentityKey('o\u0000h\u0000m1')
    live.getState().setWorkspaces([{ id: 'w1', name: 'w', base_branch: 'main', created_at: '', origin: 'https://example.test/secret.git' }])
    live.getState().setHydrated(true)
    saveSnapshot(live)
    const raw = window.localStorage.getItem(`aether.snapshot.v1:${window.location.origin}`) ?? ''
    expect(raw).not.toContain('secret.git')
    expect(raw).not.toContain('toolActivity')

    const reconnecting = createRootStore()
    reconnecting.getState().setIdentityKey('another-server')
    paintSnapshot(reconnecting)
    expect(reconnecting.getState().hydrated).toBe(false)

    live.setState({ linkStatus: { edge_url: 'https://edge.example.test' } as never })
    saveSnapshot(live)
    expect(window.localStorage.getItem(`aether.snapshot.v1:${window.location.origin}`)).toBeNull()
  })

  it('saves the current pause and the confirmed change time', () => {
    window.localStorage.clear()
    const live = createRootStore()
    live.getState().setInfo(serverInfo)
    live.getState().setIdentityKey('o\u0000h\u0000m1')
    live.getState().setRuns([
      run({ id: 'run_1', paused: false, status_changed_at: '2026-08-14T10:30:00Z' }),
      run({ id: 'run_2', status_changed_at: undefined, finished_at: undefined }),
    ])
    live.getState().setPaused('run_1', true)
    live.getState().setHydrated(true)
    saveSnapshot(live)

    const fresh = createRootStore()
    paintSnapshot(fresh)
    expect(fresh.getState().pausedRuns.run_1).toBe(true)
    expect(fresh.getState().runs.run_1.stateChangedAt).toBe('2026-08-14T10:30:00Z')
    expect(fresh.getState().runs.run_2.stateChangedAtEstimated).toBe(true)
  })

  it('drops a damaged entry instead of failing startup', () => {
    const key = `aether.snapshot.v1:${window.location.origin}`
    window.localStorage.setItem(key, JSON.stringify({ identity: 'x', info: { member: {} }, runs: [], workspaces: [] }))
    const store = createRootStore()
    paintSnapshot(store)

    expect(store.getState().hydrated).toBe(false)
    expect(window.localStorage.getItem(key)).toBeNull()
  })
})
