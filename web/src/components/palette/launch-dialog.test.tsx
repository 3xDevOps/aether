import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { LaunchDialog } from '@/components/palette/launch-dialog'
import { api } from '@/lib/api'
import { useStore } from '@/store'
import { agentInfo, alice, run, serverInfo, workspace } from '@/test/fixtures'
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
    await waitFor(() => expect(api.runLaunch).toHaveBeenCalledWith(expect.objectContaining({
      workspace_id: workspace.id, harness: 'claude',
    })))
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
      expect(api.runLaunch).toHaveBeenCalledWith(expect.objectContaining({
        workspace_id: workspace.id,
        harness: 'claude',
        task: 'rewrite the checkout flow',
      })),
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
    expect((screen.getByLabelText(/^Task/) as HTMLTextAreaElement).required).toBe(true)
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
      expect(api.runLaunch).toHaveBeenCalledWith(expect.objectContaining({
        workspace_id: workspace.id,
        harness: 'claude',
        task: 'triage the flaky tests',
        mode: 'headless',
      })),
    )
    // A launch drops the member straight into the run's terminal.
    await waitFor(() => expect(useStore.getState().route.name).toBe('terminal'))
  })

  describe('swarm', () => {
    beforeEach(() => {
      useStore.setState({
        info: serverInfo,
        capabilities: { gateway: 'remote', methods: ['*'], ws: [] },
        route: { name: 'missions', params: {} },
      })
    })

    /** The worker grid fills in after every account's harness list lands. */
    async function openSwarm() {
      const view = render(<LaunchDialog />)
      await waitFor(() =>
        expect(screen.getByLabelText('Integrator harness').textContent).toBe('claude'),
      )
      const worker = await screen.findByRole('checkbox', { name: /Alice · claude/ })
      await waitFor(() => expect((worker as HTMLInputElement).checked).toBe(true))
      return view
    }

    function setObjective(objective: string) {
      fireEvent.change(screen.getByLabelText(/^Objective/), { target: { value: objective } })
    }

    it('authorizes the integrator choice alongside the default worker choice', async () => {
      await openSwarm()
      const locked = screen.getByRole('checkbox', { name: 'Integrator execution choice' }) as HTMLInputElement
      expect(locked.checked).toBe(true)
      expect(locked.disabled).toBe(true)
      expect(screen.getByText('Integrator · Alice · claude · tui')).toBeDefined()

      setObjective('coordinate checkout work')
      fireEvent.click(screen.getByRole('button', { name: 'Create swarm' }))

      await waitFor(() => expect(api.missionCreate).toHaveBeenCalledTimes(1))
      const params = vi.mocked(api.missionCreate).mock.calls[0][0]
      expect(params.integrator).toEqual({ account_member_id: alice.id, harness: 'claude', mode: 'tui' })
      expect(params.execution_choices).toEqual([
        { account_member_id: alice.id, harness: 'claude', mode: 'headless' },
        { account_member_id: alice.id, harness: 'claude', mode: 'tui' },
      ])
    })

    it('keeps the integrator interactive and offers no integrator mode', async () => {
      await openSwarm()
      expect(screen.queryByLabelText('Integrator mode')).toBeNull()
      expect(screen.queryByLabelText('Mode')).toBeNull()
      // The worker row keeps its own mode.
      const worker = screen.getByRole('checkbox', { name: /Alice · claude/ }).closest('label') as HTMLElement
      expect(within(worker).getByRole('combobox').textContent).toBe('headless')

      await pickOption(screen.getByLabelText('Launch type'), 'Single agent')
      expect(screen.getByLabelText('Mode')).toBeDefined()
    })

    it('sends a worker choice equal to the integrator choice once', async () => {
      await openSwarm()
      const worker = screen.getByRole('checkbox', { name: /Alice · claude/ }).closest('label') as HTMLElement
      await pickOption(within(worker).getByRole('combobox'), 'tui')
      setObjective('coordinate checkout work')
      fireEvent.click(screen.getByRole('button', { name: 'Create swarm' }))

      await waitFor(() => expect(api.missionCreate).toHaveBeenCalledTimes(1))
      const params = vi.mocked(api.missionCreate).mock.calls[0][0]
      expect(params.integrator.mode).toBe('tui')
      expect(params.execution_choices).toEqual([
        { account_member_id: alice.id, harness: 'claude', mode: 'tui' },
      ])
    })

    it('sends the same choices in the same order, under the same key, after a re-tick', async () => {
      vi.mocked(api.agentList).mockResolvedValue([agentInfo(), agentInfo({ name: 'codex' })])
      vi.mocked(api.missionCreate).mockRejectedValueOnce(new Error('integrator launch failed'))
      await openSwarm()
      setObjective('coordinate the re-ticked work')
      fireEvent.click(screen.getByRole('checkbox', { name: /Alice · codex/ }))
      const create = screen.getByRole('button', { name: 'Create swarm' }) as HTMLButtonElement
      fireEvent.click(create)
      await screen.findByRole('alert')
      await waitFor(() => expect(create.disabled).toBe(false))

      // Unticking and re-ticking appends the worker at a new position.
      const claude = screen.getByRole('checkbox', { name: /Alice · claude/ })
      fireEvent.click(claude)
      fireEvent.click(claude)
      fireEvent.click(create)
      await waitFor(() => expect(api.missionCreate).toHaveBeenCalledTimes(2))

      const [first, second] = vi.mocked(api.missionCreate).mock.calls.map(([params]) => params)
      expect(second.idempotency_key).toBe(first.idempotency_key)
      expect(second.execution_choices).toEqual(first.execution_choices)
      expect(first.execution_choices).toEqual([
        { account_member_id: alice.id, harness: 'claude', mode: 'headless' },
        { account_member_id: alice.id, harness: 'claude', mode: 'tui' },
        { account_member_id: alice.id, harness: 'codex', mode: 'headless' },
      ])
    })

    it('opens on Single agent when swarm launch is unavailable', async () => {
      useStore.setState({ capabilities: null })
      await open()
      expect(screen.getByRole('button', { name: 'Launch' })).toBeDefined()
      expect(screen.queryByLabelText('Launch type')).toBeNull()
    })

    it('stays on Swarm and says why when the capabilities drop while open', async () => {
      await openSwarm()
      setObjective('coordinate checkout work')
      act(() => useStore.setState({ capabilities: null }))

      expect(screen.getByRole('status').textContent).toBe(
        'Swarm launch is unavailable: the server did not report its capabilities. Switch to Single agent to launch a run.',
      )
      const create = screen.getByRole('button', { name: 'Create swarm' }) as HTMLButtonElement
      expect(create.disabled).toBe(true)
      fireEvent.click(create)
      expect(api.missionCreate).not.toHaveBeenCalled()

      await pickOption(screen.getByLabelText('Launch type'), 'Single agent')
      expect(screen.queryByRole('status')).toBeNull()
      expect((screen.getByRole('button', { name: 'Launch' }) as HTMLButtonElement).disabled).toBe(false)
    })

    it('says a role that cannot launch is why swarm launch is unavailable', async () => {
      await openSwarm()
      act(() => useStore.setState({ info: { ...serverInfo, member: { ...alice, role: 'viewer' } } }))
      expect(screen.getByRole('status').textContent).toBe('Swarm launch is unavailable: your role cannot launch.')
      expect((screen.getByRole('button', { name: 'Create swarm' }) as HTMLButtonElement).disabled).toBe(true)
    })

    it('keeps one key per swarm contents until a create succeeds', async () => {
      const failure = new Error('mission mission_1 exists but its integrator run run_1 did not launch')
      vi.mocked(api.missionCreate)
        .mockRejectedValueOnce(failure)
        .mockRejectedValueOnce(failure)
        .mockRejectedValueOnce(failure)
        .mockRejectedValueOnce(failure)
      const key = (call: number) => vi.mocked(api.missionCreate).mock.calls[call][0].idempotency_key
      const submit = async (objective: string, calls: number) => {
        setObjective(objective)
        const create = screen.getByRole('button', { name: 'Create swarm' }) as HTMLButtonElement
        await waitFor(() => expect(create.disabled).toBe(false))
        fireEvent.click(create)
        await waitFor(() => expect(api.missionCreate).toHaveBeenCalledTimes(calls))
      }

      const first = await openSwarm()
      await submit('coordinate checkout work', 1)
      expect((await screen.findByRole('alert')).textContent).toBe(failure.message)
      // Closing the dialog unmounts it; the same contents still replay.
      first.unmount()
      await openSwarm()
      await submit('coordinate checkout work', 2)
      expect(key(1)).toBe(key(0))
      await submit('coordinate the payment work', 3)
      expect(key(2)).not.toBe(key(0))
      await submit('coordinate checkout work', 4)
      expect(key(3)).toBe(key(0))
      // The fifth call succeeds and closes the dialog.
      await submit('coordinate checkout work', 5)
      expect(key(4)).toBe(key(0))

      cleanup()
      await openSwarm()
      await submit('coordinate checkout work', 6)
      expect(key(5)).not.toBe(key(0))
    })
  })
})
