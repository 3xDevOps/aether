import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { WorkspacesRoute } from '@/routes/workspaces'
import { useStore, type RootState } from '@/store'
import { alice, fakeApi, serverInfo, workspace } from '@/test/fixtures'

function seed(extra: Partial<RootState> = {}) {
  useStore.setState({
    workspaces: { [workspace.id]: workspace },
    activeWorkspace: '',
    members: { [alice.id]: alice },
    info: serverInfo,
    // workspace.add is capability-gated; an upgraded gateway advertising
    // every method renders the admin form.
    capabilities: { gateway: 'remote', methods: ['*'], ws: ['events', 'attach'] },
    hydrated: true,
    hydrationError: null,
    route: { name: 'workspaces', params: {} },
    ...extra,
  })
}

// A legacy null capability set covers only the pre-capabilities allowlist,
// so these tests advertise every method; a desktop gateway narrows this
// via /capabilities.
describe('workspaces view', () => {
  it('submits a workspace with no image selection', async () => {
    const client = fakeApi({
      workspaceListFull: vi.fn(async () => []),
      workspaceAdd: vi.fn(async () => workspace),
    })
    seed()
    render(<WorkspacesRoute params={{}} client={client} />)

    const form = within(await screen.findByRole('form', { name: 'Add workspace' }))
    fireEvent.change(form.getByLabelText(/^Name/), { target: { value: 'bare' } })
    fireEvent.click(form.getByRole('button', { name: 'Add' }))

    expect(client.workspaceAdd).toHaveBeenCalledWith({
      name: 'bare',
      base_branch: 'main',
      environment: {},
    })
  })


  // The base branch and the steering policy live on the workspace now, so
  // the admin list is where an operator compares them across workspaces.
  it('shows each workspace base branch and steering policy', async () => {
    const restricted = { ...workspace, steer_others: 'admins_only' }
    const client = fakeApi({
      workspaceListFull: vi.fn(async () => [restricted]),
    })
    seed()
    render(<WorkspacesRoute params={{}} client={client} />)

    expect(await screen.findByText(workspace.base_branch)).toBeDefined()
    expect(screen.getByText('admins steer others')).toBeDefined()
  })

  it('wraps workspace metadata and actions without crowding', async () => {
    const client = fakeApi({
      workspaceListFull: vi.fn(async () => [workspace]),
    })
    seed()
    render(<WorkspacesRoute params={{}} client={client} />)

    const name = await screen.findByText(workspace.name)
    expect(name.parentElement?.className).toContain('flex-wrap')
  })

  it('opens a workspace by making it the active scope', async () => {
    const client = fakeApi({
      workspaceListFull: vi.fn(async () => [workspace]),
    })
    seed()
    render(<WorkspacesRoute params={{}} client={client} />)

    fireEvent.click(await screen.findByRole('button', { name: 'Open' }))

    // Every other scoped surface follows activeWorkspace, so navigating
    // without setting it would leave the sidebar pointed elsewhere.
    await waitFor(() => {
      expect(useStore.getState().activeWorkspace).toBe(workspace.id)
      expect(useStore.getState().route).toEqual({
        name: 'workspace',
        params: { workspaceId: workspace.id },
      })
    })
  })

})
