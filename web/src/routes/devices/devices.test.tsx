import { fireEvent, render, screen, within } from '@testing-library/react'
import { ApiError } from '@/lib/api'
import type { Device } from '@/lib/types'
import { DevicesRoute } from '@/routes/devices'
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
  provider: 'google',
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

describe('devices view', () => {
  it('lists label, status, key, owner and when each device was added and last seen', async () => {
    seed()
    const client = fakeApi({ memberDeviceList: vi.fn(async () => [device(), pendingDesktop]) })
    render(<DevicesRoute params={{}} client={client} />)

    const list = within(await screen.findByRole('list', { name: 'Devices' }))
    const laptop = list.getByText('laptop').closest('li')!
    expect(within(laptop).getByText('approved')).toBeDefined()
    expect(within(laptop).getByText('SHA256:examplefingerprint')).toBeDefined()
    expect(laptop.textContent).toMatch(/Alice · added .+ · last seen/)
    expect(within(laptop).getByText('signed in as alice on GitHub')).toBeDefined()
    const desktop = list.getByText('desktop').closest('li')!
    expect(within(desktop).getByText('pending')).toBeDefined()
    expect(within(desktop).getByText('SHA256:otherfingerprint')).toBeDefined()
    // An admin reads whose device it is.
    expect(desktop.textContent).toMatch(/Bob · added .+ · never seen/)
    expect(within(desktop).getByText('signed in as bob@example.com on Google')).toBeDefined()
  })

  it('names the invitation a device waits on, which has no member yet', async () => {
    seed()
    const waiting = device({ id: 'dev_new', member_id: '', invitation_id: 'inv_1', label: 'new', status: 'pending' })
    const client = fakeApi({ memberDeviceList: vi.fn(async () => [waiting]) })
    render(<DevicesRoute params={{}} client={client} />)

    const row = (await screen.findByText('new')).closest('li')!
    expect(row.textContent).toMatch(/invitation inv_1 · added/)
    expect(within(row).getByText('signed in as alice on GitHub')).toBeDefined()
  })

  it('leaves owner names off a member own list', async () => {
    seed(bob)
    const client = fakeApi({ memberDeviceList: vi.fn(async () => [pendingDesktop]) })
    render(<DevicesRoute params={{}} client={client} />)

    const desktop = (await screen.findByText('desktop')).closest('li')!
    expect(desktop.textContent).not.toContain('Bob ·')
    expect(screen.getByText('your devices')).toBeDefined()
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
    render(<DevicesRoute params={{}} client={client} />)
    await screen.findByText('desktop')

    fireEvent.change(screen.getByLabelText('Approval code'), { target: { value: ' ABCD-EFGH ' } })
    fireEvent.click(screen.getByRole('button', { name: 'Review' }))
    const dialog = within(await screen.findByRole('alertdialog'))
    expect(client.memberDeviceLookup).toHaveBeenCalledWith('ABCD-EFGH')
    expect(dialog.getByText('Approve desktop as Bob (admin)?')).toBeDefined()
    expect(dialog.getByText(/signed in as bob@example.com on Google/)).toBeDefined()
    expect(dialog.getByText('SHA256:otherfingerprint')).toBeDefined()
    expect(client.memberDeviceApprove).not.toHaveBeenCalled()

    fireEvent.click(dialog.getByRole('button', { name: 'Cancel' }))
    expect(client.memberDeviceApprove).not.toHaveBeenCalled()

    fireEvent.click(screen.getByRole('button', { name: 'Review' }))
    fireEvent.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Approve' }))
    expect(await screen.findByText('approved')).toBeDefined()
    expect(client.memberDeviceApprove).toHaveBeenCalledWith('ABCD-EFGH', 'dev_desktop')
    expect(list).toHaveBeenCalledTimes(2)
  })

  it('says when a code adds a new member through an invitation', async () => {
    seed()
    const waiting = device({ id: 'dev_new', member_id: '', invitation_id: 'inv_1', account: 'dana', label: 'new', status: 'pending' })
    const client = fakeApi({
      memberDeviceLookup: vi.fn(async () => ({ device: waiting, role: 'collaborator' as const })),
    })
    render(<DevicesRoute params={{}} client={client} />)

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
    render(<DevicesRoute params={{}} client={client} />)

    const desktop = (await screen.findByText('desktop')).closest('li')!
    expect(within(desktop).queryByRole('button', { name: /Approve/ })).toBeNull()
    const phone = screen.getByText('phone').closest('li')!
    expect(within(phone).getByText('registered')).toBeDefined()
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
    render(<DevicesRoute params={{}} client={client} />)

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
    render(<DevicesRoute params={{}} client={client} />)

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
    render(<DevicesRoute params={{}} client={client} />)

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
    render(<DevicesRoute params={{}} client={client} />)

    expect(await screen.findByText('revoked')).toBeDefined()
    expect(screen.queryByRole('button', { name: 'Revoke laptop' })).toBeNull()
  })
})
