import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ApiError } from '@/lib/api'
import type { AssistantAccount } from '@/lib/assistant-types'
import { AssistantSection, autoApproveLabel, autoApproveWarning } from '@/routes/settings/assistant'
import { useStore } from '@/store'
import { alice, fakeApi, serverInfo } from '@/test/fixtures'

/** The account check's whole line, which mixes plain text and code. */
const line = (text: string) => (_: string, element: Element | null) => element?.getAttribute('role') === 'status' && element.textContent === text

function show(methods = ['*'], over: Parameters<typeof fakeApi>[0] = {}) {
  useStore.setState({
    ...useStore.getInitialState(),
    info: serverInfo,
    members: { [alice.id]: alice },
    capabilities: { gateway: 'remote', methods, ws: [] },
  }, true)
  const client = fakeApi(over)
  render(<AssistantSection client={client} />)
  return client
}

const toggle = () => screen.getByRole('checkbox', { name: autoApproveLabel })

describe('assistant settings', () => {
  it('is hidden where the server has no assistant', () => {
    const client = show(['run.list'])
    expect(screen.queryByRole('checkbox', { name: autoApproveLabel })).toBeNull()
    expect(client.assistantSettings).not.toHaveBeenCalled()
  })

  it('warns before turning auto-approval on and turns it off without asking', async () => {
    const user = userEvent.setup()
    const client = show()
    await waitFor(() => expect(toggle()).toHaveProperty('disabled', false))
    expect(client.assistantSettings).toHaveBeenCalledWith()
    expect(toggle().getAttribute('aria-checked')).toBe('false')

    await user.click(toggle())
    let dialog = await screen.findByRole('alertdialog', { name: 'Let the assistant act without asking?' })
    expect(within(dialog).getByText(autoApproveWarning)).toBeDefined()
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }))
    expect(client.assistantSettings).toHaveBeenCalledTimes(1)
    expect(toggle().getAttribute('aria-checked')).toBe('false')

    vi.mocked(client.assistantSettings).mockResolvedValue({ auto_approve: true, suggest_auto_approve: false, suggestion_off: false, share_view: true })
    await user.click(toggle())
    dialog = await screen.findByRole('alertdialog')
    await user.click(within(dialog).getByRole('button', { name: 'Turn on' }))
    expect(client.assistantSettings).toHaveBeenLastCalledWith({ auto_approve: true })
    await waitFor(() => expect(toggle().getAttribute('aria-checked')).toBe('true'))
    expect(screen.queryByRole('alertdialog')).toBeNull()

    vi.mocked(client.assistantSettings).mockResolvedValue({ auto_approve: false, suggest_auto_approve: false, suggestion_off: false, share_view: true })
    await user.click(toggle())
    expect(screen.queryByRole('alertdialog')).toBeNull()
    expect(client.assistantSettings).toHaveBeenLastCalledWith({ auto_approve: false })
    await waitFor(() => expect(toggle().getAttribute('aria-checked')).toBe('false'))
  })

  it('follows auto-approval turned on from the assistant', async () => {
    const user = userEvent.setup()
    const client = show()
    act(() => useStore.getState().setAssistantStatus({ state: 'idle', agent: 'claude', agents: [], confirmations: [], auto_approve: false, suggest_auto_approve: true, share_view: true }))
    await waitFor(() => expect(toggle()).toHaveProperty('disabled', false))
    act(() => useStore.getState().setAssistantStatus({ ...useStore.getState().assistantStatus!, auto_approve: true, suggest_auto_approve: false }))
    expect(toggle().getAttribute('aria-checked')).toBe('true')

    vi.mocked(client.assistantSettings).mockResolvedValue({ auto_approve: false, suggest_auto_approve: false, suggestion_off: false, share_view: true })
    await user.click(toggle())
    expect(screen.queryByRole('alertdialog')).toBeNull()
    expect(client.assistantSettings).toHaveBeenLastCalledWith({ auto_approve: false })
    await waitFor(() => expect(toggle().getAttribute('aria-checked')).toBe('false'))
  })

  it('shows the server error and stays as it was', async () => {
    const user = userEvent.setup()
    const client = show()
    await waitFor(() => expect(toggle()).toHaveProperty('disabled', false))
    vi.mocked(client.assistantSettings).mockRejectedValue(new ApiError(503, 'assistant.settings: the assistant is not configured on this server'))
    await user.click(toggle())
    const dialog = await screen.findByRole('alertdialog')
    await user.click(within(dialog).getByRole('button', { name: 'Turn on' }))
    expect(await within(dialog).findByText('the assistant is not configured on this server', { exact: false })).toBeDefined()
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }))
    expect(toggle().getAttribute('aria-checked')).toBe('false')
  })

  it('signs an agent in again for the assistant, as when its sign-in expired', async () => {
    const user = userEvent.setup()
    const client = show()
    act(() => useStore.getState().setAssistantStatus({ state: 'idle', agent: 'claude', agents: [{ name: 'claude', display_name: 'Claude Code', ready: true }], confirmations: [], auto_approve: false, suggest_auto_approve: false, share_view: true }))
    await user.click(await screen.findByRole('button', { name: 'Sign in again' }))
    expect(screen.getByRole('region', { name: 'Sign in to Claude Code' })).toBeDefined()
    await waitFor(() => expect(client.assistantLoginSocket).toHaveBeenCalledWith('claude'))
    await user.click(screen.getByRole('button', { name: 'Close' }))
    expect(screen.queryByRole('region', { name: 'Sign in to Claude Code' })).toBeNull()
  })

  it('says whether each signed-in agent uses the same account as your sessions', async () => {
    const accounts: Record<string, AssistantAccount[]> = {
      claude: [{ agent: 'claude', state: 'same', email: 'alice@example.test' }, { agent: 'claude', state: 'expired' }],
      codex: [{ agent: 'codex', state: 'other', environment: 'ChatGPT account acct-other' }, { agent: 'codex', state: 'error', error: 'your environment has no ~/.codex/auth.json' }],
    }
    const signedIn = { state: 'idle' as const, agent: 'claude', agents: [{ name: 'claude', display_name: 'Claude Code', ready: true }, { name: 'codex', display_name: 'Codex', ready: true }], confirmations: [], auto_approve: false, suggest_auto_approve: false, share_view: true }
    const client = show(['*'], { assistantAccount: vi.fn(async (agent: string) => accounts[agent].shift()!) })
    act(() => useStore.getState().setAssistantStatus(signedIn))
    expect(await screen.findByText(line('Same Claude account as your sessions (alice@example.test).'))).toBeDefined()
    expect(await screen.findByText(line('Different Codex account: the assistant does not use the account your sessions use. Your sessions: ChatGPT account acct-other.'))).toBeDefined()

    cleanup()
    show(['*'], { assistantAccount: client.assistantAccount })
    act(() => useStore.getState().setAssistantStatus(signedIn))
    expect(await screen.findByText(line("Can't compare: your environment's Claude token has expired. Start a session to refresh it."))).toBeDefined()
    expect(await screen.findByText(line("Can't compare with your sessions' Codex account: your environment has no ~/.codex/auth.json"))).toBeDefined()
  })

  it('reads the account again once the first start records it', async () => {
    const accounts: AssistantAccount[] = [{ agent: 'claude', state: 'unrecorded' }, { agent: 'claude', state: 'same' }]
    const signedIn = { state: 'idle' as const, agent: 'claude', agents: [{ name: 'claude', display_name: 'Claude Code', ready: true }], confirmations: [], auto_approve: false, suggest_auto_approve: false, share_view: true }
    const client = show(['*'], { assistantAccount: vi.fn(async () => accounts.shift()!) })
    act(() => useStore.getState().setAssistantStatus(signedIn))
    await waitFor(() => expect(client.assistantAccount).toHaveBeenCalledTimes(1))
    act(() => useStore.getState().setAssistantStatus({ ...signedIn, state: 'running' }))
    expect(await screen.findByText(line('Same Claude account as your sessions.'))).toBeDefined()
    act(() => useStore.getState().setAssistantStatus({ ...signedIn, state: 'idle' }))
    act(() => useStore.getState().setAssistantStatus({ ...signedIn, state: 'running' }))
    expect(client.assistantAccount).toHaveBeenCalledTimes(2)
  })

  it('does not ask a server without the account check', async () => {
    const client = show(['assistant.status', 'assistant.settings', 'run.launch'])
    act(() => useStore.getState().setAssistantStatus({ state: 'idle', agent: 'claude', agents: [{ name: 'claude', display_name: 'Claude Code', ready: true }], confirmations: [], auto_approve: false, suggest_auto_approve: false, share_view: true }))
    expect(await screen.findByText('Signed in for the assistant.')).toBeDefined()
    expect(client.assistantAccount).not.toHaveBeenCalled()
  })

  it('shows nothing when a gateway that lists every method reaches a server without the check', async () => {
    const client = show(['*'], { assistantAccount: vi.fn(async () => { throw new ApiError(404, 'assistant.account: method not found: assistant.account', -32601) }) })
    act(() => useStore.getState().setAssistantStatus({ state: 'idle', agent: 'claude', agents: [{ name: 'claude', display_name: 'Claude Code', ready: true }], confirmations: [], auto_approve: false, suggest_auto_approve: false, share_view: true }))
    await waitFor(() => expect(client.assistantAccount).toHaveBeenCalled())
    expect(screen.queryByText(/Can't compare/)).toBeNull()
  })

  it('lists what auto-approval covers', () => {
    show()
    const scope = screen.getByLabelText('What auto-approval covers')
    for (const text of ['Without a card', 'Always asks', 'Not affected', 'Messaging or stopping another member’s session.', /shell and file tools/]) {
      expect(within(scope).getByText(text)).toBeDefined()
    }
  })

  it('shows a failed read verbatim and reads again on Try again', async () => {
    const user = userEvent.setup()
    show(['*'], {
      assistantSettings: vi.fn().mockRejectedValueOnce(new Error('database is locked')).mockResolvedValue({ auto_approve: true, suggest_auto_approve: false, suggestion_off: false, share_view: true }),
    })
    expect((await screen.findByRole('alert')).textContent).toContain('database is locked')
    expect(toggle()).toHaveProperty('disabled', true)
    await user.click(screen.getByRole('button', { name: 'Try again' }))
    await waitFor(() => expect(toggle().getAttribute('aria-checked')).toBe('true'))
    expect(screen.queryByRole('alert')).toBeNull()
  })
})
