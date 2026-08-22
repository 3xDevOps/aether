import { fireEvent, render, screen, within } from '@testing-library/react'
import { ApiError } from '@/lib/api'
import type { Member } from '@/lib/types'
import { MembersRoute } from '@/routes/members'
import { useStore, type RootState } from '@/store'
import { alice, bob, fakeApi, serverInfo, session } from '@/test/fixtures'

const pendingCara: Member = {
  id: 'mem_cara',
  display_name: 'Cara',
  color: '#4363d8',
  role: 'collaborator',
  pending: true,
}

function seed(extra: Partial<RootState> = {}) {
  useStore.setState({
    sessions: { [session.id]: session },
    members: { [alice.id]: alice, [bob.id]: bob },
    presence: [],
    info: serverInfo,
    // The admin verbs (approve/invite/remove) are capability-gated; an
    // upgraded gateway advertising every method renders them all.
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
})
