import { fireEvent, render, screen } from '@testing-library/react'
import { StatusBar } from '@/components/shell/status-bar'
import type { GatewayCapabilities, Member } from '@/lib/types'
import { useStore } from '@/store'
import {
  alice,
  bob,
  serverInfo,
  serverUpdateStatus,
  updateStatus,
} from '@/test/fixtures'

/** The desktop gateway's descriptor, carrying the update verbs. */
function caps(): GatewayCapabilities {
  return {
    gateway: 'local',
    methods: ['*'],
    ws: ['events', 'attach'],
    local: ['link.status', 'update.check', 'update.apply'],
    version: 'v1.2.3',
  }
}

function seed(over: { self?: Member; update?: ReturnType<typeof updateStatus> | null } = {}) {
  useStore.setState({
    info: { ...serverInfo, member: over.self ?? alice },
    capabilities: caps(),
    connection: 'live',
    update: over.update === undefined ? updateStatus() : over.update,
    dismissedUpdates: { cli: 'v1.3.0', server: 'v1.3.0', shell: '' },
    serverUpdate: null,
    serverUpdateProgress: null,
    hydrated: true,
  })
}

// The label is the only always-visible surface, so it is the way back to a
// banner someone closed by reflex. Without an update it stays plain text.
test('the version label is plain until an update is available', () => {
  seed({ update: null })
  render(<StatusBar />)

  expect(screen.getByText(`aether ${serverInfo.server_version}`)).toBeTruthy()
  expect(screen.queryByRole('button', { name: /Update available/ })).toBeNull()
})

test('a CLI update turns the label into a button that clears the dismissals', () => {
  seed()
  render(<StatusBar />)

  const badge = screen.getByRole('button', { name: 'Update available: v1.3.0' })
  fireEvent.click(badge)

  expect(useStore.getState().dismissedUpdates).toEqual({
    cli: '',
    server: '',
    shell: '',
  })
})

// server_behind is an admin's business: a collaborator can do nothing about
// the server, so it must not put a dot on their status bar.
test('a behind server badges the label for an admin only', () => {
  const serverOnly = updateStatus({
    cli: { ...updateStatus().cli, update_available: false },
    server_version: 'v1.2.9',
    server_behind: true,
  })

  seed({ self: bob, update: serverOnly })
  const collaborator = render(<StatusBar />)
  expect(screen.queryByRole('button', { name: /Update available/ })).toBeNull()
  collaborator.unmount()

  seed({ self: alice, update: serverOnly })
  render(<StatusBar />)
  expect(screen.getByRole('button', { name: /Update available/ })).toBeTruthy()
})

// A member who cannot press the buttons still watches the terminals drop.
// The admin has the banner, which says the same thing with the controls
// attached, so the notice would only be noise there.
describe('the server update notice', () => {
  const notice = 'server update scheduled, terminals will reconnect briefly'

  test('tells a collaborator a scheduled update is coming', () => {
    seed({ self: bob })
    useStore.getState().applyServerUpdate({ phase: 'scheduled', version: 'v1.3.0' })
    render(<StatusBar />)

    expect(screen.getByText(notice)).toBeTruthy()
  })

  test('says the update is applying once it starts', () => {
    seed({ self: bob })
    useStore.getState().applyServerUpdate({ phase: 'applying', version: 'v1.3.0' })
    render(<StatusBar />)

    expect(
      screen.getByText('server update applying, terminals will reconnect briefly'),
    ).toBeTruthy()
  })

  // Someone else scheduled it before this tab loaded: the phase never came
  // over the feed, and the status answer is what carries it.
  test('picks up a pending update from the status answer', () => {
    seed({ self: bob })
    useStore.setState({
      serverUpdate: serverUpdateStatus({
        update_available: true,
        pending: {
          version: 'v1.3.0',
          requested_by: alice.id,
          requested_at: '2026-08-14T10:06:00Z',
        },
      }),
    })
    render(<StatusBar />)

    expect(screen.getByText(notice)).toBeTruthy()
  })

  test('stays away from an admin, and once the update is over', () => {
    seed({ self: alice })
    useStore.getState().applyServerUpdate({ phase: 'scheduled', version: 'v1.3.0' })
    const admin = render(<StatusBar />)
    expect(screen.queryByText(notice)).toBeNull()
    admin.unmount()

    seed({ self: bob })
    useStore.getState().applyServerUpdate({ phase: 'cancelled', version: 'v1.3.0' })
    render(<StatusBar />)
    expect(screen.queryByText(/server update/)).toBeNull()
  })
})
