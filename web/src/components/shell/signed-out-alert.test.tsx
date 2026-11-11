import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { SignedOutAlert } from '@/components/shell/signed-out-alert'
import { api } from '@/lib/api'
import { useStore } from '@/store'
import { agentInfo, alice, run, runRecords, serverInfo } from '@/test/fixtures'

const fileProblem = '~/.claude/.credentials.json: Claude OAuth access token is missing or invalid'
const refusal = 'the agent refused: acphost: session/prompt: {"code":-32000,"message":"Authentication required"}'
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
    expect(screen.queryByRole('button', { name: 'Dismiss' })).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Sign in on Agents' }))
    expect(useStore.getState().route.name).toBe('agents')
  })

  it('lists each signed-out agent in one alert', async () => {
    vi.spyOn(api, 'agentList').mockResolvedValue([claude({ login_problem: fileProblem }), agentInfo({ name: 'codex', display_name: 'Codex', login_problem: refusal })])
    render(<SignedOutAlert />)
    const alert = await screen.findByRole('alert')
    expect(screen.getAllByRole('alert')).toHaveLength(1)
    expect(alert.textContent).toContain('Claude Code and Codex are signed out')
    expect(alert.textContent).toContain(`Claude Code: ${fileProblem}`)
    expect(alert.textContent).toContain(`Codex: ${refusal}`)
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
