import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { useState } from 'react'
import { toast, Toaster } from 'sonner'
import { ApiError } from '@/lib/api'
import { ProfileDialog } from '@/routes/members/personal'
import { useStore, type RootState } from '@/store'
import type { Member } from '@/lib/types'
import { initialEnvTerminal } from '@/store/env-terminal'
import { optimisticDeadlineMs } from '@/store/optimistic'
import { alice, bob, fakeApi, serverInfo, vera, workspace } from '@/test/fixtures'

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
    ...extra,
  })
}

describe('profile dialog', () => {
  function held<T>() {
    const answer = { resolve: (_: T) => {}, reject: (_: Error) => {} }
    const promise = new Promise<T>((resolve, reject) => Object.assign(answer, { resolve, reject }))
    return { promise, answer }
  }
  const pressed = (name: string) => screen.getByRole('button', { name }).getAttribute('aria-pressed')

  it('shows who you are and sets your colour before the server answers', async () => {
    const { promise, answer } = held<Member>()
    const client = fakeApi({ memberColor: vi.fn(() => promise) })
    seed()
    render(<ProfileDialog open onOpenChange={() => {}} client={client} />)

    const dialog = within(screen.getByRole('dialog', { name: 'Profile' }))
    expect(dialog.getByText('Alice')).toBeDefined()
    expect(pressed('Set colour #e6194b')).toBe('true')
    fireEvent.click(dialog.getByRole('button', { name: 'Set colour #3cb44b' }))
    expect(pressed('Set colour #3cb44b')).toBe('true')
    expect(useStore.getState().members[alice.id].color).toBe('#3cb44b')
    expect(client.memberColor).toHaveBeenCalledWith('#3cb44b')
    await act(async () => answer.resolve({ ...alice, color: '#3cb44b' }))
    expect(pressed('Set colour #3cb44b')).toBe('true')
  })

  it('takes a refused colour back to the one saved before it and shows the error', async () => {
    const client = fakeApi({
      memberColor: vi.fn().mockResolvedValueOnce({ ...alice, color: '#3cb44b' }).mockRejectedValueOnce(new ApiError(200, 'member.color: gateway closed', -32603)),
    })
    seed()
    render(<ProfileDialog open onOpenChange={() => {}} client={client} />)
    const picker = within(screen.getByRole('region', { name: 'Your colour' }))
    fireEvent.click(picker.getByRole('button', { name: 'Set colour #3cb44b' }))
    await waitFor(() => expect(client.memberColor).toHaveBeenCalledTimes(1))
    fireEvent.click(picker.getByRole('button', { name: 'Set colour #f58231' }))
    expect((await picker.findByRole('alert')).textContent).toBe('gateway closed')
    expect(pressed('Set colour #3cb44b')).toBe('true')
    expect(useStore.getState().members[alice.id].color).toBe('#3cb44b')
  })

  it('reverts an unanswered colour with Retry, then shows a late answer as saved', async () => {
    const { promise, answer } = held<Member>()
    const client = fakeApi({ memberColor: vi.fn(() => promise) })
    seed()
    render(<ProfileDialog open onOpenChange={() => {}} client={client} />)
    const picker = within(screen.getByRole('region', { name: 'Your colour' }))
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    try {
      fireEvent.click(picker.getByRole('button', { name: 'Set colour #3cb44b' }))
      act(() => vi.advanceTimersByTime(optimisticDeadlineMs))
      await act(async () => {})
      expect(picker.getByRole('alert').textContent).toContain('The server did not answer in 5 seconds.')
      expect(pressed('Set colour #e6194b')).toBe('true')

      fireEvent.click(picker.getByRole('button', { name: 'Retry' }))
      expect(client.memberColor).toHaveBeenCalledTimes(2)
      expect(client.memberColor).toHaveBeenLastCalledWith('#3cb44b')
      expect(pressed('Set colour #3cb44b')).toBe('true')
      await act(async () => answer.resolve({ ...alice, color: '#3cb44b' }))
      expect(picker.queryByRole('alert')).toBeNull()
      expect(pressed('Set colour #3cb44b')).toBe('true')
    } finally {
      vi.useRealTimers()
    }
  })

  it('shows a colour refused after the deadline, even once a newer colour is saved', async () => {
    const first = held<Member>()
    const client = fakeApi({
      memberColor: vi.fn().mockReturnValueOnce(first.promise).mockResolvedValueOnce({ ...alice, color: '#f58231' }),
    })
    seed()
    render(<ProfileDialog open onOpenChange={() => {}} client={client} />)
    const picker = within(screen.getByRole('region', { name: 'Your colour' }))
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    try {
      fireEvent.click(picker.getByRole('button', { name: 'Set colour #3cb44b' }))
      act(() => vi.advanceTimersByTime(optimisticDeadlineMs))
      await act(async () => {})
    } finally {
      vi.useRealTimers()
    }
    fireEvent.click(picker.getByRole('button', { name: 'Set colour #f58231' }))
    await waitFor(() => expect(pressed('Set colour #f58231')).toBe('true'))
    await act(async () => first.answer.reject(new ApiError(200, 'member.color: not a colour', -32602)))

    expect(picker.getByRole('alert').textContent).toBe('not a colour')
    expect(pressed('Set colour #f58231')).toBe('true')
  })

  it('keeps the newer colour when an older one is answered after Profile was closed and reopened', async () => {
    const first = held<Member>()
    const client = fakeApi({
      memberColor: vi.fn().mockReturnValueOnce(first.promise).mockResolvedValueOnce({ ...alice, color: '#f58231' }),
    })
    seed()
    const view = render(<ProfileDialog open onOpenChange={() => {}} client={client} />)
    fireEvent.click(screen.getByRole('button', { name: 'Set colour #3cb44b' }))
    view.unmount()
    render(<ProfileDialog open onOpenChange={() => {}} client={client} />)
    fireEvent.click(screen.getByRole('button', { name: 'Set colour #f58231' }))
    await waitFor(() => expect(client.memberColor).toHaveBeenCalledTimes(2))
    await act(async () => {})
    await act(async () => first.answer.resolve({ ...alice, color: '#3cb44b' }))

    expect(useStore.getState().members[alice.id].color).toBe('#f58231')
  })

  it('renames you in the header and roster before the server answers', async () => {
    const { promise, answer } = held<Member>()
    const client = fakeApi({ memberRename: vi.fn(() => promise) })
    seed()
    render(<ProfileDialog open onOpenChange={() => {}} client={client} />)
    const form = within(screen.getByRole('form', { name: 'Display name' }))
    expect(form.getByRole('button', { name: 'Save' }).hasAttribute('disabled')).toBe(true)
    fireEvent.change(form.getByLabelText('Display name'), { target: { value: '  Alicia ' } })
    fireEvent.click(form.getByRole('button', { name: 'Save' }))

    expect(within(screen.getByRole('dialog', { name: 'Profile' })).getByText('Alicia')).toBeDefined()
    expect(useStore.getState().info?.member.display_name).toBe('Alicia')
    expect(useStore.getState().members[alice.id].display_name).toBe('Alicia')
    expect(client.memberRename).toHaveBeenCalledWith('  Alicia ')
    await act(async () => answer.resolve({ ...alice, display_name: 'Alicia' }))
    expect((form.getByLabelText('Display name') as HTMLInputElement).value).toBe('Alicia')
    expect(form.queryByRole('alert')).toBeNull()
  })

  it('takes only the colour from a colour answer, so a pending rename stays', async () => {
    const client = fakeApi({
      memberRename: vi.fn(() => new Promise<Member>(() => {})),
      memberColor: vi.fn(async () => ({ ...alice, color: '#3cb44b' })),
    })
    seed()
    render(<ProfileDialog open onOpenChange={() => {}} client={client} />)
    const form = within(screen.getByRole('form', { name: 'Display name' }))
    fireEvent.change(form.getByLabelText('Display name'), { target: { value: 'Alicia' } })
    fireEvent.click(form.getByRole('button', { name: 'Save' }))
    fireEvent.click(screen.getByRole('button', { name: 'Set colour #3cb44b' }))

    await waitFor(() => expect(useStore.getState().members[alice.id].color).toBe('#3cb44b'))
    expect(useStore.getState().members[alice.id].display_name).toBe('Alicia')
  })

  it('takes a refused name back, keeps what you typed and shows the error', async () => {
    const client = fakeApi({ memberRename: vi.fn().mockRejectedValue(new ApiError(200, 'member.rename: display_name is longer than 64 characters', -32602)) })
    seed()
    render(<ProfileDialog open onOpenChange={() => {}} client={client} />)
    const form = within(screen.getByRole('form', { name: 'Display name' }))
    fireEvent.change(form.getByLabelText('Display name'), { target: { value: '  Alicia ' } })
    fireEvent.click(form.getByRole('button', { name: 'Save' }))

    expect((await form.findByRole('alert')).textContent).toBe('display_name is longer than 64 characters')
    expect((form.getByLabelText('Display name') as HTMLInputElement).value).toBe('  Alicia ')
    expect(useStore.getState().info?.member.display_name).toBe('Alice')
    expect(useStore.getState().members[alice.id].display_name).toBe('Alice')
  })

  it('retries an unanswered rename with one member.rename', async () => {
    const client = fakeApi({ memberRename: vi.fn(() => new Promise<Member>(() => {})) })
    seed()
    render(<ProfileDialog open onOpenChange={() => {}} client={client} />)
    const form = within(screen.getByRole('form', { name: 'Display name' }))
    fireEvent.change(form.getByLabelText('Display name'), { target: { value: 'Alicia' } })
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    try {
      fireEvent.click(form.getByRole('button', { name: 'Save' }))
      act(() => vi.advanceTimersByTime(optimisticDeadlineMs))
      await act(async () => {})
    } finally {
      vi.useRealTimers()
    }
    fireEvent.click(form.getByRole('button', { name: 'Retry' }))

    expect(client.memberRename).toHaveBeenCalledTimes(2)
  })

  it('toasts a name refused after Profile closed, and Reopen brings back what you typed', async () => {
    const { promise, answer } = held<Member>()
    const client = fakeApi({ memberRename: vi.fn(() => promise) })
    seed()
    function Host() {
      const [open, setOpen] = useState(true)
      return <><Toaster /><ProfileDialog open={open} onOpenChange={setOpen} client={client} /></>
    }
    render(<Host />)
    const form = within(screen.getByRole('form', { name: 'Display name' }))
    fireEvent.change(form.getByLabelText('Display name'), { target: { value: 'Alicia the Great' } })
    fireEvent.click(form.getByRole('button', { name: 'Save' }))
    fireEvent.keyDown(screen.getByRole('dialog', { name: 'Profile' }), { key: 'Escape' })
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Profile' })).toBeNull())
    await act(async () => answer.reject(new ApiError(200, 'member.rename: display_name is taken', -32602)))

    expect(await screen.findByText('display_name is taken')).toBeDefined()
    expect(useStore.getState().info?.member.display_name).toBe('Alice')
    fireEvent.click(screen.getByRole('button', { name: 'Reopen' }))
    const reopened = within(await screen.findByRole('form', { name: 'Display name' }))
    expect((reopened.getByLabelText('Display name') as HTMLInputElement).value).toBe('Alicia the Great')
    expect(reopened.getByRole('alert').textContent).toBe('display_name is taken')
  })

  it('dismisses an unanswered share\'s toast once the signed-in member changes, and its Retry sends nothing', async () => {
    const client = fakeApi({
      accountList: vi.fn(async () => ({ accounts: [alice], shared_with: [] })),
      accountShare: vi.fn(() => new Promise<unknown>(() => {})),
    })
    seed({ identityKey: 'alice' })
    function Host() {
      const [open, setOpen] = useState(true)
      return <><Toaster /><ProfileDialog open={open} onOpenChange={setOpen} client={client} /></>
    }
    render(<Host />)
    const share = await within(screen.getByRole('region', { name: 'Account sharing' })).findByRole('button', { name: 'Share account' })
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    try {
      fireEvent.click(share)
      fireEvent.keyDown(screen.getByRole('dialog', { name: 'Profile' }), { key: 'Escape' })
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

  it('edits the git identity your agents commit as', () => {
    seed()
    render(<ProfileDialog open onOpenChange={() => {}} client={fakeApi()} />)
    expect(within(screen.getByRole('region', { name: 'Git identity' })).getByRole('button', { name: 'Save identity' })).toBeDefined()
  })

  it('flips a share and a revoke before the server answers', async () => {
    const share = held<unknown>()
    const revoke = held<unknown>()
    const client = fakeApi({
      accountList: vi.fn(async () => ({ accounts: [alice], shared_with: [] })),
      accountShare: vi.fn(() => share.promise),
      accountRevoke: vi.fn(() => revoke.promise),
    })
    seed()
    render(<ProfileDialog open onOpenChange={() => {}} client={client} />)
    const sharing = within(screen.getByRole('region', { name: 'Account sharing' }))

    fireEvent.click(await sharing.findByRole('button', { name: 'Share account' }))
    expect(sharing.getByText('Can use your account')).toBeDefined()
    expect(sharing.getByRole('button', { name: 'Revoke access' }).hasAttribute('disabled')).toBe(true)
    await act(async () => share.answer.resolve({}))
    fireEvent.click(sharing.getByRole('button', { name: 'Revoke access' }))
    expect(sharing.getByText('Not shared')).toBeDefined()
    expect(client.accountShare).toHaveBeenCalledWith(bob.id)
    expect(client.accountRevoke).toHaveBeenCalledWith(bob.id)
    expect(client.accountList).toHaveBeenCalledTimes(1)
  })

  it('keeps a share confirmed before the slower first list read returns', async () => {
    const list = held<{ accounts: Member[]; shared_with: Member[] }>()
    const client = fakeApi({
      accountList: vi.fn(() => list.promise),
      accountShare: vi.fn(async () => ({})),
    })
    seed()
    render(<ProfileDialog open onOpenChange={() => {}} client={client} />)
    const sharing = within(screen.getByRole('region', { name: 'Account sharing' }))

    fireEvent.click(await sharing.findByRole('button', { name: 'Share account' }))
    await sharing.findByRole('button', { name: 'Revoke access' })
    await act(async () => list.answer.resolve({ accounts: [alice], shared_with: [] }))
    expect(sharing.getByText('Can use your account')).toBeDefined()
  })

  it('shows a share the server answers after the deadline as shared', async () => {
    const share = held<unknown>()
    const client = fakeApi({
      accountList: vi.fn(async () => ({ accounts: [alice], shared_with: [] })),
      accountShare: vi.fn(() => share.promise),
    })
    seed()
    render(<ProfileDialog open onOpenChange={() => {}} client={client} />)
    const sharing = within(screen.getByRole('region', { name: 'Account sharing' }))
    const button = await sharing.findByRole('button', { name: 'Share account' })
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    try {
      fireEvent.click(button)
      act(() => vi.advanceTimersByTime(optimisticDeadlineMs))
      await act(async () => {})
    } finally {
      vi.useRealTimers()
    }
    expect(sharing.getByText('Not shared')).toBeDefined()

    await act(async () => share.answer.resolve({}))
    expect(sharing.getByText('Can use your account')).toBeDefined()
    expect(sharing.queryByRole('alert')).toBeNull()
  })

  it('rolls a timed-out share back to an earlier share the server answered meanwhile', async () => {
    const first = held<unknown>()
    const client = fakeApi({
      accountList: vi.fn(async () => ({ accounts: [alice], shared_with: [] })),
      accountShare: vi.fn().mockReturnValueOnce(first.promise).mockReturnValueOnce(new Promise(() => {})),
    })
    seed()
    render(<ProfileDialog open onOpenChange={() => {}} client={client} />)
    const sharing = within(screen.getByRole('region', { name: 'Account sharing' }))
    const button = await sharing.findByRole('button', { name: 'Share account' })
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    try {
      fireEvent.click(button)
      act(() => vi.advanceTimersByTime(optimisticDeadlineMs))
      await act(async () => {})
      fireEvent.click(sharing.getByRole('button', { name: 'Share account' }))
      await act(async () => first.answer.resolve({}))
      act(() => vi.advanceTimersByTime(optimisticDeadlineMs))
      await act(async () => {})
    } finally {
      vi.useRealTimers()
    }
    expect(sharing.getByText('Can use your account')).toBeDefined()
  })

  it('takes a refused share back and shows the error', async () => {
    const client = fakeApi({
      accountList: vi.fn(async () => ({ accounts: [alice], shared_with: [] })),
      accountShare: vi.fn().mockRejectedValue(new ApiError(200, 'account.share: cannot share an account with a member pending admin approval', -32002)),
    })
    seed()
    render(<ProfileDialog open onOpenChange={() => {}} client={client} />)
    const sharing = within(screen.getByRole('region', { name: 'Account sharing' }))

    fireEvent.click(await sharing.findByRole('button', { name: 'Share account' }))
    expect((await sharing.findByRole('alert')).textContent).toBe('cannot share an account with a member pending admin approval')
    expect(sharing.getByText('Not shared')).toBeDefined()
    expect(sharing.getByRole('button', { name: 'Share account' }).hasAttribute('disabled')).toBe(false)
  })

  it('keeps one teammate\'s refused share on screen while a share for another starts', async () => {
    const client = fakeApi({
      accountList: vi.fn(async () => ({ accounts: [alice], shared_with: [] })),
      accountShare: vi.fn()
        .mockRejectedValueOnce(new ApiError(200, 'account.share: Bob is pending admin approval', -32002))
        .mockReturnValueOnce(new Promise(() => {})),
    })
    seed({ members: { [alice.id]: alice, [bob.id]: bob, [vera.id]: vera } })
    render(<ProfileDialog open onOpenChange={() => {}} client={client} />)
    const sharing = within(screen.getByRole('region', { name: 'Account sharing' }))
    const [shareBob, shareVera] = await sharing.findAllByRole('button', { name: 'Share account' })

    fireEvent.click(shareBob)
    expect((await sharing.findByRole('alert')).textContent).toBe('Bob is pending admin approval')
    fireEvent.click(shareVera)
    await waitFor(() => expect(client.accountShare).toHaveBeenCalledTimes(2))
    expect(sharing.getByRole('alert').textContent).toBe('Bob is pending admin approval')
  })

  describe('after a first share', () => {
    const notice =
      "Your Environment was started before you shared, so a Claude Code login written there will not reach Bob's runs until you stop it and open it again from Environment. Runs you already have running keep the mounts they started with until they end."
    const sharing = (running: boolean) =>
      fakeApi({
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
      render(<ProfileDialog open onOpenChange={() => {}} client={client} />)

      fireEvent.click(await screen.findByRole('button', { name: 'Share account' }))
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
      render(<ProfileDialog open onOpenChange={() => {}} client={client} />)

      const [shareBob, shareVera] = await screen.findAllByRole('button', { name: 'Share account' })
      fireEvent.click(shareBob)
      fireEvent.click(shareVera)
      expect((await screen.findByRole('status')).textContent).toContain("will not reach Vera's runs")
      await act(async () => bobShare.answer.resolve({}))
      expect(screen.getByRole('status').textContent).toContain("will not reach Vera's runs")
    })

    it('shows the stop error in the confirmation', async () => {
      const client = sharing(true)
      vi.mocked(client.terminalStop).mockRejectedValue(new Error('stop container: daemon is down'))
      seed({ capabilities: { gateway: 'remote', methods: ['*'], ws: ['events', 'attach', 'terminal'] } })
      render(<ProfileDialog open onOpenChange={() => {}} client={client} />)

      fireEvent.click(await screen.findByRole('button', { name: 'Share account' }))
      fireEvent.click(within(await screen.findByRole('status')).getByRole('button', { name: 'Stop environment' }))
      const dialog = within(await screen.findByRole('alertdialog'))
      fireEvent.click(dialog.getByRole('button', { name: 'Stop environment' }))

      expect(await dialog.findByText('stop container: daemon is down')).toBeDefined()
    })

    it('still gives the advice when the terminal read after the share fails', async () => {
      const client = sharing(true)
      vi.mocked(client.terminalStatus).mockRejectedValue(new Error('terminal.status: gateway closed'))
      seed({ capabilities: { gateway: 'remote', methods: ['*'], ws: ['events', 'attach', 'terminal'] } })
      render(<ProfileDialog open onOpenChange={() => {}} client={client} />)

      fireEvent.click(await screen.findByRole('button', { name: 'Share account' }))
      const status = await screen.findByRole('status')
      expect(status.textContent).toContain(
        "Your Environment could not be checked. If it is open, it was started before you shared, so a Claude Code login written there will not reach Bob's runs until you stop it and open it again from Environment. Runs you already have running keep the mounts they started with until they end.",
      )
      expect(within(status).getByRole('button', { name: 'Stop environment' })).toBeDefined()
    })

    it('drops the advice after a stop, also when the terminal could not be checked', async () => {
      const client = sharing(true)
      vi.mocked(client.terminalStatus).mockRejectedValue(new Error('terminal.status: gateway closed'))
      seed({ capabilities: { gateway: 'remote', methods: ['*'], ws: ['events', 'attach', 'terminal'] } })
      render(<ProfileDialog open onOpenChange={() => {}} client={client} />)

      fireEvent.click(await screen.findByRole('button', { name: 'Share account' }))
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
      render(<ProfileDialog open onOpenChange={() => {}} client={client} />)

      fireEvent.click(await screen.findByRole('button', { name: 'Share account' }))
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
      render(<ProfileDialog open onOpenChange={() => {}} client={client} />)

      fireEvent.click(await screen.findByRole('button', { name: 'Share account' }))
      await waitFor(() => expect(screen.getAllByRole('button', { name: 'Revoke access' })).toHaveLength(2))
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
      render(<ProfileDialog open onOpenChange={() => {}} client={client} />)

      fireEvent.click(await screen.findByRole('button', { name: 'Share account' }))
      await screen.findByRole('status')
      fireEvent.click(await screen.findByRole('button', { name: 'Revoke access' }))

      await waitFor(() => expect(client.accountRevoke).toHaveBeenCalledWith(bob.id))
      await screen.findByRole('button', { name: 'Share account' })
      expect(screen.queryByRole('status')).toBeNull()
    })

    it('says nothing more when no terminal is running', async () => {
      const client = sharing(false)
      seed({ capabilities: { gateway: 'remote', methods: ['*'], ws: ['events', 'attach', 'terminal'] } })
      render(<ProfileDialog open onOpenChange={() => {}} client={client} />)

      fireEvent.click(await screen.findByRole('button', { name: 'Share account' }))
      await waitFor(() => expect(client.terminalStatus).toHaveBeenCalled())
      await screen.findByRole('button', { name: 'Revoke access' })
      expect(screen.queryByRole('status')).toBeNull()
      expect(screen.queryByRole('button', { name: 'Stop environment' })).toBeNull()
    })
  })
})
