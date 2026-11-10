import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ApiError } from '@/lib/api'
import type { Member } from '@/lib/types'
import { WorkspacesRoute } from '@/routes/workspaces'
import { useStore } from '@/store'
import { alice, bob, fakeApi, otherWorkspace, serverInfo, vera, workspace } from '@/test/fixtures'
import { pickOption } from '@/test/select'

const admin: Member = { ...alice, role: 'admin' }
const root: Member = { ...alice, id: 'mem_root', display_name: 'Root', role: 'super_admin' }

function seed(self: Member, roles: { owned?: boolean; other?: boolean }) {
  useStore.setState({
    workspaces: {
      [workspace.id]: { ...workspace, owner_member_id: alice.id, role: roles.owned ? 'admin' : 'collaborator' },
      [otherWorkspace.id]: { ...otherWorkspace, owner_member_id: root.id, role: roles.other ? 'admin' : 'collaborator' },
    },
    deletedWorkspaceIDs: new Set(),
    lastSeq: 0,
    activeWorkspace: '',
    members: { [alice.id]: admin, [bob.id]: bob, [vera.id]: vera, [root.id]: root },
    info: { ...serverInfo, member: self },
    capabilities: { gateway: 'remote', methods: ['*'], ws: ['events', 'attach'] },
    hydrated: true,
    hydrationError: null,
    route: { name: 'workspaces', params: {} },
  })
}

async function menuOf(name: string) {
  const trigger = await screen.findByRole('button', { name: `More actions for ${name}` })
  trigger.focus()
  await userEvent.keyboard('{Enter}')
  return within(await screen.findByRole('menu'))
}

function client(over = {}) {
  return fakeApi({
    memberList: vi.fn(async () => ({ members: [admin, bob, vera, root] })),
    workspaceListFull: vi.fn(async () => Object.values(useStore.getState().workspaces)),
    workspaceAccessList: vi.fn(async () => ({
      workspace_id: workspace.id,
      owner_member_id: alice.id,
      grants: [{ member_id: bob.id, created_at: '2026-11-10T10:00:00Z' }],
    })),
    ...over,
  })
}

describe('workspace access', () => {
  it('offers Access only where the caller administers the workspace, and Change owner only to super admins', async () => {
    seed(admin, { owned: true })
    render(<WorkspacesRoute params={{}} client={client()} />)
    const owned = await menuOf('main-repo')
    expect(owned.getByRole('menuitem', { name: 'Access…' })).toBeTruthy()
    expect(owned.queryByRole('menuitem', { name: 'Change owner…' })).toBeNull()
    expect(owned.getByRole('menuitem', { name: 'Delete…' })).toBeTruthy()
    await userEvent.keyboard('{Escape}')
    const other = await menuOf('docs-site')
    expect(other.queryByRole('menuitem', { name: 'Access…' })).toBeNull()
    expect(other.queryByRole('menuitem', { name: 'Settings…' })).toBeNull()
    expect(other.queryByRole('menuitem', { name: 'Delete…' })).toBeNull()
  })

  it('lists the owner and grants, offers non-super admins only collaborators and viewers, and grants', async () => {
    seed(admin, { owned: true })
    const api = client()
    render(<WorkspacesRoute params={{}} client={api} />)
    fireEvent.click((await menuOf('main-repo')).getByRole('menuitem', { name: 'Access…' }))
    const dialog = within(await screen.findByRole('dialog', { name: 'Access to main-repo' }))
    const list = within(await dialog.findByRole('list', { name: 'Members with access' }))
    expect(list.getByText('Owner · Admin')).toBeTruthy()
    expect(list.getByText('Bob')).toBeTruthy()
    await userEvent.click(dialog.getByLabelText('Add a member'))
    expect(await screen.findByRole('option', { name: /Vera/ })).toBeTruthy()
    expect(screen.queryByRole('option', { name: /Root/ })).toBeNull()
    expect(screen.queryByRole('option', { name: /Bob/ })).toBeNull()
    await userEvent.keyboard('{Escape}')
    await pickOption(dialog.getByLabelText('Add a member'), /^Vera/)
    fireEvent.click(dialog.getByRole('button', { name: 'Add' }))
    await waitFor(() => expect(api.workspaceAccessGrant).toHaveBeenCalledWith(workspace.id, [vera.id]))
    await waitFor(() => expect(api.workspaceListFull).toHaveBeenCalled())
  })

  it('shows the server error of a refused revoke verbatim', async () => {
    seed(admin, { owned: true })
    const api = client({ workspaceAccessRevoke: vi.fn(async () => { throw new ApiError(500, 'member has sessions in use here') }) })
    render(<WorkspacesRoute params={{}} client={api} />)
    fireEvent.click((await menuOf('main-repo')).getByRole('menuitem', { name: 'Access…' }))
    const dialog = within(await screen.findByRole('dialog', { name: 'Access to main-repo' }))
    fireEvent.click(await dialog.findByRole('button', { name: 'Remove Bob' }))
    expect((await dialog.findByRole('alert')).textContent).toContain('member has sessions in use here')
    expect(api.workspaceAccessRevoke).toHaveBeenCalledWith(workspace.id, bob.id)
  })

  it('lets a super admin change the owner among admins', async () => {
    seed(root, { owned: true, other: true })
    const api = client()
    render(<WorkspacesRoute params={{}} client={api} />)
    fireEvent.click((await menuOf('main-repo')).getByRole('menuitem', { name: 'Change owner…' }))
    const dialog = within(await screen.findByRole('dialog', { name: 'Change owner of main-repo' }))
    await userEvent.click(dialog.getByLabelText('Owner'))
    expect(screen.queryByRole('option', { name: /Bob/ })).toBeNull()
    expect(screen.getByRole('option', { name: /^Alice \(current\)/ })).toBeTruthy()
    await userEvent.keyboard('{Escape}')
    expect(dialog.queryByText(/keeps access/)).toBeNull()
    await pickOption(dialog.getByLabelText('Owner'), /^Root/)
    fireEvent.click(dialog.getByRole('button', { name: 'Change owner' }))
    expect(dialog.getByText('Alice keeps access as a granted member.')).toBeTruthy()
    await waitFor(() => expect(api.workspaceOwnerSet).toHaveBeenCalledWith(workspace.id, root.id))
  })

  it('never offers a super admin to add, and says so when everyone has access', async () => {
    seed(admin, { owned: true })
    const api = client({
      memberList: vi.fn(async () => ({ members: [admin, bob, root] })),
      workspaceAccessList: vi.fn(async () => ({ workspace_id: workspace.id, owner_member_id: alice.id, grants: [{ member_id: bob.id, created_at: '2026-11-10T10:00:00Z' }] })),
    })
    render(<WorkspacesRoute params={{}} client={api} />)
    fireEvent.click((await menuOf('main-repo')).getByRole('menuitem', { name: 'Access…' }))
    const dialog = within(await screen.findByRole('dialog', { name: 'Access to main-repo' }))
    expect(await dialog.findByText('Everyone already has access')).toBeTruthy()
    expect(dialog.queryByRole('button', { name: 'Add' })).toBeNull()
  })

  it('shows the owner on the row and says when no one else has access', async () => {
    seed(admin, { owned: true })
    const api = client({
      workspaceAccessList: vi.fn(async () => ({ workspace_id: workspace.id, owner_member_id: alice.id, grants: [] })),
    })
    render(<WorkspacesRoute params={{}} client={api} />)
    expect(await screen.findByText(/Owner: you/)).toBeTruthy()
    expect(screen.getByText(/Owner: Root/)).toBeTruthy()
    fireEvent.click((await menuOf('main-repo')).getByRole('menuitem', { name: 'Access…' }))
    const dialog = within(await screen.findByRole('dialog', { name: 'Access to main-repo' }))
    expect(await dialog.findByText('No one else has access yet.')).toBeTruthy()
  })
})
