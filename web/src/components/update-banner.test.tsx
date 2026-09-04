import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { UpdateBanners } from '@/components/update-banner'
import type { Api } from '@/lib/api'
import type { AetherDesktop } from '@/components/shell/title-bar'
import type {
  Event,
  Member,
  Run,
  ServerUpdatePayload,
  ServerUpdateStatus,
} from '@/lib/types'
import { useStore } from '@/store'
import { connect } from '@/store/sync'
import {
  alice,
  bob,
  fakeApi,
  run,
  serverInfo,
  serverUpdateStatus,
  updateStatus,
  vera,
  workspace,
} from '@/test/fixtures'
import { StubSocket } from '@/test/stub-socket'
import { caps, seed } from '@/test/update-banner-harness'

vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

const shellWindow = window as Window & { aetherDesktop?: AetherDesktop }


/** Opens the newest stub socket and acknowledges the subscription on it. */
async function subscribe() {
  await waitFor(() => expect(StubSocket.opened.length).toBeGreaterThan(0))
  const socket = StubSocket.last()
  socket.onopen?.()
  socket.onmessage?.({ data: JSON.stringify({ ok: true }) })
  return socket
}

function deliver(socket: StubSocket, ev: Event) {
  socket.onmessage?.({ data: JSON.stringify(ev) })
}

/** One server.update frame, as the feed carries it. */
function updateEvent(seq: number, payload: ServerUpdatePayload): Event {
  return {
    id: `evt_${seq}`,
    seq,
    time: '2026-08-14T10:06:00Z',
    workspace_id: workspace.id,
    run_id: '',
    actor_id: alice.id,
    type: 'server.update',
    payload,
  }
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

// Capability is half the gate and the role is the other half: the local
// gateway advertises every method regardless of who is behind it.
describe('the server update banner', () => {
  const behind = () => serverUpdateStatus({ update_available: true })

  /**
   * A dashboard whose server answers `server.update_status` with the given
   * status, and no `update.check` verb: the CLI banner is a separate
   * prompt with its own Update button, and only one of them is under test
   * here. A null status is a server too old to serve the method.
   */
  function seedServer(
    over: { self?: Member; status?: ServerUpdateStatus | null; runs?: Run[] } = {},
  ): Api {
    seed({ self: over.self, capabilities: caps({ local: ['link.status'] }) })
    useStore.getState().setRuns(over.runs ?? [])
    const status = over.status === undefined ? behind() : over.status
    return fakeApi({
      serverUpdateStatus: vi.fn(async () => {
        if (!status) throw new Error('server.update_status: method not found')
        return status
      }),
    })
  }

  test('offers both buttons to an admin and nothing to anyone else', async () => {
    for (const self of [bob, vera]) {
      const other = seedServer({ self })
      const view = render(<UpdateBanners client={other} />)
      await waitFor(() => expect(other.serverUpdateStatus).toHaveBeenCalled())
      expect(screen.queryByText('The server is behind.')).toBeNull()
      expect(screen.queryByRole('button', { name: 'Update now' })).toBeNull()
      view.unmount()
    }

    const client = seedServer()
    render(<UpdateBanners client={client} />)
    expect(await screen.findByText('The server is behind.')).toBeTruthy()
    expect(screen.getByText(/Server v1\.2\.3, latest v1\.3\.0/)).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Update now' })).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Update when idle' })).toBeTruthy()
  })

  // The documented unprivileged install: the binary directory belongs to
  // root and the service user only reads it, so the button could not work
  // and the real reason is worth more than a friendlier sentence.
  test('a server that cannot update itself keeps the commands', async () => {
    const client = seedServer({
      status: serverUpdateStatus({
        update_available: true,
        capable: false,
        incapable: 'its binary directory is not writable by the server process',
        manual_commands: ['sudo aether update', 'sudo systemctl restart aether-server'],
      }),
    })
    render(<UpdateBanners client={client} />)

    expect(await screen.findByText('The server is behind.')).toBeTruthy()
    expect(
      screen.getByText(
        /The server cannot update itself: its binary directory is not writable by the server process\./,
      ),
    ).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Copy sudo aether update' })).toBeTruthy()
    expect(
      screen.getByRole('button', {
        name: 'Copy sudo systemctl restart aether-server',
      }),
    ).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Update now' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Update when idle' })).toBeNull()
  })

  // A server too old to serve server.update_status still says it is behind
  // through update.check, and there is nothing to press on it.
  test('falls back to update.check on a server without the status method', async () => {
    seed()
    const status = updateStatus({ server_version: 'v1.2.9', server_behind: true })
    const client = fakeApi({
      serverUpdateStatus: vi.fn(async () => {
        throw new Error('server.update_status: method not found')
      }),
      // The CLI on this machine is current; only the server is behind.
      localUpdateCheck: vi.fn(async () => ({
        ...status,
        cli: { ...status.cli, update_available: false },
      })),
    })
    render(<UpdateBanners client={client} />)

    expect(await screen.findByText('The server is behind.')).toBeTruthy()
    expect(screen.getByText(/Server v1\.2\.9, latest v1\.3\.0/)).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Copy sudo aether update' })).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Update now' })).toBeNull()
  })

  // The count is the live one from this member's own run list, and it is
  // the server's definition of busy: a paused run is a frozen container
  // and a needs-attention run is waiting on a person.
  test('the confirm dialog counts the runs a restart interrupts', async () => {
    const client = seedServer({
      runs: [
        run({ id: 'run_1', status: 'running' }),
        run({ id: 'run_2', status: 'provisioning' }),
        run({ id: 'run_3', status: 'needs-attention' }),
        run({ id: 'run_4', status: 'merged' }),
      ],
    })
    render(<UpdateBanners client={client} />)

    fireEvent.click(await screen.findByRole('button', { name: 'Update now' }))

    expect(await screen.findByText('Update the server to v1.3.0?')).toBeTruthy()
    expect(screen.getByText(/2 runs are active right now/)).toBeTruthy()
    expect(screen.getByText(/They keep running/)).toBeTruthy()

    fireEvent.click(screen.getByRole('button', { name: 'Update and restart' }))
    await waitFor(() => expect(client.serverUpdate).toHaveBeenCalledWith('now'))
  })

  // Nothing is running: the dialog must not open with "0 runs are active
  // right now. They keep running".
  test('the confirm dialog has a zero case', async () => {
    const client = seedServer({ runs: [run({ status: 'merged' })] })
    render(<UpdateBanners client={client} />)

    fireEvent.click(await screen.findByRole('button', { name: 'Update now' }))

    expect(
      await screen.findByText(
        'No runs are active right now. Attached terminals reconnect on their own.',
      ),
    ).toBeTruthy()
  })

  test('Update when idle schedules, and the banner then offers Cancel', async () => {
    const client = seedServer()
    render(<UpdateBanners client={client} />)

    fireEvent.click(await screen.findByRole('button', { name: 'Update when idle' }))
    await waitFor(() => expect(client.serverUpdate).toHaveBeenCalledWith('idle'))

    expect(
      await screen.findByText(
        'Update to v1.3.0 scheduled by Alice, applies when no run is active.',
      ),
    ).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Update now' })).toBeNull()

    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    await waitFor(() => expect(client.serverUpdate).toHaveBeenCalledWith('cancel'))
  })

  // An update someone else scheduled before this tab loaded arrives on the
  // status answer, not on the feed.
  test('shows a pending update, and what it is still waiting for', async () => {
    const client = seedServer({
      status: serverUpdateStatus({
        update_available: true,
        pending: {
          version: 'v1.3.0',
          requested_by: alice.id,
          requested_at: '2026-08-14T10:06:00Z',
        },
        // A paused run holds nothing back, so it is not named here; an
        // open workspace shell does, and the scheduled line alone would
        // leave an admin wondering why the update never fires.
        waiting: { runs: 2, paused: 1, shells: 1 },
      }),
    })
    render(<UpdateBanners client={client} />)

    expect(
      await screen.findByText(
        'Update to v1.3.0 scheduled by Alice, applies when no run is active.',
      ),
    ).toBeTruthy()
    expect(screen.getByText('Waiting for 2 runs and 1 open shell.')).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Cancel' })).toBeTruthy()
  })

  test('renders every phase the server.update feed carries', async () => {
    render(<UpdateBanners client={seedServer()} />)
    await screen.findByRole('button', { name: 'Update now' })

    const phase = (payload: ServerUpdatePayload) =>
      act(() => useStore.getState().applyServerUpdate(payload))

    phase({ phase: 'scheduled', version: 'v1.3.0', actor_id: alice.id })
    expect(
      screen.getByText(
        'Update to v1.3.0 scheduled by Alice, applies when no run is active.',
      ),
    ).toBeTruthy()

    phase({ phase: 'applying', version: 'v1.3.0' })
    expect(screen.getByText(/Downloading and verifying the release/)).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Cancel' })).toBeNull()

    phase({ phase: 'restarting', version: 'v1.3.0' })
    expect(screen.getByText(/Restarting on the new version/)).toBeTruthy()

    // A failure names the real error and falls back to the two commands,
    // because the server could not do it for itself.
    phase({ phase: 'failed', version: 'v1.3.0', detail: 'checksum mismatch' })
    expect(screen.getByText('checksum mismatch')).toBeTruthy()
    expect(screen.getByText(/Run these on the server host instead/)).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Copy sudo aether update' })).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Update now' })).toBeTruthy()
  })

  // The end of the flow, on the path production actually takes: the server
  // re-executes, the socket drops, and the client resubscribes from the
  // cursor it already has. Nothing else re-reads server.info, so without
  // the reconnect re-read the banner would say "Restarting" for as long as
  // the tab stays open.
  test('ends the banner on the reconnect that follows the restart', async () => {
    StubSocket.install()
    // What the server answers, which the re-exec changes under the client.
    // While it is down every read fails, exactly as it does in the seconds
    // between the re-exec and the reconnect.
    let running = 'v1.2.3'
    let down = false
    const refuse = () => {
      throw new Error('server unreachable: dial tcp: connection refused')
    }
    const client = fakeApi({
      serverInfo: vi.fn(async () => {
        if (down) refuse()
        return { ...serverInfo, server_version: running }
      }),
      serverUpdateStatus: vi.fn(async () => {
        if (down) refuse()
        return serverUpdateStatus({
          server_version: running,
          update_available: running !== 'v1.3.0',
        })
      }),
    })
    seed({ capabilities: caps({ local: ['link.status'] }) })
    const stop = connect(useStore, client)
    render(<UpdateBanners client={client} />)

    await subscribe()
    expect(await screen.findByText('The server is behind.')).toBeTruthy()

    // The update the admin asked for, as the feed delivers it. Both frames
    // move the cursor, which is what used to stop the re-read.
    const socket = StubSocket.last()
    deliver(socket, updateEvent(5, { phase: 'applying', version: 'v1.3.0' }))
    deliver(socket, updateEvent(6, { phase: 'restarting', version: 'v1.3.0' }))
    expect(await screen.findByText(/Restarting on the new version/)).toBeTruthy()
    await waitFor(() => expect(useStore.getState().lastSeq).toBe(6))

    // The server re-executes on the new version: the socket goes, and the
    // client comes back on the cursor it already has.
    const reads = vi.mocked(client.serverInfo).mock.calls.length
    down = true
    socket.onclose?.({ code: 1006 })
    await waitFor(() => expect(StubSocket.opened.length).toBe(2), { timeout: 2000 })
    // A read while it was away failed, and that must not have cost the
    // banner what it already knew.
    expect(screen.getByText(/Restarting on the new version/)).toBeTruthy()
    down = false
    running = 'v1.3.0'
    await subscribe()

    await waitFor(() =>
      expect(vi.mocked(client.serverInfo).mock.calls.length).toBeGreaterThan(reads),
    )
    await waitFor(() => expect(screen.queryByText('The server is behind.')).toBeNull())
    // And nothing is left saying a restart is coming, which is the line
    // every non-admin sees in the status bar.
    expect(useStore.getState().serverUpdateProgress).toBeNull()
    stop()
    vi.unstubAllGlobals()
  })

  // A status read that failed and a server that cannot update itself are
  // different things, and only the server can say the second one.
  test('says the status read failed, and retries it', async () => {
    seed({ capabilities: caps({ local: ['link.status'] }) })
    useStore.setState({ update: updateStatus({ server_behind: true }) })
    const client = fakeApi({
      serverUpdateStatus: vi
        .fn()
        .mockRejectedValueOnce(new Error('server.update_status: server unreachable'))
        .mockResolvedValue(serverUpdateStatus({ update_available: true })),
    })
    render(<UpdateBanners client={client} />)

    expect(
      await screen.findByText(
        /The dashboard could not read the server's update status: server.update_status: server unreachable\./,
      ),
    ).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Update now' })).toBeNull()

    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))

    expect(await screen.findByRole('button', { name: 'Update now' })).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Retry' })).toBeNull()
  })

  // A gateway that does not carry the method at all: the dashboard cannot
  // update the server, and it must not say the server cannot update itself.
  test('says only what it knows on a gateway without the method', async () => {
    seed({ capabilities: { gateway: 'remote', methods: ['server.info'], ws: ['events'] } })
    useStore.setState({
      update: updateStatus({ server_version: 'v1.2.9', server_behind: true }),
    })
    render(<UpdateBanners client={fakeApi()} />)

    expect(
      await screen.findByText('The dashboard cannot update the server. Run these on the server host:'),
    ).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Retry' })).toBeNull()
  })

  // Dismissing silences the offer. An update already moving is why the
  // server is about to restart, so it comes back regardless.
  test('a dismissal hides the offer but not an update in flight', async () => {
    const client = seedServer()
    useStore.setState({ dismissedUpdates: { cli: '', server: 'v1.3.0', shell: '' } })
    render(<UpdateBanners client={client} />)

    await waitFor(() => expect(client.serverUpdateStatus).toHaveBeenCalled())
    expect(screen.queryByText('The server is behind.')).toBeNull()

    act(() =>
      useStore.getState().applyServerUpdate({
        phase: 'scheduled',
        version: 'v1.3.0',
        actor_id: alice.id,
      }),
    )
    expect(screen.getByText('The server is behind.')).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Cancel' })).toBeTruthy()
  })

  // The server's refusal carries what to do about it - an incapable server
  // names the commands inside the message - so it is rendered verbatim.
  test('a refused call shows the server message and leaves the buttons usable', async () => {
    const client = seedServer()
    client.serverUpdate = vi.fn(async () => {
      throw new Error(
        'this server cannot update itself; on the server host run: sudo aether update',
      )
    })
    render(<UpdateBanners client={client} />)

    fireEvent.click(await screen.findByRole('button', { name: 'Update when idle' }))

    expect(
      await screen.findByText(
        'this server cannot update itself; on the server host run: sudo aether update',
      ),
    ).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Update when idle' })).toBeTruthy()
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
