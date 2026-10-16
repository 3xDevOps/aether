import { act, fireEvent, render, screen, within } from '@testing-library/react'
import { UpdateGate } from '@/components/update-gate'
import { ApiError, upgradeRequired } from '@/lib/api'
import type { UpdateApplyResult } from '@/lib/types'
import { useStore } from '@/store'
import { fakeApi, updateStatus } from '@/test/fixtures'
import { caps, seed } from '@/test/update-banner-harness'

vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() } }))

const required = upgradeRequired(
  new ApiError(
    426,
    'workspace.list: this aether is v1.2.3 and the server runs v1.3.0; update this client first: run `aether update`, then retry',
    -32005,
    { client_version: 'v1.2.3', server_version: 'v1.3.0', min_client_version: 'v1.3.0', command: 'aether update' },
  ),
)

beforeEach(() => {
  vi.clearAllMocks()
  seed({ capabilities: caps({ local: ['link.status', 'update.check', 'update.apply', 'link.switch'] }) })
  useStore.setState({ updateRequired: required, update: updateStatus() })
})

test('names both versions, the server words and the manual command', () => {
  render(<UpdateGate client={fakeApi()} />)
  expect(screen.getByRole('heading', { name: 'Update Aether to keep using this server' })).toBeTruthy()
  const reason = within(screen.getByRole('alert')).getByText(/The server runs v1\.3\.0 and accepts clients from v1\.3\.0; this client is v1\.2\.3\./)
  expect(within(reason).getByText(/update this client first: run `aether update`, then retry/)).toBeTruthy()
  expect(screen.getByText('aether update')).toBeTruthy()
  expect(screen.getByRole('button', { name: 'Copy details' })).toBeTruthy()
  expect(screen.getByRole('button', { name: 'Reload' })).toBeTruthy()
  // Never dismissible.
  expect(screen.queryByRole('button', { name: /dismiss|close/i })).toBeNull()
})

test('Update now applies, shows progress, then says what is left to do', async () => {
  let finish: (value: UpdateApplyResult) => void = () => {}
  const client = fakeApi({
    localUpdateApply: vi.fn(() => new Promise<UpdateApplyResult>((resolve) => (finish = resolve))),
  })
  render(<UpdateGate client={client} />)
  fireEvent.click(screen.getByRole('button', { name: 'Update now' }))
  expect(await screen.findByText('Downloading and verifying v1.3.0…')).toBeTruthy()
  expect(screen.getByRole('button', { name: 'Updating…' }).hasAttribute('disabled')).toBe(true)
  await act(async () => finish({ updated: ['/home/user/.local/bin/aether'], version: 'v1.3.0', restarting: false, rebuilding: false }))
  expect(await screen.findByText('Aether v1.3.0 is installed.')).toBeTruthy()
})

test('a failed update shows the real error and keeps the button', async () => {
  const client = fakeApi({
    localUpdateApply: vi.fn(async () => {
      throw new ApiError(500, 'update.apply: aether-linux-amd64: sha256 mismatch')
    }),
  })
  render(<UpdateGate client={client} />)
  fireEvent.click(screen.getByRole('button', { name: 'Update now' }))
  expect(await screen.findByText('update.apply: aether-linux-amd64: sha256 mismatch')).toBeTruthy()
  expect(screen.getByRole('button', { name: 'Update now' }).hasAttribute('disabled')).toBe(false)
})

test('a binary this account cannot write gets sudo and no button', () => {
  useStore.setState({ update: updateStatus({ install_method: 'manual' }) })
  render(<UpdateGate client={fakeApi()} />)
  expect(screen.queryByRole('button', { name: 'Update now' })).toBeNull()
  expect(screen.getByText('sudo aether update')).toBeTruthy()
})

test('another saved server is a way out', async () => {
  const client = fakeApi({ localLinkSwitch: vi.fn(async () => ({ active: 'lab' })) })
  useStore.setState({
    linkStatus: {
      server_configured: true,
      linked: false,
      active: '',
      default: { name: '', addr: 'old.example.invalid:2222' },
      links: [{ name: 'lab', addr: 'lab.example.invalid:2222' }],
    } as never,
  })
  render(<UpdateGate client={client} />)
  fireEvent.click(screen.getByRole('button', { name: 'Switch to lab (lab.example.invalid:2222)' }))
  expect(client.localLinkSwitch).toHaveBeenCalledWith('lab')
})
