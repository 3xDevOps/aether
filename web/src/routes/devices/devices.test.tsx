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
    kind: 'ssh',
    label: 'laptop',
    status: 'approved',
    fingerprint: 'SHA256:examplefingerprint',
    created_at: '2026-09-20T10:00:00Z',
    last_seen_at: '2026-09-27T09:00:00Z',
    ...over,
  }
}

const pendingPhone = device({
  id: 'dev_phone',
  member_id: bob.id,
  kind: 'browser',
  label: 'Safari on iPhone',
  status: 'pending',
  approval_code: 'ABCD-EFGH',
  fingerprint: undefined,
  last_seen_at: undefined,
})

function seed(self = alice) {
  useStore.setState({
    members: { [alice.id]: alice, [bob.id]: bob },
    info: { ...serverInfo, member: self },
    capabilities: { gateway: 'edge', methods: ['*'], ws: ['events'] },
    route: { name: 'devices', params: {} },
  })
}

describe('devices view', () => {
  it('lists kind, label, status, owner and when each device was added and last seen', async () => {
    seed()
    const client = fakeApi({ memberDeviceList: vi.fn(async () => [device(), pendingPhone]) })
    render(<DevicesRoute params={{}} client={client} />)

    const list = within(await screen.findByRole('list', { name: 'Devices' }))
    const laptop = list.getByText('laptop').closest('li')!
    expect(within(laptop).getByText('Computer')).toBeDefined()
    expect(within(laptop).getByText('approved')).toBeDefined()
    expect(within(laptop).getByText('SHA256:examplefingerprint')).toBeDefined()
    expect(laptop.textContent).toMatch(/Alice · added .+ · last seen/)
    const phone = list.getByText('Safari on iPhone').closest('li')!
    expect(within(phone).getByText('Browser')).toBeDefined()
    expect(within(phone).getByText('pending')).toBeDefined()
    expect(within(phone).getByText('ABCD-EFGH')).toBeDefined()
    // An admin reads whose device it is.
    expect(phone.textContent).toMatch(/Bob · added .+ · never seen/)
  })

  it('leaves owner names off a member own list', async () => {
    seed(bob)
    const client = fakeApi({ memberDeviceList: vi.fn(async () => [pendingPhone]) })
    render(<DevicesRoute params={{}} client={client} />)

    const phone = (await screen.findByText('Safari on iPhone')).closest('li')!
    expect(phone.textContent).not.toContain('Bob ·')
    expect(screen.getByText('your devices')).toBeDefined()
  })

  it('approves by code and re-reads the list', async () => {
    seed()
    const list = vi
      .fn()
      .mockResolvedValueOnce([pendingPhone])
      .mockResolvedValue([{ ...pendingPhone, status: 'approved', approval_code: undefined }])
    const client = fakeApi({
      memberDeviceList: list,
      memberDeviceApprove: vi.fn(async () => ({ ...pendingPhone, status: 'approved' as const })),
    })
    render(<DevicesRoute params={{}} client={client} />)
    await screen.findByText('Safari on iPhone')

    fireEvent.change(screen.getByLabelText('Approval code'), { target: { value: ' ABCD-EFGH ' } })
    fireEvent.click(screen.getByRole('button', { name: 'Approve' }))

    expect(await screen.findByText('approved')).toBeDefined()
    expect(client.memberDeviceApprove).toHaveBeenCalledWith('ABCD-EFGH')
    expect(list).toHaveBeenCalledTimes(2)
  })

  it('approves a pending row with its own code', async () => {
    seed()
    const client = fakeApi({
      memberDeviceList: vi.fn(async () => [pendingPhone]),
      memberDeviceApprove: vi.fn(async () => ({ ...pendingPhone, status: 'approved' as const })),
    })
    render(<DevicesRoute params={{}} client={client} />)

    fireEvent.click(await screen.findByRole('button', { name: 'Approve Safari on iPhone' }))

    await vi.waitFor(() => expect(client.memberDeviceApprove).toHaveBeenCalledWith('ABCD-EFGH'))
  })

  it('shows the server refusal of a code verbatim', async () => {
    seed()
    const client = fakeApi({
      memberDeviceApprove: vi.fn(() =>
        Promise.reject(
          new ApiError(404, 'member.device.approve: no device is waiting for approval with code "ZZZZ-ZZZZ"'),
        ),
      ),
    })
    render(<DevicesRoute params={{}} client={client} />)

    fireEvent.change(screen.getByLabelText('Approval code'), { target: { value: 'ZZZZ-ZZZZ' } })
    fireEvent.click(screen.getByRole('button', { name: 'Approve' }))

    expect((await screen.findByRole('alert')).textContent).toBe(
      'member.device.approve: no device is waiting for approval with code "ZZZZ-ZZZZ"',
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
      memberDeviceList: vi.fn(async () => [device({ member_id: bob.id, kind: 'browser', label: 'Firefox' })]),
      memberDeviceRevoke: vi.fn(() =>
        Promise.reject(new ApiError(403, 'member.device.revoke: permission denied')),
      ),
    })
    render(<DevicesRoute params={{}} client={client} />)

    fireEvent.click(await screen.findByRole('button', { name: 'Revoke Firefox' }))
    const dialog = within(await screen.findByRole('alertdialog'))
    expect(dialog.getByText('Revoke Firefox?')).toBeDefined()
    expect(dialog.getByText(/ends its dashboard session/)).toBeDefined()
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
