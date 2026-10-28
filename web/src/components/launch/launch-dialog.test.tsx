import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { toast } from 'sonner'
import { LaunchDialog } from '@/components/launch/launch-dialog'
import { PaletteDialogs } from '@/components/palette/dialogs'
import { api } from '@/lib/api'
import { useStore } from '@/store'
import { useLaunchDefaults } from '@/store/launch-defaults'
import { optimisticDeadlineMs } from '@/store/optimistic'
import { agentInfo, alice, bob, run, runRecords, serverInfo, vera, workspace } from '@/test/fixtures'
import { chooseModel, pickModel } from '@/test/model'
import { pickOption } from '@/test/select'
import { atViewport } from '@/test/viewport'

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
  vi.mocked(api.launchDefaultsGet).mockResolvedValue({ revision: 0, defaults: {} })
  useLaunchDefaults.setState({ result: null, error: null })
  useStore.setState({
    workspaces: { [workspace.id]: workspace },
    activeWorkspace: workspace.id,
    paletteDialog: 'launch',
    paletteRunID: null,
    route: { name: 'board', params: {} },
    runs: {},
    launchDefaults: {},
    agentModels: {},
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
  it('returns focus to what had it before the dialog opened', async () => {
    render(<button type="button">Board</button>)
    const before = screen.getByRole('button', { name: 'Board' })
    before.focus()
    const close = vi.fn(() => useStore.setState({ paletteDialog: null }))
    useStore.setState({ closePaletteDialog: close })
    const view = render(<LaunchDialog />)
    await waitFor(() => expect(launchButton().disabled).toBe(false))
    expect(document.activeElement).toBe(screen.getByLabelText('Task'))

    await userEvent.keyboard('{Escape}')
    view.unmount()

    await waitFor(() => expect(document.activeElement).toBe(before))
  })

  it('collapses the agent list to the choice on a phone until Change', async () => {
    atViewport(412, { pointer: 'coarse' })
    vi.mocked(api.agentList).mockResolvedValue([
      claude(),
      agentInfo({ name: 'codex', display_name: 'Codex', login_found: true }),
    ])
    await open()

    expect(screen.getAllByRole('radio', { name: /^(Claude Code|Codex)/ })).toHaveLength(1)
    expect(screen.getByRole('radiogroup', { name: 'Mode' })).toBeDefined()

    fireEvent.click(screen.getByRole('button', { name: 'Change agent' }))
    fireEvent.click(agentRow(/^Codex/))

    expect(screen.getAllByRole('radio', { name: /^(Claude Code|Codex)/ })).toHaveLength(1)
    expect(agentRow(/^Codex/).getAttribute('aria-checked')).toBe('true')
  })

  it('lists only installed agents with their login state, and sends Install more agents to Agents', async () => {
    vi.mocked(api.agentList).mockResolvedValue([
      claude(),
      agentInfo({ name: 'codex', display_name: 'Codex', login_found: false }),
      agentInfo({ name: 'pi', display_name: 'Pi', installed: false }),
    ])
    await open()

    expect(agentRow(/^Claude Code/).textContent).toBe('Claude CodeLogin found')
    expect(agentRow(/^Codex/).textContent).toBe('CodexNo login found')
    expect(screen.queryByRole('radio', { name: /^Pi/ })).toBeNull()
    expect(agentRow(/^custom/).disabled).toBe(false)

    fireEvent.click(screen.getByRole('button', { name: 'Install more agents...' }))
    expect(useStore.getState().route.name).toBe('agents')
    expect(useStore.getState().paletteDialog).toBeNull()
  })

  it('refuses limits the server has not confirmed it supports', async () => {
    useStore.setState({ capabilities: { gateway: 'remote', methods: ['*'], ws: [] } })
    vi.mocked(api.serverRunLimits).mockRejectedValue(new Error('server.run_limits: method not found'))
    await open()
    await userEvent.click(screen.getByRole('button', { name: /Resource limits/ }))
    await userEvent.type(screen.getByLabelText('Processes'), '256')
    await userEvent.type(screen.getByLabelText('Task'), 'write the changelog')
    await userEvent.click(launchButton())
    expect((await screen.findByRole('alert')).textContent).toContain('server.run_limits: method not found')
    expect(api.runLaunch).not.toHaveBeenCalled()
    vi.mocked(api.serverRunLimits).mockResolvedValue({ limits: {}, default: { cpus: 8, memory_bytes: 8 * 2 ** 30, pids: 4096 }, effective: { cpus: 8, memory_bytes: 8 * 2 ** 30, pids: 4096 } })
  })

  it('sends the run limits the launcher fills in, and nothing when none are', async () => {
    useStore.setState({ capabilities: { gateway: 'remote', methods: ['*'], ws: [] } })
    await open()
    await userEvent.click(screen.getByRole('button', { name: /Resource limits/ }))
    expect((screen.getByLabelText('Memory (GiB)') as HTMLInputElement).placeholder).toBe('8')
    await userEvent.type(screen.getByLabelText('Memory (GiB)'), '1.5')
    await userEvent.type(screen.getByLabelText('Processes'), '256')
    await userEvent.type(screen.getByLabelText('Task'), 'write the changelog')
    await userEvent.click(launchButton())
    await waitFor(() => expect(api.runLaunch).toHaveBeenCalledWith(expect.objectContaining({ limits: { memory_bytes: 1.5 * 2 ** 30, pids: 256 } })))
  })

  it('launches a never-launched agent in Enhanced when available and remembers the choice', async () => {
    await open()
    expect(agentRow(/^Claude Code/).getAttribute('aria-checked')).toBe('true')
    expect(modeSegment('Enhanced').getAttribute('aria-checked')).toBe('true')
    const [first] = within(screen.getByRole('radiogroup', { name: 'Mode' })).getAllByRole('radio')
    expect(first).toBe(modeSegment('Enhanced'))
    expect(within(first).getByText('Recommended')).toBeDefined()
    setTask('fix the flaky test')
    fireEvent.click(launchButton())

    await waitFor(() => expect(api.runLaunch).toHaveBeenCalledWith({ workspace_id: workspace.id, harness: 'claude', task: 'fix the flaky test', mode: 'acp' }))
    expect(useStore.getState().launchDefaults.claude.mode).toBe('acp')
    expect(useStore.getState().route).toEqual({ name: 'run', params: { runId: run().id } })
  })

  it('saves no choice for a member who signed in while the launch was in flight', async () => {
    let land!: () => void
    vi.mocked(api.runLaunch).mockImplementationOnce(() => new Promise((resolve) => { land = () => resolve(run()) }))
    await open()
    setTask('fix the flaky test')
    fireEvent.click(launchButton())
    await waitFor(() => expect(api.runLaunch).toHaveBeenCalledTimes(1))
    act(() => useStore.getState().setIdentityKey('origin\u0000\u0000mem_other'))
    await act(async () => land())
    expect(useStore.getState().launchDefaults).toEqual({})
    expect(useStore.getState().savedLaunchDefaults).toEqual({})
    useStore.setState({ identityKey: null })
  })

  it('keeps the mode picked for an agent for its next launch', async () => {
    await open()
    await userEvent.click(modeSegment('Standard'))
    fireEvent.click(launchButton())
    await waitFor(() => expect(api.runLaunch).toHaveBeenCalledWith({ workspace_id: workspace.id, harness: 'claude', mode: 'tui' }))
    expect(useStore.getState().launchDefaults.claude.mode).toBe('tui')
    cleanup()
    await open()
    expect(modeSegment('Standard').getAttribute('aria-checked')).toBe('true')
  })

  it('defaults to Standard when the agent has no Enhanced available', async () => {
    vi.mocked(api.agentList).mockResolvedValue([claude({ enhanced_installed: false })])
    await open()
    expect(modeSegment('Standard').getAttribute('aria-checked')).toBe('true')
    expect(within(modeSegment('Enhanced')).queryByText('Recommended')).toBeNull()
  })

  it('sends a model picked from the agent\'s discovered list', async () => {
    useStore.setState({ capabilities: { gateway: 'remote', methods: ['*'], ws: [] } })
    vi.mocked(api.agentModels).mockResolvedValue({ models: [{ id: 'claude-opus-4-1', name: 'Opus 4.1' }], source: 'agent' })
    await open()
    await pickModel(screen.getByRole('combobox', { name: 'Model' }), /^Opus 4.1/)
    fireEvent.click(launchButton())

    await waitFor(() => expect(api.runLaunch).toHaveBeenCalledWith({ workspace_id: workspace.id, harness: 'claude', mode: 'acp', model: 'claude-opus-4-1' }))
  })

  it('sends a typed model and preselects it for that agent next time', async () => {
    await open()
    await chooseModel(screen.getByRole('combobox', { name: 'Model' }), 'opus')
    fireEvent.click(launchButton())

    await waitFor(() => expect(api.runLaunch).toHaveBeenCalledWith({ workspace_id: workspace.id, harness: 'claude', mode: 'acp', model: 'opus' }))
    expect(useStore.getState().launchDefaults.claude.model).toBe('opus')
    cleanup()
    await open()
    expect(screen.getByRole('combobox', { name: 'Model' }).textContent).toBe('opus')
  })

  it('sends a picked effort, and offers none and forgets it for an agent without effort levels', async () => {
    vi.mocked(api.agentList).mockResolvedValue([claude({ effort_levels: ['low', 'high', 'max'] }), agentInfo({ name: 'codex', display_name: 'Codex', login_found: true })])
    await open()
    const effort = () => screen.getByRole('combobox', { name: 'Effort' })
    expect(effort().textContent).toBe('Agent default')
    await pickOption(effort(), 'max')
    fireEvent.click(agentRow(/^Codex/))
    expect(screen.queryByRole('combobox', { name: 'Effort' })).toBeNull()
    fireEvent.click(agentRow(/^Claude Code/))
    expect(effort().textContent).toBe('Agent default')

    await pickOption(effort(), 'high')
    fireEvent.click(launchButton())
    await waitFor(() => expect(api.runLaunch).toHaveBeenCalledWith({ workspace_id: workspace.id, harness: 'claude', mode: 'acp', effort: 'high' }))
    expect(useStore.getState().launchDefaults.claude).not.toHaveProperty('effort')
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

  it('says the launch is in flight, and past the deadline that it is still waiting', async () => {
    const answer = Promise.withResolvers<never>()
    vi.mocked(api.runLaunch).mockReturnValueOnce(answer.promise)
    await open()
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    try {
      fireEvent.click(launchButton())
      const pending = screen.getByRole('button', { name: 'Launching…' }) as HTMLButtonElement
      expect(pending.disabled).toBe(true)
      act(() => vi.advanceTimersByTime(optimisticDeadlineMs))
      expect(screen.getByRole('button', { name: 'Still waiting for the server…' })).toBe(pending)
    } finally {
      vi.useRealTimers()
    }
    await act(async () => answer.reject(new Error('scheduler: harness update timed out')))
    expect((await screen.findByRole('alert')).textContent).toBe('Launch failedscheduler: harness update timed out')
    expect(launchButton().disabled).toBe(false)
    expect(useStore.getState().paletteDialog).toBe('launch')
  })

  it('toasts the refusal of a launch whose dialog was closed while it ran', async () => {
    const answer = Promise.withResolvers<never>()
    vi.mocked(api.runLaunch).mockReturnValueOnce(answer.promise)
    const error = vi.spyOn(toast, 'error')
    const view = render(<LaunchDialog />)
    await waitFor(() => expect(launchButton().disabled).toBe(false))
    fireEvent.click(launchButton())
    view.unmount()
    await act(async () => answer.reject(new Error('scheduler: harness update timed out')))
    expect(error).toHaveBeenCalledWith('Launch failed: scheduler: harness update timed out')
    error.mockRestore()
  })

  it('shows what the workspace setup script printed when it fails the launch', async () => {
    vi.mocked(api.runLaunch).mockRejectedValueOnce(Object.assign(
      new Error('run.launch: provisioning: start container: runtime: setup script exited 1'),
      { data: { setup_output: 'npm error code E401\n' } },
    ))
    await open()
    fireEvent.click(launchButton())

    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toContain('setup script exited 1')
    expect(within(alert).getByLabelText('Setup script output').textContent).toBe('npm error code E401\n')
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

      await waitFor(() => expect(api.runLaunch).toHaveBeenCalledWith({ workspace_id: workspace.id, harness: 'bob-agent', mode: 'tui', account_member_id: bob.id }))
      expect(api.agentList).toHaveBeenCalledWith(bob.id)
    })

    it('sends the ticked fallback accounts in the order shown once one is moved or unticked', async () => {
      vi.mocked(api.accountList).mockResolvedValue({ accounts: [alice, bob, vera, { ...alice, id: 'mem_carol', display_name: 'Carol' }], shared_with: [] })
      await open()
      await userEvent.click(screen.getByRole('button', { name: /^Options/ }))
      const fallbacks = screen.getByRole('list', { name: 'Fallback accounts' })
      expect(within(fallbacks).getAllByRole('checkbox').map((box) => box.closest('label')?.textContent)).toEqual(['Bob (shared)', 'Vera (shared)', 'Carol (shared)'])
      expect(screen.getByText("The run starts on the account here with the most usage left if Alice's usage is at 95% or more at launch, and continues there if the vendor reports the limit mid-run.")).toBeDefined()

      await userEvent.click(screen.getByRole('button', { name: 'Move Carol (shared) up' }))
      await userEvent.click(screen.getByRole('checkbox', { name: 'Bob (shared)' }))
      fireEvent.click(launchButton())

      await waitFor(() => expect(api.runLaunch).toHaveBeenCalledWith({ workspace_id: workspace.id, harness: 'claude', mode: 'acp', fallback_account_member_ids: ['mem_carol', vera.id] }))
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
        'Bob is not logged in to Claude Code, so it cannot launch on this account. Bob logs in from their own Environment terminal; then open this dialog again.'
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

describe('extra repos', () => {
  const docs = { ...workspace, id: 'wsp_docs', name: 'docs-site' }
  const guides = { ...workspace, id: 'wsp_guides', name: 'guides' }
  const two = { [workspace.id]: workspace, [docs.id]: docs, [guides.id]: guides }
  const remote = { gateway: 'remote' as const, methods: ['*'], ws: [] }

  beforeEach(() => {
    useStore.setState({ info: { ...serverInfo, features: ['multi-repo'] } })
  })

  it('adds other workspaces as chips and launches with their ids', async () => {
    useStore.setState({ workspaces: two, capabilities: { gateway: 'remote', methods: ['*'], ws: [] } })
    await open()
    await userEvent.click(screen.getByRole('button', { name: 'Add repos' }))
    const list = within(await screen.findByRole('dialog', { name: 'Add repos' }))
    expect(list.queryByText('main-repo')).toBeNull()
    await userEvent.click(list.getByRole('checkbox', { name: /docs-site/ }))
    await userEvent.keyboard('{Escape}')

    expect(screen.getByRole('button', { name: 'Remove docs-site' })).toBeTruthy()
    expect(screen.getByText(/at \/repos\/<name>/)).toBeTruthy()
    fireEvent.click(launchButton())
    await waitFor(() => expect(api.runLaunch).toHaveBeenCalledWith(expect.objectContaining({ workspace_id: workspace.id, extra_workspace_ids: [docs.id] })))
  })

  it('removes a chip and then sends no extras', async () => {
    useStore.setState({ workspaces: two, capabilities: { gateway: 'remote', methods: ['*'], ws: [] } })
    await open()
    await userEvent.click(screen.getByRole('button', { name: 'Add repos' }))
    await userEvent.click(within(await screen.findByRole('dialog', { name: 'Add repos' })).getByRole('checkbox', { name: /guides/ }))
    await userEvent.keyboard('{Escape}')
    await userEvent.click(screen.getByRole('button', { name: 'Remove guides' }))
    fireEvent.click(launchButton())
    await waitFor(() => expect(api.runLaunch).toHaveBeenCalled())
    expect(vi.mocked(api.runLaunch).mock.calls[0]![0]).not.toHaveProperty('extra_workspace_ids')
  })

  it('is hidden with a single workspace and when the server does not offer it', async () => {
    useStore.setState({ capabilities: { gateway: 'remote', methods: ['*'], ws: [] } })
    await open()
    expect(screen.queryByRole('button', { name: 'Add repos' })).toBeNull()
    cleanup()

    useStore.setState({ workspaces: two, capabilities: { gateway: 'remote', methods: ['run.launch'], ws: [] } })
    await open()
    expect(screen.queryByRole('button', { name: 'Add repos' })).toBeNull()
    cleanup()

    useStore.setState({ workspaces: two, capabilities: remote, info: { ...serverInfo } })
    await open()
    expect(screen.queryByRole('button', { name: 'Add repos' })).toBeNull()
  })
})

describe('swarm', () => {
  beforeEach(() => {
    vi.mocked(api.launchDefaultsGet).mockResolvedValue({ revision: 1, defaults: { agent: 'claude', agents: { claude: { mode: 'tui' } } } })
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

  it('starts a never-launched integrator in Enhanced and saves the swarm choice on the server', async () => {
    vi.mocked(api.launchDefaultsGet).mockResolvedValue({ revision: 0, defaults: {} })
    await openSwarm()
    expect(within(screen.getByRole('radiogroup', { name: 'Integrator mode' })).getByRole('radio', { name: 'Enhanced' }).getAttribute('aria-checked')).toBe('true')
    await userEvent.click(within(screen.getByRole('radiogroup', { name: 'Integrator mode' })).getByRole('radio', { name: 'Standard' }))
    await create('coordinate checkout work')
    await waitFor(() => expect(api.missionCreate).toHaveBeenCalledTimes(1))
    expect(useStore.getState().launchDefaults.claude.mode).toBe('tui')
    await waitFor(() => expect(api.launchDefaultsSet).toHaveBeenCalledWith({ defaults: { agent: 'claude', agents: { claude: { mode: 'tui' } } }, revision: 0 }))
  })

  it('sends an Enhanced integrator and Enhanced workers for a never-launched agent', async () => {
    vi.mocked(api.launchDefaultsGet).mockResolvedValue({ revision: 0, defaults: {} })
    await openSwarm()
    await create('coordinate checkout work')
    await waitFor(() => expect(api.missionCreate).toHaveBeenCalledTimes(1))
    const params = vi.mocked(api.missionCreate).mock.calls[0][0]
    expect(params.integrator).toEqual({ account_member_id: alice.id, harness: 'claude', mode: 'acp' })
    expect(params.execution_choices).toEqual([{ account_member_id: alice.id, harness: 'claude', mode: 'acp' }])
  })

  it('authorizes the remembered Standard integrator and Enhanced workers by default', async () => {
    await openSwarm()
    expect(screen.getByRole('heading', { name: 'New swarm' })).toBeDefined()
    expect(screen.getByText('The integrator plans the work, starts a worker run per task, and combines the results.')).toBeDefined()
    expect(screen.queryByRole('radiogroup', { name: 'Mode' })).toBeNull()
    expect(workerMode('Enhanced').getAttribute('aria-checked')).toBe('true')

    await create('coordinate checkout work')
    await waitFor(() => expect(api.missionCreate).toHaveBeenCalledTimes(1))
    const params = vi.mocked(api.missionCreate).mock.calls[0][0]
    expect(params.integrator).toEqual({ account_member_id: alice.id, harness: 'claude', mode: 'tui' })
    expect(params.execution_choices).toEqual([
      { account_member_id: alice.id, harness: 'claude', mode: 'acp' },
      { account_member_id: alice.id, harness: 'claude', mode: 'tui' },
    ])
  })

  it('disables Standard for an agent Aether cannot wake, with the reason in a tooltip', async () => {
    const reason = 'codex in Standard mode has no idle wake; start it in Enhanced'
    const codex = (over: Parameters<typeof agentInfo>[0] = {}) =>
      agentInfo({ name: 'codex', display_name: 'Codex', enhanced: 'adapter', enhanced_installed: true, swarm_standard_refusal: reason, ...over })
    vi.mocked(api.agentList).mockResolvedValue([claude(), codex(), codex({ name: 'antigravity', display_name: 'Antigravity', enhanced: 'none', enhanced_installed: false })])
    vi.mocked(api.launchDefaultsGet).mockResolvedValue({ revision: 1, defaults: { agent: 'codex', agents: { codex: { mode: 'tui' } } } })
    render(<LaunchDialog />)
    await waitFor(() => expect(agentRow(/^Codex/).getAttribute('aria-checked')).toBe('true'))
    const integratorMode = (name: string) =>
      within(screen.getByRole('radiogroup', { name: 'Integrator mode' })).getByRole('radio', { name }) as HTMLButtonElement
    expect(integratorMode('Standard').disabled).toBe(true)
    expect(integratorMode('Enhanced').getAttribute('aria-checked')).toBe('true')
    await userEvent.hover(integratorMode('Standard').parentElement!)
    expect((await screen.findByRole('tooltip')).textContent).toBe(reason)

    const worker = (name: RegExp) => screen.getByRole('checkbox', { name })
    await waitFor(() => expect(worker(/^Claude Code/).getAttribute('aria-checked')).toBe('true'))
    fireEvent.click(worker(/^Codex/))
    fireEvent.click(worker(/^Claude Code/))
    expect(workerMode('Standard').hasAttribute('disabled')).toBe(true)
    fireEvent.click(worker(/^Claude Code/))
    fireEvent.click(worker(/^Antigravity/))
    await userEvent.click(workerMode('Standard'))
    expect(worker(/^Codex/).closest('label')?.textContent).toBe('CodexRuns Enhanced: no idle wake in Standard')
    expect(worker(/^Antigravity/).closest('label')?.textContent).toBe('AntigravityRuns Background: no idle wake in Standard')

    setObjective('coordinate checkout work')
    await userEvent.click(agentRow(/^Antigravity/))
    for (const name of ['Standard', 'Enhanced', 'Background']) expect(integratorMode(name).disabled).toBe(true)
    expect(createButton().disabled).toBe(true)

    await userEvent.click(agentRow(/^Codex/))
    await create('coordinate checkout work')
    await waitFor(() => expect(api.missionCreate).toHaveBeenCalledTimes(1))
    expect(vi.mocked(api.missionCreate).mock.calls[0][0].execution_choices).toEqual([
      { account_member_id: alice.id, harness: 'antigravity', mode: 'headless' },
      { account_member_id: alice.id, harness: 'claude', mode: 'tui' },
      { account_member_id: alice.id, harness: 'codex', mode: 'acp' },
    ])
  })

  it('launches an Enhanced integrator and refuses Background with the reason', async () => {
    await openSwarm()
    const integratorMode = (name: string) =>
      within(screen.getByRole('radiogroup', { name: 'Integrator mode' })).getByRole('radio', { name }) as HTMLButtonElement
    expect(integratorMode('Standard').getAttribute('aria-checked')).toBe('true')
    expect(integratorMode('Background').disabled).toBe(true)
    expect(screen.getByText('Background is not offered: it exits after one turn, so the integrator could not be asked or told.')).toBeDefined()
    await userEvent.click(integratorMode('Enhanced'))
    await userEvent.click(workerMode('Standard'))

    await create('coordinate checkout work')
    await waitFor(() => expect(api.missionCreate).toHaveBeenCalledTimes(1))
    const params = vi.mocked(api.missionCreate).mock.calls[0][0]
    expect(params.integrator).toEqual({ account_member_id: alice.id, harness: 'claude', mode: 'acp' })
    expect(params.execution_choices).toEqual([
      { account_member_id: alice.id, harness: 'claude', mode: 'acp' },
      { account_member_id: alice.id, harness: 'claude', mode: 'tui' },
    ])
  })

  it('falls back to a Standard integrator when the agent has no Enhanced support', async () => {
    vi.mocked(api.agentList).mockResolvedValue([claude({ enhanced: 'none' })])
    await openSwarm()
    const enhanced = within(screen.getByRole('radiogroup', { name: 'Integrator mode' })).getByRole('radio', { name: 'Enhanced' }) as HTMLButtonElement
    expect(enhanced.disabled).toBe(true)
    await create('coordinate checkout work')
    await waitFor(() => expect(api.missionCreate).toHaveBeenCalledTimes(1))
    expect(vi.mocked(api.missionCreate).mock.calls[0][0].integrator.mode).toBe('tui')
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

  it('sends the integrator and worker models, keeping a worker that differs only by model', async () => {
    await openSwarm()
    await userEvent.click(workerMode('Standard'))
    await chooseModel(screen.getByRole('combobox', { name: 'Integrator model' }), 'opus')
    await chooseModel(screen.getByRole('combobox', { name: 'Model for Claude Code' }), 'haiku')
    await create('coordinate checkout work')
    await waitFor(() => expect(api.missionCreate).toHaveBeenCalledTimes(1))
    const params = vi.mocked(api.missionCreate).mock.calls[0][0]
    expect(params.integrator).toEqual({ account_member_id: alice.id, harness: 'claude', mode: 'tui', model: 'opus' })
    expect(params.execution_choices).toEqual([
      { account_member_id: alice.id, harness: 'claude', mode: 'tui', model: 'haiku' },
      { account_member_id: alice.id, harness: 'claude', mode: 'tui', model: 'opus' },
    ])
  })

  it('sends the integrator and worker efforts, keeping a worker that differs only by effort', async () => {
    vi.mocked(api.agentList).mockResolvedValue([claude({ effort_levels: ['low', 'high'] })])
    await openSwarm()
    await userEvent.click(workerMode('Standard'))
    await pickOption(screen.getByRole('combobox', { name: 'Integrator effort' }), 'high')
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('combobox', { name: 'Integrator effort' })))
    await pickOption(screen.getByRole('combobox', { name: 'Effort for Claude Code' }), 'low')
    await create('coordinate checkout work')
    await waitFor(() => expect(api.missionCreate).toHaveBeenCalledTimes(1))
    const params = vi.mocked(api.missionCreate).mock.calls[0][0]
    expect(params.integrator).toEqual({ account_member_id: alice.id, harness: 'claude', mode: 'tui', effort: 'high' })
    expect(params.execution_choices).toEqual([
      { account_member_id: alice.id, harness: 'claude', mode: 'tui', effort: 'high' },
      { account_member_id: alice.id, harness: 'claude', mode: 'tui', effort: 'low' },
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
      { account_member_id: alice.id, harness: 'claude', mode: 'acp' },
      { account_member_id: alice.id, harness: 'claude', mode: 'tui' },
      { account_member_id: alice.id, harness: 'codex', mode: 'tui' },
    ])
  })

  it("lists the integrator's account as a fallback for the swarm's workers", async () => {
    vi.mocked(api.accountList).mockResolvedValue({ accounts: [alice, bob], shared_with: [] })
    await openSwarm()
    await userEvent.click(screen.getByRole('button', { name: /^Options/ }))
    const fallbacks = screen.getByRole('list', { name: 'Fallback accounts' })
    expect(within(fallbacks).getAllByRole('checkbox').map((box) => box.closest('label')?.textContent)).toEqual(['Alice (you)', 'Bob (shared)'])
    await userEvent.click(screen.getByRole('button', { name: 'Move Bob (shared) up' }))

    await create('coordinate checkout work')
    await waitFor(() => expect(api.missionCreate).toHaveBeenCalledTimes(1))
    expect(vi.mocked(api.missionCreate).mock.calls[0][0].fallback_account_member_ids).toEqual([bob.id, alice.id])
  })

  it('creates the swarm with fallback off when it is turned off under Options', async () => {
    vi.mocked(api.accountList).mockResolvedValue({ accounts: [alice, bob], shared_with: [] })
    await openSwarm()
    await userEvent.click(screen.getByRole('button', { name: /^Options/ }))
    await userEvent.click(screen.getByRole('checkbox', { name: 'Fall back to another account' }))
    expect(screen.getByRole('button', { name: /^Options/ }).textContent).toBe('Options· Integrator account: Alice (you) · Fallback off')

    await create('coordinate checkout work')
    await waitFor(() => expect(api.missionCreate).toHaveBeenCalledTimes(1))
    const params = vi.mocked(api.missionCreate).mock.calls[0][0]
    expect(params.no_fallback).toBe(true)
    expect(params.fallback_account_member_ids).toBeUndefined()
  })

  describe('extra repos', () => {
    const docs = { ...workspace, id: 'wsp_docs', name: 'docs-site' }

    async function addDocs() {
      await userEvent.click(screen.getByRole('button', { name: 'Add repos' }))
      await userEvent.click(within(await screen.findByRole('dialog', { name: 'Add repos' })).getByRole('checkbox', { name: /docs-site/ }))
      await userEvent.keyboard('{Escape}')
    }

    beforeEach(() => {
      useStore.setState({ workspaces: { [workspace.id]: workspace, [docs.id]: docs } })
    })

    it('creates the swarm with them and says candidates combine /workspace only', async () => {
      useStore.setState({ info: { ...serverInfo, features: ['multi-repo', 'swarm-repos'] } })
      await openSwarm()
      await addDocs()
      expect(screen.getByText(/Candidates combine \/workspace only/)).toBeTruthy()
      await create('coordinate checkout work')
      await waitFor(() => expect(api.missionCreate).toHaveBeenCalledTimes(1))
      expect(vi.mocked(api.missionCreate).mock.calls[0][0].extra_workspace_ids).toEqual([docs.id])
    })

    it('sends none to a server whose swarms would drop them, even ones picked for a run', async () => {
      useStore.setState({ info: { ...serverInfo, features: ['multi-repo'] } })
      await openSwarm()
      expect(screen.queryByRole('button', { name: 'Add repos' })).toBeNull()
      await userEvent.click(screen.getByRole('tab', { name: 'Run' }))
      await addDocs()
      await userEvent.click(screen.getByRole('tab', { name: 'Swarm' }))
      await create('coordinate checkout work')
      await waitFor(() => expect(api.missionCreate).toHaveBeenCalledTimes(1))
      expect(vi.mocked(api.missionCreate).mock.calls[0][0]).not.toHaveProperty('extra_workspace_ids')
    })
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
    const failure = new Error('swarm mission_1 exists but its integrator run run_1 did not launch')
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

describe('saved launch defaults', () => {
  const everyMethod = { gateway: 'remote' as const, methods: ['*'], ws: [] }

  beforeEach(() => {
    vi.mocked(api.agentList).mockResolvedValue([claude(), agentInfo({ name: 'codex', display_name: 'Codex', login_found: true })])
    useStore.setState({ capabilities: everyMethod, launchDefaults: { codex: { mode: 'headless', at: 2 } } })
  })

  it('preselects the server\'s saved agent, mode and model over this browser\'s, and saves the launch there', async () => {
    vi.mocked(api.launchDefaultsGet).mockResolvedValue({ revision: 3, defaults: { agent: 'claude', agents: { claude: { mode: 'tui', model: 'opus' } }, account_member_id: '' } })
    await open()
    expect(agentRow(/^Claude Code/).getAttribute('aria-checked')).toBe('true')
    expect(modeSegment('Standard').getAttribute('aria-checked')).toBe('true')
    expect(screen.getByRole('combobox', { name: 'Model' }).textContent).toBe('opus')

    fireEvent.click(launchButton())
    await waitFor(() => expect(api.runLaunch).toHaveBeenCalledWith({ workspace_id: workspace.id, harness: 'claude', mode: 'tui', model: 'opus' }))
    await waitFor(() => expect(api.launchDefaultsSet).toHaveBeenCalledWith({
      defaults: { agent: 'claude', agents: { claude: { mode: 'tui', model: 'opus' } }, account_member_id: '' },
      revision: 3,
    }))
  })

  it('uses this browser\'s choices when the server keeps none', async () => {
    useStore.setState({ capabilities: { gateway: 'remote', methods: ['run.launch', 'agent.list', 'account.list'], ws: [] } })
    render(<LaunchDialog />)
    await waitFor(() => expect(agentRow(/^Codex/).getAttribute('aria-checked')).toBe('true'))
    expect(modeSegment('Background').getAttribute('aria-checked')).toBe('true')
    expect(api.launchDefaultsGet).not.toHaveBeenCalled()
  })

  it('says when the saved choices cannot be read, uses this browser\'s, and presets the saved ones once a retry reads them', async () => {
    vi.mocked(api.accountList).mockResolvedValue({ accounts: [alice, bob], shared_with: [] })
    vi.mocked(api.launchDefaultsGet).mockRejectedValue(new Error('launch.defaults.get: connection refused'))
    render(<LaunchDialog />)
    await waitFor(() => expect(agentRow(/^Codex/).getAttribute('aria-checked')).toBe('true'))
    const alert = (await screen.findByText('Your saved launch choices could not be read.')).closest<HTMLElement>('[role="alert"]')!
    expect(alert.textContent).toContain('connection refused')
    expect(alert.textContent).toContain('this browser\'s last launch')

    vi.mocked(api.launchDefaultsGet).mockResolvedValue({ revision: 2, defaults: { agent: 'claude', agents: { claude: { mode: 'tui' } }, account_member_id: bob.id } })
    fireEvent.click(within(alert).getByRole('button', { name: 'Try again' }))
    await waitFor(() => expect(agentRow(/^Claude Code/).getAttribute('aria-checked')).toBe('true'))
    expect(modeSegment('Standard').getAttribute('aria-checked')).toBe('true')
    await waitFor(() => expect(api.agentList).toHaveBeenCalledWith(bob.id))
    expect(screen.queryByText('Your saved launch choices could not be read.')).toBeNull()
  })

  it('goes back to your own account when a retry finds the cached saved account cleared', async () => {
    vi.mocked(api.accountList).mockResolvedValue({ accounts: [alice, bob], shared_with: [] })
    useLaunchDefaults.setState({ result: { revision: 2, defaults: { agent: 'claude', account_member_id: bob.id, fallback: { policy: 'off' } } }, error: null })
    vi.mocked(api.launchDefaultsGet).mockRejectedValue(new Error('connection refused'))
    render(<LaunchDialog />)
    const alert = (await screen.findByText('Your saved launch choices could not be read.')).closest<HTMLElement>('[role="alert"]')!
    await waitFor(() => expect(api.agentList).toHaveBeenCalledWith(bob.id))

    vi.mocked(api.launchDefaultsGet).mockResolvedValue({ revision: 3, defaults: {} })
    fireEvent.click(within(alert).getByRole('button', { name: 'Try again' }))
    await waitFor(() => expect(vi.mocked(api.agentList).mock.lastCall).toEqual([undefined]))
    await waitFor(() => expect(launchButton().disabled).toBe(false))
    fireEvent.click(launchButton())
    await waitFor(() => expect(api.runLaunch).toHaveBeenCalledWith({ workspace_id: workspace.id, harness: 'claude', mode: 'acp' }))
  })

  it('reads the saved choices once per open', async () => {
    await open()
    expect(api.launchDefaultsGet).toHaveBeenCalledTimes(1)
  })

  it('starts over when a different member signs in while it is open', async () => {
    useStore.setState({ identityKey: 'identity-a', paletteDialog: 'launch' })
    render(<PaletteDialogs />)
    await waitFor(() => expect(launchButton().disabled).toBe(false))
    setTask('fix the cart')
    vi.mocked(api.launchDefaultsGet).mockResolvedValue({ revision: 1, defaults: { agent: 'codex', agents: { codex: { mode: 'tui' } } } })
    act(() => useStore.setState({ identityKey: 'identity-b' }))
    await waitFor(() => expect(agentRow(/^Codex/).getAttribute('aria-checked')).toBe('true'))
    expect((screen.getByLabelText('Task') as HTMLTextAreaElement).value).toBe('')
  })

  it('applies the launch to the choices another device saved meanwhile', async () => {
    vi.mocked(api.launchDefaultsGet).mockResolvedValue({ revision: 3, defaults: { agent: 'claude' } })
    vi.mocked(api.launchDefaultsSet).mockRejectedValueOnce(Object.assign(new Error('launch.defaults.set: revision 3 is not current'), {
      code: -32003,
      data: { revision: 4, defaults: { agent: 'codex', account_member_id: bob.id } },
    }))
    await open()
    fireEvent.click(launchButton())
    await waitFor(() => expect(api.launchDefaultsSet).toHaveBeenCalledTimes(2))
    expect(vi.mocked(api.launchDefaultsSet).mock.calls[1][0]).toEqual({
      defaults: { agent: 'claude', agents: { claude: { mode: 'acp' } }, account_member_id: bob.id },
      revision: 4,
    })
  })

  it('preselects the saved account and fallback, and saves them only when asked', async () => {
    vi.mocked(api.accountList).mockResolvedValue({ accounts: [alice, bob, vera], shared_with: [] })
    vi.mocked(api.launchDefaultsGet).mockResolvedValue({ revision: 2, defaults: { agent: 'claude', account_member_id: bob.id, fallback: { policy: 'off' } } })
    await open()
    await waitFor(() => expect(api.agentList).toHaveBeenCalledWith(bob.id))
    await waitFor(() => expect(launchButton().disabled).toBe(false))
    fireEvent.click(launchButton())
    await waitFor(() => expect(api.runLaunch).toHaveBeenCalledWith({ workspace_id: workspace.id, harness: 'claude', mode: 'acp', account_member_id: bob.id, no_fallback: true }))
    await waitFor(() => expect(api.launchDefaultsSet).toHaveBeenCalledTimes(1))
    const saved = vi.mocked(api.launchDefaultsSet).mock.calls[0][0].defaults
    expect(saved?.account_member_id).toBe(bob.id)
    expect(saved?.fallback).toEqual({ policy: 'off' })
  })

  it('saves the account and fallback with Save as my default', async () => {
    vi.mocked(api.accountList).mockResolvedValue({ accounts: [alice, bob], shared_with: [] })
    await open()
    fireEvent.click(screen.getByRole('button', { name: /^Options/ }))
    await pickOption(screen.getByRole('combobox', { name: 'Account' }), `${bob.display_name} (shared)`)
    fireEvent.click(screen.getByRole('checkbox', { name: 'Fall back to another account' }))
    fireEvent.click(screen.getByRole('button', { name: 'Save as my default' }))
    await waitFor(() => expect(api.launchDefaultsSet).toHaveBeenCalledWith({ defaults: { account_member_id: bob.id, fallback: { policy: 'off' } }, revision: 0 }))
    expect(await screen.findByText('Saved for every device.')).toBeDefined()
    fireEvent.click(screen.getByRole('checkbox', { name: 'Fall back to another account' }))
    expect(screen.queryByText('Saved for every device.')).toBeNull()
  })

  it('names a saved account that is no longer shared, with the reason, and launches on your own', async () => {
    const reason = `permission denied: member ${bob.id} has not shared their account with you`
    vi.mocked(api.accountList).mockResolvedValue({ accounts: [alice, vera], shared_with: [] })
    vi.mocked(api.launchDefaultsGet).mockResolvedValue({
      revision: 2,
      defaults: { agent: 'claude', account_member_id: bob.id },
      invalid: [{ field: 'account_member_id', value: bob.id, reason }],
    })
    await open()
    expect(await screen.findByText('Saved defaults not used')).toBeDefined()
    expect(screen.getByText((_, node) => node?.tagName === 'LI' && node.textContent === `Account ${bob.id}: ${reason}`)).toBeDefined()
    fireEvent.click(launchButton())
    await waitFor(() => expect(api.runLaunch).toHaveBeenCalledWith({ workspace_id: workspace.id, harness: 'claude', mode: 'acp' }))
  })
})

describe('saved launch defaults across devices', () => {
  beforeEach(() => {
    useStore.setState({ capabilities: { gateway: 'remote', methods: ['*'], ws: [] }, launchDefaults: {} })
  })

  it('reads the saved choices again on each open, so another device\'s save wins', async () => {
    vi.mocked(api.agentList).mockResolvedValue([claude(), agentInfo({ name: 'codex', display_name: 'Codex', login_found: true })])
    useLaunchDefaults.setState({ result: { revision: 1, defaults: { agent: 'codex' } }, error: null })
    vi.mocked(api.launchDefaultsGet).mockResolvedValue({ revision: 2, defaults: { agent: 'claude' } })
    await open()
    expect(agentRow(/^Claude Code/).getAttribute('aria-checked')).toBe('true')
  })

  it('switches to the saved account before picking the saved agent there', async () => {
    vi.mocked(api.accountList).mockResolvedValue({ accounts: [alice, bob], shared_with: [] })
    vi.mocked(api.agentList).mockImplementation(async (account?: string) => account === bob.id
      ? [claude(), agentInfo({ name: 'codex', display_name: 'Codex', login_found: true })]
      : [claude()])
    vi.mocked(api.launchDefaultsGet).mockResolvedValue({ revision: 1, defaults: { agent: 'codex', account_member_id: bob.id } })
    render(<LaunchDialog />)
    await waitFor(() => expect(agentRow(/^Codex/).getAttribute('aria-checked')).toBe('true'))
  })

  it('saves the Run tab\'s fallback without the account it runs on', async () => {
    vi.mocked(api.accountList).mockResolvedValue({ accounts: [alice, bob, vera], shared_with: [] })
    await open()
    fireEvent.click(screen.getByRole('button', { name: /^Options/ }))
    fireEvent.click(screen.getByRole('checkbox', { name: `${vera.display_name} (shared)` }))
    fireEvent.click(screen.getByRole('button', { name: 'Save as my default' }))
    await waitFor(() => expect(api.launchDefaultsSet).toHaveBeenCalledWith({ defaults: { account_member_id: '', fallback: { policy: 'ordered', order: [bob.id] } }, revision: 0 }))
  })
})
