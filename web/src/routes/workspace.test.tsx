import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { WorkspaceView } from '@/routes/workspace'
import { useStore, type RootState } from '@/store'
import { alice, bob, budget, fakeApi, serverInfo, workspace } from '@/test/fixtures'

function seed(extra: Partial<RootState> = {}) {
  useStore.setState({
    workspaces: { [workspace.id]: workspace },
    activeWorkspace: workspace.id,
    members: { [alice.id]: alice, [bob.id]: bob },
    budgets: {},
    info: serverInfo,
    capabilities: { gateway: 'remote', methods: ['*'], ws: ['events', 'attach'] },
    hydrated: true,
    ...extra,
  })
}

describe('repository page', () => {
  it('shows the base branch, the source, the budget and who may message other runs', async () => {
    seed({
      workspaces: { [workspace.id]: { ...workspace, steer_others: 'admins_only' } },
      budgets: { [workspace.id]: budget(workspace.id, { budget: { workspace_id: workspace.id, limit_usd: 5, warn_usd: 4 } }) },
    })
    render(<WorkspaceView params={{ workspaceId: workspace.id }} client={fakeApi()} />)

    expect(within(screen.getByRole('region', { name: 'Base branch' })).getByText('main')).toBeDefined()
    expect(await screen.findByText(/Local-only workspace/)).toBeDefined()
    expect(screen.getByText('$0.50 spent of $5.00, warns at $4.00: within budget. It never stops a session.')).toBeDefined()
    expect(screen.getByText('Only admins may message sessions started by other members.')).toBeDefined()
  })

  it('opens the budget dialog for an admin', async () => {
    seed()
    render(<WorkspaceView params={{ workspaceId: workspace.id }} client={fakeApi()} />)

    expect(screen.getByText('No budget set. A budget warns and reports; it never stops a session.')).toBeDefined()
    fireEvent.click(screen.getByRole('button', { name: 'Set budget…' }))
    expect(await screen.findByRole('dialog', { name: 'Workspace budget' })).toBeDefined()
  })

  it('gives a collaborator the facts without the admin buttons', async () => {
    seed({ info: { ...serverInfo, member: bob } })
    render(<WorkspaceView params={{ workspaceId: workspace.id }} client={fakeApi()} />)

    expect(screen.queryByRole('button', { name: 'Set budget…' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Change…' })).toBeNull()
    expect(screen.getByText('Any member who can message sessions may also message sessions started by others.')).toBeDefined()
    const environment = within(screen.getByRole('region', { name: 'Environment' }))
    expect(await environment.findByText('Only an admin can change this.')).toBeDefined()
    expect(environment.queryByRole('button', { name: 'Save' })).toBeNull()
  })

  it('lets an admin edit the environment, and scrolls to it when opened for it', async () => {
    seed()
    const scrolled = vi.mocked(Element.prototype.scrollIntoView)
    scrolled.mockClear()
    render(<WorkspaceView params={{ workspaceId: workspace.id, section: 'environment' }} client={fakeApi()} />)

    const environment = within(screen.getByRole('region', { name: 'Environment' }))
    expect(await environment.findByRole('textbox', { name: 'Setup script' })).toBeDefined()
    expect(environment.getByRole('button', { name: 'Import .env…' })).toBeDefined()
    expect(scrolled).toHaveBeenCalled()
  })

  it('names the owner under Team and opens Access from there for an admin', async () => {
    seed({ workspaces: { [workspace.id]: { ...workspace, owner_member_id: bob.id, role: 'admin' } } })
    const client = fakeApi({ workspaceAccessList: vi.fn(async () => ({ workspace_id: workspace.id, owner_member_id: bob.id, grants: [] })) })
    render(<WorkspaceView params={{ workspaceId: workspace.id }} client={client} />)

    expect(screen.getByText(/^Owner: Bob\. /)).toBeDefined()
    fireEvent.click(screen.getByRole('button', { name: 'Access…' }))
    expect(await screen.findByRole('dialog', { name: `Access to ${workspace.name}` })).toBeDefined()
  })

  it('keeps deletion in a danger zone for the workspace admin', async () => {
    seed({ info: { ...serverInfo, member: bob } })
    const { unmount } = render(<WorkspaceView params={{ workspaceId: workspace.id }} client={fakeApi()} />)
    expect(screen.queryByRole('region', { name: 'Danger zone' })).toBeNull()
    unmount()

    seed({ capabilities: { gateway: 'remote', methods: ['workspace.list'], ws: [] } })
    render(<WorkspaceView params={{ workspaceId: workspace.id }} client={fakeApi()} />)
    expect(screen.queryByRole('button', { name: 'Delete…' })).toBeNull()
  })

  it('deletes after a confirm that keeps a refusal and can be retried, then lands on Manage workspaces', async () => {
    const refusal = 'workspace has active sessions; close them before deleting'
    const client = fakeApi({ workspaceDelete: vi.fn().mockRejectedValueOnce(new Error(refusal)).mockResolvedValue({ ok: true }) })
    seed({ route: { name: 'workspace', params: { workspaceId: workspace.id } } })
    render(<WorkspaceView params={{ workspaceId: workspace.id }} client={client} />)

    fireEvent.click(within(screen.getByRole('region', { name: 'Danger zone' })).getByRole('button', { name: 'Delete…' }))
    let dialog = within(await screen.findByRole('alertdialog'))
    fireEvent.click(dialog.getByRole('button', { name: 'Cancel' }))
    expect(client.workspaceDelete).not.toHaveBeenCalled()

    fireEvent.click(screen.getByRole('button', { name: 'Delete…' }))
    dialog = within(await screen.findByRole('alertdialog'))
    fireEvent.click(dialog.getByRole('button', { name: 'Delete workspace' }))
    expect((await dialog.findByRole('alert')).textContent).toBe(refusal)
    expect(useStore.getState().workspaces[workspace.id]).toEqual(workspace)

    fireEvent.click(dialog.getByRole('button', { name: 'Delete workspace' }))
    await waitFor(() => expect(useStore.getState().route).toEqual({ name: 'workspaces', params: {} }))
    expect(useStore.getState().workspaces[workspace.id]).toBeUndefined()
    expect(useStore.getState().activeWorkspace).toBe('')
    expect(client.workspaceDelete).toHaveBeenNthCalledWith(2, workspace.id)
  })

  it('closes a dialog opened for one workspace when the route moves to another', async () => {
    const other = { ...workspace, id: 'wsp_other', name: 'other-repo' }
    seed({ workspaces: { [workspace.id]: workspace, [other.id]: other } })
    const { rerender } = render(<WorkspaceView params={{ workspaceId: workspace.id }} client={fakeApi()} />)
    fireEvent.click(screen.getByRole('button', { name: 'Delete…' }))
    expect(await screen.findByRole('alertdialog', { name: `Delete ${workspace.name}?` })).toBeDefined()

    rerender(<WorkspaceView params={{ workspaceId: other.id }} client={fakeApi()} />)
    await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull())
  })

  it('says when the workspace no longer exists', () => {
    seed()
    render(<WorkspaceView params={{ workspaceId: 'wsp_gone' }} client={fakeApi()} />)
    expect(screen.getByRole('heading', { name: 'Unknown workspace' })).toBeDefined()
  })
})
