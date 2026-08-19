import { act, render, screen } from '@testing-library/react'
import { App } from '@/App'
import { useStore } from '@/store'
import { StubSocket } from '@/test/stub-socket'

vi.mock('@/lib/api', async () => {
  const { fakeApi } = await import('@/test/fixtures')
  return { api: fakeApi(), API_BASE: '/api/v1', ApiError: Error }
})

// The shell opens the event stream on mount; keep it off the network.
beforeAll(() => {
  StubSocket.install()
})

/**
 * Renders the app and acknowledges its subscription, which is what releases
 * the hydration fetch.
 */
async function mount() {
  render(<App />)
  await vi.waitFor(() => expect(StubSocket.opened.length).toBeGreaterThan(0))
  const socket = StubSocket.last()
  act(() => {
    socket.onopen?.()
    socket.onmessage?.({ data: JSON.stringify({ ok: true }) })
  })
}

describe('App', () => {
  it('renders the shell and fills it from the server', async () => {
    await mount()

    // Sidebar, from session.list + run.list.
    expect(await screen.findByText('checkout rewrite')).toBeDefined()
    // Center view, from the default route in the registry.
    expect(screen.getByText('Run board')).toBeDefined()
    // Status bar, from server.info.
    expect(screen.getByText('aether 1.2.3')).toBeDefined()
    expect(
      screen.getByLabelText('Disk usage')
        .textContent,
    ).toContain('512 MB / 2.0 GB')
  })

  it('shows the run a sidebar row points at', async () => {
    await mount()
    await screen.findByText('checkout rewrite')

    useStore.getState().navigate('run', { runId: 'run_1' })

    expect(await screen.findByText('aether/run-1-checkout')).toBeDefined()
  })
})
