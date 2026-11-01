import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ApiError } from '@/lib/api'
import { AssistantSection, autoApproveLabel, autoApproveWarning } from '@/routes/settings/assistant'
import { useStore } from '@/store'
import { alice, fakeApi, serverInfo } from '@/test/fixtures'

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
