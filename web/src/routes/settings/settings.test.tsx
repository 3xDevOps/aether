import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { ThemeEffect } from '@/components/theme'
import type { GatewayCapabilities } from '@/lib/types'
import { SettingsRoute } from '@/routes/settings'
import { categories, entries } from '@/routes/settings/registry'
import { ServerSection } from '@/routes/settings/server'
import { createRootStore, useStore, type RootState } from '@/store'
import { runLabel } from '@/lib/status'
import { toRecord } from '@/store/runs'
import { alice, bob, fakeApi, run, serverInfo, updateStatus, workspace } from '@/test/fixtures'
import { pickOption } from '@/test/select'
import { atViewport } from '@/test/viewport'

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
    daemonInstalled: null,
    ...extra,
  })
}

describe('settings view', () => {
  it('offers the newest live run for mirroring and Change picks another', async () => {
    const older = run({ id: 'run_0', task: 'tidy the README', created_at: '2026-01-01T00:00:00Z' })
    const newer = { ...active, created_at: '2026-02-01T00:00:00Z' }
    const client = fakeApi()
    seed({ runs: { [older.id]: toRecord(older), [newer.id]: toRecord(newer) } })
    render(<SettingsRoute params={{ section: 'this-computer' }} client={client} />)

    expect(screen.getByText(runLabel(newer))).toBeDefined()
    expect(screen.getByRole('region', { name: 'Sync' })).toBeDefined()

    fireEvent.click(screen.getByRole('button', { name: 'Change' }))
    const option = await screen.findByRole('option', { name: runLabel(older) })
    option.focus()
    fireEvent.keyDown(option, { key: 'Enter' })
    expect(await screen.findByText(runLabel(older))).toBeDefined()
    fireEvent.click(await screen.findByRole('button', { name: 'Start mirroring' }))

    expect(client.localSyncStart).toHaveBeenCalledWith(older.id)
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
    expect(screen.queryByRole('region', { name: 'This computer' })).toBeNull()
    expect(screen.queryByLabelText('Mirror run files')).toBeNull()
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

  it('applies a larger text size to the document and keeps it across a reload', async () => {
    seed()
    const root = document.documentElement
    onTestFinished(() => {
      useStore.setState({ textSize: 'default' })
      delete root.dataset.textSize
    })
    render(
      <>
        <ThemeEffect />
        <SettingsRoute params={{}} client={fakeApi()} />
      </>,
    )
    expect(root.dataset.textSize).toBe('default')
    await pickOption(screen.getByLabelText('Text size'), 'Larger')
    expect(root.dataset.textSize).toBe('larger')
    expect(createRootStore().getState().textSize).toBe('larger')
  })

  it('offers an admin the server update and frees retained containers through the board dialog', async () => {
    const retained = run({ id: 'run_r', status: 'merged', reason: 'closed; retained container' })
    seed({
      runs: { [retained.id]: toRecord(retained) },
      capabilities: { ...localCaps, local: [...(localCaps.local ?? []), 'update.check'] },
      update: updateStatus(),
    })
    render(<SettingsRoute params={{ section: 'server' }} client={fakeApi()} />)

    const server = screen.getByRole('region', { name: 'Server' })
    fireEvent.click(within(server).getByRole('button', { name: 'Update…' }))
    expect(useStore.getState().updatesOpen).toBe(true)
    fireEvent.click(within(server).getByRole('button', { name: 'Free retained containers…' }))
    expect(useStore.getState().paletteDialog).toBe('release-finished')
  })

  it('tells a packaged CLI where its updates come from', () => {
    const status = updateStatus()
    seed({
      capabilities: { ...localCaps, local: [...(localCaps.local ?? []), 'update.check'] },
      update: { ...status, cli: { ...status.cli, packaged: 'Android app', update_available: false, latest: '' } },
    })
    render(<SettingsRoute params={{ section: 'server' }} client={fakeApi()} />)

    const server = screen.getByRole('region', { name: 'Server' })
    expect(within(server).getByText(/Updates come from the Android app: install its new version to update Aether\./)).toBeDefined()
    expect(within(server).queryByRole('button', { name: 'Update…' })).toBeNull()
  })

  it("shows why the server's tailnet dashboard is not serving, verbatim", () => {
    const refusal = 'servergw: HTTPS certificate for box.tail.ts.net: cert box.tail.ts.net: status 500: your Tailscale account does not support getting TLS certs; enable MagicDNS and HTTPS certificates for the tailnet in the Tailscale admin console (DNS page), or set web-port to 0'
    seed({ info: { ...serverInfo, tailnet_dashboard_error: refusal } })
    render(<SettingsRoute params={{ section: 'server' }} client={fakeApi()} />)
    const server = screen.getByRole('region', { name: 'Server' })
    expect(within(server).getByText('Tailnet dashboard')).toBeDefined()
    expect(within(server).getByText(refusal)).toBeDefined()
    expect(within(server).getByText('sudo systemctl restart aether-server')).toBeDefined()
  })

  it('re-reads whether the tailnet dashboard serves, since the server keeps retrying it', async () => {
    const refusal = 'servergw: listen: no tailnet address could be bound'
    seed()
    const serverInfoRead = vi.fn(async () => ({ ...serverInfo, tailnet_dashboard_error: refusal }))
    const { unmount } = render(<ServerSection client={fakeApi({ serverInfo: serverInfoRead })} />)
    expect(await screen.findByText(refusal)).toBeDefined()
    unmount()

    seed({ info: { ...serverInfo, tailnet_dashboard_error: refusal } })
    render(<ServerSection client={fakeApi()} />)
    await waitFor(() => expect(screen.queryByText('Tailnet dashboard')).toBeNull())
  })

  it('keeps the server admin rows from a collaborator', () => {
    seed({ info: { ...serverInfo, member: bob } })
    render(<SettingsRoute params={{ section: 'server' }} client={fakeApi()} />)
    const server = screen.getByRole('region', { name: 'Server' })
    expect(within(server).getByText('Version')).toBeDefined()
    expect(within(server).queryByText('Retained containers')).toBeNull()
  })

  it('leaves disk figures to Insights › System and links there', () => {
    seed()
    render(<SettingsRoute params={{ section: 'server' }} client={fakeApi()} />)
    const server = screen.getByRole('region', { name: 'Server' })
    expect(within(server).queryByLabelText('Disk usage')).toBeNull()
    expect(within(server).queryByText('Largest storage owners')).toBeNull()

    fireEvent.click(within(server).getByRole('button', { name: 'Open Insights › System' }))

    expect(useStore.getState().route).toEqual({ name: 'insights', params: { tab: 'system' } })
  })

  it('shows the linked server and repository read-only until Change', async () => {
    seed()
    render(<SettingsRoute params={{ section: 'this-computer' }} client={fakeApi()} />)

    const form = await screen.findByRole('form', { name: 'Install sync daemon' })
    await within(form).findByText('/src/repo')
    expect(within(form).queryByRole('textbox')).toBeNull()

    fireEvent.click(within(form).getByRole('button', { name: 'Change' }))

    expect((within(form).getByLabelText('Repository') as HTMLInputElement).value).toBe('/src/repo')
  })

  it('installs the daemon and shows the unit path and enable note', async () => {
    const client = fakeApi()
    seed()
    render(<SettingsRoute params={{ section: 'this-computer' }} client={client} />)

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
    expect(screen.getByText('enable with systemctl --user enable --now aether-sync')).toBeDefined()
    expect(client.localDaemonInstall).toHaveBeenCalledWith('host:2222', '/src/repo')
  })

  it('keeps the daemon enable note across categories until a read finds no daemon', async () => {
    const client = fakeApi()
    seed()
    const { rerender } = render(<SettingsRoute params={{ section: 'this-computer' }} client={client} />)
    const install = await screen.findByRole('button', { name: 'Install' })
    await vi.waitFor(() => expect((install as HTMLButtonElement).disabled).toBe(false))
    fireEvent.click(install)
    await screen.findByText('enable with systemctl --user enable --now aether-sync')

    const unit = '/home/alice/.config/systemd/user/aether-sync.service'
    vi.mocked(client.localDaemonStatus).mockResolvedValueOnce({ installed: true, unit_path: unit })
    rerender(<SettingsRoute params={{ section: 'general' }} client={client} />)
    expect(screen.queryByText('enable with systemctl --user enable --now aether-sync')).toBeNull()
    rerender(<SettingsRoute params={{ section: 'this-computer' }} client={client} />)
    expect(await screen.findByText('enable with systemctl --user enable --now aether-sync')).toBeDefined()

    rerender(<SettingsRoute params={{ section: 'general' }} client={client} />)
    rerender(<SettingsRoute params={{ section: 'this-computer' }} client={client} />)
    expect(await screen.findByRole('button', { name: 'Install' })).toBeDefined()
    expect(screen.queryByText('enable with systemctl --user enable --now aether-sync')).toBeNull()
  })

  it('keeps a daemon install that lands while a stale read is in flight', async () => {
    const client = fakeApi()
    seed()
    const { rerender } = render(<SettingsRoute params={{ section: 'this-computer' }} client={client} />)
    const install = await screen.findByRole('button', { name: 'Install' })
    await vi.waitFor(() => expect((install as HTMLButtonElement).disabled).toBe(false))
    let finishInstall!: () => void
    vi.mocked(client.localDaemonInstall).mockReturnValueOnce(new Promise((resolve) => {
      finishInstall = () => resolve({ unit_path: '/home/alice/.config/systemd/user/aether-sync.service', note: 'enable with systemctl --user enable --now aether-sync' })
    }))
    fireEvent.click(install)

    let finishRead!: () => void
    vi.mocked(client.localDaemonStatus).mockReturnValueOnce(new Promise((resolve) => {
      finishRead = () => resolve({ installed: false, unit_path: '' })
    }))
    rerender(<SettingsRoute params={{ section: 'general' }} client={client} />)
    rerender(<SettingsRoute params={{ section: 'this-computer' }} client={client} />)
    await act(async () => finishInstall())
    await act(async () => finishRead())

    expect(screen.getByText('enable with systemctl --user enable --now aether-sync')).toBeDefined()
    expect(screen.queryByRole('button', { name: 'Install' })).toBeNull()
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
    render(<SettingsRoute params={{ section: 'this-computer' }} client={client} />)

    expect(screen.getByRole('region', { name: 'Sync' })).toBeDefined()
    expect(client.localDaemonStatus).not.toHaveBeenCalled()
    expect(client.localDaemonInstall).not.toHaveBeenCalled()
  })

  describe('clear local state', () => {
    const reload = vi.fn()

    beforeEach(() => {
      reload.mockClear()
      vi.stubGlobal('location', { ...window.location, reload })
    })
    afterEach(() => vi.unstubAllGlobals())

    it('is the last section and resets only after confirmation', async () => {
      const client = fakeApi()
      seed({ capabilities: { ...localCaps, local: [...(localCaps.local ?? []), 'link.reset'] } })
      render(<SettingsRoute params={{ section: 'this-computer' }} client={client} />)

      const headings = screen.getAllByRole('heading', { level: 3 })
      expect(headings[headings.length - 1].textContent).toBe('Danger zone')
      fireEvent.click(screen.getByRole('button', { name: 'Clear local state' }))
      expect(client.localLinkReset).not.toHaveBeenCalled()

      fireEvent.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Clear local state' }))
      await waitFor(() => expect(reload).toHaveBeenCalled())
      expect(client.localLinkReset).toHaveBeenCalledTimes(1)
    })

    it('keeps the refusal on screen and does not reload', async () => {
      const client = fakeApi({
        localLinkReset: vi.fn(async () => {
          throw new Error('delete /config/aether: permission denied')
        }),
      })
      seed({ capabilities: { ...localCaps, local: [...(localCaps.local ?? []), 'link.reset'] } })
      render(<SettingsRoute params={{ section: 'this-computer' }} client={client} />)

      fireEvent.click(screen.getByRole('button', { name: 'Clear local state' }))
      fireEvent.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Clear local state' }))
      expect(await screen.findByText('delete /config/aether: permission denied')).toBeDefined()
      expect(reload).not.toHaveBeenCalled()
    })

    it('is absent without the link.reset verb', () => {
      seed()
      render(<SettingsRoute params={{ section: 'this-computer' }} client={fakeApi()} />)
      expect(screen.queryByRole('button', { name: 'Clear local state' })).toBeNull()
    })
  })

  describe('saved servers', () => {
    const status = {
      server_configured: true,
      linked: true,
      addr: 'edge:2222',
      user: 'alice',
      repo: '/src/repo',
      default: { name: '' as const, addr: 'edge:2222' },
      links: [
        { name: 'prod', addr: 'host:2222' },
        { name: 'staging', addr: 'staging:2222' },
      ],
      active: 'prod',
    }
    const reload = vi.fn()

    beforeEach(() => {
      reload.mockClear()
      vi.stubGlobal('location', { ...window.location, reload })
    })
    afterEach(() => vi.unstubAllGlobals())

    it('lists the default and named servers, marks the active one, and reloads after a switch', async () => {
      const client = fakeApi({ localLinkStatus: vi.fn(async () => status) })
      seed()
      render(<SettingsRoute params={{ section: 'this-computer' }} client={client} />)

      expect(await screen.findByText('Default')).toBeDefined()
      expect(screen.getByText('prod')).toBeDefined()
      expect(screen.getAllByText('Active')).toHaveLength(1)
      const switches = screen.getAllByRole('button', { name: 'Switch' })
      expect(switches).toHaveLength(2)

      fireEvent.click(switches[0])
      await waitFor(() => expect(reload).toHaveBeenCalled())
      expect(client.localLinkSwitch).toHaveBeenCalledWith('')
    })

    it('shows the gateway refusal verbatim and does not reload', async () => {
      const client = fakeApi({
        localLinkStatus: vi.fn(async () => status),
        localLinkSwitch: vi.fn(async () => {
          throw new Error('server staging:2222 refused the key')
        }),
      })
      seed()
      render(<SettingsRoute params={{ section: 'this-computer' }} client={client} />)

      fireEvent.click((await screen.findAllByRole('button', { name: 'Switch' }))[1])
      expect(await screen.findByText('server staging:2222 refused the key')).toBeDefined()
      expect(reload).not.toHaveBeenCalled()
    })

    it('opens onboarding on the server picker from Link another server', async () => {
      const client = fakeApi({ localLinkStatus: vi.fn(async () => ({ ...status, active: '' })) })
      seed({ capabilities: { ...localCaps, local: [...(localCaps.local ?? []), 'edge.link'] } })
      render(<SettingsRoute params={{ section: 'this-computer' }} client={client} />)

      fireEvent.click(await screen.findByRole('button', { name: 'Link another server' }))
      expect(useStore.getState().route).toEqual({ name: 'onboarding', params: { link: 'another' } })
    })

    it('keeps Link another server available on a named server, so an offline Default cannot block it', async () => {
      const client = fakeApi({ localLinkStatus: vi.fn(async () => status) })
      seed({ capabilities: { ...localCaps, local: [...(localCaps.local ?? []), 'edge.link'] } })
      render(<SettingsRoute params={{ section: 'this-computer' }} client={client} />)

      const button = await screen.findByRole('button', { name: 'Link another server' })
      expect((button as HTMLButtonElement).disabled).toBe(false)
      fireEvent.click(button)
      expect(useStore.getState().route).toEqual({ name: 'onboarding', params: { link: 'another' } })
    })
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
    render(<SettingsRoute params={{ section: 'this-computer' }} client={client} />)

    expect(await screen.findByText('host:2222')).toBeDefined()
    expect(screen.getByText(/as alice/)).toBeDefined()
    expect(screen.getByText('No clone linked to this server.')).toBeDefined()
    expect(screen.queryByText(/No server yet/)).toBeNull()
  })

  describe('navigation', () => {
    const everything: GatewayCapabilities = { ...localCaps, local: [...(localCaps.local ?? []), 'link.reset'] }

    it('renders an anchor for every entry its category lists, and lists every anchored section', () => {
      for (const category of categories) {
        seed({ capabilities: everything })
        const view = render(<SettingsRoute params={{ section: category.id }} client={fakeApi()} />)
        for (const entry of entries.filter((e) => e.category === category.id && !e.link)) {
          expect(document.getElementById(entry.id), entry.id).not.toBeNull()
        }
        for (const section of view.container.querySelectorAll('section[id]')) {
          expect(entries.some((e) => e.id === section.id), section.id).toBe(true)
        }
        view.unmount()
      }
    })

    it('lands on General and moves to the category a nav item names', () => {
      seed()
      const { rerender } = render(<SettingsRoute params={{}} client={fakeApi()} />)
      const nav = screen.getByRole('navigation', { name: 'Settings' })
      expect(within(nav).getByRole('button', { name: 'General' }).getAttribute('aria-current')).toBe('page')
      expect(screen.getByRole('heading', { level: 2, name: 'General' })).toBeDefined()

      fireEvent.click(within(nav).getByRole('button', { name: 'Server' }))
      expect(useStore.getState().route).toEqual({ name: 'settings', params: { section: 'server' } })
      rerender(<SettingsRoute params={{ section: 'server' }} client={fakeApi()} />)
      expect(document.activeElement).toBe(screen.getByRole('heading', { level: 2, name: 'Server' }))
    })

    it('finds a setting by keyword and opens it at its control', () => {
      seed()
      render(<SettingsRoute params={{}} client={fakeApi()} />)
      const search = screen.getByRole('searchbox', { name: 'Search settings' })
      fireEvent.change(search, { target: { value: 'dm' } })
      expect(screen.getByRole('status').textContent).toBe('1 setting')
      fireEvent.change(search, { target: { value: 'nothing like this' } })
      expect(screen.getByRole('status').textContent).toBe('No settings match “nothing like this”')
      fireEvent.change(search, { target: { value: 'dark' } })
      fireEvent.keyDown(search, { key: 'Enter' })
      expect(useStore.getState().route).toEqual({ name: 'settings', params: { section: 'theme' } })
      expect((search as HTMLInputElement).value).toBe('')
    })

    it('scrolls a deep-linked setting into view and focuses its control', () => {
      const scroll = vi.mocked(Element.prototype.scrollIntoView)
      scroll.mockClear()
      seed()
      render(<SettingsRoute params={{ section: 'keyboard' }} client={fakeApi()} />)
      expect(scroll.mock.contexts.at(-1)).toBe(document.getElementById('keyboard'))
      expect(document.activeElement).toBe(screen.getByRole('checkbox', { name: 'Single-key shortcuts' }))
    })

    it('reveals a linked row that mounts once the server info arrives', async () => {
      const scroll = vi.mocked(Element.prototype.scrollIntoView)
      scroll.mockClear()
      seed({ info: null })
      render(<SettingsRoute params={{ section: 'disk' }} client={fakeApi()} />)
      expect(document.getElementById('disk')).toBeNull()
      act(() => useStore.setState({ info: serverInfo }))
      await waitFor(() => expect(scroll.mock.contexts.at(-1)).toBe(document.getElementById('disk')))
      expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Open Insights › System' }))
    })

    it('says when a linked setting is not available and shows the default', () => {
      seed({ capabilities: { gateway: 'remote', methods: ['*'], ws: ['events'] } })
      render(<SettingsRoute params={{ section: 'danger-zone' }} client={fakeApi()} />)
      expect(screen.getByText('That setting is not available here.')).toBeDefined()
      expect(screen.getByRole('heading', { level: 2, name: 'General' })).toBeDefined()
      expect(within(screen.getByRole('navigation', { name: 'Settings' })).queryByRole('button', { name: 'This computer' })).toBeNull()
    })

    it('shows the category list on a phone and one category with a way back', () => {
      atViewport(390, { pointer: 'coarse' })
      seed()
      const { rerender } = render(<SettingsRoute params={{}} client={fakeApi()} />)
      expect(screen.queryByRole('heading', { level: 2 })).toBeNull()
      fireEvent.click(screen.getByRole('button', { name: 'Notifications' }))
      expect(useStore.getState().route.params).toEqual({ section: 'notifications' })

      rerender(<SettingsRoute params={{ section: 'notifications' }} client={fakeApi()} />)
      expect(screen.queryByRole('navigation', { name: 'Settings' })).toBeNull()
      fireEvent.click(screen.getByRole('button', { name: 'Settings' }))
      expect(useStore.getState().route).toEqual({ name: 'settings', params: {} })
      rerender(<SettingsRoute params={{}} client={fakeApi()} />)
      expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Notifications' }))
    })

    it('returns from a phone category to the list and searches on /', () => {
      atViewport(390, { pointer: 'coarse' })
      seed()
      const { rerender } = render(<SettingsRoute params={{ section: 'notifications' }} client={fakeApi()} />)
      fireEvent.keyDown(document.body, { key: '/' })
      expect(useStore.getState().route).toEqual({ name: 'settings', params: {} })

      rerender(<SettingsRoute params={{}} client={fakeApi()} />)
      expect(document.activeElement).toBe(screen.getByRole('searchbox', { name: 'Search settings' }))
    })
  })
})
