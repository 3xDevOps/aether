import { fireEvent, render, screen, within } from '@testing-library/react'
import { ApiError } from '@/lib/api'
import type { EdgeLogin, EdgeStatus, GatewayCapabilities, LinkStatus } from '@/lib/types'
import { OnboardingRoute } from '@/routes/onboarding'
import { useStore } from '@/store'
import { fakeApi, serverInfo, workspace } from '@/test/fixtures'

const edge = 'https://edge.example.test'
const serverID = 'abcdefghijklmnopqrstuvwxyz'

const localCaps: GatewayCapabilities = {
  gateway: 'local',
  methods: ['*'],
  ws: ['events', 'attach', 'terminal'],
  local: ['link.status', 'edge.login', 'edge.status', 'edge.servers', 'edge.link', 'edge.claim'],
}

const unlinked: LinkStatus = {
  server_configured: false,
  linked: false,
  addr: '',
  user: '',
  repo: '',
}

const account = { provider: 'github', subject: '583231', login: 'octocat' }

const pending: EdgeLogin = {
  state: 'pending',
  edge,
  user_code: 'WDJB-MJHT',
  verification_uri: `${edge}/device`,
}

const signedIn: EdgeStatus = { edges: [{ edge, account }] }

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
    onboardingStep: 'Link',
    onboardingFurthest: 'Link',
    onboardingWorkspace: '',
  })
}

afterEach(() => {
  vi.restoreAllMocks()
  vi.useRealTimers()
})

describe('onboarding link through an edge', () => {
  it('offers sign-in first and keeps the address form for Tailscale or a direct address', async () => {
    seed()
    render(<OnboardingRoute params={{}} client={fakeApi({ localLinkStatus: vi.fn(async () => unlinked) })} />)

    const signIn = await screen.findByRole('button', { name: 'Sign in' })
    const form = screen.getByRole('form', { name: 'Link server' })
    expect(within(form).getByText('Tailscale or a direct address')).toBeDefined()
    expect(within(form).getByLabelText('Server address')).toBeDefined()
    // Sign-in comes first in reading order.
    expect(signIn.compareDocumentPosition(form) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
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
      localEdgeServers: vi.fn(async () => ({
        edge,
        servers: [{ id: serverID, name: 'build-box', online: true, role: 'admin' }],
      })),
    })
    render(<OnboardingRoute params={{}} client={client} />)

    fireEvent.click(await screen.findByRole('button', { name: 'Sign in' }))

    expect(await screen.findByText('WDJB-MJHT')).toBeDefined()
    expect(screen.getByRole('link', { name: `${edge}/device` }).getAttribute('href')).toBe(
      `${edge}/device`,
    )
    expect(open).toHaveBeenLastCalledWith(`${edge}/device`, '_blank', 'noopener,noreferrer')
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

  it('links a server the signed-in account reaches, then continues', async () => {
    seed()
    const client = fakeApi({
      localLinkStatus: vi
        .fn()
        .mockResolvedValueOnce(unlinked)
        .mockResolvedValue({ ...unlinked, server_configured: true, user: 'aether', edge_url: edge, server_id: serverID }),
      localEdgeStatus: vi.fn(async () => signedIn),
      localEdgeServers: vi.fn(async () => ({
        edge,
        servers: [
          { id: serverID, name: 'build-box', online: true, role: 'admin' },
          { id: 'zyxwvutsrqponmlkjihgfedcba', name: 'old-box', online: false, role: 'viewer' },
        ],
      })),
      localEdgeLink: vi.fn(async () => linkResult),
    })
    render(<OnboardingRoute params={{}} client={client} />)

    const servers = within(await screen.findByRole('list', { name: 'Your servers' }))
    expect(servers.getByText('old-box').closest('li')!.textContent).toContain('offline')
    fireEvent.click(servers.getByRole('button', { name: 'Link build-box' }))

    const summary = await screen.findByText(/^Linked to/)
    expect(summary.textContent).toBe('Linked to build-box through edge.example.test as Octo (admin).')
    expect(client.localEdgeLink).toHaveBeenCalledWith(serverID, edge)
    expect(useStore.getState().connectionEpoch).toBeGreaterThan(0)
    fireEvent.click(screen.getByRole('button', { name: 'Continue' }))
    expect(screen.getByRole('listitem', { current: 'step' }).textContent).toContain('Git identity')
  })

  it('claims a new server with the code aether-server setup printed', async () => {
    seed()
    const client = fakeApi({
      localLinkStatus: vi.fn(async () => unlinked),
      localEdgeStatus: vi.fn(async () => signedIn),
      localEdgeServers: vi.fn(async () => ({ edge, servers: [] })),
      localEdgeClaim: vi.fn(async () => ({ ...linkResult, server_name: 'fresh-box' })),
    })
    render(<OnboardingRoute params={{}} client={client} />)

    expect(await screen.findByText(/Your account reaches no servers yet/)).toBeDefined()
    fireEvent.click(screen.getByRole('button', { name: 'Add a server' }))
    fireEvent.change(screen.getByLabelText('Claim code'), { target: { value: ' abcdefgh-example ' } })
    fireEvent.click(screen.getByRole('button', { name: 'Claim and link' }))

    expect((await screen.findByText(/^Linked to/)).textContent).toBe(
      'Linked to fresh-box through edge.example.test as Octo (admin).',
    )
    expect(client.localEdgeClaim).toHaveBeenCalledWith('abcdefgh-example', edge)
  })

  it('shows the edge refusal of a claim code verbatim and keeps the form', async () => {
    seed()
    const refusal =
      'edge.claim: claim server abcdefgh: edge.example.test refused: claim code is wrong (HTTP 403)'
    const client = fakeApi({
      localLinkStatus: vi.fn(async () => unlinked),
      localEdgeStatus: vi.fn(async () => signedIn),
      localEdgeServers: vi.fn(async () => ({ edge, servers: [] })),
      localEdgeClaim: vi.fn(() => Promise.reject(new ApiError(409, refusal))),
    })
    render(<OnboardingRoute params={{}} client={client} />)

    fireEvent.click(await screen.findByRole('button', { name: 'Add a server' }))
    fireEvent.change(screen.getByLabelText('Claim code'), { target: { value: 'abcdefgh-wrong' } })
    fireEvent.click(screen.getByRole('button', { name: 'Claim and link' }))

    expect(await screen.findByText(refusal)).toBeDefined()
    expect(screen.getByLabelText('Claim code')).toBeDefined()
  })
})
