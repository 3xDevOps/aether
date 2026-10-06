import { fireEvent, render, screen, within } from '@testing-library/react'
import { ApiError } from '@/lib/api'
import type { EdgeLogin, EdgeServer, EdgeStatus, GatewayCapabilities, LinkStatus } from '@/lib/types'
import { OnboardingRoute } from '@/routes/onboarding'
import { useStore } from '@/store'
import { fakeApi, serverInfo, workspace } from '@/test/fixtures'

const edge = 'https://edge.example.test'
const serverID = 'abcdefghijklmnopqrstuvwxyz'

const localCaps: GatewayCapabilities = {
  gateway: 'local',
  methods: ['*'],
  ws: ['events', 'attach', 'terminal'],
  local: ['link.status', 'edge.login', 'edge.status', 'edge.servers', 'edge.hostkey', 'edge.link', 'edge.claim'],
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
})

describe('onboarding link through an edge', () => {
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
    fireEvent.change(screen.getByLabelText('Claim code'), { target: { value: ' abcdefgh-example ' } })
    fireEvent.click(screen.getByRole('button', { name: 'Claim and link' }))

    expect((await screen.findByText(/^Linked to/)).textContent).toBe(
      `Linked to ${serverID} through edge.example.test as Octo (admin).`,
    )
    expect(client.localEdgeClaim).toHaveBeenCalledWith('abcdefgh-example', edge)
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
    fireEvent.change(screen.getByLabelText('Claim code'), { target: { value: 'abcdefgh-wrong' } })
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
    fireEvent.change(screen.getByLabelText('Claim code'), { target: { value: 'abcdefgh-wrong' } })
    fireEvent.click(screen.getByRole('button', { name: 'Claim and link' }))

    expect(await screen.findByText(refusal)).toBeDefined()
    expect(screen.getByLabelText('Claim code')).toBeDefined()
  })
})
