import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { UpdateBanners } from '@/components/update-banner'
import type { AetherDesktop } from '@/components/shell/title-bar'
import type { GatewayCapabilities, Member } from '@/lib/types'
import { useStore } from '@/store'
import type { UpdateKind } from '@/store/ui'
import { alice, bob, fakeApi, serverInfo, updateStatus } from '@/test/fixtures'

vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

const shellWindow = window as Window & { aetherDesktop?: AetherDesktop }

/** The desktop gateway's descriptor, carrying the update verbs. */
function caps(over: Partial<GatewayCapabilities> = {}): GatewayCapabilities {
  return {
    gateway: 'local',
    methods: ['*'],
    ws: ['events', 'attach'],
    local: ['link.status', 'update.check', 'update.apply'],
    version: 'v1.2.3',
    ...over,
  }
}

function seed(
  over: {
    self?: Member
    capabilities?: GatewayCapabilities
    dismissedUpdates?: Record<UpdateKind, string>
  } = {},
) {
  useStore.setState({
    info: { ...serverInfo, member: over.self ?? alice },
    capabilities: over.capabilities ?? caps(),
    dismissedUpdates: over.dismissedUpdates ?? { cli: '', server: '', shell: '' },
    update: null,
    hydrated: true,
    gatewayRestarting: false,
  })
}

beforeEach(() => {
  vi.clearAllMocks()
  delete shellWindow.aetherDesktop
})

afterEach(() => {
  vi.useRealTimers()
})

test('renders nothing where the gateway does not serve update.check', async () => {
  const client = fakeApi()
  seed({ capabilities: caps({ local: ['link.status'] }) })
  render(<UpdateBanners client={client} />)

  await waitFor(() => expect(client.localUpdateCheck).not.toHaveBeenCalled())
  expect(screen.queryByText(/is available/)).toBeNull()
})

// The binary is on the member's own machine, so the CLI prompt is not an
// admin affordance: a collaborator updates their own copy.
test('shows the CLI banner to a collaborator when the CLI is behind', async () => {
  const client = fakeApi()
  seed({ self: bob })
  render(<UpdateBanners client={client} />)

  expect(await screen.findByText('Aether v1.3.0 is available.')).toBeTruthy()
  expect(screen.getByText(/You are running v1\.2\.3/)).toBeTruthy()
  // The copy names what the restart costs: the gateway holds the attach
  // sockets and the sync sessions, and they go with it.
  expect(screen.getByText(/Attached terminals and any running file sync stop/)).toBeTruthy()
  const notes = screen.getByRole('link', { name: 'Release notes' })
  expect(notes.getAttribute('href')).toBe(
    'https://github.com/3xDevOps/Aether/releases/tag/v1.3.0',
  )
  expect(notes.getAttribute('target')).toBe('_blank')
  expect(client.localUpdateCheck).toHaveBeenCalledTimes(1)
})

// Windows cannot replace a running binary, so the button would only ever
// fail: the banner names the release page instead.
test('offers no button where the CLI cannot update itself', async () => {
  const status = updateStatus()
  const client = fakeApi({
    localUpdateCheck: vi.fn(async () => ({
      ...status,
      cli: { ...status.cli, can_self_update: false },
    })),
  })
  seed()
  render(<UpdateBanners client={client} />)

  expect(await screen.findByText(/Self-update is not supported/)).toBeTruthy()
  expect(screen.queryByRole('button', { name: 'Update now' })).toBeNull()
  expect(screen.getByRole('link', { name: 'Release notes' })).toBeTruthy()
})

test('the Update button applies and the banner says it is restarting', async () => {
  const client = fakeApi()
  seed()
  render(<UpdateBanners client={client} />)

  fireEvent.click(await screen.findByRole('button', { name: 'Update now' }))

  expect(await screen.findByText(/Restarting the dashboard/)).toBeTruthy()
  expect(client.localUpdateApply).toHaveBeenCalledTimes(1)
  expect(screen.queryByRole('button', { name: 'Update now' })).toBeNull()
})

// A single-box install swaps aether-server beside the CLI, and that server
// keeps running the old code until its unit is restarted. `aether update`
// prints the command; the banner has to as well.
test('the done state names the replaced binaries and the server restart', async () => {
  const client = fakeApi({
    localUpdateApply: vi.fn(async () => ({
      updated: ['/usr/local/bin/aether', '/usr/local/bin/aether-server'],
      version: 'v1.3.0',
      restarting: false,
      rebuilding: false,
      note: 'rerun aether gui to use the new binary',
      restart_command: 'sudo systemctl restart aether-server',
    })),
  })
  seed()
  render(<UpdateBanners client={client} />)

  fireEvent.click(await screen.findByRole('button', { name: 'Update now' }))

  expect(await screen.findByText(/Updated to v1\.3\.0/)).toBeTruthy()
  expect(
    screen.getByText('/usr/local/bin/aether, /usr/local/bin/aether-server'),
  ).toBeTruthy()
  expect(screen.getByText('sudo systemctl restart aether-server')).toBeTruthy()
})

// A client machine has no server binary beside the CLI, so there is no
// unit to restart and the banner must not invent one.
test('the done state says nothing about a server that was not replaced', async () => {
  const client = fakeApi()
  seed()
  render(<UpdateBanners client={client} />)

  fireEvent.click(await screen.findByRole('button', { name: 'Update now' }))

  await screen.findByText(/Restarting the dashboard/)
  expect(screen.queryByText(/systemctl restart/)).toBeNull()
})

// The gateway names the directory and the exact sudo command; a friendlier
// substitute would drop the only part the member can act on.
test('a failed apply shows the gateway message and leaves the button usable', async () => {
  const client = fakeApi({
    localUpdateApply: vi.fn(async () => {
      throw new Error(
        '/usr/local/bin is not writable: rerun as sudo aether update',
      )
    }),
  })
  seed()
  render(<UpdateBanners client={client} />)

  fireEvent.click(await screen.findByRole('button', { name: 'Update now' }))

  expect(
    await screen.findByText(
      '/usr/local/bin is not writable: rerun as sudo aether update',
    ),
  ).toBeTruthy()
  expect(screen.getByRole('button', { name: 'Update now' })).toBeTruthy()
})

// Capability is half the gate and the role is the other half: the local
// gateway advertises every verb regardless of who is behind it.
test('the server banner is admin-only and names the commands to run', async () => {
  const client = fakeApi({
    localUpdateCheck: vi.fn(async () =>
      updateStatus({ server_version: 'v1.2.9', server_behind: true }),
    ),
  })

  seed({ self: bob })
  const collaborator = render(<UpdateBanners client={client} />)
  await screen.findByText('Aether v1.3.0 is available.')
  expect(screen.queryByText('The server is behind.')).toBeNull()
  collaborator.unmount()

  seed()
  render(<UpdateBanners client={client} />)
  expect(await screen.findByText('The server is behind.')).toBeTruthy()
  expect(screen.getByText(/Server v1\.2\.9, latest v1\.3\.0/)).toBeTruthy()
  expect(screen.getByText(/dashboard cannot update the server/)).toBeTruthy()
  expect(screen.getByRole('button', { name: 'Copy sudo aether update' })).toBeTruthy()
  expect(
    screen.getByRole('button', {
      name: 'Copy sudo systemctl restart aether-server',
    }),
  ).toBeTruthy()
})

// Dismissing records the version, not a flag: the next release is a new
// prompt, and silencing v1.3.0 must not silence v1.3.1.
test('a dismissal hides that version and a newer release comes back', async () => {
  const client = fakeApi()
  seed()
  const first = render(<UpdateBanners client={client} />)

  fireEvent.click(await screen.findByRole('button', { name: 'Dismiss' }))
  expect(useStore.getState().dismissedUpdates.cli).toBe('v1.3.0')
  expect(screen.queryByText('Aether v1.3.0 is available.')).toBeNull()
  first.unmount()

  // The same dismissal, the same release: still silent.
  useStore.setState({ update: null })
  const again = render(<UpdateBanners client={client} />)
  await waitFor(() => expect(useStore.getState().update).not.toBeNull())
  expect(screen.queryByText('Aether v1.3.0 is available.')).toBeNull()
  again.unmount()

  const newer = fakeApi({
    localUpdateCheck: vi.fn(async () => {
      const status = updateStatus()
      return { ...status, cli: { ...status.cli, latest: 'v1.3.1' } }
    }),
  })
  useStore.setState({ update: null })
  render(<UpdateBanners client={newer} />)
  expect(await screen.findByText('Aether v1.3.1 is available.')).toBeTruthy()
})

// update.apply can start a background rebuild of the desktop app after
// swapping the CLI binary. The banner polls update.status for its progress
// while the Update button stays disabled through the whole thing.
describe('the desktop-app rebuild the Update button waits on', () => {
  function rebuildApply() {
    return vi.fn(async () => ({
      updated: ['/usr/local/bin/aether'],
      version: 'v1.3.0',
      restarting: true,
      rebuilding: true,
    }))
  }

  test('walks the phases while the button stays disabled, then relaunches', async () => {
    vi.useFakeTimers()
    const client = fakeApi({
      localUpdateApply: rebuildApply(),
      localUpdateStatus: vi
        .fn()
        .mockResolvedValueOnce({ phase: 'installing dependencies' })
        .mockResolvedValue({ phase: 'done' }),
    })
    seed()
    render(<UpdateBanners client={client} />)

    // Let the initial update.check settle - a plain microtask, no timer.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0)
    })

    fireEvent.click(screen.getByRole('button', { name: 'Update now' }))
    expect(screen.getByText('Updating the CLI...')).toBeTruthy()
    expect(
      (screen.getByRole('button', { name: 'Updating...' }) as HTMLButtonElement).disabled,
    ).toBe(true)

    // update.apply resolves.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0)
    })
    expect(
      screen.getByText(
        'Rebuilding the app (about a minute; the first time also fetches Node)...',
      ),
    ).toBeTruthy()
    expect(
      (screen.getByRole('button', { name: 'Rebuilding...' }) as HTMLButtonElement).disabled,
    ).toBe(true)

    // First update.status poll: still building, and names the phase.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1000)
    })
    expect(screen.getByText('installing dependencies')).toBeTruthy()
    expect(
      (screen.getByRole('button', { name: 'Rebuilding...' }) as HTMLButtonElement).disabled,
    ).toBe(true)

    // Second poll: the build is done and the apply said it would restart.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1000)
    })
    expect(screen.getByText('Relaunching')).toBeTruthy()
    expect(
      (screen.getByRole('button', { name: 'Relaunching...' }) as HTMLButtonElement).disabled,
    ).toBe(true)
    expect(client.localUpdateStatus).toHaveBeenCalledTimes(2)
  })

  test('a failed rebuild shows the real error and the manual command', async () => {
    vi.useFakeTimers()
    const client = fakeApi({
      localUpdateApply: rebuildApply(),
      localUpdateStatus: vi.fn(async () => ({
        phase: 'error' as const,
        error: 'npm install: exit status 1',
      })),
    })
    seed()
    render(<UpdateBanners client={client} />)
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0)
    })

    fireEvent.click(screen.getByRole('button', { name: 'Update now' }))
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0)
    })
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1000)
    })

    expect(screen.getByText('npm install: exit status 1')).toBeTruthy()
    expect(screen.getByText('aether gui build')).toBeTruthy()
    // The polling loop stops itself once the phase is terminal.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(3000)
    })
    expect(client.localUpdateStatus).toHaveBeenCalledTimes(1)
  })

  test('sets gatewayRestarting as soon as the apply says the gateway is going away', async () => {
    const client = fakeApi({ localUpdateApply: rebuildApply() })
    seed()
    render(<UpdateBanners client={client} />)

    fireEvent.click(await screen.findByRole('button', { name: 'Update now' }))
    await waitFor(() => expect(useStore.getState().gatewayRestarting).toBe(true))
  })
})

describe('the desktop app rebuild notice', () => {
  const notice = 'The desktop app is out of date.'

  // A rebuild in the app can itself fail; the gateway persists that error
  // to a file and update.check reports it until a rebuild succeeds. That is
  // exactly why the shell is still old, so the notice says so.
  test('shows a persisted rebuild error from update.check', async () => {
    shellWindow.aetherDesktop = { platform: 'linux', shellVersion: '1.2.0' }
    const client = fakeApi({
      localUpdateCheck: vi.fn(async () =>
        updateStatus({ shell_build_error: 'npm install: exit status 1' }),
      ),
    })
    seed()
    render(<UpdateBanners client={client} />)

    expect(await screen.findByText(notice)).toBeTruthy()
    expect(screen.getByText('npm install: exit status 1')).toBeTruthy()
  })

  test('appears when the shell was built by a different CLI', async () => {
    shellWindow.aetherDesktop = { platform: 'linux', shellVersion: '1.2.0' }
    seed()
    render(<UpdateBanners client={fakeApi()} />)

    expect(await screen.findByText(notice)).toBeTruthy()
    expect(screen.getByText(/aether gui build/)).toBeTruthy()
  })

  // The flow it exists for: the update already ran, the CLI is current, and
  // the shell built by the old one is what is left behind. Keying this on
  // update_available would hide it in exactly that moment.
  test('appears with no update available, which is the flow it is for', async () => {
    shellWindow.aetherDesktop = { platform: 'linux', shellVersion: '1.2.0' }
    const status = updateStatus()
    const current = fakeApi({
      localUpdateCheck: vi.fn(async () => ({
        ...status,
        cli: { ...status.cli, version: 'v1.3.0', update_available: false },
      })),
    })
    seed({ capabilities: caps({ version: 'v1.3.0' }) })
    render(<UpdateBanners client={current} />)

    expect(await screen.findByText(notice)).toBeTruthy()
    expect(screen.queryByText(/is available\./)).toBeNull()
  })

  // A browser tab has no shell at all, so there is nothing to rebuild.
  test('stays away outside the desktop shell even with no check answer', async () => {
    seed({ capabilities: caps({ local: ['link.status'] }) })
    render(<UpdateBanners client={fakeApi()} />)
    await waitFor(() => expect(screen.queryByText(notice)).toBeNull())
  })

  test('stays away when the shell matches the CLI serving the gateway', async () => {
    // "1.2.3" and "v1.2.3" are the same build; only the prefix differs.
    shellWindow.aetherDesktop = { platform: 'linux', shellVersion: '1.2.3' }
    seed()
    const same = render(<UpdateBanners client={fakeApi()} />)
    await screen.findByText('Aether v1.3.0 is available.')
    expect(screen.queryByText(notice)).toBeNull()
    same.unmount()

    // A gateway too old to report its version is not a mismatch either.
    shellWindow.aetherDesktop = { platform: 'linux', shellVersion: '1.2.0' }
    seed({ capabilities: caps({ version: undefined }) })
    render(<UpdateBanners client={fakeApi()} />)
    await screen.findByText('Aether v1.3.0 is available.')
    expect(screen.queryByText(notice)).toBeNull()
  })

  test('dismissing it lasts only until the CLI moves again', async () => {
    shellWindow.aetherDesktop = { platform: 'linux', shellVersion: '1.2.0' }
    seed({ dismissedUpdates: { cli: '', server: '', shell: 'v1.2.3' } })
    const hidden = render(<UpdateBanners client={fakeApi()} />)
    await waitFor(() => expect(screen.queryByText(notice)).toBeNull())
    hidden.unmount()

    seed({
      capabilities: caps({ version: 'v1.3.0' }),
      dismissedUpdates: { cli: '', server: '', shell: 'v1.2.3' },
    })
    render(<UpdateBanners client={fakeApi()} />)
    expect(await screen.findByText(notice)).toBeTruthy()
  })
})
