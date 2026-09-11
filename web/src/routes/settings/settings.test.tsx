import { fireEvent, render, screen } from '@testing-library/react'
import type { GatewayCapabilities } from '@/lib/types'
import { SettingsRoute } from '@/routes/settings'
import { useStore, type RootState } from '@/store'
import { runLabel } from '@/lib/status'
import { toRecord } from '@/store/runs'
import { alice, fakeApi, run, serverInfo, workspace } from '@/test/fixtures'
import { pickOption } from '@/test/select'

const active = run({ id: 'run_1', task: 'rewrite the checkout flow' })

// The local gateway's descriptor: the client-machine verbs settings rides on.
const localCaps: GatewayCapabilities = {
  gateway: 'local',
  methods: ['*'],
  ws: ['events', 'attach', 'terminal'],
  local: [
    'link.status',
    'sync.start',
    'sync.stop',
    'sync.status',
    'daemon.status',
    'daemon.install',
  ],
}

function seed(extra: Partial<RootState> = {}) {
  useStore.setState({
    workspaces: { [workspace.id]: workspace },
    activeWorkspace: workspace.id,
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
  // Picking a run is what opens the mirror panel, so picking none again has
  // to be a choice a reader can make; it was an option row before the select
  // became a primitive and a placeholder cannot be chosen.
  it('opens the mirror panel for a run and closes it again', async () => {
    seed({ runs: { [active.id]: toRecord(active) } })
    render(<SettingsRoute params={{}} client={fakeApi()} />)

    const picker = screen.getByLabelText('Run')
    await pickOption(picker, runLabel(active))
    expect(screen.getByRole('region', { name: 'Sync' })).toBeDefined()

    await pickOption(picker, 'Pick a run')

    expect(screen.queryByRole('region', { name: 'Sync' })).toBeNull()
  })

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
  it('renders with overlay capability when the daemon is unavailable', () => {
    seed({
      capabilities: {
        ...localCaps,
        local: localCaps.local?.filter((verb) => !verb.startsWith('daemon.')),
      },
      runs: { [active.id]: toRecord(active) },
    })
    render(<SettingsRoute params={{}} client={fakeApi()} />)

    expect(
      screen.getByRole('heading', { name: 'Mirror run files to your repository' }),
    ).toBeDefined()
    expect(screen.queryByText('Machine settings are unavailable here')).toBeNull()
  })

  it('lists named servers, marks the active one, and shows the switch instruction', async () => {
    const client = fakeApi({
      localLinkStatus: vi.fn(async () => ({
        server_configured: true,
        linked: true,
        addr: 'host:2222',
        user: 'alice',
        repo: '/src/repo',
        links: [
          { name: 'prod', addr: 'host:2222' },
          { name: 'staging', addr: 'staging:2222' },
        ],
        active: 'prod',
      })),
    })
    seed()
    render(<SettingsRoute params={{}} client={client} />)

    // Both profiles render; the active one is marked, not switchable.
    expect(await screen.findByText('prod')).toBeDefined()
    expect(screen.getByText('staging')).toBeDefined()
    expect(screen.getByText('active')).toBeDefined()
    const switches = screen.getAllByRole('button', { name: 'Switch' })
    expect(switches).toHaveLength(1)

    // Switching calls link.switch and renders the server's refusal verbatim:
    // the SSH identity is process-lifetime, so switching is a restart.
    fireEvent.click(switches[0])
    expect(
      await screen.findByText(
        'restart aether gui --server staging to switch servers',
      ),
    ).toBeDefined()
    expect(client.localLinkSwitch).toHaveBeenCalledWith('staging')
  })
  it('separates a configured server from its missing repository', async () => {
    const client = fakeApi({
      localLinkStatus: vi.fn(async () => ({
        server_configured: true,
        linked: false,
        addr: 'host:2222',
        user: 'alice',
        repo: '',
      })),
    })
    seed()
    render(<SettingsRoute params={{}} client={client} />)

    expect(await screen.findByText('host:2222')).toBeDefined()
    expect(screen.getByText('alice')).toBeDefined()
    expect(screen.getByText(/No repository linked/)).toBeDefined()
    expect(screen.queryByText(/No server configured/)).toBeNull()
  })
})
