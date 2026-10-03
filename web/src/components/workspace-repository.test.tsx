import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { WorkspaceRepository } from '@/components/workspace-repository'
import type { GatewayCapabilities, WorkspaceMirrorResult } from '@/lib/types'
import { useStore, type RootState } from '@/store'
import { capability } from '@/store/hooks'
import { alice, bob, fakeApi, otherWorkspace, serverInfo, workspace } from '@/test/fixtures'

const localCaps: GatewayCapabilities = {
  gateway: 'local', methods: ['*'], ws: [],
  local: ['link.status', 'link.repo', 'repo.push', 'repo.fast-forward'],
}
const caps = capability(localCaps)
const linked = { server_configured: true, linked: true, addr: 'host:2222', user: 'alice', repo: '/src/repo' }
const connected = {
  link: 'repository-link', workspace: workspace.id, path: '/src/repo',
  remote: { repo: '/src/repo', remote: 'aether', url: `ssh://alice@host:2222/${workspace.id}` },
  push: null, fastForward: null,
}
const mirrored: WorkspaceMirrorResult = {
  enabled: true, status: 'ready', source_url: 'https://github.com/acme/project.git', branch: 'main',
}

function seed(extra: Partial<RootState> = {}) {
  useStore.setState({
    identityKey: 'server:alice', connectionEpoch: 0,
    info: serverInfo, members: { [alice.id]: alice, [bob.id]: bob },
    capabilities: localCaps, linkStatus: linked, onboardingRepo: connected,
    workspaces: { [workspace.id]: workspace, [otherWorkspace.id]: otherWorkspace },
    route: { name: 'workspace', params: { workspaceId: workspace.id } },
    ...extra,
  })
}

function expectNoPush() {
  expect(screen.queryByRole('button', { name: 'Push now' })).toBeNull()
  expect(screen.queryByLabelText('Push command')).toBeNull()
  expect(screen.queryByRole('button', { name: 'Copy resolve commands' })).toBeNull()
}

describe('workspace repository ownership', () => {
  it.each(['admin', 'collaborator'] as const)('allows %s linking without base pushes when source status is unavailable', async (role) => {
    const client = fakeApi()
    seed({ onboardingRepo: null, info: { ...serverInfo, member: { ...bob, role } } })
    const available = capability({ ...localCaps, methods: ['workspace.get'] })
    render(<WorkspaceRepository client={client} caps={available} workspace={workspace} initialLocal />)
    fireEvent.change(screen.getByLabelText('Repository path'), { target: { value: '/src/repo' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add remote' }))
    await screen.findByRole('button', { name: 'Use a different repository' })
    expect(client.localLinkRepo).toHaveBeenCalledWith('/src/repo', workspace.id)
    expect(client.workspaceMirrorStatus).not.toHaveBeenCalled()
    expectNoPush()
    expect(client.localRepoPush).not.toHaveBeenCalled()
  })

  it.each([
    { role: 'admin', enabled: false },
    { role: 'admin', enabled: true },
    { role: 'collaborator', enabled: false },
    { role: 'collaborator', enabled: true },
    { role: 'viewer', enabled: false },
    { role: 'viewer', enabled: true },
  ] as const)('withholds $role pushes until ownership resolves, then honors enabled=$enabled', async ({ role, enabled }) => {
    const pending = Promise.withResolvers<WorkspaceMirrorResult>()
    const client = fakeApi({ workspaceMirrorStatus: vi.fn(() => pending.promise) })
    seed({ info: { ...serverInfo, member: { ...bob, role } } })
    render(<WorkspaceRepository client={client} caps={caps} workspace={workspace} initialLocal />)
    expectNoPush()
    await act(async () => pending.resolve(enabled ? mirrored : { enabled: false }))
    expect(client.workspaceMirrorStatus).toHaveBeenCalledWith(workspace.id)
    if (role !== 'admin') {
      expect(screen.queryByRole('button', { name: /source mirror/ })).toBeNull()
      expect(screen.queryByRole('dialog')).toBeNull()
    }
    if (enabled || role === 'viewer') {
      expectNoPush()
      expect(screen.getByRole('button', { name: 'Use a different repository' })).toBeDefined()
      expect(client.localRepoPush).not.toHaveBeenCalled()
    } else {
      expect(screen.getByLabelText('Push command')).toHaveProperty('value', 'git push -u aether main')
      fireEvent.click(screen.getByRole('button', { name: 'Push now' }))
      await waitFor(() => expect(client.localRepoPush).toHaveBeenCalledWith(workspace.id))
    }
  })

  it.each(['admin', 'collaborator'] as const)('keeps %s pushes withheld when ownership lookup fails', async (role) => {
    const client = fakeApi({ workspaceMirrorStatus: vi.fn(async () => { throw new Error('source status unavailable') }) })
    seed({ info: { ...serverInfo, member: { ...bob, role } } })
    render(<WorkspaceRepository client={client} caps={caps} workspace={workspace} initialLocal />)
    await screen.findByRole('alert')
    expectNoPush()
    expect(screen.getByRole('button', { name: 'Use a different repository' })).toBeDefined()
  })

  it('removes and restores base pushes as source configuration and disable complete', async () => {
    let source: WorkspaceMirrorResult = { enabled: false }
    const client = fakeApi({
      workspaceMirrorStatus: vi.fn(async () => source),
      workspaceMirrorConfigure: vi.fn(async () => (source = mirrored)),
      workspaceMirrorDisable: vi.fn(async () => (source = { enabled: false })),
    })
    seed()
    render(<WorkspaceRepository client={client} caps={caps} workspace={workspace} initialLocal />)
    await screen.findByRole('button', { name: 'Push now' })
    fireEvent.click(screen.getByRole('button', { name: 'Set up source mirror' }))
    let dialog = within(await screen.findByRole('dialog'))
    fireEvent.change(await dialog.findByLabelText('Source URL'), { target: { value: mirrored.source_url } })
    fireEvent.click(dialog.getByRole('button', { name: 'Configure source' }))
    await dialog.findByRole('button', { name: 'Disable source' })
    fireEvent.click(dialog.getAllByRole('button', { name: 'Close' })[0])
    expectNoPush()
    fireEvent.click(screen.getByRole('button', { name: 'Review source mirror' }))
    dialog = within(await screen.findByRole('dialog'))
    fireEvent.click(await dialog.findByRole('button', { name: 'Disable source' }))
    fireEvent.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Disable source' }))
    await waitFor(() => expect(dialog.queryByRole('button', { name: 'Disable source' })).toBeNull())
    fireEvent.click(dialog.getAllByRole('button', { name: 'Close' })[0])
    expect(await screen.findByRole('button', { name: 'Push now' })).toBeDefined()
    expect(screen.getByLabelText('Push command')).toBeDefined()
    expect(client.localRepoPush).not.toHaveBeenCalled()
  })

  it.each([
    { role: 'admin', transition: 'workspace' },
    { role: 'admin', transition: 'identity' },
    { role: 'admin', transition: 'connection' },
    { role: 'collaborator', transition: 'workspace' },
    { role: 'collaborator', transition: 'identity' },
    { role: 'collaborator', transition: 'connection' },
  ] as const)('does not retain $role local ownership after a $transition transition', async ({ role, transition }) => {
    const pending = Promise.withResolvers<WorkspaceMirrorResult>()
    const client = fakeApi({ workspaceMirrorStatus: vi.fn().mockResolvedValueOnce({ enabled: false }).mockReturnValue(pending.promise) })
    seed({ info: { ...serverInfo, member: { ...bob, role } } })
    const view = render(<WorkspaceRepository client={client} caps={caps} workspace={workspace} initialLocal />)
    await screen.findByRole('button', { name: 'Push now' })
    if (transition === 'workspace') {
      act(() => useStore.setState({ onboardingRepo: { ...connected, workspace: otherWorkspace.id } }))
      view.rerender(<WorkspaceRepository client={client} caps={caps} workspace={otherWorkspace} initialLocal />)
    } else {
      act(() => useStore.setState(transition === 'identity' ? { identityKey: 'server:other-admin' } : { connectionEpoch: 1 }))
    }
    expectNoPush()
    if (screen.queryByLabelText('Repository path')) {
      fireEvent.change(screen.getByLabelText('Repository path'), { target: { value: '/src/repo' } })
      fireEvent.click(screen.getByRole('button', { name: 'Add remote' }))
    }
    await screen.findByRole('button', { name: 'Use a different repository' })
    expectNoPush()
    await act(async () => pending.resolve(mirrored))
    expectNoPush()
    expect(client.workspaceMirrorStatus).toHaveBeenLastCalledWith(transition === 'workspace' ? otherWorkspace.id : workspace.id)
    expect(client.localRepoPush).not.toHaveBeenCalled()
  })

  it('discards a former identity\'s late local-only answer after the new identity confirms a mirror', async () => {
    const pending = Promise.withResolvers<WorkspaceMirrorResult>()
    const client = fakeApi({ workspaceMirrorStatus: vi.fn().mockReturnValueOnce(pending.promise).mockResolvedValue(mirrored) })
    seed()
    render(<WorkspaceRepository client={client} caps={caps} workspace={workspace} initialLocal />)
    act(() => useStore.setState({ identityKey: 'server:other-admin' }))
    await screen.findByRole('button', { name: 'Review source mirror' })
    fireEvent.change(screen.getByLabelText('Repository path'), { target: { value: '/src/repo' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add remote' }))
    await screen.findByRole('button', { name: 'Use a different repository' })
    await act(async () => pending.resolve({ enabled: false }))
    expectNoPush()
    expect(screen.getByRole('button', { name: 'Review source mirror' })).toBeDefined()
  })

  it('removes diverged manual push instructions when source ownership becomes mirrored', async () => {
    const client = fakeApi({
      workspaceMirrorConfigure: vi.fn(async () => mirrored),
    })
    seed({ onboardingRepo: { ...connected, push: {
      state: 'diverged', branch: 'main', remote: 'aether', local_commit: 'abc', workspace_commit: 'def', ahead: 1, behind: 1, output: '',
    } } })
    render(<WorkspaceRepository client={client} caps={caps} workspace={workspace} initialLocal />)
    await screen.findByRole('button', { name: 'Copy resolve commands' })
    fireEvent.click(screen.getByRole('button', { name: 'Set up source mirror' }))
    const dialog = within(await screen.findByRole('dialog'))
    fireEvent.change(await dialog.findByLabelText('Source URL'), { target: { value: mirrored.source_url } })
    fireEvent.click(dialog.getByRole('button', { name: 'Configure source' }))
    await dialog.findByRole('button', { name: 'Disable source' })
    fireEvent.click(dialog.getAllByRole('button', { name: 'Close' })[0])
    expectNoPush()
  })

  it('does not send a push if source ownership changes during connection verification', async () => {
    const pending = Promise.withResolvers<typeof linked>()
    const client = fakeApi({
      localLinkStatus: vi.fn().mockResolvedValueOnce(linked).mockReturnValue(pending.promise),
      workspaceMirrorConfigure: vi.fn(async () => mirrored),
    })
    seed()
    render(<WorkspaceRepository client={client} caps={caps} workspace={workspace} initialLocal />)
    fireEvent.click(await screen.findByRole('button', { name: 'Push now' }))
    await waitFor(() => expect(client.localLinkStatus).toHaveBeenCalledTimes(2))
    fireEvent.click(screen.getByRole('button', { name: 'Set up source mirror' }))
    const dialog = within(await screen.findByRole('dialog'))
    fireEvent.change(await dialog.findByLabelText('Source URL'), { target: { value: mirrored.source_url } })
    fireEvent.click(dialog.getByRole('button', { name: 'Configure source' }))
    await dialog.findByRole('button', { name: 'Disable source' })
    fireEvent.click(dialog.getAllByRole('button', { name: 'Close' })[0])
    await act(async () => pending.resolve(linked))
    expectNoPush()
    expect(client.localRepoPush).not.toHaveBeenCalled()
  })

  it.each([
    { role: 'admin', ownership: 'local' },
    { role: 'admin', ownership: 'mirrored' },
    { role: 'admin', ownership: 'unknown' },
    { role: 'collaborator', ownership: 'local' },
    { role: 'collaborator', ownership: 'mirrored' },
    { role: 'collaborator', ownership: 'unknown' },
    { role: 'viewer', ownership: 'local' },
  ] as const)('provides safe hosted $ownership clone instructions for $role', async ({ role, ownership }) => {
    const client = fakeApi({ workspaceMirrorStatus: vi.fn(async () => ownership === 'mirrored' ? mirrored : { enabled: false }) })
    const hostedCaps = capability({ gateway: 'remote', methods: ownership === 'unknown' ? ['workspace.get'] : ['*'], ws: [] })
    const selected = { ...workspace, id: 'workspace with spaces', base_branch: 'release/trunk' }
    seed({ linkStatus: null, info: { ...serverInfo, member: { ...bob, role } } })
    render(<WorkspaceRepository client={client} caps={hostedCaps} workspace={selected} initialLocal />)
    if (ownership !== 'unknown') await waitFor(() => expect(client.workspaceMirrorStatus).toHaveBeenCalledWith(selected.id))
    if (ownership === 'mirrored') await screen.findByText('Source mirror configured.')
    const instructions = screen.getByRole('region', { name: 'Local repository' })
    await waitFor(() => {
      const commands = instructions.querySelector('pre')!.textContent!
      expect(commands).toMatch(/^aether link '[^']+' --repo \/absolute\/path\/to\/clone --workspace 'workspace with spaces'/)
      if (ownership === 'local' && role !== 'viewer') {
        expect(commands.split(' &&\n')).toHaveLength(2)
        expect(commands.split(' &&\n')[1]).toBe('git -C /absolute/path/to/clone push -u aether release/trunk')
      } else {
        expect(commands).not.toMatch(/(?:^|\n)git .*push/)
      }
    })
    expect(client.localLinkRepo).not.toHaveBeenCalled()
    expect(client.localLinkStatus).not.toHaveBeenCalled()
  })
})
