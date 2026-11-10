import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { Member } from '@/lib/types'
import { MembersRoute } from '@/routes/members'
import { administered, invitableRoles, workspacesValid } from '@/routes/members/workspace-picker'
import { useStore } from '@/store'
import { alice, bob, fakeApi, otherWorkspace, serverInfo, workspace } from '@/test/fixtures'
import { openSelect, pickOption } from '@/test/select'

const admin: Member = { ...alice, role: 'admin' }
const root: Member = { ...alice, id: 'mem_root', display_name: 'Root', role: 'super_admin' }
const ownedByAlice = { ...workspace, role: 'admin' as const }
const grantedToAlice = { ...otherWorkspace, role: 'collaborator' as const }

function seed(self: Member) {
  useStore.setState({
    workspaces: { [ownedByAlice.id]: ownedByAlice, [grantedToAlice.id]: grantedToAlice },
    members: { [alice.id]: admin, [bob.id]: bob, [root.id]: root },
    invitations: null,
    revokingInvitations: [],
    presence: [],
    info: { ...serverInfo, member: self },
    capabilities: { gateway: 'local', methods: ['*'], ws: ['events'] },
    route: { name: 'members', params: {} },
  })
}

async function inviteDialog() {
  fireEvent.click(await screen.findByRole('button', { name: 'Invite…' }))
  return within(await screen.findByRole('dialog', { name: 'Invite a member' }))
}

describe('invite workspace rules', () => {
  it('lists the workspaces the inviter administers and the roles they may hand out', () => {
    expect(administered({ [ownedByAlice.id]: ownedByAlice, [grantedToAlice.id]: grantedToAlice }, 'admin').map((w) => w.id)).toEqual([ownedByAlice.id])
    expect(administered({ [grantedToAlice.id]: { ...grantedToAlice, role: undefined } }, 'admin')).toHaveLength(1)
    expect(invitableRoles('admin')).toEqual(['viewer', 'collaborator'])
    expect(invitableRoles('super_admin')).toContain('super_admin')
    expect(workspacesValid('admin', [])).toBe(false)
    expect(workspacesValid('admin', ['w'])).toBe(true)
    expect(workspacesValid('super_admin', [])).toBe(true)
  })

  it('makes an admin pick a workspace and sends only administered ones with a collaborator or viewer role', async () => {
    seed(admin)
    const created = { invitation: { id: 'inv_1', login: 'octocat', role: 'viewer' as const, created_by: alice.id, created_at: '', expires_at: '' }, link: 'https://edge.example/j', server_id: 's' }
    const client = fakeApi({ memberInvitationCreate: vi.fn(async () => created) })
    render(<MembersRoute params={{}} client={client} />)
    const dialog = await inviteDialog()

    expect(dialog.queryByLabelText('docs-site')).toBeNull()
    const picker = dialog.getByLabelText('main-repo')
    fireEvent.click(picker)
    expect(picker.getAttribute('aria-checked')).toBe('false')
    fireEvent.change(dialog.getByLabelText('GitHub login'), { target: { value: 'octocat' } })
    expect((dialog.getByRole('button', { name: 'Invite' }) as HTMLButtonElement).disabled).toBe(true)

    await openSelect(dialog.getByLabelText('Role'))
    expect(screen.queryByRole('option', { name: /^Admin/ })).toBeNull()
    expect(screen.queryByRole('option', { name: /^Super admin/ })).toBeNull()
    await userEvent.keyboard('{Escape}')

    fireEvent.click(picker)
    fireEvent.click(dialog.getByRole('button', { name: 'Invite' }))
    await waitFor(() => expect(client.memberInvitationCreate).toHaveBeenCalledWith({ login: 'octocat', role: 'collaborator', workspace_ids: [workspace.id] }))
  })

  it('lets a super admin invite any role to no workspace', async () => {
    seed(root)
    const created = { invitation: { id: 'inv_1', login: 'octocat', role: 'admin' as const, created_by: root.id, created_at: '', expires_at: '' }, link: 'https://edge.example/j', server_id: 's' }
    const client = fakeApi({ memberInvitationCreate: vi.fn(async () => created) })
    render(<MembersRoute params={{}} client={client} />)
    const dialog = await inviteDialog()

    fireEvent.change(dialog.getByLabelText('GitHub login'), { target: { value: 'octocat' } })
    await openSelect(dialog.getByLabelText('Role'))
    expect(await screen.findByRole('option', { name: /^Super admin/ })).toBeTruthy()
    await userEvent.keyboard('{Escape}')
    fireEvent.click(dialog.getByRole('button', { name: 'Invite' }))
    await waitFor(() => expect(client.memberInvitationCreate).toHaveBeenCalledWith({ login: 'octocat', role: 'collaborator', workspace_ids: undefined }))
  })

  it('hides the workspace picker for a super admin invitee and sends no workspaces', async () => {
    seed(root)
    const created = { invitation: { id: 'inv_1', login: 'octocat', role: 'super_admin' as const, created_by: root.id, created_at: '', expires_at: '' }, link: 'https://edge.example/j', server_id: 's' }
    const client = fakeApi({ memberInvitationCreate: vi.fn(async () => created) })
    render(<MembersRoute params={{}} client={client} />)
    const dialog = await inviteDialog()

    fireEvent.change(dialog.getByLabelText('GitHub login'), { target: { value: 'octocat' } })
    fireEvent.click(dialog.getByLabelText('main-repo'))
    await pickOption(dialog.getByLabelText('Role'), /^Super admin/)
    expect(dialog.queryByLabelText('main-repo')).toBeNull()
    expect(dialog.getByText('A super admin can use every workspace.')).toBeTruthy()
    fireEvent.click(dialog.getByRole('button', { name: 'Invite' }))
    await waitFor(() => expect(client.memberInvitationCreate).toHaveBeenCalledWith({ login: 'octocat', role: 'super_admin', workspace_ids: undefined }))
  })

  it('tells an admin who owns no workspace to ask a super admin', async () => {
    seed(admin)
    useStore.setState({ workspaces: { [grantedToAlice.id]: grantedToAlice } })
    render(<MembersRoute params={{}} client={fakeApi()} />)
    const dialog = await inviteDialog()
    expect(dialog.getByText('You can invite once you own a workspace. Ask a super admin.')).toBeTruthy()
  })

  it('applies the same rule to an invite code', async () => {
    seed(admin)
    const client = fakeApi()
    render(<MembersRoute params={{}} client={client} />)
    const dialog = await inviteDialog()
    const codeTab = dialog.getByRole('tab', { name: 'Invite code' })
    codeTab.focus()
    await userEvent.keyboard('{Enter}')
    const generate = await dialog.findByRole('button', { name: 'Generate code' })
    const picker = dialog.getByLabelText('main-repo')
    expect(generate.hasAttribute('disabled')).toBe(false)
    fireEvent.click(picker)
    expect(generate.hasAttribute('disabled')).toBe(true)
    fireEvent.click(picker)
    fireEvent.click(generate)
    await waitFor(() => expect(client.memberInvite).toHaveBeenCalledWith(undefined, [workspace.id]))
  })
})
