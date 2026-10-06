import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { ApiError } from '@/lib/api'
import type { WorkspaceImportResult } from '@/lib/types'
import { ImportRepositoryDialog } from '@/routes/admin-dialogs/import-repository-dialog'
import { useStore } from '@/store'
import { fakeApi, otherWorkspace, workspace } from '@/test/fixtures'

const imported: WorkspaceImportResult = {
  created: true,
  workspace: otherWorkspace,
  mirror: { enabled: true, status: 'pending', generation: 1 },
}

function fillForm(name = otherWorkspace.name) {
  fireEvent.change(screen.getByLabelText('Workspace name'), { target: { value: name } })
  fireEvent.change(screen.getByLabelText('Source URL'), { target: { value: 'https://github.com/acme/project.git' } })
}

function submit() {
  fireEvent.click(screen.getByRole('button', { name: 'Import repository' }))
}

function expectFreshForm() {
  expect(screen.getByLabelText<HTMLInputElement>('Workspace name').value).toBe('')
  expect(screen.getByLabelText<HTMLInputElement>('Source URL').value).toBe('')
  expect(screen.getByLabelText<HTMLInputElement>('Source / base branch').value).toBe('main')
  expect(screen.getByLabelText<HTMLInputElement>('Checkout Origin (optional)').value).toBe('')
  expect(screen.getByLabelText<HTMLSelectElement>('Source authentication').value).toBe('public')
  expect(screen.queryByRole('region', { name: 'Import outcome' })).toBeNull()
  expect(screen.queryByRole('alert')).toBeNull()
  expect(screen.getByRole('button', { name: 'Cancel' })).toHaveProperty('disabled', false)
}

describe('repository import context isolation', () => {
  beforeEach(() => {
    useStore.setState({
      identityKey: 'server:alice',
      connectionEpoch: 0,
      workspaces: { [workspace.id]: workspace },
      deletedWorkspaceIDs: new Set(),
    })
  })

  describe.each(['identity', 'connection', 'away-and-back', 'client', 'unmount'] as const)('%s transition', (transition) => {
    it.each(['success', 'failure'] as const)('ignores a delayed %s without changing the current store, UI or callbacks', async (outcome) => {
      const pending = Promise.withResolvers<WorkspaceImportResult>()
      const client = fakeApi({ workspaceImport: vi.fn(() => pending.promise) })
      const replacement = fakeApi()
      const onImported = vi.fn()
      const onClose = vi.fn()
      const view = render(<ImportRepositoryDialog client={client} onImported={onImported} onClose={onClose} />)
      fillForm()
      submit()
      expect(screen.getByRole('button', { name: 'Importing…' })).toHaveProperty('disabled', true)

      if (transition === 'unmount') {
        view.unmount()
      } else if (transition === 'client') {
        view.rerender(<ImportRepositoryDialog client={replacement} onImported={onImported} onClose={onClose} />)
      } else {
        act(() => {
          useStore.setState(transition === 'connection' ? { connectionEpoch: 1 } : { identityKey: 'server:bob' })
          if (transition === 'away-and-back') useStore.setState({ identityKey: 'server:alice' })
        })
      }
      if (transition !== 'unmount') expectFreshForm()

      await act(async () => {
        if (outcome === 'success') pending.resolve(imported)
        else pending.reject(new Error('old connection lost'))
      })

      expect(useStore.getState().workspaces).toEqual({ [workspace.id]: workspace })
      expect(onImported).not.toHaveBeenCalled()
      expect(onClose).not.toHaveBeenCalled()
      expect(client.workspaceImport).toHaveBeenCalledTimes(1)
      expect(replacement.workspaceImport).not.toHaveBeenCalled()
      if (transition === 'unmount') {
        expect(screen.queryByRole('dialog')).toBeNull()
      } else {
        expectFreshForm()
        fillForm('current-project')
        expect(screen.getByRole('button', { name: 'Import repository' })).toHaveProperty('disabled', false)
      }
    })
  })

  it.each(['success', 'failure'] as const)('does not let an old %s finish a new context import', async (outcome) => {
    const oldRequest = Promise.withResolvers<WorkspaceImportResult>()
    const newRequest = Promise.withResolvers<WorkspaceImportResult>()
    const client = fakeApi({ workspaceImport: vi.fn().mockReturnValueOnce(oldRequest.promise).mockReturnValueOnce(newRequest.promise) })
    const onImported = vi.fn()
    render(<ImportRepositoryDialog client={client} onImported={onImported} onClose={vi.fn()} />)
    fillForm()
    submit()
    act(() => useStore.setState({ identityKey: 'server:bob', workspaces: {} }))
    fillForm('current-project')
    submit()

    await act(async () => {
      if (outcome === 'success') oldRequest.resolve(imported)
      else oldRequest.reject(new Error('old connection lost'))
    })
    expect(screen.getByRole('button', { name: 'Importing…' })).toHaveProperty('disabled', true)
    expect(screen.getByRole('button', { name: 'Cancel' })).toHaveProperty('disabled', true)
    expect(screen.getByLabelText<HTMLInputElement>('Workspace name').value).toBe('current-project')
    expect(screen.queryByRole('region', { name: 'Import outcome' })).toBeNull()
    expect(screen.queryByRole('alert')).toBeNull()
    expect(useStore.getState().workspaces).toEqual({})
    expect(onImported).not.toHaveBeenCalled()

    const current = { ...imported, workspace: { ...workspace, id: 'wsp_current', name: 'current-project' } }
    await act(async () => newRequest.resolve(current))
    expect(useStore.getState().workspaces).toEqual({ [current.workspace.id]: current.workspace })
    expect(screen.getByRole('region', { name: 'Import outcome' }).textContent).toContain(current.workspace.id)
    expect(onImported).toHaveBeenCalledExactlyOnceWith(current.workspace)
    expect(client.workspaceImport).toHaveBeenCalledTimes(2)
  })

  it.each(['identity', 'connection', 'client'] as const)('clears retained results and private source fields on a %s change', async (transition) => {
    const client = fakeApi({ workspaceImport: vi.fn(async () => imported) })
    const onImported = vi.fn()
    const onClose = vi.fn()
    const view = render(<ImportRepositoryDialog client={client} onImported={onImported} onClose={onClose} />)
    fillForm()
    fireEvent.change(screen.getByLabelText('Source / base branch'), { target: { value: 'private-branch' } })
    fireEvent.change(screen.getByLabelText('Checkout Origin (optional)'), { target: { value: 'https://github.com/private/fork.git' } })
    fireEvent.change(screen.getByLabelText('Source authentication'), { target: { value: 'deploy-key' } })
    fireEvent.change(screen.getByLabelText('Pinned known_hosts (required for generic SSH)'), { target: { value: 'private-host ssh-ed25519 public-host-key' } })
    submit()
    await screen.findByRole('region', { name: 'Import outcome' })
    if (transition === 'client') {
      view.rerender(<ImportRepositoryDialog client={fakeApi()} onImported={onImported} onClose={onClose} />)
    } else {
      act(() => useStore.setState(transition === 'identity' ? { identityKey: 'server:bob' } : { connectionEpoch: 1 }))
    }
    expectFreshForm()
    fireEvent.change(screen.getByLabelText('Source authentication'), { target: { value: 'deploy-key' } })
    expect(screen.getByLabelText<HTMLTextAreaElement>('Pinned known_hosts (required for generic SSH)').value).toBe('')
    expect(onImported).toHaveBeenCalledTimes(1)
    expect(onClose).not.toHaveBeenCalled()
  })

  it.each([false, true])('retains same-context creation and source recovery when fetch failed: %s', async (fetchFailed) => {
    const result = { ...imported, ...(fetchFailed ? { error: 'source authentication not installed' } : {}) }
    const client = fakeApi({
      workspaceImport: vi.fn(async () => result),
      workspaceMirrorStatus: vi.fn(async () => imported.mirror),
    })
    const onImported = vi.fn()
    const onClose = vi.fn()
    render(<ImportRepositoryDialog client={client} onImported={onImported} onClose={onClose} />)
    fillForm()
    submit()
    const outcome = await screen.findByRole('region', { name: 'Import outcome' })
    expect(outcome.textContent).toContain(otherWorkspace.id)
    expect(useStore.getState().workspaces[otherWorkspace.id]).toEqual(otherWorkspace)
    expect(onImported).toHaveBeenCalledExactlyOnceWith(otherWorkspace)
    expect(screen.queryByRole('button', { name: 'Import repository' })).toBeNull()
    expect(screen.getByLabelText('Workspace name').closest('fieldset')).toHaveProperty('disabled', true)
    if (fetchFailed) expect(screen.getByRole('alert').textContent).toBe(result.error)
    fireEvent.click(screen.getByRole('button', { name: 'Continue to Repository' }))
    await screen.findByRole('heading', { name: 'Workspace Source' })
    await waitFor(() => expect(screen.getByTestId('mirror-state').textContent).toBe('pending'))
    expect(screen.getByRole('dialog').textContent).toContain(otherWorkspace.name)
    fireEvent.click(screen.getAllByRole('button', { name: 'Close' })[0])
    expect(onImported).toHaveBeenLastCalledWith(otherWorkspace)
    expect(onClose).toHaveBeenCalledTimes(1)
    expect(client.workspaceImport).toHaveBeenCalledTimes(1)
    expect(client.workspaceMirrorAdopt).not.toHaveBeenCalled()
  })

  it('leaves source recovery behind when the identity changes', async () => {
    const client = fakeApi({
      workspaceImport: vi.fn(async () => imported),
      workspaceMirrorStatus: vi.fn(async () => imported.mirror),
    })
    const onImported = vi.fn()
    const onClose = vi.fn()
    render(<ImportRepositoryDialog client={client} onImported={onImported} onClose={onClose} />)
    fillForm()
    submit()
    fireEvent.click(await screen.findByRole('button', { name: 'Continue to Repository' }))
    await screen.findByRole('heading', { name: 'Workspace Source' })
    act(() => useStore.setState({ identityKey: 'server:bob', workspaces: {} }))
    expectFreshForm()
    expect(screen.queryByRole('heading', { name: 'Workspace Source' })).toBeNull()
    expect(onImported).toHaveBeenCalledTimes(1)
    expect(onClose).not.toHaveBeenCalled()
  })

  it('keeps an uncertain same-context failure visible without retrying and clears it on a context change', async () => {
    const client = fakeApi({ workspaceImport: vi.fn(async () => { throw new Error('connection lost during import') }) })
    const onImported = vi.fn()
    render(<ImportRepositoryDialog client={client} onImported={onImported} onClose={vi.fn()} />)
    fillForm()
    submit()
    await screen.findByText('connection lost during import')
    expect(onImported).toHaveBeenCalledExactlyOnceWith()
    expect(screen.getByRole('button', { name: 'Import repository' })).toHaveProperty('disabled', true)
    submit()
    expect(client.workspaceImport).toHaveBeenCalledTimes(1)
    expect(useStore.getState().workspaces).toEqual({ [workspace.id]: workspace })
    act(() => useStore.setState({ connectionEpoch: 1 }))
    expectFreshForm()
    fillForm()
    expect(screen.getByRole('button', { name: 'Import repository' })).toHaveProperty('disabled', false)
    expect(client.workspaceImport).toHaveBeenCalledTimes(1)
  })

  it('allows an explicit corrected import after a same-context validation failure', async () => {
    const client = fakeApi({ workspaceImport: vi.fn().mockRejectedValueOnce(new ApiError(400, 'invalid source URL', -32602)).mockResolvedValueOnce(imported) })
    const onImported = vi.fn()
    render(<ImportRepositoryDialog client={client} onImported={onImported} onClose={vi.fn()} />)
    fillForm()
    submit()
    await screen.findByText('invalid source URL')
    expect(screen.getByRole('button', { name: 'Import repository' })).toHaveProperty('disabled', false)
    fireEvent.change(screen.getByLabelText('Source URL'), { target: { value: 'https://github.com/acme/corrected.git' } })
    submit()
    await screen.findByRole('region', { name: 'Import outcome' })
    expect(useStore.getState().workspaces[otherWorkspace.id]).toEqual(otherWorkspace)
    expect(onImported).toHaveBeenLastCalledWith(otherWorkspace)
    expect(client.workspaceImport).toHaveBeenCalledTimes(2)
  })
})
