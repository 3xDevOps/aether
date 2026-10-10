import { beforeEach, describe, expect, it } from 'vitest'
import { useStore } from '@/store'
import { runPeople, runWatchers } from '@/store/presence'
import { alice, bob, vera } from '@/test/fixtures'

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

describe('who is on a run', () => {
  const members = { [alice.id]: alice, [bob.id]: bob, [vera.id]: vera }
  const everyone = [vera.id, bob.id, alice.id]

  it('counts a member once however many workspaces list them, and never an offline one', () => {
    const watchers = runWatchers([
      { member_id: bob.id, state: 'watching', watching: ['run_1', 'run_2'], last_seen: '2026-09-03T12:00:00Z' },
      { member_id: bob.id, state: 'watching', watching: ['run_1'], last_seen: '2026-09-03T12:00:00Z' },
      { member_id: alice.id, state: 'online', last_seen: '2026-09-03T12:00:00Z' },
      { member_id: vera.id, state: 'offline', last_seen: '2026-09-03T12:00:00Z' },
    ], 'run_1')
    expect(watchers).toEqual([bob.id])
  })

  it('leads with the controller, even before the roster lists them', () => {
    expect(runPeople({ controller_member_id: vera.id }, everyone, members)).toEqual([vera.id, alice.id, bob.id])
    expect(runPeople({ controller_member_id: vera.id }, [bob.id], members)).toEqual([vera.id, bob.id])
  })

  it('leads with the last controller only while nobody controls and they are still on the run', () => {
    const free = { controller_member_id: '', last_controller_member_id: bob.id }
    expect(runPeople(free, everyone, members)).toEqual([bob.id, alice.id, vera.id])
    expect(runPeople(free, [vera.id, alice.id], members)).toEqual([alice.id, vera.id])
    expect(runPeople({}, everyone, members)).toEqual([alice.id, bob.id, vera.id])
  })
})
