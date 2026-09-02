import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import type { ToolSnapshot } from '@/lib/types'
import { WorkspacesRoute } from '@/routes/workspaces'
import { useStore, type RootState } from '@/store'
import { alice, fakeApi, serverInfo, workspace } from '@/test/fixtures'

const snapshot: ToolSnapshot = {
  id: 'snapshot_1',
  workspace_id: workspace.id,
  member_id: alice.id,
  digest: 'sha256deadbeefcafe',
  created_at: '2026-08-14T09:30:00Z',
  manifest: { executable: 'omp' },
  active: true,
}

const older: ToolSnapshot = {
  ...snapshot,
  id: 'snapshot_0',
  digest: 'sha256feedfacebead',
  created_at: '2026-08-14T09:10:00Z',
  active: false,
}

function seed(extra: Partial<RootState> = {}) {
  useStore.setState({
    workspaces: { [workspace.id]: workspace },
    activeWorkspace: '',
    members: { [alice.id]: alice },
    info: serverInfo,
    // workspace.add and the tools verbs are capability-gated; an upgraded
    // gateway advertising every method renders the admin forms.
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
      toolsList: vi.fn(async () => []),
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
      toolsList: vi.fn(async () => []),
    })
    seed()
    render(<WorkspacesRoute params={{}} client={client} />)

    expect(await screen.findByText(workspace.base_branch)).toBeDefined()
    expect(screen.getByText('admins steer others')).toBeDefined()
  })

  it('wraps workspace metadata and actions without crowding', async () => {
    const client = fakeApi({
      workspaceListFull: vi.fn(async () => [workspace]),
      toolsList: vi.fn(async () => []),
    })
    seed()
    render(<WorkspacesRoute params={{}} client={client} />)

    const name = await screen.findByText(workspace.name)
    expect(name.parentElement?.className).toContain('flex-wrap')
  })

  it('opens a workspace by making it the active scope', async () => {
    const client = fakeApi({
      workspaceListFull: vi.fn(async () => [workspace]),
      toolsList: vi.fn(async () => []),
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

  it('renders the tools table and rolls back after the confirm dialog', async () => {
    const client = fakeApi({
      workspaceListFull: vi.fn(async () => [workspace]),
      toolsList: vi.fn(async () => [snapshot, older]),
      toolsRollback: vi.fn(async () => ({})),
    })
    seed()
    render(<WorkspacesRoute params={{}} client={client} />)

    // Digests render as short hashes; only the active snapshot is badged.
    expect(await screen.findByText('sha256deadbe')).toBeDefined()
    expect(screen.getByText('sha256feedfa')).toBeDefined()
    expect(screen.getByText('active')).toBeDefined()

    // Rollback is offered only for inactive snapshots, and asks first.
    fireEvent.click(screen.getByRole('button', { name: 'Rollback' }))
    expect(await screen.findByText('Roll back to sha256feedfa?')).toBeDefined()
    expect(client.toolsRollback).not.toHaveBeenCalled()

    fireEvent.click(
      within(screen.getByRole('dialog')).getByRole('button', { name: 'Rollback' }),
    )
    expect(client.toolsRollback).toHaveBeenCalledWith(
      { id: workspace.id },
      older.id,
    )
  })
})
