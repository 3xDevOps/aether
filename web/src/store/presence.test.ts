import { beforeEach, describe, expect, it } from 'vitest'
import { useStore } from '@/store'

describe('presence retention', () => {
  beforeEach(() => {
    useStore.setState({ presence: [] })
  })

  it('retains the last-seen entry when a member leaves the live roster', () => {
    useStore.getState().setPresence([
      {
        member_id: 'ada',
        state: 'online',
        last_seen: '2026-09-03T12:00:00Z',
      },
    ])

    useStore.getState().setPresence([])

    expect(useStore.getState().presence).toEqual([
      {
        member_id: 'ada',
        state: 'offline',
        last_seen: '2026-09-03T12:00:00Z',
      },
    ])
  })
})
