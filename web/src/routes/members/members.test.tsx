import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { ApiError } from '@/lib/api'
import type { Member } from '@/lib/types'
import { MembersRoute } from '@/routes/members'
import { useStore, type RootState } from '@/store'
import { alice, bob, fakeApi, serverInfo, vera, workspace } from '@/test/fixtures'

const pendingCara: Member = {
  id: 'mem_cara',
  display_name: 'Cara',
  color: '#4363d8',
  role: 'collaborator',
  pending: true,
}

function seed(extra: Partial<RootState> = {}) {
  useStore.setState({
    workspaces: { [workspace.id]: workspace },
    activeWorkspace: workspace.id,
    members: { [alice.id]: alice, [bob.id]: bob },
    presence: [],
    info: serverInfo,
    // The admin verbs need both halves: a gateway advertising every method,
    // and an admin caller. serverInfo.member is alice, who is an admin, so
    // the default identity here sees them all.
    capabilities: { gateway: 'remote', methods: ['*'], ws: ['events', 'attach'] },
    hydrated: true,
    hydrationError: null,
    route: { name: 'members', params: {} },
    ...extra,
  })
}

describe('members view', () => {
  it('renders the roster with presence and role', async () => {
    const client = fakeApi({
      presenceRoster: vi.fn(async () => []),
    })
    seed({
      presence: [
        {
          member_id: bob.id,
          state: 'watching',
          watching: [],
          last_seen: '2026-08-14T10:04:00Z',
        },
      ],
    })
    render(<MembersRoute params={{}} client={client} />)

    const roster = within(await screen.findByRole('region', { name: 'Roster' }))
    expect(roster.getByText('Alice')).toBeDefined()
    expect(roster.getByText('Bob')).toBeDefined()
    // Presence comes from the store roster, one cell per member.
    const bobRow = roster.getByText('Bob').closest('tr')!
    expect(within(bobRow).getByText('online')).toBeDefined()
    const aliceRow = roster.getByText('Alice').closest('tr')!
    expect(within(aliceRow).getByText('offline')).toBeDefined()
  })

  it('shows the last-seen time for offline members', async () => {
    const client = fakeApi({
      presenceRoster: vi.fn(async () => []),
    })
    seed({
      presence: [
        {
          member_id: alice.id,
          state: 'offline',
          last_seen: '2026-08-14T10:04:00Z',
        },
      ],
    })
    render(<MembersRoute params={{}} client={client} />)

    const roster = within(await screen.findByRole('region', { name: 'Roster' }))
    const aliceRow = roster.getByText('Alice').closest('tr')!
    expect(within(aliceRow).getByText(/last seen/)).toBeDefined()
  })

  it('approves a pending member through the gateway and refetches', async () => {
    const memberList = vi
      .fn()
      .mockResolvedValueOnce([alice, bob, pendingCara])
      .mockResolvedValue([alice, bob, { ...pendingCara, pending: false }])
    const client = fakeApi({
      memberList,
      memberApprove: vi.fn(async () => ({ ...pendingCara, pending: false })),
    })
    seed()
    render(<MembersRoute params={{}} client={client} />)

    fireEvent.click(await screen.findByRole('button', { name: 'Approve' }))

    // The approved member moves from the pending section into the roster.
    const roster = within(await screen.findByRole('region', { name: 'Roster' }))
    expect(await roster.findByText('Cara')).toBeDefined()
    expect(client.memberApprove).toHaveBeenCalledWith(pendingCara.id)
    expect(memberList.mock.calls.length).toBeGreaterThanOrEqual(2)
  })

  it('renders the server refusal verbatim when approve is denied', async () => {
    const client = fakeApi({
      memberList: vi.fn(async () => [alice, bob, pendingCara]),
      memberApprove: vi.fn(async () => {
        throw new ApiError(403, 'member.approve requires the admin role')
      }),
    })
    seed()
    render(<MembersRoute params={{}} client={client} />)

    fireEvent.click(await screen.findByRole('button', { name: 'Approve' }))

    expect(
      await screen.findByText('member.approve requires the admin role'),
    ).toBeDefined()
    // The member is still pending, so the button stays.
    expect(screen.getByRole('button', { name: 'Approve' })).toBeDefined()
  })

  it('shows the one-time invite code from the server', async () => {
    const client = fakeApi({
      memberInvite: vi.fn(async () => ({
        code: 'join-me-once',
        expires_at: '2026-08-23T10:00:00Z',
      })),
    })
    seed()
    render(<MembersRoute params={{}} client={client} />)

    fireEvent.click(screen.getByRole('button', { name: 'Invite' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Generate code' }))

    const code = await screen.findByLabelText<HTMLInputElement>('Invite code')
    expect(code.value).toBe('join-me-once')
    expect(client.memberInvite).toHaveBeenCalled()
  })

  it('lets an admin change another member role and refetches the roster', async () => {
    const bobAdmin: Member = { ...bob, role: 'admin' }
    const memberList = vi
      .fn()
      .mockResolvedValueOnce([alice, bob])
      .mockResolvedValue([alice, bobAdmin])
    const client = fakeApi({
      memberList,
      memberRole: vi.fn(async () => bobAdmin),
    })
    seed()
    render(<MembersRoute params={{}} client={client} />)

    const roster = within(await screen.findByRole('region', { name: 'Roster' }))
    const select = await roster.findByRole<HTMLSelectElement>('combobox', {
      name: 'Role for Bob',
    })
    expect(select.value).toBe('collaborator')

    fireEvent.change(select, { target: { value: 'admin' } })

    await waitFor(() =>
      expect(client.memberRole).toHaveBeenCalledWith(bob.id, 'admin'),
    )
    await waitFor(() => expect(memberList.mock.calls.length).toBeGreaterThanOrEqual(2))
  })

  it('gives a non-admin the roster as text, with no admin verbs', async () => {
    const client = fakeApi({
      memberList: vi.fn(async () => [alice, bob, vera, pendingCara]),
    })
    // The local gateway forwards every method, so only the caller's role
    // keeps the admin verbs off this view.
    seed({ info: { ...serverInfo, member: bob } })
    render(<MembersRoute params={{}} client={client} />)

    const roster = within(await screen.findByRole('region', { name: 'Roster' }))
    expect(await roster.findByText('Vera')).toBeDefined()
    const veraRow = roster.getByText('Vera').closest('tr')!
    expect(within(veraRow).getByText('viewer')).toBeDefined()
    expect(roster.queryByRole('combobox')).toBeNull()
    expect(screen.queryByRole('button', { name: 'Invite' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Approve' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Remove' })).toBeNull()
    // Colour is self-service, so it survives the role gate.
    expect(screen.getByLabelText('Set color #e6194b')).toBeDefined()
    // The header says why the view is inert, rather than leaving the
    // absent buttons to imply it.
    expect(screen.getByText('3 members - read only')).toBeDefined()
  })

  it('renders the server refusal verbatim when a role change is denied', async () => {
    const client = fakeApi({
      memberRole: vi.fn(async () => {
        throw new ApiError(409, 'refusing to demote the last admin')
      }),
    })
    seed()
    render(<MembersRoute params={{}} client={client} />)

    const roster = within(await screen.findByRole('region', { name: 'Roster' }))
    fireEvent.change(
      await roster.findByRole('combobox', { name: 'Role for Bob' }),
      { target: { value: 'viewer' } },
    )

    expect(await screen.findByText('refusing to demote the last admin')).toBeDefined()
  })

  it('confirms before an admin gives up their own admin role', async () => {
    const aliceDemoted: Member = { ...alice, role: 'collaborator' }
    const client = fakeApi({
      memberRole: vi.fn(async () => aliceDemoted),
    })
    seed()
    render(<MembersRoute params={{}} client={client} />)

    const roster = within(await screen.findByRole('region', { name: 'Roster' }))
    fireEvent.change(
      await roster.findByRole('combobox', { name: 'Role for Alice' }),
      { target: { value: 'collaborator' } },
    )

    // Nothing happens until the self-lockout is confirmed.
    expect(client.memberRole).not.toHaveBeenCalled()
    fireEvent.click(
      await screen.findByRole('button', { name: 'Become collaborator' }),
    )

    await waitFor(() =>
      expect(client.memberRole).toHaveBeenCalledWith(alice.id, 'collaborator'),
    )
  })
})
