import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { SignedOutAlert } from '@/components/shell/signed-out-alert'
import { api } from '@/lib/api'
import { useStore } from '@/store'
import { agentInfo, alice, run, runRecords, serverInfo } from '@/test/fixtures'

const fileProblem = '~/.claude/.credentials.json: Claude OAuth access token is missing or invalid'
const refusal = 'the agent refused: Authentication required'
const claude = (over: Parameters<typeof agentInfo>[0] = {}) => agentInfo({ display_name: 'Claude Code', login_found: true, ...over })

beforeEach(() => {
  useStore.setState({ info: serverInfo, identityKey: 'alice', agentList: null, runs: {}, route: { name: 'board', params: {} } })
})

afterEach(() => {
  vi.restoreAllMocks()
  vi.useRealTimers()
})

describe('SignedOutAlert', () => {
  it('names the signed-out agent, shows the server error verbatim, and sends the member to Agents', async () => {
    vi.spyOn(api, 'agentList').mockResolvedValue([claude({ login_problem: fileProblem, login_blocks_launch: true }), agentInfo({ name: 'codex', display_name: 'Codex' })])
    render(<SignedOutAlert />)
    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toContain('Claude Code is signed out')
    expect(alert.textContent).toContain(fileProblem)
    fireEvent.click(screen.getByRole('button', { name: 'Sign in on Agents' }))
    expect(useStore.getState().route.name).toBe('agents')
  })

  it('lists each signed-out agent in one alert', async () => {
    vi.spyOn(api, 'agentList').mockResolvedValue([claude({ login_problem: fileProblem, login_blocks_launch: true }), agentInfo({ name: 'codex', display_name: 'Codex', login_problem: refusal })])
    render(<SignedOutAlert />)
    const alert = await screen.findByRole('alert')
    expect(screen.getAllByRole('alert')).toHaveLength(1)
    expect(within(alert).getAllByRole('listitem').map((item) => item.textContent)).toEqual([
      `Claude Code is signed out${fileProblem}`,
      `Codex's sign-in was refused${refusal}`,
    ])
    expect(within(alert).getAllByRole('button').map((button) => button.getAttribute('aria-label') || button.textContent)).toEqual(['Sign in on Agents', 'Dismiss notification'])
  })

  it('hides the alert until the shell is mounted again', async () => {
    vi.spyOn(api, 'agentList').mockResolvedValue([claude({ login_problem: fileProblem })])
    const { unmount } = render(<SignedOutAlert />)
    const alert = await screen.findByRole('alert')
    fireEvent.click(within(alert).getByRole('button', { name: 'Dismiss notification' }))
    expect(screen.queryByRole('alert')).toBeNull()

    unmount()
    render(<SignedOutAlert />)
    expect(await screen.findByRole('alert')).toBeDefined()
  })

  it('stays out of the way where the finding already shows beside its own sign-in action', async () => {
    const list = vi.spyOn(api, 'agentList').mockResolvedValue([claude({ login_problem: refusal })])
    useStore.setState({ route: { name: 'agents', params: {} } })
    const { unmount } = render(<SignedOutAlert />)
    await waitFor(() => expect(list).toHaveBeenCalled())
    expect(screen.queryByRole('alert')).toBeNull()
    unmount()

    const reason = 'enhanced turn failed: acphost: session/prompt: {"code":-32000,"message":"Authentication required"}; Alice\'s Claude Code login is signed out'
    list.mockResolvedValue([claude({ login_problem: refusal }), agentInfo({ name: 'codex', display_name: 'Codex', login_problem: fileProblem, login_blocks_launch: true })])
    useStore.setState({
      agentList: null,
      route: { name: 'run', params: { runId: 'run_1' } },
      shownRunView: { runId: 'run_1', view: 'session' },
      runs: runRecords(run({ id: 'run_1', harness: 'claude', status: 'needs-attention', member_id: alice.id, reason }), run({ id: 'run_2', harness: 'claude', status: 'failed', member_id: alice.id, reason })),
    })
    render(<SignedOutAlert />)
    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toContain('Codex is signed out')
    expect(alert.textContent).not.toContain('Claude Code')
    act(() => { useStore.setState({ shownRunView: { runId: 'run_1', view: 'terminal' } }) })
    expect(alert.textContent).toContain("Claude Code's sign-in was refused")
    // A finished run's Chat shows no sign-in callout.
    act(() => { useStore.setState({ route: { name: 'run', params: { runId: 'run_2' } }, shownRunView: { runId: 'run_2', view: 'session' } }) })
    expect(alert.textContent).toContain("Claude Code's sign-in was refused")
  })

  it('shows nothing for a working login or an agent that is not installed', async () => {
    const list = vi.spyOn(api, 'agentList').mockResolvedValue([claude(), agentInfo({ name: 'codex', installed: false, login_problem: fileProblem })])
    render(<SignedOutAlert />)
    await waitFor(() => expect(list).toHaveBeenCalled())
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('goes away on the next read once the server stops reporting the problem', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const list = vi.spyOn(api, 'agentList').mockResolvedValue([claude({ login_problem: fileProblem })])
    render(<SignedOutAlert />)
    await screen.findByRole('alert')
    list.mockResolvedValue([claude()])
    await act(async () => { await vi.advanceTimersByTimeAsync(60_000) })
    await waitFor(() => expect(screen.queryByRole('alert')).toBeNull())
  })

  it('reads again on window focus', async () => {
    const list = vi.spyOn(api, 'agentList').mockResolvedValue([claude()])
    render(<SignedOutAlert />)
    await waitFor(() => expect(list).toHaveBeenCalledTimes(1))
    list.mockResolvedValue([claude({ login_problem: refusal })])
    act(() => { window.dispatchEvent(new Event('focus')) })
    expect((await screen.findByRole('alert')).textContent).toContain(refusal)
  })

  it("reads again when one of the member's runs fails a turn", async () => {
    const list = vi.spyOn(api, 'agentList').mockResolvedValue([claude()])
    render(<SignedOutAlert />)
    await waitFor(() => expect(list).toHaveBeenCalledTimes(1))
    list.mockResolvedValue([claude({ login_problem: refusal })])
    act(() => {
      useStore.setState({ runs: runRecords(run({ id: 'run_1', member_id: alice.id, reason: `enhanced turn failed: acphost: session/prompt: {"code":-32000,"message":"Authentication required"}; Alice's Claude Code login is signed out` })) })
    })
    expect((await screen.findByRole('alert')).textContent).toContain(refusal)
    expect(list).toHaveBeenCalledTimes(2)
  })
})
