import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { toast } from 'sonner'
import { ApiError } from '@/lib/api'
import type { Connector } from '@/lib/types'
import { ConnectorsSection } from '@/routes/settings/connectors'
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

afterEach(() => vi.useRealTimers())

it('lists connectors and revokes one after confirming', async () => {
  const client = fakeApi({
    connectorList: vi.fn().mockResolvedValueOnce({ connectors: [chatgpt] }).mockResolvedValue({ connectors: [] }),
  })
  render(<ConnectorsSection client={client} />)
  expect(await screen.findByText('ChatGPT (chatgpt.com)')).toBeTruthy()
  expect(screen.getByText('read, write')).toBeTruthy()
  expect(screen.getByText(/never used/)).toBeTruthy()
  fireEvent.click(screen.getByRole('button', { name: 'Revoke ChatGPT (chatgpt.com)' }))
  fireEvent.click(await screen.findByRole('button', { name: 'Revoke' }))
  await waitFor(() => expect(client.connectorRevoke).toHaveBeenCalledWith('con-7k2m9q4xbd'))
  await waitFor(() => expect(screen.queryByText('ChatGPT (chatgpt.com)')).toBeNull())
})

it('shows the URL and code, then notices the new connector', async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true, now: Date.parse('2026-10-25T12:00:00Z') })
  const client = fakeApi({
    connectorList: vi.fn().mockResolvedValueOnce({ connectors: [] }).mockResolvedValue({ connectors: [chatgpt] }),
  })
  render(<ConnectorsSection client={client} />)
  await waitFor(() => expect(client.connectorList).toHaveBeenCalledTimes(1))
  fireEvent.click(screen.getByRole('button', { name: 'Get a code' }))
  expect(await screen.findByText('https://edge.example/mcp/srv')).toBeTruthy()
  expect(screen.getByText('ABCD-EFGH')).toBeTruthy()
  expect(screen.getByRole('link', { name: 'ChatGPT Plugins' }).getAttribute('href')).toBe('https://chatgpt.com/plugins')
  await vi.advanceTimersByTimeAsync(3000)
  await waitFor(() => expect(toast.success).toHaveBeenCalledWith('ChatGPT (chatgpt.com) connected'))
  expect(screen.queryByText('ABCD-EFGH')).toBeNull()
  expect(screen.getByText('ChatGPT (chatgpt.com)')).toBeTruthy()
})

it('shows the server error when no code can be minted', async () => {
  const msg = 'connector.code: this server has no edge (edge-url is empty)'
  const client = fakeApi({ connectorCode: vi.fn().mockRejectedValue(new ApiError(500, msg)) })
  render(<ConnectorsSection client={client} />)
  fireEvent.click(screen.getByRole('button', { name: 'Get a code' }))
  expect((await screen.findByRole('alert')).textContent).toContain('this server has no edge')
})
