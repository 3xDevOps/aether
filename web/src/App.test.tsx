import { act, fireEvent, render, screen, within } from '@testing-library/react'
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

/** The sidebar landmark; the board repeats every run task the tree shows. */
function sidebar() {
  return within(screen.getByRole('complementary', { name: 'Runs' }))
}

describe('App', () => {
  it('renders the shell and fills it from the server', async () => {
    await mount()

    // Sidebar, from workspace.list + run.list: the scope on top, its runs
    // below.
    await vi.waitFor(() =>
      expect(sidebar().getByText('rewrite the checkout flow')).toBeDefined(),
    )
    expect(
      (screen.getByLabelText('Workspace') as HTMLSelectElement).value,
    ).toBe('wsp_1')
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
    await vi.waitFor(() =>
      expect(sidebar().getByText('rewrite the checkout flow')).toBeDefined(),
    )

    useStore.getState().navigate('run', { runId: 'run_1' })

    expect(await screen.findByText('aether/run-1-checkout')).toBeDefined()
  })

  // The launch form is hosted by the shell, not by the palette: a button on
  // any surface opens the real dialog. Asserting the store alone would pass
  // even if nothing were mounted to answer it.
  it('opens the launch form from the sidebar, with no palette involved', async () => {
    await mount()
    await vi.waitFor(() =>
      expect(sidebar().getByTitle('Launch a run')).toBeDefined(),
    )

    fireEvent.click(sidebar().getByTitle('Launch a run'))

    expect(await screen.findByText('Launch a run')).toBeDefined()
    expect(await screen.findByLabelText('Target workspace')).toBeDefined()
    expect(useStore.getState().paletteOpen).toBe(false)
  })
})
