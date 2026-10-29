import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { AppShell } from '@/components/shell/app-shell'
import type * as apiModule from '@/lib/api'
import { ApiError, api } from '@/lib/api'
import type { AssistantActionRecord, AssistantConfirmation, AssistantStatus } from '@/lib/assistant-types'
import type { SessionItem } from '@/lib/session-types'
import type { Preference } from '@/lib/types'
import { useStore } from '@/store'
import { item, resetItems, ScriptedSession, say, tool } from '@/test/acp-stream'
import { alice, mission, run, runRecords, serverInfo, workspace } from '@/test/fixtures'
import { StubSocket } from '@/test/stub-socket'

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

const launch: AssistantConfirmation = {
  id: 'c1',
  method: 'run.launch',
  summary: 'Launch claude (acp) in main-repo: "Fix the login test"',
  params: { workspace_id: workspace.id, harness: 'claude', task: 'Fix the login test' },
  created_at: '2026-10-14T10:00:00Z',
  expires_at: '2099-10-14T10:10:00Z',
}

const fixLogin = run({ id: 'run_fix', title: 'fix-login', status: 'running' })

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
    runs: runRecords(fixLogin),
    hydrated: true,
    route: { name: 'board', params: {} },
    capabilities: { gateway: 'remote', methods: ['*'], ws: ['events', 'attach', 'terminal'] },
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

function assistantSocket(): ScriptedSession {
  const socket = StubSocket.opened.filter((s) => s.url.includes('/ws/assistant')).at(-1)
  if (!socket) throw new Error('no /ws/assistant socket opened')
  return new ScriptedSession(socket)
}

async function openPanel(items: SessionItem[]) {
  render(<AppShell />)
  await userEvent.click(screen.getByRole('button', { name: 'Assistant' }))
  const aside = await screen.findByRole('complementary', { name: 'Assistant' })
  assistantSocket().open({}, items)
  return aside
}

const aether = (turn: number, id: string, name: string, status: string, extra: Partial<NonNullable<SessionItem['tool_call']>> = {}) =>
  tool(turn, id, 'other', name, status, { tool_name: `mcp__aether__${name}`, ...extra })

const card = (aside: HTMLElement, name: RegExp) => within(aside).findByRole('group', { name }) as Promise<HTMLElement>

const launchInput = { workspace_id: workspace.id, harness: 'claude', task: 'Fix the login test' }

describe('action cards', () => {
  it('follows a launch from approval to a run working in the background, and opens it', async () => {
    const aside = await openPanel([item('turn_start', 1), say(1, 'user', 'Fix the login test'), aether(1, 't1', 'aether_launch_run', 'in_progress', { raw_input: launchInput })])
    assistantSocket().send({ status: status({ state: 'running', confirmations: [launch] }) })
    const pending = await card(aside, /^Launching run on main-repo with Claude Code\. Waiting for your approval$/)
    expect(pending.dataset.state).toBe('approval')
    expect(pending.textContent).toBe('Launching run on main-repo with Claude Code · Waiting for your approval below')
    expect(within(pending).queryAllByRole('button')).toHaveLength(0)
    expect(aside.querySelector('[data-slot="live-row"]')).toBeNull()

    act(() => useStore.getState().upsertRun(run({ id: 'run_9', title: 'login-test', status: 'running' })))
    assistantSocket().send({ status: status({ state: 'running' }) }).items(
      item('notice', 1, { notice: { severity: 'info', title: `Approved: ${launch.summary}`, description: 'run.launch passed after you approved its card.' } }),
      aether(1, 't1', 'aether_launch_run', 'completed', { raw_input: launchInput, raw_output: { run: { id: 'run_9' } } }),
    )
    const done = await card(aside, /^Launched run on main-repo with Claude Code\. Working in the background · approved by you$/)
    expect(within(aside).queryByText(`Approved: ${launch.summary}`)).toBeNull()

    expect(done.tabIndex).toBe(-1)
    within(aside).getByRole('textbox').focus()
    const primary = within(done).getByRole('button', { name: 'Open run' })
    while (document.activeElement !== primary) await userEvent.tab()
    await userEvent.keyboard('{Enter}')
    expect(useStore.getState().route).toEqual({ name: 'run', params: { runId: 'run_9' } })

    act(() => useStore.getState().upsertRun(run({ id: 'run_9', title: 'login-test', status: 'completed' })))
    expect(await card(aside, /^Launched run on main-repo with Claude Code\. Done · approved by you$/)).toBeDefined()
  })

  it('says a declined action was declined and retries only into the composer', async () => {
    const aside = await openPanel([
      item('turn_start', 1),
      say(1, 'user', 'Stop fix-login'),
      aether(1, 't1', 'aether_stop_run', 'failed', { raw_input: { run_id: 'run_fix' }, content: [{ type: 'text', text: 'run.kill was not approved: the member declined this action' }] }),
      item('turn_end', 1, { stop_reason: 'end_turn' }),
    ])
    const declined = await card(aside, /^Stop run fix-login\. You declined$/)
    expect(within(declined).queryByRole('alert')).toBeNull()
    expect(within(declined).queryByText(/not approved/)).toBeNull()
    await userEvent.click(within(declined).getByRole('button', { name: 'Details' }))
    expect(within(declined).getByText('run.kill was not approved: the member declined this action')).toBeDefined()

    await userEvent.click(within(declined).getByRole('button', { name: 'Retry' }))
    expect(useStore.getState().assistantDraft).toBe('Retry: Stop run fix-login')
    expect(document.activeElement).toBe(within(aside).getByRole('textbox'))
    expect(api.assistantPrompt).not.toHaveBeenCalled()

    await userEvent.click(within(declined).getByRole('button', { name: 'Open run' }))
    expect(useStore.getState().route).toEqual({ name: 'run', params: { runId: 'run_fix' } })
  })

  it('tells an expired card from a failure, shows the failure verbatim and says when the run is gone', async () => {
    const aside = await openPanel([
      item('turn_start', 1),
      say(1, 'user', 'Message both'),
      aether(1, 't1', 'aether_message_run', 'failed', { raw_input: { run_id: 'run_fix', message: 'rebase' }, raw_output: 'run.inject was not approved: the member did not answer within 10m0s' }),
      aether(1, 't2', 'aether_message_run', 'failed', { raw_input: { run_id: 'run_gone', message: 'rebase' }, raw_output: 'run.inject: run run_gone not found (-32004)' }),
      item('turn_end', 1, { stop_reason: 'end_turn' }),
    ])
    const expired = await card(aside, /^Message run fix-login\. No answer in 10 minutes$/)
    expect(within(expired).queryByRole('alert')).toBeNull()
    expect(within(expired).getByRole('button', { name: 'Retry' })).toBeDefined()
    const failed = await card(aside, /^Message run run_gone\. Failed$/)
    expect(within(failed).getByRole('alert').textContent).toBe('run.inject: run run_gone not found (-32004)')
    expect(within(failed).getByText('This run no longer exists')).toBeDefined()
    expect(within(failed).queryByRole('button', { name: 'Open run' })).toBeNull()
    expect(within(failed).getByRole('button', { name: 'Retry' })).toBeDefined()
  })

  it('marks an auto-approved action and keeps the note of a call it cannot match', async () => {
    const aside = await openPanel([
      item('turn_start', 1),
      say(1, 'user', 'Save it'),
      aether(1, 't1', 'aether_save_environment', 'in_progress'),
      item('notice', 1, { notice: { severity: 'info', title: 'Auto-approved: Save your environment', description: 'env.save passed without a card: auto-approval is on.' } }),
      item('notice', 1, { notice: { severity: 'info', title: 'Auto-approved: Stop run run_x', description: 'run.kill passed without a card: auto-approval is on.' } }),
      aether(1, 't1', 'aether_save_environment', 'completed', { raw_output: { image: 'img_1' } }),
      item('turn_end', 1, { stop_reason: 'end_turn' }),
    ])
    const saved = await card(aside, /^Saved your environment\. Done · auto-approved$/)
    await userEvent.click(within(saved).getByRole('button', { name: 'Open environment' }))
    expect(useStore.getState().route).toEqual({ name: 'environment', params: {} })
    expect(within(aside).queryByText('Auto-approved: Save your environment')).toBeNull()
    expect(within(aside).getByText('Auto-approved: Stop run run_x')).toBeDefined()
  })

  it('opens a merged pull request in a new tab and a swarm from a dashboard link on another host in the app', async () => {
    const opened = vi.fn()
    vi.stubGlobal('open', opened)
    const aside = await openPanel([
      item('turn_start', 1),
      say(1, 'user', 'Merge and start a swarm'),
      aether(1, 't1', 'aether_pr_merge', 'completed', { raw_input: { run_id: 'run_fix' }, content: [{ type: 'text', text: JSON.stringify({ number: 42, url: 'https://github.com/acme/api/pull/42' }) }] }),
      aether(1, 't2', 'aether_launch_swarm', 'completed', { raw_input: { workspace_id: workspace.id }, raw_output: { url: 'https://aether.example/?page=missions&id=mis_7', ids: { mission_id: 'mis_7' } } }),
      item('turn_end', 1, { stop_reason: 'end_turn' }),
    ])
    const merged = await card(aside, /^Merged PR #42\. Done$/)
    await userEvent.click(within(merged).getByRole('button', { name: 'Open pull request' }))
    expect(opened).toHaveBeenCalledWith('https://github.com/acme/api/pull/42', '_blank', 'noopener,noreferrer')
    await userEvent.click(within(await card(aside, /^Launched swarm on main-repo\. Working in the background$/)).getByRole('button', { name: 'Open swarm' }))
    expect(useStore.getState().route).toEqual({ name: 'missions', params: { missionId: 'mis_7' } })
  })

  it("shows a launch receipt and a watch's state, conditions and last delivery", async () => {
    const aside = await openPanel([
      item('turn_start', 1),
      say(1, 'user', 'Launch and watch it'),
      aether(1, 't1', 'aether_launch_run', 'completed', { raw_input: launchInput, raw_output: {
        run: { id: 'run_fix' },
        launch: { kind: 'run', id: 'run_fix', workspace_id: workspace.id, harness: 'claude', model: 'opus', mode: 'acp', mode_name: 'Enhanced', account: { member_id: 'mem_bob', name: 'Bob', access: 'shared' }, requested_account: { member_id: alice.id, name: 'Alice', access: 'own' }, fallback: { policy: 'ordered', order: ['mem_bob', alice.id] }, url: '?run=run_fix' },
      } }),
      aether(1, 't2', 'aether_watch_create', 'completed', { raw_input: { run_id: 'run_fix' }, raw_output: {
        watch: { state: 'active', conditions: ['needs_you', 'finished'], last_notification: { deliveries: [{ channel: 'push', status: 'no_destination' }] } },
        link: '/?page=settings&id=watches&watch=wat-1',
      } }),
      item('turn_end', 1, { stop_reason: 'end_turn' }),
    ])
    const launched = await card(aside, /^Launched run on main-repo with Claude Code\./)
    expect(within(launched).getByText("run · main-repo · Claude Code opus · Bob's account, shared · fell back from your account · then mem_bob, your account")).toBeDefined()
    const watching = await card(aside, /^Started watching run fix-login\. Done$/)
    expect(within(watching).getByText('active · on needs you, finished · last notice: push no destination')).toBeDefined()
    await userEvent.click(within(watching).getByRole('button', { name: 'Open watch' }))
    expect(useStore.getState().route).toEqual({ name: 'settings', params: { section: 'watches', watch: 'wat-1' } })
  })

  it('shows an Aether tool it has no words for by name with its inputs', async () => {
    const aside = await openPanel([
      item('turn_start', 1),
      say(1, 'user', 'Frobnicate'),
      tool(1, 't1', 'other', 'aether.aether_frobnicate', 'completed', { tool_name: 'aether_frobnicate', raw_input: { widget_id: 'w1', depth: 2 }, raw_output: { ok: true } }),
      item('turn_end', 1, { stop_reason: 'end_turn' }),
    ])
    const generic = await card(aside, /^Used aether_frobnicate\. Done$/)
    expect(within(generic).getByText('Widget').nextElementSibling?.textContent).toBe('w1')
    expect(within(generic).getByText('Depth').nextElementSibling?.textContent).toBe('2')
  })

  it('collapses consecutive reads into one line that expands to each read', async () => {
    const aside = await openPanel([
      item('turn_start', 1),
      say(1, 'user', 'How are my runs?'),
      aether(1, 'r1', 'aether_get_run', 'completed', { raw_input: { run_id: 'a' }, raw_output: { id: 'a' } }),
      aether(1, 'r2', 'aether_get_run', 'completed', { raw_input: { run_id: 'b' }, raw_output: { id: 'b' } }),
      aether(1, 'r3', 'aether_list_workspaces', 'completed', { raw_output: { workspaces: [] } }),
      aether(1, 'r4', 'aether_get_run', 'failed', { raw_input: { run_id: 'c' }, raw_output: 'run.get: run c not found' }),
      say(1, 'assistant', 'Two runs are fine.'),
      item('turn_end', 1, { stop_reason: 'end_turn' }),
    ])
    const line = await within(aside).findByRole('button', { name: /^Read 3 runs and 1 workspace list\s*· 1 failed$/ })
    expect(within(aside).queryAllByRole('group', { name: /Done$/ })).toHaveLength(0)
    expect(line.getAttribute('aria-expanded')).toBe('false')
    await userEvent.click(line)
    expect(line.getAttribute('aria-expanded')).toBe('true')
    expect(within(aside).getByText('Run c')).toBeDefined()
    expect(within(aside).getByText('run.get: run c not found')).toBeDefined()
  })
})

describe('action history', () => {
  it('lists the actions newest first, moves with the arrows and opens a target', async () => {
    const aside = await openPanel([
      item('turn_start', 1),
      say(1, 'user', 'Launch, then message'),
      aether(1, 'r1', 'aether_list_runs', 'completed', { raw_output: { runs: [] } }),
      aether(1, 't1', 'aether_launch_run', 'completed', { raw_input: launchInput, raw_output: { run: { id: 'run_fix' } } }),
      aether(1, 't2', 'aether_message_run', 'failed', { raw_input: { run_id: 'run_fix', message: 'go' }, raw_output: 'run.inject was not approved: the member declined this action' }),
      item('turn_end', 1, { stop_reason: 'end_turn' }),
    ])
    await card(aside, /^Message run fix-login\. You declined$/)
    await userEvent.click(within(aside).getByRole('button', { name: 'Actions' }))
    const list = await screen.findByRole('list', { name: 'Actions' })
    const entries = within(list).getAllByRole('button')
    expect(entries.map((entry) => entry.textContent)).toEqual([
      expect.stringMatching(/^Message run fix-loginYou declined · opens the run/),
      expect.stringMatching(/^Launched run on main-repo with Claude CodeWorking in the background · opens the run/),
    ])
    expect(document.activeElement).toBe(entries[0])
    await userEvent.keyboard('{ArrowDown}')
    expect(document.activeElement).toBe(entries[1])
    await userEvent.keyboard('{Enter}')
    expect(useStore.getState().route).toEqual({ name: 'run', params: { runId: 'run_fix' } })
    await waitFor(() => expect(screen.queryByRole('list', { name: 'Actions' })).toBeNull())
  })

  it('says when there is nothing to list', async () => {
    const aside = await openPanel([item('turn_start', 1), say(1, 'user', 'hi'), say(1, 'assistant', 'Hello'), item('turn_end', 1, { stop_reason: 'end_turn' })])
    await userEvent.click(within(aside).getByRole('button', { name: 'Actions' }))
    expect(await screen.findByText('No actions yet. Reads are not listed.')).toBeDefined()
  })
})

describe('review fixes', () => {
  it('adds the retry under text already typed instead of replacing it', async () => {
    const aside = await openPanel([
      item('turn_start', 1),
      say(1, 'user', 'Stop fix-login'),
      aether(1, 't1', 'aether_stop_run', 'failed', { raw_input: { run_id: 'run_fix' }, raw_output: 'run.kill was not approved: the member declined this action' }),
      item('turn_end', 1, { stop_reason: 'end_turn' }),
    ])
    act(() => useStore.getState().setAssistantDraft('also check CI'))
    await userEvent.click(within(await card(aside, /^Stop run fix-login\. You declined$/)).getByRole('button', { name: 'Retry' }))
    expect(useStore.getState().assistantDraft).toBe('also check CI\n\nRetry: Stop run fix-login')
    expect(api.assistantPrompt).not.toHaveBeenCalled()
  })

  it('keeps Open run on a launch whose run the store has not heard of yet', async () => {
    const aside = await openPanel([
      item('turn_start', 1),
      say(1, 'user', 'Launch'),
      aether(1, 't1', 'aether_launch_run', 'completed', { raw_input: launchInput, raw_output: { run: { id: 'run_new' } } }),
      item('turn_end', 1, { stop_reason: 'end_turn' }),
    ])
    const launched = await card(aside, /^Launched run on main-repo with Claude Code\. Done$/)
    expect(within(launched).queryByText('This run no longer exists')).toBeNull()
    await userEvent.click(within(launched).getByRole('button', { name: 'Open run' }))
    expect(useStore.getState().route).toEqual({ name: 'run', params: { runId: 'run_new' } })
  })

  it('reads the whole item behind a cut-down call when its details open', async () => {
    const head = [item('turn_start', 1), say(1, 'user', 'Message')]
    const cut = { ...aether(1, 't1', 'aether_message_run', 'completed', { raw_input: { run_id: 'run_fix' } }), truncated: true }
    vi.mocked(api.assistantItem).mockResolvedValue({ ...cut, truncated: false, tool_call: { ...cut.tool_call!, raw_input: { run_id: 'run_fix', message: 'the whole message' }, raw_output: { delivered: true } } })
    const aside = await openPanel([...head, cut, item('turn_end', 1, { stop_reason: 'end_turn' })])
    await userEvent.click(within(await card(aside, /^Messaged run fix-login\. Done$/)).getByRole('button', { name: 'Details' }))
    expect(api.assistantItem).toHaveBeenCalledWith(cut.seq)
    expect(await within(aside).findByText(/the whole message/)).toBeDefined()
  })

  it('stops saying it is loading once the whole item is read, even if the call still reads as cut', async () => {
    const head = [item('turn_start', 1), say(1, 'user', 'Message')]
    const cut = { ...aether(1, 't1', 'aether_message_run', 'completed', { raw_input: { run_id: 'run_fix' } }), truncated: true }
    vi.mocked(api.assistantItem).mockResolvedValue(cut)
    const aside = await openPanel([...head, cut, item('turn_end', 1, { stop_reason: 'end_turn' })])
    const messaged = await card(aside, /^Messaged run fix-login\. Done$/)
    await userEvent.click(within(messaged).getByRole('button', { name: 'Details' }))
    await waitFor(() => expect(api.assistantItem).toHaveBeenCalledWith(cut.seq))
    await waitFor(() => expect(within(messaged).queryByText('Loading the whole call…')).toBeNull())
  })

  it('opens a dashboard link in an assistant message in the app, and any other link in a new tab', async () => {
    const opened = vi.spyOn(window, 'open').mockReturnValue(null)
    const aside = await openPanel([
      item('turn_start', 1),
      say(1, 'user', 'Briefing'),
      say(1, 'assistant', 'Shipped: [fix-login](?run=run_fix). Docs: [guide](https://example.com/guide)'),
      item('turn_end', 1, { stop_reason: 'end_turn' }),
    ])
    const external = await within(aside).findByRole('link', { name: 'guide' })
    expect(external.getAttribute('target')).toBe('_blank')
    await userEvent.click(within(aside).getByRole('link', { name: 'fix-login' }))
    expect(useStore.getState().route).toEqual({ name: 'run', params: { runId: 'run_fix' } })
    expect(opened).not.toHaveBeenCalled()
    opened.mockRestore()
  })

  it('opens the watch a notice links to in the app', async () => {
    const aside = await openPanel([
      item('turn_start', 1),
      say(1, 'user', 'Watch it'),
      item('notice', 1, { notice: { severity: 'info', title: 'Watching run fix-login', description: 'Aether tells you when it finishes. [Stop or change this watch](?page=settings&id=watches&watch=wat-1)' } }),
      item('turn_end', 1, { stop_reason: 'end_turn' }),
    ])
    await userEvent.click(await within(aside).findByRole('link', { name: 'Stop or change this watch' }))
    expect(useStore.getState().route).toEqual({ name: 'settings', params: { section: 'watches', watch: 'wat-1' } })
  })

  it('loads earlier actions into the history when the conversation has more', async () => {
    const older = [item('turn_start', 1), say(1, 'user', 'Launch'), aether(1, 'o1', 'aether_launch_run', 'completed', { raw_input: launchInput, raw_output: { run: { id: 'run_fix' } } }), item('turn_end', 1, { stop_reason: 'end_turn' })]
    const recent = [item('turn_start', 2), say(2, 'user', 'Read'), aether(2, 'r1', 'aether_get_run', 'completed', { raw_input: { run_id: 'run_fix' } }), item('turn_end', 2, { stop_reason: 'end_turn' })]
    vi.mocked(api.assistantHistory).mockResolvedValue({ frames: older.map((it) => ({ seq: it.seq, item: it })), oldest_seq: 1 })
    render(<AppShell />)
    await userEvent.click(screen.getByRole('button', { name: 'Assistant' }))
    const aside = await screen.findByRole('complementary', { name: 'Assistant' })
    assistantSocket().open({ oldest_seq: recent[0]!.seq }, recent)
    await userEvent.click(within(aside).getByRole('button', { name: 'Actions' }))
    expect(await screen.findByText('No actions in the loaded messages. Reads are not listed.')).toBeDefined()
    await userEvent.click(screen.getByRole('button', { name: 'Show earlier actions' }))
    const list = await screen.findByRole('list', { name: 'Actions' })
    expect(within(list).getByRole('button').textContent).toMatch(/^Launched run on main-repo with Claude Code/)
  })
})

describe('agents and turn ends', () => {
  it("reads a Codex call, which has no tool name and wraps its arguments and result", async () => {
    const aside = await openPanel([
      item('turn_start', 1),
      say(1, 'user', 'Stop fix-login'),
      tool(1, 'c1', 'execute', 'mcp.aether.aether_stop_run', 'completed', {
        raw_input: { server: 'aether', tool: 'aether_stop_run', arguments: { run_id: 'run_fix' } },
        raw_output: { result: { content: [{ type: 'text', text: '{"url":"https://aether.example/?run=run_fix"}' }] }, error: null },
      }),
      tool(1, 'c2', 'execute', 'mcp.aether.aether_message_run', 'failed', {
        raw_input: { server: 'aether', tool: 'aether_message_run', arguments: { run_id: 'run_fix', message: 'go' } },
        raw_output: { result: null, error: { message: 'run.inject was not approved: the member declined this action' } },
      }),
      item('turn_end', 1, { stop_reason: 'end_turn' }),
    ])
    const stopped = await card(aside, /^Stopped run fix-login\. Done$/)
    await userEvent.click(within(stopped).getByRole('button', { name: 'Open run' }))
    expect(useStore.getState().route).toEqual({ name: 'run', params: { runId: 'run_fix' } })
    expect(await card(aside, /^Message run fix-login\. You declined$/)).toBeDefined()
  })

  it('says a call the turn ended under did not finish, rather than done', async () => {
    const aside = await openPanel([
      item('turn_start', 1),
      say(1, 'user', 'Stop fix-login'),
      aether(1, 't1', 'aether_stop_run', 'in_progress', { raw_input: { run_id: 'run_fix' } }),
      item('turn_end', 1, { stop_reason: 'cancelled' }),
    ])
    const open = await card(aside, /^Stop run fix-login\. Did not finish: the turn ended first$/)
    expect(within(open).getByRole('button', { name: 'Retry' })).toBeDefined()
  })
})

describe('status words', () => {
  it("names the assistant's own turn Generating, apart from runs working in the background", async () => {
    const aside = await openPanel([item('turn_start', 1), say(1, 'user', 'hi')])
    assistantSocket().send({ status: status({ state: 'running' }) })
    expect(await within(aside).findByText('Claude Code · Generating…')).toBeDefined()
  })
})

describe('quiet outcomes and names', () => {
  it('says a withdrawn card in its own words, without an error or a retry', async () => {
    const withdrawn: AssistantActionRecord = { id: 'a1', method: 'run.kill', summary: 'Stop run fix-login', target: { run_id: 'run_fix' }, outcome: 'withdrawn', confirmation_id: 'c1', created_at: '2026-10-26T10:00:00Z' }
    const aside = await openPanel([
      item('turn_start', 1),
      say(1, 'user', 'Stop fix-login'),
      aether(1, 't1', 'aether_stop_run', 'in_progress', { raw_input: { run_id: 'run_fix' } }),
      item('notice', 1, { notice: { severity: 'info', title: 'Withdrawn: Stop run fix-login', action: withdrawn } }),
      aether(1, 't1', 'aether_stop_run', 'failed', { raw_input: { run_id: 'run_fix' }, raw_output: 'run.kill was not approved: the turn or session ended before the member answered' }),
      item('turn_end', 1, { stop_reason: 'cancelled' }),
    ])
    const stopped = await card(aside, /^Stop run fix-login\. Withdrawn: the turn ended before you answered$/)
    expect(within(stopped).queryByRole('alert')).toBeNull()
    expect(within(stopped).queryByRole('button', { name: 'Retry' })).toBeNull()
    expect(within(aside).queryByText('Withdrawn: Stop run fix-login')).toBeNull()
  })

  it('names a run by its task when it has no title', async () => {
    useStore.getState().upsertRun(run({ id: 'run_untitled', title: '', task: 'Fix the flaky login test\nThen push', status: 'running' }))
    const aside = await openPanel([
      item('turn_start', 1),
      say(1, 'user', 'Stop it'),
      aether(1, 't1', 'aether_stop_run', 'completed', { raw_input: { run_id: 'run_untitled' } }),
      item('turn_end', 1, { stop_reason: 'end_turn' }),
    ])
    expect(await card(aside, /^Stopped run Fix the flaky login test\. Done$/)).toBeDefined()
  })

  it('reads a launched swarm it has not loaded, working until the swarm says it finished', async () => {
    let finish!: (value: Awaited<ReturnType<typeof api.missionShow>>) => void
    vi.mocked(api.missionShow).mockReturnValue(new Promise((resolve) => { finish = resolve }))
    const aside = await openPanel([
      item('turn_start', 1),
      say(1, 'user', 'Start a swarm'),
      aether(1, 't1', 'aether_launch_swarm', 'completed', { raw_input: { workspace_id: workspace.id }, raw_output: { ids: { mission_id: 'mis_7' } } }),
      item('turn_end', 1, { stop_reason: 'end_turn' }),
    ])
    expect(await card(aside, /^Launched swarm on main-repo\. Working in the background$/)).toBeDefined()
    expect(api.missionShow).toHaveBeenCalledWith('mis_7')
    act(() => finish({ mission: mission({ id: 'mis_7', phase: 'completed' }), tasks: [], attempts: [], submissions: [], diagnostics: [], questions: [] }))
    expect(await card(aside, /^Launched swarm on main-repo\. Done$/)).toBeDefined()
    act(() => useStore.setState({ missions: {} }))
    expect(await card(aside, /^Launched swarm on main-repo\. Done$/)).toBeDefined()
  })

  it('shows why a launched swarm could not be read instead of calling it working', async () => {
    vi.mocked(api.missionShow).mockRejectedValue(new Error('mission.get: mission mis_7 not found (-32004)'))
    const aside = await openPanel([
      item('turn_start', 1),
      say(1, 'user', 'Start a swarm'),
      aether(1, 't1', 'aether_launch_swarm', 'completed', { raw_input: { workspace_id: workspace.id }, raw_output: { ids: { mission_id: 'mis_7' } } }),
      item('turn_end', 1, { stop_reason: 'end_turn' }),
    ])
    const launched = await card(aside, /^Launched swarm on main-repo\. Done$/)
    expect(within(launched).getByText('Could not read swarm mis_7: mission.get: mission mis_7 not found (-32004)')).toBeDefined()
  })

  it('reads a launched swarm again on each of its changes, so the card and the history agree', async () => {
    vi.mocked(api.missionShow).mockResolvedValue({ mission: mission({ id: 'mis_7', phase: 'active' }), tasks: [], attempts: [], submissions: [], diagnostics: [], questions: [] })
    const aside = await openPanel([
      item('turn_start', 1),
      say(1, 'user', 'Start a swarm'),
      aether(1, 't1', 'aether_launch_swarm', 'completed', { raw_input: { workspace_id: workspace.id }, raw_output: { ids: { mission_id: 'mis_7' } } }),
      item('turn_end', 1, { stop_reason: 'end_turn' }),
    ])
    expect(await card(aside, /^Launched swarm on main-repo\. Working in the background$/)).toBeDefined()
    await waitFor(() => expect(useStore.getState().missions.mis_7?.phase).toBe('active'))

    vi.mocked(api.missionShow).mockResolvedValue({ mission: mission({ id: 'mis_7', phase: 'completed' }), tasks: [], attempts: [], submissions: [], diagnostics: [], questions: [] })
    act(() => useStore.getState().bumpWorkflowSignal('missionSeq', 'mis_7'))
    expect(await card(aside, /^Launched swarm on main-repo\. Done$/)).toBeDefined()
    await userEvent.click(within(aside).getByRole('button', { name: 'Actions' }))
    const history = await screen.findByRole('dialog', { name: 'Actions in this conversation' })
    expect(within(history).getByText(/^Done · opens the swarm$/)).toBeDefined()
  })

  it('says what a launch started without a receipt, and how the member hears back', async () => {
    useStore.setState({ members: { [alice.id]: alice, mem_bob: { id: 'mem_bob', display_name: 'Bob', color: '#000', role: 'collaborator' } } })
    act(() => useStore.getState().upsertRun(run({ id: 'run_9', title: 'login-test', status: 'running' })))
    const aside = await openPanel([
      item('turn_start', 1),
      say(1, 'user', 'Fix the login test on Bob'),
      aether(1, 't1', 'aether_launch_run', 'completed', {
        raw_input: { ...launchInput, model: 'opus', account_member_id: 'mem_bob', no_fallback: true },
        content: [{ type: 'text', text: `Launched run run_9.\n${JSON.stringify({ run_id: 'run_9', workspace_id: workspace.id, harness: 'claude', url: '?run=run_9' })}` }],
      }),
      item('turn_end', 1, { stop_reason: 'end_turn' }),
    ])
    const launched = await card(aside, /^Launched run on main-repo with Claude Code\. Working in the background$/)
    expect(within(launched).getByText("run · main-repo · Claude Code opus · Bob's account, shared · no fallback")).toBeDefined()
    expect(within(launched).getByText(/^Aether notifies you here/)).toBeDefined()
    await userEvent.click(within(launched).getByRole('button', { name: 'Open run' }))
    expect(useStore.getState().route).toEqual({ name: 'run', params: { runId: 'run_9' } })
  })
})

describe('preference cards', () => {
  const saved: Preference = { id: 'pref-1', text: 'Prefer table output', enabled: true, source: 'assistant', created_at: '2026-10-27T10:00:00Z', updated_at: '2026-10-27T10:00:00Z' }

  it('shows what was saved, undoes it by deleting the row and edits it in Settings', async () => {
    vi.mocked(api.preferenceList).mockResolvedValue({ preferences: [saved], enabled: true })
    vi.mocked(api.preferenceDelete).mockResolvedValue({ deleted: { ...saved, source: 'assistant' } } as never)
    const aside = await openPanel([
      item('turn_start', 1),
      say(1, 'user', 'Remember that I prefer table output'),
      aether(1, 't1', 'aether_remember', 'completed', { raw_input: { text: 'Prefer table output' }, raw_output: { preference: saved } }),
      item('turn_end', 1, { stop_reason: 'end_turn' }),
    ])
    const notice = await card(aside, /^Saved: Prefer table output\. Done$/)
    await userEvent.click(within(notice).getByRole('button', { name: 'Undo' }))
    expect(api.preferenceDelete).toHaveBeenCalledWith('pref-1', { expected_member_id: alice.id, expected_updated_at: saved.updated_at })
    expect(await within(notice).findByText('Undone')).toBeDefined()
    expect(useStore.getState().preferenceUndos).toBe(1)

    await userEvent.click(within(notice).getByRole('button', { name: 'Edit' }))
    expect(useStore.getState().route).toEqual({ name: 'settings', params: { section: 'memory', entry: 'pref-1' } })
  })

  it('shows an update with the text it replaced and undoes it back to that text', async () => {
    vi.mocked(api.preferenceUpdate).mockResolvedValue({ preference: saved } as never)
    const aside = await openPanel([
      item('turn_start', 1),
      say(1, 'user', 'Actually I prefer JSON'),
      aether(1, 't1', 'aether_remember', 'completed', {
        raw_input: { text: 'Prefer JSON output', replaces: 'pref-1' },
        raw_output: { preference: { ...saved, text: 'Prefer JSON output', updated_at: '2026-10-27T10:05:00.250Z' }, previous: saved },
      }),
      item('turn_end', 1, { stop_reason: 'end_turn' }),
    ])
    const notice = await card(aside, /^Updated: Prefer JSON output\. Done$/)
    expect(within(notice).getByText('was: Prefer table output')).toBeDefined()
    await userEvent.click(within(notice).getByRole('button', { name: 'Undo' }))
    expect(api.preferenceUpdate).toHaveBeenCalledWith({ id: 'pref-1', text: 'Prefer table output', clear_workspace: true, expected_member_id: alice.id, expected_updated_at: '2026-10-27T10:05:00.250Z' })
  })

  it('shows the server refusing to undo a save or an update the member changed since', async () => {
    const changed = (id: string) => `preference ${id} changed since you saw it, so nothing was changed; show its current text and ask again`
    vi.mocked(api.preferenceDelete).mockRejectedValue(new ApiError(409, `preference.delete: ${changed('pref-1')}`))
    vi.mocked(api.preferenceUpdate).mockRejectedValue(new ApiError(409, `preference.update: ${changed('pref-2')}`))
    const aside = await openPanel([
      item('turn_start', 1),
      say(1, 'user', 'Remember that I prefer table output'),
      aether(1, 't1', 'aether_remember', 'completed', { raw_input: { text: 'Prefer table output' }, raw_output: { preference: saved } }),
      aether(1, 't2', 'aether_remember', 'completed', {
        raw_input: { text: 'Prefer JSON output', replaces: 'pref-2' },
        raw_output: { preference: { ...saved, id: 'pref-2', text: 'Prefer JSON output', updated_at: '2026-10-27T10:05:00Z' }, previous: { ...saved, id: 'pref-2' } },
      }),
      item('turn_end', 1, { stop_reason: 'end_turn' }),
    ])
    const save = await card(aside, /^Saved: Prefer table output\. Done$/)
    await userEvent.click(within(save).getByRole('button', { name: 'Undo' }))
    expect((await within(save).findByRole('alert')).textContent).toBe(changed('pref-1'))
    const update = await card(aside, /^Updated: Prefer JSON output\. Done$/)
    await userEvent.click(within(update).getByRole('button', { name: 'Undo' }))
    expect((await within(update).findByRole('alert')).textContent).toBe(changed('pref-2'))
    expect(api.preferenceDelete).toHaveBeenCalledExactlyOnceWith('pref-1', { expected_member_id: alice.id, expected_updated_at: saved.updated_at })
    expect(api.preferenceUpdate).toHaveBeenCalledExactlyOnceWith({ id: 'pref-2', text: 'Prefer table output', clear_workspace: true, expected_member_id: alice.id, expected_updated_at: '2026-10-27T10:05:00Z' })
    expect(api.preferenceList).not.toHaveBeenCalled()
    expect(api.preferenceAdd).not.toHaveBeenCalled()
  })

  it('shows a forgotten preference, undoes it by adding the text again and shows a refusal verbatim', async () => {
    vi.mocked(api.preferenceAdd).mockRejectedValueOnce(new Error('preference.add: memory is off: you turned it off in Settings'))
    const aside = await openPanel([
      item('turn_start', 1),
      say(1, 'user', 'Forget the table thing'),
      aether(1, 't1', 'aether_forget', 'completed', { raw_input: { id: 'pref-1' }, raw_output: { deleted: { ...saved, workspace_id: workspace.id } } }),
      item('turn_end', 1, { stop_reason: 'end_turn' }),
    ])
    const notice = await card(aside, /^Forgotten: Prefer table output\. Done$/)
    expect(within(notice).queryByRole('button', { name: 'Edit' })).toBeNull()
    await userEvent.click(within(notice).getByRole('button', { name: 'Undo' }))
    expect((await within(notice).findByRole('alert')).textContent).toBe('memory is off: you turned it off in Settings')

    vi.mocked(api.preferenceAdd).mockResolvedValueOnce({ preference: saved } as never)
    await userEvent.click(within(notice).getByRole('button', { name: 'Undo' }))
    expect(api.preferenceAdd).toHaveBeenLastCalledWith({ text: 'Prefer table output', workspace_id: workspace.id, expected_member_id: alice.id })
    expect(await within(notice).findByText('Undone')).toBeDefined()
  })

  it('leaves a second restore of the same text to the server and shows its refusal', async () => {
    const remembered: Preference[] = []
    vi.mocked(api.preferenceList).mockImplementation(async () => ({ preferences: [...remembered], enabled: true }))
    vi.mocked(api.preferenceAdd).mockImplementation(async ({ text }) => {
      const kept = remembered.find((p) => p.text === text)
      if (kept) throw new ApiError(409, `preference.add: already remembered as preference ${kept.id}`)
      remembered.push({ ...saved, id: 'pref-9', text })
      return { preference: remembered.at(-1)! }
    })
    const aside = await openPanel([
      item('turn_start', 1),
      say(1, 'user', 'Forget the table thing'),
      aether(1, 't1', 'aether_forget', 'completed', { raw_input: { id: 'pref-1' }, raw_output: { deleted: saved } }),
      item('turn_end', 1, { stop_reason: 'end_turn' }),
      item('turn_start', 2),
      say(2, 'user', 'Forget it again'),
      aether(2, 't2', 'aether_forget', 'completed', { raw_input: { id: 'pref-3' }, raw_output: { deleted: { ...saved, id: 'pref-3' } } }),
      item('turn_end', 2, { stop_reason: 'end_turn' }),
    ])
    const [first, second] = await within(aside).findAllByRole('group', { name: /^Forgotten: Prefer table output\. Done$/ })
    await userEvent.click(within(first!).getByRole('button', { name: 'Undo' }))
    expect(await within(first!).findByText('Undone')).toBeDefined()
    await userEvent.click(within(second!).getByRole('button', { name: 'Undo' }))
    expect((await within(second!).findByRole('alert')).textContent).toBe('already remembered as preference pref-9')
    expect(api.preferenceAdd).toHaveBeenCalledTimes(2)
    expect(remembered).toHaveLength(1)
  })

  it("folds the server's record of a save into the card that made it", async () => {
    const record: AssistantActionRecord = { id: 'a1', method: 'assistant.remember', summary: 'Saved your preference: "Prefer table output"', target: { preference_id: 'pref-1' }, outcome: 'succeeded', created_at: '2026-10-27T10:00:00Z' }
    const aside = await openPanel([
      item('turn_start', 1),
      say(1, 'user', 'Remember that I prefer table output'),
      aether(1, 't1', 'aether_remember', 'in_progress', { raw_input: { text: 'Prefer table output' } }),
      item('notice', 1, { notice: { severity: 'info', title: record.summary, action: record } }),
      aether(1, 't1', 'aether_remember', 'completed', { raw_input: { text: 'Prefer table output' }, raw_output: { preference: saved } }),
      item('turn_end', 1, { stop_reason: 'end_turn' }),
    ])
    expect(await card(aside, /^Saved: Prefer table output\. Done$/)).toBeDefined()
    expect(within(aside).queryByText(record.summary)).toBeNull()
  })

  it('gives each forget card the record of the preference it forgot', async () => {
    const forgot = (id: string, preference: string, outcome: AssistantActionRecord['outcome']): AssistantActionRecord =>
      ({ id, method: 'assistant.forget', summary: `Forget your preference: "${preference}"`, target: { preference_id: preference }, outcome, approval: 'card', created_at: '2026-10-27T10:00:00Z', ...(outcome === 'failed' ? { error: `preference ${preference} not found` } : {}) })
    const aside = await openPanel([
      item('turn_start', 1),
      say(1, 'user', 'Forget both'),
      aether(1, 't1', 'aether_forget', 'in_progress', { raw_input: { id: 'pref-1' } }),
      aether(1, 't2', 'aether_forget', 'in_progress', { raw_input: { id: 'pref-2' } }),
      item('notice', 1, { notice: { severity: 'info', title: 'Failed: forget pref-2', action: forgot('a2', 'pref-2', 'failed') } }),
      item('notice', 1, { notice: { severity: 'info', title: 'Forgot pref-1', action: forgot('a1', 'pref-1', 'succeeded') } }),
      aether(1, 't1', 'aether_forget', 'completed', { raw_input: { id: 'pref-1' }, raw_output: { deleted: saved } }),
      aether(1, 't2', 'aether_forget', 'failed', { raw_input: { id: 'pref-2' }, raw_output: 'preference pref-2 not found' }),
      item('turn_end', 1, { stop_reason: 'end_turn' }),
    ])
    const done = await card(aside, /^Forgotten: Prefer table output\. Done/)
    expect(done.dataset.state).toBe('done')
  })

  it('says how many preferences the assistant used and lists them on request', async () => {
    const aside = await openPanel([
      item('turn_start', 1),
      say(1, 'user', 'Show the failing tests'),
      aether(1, 't1', 'aether_my_preferences', 'completed', {
        raw_output: { preferences: [
          { id: 'pref-1', text: 'Prefer table output', scope: 'all', source: 'assistant', updated_at: '2026-10-27T10:00:00Z' },
          { id: 'pref-2', text: 'Use pnpm here', scope: 'workspace', source: 'settings', updated_at: '2026-10-27T10:00:00Z' },
        ] },
      }),
      item('turn_end', 1, { stop_reason: 'end_turn' }),
    ])
    const notice = await card(aside, /^Read 2 of your preferences\. Done$/)
    expect(within(notice).queryByText('Use pnpm here')).toBeNull()
    await userEvent.click(within(notice).getByRole('button', { name: 'Show preferences' }))
    const list = within(notice).getByRole('list', { name: 'Preferences used' })
    expect(within(list).getAllByRole('listitem').map((li) => li.textContent)).toEqual([
      'Prefer table output · All my workspaces',
      'Use pnpm here · This workspace',
    ])
  })

  it('keeps Undone after the card is shown again, and only on the change it undid', async () => {
    vi.mocked(api.preferenceList).mockResolvedValue({ preferences: [saved], enabled: true })
    vi.mocked(api.preferenceDelete).mockResolvedValue({ deleted: saved } as never)
    const conversation = (preference: typeof saved) => [
      item('turn_start', 1),
      say(1, 'user', 'Remember that I prefer table output'),
      aether(1, 't1', 'aether_remember', 'completed', { raw_input: { text: 'Prefer table output' }, raw_output: { preference } }),
      item('turn_end', 1, { stop_reason: 'end_turn' }),
    ]
    const reopen = async (preference: typeof saved) => {
      cleanup()
      act(() => useStore.setState({ assistantOpen: false, acpSessions: {} }))
      return openPanel(conversation(preference))
    }
    const aside = await openPanel(conversation(saved))
    await userEvent.click(within(await card(aside, /^Saved: Prefer table output\. Done$/)).getByRole('button', { name: 'Undo' }))
    await within(aside).findByText('Undone')

    const again = await reopen(saved)
    const notice = await card(again, /^Saved: Prefer table output\. Done$/)
    expect(within(notice).queryByRole('button', { name: 'Undo' })).toBeNull()
    expect(within(notice).getByText('Undone')).toBeDefined()

    const later = await reopen({ ...saved, id: 'pref-7' })
    expect(within(await card(later, /^Saved: Prefer table output\. Done$/)).getByRole('button', { name: 'Undo' })).toBeDefined()
  })
})
