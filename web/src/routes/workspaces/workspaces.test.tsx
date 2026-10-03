import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { WorkspacesRoute } from '@/routes/workspaces'
import { useStore, type RootState } from '@/store'
import { applyEvent } from '@/store/sync'
import { alice, bob, fakeApi, otherWorkspace, serverInfo, vera, workspace } from '@/test/fixtures'

function seed(extra: Partial<RootState> = {}) {
  useStore.setState({
    workspaces: { [workspace.id]: workspace },
    deletedWorkspaceIDs: new Set(),
    lastSeq: 0,
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

  it('keeps additional remote import available and retains a partially created workspace', async () => {
    const client = fakeApi({
      workspaceImport: vi.fn(async () => ({
        created: true, workspace: otherWorkspace,
        mirror: { enabled: true, status: 'pending' as const },
        error: 'source authentication not installed',
      })),
    })
    seed()
    render(<WorkspacesRoute params={{}} client={client} />)
    await screen.findByRole('list', { name: 'Workspace list' })
    fireEvent.click(screen.getByRole('button', { name: 'Import repository' }))
    const dialog = within(await screen.findByRole('dialog'))
    fireEvent.change(dialog.getByLabelText('Workspace name'), { target: { value: 'private-project' } })
    fireEvent.change(dialog.getByLabelText('Source URL'), { target: { value: 'ssh://git@example.test/team/project.git' } })
    fireEvent.change(dialog.getByLabelText('Source authentication'), { target: { value: 'deploy-key' } })
    fireEvent.click(dialog.getByRole('button', { name: 'Import repository' }))
    await dialog.findByText('source authentication not installed')
    expect(dialog.queryByRole('button', { name: 'Import repository' })).toBeNull()
    fireEvent.click(dialog.getAllByRole('button', { name: 'Close' })[0])
    await waitFor(() => expect(useStore.getState().route).toEqual({ name: 'workspace', params: { workspaceId: otherWorkspace.id, repository: 'remote' } }))
    expect(client.workspaceImport).toHaveBeenCalledTimes(1)
    expect(client.workspaceMirrorAdopt).not.toHaveBeenCalled()
  })

  it('never creates a hosted local workspace or opens a filesystem picker', async () => {
    const client = fakeApi()
    seed()
    render(<WorkspacesRoute params={{}} client={client} />)
    fireEvent.click(screen.getByRole('button', { name: 'Create from local clone' }))
    expect(screen.queryByLabelText('Repository path')).toBeNull()
    expect(screen.queryByRole('button', { name: 'Choose folder' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Create workspace' })).toBeNull()
    expect(client.workspaceAdd).not.toHaveBeenCalled()
    expect(client.localLinkRepo).not.toHaveBeenCalled()
    const commands = screen.getByRole('region', { name: 'Add workspace' }).querySelector('pre')!.textContent!.split(' &&\n')
    expect(commands).toHaveLength(4)
    const target = commands[0].match(/^aether link ('[^']+'|\S+)$/)?.[1]
    expect(target).toBeDefined()
    expect(target).not.toMatch(/^--/)
    expect(commands[1]).toBe('aether workspace add myproject --base main')
    expect(commands[2]).toBe(`aether link ${target} --repo /absolute/path/to/clone --workspace myproject`)
    expect(commands[3]).toBe('git -C /absolute/path/to/clone push -u aether main')
  })

  it.each(['unmount', 'identity', 'connection'] as const)(
    'ignores a late creation response after %s without replaying creation',
    async (transition) => {
      const createdWorkspace = { ...workspace, id: 'wsp_pending', name: 'pending-repo' }
      const pending = Promise.withResolvers<typeof createdWorkspace>()
      const client = fakeApi({ workspaceAdd: vi.fn(() => pending.promise) })
      seed({
        identityKey: 'server:alice', connectionEpoch: 0,
        capabilities: { gateway: 'local', methods: ['*'], ws: [], local: ['link.repo'] },
      })
      const view = render(<WorkspacesRoute params={{}} client={client} />)
      await screen.findByRole('list', { name: 'Workspace list' })
      fireEvent.click(screen.getByRole('button', { name: 'Create from local clone' }))
      fireEvent.change(screen.getByLabelText('Workspace name'), { target: { value: createdWorkspace.name } })
      fireEvent.click(screen.getByRole('button', { name: 'Create workspace' }))
      await waitFor(() => expect(client.workspaceAdd).toHaveBeenCalledTimes(1))
      if (transition === 'unmount') {
        act(() => useStore.getState().navigate('agents'))
        view.unmount()
      } else {
        act(() => useStore.setState(transition === 'identity' ? { identityKey: 'server:bob' } : { connectionEpoch: 1 }))
      }
      const route = useStore.getState().route
      await act(async () => pending.resolve(createdWorkspace))
      expect(useStore.getState().route).toEqual(route)
      expect(useStore.getState().workspaces[createdWorkspace.id]).toBeUndefined()
      expect(client.workspaceAdd).toHaveBeenCalledTimes(1)
      if (transition !== 'unmount') {
        expect(screen.getByRole('button', { name: 'Create from local clone' })).toHaveProperty('disabled', false)
        view.unmount()
      }
    },
  )

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

  it.each([bob, vera])('does not offer deletion to $role members', async (member) => {
    seed({ info: { ...serverInfo, member } })
    render(<WorkspacesRoute params={{}} client={fakeApi()} />)

    await screen.findByText(otherWorkspace.name)
    expect(screen.queryByRole('button', { name: 'Delete' })).toBeNull()
  })

  it('does not offer deletion when the gateway lacks the method', async () => {
    seed({ capabilities: { gateway: 'remote', methods: ['workspace.list'], ws: [] } })
    render(<WorkspacesRoute params={{}} client={fakeApi()} />)

    await screen.findByText(otherWorkspace.name)
    expect(screen.queryByRole('button', { name: 'Delete' })).toBeNull()
  })

  it('cancels without deleting and reflects subsequent live workspace changes', async () => {
    const client = fakeApi()
    seed()
    render(<WorkspacesRoute params={{}} client={client} />)
    await screen.findByText(otherWorkspace.name)

    const row = screen.getByText(workspace.name).closest('li')!
    fireEvent.click(within(row).getByRole('button', { name: 'Delete' }))
    const dialog = within(await screen.findByRole('alertdialog'))
    expect(dialog.getByRole('heading', { name: `Delete ${workspace.name}?` })).toBeDefined()
    fireEvent.click(dialog.getByRole('button', { name: 'Cancel' }))
    expect(client.workspaceDelete).not.toHaveBeenCalled()
    expect(screen.queryByRole('alertdialog')).toBeNull()

    act(() => useStore.getState().setWorkspaces([otherWorkspace]))
    expect(screen.queryByText(workspace.name)).toBeNull()
    expect(screen.getByText(otherWorkspace.name)).toBeDefined()
  })

  it('keeps a refusal visible and allows deletion to be retried', async () => {
    const refusal = 'workspace has active runs; close them before deleting'
    const client = fakeApi({
      workspaceListFull: vi.fn().mockResolvedValueOnce([workspace]).mockResolvedValue([]),
      workspaceDelete: vi.fn()
        .mockRejectedValueOnce(new Error(refusal))
        .mockResolvedValue({ ok: true }),
    })
    seed({ activeWorkspace: workspace.id })
    render(<WorkspacesRoute params={{}} client={client} />)
    fireEvent.click(await screen.findByRole('button', { name: 'Delete' }))
    const dialog = within(await screen.findByRole('alertdialog'))

    fireEvent.click(dialog.getByRole('button', { name: 'Delete workspace' }))
    expect((await dialog.findByRole('alert')).textContent).toBe(refusal)
    expect(useStore.getState().workspaces[workspace.id]).toEqual(workspace)
    fireEvent.click(dialog.getByRole('button', { name: 'Delete workspace' }))

    await screen.findByText('No workspaces yet.')
    expect(screen.queryByRole('alertdialog')).toBeNull()
    expect(useStore.getState().activeWorkspace).toBe('')
    expect(client.workspaceDelete).toHaveBeenNthCalledWith(2, workspace.id)
  })

  it('reconciles the selected workspace after confirmed deletion', async () => {
    const client = fakeApi({
      workspaceListFull: vi.fn()
        .mockResolvedValueOnce([workspace, otherWorkspace])
        .mockResolvedValue([otherWorkspace]),
    })
    seed({ activeWorkspace: workspace.id })
    render(<WorkspacesRoute params={{}} client={client} />)
    await screen.findByText(otherWorkspace.name)
    const row = screen.getByText(workspace.name).closest('li')!
    fireEvent.click(within(row).getByRole('button', { name: 'Delete' }))
    const dialog = within(await screen.findByRole('alertdialog'))
    expect(client.workspaceDelete).not.toHaveBeenCalled()
    fireEvent.click(dialog.getByRole('button', { name: 'Delete workspace' }))

    await waitFor(() => expect(screen.queryByText(workspace.name)).toBeNull())
    expect(useStore.getState().activeWorkspace).toBe(otherWorkspace.id)
    expect(useStore.getState().workspaces).toEqual({ [otherWorkspace.id]: otherWorkspace })
    expect(screen.getByText(otherWorkspace.name)).toBeDefined()
  })

  it('does not restore a deleted target when refreshing the list fails', async () => {
    const client = fakeApi({
      workspaceListFull: vi.fn()
        .mockResolvedValueOnce([workspace])
        .mockRejectedValueOnce(new Error('workspace list unavailable'))
        .mockResolvedValue([otherWorkspace]),
    })
    seed({ activeWorkspace: workspace.id })
    render(<WorkspacesRoute params={{}} client={client} />)
    fireEvent.click(await screen.findByRole('button', { name: 'Delete' }))
    const dialog = within(await screen.findByRole('alertdialog'))
    fireEvent.click(dialog.getByRole('button', { name: 'Delete workspace' }))

    expect((await screen.findByRole('alert')).textContent).toContain('workspace list unavailable')
    expect(screen.queryByRole('alertdialog')).toBeNull()
    expect(useStore.getState().activeWorkspace).toBe('')
    expect(screen.queryByText(workspace.name)).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    await screen.findByText(otherWorkspace.name)
    expect(useStore.getState().activeWorkspace).toBe(otherWorkspace.id)
  })
  it('ignores a list request started before the workspace was deleted', async () => {
    let resolveInitial!: (value: typeof workspace[]) => void
    const initial = new Promise<typeof workspace[]>((resolve) => {
      resolveInitial = resolve
    })
    const client = fakeApi({
      workspaceListFull: vi.fn()
        .mockReturnValueOnce(initial)
        .mockResolvedValue([otherWorkspace]),
    })
    seed({ activeWorkspace: workspace.id })
    render(<WorkspacesRoute params={{}} client={client} />)
    fireEvent.click(screen.getByRole('button', { name: 'Delete' }))
    const dialog = within(await screen.findByRole('alertdialog'))
    fireEvent.click(dialog.getByRole('button', { name: 'Delete workspace' }))
    await screen.findByText(otherWorkspace.name)

    await act(async () => resolveInitial([workspace, otherWorkspace]))
    expect(screen.queryByText(workspace.name)).toBeNull()
    expect(useStore.getState().activeWorkspace).toBe(otherWorkspace.id)
  })

  it.each([
    { label: 'another workspace remains', remaining: [otherWorkspace] },
    { label: 'the last workspace is deleted', remaining: [] },
  ])('rejects a pending route snapshot after remote deletion when $label', async ({ remaining }) => {
    const initial = Promise.withResolvers<typeof workspace[]>()
    const refresh = Promise.withResolvers<typeof workspace[]>()
    const client = fakeApi({
      workspaceListFull: vi.fn()
        .mockReturnValueOnce(initial.promise)
        .mockReturnValueOnce(refresh.promise),
    })
    seed({
      workspaces: Object.fromEntries([workspace, ...remaining].map((w) => [w.id, w])),
      activeWorkspace: workspace.id,
      route: { name: 'workspace', params: { workspaceId: workspace.id } },
    })
    render(<WorkspacesRoute params={{}} client={client} />)
    let deletion!: Promise<boolean>
    act(() => {
      deletion = applyEvent(useStore, {
        id: 'evt_delete', seq: 1, time: '2026-08-14T11:00:00Z',
        workspace_id: workspace.id, run_id: '', actor_id: '',
        type: 'workspace.deleted', payload: {},
      }, client)
    })

    const expectedRoute = remaining.length
      ? { name: 'workspace', params: { workspaceId: otherWorkspace.id } }
      : { name: 'workspaces', params: {} }
    expect(screen.queryByText(workspace.name)).toBeNull()
    expect(useStore.getState().route).toEqual(expectedRoute)

    // The route request started before deletion and resolves while the
    // event's reconciliation is still pending.
    await act(async () => initial.resolve([workspace, ...remaining]))
    expect(screen.queryByText(workspace.name)).toBeNull()
    expect(useStore.getState().activeWorkspace).toBe(remaining[0]?.id ?? '')
    expect(useStore.getState().route).toEqual(expectedRoute)

    // Even the event's own refresh may carry a stale snapshot.
    await act(async () => {
      refresh.resolve([workspace, ...remaining])
      expect(await deletion).toBe(true)
    })
    expect(useStore.getState().workspaces).toEqual(
      Object.fromEntries(remaining.map((w) => [w.id, w])),
    )
    expect(useStore.getState().activeWorkspace).toBe(remaining[0]?.id ?? '')
    expect(useStore.getState().route).toEqual(expectedRoute)
    expect(screen.queryByText(workspace.name)).toBeNull()
    if (remaining.length) expect(screen.getByText(otherWorkspace.name)).toBeDefined()
    else expect(screen.getByText('No workspaces yet.')).toBeDefined()
  })
})
