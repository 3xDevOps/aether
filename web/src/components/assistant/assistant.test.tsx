import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { Step } from '@/routes/onboarding/layout'
import { AppShell } from '@/components/shell/app-shell'
import type * as apiModule from '@/lib/api'
import { api } from '@/lib/api'
import type { AssistantConfirmation, AssistantSettings, AssistantStatus } from '@/lib/assistant-types'
import { stopTimeout } from '@/components/assistant/composer'
import { hearBack } from '@/components/assistant/launch-summary'
import { useStore, createRootStore } from '@/store'
import { loadWholeItem } from '@/store/session-stream'
import { viewSyncDelay } from '@/store/view-context'
import { item, resetItems, ScriptedSession, say, state, tool } from '@/test/acp-stream'
import { alice, bob, serverInfo, workspace } from '@/test/fixtures'
import { StubSocket } from '@/test/stub-socket'
import { atViewport } from '@/test/viewport'

vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof apiModule>()
  const { fakeApi } = await import('@/test/fixtures')
  return { ...actual, api: fakeApi() }
})

Element.prototype.scrollIntoView = vi.fn()

class SizedObserver {
  constructor(private readonly callback: ResizeObserverCallback) {}
  observe(target: Element) {
    const height = (target as HTMLElement).style.overflowY ? 4000 : 24
    const box = [{ blockSize: height, inlineSize: 400 }]
    this.callback([{ target, contentRect: { height, width: 400 }, borderBoxSize: box, contentBoxSize: box } as unknown as ResizeObserverEntry], this as unknown as ResizeObserver)
  }
  unobserve() {}
  disconnect() {}
}

const offsetParent = Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'offsetParent')!
beforeAll(() => {
  Object.defineProperty(HTMLElement.prototype, 'offsetParent', { configurable: true, get() { return this.parentElement } })
})
afterAll(() => {
  Object.defineProperty(HTMLElement.prototype, 'offsetParent', offsetParent)
})

const ready = [{ name: 'claude', display_name: 'Claude Code', ready: true }]

function status(over: Partial<AssistantStatus> = {}): AssistantStatus {
  return { state: 'idle', agent: 'claude', agents: ready, confirmations: [], auto_approve: false, suggest_auto_approve: false, share_view: true, ...over }
}

const stop: AssistantConfirmation = {
  id: 'c1',
  method: 'run.kill',
  summary: 'Stop run run-8x2 ("Fix flaky login test")',
  params: { run_id: 'run-8x2' },
  created_at: '2026-10-14T10:00:00Z',
  expires_at: '2099-10-14T10:10:00Z',
}

beforeEach(() => {
  vi.stubGlobal('ResizeObserver', SizedObserver)
  StubSocket.install()
  resetItems()
  localStorage.clear()
  useStore.setState({
    ...useStore.getInitialState(),
    info: serverInfo,
    members: { [alice.id]: alice },
    workspaces: { [workspace.id]: workspace },
    hydrated: true,
    route: { name: 'board', params: {} },
    capabilities: { gateway: 'remote', methods: ['*'], ws: ['events', 'attach'] },
    onboarded: true,
  }, true)
  vi.mocked(api.assistantStatus).mockResolvedValue(status())
})

afterEach(async () => {
  cleanup()
  await Promise.resolve()
  vi.clearAllMocks()
  vi.unstubAllGlobals()
})

const panel = () => screen.findByRole('complementary', { name: 'Assistant' })

function assistantSocket(): ScriptedSession {
  const socket = StubSocket.opened.filter((s) => s.url.includes('/ws/assistant')).at(-1)
  if (!socket) throw new Error('no /ws/assistant socket opened')
  return new ScriptedSession(socket)
}

async function openPanel() {
  render(<AppShell />)
  await userEvent.click(screen.getByRole('button', { name: 'Assistant' }))
  return panel()
}

describe('opening the assistant', () => {
  it('opens from the New run row and closes from its header, returning focus to the button', async () => {
    render(<AppShell />)
    const button = screen.getByRole('button', { name: 'Assistant' })
    expect(button.getAttribute('aria-expanded')).toBe('false')
    expect(screen.queryByRole('complementary', { name: 'Assistant' })).toBeNull()

    await userEvent.click(button)
    const aside = await panel()
    expect(button.getAttribute('aria-expanded')).toBe('true')
    expect(aside.nextElementSibling).toBeNull()
    expect(aside.previousElementSibling?.id).toBe('main')

    await userEvent.click(within(aside).getByRole('button', { name: 'Close assistant' }))
    expect(screen.queryByRole('complementary', { name: 'Assistant' })).toBeNull()
    await waitFor(() => expect(document.activeElement).toBe(button))
  })

  it('puts the button beside New run in the sidebar, and in the top bar only when the sidebar is hidden', async () => {
    render(<AppShell />)
    const sidebar = within(screen.getByRole('navigation', { name: 'Aether' }))
    const assistant = sidebar.getByRole('button', { name: 'Assistant' })
    expect(assistant.parentElement?.parentElement).toBe(sidebar.getByRole('button', { name: /New run/ }).parentElement)
    expect(assistant.parentElement?.parentElement).not.toBe(sidebar.getByRole('button', { name: 'Search' }).parentElement)
    const header = screen.getAllByRole('banner')[0]!
    expect(within(header).queryByRole('button', { name: 'Assistant' })).toBeNull()

    act(() => useStore.getState().toggleSidebar())
    expect(screen.queryByRole('navigation', { name: 'Aether' })).toBeNull()
    const button = within(header).getByRole('button', { name: 'Assistant' })
    await userEvent.click(button)
    expect(button.getAttribute('aria-expanded')).toBe('true')
    expect(screen.getAllByRole('button', { name: 'Assistant' })).toHaveLength(1)
  })

  it('opens from the palette command Ask Aether', async () => {
    render(<AppShell />)
    act(() => useStore.getState().togglePalette(true))
    await userEvent.click(await screen.findByRole('option', { name: /Ask Aether/ }))
    expect(await panel()).toBeDefined()
    expect(useStore.getState().assistantOpen).toBe(true)
  })

  it('opens from the keyboard shortcut and keeps the chord from a focused terminal or page', async () => {
    render(<AppShell />)
    const forwarded = vi.fn((event: KeyboardEvent) => event.key === ' ' && event.ctrlKey)
    document.body.addEventListener('keydown', forwarded)
    await userEvent.keyboard('{Control>}{Shift>} {/Shift}{/Control}')
    document.body.removeEventListener('keydown', forwarded)
    expect(await panel()).toBeDefined()
    expect(forwarded.mock.results.some((result) => result.value)).toBe(false)
  })

  it('closes from the keyboard shortcut while its own composer has focus', async () => {
    const aside = await openPanel()
    acpOpen()
    await userEvent.click(await within(aside).findByRole('textbox', { name: 'Message the assistant' }))
    await userEvent.keyboard('{Control>}{Shift>} {/Shift}{/Control}')
    expect(screen.queryByRole('complementary', { name: 'Assistant' })).toBeNull()
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Assistant' })))
  })

  it('titles itself below the page heading', async () => {
    const aside = await openPanel()
    expect(within(aside).getByRole('heading', { level: 2, name: 'Assistant' })).toBeDefined()
  })

  it('is absent for a viewer, who cannot launch, on every surface', async () => {
    useStore.setState({ info: { ...serverInfo, member: { ...alice, role: 'viewer' } }, members: { [alice.id]: { ...alice, role: 'viewer' } } })
    render(<AppShell />)
    expect(screen.queryByRole('button', { name: 'Assistant' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Ask Aether' })).toBeNull()
    act(() => useStore.getState().toggleSidebar())
    expect(screen.queryByRole('button', { name: 'Assistant' })).toBeNull()
    act(() => useStore.getState().togglePalette(true))
    await screen.findByRole('dialog')
    expect(screen.queryByRole('option', { name: /Ask Aether/ })).toBeNull()
    await userEvent.keyboard('{Escape}')
    await userEvent.keyboard('{Control>}{Shift>} {/Shift}{/Control}')
    expect(screen.queryByRole('complementary', { name: 'Assistant' })).toBeNull()
  })

  it('is absent from a server that does not serve assistant.status', async () => {
    useStore.setState({ capabilities: { gateway: 'remote', methods: ['run.list'], ws: ['events', 'attach'] } })
    render(<AppShell />)
    expect(screen.queryByRole('button', { name: 'Assistant' })).toBeNull()
  })

  it('keeps the conversation out of the run lists', async () => {
    const aside = await openPanel()
    acpOpen()
    expect(within(aside).queryByRole('log')).toBeDefined()
    expect(Object.keys(useStore.getState().runs)).toEqual([])
  })
})

function acpOpen(items = [] as ReturnType<typeof say>[]) {
  return assistantSocket().open({}, items)
}

describe('entry points', () => {
  it('offers Ask Aether on an empty board', async () => {
    render(<AppShell />)
    await userEvent.click(await screen.findByRole('button', { name: 'Ask Aether' }))
    expect(await panel()).toBeDefined()
  })

  it('offers Stuck? Ask Aether on an onboarding step', async () => {
    render(<><Step label="Agent" title="Set up an agent" /><AppShell /></>)
    await userEvent.click(screen.getByRole('button', { name: 'Stuck? Ask Aether' }))
    expect(await panel()).toBeDefined()
  })
})

describe('states', () => {
  it('explains setup and lists each agent with its reason', async () => {
    vi.mocked(api.assistantStatus).mockResolvedValue(status({
      state: 'setup',
      agent: undefined,
      agents: [
        { name: 'claude', display_name: 'Claude Code', ready: false, reason: 'No login was found' },
        { name: 'codex', display_name: 'Codex', ready: false, reason: 'The Enhanced adapter is not installed' },
      ],
    }))
    const aside = await openPanel()
    expect(await within(aside).findByText(/with a sign-in of its own/)).toBeDefined()
    const agents = within(within(aside).getByRole('list', { name: 'Agents' }))
    expect(agents.getByText('No login was found')).toBeDefined()
    expect(agents.getByText('The Enhanced adapter is not installed')).toBeDefined()
    expect(StubSocket.opened).toHaveLength(0)
  })

  it('signs an agent in for the assistant alone and leaves setup once it is ready', async () => {
    const setup = status({
      state: 'setup',
      agent: undefined,
      agents: [
        { name: 'claude', display_name: 'Claude Code', ready: false, reason: 'Not signed in for the assistant' },
        { name: 'codex', display_name: 'Codex', ready: true },
      ],
    })
    vi.mocked(api.assistantStatus).mockResolvedValue(setup)
    const aside = await openPanel()
    const buttons = await within(aside).findAllByRole('button', { name: 'Sign in' })
    expect(buttons).toHaveLength(1)
    await userEvent.click(buttons[0]!)
    expect(within(aside).getByRole('region', { name: 'Sign in to Claude Code' })).toBeDefined()
    await waitFor(() => expect(StubSocket.opened.some((socket) => socket.url.endsWith('/ws/terminal?assistant_login=claude'))).toBe(true))
    expect(StubSocket.opened.some((socket) => socket.url.includes('tab='))).toBe(false)

    const reads = vi.mocked(api.assistantStatus).mock.calls.length
    vi.mocked(api.assistantStatus).mockResolvedValue(status())
    await userEvent.click(within(aside).getByRole('button', { name: 'Close' }))
    await waitFor(() => expect(vi.mocked(api.assistantStatus).mock.calls.length).toBeGreaterThan(reads))
    await waitFor(() => expect(useStore.getState().assistantStatus?.state).toBe('idle'))
  })

  it('hides New conversation until there is a conversation', async () => {
    vi.mocked(api.assistantStatus).mockResolvedValue(status({ state: 'setup', agent: undefined }))
    const aside = await openPanel()
    await within(aside).findByText(/with a sign-in of its own/)
    expect(within(aside).queryByRole('button', { name: 'New conversation' })).toBeNull()
  })

  it('shows the real error of a failed assistant and retries', async () => {
    vi.mocked(api.assistantStatus).mockResolvedValue(status({ state: 'failed', error: 'adapter exited: exit status 1' }))
    vi.mocked(api.assistantStart).mockResolvedValue(status())
    const aside = await openPanel()
    expect(await within(aside).findByText('adapter exited: exit status 1')).toBeDefined()
    await userEvent.click(within(aside).getByRole('button', { name: 'Retry' }))
    expect(api.assistantStart).toHaveBeenCalledWith({})
  })

  it('says when it acts without asking and links to the setting', async () => {
    const aside = await openPanel()
    expect(within(aside).queryByText(/Acts without asking/)).toBeNull()
    acpOpen()
    assistantSocket().send({ status: status({ auto_approve: true }) })
    expect(await within(aside).findByText(/Acts without asking/)).toBeDefined()
    expect(within(aside).getByText(/launches, messages and stops your runs and saves your environment without asking/)).toBeDefined()
    await userEvent.click(within(aside).getByRole('button', { name: 'Change in Settings' }))
    expect(useStore.getState().route).toEqual({ name: 'settings', params: { section: 'auto-approve' } })
    expect(useStore.getState().assistantOpen).toBe(true)
  })

  it('offers example prompts that fill the composer', async () => {
    const aside = await openPanel()
    acpOpen()
    await userEvent.click(await within(aside).findByRole('button', { name: 'Launch a run to …' }))
    expect((within(aside).getByRole('textbox', { name: 'Message the assistant' }) as HTMLTextAreaElement).value).toBe('Launch a run to ')
  })
})

describe('the conversation', () => {
  it('renders streamed items', async () => {
    const aside = await openPanel()
    acpOpen([
      item('turn_start', 1),
      say(1, 'user', 'What is running?'),
      say(1, 'assistant', 'Two runs are **working**.'),
      item('turn_end', 1, { stop_reason: 'end_turn' }),
    ])
    const log = await within(aside).findByRole('log', { name: 'Assistant conversation' })
    expect(await within(log).findByText('What is running?')).toBeDefined()
    expect(within(log).getByText('working').tagName).toBe('STRONG')
    expect(assistantSocket().header()).toEqual({ after_seq: 0, epoch: 0 })
  })

  it('sends a prompt with Enter and keeps a newline on Shift+Enter', async () => {
    vi.mocked(api.assistantPrompt).mockResolvedValue({})
    const aside = await openPanel()
    acpOpen()
    const box = await within(aside).findByRole('textbox', { name: 'Message the assistant' })
    await userEvent.type(box, 'line one{Shift>}{Enter}{/Shift}line two')
    expect(api.assistantPrompt).not.toHaveBeenCalled()
    await userEvent.keyboard('{Enter}')
    await waitFor(() => expect(api.assistantPrompt).toHaveBeenCalledTimes(1))
    expect(vi.mocked(api.assistantPrompt).mock.calls[0]![0]).toBe('line one\nline two')
    expect(vi.mocked(api.assistantPrompt).mock.calls[0]![1]).toMatch(/\S/)
    await waitFor(() => expect((box as HTMLTextAreaElement).value).toBe(''))
  })

  it('does not send on Enter with a touch screen, where Enter is a newline', async () => {
    atViewport(1200, { pointer: 'coarse' })
    const aside = await openPanel()
    acpOpen()
    const box = await within(aside).findByRole('textbox', { name: 'Message the assistant' })
    await userEvent.type(box, 'one{Enter}two')
    expect(api.assistantPrompt).not.toHaveBeenCalled()
    expect((box as HTMLTextAreaElement).value).toBe('one\ntwo')
  })

  it('sends with the Send button and shows a refusal without losing the draft', async () => {
    vi.mocked(api.assistantPrompt).mockRejectedValue(new Error('assistant is not running'))
    const aside = await openPanel()
    acpOpen()
    const box = await within(aside).findByRole('textbox', { name: 'Message the assistant' })
    await userEvent.type(box, 'hello')
    await userEvent.click(within(aside).getByRole('button', { name: /^Send/ }))
    expect(await within(aside).findByRole('alert')).toHaveProperty('textContent', 'assistant is not running')
    expect((box as HTMLTextAreaElement).value).toBe('hello')
  })

  it('shows Stop while a turn runs and cancels it', async () => {
    vi.mocked(api.assistantCancel).mockResolvedValue({})
    const aside = await openPanel()
    assistantSocket().open({ state: state({ turn_in_flight: true }) }, [item('turn_start', 1), say(1, 'user', 'Install Rust')])
    await userEvent.click(await within(aside).findByRole('button', { name: 'Stop' }))
    expect(api.assistantCancel).toHaveBeenCalled()
  })

  it('gives Stop back with the real error when assistant.cancel never answers', async () => {
    let aborted: AbortSignal | undefined
    let timeOut!: () => void
    vi.mocked(api.assistantCancel).mockImplementation((signal) => new Promise((_, reject) => {
      aborted = signal
      timeOut = () => reject(new DOMException('signal timed out', 'TimeoutError'))
    }))
    const aside = await openPanel()
    assistantSocket().open({ state: state({ turn_in_flight: true }) }, [item('turn_start', 1), say(1, 'user', 'Install Rust')])
    await userEvent.click(await within(aside).findByRole('button', { name: 'Stop' }))
    expect(aborted).toBeInstanceOf(AbortSignal)
    expect(within(aside).getByRole('button', { name: 'Stopping…' })).toBeDefined()
    act(() => timeOut())
    expect(await within(aside).findByRole('alert')).toHaveProperty('textContent', `assistant.cancel got no answer within ${stopTimeout / 1000} seconds`)
    expect(within(aside).getByRole('button', { name: 'Stop' })).toBeDefined()
  })

  it('keeps a focused step of the reply in place when the turn ends', async () => {
    const aside = await openPanel()
    const session = assistantSocket().open({ state: state({ turn_in_flight: true }) }, [
      item('turn_start', 1),
      say(1, 'user', 'Check the tests'),
      tool(1, 'x1', 'execute', 'npm test', 'in_progress'),
    ])
    const log = await within(aside).findByRole('log', { name: 'Assistant conversation' })
    const step = await within(log).findByRole('button', { name: /^Running npm test(?!,)/ })
    act(() => step.focus())
    session.items(tool(1, 'x1', 'execute', 'npm test', 'completed', { exit_code: 0 }), say(1, 'assistant', 'All green.'), item('turn_end', 1, { stop_reason: 'end_turn' }))
    expect(await within(log).findByText('All green.')).toBeDefined()
    expect(document.activeElement).toBe(step)
    expect(step.textContent).toContain('Ran npm test')
  })

  it('answers a harness permission prompt with assistant.answer', async () => {
    vi.mocked(api.assistantAnswer).mockResolvedValue({})
    const aside = await openPanel()
    acpOpen()
    assistantSocket().items(item('request', 1, {
      request: {
        id: 'req_1',
        kind: 'permission',
        title: 'rm -rf build',
        status: 'pending',
        options: [
          { id: 'allow', name: 'Allow', kind: 'allow_once' },
          { id: 'reject', name: 'Reject', kind: 'reject_once' },
        ],
      },
    }))
    await userEvent.click(await within(aside).findByRole('button', { name: 'Allow' }))
    expect(api.assistantAnswer).toHaveBeenCalledWith('req_1', 'allow', undefined)
  })
})

describe('confirmations', () => {
  it('says what a launch starts and on whose account, and renders a message readably', async () => {
    useStore.setState({ members: { [alice.id]: alice, [bob.id]: bob } })
    const aside = await openPanel()
    acpOpen()
    assistantSocket().send({ status: status({ confirmations: [
      { ...stop, id: 'l', method: 'run.launch', summary: 'Launch claude (acp) in api on "Bob"\'s account, falling back to "mem_carol"\'s account in order: "Fix the flaky login test"', other_member: true, other_owner: 'Bob', params: { workspace_id: 'ws_1', harness: 'claude', mode: 'acp', model: 'opus', account_member_id: 'mem_bob', fallback_account_member_ids: ['mem_carol'], task: 'Fix the flaky login test\nthen open a PR' } },
      { ...stop, id: 'm', method: 'run.inject', summary: 'Message a run', params: { run_id: 'run_9', message: 'Please stop and summarise' } },
    ] }) })
    const launch = await within(aside).findByRole('group', { name: /^Approve this action\? Launch claude \(acp\) in api on "Bob"'s account/ })
    expect(within(launch).getByText('Launch claude (acp) in api on "Bob"\'s account, falling back to "mem_carol"\'s account in order')).toBeDefined()
    expect(within(launch).getByText("Bob's account pays for this launch, so this asks even with auto-approval on.")).toBeDefined()
    const rows = Object.fromEntries(within(launch).getAllByRole('term').map((term) => [term.textContent, term.nextElementSibling?.textContent]))
    expect(rows).toEqual({
      Kind: 'Run: one agent on one task',
      Workspace: 'ws_1',
      Agent: 'Claude Code opus (Enhanced)',
      Account: "Bob's account, shared with you",
      Fallback: 'Then mem_carol',
      Task: 'Fix the flaky login test\nthen open a PR',
      'You hear back': hearBack.run,
    })
    const inject = within(aside).getByRole('group', { name: 'Approve this action? Message a run' })
    expect(within(inject).getByText('Message')).toBeDefined()
    expect(within(inject).getByText('Please stop and summarise')).toBeDefined()
  })

  it('says which launch choices came from saved defaults, which saved ones were skipped or unchecked, and why', async () => {
    useStore.setState({ members: { [alice.id]: alice, [bob.id]: bob } })
    const aside = await openPanel()
    acpOpen()
    assistantSocket().send({ status: status({ confirmations: [
      { ...stop, id: 'l', method: 'run.launch', summary: 'Launch claude (acp) in api on "Bob"\'s account (your saved default), no fallback: "Add the export"',
        params: { workspace_id: 'ws_1', harness: 'claude', mode: 'acp', account_member_id: 'mem_bob', no_fallback: true, task: 'Add the export' },
        defaults: { applied: ['harness', 'mode', 'account_member_id', 'fallback', 'model'], skipped: [{ field: 'mode', reason: 'enhanced mode is not installed' },
          { field: 'fallback_account_member_ids', reason: 'mem_ada: not shared' }, { field: 'fallback_account_member_ids', reason: 'mem_cy: not shared' }],
          unchecked: [{ field: 'model', reason: 'no current model list' }] } },
    ] }) })
    const launch = await within(aside).findByRole('group', { name: /^Approve this action\? Launch claude/ })
    const rows = Object.fromEntries(within(launch).getAllByRole('term').map((term) => [term.textContent, term.nextElementSibling?.textContent]))
    expect(rows['From your saved defaults']).toBe('agent, mode, account, fallback, model')
    expect(rows['Saved mode not used']).toBe('enhanced mode is not installed')
    expect(Object.values(rows).filter((v) => v?.includes('not shared'))).toEqual(['mem_ada: not shared; mem_cy: not shared'])
    expect(rows['Saved model not checked']).toBe('no current model list')
    expect(rows.Account).toBe("Bob's account, shared with you")
  })

  it('approves and declines through assistant.confirm and clears on a status frame', async () => {
    vi.mocked(api.assistantConfirm).mockResolvedValue({})
    const aside = await openPanel()
    acpOpen()
    assistantSocket().send({ status: status({ confirmations: [stop, { ...stop, id: 'c2', summary: 'Save your environment' }] }) })
    const card = await within(aside).findByRole('group', { name: `Approve this action? ${stop.summary}` })
    expect(within(card).getByText('run.kill')).toBeDefined()
    expect(within(card).getByText('Run')).toBeDefined()
    expect(within(card).getByText('run-8x2')).toBeDefined()
    expect(within(card).getByText(/expires/)).toBeDefined()

    await userEvent.click(within(card).getByRole('button', { name: 'Approve' }))
    expect(api.assistantConfirm).toHaveBeenCalledWith('c1', true)
    for (const name of ['Approving…', 'Decline']) expect(within(card).getByRole('button', { name })).toHaveProperty('disabled', true)
    await userEvent.click(within(card).getByRole('button', { name: 'Decline' }))
    expect(api.assistantConfirm).toHaveBeenCalledTimes(1)
    expect(within(aside).getByText(stop.summary)).toBeDefined()

    const second = within(aside).getByRole('group', { name: 'Approve this action? Save your environment' })
    await userEvent.click(within(second).getByRole('button', { name: 'Decline' }))
    expect(api.assistantConfirm).toHaveBeenCalledWith('c2', false)

    assistantSocket().send({ status: status() })
    await waitFor(() => expect(within(aside).queryByText(stop.summary)).toBeNull())
  })

  it('discards the conversation only after the member confirms', async () => {
    vi.mocked(api.assistantReset).mockResolvedValue(status())
    const aside = await openPanel()
    acpOpen([item('turn_start', 1), say(1, 'user', 'old question'), item('turn_end', 1)])
    expect(await within(aside).findByText('old question')).toBeDefined()
    await userEvent.click(within(aside).getByRole('button', { name: 'New conversation' }))
    expect(api.assistantReset).not.toHaveBeenCalled()
    await userEvent.click(await screen.findByRole('button', { name: 'Start new conversation' }))
    await waitFor(() => expect(api.assistantReset).toHaveBeenCalled())
    await waitFor(() => expect(within(aside).queryByText('old question')).toBeNull())
  })
})

describe('the auto-approval suggestion', () => {
  const title = 'Skip approval cards for the assistant?'
  const conversation = () => [item('turn_start', 1), say(1, 'user', 'Stop the flaky run'), item('turn_end', 1, { stop_reason: 'end_turn' })]

  it('shows only while the assistant is idle with a conversation and nothing waiting', async () => {
    const aside = await openPanel()
    acpOpen()
    assistantSocket().send({ status: status({ suggest_auto_approve: true }) })
    await within(aside).findByRole('button', { name: 'Launch a run to …' })
    expect(within(aside).queryByRole('group', { name: title })).toBeNull()

    assistantSocket().items(...conversation())
    const notice = await within(aside).findByRole('group', { name: title })
    expect(within(notice).getByText(/Each action is still noted here before it runs/)).toBeDefined()
    expect(within(notice).getByText("Still asks: messaging or stopping another member's run.")).toBeDefined()
    for (const name of ['Turn on auto-approval', 'Not now', "Don't suggest again"]) {
      expect(within(notice).getByRole('button', { name }).className).toBe(within(notice).getByRole('button', { name: 'Not now' }).className)
    }

    assistantSocket().send({ status: status({ suggest_auto_approve: true, confirmations: [stop] }) })
    await waitFor(() => expect(within(aside).queryByRole('group', { name: title })).toBeNull())
    assistantSocket().send({ status: status({ suggest_auto_approve: true, state: 'running' }) }).items(item('turn_start', 2))
    await waitFor(() => expect(within(aside).queryByRole('button', { name: 'Stop' })).not.toBeNull())
    expect(within(aside).queryByRole('group', { name: title })).toBeNull()
    assistantSocket().send({ status: status({ suggest_auto_approve: false }) }).items(item('turn_end', 2, { stop_reason: 'end_turn' }))
    await waitFor(() => expect(within(aside).queryByRole('button', { name: 'Stop' })).toBeNull())
    expect(within(aside).queryByRole('group', { name: title })).toBeNull()
  })

  it('turns auto-approval on without a second dialog and confirms until the next turn', async () => {
    vi.mocked(api.assistantSettings).mockResolvedValue({ auto_approve: true, suggest_auto_approve: false, suggestion_off: false, share_view: true })
    const aside = await openPanel()
    acpOpen(conversation())
    assistantSocket().send({ status: status({ suggest_auto_approve: true }) })
    await userEvent.click(within(await within(aside).findByRole('group', { name: title })).getByRole('button', { name: 'Turn on auto-approval' }))
    expect(api.assistantSettings).toHaveBeenCalledWith({ auto_approve: true })
    expect(screen.queryByRole('alertdialog')).toBeNull()
    expect(await within(aside).findByText(/Auto-approval is on\. Turn it off any time in/)).toBeDefined()
    expect(within(aside).queryByRole('group', { name: title })).toBeNull()
    expect(useStore.getState().assistantStatus?.auto_approve).toBe(true)

    assistantSocket().send({ status: status({ auto_approve: true, state: 'running' }) }).items(item('turn_start', 2))
    await waitFor(() => expect(within(aside).queryByText(/Auto-approval is on\./)).toBeNull())
  })

  it.each([
    ['Not now', { suggestion: 'later' }, false, /You can turn this on later in/],
    ['Not now', { suggestion: 'later' }, true, /We won't suggest this again\. Auto-approval stays available in/],
    ["Don't suggest again", { suggestion: 'never' }, true, /We won't suggest this again\. Auto-approval stays available in/],
  ] as const)('answers %s (suggestion_off %s) and links to Settings → Assistant', async (button, params, off, confirmation) => {
    vi.mocked(api.assistantSettings).mockResolvedValue({ auto_approve: false, suggest_auto_approve: false, suggestion_off: off, share_view: true })
    const aside = await openPanel()
    acpOpen(conversation())
    assistantSocket().send({ status: status({ suggest_auto_approve: true }) })
    await userEvent.click(within(await within(aside).findByRole('group', { name: title })).getByRole('button', { name: button }))
    expect(api.assistantSettings).toHaveBeenCalledWith(params)
    const said = await within(aside).findByText(confirmation)
    expect(document.activeElement).toBe(said)
    expect(within(aside).queryByRole('group', { name: title })).toBeNull()
    await userEvent.click(within(said).getByRole('button', { name: 'Settings → Assistant' }))
    expect(useStore.getState().route).toEqual({ name: 'settings', params: { section: 'assistant' } })
    expect(useStore.getState().assistantOpen).toBe(true)
  })

  it('drops the answer when the conversation is discarded', async () => {
    vi.mocked(api.assistantSettings).mockResolvedValue({ auto_approve: false, suggest_auto_approve: false, suggestion_off: false, share_view: true })
    vi.mocked(api.assistantReset).mockResolvedValue(status())
    const aside = await openPanel()
    acpOpen(conversation())
    assistantSocket().send({ status: status({ suggest_auto_approve: true }) })
    await userEvent.click(within(await within(aside).findByRole('group', { name: title })).getByRole('button', { name: 'Not now' }))
    expect(await within(aside).findByText(/You can turn this on later in/)).toBeDefined()
    await userEvent.click(within(aside).getByRole('button', { name: 'New conversation' }))
    await userEvent.click(await screen.findByRole('button', { name: 'Start new conversation' }))
    await waitFor(() => expect(within(aside).queryByText(/You can turn this on later in/)).toBeNull())
  })

  it('ignores an answer that resolves after another member signed in', async () => {
    let resolve: (s: AssistantSettings) => void = () => {}
    vi.mocked(api.assistantSettings).mockReturnValue(new Promise((r) => { resolve = r }))
    const aside = await openPanel()
    acpOpen(conversation())
    assistantSocket().send({ status: status({ suggest_auto_approve: true }) })
    await userEvent.click(within(await within(aside).findByRole('group', { name: title })).getByRole('button', { name: 'Turn on auto-approval' }))
    act(() => useStore.setState({ identityKey: 'someone else' }))
    await act(async () => resolve({ auto_approve: true, suggest_auto_approve: false, suggestion_off: false, share_view: true }))
    expect(useStore.getState().assistantStatus?.auto_approve).toBe(false)
    expect(within(aside).queryByText(/Auto-approval is on\./)).toBeNull()
  })

  it('drops an answer that resolves after the next turn started', async () => {
    let resolve: (s: AssistantSettings) => void = () => {}
    vi.mocked(api.assistantSettings).mockReturnValue(new Promise((r) => { resolve = r }))
    const aside = await openPanel()
    acpOpen(conversation())
    assistantSocket().send({ status: status({ suggest_auto_approve: true }) })
    await userEvent.click(within(await within(aside).findByRole('group', { name: title })).getByRole('button', { name: 'Turn on auto-approval' }))
    assistantSocket().send({ status: status({ state: 'running' }) }).items(item('turn_start', 2))
    await waitFor(() => expect(within(aside).queryByRole('button', { name: 'Stop' })).not.toBeNull())
    await act(async () => resolve({ auto_approve: true, suggest_auto_approve: false, suggestion_off: false, share_view: true }))
    expect(useStore.getState().assistantStatus?.auto_approve).toBe(true)
    expect(within(aside).queryByText(/Auto-approval is on\./)).toBeNull()
    assistantSocket().send({ status: status({ auto_approve: true }) }).items(item('turn_end', 2, { stop_reason: 'end_turn' }))
    await waitFor(() => expect(within(aside).queryByRole('button', { name: 'Stop' })).toBeNull())
    expect(within(aside).queryByText(/Auto-approval is on\./)).toBeNull()
  })

  it('keeps the notice and shows the error when the answer fails', async () => {
    vi.mocked(api.assistantSettings).mockRejectedValue(new Error('assistant.settings: store: put assistant settings: disk I/O error'))
    const aside = await openPanel()
    acpOpen(conversation())
    assistantSocket().send({ status: status({ suggest_auto_approve: true }) })
    const notice = await within(aside).findByRole('group', { name: title })
    await userEvent.click(within(notice).getByRole('button', { name: 'Not now' }))
    expect(await within(notice).findByRole('alert')).toHaveProperty('textContent', expect.stringContaining('disk I/O error'))
  })
})

describe('the approval card', () => {
  it('links to the setting while auto-approval is off', async () => {
    const aside = await openPanel()
    acpOpen()
    assistantSocket().send({ status: status({ confirmations: [stop] }) })
    const card = await within(aside).findByRole('group', { name: `Approve this action? ${stop.summary}` })
    await userEvent.click(within(card).getByRole('button', { name: 'Skip these cards…' }))
    expect(useStore.getState().route).toEqual({ name: 'settings', params: { section: 'assistant' } })
  })

  it("says why another member's run still asks, with no link", async () => {
    const aside = await openPanel()
    acpOpen()
    assistantSocket().send({ status: status({ auto_approve: true, confirmations: [{ ...stop, summary: `${stop.summary}, owned by Grace`, other_member: true, other_owner: 'Grace' }] }) })
    const card = await within(aside).findByRole('group', { name: `Approve this action? ${stop.summary}, owned by Grace` })
    expect(within(card).getByText('Grace owns this run, so this asks even with auto-approval on.')).toBeDefined()
    expect(within(card).queryByRole('button', { name: 'Skip these cards…' })).toBeNull()
  })
})

describe('on a phone', () => {
  it('renders a side sheet instead of an aside', async () => {
    atViewport(500)
    render(<AppShell />)
    await userEvent.click(screen.getByRole('button', { name: 'Assistant' }))
    const sheet = await screen.findByRole('dialog', { name: 'Assistant' })
    expect(sheet.getAttribute('data-variant')).toBe('side')
    expect(screen.queryByRole('complementary', { name: 'Assistant' })).toBeNull()
    expect(within(sheet).getByRole('button', { name: 'New conversation' })).toBeDefined()
  })

  it('closes the sheet when a card links to Settings', async () => {
    atViewport(500)
    render(<AppShell />)
    await userEvent.click(screen.getByRole('button', { name: 'Assistant' }))
    const sheet = await screen.findByRole('dialog', { name: 'Assistant' })
    acpOpen()
    assistantSocket().send({ status: status({ confirmations: [stop] }) })
    await userEvent.click(await within(sheet).findByRole('button', { name: 'Skip these cards…' }))
    expect(useStore.getState().route).toEqual({ name: 'settings', params: { section: 'assistant' } })
    expect(useStore.getState().assistantOpen).toBe(false)
  })

  it('closes the sheet when the auto-approval line links to Settings', async () => {
    atViewport(500)
    render(<AppShell />)
    await userEvent.click(screen.getByRole('button', { name: 'Assistant' }))
    const sheet = await screen.findByRole('dialog', { name: 'Assistant' })
    acpOpen()
    assistantSocket().send({ status: status({ auto_approve: true }) })
    await userEvent.click(await within(sheet).findByRole('button', { name: 'Change in Settings' }))
    expect(useStore.getState().route.name).toBe('settings')
    expect(useStore.getState().assistantOpen).toBe(false)
  })
})

describe('while closed', () => {
  it('badges the button and keeps the stream up while an approval is pending', async () => {
    vi.mocked(api.assistantStatus).mockResolvedValue(status({ confirmations: [stop] }))
    render(<AppShell />)
    expect(await screen.findByRole('button', { name: 'Assistant, 1 request waiting' })).toBeDefined()
    await waitFor(() => expect(StubSocket.opened.some((s) => s.url.includes('/ws/assistant'))).toBe(true))
    assistantSocket().open().send({ status: status() })
    expect(await screen.findByRole('button', { name: 'Assistant' })).toBeDefined()
  })

  it('badges a harness prompt too and keeps the stream for it', async () => {
    useStore.setState({ assistantStatus: status({ state: 'running' }) })
    render(<AppShell />)
    await waitFor(() => expect(StubSocket.opened.some((s) => s.url.includes('/ws/assistant'))).toBe(true))
    assistantSocket().open({ state: { turn_in_flight: true, queued: 0, pending: [], last_activity: '', prompt_images: false } }).send(
      { status: status() },
      { seq: 1, item: item('request', 1, { request: { id: 'r', kind: 'question', title: 'Which branch?', status: 'pending', options: [{ id: 'accept', name: 'Answer' }] } }) },
    )
    expect(await screen.findByRole('button', { name: 'Assistant, 1 request waiting' })).toBeDefined()
    assistantSocket().send({ status: status({ confirmations: [stop] }) })
    expect(await screen.findByRole('button', { name: 'Assistant, 2 requests waiting' })).toBeDefined()
  })

  it('stays disconnected while idle with nothing pending', async () => {
    render(<AppShell />)
    await waitFor(() => expect(api.assistantStatus).toHaveBeenCalledTimes(1))
    expect(StubSocket.opened).toHaveLength(0)
  })
})

describe('a retained draft', () => {
  it('is retried under the same key after the panel is closed and reopened', async () => {
    vi.mocked(api.assistantPrompt).mockRejectedValueOnce(new Error('reply lost')).mockResolvedValue({})
    const aside = await openPanel()
    acpOpen()
    await userEvent.type(await within(aside).findByRole('textbox', { name: 'Message the assistant' }), 'install rust')
    await userEvent.click(within(aside).getByRole('button', { name: /^Send/ }))
    await within(aside).findByRole('alert')
    await userEvent.click(within(aside).getByRole('button', { name: 'Close assistant' }))
    await userEvent.click(screen.getByRole('button', { name: 'Assistant' }))
    const reopened = await panel()
    acpOpen()
    await userEvent.click(await within(reopened).findByRole('button', { name: /^Send/ }))
    await waitFor(() => expect(api.assistantPrompt).toHaveBeenCalledTimes(2))
    const [first, second] = vi.mocked(api.assistantPrompt).mock.calls
    expect(second![1]).toBe(first![1])
    expect(first![2]).toMatchObject({ version: 1, route: { name: 'board' } })
    expect(second![2]).toBe(first![2])
    expect(useStore.getState().assistantAttempt).toBeNull()
  })

  it('gets a new key once its text changes', async () => {
    vi.mocked(api.assistantPrompt).mockRejectedValueOnce(new Error('lost')).mockResolvedValue({})
    const aside = await openPanel()
    acpOpen()
    const box = await within(aside).findByRole('textbox', { name: 'Message the assistant' })
    await userEvent.type(box, 'one')
    await userEvent.click(within(aside).getByRole('button', { name: /^Send/ }))
    await within(aside).findByRole('alert')
    await userEvent.type(box, ' two')
    await userEvent.click(within(aside).getByRole('button', { name: /^Send/ }))
    await waitFor(() => expect(api.assistantPrompt).toHaveBeenCalledTimes(2))
    const [first, second] = vi.mocked(api.assistantPrompt).mock.calls
    expect(second![1]).not.toBe(first![1])
  })
})

describe('the view context', () => {
  it('shows what is sent, and once turned off sends nothing more', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    try {
      vi.mocked(api.assistantPrompt).mockResolvedValue({})
      vi.mocked(api.assistantSettings).mockResolvedValue({ auto_approve: false, suggest_auto_approve: false, suggestion_off: false, share_view: false })
      useStore.getState().navigate('run', { runId: 'run-8x2', view: 'changes' })
      const aside = await openPanel()
      acpOpen()
      await act(() => vi.advanceTimersByTimeAsync(viewSyncDelay))
      expect(api.assistantView).toHaveBeenCalledTimes(1)
      expect(vi.mocked(api.assistantView).mock.calls[0]![0].route).toMatchObject({ run_id: 'run-8x2', view: 'changes' })

      const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime })
      const chips = await within(aside).findByRole('list', { name: 'Sent with this message' })
      expect(within(chips).getByRole('button', { name: 'Remove page run-8x2 from the message' })).toBeDefined()
      await user.click(within(aside).getByRole('button', { name: 'Inspect what is shared' }))
      const shared = await screen.findByRole('dialog', { name: 'What is shared' })
      expect(within(shared).getByRole('region', { name: 'Sent with the next message' }).textContent).toContain('run-8x2 · changes')
      await user.click(within(shared).getByRole('checkbox', { name: 'Share the page I am on with the assistant' }))
      expect(api.assistantSettings).toHaveBeenCalledWith({ share_view: false })
      await user.keyboard('{Escape}')
      expect(within(aside).getByRole('button', { name: /^Page not shared/ })).toBeDefined()

      act(() => useStore.getState().navigate('board'))
      await act(() => vi.advanceTimersByTimeAsync(5 * viewSyncDelay))
      await user.type(within(aside).getByRole('textbox', { name: 'Message the assistant' }), 'what is this?{Enter}')
      await waitFor(() => expect(api.assistantPrompt).toHaveBeenCalledTimes(1))
      expect(vi.mocked(api.assistantPrompt).mock.calls[0]![2]).toBeUndefined()
      expect(api.assistantView).toHaveBeenCalledTimes(1)
      expect(useStore.getState().assistantStatus?.share_view).toBe(false)
    } finally {
      vi.useRealTimers()
    }
  })
})

describe('a new conversation', () => {
  it('clears the transcript when a reconnect meets a new epoch', async () => {
    const aside = await openPanel()
    acpOpen([item('turn_start', 1), say(1, 'user', 'old question'), item('turn_end', 1)])
    expect(await within(aside).findByText('old question')).toBeDefined()
    const held = useStore.getState().acpSessions.assistant!.epoch
    const before = StubSocket.opened.length
    assistantSocket().close(1006)
    await waitFor(() => expect(StubSocket.opened.length).toBeGreaterThan(before))
    resetItems()
    const reconnected = assistantSocket().open({ epoch: 1, seq: 2, replay: 2 }, [item('turn_start', 1), say(1, 'user', 'new question')])
    expect(reconnected.header()).toEqual({ after_seq: 3, epoch: held })
    expect(await within(aside).findByText('new question')).toBeDefined()
    expect(within(aside).queryByText('old question')).toBeNull()
  })

  it('drops a full-item read that finishes after the reset', async () => {
    const whole = item('tool_call', 1, { tool_call: { id: 't', title: 'late', status: 'completed' } })
    let finish: (value: typeof whole) => void = () => {}
    vi.mocked(api.assistantItem).mockReturnValue(new Promise((resolve) => { finish = resolve }))
    const read = loadWholeItem(useStore, api, 'assistant', whole.seq)
    act(() => useStore.getState().restartAssistantConversation())
    const replace = vi.spyOn(useStore.getState(), 'acpReplaceItem')
    finish(whole)
    await read
    expect(replace).not.toHaveBeenCalled()
  })

  it('drops a full-item read that finishes after another tab reset the conversation', async () => {
    const whole = item('tool_call', 1, { tool_call: { id: 't', title: 'late', status: 'completed' } })
    let finish: (value: typeof whole) => void = () => {}
    vi.mocked(api.assistantItem).mockReturnValue(new Promise((resolve) => { finish = resolve }))
    act(() => useStore.getState().acpAck('assistant', { ok: true, seq: 0, replay: 0, epoch: 0, live: true, has_control: false }))
    const read = loadWholeItem(useStore, api, 'assistant', whole.seq)
    act(() => useStore.getState().acpAck('assistant', { ok: true, seq: 0, replay: 0, epoch: 1, live: true, has_control: false }))
    const replace = vi.spyOn(useStore.getState(), 'acpReplaceItem')
    finish(whole)
    await read
    expect(replace).not.toHaveBeenCalled()
  })

  it('sends the next prompt under a fresh idempotency key', async () => {
    vi.mocked(api.assistantPrompt).mockRejectedValueOnce(new Error('lost')).mockResolvedValue({})
    const aside = await openPanel()
    acpOpen()
    const box = await within(aside).findByRole('textbox', { name: 'Message the assistant' })
    await userEvent.type(box, 'same words')
    await userEvent.click(within(aside).getByRole('button', { name: /^Send/ }))
    await within(aside).findByRole('alert')
    act(() => useStore.getState().restartAssistantConversation())
    await userEvent.click(within(aside).getByRole('button', { name: /^Send/ }))
    await waitFor(() => expect(api.assistantPrompt).toHaveBeenCalledTimes(2))
    const [first, second] = vi.mocked(api.assistantPrompt).mock.calls
    expect(second![1]).not.toBe(first![1])
  })
})

describe('truncated items', () => {
  it('are read whole with assistant.item, not run.acp.item', async () => {
    const whole = item('tool_call', 1, { tool_call: { id: 't', title: 'ls', status: 'completed' } })
    vi.mocked(api.assistantItem).mockResolvedValue(whole)
    await loadWholeItem(useStore, api, 'assistant', whole.seq)
    expect(api.assistantItem).toHaveBeenCalledWith(whole.seq)
    expect(api.runACPItem).not.toHaveBeenCalled()
  })
})

describe('width', () => {
  it('resizes from the keyboard and persists open and width', async () => {
    atViewport(1600)
    const aside = await openPanel()
    const handle = within(aside).getByRole('separator', { name: 'Resize assistant' })
    expect(handle.getAttribute('aria-valuenow')).toBe('400')
    handle.focus()
    fireEvent.keyDown(handle, { key: 'ArrowLeft' })
    expect(useStore.getState().assistantWidth).toBe(416)
    expect(aside.style.width).toBe('416px')
    fireEvent.keyDown(handle, { key: 'End' })
    expect(useStore.getState().assistantWidth).toBe(640)
    fireEvent.keyDown(handle, { key: 'Home' })
    expect(useStore.getState().assistantWidth).toBe(320)

    const stored = JSON.parse(localStorage.getItem('aether.ui')!).state
    expect(stored.assistantOpen).toBe(true)
    expect(stored.assistantWidth).toBe(320)
  })

  it('never persists or restores an open sheet on a phone', async () => {
    atViewport(500)
    render(<AppShell />)
    await userEvent.click(screen.getByRole('button', { name: 'Assistant' }))
    await screen.findByRole('dialog', { name: 'Assistant' })
    expect(JSON.parse(localStorage.getItem('aether.ui')!).state.assistantOpen).toBe(false)
    localStorage.setItem('aether.ui', JSON.stringify({ state: { assistantOpen: true }, version: 8 }))
    expect(createRootStore().getState().assistantOpen).toBe(false)
  })

  it('clamps to the room beside the sidebar and re-clamps on resize', async () => {
    useStore.setState({ assistantWidth: 640, sidebarWidth: 260, sidebarCollapsed: false })
    const resize = atViewport(1500)
    const aside = await openPanel()
    expect(aside.style.width).toBe('640px')
    resize(1200)
    await waitFor(() => expect(aside.style.width).toBe('380px'))
    expect(useStore.getState().assistantWidth).toBe(640)
    resize(1500)
    await waitFor(() => expect(aside.style.width).toBe('640px'))
  })

  it('floats over main without a backdrop when docking would squeeze it, and closes on Escape', async () => {
    useStore.setState({ assistantWidth: 400, sidebarWidth: 260, sidebarCollapsed: false })
    atViewport(1000)
    render(<AppShell />)
    const button = screen.getByRole('button', { name: 'Assistant' })
    await userEvent.click(button)
    const aside = await panel()
    expect(screen.queryByRole('dialog', { name: 'Assistant' })).toBeNull()
    expect(aside.style.width).toBe('400px')
    expect(within(aside).queryByRole('separator', { name: 'Resize assistant' })).toBeNull()
    expect(document.getElementById('main')?.closest('[inert], [aria-hidden="true"]')).toBeNull()

    useStore.getState().setAssistantDraft('keep this')
    await userEvent.click(within(aside).getByRole('button', { name: 'New conversation' }))
    await userEvent.keyboard('{Escape}')
    expect(await panel()).toBe(aside)
    within(aside).getByRole('button', { name: 'Close assistant' }).focus()
    await userEvent.keyboard('{Escape}')
    expect(screen.queryByRole('complementary', { name: 'Assistant' })).toBeNull()
    await waitFor(() => expect(document.activeElement).toBe(button))
    expect(useStore.getState().assistantDraft).toBe('keep this')
  })

  it('keeps the same panel and its focused composer when it switches between docked and floating', async () => {
    useStore.setState({ assistantWidth: 640, sidebarWidth: 260, sidebarCollapsed: false })
    const resize = atViewport(1500)
    const aside = await openPanel()
    acpOpen()
    const composer = await within(aside).findByRole('textbox', { name: 'Message the assistant' })
    await userEvent.click(composer)
    resize(1000)
    await waitFor(() => expect(within(aside).queryByRole('separator', { name: 'Resize assistant' })).toBeNull())
    expect(await panel()).toBe(aside)
    expect(aside.style.width).toBe('500px')
    expect(document.activeElement).toBe(composer)
  })

  it('restores a stored width within its bounds', () => {
    localStorage.setItem('aether.ui', JSON.stringify({ state: { assistantOpen: true, assistantWidth: 9000 }, version: 8 }))
    const restored = createRootStore().getState()
    expect(restored.assistantOpen).toBe(true)
    expect(restored.assistantWidth).toBe(640)
  })
})
