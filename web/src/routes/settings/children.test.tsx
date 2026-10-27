import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ApiError } from '@/lib/api'
import type { ChildSettings } from '@/lib/types'
import { ChildRunsSection, childRunsLabel, fallbackLabel, sharedLabel } from '@/routes/settings/children'
import { useStore } from '@/store'
import { useChildSettings } from '@/store/children'
import { alice, fakeApi, serverInfo, workspace } from '@/test/fixtures'

const off: ChildSettings = { enabled: false, fallback: true, shared_when_missing: false, configured: false, nudge: 'show' }

function show(methods = ['*'], over: Parameters<typeof fakeApi>[0] = {}) {
  useStore.setState({
    ...useStore.getInitialState(),
    info: serverInfo,
    members: { [alice.id]: alice },
    workspaces: { [workspace.id]: workspace },
    activeWorkspace: workspace.id,
    capabilities: { gateway: 'remote', methods, ws: [] },
  }, true)
  useChildSettings.setState({ settings: null, error: null })
  const client = fakeApi(over)
  render(<ChildRunsSection client={client} />)
  return client
}

const box = (name: string) => screen.getByRole('checkbox', { name })

describe('child run settings', () => {
  it('is hidden where the server has no child runs', () => {
    const client = show(['run.list'])
    expect(screen.queryByRole('checkbox', { name: childRunsLabel })).toBeNull()
    expect(client.childSettings).not.toHaveBeenCalled()
  })

  it('asks the account choices once, and a cancel leaves child runs off', async () => {
    const user = userEvent.setup()
    const client = show()
    await waitFor(() => expect(box(childRunsLabel)).toHaveProperty('disabled', false))
    expect(await screen.findByText(/8 busy at once, 3 levels deep, 100 per run tree/)).toBeDefined()

    await user.click(box(childRunsLabel))
    let dialog = await screen.findByRole('alertdialog', { name: 'Let your runs start child runs?' })
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }))
    expect(client.childSettings).toHaveBeenCalledTimes(1)
    expect(box(childRunsLabel).getAttribute('aria-checked')).toBe('false')

    vi.mocked(client.childSettings).mockResolvedValue({ ...off, enabled: true, shared_when_missing: true, configured: true })
    await user.click(box(childRunsLabel))
    dialog = await screen.findByRole('alertdialog')
    await user.click(within(dialog).getByRole('checkbox', { name: new RegExp(sharedLabel) }))
    await user.click(within(dialog).getByRole('button', { name: 'Turn on' }))
    expect(client.childSettings).toHaveBeenLastCalledWith({ enabled: true, fallback: true, shared_when_missing: true })
    await waitFor(() => expect(box(childRunsLabel).getAttribute('aria-checked')).toBe('true'))
    expect(box(sharedLabel).getAttribute('aria-checked')).toBe('true')

    vi.mocked(client.childSettings).mockResolvedValue({ ...off, enabled: false, shared_when_missing: true, configured: true })
    await user.click(box(childRunsLabel))
    expect(screen.queryByRole('alertdialog')).toBeNull()
    expect(client.childSettings).toHaveBeenLastCalledWith({ enabled: false })
    await waitFor(() => expect(box(childRunsLabel).getAttribute('aria-checked')).toBe('false'))
  })

  it('turns a configured setting on without asking and shows what the server holds after a refusal', async () => {
    const user = userEvent.setup()
    const client = show(['*'], { childSettings: vi.fn(async () => ({ ...off, configured: true })) })
    await waitFor(() => expect(box(fallbackLabel)).toHaveProperty('disabled', false))
    vi.mocked(client.childSettings).mockRejectedValueOnce(new ApiError(403, 'child runs are disabled on this server'))
    await user.click(box(fallbackLabel))
    expect(client.childSettings).toHaveBeenCalledWith({ fallback: false })
    expect((await screen.findByRole('alert')).textContent).toContain('child runs are disabled on this server')
    expect(client.childSettings).toHaveBeenLastCalledWith({})
    expect(box(fallbackLabel).getAttribute('aria-checked')).toBe('true')

    await user.click(box(childRunsLabel))
    expect(screen.queryByRole('alertdialog')).toBeNull()
    expect(client.childSettings).toHaveBeenLastCalledWith({ enabled: true })
  })
})
