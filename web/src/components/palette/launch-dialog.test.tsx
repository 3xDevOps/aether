import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { LaunchDialog } from '@/components/palette/launch-dialog'
import { api } from '@/lib/api'
import { useStore } from '@/store'
import { agentInfo, alice, run, workspace } from '@/test/fixtures'

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
  vi.mocked(api.agentList).mockResolvedValue([agentInfo()])
  useStore.setState({
    workspaces: { [workspace.id]: workspace },
    activeWorkspace: workspace.id,
    paletteDialog: 'launch',
    paletteRunID: null,
    route: { name: 'board', params: {} },
    runs: {},
    lastHarnessByAccount: {},
    capabilities: null,
    onboardingStep: 'Link',
  })
  vi.clearAllMocks()
})

/** The harness list arrives asynchronously; nothing is launchable before it. */
async function open() {
  render(<LaunchDialog />)
  await screen.findByRole('option', { name: 'claude' })
  await waitFor(() =>
    expect((screen.getByLabelText('Agent') as HTMLSelectElement).value).not.toBe(''),
  )
}

function setMode(mode: string) {
  fireEvent.change(screen.getByLabelText('Mode'), { target: { value: mode } })
}

function setTask(task: string) {
  fireEvent.change(screen.getByLabelText(/^Task/), { target: { value: task } })
}

describe('launch dialog', () => {
  it('shows discovery errors and refreshes the list after recovery', async () => {
    vi.mocked(api.agentList).mockRejectedValue(new Error('agent.list: connection closed'))
    render(<LaunchDialog />)
    expect((await screen.findByRole('alert')).textContent).toBe('agent.list: connection closed')
    expect(screen.queryByText(/No agent is installed/)).toBeNull()

    vi.mocked(api.agentList).mockResolvedValue([agentInfo()])
    fireEvent.click(screen.getByRole('button', { name: 'Refresh agents' }))
    await screen.findByRole('option', { name: 'claude' })
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('discovers a completed installation without restarting the app', async () => {
    vi.mocked(api.agentList).mockResolvedValue([agentInfo({ installed: false })])
    render(<LaunchDialog />)
    await screen.findByText(/No agent is installed/)
    expect(screen.queryByRole('option', { name: 'claude' })).toBeNull()
    vi.mocked(api.agentList).mockResolvedValue([agentInfo()])
    fireEvent.click(screen.getByRole('button', { name: 'Refresh agents' }))
    await screen.findByRole('option', { name: 'claude' })
    fireEvent.click(screen.getByRole('button', { name: 'Launch' }))
    await waitFor(() => expect(api.runLaunch).toHaveBeenCalledWith({
      workspace_id: workspace.id, harness: 'claude',
    }))
  })
  it('offers setup, and the pinned custom harness, when nothing is installed', async () => {
    vi.mocked(api.agentList).mockResolvedValue([agentInfo({ installed: false })])
    render(<LaunchDialog />)

    await screen.findByText('No agent is installed in this account.')
    const agent = screen.getByLabelText('Agent') as HTMLSelectElement
    // Nothing is picked for the member, so the launch stays blocked.
    expect(agent.value).toBe('')
    expect(screen.queryByRole('option', { name: 'claude' })).toBeNull()
    expect((screen.getByRole('button', { name: 'Launch' }) as HTMLButtonElement).disabled).toBe(
      true,
    )
    expect(screen.getByRole('button', { name: 'Set up an agent' })).toBeDefined()

    // A deployment can pin "custom" with --harness-definitions, which no
    // account install can satisfy, so it stays launchable by hand.
    fireEvent.change(agent, { target: { value: 'custom' } })
    await waitFor(() =>
      expect((screen.getByRole('button', { name: 'Launch' }) as HTMLButtonElement).disabled).toBe(
        false,
      ),
    )
  })

  it('sends a local gateway to the onboarding wizard at its Agents step', async () => {
    useStore.setState({
      capabilities: { gateway: 'local', methods: ['*'], ws: [], local: ['link.status'] },
    })
    vi.mocked(api.agentList).mockResolvedValue([agentInfo({ installed: false })])
    render(<LaunchDialog />)

    fireEvent.click(await screen.findByRole('button', { name: 'Set up an agent' }))

    expect(useStore.getState().onboardingStep).toBe('Agents')
    expect(useStore.getState().route.name).toBe('onboarding')
    expect(useStore.getState().paletteDialog).toBeNull()
  })

  it('sends a remote gateway to the agents view', async () => {
    useStore.setState({ capabilities: { gateway: 'remote', methods: ['*'], ws: [] } })
    vi.mocked(api.agentList).mockResolvedValue([agentInfo({ installed: false })])
    render(<LaunchDialog />)

    fireEvent.click(await screen.findByRole('button', { name: 'Set up an agent' }))

    expect(useStore.getState().route.name).toBe('agents')
    expect(useStore.getState().paletteDialog).toBeNull()
  })

  it('keeps launch disabled, and setup hidden, when the agent list fails', async () => {
    vi.mocked(api.agentList).mockRejectedValue(new Error('agent.list: connection closed'))
    render(<LaunchDialog />)

    expect((await screen.findByRole('alert')).textContent).toBe('agent.list: connection closed')
    // Setup cannot fix a gateway that did not answer.
    expect(screen.queryByRole('button', { name: 'Set up an agent' })).toBeNull()
    expect((screen.getByLabelText('Agent') as HTMLSelectElement).value).toBe('')
    expect((screen.getByRole('button', { name: 'Launch' }) as HTMLButtonElement).disabled).toBe(
      true,
    )
  })

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
    expect(useStore.getState().lastHarnessByAccount[alice.id]).toBe('claude')
  })

  it('only offers installed agents and remembers the latest one used', async () => {
    useStore.getState().upsertRun(run({ harness: 'myagent' }))
    vi.mocked(api.agentList).mockResolvedValue([
      { name: 'claude', source: 'shipped', installed: true },
      { name: 'codex', source: 'shipped', installed: false },
      { name: 'myagent', source: 'member', installed: true },
    ])

    await open()

    expect(screen.queryByRole('option', { name: 'codex' })).toBeNull()
    await waitFor(() =>
      expect((screen.getByLabelText('Agent') as HTMLSelectElement).value).toBe('myagent'),
    )
  })

  it('prefers the remembered installed harness over run history', async () => {
    useStore.setState({ lastHarnessByAccount: { [alice.id]: 'myagent' } })
    useStore.getState().upsertRun(run({ harness: 'claude' }))
    vi.mocked(api.agentList).mockResolvedValue([
      { name: 'claude', source: 'shipped', installed: true },
      { name: 'myagent', source: 'member', installed: true },
    ])

    await open()

    await waitFor(() =>
      expect((screen.getByLabelText('Agent') as HTMLSelectElement).value).toBe('myagent'),
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
