import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { toast } from 'sonner'
import { ApiError, codeNotFound } from '@/lib/api'
import type { Invitation, InvitationResult } from '@/lib/types'
import { MembersRoute } from '@/routes/members'
import { useStore } from '@/store'
import { optimisticDeadlineMs } from '@/store/optimistic'
import { alice, bob, fakeApi, serverInfo, workspace } from '@/test/fixtures'
import { pickOption } from '@/test/select'

function invitation(over: Partial<Invitation> = {}): Invitation {
  return {
    id: 'inv_octocat',
    provider: 'github',
    login: 'octocat',
    role: 'collaborator',
    created_by: alice.id,
    created_at: '2026-09-27T10:00:00Z',
    expires_at: '2026-10-04T10:00:00Z',
    ...over,
  }
}

const joinLink = 'https://edge.example/join/wqc4lsjvzdzrwq3k5dabdtajwj/inv-7k2m9q4xbd#abcdefghijklmnop'

function result(inv: Invitation): InvitationResult {
  return { invitation: inv, link: joinLink, server_id: 'wqc4lsjvzdzrwq3k5dabdtajwj' }
}

function seed(self = alice) {
  useStore.setState({
    workspaces: { [workspace.id]: workspace },
    members: { [alice.id]: alice, [bob.id]: bob },
    invitations: null,
    revokingInvitations: [],
    presence: [],
    info: { ...serverInfo, member: self },
    capabilities: { gateway: 'local', methods: ['*'], ws: ['events'] },
    route: { name: 'members', params: {} },
  })
}

async function section() {
  return within(await screen.findByRole('region', { name: 'Invitations' }))
}

async function inviteDialog() {
  fireEvent.click(await screen.findByRole('button', { name: 'Invite…' }))
  return within(await screen.findByRole('dialog', { name: 'Invite a member' }))
}

describe('invitations', () => {
  it('is an admin view', async () => {
    seed(bob)
    const client = fakeApi()
    render(<MembersRoute params={{}} client={client} />)

    await screen.findByRole('region', { name: 'Roster' })
    expect(screen.queryByRole('region', { name: 'Invitations' })).toBeNull()
    expect(client.memberInvitationList).not.toHaveBeenCalled()
  })

  it('lists who each open invitation admits, with its role, author and expiry', async () => {
    seed()
    const client = fakeApi({
      memberInvitationList: vi.fn(async () => [
        invitation(),
        invitation({
          id: 'inv_dev',
          login: undefined,
          email: 'dev@example.com',
          role: 'viewer',
        }),
        // One v0.5.2-alpha.3 stored for Google, which admits nobody now.
        invitation({
          id: 'inv_old',
          provider: 'google',
          login: undefined,
          email: 'old@example.com',
          role: 'viewer',
        }),
        invitation({ id: 'inv_link', role: undefined, login: 'bob-gh', member_id: bob.id }),
      ]),
    })
    render(<MembersRoute params={{}} client={client} />)

    const list = within((await section()).getByRole('list', { name: 'Open invitations' }))
    const octocat = list.getByText('octocat on GitHub').closest('li')!
    expect(octocat.textContent).toContain('Collaborator · invited by Alice · expires')
    expect(list.getByText('dev@example.com on GitHub')).toBeDefined()
    expect(list.getByText('old@example.com on google')).toBeDefined()
    expect(list.getAllByText(/^Viewer · invited by/)).toHaveLength(2)
    expect(list.getByText(/^Links to Bob/)).toBeDefined()
  })

  it('invites by email with the chosen role', async () => {
    seed()
    const created = invitation({ id: 'inv_new', login: undefined, email: 'new@example.com', role: 'viewer' })
    const client = fakeApi({
      memberInvitationList: vi.fn(async () => []),
      memberInvitationCreate: vi.fn(async () => result(created)),
    })
    render(<MembersRoute params={{}} client={client} />)
    const invitations = await section()
    expect(await invitations.findByText('No open invitations.')).toBeDefined()

    const dialog = await inviteDialog()
    await pickOption(dialog.getByLabelText('Invite by'), 'Email')
    // The closing list hands focus back to its trigger; the next pick waits.
    await waitFor(() => expect(screen.queryByRole('listbox')).toBeNull())
    fireEvent.change(dialog.getByLabelText('Email'), { target: { value: 'new@example.com' } })
    await pickOption(dialog.getByLabelText('Role'), 'Viewer')
    fireEvent.click(dialog.getByRole('button', { name: 'Invite' }))

    expect(await invitations.findByText('new@example.com on GitHub')).toBeDefined()
    expect(client.memberInvitationCreate).toHaveBeenCalledWith({
      email: 'new@example.com',
      role: 'viewer',
    })
  })

  it('invites a GitHub login as a GitHub account', async () => {
    seed()
    const client = fakeApi({ memberInvitationCreate: vi.fn(async () => result(invitation())) })
    render(<MembersRoute params={{}} client={client} />)
    const dialog = await inviteDialog()

    fireEvent.change(dialog.getByLabelText('GitHub login'), { target: { value: ' octocat ' } })
    fireEvent.click(dialog.getByRole('button', { name: 'Invite' }))

    await vi.waitFor(() =>
      expect(client.memberInvitationCreate).toHaveBeenCalledWith({
        login: 'octocat',
        role: 'collaborator',
      }),
    )
  })

  it('shows the invite link to send, once, until the dialog is closed', async () => {
    seed()
    const writeText = vi.fn(async () => {})
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })
    const client = fakeApi({ memberInvitationCreate: vi.fn(async () => result(invitation())) })
    render(<MembersRoute params={{}} client={client} />)
    const dialog = await inviteDialog()

    fireEvent.change(dialog.getByLabelText('GitHub login'), { target: { value: 'octocat' } })
    fireEvent.click(dialog.getByRole('button', { name: 'Invite' }))

    const link = within(await dialog.findByRole('group', { name: 'Invite link' }))
    expect(link.getByText(joinLink)).toBeDefined()
    expect(dialog.getByText('Send this link to octocat on GitHub. It names their GitHub account and expires in 7 days.')).toBeDefined()
    fireEvent.click(link.getByRole('button', { name: 'Copy link' }))
    await vi.waitFor(() => expect(writeText).toHaveBeenCalledWith(joinLink))
    expect(screen.getByRole('dialog', { name: 'Invite a member' })).toBeDefined()

    fireEvent.click(dialog.getByRole('button', { name: 'Done' }))
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Invite a member' })).toBeNull())
  })

  it('says what an invitation admits under each access policy', async () => {
    seed()
    render(<MembersRoute params={{}} client={fakeApi()} />)
    const dialog = await inviteDialog()
    fireEvent.click(dialog.getByRole('button', { name: 'Learn more' }))
    const text = screen.getByRole('dialog', { name: 'Invite a member' }).textContent

    expect(text).toContain('When signing in is enough for this server')
    expect(text).toContain('When the server admits approved devices only')
    expect(text).toContain('aether device approve')
  })

  it('lists a new invitation as creating and labels Invite until the server answers, then the server row and its link', async () => {
    seed()
    let answer: (created: InvitationResult) => void = () => {}
    const client = fakeApi({
      memberInvitationList: vi.fn(async () => []),
      memberInvitationCreate: vi.fn(() => new Promise<InvitationResult>((resolve) => (answer = resolve))),
    })
    render(<MembersRoute params={{}} client={client} />)
    const invitations = await section()
    await invitations.findByText('No open invitations.')
    const dialog = await inviteDialog()

    fireEvent.change(dialog.getByLabelText('GitHub login'), { target: { value: 'octocat' } })
    fireEvent.click(dialog.getByRole('button', { name: 'Invite' }))

    expect(dialog.getByRole('button', { name: 'Inviting…' }).hasAttribute('disabled')).toBe(true)
    const pending = invitations.getByText('octocat on GitHub').closest('li')!
    expect(pending.textContent).toContain('Collaborator · invited by Alice · Creating…')
    expect(invitations.queryByRole('button', { name: /^Revoke invitation/, hidden: true })).toBeNull()
    await act(async () => answer(result(invitation())))
    expect(invitations.getAllByText('octocat on GitHub')).toHaveLength(1)
    expect(invitations.queryByText(/Creating…/)).toBeNull()
    expect(invitations.getByRole('button', { name: 'Revoke invitation for octocat on GitHub', hidden: true })).toBeDefined()
    expect(within(dialog.getByRole('group', { name: 'Invite link' })).getByText(joinLink)).toBeDefined()
  })

  it('offers the link in a toast when the dialog was closed before the server answered', async () => {
    seed()
    let answer: (created: InvitationResult) => void = () => {}
    const client = fakeApi({
      memberInvitationList: vi.fn(async () => []),
      memberInvitationCreate: vi.fn(() => new Promise<InvitationResult>((resolve) => (answer = resolve))),
    })
    const success = vi.spyOn(toast, 'success')
    try {
      render(<MembersRoute params={{}} client={client} />)
      await (await section()).findByText('No open invitations.')
      const dialog = await inviteDialog()
      fireEvent.change(dialog.getByLabelText('GitHub login'), { target: { value: 'octocat' } })
      fireEvent.click(dialog.getByRole('button', { name: 'Invite' }))
      fireEvent.click(dialog.getByRole('button', { name: 'Cancel' }))
      await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Invite a member' })).toBeNull())

      await act(async () => answer(result(invitation())))
      expect(success).toHaveBeenLastCalledWith('Invited octocat on GitHub', expect.objectContaining({ action: expect.objectContaining({ label: 'Show link' }) }))
      const [, options] = success.mock.lastCall as unknown as [string, { action: { onClick: () => void } }]
      act(() => options.action.onClick())
      const reopened = within(await screen.findByRole('dialog', { name: 'Invite a member' }))
      expect(within(reopened.getByRole('group', { name: 'Invite link' })).getByText(joinLink)).toBeDefined()
    } finally {
      success.mockRestore()
    }
  })

  it('takes a refused invitation back and shows the error in the dialog with what was typed', async () => {
    seed()
    const client = fakeApi({
      memberInvitationList: vi.fn(async () => []),
      memberInvitationCreate: vi.fn(() =>
        Promise.reject(new ApiError(400, 'member.invitation.create: login "-x" is not a GitHub login', -32602)),
      ),
    })
    render(<MembersRoute params={{}} client={client} />)
    const invitations = await section()
    const dialog = await inviteDialog()

    fireEvent.change(dialog.getByLabelText('GitHub login'), { target: { value: '-x' } })
    fireEvent.click(dialog.getByRole('button', { name: 'Invite' }))

    const reopened = within(await screen.findByRole('dialog', { name: 'Invite a member' }))
    expect((await reopened.findByRole('alert')).textContent).toBe('login "-x" is not a GitHub login')
    expect((reopened.getByLabelText('GitHub login') as HTMLInputElement).value).toBe('-x')
    expect(invitations.getByText('No open invitations.')).toBeDefined()
    expect(client.memberInvitationCreate).toHaveBeenCalledWith({
      login: '-x',
      role: 'collaborator',
    })
  })

  it('dismisses an invitation\'s failure toast once the signed-in member changes, and its Reopen does nothing', async () => {
    seed()
    useStore.setState({ identityKey: 'alice' })
    let refuse: (cause: Error) => void = () => {}
    const client = fakeApi({
      memberInvitationList: vi.fn(async () => []),
      memberInvitationCreate: vi.fn(() => new Promise<InvitationResult>((_, reject) => (refuse = reject))),
    })
    const error = vi.spyOn(toast, 'error')
    const dismiss = vi.spyOn(toast, 'dismiss')
    try {
      render(<MembersRoute params={{}} client={client} />)
      await section()
      const first = await inviteDialog()
      fireEvent.change(first.getByLabelText('GitHub login'), { target: { value: '-x' } })
      fireEvent.click(first.getByRole('button', { name: 'Invite' }))
      fireEvent.click(first.getByRole('button', { name: 'Cancel' }))
      await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Invite a member' })).toBeNull())
      await inviteDialog()
      await act(async () => refuse(new ApiError(400, 'member.invitation.create: login "-x" is not a GitHub login', -32602)))
      expect(error).toHaveBeenLastCalledWith('login "-x" is not a GitHub login', expect.objectContaining({ cancel: expect.objectContaining({ label: 'Reopen' }) }))
      const [, options] = error.mock.lastCall as unknown as [string, { cancel: { onClick: () => void } }]
      const shown = error.mock.results.at(-1)?.value

      act(() => useStore.setState({ identityKey: 'bob' }))
      expect(dismiss).toHaveBeenCalledWith(shown)
      act(() => options.cancel.onClick())
      expect(within(screen.getByRole('dialog', { name: 'Invite a member' })).queryByRole('alert')).toBeNull()
    } finally {
      error.mockRestore()
      dismiss.mockRestore()
    }
  })

  it('keeps an unanswered invitation listed as waiting, says so on Invite, then swaps in the server row', async () => {
    seed()
    let answer: (created: InvitationResult) => void = () => {}
    const client = fakeApi({
      memberInvitationList: vi.fn(async () => []),
      memberInvitationCreate: vi.fn(() => new Promise<InvitationResult>((resolve) => (answer = resolve))),
    })
    render(<MembersRoute params={{}} client={client} />)
    const invitations = await section()
    await invitations.findByText('No open invitations.')
    const dialog = await inviteDialog()
    fireEvent.change(dialog.getByLabelText('GitHub login'), { target: { value: 'octocat' } })
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    try {
      fireEvent.click(dialog.getByRole('button', { name: 'Invite' }))
      act(() => vi.advanceTimersByTime(optimisticDeadlineMs))
      await act(async () => {})
    } finally {
      vi.useRealTimers()
    }

    expect(dialog.getByRole('button', { name: 'Still waiting for the server…' })).toBeDefined()
    expect(invitations.getByText('octocat on GitHub').closest('li')!.textContent).toContain('Still waiting for the server…')
    await act(async () => answer(result(invitation())))
    expect(invitations.getByRole('button', { name: 'Revoke invitation for octocat on GitHub', hidden: true })).toBeDefined()
    expect(client.memberInvitationCreate).toHaveBeenCalledTimes(1)
  })

  it('drops an invitation refused after the deadline and shows the error in the dialog', async () => {
    seed()
    let refuse: (cause: Error) => void = () => {}
    const client = fakeApi({
      memberInvitationList: vi.fn(async () => []),
      memberInvitationCreate: vi.fn(() => new Promise<InvitationResult>((_, reject) => (refuse = reject))),
    })
    render(<MembersRoute params={{}} client={client} />)
    const invitations = await section()
    await invitations.findByText('No open invitations.')
    const dialog = await inviteDialog()
    fireEvent.change(dialog.getByLabelText('GitHub login'), { target: { value: 'octocat' } })
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    try {
      fireEvent.click(dialog.getByRole('button', { name: 'Invite' }))
      act(() => vi.advanceTimersByTime(optimisticDeadlineMs))
      await act(async () => {})
    } finally {
      vi.useRealTimers()
    }
    await act(async () => refuse(new ApiError(200, 'member.invitation.create: octocat is already a member', -32003)))

    const reopened = within(await screen.findByRole('dialog', { name: 'Invite a member' }))
    expect(reopened.getByRole('alert').textContent).toBe('octocat is already a member')
    expect((reopened.getByLabelText('GitHub login') as HTMLInputElement).value).toBe('octocat')
    expect(invitations.getByText('No open invitations.')).toBeDefined()
  })

  it('re-reads the list after a create got no answer, then drops the row and shows the error with a warning', async () => {
    seed()
    const client = fakeApi({
      memberInvitationList: vi.fn(async () => []),
      memberInvitationCreate: vi.fn(() => Promise.reject(new TypeError('Failed to fetch'))),
    })
    render(<MembersRoute params={{}} client={client} />)
    const invitations = await section()
    await invitations.findByText('No open invitations.')
    const dialog = await inviteDialog()
    fireEvent.change(dialog.getByLabelText('GitHub login'), { target: { value: 'octocat' } })
    fireEvent.click(dialog.getByRole('button', { name: 'Invite' }))

    const reopened = within(await screen.findByRole('dialog', { name: 'Invite a member' }))
    expect(reopened.getByRole('alert').textContent).toBe('Failed to fetch The invitation may have been created; check Open invitations before you invite again.')
    expect((reopened.getByLabelText('GitHub login') as HTMLInputElement).value).toBe('octocat')
    expect(client.memberInvitationList).toHaveBeenCalledTimes(2)
    expect(invitations.getByText('No open invitations.')).toBeDefined()
  })

  it('keeps an invitation the server holds after a create got no answer, and says its link is lost', async () => {
    seed()
    const client = fakeApi({
      memberInvitationList: vi.fn().mockResolvedValueOnce([]).mockResolvedValue([invitation()]),
      memberInvitationCreate: vi.fn(() => Promise.reject(new TypeError('Failed to fetch'))),
    })
    render(<MembersRoute params={{}} client={client} />)
    const invitations = await section()
    await invitations.findByText('No open invitations.')
    const dialog = await inviteDialog()
    fireEvent.change(dialog.getByLabelText('GitHub login'), { target: { value: 'octocat' } })
    fireEvent.click(dialog.getByRole('button', { name: 'Invite' }))

    expect((await dialog.findByRole('alert')).textContent).toBe(
      'Failed to fetch The server created the invitation, but its link came only in the answer that was lost; revoke it and invite again.',
    )
    expect(invitations.getByRole('button', { name: 'Revoke invitation for octocat on GitHub', hidden: true })).toBeDefined()
  })

  it('leaves an invitation answered after the signed-in identity changed out of the new list', async () => {
    seed()
    useStore.setState({ identityKey: 'alice' })
    let answer: (created: InvitationResult) => void = () => {}
    const client = fakeApi({
      memberInvitationList: vi.fn(async () => []),
      memberInvitationCreate: vi.fn(() => new Promise<InvitationResult>((resolve) => (answer = resolve))),
    })
    render(<MembersRoute params={{}} client={client} />)
    await (await section()).findByText('No open invitations.')
    const dialog = await inviteDialog()
    fireEvent.change(dialog.getByLabelText('GitHub login'), { target: { value: 'octocat' } })
    fireEvent.click(dialog.getByRole('button', { name: 'Invite' }))
    act(() => useStore.setState({ identityKey: 'bob', invitations: null }))
    await act(async () => answer(result(invitation())))

    expect(useStore.getState().invitations).toBeNull()
  })

  it('labels Generate code while the server answers, and says when it is slow', async () => {
    seed()
    render(<MembersRoute params={{}} client={fakeApi({ memberInvite: vi.fn(() => new Promise<never>(() => {})) })} />)
    const dialog = await inviteDialog()
    const codeTab = dialog.getByRole('tab', { name: 'Invite code' })
    codeTab.focus()
    await userEvent.keyboard('{Enter}')
    const generate = await dialog.findByRole('button', { name: 'Generate code' })
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    try {
      fireEvent.click(generate)
      expect(dialog.getByRole('button', { name: 'Generating…' }).hasAttribute('disabled')).toBe(true)
      act(() => vi.advanceTimersByTime(optimisticDeadlineMs))
      expect(dialog.getByRole('button', { name: 'Still waiting for the server…' })).toBeDefined()
    } finally {
      vi.useRealTimers()
    }
  })

  it('keeps an invitation created while the first list read was pending', async () => {
    seed()
    let list: (invitations: Invitation[]) => void = () => {}
    const client = fakeApi({
      memberInvitationList: vi.fn(() => new Promise<Invitation[]>((resolve) => (list = resolve))),
      memberInvitationCreate: vi.fn(async () => result(invitation())),
    })
    render(<MembersRoute params={{}} client={client} />)
    const invitations = await section()
    const dialog = await inviteDialog()
    fireEvent.change(dialog.getByLabelText('GitHub login'), { target: { value: 'octocat' } })
    fireEvent.click(dialog.getByRole('button', { name: 'Invite' }))
    await invitations.findByRole('button', { name: 'Revoke invitation for octocat on GitHub', hidden: true })

    await act(async () => list([invitation({ id: 'inv_dev', login: undefined, email: 'dev@example.com' })]))
    expect(invitations.getByText('octocat on GitHub')).toBeDefined()
    expect(invitations.getByText('dev@example.com on GitHub')).toBeDefined()
  })

  it('keeps a pending create and a pending revoke across a remount whose list read predates both', async () => {
    seed()
    const client = fakeApi({
      memberInvitationList: vi.fn(async () => [invitation({ id: 'inv_dev', login: undefined, email: 'dev@example.com' })]),
      memberInvitationCreate: vi.fn(() => new Promise<InvitationResult>(() => {})),
      memberInvitationRevoke: vi.fn(() => new Promise(() => {})),
    })
    const view = render(<MembersRoute params={{}} client={client} />)
    let invitations = await section()
    fireEvent.click(await invitations.findByRole('button', { name: 'Revoke invitation for dev@example.com on GitHub' }))
    fireEvent.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Revoke' }))
    await vi.waitFor(() => expect(client.memberInvitationRevoke).toHaveBeenCalledWith('inv_dev'))
    const dialog = await inviteDialog()
    fireEvent.change(dialog.getByLabelText('GitHub login'), { target: { value: 'octocat' } })
    fireEvent.click(dialog.getByRole('button', { name: 'Invite' }))

    view.unmount()
    render(<MembersRoute params={{}} client={client} />)
    invitations = await section()
    await vi.waitFor(() => expect(client.memberInvitationList).toHaveBeenCalledTimes(2))
    await act(async () => {})
    expect(invitations.getByText('octocat on GitHub').closest('li')!.textContent).toContain('Creating…')
    expect(invitations.queryByText('dev@example.com on GitHub')).toBeNull()
  })

  it('keeps a revoked row out of a list read sent after the revoke started and answered after it succeeded', async () => {
    seed()
    const revoke = Promise.withResolvers<unknown>()
    const read = Promise.withResolvers<Invitation[]>()
    const client = fakeApi({
      memberInvitationList: vi.fn().mockResolvedValueOnce([invitation()]).mockReturnValueOnce(read.promise),
      memberInvitationRevoke: vi.fn(() => revoke.promise),
    })
    const view = render(<MembersRoute params={{}} client={client} />)
    let invitations = await section()
    fireEvent.click(await invitations.findByRole('button', { name: 'Revoke invitation for octocat on GitHub' }))
    fireEvent.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Revoke' }))
    await vi.waitFor(() => expect(client.memberInvitationRevoke).toHaveBeenCalledWith('inv_octocat'))

    view.unmount()
    render(<MembersRoute params={{}} client={client} />)
    invitations = await section()
    await vi.waitFor(() => expect(client.memberInvitationList).toHaveBeenCalledTimes(2))
    await act(async () => revoke.resolve({}))
    await act(async () => read.resolve([invitation()]))

    expect(invitations.queryByText('octocat on GitHub')).toBeNull()
  })

  it('confirms a revoke, saying what stops working, then removes the row before the server answers', async () => {
    seed()
    const client = fakeApi({
      memberInvitationList: vi.fn(async () => [invitation()]),
      memberInvitationRevoke: vi.fn(() => new Promise(() => {})),
    })
    render(<MembersRoute params={{}} client={client} />)
    const invitations = await section()

    fireEvent.click(
      await invitations.findByRole('button', { name: 'Revoke invitation for octocat on GitHub' }),
    )
    const dialog = within(await screen.findByRole('alertdialog'))
    expect(
      dialog.getByText('octocat on GitHub can no longer join this server by signing in to the edge.'),
    ).toBeDefined()
    expect(client.memberInvitationRevoke).not.toHaveBeenCalled()

    fireEvent.click(dialog.getByRole('button', { name: 'Revoke' }))
    await vi.waitFor(() => expect(client.memberInvitationRevoke).toHaveBeenCalledWith('inv_octocat'))
    expect(invitations.queryByText('octocat on GitHub')).toBeNull()
    await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull())
  })

  it('puts a refused revoke back in the list and shows the error', async () => {
    seed()
    const client = fakeApi({
      memberInvitationList: vi.fn(async () => [invitation()]),
      memberInvitationRevoke: vi.fn().mockRejectedValue(new ApiError(403, 'member.invitation.revoke: only its creator or an admin may revoke it', -32001)),
    })
    render(<MembersRoute params={{}} client={client} />)
    const invitations = await section()

    fireEvent.click(await invitations.findByRole('button', { name: 'Revoke invitation for octocat on GitHub' }))
    fireEvent.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Revoke' }))

    expect((await invitations.findByRole('alert')).textContent).toBe('only its creator or an admin may revoke it')
    expect(invitations.getByText('octocat on GitHub')).toBeDefined()
  })

  it('puts back an invitation whose first revoke is refused as not found and shows the error', async () => {
    seed()
    const client = fakeApi({
      memberInvitationList: vi.fn(async () => [invitation()]),
      memberInvitationRevoke: vi.fn().mockRejectedValue(new ApiError(200, 'member.invitation.revoke: invitation not found', codeNotFound)),
    })
    render(<MembersRoute params={{}} client={client} />)
    const invitations = await section()

    fireEvent.click(await invitations.findByRole('button', { name: 'Revoke invitation for octocat on GitHub' }))
    fireEvent.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Revoke' }))

    expect((await invitations.findByRole('alert')).textContent).toBe('invitation not found')
    expect(invitations.getByText('octocat on GitHub')).toBeDefined()
  })

  it('retries an unanswered revoke and reads a not-found answer as revoked', async () => {
    seed()
    const client = fakeApi({
      memberInvitationList: vi.fn(async () => [invitation()]),
      memberInvitationRevoke: vi.fn()
        .mockReturnValueOnce(new Promise(() => {}))
        .mockRejectedValueOnce(new ApiError(200, 'member.invitation.revoke: invitation not found', codeNotFound)),
    })
    render(<MembersRoute params={{}} client={client} />)
    const invitations = await section()
    fireEvent.click(await invitations.findByRole('button', { name: 'Revoke invitation for octocat on GitHub' }))
    const dialog = within(await screen.findByRole('alertdialog'))
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    try {
      fireEvent.click(dialog.getByRole('button', { name: 'Revoke' }))
      await act(async () => {})
      act(() => vi.advanceTimersByTime(optimisticDeadlineMs))
      await act(async () => {})
    } finally {
      vi.useRealTimers()
    }

    expect(invitations.getByRole('alert').textContent).toContain('The server did not answer in 5 seconds.')
    expect(invitations.getByText('octocat on GitHub')).toBeDefined()
    fireEvent.click(invitations.getByRole('button', { name: 'Retry' }))

    await waitFor(() => expect(invitations.queryByText('octocat on GitHub')).toBeNull())
    expect(invitations.queryByRole('alert')).toBeNull()
    expect(client.memberInvitationRevoke).toHaveBeenCalledTimes(2)
  })
  it('drops a revoke refused late once its retry was answered', async () => {
    seed()
    let refuse: (cause: Error) => void = () => {}
    const client = fakeApi({
      memberInvitationList: vi.fn(async () => [invitation()]),
      memberInvitationRevoke: vi.fn()
        .mockReturnValueOnce(new Promise((_, reject) => (refuse = reject)))
        .mockResolvedValueOnce({}),
    })
    render(<MembersRoute params={{}} client={client} />)
    const invitations = await section()
    fireEvent.click(await invitations.findByRole('button', { name: 'Revoke invitation for octocat on GitHub' }))
    const dialog = within(await screen.findByRole('alertdialog'))
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    try {
      fireEvent.click(dialog.getByRole('button', { name: 'Revoke' }))
      await act(async () => {})
      act(() => vi.advanceTimersByTime(optimisticDeadlineMs))
      await act(async () => {})
    } finally {
      vi.useRealTimers()
    }
    fireEvent.click(invitations.getByRole('button', { name: 'Retry' }))
    await waitFor(() => expect(invitations.queryByText('octocat on GitHub')).toBeNull())
    await act(async () => refuse(new ApiError(403, 'member.invitation.revoke: only its creator or an admin may revoke it', -32001)))

    expect(invitations.queryByRole('alert')).toBeNull()
  })

  it('removes a revoked invitation again when the server answers after the deadline', async () => {
    seed()
    let answer: () => void = () => {}
    const client = fakeApi({
      memberInvitationList: vi.fn(async () => [invitation()]),
      memberInvitationRevoke: vi.fn(() => new Promise<unknown>((resolve) => (answer = () => resolve({})))),
    })
    render(<MembersRoute params={{}} client={client} />)
    const invitations = await section()
    fireEvent.click(await invitations.findByRole('button', { name: 'Revoke invitation for octocat on GitHub' }))
    const dialog = within(await screen.findByRole('alertdialog'))
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    try {
      fireEvent.click(dialog.getByRole('button', { name: 'Revoke' }))
      await act(async () => {})
      act(() => vi.advanceTimersByTime(optimisticDeadlineMs))
      await act(async () => {})
    } finally {
      vi.useRealTimers()
    }
    expect(invitations.getByText('octocat on GitHub')).toBeDefined()

    await act(async () => answer())
    expect(invitations.queryByText('octocat on GitHub')).toBeNull()
    expect(invitations.queryByRole('alert')).toBeNull()
  })
})
