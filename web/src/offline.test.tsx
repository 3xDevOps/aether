import { render, screen } from '@testing-library/react'
import { toast } from 'sonner'
import { App } from '@/App'
import { useStore } from '@/store'

vi.mock('@/store/sync', () => ({
  connect: vi.fn(() => () => {}),
}))

vi.mock('sonner', () => ({
  toast: { error: vi.fn() },
  Toaster: () => null,
}))

describe('App offline state', () => {
  beforeEach(() => {
    useStore.setState({
      connection: 'offline',
      hydrated: false,
      hydrationError: 'server unreachable: connection refused',
      streamDead: false,
      unreachable: 'server',
    })
  })

  it('replaces the shell with the server-offline page before hydration', () => {
    render(<App />)

    expect(screen.getByRole('heading', { name: 'Cannot reach your Aether server' })).toBeDefined()
    expect(screen.queryByText('Run board')).toBeNull()
  })

  it('does not also toast what the page already says', () => {
    render(<App />)

    // The toast is for a failure the shell survives. When the page IS the
    // failure, a queued toast repeats it into the shell that replaces the
    // page after a successful retry.
    expect(toast.error).not.toHaveBeenCalled()
  })
})

// An in-app update makes the gateway exit and come back on purpose: the
// same disconnect this page normally reports as a failure. The update
// banner sets gatewayRestarting before that happens, and it is never
// cleared - the page below is on its way out regardless of how the
// reconnect goes.
describe('App during an in-app update', () => {
  beforeEach(() => {
    useStore.setState({
      connection: 'offline',
      hydrated: false,
      hydrationError: 'server unreachable: connection refused',
      streamDead: false,
      unreachable: 'server',
      gatewayRestarting: true,
    })
  })

  it('does not replace the shell with the connection-error page', () => {
    render(<App />)

    expect(
      screen.queryByRole('heading', { name: 'Cannot reach your Aether server' }),
    ).toBeNull()
  })
})
