import { fireEvent, render, screen, within } from '@testing-library/react'
import { ApiError } from '@/lib/api'
import type { Device } from '@/lib/types'
import { DevicesPanel } from '@/routes/devices'
import { useStore } from '@/store'
import { alice, bob, fakeApi, serverInfo } from '@/test/fixtures'

function device(over: Partial<Device> = {}): Device {
  return {
    id: 'dev_laptop',
    member_id: alice.id,
    provider: 'github',
    account: 'alice',
    label: 'laptop',
    status: 'approved',
    fingerprint: 'SHA256:examplefingerprint',
    created_at: '2026-09-20T10:00:00Z',
    last_seen_at: '2026-09-27T09:00:00Z',
    ...over,
  }
}

const pendingDesktop = device({
  id: 'dev_desktop',
  member_id: bob.id,
  account: 'bob@example.com',
  label: 'desktop',
  status: 'pending',
  fingerprint: 'SHA256:otherfingerprint',
  last_seen_at: undefined,
})

function seed(self = alice) {
  useStore.setState({
    members: { [alice.id]: alice, [bob.id]: bob },
    info: { ...serverInfo, member: self },
    capabilities: { gateway: 'local', methods: ['*'], ws: ['events'] },
    route: { name: 'devices', params: {} },
  })
}

describe('devices tab', () => {
  it('lists label, status, key, owner and when each device was added and last seen', async () => {
    seed()
    const client = fakeApi({ memberDeviceList: vi.fn(async () => [device(), pendingDesktop]) })
    render(<DevicesPanel client={client} />)

    const list = within(await screen.findByRole('list', { name: 'Devices' }))
    const laptop = list.getByText('laptop').closest('li')!
    expect(within(laptop).getByText('Approved')).toBeDefined()
    expect(laptop.querySelector('[title="SHA256:examplefingerprint"]')).not.toBeNull()
    expect(laptop.textContent).toMatch(/Alice · alice on GitHub · added .+ · last seen/)
    const desktop = list.getByText('desktop').closest('li')!
    expect(within(desktop).getByText('Pending')).toBeDefined()
    // An admin reads whose device it is.
    expect(desktop.textContent).toMatch(/Bob · bob@example.com on GitHub · added .+ · never seen/)
  })

  it('names the provider of a device v0.5.2-alpha.3 signed in with Google', async () => {
    seed()
    const old = device({ id: 'dev_old', provider: 'google', account: 'alice@example.com', label: 'old' })
    const client = fakeApi({ memberDeviceList: vi.fn(async () => [old]) })
    render(<DevicesPanel client={client} />)

    const row = (await screen.findByText('old')).closest('li')!
    expect(row.textContent).toContain('alice@example.com on google')
  })

  it('names the invitation a device waits on, which has no member yet', async () => {
    seed()
    const waiting = device({ id: 'dev_new', member_id: '', invitation_id: 'inv_1', label: 'new', status: 'pending' })
    const client = fakeApi({ memberDeviceList: vi.fn(async () => [waiting]) })
    render(<DevicesPanel client={client} />)

    const row = (await screen.findByText('new')).closest('li')!
    expect(row.textContent).toMatch(/invitation inv_1 · alice on GitHub · added/)
  })

  it('leaves owner names off a member own list', async () => {
    seed(bob)
    const client = fakeApi({ memberDeviceList: vi.fn(async () => [pendingDesktop]) })
    render(<DevicesPanel client={client} />)

    const desktop = (await screen.findByText('desktop')).closest('li')!
    expect(desktop.textContent).not.toContain('Bob ·')
  })

  it('shows whom a code admits, and approves that device once confirmed', async () => {
    seed()
    const list = vi
      .fn()
      .mockResolvedValueOnce([pendingDesktop])
      .mockResolvedValue([{ ...pendingDesktop, status: 'approved' }])
    const client = fakeApi({
      memberDeviceList: list,
      memberDeviceLookup: vi.fn(async () => ({
        device: pendingDesktop,
        member_id: bob.id,
        display_name: 'Bob',
        role: 'admin' as const,
      })),
      memberDeviceApprove: vi.fn(async () => ({ ...pendingDesktop, status: 'approved' as const })),
    })
    render(<DevicesPanel client={client} />)
    await screen.findByText('desktop')

    fireEvent.change(screen.getByLabelText('Approval code'), { target: { value: ' ABCD-EFGH ' } })
    fireEvent.click(screen.getByRole('button', { name: 'Review' }))
    const dialog = within(await screen.findByRole('alertdialog'))
    expect(client.memberDeviceLookup).toHaveBeenCalledWith('ABCD-EFGH')
    expect(dialog.getByText('Approve desktop as Bob (admin)?')).toBeDefined()
    expect(dialog.getByText(/signed in as bob@example.com on GitHub/)).toBeDefined()
    expect(dialog.getByText('SHA256:otherfingerprint')).toBeDefined()
    expect(client.memberDeviceApprove).not.toHaveBeenCalled()

    fireEvent.click(dialog.getByRole('button', { name: 'Cancel' }))
    expect(client.memberDeviceApprove).not.toHaveBeenCalled()

    fireEvent.click(screen.getByRole('button', { name: 'Review' }))
    fireEvent.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Approve' }))
    expect(await screen.findByText('Approved')).toBeDefined()
    expect(client.memberDeviceApprove).toHaveBeenCalledWith('ABCD-EFGH', 'dev_desktop')
    expect(list).toHaveBeenCalledTimes(2)
  })

  it('says when a code adds a new member through an invitation', async () => {
    seed()
    const waiting = device({ id: 'dev_new', member_id: '', invitation_id: 'inv_1', account: 'dana', label: 'new', status: 'pending' })
    const client = fakeApi({
      memberDeviceLookup: vi.fn(async () => ({ device: waiting, role: 'collaborator' as const })),
    })
    render(<DevicesPanel client={client} />)

    fireEvent.change(screen.getByLabelText('Approval code'), { target: { value: 'ABCD-EFGH' } })
    fireEvent.click(screen.getByRole('button', { name: 'Review' }))
    const dialog = within(await screen.findByRole('alertdialog'))
    expect(dialog.getByText('Approve new as a new member (collaborator)?')).toBeDefined()
    expect(dialog.getByText(/approving accepts invitation inv_1/)).toBeDefined()
  })

  it('approves only with a code typed from the new device, never from a row', async () => {
    seed()
    const registered = device({ id: 'dev_phone', label: 'phone', status: 'registered' })
    const client = fakeApi({ memberDeviceList: vi.fn(async () => [pendingDesktop, registered]) })
    render(<DevicesPanel client={client} />)

    const desktop = (await screen.findByText('desktop')).closest('li')!
    expect(within(desktop).queryByRole('button', { name: /Approve/ })).toBeNull()
    const phone = screen.getByText('phone').closest('li')!
    expect(within(phone).getByText('Registered')).toBeDefined()
    expect(within(phone).queryByRole('button', { name: /Approve/ })).toBeNull()
    expect(within(phone).getByRole('button', { name: 'Revoke phone' })).toBeDefined()
  })

  it('shows the server refusal of a code verbatim', async () => {
    seed()
    const client = fakeApi({
      memberDeviceLookup: vi.fn(() =>
        Promise.reject(
          new ApiError(404, 'member.device.lookup: no device is waiting for approval with code "ZZZZ-ZZZZ"'),
        ),
      ),
    })
    render(<DevicesPanel client={client} />)

    fireEvent.change(screen.getByLabelText('Approval code'), { target: { value: 'ZZZZ-ZZZZ' } })
    fireEvent.click(screen.getByRole('button', { name: 'Review' }))

    expect((await screen.findByRole('alert')).textContent).toBe(
      'member.device.lookup: no device is waiting for approval with code "ZZZZ-ZZZZ"',
    )
  })

  it('confirms a revoke, saying what stops working, before it revokes', async () => {
    seed()
    const client = fakeApi({
      memberDeviceList: vi.fn(async () => [device()]),
      memberDeviceRevoke: vi.fn(async () => device({ status: 'revoked' })),
    })
    render(<DevicesPanel client={client} />)

    fireEvent.click(await screen.findByRole('button', { name: 'Revoke laptop' }))
    const dialog = within(await screen.findByRole('alertdialog'))
    expect(dialog.getByText('Revoke laptop of Alice?')).toBeDefined()
    expect(dialog.getByText(/refuses its device key on every path/)).toBeDefined()
    expect(client.memberDeviceRevoke).not.toHaveBeenCalled()

    fireEvent.click(dialog.getByRole('button', { name: 'Cancel' }))
    expect(client.memberDeviceRevoke).not.toHaveBeenCalled()

    fireEvent.click(screen.getByRole('button', { name: 'Revoke laptop' }))
    fireEvent.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Revoke' }))
    await vi.waitFor(() => expect(client.memberDeviceRevoke).toHaveBeenCalledWith('dev_laptop'))
  })

  it('keeps a refused revoke in the dialog', async () => {
    seed(bob)
    const client = fakeApi({
      memberDeviceList: vi.fn(async () => [device({ member_id: bob.id, label: 'workstation' })]),
      memberDeviceRevoke: vi.fn(() =>
        Promise.reject(new ApiError(403, 'member.device.revoke: permission denied')),
      ),
    })
    render(<DevicesPanel client={client} />)

    fireEvent.click(await screen.findByRole('button', { name: 'Revoke workstation' }))
    const dialog = within(await screen.findByRole('alertdialog'))
    expect(dialog.getByText('Revoke workstation?')).toBeDefined()
    fireEvent.click(dialog.getByRole('button', { name: 'Revoke' }))

    expect((await dialog.findByRole('alert')).textContent).toBe(
      'member.device.revoke: permission denied',
    )
  })

  it('offers nothing on a revoked device', async () => {
    seed()
    const client = fakeApi({
      memberDeviceList: vi.fn(async () => [device({ status: 'revoked' })]),
    })
    render(<DevicesPanel client={client} />)

    expect(await screen.findByText('Revoked')).toBeDefined()
    expect(screen.queryByRole('button', { name: 'Revoke laptop' })).toBeNull()
  })
})
