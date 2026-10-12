import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { toast } from 'sonner'
import { ApiError } from '@/lib/api'
import type { ServerEdge } from '@/lib/types'
import { RemoteAccessSection } from '@/routes/settings/remote-access'
import { useStore } from '@/store'
import { fakeApi, serverInfo } from '@/test/fixtures'

vi.mock('sonner', () => ({ toast: { error: vi.fn() } }))

const edgeURL = 'https://edge.example'
const ownerless: ServerEdge = {
  edge_url: edgeURL,
  server_id: 'wqc4lsjvzdzrwq3k5dabdtajwj',
  access_policy: 'approved-devices',
  owner: null,
  connected: true,
  claimed: false,
}
const desktop = ['edge.status', 'edge.login', 'edge.claim']

function seed(role: 'admin' | 'collaborator', edge: ServerEdge, local: string[] = desktop) {
  useStore.setState({
    info: { ...serverInfo, member: { ...serverInfo.member, role } },
    capabilities: { gateway: 'local', methods: ['*'], ws: ['events'], local },
    edge,
  })
}

it('shows the edge rows', () => {
  seed('collaborator', { ...ownerless, owner: { provider: 'github', login: 'octo' }, claimed: true })
  render(<RemoteAccessSection client={fakeApi()} />)
  expect(screen.getByText(edgeURL)).toBeTruthy()
  expect(screen.getByText(ownerless.server_id)).toBeTruthy()
  expect(screen.getByText('approved-devices')).toBeTruthy()
  expect(screen.getByText('octo (github)')).toBeTruthy()
  expect(screen.getByText('connected')).toBeTruthy()
  expect(screen.queryByRole('button', { name: 'Connect my GitHub account' })).toBeNull()
})

it('offers Connect my GitHub account only to an admin of a server with no owner', () => {
  seed('collaborator', ownerless)
  const { unmount } = render(<RemoteAccessSection client={fakeApi()} />)
  expect(screen.getByText('the edge has not recorded this server')).toBeTruthy()
  expect(screen.queryByRole('button', { name: 'Connect my GitHub account' })).toBeNull()
  unmount()

  seed('admin', ownerless, [])
  render(<RemoteAccessSection client={fakeApi()} />)
  expect(screen.queryByRole('button', { name: 'Connect my GitHub account' })).toBeNull()
  expect(screen.getByText('Run aether edge connect from a linked terminal')).toBeTruthy()
})

it('claims with the code server.edge.claimcode issues and shows the new owner', async () => {
  seed('admin', ownerless)
  const client = fakeApi({
    localEdgeStatus: vi.fn(async () => ({ edges: [{ edge: edgeURL, account: { id: 'acct_1', provider: 'github', subject: '1001', login: 'octo' } }] })),
    serverEdgeClaimCode: vi.fn(async () => ({ code: 'wqc4lsjvzdzrwq3k5dabdtajwj-abcdefghijklmnop', expires_at: '2026-10-12T10:30:00Z' })),
    localEdgeClaim: vi.fn(async () => ({ server_id: ownerless.server_id, edge: edgeURL, user: 'aether', member: { id: 'mem_1', display_name: 'Alice', role: 'admin' } })),
    serverEdge: vi.fn(async () => ({ ...ownerless, owner: { provider: 'github', login: 'octo' }, claimed: true })),
  })
  render(<RemoteAccessSection client={client} />)
  fireEvent.click(await screen.findByRole('button', { name: 'Connect my GitHub account (octo)' }))
  await screen.findByText('octo (github)')
  expect(client.localEdgeLogin).not.toHaveBeenCalled()
  expect(client.localEdgeClaim).toHaveBeenCalledWith('wqc4lsjvzdzrwq3k5dabdtajwj-abcdefghijklmnop', edgeURL)
  await waitFor(() => expect(vi.mocked(client.serverEdgeClaimCode).mock.invocationCallOrder[0])
    .toBeLessThan(vi.mocked(client.localEdgeClaim).mock.invocationCallOrder[0]))
})

it('refuses to claim when the gateway now reaches another server', async () => {
  seed('admin', ownerless)
  const client = fakeApi({
    localEdgeStatus: vi.fn(async () => ({ edges: [{ edge: edgeURL, account: { id: 'acct_1', provider: 'github', subject: '1001', login: 'octo' } }] })),
    serverEdgeClaimCode: vi.fn(async () => ({ code: 'abcdefghijklmnopqrstuvwxyz-abcdefghijklmnop', expires_at: '2026-10-12T10:30:00Z' })),
  })
  render(<RemoteAccessSection client={client} />)
  fireEvent.click(await screen.findByRole('button', { name: 'Connect my GitHub account (octo)' }))
  expect(await screen.findByText('The gateway now reaches another server; reload and try again.')).toBeTruthy()
  expect(client.localEdgeClaim).not.toHaveBeenCalled()
})

it('signs in afresh when the edge revoked the saved token, then claims', async () => {
  seed('admin', ownerless)
  const stale = { id: 'acct_1', provider: 'github', subject: '1001', login: 'octo' }
  const login = { state: 'pending' as const, edge: edgeURL, signin_origin: edgeURL, user_code: 'WDJB-MJHT', verification_uri: `${edgeURL}/device` }
  const revoked = new ApiError(503, 'the edge revoked this device token; sign in again', undefined, { refusal: { kind: 'token-revoked' } })
  const client = fakeApi({
    localEdgeStatus: vi.fn()
      .mockResolvedValueOnce({ edges: [{ edge: edgeURL, account: stale }] })
      .mockResolvedValueOnce({ edges: [{ edge: edgeURL, account: stale }] })
      .mockResolvedValue({ edges: [{ edge: edgeURL, account: stale }], login: { ...login, state: 'signed_in', account: stale } }),
    localEdgeLogin: vi.fn(async () => login),
    serverEdgeClaimCode: vi.fn(async () => ({ code: 'wqc4lsjvzdzrwq3k5dabdtajwj-abcdefghijklmnop', expires_at: '2026-10-12T10:30:00Z' })),
    localEdgeClaim: vi.fn()
      .mockRejectedValueOnce(revoked)
      .mockResolvedValue({ server_id: ownerless.server_id, edge: edgeURL, user: 'aether', member: { id: 'mem_1', display_name: 'Alice', role: 'admin' } }),
    serverEdge: vi.fn(async () => ({ ...ownerless, owner: { provider: 'github', login: 'octo' }, claimed: true })),
  })
  render(<RemoteAccessSection client={client} />)
  fireEvent.click(await screen.findByRole('button', { name: 'Connect my GitHub account (octo)' }))
  expect(await screen.findByText('WDJB-MJHT')).toBeTruthy()
  await screen.findByText('octo (github)', undefined, { timeout: 5000 })
  expect(client.localEdgeLogin).toHaveBeenCalledWith(edgeURL)
  expect(client.localEdgeClaim).toHaveBeenCalledTimes(2)
  expect(screen.queryByRole('alert')).toBeNull()
})

it('refreshes the owner and shows remote_error when the clone remote was not updated', async () => {
  seed('admin', ownerless)
  const client = fakeApi({
    localEdgeStatus: vi.fn(async () => ({ edges: [{ edge: edgeURL, account: { id: 'acct_1', provider: 'github', subject: '1001', login: 'octo' } }] })),
    serverEdgeClaimCode: vi.fn(async () => ({ code: 'wqc4lsjvzdzrwq3k5dabdtajwj-abcdefghijklmnop', expires_at: '2026-10-12T10:30:00Z' })),
    localEdgeClaim: vi.fn(async () => ({
      server_id: ownerless.server_id,
      edge: edgeURL,
      user: 'aether',
      member: { id: 'mem_1', display_name: 'Alice', role: 'admin' },
      remote_error: 'the link is saved, but the clone\'s aether remote was not updated: no such directory',
    })),
    serverEdge: vi.fn(async () => ({ ...ownerless, owner: { provider: 'github', login: 'octo' }, claimed: true })),
  })
  render(<RemoteAccessSection client={client} />)
  fireEvent.click(await screen.findByRole('button', { name: 'Connect my GitHub account (octo)' }))
  await screen.findByText('octo (github)')
  expect(toast.error).toHaveBeenCalledWith("the link is saved, but the clone's aether remote was not updated: no such directory")
})
