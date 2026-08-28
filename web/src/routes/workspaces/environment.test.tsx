import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import type { EnvStatusResult } from '@/lib/types'
import { WorkspaceView } from '@/routes/workspace'
import { EnvironmentPanel } from '@/routes/workspaces/environment'
import { useStore, type RootState } from '@/store'
import {
  alice,
  envVersion,
  fakeApi,
  manifestItem,
  workspace,
} from '@/test/fixtures'

// Three versions newest first: a failed proposal, the active one, and the
// standard environment it started from. Rollback's target is version 1,
// the newest good version below the active one.
const history: EnvStatusResult = {
  versions: [
    envVersion({
      version: 3,
      status: 'failed',
      failure_detail: 'go was not found in the built image',
      active: false,
      created_at: '2026-08-14T11:00:00Z',
    }),
    envVersion({
      version: 2,
      status: 'active',
      active: true,
      manifest: [
        manifestItem(),
        manifestItem({
          name: 'go',
          version: '1.24',
          reason: 'the server is written in go',
          check_command: 'go version',
        }),
      ],
      created_at: '2026-08-14T10:00:00Z',
    }),
    envVersion({
      version: 1,
      status: 'saved',
      source: 'standard',
      harness: undefined,
      active: false,
      created_at: '2026-08-14T09:00:00Z',
    }),
  ],
  active_version: 2,
}

function seed(extra: Partial<RootState> = {}) {
  useStore.setState({
    workspaces: { [workspace.id]: workspace },
    members: { [alice.id]: alice },
    runs: {},
    envBuilds: {},
    capabilities: { gateway: 'remote', methods: ['*'], ws: ['events'] },
    route: { name: 'workspace', params: { workspaceId: workspace.id } },
    ...extra,
  })
}

describe('environment panel', () => {
  it('renders the active manifest and the version history', async () => {
    const client = fakeApi({ envStatus: vi.fn(async () => history) })
    seed()
    render(<EnvironmentPanel workspaceID={workspace.id} client={client} />)

    // The active version's manifest as a plain list: name, version, reason.
    expect(await screen.findByText('jq')).toBeDefined()
    expect(screen.getByText('1.7.1')).toBeDefined()
    expect(screen.getByText('used by the project scripts')).toBeDefined()
    expect(screen.getByText('go')).toBeDefined()

    // Which path made it and with which agent, in one plain sentence.
    expect(
      screen.getByText(/mirrored from a machine with Claude Code/),
    ).toBeDefined()

    // The compact history: every version, its status, and the failure
    // detail on the failed row.
    const table = within(screen.getByRole('table', { name: 'Version history' }))
    expect(table.getByText('v3')).toBeDefined()
    expect(table.getByText('v2')).toBeDefined()
    expect(table.getByText('v1')).toBeDefined()
    expect(table.getByText('failed')).toBeDefined()
    expect(table.getByText('go was not found in the built image')).toBeDefined()
  })

  it('rolls back after a confirm that names the target version', async () => {
    const envStatus = vi.fn(async () => history)
    const client = fakeApi({ envStatus, envRollback: vi.fn(async () => 1) })
    seed()
    render(<EnvironmentPanel workspaceID={workspace.id} client={client} />)

    fireEvent.click(await screen.findByRole('button', { name: 'Rollback' }))
    expect(await screen.findByText('Roll back to version 1?')).toBeDefined()
    expect(client.envRollback).not.toHaveBeenCalled()

    fireEvent.click(
      within(screen.getByRole('dialog')).getByRole('button', {
        name: 'Rollback',
      }),
    )
    expect(client.envRollback).toHaveBeenCalledWith({ id: workspace.id })
    // The panel re-reads history so the reactivated version shows up.
    await waitFor(() => expect(envStatus).toHaveBeenCalledTimes(2))
  })

  it('shows one calm sentence when there is no environment history', async () => {
    const client = fakeApi({
      envStatus: vi.fn(async () => ({ versions: [] })),
    })
    seed()
    render(<EnvironmentPanel workspaceID={workspace.id} client={client} />)

    expect(
      await screen.findByText(/uses the image it was created with/),
    ).toBeDefined()
    expect(screen.queryByRole('button', { name: 'Rollback' })).toBeNull()
  })

  it('is absent from the workspace view without env.status', async () => {
    const client = fakeApi({ envStatus: vi.fn(async () => history) })
    seed({ capabilities: { gateway: 'remote', methods: [], ws: [] } })
    const view = render(
      <WorkspaceView
        params={{ workspaceId: workspace.id }}
        client={client}
      />,
    )
    expect(screen.queryByRole('region', { name: 'Environment' })).toBeNull()
    expect(client.envStatus).not.toHaveBeenCalled()
    view.unmount()

    seed()
    render(
      <WorkspaceView
        params={{ workspaceId: workspace.id }}
        client={client}
      />,
    )
    expect(
      await screen.findByRole('region', { name: 'Environment' }),
    ).toBeDefined()
  })
})
