import { act, fireEvent, render, screen, within } from '@testing-library/react'
import { ApiError } from '@/lib/api'
import type { GitHubRepository, GitHubRepositoryListResult, WorkspaceImportResult, WorkspaceMirrorResult } from '@/lib/types'
import { GitHubRepositoryDialog } from '@/routes/admin-dialogs/github-repository-dialog'
import { useStore } from '@/store'
import { bob, fakeApi, serverInfo, workspace } from '@/test/fixtures'

const revision = 'abc123abc123abc123abc123abc123abc123abc1234'
const repository: GitHubRepository = { id: 9, full_name: 'team/private-repo', name: 'private-repo', private: true, default_branch: 'trunk', clone_url: 'https://github.com/team/private-repo.git', can_push: false }
const page: GitHubRepositoryListResult = { account: { id: 7, login: 'alice-gh' }, repositories: [repository] }
const observed: WorkspaceMirrorResult = { enabled: true, auth: 'github', branch: 'trunk', generation: 8, observed_commit: revision, status: 'pending' }
const imported: WorkspaceImportResult = { created: true, workspace: { ...workspace, name: 'private-repo', base_branch: 'trunk' }, mirror: observed }

function clientWith(over: Parameters<typeof fakeApi>[0] = {}) {
  return fakeApi({
    githubOAuthStatus: vi.fn(async () => ({ state: 'connected' as const, login: page.account.login })),
    githubRepositories: vi.fn(async () => page),
    workspaceImport: vi.fn(async () => imported),
    workspaceMirrorAdopt: vi.fn(async () => ({ ...observed, status: 'ready' as const, accepted_commit: revision })),
    ...over,
  })
}

async function select() {
  fireEvent.click(await screen.findByRole('button', { name: 'team/private-repo · Private' }))
}
async function review() {
  await select()
  fireEvent.click(screen.getByRole('button', { name: 'Review repository' }))
  return screen.findByRole('region', { name: 'Review repository revision' })
}

beforeEach(() => {
  useStore.setState({ info: serverInfo, identityKey: 'server:alice', connectionEpoch: 0, workspaces: {}, deletedWorkspaceIDs: new Set() })
})

describe('GitHub repository onboarding', () => {
  it('reviews the actual default branch and exact revision, then accepts it before completing', async () => {
    const client = clientWith()
    const onCreated = vi.fn()
    render(<GitHubRepositoryDialog client={client} onCreated={onCreated} onClose={vi.fn()} />)
    await select()
    expect(screen.getByRole('region', { name: 'Selected repository' }).textContent).toContain('Branch: trunk')
    expect(client.workspaceImport).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'Review repository' }))
    const outcome = await screen.findByRole('region', { name: 'Review repository revision' })
    expect(outcome.textContent).toContain(revision)
    expect(outcome.textContent).toContain('Use trunk for new runs.')
    expect(onCreated).not.toHaveBeenCalled()
    expect(client.workspaceMirrorAdopt).not.toHaveBeenCalled()
    expect(client.workspaceImport).toHaveBeenCalledWith(expect.objectContaining({ base_branch: 'trunk', auth: 'github', github_account_id: 7, origin: '' }))
    fireEvent.click(screen.getByRole('button', { name: 'Use repository' }))
    await screen.findByText(/Accepted revision:/)
    expect(client.workspaceMirrorAdopt).toHaveBeenCalledExactlyOnceWith(workspace.id, 8)
    expect(onCreated).toHaveBeenCalledExactlyOnceWith(imported.workspace)
  })

  it('loads accessible pages and keeps a writable fork an explicit publishing choice', async () => {
    const fork = { ...repository, id: 10, full_name: 'alice/fork', name: 'fork', clone_url: 'https://github.com/alice/fork.git', can_push: true }
    const client = clientWith({ githubRepositories: vi.fn().mockResolvedValueOnce({ ...page, next_page: 2 }).mockResolvedValueOnce({ ...page, repositories: [fork] }) })
    render(<GitHubRepositoryDialog client={client} onCreated={vi.fn()} onClose={vi.fn()} />)
    await select()
    fireEvent.change(screen.getByLabelText('Find a repository'), { target: { value: 'fork' } })
    expect(screen.getByText('No loaded repositories match. Load more or change the filter.')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'Load more' }))
    expect(await screen.findByRole('button', { name: 'alice/fork · Private' })).toBeTruthy()
    fireEvent.click(screen.getByText('Advanced'))
    const destination = screen.getByLabelText<HTMLSelectElement>('Publish destination (Origin)')
    expect(destination.value).toBe('')
    expect(within(destination).queryByRole('option', { name: repository.full_name })).toBeNull()
    fireEvent.change(destination, { target: { value: fork.clone_url } })
    fireEvent.change(screen.getByLabelText('Workspace name'), { target: { value: 'review-project' } })
    fireEvent.change(screen.getByLabelText('Base branch'), { target: { value: 'release' } })
    fireEvent.click(screen.getByRole('button', { name: 'Review repository' }))
    await screen.findByRole('region', { name: 'Review repository revision' })
    expect(client.workspaceImport).toHaveBeenCalledWith(expect.objectContaining({ name: 'review-project', base_branch: 'release', source_url: repository.clone_url, origin: fork.clone_url }))
  })

  it('reconnects an auth-failed import locally and accepts the retained workspace without creating a duplicate', async () => {
    const failed: WorkspaceMirrorResult = { ...observed, source_url: repository.clone_url, observed_commit: undefined, status: 'auth-failed', last_error: 'GitHub authorization revoked' }
    const recovered = { ...observed, generation: 9 }
    const client = clientWith({
      githubOAuthStatus: vi.fn().mockResolvedValueOnce({ state: 'connected', login: page.account.login }).mockResolvedValue({ state: 'disconnected' }),
      githubOAuthStart: vi.fn(async () => ({ state: 'connected' as const, login: page.account.login })),
      workspaceImport: vi.fn(async () => ({ ...imported, mirror: failed, error: failed.last_error })),
      workspaceMirrorStatus: vi.fn(async () => failed),
      workspaceMirrorRefresh: vi.fn(async () => recovered),
      workspaceMirrorAdopt: vi.fn(async () => ({ ...recovered, status: 'ready' as const, accepted_commit: revision })),
    })
    const onCreated = vi.fn()
    render(<GitHubRepositoryDialog client={client} onCreated={onCreated} onClose={vi.fn()} />)
    await review()
    expect(screen.getByRole('alert').textContent).toBe('GitHub authorization revoked')
    expect(useStore.getState().workspaces[workspace.id]).toEqual(imported.workspace)
    expect(screen.getByRole('button', { name: 'Use repository' })).toHaveProperty('disabled', true)
    fireEvent.click(screen.getByRole('button', { name: 'Source settings' }))
    expect(await screen.findByRole('heading', { name: 'Workspace Source' })).toBeTruthy()
    await screen.findByText('GitHub is not connected.')
    fireEvent.click(screen.getByRole('button', { name: 'Connect GitHub' }))
    await screen.findByText(`Connected as ${page.account.login}`)
    expect(client.workspaceMirrorConfigure).not.toHaveBeenCalled()
    expect(client.workspaceMirrorAdopt).not.toHaveBeenCalled()
    fireEvent.click(screen.getAllByRole('button', { name: 'Close' })[0])
    await screen.findByRole('region', { name: 'Review repository revision' })
    await act(async () => {})
    expect(client.workspaceMirrorStatus).toHaveBeenNthCalledWith(1, workspace.id)
    expect(client.workspaceMirrorStatus).toHaveBeenNthCalledWith(2, workspace.id)
    fireEvent.click(screen.getByRole('button', { name: 'Retry fetch' }))
    expect(await screen.findByText(revision)).toBeTruthy()
    expect(onCreated).not.toHaveBeenCalled()
    expect(client.workspaceMirrorAdopt).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'Use repository' }))
    await screen.findByText(/Accepted revision:/)
    expect(onCreated).toHaveBeenCalledExactlyOnceWith(imported.workspace)
    expect(client.workspaceImport).toHaveBeenCalledTimes(1)
    expect(client.workspaceMirrorRefresh).toHaveBeenCalledExactlyOnceWith(workspace.id)
    expect(client.workspaceMirrorAdopt).toHaveBeenCalledExactlyOnceWith(workspace.id, recovered.generation)
  })

  it('does not replay creation after a lost import response', async () => {
    const client = clientWith({ workspaceImport: vi.fn(async () => { throw new Error('network connection lost') }) })
    const onCreated = vi.fn()
    render(<GitHubRepositoryDialog client={client} onCreated={onCreated} onClose={vi.fn()} />)
    await select()
    fireEvent.click(screen.getByRole('button', { name: 'Review repository' }))
    await screen.findByText('network connection lost')
    expect(screen.getByText(/did not establish whether a workspace was created/)).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Review repository' })).toHaveProperty('disabled', true)
    fireEvent.click(screen.getByRole('button', { name: 'Review repository' }))
    expect(client.workspaceImport).toHaveBeenCalledTimes(1)
    expect(onCreated).not.toHaveBeenCalled()
  })

  it('allows a corrected import after a definite precreation account refusal', async () => {
    const client = clientWith({ workspaceImport: vi.fn().mockRejectedValueOnce(new ApiError(409, 'GitHub account changed; reload repositories', -32002)).mockResolvedValueOnce(imported) })
    render(<GitHubRepositoryDialog client={client} onCreated={vi.fn()} onClose={vi.fn()} />)
    await select()
    fireEvent.click(screen.getByRole('button', { name: 'Review repository' }))
    await screen.findByText('GitHub account changed; reload repositories')
    expect(screen.getByRole('button', { name: 'Review repository' })).toHaveProperty('disabled', false)
    fireEvent.click(screen.getByRole('button', { name: 'Reload repositories' }))
    await select()
    fireEvent.click(screen.getByRole('button', { name: 'Review repository' }))
    expect(await screen.findByText(revision)).toBeTruthy()
  })

  it('discards private repository pages from the previous member', async () => {
    const old = Promise.withResolvers<GitHubRepositoryListResult>()
    const client = clientWith({ githubRepositories: vi.fn(() => old.promise) })
    render(<GitHubRepositoryDialog client={client} onCreated={vi.fn()} onClose={vi.fn()} />)
    await screen.findByText('Loading repositories…')
    act(() => useStore.setState({ info: { ...serverInfo, member: bob }, identityKey: 'server:bob' }))
    await act(async () => { old.resolve(page) })
    expect(screen.queryByText('team/private-repo', { exact: false })).toBeNull()
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('does not advance onboarding when an old server completes adoption', async () => {
    const old = Promise.withResolvers<WorkspaceMirrorResult>()
    const client = clientWith({ workspaceMirrorAdopt: vi.fn(() => old.promise) })
    const onCreated = vi.fn()
    render(<GitHubRepositoryDialog client={client} onCreated={onCreated} onClose={vi.fn()} />)
    await review()
    fireEvent.click(screen.getByRole('button', { name: 'Use repository' }))
    act(() => useStore.setState({ identityKey: 'another-server:alice', workspaces: {} }))
    await act(async () => { old.resolve({ ...observed, status: 'ready', accepted_commit: revision }) })
    expect(onCreated).not.toHaveBeenCalled()
    expect(screen.queryByRole('region', { name: 'Review repository revision' })).toBeNull()
    expect(useStore.getState().workspaces).toEqual({})
  })

  it('reconciles a lost adoption response by reading status, without automatically adopting again', async () => {
    const client = clientWith({ workspaceMirrorAdopt: vi.fn(async () => { throw new Error('adoption response lost') }), workspaceMirrorStatus: vi.fn(async () => ({ ...observed, status: 'ready' as const, accepted_commit: revision })) })
    const onCreated = vi.fn()
    render(<GitHubRepositoryDialog client={client} onCreated={onCreated} onClose={vi.fn()} />)
    await review()
    fireEvent.click(screen.getByRole('button', { name: 'Use repository' }))
    await screen.findByText('adoption response lost')
    expect(screen.getByRole('button', { name: 'Use repository' })).toHaveProperty('disabled', true)
    expect(onCreated).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'Check source status' }))
    await screen.findByText(/Accepted revision:/)
    expect(onCreated).toHaveBeenCalledExactlyOnceWith(imported.workspace)
    expect(client.workspaceMirrorAdopt).toHaveBeenCalledTimes(1)
  })

  it('shows a stale revision error rather than claiming the repository is ready', async () => {
    const client = clientWith({ workspaceMirrorAdopt: vi.fn(async () => { throw new ApiError(409, 'source generation changed; review the latest revision', -32000) }), workspaceMirrorStatus: vi.fn(async () => ({ ...observed, generation: 9, observed_commit: 'new-revision' })) })
    const onCreated = vi.fn()
    render(<GitHubRepositoryDialog client={client} onCreated={onCreated} onClose={vi.fn()} />)
    await review()
    fireEvent.click(screen.getByRole('button', { name: 'Use repository' }))
    await screen.findByText('source generation changed; review the latest revision')
    fireEvent.click(screen.getByRole('button', { name: 'Check source status' }))
    expect(await screen.findByText('new-revision')).toBeTruthy()
    expect(onCreated).not.toHaveBeenCalled()
    expect(screen.getByRole('button', { name: 'Use repository' })).toHaveProperty('disabled', false)
  })

  it.each(['identity', 'connection', 'client', 'unmount'] as const)('discards a late import after a %s change', async (change) => {
    const old = Promise.withResolvers<WorkspaceImportResult>()
    const client = clientWith({ workspaceImport: vi.fn(() => old.promise) })
    const onCreated = vi.fn()
    const onClose = vi.fn()
    const view = render(<GitHubRepositoryDialog client={client} onCreated={onCreated} onClose={onClose} />)
    await select()
    fireEvent.click(screen.getByRole('button', { name: 'Review repository' }))
    if (change === 'unmount') view.unmount()
    else if (change === 'client') view.rerender(<GitHubRepositoryDialog client={clientWith()} onCreated={onCreated} onClose={onClose} />)
    else act(() => {
      useStore.setState(change === 'identity' ? { identityKey: 'server:bob' } : { connectionEpoch: 1 })
      if (change === 'identity') useStore.setState({ identityKey: 'server:alice' })
    })
    await act(async () => { old.resolve(imported) })
    expect(screen.queryByRole('region', { name: 'Review repository revision' })).toBeNull()
    expect(useStore.getState().workspaces).toEqual({})
    expect(onCreated).not.toHaveBeenCalled()
  })

  it('does not import an empty repository with no branch', async () => {
    const client = clientWith({ githubRepositories: vi.fn(async () => ({ ...page, repositories: [{ ...repository, default_branch: '' }] })) })
    render(<GitHubRepositoryDialog client={client} onCreated={vi.fn()} onClose={vi.fn()} />)
    await select()
    expect(screen.getByText(/Add an initial commit on GitHub/)).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Review repository' })).toHaveProperty('disabled', true)
  })

  it('rejects account changes across repository pages instead of mixing permissions', async () => {
    const client = clientWith({ githubRepositories: vi.fn().mockResolvedValueOnce({ ...page, next_page: 2 }).mockResolvedValueOnce({ account: { id: 99, login: 'other' }, repositories: [repository] }) })
    render(<GitHubRepositoryDialog client={client} onCreated={vi.fn()} onClose={vi.fn()} />)
    await select()
    fireEvent.click(screen.getByRole('button', { name: 'Load more' }))
    await screen.findByText(/The GitHub account changed/)
    expect(screen.queryByRole('region', { name: 'Selected repository' })).toBeNull()
    expect(screen.getByRole('button', { name: 'Review repository' })).toHaveProperty('disabled', true)
  })

  it('hides the picker and makes no administrative request for nonadmins', () => {
    useStore.setState({ info: { ...serverInfo, member: bob } })
    const client = clientWith()
    render(<GitHubRepositoryDialog client={client} onCreated={vi.fn()} onClose={vi.fn()} />)
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(client.githubRepositories).not.toHaveBeenCalled()
    expect(client.githubOAuthStatus).not.toHaveBeenCalled()
  })
})
