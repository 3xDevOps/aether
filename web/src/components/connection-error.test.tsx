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

  it('frames a 403 as the gateway refusing this device and keeps the reason', () => {
    render(
      <ConnectionError
        kind="refused"
        dead={false}
        error="tagged tailnet node; the dashboard identifies members by their tailnet login and a tagged node has none"
        onRetry={vi.fn()}
      />,
    )
    expect(screen.getByRole('heading', { name: 'The gateway refused this device' })).toBeDefined()
    expect(screen.getByText(/^tagged tailnet node; the dashboard identifies/)).toBeDefined()
    expect(screen.queryByText(/check your connection/i)).toBeNull()
  })

  it('sends the operator to tailscaled when the server cannot identify the device', () => {
    render(
      <ConnectionError
        kind="identity"
        dead={false}
        error="tailnet identity unavailable: sshd: tailnet whois: dial unix /var/run/tailscale/tailscaled.sock: connect: no such file or directory"
        onRetry={vi.fn()}
      />,
    )
    expect(screen.getByRole('heading', { name: 'The server cannot identify this device' })).toBeDefined()
    expect(screen.getByText(/check that tailscaled is running/)).toBeDefined()
    expect(screen.getByText(/^tailnet identity unavailable: sshd/)).toBeDefined()
    expect(screen.queryByText(/check your connection/i)).toBeNull()
  })

  it('explains that a dashboard link needs to be minted again', () => {
    render(
      <ConnectionError
        kind={null}
        dead
        error="a valid gateway token is required"
        onRetry={vi.fn()}
      />,
    )

    expect(screen.getByRole('heading', { name: 'This dashboard link has expired' })).toBeDefined()
    expect(screen.getByText(/aether gui/i)).toBeDefined()
  })
})
