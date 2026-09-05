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
  // The add form rides the shared environment choice: standard preselected,
  // no environment shaping of its own.
  it('submits the standard environment by default', async () => {
    const client = fakeApi({
      workspaceListFull: vi.fn(async () => []),
      workspaceAdd: vi.fn(async () => workspace),
    })
    seed()
    render(<WorkspacesRoute params={{}} client={client} />)

    const form = within(await screen.findByRole('form', { name: 'Add workspace' }))
    expect(
      form.getByRole('radio', { name: /Standard environment/ }),
    ).toHaveProperty('checked', true)
    fireEvent.change(form.getByLabelText(/^Name/), { target: { value: 'bare' } })
    fireEvent.click(form.getByRole('button', { name: 'Add' }))

    expect(client.workspaceAdd).toHaveBeenCalledWith({
      name: 'bare',
      base_branch: 'main',
      environment: { custom_image: serverInfo.standard_image },
    })
  })

  it('submits a custom image environment from the custom card', async () => {
    const client = fakeApi({
      workspaceListFull: vi.fn(async () => [workspace]),
      workspaceAdd: vi.fn(async () => workspace),
    })
    seed()
    render(<WorkspacesRoute params={{}} client={client} />)

    const form = within(await screen.findByRole('form', { name: 'Add workspace' }))
    fireEvent.change(form.getByLabelText(/^Name/), { target: { value: 'infra' } })
    fireEvent.change(form.getByLabelText('Base branch'), {
      target: { value: 'trunk' },
    })
    fireEvent.click(form.getByRole('radio', { name: /Custom image/ }))
    // The image input exists only once its card is chosen, and an empty one
    // keeps the submit disabled.
    expect(form.getByRole('button', { name: 'Add' })).toHaveProperty(
      'disabled',
      true,
    )
    fireEvent.change(form.getByLabelText('Image reference'), {
      target: { value: 'ubuntu:24.04' },
    })
    fireEvent.click(form.getByRole('button', { name: 'Add' }))

    expect(client.workspaceAdd).toHaveBeenCalledWith({
      name: 'infra',
      base_branch: 'trunk',
      environment: { custom_image: 'ubuntu:24.04' },
    })
  })

  it('sends the neutral image from the minimal starter card', async () => {
    const client = fakeApi({
      workspaceListFull: vi.fn(async () => []),
      workspaceAdd: vi.fn(async () => workspace),
    })
    seed()
    render(<WorkspacesRoute params={{}} client={client} />)

    const form = within(await screen.findByRole('form', { name: 'Add workspace' }))
    fireEvent.change(form.getByLabelText(/^Name/), { target: { value: 'bare' } })
    fireEvent.click(form.getByRole('radio', { name: /Minimal starter/ }))
    fireEvent.click(form.getByRole('button', { name: 'Add' }))

    expect(client.workspaceAdd).toHaveBeenCalledWith({
      name: 'bare',
      base_branch: 'main',
      environment: { neutral_image: true },
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
  it('updates a workspace on a stale standard image', async () => {
    const client = fakeApi({
      workspaceListFull: vi.fn(async () => [workspace]),
      workspaceImage: vi
        .fn()
        .mockResolvedValueOnce({
          workspace,
          image: 'ghcr.io/3xdevops/aether-standard:1.2.2',
        })
        .mockResolvedValueOnce({
          workspace,
          image: serverInfo.standard_image ?? '',
        }),
    })
    render(<WorkspacesRoute params={{}} client={client} />)

    const update = await screen.findByRole('button', { name: 'Update to 1.2.3' })
    fireEvent.click(update)

    expect(client.workspaceImage).toHaveBeenCalledWith(workspace.id, serverInfo.standard_image)
    expect(await screen.findByText(serverInfo.standard_image as string)).toBeDefined()
  })

  it('does not offer a standard image update when already current', async () => {
    const client = fakeApi({
      workspaceListFull: vi.fn(async () => [workspace]),
      workspaceImage: vi.fn(async () => ({
        workspace,
        image: serverInfo.standard_image ?? '',
      })),
    })
    seed()
    render(<WorkspacesRoute params={{}} client={client} />)

    await screen.findByText(serverInfo.standard_image as string)
    expect(screen.queryByRole('button', { name: 'Update to 1.2.3' })).toBeNull()
  })

  it('hides workspace images without the workspace.image capability', async () => {
    const client = fakeApi({
      workspaceListFull: vi.fn(async () => [workspace]),
    })
    seed({
      capabilities: {
        gateway: 'remote',
        methods: ['workspace.add'],
        ws: ['events', 'attach'],
      },
    })
    render(<WorkspacesRoute params={{}} client={client} />)

    await screen.findByText(workspace.name)
    expect(screen.queryByText('Image')).toBeNull()
    expect(client.workspaceImage).not.toHaveBeenCalled()
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
