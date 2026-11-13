import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { toast } from 'sonner'
import type { LaunchDraft } from '@/components/launch/draft'
import { useIsMobile } from '@/lib/breakpoints'
import { LaunchDialog } from '@/components/launch/launch-dialog'
import { PaletteDialogs } from '@/components/palette/dialogs'
import { api } from '@/lib/api'
import { useStore } from '@/store'
import { useChildSettings } from '@/store/children'
import { useLaunchDefaults } from '@/store/launch-defaults'
import { optimisticDeadlineMs } from '@/store/optimistic'
import { atViewport } from '@/test/viewport'
import { agentInfo, alice, bob, otherWorkspace, run, runRecords, serverInfo, vera, workspace } from '@/test/fixtures'
import { chooseModel, pickModel } from '@/test/model'
import { pickOption } from '@/test/select'

vi.mock('@/lib/api', async () => {
  const { fakeApi } = await import('@/test/fixtures')
  return { api: fakeApi(), API_BASE: '/api/v1', ApiError: Error }
})

const claude = (over: Parameters<typeof agentInfo>[0] = {}) =>
  agentInfo({ display_name: 'Claude Code', enhanced: 'adapter', enhanced_installed: true, login_found: true, default_mode: 'tui', ...over })

beforeEach(() => {
  localStorage.clear()
  Element.prototype.scrollIntoView = vi.fn()
  vi.mocked(api.runLaunch).mockResolvedValue(run())
  vi.mocked(api.agentList).mockResolvedValue([claude()])
  vi.mocked(api.accountList).mockResolvedValue({ accounts: [alice], shared_with: [] })
  vi.mocked(api.accountUsage).mockResolvedValue({ account_member_id: alice.id, providers: [] })
  vi.mocked(api.launchDefaultsGet).mockResolvedValue({ revision: 0, defaults: {} })
  useLaunchDefaults.setState({ result: null, error: null })
  useStore.setState({
    workspaces: { [workspace.id]: workspace },
    activeWorkspace: workspace.id,
    paletteDialog: 'launch',
    launchDraft: null,
    launchDraftError: null,
    linkStatus: null,
    identityKey: null,
    paletteRunID: null,
    route: { name: 'board', params: {} },
    runs: {},
    launchDefaults: {},
    savedLaunchDefaults: {},
    agentModels: {},
    capabilities: null,
    info: serverInfo,
  })
  vi.clearAllMocks()
})

const signedOut = '~/.claude/.credentials.json: Claude OAuth access token is missing or invalid'
const launchButton = () => screen.getByRole('button', { name: 'Launch' }) as HTMLButtonElement
const agentRow = (name: RegExp | string) => screen.getByRole('radio', { name }) as HTMLButtonElement
const modeSegment = (name: string) => within(screen.getByRole('radiogroup', { name: 'Mode' })).getByRole('radio', { name }) as HTMLButtonElement
const allowDraftStorage = () => useStore.getState().setCapabilities({ gateway: 'remote', methods: ['run.launch', 'agent.list', 'account.list'], ws: [] })

async function open() {
  render(<LaunchDialog />)
  await waitFor(() => expect(launchButton().disabled).toBe(false))
}

function setTask(task: string) {
  fireEvent.change(screen.getByLabelText('Task'), { target: { value: task } })
}

const draft = (over: Partial<LaunchDraft> = {}): LaunchDraft => ({
  version: 1, id: 'saved-draft', workspace: workspace.id, extras: [], account: alice.id,
  fallback: { off: false, order: null, excluded: [] }, permission: 'default', childRuns: '',
  limits: { cpus: '', memory: '', pids: '' }, harness: 'claude', mode: 'tui', model: '', effort: '', task: 'keep this prompt',
  ...over,
})

describe('reopening', () => {
  it('starts a fresh form when New run opens again while the last panel is still leaving', async () => {
    render(<PaletteDialogs />)
    fireEvent.change(await screen.findByLabelText('Task'), { target: { value: 'first task' } })
    fireEvent.click(screen.getByRole('button', { name: 'Close' }))
    act(() => useStore.getState().openPaletteDialog('launch'))
    expect(((await screen.findAllByLabelText('Task')).at(-1) as HTMLTextAreaElement).value).toBe('')
    expect(screen.getAllByRole('dialog')).toHaveLength(1)
  })
})

describe('saved launch draft', () => {
  const resume = () => screen.getByRole('button', { name: 'Resume new session draft' })

  it('collapses on an outside click, keeps that click and restores the task and mode', async () => {
    const mainClick = vi.fn()
    render(<><button onClick={mainClick}>Main chat</button><PaletteDialogs /></>)
    await waitFor(() => expect(launchButton().disabled).toBe(false))
    setTask('keep my unfinished prompt')
    fireEvent.click(modeSegment('Standard'))
    await userEvent.click(screen.getByRole('button', { name: 'Main chat' }))
    await waitFor(() => expect(screen.queryByLabelText('Task')).toBeNull())
    expect(mainClick).toHaveBeenCalledOnce()
    await userEvent.click(resume())
    expect((await screen.findByLabelText('Task') as HTMLTextAreaElement).value).toBe('keep my unfinished prompt')
    expect(modeSegment('Standard').getAttribute('aria-checked')).toBe('true')
  })

  it('keeps the draft on Escape and restores it through New session', async () => {
    render(<PaletteDialogs />)
    await waitFor(() => expect(launchButton().disabled).toBe(false))
    setTask('Escape preserves this')
    await userEvent.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByLabelText('Task')).toBeNull())
    expect(resume()).toBeTruthy()
    act(() => useStore.getState().openPaletteDialog('launch'))
    expect((await screen.findByLabelText('Task') as HTMLTextAreaElement).value).toBe('Escape preserves this')
  })

  it('recovers the task, mode and model collapsed after a reload without applying saved defaults over them', async () => {
    allowDraftStorage()
    useStore.getState().setIdentityKey('server/alice')
    const view = render(<PaletteDialogs />)
    await waitFor(() => expect(launchButton().disabled).toBe(false))
    setTask('recover after browser restart')
    fireEvent.click(modeSegment('Standard'))
    await chooseModel(screen.getByRole('combobox', { name: 'Model' }), 'draft-model')
    view.unmount()
    useStore.setState({ identityKey: null, launchDraft: null, paletteDialog: null })
    vi.mocked(api.launchDefaultsGet).mockResolvedValue({ revision: 2, defaults: { agents: { claude: { mode: 'headless', model: 'other-model' } } } })
    useLaunchDefaults.setState({ result: null, error: null })
    useStore.getState().setIdentityKey('server/alice')
    render(<PaletteDialogs />)
    expect(screen.queryByLabelText('Task')).toBeNull()
    await userEvent.click(resume())
    expect((await screen.findByLabelText('Task') as HTMLTextAreaElement).value).toBe('recover after browser restart')
    await waitFor(() => expect(launchButton().disabled).toBe(false))
    expect(modeSegment('Standard').getAttribute('aria-checked')).toBe('true')
    fireEvent.click(launchButton())
    await waitFor(() => expect(api.runLaunch).toHaveBeenCalledWith(expect.objectContaining({ task: 'recover after browser restart', mode: 'tui', model: 'draft-model' })))
    expect(useStore.getState().launchDraft).toBeNull()
  })

  it.each(['Close', 'Cancel'])('discards the recovery copy with %s', async (name) => {
    allowDraftStorage()
    useStore.getState().setIdentityKey('server/alice')
    render(<PaletteDialogs />)
    await waitFor(() => expect(launchButton().disabled).toBe(false))
    setTask('discard me')
    await userEvent.click(screen.getByRole('button', { name }))
    await waitFor(() => expect(screen.queryByLabelText('Task')).toBeNull())
    expect(screen.queryByRole('button', { name: 'Resume new session draft' })).toBeNull()
    act(() => {
      useStore.getState().setIdentityKey('server/bob')
      useStore.getState().setIdentityKey('server/alice')
      useStore.getState().openPaletteDialog('launch')
    })
    expect((await screen.findByLabelText('Task') as HTMLTextAreaElement).value).toBe('')
  })

  it('preserves a pending launch through collapse and clears the draft when it succeeds', async () => {
    let finish!: (value: ReturnType<typeof run>) => void
    vi.mocked(api.runLaunch).mockImplementation(() => new Promise((resolve) => { finish = resolve }))
    render(<PaletteDialogs />)
    await waitFor(() => expect(launchButton().disabled).toBe(false))
    setTask('launch once')
    fireEvent.click(launchButton())
    await userEvent.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByLabelText('Task')).toBeNull())
    await userEvent.click(resume())
    expect((screen.getByRole('button', { name: 'Launching…' }) as HTMLButtonElement).disabled).toBe(true)
    expect(api.runLaunch).toHaveBeenCalledOnce()
    await act(async () => finish(run()))
    await waitFor(() => expect(useStore.getState().launchDraft).toBeNull())
  })

  it('keeps the task and refusal when a collapsed launch fails', async () => {
    let refuse!: (reason: Error) => void
    vi.mocked(api.runLaunch).mockImplementation(() => new Promise((_, reject) => { refuse = reject }))
    render(<PaletteDialogs />)
    await waitFor(() => expect(launchButton().disabled).toBe(false))
    setTask('retry this task')
    fireEvent.click(launchButton())
    await userEvent.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByLabelText('Task')).toBeNull())
    await act(async () => refuse(new Error('server refused this launch')))
    await userEvent.click(resume())
    expect((await screen.findByLabelText('Task') as HTMLTextAreaElement).value).toBe('retry this task')
    expect(screen.getByText('server refused this launch')).toBeTruthy()
    expect(launchButton().disabled).toBe(false)
  })

  it('keeps the draft repository when the sidebar changes workspace after collapse', async () => {
    useStore.setState({ workspaces: { [workspace.id]: workspace, [otherWorkspace.id]: otherWorkspace } })
    render(<PaletteDialogs />)
    await waitFor(() => expect(launchButton().disabled).toBe(false))
    setTask('keep this repository')
    await userEvent.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByLabelText('Task')).toBeNull())
    act(() => useStore.setState({ activeWorkspace: otherWorkspace.id }))
    await userEvent.click(resume())
    await waitFor(() => expect(launchButton().disabled).toBe(false))
    fireEvent.click(launchButton())
    await waitFor(() => expect(api.runLaunch).toHaveBeenCalledWith(expect.objectContaining({ workspace_id: workspace.id, task: 'keep this repository' })))
  })

  it('isolates recovery by server and member', async () => {
    allowDraftStorage()
    useStore.getState().setIdentityKey('server/alice')
    render(<PaletteDialogs />)
    await waitFor(() => expect(launchButton().disabled).toBe(false))
    setTask('Alice private draft')
    act(() => useStore.getState().setIdentityKey('other-server/alice'))
    expect((await screen.findByLabelText('Task') as HTMLTextAreaElement).value).toBe('')
    setTask('other server draft')
    act(() => useStore.getState().setIdentityKey('server/bob'))
    expect((await screen.findByLabelText('Task') as HTMLTextAreaElement).value).toBe('')
    act(() => useStore.getState().setIdentityKey('server/alice'))
    expect((await screen.findByLabelText('Task') as HTMLTextAreaElement).value).toBe('Alice private draft')
  })

  it('keeps an Edge-linked draft in memory and removes its browser copy', () => {
    const saved = draft()
    allowDraftStorage()
    useStore.setState({ identityKey: 'server/alice', launchDraft: saved })
    useStore.getState().setLaunchDraft(saved)
    expect(localStorage.getItem('aether.launch-draft.v1:server%2Falice')).not.toBeNull()

    useStore.getState().setLinkStatus({ edge_url: 'http://127.0.0.1:4173' } as never)

    expect(useStore.getState().launchDraft).toEqual(saved)
    expect(localStorage.getItem('aether.launch-draft.v1:server%2Falice')).toBeNull()
    useStore.getState().setLaunchDraft({ ...saved, task: 'still in memory' })
    expect(localStorage.getItem('aether.launch-draft.v1:server%2Falice')).toBeNull()
    useStore.setState({ identityKey: null, launchDraft: null })
    useStore.getState().setIdentityKey('server/alice')
    expect(useStore.getState().launchDraft).toBeNull()
  })

  it('waits for the local link status before reading a saved draft', () => {
    const saved = draft()
    const key = 'aether.launch-draft.v1:server%2Falice'
    localStorage.setItem(key, JSON.stringify(saved))
    useStore.setState({
      paletteDialog: null,
      capabilities: { gateway: 'local', methods: [], ws: [], local: ['link.status'] },
    })
    useStore.getState().setIdentityKey('server/alice')
    expect(useStore.getState().launchDraft).toBeNull()
    expect(useStore.getState().launchDraftError).toContain('Could not confirm')

    useStore.getState().setLinkStatus({ linked: false } as never)

    expect(useStore.getState().launchDraft).toEqual(saved)
    expect(useStore.getState().launchDraftError).toBeNull()
  })

  it('never reads an Edge-linked draft from browser storage', () => {
    const saved = draft()
    const key = 'aether.launch-draft.v1:server%2Falice'
    localStorage.setItem(key, JSON.stringify(saved))
    useStore.setState({ capabilities: { gateway: 'local', methods: [], ws: [], local: ['link.status'] } })
    useStore.getState().setIdentityKey('server/alice')
    expect(useStore.getState().launchDraft).toBeNull()
    expect(localStorage.getItem(key)).not.toBeNull()

    useStore.getState().setLinkStatus({ edge_url: 'http://127.0.0.1:4173' } as never)

    expect(useStore.getState().launchDraft).toBeNull()
    expect(localStorage.getItem(key)).toBeNull()
  })

  it('reports invalid saved draft data and waits for an explicit fresh draft', async () => {
    const key = 'aether.launch-draft.v1:server%2Falice'
    localStorage.setItem(key, '{')
    allowDraftStorage()
    useStore.getState().setIdentityKey('server/alice')
    expect(useStore.getState().launchDraftError).toContain('Could not read saved launch draft')
    render(<LaunchDialog />)
    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toContain('Could not read saved launch draft')
    expect(localStorage.getItem(key)).toBe('{')

    await userEvent.click(within(alert).getByRole('button', { name: 'Start a new draft' }))
    await waitFor(() => expect(useStore.getState().launchDraftError).toBeNull())
    await waitFor(() => expect(JSON.parse(localStorage.getItem(key) ?? 'null').version).toBe(1))
  })

  it('shows browser storage read errors without overwriting the saved draft', async () => {
    const key = 'aether.launch-draft.v1:server%2Falice'
    localStorage.setItem(key, JSON.stringify(draft()))
    const getItem = vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('storage blocked') })
    allowDraftStorage()
    useStore.getState().setIdentityKey('server/alice')
    getItem.mockRestore()
    render(<LaunchDialog />)
    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toContain('storage blocked')
    expect(JSON.parse(localStorage.getItem(key) ?? 'null').task).toBe('keep this prompt')
  })

  it('falls back to the active workspace when the saved workspace was deleted', async () => {
    useStore.setState({
      activeWorkspace: otherWorkspace.id,
      workspaces: { [otherWorkspace.id]: otherWorkspace },
      launchDraft: draft({ workspace: 'deleted-workspace' }),
    })
    await open()
    fireEvent.click(launchButton())
    await waitFor(() => expect(api.runLaunch).toHaveBeenCalledWith(expect.objectContaining({ workspace_id: otherWorkspace.id, task: 'keep this prompt' })))
  })

  it('replaces a saved account that is no longer accessible while keeping the prompt', async () => {
    useStore.setState({ launchDraft: draft({ account: bob.id }) })
    await open()
    expect((screen.getByLabelText('Task') as HTMLTextAreaElement).value).toBe('keep this prompt')
    fireEvent.click(launchButton())
    await waitFor(() => expect(api.runLaunch).toHaveBeenCalledWith(expect.objectContaining({ workspace_id: workspace.id, task: 'keep this prompt' })))
    expect(vi.mocked(api.runLaunch).mock.calls[0]?.[0]).not.toHaveProperty('account_member_id')
  })

  it('moves the resume arrow to the replacement sidebar after resizing back from phone', async () => {
    const resize = atViewport(1024)
    function Shell() {
      const phone = useIsMobile()
      return <>{!phone && <aside id="sidebar" />}<PaletteDialogs /></>
    }
    useStore.setState({ paletteDialog: null, launchDraft: draft() })
    const view = render(<Shell />)
    const original = document.getElementById('sidebar')
    expect(screen.getByRole('button', { name: 'Resume new session draft' }).parentElement).toBe(original)
    resize(767)
    resize(1024)
    await waitFor(() => expect(screen.getByRole('button', { name: 'Resume new session draft' }).parentElement).toBe(document.getElementById('sidebar')))
    expect(document.getElementById('sidebar')).not.toBe(original)
    view.unmount()
  })
})

describe('child runs switch', () => {
  const childSwitch = () => screen.getByRole('switch', { name: 'Subsessions' })
  beforeEach(() => {
    useStore.setState({ capabilities: { gateway: 'remote', methods: ['*'], ws: [] } })
    vi.mocked(api.childSettings).mockResolvedValue({ enabled: true, fallback: 'ask', shared_when_missing: false, configured: true, nudge: 'show' } as never)
  })

  it('follows the member setting and sends nothing until flipped', async () => {
    await open()
    await waitFor(() => expect(childSwitch().getAttribute('aria-checked')).toBe('true'))
    fireEvent.click(launchButton())
    await waitFor(() => expect(api.runLaunch).toHaveBeenCalled())
    expect(vi.mocked(api.runLaunch).mock.calls[0]![0]).not.toHaveProperty('children')
  })

  it('sends off for this run only, and not the saved defaults', async () => {
    await open()
    await waitFor(() => expect(childSwitch().getAttribute('aria-checked')).toBe('true'))
    fireEvent.click(childSwitch())
    expect(childSwitch().getAttribute('aria-checked')).toBe('false')
    fireEvent.click(launchButton())
    await waitFor(() => expect(api.runLaunch).toHaveBeenCalled())
    expect(vi.mocked(api.runLaunch).mock.calls[0]![0]).toMatchObject({ children: 'off' })
    for (const [params] of vi.mocked(api.launchDefaultsSet).mock.calls) expect(JSON.stringify(params)).not.toContain('children')
  })

  it('offers the subsession agents while subsessions are on for the launch', async () => {
    useChildSettings.setState({ settings: null, error: null })
    vi.mocked(api.childSettings).mockResolvedValue({ enabled: true, off: [{ harness: 'codex' }], choices: [
      { account_member_id: alice.id, account_name: 'Alice', harness: 'claude', display_name: 'Claude Code', status: 'ready' },
      { account_member_id: alice.id, account_name: 'Alice', harness: 'codex', display_name: 'Codex', status: 'ready' },
    ] })
    await open()
    expect(await screen.findByRole('button', { name: 'Subsession agents: 1 off' })).toBeDefined()
    fireEvent.click(childSwitch())
    expect(screen.queryByRole('button', { name: /^Subsession agents/ })).toBeNull()
  })

  it('is off and disabled for a Background run', async () => {
    await open()
    await waitFor(() => expect(childSwitch().getAttribute('aria-checked')).toBe('true'))
    fireEvent.click(modeSegment('Background'))
    expect(childSwitch().getAttribute('aria-checked')).toBe('false')
    expect((childSwitch() as HTMLButtonElement).disabled).toBe(true)
  })
})

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

  it('keeps Launch off with a reason while its workspace no longer exists, and keeps the task', async () => {
    await open()
    setTask('keep me')
    act(() => useStore.setState({ workspaces: {} }))
    expect(screen.getByRole('alert').textContent).toContain('This workspace no longer exists.')
    expect(launchButton().disabled).toBe(true)
    expect((screen.getByLabelText('Task') as HTMLTextAreaElement).value).toBe('keep me')
  })

  it('lists only installed agents with their login state, and sends Install more agents to Agents', async () => {
    vi.mocked(api.agentList).mockResolvedValue([
      claude(),
      agentInfo({ name: 'codex', display_name: 'Codex', login_found: false }),
      agentInfo({ name: 'pi', display_name: 'Pi', installed: false }),
    ])
    await open()

    expect(agentRow(/^Claude Code/).textContent).toBe('Claude CodeReady')
    expect(agentRow(/^Codex/).textContent).toBe('CodexSign in after launch')
    expect(screen.queryByRole('radio', { name: /^Pi/ })).toBeNull()
    expect(agentRow(/^custom/).disabled).toBe(false)

    fireEvent.click(screen.getByRole('button', { name: 'Install more agents...' }))
    expect(useStore.getState().route.name).toBe('agents')
    expect(useStore.getState().paletteDialog).toBeNull()
  })

  it('says a signed-out agent is signed out, shows the problem and sends the member to Agents, without refusing the launch', async () => {
    vi.mocked(api.agentList).mockResolvedValue([claude({ login_problem: signedOut, login_blocks_launch: true })])
    await open()

    expect(agentRow(/^Claude Code/).textContent).toBe('Claude CodeSigned out')
    expect(screen.queryByRole('radiogroup', { name: 'Account' })).toBeNull()
    const note = screen.getByText('Claude Code is signed out').closest('[data-slot="callout"]')!
    expect(note.textContent).toContain(signedOut)
    expect(note.textContent).toContain('Until then a launch here is refused, unless a workspace variable signs the agent in.')
    expect(launchButton().disabled).toBe(false)
    fireEvent.click(within(note as HTMLElement).getByRole('button', { name: 'Sign in on Agents' }))
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

  it('starts on the saved permission, saves a change at once, and launches with it', async () => {
    useStore.setState({ info: { ...serverInfo, features: ['permission'] }, capabilities: { gateway: 'remote', methods: ['*'], ws: [] } })
    vi.mocked(api.launchDefaultsGet).mockResolvedValue({ revision: 3, defaults: { permission: 'isolated' } })
    await open()
    const tier = (name: RegExp) => within(screen.getByRole('radiogroup', { name: 'Permission' })).getByRole('radio', { name })
    await waitFor(() => expect(tier(/^Isolated/).getAttribute('aria-checked')).toBe('true'))
    await userEvent.click(tier(/^Root access/))
    await waitFor(() => expect(api.launchDefaultsSet).toHaveBeenCalledWith({ defaults: { permission: 'root' }, revision: 3 }))
    fireEvent.click(launchButton())
    await waitFor(() => expect(api.runLaunch).toHaveBeenCalledWith({ workspace_id: workspace.id, harness: 'claude', mode: 'acp', permission: 'root' }))
  })

  it('offers no permission on a server without tiers, and sends none', async () => {
    await open()
    expect(screen.queryByRole('radiogroup', { name: 'Permission' })).toBeNull()
    fireEvent.click(launchButton())
    await waitFor(() => expect(api.runLaunch).toHaveBeenCalledWith({ workspace_id: workspace.id, harness: 'claude', mode: 'acp' }))
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

  it('gives an admin the adopt command and the way to the workspace settings page', async () => {
    const command = 'aether workspace mirror adopt --workspace ' + workspace.id + ' --generation 3 --commit ' + 'a'.repeat(40) + ' --yes'
    const text = 'gitengine: mirror no-candidate for branch "dev": revision ' + 'a'.repeat(40) + ' is waiting for an admin to adopt it; check that it is the upstream commit you expect, then run: ' + command
    vi.mocked(api.runLaunch).mockRejectedValueOnce(Object.assign(new Error(text), { data: { kind: 'no-candidate', adopt_command: command } }))
    await open()
    fireEvent.click(launchButton())

    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toContain(text.slice(0, -command.length - 1))
    expect(alert.textContent?.split(command)).toHaveLength(2)
    expect(within(alert).getByLabelText('Adopt command').textContent).toBe(command)
    fireEvent.click(within(alert).getByRole('button', { name: 'Copy the adopt command' }))
    fireEvent.click(within(alert).getByRole('button', { name: 'Open workspace settings' }))
    expect(api.runLaunch).toHaveBeenCalledTimes(1)
    expect(useStore.getState().route).toEqual({ name: 'workspace', params: { workspaceId: workspace.id } })
    expect(useStore.getState().paletteDialog).toBeNull()
  })

  it('tells a non-admin to ask an admin to adopt the source mirror', async () => {
    useStore.setState({ info: { ...serverInfo, member: { ...alice, role: 'collaborator' } } })
    vi.mocked(api.runLaunch).mockRejectedValueOnce(Object.assign(new Error('gitengine: mirror no-candidate'), { data: { kind: 'no-candidate', adopt_command: 'aether workspace mirror adopt' } }))
    await open()
    fireEvent.click(launchButton())

    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toContain('Ask an admin to adopt it with Review source mirror → Adopt candidate under Advanced on the workspace settings page.')
    expect(within(alert).getByLabelText('Adopt command').textContent).toBe('aether workspace mirror adopt')
    expect(within(alert).queryByRole('button', { name: 'Open workspace settings' })).toBeNull()
  })

  it('offers no adopt step when the pending mirror has no candidate yet', async () => {
    vi.mocked(api.runLaunch).mockRejectedValueOnce(Object.assign(new Error('gitengine: mirror no-candidate'), { data: { kind: 'no-candidate' } }))
    await open()
    fireEvent.click(launchButton())

    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toContain('gitengine: mirror no-candidate')
    expect(alert.textContent).not.toContain('Adopt candidate')
    expect(within(alert).queryByRole('button')).toBeNull()
  })

  it('needs a task for Background and says so', async () => {
    await open()
    await userEvent.click(modeSegment('Background'))
    expect(screen.getByText('Required. A Background session starts with this task and takes no input.')).toBeDefined()
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

    it('hides Account and Fallback without a shared account', async () => {
      vi.mocked(api.accountList).mockResolvedValue({ accounts: [alice], shared_with: [] })
      await open()
      expect(screen.queryByRole('radiogroup', { name: 'Account' })).toBeNull()
      expect(screen.queryByRole('button', { name: /^Fallback/ })).toBeNull()
    })

    it('lists each account with its access and the chosen agent\'s usage or readiness there', async () => {
      vi.mocked(api.accountList).mockResolvedValue({ accounts: [alice, bob, vera], shared_with: [] })
      vi.mocked(api.agentList).mockImplementation(async (account?: string) => account === vera.id ? [claude({ login_missing: true })] : [claude()])
      vi.mocked(api.accountUsage).mockImplementation(async (params = {}) => ({
        account_member_id: params.account_member_id ?? alice.id,
        providers: params.account_member_id === bob.id
          ? [{ provider: 'claude', status: 'ok', checked_at: '2026-11-06T00:00:00Z', windows: [{ id: '5h', label: '5 hours', used_percent: 12 }, { id: '7d', label: '7 days', used_percent: 96.4 }] }]
          : [],
      }))
      await open()
      const accounts = screen.getByRole('radiogroup', { name: 'Account' })
      await waitFor(() => expect(within(accounts).getAllByRole('radio').map((row) => row.textContent)).toEqual([
        'AliceYouReady',
        'BobShared96% used',
        "VeraSharedNeeds Vera's login",
      ]))
      expect(within(accounts).getByRole('radio', { name: /^Alice/ }).getAttribute('aria-checked')).toBe('true')
      expect(api.accountUsage).toHaveBeenCalledWith({ account_member_id: bob.id })
    })

    it('launches on the shared account chosen under Account', async () => {
      vi.mocked(api.agentList).mockImplementation(async (account?: string) =>
        account === bob.id ? [agentInfo({ name: 'bob-agent', source: 'member', install_script: undefined })] : [claude()])
      await open()
      await userEvent.click(within(screen.getByRole('radiogroup', { name: 'Account' })).getByRole('radio', { name: /^Bob/ }))
      await waitFor(() => expect(agentRow(/^bob-agent/).getAttribute('aria-checked')).toBe('true'))
      expect(screen.getByText("Uses Bob's bob-agent login and quota. Runs in your environment with your GitHub login; the session stays yours.")).toBeDefined()
      fireEvent.click(launchButton())

      await waitFor(() => expect(api.runLaunch).toHaveBeenCalledWith({ workspace_id: workspace.id, harness: 'bob-agent', mode: 'tui', account_member_id: bob.id }))
      expect(api.agentList).toHaveBeenCalledWith(bob.id)
    })

    it('sends the ticked fallback accounts in the order shown once one is moved or unticked', async () => {
      vi.mocked(api.accountList).mockResolvedValue({ accounts: [alice, bob, vera, { ...alice, id: 'mem_carol', display_name: 'Carol' }], shared_with: [] })
      await open()
      await userEvent.click(screen.getByRole('button', { name: /^Fallback/ }))
      const fallbacks = screen.getByRole('list', { name: 'Fallback accounts' })
      await waitFor(() => expect(within(fallbacks).getAllByRole('checkbox').map((box) => box.closest('label')?.textContent)).toEqual(['BobSharedReady', 'VeraSharedReady', 'CarolSharedReady']))
      expect(screen.getByRole('button', { name: /^Fallback/ }).textContent).toBe('Fallback· Automatic · ifAliceruns out')
      expect(screen.getByText("If Alice's usage is 95% or more at launch, or the vendor reports the limit mid-session, the session moves to the ticked account with the most usage left.")).toBeDefined()

      await userEvent.click(screen.getByRole('button', { name: 'Move Carol up' }))
      await userEvent.click(screen.getByRole('checkbox', { name: /^Bob/ }))
      expect(screen.getByText(/the session moves to the first ticked account, top to bottom, with usage left\.$/)).toBeDefined()
      fireEvent.click(launchButton())

      await waitFor(() => expect(api.runLaunch).toHaveBeenCalledWith({ workspace_id: workspace.id, harness: 'claude', mode: 'acp', fallback_account_member_ids: ['mem_carol', vera.id] }))
    })

    it('keeps a fallback account ticked while it is the launch account and the list is edited', async () => {
      vi.mocked(api.accountList).mockResolvedValue({ accounts: [alice, bob, vera], shared_with: [] })
      vi.mocked(api.launchDefaultsGet).mockResolvedValue({ revision: 1, defaults: { fallback: { policy: 'ordered', order: [bob.id, vera.id] } } })
      useStore.setState({ capabilities: { gateway: 'remote', methods: ['*'], ws: [] } })
      await open()
      const accounts = () => within(screen.getByRole('radiogroup', { name: 'Account' }))
      await userEvent.click(screen.getByRole('button', { name: /^Fallback/ }))
      await userEvent.click(accounts().getByRole('radio', { name: /^Bob/ }))
      await userEvent.click(screen.getByRole('checkbox', { name: /^Vera/ }))
      await userEvent.click(accounts().getByRole('radio', { name: /^Alice/ }))
      await waitFor(() => expect(launchButton().disabled).toBe(false))
      fireEvent.click(launchButton())
      await waitFor(() => expect(api.runLaunch).toHaveBeenCalledWith(expect.objectContaining({ fallback_account_member_ids: [bob.id] })))
      expect(vi.mocked(api.runLaunch).mock.calls[0]![0]).not.toHaveProperty('account_member_id')
    })

    it('keeps the account a launch moved off ticked, last, in a list reordered from automatic', async () => {
      vi.mocked(api.accountList).mockResolvedValue({ accounts: [alice, bob, vera], shared_with: [] })
      await open()
      await userEvent.click(screen.getByRole('button', { name: /^Fallback/ }))
      await userEvent.click(screen.getByRole('button', { name: 'Move Vera up' }))
      await userEvent.click(within(screen.getByRole('radiogroup', { name: 'Account' })).getByRole('radio', { name: /^Bob/ }))
      const fallbacks = screen.getByRole('list', { name: 'Fallback accounts' })
      const rows = () => within(fallbacks).getAllByRole('checkbox').map((box) => [box.closest('li')?.querySelector('.truncate')?.textContent, box.getAttribute('aria-checked')])
      await waitFor(() => expect(rows()).toEqual([['Vera', 'true'], ['Alice', 'true']]))
      await waitFor(() => expect(launchButton().disabled).toBe(false))
      fireEvent.click(launchButton())
      await waitFor(() => expect(api.runLaunch).toHaveBeenCalledWith(expect.objectContaining({ account_member_id: bob.id, fallback_account_member_ids: [vera.id, alice.id] })))
    })

    it("says the shared account's login is signed out and that its owner signs in again", async () => {
      vi.mocked(api.agentList).mockImplementation(async (account?: string) => account === bob.id ? [claude({ login_problem: signedOut, login_blocks_launch: true })] : [claude()])
      await open()
      const accountRow = () => within(screen.getByRole('radiogroup', { name: 'Account' })).getByRole('radio', { name: /^Bob/ })
      await waitFor(() => expect(accountRow().textContent).toBe('BobSharedSigned out'))
      await userEvent.click(accountRow())

      const note = (await screen.findByText("Bob's Claude Code login is signed out")).closest('[data-slot="callout"]') as HTMLElement
      expect(note.textContent).toContain(signedOut)
      expect(note.textContent).toContain('Bob signs in again on their Agents page or with aether agent login claude.')
      expect(within(note).queryByRole('button')).toBeNull()
      expect(agentRow(/^Claude Code/).textContent).toBe('Claude CodeSigned out')
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
      await userEvent.click(within(screen.getByRole('radiogroup', { name: 'Account' })).getByRole('radio', { name: /^Bob/ }))

      const notes = await screen.findByRole('status')
      expect(notes.textContent).toBe(
        'Bob is not logged in to Claude Code, so it cannot launch on this account. Bob logs in from their own Environment terminal; then open this dialog again.'
          + 'Your own agent definitions run only on your own account: myagent. To launch one, choose your own account under Account.'
          + `codex cannot launch on this account: ${refusal}`,
      )
      expect(agentRow(/^Claude Code/).textContent).toBe("Claude CodeNeeds Bob's login")
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
    act(() => useStore.getState().setIdentityKey('identity-b'))
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
    fireEvent.click(within(screen.getByRole('radiogroup', { name: 'Account' })).getByRole('radio', { name: new RegExp(`^${bob.display_name}`) }))
    fireEvent.click(screen.getByRole('button', { name: /^Fallback/ }))
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
    fireEvent.click(screen.getByRole('button', { name: /^Fallback/ }))
    fireEvent.click(screen.getByRole('checkbox', { name: new RegExp(`^${vera.display_name}`) }))
    fireEvent.click(screen.getByRole('button', { name: 'Save as my default' }))
    await waitFor(() => expect(api.launchDefaultsSet).toHaveBeenCalledWith({ defaults: { account_member_id: '', fallback: { policy: 'ordered', order: [bob.id] } }, revision: 0 }))
  })
})
