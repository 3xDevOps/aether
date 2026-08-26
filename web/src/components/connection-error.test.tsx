import { render, screen } from '@testing-library/react'
import { ConnectionError } from '@/components/connection-error'

describe('ConnectionError', () => {
  it('explains that the Aether server is offline and offers retry', () => {
    const retry = vi.fn()

    render(<ConnectionError kind="server" dead={false} error={null} onRetry={retry} />)

    expect(screen.getByRole('heading', { name: 'Cannot reach your Aether server' })).toBeDefined()
    expect(screen.getByText(/server did not answer over SSH/i)).toBeDefined()
    screen.getByRole('button', { name: 'Retry connection' }).click()
    expect(retry).toHaveBeenCalledOnce()
  })

  it('blames the local connection, not the server, when the network is down', () => {
    render(<ConnectionError kind="network" dead={false} error={null} onRetry={vi.fn()} />)

    expect(
      screen.getByRole('heading', { name: 'This computer is offline' }),
    ).toBeDefined()
    // The server is not implicated, so the copy must not send the user to it.
    expect(screen.queryByText(/aether-server/i)).toBeNull()
  })

  it('explains that a dashboard link needs to be minted again', () => {
    render(
      <ConnectionError
        kind={null}
        dead
        error="dashboard token revoked"
        onRetry={vi.fn()}
      />,
    )

    expect(screen.getByRole('heading', { name: 'This dashboard link has expired' })).toBeDefined()
    expect(screen.getByText(/aether gui/i)).toBeDefined()
  })
})
