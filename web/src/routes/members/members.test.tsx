import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ApiError } from '@/lib/api'
import type { Member } from '@/lib/types'
import { CenterView } from '@/components/shell/center-view'
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
    capabilities: { gateway: 'remote', methods: ['*'], ws: ['events', 'attach'] },
    hydrated: true,
    hydrationError: null,
    route: { name: 'members', params: {} },
    ...extra,
  })
}

async function pickMenu(trigger: string, item: string, role: 'menuitem' | 'menuitemradio' = 'menuitem') {
  screen.getByRole('button', { name: trigger }).focus()
  await userEvent.keyboard('{Enter}')
  const menu = await screen.findByRole('menu')
  fireEvent.click(within(menu).getByRole(role, { name: item }))
}

const row = (name: string) => within(screen.getByRole('region', { name: 'Roster' })).getByText(name).closest('tr')!

describe('members page', () => {
  it('lists each member with role, account sharing and when they were last seen', async () => {
    const client = fakeApi({
      accountList: vi.fn(async () => ({ accounts: [alice, bob], shared_with: [] })),
      memberList: vi.fn(async () => [alice, bob, vera]),
    })
    seed({
      members: { [alice.id]: alice, [bob.id]: bob, [vera.id]: vera },
      presence: [
        { member_id: bob.id, state: 'watching', watching: [], last_seen: '2026-08-14T10:04:00Z' },
        { member_id: vera.id, state: 'offline', last_seen: '2026-08-14T10:04:00Z' },
      ],
    })
    render(<MembersRoute params={{}} client={client} />)

    await waitFor(() => expect(within(row('Bob')).getByText('Shares with you')).toBeDefined())
    expect(within(row('Bob')).getByText('Online')).toBeDefined()
    expect(within(row('Bob')).getByText('Collaborator')).toBeDefined()
    expect(within(row('Vera')).getByText(/ago/)).toBeDefined()
    expect(within(row('Vera')).getByText('Not shared')).toBeDefined()
    expect(within(row('Alice')).getByText('(you)')).toBeDefined()
    expect(within(row('Alice')).getByText('Offline')).toBeDefined()
  })

  it('approves a pending member through the gateway and refetches', async () => {
    const memberList = vi
      .fn()
      .mockResolvedValueOnce([alice, bob, pendingCara])
      .mockResolvedValue([alice, bob, { ...pendingCara, pending: false }])
    const client = fakeApi({ memberList, memberApprove: vi.fn(async () => ({ ...pendingCara, pending: false })) })
    seed()
    render(<MembersRoute params={{}} client={client} />)

    fireEvent.click(await screen.findByRole('button', { name: 'Approve' }))

    expect(await within(screen.getByRole('region', { name: 'Roster' })).findByText('Cara')).toBeDefined()
    expect(client.memberApprove).toHaveBeenCalledWith(pendingCara.id)
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

    expect(await screen.findByText('member.approve requires the admin role')).toBeDefined()
    expect(screen.getByRole('button', { name: 'Approve' })).toBeDefined()
  })

  it('shows the one-time invite code from the server', async () => {
    const client = fakeApi({ memberInvite: vi.fn(async () => ({ code: 'join-me-once', expires_at: '2026-08-23T10:00:00Z' })) })
    seed()
    render(<MembersRoute params={{}} client={client} />)

    fireEvent.click(screen.getByRole('button', { name: 'Invite…' }))
    const dialog = within(await screen.findByRole('dialog', { name: 'Invite a member' }))
    const codeTab = dialog.getByRole('tab', { name: 'Invite code' })
    codeTab.focus()
    await userEvent.keyboard('{Enter}')
    fireEvent.click(await dialog.findByRole('button', { name: 'Generate code' }))

    expect(await dialog.findByText('join-me-once')).toBeDefined()
    expect(client.memberInvite).toHaveBeenCalled()
  })

  it('returns focus to Invite… when the dialog closes', async () => {
    seed()
    render(<MembersRoute params={{}} client={fakeApi()} />)

    const invite = screen.getByRole('button', { name: 'Invite…' })
    invite.focus()
    await userEvent.keyboard('{Enter}')
    await screen.findByRole('dialog', { name: 'Invite a member' })
    await userEvent.keyboard('{Escape}')

    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    await waitFor(() => expect(document.activeElement).toBe(invite))
  })

  it('lets an admin change another member role from the row menu', async () => {
    const bobAdmin: Member = { ...bob, role: 'admin' }
    const memberList = vi.fn().mockResolvedValueOnce([alice, bob]).mockResolvedValue([alice, bobAdmin])
    const client = fakeApi({ memberList, memberRole: vi.fn(async () => bobAdmin) })
    seed()
    render(<MembersRoute params={{}} client={client} />)

    await pickMenu('Actions for Bob', 'Admin', 'menuitemradio')

    await waitFor(() => expect(client.memberRole).toHaveBeenCalledWith(bob.id, 'admin'))
    await waitFor(() => expect(within(row('Bob')).getByText('Admin')).toBeDefined())
  })

  it('renders the server refusal verbatim when a role change is denied', async () => {
    const client = fakeApi({
      memberRole: vi.fn(async () => {
        throw new ApiError(409, 'refusing to demote the last admin')
      }),
    })
    seed()
    render(<MembersRoute params={{}} client={client} />)

    await pickMenu('Actions for Bob', 'Viewer', 'menuitemradio')

    expect(await screen.findByText('refusing to demote the last admin')).toBeDefined()
  })

  it('confirms a removal and keeps a refusal in the dialog', async () => {
    const client = fakeApi({
      memberRemove: vi.fn(async () => {
        throw new ApiError(409, 'member has running runs')
      }),
    })
    seed()
    render(<MembersRoute params={{}} client={client} />)

    await pickMenu('Actions for Bob', 'Remove…')
    const dialog = within(await screen.findByRole('alertdialog', { name: 'Remove Bob?' }))
    fireEvent.click(dialog.getByRole('button', { name: 'Remove' }))

    expect(await dialog.findByText('member has running runs')).toBeDefined()
    expect(client.memberRemove).toHaveBeenCalledWith(bob.id)
  })

  it('returns focus to the row menu trigger when a confirm opened from it closes', async () => {
    seed()
    render(<MembersRoute params={{}} client={fakeApi()} />)

    await pickMenu('Actions for Bob', 'Remove…')
    await screen.findByRole('alertdialog', { name: 'Remove Bob?' })
    await userEvent.keyboard('{Escape}')

    await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull())
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Actions for Bob' })))
  })

  it('confirms before an admin gives up their own admin role, and holds the confirm until the server answers', async () => {
    let refuse: (reason: Error) => void = () => {}
    const client = fakeApi({ memberRole: vi.fn(() => new Promise<never>((_, reject) => { refuse = reject })) })
    seed()
    render(<MembersRoute params={{}} client={client} />)

    await pickMenu('Actions for Alice', 'Collaborator', 'menuitemradio')
    expect(client.memberRole).not.toHaveBeenCalled()
    fireEvent.click(await screen.findByRole('button', { name: 'Become collaborator' }))
    expect(client.memberRole).toHaveBeenCalledWith(alice.id, 'collaborator')

    fireEvent.keyDown(screen.getByRole('alertdialog'), { key: 'Escape' })
    expect(screen.getByRole('alertdialog')).toBeDefined()
    refuse(new Error('member.role: last admin'))
    expect(await screen.findByText(/last admin/)).toBeDefined()
  })

  it('gives a non-admin the roster as text, with no admin verbs', async () => {
    const client = fakeApi({ memberList: vi.fn(async () => [alice, bob, vera, pendingCara]) })
    // The local gateway forwards every method, so only the caller's role keeps the admin verbs off.
    seed({ info: { ...serverInfo, member: bob } })
    render(<MembersRoute params={{}} client={client} />)

    expect(await within(screen.getByRole('region', { name: 'Roster' })).findByText('Vera')).toBeDefined()
    expect(within(row('Vera')).getByText('Viewer')).toBeDefined()
    expect(screen.queryByRole('button', { name: /^Actions for/ })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Invite…' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Approve' })).toBeNull()
    expect(screen.getByText('Waiting for an admin')).toBeDefined()
    expect(screen.queryByRole('region', { name: 'Invitations' })).toBeNull()
  })

  it('switches to Devices as its own route, so the address keeps the tab', async () => {
    seed()
    render(<MembersRoute params={{}} client={fakeApi()} />)

    const tab = screen.getByRole('tab', { name: 'Devices' })
    tab.focus()
    await userEvent.keyboard('{Enter}')
    expect(useStore.getState().route).toEqual({ name: 'devices', params: {} })
  })

  it('opens the Devices route on its tab', async () => {
    seed({ route: { name: 'devices', params: {} } })
    render(<MembersRoute params={{}} client={fakeApi()} />)

    expect(screen.getByRole('tab', { name: 'Devices', selected: true })).toBeDefined()
    expect(await screen.findByRole('form', { name: 'Approve a device' })).toBeDefined()
  })

  it('keeps focus on the tab it switched to, since both tabs are one mounted page', async () => {
    seed()
    render(<main id="main"><CenterView /></main>)

    const devices = await screen.findByRole('tab', { name: 'Devices' })
    devices.focus()
    await userEvent.keyboard('{Enter}')

    await waitFor(() => expect(useStore.getState().route.name).toBe('devices'))
    expect(document.activeElement).toBe(screen.getByRole('tab', { name: 'Devices', selected: true }))
  })
})
