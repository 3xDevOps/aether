import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ApiError } from '@/lib/api'
import type { Member, MemberList } from '@/lib/types'
import { CenterView } from '@/components/shell/center-view'
import { MembersRoute } from '@/routes/members'
import { useStore, type RootState } from '@/store'
import { optimisticDeadlineMs } from '@/store/optimistic'
import { alice, bob, fakeApi, serverInfo, vera, workspace } from '@/test/fixtures'
import { openSelect, pickOption } from '@/test/select'

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

const pickRole = (member: string, role: string) => pickOption(screen.getByRole('combobox', { name: `Role of ${member}` }), new RegExp(`^${role}`))

const row = (name: string) => within(screen.getByRole('region', { name: 'Roster' })).getByText(name).closest('tr')!

describe('members page', () => {
  it('lists each member with role, account sharing and when they were last seen', async () => {
    const client = fakeApi({
      accountList: vi.fn(async () => ({ accounts: [alice, bob], shared_with: [] })),
      memberList: vi.fn(async () => ({ members: [alice, bob, vera] })),
    })
    seed({
      members: { [alice.id]: alice, [bob.id]: bob, [vera.id]: vera },
      presence: [
        { member_id: bob.id, state: 'watching', watching: [], last_seen: '2026-08-14T10:04:00Z' },
        { member_id: vera.id, state: 'offline', last_seen: '2026-08-14T10:04:00Z' },
      ],
    })
    render(<MembersRoute params={{}} client={client} />)

    await waitFor(() => expect(within(row('Bob')).getByText('Shares theirs with you')).toBeDefined())
    expect(within(row('Bob')).getAllByText('Online')).toHaveLength(2)
    expect(within(row('Bob')).getByRole('combobox', { name: 'Role of Bob' }).textContent).toBe('Collaborator')
    expect(within(row('Bob')).getByRole('button', { name: 'Share your agent account with Bob' }).textContent).toBe('Share my account')
    expect(within(row('Vera')).getAllByText(/ago/)).toHaveLength(2)
    expect(within(row('Vera')).queryByText('Shares theirs with you')).toBeNull()
    expect(within(row('Alice')).getByText('(you)')).toBeDefined()
    expect(within(row('Alice')).queryByRole('button', { name: /agent account/ })).toBeNull()
    expect(within(row('Alice')).queryByRole('button', { name: 'Remove Alice' })).toBeNull()
  })

  it('tells everyone but a super admin which members the roster leaves out', async () => {
    const note = 'You see super admins and the members of workspaces you can use.'
    seed({ info: { ...serverInfo, member: { ...alice, role: 'admin' } } })
    const view = render(<MembersRoute params={{}} client={fakeApi()} />)
    expect(await screen.findByText(note)).toBeDefined()
    view.unmount()
    seed({ info: { ...serverInfo, member: { ...alice, role: 'super_admin' } } })
    render(<MembersRoute params={{}} client={fakeApi()} />)
    await screen.findByRole('region', { name: 'Roster' })
    expect(screen.queryByText(note)).toBeNull()
  })

  it('shows a pending member approved before the server answers, with no second Approve', async () => {
    let answer: (member: Member) => void = () => {}
    const client = fakeApi({
      memberList: vi.fn(async () => ({ members: [alice, bob, pendingCara] })),
      memberApprove: vi.fn(() => new Promise<Member>((resolve) => (answer = resolve))),
    })
    seed()
    render(<MembersRoute params={{}} client={client} />)

    fireEvent.click(await screen.findByRole('button', { name: 'Approve' }))

    expect(within(screen.getByRole('region', { name: 'Roster' })).getByText('Cara')).toBeDefined()
    expect(screen.queryByRole('button', { name: 'Approve' })).toBeNull()
    expect(client.memberApprove).toHaveBeenCalledTimes(1)
    await act(async () => answer({ ...pendingCara, pending: false }))
    expect(within(screen.getByRole('region', { name: 'Roster' })).getByText('Cara')).toBeDefined()
    expect(client.memberList).toHaveBeenCalledTimes(1)
  })

  it('takes a refused approval back and renders the server refusal verbatim', async () => {
    const client = fakeApi({
      memberList: vi.fn(async () => ({ members: [alice, bob, pendingCara] })),
      memberApprove: vi.fn(async () => {
        throw new ApiError(403, 'member.approve requires the admin role')
      }),
    })
    seed()
    render(<MembersRoute params={{}} client={client} />)

    fireEvent.click(await screen.findByRole('button', { name: 'Approve' }))

    expect(await screen.findByText('member.approve requires the admin role')).toBeDefined()
    expect(screen.getByRole('button', { name: 'Approve' })).toBeDefined()
    expect(within(screen.getByRole('region', { name: 'Roster' })).queryByText('Cara')).toBeNull()
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

  it('moves another member to a new role from the Role column before the server answers', async () => {
    let answer: (member: Member) => void = () => {}
    const client = fakeApi({ memberRole: vi.fn(() => new Promise<Member>((resolve) => (answer = resolve))) })
    seed()
    render(<MembersRoute params={{}} client={client} />)

    await pickRole('Bob', 'Admin')

    expect(within(row('Bob')).getByText('Admin')).toBeDefined()
    expect(client.memberRole).toHaveBeenCalledWith(bob.id, 'admin')
    await act(async () => answer({ ...bob, role: 'admin' }))
    expect(within(row('Bob')).getByText('Admin')).toBeDefined()
  })

  it('takes a refused role change back and renders the server refusal verbatim', async () => {
    const client = fakeApi({
      memberRole: vi.fn(async () => {
        throw new ApiError(409, 'refusing to demote the last admin')
      }),
    })
    seed()
    render(<MembersRoute params={{}} client={client} />)

    await pickRole('Bob', 'Viewer')

    expect(await screen.findByText('refusing to demote the last admin')).toBeDefined()
    expect(within(row('Bob')).getByText('Collaborator')).toBeDefined()
  })

  it('keeps a role the server already sent when the change goes unanswered', async () => {
    const client = fakeApi({ memberRole: vi.fn(() => new Promise<Member>(() => {})) })
    seed()
    render(<MembersRoute params={{}} client={client} />)
    const list = await openSelect(screen.getByRole('combobox', { name: 'Role of Bob' }))
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    try {
      const admin = within(list).getByRole('option', { name: /^Admin/ })
      admin.focus()
      fireEvent.keyDown(admin, { key: 'Enter' })
      act(() => useStore.getState().setMembers([alice, { ...bob, role: 'admin' }]))
      act(() => vi.advanceTimersByTime(optimisticDeadlineMs))
      await act(async () => {})
    } finally {
      vi.useRealTimers()
    }

    expect(within(row('Bob')).getByText('Admin')).toBeDefined()
  })

  it('keeps the newer role when an older role change is answered after it', async () => {
    let answerFirst: (member: Member) => void = () => {}
    const client = fakeApi({
      memberRole: vi.fn()
        .mockReturnValueOnce(new Promise<Member>((resolve) => (answerFirst = resolve)))
        .mockResolvedValueOnce({ ...bob, role: 'viewer' }),
    })
    seed()
    render(<MembersRoute params={{}} client={client} />)

    await pickRole('Bob', 'Admin')
    await waitFor(() => expect(screen.queryByRole('listbox')).toBeNull())
    await pickRole('Bob', 'Viewer')
    await waitFor(() => expect(client.memberRole).toHaveBeenCalledTimes(2))
    await act(async () => answerFirst({ ...bob, role: 'admin' }))

    expect(within(row('Bob')).getByText('Viewer')).toBeDefined()
  })

  it('keeps a confirmed role when the roster read sent before it answers later', async () => {
    let list: (list: MemberList) => void = () => {}
    const client = fakeApi({
      memberList: vi.fn(() => new Promise<MemberList>((resolve) => (list = resolve))),
      memberRole: vi.fn(async () => ({ ...bob, role: 'admin' as const })),
    })
    seed()
    render(<MembersRoute params={{}} client={client} />)

    await pickRole('Bob', 'Admin')
    await waitFor(() => expect(client.memberRole).toHaveBeenCalled())
    await act(async () => list({ members: [alice, bob] }))

    expect(within(row('Bob')).getByText('Admin')).toBeDefined()
  })

  it('keeps a colour Profile saved while a roster read was pending', async () => {
    let list: (list: MemberList) => void = () => {}
    const client = fakeApi({ memberList: vi.fn(() => new Promise<MemberList>((resolve) => (list = resolve))) })
    seed()
    render(<MembersRoute params={{}} client={client} />)
    await waitFor(() => expect(client.memberList).toHaveBeenCalled())

    const { patchMember, confirmMember } = useStore.getState()
    act(() => void confirmMember(alice.id, { color: '#3cb44b' }, patchMember(alice.id, { color: '#3cb44b' })))
    await act(async () => list({ members: [alice, bob] }))

    expect(useStore.getState().members[alice.id].color).toBe('#3cb44b')
  })

  it('keeps a pending role over the roster re-read after a removal', async () => {
    const client = fakeApi({
      memberList: vi.fn().mockResolvedValueOnce({ members: [alice, bob, vera] }).mockResolvedValue({ members: [alice, bob] }),
      memberRole: vi.fn(() => new Promise<Member>(() => {})),
    })
    seed({ members: { [alice.id]: alice, [bob.id]: bob, [vera.id]: vera } })
    render(<MembersRoute params={{}} client={client} />)
    await waitFor(() => expect(client.memberList).toHaveBeenCalledTimes(1))

    await pickRole('Bob', 'Admin')
    await waitFor(() => expect(screen.queryByRole('listbox')).toBeNull())
    fireEvent.click(screen.getByRole('button', { name: 'Remove Vera' }))
    fireEvent.click(within(await screen.findByRole('alertdialog', { name: 'Remove Vera?' })).getByRole('button', { name: 'Remove' }))
    await waitFor(() => expect(screen.queryByText('Vera')).toBeNull())

    expect(within(row('Bob')).getByText('Admin')).toBeDefined()
  })

  it('takes a member from a later roster read once its role change was answered', async () => {
    const client = fakeApi({ memberRole: vi.fn(async () => ({ ...bob, role: 'admin' as const })) })
    seed()
    const { rerender } = render(<MembersRoute params={{}} client={client} />)
    await waitFor(() => expect(client.memberList).toHaveBeenCalledTimes(1))

    await pickRole('Bob', 'Admin')
    await waitFor(() => expect(client.memberRole).toHaveBeenCalled())
    rerender(<MembersRoute params={{}} client={fakeApi({ memberList: vi.fn(async () => ({ members: [alice, { ...bob, role: 'viewer' as const }] })) })} />)

    await waitFor(() => expect(within(row('Bob')).getByText('Viewer')).toBeDefined())
  })

  it('takes a teammate\'s own rename from a roster read while their role change is pending, and still reverts the role', async () => {
    seed()
    const { patchMember, readMembers, setMemberList, revertMember } = useStore.getState()
    const patch = patchMember(bob.id, { role: 'admin' })
    setMemberList(await readMembers(Promise.resolve({ members: [alice, { ...bob, display_name: 'Robert' }] })))
    expect(useStore.getState().members[bob.id]).toMatchObject({ role: 'admin', display_name: 'Robert' })
    revertMember(bob.id, patch)

    expect(useStore.getState().members[bob.id]).toMatchObject({ role: bob.role, display_name: 'Robert' })
  })

  it('puts your own header name back from a roster read that landed while your refused rename was pending', () => {
    seed()
    const { patchMember, setMembers, revertMember } = useStore.getState()
    const patch = patchMember(alice.id, { display_name: 'Alicia' })
    setMembers([alice, bob])
    revertMember(alice.id, patch)

    expect(useStore.getState().info?.member.display_name).toBe('Alice')
  })

  it('goes back to the confirmed name when two overlapping renames are refused in the order they started', () => {
    seed()
    const { patchMember, revertMember } = useStore.getState()
    const first = patchMember(alice.id, { display_name: 'Alicia' })
    const second = patchMember(alice.id, { display_name: 'Ali' })
    revertMember(alice.id, first)
    expect(useStore.getState().members[alice.id].display_name).toBe('Ali')
    revertMember(alice.id, second)

    expect(useStore.getState().members[alice.id].display_name).toBe('Alice')
    expect(useStore.getState().info?.member.display_name).toBe('Alice')
  })

  it('labels the removal confirm while the server answers', async () => {
    const client = fakeApi({ memberRemove: vi.fn(() => new Promise<never>(() => {})) })
    seed()
    render(<MembersRoute params={{}} client={client} />)

    fireEvent.click(screen.getByRole('button', { name: 'Remove Bob' }))
    const dialog = within(await screen.findByRole('alertdialog', { name: 'Remove Bob?' }))
    fireEvent.click(dialog.getByRole('button', { name: 'Remove' }))

    expect(dialog.getByRole('button', { name: 'Removing…' }).hasAttribute('disabled')).toBe(true)
  })

  it('confirms a removal and keeps a refusal in the dialog', async () => {
    const client = fakeApi({
      memberRemove: vi.fn(async () => {
        throw new ApiError(409, 'member has running sessions')
      }),
    })
    seed()
    render(<MembersRoute params={{}} client={client} />)

    fireEvent.click(screen.getByRole('button', { name: 'Remove Bob' }))
    const dialog = within(await screen.findByRole('alertdialog', { name: 'Remove Bob?' }))
    fireEvent.click(dialog.getByRole('button', { name: 'Remove' }))

    expect(await dialog.findByText('member has running sessions')).toBeDefined()
    expect(client.memberRemove).toHaveBeenCalledWith(bob.id)
  })

  it('returns focus to the Remove button when the confirm it opened closes', async () => {
    seed()
    render(<MembersRoute params={{}} client={fakeApi()} />)

    const remove = screen.getByRole('button', { name: 'Remove Bob' })
    remove.focus()
    fireEvent.click(remove)
    await screen.findByRole('alertdialog', { name: 'Remove Bob?' })
    await userEvent.keyboard('{Escape}')

    await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull())
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Remove Bob' })))
  })

  it('confirms before an admin gives up their own admin role, and holds the confirm until the server answers', async () => {
    let refuse: (reason: Error) => void = () => {}
    const client = fakeApi({ memberRole: vi.fn(() => new Promise<never>((_, reject) => { refuse = reject })) })
    seed()
    render(<MembersRoute params={{}} client={client} />)

    await pickRole('Alice', 'Collaborator')
    expect(client.memberRole).not.toHaveBeenCalled()
    fireEvent.click(await screen.findByRole('button', { name: 'Become collaborator' }))
    expect(client.memberRole).toHaveBeenCalledWith(alice.id, 'collaborator')

    fireEvent.keyDown(screen.getByRole('alertdialog'), { key: 'Escape' })
    expect(screen.getByRole('alertdialog')).toBeDefined()
    refuse(new Error('member.role: last admin'))
    expect(await screen.findByText(/last admin/)).toBeDefined()
  })

  it('gives a non-admin the roster as text, with no admin verbs', async () => {
    const client = fakeApi({ memberList: vi.fn(async () => ({ members: [alice, bob, vera, pendingCara] })) })
    // The local gateway forwards every method, so only the caller's role keeps the admin verbs off.
    seed({ info: { ...serverInfo, member: bob } })
    render(<MembersRoute params={{}} client={client} />)

    expect(await within(screen.getByRole('region', { name: 'Roster' })).findByText('Vera')).toBeDefined()
    expect(within(row('Vera')).getByText('Viewer')).toBeDefined()
    expect(screen.queryByRole('combobox', { name: /^Role of/ })).toBeNull()
    expect(screen.queryByRole('button', { name: /^Remove / })).toBeNull()
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

  it('opens the Devices route on its tab, without the Members-only Invite…', async () => {
    seed({ route: { name: 'devices', params: {} } })
    render(<MembersRoute params={{}} client={fakeApi()} />)

    expect(screen.getByRole('tab', { name: 'Devices', selected: true })).toBeDefined()
    expect(await screen.findByRole('form', { name: 'Approve a device' })).toBeDefined()
    expect(screen.queryByRole('button', { name: 'Invite…' })).toBeNull()
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
