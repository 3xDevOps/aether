import { fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { EdgeAccessPage, SignOutButton } from '@/components/edge-access'
import { StatusBar } from '@/components/shell/status-bar'
import { ApiError } from '@/lib/api'
import { useStore } from '@/store'
import { fakeApi, serverInfo } from '@/test/fixtures'
import { atViewport } from '@/test/viewport'

describe('EdgeAccessPage', () => {
  it('offers one sign-in that goes to the server login path', () => {
    render(<EdgeAccessPage access={{ state: 'signed-out' }} onRetry={vi.fn()} client={fakeApi()} />)

    expect(screen.getByRole('heading', { name: 'Sign in to this Aether server' })).toBeDefined()
    const links = screen.getAllByRole('link')
    expect(links).toHaveLength(1)
    expect(links[0].textContent).toBe('Sign in')
    expect(links[0].getAttribute('href')).toBe('/auth/login')
  })

  it('shows a waiting browser its code and the exact commands that approve it', () => {
    const retry = vi.fn()
    render(
      <EdgeAccessPage
        access={{ state: 'pending', approvalCode: 'ABCD-EFGH' }}
        onRetry={retry}
        client={fakeApi()}
      />,
    )

    expect(screen.getByRole('heading', { name: 'This browser is waiting for approval' })).toBeDefined()
    expect(screen.getByText('ABCD-EFGH')).toBeDefined()
    expect(screen.getByText('aether device approve ABCD-EFGH')).toBeDefined()
    expect(screen.getByText('sudo aether-server device approve ABCD-EFGH')).toBeDefined()
    fireEvent.click(screen.getByRole('button', { name: 'Check again' }))
    expect(retry).toHaveBeenCalledOnce()
  })

  it('keeps a failed sign-out on screen in the gateway words', async () => {
    const client = fakeApi({
      signOut: vi.fn(() => Promise.reject(new ApiError(403, 'sign out: cross-site request refused'))),
    })
    render(
      <EdgeAccessPage
        access={{ state: 'pending', approvalCode: 'ABCD-EFGH' }}
        onRetry={vi.fn()}
        client={client}
      />,
    )

    fireEvent.click(screen.getByRole('button', { name: 'Sign out' }))

    expect((await screen.findByRole('alert')).textContent).toBe(
      'sign out: cross-site request refused',
    )
    expect(client.signOut).toHaveBeenCalledOnce()
  })
})

describe('SignOutButton', () => {
  it('exists only on the edge gateway', () => {
    useStore.setState({ capabilities: { gateway: 'server', methods: ['*'], ws: ['events'] } })
    const { container } = render(<SignOutButton client={fakeApi()} />)
    expect(container.textContent).toBe('')
  })

  it('ends the session through the gateway', () => {
    useStore.setState({ capabilities: { gateway: 'edge', methods: ['*'], ws: ['events'] } })
    const client = fakeApi()
    render(<SignOutButton client={client} />)

    fireEvent.click(screen.getByRole('button', { name: 'Sign out' }))

    expect(client.signOut).toHaveBeenCalledOnce()
  })

  it('is reachable from the status details on a phone', async () => {
    atViewport(390, { height: 844, pointer: 'coarse' })
    useStore.setState({
      info: serverInfo,
      capabilities: { gateway: 'edge', methods: ['*'], ws: ['events'] },
      connection: 'live',
      unreachable: null,
      update: null,
      hydrated: true,
    })
    render(<StatusBar />)

    await userEvent.click(screen.getByRole('button', { name: 'Show status details' }))

    expect(screen.getByRole('button', { name: 'Sign out' })).toBeDefined()
  })
})
