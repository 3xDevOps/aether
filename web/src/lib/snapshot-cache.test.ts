import { describe, expect, it } from 'vitest'
import { clearSnapshot, paintSnapshot, saveSnapshot } from '@/lib/snapshot-cache'
import type { ServerInfo } from '@/lib/types'
import { createRootStore } from '@/store'
import { serverInfo } from '@/test/fixtures'

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
})
