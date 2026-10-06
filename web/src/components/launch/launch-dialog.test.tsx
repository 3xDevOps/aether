import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { LaunchDialog } from '@/components/launch/launch-dialog'
import { api } from '@/lib/api'
import { useStore } from '@/store'
import { agentInfo, alice, bob, run, runRecords, serverInfo, workspace } from '@/test/fixtures'
import { pickOption } from '@/test/select'

vi.mock('@/lib/api', async () => {
  const { fakeApi } = await import('@/test/fixtures')
  return { api: fakeApi(), API_BASE: '/api/v1', ApiError: Error }
})

const claude = (over: Parameters<typeof agentInfo>[0] = {}) =>
  agentInfo({ display_name: 'Claude Code', enhanced: 'adapter', enhanced_installed: true, login_found: true, default_mode: 'tui', ...over })

beforeEach(() => {
  Element.prototype.scrollIntoView = vi.fn()
  vi.mocked(api.agentList).mockResolvedValue([claude()])
  vi.mocked(api.accountList).mockResolvedValue({ accounts: [alice], shared_with: [] })
  useStore.setState({
    workspaces: { [workspace.id]: workspace },
    activeWorkspace: workspace.id,
    paletteDialog: 'launch',
    paletteRunID: null,
    route: { name: 'board', params: {} },
    runs: {},
    launchDefaults: {},
    capabilities: null,
    info: serverInfo,
  })
  vi.clearAllMocks()
})

const launchButton = () => screen.getByRole('button', { name: 'Launch' }) as HTMLButtonElement
const agentRow = (name: RegExp | string) => screen.getByRole('radio', { name }) as HTMLButtonElement
const modeSegment = (name: string) => within(screen.getByRole('radiogroup', { name: 'Mode' })).getByRole('radio', { name }) as HTMLButtonElement

async function open() {
  render(<LaunchDialog />)
  await waitFor(() => expect(launchButton().disabled).toBe(false))
}

function setTask(task: string) {
  fireEvent.change(screen.getByLabelText('Task'), { target: { value: task } })
}

describe('new run', () => {
  it('lists every agent with its login and install state, and sends setup to Agents', async () => {
    vi.mocked(api.agentList).mockResolvedValue([
      claude(),
      agentInfo({ name: 'codex', display_name: 'Codex', login_found: false }),
      agentInfo({ name: 'pi', display_name: 'Pi', installed: false }),
    ])
    await open()

    expect(agentRow(/^Claude Code/).textContent).toBe('Claude CodeLogin found')
    expect(agentRow(/^Codex/).textContent).toBe('CodexNo login found')
    expect(agentRow(/^Pi/).disabled).toBe(true)
    expect(agentRow(/^Pi/).textContent).toBe('PiNot installed')
    expect(agentRow(/^custom/).disabled).toBe(false)

    fireEvent.click(screen.getByRole('button', { name: 'Set up Pi' }))
    expect(useStore.getState().route.name).toBe('agents')
    expect(useStore.getState().paletteDialog).toBeNull()
  })

  it('launches the preselected agent in Standard and remembers the choice', async () => {
    await open()
    expect(agentRow(/^Claude Code/).getAttribute('aria-checked')).toBe('true')
    expect(modeSegment('Standard').getAttribute('aria-checked')).toBe('true')
    setTask('fix the flaky test')
    fireEvent.click(launchButton())

    await waitFor(() => expect(api.runLaunch).toHaveBeenCalledWith({ workspace_id: workspace.id, harness: 'claude', task: 'fix the flaky test' }))
    expect(useStore.getState().launchDefaults.claude.mode).toBe('tui')
    expect(useStore.getState().route).toEqual({ name: 'terminal', params: { runId: run().id } })
  })

  it('preselects the most recently launched agent in its remembered mode', async () => {
    vi.mocked(api.agentList).mockResolvedValue([claude(), agentInfo({ name: 'codex', display_name: 'Codex' })])
    useStore.setState({ launchDefaults: { claude: { mode: 'tui', at: 1 }, codex: { mode: 'headless', at: 2 } } })
    render(<LaunchDialog />)
    await waitFor(() => expect(agentRow(/^Codex/).getAttribute('aria-checked')).toBe('true'))
    expect(modeSegment('Background').getAttribute('aria-checked')).toBe('true')

    await userEvent.click(agentRow(/^Claude Code/))
    expect(modeSegment('Standard').getAttribute('aria-checked')).toBe('true')
  })

  it('falls back to the newest run when nothing is remembered', async () => {
    vi.mocked(api.agentList).mockResolvedValue([claude(), agentInfo({ name: 'codex', display_name: 'Codex' })])
    useStore.setState({ runs: runRecords(run({ harness: 'codex' })) })
    await open()
    expect(agentRow(/^Codex/).getAttribute('aria-checked')).toBe('true')
  })

  it('starts in the agent\'s default mode', async () => {
    vi.mocked(api.agentList).mockResolvedValue([agentInfo({ name: 'opencode', display_name: 'OpenCode', enhanced: 'native', enhanced_installed: true, default_mode: 'acp' })])
    await open()
    expect(modeSegment('Enhanced').getAttribute('aria-checked')).toBe('true')
    fireEvent.click(launchButton())
    await waitFor(() => expect(api.runLaunch).toHaveBeenCalledWith({ workspace_id: workspace.id, harness: 'opencode', mode: 'acp' }))
  })

  it('disables Enhanced with the reason and a setup link when the adapter is missing', async () => {
    vi.mocked(api.agentList).mockResolvedValue([claude({ enhanced_installed: false })])
    useStore.setState({ launchDefaults: { claude: { mode: 'acp', at: 1 } } })
    await open()
    expect(modeSegment('Enhanced').disabled).toBe(true)
    // A remembered mode the agent cannot use shows as Standard, never as a silent downgrade at launch.
    expect(modeSegment('Standard').getAttribute('aria-checked')).toBe('true')
    const reason = screen.getByText('The Enhanced adapter for Claude Code is not installed.')
    expect(modeSegment('Enhanced').getAttribute('aria-describedby')?.split(' ')).toContain(reason.id)
    fireEvent.click(screen.getByRole('button', { name: 'Set up' }))
    expect(useStore.getState().route.name).toBe('agents')
  })

  it('disables Enhanced without setup for an agent that has no support', async () => {
    vi.mocked(api.agentList).mockResolvedValue([agentInfo({ name: 'aider', source: 'member', enhanced: 'none' })])
    await open()
    expect(modeSegment('Enhanced').disabled).toBe(true)
    expect(screen.getByText('aider has no Enhanced support.')).toBeDefined()
    expect(screen.queryByRole('button', { name: 'Set up' })).toBeNull()
  })

  it('shows the server\'s refusal in the dialog and keeps it open', async () => {
    vi.mocked(api.runLaunch).mockRejectedValueOnce(new Error('scheduler: harness "claude" has no command for mode "acp"'))
    await open()
    await userEvent.click(modeSegment('Enhanced'))
    fireEvent.click(launchButton())

    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toBe('Launch failedscheduler: harness "claude" has no command for mode "acp"')
    expect(alert.scrollIntoView).toHaveBeenCalled()
    expect(api.runLaunch).toHaveBeenCalledWith({ workspace_id: workspace.id, harness: 'claude', mode: 'acp' })
    expect(useStore.getState().paletteDialog).toBe('launch')
    expect(launchButton().disabled).toBe(false)

    await userEvent.click(modeSegment('Standard'))
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('needs a task for Background and says so', async () => {
    await open()
    await userEvent.click(modeSegment('Background'))
    expect(screen.getByText('Required. A Background run starts with this task and takes no input.')).toBeDefined()
    expect(launchButton().disabled).toBe(true)

    setTask('write the changelog')
    fireEvent.click(launchButton())
    await waitFor(() => expect(api.runLaunch).toHaveBeenCalledWith({ workspace_id: workspace.id, harness: 'claude', task: 'write the changelog', mode: 'headless' }))
  })

  it('keeps launch disabled, and setup hidden, when the agent list fails', async () => {
    vi.mocked(api.agentList).mockRejectedValue(new Error('agent.list: connection closed'))
    render(<LaunchDialog />)
    expect((await screen.findByRole('alert')).textContent).toBe('agent.list: connection closed')
    expect(screen.queryByRole('button', { name: 'Set up an agent' })).toBeNull()
    expect(launchButton().disabled).toBe(true)
  })

  it('offers setup, and the pinned custom harness, when nothing is installed', async () => {
    vi.mocked(api.agentList).mockResolvedValue([claude({ installed: false })])
    render(<LaunchDialog />)
    await screen.findByText('No agent is installed in your environment.')
    expect(launchButton().disabled).toBe(true)
    expect(screen.queryByRole('button', { name: 'Set up Claude Code' })).toBeNull()

    await userEvent.click(agentRow(/^custom/))
    expect(launchButton().disabled).toBe(false)
    fireEvent.click(screen.getByRole('button', { name: 'Set up an agent' }))
    expect(useStore.getState().route.name).toBe('agents')
  })

  it('names the workspace the run lands in', async () => {
    await open()
    expect(screen.getByLabelText('Target workspace').textContent).toBe('main-repo from main')
  })

  describe('shared account', () => {
    beforeEach(() => {
      vi.mocked(api.accountList).mockResolvedValue({ accounts: [alice, bob], shared_with: [] })
    })

    it('hides Options without a shared account', async () => {
      vi.mocked(api.accountList).mockResolvedValue({ accounts: [alice], shared_with: [] })
      await open()
      expect(screen.queryByText('Options')).toBeNull()
    })

    it('launches on the account chosen under Options', async () => {
      vi.mocked(api.agentList).mockImplementation(async (account?: string) =>
        account === bob.id ? [agentInfo({ name: 'bob-agent', source: 'member', install_script: undefined })] : [claude()])
      await open()
      await userEvent.click(await screen.findByRole('button', { name: /^Options/ }))
      await pickOption(screen.getByLabelText('Account'), 'Bob (shared)')
      await waitFor(() => expect(agentRow(/^bob-agent/).getAttribute('aria-checked')).toBe('true'))
      expect(screen.getByText(/^Uses Bob's agent login and vendor quota/)).toBeDefined()
      fireEvent.click(launchButton())

      await waitFor(() => expect(api.runLaunch).toHaveBeenCalledWith({ workspace_id: workspace.id, harness: 'bob-agent', account_member_id: bob.id }))
      expect(api.agentList).toHaveBeenCalledWith(bob.id)
    })

    it('gives the server\'s reasons an installed agent cannot launch there', async () => {
      const refusal = 'scheduler: memberhome: login path .claude/.credentials.json in "mem_bob" has another hard link and cannot be shared'
      vi.mocked(api.agentList).mockImplementation(async (account?: string) => account === bob.id
        ? [
            claude({ login_missing: true }),
            agentInfo({ name: 'codex', unavailable: refusal }),
            agentInfo({ name: 'myagent', source: 'member', own_account_only: true }),
          ]
        : [claude()])
      await open()
      await userEvent.click(screen.getByRole('button', { name: /^Options/ }))
      await pickOption(screen.getByLabelText('Account'), 'Bob (shared)')

      const notes = await screen.findByRole('status')
      expect(notes.textContent).toBe(
        'Bob is not logged in to Claude Code, so it cannot launch on this account. Bob logs in from the terminal dock on their own Board; then open this dialog again.'
          + 'Your own agent definitions run only on your own account: myagent. To launch one, choose your own account, marked (you), under Account.'
          + `codex cannot launch on this account: ${refusal}`,
      )
      expect(agentRow(/^Claude Code/).textContent).toBe('Claude CodeNot logged in')
      expect(agentRow(/^codex/).disabled).toBe(true)
      expect(agentRow(/^myagent/).textContent).toBe('myagentYour account only')
      expect(launchButton().disabled).toBe(true)
    })
  })
})

describe('swarm', () => {
  beforeEach(() => {
    useStore.setState({
      capabilities: { gateway: 'remote', methods: ['*'], ws: [] },
      route: { name: 'missions', params: {} },
    })
  })

  async function openSwarm() {
    const view = render(<LaunchDialog />)
    await waitFor(() => expect(agentRow(/^Claude Code/).getAttribute('aria-checked')).toBe('true'))
    const worker = await screen.findByRole('checkbox', { name: /^(Alice · )?Claude Code/ })
    await waitFor(() => expect(worker.getAttribute('aria-checked')).toBe('true'))
    return view
  }

  const createButton = () => screen.getByRole('button', { name: 'Create swarm' }) as HTMLButtonElement
  const workerMode = (name: string) => within(screen.getByRole('radiogroup', { name: 'Worker mode' })).getByRole('radio', { name })

  function setObjective(objective: string) {
    fireEvent.change(screen.getByLabelText('Objective'), { target: { value: objective } })
  }

  async function create(objective: string) {
    setObjective(objective)
    await waitFor(() => expect(createButton().disabled).toBe(false))
    fireEvent.click(createButton())
  }

  it('authorizes a Standard integrator and Background workers by default', async () => {
    await openSwarm()
    expect(screen.getByRole('heading', { name: 'New swarm' })).toBeDefined()
    expect(screen.getByText('The integrator plans the work, starts a worker run per task, and combines the results.')).toBeDefined()
    expect(screen.queryByRole('radiogroup', { name: 'Mode' })).toBeNull()
    expect(workerMode('Background').getAttribute('aria-checked')).toBe('true')

    await create('coordinate checkout work')
    await waitFor(() => expect(api.missionCreate).toHaveBeenCalledTimes(1))
    const params = vi.mocked(api.missionCreate).mock.calls[0][0]
    expect(params.integrator).toEqual({ account_member_id: alice.id, harness: 'claude', mode: 'tui' })
    expect(params.execution_choices).toEqual([
      { account_member_id: alice.id, harness: 'claude', mode: 'headless' },
      { account_member_id: alice.id, harness: 'claude', mode: 'tui' },
    ])
  })

  it('runs a worker whose agent cannot use the chosen mode in Standard, and says so', async () => {
    vi.mocked(api.agentList).mockResolvedValue([claude(), agentInfo({ name: 'pi', display_name: 'Pi', enhanced: 'none' })])
    await openSwarm()
    fireEvent.click(screen.getByRole('checkbox', { name: /Pi/ }))
    await userEvent.click(workerMode('Enhanced'))
    expect(screen.getByRole('checkbox', { name: /Pi/ }).closest('label')?.textContent).toBe('PiRuns Standard: no Enhanced support')

    await create('coordinate checkout work')
    await waitFor(() => expect(api.missionCreate).toHaveBeenCalledTimes(1))
    expect(vi.mocked(api.missionCreate).mock.calls[0][0].execution_choices).toEqual([
      { account_member_id: alice.id, harness: 'claude', mode: 'acp' },
      { account_member_id: alice.id, harness: 'claude', mode: 'tui' },
      { account_member_id: alice.id, harness: 'pi', mode: 'tui' },
    ])
  })

  it('sends a worker choice equal to the integrator choice once', async () => {
    await openSwarm()
    await userEvent.click(workerMode('Standard'))
    await create('coordinate checkout work')
    await waitFor(() => expect(api.missionCreate).toHaveBeenCalledTimes(1))
    expect(vi.mocked(api.missionCreate).mock.calls[0][0].execution_choices).toEqual([
      { account_member_id: alice.id, harness: 'claude', mode: 'tui' },
    ])
  })

  it('offers no worker on an agent a shared account cannot launch, and says why', async () => {
    const refusal = 'scheduler: memberhome: login path .claude/.credentials.json in "mem_bob" has another hard link and cannot be shared'
    vi.mocked(api.accountList).mockResolvedValue({ accounts: [alice, bob], shared_with: [] })
    vi.mocked(api.agentList).mockImplementation(async (account?: string) => account === bob.id
      ? [claude({ login_missing: true }), agentInfo({ name: 'codex', unavailable: refusal }), agentInfo({ name: 'myagent', source: 'member', own_account_only: true })]
      : [claude()])
    await openSwarm()

    const label = (name: RegExp) => screen.getByRole('checkbox', { name }).closest('label')?.textContent
    expect(screen.getByRole('checkbox', { name: /Bob · Claude Code/ }).hasAttribute('disabled')).toBe(true)
    expect(label(/Bob · Claude Code/)).toBe('Bob · Claude CodeBob is not logged in to Claude Code')
    expect(label(/Bob · codex/)).toBe(`Bob · codex${refusal}`)
    expect(label(/Bob · myagent/)).toBe('Bob · myagentyour own definition runs only on your own account')
  })

  it('drops a ticked worker whose agent stops being launchable when the list is read again', async () => {
    vi.mocked(api.accountList).mockResolvedValue({ accounts: [alice, bob], shared_with: [] })
    await openSwarm()
    fireEvent.click(screen.getByRole('checkbox', { name: /Bob · Claude Code/ }))
    await waitFor(() => expect(screen.getByRole('checkbox', { name: /Bob · Claude Code/ }).getAttribute('aria-checked')).toBe('true'))

    vi.mocked(api.agentList).mockImplementation(async (account?: string) => account === bob.id ? [claude({ login_missing: true })] : [claude()])
    await userEvent.click(screen.getByRole('tab', { name: 'Run' }))
    await userEvent.click(screen.getByRole('tab', { name: 'Swarm' }))
    await waitFor(() => expect(screen.getByRole('checkbox', { name: /Bob · Claude Code/ }).hasAttribute('disabled')).toBe(true))
    expect(screen.getByRole('checkbox', { name: /Bob · Claude Code/ }).getAttribute('aria-checked')).toBe('false')

    await create('coordinate without Bob')
    await waitFor(() => expect(api.missionCreate).toHaveBeenCalledTimes(1))
    expect(vi.mocked(api.missionCreate).mock.calls[0][0].execution_choices.some((choice) => choice.account_member_id === bob.id)).toBe(false)
  })

  it('sends the same choices in the same order, under the same key, after a re-tick', async () => {
    vi.mocked(api.agentList).mockResolvedValue([claude(), agentInfo({ name: 'codex', display_name: 'Codex' })])
    vi.mocked(api.missionCreate).mockRejectedValueOnce(new Error('integrator launch failed'))
    await openSwarm()
    fireEvent.click(screen.getByRole('checkbox', { name: /Codex/ }))
    await create('coordinate the re-ticked work')
    await screen.findByRole('alert')
    await waitFor(() => expect(createButton().disabled).toBe(false))

    const worker = screen.getByRole('checkbox', { name: /Claude Code/ })
    fireEvent.click(worker)
    fireEvent.click(worker)
    fireEvent.click(createButton())
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

  it('shows the server\'s refusal verbatim', async () => {
    vi.mocked(api.missionCreate).mockRejectedValueOnce(new Error('mission: coordination is unavailable'))
    await openSwarm()
    await create('coordinate checkout work')
    expect((await screen.findByRole('alert')).textContent).toBe('Swarm not createdmission: coordination is unavailable')
  })

  it('says so in the worker list when reading an account\'s agents fails', async () => {
    vi.mocked(api.accountList).mockResolvedValue({ accounts: [alice, bob], shared_with: [] })
    vi.mocked(api.agentList).mockImplementation(async (account?: string) => {
      if (account === bob.id) throw new Error('agent.list: connection closed')
      return [claude()]
    })
    render(<LaunchDialog />)
    expect((await screen.findByRole('alert')).textContent).toBe('Listing agents for workers failed: agent.list: connection closed')
    expect(agentRow(/^Claude Code/).getAttribute('aria-checked')).toBe('true')
  })

  it('opens on Run, with no tabs, when swarm launch is unavailable', async () => {
    useStore.setState({ capabilities: null })
    await open()
    expect(screen.queryByRole('tab')).toBeNull()
    expect(screen.getByRole('heading', { name: 'New run' })).toBeDefined()
  })

  it('stays on Swarm and says why when the capabilities drop while open', async () => {
    await openSwarm()
    setObjective('coordinate checkout work')
    act(() => useStore.setState({ capabilities: null }))

    expect(screen.getByRole('status').textContent).toBe(
      'Swarm launch is unavailable: the server did not report its capabilities. Switch to Run to launch a run.',
    )
    expect(createButton().disabled).toBe(true)
    await userEvent.click(screen.getByRole('tab', { name: 'Run' }))
    expect(screen.queryByRole('status')).toBeNull()
    await waitFor(() => expect(launchButton().disabled).toBe(false))
  })

  it('names missing capabilities before the role', async () => {
    await openSwarm()
    act(() => useStore.setState({ capabilities: null, info: { ...serverInfo, member: { ...alice, role: 'viewer' } } }))
    expect(screen.getByRole('status').textContent).toBe(
      'Swarm launch is unavailable: the server did not report its capabilities. Switch to Run to launch a run.',
    )
  })

  it('says a role that cannot launch is why swarm launch is unavailable', async () => {
    await openSwarm()
    act(() => useStore.setState({ info: { ...serverInfo, member: { ...alice, role: 'viewer' } } }))
    expect(screen.getByRole('status').textContent).toBe('Swarm launch is unavailable: your role cannot launch.')
    expect(createButton().disabled).toBe(true)
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
      await create(objective)
      await waitFor(() => expect(api.missionCreate).toHaveBeenCalledTimes(calls))
    }

    const first = await openSwarm()
    await submit('coordinate checkout work', 1)
    expect((await screen.findByRole('alert')).textContent).toBe(`Swarm not created${failure.message}`)
    first.unmount()
    await openSwarm()
    await submit('coordinate checkout work', 2)
    expect(key(1)).toBe(key(0))
    await submit('coordinate the payment work', 3)
    expect(key(2)).not.toBe(key(0))
    await submit('coordinate checkout work', 4)
    expect(key(3)).toBe(key(0))
    await submit('coordinate checkout work', 5)
    expect(key(4)).toBe(key(0))

    cleanup()
    await openSwarm()
    await submit('coordinate checkout work', 6)
    expect(key(5)).not.toBe(key(0))
  })
})
