import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { ApiError, type Api } from '@/lib/api'
import type { Connector, ServerEdge } from '@/lib/types'
import { ConnectorsSection, CONNECTORS_SECTION_ID } from '@/routes/settings/connectors'
import { useStore } from '@/store'
import { fakeApi } from '@/test/fixtures'

vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

const chatgpt: Connector = {
  id: 'con-7k2m9q4xbd',
  member_id: 'mem_1',
  client_name: 'ChatGPT',
  redirect_host: 'chatgpt.com',
  scopes: ['read', 'write'],
  created_at: '2026-10-18T10:00:00Z',
}

const claude: Connector = { ...chatgpt, id: 'con-claude00001', client_name: 'Claude', redirect_host: 'claude.ai', scopes: ['read'] }

function show(over: Partial<Api> = {}, params: Record<string, string> = {}) {
  useStore.setState({
    ...useStore.getInitialState(),
    route: { name: 'settings', params },
    capabilities: { gateway: 'remote', methods: ['*'], ws: [] },
  }, true)
  const client = fakeApi(over)
  render(<ConnectorsSection client={client} />)
  return client
}

afterEach(() => {
  vi.useRealTimers()
  vi.clearAllMocks()
})

it('lists connectors and revokes one after confirming', async () => {
  const client = show({
    connectorList: vi.fn().mockResolvedValueOnce({ connectors: [chatgpt] }).mockResolvedValue({ connectors: [] }),
  })
  expect(await screen.findByText('ChatGPT (chatgpt.com)')).toBeTruthy()
  expect(screen.getByText('Read, Launch and message')).toBeTruthy()
  expect(screen.getByText(/never used/)).toBeTruthy()
  fireEvent.click(screen.getByRole('button', { name: 'Revoke ChatGPT (chatgpt.com)' }))
  fireEvent.click(await screen.findByRole('button', { name: 'Revoke' }))
  await waitFor(() => expect(client.connectorRevoke).toHaveBeenCalledWith('con-7k2m9q4xbd'))
  expect(await screen.findByText('No connectors yet')).toBeTruthy()
  expect(screen.queryByText('ChatGPT (chatgpt.com)')).toBeNull()
})

it('shows the URL and code from connector.code with the code’s expiry, then the new connector', async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true, now: Date.parse('2026-10-25T12:00:00Z') })
  show({
    connectorList: vi.fn().mockResolvedValueOnce({ connectors: [] }).mockResolvedValueOnce({ connectors: [] }).mockResolvedValue({ connectors: [chatgpt] }),
  })
  expect(await screen.findByText('The URL appears with the code.')).toBeTruthy()
  expect(screen.getByRole('link', { name: 'ChatGPT Plugins' }).getAttribute('href')).toBe('https://chatgpt.com/plugins')
  fireEvent.click(screen.getByRole('button', { name: 'Get a code' }))
  expect(await screen.findByText('ABCD-EFGH')).toBeTruthy()
  expect(screen.getByText('https://edge.example/mcp/srv')).toBeTruthy()
  expect(screen.getByRole('timer').textContent).toMatch(/^One use, expires in (10:00|9:59)\.$/)
  expect(screen.getByText('Waiting for ChatGPT to connect…')).toBeTruthy()
  await vi.advanceTimersByTimeAsync(3000)
  const done = await screen.findByText('ChatGPT (chatgpt.com) connected')
  expect(done.closest('[data-slot=callout]')?.textContent).toContain('With Read, Launch and message.')
  expect(screen.queryByText('ABCD-EFGH')).toBeNull()
})

it('counts a connector added before the code as already there, and says when the code expired', async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true, now: Date.parse('2026-10-25T12:00:00Z') })
  show({
    connectorList: vi.fn().mockResolvedValueOnce({ connectors: [] }).mockResolvedValue({ connectors: [chatgpt] }),
  })
  await screen.findByText('No connectors yet')
  fireEvent.click(screen.getByRole('button', { name: 'Get a code' }))
  expect(await screen.findByText('ABCD-EFGH')).toBeTruthy()
  expect(screen.getByText('ChatGPT (chatgpt.com)')).toBeTruthy()
  await vi.advanceTimersByTimeAsync(6000)
  expect(screen.queryByText(/connected$/)).toBeNull()
  await vi.advanceTimersByTimeAsync(600_000)
  expect(await screen.findByText(/The last code expired\./)).toBeTruthy()
  expect(screen.queryByText('ABCD-EFGH')).toBeNull()
})

it('shows the server error and the edge fix when no code can be minted', async () => {
  const msg = 'connector.code: this server has no edge (edge-url is empty)'
  show({
    serverEdge: vi.fn(async (): Promise<ServerEdge> => ({ edge_url: '', server_id: 'srv', access_policy: 'account', owner: null, connected: false, claimed: false })),
    connectorCode: vi.fn().mockRejectedValue(new ApiError(500, msg)),
  })
  expect(await screen.findByText(/This server has no edge/)).toBeTruthy()
  fireEvent.click(screen.getByRole('button', { name: 'Get a code' }))
  const alert = await screen.findByRole('alert')
  expect(alert.textContent).toContain('this server has no edge (edge-url is empty)')
  expect(within(alert).getByRole('link', { name: 'Edge guide' }).getAttribute('href')).toContain('docs/edge.md#turning-it-on')
})

it('tells an admin to update a server too old for connectors', async () => {
  show({ connectorList: vi.fn().mockRejectedValue(new ApiError(200, 'method not found: connector.list', -32601)) })
  const alert = await screen.findByRole('alert')
  expect(alert.textContent).toContain('method not found: connector.list')
  expect(alert.textContent).toContain('sudo aether-server update')
  expect(screen.queryByRole('button', { name: 'Get a code' })).toBeNull()
})

it('reconnects by revoking and minting a code on the connector’s own tab', async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true, now: Date.parse('2026-10-25T12:00:00Z') })
  const client = show({
    connectorList: vi.fn().mockResolvedValueOnce({ connectors: [claude] }).mockResolvedValue({ connectors: [] }),
  })
  fireEvent.click(await screen.findByRole('button', { name: 'Reconnect Claude (claude.ai)' }))
  fireEvent.click(await screen.findByRole('button', { name: 'Reconnect' }))
  expect(await screen.findByText('ABCD-EFGH')).toBeTruthy()
  expect(client.connectorRevoke).toHaveBeenCalledWith('con-claude00001')
  expect(screen.getByRole('tab', { name: 'Claude' }).getAttribute('aria-selected')).toBe('true')
  expect(screen.getByRole('link', { name: 'Claude connectors' })).toBeTruthy()
})

it('scrolls into view from a link', async () => {
  const scroll = vi.mocked(Element.prototype.scrollIntoView)
  scroll.mockClear()
  show({}, { section: CONNECTORS_SECTION_ID })
  await screen.findByText('No connectors yet')
  expect(scroll.mock.contexts.at(-1)).toBe(document.getElementById(CONNECTORS_SECTION_ID))
})
