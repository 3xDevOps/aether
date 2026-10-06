import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { ProfileDialog } from '@/routes/members/personal'
import { useStore, type RootState } from '@/store'
import { initialEnvTerminal } from '@/store/env-terminal'
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
  it('shows who you are and sets your colour', async () => {
    const client = fakeApi({ memberColor: vi.fn(async () => ({ ...alice, color: '#3cb44b' })) })
    seed()
    render(<ProfileDialog open onOpenChange={() => {}} client={client} />)

    const dialog = within(screen.getByRole('dialog', { name: 'Profile' }))
    expect(dialog.getByText('Alice')).toBeDefined()
    expect(dialog.getByRole('button', { name: 'Set colour #e6194b' }).getAttribute('aria-pressed')).toBe('true')
    fireEvent.click(dialog.getByRole('button', { name: 'Set colour #3cb44b' }))
    await waitFor(() => expect(client.memberColor).toHaveBeenCalledWith('#3cb44b'))
  })

  it('edits the git identity your agents commit as', () => {
    seed()
    render(<ProfileDialog open onOpenChange={() => {}} client={fakeApi()} />)
    expect(within(screen.getByRole('region', { name: 'Git identity' })).getByRole('button', { name: 'Save identity' })).toBeDefined()
  })

  it('lets a member grant and revoke use of their agent account', async () => {
    const accountList = vi
      .fn()
      .mockResolvedValueOnce({ accounts: [alice], shared_with: [] })
      .mockResolvedValueOnce({ accounts: [alice], shared_with: [bob] })
      .mockResolvedValue({ accounts: [alice], shared_with: [] })
    const client = fakeApi({
      accountList,
      accountShare: vi.fn(async () => ({})),
      accountRevoke: vi.fn(async () => ({})),
    })
    seed()
    render(<ProfileDialog open onOpenChange={() => {}} client={client} />)

    fireEvent.click(await screen.findByRole('button', { name: 'Share account' }))
    await waitFor(() => expect(client.accountShare).toHaveBeenCalledWith(bob.id))
    fireEvent.click(await screen.findByRole('button', { name: 'Revoke access' }))
    await waitFor(() => expect(client.accountRevoke).toHaveBeenCalledWith(bob.id))
  })

  describe('after a first share', () => {
    const notice =
      "Your environment terminal was started before you shared, so a Claude Code login written there will not reach Bob's runs until you stop it and open it again from Environment. Runs you already have running keep the mounts they started with until they end."
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

    it('still gives the advice when the reads after the share fail', async () => {
      const client = sharing(true)
      vi.mocked(client.terminalStatus).mockRejectedValue(new Error('terminal.status: gateway closed'))
      vi.mocked(client.accountList)
        .mockReset()
        .mockResolvedValueOnce({ accounts: [alice], shared_with: [] })
        .mockRejectedValue(new Error('account.list: gateway closed'))
      seed({ capabilities: { gateway: 'remote', methods: ['*'], ws: ['events', 'attach', 'terminal'] } })
      render(<ProfileDialog open onOpenChange={() => {}} client={client} />)

      fireEvent.click(await screen.findByRole('button', { name: 'Share account' }))
      const status = await screen.findByRole('status')
      expect(status.textContent).toContain(
        "Your environment terminal could not be checked. If it is open, it was started before you shared, so a Claude Code login written there will not reach Bob's runs until you stop it and open it again from Environment. Runs you already have running keep the mounts they started with until they end.",
      )
      expect(within(status).getByRole('button', { name: 'Stop environment' })).toBeDefined()
      expect(await screen.findByText('account.list: gateway closed')).toBeDefined()
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
