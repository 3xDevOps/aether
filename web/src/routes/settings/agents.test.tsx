import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { SettingsRoute } from '@/routes/settings'
import { search, visibleEntries } from '@/routes/settings/registry'
import { useStore } from '@/store'
import { fakeApi, serverInfo } from '@/test/fixtures'

function seed(methods = ['*']) {
  useStore.setState({
    ...useStore.getInitialState(),
    info: serverInfo,
    capabilities: { gateway: 'remote', methods, ws: [] },
    hydrated: true,
    identityKey: 'alice',
    route: { name: 'settings', params: {} },
  }, true)
}

describe('agents settings', () => {
  it('shows each agent with its sign-in state and opens the Agents page', async () => {
    const user = userEvent.setup()
    const client = fakeApi()
    vi.mocked(client.agentList).mockResolvedValue([
      { source: 'shipped' as const, name: 'claude', display_name: 'Claude Code', installed: true, login_found: false },
      { source: 'shipped' as const, name: 'codex', display_name: 'Codex', installed: true, login_found: true },
    ])
    seed()
    render(<SettingsRoute params={{ section: 'agents' }} client={client} />)

    const list = await screen.findByRole('list', { name: 'Agents' })
    expect(within(list).getByText('Installed · No login found')).toBeDefined()
    expect(within(list).getByText('Installed · Login found')).toBeDefined()
    await user.click(screen.getByRole('button', { name: 'Log in Claude Code' }))
    await waitFor(() => expect(useStore.getState().route.name).toBe('agents'))
  })

  it('is found by searching for sign in, login or Claude, and absent without agent.list', () => {
    const gates = (methods: string[]) => ({
      cap: { hasMethod: (m: string) => methods.includes(m), hasLocal: () => false, hasWS: () => false },
      admin: false, assistant: false, onboarding: false, member: true, workspace: true,
    }) as unknown as Parameters<typeof visibleEntries>[0]
    const shown = visibleEntries(gates(['agent.list']))
    for (const query of ['sign in', 'login', 'claude', 'agents', 'install']) {
      expect(search(query, shown).map((entry) => entry.id), query).toContain('agents-list')
    }
    expect(visibleEntries(gates([])).some((entry) => entry.category === 'agents')).toBe(false)
  })
})
