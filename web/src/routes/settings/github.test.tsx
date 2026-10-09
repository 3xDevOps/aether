import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import type { Api } from '@/lib/api'
import type { Workspace } from '@/lib/types'
import { GitHubSection } from '@/routes/settings/github'
import { GitHubRepositoryDialog } from '@/routes/admin-dialogs/github-repository-dialog'
import { useStore } from '@/store'
import { fakeApi, serverInfo, workspace } from '@/test/fixtures'

const accepted = { ...workspace, id: 'ws_second', name: 'Second repository' }
let accept: (workspace: Workspace) => void
let useRealPicker = false
vi.mock('@/routes/admin-dialogs', () => ({
  GitHubRepositoryDialog: (props: { client?: Api; onCreated: (workspace: Workspace) => void; onClose: () => void }) => {
    if (useRealPicker) return <GitHubRepositoryDialog {...props} />
    const { onCreated, onClose } = props
    accept = onCreated
    return <div role="dialog" aria-label="Repository picker"><button onClick={() => onCreated(accepted)}>Accept revision</button><button onClick={onClose}>Close picker</button></div>
  },
}))

beforeEach(() => { useRealPicker = false })

function seed(role: 'admin' | 'collaborator' = 'admin') {
  useStore.setState({
    info: { ...serverInfo, member: { ...serverInfo.member, role } },
    capabilities: { gateway: 'remote', methods: ['*'], ws: ['events'] },
    route: { name: 'settings', params: {} },
    workspaces: { [workspace.id]: workspace },
    activeWorkspace: workspace.id,
  })
}

it('opens the shared picker without changing scope, then selects and opens its accepted workspace', async () => {
  seed()
  const client = fakeApi({ githubOAuthStatus: vi.fn(async () => ({ state: 'connected' as const, login: 'octocat' })) })
  render(<GitHubSection client={client} />)
  await screen.findByText(/octocat/)
  fireEvent.click(screen.getByRole('button', { name: 'Add repository' }))
  expect(useStore.getState().activeWorkspace).toBe(workspace.id)
  fireEvent.click(screen.getByRole('button', { name: 'Accept revision' }))
  expect(useStore.getState().workspaces[accepted.id]).toEqual(accepted)
  expect(useStore.getState().activeWorkspace).toBe(accepted.id)
  expect(useStore.getState().route).toEqual({ name: 'workspace', params: { workspaceId: accepted.id } })
  expect(client.githubOAuthStart).not.toHaveBeenCalled()
})

it('refreshes the native account after connecting in the picker and closing without importing', async () => {
  seed()
  useRealPicker = true
  let login: string | undefined
  const client = fakeApi({
    githubOAuthStatus: vi.fn(async () => login
      ? { state: 'connected' as const, login }
      : { state: 'disconnected' as const }),
    githubOAuthStart: vi.fn(async () => {
      login = 'new-octocat'
      return { state: 'connected' as const, login }
    }),
    githubRepositories: vi.fn(async () => ({
      account: { id: 42, login: 'new-octocat' },
      repositories: [],
    })),
  })
  render(<GitHubSection client={client} />)
  await waitFor(() => expect(screen.getByRole('button', { name: 'Connect GitHub' }).hasAttribute('disabled')).toBe(false))
  fireEvent.click(screen.getByRole('button', { name: 'Add repository' }))
  const picker = screen.getByRole('dialog')
  const connect = await within(picker).findByRole('button', { name: 'Connect GitHub' })
  await waitFor(() => expect(connect.hasAttribute('disabled')).toBe(false))
  await act(async () => { fireEvent.click(connect) })
  await waitFor(() => expect(within(picker).getByRole('button', { name: 'Reload repositories' }).hasAttribute('disabled')).toBe(false))
  expect(within(picker).getByRole('button', { name: 'Review repository' }).hasAttribute('disabled')).toBe(true)
  fireEvent.click(within(picker).getByRole('button', { name: 'Cancel' }))

  expect(screen.queryByRole('dialog')).toBeNull()
  const connection = screen.getByRole('region', { name: 'GitHub connection' })
  await waitFor(() => expect(within(connection).getByRole('status').textContent).toContain('new-octocat'))
  await waitFor(() => expect(within(connection).getByRole('button', { name: 'Check connection' }).hasAttribute('disabled')).toBe(false))
  expect(within(connection).queryByRole('button', { name: 'Connect GitHub' })).toBeNull()
  expect(useStore.getState().workspaces).toEqual({ [workspace.id]: workspace })
  expect(useStore.getState().activeWorkspace).toBe(workspace.id)
  expect(useStore.getState().route).toEqual({ name: 'settings', params: {} })
  expect(client.workspaceImport).not.toHaveBeenCalled()
  expect(client.githubOAuthStart).toHaveBeenCalledTimes(1)
})

it('dismisses the picker and ignores late acceptance after connection context changes', () => {
  seed()
  render(<GitHubSection client={fakeApi()} />)
  fireEvent.click(screen.getByRole('button', { name: 'Add repository' }))
  const staleAccept = accept
  act(() => useStore.setState((state) => ({ connectionEpoch: state.connectionEpoch + 1 })))
  expect(screen.queryByRole('dialog')).toBeNull()
  act(() => staleAccept(accepted))
  expect(useStore.getState().activeWorkspace).toBe(workspace.id)
  expect(useStore.getState().workspaces[accepted.id]).toBeUndefined()
  expect(useStore.getState().route.name).toBe('settings')
})

it('keeps cancellation in Settings and offers existing workspace management', () => {
  seed()
  render(<GitHubSection client={fakeApi()} />)
  fireEvent.click(screen.getByRole('button', { name: 'Add repository' }))
  fireEvent.click(screen.getByRole('button', { name: 'Close picker' }))
  expect(useStore.getState().activeWorkspace).toBe(workspace.id)
  expect(useStore.getState().route.name).toBe('settings')
  fireEvent.click(screen.getByRole('button', { name: 'Manage workspaces' }))
  expect(useStore.getState().route.name).toBe('workspaces')
})

it('does not expose administrator connection or repository controls to members', () => {
  seed('collaborator')
  const client = fakeApi()
  render(<GitHubSection client={client} />)
  expect(screen.queryByRole('region', { name: 'GitHub' })).toBeNull()
  expect(screen.queryByRole('button', { name: 'Add repository' })).toBeNull()
  expect(client.githubOAuthStatus).not.toHaveBeenCalled()
})
