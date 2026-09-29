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
    expect(screen.queryByText(/server host/i)).toBeNull()
  })

  it('sends a phone to Tailscale and the server host, never to aether gui', () => {
    render(<ConnectionError kind="tailnet" dead={false} error={null} onRetry={vi.fn()} />)

    expect(
      screen.getByRole('heading', { name: 'Cannot reach your server over the tailnet' }),
    ).toBeDefined()
    expect(screen.getByText(/Tailscale is connected here/)).toBeDefined()
    expect(screen.getByText(/server host is running/)).toBeDefined()
    // A phone has neither.
    expect(screen.queryByText(/desktop app/i)).toBeNull()
    expect(screen.queryByText(/aether gui/i)).toBeNull()
  })

  it('sends the desktop user back to the gateway they can restart', () => {
    render(<ConnectionError kind="gateway" dead={false} error={null} onRetry={vi.fn()} />)

    expect(
      screen.getByRole('heading', { name: 'Cannot reach the dashboard gateway' }),
    ).toBeDefined()
    expect(screen.getByText(/Restart the desktop app/)).toBeDefined()
    expect(screen.getByText('aether gui')).toBeDefined()
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

  describe('on a link through an edge', () => {
    it('names a server with no edge connection and where to see why', () => {
      render(
        <ConnectionError
          kind="edge-server"
          dead={false}
          error="server unreachable: connect to server abc: edge.example.test refused: server is not connected to the edge (HTTP 503)"
          onRetry={vi.fn()}
        />,
      )

      expect(
        screen.getByRole('heading', { name: 'Your server is not connected to the edge' }),
      ).toBeDefined()
      expect(screen.getByText('sudo aether-server edge status')).toBeDefined()
      expect(screen.getByText(/refused: server is not connected to the edge/)).toBeDefined()
      expect(screen.queryByText(/Tailscale/)).toBeNull()
    })

    it('does not send an edge outage to the server', () => {
      render(<ConnectionError kind="edge" dead={false} error={null} onRetry={vi.fn()} />)

      expect(screen.getByRole('heading', { name: 'Cannot reach the edge' })).toBeDefined()
      expect(screen.getByText(/nothing was asked of the server/)).toBeDefined()
      expect(screen.getByRole('button', { name: 'Retry connection' })).toBeDefined()
    })

    it('gives a signed-out computer the sign-in command for its own edge', () => {
      render(
        <ConnectionError
          kind="signed-out"
          dead={false}
          error={null}
          edge="https://edge.example.test"
          onRetry={vi.fn()}
        />,
      )

      expect(
        screen.getByRole('heading', { name: 'This computer is signed out of the edge' }),
      ).toBeDefined()
      expect(screen.getByText('aether login --edge https://edge.example.test')).toBeDefined()
    })

    it('offers no retry to a revoked device, which a retry cannot fix', () => {
      render(
        <ConnectionError
          kind="device-revoked"
          dead={false}
          error='device "laptop" was revoked on this server'
          onRetry={vi.fn()}
        />,
      )

      expect(screen.getByRole('heading', { name: 'The server revoked this device' })).toBeDefined()
      expect(screen.getByText('device "laptop" was revoked on this server')).toBeDefined()
      expect(screen.queryByRole('button', { name: 'Retry connection' })).toBeNull()
    })

    it('points a pending computer at the approve command the server printed', () => {
      const banner =
        'device "laptop" is waiting for approval. From a device this account already uses, or as an admin, run:\n  aether device approve ABCD-EFGH'
      render(<ConnectionError kind="device-pending" dead={false} error={banner} onRetry={vi.fn()} />)

      expect(
        screen.getByRole('heading', { name: 'This computer is waiting for approval' }),
      ).toBeDefined()
      expect(screen.getByText(/aether device approve ABCD-EFGH/)).toBeDefined()
    })
  })
})
