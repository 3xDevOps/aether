import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ApiError } from '@/lib/api'
import type { ChildSettings } from '@/lib/types'
import { ChildRunsSection, childRunsLabel } from '@/routes/settings/children'
import { useStore } from '@/store'
import { useChildSettings } from '@/store/children'
import { alice, fakeApi, serverInfo, workspace } from '@/test/fixtures'

const off: ChildSettings = { enabled: false }

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

  it('turns child runs off and on, and shows what the server holds after a refusal', async () => {
    const user = userEvent.setup()
    const client = show(['*'], { childSettings: vi.fn(async () => ({ enabled: true })) })
    await waitFor(() => expect(box(childRunsLabel)).toHaveProperty('disabled', false))
    expect(await screen.findByText(/8 subsessions busy at once per parent session \(more queue\)/)).toBeDefined()
    expect(box(childRunsLabel).getAttribute('aria-checked')).toBe('true')

    vi.mocked(client.childSettings).mockResolvedValueOnce(off)
    await user.click(box(childRunsLabel))
    expect(client.childSettings).toHaveBeenLastCalledWith({ enabled: false })
    await waitFor(() => expect(box(childRunsLabel).getAttribute('aria-checked')).toBe('false'))

    vi.mocked(client.childSettings).mockRejectedValueOnce(new ApiError(403, 'subsessions are disabled on this server'))
    await user.click(box(childRunsLabel))
    expect(client.childSettings).toHaveBeenCalledWith({ enabled: true })
    expect((await screen.findByRole('alert')).textContent).toContain('subsessions are disabled on this server')
    expect(client.childSettings).toHaveBeenLastCalledWith({})
  })
})
