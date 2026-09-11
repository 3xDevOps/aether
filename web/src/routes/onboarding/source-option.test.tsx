import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import type { WorkspaceMirrorResult } from '@/lib/types'
import { OnboardingSourceOption } from '@/routes/onboarding/source-option'
import { fakeApi, workspace } from '@/test/fixtures'
import { useStore } from '@/store'

const suggestedSource = 'https://github.com/acme/project.git'

function seed() {
  useStore.setState({
    workspaces: {
      [workspace.id]: {
        ...workspace,
        origin: 'https://github.com/acme/legacy.git',
      },
    },
  })
}

function configured(over: Partial<WorkspaceMirrorResult> = {}): WorkspaceMirrorResult {
  return {
    enabled: true,
    source_url: suggestedSource,
    source_identity: 'github.com/acme/project',
    branch: 'main',
    auth: 'public',
    generation: 4,
    status: 'ready',
    observed_commit: 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
    accepted_commit: 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
    ...over,
  }
}

describe('onboarding source option', () => {
  it('offers local-only setup and opens the complete mirror flow', async () => {
    seed()
    const client = fakeApi({
      workspaceMirrorStatus: vi.fn(async () => ({ enabled: false })),
    })
    render(
      <OnboardingSourceOption
        client={client}
        workspaceID={workspace.id}
        suggestedSource={suggestedSource}
      />,
    )

    const status = await screen.findByRole('status', { name: 'Source mirror status' })
    expect(status.textContent).toContain('Local-only workspace.')
    fireEvent.click(screen.getByRole('button', { name: 'Set up source mirror' }))

    const dialog = within(await screen.findByRole('dialog'))
    expect(dialog.getByText('Workspace Source')).toBeDefined()
    expect(dialog.getByLabelText<HTMLInputElement>('Source URL').value).toBe(suggestedSource)
  })

  it('shows configured source, status, branch, and the review action', async () => {
    seed()
    const current = configured({ source_url: 'ssh://git@example.com/acme/project.git', branch: 'develop', status: 'pending' })
    const client = fakeApi({
      workspaceMirrorStatus: vi.fn(async () => current),
    })
    render(<OnboardingSourceOption client={client} workspaceID={workspace.id} suggestedSource={suggestedSource} />)

    const status = await screen.findByRole('status', { name: 'Source mirror status' })
    expect(status.textContent).toContain('Source mirror configured.')
    expect(status.textContent).toContain('pending')
    expect(status.textContent).toContain('ssh://git@example.com/acme/project.git')
    expect(status.textContent).toContain('develop')
    expect(screen.getByRole('button', { name: 'Review source mirror' })).toBeDefined()
  })

  it('updates the inline summary from a dialog mutation callback', async () => {
    seed()
    const current = configured({ status: 'pending' })
    const client = fakeApi({
      workspaceMirrorStatus: vi.fn(async () => ({ enabled: false })),
      workspaceMirrorConfigure: vi.fn(async (params) => ({
        ...current,
        source_url: params.source_url,
        branch: params.branch,
      })),
    })
    render(<OnboardingSourceOption client={client} workspaceID={workspace.id} suggestedSource={suggestedSource} />)

    await screen.findByText('Local-only workspace.')
    fireEvent.click(screen.getByRole('button', { name: 'Set up source mirror' }))
    const dialog = within(await screen.findByRole('dialog'))
    fireEvent.click(dialog.getByRole('button', { name: 'Configure source' }))

    await waitFor(() => {
      expect(client.workspaceMirrorConfigure).toHaveBeenCalledWith({
        workspace_id: workspace.id,
        source_url: suggestedSource,
        branch: 'main',
        auth: 'public',
      })
      expect(screen.getByRole('status', { name: 'Source mirror status' }).textContent).toContain('Source mirror configured.')
    })
    expect(screen.getByRole('status', { name: 'Source mirror status' }).textContent).toContain('pending')
  })

  it('keeps dialog-published status when the inline read resolves later', async () => {
    seed()
    const inlineStatus = Promise.withResolvers<WorkspaceMirrorResult>()
    const current = configured()
    const client = fakeApi({
      workspaceMirrorStatus: vi.fn()
        .mockReturnValueOnce(inlineStatus.promise)
        .mockResolvedValueOnce({ enabled: false }),
      workspaceMirrorConfigure: vi.fn(async () => current),
    })
    render(<OnboardingSourceOption client={client} workspaceID={workspace.id} suggestedSource={suggestedSource} />)

    await waitFor(() => expect(client.workspaceMirrorStatus).toHaveBeenCalledTimes(1))
    fireEvent.click(screen.getByRole('button', { name: 'Set up source mirror' }))
    const dialog = within(await screen.findByRole('dialog'))
    fireEvent.click(await dialog.findByRole('button', { name: 'Configure source' }))

    await waitFor(() => {
      const status = screen.getByRole('status', { name: 'Source mirror status' })
      expect(status.textContent).toContain('Source mirror configured.')
      expect(status.textContent).toContain(current.source_url)
      expect(status.textContent).not.toContain('Checking source mirror status...')
    })

    await act(async () => {
      inlineStatus.resolve({ enabled: false })
      await inlineStatus.promise
    })
    const status = screen.getByRole('status', { name: 'Source mirror status' })
    expect(status.textContent).toContain('Source mirror configured.')
    expect(status.textContent).toContain(current.source_url)
    expect(status.textContent).not.toContain('Checking source mirror status...')
    expect(status.textContent).not.toContain('Could not check source mirror status:')
  })

  it('keeps dialog-published status when the inline read rejects later', async () => {
    seed()
    const inlineStatus = Promise.withResolvers<WorkspaceMirrorResult>()
    const current = configured()
    const client = fakeApi({
      workspaceMirrorStatus: vi.fn()
        .mockReturnValueOnce(inlineStatus.promise)
        .mockResolvedValueOnce({ enabled: false }),
      workspaceMirrorConfigure: vi.fn(async () => current),
    })
    render(<OnboardingSourceOption client={client} workspaceID={workspace.id} suggestedSource={suggestedSource} />)

    await waitFor(() => expect(client.workspaceMirrorStatus).toHaveBeenCalledTimes(1))
    fireEvent.click(screen.getByRole('button', { name: 'Set up source mirror' }))
    const dialog = within(await screen.findByRole('dialog'))
    fireEvent.click(await dialog.findByRole('button', { name: 'Configure source' }))

    await waitFor(() => {
      const status = screen.getByRole('status', { name: 'Source mirror status' })
      expect(status.textContent).toContain('Source mirror configured.')
      expect(status.textContent).toContain(current.source_url)
      expect(status.textContent).not.toContain('Checking source mirror status...')
    })

    await act(async () => {
      inlineStatus.reject(new Error('workspace.mirror.status: stale failure'))
      await inlineStatus.promise.catch(() => {})
    })
    const status = screen.getByRole('status', { name: 'Source mirror status' })
    expect(status.textContent).toContain('Source mirror configured.')
    expect(status.textContent).toContain(current.source_url)
    expect(status.textContent).not.toContain('Checking source mirror status...')
    expect(status.textContent).not.toContain('Could not check source mirror status:')
  })

  it('keeps setup available when reading mirror status fails', async () => {
    seed()
    const client = fakeApi({
      workspaceMirrorStatus: vi.fn(async () => {
        throw new Error('workspace.mirror.status: unavailable')
      }),
    })
    render(<OnboardingSourceOption client={client} workspaceID={workspace.id} suggestedSource={suggestedSource} />)

    const status = await screen.findByRole('status', { name: 'Source mirror status' })
    expect(status.textContent).toContain('workspace.mirror.status: unavailable')
    expect(screen.getByRole('button', { name: 'Set up source mirror' })).toBeDefined()

    fireEvent.click(screen.getByRole('button', { name: 'Set up source mirror' }))
    const dialog = within(await screen.findByRole('dialog'))
    expect(dialog.getByLabelText<HTMLInputElement>('Source URL').value).toBe(suggestedSource)
  })
})
