import { act, fireEvent, render, screen } from '@testing-library/react'
import { ThemeEffect } from '@/components/theme'
import type { GatewayCapabilities } from '@/lib/types'
import { SettingsRoute } from '@/routes/settings'
import { createRootStore, useStore, type RootState } from '@/store'
import { runLabel } from '@/lib/status'
import { toRecord } from '@/store/runs'
import { alice, fakeApi, run, serverInfo, workspace } from '@/test/fixtures'
import { pickOption } from '@/test/select'

const active = run({ id: 'run_1', task: 'rewrite the checkout flow' })

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
    theme: 'system',
    ...extra,
  })
}

describe('settings view', () => {
  // A placeholder cannot be chosen, so picking no run must be a real option.
  it('opens the mirror panel for a run and closes it again', async () => {
    seed({ runs: { [active.id]: toRecord(active) } })
    render(<SettingsRoute params={{}} client={fakeApi()} />)

    const picker = screen.getByLabelText('Run')
    await pickOption(picker, runLabel(active))
    expect(screen.getByRole('region', { name: 'Sync' })).toBeDefined()

    await pickOption(picker, 'Pick a run')

    expect(screen.queryByRole('region', { name: 'Sync' })).toBeNull()
  })

  it('applies and persists explicit themes on a server without calling local methods', async () => {
    const client = fakeApi()
    const root = document.documentElement
    const previousClass = root.className
    const previousTheme = root.dataset.theme
    const previousScheme = root.style.colorScheme
    const previousPreference = useStore.getState().theme
    const originalMatchMedia = window.matchMedia
    const media = Object.assign(new EventTarget(), {
      matches: false,
      media: '(prefers-color-scheme: dark)',
    })
    const matchMedia = vi.spyOn(window, 'matchMedia').mockImplementation((query) =>
      query === media.media ? media as MediaQueryList : originalMatchMedia(query),
    )
    seed({
      capabilities: { gateway: 'remote', methods: ['*'], ws: ['events', 'attach'] },
    })
    const view = render(
      <>
        <ThemeEffect />
        <SettingsRoute params={{}} client={client} />
      </>,
    )
    onTestFinished(() => {
      view.unmount()
      matchMedia.mockRestore()
      useStore.setState({ theme: previousPreference })
      root.className = previousClass
      if (previousTheme === undefined) delete root.dataset.theme
      else root.dataset.theme = previousTheme
      root.style.colorScheme = previousScheme
    })
    const changeSystemTheme = (dark: boolean) => act(() => {
      media.matches = dark
      media.dispatchEvent(new Event('change'))
    })
    const theme = screen.getByLabelText('Theme')
    expect(root.classList.contains('dark')).toBe(false)
    changeSystemTheme(true)
    expect(root.classList.contains('dark')).toBe(true)

    await pickOption(theme, 'Light')
    expect(root.dataset.theme).toBe('light')
    expect(createRootStore().getState().theme).toBe('light')
    changeSystemTheme(false)
    changeSystemTheme(true)
    expect(root.classList.contains('dark')).toBe(false)

    await pickOption(theme, 'Dark')
    expect(root.dataset.theme).toBe('dark')
    expect(createRootStore().getState().theme).toBe('dark')
    changeSystemTheme(false)
    expect(root.classList.contains('dark')).toBe(true)

    await pickOption(theme, 'System')
    expect(root.dataset.theme).toBe('light')
    expect(createRootStore().getState().theme).toBe('system')
    changeSystemTheme(true)
    expect(root.dataset.theme).toBe('dark')
    expect(root.style.colorScheme).toBe('dark')
    expect(screen.queryByRole('region', { name: 'Link' })).toBeNull()
    expect(screen.queryByRole('region', { name: 'Sync daemon' })).toBeNull()
    expect(screen.queryByLabelText('Run')).toBeNull()
    for (const [method, call] of Object.entries(client)) {
      if (method.startsWith('local')) expect(call).not.toHaveBeenCalled()
    }
  })

  it('turns single-key shortcuts off and keeps the choice across a reload', () => {
    seed()
    onTestFinished(() => { useStore.setState({ singleKeyShortcuts: true }) })
    render(<SettingsRoute params={{}} client={fakeApi()} />)

    const toggle = screen.getByRole('checkbox', { name: 'Single-key shortcuts' })
    expect(toggle.getAttribute('aria-checked')).toBe('true')
    fireEvent.click(toggle)

    expect(toggle.getAttribute('aria-checked')).toBe('false')
    expect(createRootStore().getState().singleKeyShortcuts).toBe(false)
  })

  it('installs the daemon and shows the unit path and enable note', async () => {
    const client = fakeApi()
    seed()
    render(<SettingsRoute params={{}} client={client} />)

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
    expect(client.localDaemonInstall).toHaveBeenCalledWith('host:2222', '/src/repo')
  })
  it('opens a sync overlay without requesting unavailable daemon methods', async () => {
    const client = fakeApi()
    seed({
      capabilities: {
        ...localCaps,
        local: localCaps.local?.filter((verb) => !verb.startsWith('daemon.')),
      },
      runs: { [active.id]: toRecord(active) },
    })
    render(<SettingsRoute params={{}} client={client} />)

    await pickOption(screen.getByLabelText('Run'), runLabel(active))
    expect(screen.getByRole('region', { name: 'Sync' })).toBeDefined()
    expect(client.localDaemonStatus).not.toHaveBeenCalled()
    expect(client.localDaemonInstall).not.toHaveBeenCalled()
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

    expect(await screen.findByText('prod')).toBeDefined()
    expect(screen.getByText('staging')).toBeDefined()
    expect(screen.getByText('active')).toBeDefined()
    const switches = screen.getAllByRole('button', { name: 'Switch' })
    expect(switches).toHaveLength(1)

    // The SSH identity is process-lifetime, so switching is a restart.
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
