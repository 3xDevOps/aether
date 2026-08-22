import { fireEvent, render, screen, within } from '@testing-library/react'
import type { ToolSnapshot, Workspace } from '@/lib/types'
import { WorkspacesRoute } from '@/routes/workspaces'
import { useStore, type RootState } from '@/store'
import { alice, fakeApi, serverInfo, session } from '@/test/fixtures'

const workspace: Workspace = {
  id: 'wsp_1',
  name: 'team',
  created_at: '2026-08-14T09:00:00Z',
}

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
    sessions: { [session.id]: session },
    members: { [alice.id]: alice },
    info: serverInfo,
    capabilities: null,
    hydrated: true,
    hydrationError: null,
    route: { name: 'workspaces', params: {} },
    ...extra,
  })
}

// The remote-legacy null capability set claims every method, so the admin
// forms render; a desktop gateway narrows this via /capabilities.
describe('workspaces view', () => {
  it('submits the add form with a custom image environment', async () => {
    const client = fakeApi({
      workspaceListFull: vi.fn(async () => [workspace]),
      workspaceAdd: vi.fn(async () => workspace),
      toolsList: vi.fn(async () => []),
    })
    seed()
    render(<WorkspacesRoute params={{}} client={client} />)

    const form = within(await screen.findByRole('form', { name: 'Add workspace' }))
    fireEvent.change(form.getByLabelText(/^Name/), { target: { value: 'infra' } })
    fireEvent.change(form.getByLabelText(/Custom image/), {
      target: { value: 'ubuntu:24.04' },
    })
    fireEvent.click(form.getByRole('button', { name: 'Add' }))

    expect(client.workspaceAdd).toHaveBeenCalledWith({
      name: 'infra',
      environment: { custom_image: 'ubuntu:24.04' },
    })
  })

  it('sends the neutral image when the image field is empty', async () => {
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
      environment: { neutral_image: true },
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
