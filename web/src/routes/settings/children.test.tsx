import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ApiError } from '@/lib/api'
import type { ChildChoice, ChildSettings } from '@/lib/types'
import { ChildRunsSection, childRunsLabel } from '@/routes/settings/children'
import { useStore } from '@/store'
import { useChildSettings } from '@/store/children'
import { alice, bob, fakeApi, serverInfo, workspace } from '@/test/fixtures'

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

  it('shows why no agents are listed when every account fails to list them', async () => {
    show(['*'], { childSettings: vi.fn(async () => ({ enabled: true, off: [], choices: [], choices_error: 'list the agents on account mem_bob: decode harness definition: boom' })) })
    expect((await screen.findByRole('alert')).textContent).toBe('list the agents on account mem_bob: decode harness definition: boom')
    expect(screen.queryByRole('button', { name: /^Subsession agents/ })).toBeNull()
  })

  it('turns an agent, or one account of it, off for subsessions from the agents menu', async () => {
    const user = userEvent.setup()
    const choices: ChildChoice[] = [
      { account_member_id: alice.id, account_name: 'Alice', harness: 'claude', display_name: 'Claude Code', status: 'ready', detail: "Alice's claude login may be signed out: token expired" },
      { account_member_id: bob.id, account_name: 'Bob', harness: 'claude', display_name: 'Claude Code', status: 'ready' },
      { account_member_id: alice.id, account_name: 'Alice', harness: 'codex', display_name: 'Codex', status: 'ready' },
      { account_member_id: alice.id, account_name: 'Alice', harness: 'gemini', display_name: 'Gemini', status: 'not-installed' },
    ]
    const client = show(['*'], { childSettings: vi.fn(async () => ({ enabled: true, off: [], choices })) })
    const trigger = await screen.findByRole('button', { name: 'Subsession agents: All on, 1 needs attention' })
    await user.click(trigger)
    const menu = screen.getByRole('menu')
    const item = (name: RegExp) => within(menu).getByRole('menuitemcheckbox', { name })
    expect(within(menu).getAllByRole('menuitemcheckbox').map((el) => el.textContent)).toEqual(['Claude Code', 'Your accountSigned out', "Bob's account", 'Codex'])

    vi.mocked(client.childSettings).mockResolvedValueOnce({ enabled: true, off: [{ harness: 'claude', account_member_id: alice.id }], choices })
    await user.click(item(/Your account/))
    expect(client.childSettings).toHaveBeenLastCalledWith({ choice: { harness: 'claude', account_member_id: alice.id, on: false } })
    await waitFor(() => expect(item(/Your account/).getAttribute('aria-checked')).toBe('false'))
    expect(item(/Bob's account/).getAttribute('aria-checked')).toBe('true')

    vi.mocked(client.childSettings).mockResolvedValueOnce({ enabled: true, off: [{ harness: 'claude', account_member_id: alice.id }, { harness: 'codex' }], choices })
    await user.click(item(/Codex/))
    expect(client.childSettings).toHaveBeenLastCalledWith({ choice: { harness: 'codex' } })
    await user.keyboard('{Escape}')
    expect(await screen.findByRole('button', { name: 'Subsession agents: 2 off' })).toBeDefined()
  })
})
