import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import type { GatewayCapabilities, ServerReleaseChannelSet, ServerUpdateStatus } from '@/lib/types'
import { ReleaseChannelSection, confirmation, promoteURL } from '@/routes/settings/release-channel'
import { useStore } from '@/store'
import { bob, fakeApi, serverInfo, serverUpdateStatus } from '@/test/fixtures'

const caps: GatewayCapabilities = { gateway: 'server', methods: ['*'], ws: ['events'], local: [] }

function seed(status: Partial<ServerUpdateStatus>, member = serverInfo.member) {
  useStore.setState({
    info: { ...serverInfo, member },
    capabilities: caps,
    serverUpdate: serverUpdateStatus({ channel: 'beta', ...status }),
  })
}

const dry = (over: Partial<ServerReleaseChannelSet> = {}): ServerReleaseChannelSet => ({
  channel: 'beta',
  previous: 'nightly',
  saved: false,
  running: { version: 'v0.9.0-nightly.12', build: 412 },
  target: { version: 'v0.9.0-beta.1', build: 380 },
  wait: true,
  ...over,
})

describe('release channel section', () => {
  it('asks before nightly with the plan text and saves nothing on Cancel', async () => {
    seed({})
    const client = fakeApi()
    render(<ReleaseChannelSection client={client} />)
    fireEvent.click(screen.getByRole('radio', { name: 'Nightly' }))
    const dialog = await screen.findByRole('dialog', { name: 'Switch to nightly builds?' })
    expect(within(dialog).getByText('Not recommended unless you are developing Cloudfield.')).toBeDefined()
    expect(within(dialog).getByText('A nightly build may contain security issues that a later build fixes.')).toBeDefined()
    fireEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }))
    expect(client.serverReleaseChannelSet).not.toHaveBeenCalled()

    fireEvent.click(screen.getByRole('radio', { name: 'Nightly' }))
    fireEvent.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Switch to nightly' }))
    await waitFor(() => expect(client.serverReleaseChannelSet).toHaveBeenCalledWith('nightly'))
  })

  it('shows the versions from the dry run before a switch that makes the server wait', async () => {
    seed({ channel: 'nightly' })
    const client = fakeApi({ serverReleaseChannelSet: vi.fn(async (_c, dryRun) => (dryRun ? dry() : dry({ saved: true }))) })
    render(<ReleaseChannelSection client={client} />)
    fireEvent.click(screen.getByRole('radio', { name: 'Beta' }))
    const dialog = await screen.findByRole('dialog', { name: 'Switch to beta?' })
    expect(client.serverReleaseChannelSet).toHaveBeenCalledWith('beta', true)
    expect(dialog.textContent).toContain(
      'This server runs v0.9.0-nightly.12 (build 412). Beta is at v0.9.0-beta.1 (build 380). The server stays on its current build until beta reaches build 412, then updates as usual. It never installs an older build.',
    )
    expect(client.serverReleaseChannelSet).toHaveBeenCalledTimes(1)
    fireEvent.click(within(dialog).getByRole('button', { name: 'Switch to beta' }))
    await waitFor(() => expect(client.serverReleaseChannelSet).toHaveBeenLastCalledWith('beta'))
  })

  it('saves at once when the dry run says nothing waits', async () => {
    seed({ channel: 'stable' })
    const client = fakeApi({ serverReleaseChannelSet: vi.fn(async (_c, dryRun) => dry({ wait: false, saved: !dryRun })) })
    render(<ReleaseChannelSection client={client} />)
    fireEvent.click(screen.getByRole('radio', { name: 'Beta' }))
    await waitFor(() => expect(client.serverReleaseChannelSet).toHaveBeenLastCalledWith('beta'))
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('shows the server error as it is returned', async () => {
    seed({})
    const client = fakeApi({
      serverReleaseChannelSet: vi.fn(async () => {
        throw new Error('server.release_channel_set: only a super admin may change the release channel')
      }),
    })
    render(<ReleaseChannelSection client={client} />)
    fireEvent.click(screen.getByRole('radio', { name: 'Stable' }))
    expect((await screen.findByRole('alert')).textContent).toContain('only a super admin may change the release channel')
  })

  it('is read-only for a member who is not a super admin', () => {
    seed({}, bob)
    render(<ReleaseChannelSection client={fakeApi()} />)
    expect(screen.queryByRole('radio')).toBeNull()
    expect(screen.getByText('Beta')).toBeDefined()
    expect(screen.getByText(/Only a super admin can change it/)).toBeDefined()
  })

  it('stays out of the way of a server that reports no channel', () => {
    seed({ channel: undefined })
    const { container } = render(<ReleaseChannelSection client={fakeApi()} />)
    expect(container.textContent).toBe('')
  })

  it('offers Promote for the newest beta and nightly, with days since the beta was cut, as links that only open GitHub', () => {
    vi.useFakeTimers({ toFake: ['Date'], now: new Date('2026-11-20T12:00:00Z') })
    try {
      seed({
        channel: 'nightly',
        channel_pointers: {
          nightly: { version: 'v0.9.0-nightly.13', build: 420, published_at: '2026-11-20T08:00:00Z' },
          beta: { version: 'v0.9.0-beta.1', build: 380, published_at: '2026-11-15T08:00:00Z' },
        },
      })
      const client = fakeApi()
      render(<ReleaseChannelSection client={client} />)
      const beta = screen.getByRole('link', { name: 'Promote the newest beta on GitHub' })
      const nightly = screen.getByRole('link', { name: 'Promote the newest nightly on GitHub' })
      expect(beta.getAttribute('href')).toBe(promoteURL)
      expect(nightly.getAttribute('target')).toBe('_blank')
      expect(screen.getByText(/v0\.9\.0-beta\.1 \(build 380\), cut 5 days ago/)).toBeDefined()
      expect(screen.getByText(/v0\.9\.0-nightly\.13 \(build 420\), cut today/)).toBeDefined()
      expect(screen.getByText(/v0\.9\.0-beta\.1 \(build 380\), cut 5 days ago\. Promote to stable from the Promote workflow on GitHub\./)).toBeDefined()
      expect(screen.getByText(/Promote to beta from the Promote workflow/)).toBeDefined()
      fireEvent.click(beta)
      expect(client.serverReleaseChannelSet).not.toHaveBeenCalled()
    } finally {
      vi.useRealTimers()
    }
  })

  it('hides a Promote row whose pointer the server did not verify, and all of them on stable or for a member', () => {
    const pointers = {
      nightly: { version: 'v0.9.0-nightly.13', published_at: '2026-11-20T08:00:00Z' },
      beta: { version: 'v0.9.0-beta.1', published_at: '2026-11-15T08:00:00Z' },
    }
    seed({ channel: 'beta', channel_pointers: { beta: pointers.beta } })
    const first = render(<ReleaseChannelSection client={fakeApi()} />)
    expect(screen.getAllByRole('link')).toHaveLength(1)
    first.unmount()
    seed({ channel: 'stable', channel_pointers: pointers })
    const second = render(<ReleaseChannelSection client={fakeApi()} />)
    expect(screen.queryByRole('link')).toBeNull()
    second.unmount()
    seed({ channel: 'beta', channel_pointers: pointers }, bob)
    render(<ReleaseChannelSection client={fakeApi()} />)
    expect(screen.queryByRole('link')).toBeNull()
  })
})

describe('release channel context', () => {
  it('explains the selected channel and shows the pointer age to every member', () => {
    vi.useFakeTimers({ toFake: ['Date'], now: new Date('2026-11-20T12:00:00Z') })
    try {
      seed({ channel: 'beta', channel_pointers: { beta: { version: 'v0.9.0-beta.1', published_at: '2026-11-08T12:00:00Z' } } }, bob)
      render(<ReleaseChannelSection client={fakeApi()} />)
      expect(screen.getByText(/Builds promoted from nightly\. Beta last published 12 days ago\./)).toBeDefined()
    } finally {
      vi.useRealTimers()
    }
  })

  it('hides the age when the server did not verify the pointer', () => {
    seed({ channel: 'nightly' })
    render(<ReleaseChannelSection client={fakeApi()} />)
    expect(screen.getByText(/Built from main, at most every 4 hours\./).textContent).not.toContain('last published')
  })

  it('shows a spinner line while the dry run is out', async () => {
    seed({ channel: 'nightly' })
    let release: (r: ServerReleaseChannelSet) => void = () => {}
    const client = fakeApi({ serverReleaseChannelSet: vi.fn(() => new Promise<ServerReleaseChannelSet>((resolve) => { release = resolve })) })
    render(<ReleaseChannelSection client={client} />)
    fireEvent.click(screen.getByRole('radio', { name: 'Beta' }))
    expect(await screen.findByText('Checking beta…')).toBeDefined()
    release(dry())
    await screen.findByRole('dialog', { name: 'Switch to beta?' })
  })

  it('puts the server error on its own line when the channel cannot be read', async () => {
    seed({ channel: 'nightly' })
    const failure = 'channel pointer beta.json: 404 Not Found'
    const client = fakeApi({ serverReleaseChannelSet: vi.fn(async () => dry({ wait: false, target: undefined, target_error: failure })) })
    render(<ReleaseChannelSection client={client} />)
    fireEvent.click(screen.getByRole('radio', { name: 'Beta' }))
    const dialog = await screen.findByRole('dialog', { name: 'Switch to beta?' })
    expect(within(dialog).getByRole('alert').textContent).toBe(failure)
    expect(within(dialog).getByText('The server never installs an older build.')).toBeDefined()
  })
})

describe('a pending update the change kept', () => {
  it('shows why the server could not check it, unchanged', async () => {
    seed({ channel: 'beta' })
    const failure = 'fetch https://github.com/3xDevOps/aether/releases/download/v0.9.1-nightly.2/aether-release.json: 502 Bad Gateway'
    const client = fakeApi({
      serverUpdateStatus: vi.fn(async () => serverUpdateStatus({ channel: 'beta' })),
      serverReleaseChannelSet: vi.fn(async (_c, dryRun) => dry({ wait: false, saved: !dryRun, kept: 'v0.9.1-nightly.2', kept_error: failure })),
    })
    render(<ReleaseChannelSection client={client} />)
    fireEvent.click(screen.getByRole('radio', { name: 'Stable' }))
    expect(await screen.findByText(failure)).toBeDefined()
    expect(screen.getByText(/The pending update to v0\.9\.1-nightly\.2 was kept/)).toBeDefined()
  })
})

describe('hotfix builds', () => {
  it('names the hotfix in the confirmation, since the same build can still be above the channel', async () => {
    seed({ channel: 'nightly' })
    const client = fakeApi({
      serverReleaseChannelSet: vi.fn(async () => dry({ running: { version: 'v0.9.1', build: 412, hotfix: 1 }, target: { version: 'v0.9.0', build: 412 } })),
    })
    render(<ReleaseChannelSection client={client} />)
    fireEvent.click(screen.getByRole('radio', { name: 'Beta' }))
    const dialog = await screen.findByRole('dialog', { name: 'Switch to beta?' })
    expect(dialog.textContent).toContain('This server runs v0.9.1 (build 412 hotfix 1). Beta is at v0.9.0 (build 412). The server stays on its current build until beta reaches build 412 hotfix 1')
  })
})

describe('confirmation', () => {
  it('asks for nightly always and for others only when the server would wait or cannot tell', () => {
    expect(confirmation('nightly')).toBe('nightly')
    expect(confirmation('beta', dry())).toBe('wait')
    expect(confirmation('beta', dry({ wait: false }))).toBeNull()
    expect(confirmation('beta', dry({ wait: false, target: undefined, target_error: 'beta.json: 404' }))).toBe('wait')
  })
})
