import { fireEvent, render, screen, within } from '@testing-library/react'
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
    expect(screen.getByText('$0.50 spent of $5.00, warns at $4.00: within budget. It never stops a run.')).toBeDefined()
    expect(screen.getByText('Only admins may message runs started by other members.')).toBeDefined()
  })

  it('opens the budget dialog for an admin', async () => {
    seed()
    render(<WorkspaceView params={{ workspaceId: workspace.id }} client={fakeApi()} />)

    expect(screen.getByText('No budget set. A budget warns and reports; it never stops a run.')).toBeDefined()
    fireEvent.click(screen.getByRole('button', { name: 'Set budget…' }))
    expect(await screen.findByRole('dialog', { name: 'Workspace budget' })).toBeDefined()
  })

  it('gives a collaborator the facts without the admin buttons', async () => {
    seed({ info: { ...serverInfo, member: bob } })
    render(<WorkspaceView params={{ workspaceId: workspace.id }} client={fakeApi()} />)

    expect(screen.queryByRole('button', { name: 'Set budget…' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Change…' })).toBeNull()
    expect(screen.getByText('Any member who can message runs may also message runs started by others.')).toBeDefined()
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

  it('says when the workspace no longer exists', () => {
    seed()
    render(<WorkspaceView params={{ workspaceId: 'wsp_gone' }} client={fakeApi()} />)
    expect(screen.getByRole('heading', { name: 'Unknown workspace' })).toBeDefined()
  })
})
