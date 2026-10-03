import { act, fireEvent, render, screen, within } from '@testing-library/react'
import type * as apiModule from '@/lib/api'
import { App } from '@/App'
import { useStore } from '@/store'
import { StubSocket } from '@/test/stub-socket'

vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof apiModule>()
  const { fakeApi } = await import('@/test/fixtures')
  return { ...actual, api: fakeApi(), API_BASE: '/api/v1', ApiError: Error }
})
// The shell opens the event stream on mount; keep it off the network.
beforeAll(() => {
  StubSocket.install()
})

afterAll(() => vi.unstubAllGlobals())

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
  it('renders the shell and fills it from the server for a member who completed onboarding', async () => {
    useStore.getState().setOnboarded(true)
    await mount()

    // Sidebar, from workspace.list + run.list: the scope on top, its runs
    // below.
    await vi.waitFor(() =>
      expect(sidebar().getByText('rewrite the checkout flow')).toBeDefined(),
    )
    expect(screen.getByRole('combobox', { name: 'Workspace' }).textContent).toBe('main-repo')
    // Completed members keep the default board route. By role: the sidebar
    // nav entry carries the same words.
    expect(screen.getByRole('heading', { level: 1, name: 'Board' })).toBeDefined()
    // Status bar, from server.info.
    fireEvent.click(screen.getByRole('button', { name: 'Show status details' }))
    expect(screen.getByText('aether 1.2.3')).toBeDefined()
    expect(
      screen.getByLabelText('Disk usage')
        .textContent,
    ).toContain('512 MB / 2.0 GB')
  })

  it('shows the terminal tab a sidebar row lands on', async () => {
    await mount()
    await vi.waitFor(() =>
      expect(sidebar().getByText('rewrite the checkout flow')).toBeDefined(),
    )

    const row = sidebar().getByRole('button', { name: /rewrite the checkout flow/ })
    fireEvent.click(row)

    expect(row.getAttribute('aria-current')).toBe('page')
    const strip = await screen.findByRole('tablist', { name: 'Run tabs' })
    await vi.waitFor(() =>
      expect(
        within(strip).getByRole('tab', { name: 'Terminal' }).getAttribute('aria-selected'),
      ).toBe('true'),
    )
  })

  // The launch form is hosted by the shell, not by the palette: a button on
  // any surface opens the real dialog. Asserting the store alone would pass
  // even if nothing were mounted to answer it.
  it('opens the launch form from the title bar, with no palette involved', async () => {
    await mount()
    const titleBar = await screen.findByRole('banner', { name: 'Aether' })
    const launch = await within(titleBar).findByRole('button', { name: 'New run' })

    fireEvent.click(launch)

    expect(await screen.findByText('Launch a run')).toBeDefined()
    expect(await screen.findByLabelText('Target workspace')).toBeDefined()
    expect(useStore.getState().paletteOpen).toBe(false)
  })
})
