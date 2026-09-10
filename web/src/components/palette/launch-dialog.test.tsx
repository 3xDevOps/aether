import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { LaunchDialog } from '@/components/palette/launch-dialog'
import { api } from '@/lib/api'
import { useStore } from '@/store'
import { agentInfo, alice, run, workspace } from '@/test/fixtures'
import { openSelect, pickOption } from '@/test/select'

vi.mock('@/lib/api', async () => {
  const { fakeApi } = await import('@/test/fixtures')
  return { api: fakeApi(), API_BASE: '/api/v1', ApiError: Error }
})

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
  })
  vi.clearAllMocks()
})

/** The harness list arrives asynchronously; nothing is launchable before it,
 * and until it lands the field still reads its placeholder. */
async function open() {
  render(<LaunchDialog />)
  await waitFor(() =>
    expect(screen.getByLabelText('Agent').textContent).not.toBe('Choose an agent'),
  )
}

async function setMode(mode: string) {
  await pickOption(screen.getByLabelText('Mode'), mode)
}

/** Radix hides the rest of the document while a list is open, so a test that
 * read one has to shut it before touching anything else. */
async function closeList() {
  await userEvent.keyboard('{Escape}')
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
    await waitFor(() =>
      expect(screen.getByLabelText('Agent').textContent).toBe('claude'),
    )
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('discovers a completed installation without restarting the app', async () => {
    vi.mocked(api.agentList).mockResolvedValue([agentInfo({ installed: false })])
    render(<LaunchDialog />)
    await screen.findByText(/No agent is installed/)
    await openSelect(screen.getByLabelText('Agent'))
    expect(screen.queryByRole('option', { name: 'claude' })).toBeNull()
    await closeList()
    vi.mocked(api.agentList).mockResolvedValue([agentInfo()])
    fireEvent.click(screen.getByRole('button', { name: 'Refresh agents' }))
    await waitFor(() =>
      expect(screen.getByLabelText('Agent').textContent).toBe('claude'),
    )
    fireEvent.click(screen.getByRole('button', { name: 'Launch' }))
    await waitFor(() => expect(api.runLaunch).toHaveBeenCalledWith({
      workspace_id: workspace.id, harness: 'claude',
    }))
  })
  it('offers setup, and the pinned custom harness, when nothing is installed', async () => {
    vi.mocked(api.agentList).mockResolvedValue([agentInfo({ installed: false })])
    render(<LaunchDialog />)

    await screen.findByText('No agent is installed in this account.')
    const agent = screen.getByLabelText('Agent')
    // Nothing is picked for the member, so the launch stays blocked.
    expect(agent.textContent).toBe('Choose an agent')
    expect((screen.getByRole('button', { name: 'Launch' }) as HTMLButtonElement).disabled).toBe(
      true,
    )
    expect(screen.getByRole('button', { name: 'Set up an agent' })).toBeDefined()

    await openSelect(agent)
    expect(screen.queryByRole('option', { name: 'claude' })).toBeNull()
    await closeList()

    // A deployment can pin "custom" with --harness-definitions, which no
    // account install can satisfy, so it stays launchable by hand.
    await pickOption(agent, 'custom')
    await waitFor(() =>
      expect((screen.getByRole('button', { name: 'Launch' }) as HTMLButtonElement).disabled).toBe(
        false,
      ),
    )
  })

  it('sends setup to the agents view on every gateway', async () => {
    useStore.setState({
      capabilities: { gateway: 'local', methods: ['*'], ws: [], local: ['link.status'] },
    })
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
    expect(screen.getByLabelText('Agent').textContent).toBe('Choose an agent')
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

    const agent = screen.getByLabelText('Agent')
    await waitFor(() => expect(agent.textContent).toBe('myagent'))
    await openSelect(agent)
    expect(screen.queryByRole('option', { name: 'codex' })).toBeNull()
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
      expect(screen.getByLabelText('Agent').textContent).toBe('myagent'),
    )
  })

  it('refuses a headless launch with no task, and says why', async () => {
    await open()

    await setMode('Headless')

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

    await setMode('Headless')
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
