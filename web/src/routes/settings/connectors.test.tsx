import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { ApiError, type Api } from '@/lib/api'
import type { Connector, ServerEdge } from '@/lib/types'
import { ConnectorsSection } from '@/routes/settings/connectors'
import { useStore } from '@/store'
import { fakeApi } from '@/test/fixtures'

vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

const chatgpt: Connector = {
  id: 'con-7k2m9q4xbd',
  member_id: 'mem_1',
  client_id: 'https://chatgpt.com/oauth/client.json',
  client_name: 'ChatGPT',
  redirect_host: 'chatgpt.com',
  scopes: ['read', 'write'],
  created_at: '2026-10-18T10:00:00Z',
}

const claude: Connector = { ...chatgpt, id: 'con-claude00001', client_id: 'https://claude.ai/oauth/mcp-oauth-client-metadata', client_name: 'Claude', redirect_host: 'claude.ai', scopes: ['read'] }

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
  vi.restoreAllMocks()
  localStorage.clear()
  vi.useRealTimers()
  vi.clearAllMocks()
})

it('lists connectors and drops a revoked one even when the list then fails', async () => {
  const client = show({
    connectorList: vi.fn().mockResolvedValueOnce({ connectors: [chatgpt] }).mockRejectedValue(new ApiError(500, 'connector.list: store is closed')),
  })
  expect(await screen.findByText('ChatGPT (chatgpt.com)')).toBeTruthy()
  expect(screen.getByText('Read, Launch and message')).toBeTruthy()
  expect(screen.getByText(/never used/)).toBeTruthy()
  fireEvent.click(screen.getByRole('button', { name: 'Revoke ChatGPT (chatgpt.com)' }))
  fireEvent.click(await screen.findByRole('button', { name: 'Revoke' }))
  await waitFor(() => expect(client.connectorRevoke).toHaveBeenCalledWith('con-7k2m9q4xbd'))
  expect((await screen.findByRole('alert')).textContent).toContain('store is closed')
  expect(screen.getByText('No connectors yet')).toBeTruthy()
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

it('reconnects on the connector’s own tab with a code, revoking it only once its replacement calls Aether, from a server that lists no client_id', async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true, now: Date.parse('2026-10-25T12:00:00Z') })
  const older = { ...claude, client_id: undefined }
  const replacement = { ...older, id: 'con-claude00002', last_used_at: '2026-10-25T12:00:05Z' }
  const client = show({
    connectorList: vi.fn().mockResolvedValueOnce({ connectors: [older] }).mockResolvedValueOnce({ connectors: [older] })
      .mockResolvedValue({ connectors: [older, replacement] }),
  })
  fireEvent.click(await screen.findByRole('button', { name: 'Reconnect Claude (claude.ai)' }))
  fireEvent.click(await screen.findByRole('button', { name: 'Reconnect' }))
  expect(await screen.findByText('ABCD-EFGH')).toBeTruthy()
  expect(screen.getByRole('tab', { name: 'Claude' }).getAttribute('aria-selected')).toBe('true')
  expect(screen.getByText('Waiting for Claude to connect…')).toBeTruthy()
  expect(client.connectorRevoke).not.toHaveBeenCalled()
  await vi.advanceTimersByTimeAsync(3000)
  await waitFor(() => expect(client.connectorRevoke).toHaveBeenCalledWith('con-claude00001'))
  expect(await screen.findByText('Claude (claude.ai) connected')).toBeTruthy()
})

it('keeps the connector when reconnecting cannot get a code', async () => {
  const client = show({
    connectorList: vi.fn().mockResolvedValue({ connectors: [claude] }),
    connectorCode: vi.fn().mockRejectedValue(new ApiError(503, 'connector.code: edge https://edge.example speaks edge protocol 1 and cannot forward connectors; its operator upgrades it')),
  })
  fireEvent.click(await screen.findByRole('button', { name: 'Reconnect Claude (claude.ai)' }))
  fireEvent.click(await screen.findByRole('button', { name: 'Reconnect' }))
  expect(await within(screen.getByRole('alertdialog')).findByText(/speaks edge protocol 1/)).toBeTruthy()
  expect(client.connectorRevoke).not.toHaveBeenCalled()
  expect(screen.getByText('Claude (claude.ai)')).toBeTruthy()
})

it('shows a failed check while waiting, and forgets the code when the member changes', async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true, now: Date.parse('2026-10-25T12:00:00Z') })
  show({
    connectorList: vi.fn().mockResolvedValueOnce({ connectors: [] }).mockResolvedValueOnce({ connectors: [] }).mockRejectedValue(new ApiError(502, 'gateway: server unreachable')),
  })
  await screen.findByText('No connectors yet')
  fireEvent.click(screen.getByRole('button', { name: 'Get a code' }))
  expect(await screen.findByText('ABCD-EFGH')).toBeTruthy()
  await vi.advanceTimersByTimeAsync(3000)
  expect((await screen.findByRole('alert')).textContent).toContain('server unreachable')
  act(() => useStore.setState({ identityKey: 'someone-else' }))
  expect(screen.queryByText('ABCD-EFGH')).toBeNull()
})

const universal = 'https://edge.example/mcp'
const own = 'https://edge.example/mcp/srv'
const signedIn = { url: universal, universal_url: universal, sign_in: true }

it('shows the universal URL without minting a code when signing in replaces it, and watches for the connection', async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true, now: Date.parse('2026-10-25T12:00:00Z') })
  const client = show({
    connectorList: vi.fn().mockResolvedValueOnce({ connectors: [], ...signedIn }).mockResolvedValueOnce({ connectors: [], ...signedIn })
      .mockResolvedValue({ connectors: [{ ...chatgpt, resource: universal }], ...signedIn }),
  })
  expect(await screen.findByText(universal)).toBeTruthy()
  expect(screen.getByText(/Install Aether from the ChatGPT directory where it is listed, or add it as a custom connector/)).toBeTruthy()
  expect(screen.getByText(/No code is needed/)).toBeTruthy()
  expect(screen.queryByRole('button', { name: 'Get a code' })).toBeNull()
  fireEvent.click(screen.getByRole('button', { name: 'Watch for the connection' }))
  expect(await screen.findByText('Waiting for ChatGPT to connect…')).toBeTruthy()
  await vi.advanceTimersByTimeAsync(3000)
  expect(await screen.findByText('ChatGPT (chatgpt.com) connected')).toBeTruthy()
  expect(screen.getByText('Universal URL')).toBeTruthy()
  expect(client.connectorCode).not.toHaveBeenCalled()
})

it('shows the server’s own URL with how to link GitHub, and still asks for a code', async () => {
  show({ connectorList: vi.fn().mockResolvedValue({ connectors: [], url: own, universal_url: universal }) })
  expect(await screen.findByText(own)).toBeTruthy()
  expect(screen.getByText(/Profile › Sign-ins › Link a GitHub account/)).toBeTruthy()
  expect(screen.getByRole('button', { name: 'Get a code' })).toBeTruthy()
})

it('watches for a sign-in that finished before Watch for the connection', async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true, now: Date.parse('2026-10-25T12:00:00Z') })
  show({
    connectorList: vi.fn().mockResolvedValueOnce({ connectors: [], ...signedIn }).mockResolvedValue({ connectors: [{ ...chatgpt, resource: universal }], ...signedIn }),
  })
  await screen.findByText('No connectors yet')
  fireEvent.click(screen.getByRole('button', { name: 'Watch for the connection' }))
  expect(await screen.findByText('Waiting for ChatGPT to connect…')).toBeTruthy()
  await vi.advanceTimersByTimeAsync(3000)
  expect(await screen.findByText('ChatGPT (chatgpt.com) connected')).toBeTruthy()
})

it('reconnects a connector on the server’s own URL there with a code, even when the universal URL takes the sign-in', async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true, now: Date.parse('2026-10-25T12:00:00Z') })
  const old = { ...claude, resource: own }
  const client = show({
    connectorList: vi.fn().mockResolvedValue({ connectors: [old], url: universal, universal_url: universal, sign_in: true }),
    connectorCode: vi.fn(async () => ({ url: universal, code: 'ABCD-EFGH', expires_at: '2026-10-25T12:10:00Z' })),
  })
  fireEvent.click(await screen.findByRole('button', { name: 'Reconnect Claude (claude.ai)' }))
  const dialog = await screen.findByRole('alertdialog')
  expect(dialog.textContent).toContain('This gets a new code. Connect Aether again in Claude and paste the code; you can choose other scopes. Once the new connection calls Aether, this connector is revoked.')
  fireEvent.click(within(dialog).getByRole('button', { name: 'Reconnect' }))
  expect(await screen.findByText('ABCD-EFGH')).toBeTruthy()
  const panel = screen.getByRole('tabpanel', { name: 'Claude' })
  expect(within(panel).getByText(own)).toBeTruthy()
  expect(within(panel).getByText(/where the connector you are reconnecting was added/)).toBeTruthy()
  expect(within(panel).queryByText(/No code is needed/)).toBeNull()

  // The code expires: the connector is still there, and a new code is for its own URL.
  await vi.advanceTimersByTimeAsync(600_000)
  expect(await screen.findByText(/The last code expired\./)).toBeTruthy()
  expect(client.connectorRevoke).not.toHaveBeenCalled()
  expect(within(panel).getByText(own)).toBeTruthy()
  expect(within(panel).queryByText(/No code is needed/)).toBeNull()
  expect(screen.getByText(/Reconnecting Claude \(claude\.ai\): get a new code to continue\./)).toBeTruthy()
  fireEvent.click(within(panel).getByRole('button', { name: 'Get a code' }))
  await waitFor(() => expect(client.connectorCode).toHaveBeenCalledTimes(2))
  expect(await screen.findByText('Waiting for Claude to connect…')).toBeTruthy()
  expect(within(panel).getByText(own)).toBeTruthy()
})

async function move(name: string) {
  fireEvent.click(await screen.findByRole('button', { name: `Move ${name} to the universal URL` }))
  fireEvent.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Move' }))
}

it('moves a connector on the server’s own URL to the universal URL, revoking it only once its replacement calls Aether', async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true, now: Date.parse('2026-10-25T12:00:00Z') })
  const old = { ...chatgpt, resource: own }
  const added = { ...chatgpt, id: 'con-universal01', resource: universal }
  const unrelated = { ...claude, id: 'con-claudeuni01', resource: universal, last_used_at: '2026-10-25T12:00:01Z' }
  const client = show({
    connectorList: vi.fn().mockResolvedValueOnce({ connectors: [old], ...signedIn }).mockResolvedValueOnce({ connectors: [old], ...signedIn })
      .mockResolvedValueOnce({ connectors: [old, unrelated], ...signedIn }).mockResolvedValueOnce({ connectors: [old, unrelated, added], ...signedIn })
      .mockResolvedValue({ connectors: [old, unrelated, { ...added, last_used_at: '2026-10-25T12:00:09Z' }], ...signedIn }),
  })
  expect(await screen.findByText(own)).toBeTruthy()
  fireEvent.click(screen.getByRole('button', { name: 'Move ChatGPT (chatgpt.com) to the universal URL' }))
  const dialog = await screen.findByRole('alertdialog')
  expect(dialog.textContent).toContain(`Add Aether again in ChatGPT with ${universal} and sign in. Once the new connection calls Aether, this connector is revoked`)
  fireEvent.click(within(dialog).getByRole('button', { name: 'Move' }))
  expect(await screen.findByText('Waiting for ChatGPT to connect…')).toBeTruthy()
  await vi.advanceTimersByTimeAsync(6000)
  expect(await screen.findByText('Waiting for ChatGPT (chatgpt.com) to call Aether, then revoking ChatGPT (chatgpt.com)…')).toBeTruthy()
  expect(client.connectorRevoke).not.toHaveBeenCalled()
  await vi.advanceTimersByTimeAsync(3000)
  await waitFor(() => expect(client.connectorRevoke).toHaveBeenCalledWith('con-7k2m9q4xbd'))
  expect(client.connectorCode).not.toHaveBeenCalled()
})

it('leaves a connector another tab’s move took to that move', async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true, now: Date.parse('2026-10-25T12:00:00Z') })
  const old = { ...chatgpt, resource: own }
  const theirs = { ...chatgpt, id: 'con-universal01', resource: universal, last_used_at: '2026-10-25T12:00:01Z' }
  const ours = { ...chatgpt, id: 'con-universal02', resource: universal, last_used_at: '2026-10-25T12:00:05Z' }
  const client = show({
    connectorList: vi.fn().mockResolvedValueOnce({ connectors: [old], ...signedIn }).mockResolvedValueOnce({ connectors: [old], ...signedIn })
      .mockResolvedValueOnce({ connectors: [old, theirs], ...signedIn }).mockResolvedValue({ connectors: [old, theirs, ours], ...signedIn }),
  })
  await move('ChatGPT (chatgpt.com)')
  expect(await screen.findByText('Waiting for ChatGPT to connect…')).toBeTruthy()
  localStorage.setItem('aether:connector-move-claims', JSON.stringify({ 'con-universal01': 'another-tab' }))
  await vi.advanceTimersByTimeAsync(3000)
  expect(client.connectorRevoke).not.toHaveBeenCalled()
  await vi.advanceTimersByTimeAsync(3000)
  await waitFor(() => expect(client.connectorRevoke).toHaveBeenCalledWith('con-7k2m9q4xbd'))
  expect(await screen.findByText('ChatGPT (chatgpt.com) connected')).toBeTruthy()
  expect(JSON.parse(localStorage.getItem('aether:connector-move-claims') ?? '{}')).toEqual({ 'con-universal01': 'another-tab', 'con-universal02': expect.any(String) })
})

it('moves a Claude Code connector by its client, whatever loopback port it returns to', async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true, now: Date.parse('2026-10-25T12:00:00Z') })
  const code = { ...claude, client_id: 'https://claude.ai/oauth/claude-code-client-metadata', client_name: 'Claude Code' }
  const old = { ...code, redirect_host: '127.0.0.1:53111', resource: own }
  const desktop = { ...claude, id: 'con-claudeuni01', resource: universal, last_used_at: '2026-10-25T12:00:01Z' }
  const added = { ...code, id: 'con-universal01', redirect_host: '127.0.0.1:60222', resource: universal, last_used_at: '2026-10-25T12:00:05Z' }
  const client = show({
    connectorList: vi.fn().mockResolvedValueOnce({ connectors: [old], ...signedIn }).mockResolvedValueOnce({ connectors: [old], ...signedIn })
      .mockResolvedValueOnce({ connectors: [old, desktop], ...signedIn }).mockResolvedValue({ connectors: [old, desktop, added], ...signedIn }),
  })
  await move('Claude Code (127.0.0.1:53111)')
  expect(await screen.findByText('Waiting for Claude to connect…')).toBeTruthy()
  expect(screen.getByRole('tab', { name: 'Claude' }).getAttribute('aria-selected')).toBe('true')
  await vi.advanceTimersByTimeAsync(3000)
  expect(client.connectorRevoke).not.toHaveBeenCalled()
  await vi.advanceTimersByTimeAsync(3000)
  await waitFor(() => expect(client.connectorRevoke).toHaveBeenCalledWith('con-claude00001'))
})

it('takes a replacement that called Aether over an earlier one that never did', async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true, now: Date.parse('2026-10-25T12:00:00Z') })
  const old = { ...chatgpt, resource: own }
  const lost = { ...chatgpt, id: 'con-universal01', resource: universal }
  const working = { ...chatgpt, id: 'con-universal02', resource: universal, last_used_at: '2026-10-25T12:00:05Z' }
  const client = show({
    connectorList: vi.fn().mockResolvedValueOnce({ connectors: [old], ...signedIn }).mockResolvedValueOnce({ connectors: [old], ...signedIn })
      .mockResolvedValueOnce({ connectors: [old, lost], ...signedIn }).mockResolvedValue({ connectors: [old, lost, working], ...signedIn }),
  })
  await move('ChatGPT (chatgpt.com)')
  expect(await screen.findByText('Waiting for ChatGPT to connect…')).toBeTruthy()
  await vi.advanceTimersByTimeAsync(3000)
  expect(client.connectorRevoke).not.toHaveBeenCalled()
  await vi.advanceTimersByTimeAsync(3000)
  await waitFor(() => expect(client.connectorRevoke).toHaveBeenCalledWith('con-7k2m9q4xbd'))
  expect(JSON.parse(localStorage.getItem('aether:connector-move-claims') ?? '{}')).toEqual({ 'con-universal02': expect.any(String) })
})

it('says when a move or a watch saw nothing connect in 10 minutes, and keeps the old connector', async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true, now: Date.parse('2026-10-25T12:00:00Z') })
  const old = { ...chatgpt, resource: own }
  const client = show({ connectorList: vi.fn().mockResolvedValue({ connectors: [old], ...signedIn }) })
  await move('ChatGPT (chatgpt.com)')
  expect(await screen.findByText('Waiting for ChatGPT to connect…')).toBeTruthy()
  await vi.advanceTimersByTimeAsync(600_000)
  expect((await screen.findByRole('alert')).textContent).toContain(`No new ChatGPT connection appeared at ${universal}, so ChatGPT (chatgpt.com) was kept.`)
  expect(screen.getByText(/Nothing connected in the last 10 minutes\./)).toBeTruthy()
  expect(client.connectorRevoke).not.toHaveBeenCalled()
})

it('keeps the old connector and says why when the browser cannot record the move', async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true, now: Date.parse('2026-10-25T12:00:00Z') })
  const old = { ...chatgpt, resource: own }
  const added = { ...chatgpt, id: 'con-universal01', resource: universal, last_used_at: '2026-10-25T12:00:05Z' }
  const client = show({
    connectorList: vi.fn().mockResolvedValueOnce({ connectors: [old], ...signedIn }).mockResolvedValueOnce({ connectors: [old], ...signedIn })
      .mockResolvedValue({ connectors: [old, added], ...signedIn }),
  })
  await move('ChatGPT (chatgpt.com)')
  expect(await screen.findByText('Waiting for ChatGPT to connect…')).toBeTruthy()
  vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
    throw new DOMException('The quota has been exceeded.', 'QuotaExceededError')
  })
  await vi.advanceTimersByTimeAsync(3000)
  expect((await screen.findByRole('alert')).textContent).toContain('This browser cannot record which connection replaces ChatGPT (chatgpt.com) (QuotaExceededError: The quota has been exceeded.), so it was kept.')
  expect(screen.queryByText('Waiting for ChatGPT to connect…')).toBeNull()
  expect(client.connectorRevoke).not.toHaveBeenCalled()
})

it('starts a move where crypto.randomUUID is missing, as on plain HTTP', async () => {
  vi.stubGlobal('crypto', {})
  try {
    show({ connectorList: vi.fn().mockResolvedValue({ connectors: [{ ...chatgpt, resource: own }], ...signedIn }) })
    await move('ChatGPT (chatgpt.com)')
    expect(await screen.findByText('Waiting for ChatGPT to connect…')).toBeTruthy()
  } finally {
    vi.unstubAllGlobals()
  }
})

it('watches only for the app it was started for', async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true, now: Date.parse('2026-10-25T12:00:00Z') })
  const other = { ...claude, id: 'con-claudeuni01', resource: universal }
  const wanted = { ...chatgpt, id: 'con-chatgptu01', resource: universal }
  show({
    connectorList: vi.fn().mockResolvedValueOnce({ connectors: [], ...signedIn }).mockResolvedValueOnce({ connectors: [], ...signedIn })
      .mockResolvedValueOnce({ connectors: [other], ...signedIn }).mockResolvedValue({ connectors: [other, wanted], ...signedIn }),
  })
  await screen.findByText('No connectors yet')
  fireEvent.click(screen.getByRole('button', { name: 'Watch for the connection' }))
  expect(await screen.findByText('Waiting for ChatGPT to connect…')).toBeTruthy()
  await vi.advanceTimersByTimeAsync(3000)
  expect(screen.queryByText(/connected$/)).toBeNull()
  expect(screen.getByText('Waiting for ChatGPT to connect…')).toBeTruthy()
  await vi.advanceTimersByTimeAsync(3000)
  expect(await screen.findByText('ChatGPT (chatgpt.com) connected')).toBeTruthy()
})

it('holds off another move or reconnect while one is in progress, until it is stopped', async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true, now: Date.parse('2026-10-25T12:00:00Z') })
  const first = { ...chatgpt, resource: own }
  const second = { ...claude, resource: own }
  show({ connectorList: vi.fn().mockResolvedValue({ connectors: [first, second], ...signedIn }) })
  await move('ChatGPT (chatgpt.com)')
  expect(await screen.findByText('Waiting for ChatGPT to connect…')).toBeTruthy()
  expect(screen.getByText(/Moving ChatGPT \(chatgpt\.com\)\. .*another move or reconnect waits until this one ends\./)).toBeTruthy()
  expect(screen.getByRole('button', { name: 'Move Claude (claude.ai) to the universal URL' }).hasAttribute('disabled')).toBe(true)
  expect(screen.getByRole('button', { name: 'Reconnect Claude (claude.ai)' }).hasAttribute('disabled')).toBe(true)
  fireEvent.click(screen.getByRole('button', { name: 'Stop' }))
  expect(screen.queryByText('Waiting for ChatGPT to connect…')).toBeNull()
  expect(screen.getByRole('button', { name: 'Move Claude (claude.ai) to the universal URL' }).hasAttribute('disabled')).toBe(false)
})

it('does not resume a move stopped while its retry waits for the server', async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true, now: Date.parse('2026-10-25T12:00:00Z') })
  const old = { ...chatgpt, resource: own }
  const added = { ...chatgpt, id: 'con-universal01', resource: universal, last_used_at: '2026-10-25T12:11:00Z' }
  const list = vi.fn().mockResolvedValue({ connectors: [old], ...signedIn })
  const client = show({ connectorList: list })
  await move('ChatGPT (chatgpt.com)')
  expect(await screen.findByText('Waiting for ChatGPT to connect…')).toBeTruthy()
  await vi.advanceTimersByTimeAsync(600_000)
  await screen.findByRole('alert')
  let release = () => {}
  list.mockImplementationOnce(() => new Promise((resolve) => {
    release = () => resolve({ connectors: [old], ...signedIn })
  })).mockResolvedValue({ connectors: [old, added], ...signedIn })
  fireEvent.click(screen.getByRole('button', { name: 'Watch for the connection' }))
  fireEvent.click(screen.getByRole('button', { name: 'Stop' }))
  await act(async () => release())
  await vi.advanceTimersByTimeAsync(6000)
  expect(screen.queryByText('Waiting for ChatGPT to connect…')).toBeNull()
  expect(screen.queryByText(/^Moving /)).toBeNull()
  expect(client.connectorRevoke).not.toHaveBeenCalled()
})

it('retries a move for its own app and still takes a replacement that appeared before the retry', async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true, now: Date.parse('2026-10-25T12:00:00Z') })
  const old = { ...chatgpt, resource: own }
  const added = { ...chatgpt, id: 'con-universal01', resource: universal }
  let connectors = [old]
  const client = show({ connectorList: vi.fn(async () => ({ connectors, ...signedIn })) })
  await move('ChatGPT (chatgpt.com)')
  expect(await screen.findByText('Waiting for ChatGPT to connect…')).toBeTruthy()
  connectors = [old, added]
  await vi.advanceTimersByTimeAsync(600_000)
  expect((await screen.findByRole('alert')).textContent).toContain('ChatGPT (chatgpt.com) has not called Aether yet')
  fireEvent.mouseDown(screen.getByRole('tab', { name: 'Claude' }))
  expect(screen.getByRole('tab', { name: 'Claude' }).getAttribute('aria-selected')).toBe('true')
  fireEvent.click(screen.getByRole('button', { name: 'Watch for the connection' }))
  expect(await screen.findByText(/^Waiting for ChatGPT/)).toBeTruthy()
  expect(screen.getByRole('tab', { name: 'ChatGPT' }).getAttribute('aria-selected')).toBe('true')
  connectors = [old, { ...added, last_used_at: '2026-10-25T12:11:00Z' }]
  await vi.advanceTimersByTimeAsync(3000)
  await waitFor(() => expect(client.connectorRevoke).toHaveBeenCalledWith('con-7k2m9q4xbd'))
})
