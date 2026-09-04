import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { UpdateBanners } from '@/components/update-banner'
import { ApiError } from '@/lib/api'
import { useStore } from '@/store'
import { bob, fakeApi, updateStatus } from '@/test/fixtures'
import { seed } from '@/test/update-banner-harness'

vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

beforeEach(() => {
  vi.clearAllMocks()
})

afterEach(() => {
  vi.useRealTimers()
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

// The binary sits in a root-owned directory on macOS. The gateway can still
// replace it, through the administrator password dialog, and that dialog
// carries osascript's name rather than Aether's - so the banner says what
// is about to appear and where the password goes before anything is
// pressed.
describe('a binary macOS installs through the administrator dialog', () => {
  const adminPrompt = () =>
    fakeApi({
      localUpdateCheck: vi.fn(async () =>
        updateStatus({
          cli_path: '/usr/local/bin/aether',
          install_method: 'admin-prompt',
        }),
      ),
    })

  test('names the path and the dialog before and after the click', async () => {
    const client = adminPrompt()
    seed()
    render(<UpdateBanners client={client} />)

    expect(
      await screen.findByText(
        'macOS will ask for an administrator password: /usr/local/bin/aether is in a directory this account cannot write to. The dialog is labelled osascript, the tool Aether asks through. Aether never sees your password.',
      ),
    ).toBeTruthy()

    fireEvent.click(screen.getByRole('button', { name: 'Update now' }))
    expect(
      await screen.findByText(
        'Downloading v1.3.0, then macOS asks for an administrator password...',
      ),
    ).toBeTruthy()
  })

  // Closing the dialog is not a failure: nothing was downloaded into place,
  // and the member may simply try again. The gateway answers 403 for it
  // and nothing else on update.apply does.
  test('a closed dialog is a cancel, not a failure, and the button comes back', async () => {
    const client = adminPrompt()
    client.localUpdateApply = vi.fn(async () => {
      throw new ApiError(
        403,
        'update.apply: nothing was changed: administrator access was not granted: execution error: User canceled. (-128)',
      )
    })
    seed()
    render(<UpdateBanners client={client} />)

    fireEvent.click(await screen.findByRole('button', { name: 'Update now' }))

    expect(
      await screen.findByText('Update cancelled, nothing was changed.'),
    ).toBeTruthy()
    const detail = screen.getByText(
      'update.apply: nothing was changed: administrator access was not granted: execution error: User canceled. (-128)',
    )
    expect(detail.className).toContain('text-muted-foreground')
    expect(detail.className).not.toContain('text-state-failed')
    const button = screen.getByRole('button', { name: 'Update now' }) as HTMLButtonElement
    expect(button.disabled).toBe(false)
  })

  // Any other status is the install failing for real, and stays red.
  test('a 503 is still the red failure line', async () => {
    const client = adminPrompt()
    client.localUpdateApply = vi.fn(async () => {
      throw new ApiError(503, 'update.apply: install failed: checksum mismatch')
    })
    seed()
    render(<UpdateBanners client={client} />)

    fireEvent.click(await screen.findByRole('button', { name: 'Update now' }))

    const detail = await screen.findByText(
      'update.apply: install failed: checksum mismatch',
    )
    expect(detail.className).toContain('text-state-failed')
    expect(screen.queryByText('Update cancelled, nothing was changed.')).toBeNull()
    expect(screen.getByRole('button', { name: 'Update now' })).toBeTruthy()
  })
})

// A root-owned directory on Linux: the gateway has no dialog to ask
// through, so the banner hands over the command instead of a button that
// could only fail.
test('a binary the gateway cannot write gets the sudo command and no button', async () => {
  const client = fakeApi({
    localUpdateCheck: vi.fn(async () =>
      updateStatus({ cli_path: '/usr/local/bin/aether', install_method: 'manual' }),
    ),
  })
  seed()
  render(<UpdateBanners client={client} />)

  expect(
    await screen.findByText(
      '/usr/local/bin/aether is not writable by this account. Update it from a terminal:',
    ),
  ).toBeTruthy()
  expect(screen.getByRole('button', { name: 'Copy sudo aether update' })).toBeTruthy()
  expect(screen.queryByRole('button', { name: 'Update now' })).toBeNull()
  expect(screen.queryByText(/Updating replaces the aether binary/)).toBeNull()
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

  // An unsupervised gateway - `aether gui` in a browser tab - rebuilds the
  // app and deliberately keeps serving. The flag is never cleared, so
  // setting it here would hide a genuine disconnect in that tab for good.
  test('leaves gatewayRestarting alone when only a rebuild is running', async () => {
    const client = fakeApi({
      localUpdateApply: vi.fn(async () => ({
        updated: ['/usr/local/bin/aether'],
        version: 'v1.3.0',
        restarting: false,
        rebuilding: true,
        note: 'rebuilding the desktop app; restart it when the rebuild finishes',
      })),
    })
    seed()
    render(<UpdateBanners client={client} />)

    fireEvent.click(await screen.findByRole('button', { name: 'Update now' }))
    await screen.findByText(
      'Rebuilding the app (about a minute; the first time also fetches Node)...',
    )
    expect(useStore.getState().gatewayRestarting).toBe(false)
  })

  // The gateway's note says what it was about to do. Once the build is over
  // the banner has to stop saying a finished rebuild is still running.
  test('says the rebuild finished on a gateway that is not going away', async () => {
    vi.useFakeTimers()
    const client = fakeApi({
      localUpdateApply: vi.fn(async () => ({
        updated: ['/usr/local/bin/aether'],
        version: 'v1.3.0',
        restarting: false,
        rebuilding: true,
        note: 'rebuilding the desktop app; restart it when the rebuild finishes',
      })),
      localUpdateStatus: vi.fn(async () => ({ phase: 'done' as const })),
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

    expect(
      screen.getByText(
        /Updated to v1\.3\.0\. The app was rebuilt; restart it to use the new version\./,
      ),
    ).toBeTruthy()
    expect(screen.queryByText(/restart it when the rebuild finishes/)).toBeNull()
    // Nothing left for the button to do.
    expect(screen.queryByRole('button', { name: /Update|Rebuild|Relaunch/ })).toBeNull()
  })
})
