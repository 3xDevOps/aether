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

async function openAccess(name: string) {
  fireEvent.click(await screen.findByRole('button', { name: `Access to ${name}` }))
  return within(await screen.findByRole('dialog', { name: `Access to ${name}` }))
}

async function openOwner(name: string) {
  const access = await openAccess(name)
  fireEvent.click(await access.findByRole('button', { name: 'Change owner…' }))
  return within(await screen.findByRole('dialog', { name: `Change owner of ${name}` }))
}

const box = (dialog: ReturnType<typeof within>, name: string) => dialog.getByRole('checkbox', { name: new RegExp(`^${name}`) })

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
  it('puts Access on the rows the caller administers, and Change owner in it only for super admins', async () => {
    seed(admin, { owned: true })
    render(<WorkspacesRoute params={{}} client={client()} />)
    expect(await screen.findByRole('button', { name: 'Settings for docs-site' })).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Access to docs-site' })).toBeNull()
    const dialog = await openAccess('main-repo')
    expect(await dialog.findByText('Owner · Admin')).toBeTruthy()
    expect(dialog.queryByRole('button', { name: 'Change owner…' })).toBeNull()
  })

  it('ticks who has access, leaves super admins out of the list, and grants with one click', async () => {
    seed(admin, { owned: true })
    const api = client()
    render(<WorkspacesRoute params={{}} client={api} />)
    const dialog = await openAccess('main-repo')
    await waitFor(() => expect(box(dialog, 'Bob').getAttribute('aria-checked')).toBe('true'))
    expect(box(dialog, 'Vera').getAttribute('aria-checked')).toBe('false')
    expect(dialog.queryByRole('checkbox', { name: /^Root/ })).toBeNull()
    expect(dialog.getByText(/Super admins reach every workspace: Root\./)).toBeTruthy()
    expect(dialog.getByText('1 of 2 members have access')).toBeTruthy()
    fireEvent.click(box(dialog, 'Vera'))
    await waitFor(() => expect(api.workspaceAccessGrant).toHaveBeenCalledWith(workspace.id, [vera.id]))
    await waitFor(() => expect(api.workspaceListFull).toHaveBeenCalled())
  })

  it('narrows the list as the caller types a name', async () => {
    seed(admin, { owned: true })
    render(<WorkspacesRoute params={{}} client={client()} />)
    const dialog = await openAccess('main-repo')
    await dialog.findByRole('checkbox', { name: /^Bob/ })
    fireEvent.change(dialog.getByRole('searchbox', { name: 'Search members' }), { target: { value: 've' } })
    expect(dialog.queryByRole('checkbox', { name: /^Bob/ })).toBeNull()
    expect(box(dialog, 'Vera')).toBeTruthy()
    fireEvent.change(dialog.getByRole('searchbox', { name: 'Search members' }), { target: { value: 'zed' } })
    expect(dialog.getByText('No member matches “zed”.')).toBeTruthy()
  })

  it('locks admins for a caller who is not a super admin', async () => {
    seed(admin, { owned: true })
    const other: Member = { ...bob, id: 'mem_ida', display_name: 'Ida', role: 'admin' }
    render(<WorkspacesRoute params={{}} client={client({ memberList: vi.fn(async () => ({ members: [admin, bob, other, root] })) })} />)
    const dialog = await openAccess('main-repo')
    await waitFor(() => expect((box(dialog, 'Ida') as HTMLButtonElement).disabled).toBe(true))
    expect(dialog.getByText("Admin · only a super admin changes an admin's access")).toBeTruthy()
  })

  it('shows the server error of a refused revoke verbatim', async () => {
    seed(admin, { owned: true })
    const api = client({ workspaceAccessRevoke: vi.fn(async () => { throw new ApiError(500, 'member has sessions in use here') }) })
    render(<WorkspacesRoute params={{}} client={api} />)
    const dialog = await openAccess('main-repo')
    await waitFor(() => expect(box(dialog, 'Bob').getAttribute('aria-checked')).toBe('true'))
    fireEvent.click(box(dialog, 'Bob'))
    expect((await dialog.findByRole('alert')).textContent).toContain('member has sessions in use here')
    expect(api.workspaceAccessRevoke).toHaveBeenCalledWith(workspace.id, bob.id)
  })

  it('lets a super admin change the owner among admins', async () => {
    seed(root, { owned: true, other: true })
    const api = client()
    render(<WorkspacesRoute params={{}} client={api} />)
    const dialog = await openOwner('main-repo')
    await userEvent.click(dialog.getByLabelText('Owner'))
    expect(screen.queryByRole('option', { name: /Bob/ })).toBeNull()
    expect(screen.getByRole('option', { name: /^Alice \((you, )?current owner\)/ })).toBeTruthy()
    await userEvent.keyboard('{Escape}')
    expect(dialog.queryByText(/keeps access/)).toBeNull()
    await pickOption(dialog.getByLabelText('Owner'), /^Root/)
    fireEvent.click(dialog.getByRole('button', { name: 'Change owner' }))
    expect(dialog.getByText('Alice keeps access as a granted member.')).toBeTruthy()
    await waitFor(() => expect(api.workspaceOwnerSet).toHaveBeenCalledWith(workspace.id, root.id))
  })

  it('clears a member list failure once a later read succeeds', async () => {
    seed(admin, { owned: true })
    const failing = client({ memberList: vi.fn(async () => { throw new ApiError(500, 'members unavailable') }) })
    const { rerender } = render(<WorkspacesRoute params={{}} client={failing} />)
    expect(await screen.findByText(/Could not list members/)).toBeTruthy()
    rerender(<WorkspacesRoute params={{}} client={client()} />)
    await waitFor(() => expect(screen.queryByText(/Could not list members/)).toBeNull())
  })

  it('shows why the member list could not be read', async () => {
    seed(admin, { owned: true })
    const api = client({ memberList: vi.fn(async () => { throw new ApiError(500, 'members unavailable') }) })
    render(<WorkspacesRoute params={{}} client={api} />)
    expect(await screen.findByText(/Could not list members: .*members unavailable/)).toBeTruthy()
    const dialog = await openAccess('main-repo')
    expect(await dialog.findByText(/Could not list members: .*members unavailable/)).toBeTruthy()
  })

  it('tells an owner who is not a super admin how to reach members they cannot see', async () => {
    seed(admin, { owned: true })
    render(<WorkspacesRoute params={{}} client={client({ memberList: vi.fn(async () => ({ members: [admin, bob, root] })) })} />)
    const dialog = await openAccess('main-repo')
    expect(await dialog.findByText(/Invite someone new from Members, or ask a super admin to add a member you do not see here\./)).toBeTruthy()
  })

  it('shows the owner on the row and counts who has access', async () => {
    seed(admin, { owned: true })
    const api = client({
      workspaceAccessList: vi.fn(async () => ({ workspace_id: workspace.id, owner_member_id: alice.id, grants: [] })),
    })
    render(<WorkspacesRoute params={{}} client={api} />)
    expect(await screen.findByText(/Owner: you/)).toBeTruthy()
    expect(screen.getByText(/Owner: Root/)).toBeTruthy()
    const dialog = await openAccess('main-repo')
    expect(await dialog.findByText('0 of 2 members have access')).toBeTruthy()
  })

  it('drops a workspace list that arrives after the identity changed', async () => {
    seed(admin, { owned: true })
    let release: (list: unknown[]) => void = () => {}
    const api = client({
      workspaceListFull: vi.fn()
        .mockResolvedValueOnce(Object.values(useStore.getState().workspaces))
        .mockImplementationOnce(() => new Promise((resolve) => { release = resolve })),
    })
    render(<WorkspacesRoute params={{}} client={api} />)
    const dialog = await openAccess('main-repo')
    await waitFor(() => expect(box(dialog, 'Bob').getAttribute('aria-checked')).toBe('true'))
    fireEvent.click(box(dialog, 'Bob'))
    await waitFor(() => expect(api.workspaceListFull).toHaveBeenCalledTimes(2))
    useStore.setState({ identityKey: 'another-server' })
    release([{ ...workspace, id: 'ws_stale' }])
    await waitFor(() => expect(api.workspaceAccessRevoke).toHaveBeenCalled())
    await new Promise((resolve) => setTimeout(resolve, 20))
    expect(useStore.getState().workspaces.ws_stale).toBeUndefined()
  })

  it('closes the access dialog and drops a late grant list when the signed-in member changes', async () => {
    seed(admin, { owned: true })
    useStore.setState({ identityKey: 'alice' })
    let release: (v: unknown) => void = () => {}
    const api = client({
      workspaceAccessList: vi.fn(() => new Promise((resolve) => { release = resolve })),
    })
    render(<WorkspacesRoute params={{}} client={api} />)
    await openAccess('main-repo')
    useStore.setState({ identityKey: 'bob' })
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    release({ workspace_id: workspace.id, owner_member_id: alice.id, grants: [] })
    expect(screen.queryByText(/members have access/)).toBeNull()
  })

  it('closes the owner dialog when the signed-in member changes', async () => {
    seed(root, { owned: true, other: true })
    useStore.setState({ identityKey: 'root' })
    render(<WorkspacesRoute params={{}} client={client()} />)
    await openOwner('main-repo')
    useStore.setState({ identityKey: 'alice' })
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
  })
})

describe('a server without workspace access', () => {
  it('offers no Access or Change owner actions', async () => {
    seed(root, { owned: true })
    useStore.setState({ info: { ...serverInfo, member: { ...root, role: 'admin' }, features: [] } })
    render(<WorkspacesRoute params={{}} client={client()} />)
    expect(await screen.findByRole('button', { name: 'Settings for main-repo' })).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Access to main-repo' })).toBeNull()
  })
})
