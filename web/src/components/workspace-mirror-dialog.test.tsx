import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { WorkspaceMirrorDialog } from '@/components/workspace-mirror-dialog'
import { fakeApi, serverInfo, workspace } from '@/test/fixtures'
import { useStore } from '@/store'
import type { WorkspaceMirrorResult } from '@/lib/types'

const accepted = 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
const candidate = 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'

function seed(over: Partial<typeof workspace> = {}) {
  useStore.setState({
    workspaces: { [workspace.id]: { ...workspace, ...over } },
  })
}

function mirror(over: Partial<WorkspaceMirrorResult> = {}): WorkspaceMirrorResult {
  return {
    enabled: true,
    source_url: 'https://github.com/acme/project.git',
    source_identity: 'github.com/acme/project',
    branch: 'main',
    auth: 'public',
    generation: 4,
    status: 'ready',
    observed_commit: accepted,
    accepted_commit: accepted,
    last_attempt_at: '2026-09-11T10:00:00Z',
    ...over,
  }
}

describe('workspace mirror dialog', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })
  it('shows the authorizing GitHub account and reconnect flow without deploy-key instructions', async () => {
    seed()
    useStore.setState({ info: serverInfo })
    const client = fakeApi({
      workspaceMirrorStatus: vi.fn(async () => mirror({ auth: 'github', github_member_id: serverInfo.member.id, github_user_id: 42, status: 'auth-failed', last_error: 'GitHub account changed; reconfigure source' })),
      githubOAuthStatus: vi.fn(async () => ({ state: 'connected' as const, login: 'alice-gh' })),
    })
    render(<WorkspaceMirrorDialog workspaceID={workspace.id} client={client} onClose={vi.fn()} />)
    expect(await screen.findByText('Connected as alice-gh')).toBeTruthy()
    expect(screen.getByText(/Account ID 42/)).toBeTruthy()
    expect(screen.getByText('GitHub account changed; reconfigure source')).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Copy public key' })).toBeNull()
    expect(screen.queryByText(/Install the generated key/)).toBeNull()
    expect(screen.getByLabelText<HTMLSelectElement>('Authentication').value).toBe('github')
  })

  it('does not publish an old source mutation after the server identity changes', async () => {
    seed()
    const pending = Promise.withResolvers<WorkspaceMirrorResult>()
    const client = fakeApi({ workspaceMirrorStatus: vi.fn(async () => mirror()), workspaceMirrorRefresh: vi.fn(() => pending.promise) })
    const onStatusChange = vi.fn()
    render(<WorkspaceMirrorDialog workspaceID={workspace.id} client={client} onStatusChange={onStatusChange} onClose={vi.fn()} />)
    fireEvent.click(await screen.findByRole('button', { name: 'Refresh' }))
    act(() => { useStore.setState({ identityKey: 'new-server:alice' }) })
    await act(async () => { pending.resolve(mirror({ observed_commit: 'old-server-private-revision' })) })
    expect(screen.queryByText('old-server-private-revision')).toBeNull()
    expect(onStatusChange.mock.calls.some(([result]) => result.observed_commit === 'old-server-private-revision')).toBe(false)
  })

  it('shows a local-only workspace and prefills the configure source', async () => {
    seed({ origin: 'https://github.com/acme/project.git' })
    const client = fakeApi({
      workspaceMirrorStatus: vi.fn(async () => ({ enabled: false })),
    })
    render(<WorkspaceMirrorDialog workspaceID={workspace.id} client={client} onClose={() => {}} />)

    const dialog = within(await screen.findByRole('dialog'))
    expect(dialog.getByTestId('mirror-state').textContent).toBe('local-only')
    expect(dialog.getByLabelText<HTMLInputElement>('Source URL').value).toBe('https://github.com/acme/project.git')
    expect(dialog.getByLabelText<HTMLInputElement>('Source branch').value).toBe('main')
  })
  it('prefills a supplied checkout Origin before the workspace origin', async () => {
    seed({ origin: 'https://github.com/acme/legacy.git' })
    const suggestedSource = 'https://github.com/acme/project.git'
    const client = fakeApi({
      workspaceMirrorStatus: vi.fn(async () => ({ enabled: false })),
    })
    render(
      <WorkspaceMirrorDialog
        workspaceID={workspace.id}
        client={client}
        suggestedSource={suggestedSource}
        onClose={() => {}}
      />,
    )

    const dialog = within(await screen.findByRole('dialog'))
    expect(dialog.getByLabelText<HTMLInputElement>('Source URL').value).toBe(suggestedSource)
  })

  it('configures a public source without credentials', async () => {
    seed()
    const configured = mirror({ status: 'pending', auth: 'public' })
    const client = fakeApi({
      workspaceMirrorStatus: vi.fn(async () => ({ enabled: false })),
      workspaceMirrorConfigure: vi.fn(async () => configured),
    })
    render(<WorkspaceMirrorDialog workspaceID={workspace.id} client={client} onClose={() => {}} />)

    const dialog = within(await screen.findByRole('dialog'))
    fireEvent.change(dialog.getByLabelText('Source URL'), {
      target: { value: 'https://github.com/acme/project.git' },
    })
    fireEvent.click(dialog.getByRole('button', { name: 'Configure source' }))

    await waitFor(() => {
      expect(client.workspaceMirrorConfigure).toHaveBeenCalledWith({
        workspace_id: workspace.id,
        source_url: 'https://github.com/acme/project.git',
        branch: 'main',
        auth: 'public',
      })
    })
    expect(dialog.getByTestId('mirror-state').textContent).toBe('pending')
  })

  it('shows and copies the deploy public key with a GitHub deploy-key link', async () => {
    seed()
    const configured = mirror({
      auth: 'deploy-key',
      source_url: 'ssh://git@github.com/acme/project.git',
      public_key: 'ssh-ed25519 AAAA aether-mirror',
      status: 'pending',
    })
    const client = fakeApi({
      workspaceMirrorStatus: vi.fn(async () => ({ enabled: false })),
      workspaceMirrorConfigure: vi.fn(async () => configured),
    })
    const writeText = vi.fn(async () => {})
    vi.stubGlobal('navigator', { ...navigator, clipboard: { writeText } })
    render(<WorkspaceMirrorDialog workspaceID={workspace.id} client={client} onClose={() => {}} />)

    const dialog = within(await screen.findByRole('dialog'))
    fireEvent.change(dialog.getByLabelText('Authentication'), {
      target: { value: 'deploy-key' },
    })
    fireEvent.change(dialog.getByLabelText('Source URL'), {
      target: { value: 'https://github.com/acme/project.git' },
    })
    fireEvent.click(dialog.getByRole('button', { name: 'Configure source' }))

    expect(await screen.findByText('ssh-ed25519 AAAA aether-mirror')).toBeDefined()
    fireEvent.click(dialog.getByRole('button', { name: 'Copy public key' }))
    await waitFor(() => expect(writeText).toHaveBeenCalledWith('ssh-ed25519 AAAA aether-mirror'))
    const link = dialog.getByRole('link', { name: 'Install this key in GitHub deploy keys' })
    expect(link.getAttribute('href')).toBe('https://github.com/acme/project/settings/keys')
  })

  it('renders ready state with exact accepted SHA and check time', async () => {
    seed()
    const client = fakeApi({ workspaceMirrorStatus: vi.fn(async () => mirror()) })
    render(<WorkspaceMirrorDialog workspaceID={workspace.id} client={client} onClose={() => {}} />)

    const dialog = within(await screen.findByRole('dialog'))
    expect(dialog.getByTestId('mirror-state').textContent).toBe('ready')
    expect(dialog.getAllByText(accepted).length).toBeGreaterThanOrEqual(1)
    expect(dialog.getByText('2026-09-11T10:00:00Z')).toBeDefined()
  })

  it('requires confirmation before adopting a failure candidate', async () => {
    seed()
    const failed = mirror({
      status: 'rewritten',
      observed_commit: candidate,
      accepted_commit: accepted,
      last_error: 'source history was rewritten',
    })
    const client = fakeApi({
      workspaceMirrorStatus: vi.fn(async () => failed),
      workspaceMirrorAdopt: vi.fn(async () => mirror({ observed_commit: candidate, accepted_commit: candidate })),
    })
    render(<WorkspaceMirrorDialog workspaceID={workspace.id} client={client} onClose={() => {}} />)

    const dialog = within(await screen.findByRole('dialog'))
    expect(dialog.getByText('source history was rewritten')).toBeDefined()
    fireEvent.click(dialog.getByRole('button', { name: 'Adopt candidate' }))
    const confirmation = within(await screen.findByRole('alertdialog'))
    expect(confirmation.getByText(candidate)).toBeDefined()
    fireEvent.click(confirmation.getByRole('button', { name: 'Adopt candidate' }))

    await waitFor(() => expect(client.workspaceMirrorAdopt).toHaveBeenCalledWith(workspace.id, 4, candidate))
  })

  it('retains the reviewed candidate after a stale adoption until an explicit refresh and confirmation', async () => {
    seed()
    const nextCommit = 'cccccccccccccccccccccccccccccccccccccccc'
    let current = mirror({ status: 'rewritten', observed_commit: candidate })
    const onClose = vi.fn()
    const client = fakeApi({
      workspaceMirrorStatus: vi.fn(async () => current),
      workspaceMirrorRefresh: vi.fn(async () => current),
      workspaceMirrorAdopt: vi.fn(async (_workspaceID: string, _generation: number, expectedCommit: string) => {
        if (expectedCommit !== current.observed_commit) throw new Error('source candidate changed; review the latest revision')
        current = { ...current, status: 'ready', accepted_commit: expectedCommit }
        return current
      }),
    })
    render(<WorkspaceMirrorDialog workspaceID={workspace.id} client={client} onClose={onClose} />)
    const dialog = within(await screen.findByRole('dialog'))
    fireEvent.click(await dialog.findByRole('button', { name: 'Adopt candidate' }))
    const confirmation = within(await screen.findByRole('alertdialog'))
    expect(confirmation.getByText(candidate)).toBeTruthy()
    current = { ...current, observed_commit: nextCommit }
    fireEvent.click(confirmation.getByRole('button', { name: 'Adopt candidate' }))
    expect((await screen.findByRole('alert')).textContent).toContain('source candidate changed; review the latest revision')
    expect(client.workspaceMirrorAdopt).toHaveBeenCalledExactlyOnceWith(workspace.id, 4, candidate)
    expect(client.workspaceMirrorStatus).toHaveBeenCalledTimes(1)
    expect(client.workspaceMirrorRefresh).not.toHaveBeenCalled()
    expect(dialog.queryByText(nextCommit)).toBeNull()
    expect(current.accepted_commit).toBe(accepted)
    expect(onClose).not.toHaveBeenCalled()
    expect(useStore.getState().workspaces[workspace.id]).toBeTruthy()

    fireEvent.click(await dialog.findByRole('button', { name: /^(Verify|Refresh)$/ }))
    await waitFor(() => expect(dialog.getAllByText(nextCommit).length).toBeGreaterThan(0))
    expect(client.workspaceMirrorAdopt).toHaveBeenCalledTimes(1)
    fireEvent.click(dialog.getByRole('button', { name: 'Adopt candidate' }))
    const freshConfirmation = within(await screen.findByRole('alertdialog'))
    expect(freshConfirmation.getByText(nextCommit)).toBeTruthy()
    fireEvent.click(freshConfirmation.getByRole('button', { name: 'Adopt candidate' }))
    await waitFor(() => expect(dialog.getByTestId('mirror-state').textContent).toBe('ready'))
    expect(client.workspaceMirrorAdopt).toHaveBeenLastCalledWith(workspace.id, 4, nextCommit)
    expect(current.accepted_commit).toBe(nextCommit)
  })
  it('keeps refresh errors while loading a persisted candidate for adoption', async () => {
    seed()
    const failed = mirror({
      status: 'rewritten',
      observed_commit: candidate,
      accepted_commit: accepted,
      last_error: 'source history was rewritten',
    })
    const onStatusChange = vi.fn()
    const client = fakeApi({
      workspaceMirrorStatus: vi.fn().mockResolvedValueOnce(mirror()).mockResolvedValueOnce(failed),
      workspaceMirrorRefresh: vi.fn(async () => {
        throw new Error('workspace.mirror.refresh: unavailable')
      }),
      workspaceMirrorAdopt: vi.fn(async () => mirror({ observed_commit: candidate, accepted_commit: candidate })),
    })
    render(
      <WorkspaceMirrorDialog
        workspaceID={workspace.id}
        client={client}
        onStatusChange={onStatusChange}
        onClose={() => {}}
      />,
    )

    const dialog = within(await screen.findByRole('dialog'))
    await waitFor(() => expect(dialog.getByRole('button', { name: 'Refresh' })).toBeDefined())
    fireEvent.click(dialog.getByRole('button', { name: 'Refresh' }))

    await waitFor(() => {
      expect(client.workspaceMirrorStatus).toHaveBeenCalledTimes(2)
      expect(dialog.getByTestId('mirror-state').textContent).toBe('rewritten')
      expect(dialog.getByRole('button', { name: 'Adopt candidate' })).toBeDefined()
    })
    expect((await screen.findByRole('alert')).textContent).toContain('workspace.mirror.refresh: unavailable')
    expect(onStatusChange).toHaveBeenLastCalledWith(failed)

    fireEvent.click(dialog.getByRole('button', { name: 'Adopt candidate' }))
    const confirmation = within(await screen.findByRole('alertdialog'))
    fireEvent.click(confirmation.getByRole('button', { name: 'Adopt candidate' }))

    await waitFor(() => expect(client.workspaceMirrorAdopt).toHaveBeenCalledWith(workspace.id, 4, candidate))
  })


  it('requires confirmation before disabling a configured source', async () => {
    seed()
    const client = fakeApi({
      workspaceMirrorStatus: vi.fn(async () => mirror()),
      workspaceMirrorDisable: vi.fn(async () => ({ enabled: false })),
    })
    render(<WorkspaceMirrorDialog workspaceID={workspace.id} client={client} onClose={() => {}} />)

    const dialog = within(await screen.findByRole('dialog'))
    fireEvent.click(dialog.getByRole('button', { name: 'Disable source' }))
    const confirmation = within(await screen.findByRole('alertdialog'))
    expect(confirmation.getByText(/becomes local-only/)).toBeDefined()
    fireEvent.click(confirmation.getByRole('button', { name: 'Disable source' }))

    await waitFor(() => expect(client.workspaceMirrorDisable).toHaveBeenCalledWith(workspace.id))
    expect(dialog.getByTestId('mirror-state').textContent).toBe('local-only')
  })

  it('refetches persisted disabling state and exposes only a safe retry', async () => {
    seed()
    const disabling = mirror({ status: 'disabling' })
    const client = fakeApi({
      workspaceMirrorStatus: vi.fn()
        .mockResolvedValueOnce(mirror())
        .mockResolvedValueOnce(disabling),
      workspaceMirrorDisable: vi.fn(async () => {
        throw new Error('workspace.mirror.disable: cleanup incomplete')
      }),
    })
    render(<WorkspaceMirrorDialog workspaceID={workspace.id} client={client} onClose={() => {}} />)

    const dialog = within(await screen.findByRole('dialog'))
    fireEvent.click(dialog.getByRole('button', { name: 'Disable source' }))
    const confirmation = within(await screen.findByRole('alertdialog'))
    fireEvent.click(confirmation.getByRole('button', { name: 'Disable source' }))

    await waitFor(() => {
      expect(client.workspaceMirrorDisable).toHaveBeenCalledWith(workspace.id)
      expect(client.workspaceMirrorStatus).toHaveBeenCalledTimes(2)
      expect(dialog.getByTestId('mirror-state').textContent).toBe('disabling')
    })
    expect(dialog.getByText(/other actions are unavailable until cleanup finishes/)).toBeDefined()
    expect(dialog.getByRole('button', { name: 'Retry disable' })).toBeDefined()
    expect(dialog.queryByRole('button', { name: 'Configure source' })).toBeNull()
    expect(dialog.queryByRole('button', { name: 'Save source' })).toBeNull()
    expect(dialog.queryByRole('button', { name: 'Refresh' })).toBeNull()
    expect(dialog.queryByRole('button', { name: 'Verify' })).toBeNull()
    expect(dialog.queryByRole('button', { name: 'Adopt candidate' })).toBeNull()
    expect((await screen.findByRole('alert')).textContent).toContain('cleanup incomplete')
  })

  it('surfaces status and configure errors', async () => {
    seed()
    const client = fakeApi({
      workspaceMirrorStatus: vi.fn(async () => {
        throw new Error('workspace.mirror.status: unavailable')
      }),
      workspaceMirrorConfigure: vi.fn(async () => {
        throw new Error('workspace.mirror.configure: invalid source')
      }),
    })
    render(<WorkspaceMirrorDialog workspaceID={workspace.id} client={client} onClose={() => {}} />)

    const dialog = within(await screen.findByRole('dialog'))
    expect((await screen.findByRole('alert')).textContent).toContain('workspace.mirror.status: unavailable')
    fireEvent.change(dialog.getByLabelText('Source URL'), {
      target: { value: 'https://github.com/acme/project.git' },
    })
    fireEvent.click(dialog.getByRole('button', { name: 'Configure source' }))
    expect((await screen.findByRole('alert')).textContent).toContain('workspace.mirror.configure: invalid source')
  })
})
