import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { ApiError } from '@/lib/api'
import type { Invitation } from '@/lib/types'
import { MembersRoute } from '@/routes/members'
import { useStore } from '@/store'
import { alice, bob, fakeApi, serverInfo, workspace } from '@/test/fixtures'
import { pickOption } from '@/test/select'

function invitation(over: Partial<Invitation> = {}): Invitation {
  return {
    id: 'inv_octocat',
    provider: 'github',
    login: 'octocat',
    role: 'collaborator',
    created_by: alice.id,
    created_at: '2026-09-27T10:00:00Z',
    expires_at: '2026-10-04T10:00:00Z',
    ...over,
  }
}

function seed(self = alice) {
  useStore.setState({
    workspaces: { [workspace.id]: workspace },
    members: { [alice.id]: alice, [bob.id]: bob },
    presence: [],
    info: { ...serverInfo, member: self },
    capabilities: { gateway: 'edge', methods: ['*'], ws: ['events'] },
    route: { name: 'members', params: {} },
  })
}

async function section() {
  return within(await screen.findByRole('region', { name: 'Invitations' }))
}

describe('invitations', () => {
  it('is an admin view', async () => {
    seed(bob)
    const client = fakeApi()
    render(<MembersRoute params={{}} client={client} />)

    await screen.findByRole('region', { name: 'Roster' })
    expect(screen.queryByRole('region', { name: 'Invitations' })).toBeNull()
    expect(client.memberInvitationList).not.toHaveBeenCalled()
  })

  it('lists who each open invitation admits, with its role, author and expiry', async () => {
    seed()
    const client = fakeApi({
      memberInvitationList: vi.fn(async () => [
        invitation(),
        invitation({
          id: 'inv_dev',
          provider: 'google',
          login: undefined,
          email: 'dev@example.com',
          role: 'viewer',
        }),
        invitation({ id: 'inv_link', role: undefined, login: 'bob-gh', member_id: bob.id }),
      ]),
    })
    render(<MembersRoute params={{}} client={client} />)

    const list = within((await section()).getByRole('list', { name: 'Open invitations' }))
    const octocat = list.getByText('octocat on GitHub').closest('li')!
    expect(within(octocat).getByText('collaborator')).toBeDefined()
    expect(octocat.textContent).toContain('invited by Alice · expires')
    expect(list.getByText('dev@example.com on Google')).toBeDefined()
    expect(list.getByText('viewer')).toBeDefined()
    expect(list.getByText('links Bob')).toBeDefined()
  })

  it('invites by email with the chosen role and re-reads the list', async () => {
    seed()
    const created = invitation({ id: 'inv_new', login: undefined, provider: '', email: 'new@example.com', role: 'viewer' })
    const list = vi.fn().mockResolvedValueOnce([]).mockResolvedValue([created])
    const client = fakeApi({
      memberInvitationList: list,
      memberInvitationCreate: vi.fn(async () => created),
    })
    render(<MembersRoute params={{}} client={client} />)
    const invitations = await section()
    expect(await invitations.findByText('No open invitations.')).toBeDefined()

    await pickOption(invitations.getByLabelText('Invite by'), 'Email')
    // The closing list hands focus back to its trigger; the next pick waits.
    await waitFor(() => expect(screen.queryByRole('listbox')).toBeNull())
    fireEvent.change(invitations.getByLabelText('Email'), { target: { value: 'new@example.com' } })
    await pickOption(invitations.getByLabelText('Role'), 'viewer')
    fireEvent.click(invitations.getByRole('button', { name: 'Invite account' }))

    expect(await invitations.findByText('new@example.com')).toBeDefined()
    expect(client.memberInvitationCreate).toHaveBeenCalledWith({
      email: 'new@example.com',
      role: 'viewer',
    })
  })

  it('shows the server refusal of an invitation verbatim', async () => {
    seed()
    const client = fakeApi({
      memberInvitationCreate: vi.fn(() =>
        Promise.reject(new ApiError(400, 'member.invitation.create: login "-x" is not a GitHub login')),
      ),
    })
    render(<MembersRoute params={{}} client={client} />)
    const invitations = await section()

    fireEvent.change(invitations.getByLabelText('GitHub login'), { target: { value: '-x' } })
    fireEvent.click(invitations.getByRole('button', { name: 'Invite account' }))

    expect((await invitations.findByRole('alert')).textContent).toBe(
      'member.invitation.create: login "-x" is not a GitHub login',
    )
    expect(client.memberInvitationCreate).toHaveBeenCalledWith({ login: '-x', role: 'collaborator' })
  })

  it('confirms a revoke, saying what stops working, before it revokes', async () => {
    seed()
    const client = fakeApi({
      memberInvitationList: vi.fn(async () => [invitation()]),
      memberInvitationRevoke: vi.fn(async () => ({})),
    })
    render(<MembersRoute params={{}} client={client} />)
    const invitations = await section()

    fireEvent.click(
      await invitations.findByRole('button', { name: 'Revoke invitation for octocat on GitHub' }),
    )
    const dialog = within(await screen.findByRole('alertdialog'))
    expect(
      dialog.getByText('octocat on GitHub can no longer join this server by signing in to the edge.'),
    ).toBeDefined()
    expect(client.memberInvitationRevoke).not.toHaveBeenCalled()

    fireEvent.click(dialog.getByRole('button', { name: 'Revoke' }))
    await vi.waitFor(() => expect(client.memberInvitationRevoke).toHaveBeenCalledWith('inv_octocat'))
  })
})
