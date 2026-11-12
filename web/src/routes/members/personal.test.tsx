import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { useState } from 'react'
import { Toaster } from 'sonner'
import { ApiError } from '@/lib/api'
import { ProfileDialog } from '@/routes/members/personal'
import { useStore, type RootState } from '@/store'
import type { Member } from '@/lib/types'
import { optimisticDeadlineMs } from '@/store/optimistic'
import { alice, bob, fakeApi, serverInfo, workspace } from '@/test/fixtures'

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

  it('edits the git identity your agents commit as', () => {
    seed()
    render(<ProfileDialog open onOpenChange={() => {}} client={fakeApi()} />)
    expect(within(screen.getByRole('region', { name: 'Git identity' })).getByRole('button', { name: 'Save identity' })).toBeDefined()
  })

})
