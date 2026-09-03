import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { LaunchDialog } from '@/components/palette/launch-dialog'
import { api } from '@/lib/api'
import { useStore } from '@/store'
import { workspace } from '@/test/fixtures'

vi.mock('@/lib/api', async () => {
  const { fakeApi } = await import('@/test/fixtures')
  return { api: fakeApi(), API_BASE: '/api/v1', ApiError: Error }
})

// jsdom has neither browser API the dialog reaches for.
Element.prototype.scrollIntoView = vi.fn()
vi.stubGlobal(
  'ResizeObserver',
  class {
    observe() {}
    unobserve() {}
    disconnect() {}
  },
)

beforeEach(() => {
  useStore.setState({
    workspaces: { [workspace.id]: workspace },
    activeWorkspace: workspace.id,
    paletteDialog: 'launch',
    paletteRunID: null,
    route: { name: 'board', params: {} },
  })
  vi.clearAllMocks()
})

/** The harness list arrives asynchronously; nothing is launchable before it. */
async function open() {
  render(<LaunchDialog />)
  await screen.findByRole('option', { name: 'claude' })
}

function setMode(mode: string) {
  fireEvent.change(screen.getByLabelText('Mode'), { target: { value: mode } })
}

function setTask(task: string) {
  fireEvent.change(screen.getByLabelText(/^Task/), { target: { value: task } })
}

describe('launch dialog', () => {
  it('seeds the agent with a task in the default interactive mode', async () => {
    await open()

    setTask('  rewrite the checkout flow  ')
    fireEvent.click(screen.getByRole('button', { name: 'Launch' }))

    // The default mode is the server's own, so it stays off the wire.
    await waitFor(() =>
      expect(api.runLaunch).toHaveBeenCalledWith({
        workspace_id: workspace.id,
        harness: 'claude',
        task: 'rewrite the checkout flow',
      }),
    )
  })

  it('refuses a headless launch with no task, and says why', async () => {
    await open()

    setMode('headless')

    // The server would refuse this launch; the form refuses it first, and a
    // headless task is required rather than optional.
    const launch = screen.getByRole('button', { name: 'Launch' }) as HTMLButtonElement
    expect(launch.disabled).toBe(true)
    expect(screen.getByLabelText('Task (required)')).toBeDefined()
    expect(screen.getByText(/needs a task/)).toBeDefined()

    fireEvent.click(launch)
    expect(api.runLaunch).not.toHaveBeenCalled()
  })

  it('launches headless once a task is written', async () => {
    await open()

    setMode('headless')
    setTask('triage the flaky tests')

    expect(screen.queryByText(/needs a task/)).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Launch' }))

    await waitFor(() =>
      expect(api.runLaunch).toHaveBeenCalledWith({
        workspace_id: workspace.id,
        harness: 'claude',
        task: 'triage the flaky tests',
        mode: 'headless',
      }),
    )
    // A launch drops the member straight into the run's terminal.
    await waitFor(() => expect(useStore.getState().route.name).toBe('terminal'))
  })
})
