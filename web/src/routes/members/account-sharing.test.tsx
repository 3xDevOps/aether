import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { useState } from 'react'
import { toast, Toaster } from 'sonner'
import { ApiError } from '@/lib/api'
import type { Member } from '@/lib/types'
import { MembersRoute } from '@/routes/members'
import { useStore, type RootState } from '@/store'
import { initialEnvTerminal } from '@/store/env-terminal'
import { optimisticDeadlineMs } from '@/store/optimistic'
import { alice, bob, fakeApi, serverInfo, vera, workspace } from '@/test/fixtures'

// The roster read answers with the members each test seeded.
const api = (over: Parameters<typeof fakeApi>[0] = {}) =>
  fakeApi({ memberList: vi.fn(async () => ({ members: Object.values(useStore.getState().members) })), ...over })

function seed(extra: Partial<RootState> = {}) {
  useStore.setState({
    workspaces: { [workspace.id]: workspace },
    activeWorkspace: workspace.id,
    members: { [alice.id]: alice, [bob.id]: bob },
    presence: [],
    info: serverInfo,
    capabilities: { gateway: 'remote', methods: ['*'], ws: ['events', 'attach'] },
    hydrated: true,
    hydrationError: null,
    route: { name: 'members', params: {} },
    ...extra,
  })
}

function held<T>() {
  const answer = { resolve: (_: T) => {}, reject: (_: Error) => {} }
  const promise = new Promise<T>((resolve, reject) => Object.assign(answer, { resolve, reject }))
  return { promise, answer }
}

describe('agent account sharing on Members', () => {
  it('dismisses an unanswered share\'s toast, raised after leaving Members, once the signed-in member changes, and its Retry sends nothing', async () => {
    const client = api({
      accountList: vi.fn(async () => ({ accounts: [alice], shared_with: [] })),
      accountShare: vi.fn(() => new Promise<unknown>(() => {})),
    })
    seed({ identityKey: 'alice' })
    let leave: () => void = () => {}
    function Host() {
      const [shown, setShown] = useState(true)
      leave = () => setShown(false)
      return <><Toaster />{shown && <MembersRoute params={{}} client={client} />}</>
    }
    render(<Host />)
    const share = await within(await screen.findByRole('region', { name: 'Roster' })).findByRole('button', { name: 'Share your agent account with Bob' })
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    try {
      fireEvent.click(share)
      act(() => leave())
      act(() => vi.advanceTimersByTime(optimisticDeadlineMs))
      await act(async () => {})
      act(() => vi.runOnlyPendingTimers())
    } finally {
      vi.useRealTimers()
    }
    const retry = await screen.findByRole('button', { name: 'Retry' })

    const dismiss = vi.spyOn(toast, 'dismiss')
    act(() => useStore.setState({ identityKey: 'bob' }))
    expect(dismiss).toHaveBeenCalled()
    fireEvent.click(retry)
    expect(client.accountShare).toHaveBeenCalledTimes(1)
    dismiss.mockRestore()
  })

  it('flips a share and a revoke before the server answers', async () => {
    const share = held<unknown>()
    const revoke = held<unknown>()
    const client = api({
      accountList: vi.fn(async () => ({ accounts: [alice], shared_with: [] })),
      accountShare: vi.fn(() => share.promise),
      accountRevoke: vi.fn(() => revoke.promise),
    })
    seed()
    render(<MembersRoute params={{}} client={client} />)
    const sharing = within(await screen.findByRole('region', { name: 'Roster' }))

    fireEvent.click(await sharing.findByRole('button', { name: 'Share your agent account with Bob' }))
    expect(sharing.getByRole('button', { name: 'Stop sharing your agent account with Bob' })).toBeDefined()
    expect(sharing.getByRole('button', { name: 'Stop sharing your agent account with Bob' }).hasAttribute('disabled')).toBe(true)
    await act(async () => share.answer.resolve({}))
    fireEvent.click(sharing.getByRole('button', { name: 'Stop sharing your agent account with Bob' }))
    expect(sharing.getByRole('button', { name: 'Share your agent account with Bob' })).toBeDefined()
    expect(client.accountShare).toHaveBeenCalledWith(bob.id)
    expect(client.accountRevoke).toHaveBeenCalledWith(bob.id)
    expect(client.accountList).toHaveBeenCalledTimes(1)
  })

  it('keeps a share confirmed before the slower first list read returns', async () => {
    const list = held<{ accounts: Member[]; shared_with: Member[] }>()
    const client = api({
      accountList: vi.fn(() => list.promise),
      accountShare: vi.fn(async () => ({})),
    })
    seed()
    render(<MembersRoute params={{}} client={client} />)
    const sharing = within(await screen.findByRole('region', { name: 'Roster' }))

    fireEvent.click(await sharing.findByRole('button', { name: 'Share your agent account with Bob' }))
    await sharing.findByRole('button', { name: 'Stop sharing your agent account with Bob' })
    await act(async () => list.answer.resolve({ accounts: [alice], shared_with: [] }))
    expect(sharing.getByRole('button', { name: 'Stop sharing your agent account with Bob' })).toBeDefined()
  })

  it('shows a share the server answers after the deadline as shared', async () => {
    const share = held<unknown>()
    const client = api({
      accountList: vi.fn(async () => ({ accounts: [alice], shared_with: [] })),
      accountShare: vi.fn(() => share.promise),
    })
    seed()
    render(<MembersRoute params={{}} client={client} />)
    const sharing = within(await screen.findByRole('region', { name: 'Roster' }))
    const button = await sharing.findByRole('button', { name: 'Share your agent account with Bob' })
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    try {
      fireEvent.click(button)
      act(() => vi.advanceTimersByTime(optimisticDeadlineMs))
      await act(async () => {})
    } finally {
      vi.useRealTimers()
    }
    expect(sharing.getByRole('button', { name: 'Share your agent account with Bob' })).toBeDefined()

    await act(async () => share.answer.resolve({}))
    expect(sharing.getByRole('button', { name: 'Stop sharing your agent account with Bob' })).toBeDefined()
    expect(sharing.queryByRole('alert')).toBeNull()
  })

  it('rolls a timed-out share back to an earlier share the server answered meanwhile', async () => {
    const first = held<unknown>()
    const client = api({
      accountList: vi.fn(async () => ({ accounts: [alice], shared_with: [] })),
      accountShare: vi.fn().mockReturnValueOnce(first.promise).mockReturnValueOnce(new Promise(() => {})),
    })
    seed()
    render(<MembersRoute params={{}} client={client} />)
    const sharing = within(await screen.findByRole('region', { name: 'Roster' }))
    const button = await sharing.findByRole('button', { name: 'Share your agent account with Bob' })
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    try {
      fireEvent.click(button)
      act(() => vi.advanceTimersByTime(optimisticDeadlineMs))
      await act(async () => {})
      fireEvent.click(sharing.getByRole('button', { name: 'Share your agent account with Bob' }))
      await act(async () => first.answer.resolve({}))
      act(() => vi.advanceTimersByTime(optimisticDeadlineMs))
      await act(async () => {})
    } finally {
      vi.useRealTimers()
    }
    expect(sharing.getByRole('button', { name: 'Stop sharing your agent account with Bob' })).toBeDefined()
  })

  it('takes a refused share back and shows the error', async () => {
    const client = api({
      accountList: vi.fn(async () => ({ accounts: [alice], shared_with: [] })),
      accountShare: vi.fn().mockRejectedValue(new ApiError(200, 'account.share: cannot share an account with a member pending admin approval', -32002)),
    })
    seed()
    render(<MembersRoute params={{}} client={client} />)
    const sharing = within(await screen.findByRole('region', { name: 'Roster' }))

    fireEvent.click(await sharing.findByRole('button', { name: 'Share your agent account with Bob' }))
    expect((await sharing.findByRole('alert')).textContent).toBe('cannot share an account with a member pending admin approval')
    expect(sharing.getByRole('button', { name: 'Share your agent account with Bob' })).toBeDefined()
    expect(sharing.getByRole('button', { name: 'Share your agent account with Bob' }).hasAttribute('disabled')).toBe(false)
  })

  it('keeps one teammate\'s refused share on screen while a share for another starts', async () => {
    const client = api({
      accountList: vi.fn(async () => ({ accounts: [alice], shared_with: [] })),
      accountShare: vi.fn()
        .mockRejectedValueOnce(new ApiError(200, 'account.share: Bob is pending admin approval', -32002))
        .mockReturnValueOnce(new Promise(() => {})),
    })
    seed({ members: { [alice.id]: alice, [bob.id]: bob, [vera.id]: vera } })
    render(<MembersRoute params={{}} client={client} />)
    const sharing = within(await screen.findByRole('region', { name: 'Roster' }))
    const [shareBob, shareVera] = await sharing.findAllByRole('button', { name: /^Share your agent account with/ })

    fireEvent.click(shareBob)
    expect((await sharing.findByRole('alert')).textContent).toBe('Bob is pending admin approval')
    fireEvent.click(shareVera)
    await waitFor(() => expect(client.accountShare).toHaveBeenCalledTimes(2))
    expect(sharing.getByRole('alert').textContent).toBe('Bob is pending admin approval')
  })

  describe('after a first share', () => {
    const notice =
      "Your Environment was started before you shared, so a Claude Code login written there will not reach Bob's sessions until you stop it and open it again from Environment. Sessions you already have running keep the mounts they started with until they end."
    const sharing = (running: boolean) =>
      api({
        accountList: vi
          .fn()
          .mockResolvedValueOnce({ accounts: [alice], shared_with: [] })
          .mockResolvedValue({ accounts: [alice], shared_with: [bob] }),
        accountShare: vi.fn(async () => ({})),
        terminalStatus: vi.fn(async () => ({ running, tabs: running ? ['main'] : [] })),
        terminalStop: vi.fn(async () => ({})),
      })
    beforeEach(() => useStore.setState({ envTerminal: initialEnvTerminal }))

    it('offers to stop a terminal started before the share', async () => {
      const client = sharing(true)
      seed({ capabilities: { gateway: 'remote', methods: ['*'], ws: ['events', 'attach', 'terminal'] } })
      render(<MembersRoute params={{}} client={client} />)

      fireEvent.click(await screen.findByRole('button', { name: 'Share your agent account with Bob' }))
      const status = await screen.findByRole('status')
      expect(status.textContent).toContain(notice)

      fireEvent.click(within(status).getByRole('button', { name: 'Stop environment' }))
      const dialog = within(await screen.findByRole('alertdialog'))
      expect(dialog.getByText(/so does everything running in it/)).toBeDefined()
      expect(client.terminalStop).not.toHaveBeenCalled()
      fireEvent.click(dialog.getByRole('button', { name: 'Stop environment' }))

      await waitFor(() => expect(client.terminalStop).toHaveBeenCalled())
      await waitFor(() => expect(screen.queryByRole('status')).toBeNull())
      expect(useStore.getState().envTerminal.status?.running).toBe(false)
    })

    it('gives the advice for the share the server saved first, not the one pressed first', async () => {
      const bobShare = held<unknown>()
      const client = sharing(true)
      vi.mocked(client.accountShare).mockReturnValueOnce(bobShare.promise)
      seed({
        members: { [alice.id]: alice, [bob.id]: bob, [vera.id]: vera },
        capabilities: { gateway: 'remote', methods: ['*'], ws: ['events', 'attach', 'terminal'] },
      })
      render(<MembersRoute params={{}} client={client} />)

      const [shareBob, shareVera] = await screen.findAllByRole('button', { name: /^Share your agent account with/ })
      fireEvent.click(shareBob)
      fireEvent.click(shareVera)
      expect((await screen.findByRole('status')).textContent).toContain("will not reach Vera's sessions")
      await act(async () => bobShare.answer.resolve({}))
      expect(screen.getByRole('status').textContent).toContain("will not reach Vera's sessions")
    })

    it('shows the stop error in the confirmation', async () => {
      const client = sharing(true)
      vi.mocked(client.terminalStop).mockRejectedValue(new Error('stop container: daemon is down'))
      seed({ capabilities: { gateway: 'remote', methods: ['*'], ws: ['events', 'attach', 'terminal'] } })
      render(<MembersRoute params={{}} client={client} />)

      fireEvent.click(await screen.findByRole('button', { name: 'Share your agent account with Bob' }))
      fireEvent.click(within(await screen.findByRole('status')).getByRole('button', { name: 'Stop environment' }))
      const dialog = within(await screen.findByRole('alertdialog'))
      fireEvent.click(dialog.getByRole('button', { name: 'Stop environment' }))

      expect(await dialog.findByText('stop container: daemon is down')).toBeDefined()
    })

    it('still gives the advice when the terminal read after the share fails', async () => {
      const client = sharing(true)
      vi.mocked(client.terminalStatus).mockRejectedValue(new Error('terminal.status: gateway closed'))
      seed({ capabilities: { gateway: 'remote', methods: ['*'], ws: ['events', 'attach', 'terminal'] } })
      render(<MembersRoute params={{}} client={client} />)

      fireEvent.click(await screen.findByRole('button', { name: 'Share your agent account with Bob' }))
      const status = await screen.findByRole('status')
      expect(status.textContent).toContain(
        "Your Environment could not be checked. If it is open, it was started before you shared, so a Claude Code login written there will not reach Bob's sessions until you stop it and open it again from Environment. Sessions you already have running keep the mounts they started with until they end.",
      )
      expect(within(status).getByRole('button', { name: 'Stop environment' })).toBeDefined()
    })

    it('drops the advice after a stop, also when the terminal could not be checked', async () => {
      const client = sharing(true)
      vi.mocked(client.terminalStatus).mockRejectedValue(new Error('terminal.status: gateway closed'))
      seed({ capabilities: { gateway: 'remote', methods: ['*'], ws: ['events', 'attach', 'terminal'] } })
      render(<MembersRoute params={{}} client={client} />)

      fireEvent.click(await screen.findByRole('button', { name: 'Share your agent account with Bob' }))
      fireEvent.click(within(await screen.findByRole('status')).getByRole('button', { name: 'Stop environment' }))
      fireEvent.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Stop environment' }))

      await waitFor(() => expect(client.terminalStop).toHaveBeenCalled())
      await waitFor(() => expect(screen.queryByRole('status')).toBeNull())
    })

    it('ignores a terminal read answered after the stop', async () => {
      const client = sharing(true)
      let answer: (status: { running: boolean; tabs: string[] }) => void = () => {}
      vi.mocked(client.terminalStatus).mockReturnValue(new Promise((resolve) => (answer = resolve)))
      useStore.setState({
        envTerminal: { ...initialEnvTerminal, status: { running: true, tabs: ['main'] } },
      })
      seed({ capabilities: { gateway: 'remote', methods: ['*'], ws: ['events', 'attach', 'terminal'] } })
      render(<MembersRoute params={{}} client={client} />)

      fireEvent.click(await screen.findByRole('button', { name: 'Share your agent account with Bob' }))
      fireEvent.click(within(await screen.findByRole('status')).getByRole('button', { name: 'Stop environment' }))
      fireEvent.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Stop environment' }))
      await waitFor(() => expect(screen.queryByRole('status')).toBeNull())

      await act(async () => answer({ running: true, tabs: ['main'] }))
      expect(screen.queryByRole('status')).toBeNull()
      expect(useStore.getState().envTerminal.status?.running).toBe(false)
    })

    it('says nothing for a share while another grant already exists', async () => {
      const client = sharing(true)
      vi.mocked(client.accountList)
        .mockReset()
        .mockResolvedValueOnce({ accounts: [alice], shared_with: [vera] })
        .mockResolvedValue({ accounts: [alice], shared_with: [vera, bob] })
      seed({
        members: { [alice.id]: alice, [bob.id]: bob, [vera.id]: vera },
        capabilities: { gateway: 'remote', methods: ['*'], ws: ['events', 'attach', 'terminal'] },
      })
      render(<MembersRoute params={{}} client={client} />)

      fireEvent.click(await screen.findByRole('button', { name: 'Share your agent account with Bob' }))
      await waitFor(() => expect(screen.getAllByRole('button', { name: /^Stop sharing your agent account with/ })).toHaveLength(2))
      expect(client.terminalStatus).not.toHaveBeenCalled()
      expect(screen.queryByRole('status')).toBeNull()
    })

    it('drops the advice when the first share is revoked', async () => {
      const client = sharing(true)
      vi.mocked(client.accountList)
        .mockReset()
        .mockResolvedValueOnce({ accounts: [alice], shared_with: [] })
        .mockResolvedValueOnce({ accounts: [alice], shared_with: [bob] })
        .mockResolvedValue({ accounts: [alice], shared_with: [] })
      client.accountRevoke = vi.fn(async () => ({}))
      seed({ capabilities: { gateway: 'remote', methods: ['*'], ws: ['events', 'attach', 'terminal'] } })
      render(<MembersRoute params={{}} client={client} />)

      fireEvent.click(await screen.findByRole('button', { name: 'Share your agent account with Bob' }))
      await screen.findByRole('status')
      fireEvent.click(await screen.findByRole('button', { name: 'Stop sharing your agent account with Bob' }))

      await waitFor(() => expect(client.accountRevoke).toHaveBeenCalledWith(bob.id))
      await screen.findByRole('button', { name: 'Share your agent account with Bob' })
      expect(screen.queryByRole('status')).toBeNull()
    })

    it('says nothing more when no terminal is running', async () => {
      const client = sharing(false)
      seed({ capabilities: { gateway: 'remote', methods: ['*'], ws: ['events', 'attach', 'terminal'] } })
      render(<MembersRoute params={{}} client={client} />)

      fireEvent.click(await screen.findByRole('button', { name: 'Share your agent account with Bob' }))
      await waitFor(() => expect(client.terminalStatus).toHaveBeenCalled())
      await screen.findByRole('button', { name: 'Stop sharing your agent account with Bob' })
      expect(screen.queryByRole('status')).toBeNull()
      expect(screen.queryByRole('button', { name: 'Stop environment' })).toBeNull()
    })
  })
})
