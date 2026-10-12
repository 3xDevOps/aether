import { fireEvent, render, screen, within } from '@testing-library/react'
import { ApiError } from '@/lib/api'
import type { EdgeLogin, EdgeServer, EdgeStatus, GatewayCapabilities, LinkStatus } from '@/lib/types'
import { bindRouteToUrl, forgetHandedLink } from '@/lib/url-state'
import { OnboardingRoute } from '@/routes/onboarding'
import { useStore } from '@/store'
import { fakeApi, serverInfo, workspace } from '@/test/fixtures'

const edge = 'https://edge.example.test'
const serverID = 'abcdefghijklmnopqrstuvwxyz'

const localCaps: GatewayCapabilities = {
  gateway: 'local',
  methods: ['*'],
  ws: ['events', 'attach', 'terminal'],
  local: ['link.status', 'edge.login', 'edge.status', 'edge.servers', 'edge.hostkey', 'edge.link', 'edge.claim', 'edge.join'],
}

const unlinked: LinkStatus = {
  server_configured: false,
  linked: false,
  addr: '',
  user: '',
  repo: '',
}

const account = {
  id: 'acct_aaaaaaaaaaaaaaaaaaaaaaaaaa',
  provider: 'github',
  subject: '583231',
  login: 'octocat',
}

const pending: EdgeLogin = {
  state: 'pending',
  edge,
  signin_origin: 'https://auth.example.test',
  user_code: 'WDJB-MJHT',
  verification_uri: `${edge}/device`,
}

const signedIn: EdgeStatus = { edges: [{ edge, account }] }

const buildBox: EdgeServer = {
  id: serverID,
  name: 'build-box',
  online: true,
  role: 'admin',
  access_policy: 'approved-devices',
  kind: 'self-hosted',
}

const linkResult = {
  server_id: serverID,
  edge,
  user: 'aether',
  member: { id: 'mem_octo', display_name: 'Octo', role: 'admin' },
}

function seed() {
  useStore.setState({
    workspaces: { [workspace.id]: workspace },
    activeWorkspace: workspace.id,
    info: serverInfo,
    capabilities: localCaps,
    hydrated: true,
    hydrationError: null,
    route: { name: 'onboarding', params: {} },
    onboarded: false,
    onboardingStep: 'Connect',
    onboardingFurthest: 'Connect',
    onboardingWorkspace: '',
  })
}

afterEach(() => {
  vi.restoreAllMocks()
  vi.useRealTimers()
  forgetHandedLink()
  window.history.replaceState(null, '', '/')
})

describe('onboarding link through an edge', () => {
  it('opens the Join panel for a join link pasted before signing in, and signs in to its edge', async () => {
    seed()
    const custom = 'https://edge.custom.test'
    const customLogin = { ...pending, edge: custom, verification_uri: `${custom}/device` }
    const client = fakeApi({
      localLinkStatus: vi.fn(async () => unlinked),
      localEdgeStatus: vi.fn(async () => ({ edges: [] })),
      localEdgeLogin: vi.fn(async () => customLogin),
    })
    render(<OnboardingRoute params={{}} client={client} />)

    fireEvent.change(await screen.findByLabelText('Have an invitation or claim link?'), {
      target: { value: `${custom}/join/${serverID}/inv-7k2m9q4xbd#abcdefghijk23456` },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Open link' }))

    const join = within(await screen.findByRole('region', { name: 'Join' }))
    fireEvent.click(await join.findByRole('button', { name: 'Sign in' }))
    expect(client.localEdgeLogin).toHaveBeenCalledWith(custom)
  })

  it('offers sign-in first and keeps the address form behind a disclosure', async () => {
    seed()
    render(<OnboardingRoute params={{}} client={fakeApi({ localLinkStatus: vi.fn(async () => unlinked) })} />)

    const signIn = await screen.findByRole('button', { name: 'Sign in' })
    const byAddress = screen.getByRole('button', { name: /^Link by address/ })
    expect(signIn.compareDocumentPosition(byAddress) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(screen.queryByRole('form', { name: 'Link server' })).toBeNull()

    fireEvent.click(byAddress)
    const form = screen.getByRole('form', { name: 'Link server' })
    expect(within(form).getByLabelText('Server address')).toBeDefined()
    expect(screen.getByRole('button', { name: 'Sign in' })).toBeDefined()
  })

  it('shows the code, opens the browser, and waits for the gateway to report the sign-in', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    // The desktop shell refuses a blank window and opens the address itself.
    const open = vi.spyOn(window, 'open').mockReturnValue(null)
    seed()
    const status = vi
      .fn<() => Promise<EdgeStatus>>()
      .mockResolvedValueOnce({ edges: [] })
      .mockResolvedValueOnce({ edges: [], login: pending })
      .mockResolvedValue({ ...signedIn, login: { ...pending, state: 'signed_in', account } })
    const client = fakeApi({
      localLinkStatus: vi.fn(async () => unlinked),
      localEdgeStatus: status,
      localEdgeLogin: vi.fn(async () => pending),
      localEdgeServers: vi.fn(async () => ({ edge, servers: [buildBox] })),
    })
    render(<OnboardingRoute params={{}} client={client} />)

    fireEvent.click(await screen.findByRole('button', { name: 'Sign in' }))

    expect(await screen.findByText('WDJB-MJHT')).toBeDefined()
    expect(screen.getByRole('link', { name: `${edge}/device` }).getAttribute('href')).toBe(
      `${edge}/device`,
    )
    expect(open).toHaveBeenLastCalledWith(`${edge}/device`, '_blank', 'noopener,noreferrer')
    // Both host names, so the person can tell the sign-in page belongs to the edge.
    expect(screen.getByText(/signs you in/).textContent).toBe(
      'auth.example.test signs you in for the edge edge.example.test.',
    )
    expect(screen.getByRole('status').textContent).toContain('Waiting for you to confirm')

    await vi.advanceTimersByTimeAsync(2000)
    await vi.advanceTimersByTimeAsync(2000)

    expect(await screen.findByText('octocat (GitHub)')).toBeDefined()
    expect(await screen.findByText('build-box')).toBeDefined()
    expect(client.localEdgeServers).toHaveBeenCalledWith(edge)
  })

  it('stops waiting with a retry when another window replaces the sign-in', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    vi.spyOn(window, 'open').mockReturnValue(null)
    seed()
    const client = fakeApi({
      localLinkStatus: vi.fn(async () => unlinked),
      localEdgeStatus: vi
        .fn<() => Promise<EdgeStatus>>()
        .mockResolvedValueOnce({ edges: [] })
        .mockResolvedValue({ edges: [], login: { ...pending, user_code: 'QXRT-PLMN' } }),
      localEdgeLogin: vi.fn(async () => pending),
    })
    render(<OnboardingRoute params={{}} client={client} />)

    fireEvent.click(await screen.findByRole('button', { name: 'Sign in' }))
    expect(await screen.findByText('WDJB-MJHT')).toBeDefined()
    await vi.advanceTimersByTimeAsync(2000)

    expect(await screen.findByText('Sign-in was replaced by another window. Try again.')).toBeDefined()
    expect((screen.getByRole('button', { name: 'Sign in' }) as HTMLButtonElement).disabled).toBe(false)
  })

  it('resumes a sign-in still pending when the step opens again', async () => {
    seed()
    const client = fakeApi({
      localLinkStatus: vi.fn(async () => unlinked),
      localEdgeStatus: vi.fn(async () => ({ edges: [], login: pending })),
    })
    render(<OnboardingRoute params={{}} client={client} />)

    expect(await screen.findByText('WDJB-MJHT')).toBeDefined()
    expect(client.localEdgeLogin).not.toHaveBeenCalled()
  })

  it('shows the gateway error when the sign-in cannot start', async () => {
    vi.spyOn(window, 'open').mockReturnValue(null)
    seed()
    const client = fakeApi({
      localLinkStatus: vi.fn(async () => unlinked),
      localEdgeLogin: vi.fn(() =>
        Promise.reject(
          new ApiError(503, 'edge.login: sign in: Post "https://edge.onaether.dev/v1/device": dial tcp: i/o timeout'),
        ),
      ),
    })
    render(<OnboardingRoute params={{}} client={client} />)

    fireEvent.click(await screen.findByRole('button', { name: 'Sign in' }))

    expect(
      await screen.findByText(
        'edge.login: sign in: Post "https://edge.onaether.dev/v1/device": dial tcp: i/o timeout',
      ),
    ).toBeDefined()
    expect(screen.getByRole('button', { name: 'Sign in' })).toBeDefined()
  })

  it('shows why a sign-in failed', async () => {
    seed()
    const client = fakeApi({
      localLinkStatus: vi.fn(async () => unlinked),
      localEdgeStatus: vi.fn(async () => ({
        edges: [],
        login: {
          ...pending,
          state: 'failed' as const,
          error: 'sign in: code WDJB-MJHT expired before it was confirmed; run aether login again',
        },
      })),
    })
    render(<OnboardingRoute params={{}} client={client} />)

    expect(
      await screen.findByText(
        'sign in: code WDJB-MJHT expired before it was confirmed; run aether login again',
      ),
    ).toBeDefined()
    expect(screen.getByRole('button', { name: 'Sign in' })).toBeDefined()
  })

  it('links a listed server only after showing what the link pins', async () => {
    seed()
    const fingerprint = 'SHA256:hostkeyfingerprintexample'
    const client = fakeApi({
      localLinkStatus: vi
        .fn()
        .mockResolvedValueOnce(unlinked)
        .mockResolvedValue({ ...unlinked, server_configured: true, user: 'aether', edge_url: edge, server_id: serverID }),
      localEdgeStatus: vi.fn(async () => signedIn),
      localEdgeServers: vi.fn(async () => ({
        edge,
        servers: [
          buildBox,
          {
            id: 'zyxwvutsrqponmlkjihgfedcba',
            name: 'old-box',
            online: false,
            role: 'viewer',
            access_policy: 'account' as const,
            kind: 'self-hosted' as const,
          },
        ],
      })),
      localEdgeHostKey: vi.fn(async () => ({ edge, server_id: serverID, fingerprint })),
      localEdgeLink: vi.fn(async () => linkResult),
    })
    render(<OnboardingRoute params={{}} client={client} />)

    const servers = within(await screen.findByRole('list', { name: 'Your servers' }))
    const oldBox = servers.getByText('old-box').closest('li')!
    expect(oldBox.textContent).toContain('Offline · viewer · signing in is enough')
    const box = servers.getByText('build-box').closest('li')!
    expect(box.textContent).toContain('Online · admin · a new device waits for approval')
    // The id and claim forms wait behind a disclosure while servers are listed.
    expect(screen.queryByLabelText('Server id from your admin')).toBeNull()
    fireEvent.click(servers.getByRole('button', { name: 'Link build-box' }))

    const confirm = within(await screen.findByRole('region', { name: 'Confirm server' }))
    expect(await confirm.findByText(fingerprint)).toBeDefined()
    expect(confirm.getByText(serverID)).toBeDefined()
    expect(confirm.getByText(/compare the id/).textContent).toContain('aether-server edge status')
    expect(client.localEdgeHostKey).toHaveBeenCalledWith(serverID, edge)
    expect(client.localEdgeLink).not.toHaveBeenCalled()
    fireEvent.click(confirm.getByRole('button', { name: 'Link and pin' }))

    const summary = await screen.findByText(/^Linked to/)
    expect(summary.textContent).toBe('Linked to build-box through edge.example.test as Octo (admin).')
    expect(client.localEdgeLink).toHaveBeenCalledWith(serverID, edge)
    expect(useStore.getState().connectionEpoch).toBeGreaterThan(0)
    fireEvent.click(screen.getByRole('button', { name: 'Continue' }))
    expect(screen.getByRole('listitem', { current: 'step' }).textContent).toContain('Repository')
  })

  it('offers no link when the host key cannot be read, and cancels', async () => {
    seed()
    const refusal = `edge.hostkey: read host key of server ${serverID}: host key SHA256:x is server y, not the listed server ${serverID}`
    const client = fakeApi({
      localLinkStatus: vi.fn(async () => unlinked),
      localEdgeStatus: vi.fn(async () => signedIn),
      localEdgeServers: vi.fn(async () => ({ edge, servers: [buildBox] })),
      localEdgeHostKey: vi.fn(() => Promise.reject(new ApiError(503, refusal))),
    })
    render(<OnboardingRoute params={{}} client={client} />)

    fireEvent.click(await screen.findByRole('button', { name: 'Link build-box' }))
    const confirm = within(await screen.findByRole('region', { name: 'Confirm server' }))
    expect(await confirm.findByText(refusal)).toBeDefined()
    expect((confirm.getByRole('button', { name: 'Link and pin' }) as HTMLButtonElement).disabled).toBe(true)
    fireEvent.click(confirm.getByRole('button', { name: 'Cancel' }))
    expect(screen.queryByRole('region', { name: 'Confirm server' })).toBeNull()
    expect(client.localEdgeLink).not.toHaveBeenCalled()
  })

  it('links by the server id an admin gave, pinned to that id', async () => {
    seed()
    const client = fakeApi({
      localLinkStatus: vi.fn(async () => unlinked),
      localEdgeStatus: vi.fn(async () => signedIn),
      localEdgeServers: vi.fn(async () => ({ edge, servers: [] })),
      localEdgeLink: vi.fn(async () => linkResult),
    })
    render(<OnboardingRoute params={{}} client={client} />)

    fireEvent.click(await screen.findByRole('button', { name: 'Other ways to link' }))
    fireEvent.change(screen.getByLabelText('Server id from your admin'), {
      target: { value: ` ${serverID} ` },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Link by id' }))

    expect((await screen.findByText(/^Linked to/)).textContent).toBe(
      `Linked to ${serverID} through edge.example.test as Octo (admin).`,
    )
    expect(client.localEdgeLink).toHaveBeenCalledWith(serverID, edge)
    expect(client.localEdgeHostKey).not.toHaveBeenCalled()
  })

  it('claims a new server with the code aether-server setup printed', async () => {
    seed()
    const client = fakeApi({
      localLinkStatus: vi.fn(async () => unlinked),
      localEdgeStatus: vi.fn(async () => signedIn),
      localEdgeServers: vi.fn(async () => ({ edge, servers: [] })),
      localEdgeClaim: vi.fn(async () => linkResult),
    })
    render(<OnboardingRoute params={{}} client={client} />)

    expect(await screen.findByText(/Your account reaches no servers yet/)).toBeDefined()
    fireEvent.click(screen.getByRole('button', { name: 'Other ways to link' }))
    // The claim is made for the account the edge reported at sign-in, shown
    // before the code is sent.
    const form = screen.getByRole('form', { name: 'Add a server' })
    expect(form.textContent).toContain("Claiming makes octocat (GitHub) the server's admin.")
    fireEvent.change(screen.getByLabelText('Claim code or link'), { target: { value: ' abcdefgh-example ' } })
    fireEvent.click(screen.getByRole('button', { name: 'Claim and link' }))

    expect((await screen.findByText(/^Linked to/)).textContent).toBe(
      `Linked to ${serverID} through edge.example.test as Octo (admin).`,
    )
    expect(client.localEdgeClaim).toHaveBeenCalledWith('abcdefgh-example', edge)
  })

  it('claims with the code a pasted claim link carries', async () => {
    seed()
    const client = fakeApi({
      localLinkStatus: vi.fn(async () => unlinked),
      localEdgeStatus: vi.fn(async () => signedIn),
      localEdgeServers: vi.fn(async () => ({ edge, servers: [] })),
      localEdgeClaim: vi.fn(async () => linkResult),
    })
    render(<OnboardingRoute params={{}} client={client} />)

    fireEvent.click(await screen.findByRole('button', { name: 'Other ways to link' }))
    fireEvent.change(screen.getByLabelText('Claim code or link'), {
      target: { value: `aether://claim?edge=${encodeURIComponent(edge)}&server=${serverID}#abcdefghijk23456` },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Claim and link' }))

    expect(await screen.findByText(/^Linked to/)).toBeDefined()
    expect(client.localEdgeClaim).toHaveBeenCalledWith(`${serverID}-abcdefghijk23456`, edge)
  })

  it('opens the Join panel for a join link pasted into the claim form', async () => {
    seed()
    const joinLink = `${edge}/join/${serverID}/inv-7k2m9q4xbd#abcdefghijk23456`
    const client = fakeApi({
      localLinkStatus: vi.fn(async () => unlinked),
      localEdgeStatus: vi.fn(async () => signedIn),
      localEdgeServers: vi.fn(async () => ({ edge, servers: [] })),
      localEdgeJoin: vi.fn(async () => linkResult),
    })
    render(<OnboardingRoute params={{}} client={client} />)

    fireEvent.click(await screen.findByRole('button', { name: 'Other ways to link' }))
    fireEvent.change(screen.getByLabelText('Claim code or link'), { target: { value: ` ${joinLink} ` } })
    fireEvent.click(screen.getByRole('button', { name: 'Claim and link' }))

    const join = within(await screen.findByRole('region', { name: 'Join' }))
    fireEvent.click(await join.findByRole('button', { name: 'Join' }))
    expect(await screen.findByText(/^Linked to/)).toBeDefined()
    expect(client.localEdgeJoin).toHaveBeenCalledWith(joinLink)
    expect(client.localEdgeClaim).not.toHaveBeenCalled()
  })

  it('moves the sign-in to the edge a pasted claim link names', async () => {
    seed()
    const other = 'https://edge.other.test'
    const client = fakeApi({
      localLinkStatus: vi.fn(async () => unlinked),
      localEdgeStatus: vi.fn(async () => signedIn),
      localEdgeServers: vi.fn(async () => ({ edge, servers: [] })),
      localEdgeClaim: vi.fn(async () => linkResult),
    })
    render(<OnboardingRoute params={{}} client={client} />)

    fireEvent.click(await screen.findByRole('button', { name: 'Other ways to link' }))
    fireEvent.change(screen.getByLabelText('Claim code or link'), {
      target: { value: `aether://claim?edge=${encodeURIComponent(other)}&server=${serverID}#abcdefghijk23456` },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Claim and link' }))

    expect(await screen.findByRole('button', { name: 'Sign in' })).toBeDefined()
    expect(screen.getByText('edge.other.test')).toBeDefined()
    expect(client.localEdgeClaim).not.toHaveBeenCalled()
  })

  it('treats a claim link for the same host on another scheme as a different edge', async () => {
    seed()
    const client = fakeApi({
      localLinkStatus: vi.fn(async () => unlinked),
      localEdgeStatus: vi.fn(async () => signedIn),
      localEdgeServers: vi.fn(async () => ({ edge, servers: [] })),
      localEdgeClaim: vi.fn(async () => linkResult),
    })
    render(<OnboardingRoute params={{}} client={client} />)

    fireEvent.click(await screen.findByRole('button', { name: 'Other ways to link' }))
    const plain = edge.replace('https://', 'http://')
    fireEvent.change(screen.getByLabelText('Claim code or link'), {
      target: { value: `aether://claim?edge=${encodeURIComponent(plain)}&server=${serverID}#abcdefghijk23456` },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Claim and link' }))

    expect(await screen.findByRole('button', { name: 'Sign in' })).toBeDefined()
    expect(client.localEdgeClaim).not.toHaveBeenCalled()
  })

  it('makes the person choose the edge when signed in to more than one', async () => {
    seed()
    const other = 'https://edge.other.test'
    const client = fakeApi({
      localLinkStatus: vi.fn(async () => unlinked),
      localEdgeStatus: vi.fn(async () => ({
        edges: [
          { edge, account },
          { edge: other, account: { ...account, login: 'octo-work' } },
        ],
      })),
      localEdgeServers: vi.fn(async (e: string) => ({
        edge: e,
        servers: e === other ? [{ ...buildBox, name: 'work-box' }] : [buildBox],
      })),
      localEdgeLink: vi.fn(async () => ({ ...linkResult, edge: other })),
      localEdgeClaim: vi.fn(() => Promise.reject(new ApiError(409, 'claim code is wrong'))),
    })
    render(<OnboardingRoute params={{}} client={client} />)

    const choice = within(await screen.findByRole('group', { name: /signed in to 2 edges/ }))
    expect(screen.queryByLabelText('Server id from your admin')).toBeNull()
    expect(client.localEdgeServers).not.toHaveBeenCalled()

    fireEvent.click(choice.getByRole('radio', { name: 'edge.other.test as octo-work (GitHub)' }))
    expect(await screen.findByText('work-box')).toBeDefined()
    expect(client.localEdgeServers).toHaveBeenCalledWith(other)
    expect(client.localEdgeServers).not.toHaveBeenCalledWith(edge)

    fireEvent.click(screen.getByRole('button', { name: 'Other ways to link' }))
    fireEvent.change(screen.getByLabelText('Claim code or link'), { target: { value: 'abcdefgh-wrong' } })
    fireEvent.click(screen.getByRole('button', { name: 'Claim and link' }))
    expect(await screen.findByText('claim code is wrong')).toBeDefined()
    expect(client.localEdgeClaim).toHaveBeenCalledWith('abcdefgh-wrong', other)

    fireEvent.change(screen.getByLabelText('Server id from your admin'), { target: { value: serverID } })
    fireEvent.click(screen.getByRole('button', { name: 'Link by id' }))
    expect(await screen.findByText(/^Linked to/)).toBeDefined()
    expect(client.localEdgeLink).toHaveBeenCalledWith(serverID, other)
  })

  it('shows the edge refusal of a claim code verbatim and keeps the form', async () => {
    seed()
    const refusal = `ssh handshake with ${serverID}.edge.aether.invalid: ssh: handshake failed: server said: claim code is wrong`
    const client = fakeApi({
      localLinkStatus: vi.fn(async () => unlinked),
      localEdgeStatus: vi.fn(async () => signedIn),
      localEdgeServers: vi.fn(async () => ({ edge, servers: [] })),
      localEdgeClaim: vi.fn(() => Promise.reject(new ApiError(409, refusal))),
    })
    render(<OnboardingRoute params={{}} client={client} />)

    fireEvent.click(await screen.findByRole('button', { name: 'Other ways to link' }))
    fireEvent.change(screen.getByLabelText('Claim code or link'), { target: { value: 'abcdefgh-wrong' } })
    fireEvent.click(screen.getByRole('button', { name: 'Claim and link' }))

    expect(await screen.findByText(refusal)).toBeDefined()
    expect(screen.getByLabelText('Claim code or link')).toBeDefined()
  })
})

describe('a join link handed to the dashboard', () => {
  const joinLink = `aether://join?edge=${encodeURIComponent(edge)}&server=${serverID}&invitation=inv-7k2m9q4xbd#abcdefghijk23456`

  function handOver() {
    window.history.replaceState(null, '', `/?token=t#join=${encodeURIComponent(joinLink)}`)
  }

  it('shows the account it joins as, then joins, and takes the link off the address', async () => {
    handOver()
    seed()
    useStore.setState({ onboardingStep: 'Agent', onboardingFurthest: 'Agent' })
    const client = fakeApi({
      localLinkStatus: vi.fn(async () => unlinked),
      localEdgeStatus: vi.fn(async () => signedIn),
      localEdgeJoin: vi.fn(async () => linkResult),
    })
    render(<OnboardingRoute params={{}} client={client} />)

    expect(window.location.search).toBe('?token=t')
    expect(window.location.hash).toBe('')
    const join = within(await screen.findByRole('region', { name: 'Join' }))
    expect(join.getByText(/as the invited account\?/).textContent).toBe(`Join ${serverID} as the invited account?`)
    expect((await join.findByText(/This device is signed in/)).textContent).toContain('edge.example.test as octocat (GitHub)')
    expect(client.localEdgeJoin).not.toHaveBeenCalled()
    fireEvent.click(join.getByRole('button', { name: 'Join' }))

    expect((await screen.findByText(/^Linked to/)).textContent).toBe(
      `Linked to ${serverID} through edge.example.test as Octo (admin).`,
    )
    expect(client.localEdgeJoin).toHaveBeenCalledWith(joinLink)
    expect(screen.queryByRole('region', { name: 'Join' })).toBeNull()
  })

  it('waits for an admin while the device is pending, then joins', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    handOver()
    seed()
    const pendingDevice = new ApiError(409, 'edge.join: this device waits for an admin', undefined, {
      refusal: { kind: 'device-pending', server: 'build-box', device: 'laptop', fingerprint: 'SHA256:devicekeyexample' },
    })
    const client = fakeApi({
      localLinkStatus: vi.fn(async () => unlinked),
      localEdgeStatus: vi.fn(async () => signedIn),
      localEdgeJoin: vi.fn().mockRejectedValueOnce(pendingDevice).mockResolvedValue(linkResult),
    })
    render(<OnboardingRoute params={{}} client={client} />)

    fireEvent.click(await screen.findByRole('button', { name: 'Join' }))
    const waiting = await screen.findByRole('status')
    expect(within(waiting).getByText('Waiting for an admin of build-box to approve this device')).toBeDefined()
    expect(waiting.textContent).toContain('laptop')
    expect(waiting.textContent).toContain('SHA256:devicekeyexample')
    expect(client.localEdgeJoin).toHaveBeenCalledTimes(1)

    await vi.advanceTimersByTimeAsync(5000)

    expect(await screen.findByText(/^Linked to/)).toBeDefined()
    expect(client.localEdgeJoin).toHaveBeenCalledTimes(2)
  })

  it('shows the console command when nobody can approve in the app, and keeps asking', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    handOver()
    seed()
    const text = 'device "laptop", signed in as github account octocat, is waiting for approval. Approve it as an admin:\n  aether device approve ABCD-EFGH\nor on the server:\n  sudo aether-server device approve ABCD-EFGH'
    const pendingDevice = new ApiError(409, text, undefined, {
      refusal: { kind: 'device-pending', server: 'build-box', device: 'laptop', fingerprint: 'SHA256:devicekeyexample', approval_code: 'ABCD-EFGH' },
    })
    const client = fakeApi({
      localLinkStatus: vi.fn(async () => unlinked),
      localEdgeStatus: vi.fn(async () => signedIn),
      localEdgeJoin: vi.fn().mockRejectedValueOnce(pendingDevice).mockResolvedValue(linkResult),
    })
    render(<OnboardingRoute params={{}} client={client} />)

    fireEvent.click(await screen.findByRole('button', { name: 'Join' }))
    const waiting = await screen.findByRole('status')
    expect(waiting.textContent).toContain('sudo aether-server device approve ABCD-EFGH')
    expect(within(waiting).queryByText(/^Waiting for an admin/)).toBeNull()

    await vi.advanceTimersByTimeAsync(5000)

    expect(await screen.findByText(/^Linked to/)).toBeDefined()
    expect(client.localEdgeJoin).toHaveBeenCalledTimes(2)
  })

  it('offers Sign in again when the edge revoked the saved token', async () => {
    handOver()
    seed()
    const revoked = new ApiError(503, 'the edge revoked this device token; sign in again', undefined, {
      refusal: { kind: 'token-revoked' },
    })
    const client = fakeApi({
      localLinkStatus: vi.fn(async () => unlinked),
      localEdgeStatus: vi.fn(async () => signedIn),
      localEdgeJoin: vi.fn().mockRejectedValue(revoked),
    })
    render(<OnboardingRoute params={{}} client={client} />)

    fireEvent.click(await screen.findByRole('button', { name: 'Join' }))
    const join = within(await screen.findByRole('region', { name: 'Join' }))
    expect(await join.findByRole('button', { name: 'Sign in' })).toBeDefined()
    expect(join.getByRole('alert').textContent).toContain('the edge revoked this device token')
    expect(join.queryByRole('button', { name: 'Join' })).toBeNull()
  })

  it('offers a new sign-in when the invitation is for another account, then joins with it', async () => {
    handOver()
    seed()
    const other = { ...account, id: 'acct_bbbbbbbbbbbbbbbbbbbbbbbbbb', login: 'hubot' }
    const wrong = new ApiError(403, 'this invitation is for octocat, and this device signed in as hubot', undefined, {
      refusal: { kind: 'join-wrong', user: 'octocat', account: 'hubot' },
    })
    const localEdgeStatus = vi
      .fn()
      .mockResolvedValueOnce({ edges: [{ edge, account: other }] })
      .mockResolvedValue({ edges: [{ edge, account }], login: { ...pending, state: 'signed_in', account } })
    const client = fakeApi({
      localLinkStatus: vi.fn(async () => unlinked),
      localEdgeStatus,
      localEdgeLogin: vi.fn(async () => pending),
      localEdgeJoin: vi.fn().mockRejectedValueOnce(wrong).mockResolvedValue(linkResult),
    })
    vi.spyOn(window, 'open').mockReturnValue(null)
    render(<OnboardingRoute params={{}} client={client} />)

    fireEvent.click(await screen.findByRole('button', { name: 'Join' }))
    const join = within(await screen.findByRole('region', { name: 'Join' }))
    expect((await join.findByRole('alert')).textContent).toContain('this invitation is for octocat')
    expect(join.queryByRole('button', { name: 'Join' })).toBeNull()
    fireEvent.click(join.getByRole('button', { name: 'Sign in with another account' }))

    expect(client.localEdgeLogin).toHaveBeenCalledWith(edge)
    expect((await join.findByText(/This device is signed in/, undefined, { timeout: 3000 })).textContent).toContain('as octocat')
    fireEvent.click(join.getByRole('button', { name: 'Join' }))
    expect(await screen.findByText(/^Linked to/)).toBeDefined()
  })

  it('offers a fresh sign-in when the edge says the login is stale, then joins again', async () => {
    handOver()
    seed()
    const stale = new ApiError(403, 'your login and email were last confirmed over 24 hours ago', undefined, {
      refusal: { kind: 'identity-stale' },
    })
    const client = fakeApi({
      localLinkStatus: vi.fn(async () => unlinked),
      localEdgeStatus: vi
        .fn()
        .mockResolvedValueOnce(signedIn)
        .mockResolvedValue({ edges: [{ edge, account }], login: { ...pending, state: 'signed_in', account } }),
      localEdgeLogin: vi.fn(async () => pending),
      localEdgeJoin: vi.fn().mockRejectedValueOnce(stale).mockResolvedValue(linkResult),
    })
    vi.spyOn(window, 'open').mockReturnValue(null)
    render(<OnboardingRoute params={{}} client={client} />)

    fireEvent.click(await screen.findByRole('button', { name: 'Join' }))
    const join = within(await screen.findByRole('region', { name: 'Join' }))
    expect((await join.findByRole('alert')).textContent).toContain('last confirmed over 24 hours ago')
    expect(join.queryByRole('button', { name: 'Join' })).toBeNull()
    fireEvent.click(join.getByRole('button', { name: 'Sign in again' }))

    expect(client.localEdgeLogin).toHaveBeenCalledWith(edge)
    expect((await join.findByText(/This device is signed in/, undefined, { timeout: 3000 })).textContent).toContain('as octocat')
    fireEvent.click(join.getByRole('button', { name: 'Join' }))
    expect(await screen.findByText(/^Linked to/)).toBeDefined()
  })

  it('follows the gateway onto the joined server when the join ends after the panel closed', async () => {
    handOver()
    seed()
    const reconnect = vi.fn()
    useStore.setState({ reconnect })
    let finish: (result: typeof linkResult) => void = () => {}
    const client = fakeApi({
      localLinkStatus: vi.fn(async () => unlinked),
      localEdgeStatus: vi.fn(async () => signedIn),
      localEdgeJoin: vi.fn(() => new Promise<typeof linkResult>((resolve) => (finish = resolve))),
    })
    const { unmount } = render(<OnboardingRoute params={{}} client={client} />)

    fireEvent.click(await screen.findByRole('button', { name: 'Join' }))
    expect(client.localEdgeJoin).toHaveBeenCalledTimes(1)
    unmount()
    finish(linkResult)

    await vi.waitFor(() => expect(reconnect).toHaveBeenCalledTimes(1))
  })

  it('opens a join link handed over while the dashboard is already open', async () => {
    seed()
    useStore.setState({ onboardingStep: 'Agent', onboardingFurthest: 'Agent' })
    const client = fakeApi({
      localLinkStatus: vi.fn(async () => unlinked),
      localEdgeStatus: vi.fn(async () => signedIn),
    })
    render(<OnboardingRoute params={{}} client={client} />)
    useStore.setState({ route: { name: 'board', params: {} } })
    const unbind = bindRouteToUrl(useStore)
    expect(screen.queryByRole('region', { name: 'Join' })).toBeNull()

    handOver()
    window.dispatchEvent(new HashChangeEvent('hashchange'))

    expect(await screen.findByRole('region', { name: 'Join' })).toBeDefined()
    expect(useStore.getState().route.name).toBe('onboarding')
    expect(window.location.hash).toBe('')
    unbind()
  })

  it('replaces a waiting join with a second handed join link and stops asking for the first', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    handOver()
    seed()
    const pendingDevice = new ApiError(409, 'edge.join: this device waits for an admin', undefined, {
      refusal: { kind: 'device-pending', server: 'build-box', device: 'laptop', fingerprint: 'SHA256:devicekeyexample' },
    })
    const client = fakeApi({
      localLinkStatus: vi.fn(async () => unlinked),
      localEdgeStatus: vi.fn(async () => signedIn),
      localEdgeJoin: vi.fn().mockRejectedValue(pendingDevice),
    })
    render(<OnboardingRoute params={{}} client={client} />)

    fireEvent.click(await screen.findByRole('button', { name: 'Join' }))
    expect(await screen.findByRole('status')).toBeDefined()
    expect(client.localEdgeJoin).toHaveBeenCalledTimes(1)

    const otherServer = 'zyxwvutsrqponmlkjihgfedcba'
    const second = joinLink.replace(serverID, otherServer)
    window.history.replaceState(null, '', `/?token=t#join=${encodeURIComponent(second)}`)
    window.dispatchEvent(new HashChangeEvent('hashchange'))

    const join = within(await screen.findByRole('region', { name: 'Join' }))
    expect(join.getByText(/as the invited account\?/).textContent).toBe(`Join ${otherServer} as the invited account?`)
    expect(screen.queryByRole('status')).toBeNull()
    await vi.advanceTimersByTimeAsync(10000)
    expect(client.localEdgeJoin).toHaveBeenCalledTimes(1)
    expect(client.localEdgeJoin).toHaveBeenCalledWith(joinLink)
  })

  it('shows the command where the dashboard has no edge.join', async () => {
    handOver()
    seed()
    useStore.setState({ capabilities: { ...localCaps, local: [] } })
    const client = fakeApi()
    render(<OnboardingRoute params={{}} client={client} />)

    const command = await screen.findByRole('region', { name: 'Link from a terminal' })
    expect(within(command).getByText(`aether link '${joinLink}'`)).toBeDefined()
    expect(client.localLinkStatus).not.toHaveBeenCalled()
    expect(client.localEdgeJoin).not.toHaveBeenCalled()
  })
})

describe('a claim link handed to the dashboard', () => {
  it('shows a command that claims through the edge the link names where the dashboard has no edge.claim', async () => {
    const custom = 'https://edge.example.test:8443'
    const claimLink = `aether://claim?edge=${encodeURIComponent(custom)}&server=${serverID}#abcdefghijk23456`
    window.history.replaceState(null, '', `/?token=t#claim=${encodeURIComponent(claimLink)}`)
    seed()
    useStore.setState({ capabilities: { ...localCaps, local: [] } })
    render(<OnboardingRoute params={{}} client={fakeApi()} />)

    const command = await screen.findByRole('region', { name: 'Link from a terminal' })
    expect(within(command).getByText(`aether link --claim ${serverID}-abcdefghijk23456 --edge ${custom}`)).toBeDefined()
  })

  it('keeps the later edge when status reads resolve out of order', async () => {
    const custom = 'https://edge.custom.test:8443'
    const claimTo = (to: string) => `aether://claim?edge=${encodeURIComponent(to)}&server=${serverID}#abcdefghijk23456`
    window.history.replaceState(null, '', `/?token=t#claim=${encodeURIComponent(claimTo(edge))}`)
    seed()
    let first: (status: EdgeStatus) => void = () => {}
    const localEdgeStatus = vi
      .fn<() => Promise<EdgeStatus>>()
      .mockReturnValueOnce(new Promise((resolve) => (first = resolve)))
      .mockResolvedValue({ edges: [] })
    const client = fakeApi({ localLinkStatus: vi.fn(async () => unlinked), localEdgeStatus })
    render(<OnboardingRoute params={{}} client={client} />)
    await vi.waitFor(() => expect(localEdgeStatus).toHaveBeenCalledTimes(1))

    window.history.replaceState(null, '', `/?token=t#claim=${encodeURIComponent(claimTo(custom))}`)
    window.dispatchEvent(new HashChangeEvent('hashchange'))
    const section = within(await screen.findByRole('region', { name: 'Sign in' }))
    expect(await section.findByRole('button', { name: 'Sign in' })).toBeDefined()

    first(signedIn)
    await new Promise((resolve) => setTimeout(resolve, 20))
    expect(screen.getByRole('region', { name: 'Sign in' })).toBeDefined()
    expect(screen.queryByText(/octocat/)).toBeNull()
  })

  it('signs in to the custom edge the link names and claims through it', async () => {
    const custom = 'https://edge.custom.test:8443'
    const claimLink = `aether://claim?edge=${encodeURIComponent(custom)}&server=${serverID}#abcdefghijk23456`
    window.history.replaceState(null, '', `/?token=t#claim=${encodeURIComponent(claimLink)}`)
    vi.spyOn(window, 'open').mockReturnValue(null)
    seed()
    const customLogin = { ...pending, edge: custom, verification_uri: `${custom}/device` }
    const client = fakeApi({
      localLinkStatus: vi.fn(async () => unlinked),
      localEdgeStatus: vi
        .fn<() => Promise<EdgeStatus>>()
        .mockResolvedValueOnce(signedIn)
        .mockResolvedValue({ edges: [{ edge, account }, { edge: custom, account }], login: { ...customLogin, state: 'signed_in', account } }),
      localEdgeLogin: vi.fn(async () => customLogin),
      localEdgeServers: vi.fn(async () => ({ edge: custom, servers: [] })),
      localEdgeClaim: vi.fn(async () => ({ ...linkResult, edge: custom })),
    })
    render(<OnboardingRoute params={{}} client={client} />)

    const section = within(await screen.findByRole('region', { name: 'Sign in' }))
    expect(section.getByText('edge.custom.test:8443')).toBeDefined()
    fireEvent.click(await section.findByRole('button', { name: 'Sign in' }))
    expect(client.localEdgeLogin).toHaveBeenCalledWith(custom)

    fireEvent.click(await screen.findByRole('button', { name: 'Claim and link' }, { timeout: 4000 }))
    expect(await screen.findByText(/^Linked to/)).toBeDefined()
    expect(client.localEdgeClaim).toHaveBeenCalledWith(`${serverID}-abcdefghijk23456`, custom)
  })
})

describe('a claim link handed to a linked dashboard', () => {
  it('offers the claim form, prefilled, with the account it claims as', async () => {
    const claimLink = `aether://claim?edge=${encodeURIComponent(edge)}&server=${serverID}#abcdefghijk23456`
    window.history.replaceState(null, '', `/?token=t#claim=${encodeURIComponent(claimLink)}`)
    seed()
    const client = fakeApi({
      localLinkStatus: vi.fn(async () => ({ server_configured: true, linked: true, addr: 'build-box:2222', user: 'aether', repo: '' })),
      localEdgeStatus: vi.fn(async () => signedIn),
      localEdgeServers: vi.fn(async () => ({ edge, servers: [] })),
    })
    render(<OnboardingRoute params={{}} client={client} />)

    expect((await screen.findByLabelText('Claim code or link') as HTMLInputElement).value).toBe(claimLink)
    expect(screen.getByRole('form', { name: 'Add a server' }).textContent).toContain("Claiming makes octocat (GitHub) the server's admin.")
    expect(screen.getByText(/^Linked to/)).toBeDefined()
  })
})
