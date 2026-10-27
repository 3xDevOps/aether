import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { LaunchDefaultsResult } from '@/lib/types'
import { LaunchDefaultsSection } from '@/routes/settings/launch'
import { useStore } from '@/store'
import { useLaunchDefaults } from '@/store/launch-defaults'
import { alice, bob, fakeApi, serverInfo } from '@/test/fixtures'
import { pickOption } from '@/test/select'

const saved: LaunchDefaultsResult = {
  revision: 4,
  source: 'assistant',
  updated_at: '2026-10-20T09:00:00Z',
  defaults: { agent: 'claude', agents: { claude: { mode: 'acp', model: 'opus' }, codex: { mode: 'tui' } }, account_member_id: bob.id, fallback: { policy: 'off' } },
  invalid: [{ field: 'account_member_id', value: bob.id, reason: `permission denied: member ${bob.id} has not shared their account with you` }],
}

function show(methods = ['*'], result: LaunchDefaultsResult = saved) {
  useStore.setState({ ...useStore.getInitialState(), info: serverInfo, capabilities: { gateway: 'remote', methods, ws: [] } }, true)
  useLaunchDefaults.setState({ result: null, error: null })
  const client = fakeApi()
  vi.mocked(client.launchDefaultsGet).mockResolvedValue(result)
  vi.mocked(client.accountList).mockResolvedValue({ accounts: [alice, bob], shared_with: [] })
  render(<LaunchDefaultsSection client={client} />)
  return client
}

describe('saved launch defaults settings', () => {
  it('is hidden where the server keeps none', () => {
    const client = show(['run.list'])
    expect(screen.queryByText('Saved defaults')).toBeNull()
    expect(client.launchDefaultsGet).not.toHaveBeenCalled()
  })

  it('shows each saved choice, who saved it, and an unusable one with its reason', async () => {
    show()
    expect(await screen.findByText(/Saved by the assistant/)).toBeDefined()
    const list = screen.getByRole('list', { name: 'Saved agent choices' })
    expect(within(list).getAllByRole('listitem').map((item) => item.textContent)).toEqual(['claude · Enhanced · opusForget', 'codex · Standard · Default modelForget'])
    expect(screen.getByText('Not usable now')).toBeDefined()
    expect(screen.getByText(new RegExp(`has not shared their account with you`))).toBeDefined()
  })

  it('forgets one agent, changes the account, and clears everything after confirming', async () => {
    const user = userEvent.setup()
    const client = show()
    await screen.findByRole('list', { name: 'Saved agent choices' })

    await user.click(screen.getByRole('button', { name: 'Forget claude' }))
    await waitFor(() => expect(client.launchDefaultsSet).toHaveBeenCalledWith({
      defaults: { agent: '', agents: { codex: { mode: 'tui' } }, account_member_id: bob.id, fallback: { policy: 'off' } },
      revision: 4,
    }))

    await pickOption(screen.getByRole('combobox', { name: 'Account' }), 'Your own account')
    await waitFor(() => expect(vi.mocked(client.launchDefaultsSet).mock.calls[1][0].defaults?.account_member_id).toBe(''))

    await user.click(screen.getByRole('button', { name: 'Clear' }))
    await user.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Clear' }))
    await waitFor(() => expect(client.launchDefaultsSet).toHaveBeenLastCalledWith({ clear: true, revision: 6 }))
  })

  it('says nothing is saved yet', async () => {
    show(['*'], { revision: 0, defaults: {} })
    expect(await screen.findByText(/Nothing saved yet/)).toBeDefined()
    expect(screen.queryByRole('button', { name: 'Clear' })).toBeNull()
  })
})
