import { act, fireEvent, render, screen } from '@testing-library/react'
import { GitHubConnection } from '@/components/github-connection'
import type { GitHubOAuthResult } from '@/lib/types'
import { useStore } from '@/store'
import { bob, fakeApi, serverInfo } from '@/test/fixtures'

const pending: GitHubOAuthResult = { state: 'pending', session_id: 'attempt-one', user_code: 'ABCD-EFGH', verification_url: 'https://github.com/login/device' }

beforeEach(() => {
  useStore.setState({ info: serverInfo, identityKey: 'server:alice', connectionEpoch: 0 })
})
afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals(); vi.restoreAllMocks() })

describe('GitHub connection', () => {
  it('completes authorization automatically and removes the code without a manual completion action', async () => {
    vi.useFakeTimers()
    const client = fakeApi({ githubOAuthStatus: vi.fn().mockResolvedValueOnce({ state: 'disconnected' }).mockResolvedValueOnce({ state: 'finishing', session_id: pending.session_id }).mockResolvedValue({ state: 'connected', login: 'alice-gh' }) })
    const onConnected = vi.fn()
    render(<GitHubConnection client={client} onConnected={onConnected} />)
    await act(async () => {})
    fireEvent.click(screen.getByRole('button', { name: 'Connect GitHub' }))
    await act(async () => {})
    expect(screen.getByText(pending.user_code!)).toBeTruthy()
    await act(async () => { await vi.advanceTimersByTimeAsync(2000) })
    expect(screen.getByText('Finishing GitHub setup…')).toBeTruthy()
    await act(async () => { await vi.advanceTimersByTimeAsync(2000) })
    expect(screen.getByText('Connected as alice-gh')).toBeTruthy()
    expect(screen.queryByText(pending.user_code!)).toBeNull()
    expect(onConnected).toHaveBeenCalledExactlyOnceWith('alice-gh')
    const calls = vi.mocked(client.githubOAuthStatus).mock.calls.length
    await act(async () => { await vi.advanceTimersByTimeAsync(10000) })
    expect(client.githubOAuthStatus).toHaveBeenCalledTimes(calls)
  })

  it('keeps OAuth actions from submitting an enclosing source configuration form', async () => {
    const submit = vi.fn()
    const client = fakeApi({
      githubOAuthStatus: vi.fn().mockResolvedValueOnce({ state: 'disconnected' }).mockResolvedValue({ state: 'connected', login: 'alice-gh' }),
      githubOAuthStart: vi.fn().mockResolvedValueOnce(pending).mockResolvedValue({ state: 'connected', login: 'alice-gh' }),
    })
    vi.stubGlobal('navigator', { clipboard: { writeText: vi.fn(async () => {}) } })
    const open = vi.spyOn(window, 'open').mockReturnValue(null)
    render(<form onSubmit={(event) => { event.preventDefault(); submit() }}>
      <GitHubConnection client={client} />
      <button type="submit">Save source</button>
    </form>)
    await screen.findByText('GitHub is not connected.')
    fireEvent.click(screen.getByRole('button', { name: 'Connect GitHub' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Copy code and open GitHub' }))
    await screen.findByRole('button', { name: 'Code copied — open GitHub' })
    expect(open).toHaveBeenCalledExactlyOnceWith('https://github.com/login/device', '_blank', 'noopener,noreferrer')
    fireEvent.click(screen.getByRole('button', { name: 'Cancel connection' }))
    await screen.findByText('GitHub connection cancelled.')
    fireEvent.click(screen.getByRole('button', { name: 'Connect GitHub' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Check connection' }))
    await act(async () => {})
    expect(client.githubOAuthStart).toHaveBeenCalledTimes(2)
    expect(client.githubOAuthCancel).toHaveBeenCalledExactlyOnceWith(pending.session_id)
    expect(client.githubOAuthStatus).toHaveBeenCalledTimes(2)
    expect(submit).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'Save source' }))
    expect(submit).toHaveBeenCalledTimes(1)
  })

  it('keeps the safe GitHub link and visible code when copying fails', async () => {
    const client = fakeApi({ githubOAuthStatus: vi.fn(async () => ({ ...pending, verification_url: 'https://attacker.invalid/device' })) })
    vi.stubGlobal('navigator', { clipboard: { writeText: vi.fn(async () => { throw new Error('Clipboard permission denied') }) } })
    vi.spyOn(window, 'open').mockReturnValue(null)
    render(<GitHubConnection client={client} />)
    fireEvent.click(await screen.findByRole('button', { name: 'Copy code and open GitHub' }))
    expect((await screen.findByRole('alert')).textContent).toContain('Clipboard permission denied')
    expect(screen.getByText(pending.user_code!)).toBeTruthy()
    expect(screen.getByRole('link', { name: 'Open GitHub' }).getAttribute('href')).toBe('https://github.com/login/device')
  })

  it('cancels the current attempt and stops polling', async () => {
    vi.useFakeTimers()
    const client = fakeApi({ githubOAuthStatus: vi.fn(async () => pending) })
    render(<GitHubConnection client={client} />)
    await act(async () => {})
    fireEvent.click(screen.getByRole('button', { name: 'Cancel connection' }))
    await act(async () => {})
    expect(screen.getByText('GitHub connection cancelled.')).toBeTruthy()
    expect(screen.queryByText(pending.user_code!)).toBeNull()
    await act(async () => { await vi.advanceTimersByTimeAsync(10000) })
    expect(client.githubOAuthStatus).toHaveBeenCalledTimes(1)
    expect(screen.getByRole('button', { name: 'Connect GitHub' })).toHaveProperty('disabled', false)
  })

  it.each(['member', 'server', 'client', 'unmount'] as const)('ignores authorization completion after a %s change', async (change) => {
    const old = Promise.withResolvers<GitHubOAuthResult>()
    const client = fakeApi({ githubOAuthStatus: vi.fn().mockReturnValueOnce(old.promise).mockResolvedValue({ state: 'disconnected' }) })
    const onConnected = vi.fn()
    const view = render(<GitHubConnection client={client} onConnected={onConnected} />)
    if (change === 'unmount') view.unmount()
    else if (change === 'client') view.rerender(<GitHubConnection client={fakeApi()} onConnected={onConnected} />)
    else act(() => {
      if (change === 'member') useStore.setState({ info: { ...serverInfo, member: bob } })
      else {
        useStore.setState({ identityKey: 'other:alice' })
        useStore.setState({ identityKey: 'server:alice' })
      }
    })
    await act(async () => { old.resolve({ state: 'connected', login: 'old-account' }) })
    expect(screen.queryByText('Connected as old-account')).toBeNull()
    expect(onConnected).not.toHaveBeenCalled()
  })

  it('shows the actual OAuth error and lets an administrator begin a fresh attempt', async () => {
    const client = fakeApi({ githubOAuthStart: vi.fn().mockResolvedValueOnce({ state: 'failed', error: 'gh: authorization denied by organization policy' }).mockResolvedValueOnce(pending) })
    render(<GitHubConnection client={client} />)
    await screen.findByText('GitHub is not connected.')
    fireEvent.click(screen.getByRole('button', { name: 'Connect GitHub' }))
    expect((await screen.findByRole('alert')).textContent).toBe('gh: authorization denied by organization policy')
    fireEvent.click(screen.getByRole('button', { name: 'Connect GitHub' }))
    expect(await screen.findByText(pending.user_code!)).toBeTruthy()
  })

  it('does not expose connection controls or read administrator OAuth status to a nonadmin', () => {
    useStore.setState({ info: { ...serverInfo, member: bob } })
    const client = fakeApi()
    render(<GitHubConnection client={client} />)
    expect(screen.queryByRole('region', { name: 'GitHub connection' })).toBeNull()
    expect(client.githubOAuthStatus).not.toHaveBeenCalled()
  })
})
