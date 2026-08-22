import { fireEvent, render, screen } from '@testing-library/react'
import type { GatewayCapabilities } from '@/lib/types'
import { SettingsRoute } from '@/routes/settings'
import { useStore, type RootState } from '@/store'
import { alice, fakeApi, serverInfo, session } from '@/test/fixtures'

// The local gateway's descriptor: the client-machine verbs settings rides on.
const localCaps: GatewayCapabilities = {
  gateway: 'local',
  methods: ['*'],
  ws: ['events', 'attach', 'shell'],
  local: [
    'link.status',
    'sync.start',
    'sync.stop',
    'sync.status',
    'daemon.status',
    'daemon.install',
    'image.scaffold',
  ],
}

function seed(extra: Partial<RootState> = {}) {
  useStore.setState({
    sessions: { [session.id]: session },
    members: { [alice.id]: alice },
    runs: {},
    syncSessions: {},
    linkStatus: null,
    info: serverInfo,
    capabilities: localCaps,
    hydrated: true,
    hydrationError: null,
    route: { name: 'settings', params: {} },
    ...extra,
  })
}

describe('settings view', () => {
  it('renders the desktop-only empty state on a remote gateway', () => {
    // The remote descriptor has no local verbs, so there is nothing to manage.
    seed({
      capabilities: { gateway: 'remote', methods: ['*'], ws: ['events', 'attach'] },
    })
    render(<SettingsRoute params={{}} client={fakeApi()} />)

    expect(screen.getByText(/aether gui/)).toBeDefined()
    expect(screen.queryByRole('region', { name: 'Link' })).toBeNull()
  })

  it('installs the daemon and shows the unit path and enable note', async () => {
    const client = fakeApi()
    seed()
    render(<SettingsRoute params={{}} client={client} />)

    // link.status prefills the form; the button enables once both land.
    const install = await screen.findByRole('button', { name: 'Install' })
    expect(
      screen.getByRole('form', { name: 'Install sync daemon' }),
    ).toBeDefined()
    await vi.waitFor(() => {
      expect((install as HTMLButtonElement).disabled).toBe(false)
    })
    fireEvent.click(install)

    expect(
      await screen.findByText('/home/alice/.config/systemd/user/aether-sync.service'),
    ).toBeDefined()
    const note = screen.getByLabelText<HTMLInputElement>('Enable command')
    expect(note.value).toBe(
      'enable with systemctl --user enable --now aether-sync',
    )
    // The prefill came from link.status, through the store mirror.
    expect(client.localDaemonInstall).toHaveBeenCalledWith('host:2222', '/src/repo')
  })
})
