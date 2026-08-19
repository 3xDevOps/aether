import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { CommandPalette } from '@/components/palette'
import { api } from '@/lib/api'
import { useStore } from '@/store'
import { toRecord } from '@/store/runs'
import { alice, run, session } from '@/test/fixtures'

vi.mock('@/lib/api', async () => {
  const { fakeApi } = await import('@/test/fixtures')
  return { api: fakeApi(), API_BASE: '/api/v1', ApiError: Error }
})

// jsdom has neither of the two browser APIs the dialog and cmdk reach for.
Element.prototype.scrollIntoView = vi.fn()
vi.stubGlobal(
  'ResizeObserver',
  class {
    observe() {}
    unobserve() {}
    disconnect() {}
  },
)

const active = run({ id: 'run_1', task: 'rewrite the checkout flow' })

beforeEach(() => {
  useStore.setState({
    sessions: { [session.id]: session },
    members: { [alice.id]: alice },
    runs: { [active.id]: toRecord(active) },
    acked: {},
    pausedRuns: {},
    paletteOpen: false,
    paletteDialog: null,
    paletteRunID: null,
    route: { name: 'board', params: {} },
    hydrated: true,
  })
  vi.clearAllMocks()
})

function open() {
  render(<CommandPalette />)
  fireEvent.keyDown(window, { key: 'k', metaKey: true })
}

describe('command palette', () => {
  it('opens on the shortcut and jumps to a run', async () => {
    open()

    const item = await screen.findByText('rewrite the checkout flow')
    fireEvent.click(item)

    expect(useStore.getState().route).toEqual({
      name: 'run',
      params: { runId: 'run_1' },
    })
    expect(useStore.getState().paletteOpen).toBe(false)
    // Revealing a run acknowledges it, here as everywhere else.
    expect(useStore.getState().acked[active.id]).toEqual({
      status: active.status,
      at: active.started_at,
    })
  })

  it('steers the run the centre view is showing, on any of its tabs', async () => {
    // The terminal tab is a route of its own; it carries the same runId.
    useStore.setState({
      route: { name: 'terminal', params: { runId: 'run_1' } },
      pausedRuns: { run_1: false },
    })
    open()

    fireEvent.click(await screen.findByText('Pause run'))

    await waitFor(() => expect(api.runPause).toHaveBeenCalledWith('run_1'))
  })

  it('offers neither pause nor resume while the paused state is unknown', async () => {
    // Nothing seeds pausedRuns at hydration, so after a reload the client
    // cannot tell which verb the server would accept.
    useStore.setState({ route: { name: 'run', params: { runId: 'run_1' } } })
    open()

    await screen.findByText('Kill run')
    expect(screen.queryByText('Pause run')).toBeNull()
    expect(screen.queryByText('Resume run')).toBeNull()
  })

  it('offers no steering verbs without a run in view', async () => {
    open()
    await screen.findByText('rewrite the checkout flow')
    expect(screen.queryByText('Pause run')).toBeNull()
  })

  it('launches a run from the dialog', async () => {
    open()

    fireEvent.click(await screen.findByText('Launch a run...'))
    const task = await screen.findByPlaceholderText('What should the agent do?')
    fireEvent.change(task, { target: { value: 'fix the flaky test' } })
    fireEvent.click(screen.getByRole('button', { name: 'Launch' }))

    await waitFor(() =>
      expect(api.runLaunch).toHaveBeenCalledWith({
        session_id: session.id,
        task: 'fix the flaky test',
        harness: 'claude',
        mode: 'tui',
      }),
    )
    // A launch reveals what it launched.
    await waitFor(() =>
      expect(useStore.getState().route.name).toBe('run'),
    )
  })

  it('launches a templated run from the dialog', async () => {
    open()

    fireEvent.click(await screen.findByText('Launch from a template...'))
    // The session's templates arrive from template.list.
    await screen.findByRole('option', { name: 'nightly triage' })
    fireEvent.click(screen.getByRole('button', { name: 'Launch' }))

    await waitFor(() =>
      expect(api.templateLaunch).toHaveBeenCalledWith(session.id, 'nightly triage'),
    )
    await waitFor(() =>
      expect(useStore.getState().route).toEqual({
        name: 'run',
        params: { runId: 'run_tpl' },
      }),
    )
  })
})
